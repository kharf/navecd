package main

import (
	"context"
	"dagger/navecd/internal/dagger"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type Navecd struct{}

var (
	// tool binaries
	localBin      = "bin"
	envTest       = filepath.Join(localBin, "setup-envtest")
	controllerGen = filepath.Join(localBin, "controller-gen")
	workDir       = "/navecd"
	tmp           = "/tmp"
)

func (n *Navecd) buildEnv(source *dagger.Directory) *dagger.Container {
	goCache := dag.CacheVolume("go")
	return dag.Container().
		From("golang:1.25.1-alpine").
		WithExec([]string{"apk", "add", "--no-cache", "git"}).
		WithExec([]string{"apk", "add", "--no-cache", "openssh-client"}).
		WithExec([]string{"apk", "add", "--no-cache", "curl"}).
		WithExec([]string{"apk", "add", "--no-cache", "docker"}).
		WithDirectory(workDir, source, dagger.ContainerWithDirectoryOpts{
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
			},
		}).
		WithMountedCache("/go", goCache).
		WithWorkdir(workDir).
		WithEnvVariable("GOBIN", filepath.Join(workDir, localBin)).
		WithEnvVariable("TMPDIR", tmp)
}

// when changed, the renovate customManager has also to be updated.
var kubernetesVersion = "v1.34.0"

func (n *Navecd) kubernetesTestEnv(
	ctx context.Context,
	base *dagger.Container,
) (*dagger.Container, error) {
	kubernetesVersion, _ = strings.CutPrefix(kubernetesVersion, "v")
	container := base.
		WithExec(
			[]string{"go", "install", "sigs.k8s.io/controller-runtime/tools/setup-envtest@latest"},
		).
		WithExec([]string{envTest, "use", kubernetesVersion, "--bin-dir", localBin, "-p", "path"})

	apiServerPath, err := container.Stdout(ctx)
	if err != nil {
		return nil, err
	}

	container = container.WithEnvVariable(
		"KUBEBUILDER_ASSETS",
		filepath.Join(workDir, apiServerPath),
	)

	return container, nil
}

// when changed, the renovate customManager has also to be updated.
var controllerGenDep = "sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0"

// when changed, the renovate customManager has also to be updated.
var cueDep = "cuelang.org/go/cmd/cue@v0.14.1"

func (n *Navecd) GenApi(source *dagger.Directory) *dagger.File {
	return n.buildEnv(source).
		WithExec([]string{"go", "install", controllerGenDep}).
		WithExec([]string{"go", "install", cueDep}).
		WithExec([]string{controllerGen, "crd", "paths=./api/v1beta1/...", "output:crd:artifacts:config=internal/manifest"}).
		WithExec([]string{"bin/cue", "import", "-f", "-o", "internal/manifest/crd.cue", "internal/manifest/gitops.navecd.io_gitopsprojects.yaml", "-l", "_crd:", "-p", "navecd"}).
		File("internal/manifest/crd.cue")
}

func (n *Navecd) Test(
	ctx context.Context,
	source *dagger.Directory,
	// +optional
	pkg string,
	// +optional
	test string,
) (string, error) {
	container, err := n.kubernetesTestEnv(ctx, n.buildEnv(source))
	if err != nil {
		return "", err
	}

	prepareTest := container.WithEnvVariable("CACHEBUSTER", time.Now().String())

	if pkg == "" && test == "" {
		return prepareTest.
			WithExec(
				[]string{
					"go",
					"test",
					"-v",
					"./...",
					"-coverprofile",
					"cover.out",
				},
			).
			Stdout(ctx)
	}

	return prepareTest.
		WithExec([]string{"go", "test", "-v", "./" + pkg, "-run", test}).
		Stdout(ctx)
}

// when changed, the renovate customManager has also to be updated.
var goreleaserDep = "github.com/goreleaser/goreleaser/v2@v2.12.5"

func (n *Navecd) Build(
	ctx context.Context,
	source *dagger.Directory,
) (*dagger.Directory, error) {
	bin := filepath.Join(workDir, localBin)
	build := n.buildEnv(source).
		WithoutEnvVariable("GOOS").
		WithoutEnvVariable("GOARCH").
		WithWorkdir(workDir).
		WithExec([]string{"go", "install", goreleaserDep}).
		WithEnvVariable("PATH", "$PATH:"+bin, dagger.ContainerWithEnvVariableOpts{Expand: true})

	build, err := build.
		WithExec(
			[]string{"goreleaser", "build", "--single-target", "--snapshot", "--clean"},
		).Sync(ctx)
	if err != nil {
		return nil, err
	}

	return build.Directory("./dist"), nil
}

