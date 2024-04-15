package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kharf/declcd/internal/manifest"
	"github.com/kharf/declcd/pkg/component"
	"github.com/kharf/declcd/pkg/kube"
	"github.com/kharf/declcd/pkg/project"
	"github.com/kharf/declcd/pkg/secret"
	"github.com/kharf/declcd/pkg/vcs"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var (
	ErrHelmInstallationUnsupported = errors.New("Helm installation not supported yet")
)

type options struct {
	namespace string
	branch    string
	url       string
	name      string
	token     string
	interval  int
}

type option interface {
	Apply(opts *options)
}

type Namespace string

var _ option = (*Namespace)(nil)

func (ns Namespace) Apply(opts *options) {
	opts.namespace = string(ns)
}

type URL string

var _ option = (*URL)(nil)

func (url URL) Apply(opts *options) {
	opts.url = string(url)
}

type Branch string

var _ option = (*Branch)(nil)

func (branch Branch) Apply(opts *options) {
	opts.branch = string(branch)
}

type Name string

var _ option = (*Name)(nil)

func (name Name) Apply(opts *options) {
	opts.name = string(name)
}

type Token string

var _ option = (*Token)(nil)

func (token Token) Apply(opts *options) {
	opts.token = string(token)
}

type Interval int

var _ option = (*Interval)(nil)

func (interval Interval) Apply(opts *options) {
	opts.interval = int(interval)
}

type Action struct {
	kubeClient       *kube.DynamicClient
	httpClient       *http.Client
	componentBuilder component.Builder
	projectRoot      string
}

func NewAction(
	kubeClient *kube.DynamicClient,
	httpClient *http.Client,
	projectRoot string,
) Action {
	return Action{
		kubeClient:  kubeClient,
		projectRoot: projectRoot,
		httpClient:  httpClient,
	}
}

func (act Action) Install(ctx context.Context, opts ...option) error {
	instOpts := options{}
	for _, o := range opts {
		o.Apply(&instOpts)
	}
	var projectBuf bytes.Buffer
	projectTmpl, err := template.New("").Parse(manifest.Project)
	if err != nil {
		return err
	}
	if err := projectTmpl.Execute(&projectBuf, map[string]interface{}{
		"Name":                instOpts.name,
		"Namespace":           instOpts.namespace,
		"Branch":              instOpts.branch,
		"PullIntervalSeconds": instOpts.interval,
		"Url":                 instOpts.url,
	}); err != nil {
		return err
	}
	declcdDir := filepath.Join(act.projectRoot, "declcd")
	if err := os.WriteFile(filepath.Join(declcdDir, "project.cue"), projectBuf.Bytes(), 0666); err != nil {
		return err
	}
	instances, err := act.componentBuilder.Build(
		component.WithPackagePath("./declcd"),
		component.WithProjectRoot(act.projectRoot),
	)
	if err != nil {
		return err
	}
	dag := component.NewDependencyGraph()
	if err := dag.Insert(instances...); err != nil {
		return err
	}
	instances, err = dag.TopologicalSort()
	if err != nil {
		return err
	}
	for _, instance := range instances {
		manifest, ok := instance.(*component.Manifest)
		if !ok {
			return ErrHelmInstallationUnsupported
		}
		if err := act.installObject(ctx, &manifest.Content, project.ControllerName); err != nil {
			return err
		}
	}
	repoConfigurator, err := vcs.NewRepositoryConfigurator(
		instOpts.namespace,
		act.kubeClient,
		act.httpClient,
		instOpts.url,
		instOpts.token,
	)
	if err != nil {
		return err
	}
	// TODO save encrypted deploy key in git repo
	if err := repoConfigurator.CreateDeployKeySecretIfNotExists(ctx, project.ControllerName); err != nil {
		return err
	}
	if err := secret.NewManager(act.projectRoot, instOpts.namespace, act.kubeClient, 1).CreateKeyIfNotExists(ctx, project.ControllerName); err != nil {
		return err
	}
	return nil
}

func (act Action) installObject(
	ctx context.Context,
	unstr *unstructured.Unstructured,
	fieldManager string,
) error {
	_, err := act.kubeClient.Get(ctx, unstr)
	if err != nil {
		if k8sErrors.ReasonForError(err) != metav1.StatusReasonNotFound {
			return err
		}
	}
	kind, _ := unstr.Object["kind"].(string)
	if kind == "GitOpsProject" {
		timeoutCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		for {
			select {
			case <-timeoutCtx.Done():
				return fmt.Errorf("%w: %w", ctx.Err(), err)
			default:
			}
			err = act.kubeClient.Apply(ctx, unstr, fieldManager)
			if err == nil {
				return nil
			}
			if k8sErrors.ReasonForError(err) != metav1.StatusReasonNotFound {
				return err
			}
			time.Sleep(1 * time.Second)
		}
	}
	if err := act.kubeClient.Apply(ctx, unstr, fieldManager); err != nil {
		return err
	}
	if kind == "CustomResourceDefinition" {
		// clear cache because we just introduced a new crd
		if err := act.kubeClient.Invalidate(); err != nil {
			return err
		}
	}
	return nil
}
