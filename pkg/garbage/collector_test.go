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

package garbage_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	goRuntime "runtime"
	"testing"

	"github.com/go-logr/logr"
	"github.com/kharf/navecd/internal/helmtest"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/pkg/component"
	"github.com/kharf/navecd/pkg/garbage"
	"github.com/kharf/navecd/pkg/helm"
	"github.com/kharf/navecd/pkg/inventory"
	"github.com/kharf/navecd/pkg/kube"
	"go.uber.org/goleak"
	"gotest.tools/v3/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type testCaseContext struct {
	ctx               context.Context
	kubernetes        *kubetest.Environment
	inventoryInstance *inventory.Instance
	collector         garbage.Collector
	chartReconciler   helm.ChartReconciler
}

func TestCollector_Collect(t *testing.T) {
	defer goleak.VerifyNone(
		t,
	)

	var err error
	helmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(false),
		helmtest.WithPrivate(false),
	)
	assert.NilError(t, err)
	defer helmEnvironment.Close()

	nsA := &inventory.ManifestItem{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		Name: "a",
		ID:   "a___Namespace",
	}
	depA := &inventory.ManifestItem{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		Name:      "a",
		Namespace: "a",
		ID:        "a_a_apps_Deployment",
	}
	nsB := &inventory.ManifestItem{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		Name: "b",
		ID:   "b___Namespace",
	}
	depB := &inventory.ManifestItem{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		Name:      "b",
		Namespace: "b",
		ID:        "b_b_apps_Deployment",
	}
	invManifests := []*inventory.ManifestItem{
		nsA,
		depA,
		nsB,
		depB,
	}

	hr := &inventory.HelmReleaseItem{
		Name:      "test",
		Namespace: "test",
		ID:        "test_test_HelmRelease",
	}
	invHelmReleases := []*inventory.HelmReleaseItem{
		hr,
	}

	testCases := []struct {
		name    string
		runCase func(context testCaseContext)
	}{
		{
			name: "Deleted-DepB-and-HR",
			runCase: func(context testCaseContext) {
				dag := component.NewDependencyGraph()
				ctx := context.ctx
				kubernetes := context.kubernetes
				inventoryInstance := context.inventoryInstance

				prepareManifests(ctx,
					t,
					invManifests,
					kubernetes.DynamicTestKubeClient.DynamicClient(),
					inventoryInstance,
					dag,
				)

				prepareHelmReleases(
					ctx,
					t,
					helmEnvironment,
					invHelmReleases,
					context.chartReconciler,
					inventoryInstance,
					dag,
				)

				storage, err := inventoryInstance.Load()
				assert.NilError(t, err)

				dynClient := kubernetes.DynamicTestKubeClient.DynamicClient()
				assertItems(t, invManifests, invHelmReleases, storage)
				assertRunningAll := func(t *testing.T) {
					assertRunning(ctx, t, dynClient, &unstructured.Unstructured{
						Object: map[string]interface{}{
							"apiVersion": "apps/v1",
							"kind":       "Deployment",
							"metadata": map[string]interface{}{
								"name":      "a",
								"namespace": "a",
							},
						},
					})

					assertRunning(ctx, t, dynClient, &unstructured.Unstructured{
						Object: map[string]interface{}{
							"apiVersion": "apps/v1",
							"kind":       "Deployment",
							"metadata": map[string]interface{}{
								"name":      "b",
								"namespace": "b",
							},
						},
					})

					assertRunning(ctx, t, dynClient, &unstructured.Unstructured{
						Object: map[string]interface{}{
							"apiVersion": "apps/v1",
							"kind":       "Deployment",
							"metadata": map[string]interface{}{
								"name":      "test",
								"namespace": "test",
							},
						},
					})
				}
				assertRunningAll(t)

				err = context.collector.Collect(ctx, &dag)
				assert.NilError(t, err)

				storage, err = inventoryInstance.Load()
				assert.NilError(t, err)

				assertItems(t, invManifests, invHelmReleases, storage)
				assertRunningAll(t)

				renderedManifests := []*inventory.ManifestItem{
					nsA,
					nsB,
					depA,
				}

				dag = component.NewDependencyGraph()
				prepareManifests(
					ctx,
					t,
					renderedManifests,
					kubernetes.DynamicTestKubeClient.DynamicClient(),
					inventoryInstance,
					dag,
				)

				err = context.collector.Collect(ctx, &dag)
				assert.NilError(t, err)

				assertRunning(ctx, t, dynClient, &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name":      "a",
							"namespace": "a",
						},
					},
				})

				assertNotRunning(ctx, t, dynClient, &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name":      "b",
							"namespace": "b",
						},
					},
				})

				assertNotRunning(ctx, t, dynClient, &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name":      "test",
							"namespace": "test",
						},
					},
				})
			},
		},
		{
			name: "Deleted-Deployment-But-Still-In-Inventory",
			runCase: func(context testCaseContext) {
				renderedManifests := []*inventory.ManifestItem{
					nsA,
					depA,
				}

				dag := component.NewDependencyGraph()
				ctx := context.ctx
				kubernetes := context.kubernetes
				inventoryInstance := context.inventoryInstance

				prepareManifests(
					ctx,
					t,
					renderedManifests,
					kubernetes.DynamicTestKubeClient.DynamicClient(),
					inventoryInstance,
					dag,
				)
				storage, err := inventoryInstance.Load()
				assert.NilError(t, err)
				assertItems(t, renderedManifests, []*inventory.HelmReleaseItem{}, storage)

				obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(toObject(depA))
				assert.NilError(t, err)
				unstr := &unstructured.Unstructured{Object: obj}

				dynClient := kubernetes.DynamicTestKubeClient.DynamicClient()
				assertRunning(ctx, t, dynClient, unstr)

				err = dynClient.Delete(ctx, unstr)
				assert.NilError(t, err)

				storage, err = inventoryInstance.Load()
				assert.NilError(t, err)
				assertItems(t, renderedManifests, []*inventory.HelmReleaseItem{}, storage)

				err = context.collector.Collect(ctx, &dag)
				assert.NilError(t, err)

				storage, err = inventoryInstance.Load()
				assert.NilError(t, err)

				assertNotRunning(ctx, t, dynClient, &unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]interface{}{
							"name":      "a",
							"namespace": "a",
						},
					},
				})
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			kubernetes := kubetest.StartKubetestEnv(t, logr.Discard(), kubetest.WithEnabled(true))
			defer kubernetes.Stop()

			inventoryInstance := &inventory.Instance{
				Path: filepath.Join(t.TempDir(), "inventory"),
			}

			log := logr.Discard()

			chartReconciler := helm.ChartReconciler{
				KubeConfig:            kubernetes.ControlPlane.Config,
				Client:                kubernetes.DynamicTestKubeClient,
				FieldManager:          "controller",
				InsecureSkipTLSVerify: true,
				InventoryInstance:     inventoryInstance,
				Log:                   log,
			}

			collector := garbage.Collector{
				Log:               log,
				Client:            kubernetes.DynamicTestKubeClient.DynamicClient(),
				ChartReconciler:   chartReconciler,
				InventoryInstance: inventoryInstance,
				WorkerPoolSize:    goRuntime.GOMAXPROCS(0),
			}

			ctx := context.Background()
			tc.runCase(testCaseContext{
				ctx:               ctx,
				kubernetes:        kubernetes,
				inventoryInstance: inventoryInstance,
				collector:         collector,
				chartReconciler:   chartReconciler,
			})
		})
	}
}