func (n *Navecd) Release(
	ctx context.Context,
	source *dagger.Directory,
	// +optional
	previousVersion string,
	version string,
	token *dagger.Secret,
	user string,
) (string, error) {
	var prefixedVersion string
	if !strings.HasPrefix(version, "v") {
		prefixedVersion = "v" + version
	} else {
		prefixedVersion = version
		version, _ = strings.CutPrefix(version, "v")
	}

	bin := filepath.Join(workDir, localBin)
	publish := n.buildEnv(source).
		WithoutEnvVariable("GOOS").
		WithoutEnvVariable("GOARCH").
		WithExec([]string{"go", "install", cueDep}).
		WithEnvVariable("CUE_REGISTRY", "ghcr.io/kharf").
		WithSecretVariable("GITHUB_TOKEN", token).
		WithEnvVariable("GITHUB_USER", user).
		WithExec([]string{"sh", "-c", "echo $GITHUB_TOKEN | docker login ghcr.io -u $GITHUB_USER --password-stdin"}).
		WithWorkdir(workDir).
		WithExec([]string{"go", "install", goreleaserDep}).
		WithEnvVariable("PATH", "$PATH:"+bin, dagger.ContainerWithEnvVariableOpts{Expand: true}).
		WithExec(
			[]string{
				"sh",
				"-c",
				`git config --global url.https://$GITHUB_USER:$GITHUB_TOKEN@github.com/kharf/navecd.git.insteadOf git@github.com:kharf/navecd.git`,
			},
		).
		WithEnvVariable("CACHEBUSTER", time.Now().String()).
		WithExec([]string{"git", "tag", prefixedVersion}).
		WithExec([]string{"git", "push", "origin", prefixedVersion})

	if previousVersion != "" {
		publish = publish.WithEnvVariable("GORELEASER_PREVIOUS_TAG", previousVersion)
	}

	publish, err := publish.
		WithExec([]string{"goreleaser", "release", "--clean", "--skip=validate"}).
		WithWorkdir("schema").
		WithExec([]string{"../bin/cue", "mod", "publish", prefixedVersion}).
		Sync(ctx)
	if err != nil {
		return "", err
	}

	ref, err := publish.
		WithWorkdir(workDir).
		Directory(".").
		DockerBuild().
		WithRegistryAuth("ghcr.io", "kharf", token).
		WithAnnotation("org.opencontainers.image.title", "navecd").
		WithAnnotation("org.opencontainers.image.created", time.Now().String()).
		WithAnnotation("org.opencontainers.image.source", "https://github.com/kharf/navecd").
		WithAnnotation("org.opencontainers.image.url", "https://github.com/kharf/navecd").
		Publish(ctx, "ghcr.io/kharf/navecd:"+version)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("Published image to: %s", ref), nil

}

func (n *Navecd) GenWorkflows(ctx context.Context, source *dagger.Directory) *dagger.Directory {
	workflowsDir := "workflows"
	return n.buildEnv(source).
		WithWorkdir(".github").
		WithExec([]string{"mkdir", "-p", workflowsDir}).
		WithExec([]string{"go", "install", cueDep}).
		WithExec([]string{"../bin/cue", "cmd", "genyamlworkflows"}).
		Directory(workflowsDir)
}

func (n *Navecd) CommitWorkflows(
	ctx context.Context,
	source *dagger.Directory,
	token *dagger.Secret,
) (*dagger.Container, error) {
	commitContainer, err := n.buildEnv(source).
		WithExec([]string{"sh", "-c", "git diff --exit-code .github; echo -n $? > /exit_code"}).
		Sync(ctx)
	if err != nil {
		return nil, err
	}

	exitCode, err := commitContainer.File("/exit_code").Contents(ctx)
	if err != nil {
		return nil, err
	}

	if exitCode != "0" {
		return commitContainer.
			WithSecretVariable("GITHUB_TOKEN", token).
			WithExec([]string{"git", "config", "--global", "user.email", "kevinfritz210@gmail.com"}).
			WithExec([]string{"git", "config", "--global", "user.name", "Navecd Bot"}).
			WithExec([]string{"sh", "-c", "git remote set-url origin https://$GITHUB_TOKEN@github.com/kharf/navecd.git"}).
			WithExec([]string{"git", "add", ".github/workflows"}).
			WithExec([]string{"git", "commit", "-m", "chore: update yaml workflows"}).
			WithExec([]string{"git", "push"}), nil
	}

	return commitContainer, nil
}

func (n *Navecd) Update(ctx context.Context, token *dagger.Secret) (*dagger.Container, error) {
	return dag.Container().
		From("golang:1.25").
		WithEnvVariable("LOG_LEVEL", "DEBUG").
		WithSecretVariable("RENOVATE_TOKEN", token).
		WithEnvVariable("RENOVATE_REPOSITORIES", "kharf/navecd").
		WithExec([]string{"sh", "-c", "apt-get update; apt-get install -y --no-install-recommends npm"}).
		WithEnvVariable("NVM_DIR", "/root/.nvm").
		WithExec([]string{"sh", "-c", "curl https://raw.githubusercontent.com/creationix/nvm/master/install.sh | bash && . $NVM_DIR/nvm.sh && nvm install 20 && npm install -g renovate && renovate"}).
		Sync(ctx)
}
