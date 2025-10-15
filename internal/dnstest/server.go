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

package dnstest

import (
	"fmt"
	"net"

	"github.com/foxcpp/go-mockdns"
	"github.com/kharf/navecd/internal/cloudtest"
)

type nullLogger struct{}

var _ mockdns.Logger = (*nullLogger)(nil)

func (l nullLogger) Printf(f string, args ...interface{}) {}

type DNSServer struct {
	mock *mockdns.Server
}

func (server *DNSServer) Close() {
	server.mock.Close()
}

// Create a mock dns server which binds several hostnames to 127.0.0.1.
// All OCI tests have to use navecd.io as registry host.
func NewDNSServer() (*DNSServer, error) {
	dnsServer, err := mockdns.NewServerWithLogger(map[string]mockdns.Zone{
		"navecd.io.": {
			A: []string{"127.0.0.1"},
		},
		"metadata.google.internal.": {
			A: []string{"127.0.0.1"},
		},
		"docker.io.": {
			CNAME: "navecd.io",
		},
		"index.docker.io.": {
			CNAME: "navecd.io",
		},
		"registry.cue.works": {
			A: []string{"127.0.0.1"},
		},
		fmt.Sprintf("%s.", cloudtest.AWSRegistryHost): {
			A: []string{"127.0.0.1"},
		},
		fmt.Sprintf("%s.", cloudtest.AWSApiECRHost): {
			A: []string{"127.0.0.1"},
		},
		fmt.Sprintf("%s.", cloudtest.AzureAuthHost): {
			A: []string{"127.0.0.1"},
		},
	},
		nullLogger{},
		false,
	)
	if err != nil {
		return nil, err
	}

	dnsServer.PatchNet(net.DefaultResolver)

	return &DNSServer{
		mock: dnsServer,
	}, nil
}
