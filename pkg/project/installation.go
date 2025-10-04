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
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kharf/navecd/internal/manifest"
	"github.com/kharf/navecd/pkg/cloud"
	"github.com/kharf/navecd/pkg/component"
	"github.com/kharf/navecd/pkg/kube"
	"github.com/kharf/navecd/pkg/oci"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var (
	ErrHelmInstallationUnsupported = errors.New("Helm installation not supported yet")
)

type InstallOptions struct {
	Url       string
	Ref       string
	Dir       string
	Name      string
	WIP       string
	SecretRef string
	Interval  int
	Shard     string
}

type InstallAction struct {
	kubeClient       *kube.DynamicClient
	httpClient       *http.Client
	componentBuilder component.Builder
	projectRoot      string
}

func NewInstallAction(
	kubeClient *kube.DynamicClient,
	httpClient *http.Client,
	projectRoot string,
) InstallAction {
	return InstallAction{
		kubeClient:  kubeClient,
		projectRoot: projectRoot,
		httpClient:  httpClient,
	}
}

func (act InstallAction) Install(ctx context.Context, opts InstallOptions) (string, error) {
	navecdDir := filepath.Join(act.projectRoot, "navecd")
	projectFileName := filepath.Join(navecdDir, fmt.Sprintf("%s_project.cue", opts.Name))

	_, err := os.Stat(projectFileName)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}

	if os.IsNotExist(err) {
		var projectBuf bytes.Buffer
		projectTmpl, err := template.New("").Parse(manifest.Project)
		if err != nil {
			return "", err
		}

		provider := ""
		secretRef := ""
		if opts.WIP != "" {
			switch opts.WIP {
			case string(cloud.AWS):
				provider = "AWS"

			case string(cloud.Azure):
				provider = "Azure"

			case string(cloud.GCP):
				provider = "GCP"
			}
		} else if opts.SecretRef != "" {
			secretRef = opts.SecretRef
		}

		if err := projectTmpl.Execute(&projectBuf, map[string]any{
			"Name":                opts.Name,
			"Namespace":           ControllerNamespace,
			"Url":                 opts.Url,
			"Ref":                 opts.Ref,
			"Dir":                 opts.Dir,
			"PullIntervalSeconds": opts.Interval,
			"Shard":               opts.Shard,
			"Provider":            provider,
			"SecretRef":           secretRef,
		}); err != nil {
			return "", err
		}

		if err := os.WriteFile(projectFileName, projectBuf.Bytes(), 0666); err != nil {
			return "", err
		}
	}

	buildResult, err := act.componentBuilder.Build(
		component.WithPackagePath("./navecd"),
		component.WithProjectRoot(act.projectRoot),
	)
	if err != nil {
		return "", err
	}

	dag := component.NewDependencyGraph()
	if err := dag.Insert(buildResult.Instances...); err != nil {
		return "", err
	}

	instances, err := dag.TopologicalSort()
	if err != nil {
		return "", err
	}

	controllerName := getControllerName(opts.Shard)
	for _, instance := range instances {
		manifest, ok := instance.(*component.Manifest)
		if !ok {
			return "", ErrHelmInstallationUnsupported
		}

		if opts.Shard == manifest.GetLabels()["navecd/shard"] {
			timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()

			if err := act.installObject(
				timeoutCtx,
				manifest.Content.Unstructured,
				controllerName,
			); err != nil {
				return "", err
			}
		}
	}

	ociClient, err := oci.NewRepositoryClient(opts.Url)
	if err != nil {
		return "", err
	}
	projectClient := oci.NewProjectClient(ociClient)

	digest, err := projectClient.PushImageFromPath(
		opts.Ref,
		act.projectRoot,
	)
	if err != nil {
		return "", err
	}

	return digest, nil
}

func (act InstallAction) installObject(
	ctx context.Context,
	unstr *unstructured.Unstructured,
	fieldManager string,
) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if _, err := act.kubeClient.Apply(ctx, unstr, fieldManager); err != nil {
		if k8sErrors.IsNotFound(err) {
			time.Sleep(1 * time.Second)
			return act.installObject(ctx, unstr, fieldManager)
		}
		return err
	}

	return nil
}
