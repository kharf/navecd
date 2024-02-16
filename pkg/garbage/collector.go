package garbage

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/kharf/declcd/pkg/component"
	"github.com/kharf/declcd/pkg/helm"
	"github.com/kharf/declcd/pkg/inventory"
	"github.com/kharf/declcd/pkg/kube"
	"golang.org/x/sync/errgroup"
	"helm.sh/helm/v3/pkg/action"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/rest"
)

// Collector inspects the inventory for dangling manifests or helm releases,
// which are undefined in the declcd gitops repository, and uninstalls them from
// the Kubernetes cluster and inventory.
type Collector struct {
	Log              logr.Logger
	Client           *kube.DynamicClient
	KubeConfig       *rest.Config
	InventoryManager *inventory.Manager
	WorkerPoolSize   int
}

// Collect inspects the inventory for dangling manifests or helm releases,
// which are undefined in the declcd gitops repository, and uninstalls them from
// the Kubernetes cluster and inventory.
// The DependencyGraph is a representation of the gitops repository.
func (c *Collector) Collect(ctx context.Context, dag *component.DependencyGraph) error {
	storage, err := c.InventoryManager.Load()
	if err != nil {
		return err
	}
	eg := errgroup.Group{}
	eg.SetLimit(c.WorkerPoolSize)
	for _, invComponent := range storage.Items() {
		eg.Go(func() error {
			return c.collect(ctx, dag, invComponent)
		})
	}
	return eg.Wait()
}

func (c *Collector) collect(
	ctx context.Context,
	dag *component.DependencyGraph,
	inventoryItem inventory.Item,
) error {
	collect := true
	instance := dag.Get(inventoryItem.GetID())
	if instance != nil {
		collect = inventoryItem.GetID() != instance.GetID()
	}
	if collect {
		switch item := inventoryItem.(type) {
		case *inventory.HelmReleaseItem:
			if err := c.collectHelmRelease(item); err != nil {
				return err
			}
		case *inventory.ManifestItem:
			if err := c.collectManifest(ctx, item); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Collector) collectHelmRelease(invHr *inventory.HelmReleaseItem) error {
	c.Log.Info(
		"Collecting unreferenced helm release",
		"namespace",
		invHr.GetNamespace(),
		"name",
		invHr.GetName(),
	)
	// fieldManager is irrelevant for deleting.
	helmCfg, err := helm.Init(invHr.GetNamespace(), c.KubeConfig, c.Client, "")
	if err != nil {
		return err
	}
	client := action.NewUninstall(helmCfg)
	client.Wait = false
	_, err = client.Run(invHr.GetName())
	if err != nil {
		return err
	}
	if err := c.InventoryManager.DeleteItem(invHr); err != nil {
		return err
	}
	return nil
}

func (c *Collector) collectManifest(
	ctx context.Context,
	invManifest *inventory.ManifestItem,
) error {
	c.Log.Info(
		"Collecting unreferenced manifest",
		"namespace",
		invManifest.GetNamespace(),
		"name",
		invManifest.GetName(),
		"kind",
		invManifest.TypeMeta.Kind,
	)
	unstr := &unstructured.Unstructured{}
	unstr.SetName(invManifest.GetName())
	unstr.SetNamespace(invManifest.GetNamespace())
	unstr.SetKind(invManifest.TypeMeta.Kind)
	unstr.SetAPIVersion(invManifest.TypeMeta.APIVersion)
	if err := c.Client.Delete(ctx, unstr); err != nil {
		return err
	}
	if err := c.InventoryManager.DeleteItem(invManifest); err != nil {
		return err
	}
	return nil
}
