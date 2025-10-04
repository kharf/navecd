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

package component

import (
	"fmt"
	"strings"
	"testing"

	"github.com/kharf/navecd/internal/dnstest"
	"github.com/kharf/navecd/internal/ocitest"
	"github.com/kharf/navecd/internal/testtemplates"
	"github.com/kharf/navecd/internal/txtar"
	"github.com/kharf/navecd/pkg/cloud"
	"github.com/kharf/navecd/pkg/helm"
	"github.com/kharf/navecd/pkg/kube"
	"go.uber.org/goleak"
	"gotest.tools/v3/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func useAllFeaturesTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/success/component.cue --
package success

import (
	"github.com/kharf/navecd/schema/component"
	"github.com/kharf/navecd/schema/workloadidentity"
)

#namespace: {
	@ignore(conflict)

	apiVersion: string | *"v1" @ignore(conflict)
	kind:       "Namespace"
	metadata: {
		name: "prometheus" @ignore(conflict)
	}
}

ns: component.#Manifest & {
	content: #namespace
}

#secret: {
	apiVersion: string | *"v1"
	kind:       string | *"Secret"
	metadata: {
		name:      "secret"
		namespace: #namespace.metadata.name
	} | {
		name:      "default-secret-name"
		namespace: "default-secret-namespace"
	}
	...
}

secret: component.#Manifest & {
	dependencies: [
		ns.id,
	]
	content: #secret & {
		metadata: {
			name: "secret"
		}
		data: {
			foo: 'bar' @ignore(conflict)
		}
	}
}

_deployment: {
	apiVersion: "apps/v1"
	kind:       "Deployment"
	metadata: {
		name:      "prometheus"
		namespace: ns.content.metadata.name
	}
	spec: {
		replicas: 1 @ignore(conflict)
		selector: matchLabels: app: _deployment.metadata.name
		template: {
			metadata: labels: app: _deployment.metadata.name
			spec: {
				containers: [
					{
						name:  "prometheus"
						image: "prometheus:1.14.2"
						ports: [{
							containerPort: 80
						}]
					},
					{
						name:  "sidecar"
						image: "sidecar:1.14.2" @ignore(conflict) // ignore attribute in lists is not supported
						ports: [{
							containerPort: 80
						}]
					},
					{
						name:  "sidecar2"
						image: "sidecar2:1.14.2" @ignore(conflict)
						ports: [{
							containerPort: 80
						}]
					},
				] @ignore(conflict)
			}
		}
	}
}

deployment: component.#Manifest & {
	dependencies: [
		ns.id,
	]
	content: _deployment
}

role: component.#Manifest & {
	dependencies: [ns.id]
	content: {
		apiVersion: "rbac.authorization.k8s.io/v1"
		kind:       "Role"
		metadata: {
			name:      "prometheus"
			namespace: ns.content.metadata.name
		}
		rules: [
			{
				apiGroups: ["coordination.k8s.io"]
				resources: ["leases"]
				verbs: [
					"get",
					"create",
					"update",
				]
			},
			{
				apiGroups: [""]
				resources: ["events"]
				verbs: [
					"create",
					"patch",
				]
			},
		]
	}
}

#deployment: {
	apiVersion: string | *"apps/v1"
	kind:       "Deployment"
	...
}

_chart: {
	name:    "test"
	repoURL: "oci://test"
	version: "4.9.9"
}

release: component.#HelmRelease & {
	dependencies: [
		ns.id,
	]
	name:      "test"
	namespace: #namespace.metadata.name

	chart: _chart

	patches: [
		#deployment & {
			metadata: {
				name:      "test"
				namespace: ns.content.metadata.name
			}
			spec: {
				replicas: 1 @ignore(conflict)
			}
		},
		#deployment & {
			metadata: {
				name:      "hello"
				namespace: ns.content.metadata.name
			}
			spec: {
				replicas: 2 @ignore(conflict)
				template: {
					spec: {
						containers: [
							{
								name:  "prometheus"
								image: "prometheus:1.14.2"
								ports: [{
									containerPort: 80
								}]
							},
							{
								name:  "sidecar"
								image: "sidecar:1.14.2" @ignore(conflict) // attributes in lists are not supported
								ports: [{
									containerPort: 80
								}]
							},
						] @ignore(conflict)
					}
				}
			}
		},
	]

	values: {
		autoscaling: enabled: true
	}
}

releaseSecretRef: component.#HelmRelease & {
	dependencies: [
		ns.id,
	]
	name:      "test-secret-ref"
	namespace: #namespace.metadata.name
	chart: {
		name:    "test"
		repoURL: "oci://test"
		version: "test"
		auth: secretRef: {
			name:      "secret"
			namespace: "namespace"
		}
	}
}

