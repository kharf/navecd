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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"cuelabs.dev/go/oci/ociregistry"
	"github.com/go-co-op/gocron/v2"
	"github.com/go-git/go-git/v5"
	gitops "github.com/kharf/navecd/api/v1beta1"
	"github.com/kharf/navecd/internal/cloudtest"
	"github.com/kharf/navecd/internal/dnstest"
	"github.com/kharf/navecd/internal/helmtest"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/internal/ocitest"
	"github.com/kharf/navecd/internal/projecttest"
	"github.com/kharf/navecd/internal/testtemplates"
	"github.com/kharf/navecd/pkg/cloud"
	"github.com/kharf/navecd/pkg/component"
	"github.com/kharf/navecd/pkg/helm"
	"github.com/kharf/navecd/pkg/inventory"
	"github.com/kharf/navecd/pkg/kube"
	"github.com/kharf/navecd/pkg/project"
	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/errgroup"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
	"gotest.tools/v3/poll"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

type broadProjectTemplate struct {
	template   string
	data       broadTemplateData
	containers []string
}

func (tmpl *broadProjectTemplate) Template() string {
	return tmpl.template
}

func (tmpl *broadProjectTemplate) Data() any {
	return tmpl.data
}

func (tmpl *broadProjectTemplate) Containers() []string {
	return tmpl.containers
}

var _ testtemplates.Template = (*broadProjectTemplate)(nil)

type broadTemplateData struct {
	Name              string
	HelmRepoURL       string
	ContainerRegistry string
}

