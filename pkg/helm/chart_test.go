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

package helm_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"go.uber.org/zap/zapcore"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrlZap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	"gotest.tools/v3/assert"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"

	"github.com/kharf/navecd/internal/cloudtest"
	"github.com/kharf/navecd/internal/dnstest"
	"github.com/kharf/navecd/internal/helmtest"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/internal/ocitest"
	"github.com/kharf/navecd/pkg/cloud"
	"github.com/kharf/navecd/pkg/helm"
	. "github.com/kharf/navecd/pkg/helm"
	"github.com/kharf/navecd/pkg/inventory"
	"github.com/kharf/navecd/pkg/kube"
)

func newHelmEnvironment(
	t *testing.T,
	oci bool,
	private bool,
	cloudProvider cloud.ProviderID,
	digest string,
) *helmtest.Environment {
	helmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(oci),
		helmtest.WithPrivate(private),
		helmtest.WithProvider(cloudProvider),
		helmtest.WithDigest(digest),
	)
	assert.NilError(t, err)
	return helmEnvironment
}

func applyRepoAuthSecret(
	t *testing.T,
	ctx context.Context,
	name string,
	namespace string,
	client *kube.DynamicClient,
) {
	unstr := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"data": map[string][]byte{
				"username": []byte("navecd"),
				"password": []byte("abcd"),
			},
		},
	}
	_, err := client.Apply(
		ctx,
		&unstr,
		"charttest",
	)
	assert.NilError(t, err)
}

func createReleaseDeclaration(
	namespace string,
	url string,
	version string,
	auth *cloud.Auth,
	allowUpgrade bool,
	values Values,
	patches *Patches,
) ReleaseDeclaration {
	release := helm.ReleaseDeclaration{
		Name:      "test",
		Namespace: namespace,
		CRDs: CRDs{
			AllowUpgrade: allowUpgrade,
		},
		Chart: &Chart{
			Name:    "test",
			RepoURL: url,
			Version: version,
			Auth:    auth,
		},
		Values:  values,
		Patches: patches,
	}
	return release
}

func assertChartv1(
	t *testing.T,
	env *kubetest.Environment,
	liveName string,
	namespace string,
	replicas int32,
) {
	ctx := context.Background()
	var deployment appsv1.Deployment
	err := env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&deployment,
	)
	assert.NilError(t, err)

	gracePeriodSeconds := int64(30)
	historyLimit := int32(10)
	progressDeadlineSeconds := int32(600)
	expectedDeployment := appsv1.Deployment{
		ObjectMeta: v1.ObjectMeta{
			Name:      "test",
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/instance":   "test",
				"app.kubernetes.io/managed-by": "Helm",
				"app.kubernetes.io/name":       "test",
				"app.kubernetes.io/version":    "1.16.0",
				"helm.sh/chart":                "test-1.0.0",
			},
			Annotations: map[string]string{
				"meta.helm.sh/release-name":      "test",
				"meta.helm.sh/release-namespace": namespace,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &v1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/instance": "test",
					"app.kubernetes.io/name":     "test",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/instance": "test",
						"app.kubernetes.io/name":     "test",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "test",
							Image: "nginx:1.16.0",
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/",
										Port: intstr.IntOrString{
											Type:   intstr.String,
											StrVal: "http",
										},
										Scheme: corev1.URISchemeHTTP,
									},
								},
								InitialDelaySeconds: 0,
								TimeoutSeconds:      1,
								PeriodSeconds:       10,
								SuccessThreshold:    1,
								FailureThreshold:    3,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/",
										Port: intstr.IntOrString{
											Type:   intstr.String,
											StrVal: "http",
										},
										Scheme: corev1.URISchemeHTTP,
									},
								},
								InitialDelaySeconds: 0,
								TimeoutSeconds:      1,
								PeriodSeconds:       10,
								SuccessThreshold:    1,
								FailureThreshold:    3,
							},
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: int32(80),
									Protocol:      "TCP",
								},
							},
							TerminationMessagePath:   "/dev/termination-log",
							TerminationMessagePolicy: "File",
							ImagePullPolicy:          "IfNotPresent",
							SecurityContext:          &corev1.SecurityContext{},
						},
					},
					SecurityContext:               &corev1.PodSecurityContext{},
					RestartPolicy:                 corev1.RestartPolicyAlways,
					TerminationGracePeriodSeconds: &gracePeriodSeconds,
					DNSPolicy:                     "ClusterFirst",
					ServiceAccountName:            "test",
					DeprecatedServiceAccount:      "test",
					SchedulerName:                 "default-scheduler",
				},
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: "RollingUpdate",
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &intstr.IntOrString{Type: intstr.String, StrVal: "25%"},
					MaxSurge:       &intstr.IntOrString{Type: intstr.String, StrVal: "25%"},
				},
			},
			MinReadySeconds:         0,
			RevisionHistoryLimit:    &historyLimit,
			Paused:                  false,
			ProgressDeadlineSeconds: &progressDeadlineSeconds,
		},
	}

	EqualDeployment(t, deployment, expectedDeployment)

	var svc corev1.Service
	err = env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&svc,
	)
	assert.NilError(t, err)
	assert.Equal(t, svc.Name, liveName)
	assert.Equal(t, svc.Namespace, namespace)
	assert.Equal(t, svc.Spec.Type, corev1.ServiceTypeClusterIP)

	var svcAcc corev1.ServiceAccount
	err = env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&svcAcc,
	)
	assert.NilError(t, err)
	assert.Equal(t, svcAcc.Name, liveName)
	assert.Equal(t, svcAcc.Namespace, namespace)
	assertCRDNoChanges(t, ctx, env.DynamicTestKubeClient.DynamicClient())
}

