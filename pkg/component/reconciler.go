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
	"bytes"
	"context"
	"encoding/json"

	"github.com/go-logr/logr"
	"github.com/kharf/navecd/pkg/helm"
	"github.com/kharf/navecd/pkg/inventory"
	"github.com/kharf/navecd/pkg/kube"
	"golang.org/x/sync/errgroup"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Reconciler reads Components with their desired state
// and applies them on a Kubernetes cluster.
// It stores objects in the inventory.
type Reconciler struct {
	Log logr.Logger

	// DynamicClient connects to a Kubernetes cluster
	// to create, read, update and delete manifests/objects.
	DynamicClient *kube.ExtendedDynamicClient

	// ChartReconciler reads Helm Packages with their desired state
	// and applies them on a Kubernetes cluster.
	// It stores releases in the inventory, but never collects it.
	ChartReconciler helm.ChartReconciler

	// Instance is a representation of an inventory.
	// It can store, delete and read items.
	// The object does not include the storage itself, it only holds a reference to the storage.
	InventoryInstance *inventory.Instance

	// Managers identify distinct workflows that are modifying the object (especially useful on conflicts!),
	FieldManager string

	// Defines the concurrency level of Navecd operations.
	WorkerPoolSize int
}

func (reconciler *Reconciler) Reconcile(
	ctx context.Context,
	instances []Instance,
) error {
	eg := errgroup.Group{}
	eg.SetLimit(reconciler.WorkerPoolSize)

	errChan := make(chan string, 10)

	go func() {
		for {
			select {
			case errorneousComponent := <-errChan:

			}
		}

	}()

	instanceLayers := Layer(instances)
	for _, layer := range instanceLayers {
		for _, instance := range layer.Components {
			eg.Go(func() error {
				err := reconciler.reconcile(
					ctx,
					instance,
				)
				if err != nil {
					reconciler.Log.Error(err,
						"Unable to reconcile component",
						"id",
						instance.GetID(),
					)
					errChan <- instance.GetID()
				}

				return nil
			})
		}

		if err := eg.Wait(); err != nil {
			return err
		}
	}
	return eg.Wait()
}

func (reconciler *Reconciler) ReconcileOld(
	ctx context.Context,
	instances []Instance,
) error {
	eg := errgroup.Group{}
	eg.SetLimit(reconciler.WorkerPoolSize)
	for _, instance := range instances {
		// TODO: implement SCC decomposition for better concurrency/parallelism
		if len(instance.GetDependencies()) == 0 {
			eg.Go(func() error {
				return reconciler.reconcile(
					ctx,
					instance,
				)
			})
		} else {
			if err := eg.Wait(); err != nil {
				return err
			}
			if err := reconciler.reconcile(
				ctx,
				instance,
			); err != nil {
				return err
			}
		}
	}
	return eg.Wait()
}

func (reconciler *Reconciler) ReconcileSeq(
	ctx context.Context,
	instances []Instance,
) error {
	for _, instance := range instances {
		if err := reconciler.reconcile(
			ctx,
			instance,
		); err != nil {
			return err
		}
	}

	return nil
}

func (reconciler *Reconciler) reconcile(
	ctx context.Context,
	instance Instance,
) error {
	switch componentInstance := instance.(type) {
	case *Manifest:
		reconciler.Log.V(1).Info(
			"Applying manifest",
			"namespace",
			componentInstance.GetNamespace(),
			"name",
			componentInstance.GetName(),
			"kind",
			componentInstance.GetKind(),
		)

		unstr := componentInstance.Content
		if _, err := reconciler.DynamicClient.Apply(ctx, &unstr, reconciler.FieldManager, kube.ForceApply(true)); err != nil {
			return err
		}

		invManifest := &inventory.ManifestItem{
			ID: componentInstance.ID,
			TypeMeta: v1.TypeMeta{
				Kind:       componentInstance.GetKind(),
				APIVersion: componentInstance.GetAPIVersion(),
			},
			Name:      componentInstance.GetName(),
			Namespace: componentInstance.GetNamespace(),
		}

		buf := &bytes.Buffer{}
		if err := json.NewEncoder(buf).Encode(unstr.Object); err != nil {
			return err
		}

		if err := reconciler.InventoryInstance.StoreItem(invManifest, buf); err != nil {
			return err
		}

	case *helm.ReleaseComponent:
		if _, err := reconciler.ChartReconciler.Reconcile(
			ctx,
			componentInstance,
		); err != nil {
			return err
		}
	}
	return nil
}
