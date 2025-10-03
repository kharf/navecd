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
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
)

const (
	AzureAuthHost = "login.microsoftonline.com"
)

// A test Cloud Environment imitating Azure Active Directory.
type AzureEnvironment struct {
	TokenServer      *httptest.Server
	OIDCIssuerServer *httptest.Server
}

func (env *AzureEnvironment) Close() {
	if env.TokenServer != nil {
		env.TokenServer.Close()
	}
	if env.OIDCIssuerServer != nil {
		env.OIDCIssuerServer.Close()
	}
}

type azureDiscoveryDocument struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
}

type azureAccessToken struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

func NewAzureEnvironment() (*AzureEnvironment, error) {
	tokenMux := http.NewServeMux()
	tokenMux.HandleFunc(
		"POST /token",
		func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				return
			}

			if string(
				body,
			) != "client_assertion=federatedtoken&client_assertion_type=urn%3Aietf%3Aparams%3Aoauth%3Aclient-assertion-type%3Ajwt-bearer"+
				"&client_id=xxx&client_info=1&grant_type=client_credentials&scope=https%3A%2F%2Fmanagement.azure.com%2F.default+openid+offline_access+profile" {
				w.WriteHeader(500)
				return
			}

			w.WriteHeader(200)
			err = json.NewEncoder(w).Encode(&azureAccessToken{
				AccessToken: "nottheacrtoken",
				ExpiresIn:   10 * 60,
				TokenType:   "bearer",
			})
			if err != nil {
				w.WriteHeader(500)
				return
			}
		},
	)

	tokenServer := httptest.NewUnstartedServer(tokenMux)
	tokenServer.StartTLS()
	fmt.Println("Azure Token Server listening on", tokenServer.URL)

	tenantID := "tenant"
	oidcIssuerMux := http.NewServeMux()
	oidcIssuerServer, err := newUnstartedServerFromEndpoint(
		"0",
		oidcIssuerMux,
	)
	if err != nil {
		return nil, err
	}
	oidcIssuerMux.HandleFunc(
		fmt.Sprintf("GET /%s/v2.0/.well-known/openid-configuration", tenantID),
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			err := json.NewEncoder(w).Encode(&azureDiscoveryDocument{
				AuthorizationEndpoint: "auth",
				TokenEndpoint:         fmt.Sprintf("%s/token", tokenServer.URL),
				Issuer:                oidcIssuerServer.URL,
			})
			if err != nil {
				w.WriteHeader(500)
				return
			}
		},
	)
	oidcIssuerServer.StartTLS()
	fmt.Println("Azure OIDC Issuer Server listening on", oidcIssuerServer.URL)

	tokenFileDir, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, err
	}

	tokenFile := filepath.Join(tokenFileDir, "token")
	err = os.WriteFile(tokenFile, []byte("federatedtoken"), 0666)
	if err != nil {
		return nil, err
	}

	os.Setenv(
		"AZURE_CLIENT_ID",
		"xxx",
	)
	os.Setenv(
		"AZURE_FEDERATED_TOKEN_FILE",
		tokenFile,
	)
	os.Setenv(
		"AZURE_TENANT_ID",
		"tenant",
	)
	os.Setenv(
		"AZURE_AUTHORITY_HOST",
		oidcIssuerServer.URL,
	)

	return &AzureEnvironment{
		TokenServer:      tokenServer,
		OIDCIssuerServer: oidcIssuerServer,
	}, nil
}