releaseWorkloadIdentity: component.#HelmRelease & {
	dependencies: [
		ns.id,
	]
	name:      "test-workload-identity"
	namespace: #namespace.metadata.name
	chart: {
		name:    "test"
		repoURL: "oci://test"
		version: "test"
		auth:    workloadidentity.#GCP
	}
	values: {
		autoscaling: enabled: true
	}
}

crd: component.#Manifest & {
	content: {
		apiVersion: "apiextensions.k8s.io/v1"
		kind:       "CustomResourceDefinition"
		metadata: {
			annotations: "controller-gen.kubebuilder.io/version": "v0.15.0"
			name: "gitopsprojects.gitops.navecd.io"
		}
		spec: {
			group: "gitops.navecd.io"
			names: {
				kind:     "GitOpsProject"
				listKind: "GitOpsProjectList"
				plural:   "gitopsprojects"
				singular: "gitopsproject"
			}
			scope: "Namespaced"
			versions: [{
				name: "v1beta1"
				schema: openAPIV3Schema: {
					description: "GitOpsProject is the Schema for the gitopsprojects API"
					properties: {
						apiVersion: {
							description: """
	APIVersion defines the versioned schema of this representation of an object.
	Servers should convert recognized schemas to the latest internal value, and
	may reject unrecognized values.
	More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources
	"""
							type: "string"
						}
						kind: {
							description: """
	Kind is a string value representing the REST resource this object represents.
	Servers may infer this from the endpoint the client submits requests to.
	Cannot be updated.
	In CamelCase.
	More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds
	"""
							type: "string"
						}
						metadata: type: "object"
						spec: {
							description: "GitOpsProjectSpec defines the desired state of GitOpsProject"
							properties: {
								branch: {
									description: "The branch of the gitops repository holding the navecd configuration."
									minLength:   1
									type:        "string"
								}
								pullIntervalSeconds: {
									description: "This defines how often navecd will try to fetch changes from the gitops repository."
									minimum:     5
									type:        "integer"
								}
								serviceAccountName: type: "string"
								suspend: {
									description: """
	This flag tells the controller to suspend subsequent executions, it does
	not apply to already started executions.  Defaults to false.
	"""
									type: "boolean"
								}
								url: {
									description: "The url to the gitops repository."
									minLength:   1
									type:        "string"
								}
							}
							required: [
								"branch",
								"pullIntervalSeconds",
								"url",
							]
							type: "object"
						}
						status: {
							description: "GitOpsProjectStatus defines the observed state of GitOpsProject"
							properties: {
								conditions: {
									items: {
										description: ""
										properties: {
											lastTransitionTime: {
												description: """
	lastTransitionTime is the last time the condition transitioned from one status to another.
	This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.
	"""
												format: "date-time"
												type:   "string"
											}
											message: {
												description: """
	message is a human readable message indicating details about the transition.
	This may be an empty string.
	"""
												maxLength: 32768
												type:      "string"
											}
											observedGeneration: {
												description: """
	observedGeneration represents the .metadata.generation that the condition was set based upon.
	For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date
	with respect to the current state of the instance.
	"""
												format:  "int64"
												minimum: 0
												type:    "integer"
											}
											reason: {
												description: """
	reason contains a programmatic identifier indicating the reason for the condition's last transition.
	Producers of specific condition types may define expected values and meanings for this field,
	and whether the values are considered a guaranteed API.
	The value should be a CamelCase string.
	This field may not be empty.
	"""
												maxLength: 1024
												minLength: 1
												pattern:   "^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$"
												type:      "string"
											}
											status: {
												description: "status of the condition, one of True, False, Unknown."
												enum: [
													"True",
													"False",
													"Unknown",
												]
												type: "string"
											}
											type: {
												description: ""
												maxLength:   316
												pattern:     "^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])$"
												type:        "string"
											}
										}
										required: [
											"lastTransitionTime",
											"message",
											"reason",
											"status",
											"type",
										]
										type: "object"
									}
									type: "array"
								}
								revision: {
									properties: {
										commitHash: type: "string"
										reconcileTime: {
											format: "date-time"
											type:   "string"
										}
									}
									type: "object"
								}
							}
							type: "object"
						}
					}
					type: "object"
				}
				served:  true
				storage: true
				subresources: status: {}
			}]
		}
	}
}
`, testtemplates.ModuleVersion)
}

func useContentWrongTypeTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/contentwrongtype/component.cue --
package contentwrongtype

namespace: {
	type: "Manifest"
	id:   "unimportant"
	dependencies: []
	content: "hello"
}
`, testtemplates.ModuleVersion)
}

func usePatchesWrongTypeTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/patcheswrongtype/component.cue --
package patcheswrongtype

release: {
	type: "HelmRelease"
	id:   "unimportant"
	dependencies: []
	name:      "test"
	namespace: "test"
	chart: {
		name:    "test"
		repoURL: "oci://test"
		version: "test"
	}

	patches: [
		"hello",
	]

	values: {}
}
`, testtemplates.ModuleVersion)
}

func useMissingIDTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/idmissing/component.cue --
package idmissing

secret: {
	type: "Manifest"
	dependencies: []
	content: {
		apiVersion: "v1"
		kind:       "Secret"
		data: {
			foo: 'bar'
		}
	}
}
`, testtemplates.ModuleVersion)
}

func useMissingMetadataTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/metadatamissing/component.cue --
package metadatamissing

secret: {
	type: "Manifest"
	id:   "unimportant"
	dependencies: []
	content: {
		apiVersion: "v1"
		kind:       "Secret"
		data: {
			foo: 'bar'
		}
	}
}
`, testtemplates.ModuleVersion)
}

func useMissingMetadataNameWithSchemaTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/metadatanameschemamissing/component.cue --
package metadatanameschemamissing

import (
	"github.com/kharf/navecd/schema/component"
)

secret: component.#Manifest & {
	content: {
		apiVersion: "v1"
		kind:       "Secret"
		metadata: {
			namespace: "test"
		}
		data: {
			foo: 'bar'
		}
	}
}
`, testtemplates.ModuleVersion)
}

func useMissingMetadataNameTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/metadatanamemissing/component.cue --
package metadatanamemissing

secret: {
	type: "Manifest"
	id:   "unimportant"
	content: {
		apiVersion: "v1"
		kind:       "Secret"
		metadata: {
			namespace: "test"
		}
		data: {
			foo: 'bar'
		}
	}
}
`, testtemplates.ModuleVersion)
}

func useMissingApiVersionTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/apiversionmissing/component.cue --
package apiversionmissing

secret: {
	type: "Manifest"
	id:   "unimportant"
	content: {
		kind: "Secret"
		metadata: {
			name:      "secret"
			namespace: "test"
		}
		data: {
			foo: 'bar'
		}
	}
}
`, testtemplates.ModuleVersion)
}

func useMissingKindTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/kindmissing/component.cue --
package kindmissing

secret: {
	type: "Manifest"
	id:   "unimportant"
	content: {
		apiVersion: "v1"
		metadata: {
			name:      "secret"
			namespace: "test"
		}
		data: {
			foo: 'bar'
		}
	}
}
`, testtemplates.ModuleVersion)
}

func useEmptyReleaseNameWithSchemaTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/emptyreleasenamewithschema/component.cue --
package emptyreleasenamewithschema

import (
	"github.com/kharf/navecd/schema/component"
)

release: component.#HelmRelease & {
	name:      ""
	namespace: "test"
	chart: {
		name:    "test"
		repoURL: "http://test"
		version: "test"
	}
	values: {
		autoscaling: enabled: true
	}
}
`, testtemplates.ModuleVersion)
}

func useEmptyReleaseChartNameWithSchemaTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/emptyreleasechartnamewithschema/component.cue --
package emptyreleasechartnamewithschema

import (
	"github.com/kharf/navecd/schema/component"
)

release: component.#HelmRelease & {
	name:      "test"
	namespace: "test"
	chart: {
		name:    ""
		repoURL: "oci://"
		version: "test"
	}
	values: {
		autoscaling: enabled: true
	}
}
`, testtemplates.ModuleVersion)
}

func useEmptyReleaseChartVersionWithSchemaTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/emptyreleasechartversionwithschema/component.cue --
package emptyreleasechartversionwithschema

import (
	"github.com/kharf/navecd/schema/component"
)

release: component.#HelmRelease & {
	name:      "test"
	namespace: "test"
	chart: {
		name:    "test"
		repoURL: "https://test"
		version: ""
	}
	values: {
		autoscaling: enabled: true
	}
}
`, testtemplates.ModuleVersion)
}

func useWrongPrefixReleaseChartUrlWithSchemaTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/wrongprefixreleasecharturlwithschema/component.cue --
package wrongprefixreleasecharturlwithschema

import (
	"github.com/kharf/navecd/schema/component"
)

