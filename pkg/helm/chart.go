package helm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/go-logr/logr"
	"github.com/kharf/declcd/pkg/kube"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/storage/driver"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	ErrNoChartURLs        = errors.New("helm chart does not provide download urls")
	ErrPullFailed         = errors.New("could not pull helm chart")
	ErrHelmChartVersion   = errors.New("helm chart version error")
	ErrManifestNoMetadata = errors.New("helm chart manifest has no metadata")
)

type Chart struct {
	Name    string `json:"name"`
	RepoURL string `json:"repoURL"`
	Version string `json:"version"`
}

type ChartReconciler struct {
	Cfg    action.Configuration
	Log    logr.Logger
	Client *kube.Client
}

type options struct {
	releaseName string
	namespace   string
	values      map[string]interface{}
}

type option interface {
	Apply(opts *options)
}

type ReleaseName string

func (r ReleaseName) Apply(opts *options) {
	opts.releaseName = string(r)
}

type Namespace string

func (n Namespace) Apply(opts *options) {
	opts.namespace = string(n)
}

type Values map[string]interface{}

func (v Values) Apply(opts *options) {
	opts.values = v
}

func (c ChartReconciler) Reconcile(ctx context.Context, chart Chart, opts ...option) (*release.Release, error) {
	reconcileOpts := &options{}
	for _, opt := range opts {
		opt.Apply(reconcileOpts)
	}
	releaseName := chart.Name
	if reconcileOpts.releaseName != "" {
		releaseName = reconcileOpts.releaseName
	}
	namespace := "default"
	if reconcileOpts.namespace != "" {
		namespace = reconcileOpts.namespace
	}
	logArgs := []interface{}{"name", chart.Name, "url", chart.RepoURL, "version", chart.Version, "releasename", releaseName, "namespace", namespace}
	c.Log.Info("pulling chart", logArgs...)
	chrt, err := c.pull(chart)
	if err != nil {
		return nil, err
	}
	histClient := action.NewHistory(&c.Cfg)
	histClient.Max = 1
	var releases []*release.Release
	if releases, err = histClient.Run(releaseName); err == driver.ErrReleaseNotFound {
		client := action.NewInstall(&c.Cfg)
		client.Wait = false
		client.ReleaseName = releaseName
		client.CreateNamespace = true
		client.Namespace = namespace
		c.Log.Info("installing chart", logArgs...)
		release, err := client.Run(chrt, reconcileOpts.values)
		if err != nil {
			c.Log.Error(err, "installing chart failed", logArgs...)
			return nil, err
		}
		c.Log.Info("installing chart finished", logArgs...)
		return release, nil
	}

	diff, err := c.diff(ctx, chrt, releaseName, reconcileOpts.values, namespace)
	if err != nil {
		return nil, err
	}

	if !diff {
		c.Log.Info("no changes", logArgs...)
		return releases[0], nil
	}

	upgrade := action.NewUpgrade(&c.Cfg)
	upgrade.Wait = false
	upgrade.Namespace = namespace
	c.Log.Info("upgrading chart", logArgs...)
	release, err := upgrade.Run(releaseName, chrt, reconcileOpts.values)
	if err != nil {
		c.Log.Error(err, "upgrading chart failed", logArgs...)
		return nil, err
	}
	c.Log.Info("upgrading chart finished", logArgs...)
	return release, nil
}

func (c ChartReconciler) diff(ctx context.Context, chrt *chart.Chart, releaseName string, values Values, namespace string) (bool, error) {
	upgrade := action.NewUpgrade(&c.Cfg)
	upgrade.Wait = false
	upgrade.Namespace = namespace
	upgrade.DryRun = true
	release, err := upgrade.Run(releaseName, chrt, values)
	if err != nil {
		return false, err
	}

	newManifests := make([]*unstructured.Unstructured, 0, 3)
	decoder := yaml.NewDecoder(bytes.NewBufferString(release.Manifest))
	for {
		var unstr map[string]interface{}
		if err = decoder.Decode(&unstr); err != nil {
			if err == io.EOF {
				break
			}
			return false, err
		}
		newManifests = append(newManifests, &unstructured.Unstructured{Object: unstr})
	}

	for _, manifest := range newManifests {
		groupVersion := strings.Split(manifest.GetAPIVersion(), "/")
		group := ""
		var version string
		if len(groupVersion) == 1 {
			version = groupVersion[0]
		} else {
			group = groupVersion[0]
			version = groupVersion[1]
		}
		name := ""
		if metadata, ok := manifest.Object["metadata"].(map[interface{}]interface{}); ok {
			name = metadata["name"].(string)
		} else {
			return false, ErrManifestNoMetadata
		}
		obj, err := c.Client.Get(ctx, schema.GroupVersionKind{
			Group:   group,
			Version: version,
			Kind:    manifest.GetKind(),
		}, name, namespace)
		if err != nil {
			return false, err
		}

		if obj == nil {
			return true, nil
		}

		if !equality.Semantic.DeepEqual(obj.Object["spec"], manifest.Object["spec"]) {
			fmt.Println("it diffs", obj.GetKind())
			fmt.Println(obj.Object["spec"])
			fmt.Println(manifest.Object["spec"])
			return true, nil
		}
	}

	return false, nil
}

func (c ChartReconciler) pull(chartRequest Chart) (*chart.Chart, error) {
	var err error
	getters := []getter.Provider{
		{
			Schemes: []string{"http", "https"},
			New:     getter.NewHTTPGetter,
		},
	}
	chartDownloader := downloader.ChartDownloader{
		Out:     os.Stdout,
		Getters: getters,
	}
	entry := &repo.Entry{
		URL:  chartRequest.RepoURL,
		Name: chartRequest.Name,
	}
	chartRepo, err := repo.NewChartRepository(entry, getters)
	if err != nil {
		return nil, err
	}
	path, err := chartRepo.DownloadIndexFile()
	if err != nil {
		return nil, err
	}

	index, err := repo.LoadIndexFile(path)
	if err != nil {
		return nil, err
	}

	chartVersion, err := index.Get(chartRequest.Name, chartRequest.Version)
	if err != nil {
		return nil, fmt.Errorf("%w: version: %s not found: %w", ErrHelmChartVersion, chartRequest.Version, err)
	}

	if len(chartVersion.URLs) < 1 {
		return nil, ErrNoChartURLs
	}

	absoluteChartURL, err := repo.ResolveReferenceURL(chartRequest.RepoURL, chartVersion.URLs[0])
	if err != nil {
		return nil, err
	}

	dest, err := os.MkdirTemp("", "")
	if err != nil {
		return nil, err
	}

	chartPath, _, err := chartDownloader.DownloadTo(absoluteChartURL, chartRequest.Version, dest)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPullFailed, err)
	}

	chart, err := loader.Load(chartPath)
	if err != nil {
		return nil, err
	}

	return chart, nil
}