func assertChartv1Patches(
	t *testing.T,
	env *kubetest.Environment,
	liveName string,
	namespace string,
) {
	ctx := context.Background()
	var deployment appsv1.Deployment
	err := env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&deployment,
	)
	assert.NilError(t, err)

	expectedReplicas := int32(2)
	gracePeriodSeconds := int64(30)
	historyLimit := int32(10)
	progressDeadlineSeconds := int32(600)
	expectedDeployment := appsv1.Deployment{
		ObjectMeta: v1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
			Labels: map[string]string{
				"app.kubernetes.io/instance":   "test",
				"app.kubernetes.io/managed-by": "Helm",
				"app.kubernetes.io/name":       "test",
				"app.kubernetes.io/version":    "1.16.0",
				"helm.sh/chart":                "test-1.0.0",
			},
			Annotations: map[string]string{
				"meta.helm.sh/release-name":      "test",
				"meta.helm.sh/release-namespace": "default",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &expectedReplicas,
			Selector: &v1.LabelSelector{
				MatchLabels: map[string]string{
					"app.kubernetes.io/instance": "test",
					"app.kubernetes.io/name":     "test",
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: v1.ObjectMeta{
					Labels: map[string]string{
						"app.kubernetes.io/instance": "test",
						"app.kubernetes.io/name":     "test",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "prometheus",
							Image: "prometheus:1.14.2",
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: int32(80),
									Protocol:      "TCP",
								},
							},
							TerminationMessagePath:   "/dev/termination-log",
							TerminationMessagePolicy: "File",
							ImagePullPolicy:          "IfNotPresent",
						},
						{
							Name:  "sidecar",
							Image: "sidecar:1.14.2",
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: int32(80),
									Protocol:      "TCP",
								},
							},
							TerminationMessagePath:   "/dev/termination-log",
							TerminationMessagePolicy: "File",
							ImagePullPolicy:          "IfNotPresent",
						},
					},
					SecurityContext:               &corev1.PodSecurityContext{},
					RestartPolicy:                 corev1.RestartPolicyAlways,
					TerminationGracePeriodSeconds: &gracePeriodSeconds,
					DNSPolicy:                     "ClusterFirst",
					ServiceAccountName:            "test",
					DeprecatedServiceAccount:      "test",
					SchedulerName:                 "default-scheduler",
				},
			},
			Strategy: appsv1.DeploymentStrategy{
				Type: "RollingUpdate",
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: &intstr.IntOrString{Type: intstr.String, StrVal: "25%"},
					MaxSurge:       &intstr.IntOrString{Type: intstr.String, StrVal: "25%"},
				},
			},
			MinReadySeconds:         0,
			RevisionHistoryLimit:    &historyLimit,
			Paused:                  false,
			ProgressDeadlineSeconds: &progressDeadlineSeconds,
		},
	}

	EqualDeployment(t, deployment, expectedDeployment)

	var svc corev1.Service
	err = env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&svc,
	)
	assert.NilError(t, err)
	assert.Equal(t, svc.Name, liveName)
	assert.Equal(t, svc.Namespace, namespace)
	assert.Equal(t, svc.Spec.Type, corev1.ServiceTypeNodePort)

	var svcAcc corev1.ServiceAccount
	err = env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&svcAcc,
	)
	assert.NilError(t, err)
	assert.Equal(t, svcAcc.Name, liveName)
	assert.Equal(t, svcAcc.Namespace, namespace)
	assertCRDNoChanges(t, ctx, env.DynamicTestKubeClient.DynamicClient())
}

