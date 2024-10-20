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

package build

import (
	"context"
	"os"
)

type WorkflowsGen struct {
	Export bool
}

var _ step = (*WorkflowsGen)(nil)

func (_ WorkflowsGen) name() string {
	return "Generate Workflows"
}

func (workflow WorkflowsGen) run(ctx context.Context, request stepRequest) (*stepResult, error) {
	workflowsDir := ".github/workflows"
	gen := request.container.
		WithExec([]string{"mkdir", "-p", workflowsDir}).
		WithExec([]string{"go", "install", cueDep}).
		WithEnvVariable("CUE_REGISTRY", "ghcr.io/kharf").
		WithWorkdir("build").
		WithExec([]string{"../bin/cue", "cmd", "genyamlworkflows"}).
		WithWorkdir(workDir)

	if workflow.Export {
		_, err := gen.Directory(workflowsDir).Export(ctx, workflowsDir)
		if err != nil {
			return nil, err
		}
	}

	return &stepResult{
		container: gen,
	}, nil
}

type commitWorkflows struct{}

var CommitWorkflows = commitWorkflows{}

var _ step = (*commitWorkflows)(nil)

func (_ commitWorkflows) name() string {
	return "Commit Workflows"
}

func (_ commitWorkflows) run(ctx context.Context, request stepRequest) (*stepResult, error) {
	pat := request.client.SetSecret("gh-token", os.Getenv("GH_PAT"))

	commitContainer, err := request.container.
		WithExec([]string{"sh", "-c", "git diff --exit-code .github; echo -n $? > /exit_code"}).
		Sync(ctx)
	if err != nil {
		return nil, err
	}

	exitCode, err := commitContainer.File("/exit_code").Contents(ctx)
	if err != nil {
		return nil, err
	}

	lastContainer := commitContainer
	if exitCode != "0" {
		lastContainer = commitContainer.
			WithSecretVariable("GH_PAT", pat).
			WithExec([]string{"git", "config", "--global", "user.email", "kevinfritz210@gmail.com"}).
			WithExec([]string{"git", "config", "--global", "user.name", "Navecd Bot"}).
			WithExec([]string{"sh", "-c", "git remote set-url origin https://$GH_PAT@github.com/kharf/navecd.git"}).
			WithExec([]string{"git", "add", ".github/workflows"}).
			WithExec([]string{"git", "commit", "-m", "chore: update yaml workflows"}).
			WithExec([]string{"git", "push"})
	}

	return &stepResult{
		container: lastContainer,
	}, nil
}
