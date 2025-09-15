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
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/pkg/kube"
	"gotest.tools/v3/assert"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type update struct {
	manager string
	unstr   *unstructured.Unstructured
}

func TestExtendedDynamicClient_Apply(t *testing.T) {
	testCases := []struct {
		name                           string
		haveUnstructured               *unstructured.Unstructured
		haveUnstructuredForeignUpdates []update
		haveUnstructuredUpdate         update
		wantUnstructured               *unstructured.Unstructured
		wantErr                        string
	}{
		{
			name: "Apply-With-Manual-Change-Overwrite",
			haveUnstructured: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "test",
					},
					"spec": map[string]any{
						"selector": map[string]any{
							"matchLabels": map[string]string{
								"app": "test",
							},
						},
						"replicas": 2,
						"template": map[string]any{
							"metadata": map[string]any{
								"labels": map[string]string{
									"app": "test",
								},
							},
							"spec": map[string]any{
								"securityContext": map[string]any{
									"runAsNonRoot":        false,
									"fsGroup":             0,
									"fsGroupChangePolicy": "Always",
								},
								"containers": []map[string]any{
									{
										"image": "test",
										"name":  "test",
									},
								},
							},
						},
					},
				},
			},
			haveUnstructuredForeignUpdates: []update{
				{
					manager: "kubectl",
					unstr: &unstructured.Unstructured{
						Object: map[string]any{
							"apiVersion": "apps/v1",
							"kind":       "Deployment",
							"metadata": map[string]any{
								"name":      "test",
								"namespace": "test",
							},
							"spec": map[string]any{
								"selector": map[string]any{
									"matchLabels": map[string]string{
										"app": "test",
									},
								},
								"template": map[string]any{
									"metadata": map[string]any{
										"labels": map[string]string{
											"app": "test",
										},
									},
									"spec": map[string]any{
										"containers": []map[string]any{
											{
												"name": "test",
												"env": []map[string]any{
													{
														"name":  "test",
														"value": "test",
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
				{
					manager: "kubectl-edit",
					unstr: &unstructured.Unstructured{
						Object: map[string]any{
							"apiVersion": "apps/v1",
							"kind":       "Deployment",
							"metadata": map[string]any{
								"name":      "test",
								"namespace": "test",
							},
							"spec": map[string]any{
								"selector": map[string]any{
									"matchLabels": map[string]string{
										"app": "test",
									},
								},
								"template": map[string]any{
									"metadata": map[string]any{
										"labels": map[string]string{
											"app": "test",
										},
									},
									"spec": map[string]any{
										"dnsPolicy": "Default",
									},
								},
							},
						},
					},
				},
			},
			haveUnstructuredUpdate: update{
				manager: "controller",
				unstr: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]any{
							"name":      "test",
							"namespace": "test",
						},
						"spec": map[string]any{
							"replicas": 2,
							"selector": map[string]any{
								"matchLabels": map[string]string{
									"app": "test",
								},
							},
							"template": map[string]any{
								"metadata": map[string]any{
									"labels": map[string]string{
										"app": "test",
									},
								},
								"spec": map[string]any{
									"securityContext": map[string]any{
										"runAsNonRoot":        false,
										"fsGroup":             0,
										"fsGroupChangePolicy": "Always",
									},
									"containers": []map[string]any{
										{
											"image": "test",
											"name":  "test",
										},
									},
								},
							},
						},
					},
				},
			},
			wantUnstructured: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "test",
						"managedFields": []v1.ManagedFieldsEntry{
							{
								APIVersion: "apps/v1",
								Manager:    "controller",
								Operation:  v1.ManagedFieldsOperationApply,
								FieldsType: "FieldsV1",
								FieldsV1: &v1.FieldsV1{
									Raw: []byte(
										`{"f:spec":{"f:replicas":{},"f:selector":{},"f:template":{"f:metadata":{"f:labels":{"f:app":{}}},"f:spec":{"f:containers":{"k:{\"name\":\"test\"}":{".":{},"f:image":{},"f:name":{}}},"f:securityContext":{"f:fsGroup":{},"f:fsGroupChangePolicy":{},"f:runAsNonRoot":{}}}}}}`,
									),
								},
							},
						},
					},
					"spec": map[string]any{
						"progressDeadlineSeconds": int64(600),
						"replicas":                int64(2),
						"revisionHistoryLimit":    int64(10),
						"selector": map[string]any{
							"matchLabels": map[string]any{
								"app": "test",
							},
						},
						"strategy": map[string]any{
							"rollingUpdate": map[string]any{
								"maxSurge":       "25%",
								"maxUnavailable": "25%",
							},
							"type": "RollingUpdate",
						},
						"template": map[string]any{
							"metadata": map[string]any{
								"labels": map[string]any{
									"app": "test",
								},
							},
							"spec": map[string]any{
								"dnsPolicy":     "ClusterFirst",
								"restartPolicy": "Always",
								"schedulerName": "default-scheduler",
								"securityContext": map[string]any{
									"fsGroup":             int64(0),
									"fsGroupChangePolicy": "Always",
									"runAsNonRoot":        false,
								},
								"terminationGracePeriodSeconds": int64(30),
								"containers": []any{
									map[string]any{
										"image":                    "test",
										"imagePullPolicy":          "Always",
										"name":                     "test",
										"resources":                map[string]any{},
										"terminationMessagePath":   "/dev/termination-log",
										"terminationMessagePolicy": "File",
									},
								},
							},
						},
					},
				},
			},
		}, {
			name: "Apply-With-Other-Field-Managers",
			haveUnstructured: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "test",
					},
					"spec": map[string]any{
						"selector": map[string]any{
							"matchLabels": map[string]string{
								"app": "test",
							},
						},
						"replicas": 2,
						"template": map[string]any{
							"metadata": map[string]any{
								"labels": map[string]string{
									"app": "test",
								},
							},
							"spec": map[string]any{
								"securityContext": map[string]any{
									"runAsNonRoot":        false,
									"fsGroup":             0,
									"fsGroupChangePolicy": "Always",
								},
								"containers": []map[string]any{
									{
										"image": "test",
										"name":  "test",
									},
								},
							},
						},
					},
				},
			},
			haveUnstructuredForeignUpdates: []update{
				{
					manager: "kube-controller-manager",
					unstr: &unstructured.Unstructured{
						Object: map[string]any{
							"apiVersion": "apps/v1",
							"kind":       "Deployment",
							"metadata": map[string]any{
								"name":      "test",
								"namespace": "test",
							},
							"spec": map[string]any{
								"selector": map[string]any{
									"matchLabels": map[string]string{
										"app": "test",
									},
								},
								"template": map[string]any{
									"metadata": map[string]any{
										"labels": map[string]string{
											"app": "test",
										},
									},
									"spec": map[string]any{
										"containers": []map[string]any{
											{
												"name": "test",
												"env": []map[string]any{
													{
														"name":  "test",
														"value": "test",
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
			haveUnstructuredUpdate: update{
				manager: "controller",
				unstr: &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"metadata": map[string]any{
							"name":      "test",
							"namespace": "test",
						},
						"spec": map[string]any{
							"replicas": 2,
							"selector": map[string]any{
								"matchLabels": map[string]string{
									"app": "test",
								},
							},
							"template": map[string]any{
								"metadata": map[string]any{
									"labels": map[string]string{
										"app": "test",
									},
								},
								"spec": map[string]any{
									"securityContext": map[string]any{
										"runAsNonRoot":        false,
										"fsGroup":             0,
										"fsGroupChangePolicy": "Always",
									},
									"containers": []map[string]any{
										{
											"image": "test",
											"name":  "test",
										},
									},
								},
							},
						},
					},
				},
			},
			wantUnstructured: &unstructured.Unstructured{
				Object: map[string]any{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]any{
						"name":      "test",
						"namespace": "test",
						"managedFields": []v1.ManagedFieldsEntry{
							{
								APIVersion: "apps/v1",
								Manager:    "controller",
								Operation:  v1.ManagedFieldsOperationApply,
								FieldsType: "FieldsV1",
								FieldsV1: &v1.FieldsV1{
									Raw: []byte(
										`{"f:spec":{"f:replicas":{},"f:selector":{},"f:template":{"f:metadata":{"f:labels":{"f:app":{}}},"f:spec":{"f:containers":{"k:{\"name\":\"test\"}":{".":{},"f:image":{},"f:name":{}}},"f:securityContext":{"f:fsGroup":{},"f:fsGroupChangePolicy":{},"f:runAsNonRoot":{}}}}}}`,
									),
								},
							},
							{
								APIVersion: "apps/v1",
								Manager:    "kube-controller-manager",
								Operation:  v1.ManagedFieldsOperationApply,
								FieldsType: "FieldsV1",
								FieldsV1: &v1.FieldsV1{
									Raw: []byte(
										`{"f:spec":{"f:selector":{},"f:template":{"f:metadata":{"f:labels":{"f:app":{}}},"f:spec":{"f:containers":{"k:{\"name\":\"test\"}":{".":{},"f:env":{"k:{\"name\":\"test\"}":{".":{},"f:name":{},"f:value":{}}},"f:name":{}}}}}}}`,
									),
								},
							},
						},
					},
					"spec": map[string]any{
						"progressDeadlineSeconds": int64(600),
						"replicas":                int64(2),
						"revisionHistoryLimit":    int64(10),
						"selector": map[string]any{
							"matchLabels": map[string]any{
								"app": "test",
							},
						},
						"strategy": map[string]any{
							"rollingUpdate": map[string]any{
								"maxSurge":       "25%",
								"maxUnavailable": "25%",
							},
							"type": "RollingUpdate",
						},
						"template": map[string]any{
							"metadata": map[string]any{
								"labels": map[string]any{
									"app": "test",
								},
							},
							"spec": map[string]any{
								"dnsPolicy":     "ClusterFirst",
								"restartPolicy": "Always",
								"schedulerName": "default-scheduler",
								"securityContext": map[string]any{
									"fsGroup":             int64(0),
									"fsGroupChangePolicy": "Always",
									"runAsNonRoot":        false,
								},
								"terminationGracePeriodSeconds": int64(30),
								"containers": []any{
									map[string]any{
										"image":                    "test",
										"imagePullPolicy":          "Always",
										"name":                     "test",
										"resources":                map[string]any{},
										"terminationMessagePath":   "/dev/termination-log",
										"terminationMessagePolicy": "File",
										"env": []any{
											map[string]any{
												"name":  "test",
												"value": "test",
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

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			kubernetes := kubetest.StartKubetestEnv(t, logr.Discard(), kubetest.WithEnabled(true))
			defer kubernetes.Stop()

			dynClient := kubernetes.DynamicTestKubeClient.DynamicClient()
			ctx := context.Background()

			if tc.haveUnstructured.GetKind() != "Namespace" {
				ns := &unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": "v1",
						"kind":       "Namespace",
						"metadata": map[string]any{
							"name": tc.haveUnstructured.GetNamespace(),
						},
					},
				}
				_, err := dynClient.Apply(
					ctx,
					ns,
					"controller",
					kube.ForceApply(true),
				)
				assert.NilError(t, err)
			}

			_, err := dynClient.Apply(
				ctx,
				tc.haveUnstructured,
				"controller",
				kube.ForceApply(true),
			)
			assert.NilError(t, err)

			for _, update := range tc.haveUnstructuredForeignUpdates {
				_, err = dynClient.Apply(
					ctx,
					update.unstr,
					update.manager,
					kube.ForceApply(true),
				)
				assert.NilError(t, err)
			}

			appliedUnstr, err := kubernetes.DynamicTestKubeClient.Apply(
				ctx,
				&kube.ExtendedUnstructured{
					Unstructured: tc.haveUnstructuredUpdate.unstr,
				},
				tc.haveUnstructuredUpdate.manager,
				kube.ForceApply(true),
			)

			if tc.wantErr != "" {
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}

			assert.NilError(t, err)

			assert.Equal(t, appliedUnstr.GetName(), tc.wantUnstructured.GetName())
			assert.Equal(t, appliedUnstr.GetNamespace(), tc.wantUnstructured.GetNamespace())
			slices.SortFunc(
				appliedUnstr.GetManagedFields(),
				func(a v1.ManagedFieldsEntry, b v1.ManagedFieldsEntry) int {
					return strings.Compare(a.Manager, b.Manager)
				},
			)
			wantManagedFields := tc.wantUnstructured.Object["metadata"].(map[string]any)["managedFields"].([]v1.ManagedFieldsEntry)
			slices.SortFunc(
				wantManagedFields,
				func(a v1.ManagedFieldsEntry, b v1.ManagedFieldsEntry) int {
					return strings.Compare(a.Manager, b.Manager)
				},
			)

			assert.DeepEqual(
				t,
				appliedUnstr.GetManagedFields(),
				wantManagedFields,
				cmpopts.IgnoreFields(v1.ManagedFieldsEntry{}, "Time"),
			)

			assert.DeepEqual(t, appliedUnstr.Object["spec"], tc.wantUnstructured.Object["spec"])
		})
	}
}
