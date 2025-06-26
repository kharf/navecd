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
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"

	"github.com/google/go-cmp/cmp"
	gitops "github.com/kharf/navecd/api/v1beta1"
	"github.com/kharf/navecd/internal/dnstest"
	"github.com/kharf/navecd/internal/gittest"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/internal/manifest"
	"github.com/kharf/navecd/internal/ocitest"
	"github.com/kharf/navecd/pkg/project"
	"github.com/kharf/navecd/pkg/vcs"
	"gotest.tools/v3/assert"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func defaultAssertion(
	t *testing.T,
	kubernetes *kubetest.Environment,
	projectName string,
	testProject string,
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
		"Branch":              branch,
		"PullIntervalSeconds": intervalInSeconds,
		"Url":                 url,
		"Shard":               projectName,
	})
	assert.NilError(t, err)

	assert.Equal(t, string(projectContent), projectBuf.String())

	var vcsKey v1.Secret
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{
			Name:      vcs.SecretName(projectName),
			Namespace: project.ControllerNamespace,
		},
		&vcsKey,
	)
	assert.NilError(t, err)

	var knownHostsCm v1.ConfigMap
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: "known-hosts", Namespace: project.ControllerNamespace},
		&knownHostsCm,
	)
	assert.NilError(t, err)

	var service v1.Service
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{Name: controllerName, Namespace: project.ControllerNamespace},
		&service,
	)
	assert.NilError(t, err)
}

const (
	intervalInSeconds = 5
	url               = "git@github.com:owner/repo.git"
	branch            = "main"
)

type testContext struct {
	kubernetes *kubetest.Environment
}

func TestInstallAction_Install(t *testing.T) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	cueModuleRegistry, err := ocitest.StartCUERegistry(t.TempDir())
	assert.NilError(t, err)
	defer cueModuleRegistry.Close()

	testCases := []struct {
		name string
		test func(*testing.T, testContext)
	}{
		{
			name: "Fresh",
			test: fresh,
		},
		{
			name: "Persist-Token",
			test: persistToken,
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
			tc.test(t, testContext{
				kubernetes: kubernetes,
			})
		})
	}
}

func fresh(t *testing.T, testContext testContext) {
	projectName := "fresh"
	kubernetes := testContext.kubernetes

	var server *httptest.Server
	server, client := gittest.MockGitProvider(
		t,
		"owner/repo",
		fmt.Sprintf("navecd-%s", projectName),
		nil,
		nil,
	)
	defer server.Close()

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
		client,
		testProject,
	)

	ctx := context.Background()
	err = action.Install(
		ctx,
		project.InstallOptions{
			Name:     projectName,
			Shard:    projectName,
			Branch:   branch,
			Interval: intervalInSeconds,
			Url:      url,
			Token:    "aaaa",
		},
	)
	assert.NilError(t, err)

	defaultAssertion(t, kubernetes, projectName, testProject)
}

func persistToken(t *testing.T, testContext testContext) {
	projectName := "token"
	kubernetes := testContext.kubernetes

	var server *httptest.Server
	server, client := gittest.MockGitProvider(
		t,
		"owner/repo",
		fmt.Sprintf("navecd-%s", projectName),
		nil,
		nil,
	)
	defer server.Close()

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
		client,
		testProject,
	)

	ctx := context.Background()
	token := "aaaa"
	err = action.Install(
		ctx,
		project.InstallOptions{
			Name:         projectName,
			Shard:        projectName,
			Branch:       branch,
			Interval:     intervalInSeconds,
			Url:          url,
			Token:        token,
			PersistToken: true,
		},
	)
	assert.NilError(t, err)

	defaultAssertion(t, kubernetes, projectName, testProject)

	var vcsKey v1.Secret
	err = kubernetes.TestKubeClient.Get(
		ctx,
		types.NamespacedName{
			Name:      vcs.SecretName(projectName),
			Namespace: project.ControllerNamespace,
		},
		&vcsKey,
	)
	assert.NilError(t, err)
	persistedToken, _ := vcsKey.Data[vcs.Token]
	assert.Equal(t, string(persistedToken), token)
}

func multiTenancy(t *testing.T, testContext testContext) {
	projectName := "primary"
	kubernetes := testContext.kubernetes

	server, client := gittest.MockGitProvider(
		t,
		"owner/repo",
		fmt.Sprintf("navecd-%s", projectName),
		nil,
		nil,
	)
	defer server.Close()

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
		client,
		testProject,
	)

	ctx := context.Background()
	err = action.Install(
		ctx,
		project.InstallOptions{
			Name:     projectName,
			Shard:    projectName,
			Branch:   branch,
			Interval: intervalInSeconds,
			Url:      url,
			Token:    "aaaa",
		},
	)
	assert.NilError(t, err)

	defaultAssertion(t, kubernetes, projectName, testProject)

	secondaryProjectName := "secondary"
	server, client = gittest.MockGitProvider(
		t,
		"owner/repo",
		fmt.Sprintf("navecd-%s", secondaryProjectName),
		nil,
		nil,
	)
	defer server.Close()
	action = project.NewInstallAction(
		kubernetes.DynamicTestKubeClient.DynamicClient(),
		client,
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

	err = action.Install(
		ctx,
		project.InstallOptions{
			Name:     secondaryProjectName,
			Shard:    secondaryProjectName,
			Branch:   branch,
			Interval: intervalInSeconds,
			Url:      url,
			Token:    "aaaa",
		},
	)
	assert.NilError(t, err)

	defaultAssertion(t, kubernetes, secondaryProjectName, testProject)
}

func runTwice(t *testing.T, testContext testContext) {
	projectName := "runtwice"
	kubernetes := testContext.kubernetes

	server, client := gittest.MockGitProvider(
		t,
		"owner/repo",
		fmt.Sprintf("navecd-%s", projectName),
		nil,
		nil,
	)
	defer server.Close()

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
		client,
		testProject,
	)

	ctx := context.Background()
	err = action.Install(
		ctx,
		project.InstallOptions{
			Name:     projectName,
			Shard:    projectName,
			Branch:   branch,
			Interval: intervalInSeconds,
			Url:      url,
			Token:    "aaaa",
		},
	)
	assert.NilError(t, err)

	defaultAssertion(t, kubernetes, projectName, testProject)

	var getSecret func() v1.Secret
	getSecret = func() v1.Secret {
		var vcsKey v1.Secret
		err := kubernetes.TestKubeClient.Get(
			ctx,
			types.NamespacedName{
				Name:      vcs.SecretName(projectName),
				Namespace: project.ControllerNamespace,
			},
			&vcsKey,
		)
		assert.NilError(t, err)
		return vcsKey
	}
	vcsKeyBefore := getSecret()
	err = action.Install(
		ctx,
		project.InstallOptions{
			Branch:   branch,
			Interval: intervalInSeconds,
			Name:     projectName,
			Shard:    projectName,
			Url:      url,
			Token:    "aaaa",
		},
	)
	assert.NilError(t, err)
	defaultAssertion(t, kubernetes, projectName, testProject)
	vcsKeyAfter := getSecret()
	assert.Assert(t, cmp.Equal(vcsKeyAfter.Data, vcsKeyBefore.Data))
}
