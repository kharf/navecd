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

	"github.com/go-co-op/gocron/v2"
	"github.com/go-logr/logr"
	gitops "github.com/kharf/navecd/api/v1beta1"
	"github.com/kharf/navecd/pkg/component"
	"github.com/kharf/navecd/pkg/garbage"
	"github.com/kharf/navecd/pkg/helm"
	"github.com/kharf/navecd/pkg/inventory"
	"github.com/kharf/navecd/pkg/kube"
	"github.com/kharf/navecd/pkg/vcs"
	"github.com/kharf/navecd/pkg/version"
	"golang.org/x/sync/errgroup"
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

	// Cron scheduler running in the background scanning for image updates.
	Scheduler         gocron.Scheduler
	SchedulerQuitChan chan struct{}
	SchedulerErrGroup *errgroup.Group
}

// ReconcileResult reports the outcome and metadata of a reconciliation.
type ReconcileResult struct {
	// Reports whether the GitOpsProject was flagged as suspended.
	Suspended bool

	// The hash of the reconciled Git Commit.
	CommitHash string

	// VCS Repository cache.
	LocalRepositoryPath string
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

	cfg := reconciler.KubeConfig
	if gProject.Spec.ServiceAccountName != "" {
		cfg.Impersonate = rest.ImpersonationConfig{
			UserName: fmt.Sprintf(
				"system:serviceaccount:%s:%s",
				gProject.Namespace,
				gProject.Spec.ServiceAccountName,
			),
		}
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

	reconciledCommitHash, err := repository.Pull()
	if err != nil {
		log.Error(
			err,
			"Unable to pull gitops project repository",
		)
		return nil, err
	}

	projectInstance, err := reconciler.ProjectManager.Load(repositoryDir)
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

		updateScheduler := version.UpdateScheduler{
			Log:       log,
			Scheduler: reconciler.Scheduler,
			Scanner: version.Scanner{
				Log:        log,
				KubeClient: kubeDynamicClient.DynamicClient(),
				Namespace:  reconciler.Namespace,
			},
			Updater: version.Updater{
				Log:        log,
				Repository: updateRepository,
				Branch:     gProject.Spec.Branch,
			},
			QuitChan: reconciler.SchedulerQuitChan,
			ErrGroup: reconciler.SchedulerErrGroup,
		}

		if _, err := updateScheduler.Schedule(ctx, projectInstance.UpdateInstructions); err != nil {
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

	if err := reconciler.reconcileComponents(ctx, componentReconciler, componentInstances); err != nil {
		log.Error(
			err,
			"Unable to reconcile components",
		)
		return nil, err
	}

	return &ReconcileResult{
		Suspended:           false,
		CommitHash:          reconciledCommitHash,
		LocalRepositoryPath: repositoryDir,
	}, nil
}

func (reconciler *Reconciler) reconcileComponents(
	ctx context.Context,
	componentReconciler component.Reconciler,
	componentInstances []component.Instance,
) error {
	eg := errgroup.Group{}
	eg.SetLimit(reconciler.WorkerPoolSize)
	for _, instance := range componentInstances {
		// TODO: implement SCC decomposition for better concurrency/parallelism
		if len(instance.GetDependencies()) == 0 {
			eg.Go(func() error {
				return componentReconciler.Reconcile(
					ctx,
					instance,
				)
			})
		} else {
			if err := eg.Wait(); err != nil {
				return err
			}
			if err := componentReconciler.Reconcile(
				ctx,
				instance,
			); err != nil {
				return err
			}
		}
	}
	return eg.Wait()
}