func EqualDeployment(
	t *testing.T,
	actual appsv1.Deployment,
	expected appsv1.Deployment,
) {
	actual.UID = ""
	actual.ResourceVersion = ""
	actual.Generation = 0
	actual.CreationTimestamp = v1.Time{}
	actual.ManagedFields = nil

	assert.DeepEqual(t, actual, expected)
}

func assertCRDNoChanges(t *testing.T, ctx context.Context, dynamicClient *kube.DynamicClient) {
	crontabCRD := &unstructured.Unstructured{}
	crontabCRDName := "crontabs.stable.example.com"
	crontabCRD.SetName(crontabCRDName)
	crontabCRD.SetAPIVersion("apiextensions.k8s.io/v1")
	crontabCRD.SetKind("CustomResourceDefinition")
	crontabCRD, err := dynamicClient.Get(ctx, crontabCRD)
	assert.NilError(t, err)

	replicas, _ := getReplicas(crontabCRD)
	propType, _ := replicas["type"].(string)
	assert.Equal(t, propType, "integer")
}

func getReplicas(crontabCRD *unstructured.Unstructured) (map[string]interface{}, bool) {
	spec, _ := crontabCRD.Object["spec"].(map[string]interface{})
	versions, _ := spec["versions"].([]interface{})
	version, _ := versions[0].(map[string]interface{})
	schema, _ := version["schema"].(map[string]interface{})
	openAPISchema, _ := schema["openAPIV3Schema"].(map[string]interface{})
	properties, _ := openAPISchema["properties"].(map[string]interface{})
	propSpec, _ := properties["spec"].(map[string]interface{})
	specProperties, _ := propSpec["properties"].(map[string]interface{})
	replicas, ok := specProperties["replicas"].(map[string]interface{})
	return replicas, ok
}

func assertChartv2(t *testing.T, env *kubetest.Environment, liveName string, namespace string) {
	ctx := context.Background()
	var deployment appsv1.Deployment
	err := env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&deployment,
	)
	assert.NilError(t, err)
	assert.Equal(t, deployment.Name, liveName)
	assert.Equal(t, deployment.Namespace, namespace)
	var hpa autoscalingv2.HorizontalPodAutoscaler
	err = env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&hpa,
	)
	assert.Error(t, err, "horizontalpodautoscalers.autoscaling \"test\" not found")
	var svc corev1.Service
	err = env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&svc,
	)
	assert.NilError(t, err)
	assert.Equal(t, svc.Name, liveName)
	assert.Equal(t, svc.Namespace, namespace)
	var svcAcc corev1.ServiceAccount
	err = env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&svcAcc,
	)
	assert.Error(t, err, "serviceaccounts \"test\" not found")
	assertCRDNoChanges(t, ctx, env.DynamicTestKubeClient.DynamicClient())
}

