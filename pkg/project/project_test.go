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
	"github.com/kharf/navecd/pkg/project"
	"gotest.tools/v3/assert"
)

func useManagerTemplate() string {
	return fmt.Sprintf(`
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
`, testtemplates.ModuleVersion)
}

func TestManager_Load(t *testing.T) {
	var err error
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.StartCUERegistry(t.TempDir())
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	env := projecttest.InitTestEnvironment(t, []byte(useManagerTemplate()))

	pm := project.NewManager(component.NewBuilder(), runtime.GOMAXPROCS(0))
	instance, err := pm.Load(env.LocalTestProject)
	assert.NilError(t, err)

	dag := instance.Dag

	ns := dag.Get("toola___Namespace")
	assert.Assert(t, ns != nil)
	nsManifest, ok := ns.(*component.Manifest)
	assert.Assert(t, ok)
	assert.Assert(t, nsManifest.GetAPIVersion() == "v1")
	assert.Assert(t, nsManifest.GetKind() == "Namespace")
	assert.Assert(t, nsManifest.GetName() == "toola")

	ns = dag.Get("toolb___Namespace")
	assert.Assert(t, ns != nil)
	nsManifest, ok = ns.(*component.Manifest)
	assert.Assert(t, ok)
	assert.Assert(t, nsManifest.GetAPIVersion() == "v1")
	assert.Assert(t, nsManifest.GetKind() == "Namespace")
	assert.Assert(t, nsManifest.GetName() == "toolb")
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

	cueModuleRegistry, err := ocitest.StartCUERegistry(b.TempDir())
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
	b.ResetTimer()
	var inst *project.Instance
	for n := 0; n < b.N; n++ {
		inst, err = pm.Load(root)
		b.StopTimer()
		assert.NilError(b, err)
		b.StartTimer()
	}
	instance = inst
}
