package garbage_test

import (
	"context"
	"testing"

	"github.com/kharf/declcd/internal/projecttest"
	"github.com/kharf/declcd/pkg/garbage"
	"github.com/kharf/declcd/pkg/inventory"
	"github.com/kharf/declcd/pkg/kube"
	"gotest.tools/v3/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCollector_Collect_NoChanges(t *testing.T) {
	env := projecttest.StartProjectEnv(t)
	defer env.Stop()
	invManifests := []inventory.Manifest{
		{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Namespace",
				APIVersion: "v1",
			},
			Name: "a",
		},
		{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
			},
			Name:      "a",
			Namespace: "a",
		},
		{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Namespace",
				APIVersion: "v1",
			},
			Name: "b",
		},
		{
			TypeMeta: metav1.TypeMeta{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
			},
			Name:      "b",
			Namespace: "b",
		},
	}

	ctx := context.Background()
	invMap := make(map[string]inventory.Manifest)
	renderedManifests := make([]unstructured.Unstructured, 0, len(invMap))
	converter := runtime.DefaultUnstructuredConverter
	client, err := kube.NewClient(env.ControlPlane.Config)
	assert.NilError(t, err)
	for _, im := range invManifests {
		invMap[im.AsKey()] = im
		obj, err := converter.ToUnstructured(toObject(im))
		unstr := unstructured.Unstructured{Object: obj}
		err = client.Apply(ctx, &unstr)
		assert.NilError(t, err)
		renderedManifests = append(renderedManifests, unstr)
		assert.NilError(t, err)
		err = env.InventoryManager.Store(im)
		assert.NilError(t, err)
	}

	collector := garbage.Collector{
		Log:              env.Log,
		Client:           client,
		InventoryManager: env.InventoryManager,
	}

	err = collector.Collect(ctx, inventory.Storage{Manifests: invMap}, renderedManifests)
	assert.NilError(t, err)

	var deploymentA appsv1.Deployment
	err = env.TestKubeClient.Get(ctx, types.NamespacedName{Name: "a", Namespace: "a"}, &deploymentA)
	assert.NilError(t, err)
	assert.Equal(t, deploymentA.Name, "a")
	assert.Equal(t, deploymentA.Namespace, "a")

	var deploymentB appsv1.Deployment
	err = env.TestKubeClient.Get(ctx, types.NamespacedName{Name: "b", Namespace: "b"}, &deploymentB)
	assert.NilError(t, err)
	assert.Equal(t, deploymentB.Name, "b")
	assert.Equal(t, deploymentB.Namespace, "b")
}

func TestCollector_Collect(t *testing.T) {
	env := projecttest.StartProjectEnv(t)
	defer env.Stop()
	nsA := inventory.Manifest{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		Name: "a",
	}
	nsB := inventory.Manifest{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		Name: "b",
	}
	depA := inventory.Manifest{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		Name:      "a",
		Namespace: "a",
	}
	depB := inventory.Manifest{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		Name:      "b",
		Namespace: "b",
	}
	invManifests := []inventory.Manifest{
		nsA,
		nsB,
		depA,
		depB,
	}

	ctx := context.Background()
	converter := runtime.DefaultUnstructuredConverter
	invMap := make(map[string]inventory.Manifest)
	client, err := kube.NewClient(env.ControlPlane.Config)
	assert.NilError(t, err)
	for _, im := range invManifests {
		invMap[im.AsKey()] = im
		obj, err := converter.ToUnstructured(toObject(im))
		unstr := unstructured.Unstructured{Object: obj}
		err = client.Apply(ctx, &unstr)
		assert.NilError(t, err)
		err = env.InventoryManager.Store(im)
		assert.NilError(t, err)
	}

	unstrNsA, err := converter.ToUnstructured(toObject(nsA))
	unstrNsB, err := converter.ToUnstructured(toObject(nsB))
	unstrDepA, err := converter.ToUnstructured(toObject(depA))
	renderedManifests := []unstructured.Unstructured{
		{Object: unstrNsA},
		{Object: unstrNsB},
		{Object: unstrDepA},
	}

	collector := garbage.Collector{
		Log:              env.Log,
		Client:           client,
		InventoryManager: env.InventoryManager,
	}

	err = collector.Collect(ctx, inventory.Storage{Manifests: invMap}, renderedManifests)
	assert.NilError(t, err)

	var deploymentA appsv1.Deployment
	err = env.TestKubeClient.Get(ctx, types.NamespacedName{Name: "a", Namespace: "a"}, &deploymentA)
	assert.NilError(t, err)
	assert.Equal(t, deploymentA.Name, "a")
	assert.Equal(t, deploymentA.Namespace, "a")

	var deploymentB appsv1.Deployment
	err = env.TestKubeClient.Get(ctx, types.NamespacedName{Name: "b", Namespace: "b"}, &deploymentB)
	assert.Error(t, err, "deployments.apps \"b\" not found")
}

func toObject(invManifest inventory.Manifest) client.Object {
	switch invManifest.Kind {
	case "Deployment":
		return deployment(invManifest)
	case "Namespace":
		return namespace(invManifest)
	}

	return nil
}

func namespace(invManifest inventory.Manifest) client.Object {
	return &v1.Namespace{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: invManifest.Name,
		},
	}
}

func deployment(invManifest inventory.Manifest) client.Object {
	replicas := int32(1)
	labels := map[string]string{
		"app": invManifest.Name,
	}
	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      invManifest.Name,
			Namespace: invManifest.Namespace,
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
							Name:  invManifest.Name,
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
