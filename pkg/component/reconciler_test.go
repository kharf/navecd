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

package component_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/kharf/navecd/internal/helmtest"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/pkg/component"
	"github.com/kharf/navecd/pkg/helm"
	"github.com/kharf/navecd/pkg/inventory"
	"github.com/kharf/navecd/pkg/kube"
	"go.uber.org/goleak"
	"go.uber.org/zap/zapcore"
	"gotest.tools/v3/assert"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrlZap "sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestReconciler_Reconcile(t *testing.T) {
	defer goleak.VerifyNone(
		t,
	)

	cacheDir := t.TempDir()
	inventoryDir := t.TempDir()
	kubernetes := kubetest.StartKubetestEnv(t, logr.Discard(), kubetest.WithEnabled(true))
	publicHelmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(false),
		helmtest.WithPrivate(false),
	)
	assert.NilError(t, err)
	defer func() {
		publicHelmEnvironment.Close()
		kubernetes.Stop()
	}()

	inventoryInstance := &inventory.Instance{
		Path: inventoryDir,
	}

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))

	chartReconciler := helm.ChartReconciler{
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "manager",
		InventoryInstance:     inventoryInstance,
		InsecureSkipTLSverify: true,
		PlainHTTP:             false,
		Log:                   log,
		ChartCacheRoot:        cacheDir,
	}

	reconciler := component.Reconciler{
		Log:               log,
		DynamicClient:     kubernetes.DynamicTestKubeClient,
		ChartReconciler:   chartReconciler,
		InventoryInstance: inventoryInstance,
		FieldManager:      "manager",
		WorkerPoolSize:    -1,
	}

	instances := []component.Instance{
		namespace("a", nil),
		hr("a", "a", []string{"a___Namespace"}, publicHelmEnvironment.ChartServer.URL()),
		namespace("c", nil),
		hr("c", "c", []string{"c___Namespace"}, publicHelmEnvironment.ChartServer.URL()),
		namespace("b", nil),
		hr(
			"b",
			"b",
			[]string{"b___Namespace", "c_c_HelmRelease"},
			publicHelmEnvironment.ChartServer.URL(),
		),
	}

	err = reconciler.Reconcile(kubernetes.Ctx, instances)
	assert.NilError(t, err)

	var depA appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		context.Background(),
		types.NamespacedName{Name: "a-test", Namespace: "a"},
		&depA,
	)
	assert.NilError(t, err)

	var depB appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		context.Background(),
		types.NamespacedName{Name: "b-test", Namespace: "b"},
		&depB,
	)
	assert.NilError(t, err)

	var depC appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		context.Background(),
		types.NamespacedName{Name: "c-test", Namespace: "c"},
		&depC,
	)
	assert.NilError(t, err)
}

func TestReconciler_Reconcile_Error(t *testing.T) {
	defer goleak.VerifyNone(
		t,
	)

	cacheDir := t.TempDir()
	inventoryDir := t.TempDir()
	kubernetes := kubetest.StartKubetestEnv(t, logr.Discard(), kubetest.WithEnabled(true))
	publicHelmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(false),
		helmtest.WithPrivate(false),
	)
	assert.NilError(t, err)
	defer func() {
		publicHelmEnvironment.Close()
		kubernetes.Stop()
	}()

	inventoryInstance := &inventory.Instance{
		Path: inventoryDir,
	}

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))

	chartReconciler := helm.ChartReconciler{
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "manager",
		InventoryInstance:     inventoryInstance,
		InsecureSkipTLSverify: true,
		PlainHTTP:             false,
		Log:                   log,
		ChartCacheRoot:        cacheDir,
	}

	reconciler := component.Reconciler{
		Log:               log,
		DynamicClient:     kubernetes.DynamicTestKubeClient,
		ChartReconciler:   chartReconciler,
		InventoryInstance: inventoryInstance,
		FieldManager:      "manager",
		WorkerPoolSize:    -1,
	}

	instances := []component.Instance{
		hr("a", "a", []string{}, publicHelmEnvironment.ChartServer.URL()),
		namespace("c", nil),
		hr("c", "c", []string{"c___Namespace"}, publicHelmEnvironment.ChartServer.URL()),
		namespace("b", nil),
		hr(
			"b",
			"b",
			[]string{"b___Namespace", "c_c_HelmRelease"},
			publicHelmEnvironment.ChartServer.URL(),
		),
		namespace("d", nil),
		hr("d", "d", []string{"a_a_HelmRelease"}, publicHelmEnvironment.ChartServer.URL()),
	}

	dag := component.NewDependencyGraph()
	err = dag.Insert(instances...)
	assert.NilError(t, err)
	instances, err = dag.TopologicalSort()
	assert.NilError(t, err)

	err = reconciler.Reconcile(kubernetes.Ctx, instances)
	assert.ErrorContains(t, err, `namespaces "a" not found`)

	var depA appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		context.Background(),
		types.NamespacedName{Name: "a-test", Namespace: "a"},
		&depA,
	)
	assert.ErrorContains(t, err, "not found")

	var depB appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		context.Background(),
		types.NamespacedName{Name: "b-test", Namespace: "b"},
		&depB,
	)
	assert.NilError(t, err)

	var depC appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		context.Background(),
		types.NamespacedName{Name: "c-test", Namespace: "c"},
		&depC,
	)
	assert.NilError(t, err)

	var depD appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		context.Background(),
		types.NamespacedName{Name: "d-test", Namespace: "d"},
		&depD,
	)
	assert.ErrorContains(t, err, "not found")
}

