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

package project_test

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"text/template"

	"github.com/kharf/navecd/internal/dnstest"
	"github.com/kharf/navecd/internal/ocitest"
	"github.com/kharf/navecd/internal/projecttest"
	"github.com/kharf/navecd/internal/testtemplates"
	"github.com/kharf/navecd/internal/txtar"
	"github.com/kharf/navecd/pkg/component"
	"github.com/kharf/navecd/pkg/oci"
	"github.com/kharf/navecd/pkg/project"
	"gotest.tools/v3/assert"
)

type manifestMeta struct {
	apiVersion string
	kind       string
	name       string
	namespace  string
}

func TestManager_Load(t *testing.T) {
	testCases := []struct {
		name                string
		reconcileDir        string
		template            string
		expectedManifests   []manifestMeta
		unexpectedManifests []manifestMeta
	}{
		{
			name:         "SingleStageProject",
			reconcileDir: ".",
			template: fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/controller/projectone@v0"
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

-- infra/toolb/namespace.cue --
package toolb

import (
	"github.com/kharf/navecd/schema/component"
)

#namespace: {
	apiVersion: "v1"
	kind:       "Namespace"
	metadata: name: "toolb"
}

ns: component.#Manifest & {
	content: #namespace
}
`, testtemplates.ModuleVersion),

			expectedManifests: []manifestMeta{
				{
					apiVersion: "v1",
					kind:       "Namespace",
					name:       "toola",
					namespace:  "",
				},
				{
					apiVersion: "v1",
					kind:       "Namespace",
					name:       "toolb",
					namespace:  "",
				},
			},

			unexpectedManifests: []manifestMeta{},
		},

		{
			name:         "MultiStageProject",
			reconcileDir: "dev",
			template: fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/controller/projectone@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- dev/infra/toola/namespace.cue --
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

-- int/infra/toolb/namespace.cue --
package toolb

import (
	"github.com/kharf/navecd/schema/component"
)

#namespace: {
	apiVersion: "v1"
	kind:       "Namespace"
	metadata: name: "toolb"
}

ns: component.#Manifest & {
	content: #namespace
}
`, testtemplates.ModuleVersion),

			expectedManifests: []manifestMeta{
				{
					apiVersion: "v1",
					kind:       "Namespace",
					name:       "toola",
					namespace:  "",
				},
			},
			unexpectedManifests: []manifestMeta{
				{
					apiVersion: "v1",
					kind:       "Namespace",
					name:       "toolb",
					namespace:  "",
				},
			},
		},
	}

	var err error
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			env := projecttest.InitTestEnvironment(t)
			defer env.Close()

			repository := env.PushProject(t, "test", "latest", []byte(tc.template))

			pm := project.NewManager(component.NewBuilder(), runtime.GOMAXPROCS(0))

			instance, err := pm.Load(
				t.Context(),
				filepath.Join(env.TestRoot, "project"),
				tc.reconcileDir,
				project.WithRemoteLoader(&project.OCIRemoteLoader{
					Repository: repository,
					CacheDir:   t.TempDir(),
				}),
			)
			assert.NilError(t, err)

			dag := instance.Dag

			for _, expectedManifest := range tc.expectedManifests {
				apiVersionSplit := strings.Split(expectedManifest.apiVersion, "/")
				group := ""
				if len(apiVersionSplit) == 2 {
					group = apiVersionSplit[0]
				}
				manifest := dag.Get(
					fmt.Sprintf(
						"%s_%s_%s_%s",
						expectedManifest.name,
						expectedManifest.namespace,
						group,
						expectedManifest.kind,
					),
				)

				assert.Assert(t, manifest != nil)
				compManifest, ok := manifest.(*component.Manifest)
				assert.Assert(t, ok)
				assert.Equal(t, compManifest.GetAPIVersion(), expectedManifest.apiVersion)
				assert.Equal(t, compManifest.GetKind(), expectedManifest.kind)
				assert.Equal(t, compManifest.GetName(), expectedManifest.name)
				assert.Equal(t, compManifest.GetNamespace(), expectedManifest.namespace)
			}

			for _, unexpectedManifest := range tc.unexpectedManifests {
				apiVersionSplit := strings.Split(unexpectedManifest.apiVersion, "/")
				group := ""
				if len(apiVersionSplit) == 2 {
					group = apiVersionSplit[0]
				}
				manifest := dag.Get(
					fmt.Sprintf(
						"%s_%s_%s_%s",
						unexpectedManifest.name,
						unexpectedManifest.namespace,
						group,
						unexpectedManifest.kind,
					),
				)

				assert.Assert(t, manifest == nil)
			}
		})
	}
}

