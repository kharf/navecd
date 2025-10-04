package cue_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"

	"cuelang.org/go/mod/module"
	"github.com/kharf/navecd/internal/cloudtest"
	"github.com/kharf/navecd/internal/dnstest"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/internal/ocitest"
	"github.com/kharf/navecd/internal/testtemplates"
	"github.com/kharf/navecd/internal/txtar"
	"github.com/kharf/navecd/pkg/cloud"
	"github.com/kharf/navecd/pkg/cue"
	"go.uber.org/zap/zapcore"
	"gotest.tools/v3/assert"
	ctrlZap "sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var project = fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/test@v1"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/toola/namespace.cue --
package toola

import (
	"github.com/kharf/navecd/schema/component"
)

#namespace: {
	apiVersion: "v1"
	kind:       "Namespace"
	metadata: name: "toola"
}

ns: component.#Manifest & {
	content: #namespace
}
`,
	testtemplates.ModuleVersion)

func TestLoad(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registry, err := ocitest.NewTLSRegistry(false, "")
	assert.NilError(t, err)
	defer registry.Close()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	gcpCloudEnvironment, err := cloudtest.NewGCPEnvironment()
	assert.NilError(t, err)
	defer gcpCloudEnvironment.Close()

	arch, err := txtar.Create(t.TempDir(), strings.NewReader(project))
	assert.NilError(t, err)

	var content bytes.Buffer
	zw := zip.NewWriter(&content)
	defer zw.Close()
	for _, file := range arch.Files {
		fw, err := zw.Create(file.Name)
		assert.NilError(t, err)
		_, err = fw.Write(file.Data)
		assert.NilError(t, err)
	}
	assert.NilError(t, zw.Close())

	version, err := module.NewVersion("github.com/test", "v1.5.0")
	assert.NilError(t, err)
	err = registry.CUERegistryClient().PutModule(t.Context(), version, bytes.NewReader(content.Bytes()), int64(content.Len()))
	assert.NilError(t, err)

	moduleManager := cue.ModuleManager{
		KubeClient:           kubernetes.DynamicTestKubeClient.DynamicClient(),
		GCPMetadataServerURL: gcpCloudEnvironment.HttpsServer.URL,
	}
	_, err = moduleManager.Load(t.Context(), fmt.Sprintf("%s/%s", registry.Addr(), version.BasePath()), "v1.5.0", t.TempDir(), cloud.Auth{
		WorkloadIdentity: &cloud.WorkloadIdentity{
			Provider: cloud.GCP,
		},
	})
	assert.NilError(t, err)
}
