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

package project_test

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"

	gitops "github.com/kharf/navecd/api/v1beta1"
	"github.com/kharf/navecd/internal/dnstest"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/internal/manifest"
	"github.com/kharf/navecd/internal/ocitest"
	"github.com/kharf/navecd/pkg/oci"
	"github.com/kharf/navecd/pkg/project"
	"gotest.tools/v3/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func defaultAssertion(
	t *testing.T,
	kubernetes *kubetest.Environment,
	registry *ocitest.Registry,
	projectName string,
	testProject string,
	digest string,
) {
	ctx := context.Background()
	var ns v1.Namespace
	err := kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: project.ControllerNamespace},
		&ns,
	)
	assert.NilError(t, err)

	var deployment appsv1.Deployment
	controllerName := fmt.Sprintf("%s-%s", "project-controller", projectName)
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{
			Name:      controllerName,
			Namespace: project.ControllerNamespace,
		},
		&deployment,
	)
	assert.NilError(t, err)

	var gitOpsProject gitops.GitOpsProject
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: projectName, Namespace: project.ControllerNamespace},
		&gitOpsProject,
	)
	assert.NilError(t, err)

	projectFile, err := os.Open(
		filepath.Join(
			testProject,
			fmt.Sprintf("navecd/%s_project.cue", projectName),
		),
	)
	assert.NilError(t, err)

	projectContent, err := io.ReadAll(projectFile)
	assert.NilError(t, err)

	var projectBuf bytes.Buffer
	projectTmpl, err := template.New("").Parse(manifest.Project)

	assert.NilError(t, err)
	err = projectTmpl.Execute(&projectBuf, map[string]any{
		"Name":                projectName,
		"Namespace":           project.ControllerNamespace,
		"Ref":                 ref,
		"Dir":                 dir,
		"PullIntervalSeconds": intervalInSeconds,
		"Url":                 filepath.Join(registry.Addr(), projectName),
		"Shard":               projectName,
	})
	assert.NilError(t, err)

	assert.Equal(t, string(projectContent), projectBuf.String())

	var service v1.Service
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: controllerName, Namespace: project.ControllerNamespace},
		&service,
	)
	assert.NilError(t, err)

	ociClient, err := oci.NewRepositoryClient(fmt.Sprintf("%s/%s", registry.Addr(), projectName))
	assert.NilError(t, err)
	projectClient := oci.NewProjectClient(ociClient)
	tmpDir, err := os.MkdirTemp("", "")
	assert.NilError(t, err)
	gotDigest, err := projectClient.LoadImage(ctx, ref, tmpDir)
	assert.NilError(t, err)
	assert.Equal(t, gotDigest, digest)
}

const (
	intervalInSeconds = 5
	ref               = "test"
	dir               = "dev"
)

type testContext struct {
	kubernetes *kubetest.Environment
	registry   *ocitest.Registry
}

func TestInstallAction_Install(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	testCases := []struct {
		name string
		test func(*testing.T, testContext)
	}{
		{
			name: "Fresh",
			test: fresh,
		},
		{
			name: "Multi-Tenancy",
			test: multiTenancy,
		},
		{
			name: "Run-Twice",
			test: runTwice,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			kubernetes := kubetest.StartKubetestEnv(t, logr.Discard(), kubetest.WithEnabled(true))
			defer kubernetes.Stop()
			registry, err := ocitest.NewTLSRegistryWithSchema()
			assert.NilError(t, err)
			defer registry.Close()
			tc.test(t, testContext{
				kubernetes: kubernetes,
				registry:   registry,
			})
		})
	}
}

func fresh(t *testing.T, testContext testContext) {
	projectName := "fresh"
	kubernetes := testContext.kubernetes
	registry := testContext.registry

	testProject := t.TempDir()
	err := project.Init(
		"github.com/owner/repo/installation",
		projectName,
		"image",
		false,
		testProject,
		"0.0.99",
	)
	assert.NilError(t, err)

	action := project.NewInstallAction(
		kubernetes.DynamicTestKubeClient.DynamicClient(),
		http.DefaultClient,
		testProject,
	)

	ctx := context.Background()
	digest, err := action.Install(
		ctx,
		project.InstallOptions{
			Name:     projectName,
			Shard:    projectName,
			Ref:      ref,
			Dir:      dir,
			Interval: intervalInSeconds,
			Url:      fmt.Sprintf("%s/%s", registry.Addr(), projectName),
		},
	)
	assert.NilError(t, err)

	defaultAssertion(t, kubernetes, registry, projectName, testProject, digest)
}

func multiTenancy(t *testing.T, testContext testContext) {
	projectName := "primary"
	kubernetes := testContext.kubernetes
	registry := testContext.registry

	testProject := t.TempDir()
	err := project.Init(
		"github.com/owner/repo/installation",
		projectName,
		"image",
		false,
		testProject,
		"0.0.99",
	)
	assert.NilError(t, err)

	action := project.NewInstallAction(
		kubernetes.DynamicTestKubeClient.DynamicClient(),
		http.DefaultClient,
		testProject,
	)

	ctx := context.Background()
	digest, err := action.Install(
		ctx,
		project.InstallOptions{
			Name:     projectName,
			Shard:    projectName,
			Ref:      ref,
			Dir:      dir,
			Interval: intervalInSeconds,
			Url:      filepath.Join(registry.Addr(), projectName),
		},
	)
	assert.NilError(t, err)

	defaultAssertion(t, kubernetes, registry, projectName, testProject, digest)

	secondaryProjectName := "secondary"
	action = project.NewInstallAction(
		kubernetes.DynamicTestKubeClient.DynamicClient(),
		http.DefaultClient,
		testProject,
	)

	err = project.Init(
		"github.com/owner/repo/installation",
		secondaryProjectName,
		"image",
		true,
		testProject,
		"0.0.99",
	)
	assert.NilError(t, err)

	digest, err = action.Install(
		ctx,
		project.InstallOptions{
			Name:     secondaryProjectName,
			Shard:    secondaryProjectName,
			Ref:      ref,
			Dir:      dir,
			Interval: intervalInSeconds,
			Url:      filepath.Join(registry.Addr(), secondaryProjectName),
		},
	)
	assert.NilError(t, err)

	defaultAssertion(t, kubernetes, registry, secondaryProjectName, testProject, digest)
}

func runTwice(t *testing.T, testContext testContext) {
	projectName := "runtwice"
	kubernetes := testContext.kubernetes
	registry := testContext.registry

	testProject := t.TempDir()
	err := project.Init(
		"github.com/owner/repo/installation",
		projectName,
		"image",
		false,
		testProject,
		"0.0.99",
	)
	assert.NilError(t, err)

	action := project.NewInstallAction(
		kubernetes.DynamicTestKubeClient.DynamicClient(),
		http.DefaultClient,
		testProject,
	)

	ctx := context.Background()
	digest, err := action.Install(
		ctx,
		project.InstallOptions{
			Name:     projectName,
			Shard:    projectName,
			Ref:      ref,
			Dir:      dir,
			Interval: intervalInSeconds,
			Url:      filepath.Join(registry.Addr(), projectName),
		},
	)
	assert.NilError(t, err)

	defaultAssertion(t, kubernetes, registry, projectName, testProject, digest)

	digest, err = action.Install(
		ctx,
		project.InstallOptions{
			Ref:      ref,
			Dir:      dir,
			Interval: intervalInSeconds,
			Name:     projectName,
			Shard:    projectName,
			Url:      filepath.Join(registry.Addr(), projectName),
		},
	)
	assert.NilError(t, err)
	defaultAssertion(t, kubernetes, registry, projectName, testProject, digest)
}
