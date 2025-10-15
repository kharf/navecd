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

package cloudtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	AWSApiECRHost   = "api.ecr.eu-north-1.amazonaws.com"
	AWSRegistryHost = "account-id.dkr.ecr.eu-north-1.amazonaws.com"
)

// A test Cloud Environment imitating AWS Pod Identity Agents and ECR auth.
// In order to test AWS OCI, we have to bind some hosts to localhost.
// We use a mock dns server to create an A record which binds api.ecr.eu-north-1.amazonaws.com and account-id.dkr.ecr.eu-north-1.amazonaws.com to 127.0.0.1.
// All AWS OCI tests have to use account-id.dkr.ecr.eu-north-1.amazonaws.com (AWSRegistryHost) as host.
type AWSEnvironment struct {
	PodIdentityAgent *httptest.Server
	ECRServer        *httptest.Server
	addr             string
}

func (env *AWSEnvironment) RegistryAddr() string {
	return env.addr
}

func (env *AWSEnvironment) Close() {
	if env.PodIdentityAgent != nil {
		env.PodIdentityAgent.Close()
	}
	if env.ECRServer != nil {
		env.ECRServer.Close()
	}
}

type authorizationData struct {
	AuthorizationToken string `json:"authorizationToken"`
	ExpiresAt          int64  `json:"expiresAt"`
	ProxyEndpoint      string `json:"proxyEndpoint"`
}

type awsToken struct {
	AuthorizationData []authorizationData `json:"authorizationData"`
}

type awsCredentials struct {
	Version         int
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string
	SessionToken    string
	Expiration      *time.Time
}

func NewAWSEnvironment(
	registryAddr string,
) (*AWSEnvironment, error) {
	agentMux := http.NewServeMux()
	agentMux.HandleFunc(
		"GET /get-credentials",
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			creds := awsCredentials{
				AccessKeyID:     "aaaa",
				SecretAccessKey: "bbbb",
			}
			err := json.NewEncoder(w).Encode(&creds)
			if err != nil {
				w.WriteHeader(500)
				return
			}
		},
	)

	agentServer := httptest.NewUnstartedServer(agentMux)
	agentServer.Start()
	os.Setenv(
		"AWS_CONTAINER_CREDENTIALS_FULL_URI",
		agentServer.URL+"/get-credentials",
	)
	os.Setenv("AWS_CONTAINER_AUTHORIZATION_TOKEN", "Bearer aaaa")
	fmt.Println("Pod Identity Agent Server listening on", agentServer.URL)

	ecrMux := http.NewServeMux()
	url, err := url.Parse("https://" + registryAddr)
	if err != nil {
		return nil, err
	}
	registryRProxy := httputil.NewSingleHostReverseProxy(url)

	ecrMux.HandleFunc(
		"/",
		func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/v2") {
				registryRProxy.ServeHTTP(w, r)
				return
			}

			w.WriteHeader(200)
			token := awsToken{
				AuthorizationData: []authorizationData{
					{
						AuthorizationToken: "bmF2ZWNkOmFiY2Q=",
						ExpiresAt:          time.Now().Add(10 * time.Minute).Unix(),
					},
				},
			}
			err := json.NewEncoder(w).Encode(&token)
			if err != nil {
				w.WriteHeader(500)
				return
			}
		},
	)
	ecrServer, err := newUnstartedServerFromEndpoint(
		"0",
		ecrMux,
	)
	if err != nil {
		return nil, err
	}
	ecrServer.StartTLS()

	fmt.Println("ECR Server listening on", ecrServer.URL)

	return &AWSEnvironment{
		PodIdentityAgent: agentServer,
		ECRServer:        ecrServer,
		addr: strings.Replace(
			ecrServer.URL,
			"https://127.0.0.1",
			AWSRegistryHost,
			1,
		),
	}, nil
}