func assertChartv3(t *testing.T, env *kubetest.Environment, liveName string, namespace string) {
	ctx := context.Background()
	var deployment appsv1.Deployment
	err := env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&deployment,
	)
	assert.NilError(t, err)
	assert.Equal(t, deployment.Name, liveName)
	assert.Equal(t, deployment.Namespace, namespace)
	var hpa autoscalingv2.HorizontalPodAutoscaler
	err = env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&hpa,
	)
	assert.Error(t, err, "horizontalpodautoscalers.autoscaling \"test\" not found")
	var svc corev1.Service
	err = env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&svc,
	)
	assert.NilError(t, err)
	assert.Equal(t, svc.Name, liveName)
	assert.Equal(t, svc.Namespace, namespace)
	var svcAcc corev1.ServiceAccount
	err = env.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: liveName, Namespace: namespace},
		&svcAcc,
	)
	assert.Error(t, err, "serviceaccounts \"test\" not found")
	assertCRDChartv3(t, ctx, env.DynamicTestKubeClient.DynamicClient())
}

func assertCRDChartv3(t *testing.T, ctx context.Context, dynamicClient *kube.DynamicClient) {
	crontabCRD := &unstructured.Unstructured{}
	crontabCRDName := "crontabs.stable.example.com"
	crontabCRD.SetName(crontabCRDName)
	crontabCRD.SetAPIVersion("apiextensions.k8s.io/v1")
	crontabCRD.SetKind("CustomResourceDefinition")
	crontabCRD, err := dynamicClient.Get(ctx, crontabCRD)
	assert.NilError(t, err)

	_, ok := getReplicas(crontabCRD)
	assert.Assert(t, !ok)
}

func TestChartReconciler_Reconcile_HTTP(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment := newHelmEnvironment(t, false, false, "", "digest")
	defer publicHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		publicHelmEnvironment.ChartServer.URL(),
		"1.0.0@digest",
		nil,
		false,
		Values{
			"autoscaling": map[string]any{
				"enabled": true,
			},
		},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv1(t, kubernetes, release.Name, release.Namespace, 1)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())

	var hpa autoscalingv2.HorizontalPodAutoscaler
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{
			Name:      releaseDeclaration.Name,
			Namespace: releaseDeclaration.Namespace,
		},
		&hpa,
	)
	assert.NilError(t, err)
	assert.Equal(t, hpa.Name, releaseDeclaration.Name)
	assert.Equal(t, hpa.Namespace, releaseDeclaration.Namespace)
}

func TestChartReconciler_Reconcile_HTTPAuthSecretNotFound(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment := newHelmEnvironment(t, false, false, "", "")
	defer publicHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		publicHelmEnvironment.ChartServer.URL(),
		"1.0.0",
		&cloud.Auth{
			SecretRef: &cloud.SecretRef{
				Name: "repauth",
			},
		},
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	_, err = chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.Error(t, err, "secrets \"repauth\" not found")
}

func TestChartReconciler_Reconcile_HTTPAuthSecretRefNotFound(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment := newHelmEnvironment(t, false, false, "", "")
	defer publicHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		publicHelmEnvironment.ChartServer.URL(),
		"1.0.0",
		&cloud.Auth{
			SecretRef: nil,
		},
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	_, err = chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.ErrorIs(t, err, cloud.ErrSecretRefNotSet)
}

func TestChartReconciler_Reconcile_HTTPAuth(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	privateHelmEnvironment := newHelmEnvironment(t, false, true, "", "")
	defer privateHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		privateHelmEnvironment.ChartServer.URL(),
		"1.0.0",
		&cloud.Auth{
			SecretRef: &cloud.SecretRef{
				Name: "auth",
			},
		},
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	applyRepoAuthSecret(
		t,
		ctx,
		releaseDeclaration.Chart.Auth.SecretRef.Name,
		releaseDeclaration.Namespace,
		kubernetes.DynamicTestKubeClient.DynamicClient(),
	)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv1(t, kubernetes, release.Name, release.Namespace, 1)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())
}

func TestChartReconciler_Reconcile_OCI(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer registry.Close()

	helmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(true),
		helmtest.WithPrivate(false),
		helmtest.WithDigest(""),
		helmtest.WithRegistry(*registry),
	)
	assert.NilError(t, err)
	defer helmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		helmEnvironment.ChartServer.URL(),
		fmt.Sprintf("%s@%s", "1.0.0", helmEnvironment.V1Digest),
		nil,
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv1(t, kubernetes, release.Name, release.Namespace, 1)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())
}