release: component.#HelmRelease & {
	name:      "test"
	namespace: "test"
	chart: {
		name:    "test"
		repoURL: "heelloo.com"
		version: "test"
	}
	values: {
		autoscaling: enabled: true
	}
}
`, testtemplates.ModuleVersion)
}

func useConflictingChartAuthTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/conflictingchartauth/component.cue --
package conflictingchartauth

import (
	"github.com/kharf/navecd/schema/component"
	"github.com/kharf/navecd/schema/workloadidentity"
)

#namespace: {
	apiVersion: "v1"
	kind:       "Namespace"
	metadata: {
		name: "prometheus"
	}
}

ns: component.#Manifest & {
	content: #namespace
}

release: component.#HelmRelease & {
	dependencies: [
		ns.id,
	]
	name:      "test-workload-identity"
	namespace: #namespace.metadata.name
	chart: {
		name:    "test"
		repoURL: "oci://test"
		version: "test"
		auth:    workloadidentity.#GCP
		auth: secretRef: {
			name:      "no"
			namespace: "no"
		}
	}
	values: {
		autoscaling: enabled: true
	}
}
`, testtemplates.ModuleVersion)
}

func useAllowCRDsUpgradeTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/allowcrdsupgrade/component.cue --
package allowcrdsupgrade

import (
	"github.com/kharf/navecd/schema/component"
)

release: component.#HelmRelease & {
	name:      "test"
	namespace: "test"
	chart: {
		name:    "test"
		repoURL: "http://test"
		version: "test"
	}
	values: {
		autoscaling: enabled: true
	}
	crds: {
		allowUpgrade: true
	}
}
`, testtemplates.ModuleVersion)
}

func useForceCRDsUpgradeTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/component/build@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/forcecrdsupgrade/component.cue --
package forcecrdsupgrade

import (
	"github.com/kharf/navecd/schema/component"
)

release: component.#HelmRelease & {
	name:      "test"
	namespace: "test"
	chart: {
		name:    "test"
		repoURL: "http://test"
		version: "test"
	}
	values: {
		autoscaling: enabled: true
	}
	crds: {
		forceUpgrade: true
	}
}
`, testtemplates.ModuleVersion)
}

