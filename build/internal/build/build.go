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
	"time"

	"dagger.io/dagger"
)

var ApiGen apigen = "apigen"

type apigen string

var _ step = (*apigen)(nil)

func (g apigen) name() string {
	return string(g)
}

// when changed, the renovate customManager has also to be updated.
var controllerGenDep = "sigs.k8s.io/controller-tools/cmd/controller-gen@v0.16.5"

// when changed, the renovate customManager has also to be updated.
var cueDep = "cuelang.org/go/cmd/cue@v0.10.0"

func (g apigen) run(ctx context.Context, request stepRequest) (*stepResult, error) {
	gen := request.container.
		WithExec(
			[]string{"go", "install", controllerGenDep},
		).
		WithExec([]string{"go", "install", cueDep}).
		WithExec([]string{controllerGen, "crd", "paths=./api/v1beta1/...", "output:crd:artifacts:config=internal/manifest"}).
		WithExec([]string{"bin/cue", "import", "-f", "-o", "internal/manifest/crd.cue", "internal/manifest/gitops.navecd.io_gitopsprojects.yaml", "-l", "_crd:", "-p", "navecd"})
	_, err := gen.File("internal/manifest/crd.cue").
		Export(ctx, "internal/manifest/crd.cue", dagger.FileExportOpts{AllowParentDirPath: false})
	if err != nil {
		return nil, err
	}
	return &stepResult{
		container: gen,
	}, nil
}

type Publish struct {
	Version     string
	PreviousTag string
}

var _ step = (*Publish)(nil)

func (p Publish) name() string {
	return "publish"
}

// when changed, the renovate customManager has also to be updated.
var goreleaserDep = "github.com/goreleaser/goreleaser/v2@v2.3.2"

func (p Publish) run(ctx context.Context, request stepRequest) (*stepResult, error) {
	reqVersion := p.Version
	var prefixedVersion string
	var version string
	if !strings.HasPrefix(reqVersion, "v") {
		prefixedVersion = "v" + reqVersion
		version = reqVersion
	} else {
		prefixedVersion = reqVersion
		version, _ = strings.CutPrefix(reqVersion, "v")
	}

	token := request.client.SetSecret("token", os.Getenv("GITHUB_TOKEN"))

	bin := filepath.Join(workDir, localBin)
	publish := request.container.
		WithoutEnvVariable("GOOS").
		WithoutEnvVariable("GOARCH").
		WithExec([]string{"go", "install", cueDep}).
		WithEnvVariable("CUE_REGISTRY", "ghcr.io/kharf").
		WithSecretVariable("GITHUB_TOKEN", token).
		WithExec([]string{"sh", "-c", "docker login ghcr.io -u kharf -p $GITHUB_TOKEN"}).
		WithWorkdir("schema").
		WithExec([]string{"../bin/cue", "mod", "publish", prefixedVersion}).
		WithWorkdir(workDir).
		WithExec([]string{"go", "install", goreleaserDep}).
		WithEnvVariable("PATH", "$PATH:"+bin, dagger.ContainerWithEnvVariableOpts{Expand: true}).
		WithExec(
			[]string{
				"sh",
				"-c",
				`git config --global url.https://kharf:$GITHUB_TOKEN@github.com/kharf/navecd.git.insteadOf git@github.com:kharf/navecd.git`,
			},
		).
		WithExec([]string{"git", "tag", prefixedVersion}).
		WithExec([]string{"git", "push", "origin", prefixedVersion})

	if p.PreviousTag != "" {
		publish = publish.WithEnvVariable("GORELEASER_PREVIOUS_TAG", p.PreviousTag)
	}

	publish, err := publish.
		WithExec([]string{"goreleaser", "release", "--clean", "--skip=validate"}).Sync(ctx)
	if err != nil {
		return nil, err
	}

	ref, err := publish.
		Directory(".").
		DockerBuild().
		WithRegistryAuth("ghcr.io", "kharf", token).
		WithAnnotation("org.opencontainers.image.title", "navecd").
		WithAnnotation("org.opencontainers.image.created", time.Now().String()).
		WithAnnotation("org.opencontainers.image.source", "https://github.com/kharf/navecd").
		WithAnnotation("org.opencontainers.image.url", "https://github.com/kharf/navecd").
		Publish(ctx, "ghcr.io/kharf/navecd:"+version)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Published image to: %s\n", ref)
	return &stepResult{
		container: publish,
	}, nil
}
