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
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"cuelabs.dev/go/oci/ociregistry/ocimem"
	"cuelabs.dev/go/oci/ociregistry/ociserver"
	"cuelang.org/go/mod/modregistry"
	"cuelang.org/go/mod/module"
	"cuelang.org/go/mod/modzip"

	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/kharf/navecd/pkg/cloud"
)

type Registry struct {
	httpsServer *httptest.Server
	*ocimem.Registry
	client         *http.Client
	registryClient *modregistry.Client
}

func (r *Registry) Client() *http.Client {
	return r.client
}

func (r *Registry) CUERegistryClient() *modregistry.Client {
	return r.registryClient
}

func (r *Registry) Addr() string {
	return r.httpsServer.Config.Addr
}

func (r *Registry) URL() string {
	return r.httpsServer.URL
}

func (r *Registry) Close() {
	if r.httpsServer != nil {
		r.httpsServer.Close()
	}
	os.Setenv("CUE_REGISTRY", "")
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
	registry := ocimem.New()
	ociHandler := ociserver.New(registry, nil)
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
				_, _ = w.Write(
					[]byte(
						fmt.Sprintf(
							"got wrong request: %s, expected: %s",
							string(body),
							expectedBody,
						),
					),
				)
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
			if private {
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
					_, _ = w.Write(
						[]byte(fmt.Sprintf("wrong credentials, expected %s", expectedCreds)),
					)
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

	ociClient := modregistry.NewClient(registry)

	httpsServer.URL = strings.Replace(
		httpsServer.URL,
		"https://127.0.0.1",
		"oci://navecd.io",
		1,
	)

	return &Registry{
		httpsServer:    httpsServer,
		client:         client,
		registryClient: ociClient,
		Registry:       registry,
	}, nil
}

func StartCUERegistry(
	registryPath string,
) (*Registry, error) {
	cueModuleRegistry, err := NewTLSRegistry(false, "")
	if err != nil {
		return nil, err
	}

	ociClient := cueModuleRegistry.CUERegistryClient()
	modDir, err := os.MkdirTemp(registryPath, "")
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
	err = os.CopyFS(filepath.Join(modDir, schemaSrc), os.DirFS(schemaSrc))
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	m, err := module.NewVersion("github.com/kharf/navecd/schema", "v0.0.99")
	if err != nil {
		return nil, err
	}

	schemaModuleReader, schemaLen, err := createImage(m, modDir, "schema")
	if err != nil {
		return nil, err
	}

	err = ociClient.PutModule(ctx, m, schemaModuleReader, schemaLen)
	if err != nil {
		return nil, err
	}

	err = os.Setenv("CUE_REGISTRY", cueModuleRegistry.Addr())
	if err != nil {
		return nil, err
	}
	return cueModuleRegistry, nil
}

func createImage(
	m module.Version,
	modDir string,
	mod string,
) (io.ReaderAt, int64, error) {
	zipFile, err := createZip(m, modDir, mod)
	if err != nil {
		return nil, 0, err
	}
	return bytes.NewReader(zipFile), int64(len(zipFile)), nil
}

func createZip(
	m module.Version,
	modDir string,
	mod string,
) ([]byte, error) {
	var zipBytes bytes.Buffer
	err := modzip.CreateFromDir(&zipBytes, m, filepath.Join(modDir, mod))
	if err != nil {
		return nil, err
	}
	return zipBytes.Bytes(), nil
}
