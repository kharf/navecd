/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/kharf/navecd/internal/dnstest"
)

var (
	dnsServer *dnstest.DNSServer
	test      *testing.T
)

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)
	test = t

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	var err error

	dnsServer, err = dnstest.NewDNSServer()
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	dnsServer.Close()
})
