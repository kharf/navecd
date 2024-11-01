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

package kube_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/pkg/kube"
	"gotest.tools/v3/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestDiffer_Diff(t *testing.T) {
	testCases := []struct {
		name             string
		haveUnstructured *unstructured.Unstructured
		haveChange       *unstructured.Unstructured
		wantDifference   *kube.Difference
	}{
		{
			name: "Add",
			haveUnstructured: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Namespace",
					"metadata": map[string]any{
						"name": "test",
					},
				},
			},
			haveChange: &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Namespace",
					"metadata": map[string]any{
						"name": "test",
						"labels": map[string]any{
							"new-label": "hello",
						},
					},
				},
			},
			wantDifference: &kube.Difference{
				GVK: schema.GroupVersionKind{
					Group:   "",
					Version: "v1",
					Kind:    "Namespace",
				},
				Lines: []kube.DiffLine{
					{
						Node:        "apiVersion",
						Value:       "v1",
						DiffType:    kube.NoDiff,
						Indentation: "",
					},
					{
						Node:        "kind",
						Value:       "Namespace",
						DiffType:    kube.NoDiff,
						Indentation: "",
					},
					{
						Node:        "metadata",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "",
					},
					{
						Node:        "labels",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "  ",
					},
					{
						Node:        "kubernetes.io/metadata.name",
						Value:       "test",
						DiffType:    kube.NoDiff,
						Indentation: "    ",
					},
					{
						Node:        "new-label",
						Value:       "hello",
						DiffType:    kube.AddDiff,
						Indentation: "    ",
					},
					{
						Node:        "managedFields",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "  ",
					},
					{
						Node:        "- apiVersion",
						Value:       "v1",
						DiffType:    kube.NoDiff,
						Indentation: "    ",
					},
					{
						Node:        "fieldsType",
						Value:       "FieldsV1",
						DiffType:    kube.NoDiff,
						Indentation: "      ",
					},
					{
						Node:        "fieldsV1",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "      ",
					},
					{
						Node:        "f:metadata",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "        ",
					},
					{
						Node:        "f:labels",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "          ",
					},
					{
						Node:        "f:new-label",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "            ",
					},
					{
						Node:        "manager",
						Value:       "test",
						DiffType:    kube.NoDiff,
						Indentation: "      ",
					},
					{
						Node:        "operation",
						Value:       "Apply",
						DiffType:    kube.NoDiff,
						Indentation: "      ",
					},
					{
						Node:        "- apiVersion",
						Value:       "v1",
						DiffType:    kube.NoDiff,
						Indentation: "    ",
					},
					{
						Node:        "fieldsType",
						Value:       "FieldsV1",
						DiffType:    kube.NoDiff,
						Indentation: "      ",
					},
					{
						Node:        "fieldsV1",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "      ",
					},
					{
						Node:        "f:metadata",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "        ",
					},
					{
						Node:        "f:labels",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "          ",
					},
					{
						Node:        ".",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "            ",
					},
					{
						Node:        "f:kubernetes.io/metadata.name",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "            ",
					},
					{
						Node:        "manager",
						Value:       "kube.test",
						DiffType:    kube.NoDiff,
						Indentation: "      ",
					},
					{
						Node:        "operation",
						Value:       "Update",
						DiffType:    kube.NoDiff,
						Indentation: "      ",
					},
					{
						Node:        "name",
						Value:       "test",
						DiffType:    kube.NoDiff,
						Indentation: "  ",
					},
					{
						Node:        "spec",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "",
					},
					{
						Node:        "finalizers",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "  ",
					},
					{
						Node:        "- kubernetes",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "    ",
					},
					{
						Node:        "status",
						Value:       "",
						DiffType:    kube.NoDiff,
						Indentation: "",
					},
					{
						Node:        "phase",
						Value:       "Active",
						DiffType:    kube.NoDiff,
						Indentation: "  ",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			kubernetes := kubetest.StartKubetestEnv(t, logr.Discard(), kubetest.WithEnabled(true))
			defer kubernetes.Stop()

			dynClient := kubernetes.DynamicTestKubeClient.DynamicClient()
			ctx := context.Background()

			_, err := dynClient.Apply(
				ctx,
				tc.haveUnstructured,
				"controller",
				kube.ForceApply(true),
			)
			assert.NilError(t, err)

			differ := kube.Differ{
				KubeClient:   *dynClient,
				FieldManager: "test",
			}

			diff, err := differ.Diff(ctx, tc.haveChange)
			assert.NilError(t, err)

			fmt.Print(diff)

			assert.Equal(t, diff.GVK, tc.haveUnstructured.GroupVersionKind())
			assert.Equal(t, diff.GVK, tc.wantDifference.GVK)

			// assert.Equal(t, len(diff.Lines), len(tc.wantDifference.Lines))
			assert.DeepEqual(
				t,
				diff,
				tc.wantDifference,
				cmpopts.IgnoreSliceElements(func(line kube.DiffLine) bool {
					return line.Node == "creationTimestamp" || line.Node == "time" ||
						line.Node == "resourceVersion" || line.Node == "uid"
				}),
			)
		})
	}
}
