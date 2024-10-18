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
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/kharf/navecd/internal/helmtest"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/pkg/component"
	"github.com/kharf/navecd/pkg/helm"
	"github.com/kharf/navecd/pkg/inventory"
	"github.com/kharf/navecd/pkg/kube"
	"gotest.tools/v3/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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

	ns := namespace("a", nil)
	_, err = kubernetes.DynamicTestKubeClient.Apply(
		kubernetes.Ctx,
		&ns.(*component.Manifest).Content,
		"manager",
	)
	assert.NilError(b, err)

	count := 250
	instances := make([]component.Instance, 0, count)
	for c := range count {
		instances = append(
			instances,
			hr(
				fmt.Sprintf("app%v", c),
				"a",
				nil,
				publicHelmEnvironment.ChartServer.URL(),
			))
	}

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
		hr(name, ns, nil, repoURL),
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
		ID: fmt.Sprintf("%s_prometheus_HelmRelease", name),
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
