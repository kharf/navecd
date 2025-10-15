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

package main

import (
	"flag"
	"fmt"
	_ "net/http/pprof"
	"os"

	_ "go.uber.org/automaxprocs"

	_ "github.com/grafana/pyroscope-go/godeltaprof/http/pprof"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/kharf/navecd/internal/controller"
)

var (
	Version string
)

func main() {
	var metricsAddr string
	var probeAddr string
	var logLevel int
	var namespacePodinfoPath string
	var namePodinfoPath string
	var shardPodinfoPath string
	var insecureSkipTLSverify bool
	var plainHTTP bool
	flag.StringVar(
		&metricsAddr,
		"metrics-bind-address",
		"",
		"The address the metric endpoint binds to.",
	)
	flag.StringVar(
		&probeAddr,
		"health-probe-bind-address",
		"",
		"The address the probe endpoint binds to.",
	)
	flag.IntVar(&logLevel, "log-level", 0, "The verbosity level. Higher means chattier.")
	flag.StringVar(
		&namespacePodinfoPath,
		"namespace-podinfo-path",
		"",
		"The file which holds the controller namespace.",
	)
	flag.StringVar(
		&namePodinfoPath,
		"name-podinfo-path",
		"",
		"The file which holds the controller name.",
	)
	flag.StringVar(
		&shardPodinfoPath,
		"shard-podinfo-path",
		"",
		"The file which holds the controller shard.",
	)
	flag.BoolVar(
		&insecureSkipTLSverify,
		"insecure-skip-tls-verify",
		false,
		"InsecureSkipVerify controls whether the Helm client verifies the server's certificate chain and host name.",
	)
	flag.BoolVar(
		&plainHTTP,
		"plain-http",
		false,
		"Force http for Helm registries.",
	)
	flag.Parse()

	cfg := ctrl.GetConfigOrDie()

	mgr, err := controller.Setup(
		cfg,
		controller.NamePodinfoPath(namePodinfoPath),
		controller.NamespacePodinfoPath(namespacePodinfoPath),
		controller.ShardPodinfoPath(shardPodinfoPath),
		controller.MetricsAddr(metricsAddr),
		controller.ProbeAddr(probeAddr),
		controller.LogLevel(logLevel),
		controller.PlainHTTP(plainHTTP),
		controller.InsecureSkipTLSverify(insecureSkipTLSverify),
	)
	if err != nil {
		os.Exit(1)
	}

	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