func TestChartReconciler_Reconcile_OCIAuthSecretNotFound(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer registry.Close()

	helmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(true),
		helmtest.WithPrivate(true),
		helmtest.WithDigest(""),
		helmtest.WithRegistry(*registry),
	)
	assert.NilError(t, err)
	defer helmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		helmEnvironment.ChartServer.URL(),
		"1.0.0",
		&cloud.Auth{
			SecretRef: &cloud.SecretRef{
				Name: "regauth",
			},
		},
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	_, err = chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.Error(t, err, "secrets \"regauth\" not found")
}

func TestChartReconciler_Reconcile_OCIAuthSecretRefNotFound(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer registry.Close()

	helmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(true),
		helmtest.WithPrivate(true),
		helmtest.WithDigest(""),
		helmtest.WithRegistry(*registry),
	)
	assert.NilError(t, err)
	defer helmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		helmEnvironment.ChartServer.URL(),
		"1.0.0",
		&cloud.Auth{
			SecretRef: nil,
		},
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	_, err = chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.ErrorIs(t, err, cloud.ErrSecretRefNotSet)
}

func TestChartReconciler_Reconcile_OCIAuth(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer registry.Close()

	helmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(true),
		helmtest.WithPrivate(true),
		helmtest.WithDigest(""),
		helmtest.WithRegistry(*registry),
	)
	assert.NilError(t, err)
	defer helmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		helmEnvironment.ChartServer.URL(),
		"1.0.0",
		&cloud.Auth{
			SecretRef: &cloud.SecretRef{
				Name: "auth",
			},
		},
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	applyRepoAuthSecret(
		t,
		ctx,
		releaseDeclaration.Chart.Auth.SecretRef.Name,
		releaseDeclaration.Namespace,
		kubernetes.DynamicTestKubeClient.DynamicClient(),
	)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv1(t, kubernetes, release.Name, release.Namespace, 1)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())
}

func TestChartReconciler_Reconcile_OCIGCPWorkloadIdentity(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer registry.Close()

	helmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(true),
		helmtest.WithPrivate(true),
		helmtest.WithProvider(cloud.GCP),
		helmtest.WithDigest(""),
		helmtest.WithRegistry(*registry),
	)
	assert.NilError(t, err)
	defer helmEnvironment.Close()
	gcpCloudEnvironment, err := cloudtest.NewGCPEnvironment()
	assert.NilError(t, err)
	defer gcpCloudEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		helmEnvironment.ChartServer.URL(),
		"1.0.0",
		&cloud.Auth{
			WorkloadIdentity: &cloud.WorkloadIdentity{
				Provider: cloud.GCP,
			},
		},
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
		GCPMetadataServerURL:  gcpCloudEnvironment.HttpsServer.URL,
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv1(t, kubernetes, release.Name, release.Namespace, 1)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())
}

func TestChartReconciler_Reconcile_OCIAWSWorkloadIdentity(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer registry.Close()

	helmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(true),
		helmtest.WithPrivate(true),
		helmtest.WithProvider(cloud.AWS),
		helmtest.WithDigest(""),
		helmtest.WithRegistry(*registry),
	)
	assert.NilError(t, err)
	defer helmEnvironment.Close()

	awsEnvironment, err := cloudtest.NewAWSEnvironment(
		helmEnvironment.ChartServer.Addr(),
	)
	assert.NilError(t, err)
	defer awsEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		fmt.Sprintf("oci://%s", awsEnvironment.RegistryAddr()),
		"1.0.0",
		&cloud.Auth{
			WorkloadIdentity: &cloud.WorkloadIdentity{
				Provider: cloud.AWS,
			},
		},
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv1(t, kubernetes, release.Name, release.Namespace, 1)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())
}

