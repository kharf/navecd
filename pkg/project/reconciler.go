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
	"github.com/kharf/navecd/pkg/vcs"
	"github.com/kharf/navecd/pkg/version"
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

	// RepositoryManager clones a remote vcs repository to a local path.
	RepositoryManager vcs.RepositoryManager

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

	// Directory used to cache vcs repositories or helm charts.
	CacheDir string

	// Namespace the controller runs in.
	Namespace string

	// UpdateScheduler runs background tasks periodically to update Container or Helm Charts.
	UpdateScheduler *version.UpdateScheduler
}

// ReconcileResult reports the outcome and metadata of a reconciliation.
type ReconcileResult struct {
	// Reports whether the GitOpsProject was flagged as suspended.
	Suspended bool

	// The hash of the reconciled Git Commit.
	CommitHash string

	// PullError reports any error occured while trying to pull from vcs.
	// It is a soft error, which does not halt the reconciliation process, but has to be reported.
	PullError error

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
		// /inventory is mounted as volume.
		Path: filepath.Join("/inventory", projectUID),
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

	repository, err := reconciler.RepositoryManager.Load(
		ctx,
		gProject.Spec.URL,
		gProject.Spec.Branch,
		repositoryDir,
		gProject.Name,
	)
	if err != nil {
		log.Error(
			err,
			"Unable to load gitops project repository",
		)
		return nil, err
	}

	reconciledCommitHash, pullErr := repository.Pull()
	if pullErr != nil {
		log.Error(
			err,
			"Unable to pull gitops project repository",
		)

		reconciledCommitHash = gProject.Status.Revision.CommitHash
	}

	projectInstance, err := reconciler.ProjectManager.Load(repositoryDir, gProject.Spec.Dir)
	if err != nil {
		log.Error(
			err,
			"Unable to load navecd project",
		)
		return nil, err
	}

	go func() {
		updateRepositoryPath := fmt.Sprintf("%s-updates", repository.Path())

		updateRepository, err := reconciler.RepositoryManager.LoadLocally(
			ctx,
			repositoryDir,
			updateRepositoryPath,
			gProject.Name,
		)
		if err != nil {
			log.Error(
				err,
				"Unable to load gitops project repository for updates",
			)
			return
		}

		if _, err := reconciler.UpdateScheduler.Schedule(ctx, version.ScheduleRequest{
			ProjectUID: projectUID,
			Scanner: version.Scanner{
				Log:        log,
				KubeClient: kubeDynamicClient.DynamicClient(),
				Namespace:  reconciler.Namespace,
			},
			Repository:   updateRepository,
			Branch:       gProject.Spec.Branch,
			Instructions: projectInstance.UpdateInstructions,
		}); err != nil {
			log.Error(err, "Unable to update update scheduler")
		}
	}()

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

	return &ReconcileResult{
		Suspended:      false,
		CommitHash:     reconciledCommitHash,
		PullError:      pullErr,
		ComponentError: componentReconciler.Reconcile(ctx, componentInstances),
	}, nil
}
