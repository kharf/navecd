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

package project

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/go-logr/logr"
	gitops "github.com/kharf/navecd/api/v1beta1"
	"github.com/kharf/navecd/pkg/component"
	"github.com/kharf/navecd/pkg/garbage"
	"github.com/kharf/navecd/pkg/helm"
	"github.com/kharf/navecd/pkg/inventory"
	"github.com/kharf/navecd/pkg/kube"
	"k8s.io/client-go/rest"
)

// Reconciler clones, pulls and loads a GitOps Git repository containing the desired cluster state,
// translates cue definitions to either Kubernetes unstructurd objects or Helm Releases and applies/installs them on a Kubernetes cluster.
// Every run stores objects in the inventory and collects dangling objects.
type Reconciler struct {
	Log logr.Logger

	KubeConfig *rest.Config

	// Manager loads a navecd project and resolves the component dependency graph.
	ProjectManager Manager

	// ComponentBuilder compiles and decodes CUE kubernetes manifest definitions of a component to the corresponding Go struct.
	ComponentBuilder component.Builder

	// Managers identify distinct workflows that are modifying the object (especially useful on conflicts!),
	FieldManager string

	// Defines the concurrency level of Navecd operations.
	WorkerPoolSize int

	// InsecureSkipVerify controls whether Helm clients verify server
	// certificate chains and host names.
	InsecureSkipTLSverify bool

	// Force http for Helm registries.
	PlainHTTP bool

	// Directory used to cache repositories or helm charts.
	CacheDir string

	// Directory used to save the inventory of component references for all managed navecd projects.
	InventoryRootDir string

	// Namespace the controller runs in.
	Namespace string

	// Endpoint to the microsoft azure login server.
	// Default is usually: https://login.microsoftonline.com/.
	AzureLoginURL string

	// Endpoint to the google metadata server, which provides access tokens.
	// Default is: http://metadata.google.internal.
	GCPMetadataServerURL string
}

// ReconcileResult reports the outcome and metadata of a reconciliation.
type ReconcileResult struct {
	// Reports whether the GitOpsProject was flagged as suspended.
	Suspended bool

	// The digest of the reconciled navecd project artifact.
	Digest string

	// DownloadError reports any error occured while trying to load the navecd project artifact.
	// It is a soft error, which does not halt the reconciliation process, but has to be reported.
	DownloadError error

	// ComponentError reports the first occured component reconciliation error.
	// It is a soft error, which does not halt the reconciliation process, but has to be reported.
	ComponentError error
}

// Reconcile clones, pulls and loads a GitOps Git repository containing the desired cluster state,
// translates cue definitions to either Kubernetes unstructurd objects or Helm Releases and applies/installs them on a Kubernetes cluster.
// It stores objects in the inventory and collects dangling objects.
func (reconciler *Reconciler) Reconcile(
	ctx context.Context,
	gProject gitops.GitOpsProject,
) (*ReconcileResult, error) {
	if *gProject.Spec.Suspend {
		return &ReconcileResult{Suspended: true}, nil
	}
	log := reconciler.Log

	var cfg *rest.Config
	if gProject.Spec.ServiceAccountName != "" {
		impCfg := *reconciler.KubeConfig
		impCfg.Impersonate = rest.ImpersonationConfig{
			UserName: fmt.Sprintf(
				"system:serviceaccount:%s:%s",
				gProject.Namespace,
				gProject.Spec.ServiceAccountName,
			),
		}
		cfg = &impCfg
	} else {
		cfg = reconciler.KubeConfig
	}

	log = log.WithValues(
		"project",
		gProject.GetName(),
		"repository",
		gProject.Spec.URL,
		"impersonated",
		gProject.Spec.ServiceAccountName,
	)

	kubeDynamicClient, err := kube.NewExtendedDynamicClient(cfg)
	if err != nil {
		log.Error(
			err,
			"Unable to create Kubernetes Client",
		)
		return nil, err
	}

	projectUID := string(gProject.GetUID())
	repositoryDir := filepath.Join(reconciler.CacheDir, "navecd", projectUID)

	inventoryInstance := &inventory.Instance{
		Path: filepath.Join(reconciler.InventoryRootDir, projectUID),
	}

	chartReconciler := helm.ChartReconciler{
		KubeConfig:            cfg,
		Client:                kubeDynamicClient,
		FieldManager:          reconciler.FieldManager,
		InventoryInstance:     inventoryInstance,
		InsecureSkipTLSverify: reconciler.InsecureSkipTLSverify,
		PlainHTTP:             reconciler.PlainHTTP,
		Log:                   log,
		ChartCacheRoot:        reconciler.CacheDir,
	}

	garbageCollector := garbage.Collector{
		Log:               log,
		Client:            kubeDynamicClient.DynamicClient(),
		ChartReconciler:   chartReconciler,
		InventoryInstance: inventoryInstance,
		WorkerPoolSize:    reconciler.WorkerPoolSize,
	}

	componentReconciler := component.Reconciler{
		Log:               log,
		DynamicClient:     kubeDynamicClient,
		ChartReconciler:   chartReconciler,
		InventoryInstance: inventoryInstance,
		FieldManager:      reconciler.FieldManager,
		WorkerPoolSize:    reconciler.WorkerPoolSize,
	}

	projectInstance, err := reconciler.ProjectManager.Load(
		ctx,
		repositoryDir,
		gProject.Spec.Dir,
		WithRemoteLoader(&OCIRemoteLoader{
			Repository: OCIRepositoryRef{
				Name: gProject.Spec.URL,
				Ref:  gProject.Spec.Ref,
			},
			KubeClient:           kubeDynamicClient.DynamicClient(),
			CacheDir:             reconciler.CacheDir,
			Namespace:            reconciler.Namespace,
			AzureLoginURL:        reconciler.AzureLoginURL,
			GCPMetadataServerURL: reconciler.GCPMetadataServerURL,
		}),
		WithAuth(gProject.Spec.Auth),
	)
	if err != nil {
		log.Error(
			err,
			"Unable to load navecd project",
		)
		return nil, err
	}

	componentInstances, err := projectInstance.Dag.TopologicalSort()
	if err != nil {
		log.Error(
			err,
			"Unable to resolve dependencies",
		)
		return nil, err
	}

	if err := garbageCollector.Collect(ctx, projectInstance.Dag); err != nil {
		return nil, err
	}

	var digest string
	if projectInstance.Digest == "" {
		digest = gProject.Status.Revision.Digest
	} else {
		digest = string(projectInstance.Digest)
	}

	return &ReconcileResult{
		Suspended:      false,
		Digest:         digest,
		DownloadError:  projectInstance.LoadError,
		ComponentError: componentReconciler.Reconcile(ctx, componentInstances),
	}, nil
}