func useBroadTemplate(data broadTemplateData) *broadProjectTemplate {
	return &broadProjectTemplate{
		template: fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/projecttest/broad@v0"
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

#namespace: {
	apiVersion: "v1"
	kind:       "Namespace"
	metadata: name: "toolb"
}

-- infra/toolb/secret.cue --
package toolb

_secret: {
	apiVersion: "v1"
	kind:       "Secret"
	metadata: {
		name:      "secret"
		namespace: #namespace.metadata.name
	}
	data: {
		foo: 'bar'
	}
}

-- infra/toolb/releases.cue --
package toolb

import (
	"github.com/kharf/navecd/schema/component"
	"github.com/kharf/navecd/internal/projecttest/broad/infra/toola"
)

release: component.#HelmRelease & {
	dependencies: [
		ns.id,
		toola.ns.id,
	]
	name:      "{{.Name}}"
	namespace: #namespace.metadata.name
	chart: {
		name:    "test"
		repoURL: "{{.HelmRepoURL}}"
		version: "1.0.0"
	} @update(constraint="<=2.0.0", integration=direct, schedule="* * * * * *")

	crds: {
		allowUpgrade: true
	}

	values: {
		autoscaling: enabled: true
	}

	patches: [
		{
			apiVersion: "apps/v1"
			kind:       "Deployment"
			metadata: {
				name:      "{{.Name}}"
				namespace: #namespace.metadata.name
			}
			spec: {
				replicas: 5 @ignore(conflict)
				template: {
					spec: {
						containers: [
							{
								name:  "toolb"
								image: "{{.ContainerRegistry}}/toolb:1.14.2" @update(constraint="*", integration=direct, schedule="* * * * * *")
								ports: [{
									containerPort: 80
								}]
							},
							{
								name:  "sidecar"
								image: "{{.ContainerRegistry}}/sidecar:1.14.2" @ignore(conflict) // attributes in lists are not supported
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
}

-- infra/toolb/component.cue --
package toolb

import (
	"github.com/kharf/navecd/schema/component"
)

ns: component.#Manifest & {
	content: #namespace
}

secret: component.#Manifest & {
	dependencies: [
		ns.id,
	]
	content: _secret
}

-- infra/toolb/subtool/component.cue --
package subtool

import (
	"github.com/kharf/navecd/schema/component"
	"github.com/kharf/navecd/internal/projecttest/broad/infra/toolb"
)

deployment: component.#Manifest & {
	dependencies: [
		toolb.ns.id,
	]
	content: _deployment
}

anotherDeployment: component.#Manifest & {
	dependencies: [
		toolb.ns.id,
	]
	content: _anotherDeployment
}

-- infra/toolb/subtool/deployment.cue --
package subtool

import (
	"github.com/kharf/navecd/internal/projecttest/broad/infra/toolb"
)

_deployment: {
	apiVersion: "apps/v1"
	kind:       "Deployment"
	metadata: {
		name:      "mysubcomponent"
		namespace: toolb.#namespace.metadata.name
	}
	spec: {
		replicas: 1
		selector: matchLabels: app: _deployment.metadata.name
		template: {
			metadata: labels: app: _deployment.metadata.name
			spec: {
				securityContext: {
					runAsNonRoot:        true
					fsGroup:             65532
					fsGroupChangePolicy: "OnRootMismatch"
				}
				containers: [
					{
						name:  "containerone"
						image: "{{.ContainerRegistry}}/containerone:1.14.2" @update(strategy=semver, constraint="1.2.x", integration=direct, schedule="* * * * * *")
						ports: [{
							name:          "http"
							containerPort: 80
						}]
					},
					{
						name:  "containertwo"
						image: "{{.ContainerRegistry}}/containertwo:1.14.2" @update(strategy=semver, constraint="<=1.16", integration=direct, schedule="* * * * * *")
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

_anotherDeployment: {
	apiVersion: "apps/v1"
	kind:       "Deployment"
	metadata: {
		name:      "anothersubcomponent"
		namespace: toolb.#namespace.metadata.name
	}
	spec: {
		replicas: 1 @ignore(conflict)
		selector: matchLabels: app: _anotherDeployment.metadata.name
		template: {
			metadata: labels: app: _anotherDeployment.metadata.name
			spec: {
				securityContext: {
					runAsNonRoot:        true  @ignore(conflict)
					fsGroup:             65532 @ignore(conflict)
					fsGroupChangePolicy: "OnRootMismatch"
				}
				containers: [
					{
						name:  "subcomponent"
						image: "{{.ContainerRegistry}}/subcomponent:1.14.2"
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
`, testtemplates.ModuleVersion),
		data: data,
		containers: []string{
			"containerone",
			"containertwo",
			"toolb",
			"sidecar",
			"subcomponent",
		},
	}
}

func TestReconciler_Reconcile(t *testing.T) {
	ctx := context.Background()
	var err error
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registryPath := t.TempDir()

	cueModuleRegistry, err := ocitest.StartCUERegistry(registryPath)
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(false),
		helmtest.WithPrivate(false),
	)
	assert.NilError(t, err)
	defer publicHelmEnvironment.Close()

	tlsRegistry, err := ocitest.NewTLSRegistry(false, "")
	assert.NilError(t, err)
	defer tlsRegistry.Close()

	broadTemplate := useBroadTemplate(
		broadTemplateData{
			Name:              "test",
			HelmRepoURL:       publicHelmEnvironment.ChartServer.URL(),
			ContainerRegistry: tlsRegistry.Addr(),
		},
	)
	for _, container := range broadTemplate.Containers() {
		manifest := ociregistry.Manifest{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Annotations: map[string]string{
				"org.opencontainers.image.url": "test",
			},
			Config: ociregistry.Descriptor{
				Digest: digest.FromString(""),
			},
		}

		bytes, err := json.Marshal(&manifest)
		assert.NilError(t, err)
		desc, err := tlsRegistry.PushManifest(
			ctx,
			container,
			"1.15.3",
			bytes,
			"application/vnd.docker.distribution.manifest.v2+json",
		)
		assert.NilError(t, err)
		defer tlsRegistry.DeleteManifest(ctx, container, desc.Digest)
	}

	parsedTemplate, err := testtemplates.Parse(broadTemplate)
	assert.NilError(t, err)
	env := projecttest.InitTestEnvironment(
		t,
		parsedTemplate,
	)

	kubernetes := kubetest.StartKubetestEnv(t, env.Log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()
	projectManager := project.NewManager(component.NewBuilder(), -1)

	scheduler, err := gocron.NewScheduler()
	assert.NilError(t, err)
	scheduler.Start()
	quitChan := make(chan struct{}, 1)
	schedulerEg := &errgroup.Group{}
	schedulerEg.SetLimit(1)
	defer func() {
		quitChan <- struct{}{}
		_ = scheduler.Shutdown()
	}()

	reconciler := project.Reconciler{
		KubeConfig:            kubernetes.ControlPlane.Config,
		ComponentBuilder:      component.NewBuilder(),
		RepositoryManager:     kubernetes.RepositoryManager,
		ProjectManager:        projectManager,
		Log:                   env.Log,
		FieldManager:          "controller",
		WorkerPoolSize:        -1,
		InsecureSkipTLSverify: true,
		CacheDir:              env.TestRoot,
		Scheduler:             scheduler,
		SchedulerQuitChan:     quitChan,
		SchedulerErrGroup:     schedulerEg,
	}

	suspend := false
	gProject := gitops.GitOpsProject{
		TypeMeta: v1.TypeMeta{
			APIVersion: "gitops.navecd.io/v1",
			Kind:       "GitOpsProject",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			UID:       types.UID(env.TestRoot),
		},
		Spec: gitops.GitOpsProjectSpec{
			URL:                 env.TestProject,
			Branch:              "main",
			PullIntervalSeconds: 5,
			Suspend:             &suspend,
		},
	}

	inventoryInstance := &inventory.Instance{
		// /inventory is mounted as volume.
		Path: filepath.Join("/inventory", string(gProject.GetUID())),
	}

	result, err := reconciler.Reconcile(ctx, gProject)
	assert.NilError(t, err)
	assert.Equal(t, result.Suspended, false)

	ns := "toolb"
	var mysubcomponent appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "mysubcomponent", Namespace: ns},
		&mysubcomponent,
	)

	assert.NilError(t, err)
	assert.Equal(t, mysubcomponent.Name, "mysubcomponent")
	assert.Equal(t, mysubcomponent.Namespace, ns)
	subcomponentContainers := mysubcomponent.Spec.Template.Spec.Containers
	assert.Assert(t, len(subcomponentContainers) == 2)
	assert.Assert(
		t,
		slices.ContainsFunc(subcomponentContainers, func(container corev1.Container) bool {
			return container.Name == "containertwo" &&
				container.Image == fmt.Sprintf("%s/containertwo:1.14.2", tlsRegistry.Addr())
		}),
	)

	var dep appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "test", Namespace: ns},
		&dep,
	)
	assert.NilError(t, err)
	assert.Equal(t, dep.Name, "test")
	assert.Equal(t, dep.Namespace, ns)

	var sec corev1.Secret
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "secret", Namespace: ns},
		&sec,
	)
	assert.NilError(t, err)
	fooSecretValue, found := sec.Data["foo"]
	assert.Assert(t, found)
	assert.Equal(t, string(fooSecretValue), "bar")

	inventoryStorage, err := inventoryInstance.Load()
	assert.NilError(t, err)

	invComponents := inventoryStorage.Items()
	assert.Assert(t, len(invComponents) == 6)
	testHR := &inventory.HelmReleaseItem{
		Name:      dep.Name,
		Namespace: dep.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", dep.Name, dep.Namespace),
	}
	assert.Assert(t, inventoryStorage.HasItem(testHR))

	contentReader, err := inventoryInstance.GetItem(testHR)
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredRelease := helm.Release{
		Name:      testHR.Name,
		Namespace: testHR.Namespace,
		CRDs: helm.CRDs{
			AllowUpgrade: true,
		},
		Chart: &helm.Chart{
			Name:    "test",
			RepoURL: publicHelmEnvironment.ChartServer.URL(),
			Version: "1.0.0",
			Auth:    nil,
		},
		Values: helm.Values{
			"autoscaling": map[string]interface{}{
				"enabled": true,
			},
		},
		Patches: &helm.Patches{
			Unstructureds: map[string]kube.ExtendedUnstructured{
				"apps/v1-Deployment-toolb-test": {
					Unstructured: &unstructured.Unstructured{
						Object: map[string]interface{}{
							"apiVersion": "apps/v1",
							"kind":       "Deployment",
							"metadata": map[string]any{
								"name":      testHR.Name,
								"namespace": testHR.Namespace,
							},
							"spec": map[string]any{
								"replicas": int64(5),
								"template": map[string]any{
									"spec": map[string]any{
										"containers": []any{
											map[string]any{
												"name": "toolb",
												"image": fmt.Sprintf(
													"%s/toolb:1.14.2",
													tlsRegistry.Addr(),
												),
												"ports": []any{
													map[string]any{
														"containerPort": int64(
															80,
														),
													},
												},
											},
											map[string]any{
												"name": "sidecar",
												"image": fmt.Sprintf(
													"%s/sidecar:1.14.2",
													tlsRegistry.Addr(),
												),
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
	}

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(desiredRelease)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())

	invNs := &inventory.ManifestItem{
		TypeMeta: v1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		Name:      mysubcomponent.Namespace,
		Namespace: "",
		ID:        fmt.Sprintf("%s___Namespace", mysubcomponent.Namespace),
	}
	assert.Assert(t, inventoryStorage.HasItem(invNs))

	subComponentDeploymentManifest := &inventory.ManifestItem{
		TypeMeta: v1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		Name:      mysubcomponent.Name,
		Namespace: mysubcomponent.Namespace,
		ID: fmt.Sprintf(
			"%s_%s_apps_Deployment",
			mysubcomponent.Name,
			mysubcomponent.Namespace,
		),
	}
	assert.Assert(t, inventoryStorage.HasItem(subComponentDeploymentManifest))

	check := func(t poll.LogT) poll.Result {
		_, err = reconciler.Reconcile(ctx, gProject)
		if err != nil {
			return poll.Error(err)
		}

		var mysubcomponent appsv1.Deployment
		err = kubernetes.TestKubeClient.Get(
			ctx,
			types.NamespacedName{Name: "mysubcomponent", Namespace: ns},
			&mysubcomponent,
		)

		if err != nil {
			return poll.Error(err)
		}

		subcomponentContainers := mysubcomponent.Spec.Template.Spec.Containers
		return poll.Compare(cmp.Equal(
			slices.ContainsFunc(subcomponentContainers, func(container corev1.Container) bool {
				return container.Name == "containertwo" &&
					container.Image == fmt.Sprintf("%s/containertwo:1.15.3", tlsRegistry.Addr())
			}), true,
		))
	}

	poll.WaitOn(t, check, poll.WithDelay(1*time.Second))

	err = env.GitRepository.Worktree.Pull(&git.PullOptions{Force: true})
	assert.NilError(t, err)
	err = os.RemoveAll(
		filepath.Join(env.LocalTestProject, "infra", "toolb", "subtool"),
	)
	assert.NilError(t, err)
	_, err = env.GitRepository.Worktree.Add(".")
	assert.NilError(t, err)
	_, err = env.GitRepository.CommitFile(
		".",
		"undeploy subcomponent",
	)
	assert.NilError(t, err)
	_, err = reconciler.Reconcile(ctx, gProject)
	assert.NilError(t, err)

	inventoryStorage, err = inventoryInstance.Load()
	assert.NilError(t, err)

	invComponents = inventoryStorage.Items()
	assert.Assert(t, len(invComponents) == 4)
	assert.Assert(t, !inventoryStorage.HasItem(subComponentDeploymentManifest))
	assert.Assert(t, inventoryStorage.HasItem(invNs))
	assert.Assert(t, inventoryStorage.HasItem(testHR))
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "mysubcomponent", Namespace: ns},
		&mysubcomponent,
	)
	assert.Error(t, err, "deployments.apps \"mysubcomponent\" not found")
}

func useMiniTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/projecttest/mini@v0"
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
`, testtemplates.ModuleVersion)
}

func TestReconciler_Reconcile_Impersonation(t *testing.T) {
	ctx := context.Background()
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registryPath := t.TempDir()

	cueModuleRegistry, err := ocitest.StartCUERegistry(registryPath)
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	env := projecttest.InitTestEnvironment(
		t,
		[]byte(useMiniTemplate()),
	)

	kubernetes := kubetest.StartKubetestEnv(t, env.Log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()
	projectManager := project.NewManager(component.NewBuilder(), -1)

	scheduler, err := gocron.NewScheduler()
	assert.NilError(t, err)
	quitChan := make(chan struct{}, 1)
	schedulerEg := &errgroup.Group{}
	schedulerEg.SetLimit(1)
	defer func() {
		quitChan <- struct{}{}
		_ = scheduler.Shutdown()
	}()

	reconciler := project.Reconciler{
		KubeConfig:            kubernetes.ControlPlane.Config,
		ComponentBuilder:      component.NewBuilder(),
		RepositoryManager:     kubernetes.RepositoryManager,
		ProjectManager:        projectManager,
		Log:                   env.Log,
		FieldManager:          "controller",
		WorkerPoolSize:        -1,
		InsecureSkipTLSverify: true,
		CacheDir:              env.TestRoot,
		Scheduler:             scheduler,
		SchedulerQuitChan:     quitChan,
		SchedulerErrGroup:     schedulerEg,
	}

	suspend := false
	gProject := gitops.GitOpsProject{
		TypeMeta: v1.TypeMeta{
			APIVersion: "gitops.navecd.io/v1",
			Kind:       "GitOpsProject",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      "test",
			Namespace: "tenant",
			UID:       types.UID(env.TestRoot),
		},
		Spec: gitops.GitOpsProjectSpec{
			ServiceAccountName:  "mysa",
			URL:                 env.TestProject,
			Branch:              "main",
			PullIntervalSeconds: 5,
			Suspend:             &suspend,
		},
	}

	result, err := reconciler.Reconcile(ctx, gProject)
	assert.NilError(t, err)
	assert.NilError(t, result.PullError)
	assert.Assert(t, result.ComponentError != nil)
	assert.ErrorContains(
		t,
		result.ComponentError,
		`is forbidden: User "system:serviceaccount:tenant:mysa" cannot get resource`,
	)

	namespace := corev1.Namespace{
		TypeMeta: v1.TypeMeta{
			APIVersion: "",
			Kind:       "Namespace",
		},
		ObjectMeta: v1.ObjectMeta{
			Name: "tenant",
		},
	}

	err = kubernetes.TestKubeClient.Create(ctx, &namespace)
	assert.NilError(t, err)

	namespace = corev1.Namespace{
		TypeMeta: v1.TypeMeta{
			APIVersion: "",
			Kind:       "Namespace",
		},
		ObjectMeta: v1.ObjectMeta{
			Name: "monitoring",
		},
	}

	err = kubernetes.TestKubeClient.Create(ctx, &namespace)
	assert.NilError(t, err)

	serviceAccount := corev1.ServiceAccount{
		TypeMeta: v1.TypeMeta{
			APIVersion: "",
			Kind:       "ServiceAccount",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      "mysa",
			Namespace: "tenant",
		},
	}

	err = kubernetes.TestKubeClient.Create(ctx, &serviceAccount)
	assert.NilError(t, err)

	role := rbacv1.ClusterRole{
		TypeMeta: v1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRole",
		},
		ObjectMeta: v1.ObjectMeta{
			Name: "imp",
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:     []string{"*"},
				Resources: []string{"*"},
				APIGroups: []string{"*"},
			},
		},
	}

	err = kubernetes.TestKubeClient.Create(ctx, &role)
	assert.NilError(t, err)

	roleBinding := rbacv1.ClusterRoleBinding{
		TypeMeta: v1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRoleBinding",
		},
		ObjectMeta: v1.ObjectMeta{
			Name: "imp",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "mysa",
				Namespace: "tenant",
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "imp",
		},
	}

	err = kubernetes.TestKubeClient.Create(ctx, &roleBinding)
	assert.NilError(t, err)

	result, err = reconciler.Reconcile(ctx, gProject)
	assert.NilError(t, err)
	assert.Equal(t, result.Suspended, false)

	nsName := "toola"

	var ns corev1.Namespace
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: nsName},
		&ns,
	)
	assert.NilError(t, err)
	assert.Equal(t, ns.Name, nsName)

	inventoryInstance := &inventory.Instance{
		// /inventory is mounted as volume.
		Path: filepath.Join("/inventory", string(gProject.GetUID())),
	}
	inventoryStorage, err := inventoryInstance.Load()
	assert.NilError(t, err)

	invComponents := inventoryStorage.Items()
	assert.Assert(t, len(invComponents) == 1)
	nsManifest := &inventory.ManifestItem{
		TypeMeta: v1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Namespace",
		},
		Name: ns.Name,
		ID:   fmt.Sprintf("%s_%s__Namespace", ns.Name, ns.Namespace),
	}
	assert.Assert(t, inventoryStorage.HasItem(nsManifest))
}

func TestReconciler_Reconcile_GitPullError(t *testing.T) {
	ctx := context.Background()
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registryPath := t.TempDir()

	cueModuleRegistry, err := ocitest.StartCUERegistry(registryPath)
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	env := projecttest.InitTestEnvironment(
		t,
		[]byte(useMiniTemplate()),
	)

	kubernetes := kubetest.StartKubetestEnv(t, env.Log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()
	projectManager := project.NewManager(component.NewBuilder(), -1)

	scheduler, err := gocron.NewScheduler()
	assert.NilError(t, err)
	quitChan := make(chan struct{}, 1)
	schedulerEg := &errgroup.Group{}
	schedulerEg.SetLimit(1)
	defer func() {
		quitChan <- struct{}{}
		_ = scheduler.Shutdown()
	}()

	reconciler := project.Reconciler{
		KubeConfig:            kubernetes.ControlPlane.Config,
		ComponentBuilder:      component.NewBuilder(),
		RepositoryManager:     kubernetes.RepositoryManager,
		ProjectManager:        projectManager,
		Log:                   env.Log,
		FieldManager:          "controller",
		WorkerPoolSize:        -1,
		InsecureSkipTLSverify: true,
		CacheDir:              env.TestRoot,
		Scheduler:             scheduler,
		SchedulerQuitChan:     quitChan,
		SchedulerErrGroup:     schedulerEg,
	}

	suspend := false
	gProject := gitops.GitOpsProject{
		TypeMeta: v1.TypeMeta{
			APIVersion: "gitops.navecd.io/v1",
			Kind:       "GitOpsProject",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      "test",
			Namespace: "tenant",
			UID:       types.UID(env.TestRoot),
		},
		Spec: gitops.GitOpsProjectSpec{
			URL:                 env.TestProject,
			Branch:              "main",
			PullIntervalSeconds: 5,
			Suspend:             &suspend,
		},
	}

	namespace := corev1.Namespace{
		TypeMeta: v1.TypeMeta{
			APIVersion: "",
			Kind:       "Namespace",
		},
		ObjectMeta: v1.ObjectMeta{
			Name: "tenant",
		},
	}

	err = kubernetes.TestKubeClient.Create(ctx, &namespace)
	assert.NilError(t, err)

	namespace = corev1.Namespace{
		TypeMeta: v1.TypeMeta{
			APIVersion: "",
			Kind:       "Namespace",
		},
		ObjectMeta: v1.ObjectMeta{
			Name: "monitoring",
		},
	}

	err = kubernetes.TestKubeClient.Create(ctx, &namespace)
	assert.NilError(t, err)

	serviceAccount := corev1.ServiceAccount{
		TypeMeta: v1.TypeMeta{
			APIVersion: "",
			Kind:       "ServiceAccount",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      "mysa",
			Namespace: "tenant",
		},
	}

	err = kubernetes.TestKubeClient.Create(ctx, &serviceAccount)
	assert.NilError(t, err)

	result, err := reconciler.Reconcile(ctx, gProject)
	assert.NilError(t, err)
	assert.Equal(t, result.Suspended, false)
	assert.NilError(t, result.PullError)
	assert.NilError(t, result.ComponentError)

	nsName := "toola"

	var ns corev1.Namespace
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: nsName},
		&ns,
	)
	assert.NilError(t, err)
	assert.Equal(t, ns.Name, nsName)

	inventoryInstance := &inventory.Instance{
		Path: filepath.Join("/inventory", string(gProject.GetUID())),
	}
	inventoryStorage, err := inventoryInstance.Load()
	assert.NilError(t, err)

	invComponents := inventoryStorage.Items()
	assert.Assert(t, len(invComponents) == 1)
	nsManifest := &inventory.ManifestItem{
		TypeMeta: v1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Namespace",
		},
		Name: ns.Name,
		ID:   fmt.Sprintf("%s_%s__Namespace", ns.Name, ns.Namespace),
	}
	assert.Assert(t, inventoryStorage.HasItem(nsManifest))

	err = os.RemoveAll(env.TestProject)
	assert.NilError(t, err)

	result, err = reconciler.Reconcile(ctx, gProject)
	assert.NilError(t, err)
	assert.NilError(t, result.ComponentError)
	assert.ErrorContains(t, result.PullError, "repository not found")
}

func TestReconciler_Reconcile_ComponentError(t *testing.T) {
	ctx := context.Background()
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registryPath := t.TempDir()

	cueModuleRegistry, err := ocitest.StartCUERegistry(registryPath)
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	env := projecttest.InitTestEnvironment(
		t,
		[]byte(useMiniTemplate()),
	)

	kubernetes := kubetest.StartKubetestEnv(t, env.Log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()
	projectManager := project.NewManager(component.NewBuilder(), -1)

	scheduler, err := gocron.NewScheduler()
	assert.NilError(t, err)
	quitChan := make(chan struct{}, 1)
	schedulerEg := &errgroup.Group{}
	schedulerEg.SetLimit(1)
	defer func() {
		quitChan <- struct{}{}
		_ = scheduler.Shutdown()
	}()

	reconciler := project.Reconciler{
		KubeConfig:            kubernetes.ControlPlane.Config,
		ComponentBuilder:      component.NewBuilder(),
		RepositoryManager:     kubernetes.RepositoryManager,
		ProjectManager:        projectManager,
		Log:                   env.Log,
		FieldManager:          "controller",
		WorkerPoolSize:        -1,
		InsecureSkipTLSverify: true,
		CacheDir:              env.TestRoot,
		Scheduler:             scheduler,
		SchedulerQuitChan:     quitChan,
		SchedulerErrGroup:     schedulerEg,
	}

	suspend := false
	gProject := gitops.GitOpsProject{
		TypeMeta: v1.TypeMeta{
			APIVersion: "gitops.navecd.io/v1",
			Kind:       "GitOpsProject",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      "test",
			Namespace: "tenant",
			UID:       types.UID(env.TestRoot),
		},
		Spec: gitops.GitOpsProjectSpec{
			ServiceAccountName:  "unprivileged",
			URL:                 env.TestProject,
			Branch:              "main",
			PullIntervalSeconds: 5,
			Suspend:             &suspend,
		},
	}

	result, err := reconciler.Reconcile(ctx, gProject)
	assert.NilError(t, err)
	assert.Equal(t, result.Suspended, false)
	assert.NilError(t, result.PullError)
	assert.ErrorContains(
		t,
		result.ComponentError,
		`is forbidden: User "system:serviceaccount:tenant:unprivileged" cannot get resource`,
	)
}

type workloadIdentityTemplateData struct {
	Name              string
	HelmRepoURL       string
	ContainerRegistry string
}

func useWorkloadIdentityTemplate(
	data workloadIdentityTemplateData,
) *workloadIdentityProjectTemplate {
	return &workloadIdentityProjectTemplate{
		template: fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/projecttest/mini@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/toola/component.cue --
package toola

import (
	"github.com/kharf/navecd/schema/component"
	"github.com/kharf/navecd/schema/workloadidentity"
)

#namespace: {
	apiVersion: "v1"
	kind:       "Namespace"
	metadata: name: "toola"
}

ns: component.#Manifest & {
	content: #namespace
}

deployment: component.#Manifest & {
	content: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		metadata: {
			name:      "deployment"
			namespace: #namespace.metadata.name
		}
		spec: {
			selector: matchLabels: app: "deployment"
			replicas: 1 @ignore(conflict)
			template: {
				metadata: labels: app: "deployment"
				spec: {
					securityContext: {
						runAsNonRoot:        true  @ignore(conflict)
						fsGroup:             65532 @ignore(conflict)
						fsGroupChangePolicy: "OnRootMismatch"
					}
					containers: [
						{
							name:  "subcomponent"
							image: "{{.ContainerRegistry}}/subcomponent:1.14.2" @update(constraint="*", wi=aws, integration=direct, schedule="* * * * * *")
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

release: component.#HelmRelease & {
	dependencies: [
		ns.id,
	]
	name:      "{{.Name}}"
	namespace: #namespace.metadata.name
	chart: {
		name:    "test"
		repoURL: "{{.HelmRepoURL}}"
		version: "1.0.0"
		auth:    workloadidentity.#AWS
	} @update(constraint="*", integration=direct, schedule="* * * * * *")

	crds: {
		allowUpgrade: true
	}

	values: {
		autoscaling: enabled: true
	}
}
`, testtemplates.ModuleVersion),
		data: data,
	}
}

type workloadIdentityProjectTemplate struct {
	template string
	data     workloadIdentityTemplateData
}

func (tmpl *workloadIdentityProjectTemplate) Template() string {
	return tmpl.template
}

func (tmpl *workloadIdentityProjectTemplate) Data() any {
	return tmpl.data
}

var _ testtemplates.Template = (*broadProjectTemplate)(nil)

func TestReconciler_Reconcile_WorkloadIdentity(t *testing.T) {
	ctx := context.Background()
	var err error
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registryPath := t.TempDir()

	cueModuleRegistry, err := ocitest.StartCUERegistry(registryPath)
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	helmEnv, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(true),
		helmtest.WithPrivate(true),
		helmtest.WithProvider(cloud.AWS),
	)
	assert.NilError(t, err)
	defer helmEnv.Close()

	aws, err := cloudtest.NewAWSEnvironment(helmEnv.ChartServer.Addr())
	assert.NilError(t, err)
	defer aws.Close()

	workloadIdentityTemplate := useWorkloadIdentityTemplate(
		workloadIdentityTemplateData{
			Name:              "test",
			HelmRepoURL:       fmt.Sprintf("oci://%s", aws.ECRServer.URL),
			ContainerRegistry: aws.ECRServer.URL,
		},
	)

	registry := helmEnv.ChartServer.(*helmtest.OciRegistry)
	manifest := ociregistry.Manifest{
		MediaType:   "application/vnd.oci.image.manifest.v1+json",
		Annotations: map[string]string{},
		Config: ociregistry.Descriptor{
			Digest: digest.FromString(""),
		},
	}
	bytes, err := json.Marshal(&manifest)
	assert.NilError(t, err)

	_, err = registry.Server.PushManifest(
		ctx,
		"subcomponent",
		"1.15.0",
		bytes,
		"application/vnd.docker.distribution.manifest.v2+json",
	)
	assert.NilError(t, err)

	parsedTemplate, err := testtemplates.Parse(workloadIdentityTemplate)
	assert.NilError(t, err)
	env := projecttest.InitTestEnvironment(
		t,
		parsedTemplate,
	)

	kubernetes := kubetest.StartKubetestEnv(t, env.Log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()
	projectManager := project.NewManager(component.NewBuilder(), -1)

	scheduler, err := gocron.NewScheduler()
	assert.NilError(t, err)
	scheduler.Start()
	quitChan := make(chan struct{}, 1)
	schedulerEg := &errgroup.Group{}
	schedulerEg.SetLimit(1)
	defer func() {
		quitChan <- struct{}{}
		_ = scheduler.Shutdown()
	}()

	reconciler := project.Reconciler{
		KubeConfig:            kubernetes.ControlPlane.Config,
		ComponentBuilder:      component.NewBuilder(),
		RepositoryManager:     kubernetes.RepositoryManager,
		ProjectManager:        projectManager,
		Log:                   env.Log,
		FieldManager:          "controller",
		WorkerPoolSize:        -1,
		InsecureSkipTLSverify: true,
		CacheDir:              env.TestRoot,
		Scheduler:             scheduler,
		SchedulerQuitChan:     quitChan,
		SchedulerErrGroup:     schedulerEg,
	}

	suspend := false
	gProject := gitops.GitOpsProject{
		TypeMeta: v1.TypeMeta{
			APIVersion: "gitops.navecd.io/v1",
			Kind:       "GitOpsProject",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			UID:       types.UID(env.TestRoot),
		},
		Spec: gitops.GitOpsProjectSpec{
			URL:                 env.TestProject,
			Branch:              "main",
			PullIntervalSeconds: 5,
			Suspend:             &suspend,
		},
	}

	inventoryInstance := &inventory.Instance{
		// /inventory is mounted as volume.
		Path: filepath.Join("/inventory", string(gProject.GetUID())),
	}

	result, err := reconciler.Reconcile(ctx, gProject)
	assert.NilError(t, err)
	assert.Equal(t, result.Suspended, false)

	toolaNs := "toola"

	var testDep appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "test", Namespace: toolaNs},
		&testDep,
	)
	assert.NilError(t, err)
	assert.Equal(t, testDep.Name, "test")
	assert.Equal(t, testDep.Namespace, toolaNs)

	var dep appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "deployment", Namespace: toolaNs},
		&dep,
	)
	assert.NilError(t, err)
	assert.Equal(t, dep.Name, "deployment")
	assert.Equal(t, dep.Namespace, toolaNs)

	check := func(t poll.LogT) poll.Result {
		_, err = reconciler.Reconcile(ctx, gProject)
		if err != nil {
			return poll.Error(err)
		}

		// does not exist in chart v3 and therefore would mean that updates worked.
		var svcAcc corev1.ServiceAccount
		err = kubernetes.TestKubeClient.Get(
			ctx,
			types.NamespacedName{Name: "test", Namespace: toolaNs},
			&svcAcc,
		)
		if err == nil {
			return poll.Continue("service account still found")
		}

		if err.Error() != "serviceaccounts \"test\" not found" {
			return poll.Error(err)
		}

		inventoryStorage, err := inventoryInstance.Load()
		if err != nil {
			return poll.Error(err)
		}

		invComponents := inventoryStorage.Items()
		if len(invComponents) != 3 {
			return poll.Continue("expected inventory to contain 3 items")
		}
		testHR := &inventory.HelmReleaseItem{
			Name:      testDep.Name,
			Namespace: testDep.Namespace,
			ID:        fmt.Sprintf("%s_%s_HelmRelease", testDep.Name, testDep.Namespace),
		}
		return poll.Compare(cmp.Equal(inventoryStorage.HasItem(testHR), true))
	}

	poll.WaitOn(t, check, poll.WithDelay(1*time.Second))
}

func TestReconciler_Reconcile_Suspend(t *testing.T) {
	ctx := context.Background()
	var err error
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registryPath := t.TempDir()

	cueModuleRegistry, err := ocitest.StartCUERegistry(registryPath)
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(false),
		helmtest.WithPrivate(false),
	)
	assert.NilError(t, err)
	defer publicHelmEnvironment.Close()

	tlsRegistry, err := ocitest.NewTLSRegistry(false, "")
	assert.NilError(t, err)
	defer tlsRegistry.Close()

	broadTemplate := useBroadTemplate(
		broadTemplateData{
			Name:              "test",
			HelmRepoURL:       publicHelmEnvironment.ChartServer.URL(),
			ContainerRegistry: tlsRegistry.Addr(),
		},
	)
	for _, container := range broadTemplate.Containers() {
		desc, err := tlsRegistry.PushManifest(
			ctx,
			container,
			"1.15.3",
			[]byte{},
			"application/vnd.docker.distribution.manifest.v2+json",
		)
		assert.NilError(t, err)
		defer tlsRegistry.DeleteManifest(ctx, container, desc.Digest)
	}

	parsedTemplate, err := testtemplates.Parse(broadTemplate)
	assert.NilError(t, err)
	env := projecttest.InitTestEnvironment(
		t,
		parsedTemplate,
	)

	kubernetes := kubetest.StartKubetestEnv(t, env.Log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()
	projectManager := project.NewManager(component.NewBuilder(), -1)

	reconciler := project.Reconciler{
		KubeConfig:            kubernetes.ControlPlane.Config,
		ComponentBuilder:      component.NewBuilder(),
		RepositoryManager:     kubernetes.RepositoryManager,
		ProjectManager:        projectManager,
		Log:                   env.Log,
		FieldManager:          "controller",
		WorkerPoolSize:        -1,
		InsecureSkipTLSverify: true,
		CacheDir:              env.TestRoot,
	}

	suspend := true
	gProject := gitops.GitOpsProject{
		TypeMeta: v1.TypeMeta{
			APIVersion: "gitops.navecd.io/v1",
			Kind:       "GitOpsProject",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			UID:       types.UID(env.TestRoot),
		},
		Spec: gitops.GitOpsProjectSpec{
			URL:                 env.TestProject,
			Branch:              "main",
			PullIntervalSeconds: 5,
			Suspend:             &suspend,
		},
	}

	result, err := reconciler.Reconcile(ctx, gProject)
	assert.NilError(t, err)
	assert.Equal(t, result.Suspended, true)

	var deployment appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "test", Namespace: "toolb"},
		&deployment,
	)
	assert.Error(t, err, "deployments.apps \"test\" not found")
}

func TestReconciler_Reconcile_Conflict(t *testing.T) {
	ctx := context.Background()
	var err error
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registryPath := t.TempDir()

	cueModuleRegistry, err := ocitest.StartCUERegistry(registryPath)
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(false),
		helmtest.WithPrivate(false),
	)
	assert.NilError(t, err)
	defer publicHelmEnvironment.Close()

	tlsRegistry, err := ocitest.NewTLSRegistry(false, "")
	assert.NilError(t, err)
	defer tlsRegistry.Close()

	broadTemplate := useBroadTemplate(
		broadTemplateData{
			Name:              "test",
			HelmRepoURL:       publicHelmEnvironment.ChartServer.URL(),
			ContainerRegistry: tlsRegistry.Addr(),
		},
	)
	for _, container := range broadTemplate.Containers() {
		manifest := ociregistry.Manifest{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Annotations: map[string]string{
				"org.opencontainers.image.url": "test",
			},
			Config: ociregistry.Descriptor{
				Digest: digest.FromString(""),
			},
		}

		bytes, err := json.Marshal(&manifest)
		assert.NilError(t, err)

		desc, err := tlsRegistry.PushManifest(
			ctx,
			container,
			"1.15.3",
			bytes,
			"application/vnd.docker.distribution.manifest.v2+json",
		)
		assert.NilError(t, err)
		defer tlsRegistry.DeleteManifest(ctx, container, desc.Digest)
	}

	parsedTemplate, err := testtemplates.Parse(broadTemplate)
	assert.NilError(t, err)
	env := projecttest.InitTestEnvironment(
		t,
		parsedTemplate,
	)

	kubernetes := kubetest.StartKubetestEnv(t, env.Log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()
	projectManager := project.NewManager(component.NewBuilder(), -1)

	scheduler, err := gocron.NewScheduler()
	assert.NilError(t, err)
	quitChan := make(chan struct{}, 1)
	schedulerEg := &errgroup.Group{}
	schedulerEg.SetLimit(1)
	defer func() {
		quitChan <- struct{}{}
		_ = scheduler.Shutdown()
	}()

	reconciler := project.Reconciler{
		KubeConfig:            kubernetes.ControlPlane.Config,
		ComponentBuilder:      component.NewBuilder(),
		RepositoryManager:     kubernetes.RepositoryManager,
		ProjectManager:        projectManager,
		Log:                   env.Log,
		FieldManager:          "controller",
		WorkerPoolSize:        -1,
		InsecureSkipTLSverify: true,
		CacheDir:              env.TestRoot,
		Scheduler:             scheduler,
		SchedulerQuitChan:     quitChan,
		SchedulerErrGroup:     schedulerEg,
	}

	suspend := false
	gProject := gitops.GitOpsProject{
		TypeMeta: v1.TypeMeta{
			APIVersion: "gitops.navecd.io/v1",
			Kind:       "GitOpsProject",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			UID:       types.UID(env.TestRoot),
		},
		Spec: gitops.GitOpsProjectSpec{
			URL:                 env.TestProject,
			Branch:              "main",
			PullIntervalSeconds: 5,
			Suspend:             &suspend,
		},
	}

	result, err := reconciler.Reconcile(ctx, gProject)
	assert.NilError(t, err)
	assert.Equal(t, result.Suspended, false)

	var deployment appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "mysubcomponent", Namespace: "toolb"},
		&deployment,
	)
	assert.NilError(t, err)

	var anotherDeployment appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "anothersubcomponent", Namespace: "toolb"},
		&anotherDeployment,
	)
	assert.NilError(t, err)

	unstr := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      "mysubcomponent",
				"namespace": "toolb",
			},
			"spec": map[string]interface{}{
				"replicas": 2,
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"securityContext": map[string]interface{}{
							"runAsNonRoot":        false,
							"fsGroup":             0,
							"fsGroupChangePolicy": "Always",
						},
					},
				},
			},
		},
	}

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		&unstr,
		"imposter",
		kube.ForceApply(true),
	)
	assert.NilError(t, err)

	_, err = reconciler.Reconcile(ctx, gProject)
	assert.NilError(t, err)

	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "mysubcomponent", Namespace: "toolb"},
		&deployment,
	)
	assert.NilError(t, err)
	assert.Equal(t, deployment.Name, "mysubcomponent")
	assert.Equal(t, deployment.Namespace, "toolb")
	assert.Equal(t, *deployment.Spec.Replicas, int32(1))
	assert.Equal(
		t,
		*deployment.Spec.Template.Spec.SecurityContext.RunAsNonRoot,
		true,
	)
	assert.Equal(
		t,
		*deployment.Spec.Template.Spec.SecurityContext.FSGroup,
		int64(65532),
	)
	assert.Equal(
		t,
		*deployment.Spec.Template.Spec.SecurityContext.FSGroupChangePolicy,
		corev1.FSGroupChangeOnRootMismatch,
	)
}

func TestReconciler_Reconcile_IgnoreConflicts(t *testing.T) {
	ctx := context.Background()
	var err error
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registryPath := t.TempDir()

	cueModuleRegistry, err := ocitest.StartCUERegistry(registryPath)
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(false),
		helmtest.WithPrivate(false),
	)
	assert.NilError(t, err)
	defer publicHelmEnvironment.Close()

	tlsRegistry, err := ocitest.NewTLSRegistry(false, "")
	assert.NilError(t, err)
	defer tlsRegistry.Close()

	broadTemplate := useBroadTemplate(
		broadTemplateData{
			Name:              "test",
			HelmRepoURL:       publicHelmEnvironment.ChartServer.URL(),
			ContainerRegistry: tlsRegistry.Addr(),
		},
	)
	for _, container := range broadTemplate.Containers() {
		manifest := ociregistry.Manifest{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Annotations: map[string]string{
				"org.opencontainers.image.url": "test",
			},
			Config: ociregistry.Descriptor{
				Digest: digest.FromString(""),
			},
		}

		bytes, err := json.Marshal(&manifest)
		assert.NilError(t, err)

		desc, err := tlsRegistry.PushManifest(
			ctx,
			container,
			"1.15.3",
			bytes,
			"application/vnd.docker.distribution.manifest.v2+json",
		)
		assert.NilError(t, err)
		defer tlsRegistry.DeleteManifest(ctx, container, desc.Digest)
	}

	parsedTemplate, err := testtemplates.Parse(broadTemplate)
	assert.NilError(t, err)
	env := projecttest.InitTestEnvironment(
		t,
		parsedTemplate,
	)

	kubernetes := kubetest.StartKubetestEnv(t, env.Log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	projectManager := project.NewManager(component.NewBuilder(), -1)

	scheduler, err := gocron.NewScheduler()
	assert.NilError(t, err)
	quitChan := make(chan struct{}, 1)
	schedulerEg := &errgroup.Group{}
	schedulerEg.SetLimit(1)
	defer func() {
		quitChan <- struct{}{}
		_ = scheduler.Shutdown()
	}()

	reconciler := project.Reconciler{
		KubeConfig:            kubernetes.ControlPlane.Config,
		ComponentBuilder:      component.NewBuilder(),
		RepositoryManager:     kubernetes.RepositoryManager,
		ProjectManager:        projectManager,
		Log:                   env.Log,
		FieldManager:          "controller",
		WorkerPoolSize:        -1,
		InsecureSkipTLSverify: true,
		CacheDir:              env.TestRoot,
		Scheduler:             scheduler,
		SchedulerQuitChan:     quitChan,
		SchedulerErrGroup:     schedulerEg,
	}

	suspend := false
	gProject := gitops.GitOpsProject{
		TypeMeta: v1.TypeMeta{
			APIVersion: "gitops.navecd.io/v1",
			Kind:       "GitOpsProject",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			UID:       types.UID(env.TestRoot),
		},
		Spec: gitops.GitOpsProjectSpec{
			URL:                 env.TestProject,
			Branch:              "main",
			PullIntervalSeconds: 5,
			Suspend:             &suspend,
		},
	}

	result, err := reconciler.Reconcile(ctx, gProject)
	assert.NilError(t, err)
	assert.Equal(t, result.Suspended, false)

	var deployment appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "mysubcomponent", Namespace: "toolb"},
		&deployment,
	)
	assert.NilError(t, err)

	var anotherDeployment appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "anothersubcomponent", Namespace: "toolb"},
		&anotherDeployment,
	)
	assert.NilError(t, err)

	anotherUnstr := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      "anothersubcomponent",
				"namespace": "toolb",
			},
			"spec": map[string]interface{}{
				"replicas": 2,
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"securityContext": map[string]interface{}{
							"runAsNonRoot":        false,
							"fsGroup":             0,
							"fsGroupChangePolicy": "Always",
						},
					},
				},
			},
		},
	}

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		&anotherUnstr,
		"imposter",
		kube.ForceApply(true),
	)
	assert.NilError(t, err)

	_, err = reconciler.Reconcile(ctx, gProject)
	assert.NilError(t, err)

	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "anothersubcomponent", Namespace: "toolb"},
		&anotherDeployment,
	)
	assert.NilError(t, err)
	assert.Equal(t, anotherDeployment.Name, "anothersubcomponent")
	assert.Equal(t, anotherDeployment.Namespace, "toolb")
	assert.Equal(t, *anotherDeployment.Spec.Replicas, int32(2))
	assert.Equal(
		t,
		*anotherDeployment.Spec.Template.Spec.SecurityContext.RunAsNonRoot,
		false,
	)
	assert.Equal(
		t,
		*anotherDeployment.Spec.Template.Spec.SecurityContext.FSGroup,
		int64(0),
	)
	assert.Equal(
		t,
		*anotherDeployment.Spec.Template.Spec.SecurityContext.FSGroupChangePolicy,
		corev1.FSGroupChangeOnRootMismatch,
	)
}