func TestManager_Load_UnrecoverableError(t *testing.T) {
	var err error
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	env := projecttest.InitTestEnvironment(t)
	defer env.Close()

	pm := project.NewManager(component.NewBuilder(), runtime.GOMAXPROCS(0))

	_, err = pm.Load(
		t.Context(),
		filepath.Join(env.TestRoot, "project"),
		".",
		project.WithRemoteLoader(&projecttest.FakeRemoteLoader{
			Err: &oci.UnrecoverableError{},
		}),
	)
	assert.ErrorIs(t, err, project.ErrLoadProject)
}

func TestManager_Load_LoadError(t *testing.T) {
	var err error
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	pm := project.NewManager(component.NewBuilder(), runtime.GOMAXPROCS(0))

	projectPath := filepath.Join(t.TempDir(), "project")
	_, err = pm.Load(
		t.Context(),
		projectPath,
		".",
		project.WithRemoteLoader(&project.OCIRemoteLoader{
			Repository: project.OCIRepositoryRef{
				Name: "test",
				Ref:  "test",
			},
			CacheDir: t.TempDir(),
		}),
	)
	assert.ErrorIs(t, err, project.ErrLoadProject)
}

func TestManager_Load_LoadError_Recoverable(t *testing.T) {
	var err error
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	env := projecttest.InitTestEnvironment(t)
	defer env.Close()

	template := fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/controller/projectone@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- dev/infra/toola/namespace.cue --
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
`, testtemplates.ModuleVersion)
	repository := env.PushProject(t, "test", "latest", []byte(template))

	pm := project.NewManager(component.NewBuilder(), runtime.GOMAXPROCS(0))

	projectPath := filepath.Join(env.TestRoot, "project")
	withProjectLoader := project.WithRemoteLoader(&project.OCIRemoteLoader{
		Repository: repository,
		CacheDir:   t.TempDir(),
	})
	instance, err := pm.Load(
		t.Context(),
		projectPath,
		".",
		withProjectLoader,
	)
	assert.NilError(t, err)
	assert.Equal(t, instance.Path, projectPath)

	manifestID := fmt.Sprintf(
		"%s_%s_%s_%s",
		"toola",
		"",
		"",
		"Namespace",
	)
	manifest := instance.Dag.Get(manifestID)
	assert.Assert(t, manifest != nil)

	env.OCIRegistry.Close()

	instance, err = pm.Load(
		t.Context(),
		projectPath,
		".",
		withProjectLoader,
	)
	assert.NilError(t, err)
	assert.Error(t, instance.LoadError, (&project.RecoverableLoadError{}).Error())

	manifest = instance.Dag.Get(manifestID)
	assert.Assert(t, manifest != nil)
}

func TestManager_Load_Update(t *testing.T) {
	var err error
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	env := projecttest.InitTestEnvironment(t)
	defer env.Close()

	template := fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/controller/projectone@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- dev/infra/toola/namespace.cue --
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
`, testtemplates.ModuleVersion)
	repository := env.PushProject(t, "test", "latest", []byte(template))

	pm := project.NewManager(component.NewBuilder(), runtime.GOMAXPROCS(0))

	projectPath := filepath.Join(env.TestRoot, "project")
	instance, err := pm.Load(
		t.Context(),
		projectPath,
		".",
		project.WithRemoteLoader(&project.OCIRemoteLoader{
			Repository: repository,
			CacheDir:   t.TempDir(),
		}),
	)
	assert.NilError(t, err)
	assert.Equal(t, instance.Path, projectPath)

	manifestID := fmt.Sprintf(
		"%s_%s_%s_%s",
		"toola",
		"",
		"",
		"Namespace",
	)
	manifest := instance.Dag.Get(manifestID)
	assert.Assert(t, manifest != nil)

	template = fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/controller/projectone@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- dev/infra/toola/namespace.cue --