func TestChartReconciler_Reconcile_OCIAzureWorkloadIdentity(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	registry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer registry.Close()

	helmEnvironment, err := helmtest.NewHelmEnvironment(
		t,
		helmtest.WithOCI(true),
		helmtest.WithPrivate(true),
		helmtest.WithProvider(cloud.Azure),
		helmtest.WithDigest(""),
		helmtest.WithRegistry(*registry),
	)
	assert.NilError(t, err)
	defer helmEnvironment.Close()

	azureEnvironment, err := cloudtest.NewAzureEnvironment()
	assert.NilError(t, err)
	defer azureEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		helmEnvironment.ChartServer.URL(),
		"1.0.0",
		&cloud.Auth{
			WorkloadIdentity: &cloud.WorkloadIdentity{
				Provider: cloud.Azure,
			},
		},
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
		AzureLoginURL:         azureEnvironment.OIDCIssuerServer.URL,
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv1(t, kubernetes, release.Name, release.Namespace, 1)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())
}

func TestChartReconciler_Reconcile_Namespaced(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment := newHelmEnvironment(t, false, false, "", "")
	defer publicHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"mynamespace",
		publicHelmEnvironment.ChartServer.URL(),
		"1.0.0",
		nil,
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv1(t, kubernetes, release.Name, release.Namespace, 1)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())
}

func TestChartReconciler_Reconcile_Cached(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	flakyHelmEnvironment := newHelmEnvironment(t, false, false, "", "")
	defer flakyHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		flakyHelmEnvironment.ChartServer.URL(),
		"1.0.0",
		nil,
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv1(t, kubernetes, release.Name, release.Namespace, 1)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())

	flakyHelmEnvironment.ChartServer.Close()

	err = kubernetes.TestKubeClient.Delete(ctx, &appsv1.Deployment{
		ObjectMeta: v1.ObjectMeta{
			Name:      "test",
			Namespace: "default",
		},
	})
	assert.NilError(t, err)

	var deployment appsv1.Deployment
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "test", Namespace: "default"},
		&deployment,
	)
	assert.Error(t, err, "deployments.apps \"test\" not found")

	actualRelease, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)

	assertChartv1(
		t,
		kubernetes,
		actualRelease.Name,
		actualRelease.Namespace,
		1,
	)
	assert.Equal(t, actualRelease.Version, 2)
}

func TestChartReconciler_Reconcile_InstallPatches(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment := newHelmEnvironment(t, false, false, "", "")
	defer publicHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		publicHelmEnvironment.ChartServer.URL(),
		"1.0.0",
		nil,
		false,
		Values{},
		&helm.Patches{
			Unstructureds: map[string]kube.ExtendedUnstructured{
				"v1-Service-default-test": {
					Unstructured: &unstructured.Unstructured{
						Object: map[string]interface{}{
							"apiVersion": "v1",
							"kind":       "Service",
							"metadata": map[string]any{
								"name":      "test",
								"namespace": "default",
							},
							"spec": map[string]any{
								"type": "NodePort",
							},
						},
					},
				},
				"apps/v1-Deployment-default-test": {
					Unstructured: &unstructured.Unstructured{
						Object: map[string]interface{}{
							"apiVersion": "apps/v1",
							"kind":       "Deployment",
							"metadata": map[string]any{
								"name": "test",
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
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv1Patches(t, kubernetes, release.Name, release.Namespace)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())
}

func TestChartReconciler_Reconcile_Upgrade(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment := newHelmEnvironment(t, false, false, "", "digest")
	defer publicHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		publicHelmEnvironment.ChartServer.URL(),
		"1.0.0@digest",
		nil,
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv1(t, kubernetes, release.Name, release.Namespace, 1)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())

	chart := &Chart{
		Name:    "test",
		RepoURL: publicHelmEnvironment.ChartServer.URL(),
		Version: "2.0.0@digest",
	}

	releaseDeclaration.Chart = chart
	actualRelease, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)

	assertChartv2(
		t,
		kubernetes,
		actualRelease.Name,
		actualRelease.Namespace,
	)
	assert.Equal(t, actualRelease.Version, 2)
}

func TestChartReconciler_Reconcile_UpgradeCRDs(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment := newHelmEnvironment(t, false, false, "", "")
	defer publicHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		publicHelmEnvironment.ChartServer.URL(),
		"2.0.0",
		nil,
		true,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv2(t, kubernetes, release.Name, release.Namespace)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())

	chart := &Chart{
		Name:    "test",
		RepoURL: publicHelmEnvironment.ChartServer.URL(),
		Version: "3.0.0",
	}

	releaseDeclaration.Chart = chart
	actualRelease, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)

	assertChartv3(
		t,
		kubernetes,
		actualRelease.Name,
		actualRelease.Namespace,
	)
	assert.Equal(t, actualRelease.Version, 2)
}

func TestChartReconciler_Reconcile_UpgradeCRDsBreakingChangeWithoutUpgrade(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment := newHelmEnvironment(t, false, false, "", "")
	defer publicHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		publicHelmEnvironment.ChartServer.URL(),
		"2.0.0",
		nil,
		false,
		Values{
			"crontab": map[string]any{
				"enabled": true,
			},
		},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv2(t, kubernetes, release.Name, release.Namespace)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())

	chart := &Chart{
		Name:    "test",
		RepoURL: publicHelmEnvironment.ChartServer.URL(),
		Version: "3.0.0",
	}

	releaseDeclaration.Chart = chart
	_, err = chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.ErrorContains(
		t,
		err,
		"failed to create typed patch object (default/test; stable.example.com/v1, Kind=CronTab): .spec.ref: field not declared in schema",
	)

	// specifiying allow crds upgrade after the first upgrade, where it was disallowed, should still fail
	releaseDeclaration.CRDs.AllowUpgrade = true
	_, err = chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.ErrorContains(
		t,
		err,
		"failed to create typed patch object (default/test; stable.example.com/v1, Kind=CronTab): .spec.ref: field not declared in schema",
	)

	// specifiying force after the first upgrade, where it was disallowed, crds should be applied and reconciliation should not fail
	releaseDeclaration.CRDs.ForceUpgrade = true
	actualRelease, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)

	assertChartv3(
		t,
		kubernetes,
		actualRelease.Name,
		actualRelease.Namespace,
	)
	assert.Equal(t, actualRelease.Version, 2)
}