func TestBuilder_Build(t *testing.T) {
	defer goleak.VerifyNone(
		t,
	)

	rootDir := t.TempDir()

	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer registry.Close()

	builder := NewBuilder()
	assert.NilError(t, err)

	testCases := []struct {
		name                string
		packagePath         string
		template            string
		expectedBuildResult *BuildResult
		expectedErr         string
	}{
		{
			name:        "All-Features",
			packagePath: "./infra/success",
			template:    useAllFeaturesTemplate(),
			expectedBuildResult: &BuildResult{
				Instances: []Instance{
					&Manifest{
						ID: "prometheus___Namespace",
						Content: ExtendedUnstructured{
							Unstructured: &unstructured.Unstructured{
								Object: map[string]any{
									"apiVersion": "v1",
									"kind":       "Namespace",
									"metadata": map[string]any{
										"name": "prometheus",
									},
								},
							},
							Metadata: &kube.ManifestMetadata{
								Field: &kube.ManifestFieldMetadata{
									IgnoreInstr: kube.OnConflict,
								},
								Node: map[string]kube.ManifestMetadata{
									"apiVersion": {
										Field: &kube.ManifestFieldMetadata{
											IgnoreInstr: kube.OnConflict,
										},
									},
									"metadata": {
										Node: map[string]kube.ManifestMetadata{
											"name": {
												Field: &kube.ManifestFieldMetadata{
													IgnoreInstr: kube.OnConflict,
												},
											},
										},
									},
								},
							},
						},
						Dependencies: []string{},
					},
					&Manifest{
						ID: "secret_prometheus__Secret",
						Content: ExtendedUnstructured{
							Unstructured: &unstructured.Unstructured{
								Object: map[string]any{
									"apiVersion": "v1",
									"kind":       "Secret",
									"metadata": map[string]any{
										"name":      "secret",
										"namespace": "prometheus",
									},
									"data": map[string]any{
										"foo": []byte("bar"),
									},
								},
							},
							Metadata: &kube.ManifestMetadata{
								Node: map[string]kube.ManifestMetadata{
									"data": {
										Node: map[string]kube.ManifestMetadata{
											"foo": {
												Field: &kube.ManifestFieldMetadata{
													IgnoreInstr: kube.OnConflict,
												},
											},
										},
									},
								},
							},
						},
						Dependencies: []string{"prometheus___Namespace"},
					},
					&Manifest{
						ID: "prometheus_prometheus_apps_Deployment",
						Content: ExtendedUnstructured{
							Unstructured: &unstructured.Unstructured{
								Object: map[string]any{
									"apiVersion": "apps/v1",
									"kind":       "Deployment",
									"metadata": map[string]any{
										"name":      "prometheus",
										"namespace": "prometheus",
									},
									"spec": map[string]any{
										"replicas": int64(1),
										"selector": map[string]any{
											"matchLabels": map[string]any{
												"app": "prometheus",
											},
										},
										"template": map[string]any{
											"metadata": map[string]any{
												"labels": map[string]any{
													"app": "prometheus",
												},
											},
											"spec": map[string]any{
												"containers": []any{
													map[string]any{
														"name":  "prometheus",
														"image": "prometheus:1.14.2",
														"ports": []any{
															map[string]any{
																"containerPort": int64(
																	80,
																),
															},
														},
													},
													map[string]any{
														"name":  "sidecar",
														"image": "sidecar:1.14.2",
														"ports": []any{
															map[string]any{
																"containerPort": int64(
																	80,
																),
															},
														},
													},
													map[string]any{
														"name":  "sidecar2",
														"image": "sidecar2:1.14.2",
														"ports": []any{
															map[string]any{
																"containerPort": int64(
																	80,
																),
															},
														},
													},
												},
											},
										},
									},
								},
							},
							Metadata: &kube.ManifestMetadata{
								Node: map[string]kube.ManifestMetadata{
									"spec": {
										Node: map[string]kube.ManifestMetadata{
											"replicas": {
												Field: &kube.ManifestFieldMetadata{
													IgnoreInstr: kube.OnConflict,
												},
											},
											"template": {
												Node: map[string]kube.ManifestMetadata{
													"spec": {
														Node: map[string]kube.ManifestMetadata{
															"containers": {
																Field: &kube.ManifestFieldMetadata{
																	IgnoreInstr: kube.OnConflict,
																},
																List: []kube.ManifestMetadata{
																	{
																		Node: map[string]kube.ManifestMetadata{
																			"image": {
																				Field: &kube.ManifestFieldMetadata{
																					IgnoreInstr: kube.OnConflict,
																				},
																			},
																		},
																	},
																	{
																		Node: map[string]kube.ManifestMetadata{
																			"image": {
																				Field: &kube.ManifestFieldMetadata{
																					IgnoreInstr: kube.OnConflict,
																				},
																			},
																		},
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
						Dependencies: []string{"prometheus___Namespace"},
					},
					&Manifest{
						ID: "prometheus_prometheus_rbac.authorization.k8s.io_Role",
						Content: ExtendedUnstructured{
							Unstructured: &unstructured.Unstructured{
								Object: map[string]any{
									"apiVersion": "rbac.authorization.k8s.io/v1",
									"kind":       "Role",
									"metadata": map[string]any{
										"name":      "prometheus",
										"namespace": "prometheus",
									},
									"rules": []any{
										map[string]any{
											"apiGroups": []any{"coordination.k8s.io"},
											"resources": []any{"leases"},
											"verbs": []any{
												"get",
												"create",
												"update",
											},
										},
										map[string]any{
											"apiGroups": []any{""},
											"resources": []any{"events"},
											"verbs": []any{
												"create",
												"patch",
											},
										},
									},
								},
							},
						},
						Dependencies: []string{"prometheus___Namespace"},
					},
					&helm.ReleaseComponent{
						ID: "test_prometheus_HelmRelease",
						Content: helm.ReleaseDeclaration{
							Name:      "test",
							Namespace: "prometheus",
							Chart: &helm.Chart{
								Name:    "test",
								RepoURL: "oci://test",
								Version: "4.9.9",
							},
							Values: helm.Values{
								"autoscaling": map[string]interface{}{
									"enabled": true,
								},
							},
							CRDs: helm.CRDs{
								AllowUpgrade: false,
							},
							Patches: &helm.Patches{
								Unstructureds: map[string]kube.ExtendedUnstructured{
									"apps/v1-Deployment-prometheus-test": {
										Unstructured: &unstructured.Unstructured{
											Object: map[string]interface{}{
												"apiVersion": "apps/v1",
												"kind":       "Deployment",
												"metadata": map[string]any{
													"name":      "test",
													"namespace": "prometheus",
												},
												"spec": map[string]any{
													"replicas": int64(1),
												},
											},
										},
										Metadata: &kube.ManifestMetadata{
											Node: map[string]kube.ManifestMetadata{
												"spec": {
													Node: map[string]kube.ManifestMetadata{
														"replicas": {
															Field: &kube.ManifestFieldMetadata{
																IgnoreInstr: kube.OnConflict,
															},
														},
													},
												},
											},
										},
									},
									"apps/v1-Deployment-prometheus-hello": {
										Unstructured: &unstructured.Unstructured{
											Object: map[string]interface{}{
												"apiVersion": "apps/v1",
												"kind":       "Deployment",
												"metadata": map[string]any{
													"name":      "hello",
													"namespace": "prometheus",
												},
												"spec": map[string]any{
													"replicas": int64(2),
													"template": map[string]any{
														"spec": map[string]any{
															"containers": []any{
																map[string]any{
																	"name":  "prometheus",
																	"image": "prometheus:1.14.2",
																	"ports": []any{
																		map[string]any{
																			"containerPort": int64(
																				80,
																			),
																		},
																	},
																},
																map[string]any{
																	"name":  "sidecar",
																	"image": "sidecar:1.14.2",
																	"ports": []any{
																		map[string]any{
																			"containerPort": int64(
																				80,
																			),
																		},
																	},
																},
															},
														},
													},
												},
											},
										},
										Metadata: &kube.ManifestMetadata{
											Node: map[string]kube.ManifestMetadata{
												"spec": {
													Node: map[string]kube.ManifestMetadata{
														"replicas": {
															Field: &kube.ManifestFieldMetadata{
																IgnoreInstr: kube.OnConflict,
															},
														},
														"template": {
															Node: map[string]kube.ManifestMetadata{
																"spec": {
																	Node: map[string]kube.ManifestMetadata{
																		"containers": {
																			Field: &kube.ManifestFieldMetadata{
																				IgnoreInstr: kube.OnConflict,
																			},
																			List: []kube.ManifestMetadata{
																				{
																					Node: map[string]kube.ManifestMetadata{
																						"image": {
																							Field: &kube.ManifestFieldMetadata{
																								IgnoreInstr: kube.OnConflict,
																							},
																						},
																					},
																				},
																			},
																		},
																	},
																},
															},
														},
													},
												},
											},
										},
									},
								},
							},
						},
						Dependencies: []string{"prometheus___Namespace"},
					},
					&helm.ReleaseComponent{
						ID: "test-secret-ref_prometheus_HelmRelease",
						Content: helm.ReleaseDeclaration{
							Name:      "test-secret-ref",
							Namespace: "prometheus",
							Chart: &helm.Chart{
								Name:    "test",
								RepoURL: "oci://test",
								Version: "test",
								Auth: &cloud.Auth{
									SecretRef: &cloud.SecretRef{
										Name: "secret",
									},
								},
							},
							Values: helm.Values{},
						},
						Dependencies: []string{"prometheus___Namespace"},
					},
					&helm.ReleaseComponent{
						ID: "test-workload-identity_prometheus_HelmRelease",
						Content: helm.ReleaseDeclaration{
							Name:      "test-workload-identity",
							Namespace: "prometheus",
							Chart: &helm.Chart{
								Name:    "test",
								RepoURL: "oci://test",
								Version: "test",
								Auth: &cloud.Auth{
									WorkloadIdentity: &cloud.WorkloadIdentity{
										Provider: "gcp",
									},
								},
							},
							Values: helm.Values{
								"autoscaling": map[string]interface{}{
									"enabled": true,
								},
							},
						},
						Dependencies: []string{"prometheus___Namespace"},
					},
					&Manifest{
						ID: "gitopsprojects.gitops.navecd.io__apiextensions.k8s.io_CustomResourceDefinition",
						Content: ExtendedUnstructured{
							Unstructured: &unstructured.Unstructured{
								Object: map[string]any{
									"apiVersion": "apiextensions.k8s.io/v1",
									"kind":       "CustomResourceDefinition",
									"metadata": map[string]any{
										"annotations": map[string]any{
											"controller-gen.kubebuilder.io/version": "v0.15.0",
										},
										"name": "gitopsprojects.gitops.navecd.io",
									},
									"spec": map[string]any{
										"group": "gitops.navecd.io",
										"names": map[string]any{
											"kind":     "GitOpsProject",
											"listKind": "GitOpsProjectList",
											"plural":   "gitopsprojects",
											"singular": "gitopsproject",
										},
										"scope": "Namespaced",
										"versions": []any{
											map[string]any{
												"name": "v1beta1",
												"schema": map[string]any{
													"openAPIV3Schema": map[string]any{
														"description": "GitOpsProject is the Schema for the gitopsprojects API",
														"properties": map[string]any{
															"apiVersion": map[string]any{
																"description": string(
																	`APIVersion defines the versioned schema of this representation of an object.
Servers should convert recognized schemas to the latest internal value, and
may reject unrecognized values.
More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources`,
																),
																"type": "string",
															},
															"kind": map[string]any{
																"description": string(
																	`Kind is a string value representing the REST resource this object represents.
Servers may infer this from the endpoint the client submits requests to.
Cannot be updated.
In CamelCase.
More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds`,
																),
																"type": "string",
															},
															"metadata": map[string]any{
																"type": "object",
															},
															"spec": map[string]any{
																"description": "GitOpsProjectSpec defines the desired state of GitOpsProject",
																"properties": map[string]any{
																	"branch": map[string]any{
																		"description": "The branch of the gitops repository holding the navecd configuration.",
																		"minLength":   int64(1),
																		"type":        "string",
																	},
																	"pullIntervalSeconds": map[string]any{
																		"description": "This defines how often navecd will try to fetch changes from the gitops repository.",
																		"minimum":     int64(5),
																		"type":        "integer",
																	},
																	"serviceAccountName": map[string]any{
																		"type": "string",
																	},
																	"suspend": map[string]any{
																		"description": "This flag tells the controller to suspend subsequent executions, it does\nnot apply to already started executions.  Defaults to false.",
																		"type":        "boolean",
																	},
																	"url": map[string]any{
																		"description": "The url to the gitops repository.",
																		"minLength":   int64(1),
																		"type":        "string",
																	},
																},
																"required": []any{
																	"branch", "pullIntervalSeconds", "url",
																},
																"type": "object",
															},
															"status": map[string]any{
																"description": "GitOpsProjectStatus defines the observed state of GitOpsProject",
																"properties": map[string]any{
																	"conditions": map[string]any{
																		"items": map[string]any{
																			"description": "",
																			"properties": map[string]any{
																				"lastTransitionTime": map[string]any{
																					"description": "lastTransitionTime is the last time the condition transitioned from one status to another.\nThis should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.",
																					"format":      "date-time",
																					"type":        "string",
																				},
																				"message": map[string]any{
																					"description": "message is a human readable message indicating details about the transition.\nThis may be an empty string.",
																					"maxLength": int64(
																						32768,
																					),
																					"type": "string",
																				},
																				"observedGeneration": map[string]any{
																					"description": "observedGeneration represents the .metadata.generation that the condition was set based upon.\nFor instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date\nwith respect to the current state of the instance.",
																					"format":      "int64",
																					"minimum": int64(
																						0,
																					),
																					"type": "integer",
																				},
																				"reason": map[string]any{
																					"description": string(
																						`reason contains a programmatic identifier indicating the reason for the condition's last transition.
Producers of specific condition types may define expected values and meanings for this field,
and whether the values are considered a guaranteed API.
The value should be a CamelCase string.
This field may not be empty.`,
																					),
																					"maxLength": int64(
																						1024,
																					),
																					"minLength": int64(
																						1,
																					),
																					"pattern": "^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$",
																					"type":    "string",
																				},
																				"status": map[string]any{
																					"description": "status of the condition, one of True, False, Unknown.",
																					"enum": []any{
																						"True",
																						"False",
																						"Unknown",
																					},
																					"type": "string",
																				},
																				"type": map[string]any{
																					"description": "",
																					"maxLength": int64(
																						316,
																					),
																					"pattern": "^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])$",
																					"type":    "string",
																				},
																			},
																			"required": []any{
																				"lastTransitionTime",
																				"message",
																				"reason",
																				"status",
																				"type",
																			},
																			"type": "object",
																		},
																		"type": "array",
																	},
																	"revision": map[string]any{
																		"properties": map[string]any{
																			"commitHash": map[string]any{
																				"type": "string",
																			},
																			"reconcileTime": map[string]any{
																				"format": "date-time",
																				"type":   "string",
																			},
																		},
																		"type": "object",
																	},
																},
																"type": "object",
															},
														},
														"type": "object",
													},
												},
												"served":  true,
												"storage": true,
												"subresources": map[string]any{
													"status": map[string]any{},
												},
											},
										},
									},
								},
							},
						},
						Dependencies: []string{},
					},
				},
			},
			expectedErr: "",
		},
		{
			name:        "Content-Wrong-Field-Type",
			packagePath: "./infra/contentwrongtype",
			template:    useContentWrongTypeTemplate(),
			expectedErr: "CUE Build Error: expected content to be of type struct",
		},
		{
			name:        "Patches-Wrong-Field-Type",
			packagePath: "./infra/patcheswrongtype",
			template:    usePatchesWrongTypeTemplate(),
			expectedErr: "CUE Build Error: expected patches content to be of type struct",
		},
		{
			name:        "Missing-ID",
			packagePath: "./infra/idmissing",
			template:    useMissingIDTemplate(),
			expectedErr: "secret: field not found: id",
		},
		{
			name:        "Missing-Metadata",
			packagePath: "./infra/metadatamissing",
			template:    useMissingMetadataTemplate(),
			expectedErr: ErrMissingField.Error(),
		},
		{
			name:        "Missing-Metadata-Name-With-Schema",
			packagePath: "./infra/metadatanameschemamissing",
			template:    useMissingMetadataNameWithSchemaTemplate(),
			expectedErr: "CUE Build Error: secret.id: invalid interpolation: non-concrete value strings.MinRunes(1) (type string)",
		},
		{
			name:        "Missing-Metadata-Name",
			packagePath: "./infra/metadatanamemissing",
			template:    useMissingMetadataNameTemplate(),
			expectedErr: ErrMissingField.Error(),
		},
		{
			name:        "Missing-ApiVersion",
			packagePath: "./infra/apiversionmissing",
			template:    useMissingApiVersionTemplate(),
			expectedErr: ErrMissingField.Error(),
		},
		{
			name:        "Missing-Kind",
			packagePath: "./infra/kindmissing",
			template:    useMissingKindTemplate(),
			expectedErr: ErrMissingField.Error(),
		},
		{
			name:        "Empty-Release-Name-With-Schema",
			packagePath: "./infra/emptyreleasenamewithschema",
			template:    useEmptyReleaseNameWithSchemaTemplate(),
			expectedErr: "release.name: invalid value \"\" (does not satisfy strings.MinRunes(1))",
		},
		{
			name:        "Empty-Release-Chart-Name-With-Schema",
			packagePath: "./infra/emptyreleasechartnamewithschema",
			template:    useEmptyReleaseChartNameWithSchemaTemplate(),
			expectedErr: "release.chart.name: invalid value \"\" (does not satisfy strings.MinRunes(1))",
		},
		{
			name:        "Empty-Release-Chart-Version-With-Schema",
			packagePath: "./infra/emptyreleasechartversionwithschema",
			template:    useEmptyReleaseChartVersionWithSchemaTemplate(),
			expectedErr: "release.chart.version: invalid value \"\" (does not satisfy strings.MinRunes(1))",
		},
		{
			name:        "Wrong-Prefix-Release-Chart-Url-With-Schema",
			packagePath: "./infra/wrongprefixreleasecharturlwithschema",
			template:    useWrongPrefixReleaseChartUrlWithSchemaTemplate(),
			expectedErr: "CUE Build Error: release.chart.repoURL: 3 errors in empty disjunction:\nrelease.chart.repoURL: invalid value \"heelloo.com\" (does not satisfy strings.HasPrefix(\"oci://\")):",
		},
		{
			name:        "Conflicting-Chart-Auth",
			packagePath: "./infra/conflictingchartauth",
			template:    useConflictingChartAuthTemplate(),
			expectedErr: "CUE Build Error: release.chart.auth: 2 errors in empty disjunction:\nrelease.chart.auth.secretRef: field not allowed:",
		},
		{
			name:        "Allow-CRDs-Upgrade",
			packagePath: "./infra/allowcrdsupgrade",
			template:    useAllowCRDsUpgradeTemplate(),
			expectedBuildResult: &BuildResult{
				Instances: []Instance{
					&helm.ReleaseComponent{
						ID: "test_test_HelmRelease",
						Content: helm.ReleaseDeclaration{
							Name:      "test",
							Namespace: "test",
							Chart: &helm.Chart{
								Name:    "test",
								RepoURL: "http://test",
								Version: "test",
							},
							Values: helm.Values{
								"autoscaling": map[string]interface{}{
									"enabled": true,
								},
							},
							CRDs: helm.CRDs{
								AllowUpgrade: true,
							},
						},
						Dependencies: []string{},
					},
				},
			},
			expectedErr: "",
		},
		{
			name:        "Force-CRDs-Upgrade",
			packagePath: "./infra/forcecrdsupgrade",
			template:    useForceCRDsUpgradeTemplate(),
			expectedBuildResult: &BuildResult{
				Instances: []Instance{
					&helm.ReleaseComponent{
						ID: "test_test_HelmRelease",
						Content: helm.ReleaseDeclaration{
							Name:      "test",
							Namespace: "test",
							Chart: &helm.Chart{
								Name:    "test",
								RepoURL: "http://test",
								Version: "test",
							},
							Values: helm.Values{
								"autoscaling": map[string]any{
									"enabled": true,
								},
							},
							CRDs: helm.CRDs{
								ForceUpgrade: true,
							},
						},
						Dependencies: []string{},
					},
				},
			},
			expectedErr: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := txtar.Create(rootDir, strings.NewReader(tc.template))
			assert.NilError(t, err)

			buildResult, err := builder.Build(
				WithProjectRoot(rootDir),
				WithPackagePath(tc.packagePath),
			)

			if tc.expectedErr != "" {
				assert.ErrorContains(t, err, tc.expectedErr)
			} else {
				assert.NilError(t, err)
				assert.Assert(t, buildResult != nil)

				assert.DeepEqual(t, buildResult.Instances, tc.expectedBuildResult.Instances)
			}
		})
	}
}
