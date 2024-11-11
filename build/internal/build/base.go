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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"dagger.io/dagger"
)

var (
	// tool binaries
	localBin      = "bin"
	envTest       = filepath.Join(localBin, "setup-envtest")
	controllerGen = filepath.Join(localBin, "controller-gen")
	workDir       = "/navecd"
	tmp           = "/tmp"
)

type step interface {
	name() string
	run(context.Context, stepRequest) (*stepResult, error)
}

type stepRequest struct {
	container *dagger.Container
	client    *dagger.Client
}

type stepResult struct {
	container *dagger.Container
}

func RunWith(steps ...step) error {
	ctx := context.Background()
	client, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stdout), dagger.WithWorkdir(".."))
	if err != nil {
		return err
	}
	defer client.Close()
	goCache := client.CacheVolume("go")
	base := client.Container().
		From("golang:1.23.3-alpine").
		WithExec([]string{"apk", "add", "--no-cache", "git"}).
		WithExec([]string{"apk", "add", "--no-cache", "openssh-client"}).
		WithExec([]string{"apk", "add", "--no-cache", "curl"}).
		WithExec([]string{"apk", "add", "--no-cache", "docker"}).
		WithDirectory(workDir, client.Host().Directory("."), dagger.ContainerWithDirectoryOpts{
			Include: []string{
				".git",
				".gitignore",
				".github",
				".goreleaser.yaml",
				"cmd",
				"pkg",
				"internal",
				"schema",
				"test",
				"api",
				"go.mod",
				"go.sum",
				"Dockerfile",
				"build/cue.mod",
				"build/gen_tool.cue",
				"build/workflows.cue",
			},
		}).
		WithMountedCache("/go", goCache).
		WithWorkdir(workDir).
		WithEnvVariable("GOBIN", filepath.Join(workDir, localBin)).
		WithEnvVariable("TMPDIR", tmp)
	latestContainer := base
	for _, step := range steps {
		stepResult, err := step.run(ctx, stepRequest{client: client, container: latestContainer})
		if err != nil {
			return err
		}
		if stepResult.container != nil {
			_, err := stepResult.container.Stderr(ctx)
			if err != nil {
				return err
			}
			latestContainer = stepResult.container
		}
		fmt.Println("\033[32m", strings.ToUpper(step.name())+" passed!")
	}

	return nil
}
