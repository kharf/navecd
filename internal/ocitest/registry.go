// Copyright 2024 kharf
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ocitest

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"cuelang.org/go/mod/modconfig"
	"cuelang.org/go/mod/modregistry"
	"cuelang.org/go/mod/module"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/kharf/navecd/internal/cloudtest"
	"github.com/kharf/navecd/pkg/cloud"
)

type Registry struct {
	httpsServer       *httptest.Server
	client            *http.Client
	cueRegistryClient *modregistry.Client
	provider          cloud.ProviderID
	addr              string
	aws               *cloudtest.AWSEnvironment
	azure             *cloudtest.AzureEnvironment
	gcp               *cloudtest.GCPEnvironment
}

func (r *Registry) Client() *http.Client {
	return r.client
}

func (r *Registry) CUERegistryClient() *modregistry.Client {
	return r.cueRegistryClient
}

func (r *Registry) Addr() string {
	return r.addr
}

func (r *Registry) URL() string {
	return r.httpsServer.URL
}

func (r *Registry) Provider() cloud.ProviderID {
	return r.provider
}

func (r *Registry) AWS() *cloudtest.AWSEnvironment {
	return r.aws
}

func (r *Registry) Azure() *cloudtest.AzureEnvironment {
	return r.azure
}

func (r *Registry) GCP() *cloudtest.GCPEnvironment {
	return r.gcp
}
func (r *Registry) Close() {
	if r.httpsServer != nil {
		r.httpsServer.Close()
	}
	if r.aws != nil {
		r.aws.Close()
	}
	if r.azure != nil {
		r.azure.Close()
	}
	if r.gcp != nil {
		r.gcp.Close()
	}
	os.Setenv("CUE_REGISTRY", "")
}