func assertRunning(
	ctx context.Context,
	t *testing.T,
	client *kube.DynamicClient,
	obj *unstructured.Unstructured,
) {
	foundObj, err := client.Get(ctx, obj)
	assert.NilError(t, err)
	assert.Equal(t, foundObj.GetName(), obj.GetName())
	assert.Equal(t, foundObj.GetNamespace(), obj.GetNamespace())
}

func assertNotRunning(
	ctx context.Context,
	t *testing.T,
	client *kube.DynamicClient,
	obj *unstructured.Unstructured,
) {
	restMapper := client.RESTMapper()
	gvk := obj.GroupVersionKind()
	mapping, err := restMapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	assert.NilError(t, err)
	_, err = client.Get(ctx, obj)
	assert.Error(
		t,
		err,
		fmt.Sprintf(
			"%s.%s \"%s\" not found",
			mapping.Resource.Resource,
			mapping.Resource.Group,
			obj.GetName(),
		),
	)
}

func assertItems(
	t *testing.T,
	manifests []*inventory.ManifestItem,
	releases []*inventory.HelmReleaseItem,
	storage *inventory.Storage,
) {
	for _, manifest := range manifests {
		assert.Assert(t, storage.HasItem(manifest))
	}
	for _, release := range releases {
		assert.Assert(t, storage.HasItem(release))
	}
}

