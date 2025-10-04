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

package projecttest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"text/template"

	"github.com/go-logr/logr"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/internal/ocitest"
	"github.com/kharf/navecd/internal/txtar"
	"github.com/kharf/navecd/pkg/oci"
	"github.com/kharf/navecd/pkg/project"
	"go.uber.org/zap/zapcore"
	"gotest.tools/v3/assert"
	ctrlZap "sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type Environment struct {
	Log         logr.Logger
	TestRoot    string
	OCIRegistry *ocitest.Registry
}

type Option interface {
	Apply(opts *options)
}

type options struct {
	projectSources []string
	kubeOpts       []kubetest.Option
}

type WithProjectSource string

var _ Option = (*WithProjectSource)(nil)

func (opt WithProjectSource) Apply(opts *options) {
	opts.projectSources = append(opts.projectSources, string(opt))
}

type withKubernetes []kubetest.Option

func WithKubernetes(opts ...kubetest.Option) withKubernetes {
	return opts
}

var _ Option = (*WithProjectSource)(nil)

func (opt withKubernetes) Apply(opts *options) {
	opts.kubeOpts = opt
}

func InitTestEnvironment(t testing.TB, opts ...ocitest.Option) Environment {
	testRoot := t.TempDir()
	logOpts := ctrlZap.Options{
		Development: false,
		Level:       zapcore.Level(-1),
	}
	log := ctrlZap.New(ctrlZap.UseFlagOptions(&logOpts))

	registry, err := ocitest.NewTLSRegistryWithSchema(opts...)
	assert.NilError(t, err)

	env := Environment{}
	env.TestRoot = testRoot
	env.OCIRegistry = registry
	env.Log = log
	return env
}

func (env *Environment) PushProject(t testing.TB, projectName string, tag string, txtarData []byte) project.OCIRepositoryRef {
	tmpDir, err := os.MkdirTemp(t.TempDir(), "*")
	assert.NilError(t, err)
	_, err = txtar.Create(tmpDir, bytes.NewReader(txtarData))
	assert.NilError(t, err)

	repoName := fmt.Sprintf("%s/%s", env.OCIRegistry.Addr(), projectName)
	ociClient, err := oci.NewRepositoryClient(repoName)
	assert.NilError(t, err)
	projectClient := oci.NewProjectClient(ociClient)

	var user string
	var pw string
	switch {
	case env.OCIRegistry.GCP() != nil:
		user = "oauth2accesstoken"
		pw = "aaaa"
	case env.OCIRegistry.Azure() != nil:
		user = "00000000-0000-0000-0000-000000000000"
		pw = "aaaa"
	default:
		user = "navecd"
		pw = "abcd"
	}

	_, err = projectClient.PushImageFromPath(
		tag,
		tmpDir,
		oci.WithRepositoryOption(
			oci.WithBasicAuth(user, pw),
		),
	)
	assert.NilError(t, err)
	return project.OCIRepositoryRef{
		Name: repoName,
		Ref:  tag,
	}
}

func (env *Environment) Close() {
	env.OCIRegistry.Close()
}

type Template struct {
	TestProjectPath  string
	RelativeFilePath string
	Data             any
}

func ReplaceTemplate(
	tmpl Template,
) error {
	releasesFilePath := filepath.Join(
		tmpl.TestProjectPath,
		tmpl.RelativeFilePath,
	)

	releasesContent, err := os.ReadFile(releasesFilePath)
	if err != nil {
		return err
	}

	parsedTemplate, err := template.New("").Parse(string(releasesContent))
	if err != nil {
		return err
	}

	releasesFile, err := os.Create(releasesFilePath)
	if err != nil {
		return err
	}
	defer releasesFile.Close()

	err = parsedTemplate.Execute(releasesFile, tmpl.Data)
	if err != nil {
		return err
	}

	return nil
}