func (r *Registry) PushModuleFromPath(
	ctx context.Context,
	version module.Version,
	path string,
) error {
	var content bytes.Buffer
	zw := zip.NewWriter(&content)
	defer zw.Close()

	p := path
	err := filepath.Walk(p, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(p, path)
		if err != nil {
			return err
		}

		if relPath == "." {
			return nil
		}

		fw, err := zw.Create(relPath)
		if err != nil {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		data, err := io.ReadAll(file)
		if err != nil {
			return err
		}
		_, err = fw.Write(data)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	err = r.CUERegistryClient().PutModule(ctx, version, bytes.NewReader(content.Bytes()), int64(content.Len()))
	if err != nil {
		return err
	}
	return nil
}

// Creates an OCI registry to test tls/https.
//
// Note: Container libs use Docker under the hood to handle OCI
// and Docker defaults to HTTP when it detects that the registry host
// is localhost or 127.0.0.1.
// In order to test OCI with a HTTPS server, we have to supply a "fake" host.
// We use a mock dns server to create an A record which binds navecd.io to 127.0.0.1.
// All OCI tests have to use navecd.io as host.
func NewTLSRegistry(private bool, cloudProviderID cloud.ProviderID) (*Registry, error) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", "navecd.io:0")
	if err != nil {
		return nil, err
	}
	listener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	addr := "navecd.io:" + strconv.Itoa(port)
	ociHandler := registry.New()
	mux := http.NewServeMux()
	mux.HandleFunc(
		"POST /oauth2/exchange",
		func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				return
			}

			expectedBody := "access_token=nottheacrtoken&grant_type=access_token&service=navecd.io%3A" + strconv.Itoa(
				port,
			) + "&tenant=tenant"
			if string(
				body,
			) != expectedBody {
				w.WriteHeader(500)
				_, _ = fmt.Fprintf(w,
					"got wrong request: %s, expected: %s",
					string(body),
					expectedBody)
				return
			}

			w.WriteHeader(200)
			_, err = w.Write([]byte(`{"refresh_token": "aaaa"}`))
			if err != nil {
				w.WriteHeader(500)
				return
			}
		},
	)
	mux.HandleFunc(
		"/v2/",
		func(w http.ResponseWriter, r *http.Request) {
			isCue := false
			agents, _ := r.Header["User-Agent"]

			for _, agent := range agents {
				if strings.HasPrefix(agent, "Cue") {
					isCue = true
					break
				}
			}

			if private && !isCue {
				auth, found := r.Header["Authorization"]
				if !found {
					w.Header().Set("WWW-Authenticate", "Basic realm=\"test\"")
					w.WriteHeader(401)
					return
				}

				if len(auth) != 1 {
					w.WriteHeader(401)
					return
				}

				credsBase64, found := strings.CutPrefix(auth[0], "Basic ")
				if !found {
					w.WriteHeader(400)
					return
				}

				credsBytes, err := base64.StdEncoding.DecodeString(credsBase64)
				if err != nil {
					w.WriteHeader(500)
					return
				}
				creds := string(credsBytes)

				var expectedCreds string
				switch cloudProviderID {
				case cloud.GCP:
					expectedCreds = "oauth2accesstoken:aaaa"
				case cloud.Azure:
					expectedCreds = "00000000-0000-0000-0000-000000000000:aaaa"
				default:
					expectedCreds = "navecd:abcd"
				}

				if creds != expectedCreds {
					w.WriteHeader(401)
					_, _ = fmt.Fprintf(w,
						"wrong credentials, expected %s", expectedCreds)
					return
				}

				ociHandler.ServeHTTP(w, r)
				return
			} else {
				ociHandler.ServeHTTP(w, r)
			}
		},
	)
	httpsServer := httptest.NewUnstartedServer(mux)
	httpsServer.Config.Addr = addr
	httpsServer.Listener = listener
	httpsServer.StartTLS()

	client := httpsServer.Client()

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	}
	client.Transport = transport
	remote.DefaultTransport = transport
	// set to to true globally as CUE for example uses the DefaultTransport
	http.DefaultTransport = transport
	http.DefaultClient.Transport = transport

	registry := &Registry{}
	switch cloudProviderID {
	case cloud.AWS:
		aws, err := cloudtest.NewAWSEnvironment(httpsServer.Config.Addr)
		if err != nil {
			return nil, err
		}
		registry.aws = aws
		registry.addr = aws.RegistryAddr()

	case cloud.Azure:
		azure, err := cloudtest.NewAzureEnvironment()
		if err != nil {
			return nil, err
		}
		registry.azure = azure
		registry.addr = httpsServer.Config.Addr

	case cloud.GCP:
		gcp, err := cloudtest.NewGCPEnvironment()
		if err != nil {
			return nil, err
		}
		registry.gcp = gcp
		registry.addr = httpsServer.Config.Addr

	default:
		registry.addr = httpsServer.Config.Addr
	}

	err = os.Setenv("CUE_REGISTRY", registry.addr)
	if err != nil {
		return nil, err
	}

	regResolver, err := modconfig.NewResolver(&modconfig.Config{
		Transport: transport,
	})
	if err != nil {
		return nil, err
	}
	cueRegistryClient := modregistry.NewClientWithResolver(regResolver)

	httpsServer.URL = strings.Replace(
		httpsServer.URL,
		"https://127.0.0.1",
		"oci://navecd.io",
		1,
	)

	fmt.Println("OCI registry listening on", httpsServer.URL)

	registry.httpsServer = httpsServer
	registry.client = client
	registry.cueRegistryClient = cueRegistryClient
	registry.provider = cloudProviderID

	return registry, nil
}

type options struct {
	private         bool
	cloudProviderID cloud.ProviderID
}

type private bool

var _ Option = (*private)(nil)

func (opt private) Apply(opts *options) {
	opts.private = bool(opt)
}

type provider cloud.ProviderID

var _ Option = (*provider)(nil)

func (opt provider) Apply(opts *options) {
	opts.cloudProviderID = cloud.ProviderID(opt)
}

type Option interface {
	Apply(*options)
}

func WithPrivate(enabled bool) private {
	return private(enabled)
}

func WithProvider(providerID cloud.ProviderID) provider {
	return provider(providerID)
}

func NewTLSRegistryWithSchema(opts ...Option) (*Registry, error) {
	options := &options{
		private:         false,
		cloudProviderID: "",
	}
	for _, o := range opts {
		o.Apply(options)
	}

	registry, err := NewTLSRegistry(options.private, options.cloudProviderID)
	if err != nil {
		return nil, err
	}

	_, filename, _, _ := runtime.Caller(0)
	dir := path.Join(path.Dir(filename), "../..")
	err = os.Chdir(dir)
	if err != nil {
		return nil, err
	}

	schemaSrc := "schema"

	ctx := context.Background()
	m, err := module.NewVersion("github.com/kharf/navecd/schema", "v0.0.99")
	if err != nil {
		return nil, err
	}

	err = registry.PushModuleFromPath(ctx, m, schemaSrc)
	if err != nil {
		return nil, err
	}

	return registry, nil
}