func prepareHelmReleases(
	ctx context.Context,
	t *testing.T,
	helmEnvironment *helmtest.Environment,
	invHelmReleases []*inventory.HelmReleaseItem,
	chartReconciler helm.ChartReconciler,
	inventoryInstance *inventory.Instance,
	dag component.DependencyGraph,
) {
	releases := make([]helm.ReleaseDeclaration, 0, len(invHelmReleases))
	for _, hrMetadata := range invHelmReleases {
		release := helm.ReleaseDeclaration{
			Name:      hrMetadata.GetName(),
			Namespace: hrMetadata.GetNamespace(),
			Chart: &helm.Chart{
				Name:    "test",
				RepoURL: helmEnvironment.ChartServer.URL(),
				Version: "1.0.0",
			},
			Values: helm.Values{},
		}
		id := fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace)
		_, err := chartReconciler.Reconcile(
			ctx,
			&helm.ReleaseComponent{
				ID:      id,
				Content: release,
			},
		)
		assert.NilError(t, err)
		releases = append(releases, release)
		err = inventoryInstance.StoreItem(&inventory.HelmReleaseItem{
			Name:      release.Name,
			Namespace: release.Namespace,
			ID:        id,
		}, nil)
		assert.NilError(t, err)
		dag.Insert(&helm.ReleaseComponent{
			ID:           hrMetadata.ID,
			Dependencies: []string{},
			Content:      release,
		})
	}
}

func prepareManifests(
	ctx context.Context,
	t *testing.T,
	invManifests []*inventory.ManifestItem,
	client *kube.DynamicClient,
	inventoryInstance *inventory.Instance,
	dag component.DependencyGraph,
) {
	for _, im := range invManifests {
		obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(toObject(im))
		unstr := unstructured.Unstructured{Object: obj}
		_, err = client.Apply(ctx, &unstr, "test")
		assert.NilError(t, err)
		buf := &bytes.Buffer{}
		json.NewEncoder(buf).Encode(unstr.Object)
		err = inventoryInstance.StoreItem(im, buf)
		assert.NilError(t, err)
		dag.Insert(
			&component.Manifest{
				ID:           im.ID,
				Dependencies: []string{},
				Content:      component.ExtendedUnstructured{Unstructured: &unstr},
			},
		)
	}
}

func toObject(invManifest *inventory.ManifestItem) client.Object {
	switch invManifest.TypeMeta.Kind {
	case "Deployment":
		return deployment(invManifest)
	case "Namespace":
		return namespace(invManifest)
	}
	return nil
}

func namespace(invManifest *inventory.ManifestItem) client.Object {
	return &v1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: invManifest.GetName(),
		},
	}
}

func deployment(invManifest *inventory.ManifestItem) client.Object {
	replicas := int32(1)
	labels := map[string]string{
		"app": invManifest.GetName(),
	}
	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      invManifest.GetName(),
			Namespace: invManifest.GetNamespace(),
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Replicas: &replicas,
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: v1.PodSpec{
					Containers: []v1.Container{
						{
							Name:  invManifest.GetName(),
							Image: "test",
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("10m"),
									v1.ResourceMemory: resource.MustParse("10Mi"),
								},
								Requests: v1.ResourceList{
									v1.ResourceCPU:    resource.MustParse("10m"),
									v1.ResourceMemory: resource.MustParse("10Mi"),
								},
							},
						},
					},
				}},
		},
	}
}
