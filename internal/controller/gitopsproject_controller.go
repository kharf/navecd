/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"go.uber.org/zap/zapcore"
	ctrlZap "sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/go-co-op/gocron/v2"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/rest"
	"k8s.io/kubectl/pkg/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/go-logr/logr"
	gitops "github.com/kharf/navecd/api/v1beta1"
	"github.com/kharf/navecd/pkg/component"
	"github.com/kharf/navecd/pkg/kube"
	"github.com/kharf/navecd/pkg/project"
	"github.com/kharf/navecd/pkg/vcs"
	"github.com/kharf/navecd/pkg/version"
	"github.com/prometheus/client_golang/prometheus"
	helmKube "helm.sh/helm/v3/pkg/kube"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	// +kubebuilder:scaffold:imports
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme.Scheme))
	utilruntime.Must(gitops.AddToScheme(scheme.Scheme))
	// +kubebuilder:scaffold:scheme
}

// GitOpsProjectController reconciles a GitOpsProject object
type GitOpsProjectController struct {
	Log logr.Logger

	// Client connects to a Kubernetes cluster
	// to create, read, update and delete standard Kubernetes manifests/objects.
	Client client.Client

	Reconciler project.Reconciler

	ReconciliationHistogram *prometheus.HistogramVec
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (controller *GitOpsProjectController) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {
	triggerTime := v1.Now()
	log := controller.Log

	log.Info("Reconciling")

	var gProject gitops.GitOpsProject
	if err := controller.Client.Get(ctx, req.NamespacedName, &gProject); err != nil {
		log.Error(err, "Unable to fetch GitOpsProject resource from cluster")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	requeueResult := ctrl.Result{
		RequeueAfter: time.Duration(gProject.Spec.PullIntervalSeconds) * time.Second,
	}

	gProject.Status.Conditions = make([]v1.Condition, 0, 2)
	if err := controller.updateCondition(ctx, &gProject, v1.Condition{
		Type:               "Running",
		Reason:             "Interval",
		Message:            "Reconciling",
		Status:             "True",
		LastTransitionTime: triggerTime,
	}); err != nil {
		log.Error(err, "Unable to update GitOpsProject status condition to 'Running'")
		return requeueResult, nil
	}

	result, err := controller.Reconciler.Reconcile(ctx, gProject)
	if err != nil {
		log.Error(err, "Reconciling failed")
		return requeueResult, nil
	}

	// Refetch to get newest state.
	if err := controller.Client.Get(ctx, req.NamespacedName, &gProject); err != nil {
		log.Error(err, "Unable to fetch GitOpsProject resource from cluster")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	reconciledTime := v1.Now()
	gProject.Status.Revision = gitops.GitOpsProjectRevision{
		CommitHash:    result.CommitHash,
		ReconcileTime: reconciledTime,
	}

	if err := controller.updateCondition(ctx, &gProject, v1.Condition{
		Type:               "Finished",
		Reason:             "Success",
		Message:            "Reconciled",
		Status:             "True",
		LastTransitionTime: reconciledTime,
	}); err != nil {
		log.Error(err, "Unable to update GitOpsProject status")
		return requeueResult, nil
	}

	controller.ReconciliationHistogram.With(prometheus.Labels{
		"project": gProject.GetName(),
		"url":     gProject.Spec.URL,
	}).Observe(time.Since(triggerTime.Time).Seconds())

	log.Info("Reconciling finished")
	return requeueResult, nil
}

func (reconciler *GitOpsProjectController) updateCondition(
	ctx context.Context,
	gProject *gitops.GitOpsProject,
	condition v1.Condition,
) error {
	gProject.Status.Conditions = append(gProject.Status.Conditions, condition)
	if err := reconciler.Client.Status().Update(ctx, gProject, client.FieldOwner(reconciler.Reconciler.FieldManager)); err != nil {
		return err
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (reconciler *GitOpsProjectController) SetupWithManager(
	mgr ctrl.Manager,
	controllerName string,
) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&gitops.GitOpsProject{}).
		Named(controllerName).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(reconciler)
}

type setupOptions struct {
	NamePodinfoPath       string
	NamespacePodinfoPath  string
	ShardPodinfoPath      string
	MetricsAddr           string
	ProbeAddr             string
	LogLevel              int
	InsecureSkipTLSverify bool
	PlainHTTP             bool
}

type option interface {
	apply(options *setupOptions)
}

type NamePodinfoPath string

func (opt NamePodinfoPath) apply(options *setupOptions) {
	if opt != "" {
		options.NamePodinfoPath = string(opt)
	}
}

type NamespacePodinfoPath string

func (opt NamespacePodinfoPath) apply(options *setupOptions) {
	if opt != "" {
		options.NamespacePodinfoPath = string(opt)
	}
}

type ShardPodinfoPath string

func (opt ShardPodinfoPath) apply(options *setupOptions) {
	if opt != "" {
		options.ShardPodinfoPath = string(opt)
	}
}

type MetricsAddr string

func (opt MetricsAddr) apply(options *setupOptions) {
	if opt != "" {
		options.MetricsAddr = string(opt)
	}
}

type ProbeAddr string

func (opt ProbeAddr) apply(options *setupOptions) {
	if opt != "" {
		options.ProbeAddr = string(opt)
	}
}

type InsecureSkipTLSverify bool

func (opt InsecureSkipTLSverify) apply(options *setupOptions) {
	options.InsecureSkipTLSverify = bool(opt)
}

type PlainHTTP bool

func (opt PlainHTTP) apply(options *setupOptions) {
	options.PlainHTTP = bool(opt)
}

type LogLevel int

func (opt LogLevel) apply(options *setupOptions) {
	options.LogLevel = int(opt)
}

func Setup(cfg *rest.Config, options ...option) (manager.Manager, gocron.Scheduler, error) {
	opts := &setupOptions{
		NamePodinfoPath:       "/podinfo/name",
		NamespacePodinfoPath:  "/podinfo/namespace",
		ShardPodinfoPath:      "/podinfo/shard",
		MetricsAddr:           ":8080",
		ProbeAddr:             ":8081",
		InsecureSkipTLSverify: false,
		PlainHTTP:             false,
		LogLevel:              0,
	}

	for _, opt := range options {
		opt.apply(opts)
	}

	log := ctrlZap.New(ctrlZap.UseFlagOptions(&ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(opts.LogLevel * -1),
	}))
	ctrl.SetLogger(log)

	nameBytes, err := os.ReadFile(opts.NamePodinfoPath)
	if err != nil {
		log.Error(err, "Unable to read controller name")
		return nil, nil, err
	}

	controllerName := strings.TrimSpace(string(nameBytes))

	namespaceBytes, err := os.ReadFile(opts.NamespacePodinfoPath)
	if err != nil {
		log.Error(err, "Unable to read namespace")
		return nil, nil, err
	}

	namespace := strings.TrimSpace(string(namespaceBytes))

	shardBytes, err := os.ReadFile(opts.ShardPodinfoPath)
	if err != nil {
		log.Error(err, "Unable to read shard")
		return nil, nil, err
	}

	shard := strings.TrimSpace(string(shardBytes))

	labelReq, err := labels.NewRequirement("navecd/shard", selection.Equals, []string{shard})
	if err != nil {
		log.Error(err, "Unable to set label requirements")
		return nil, nil, err
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: server.Options{
			BindAddress: opts.MetricsAddr,
			ExtraHandlers: map[string]http.Handler{
				"/debug/pprof/": http.DefaultServeMux,
			},
		},
		HealthProbeBindAddress:  opts.ProbeAddr,
		LeaderElection:          true,
		LeaderElectionID:        shard,
		LeaderElectionNamespace: namespace,
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&gitops.GitOpsProject{}: {
					Label: labels.NewSelector().
						Add(*labelReq),
				},
			},
		},
	})
	if err != nil {
		log.Error(err, "Unable to create manager")
		return nil, nil, err
	}

	componentBuilder := component.NewBuilder()

	// -1 means no limit. According to benchmarks this config had the best performance for all cpu quotas tested (1, 2, 4 cpus).
	workerSize := -1

	projectManager := project.NewManager(componentBuilder, workerSize)

	helmKube.ManagedFieldsManager = controllerName

	kubeDynamicClient, err := kube.NewExtendedDynamicClient(cfg)
	if err != nil {
		log.Error(err, "Unable to setup Kubernetes client")
		return nil, nil, err
	}

	reconciliationHisto := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "navecd",
		Name:      "reconciliation_duration_seconds",
		Help:      "Duration of a GitOps Project reconciliation",
	}, []string{"project", "url"})
	if err := metrics.Registry.Register(reconciliationHisto); err != nil {
		log.Error(err, "Unable to register Prometheus Collector")
		return nil, nil, err
	}

	scheduler, quitChan, err := version.NewUpdateScheduler(
		log,
	)
	if err != nil {
		log.Error(err, "Unable to setup update scheduler")
		return nil, nil, err
	}

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-signalChan
		quitChan <- struct{}{}
	}()

	if err := (&GitOpsProjectController{
		Log:                     log,
		ReconciliationHistogram: reconciliationHisto,
		Client:                  mgr.GetClient(),
		Reconciler: project.Reconciler{
			Log:                   log,
			KubeConfig:            cfg,
			ComponentBuilder:      componentBuilder,
			RepositoryManager:     vcs.NewRepositoryManager(namespace, kubeDynamicClient.DynamicClient(), log),
			ProjectManager:        projectManager,
			FieldManager:          controllerName,
			WorkerPoolSize:        workerSize,
			InsecureSkipTLSverify: opts.InsecureSkipTLSverify,
			PlainHTTP:             opts.PlainHTTP,
			CacheDir:              os.TempDir(),
			Namespace:             namespace,
			UpdateScheduler:       scheduler,
		},
	}).SetupWithManager(mgr, controllerName); err != nil {
		log.Error(err, "Unable to create controller")
		return nil, nil, err
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "Unable to set up health check")
		return nil, nil, err
	}

	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "Unable to set up ready check")
		return nil, nil, err
	}

	return mgr, scheduler, nil
}
