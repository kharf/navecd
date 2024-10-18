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

	// Limit of concurrent reconciliations.
	WorkerPoolSize int
}

func (reconciler *Reconciler) Reconcile(
	ctx context.Context,
	instances []Instance,
) error {
	instanceLayers := Layer(instances)

	var firstError error
	var prevLayerErrComponents map[string]struct{}

	for _, layer := range instanceLayers {
		var err error
		prevLayerErrComponents, err = reconciler.reconcileLayer(ctx, layer, prevLayerErrComponents)
		if err != nil && firstError == nil {
			firstError = err
		}
	}

	return firstError
}

func (reconciler *Reconciler) reconcileLayer(
	ctx context.Context,
	layer InstanceLayer,
	prevLayerErrComponents map[string]struct{},
) (map[string]struct{}, error) {
	recEG := errgroup.Group{}
	recEG.SetLimit(reconciler.WorkerPoolSize)

	errChan := make(chan string)
	errComponents := make(map[string]struct{}, len(layer.Components))

	errComponentsEG := errgroup.Group{}
	errComponentsEG.Go(func() error {
		for component := range errChan {
			errComponents[component] = struct{}{}
		}

		return nil
	})

	if len(prevLayerErrComponents) != 0 {
		for _, instance := range layer.Components {
			recEG.Go(func() error {
				for _, dep := range instance.GetDependencies() {
					if _, found := prevLayerErrComponents[dep]; found {
						reconciler.Log.V(0).
							Info("Errorneous dependency. Skipping component", "id", instance.GetID())
						return nil
					}
				}

				if err := reconciler.reconcile(ctx, instance); err != nil {
					reconciler.Log.Error(err,
						"Unable to reconcile component",
						"id",
						instance.GetID(),
					)

					errChan <- instance.GetID()
					return err
				}

				return nil
			})
		}
	} else {
		for _, instance := range layer.Components {
			recEG.Go(func() error {
				if err := reconciler.reconcile(ctx, instance); err != nil {
					reconciler.Log.Error(err,
						"Unable to reconcile component",
						"id",
						instance.GetID(),
					)

					errChan <- instance.GetID()
					return err
				}

				return nil
			})
		}
	}

	recErr := recEG.Wait()

	close(errChan)

	_ = errComponentsEG.Wait()

	return errComponents, recErr
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