func TestChartReconciler_Reconcile_NoUpgrade(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment := newHelmEnvironment(t, false, false, "", "")
	defer publicHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		publicHelmEnvironment.ChartServer.URL(),
		"1.0.0",
		nil,
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	release, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)
	assertChartv1(t, kubernetes, release.Name, release.Namespace, 1)
	assert.Equal(t, release.Version, 1)
	assert.Equal(t, release.Name, releaseDeclaration.Name)
	assert.Equal(t, release.Namespace, releaseDeclaration.Namespace)

	contentReader, err := inventoryInstance.GetItem(&inventory.HelmReleaseItem{
		Name:      release.Name,
		Namespace: release.Namespace,
		ID:        fmt.Sprintf("%s_%s_HelmRelease", release.Name, release.Namespace),
	})
	defer contentReader.Close()

	storedBytes, err := io.ReadAll(contentReader)
	assert.NilError(t, err)

	desiredBuf := &bytes.Buffer{}
	err = json.NewEncoder(desiredBuf).Encode(release)
	assert.NilError(t, err)

	assert.Equal(t, string(storedBytes), desiredBuf.String())

	actualRelease, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)

	assertChartv1(
		t,
		kubernetes,
		actualRelease.Name,
		actualRelease.Namespace,
		1,
	)
	assert.Equal(t, actualRelease.Version, 1)
}

func TestChartReconciler_Reconcile_Conflicts(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment := newHelmEnvironment(t, false, false, "", "")
	defer publicHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		publicHelmEnvironment.ChartServer.URL(),
		"1.0.0",
		nil,
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	_, err = chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	unstr := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      "test",
				"namespace": "default",
			},
			"spec": map[string]interface{}{
				"replicas": 2,
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

	actualRelease, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)

	assertChartv1(
		t,
		kubernetes,
		actualRelease.Name,
		actualRelease.Namespace,
		1,
	)
	assert.Equal(t, actualRelease.Version, 2)
}