package toola

import (
	"github.com/kharf/navecd/schema/component"
)

#namespace: {
	apiVersion: "v1"
	kind:       "Namespace"
	metadata: name: "toolc"
}

ns: component.#Manifest & {
	content: #namespace
}
`, testtemplates.ModuleVersion)
	repository = env.PushProject(t, "test", "latest", []byte(template))

	instance, err = pm.Load(
		t.Context(),
		projectPath,
		".",
		project.WithRemoteLoader(&project.OCIRemoteLoader{
			Repository: repository,
			CacheDir:   t.TempDir(),
		}),
	)
	assert.NilError(t, err)

	manifest = instance.Dag.Get(manifestID)
	assert.Assert(t, manifest == nil)

	manifestID = fmt.Sprintf(
		"%s_%s_%s_%s",
		"toolc",
		"",
		"",
		"Namespace",
	)

	manifest = instance.Dag.Get(manifestID)
	assert.Assert(t, manifest != nil)
}

var appTemplate = `
-- infra/{{ .Package }}/namespace.cue --
package {{ .Package }}

import (
	"github.com/kharf/navecd/schema/component"
)

#namespace: {
	apiVersion: "v1"
	kind:       "Namespace"
	metadata: name: "{{ .Namespace }}"
}

ns: component.#Manifest & {
	content: #namespace
}


service: component.#Manifest & {
	dependencies: [
		ns.id, deployment.id,
	]

	content: {
		apiVersion: string | *"v1"
		kind:       "Service"
		metadata: {
			name:      "{{ .Name }}"
			namespace: "{{ .Namespace }}"
			labels: app: "{{ .Name }}"
		}
		spec: {
			ports: [{
				port:       8080
				name:       "high"
				protocol:   "TCP"
				targetPort: 8080
			}]
			selector: app: "{{ .Name }}"
		}
	}
}

deployment: component.#Manifest & {
	dependencies: [
		ns.id,
	]

	content: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		metadata: {
			name:      "{{ .Name }}"
			namespace: "{{ .Namespace }}"
		}
		spec: {
			replicas: 1 @ignore(conflict)
			selector: matchLabels: app: deployment.content.metadata.name
			template: {
				metadata: labels: app: deployment.content.metadata.name
				spec: {
					securityContext: {
						runAsNonRoot:        true  @ignore(conflict)
						fsGroup:             65532 @ignore(conflict)
						fsGroupChangePolicy: "OnRootMismatch"
					}
					containers: [
						{
							name:  "container"
							image: "container:1.14.2"
							ports: [{
								name:          "http"
								containerPort: 80
							}]
						},
					]
				}
			}
		}
	}
}
`

var instance *project.Instance

func BenchmarkManager_Load(b *testing.B) {
	b.ReportAllocs()

	moduleTemplate := fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/controller/projectbench@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}
`, testtemplates.ModuleVersion)

	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(b, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(b, err)
	defer cueModuleRegistry.Close()

	root := b.TempDir()

	tmpl, err := template.New("").Parse(appTemplate)
	assert.NilError(b, err)

	_, err = txtar.Create(root, strings.NewReader(moduleTemplate))
	assert.NilError(b, err)

	count := 250
	for i := range count {
		sb := strings.Builder{}
		err = tmpl.Execute(&sb, map[string]string{
			"Package":   fmt.Sprintf("app%v", i),
			"Namespace": fmt.Sprintf("app%v", i),
			"Name":      fmt.Sprintf("app%v", i),
		})
		assert.NilError(b, err)

		_, err = txtar.Create(root, strings.NewReader(sb.String()))
		assert.NilError(b, err)
	}

	pm := project.NewManager(component.NewBuilder(), -1)
	ctx := b.Context()

	var inst *project.Instance
	for b.Loop() {
		inst, err = pm.Load(ctx, root, ".")
		b.StopTimer()
		assert.NilError(b, err)
		b.StartTimer()
	}
	instance = inst
}