var err error

func BenchmarkReconciler_Reconcile(b *testing.B) {
	b.ReportAllocs()

	cacheDir := b.TempDir()
	inventoryDir := b.TempDir()
	kubernetes := kubetest.StartKubetestEnv(b, logr.Discard(), kubetest.WithEnabled(true))
	publicHelmEnvironment, err := helmtest.NewHelmEnvironment(
		b,
		helmtest.WithOCI(false),
		helmtest.WithPrivate(false),
	)
	assert.NilError(b, err)
	defer func() {
		b.StopTimer()
		publicHelmEnvironment.Close()
		kubernetes.Stop()
	}()

	inventoryInstance := &inventory.Instance{
		Path: inventoryDir,
	}

	chartReconciler := helm.ChartReconciler{
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "manager",
		InventoryInstance:     inventoryInstance,
		InsecureSkipTLSverify: true,
		PlainHTTP:             false,
		Log:                   logr.Discard(),
		ChartCacheRoot:        cacheDir,
	}

	reconciler := component.Reconciler{
		Log:               logr.Discard(),
		DynamicClient:     kubernetes.DynamicTestKubeClient,
		ChartReconciler:   chartReconciler,
		InventoryInstance: inventoryInstance,
		FieldManager:      "manager",
		WorkerPoolSize:    -1,
	}

	count := 250
	dag := component.NewDependencyGraph()
	for c := range count {
		name := fmt.Sprintf("app%v", c)
		err := dag.Insert(app(
			name,
			name,
			publicHelmEnvironment.ChartServer.URL(),
		)...)
		assert.NilError(b, err)
	}

	instances, err := dag.TopologicalSort()
	assert.NilError(b, err)

	var recErr error
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recErr = reconciler.Reconcile(kubernetes.Ctx, instances)
		b.StopTimer()
		assert.NilError(b, err)
		b.StartTimer()
	}

	err = recErr
}

func app(
	name string,
	ns string,
	repoURL string,
) []component.Instance {
	return []component.Instance{
		namespace(ns, nil),
		hr(name, ns, []string{fmt.Sprintf("%s___Namespace", ns)}, repoURL),
	}
}

func namespace(name string, dependencies []string) component.Instance {
	return &component.Manifest{
		ID: fmt.Sprintf("%s___Namespace", name),
		Content: kube.ExtendedUnstructured{
			Unstructured: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "v1",
					"kind":       "Namespace",
					"metadata": map[string]any{
						"name": name,
					},
				},
			},
		},
		Dependencies: dependencies,
	}
}

func hr(name string, namespace string, dependencies []string, repoURL string) component.Instance {
	return &helm.ReleaseComponent{
		ID: fmt.Sprintf("%s_%s_HelmRelease", name, namespace),
		Content: helm.ReleaseDeclaration{
			Name:      name,
			Namespace: namespace,
			Chart: &helm.Chart{
				Name:    "test",
				RepoURL: repoURL,
				Version: "1.0.0",
			},
			Values: helm.Values{
				"autoscaling": map[string]interface{}{
					"enabled": true,
				},
			},
			CRDs: helm.CRDs{
				AllowUpgrade: false,
			},
		},
		Dependencies: dependencies,
	}
}