func TestChartReconciler_Reconcile_IngoreConflicts(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment := newHelmEnvironment(t, false, false, "", "")
	defer publicHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		publicHelmEnvironment.ChartServer.URL(),
		"1.0.0",
		nil,
		false,
		Values{},
		&helm.Patches{
			Unstructureds: map[string]kube.ExtendedUnstructured{
				"apps/v1-Deployment-default-test": {
					Unstructured: &unstructured.Unstructured{
						Object: map[string]interface{}{
							"apiVersion": "apps/v1",
							"kind":       "Deployment",
							"metadata": map[string]any{
								"name":      "test",
								"namespace": "default",
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
			},
		},
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	_, err = chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	unstr := unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      "test",
				"namespace": "default",
			},
			"spec": map[string]interface{}{
				"replicas": 2,
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

	actualRelease, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)

	assertChartv1(
		t,
		kubernetes,
		actualRelease.Name,
		actualRelease.Namespace,
		2,
	)
	assert.Equal(t, actualRelease.Version, 2)
}

func TestChartReconciler_Reconcile_PendingUpgradeRecovery(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment := newHelmEnvironment(t, false, false, "", "")
	defer publicHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		publicHelmEnvironment.ChartServer.URL(),
		"1.0.0",
		nil,
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	_, err = chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	helmConfig, err := helmtest.ConfigureHelm(chartReconciler.KubeConfig)
	assert.NilError(t, err)

	helmGet := action.NewGet(helmConfig)
	rel, err := helmGet.Run("test")
	assert.NilError(t, err)

	rel.Info.Status = release.StatusPendingUpgrade
	rel.Version = 2

	err = helmConfig.Releases.Create(rel)
	assert.NilError(t, err)

	actualRelease, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)

	assertChartv1(
		t,
		kubernetes,
		actualRelease.Name,
		actualRelease.Namespace,
		1,
	)
	assert.Equal(t, actualRelease.Version, 2)
}

func TestChartReconciler_Reconcile_PendingInstallRecovery(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.NewTLSRegistryWithSchema()
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	publicHelmEnvironment := newHelmEnvironment(t, false, false, "", "")
	defer publicHelmEnvironment.Close()

	releaseDeclaration := createReleaseDeclaration(
		"default",
		publicHelmEnvironment.ChartServer.URL(),
		"1.0.0",
		nil,
		false,
		Values{},
		nil,
	)

	ctx := context.Background()

	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))
	kubernetes := kubetest.StartKubetestEnv(t, log, kubetest.WithEnabled(true))
	defer kubernetes.Stop()

	inventoryInstance := inventory.Instance{
		Path: filepath.Join(t.TempDir(), "inventory"),
	}

	chartReconciler := helm.ChartReconciler{
		Log:                   log,
		KubeConfig:            kubernetes.ControlPlane.Config,
		Client:                kubernetes.DynamicTestKubeClient,
		FieldManager:          "controller",
		InventoryInstance:     &inventoryInstance,
		InsecureSkipTLSverify: true,
		ChartCacheRoot:        t.TempDir(),
	}

	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetName(releaseDeclaration.Namespace)

	_, err = kubernetes.DynamicTestKubeClient.DynamicClient().Apply(
		ctx,
		ns,
		"controller",
	)
	assert.NilError(t, err)

	_, err = chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)

	helmConfig, err := helmtest.ConfigureHelm(chartReconciler.KubeConfig)
	assert.NilError(t, err)

	helmGet := action.NewGet(helmConfig)
	rel, err := helmGet.Run("test")
	assert.NilError(t, err)

	rel.Info.Status = release.StatusPendingInstall
	err = helmConfig.Releases.Update(rel)
	assert.NilError(t, err)

	actualRelease, err := chartReconciler.Reconcile(
		ctx,
		&helm.ReleaseComponent{
			ID: fmt.Sprintf(
				"%s_%s_%s",
				releaseDeclaration.Name,
				releaseDeclaration.Namespace,
				"HelmRelease",
			),
			Content: releaseDeclaration,
		},
	)
	assert.NilError(t, err)

	assertChartv1(
		t,
		kubernetes,
		actualRelease.Name,
		actualRelease.Namespace,
		1,
	)
	assert.Equal(t, actualRelease.Version, 1)
}
