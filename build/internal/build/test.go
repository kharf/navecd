package build

import (
	"context"
	"path/filepath"

	"dagger.io/dagger"
)

type Test struct {
	ID      string
	Package string
}

const TestAllArg = "./..."

var TestAll = Test{ID: "./..."}

var _ step = (*Test)(nil)

func (t Test) name() string {
	if t.ID != TestAllArg {
		return "Test " + t.ID
	}
	return "Tests"
}

func (t Test) run(ctx context.Context, request stepRequest) (*stepResult, error) {
	testBase := request.container.
		WithExec([]string{"go", "install", "sigs.k8s.io/controller-runtime/tools/setup-envtest@latest"}).
		WithExec([]string{envTest, "use", "1.26.1", "--bin-dir", localBin, "-p", "path"})

	apiServerPath, err := testBase.Stdout(ctx)
	if err != nil {
		return nil, err
	}

	prepareTest := testBase.WithExec([]string{"mkdir", "-p", declTmp}).
		WithExec([]string{"cp", "test/testdata/simple", "-r", declTmp}).
		WithWorkdir(filepath.Join(declTmp, "simple")).
		WithExec([]string{"git", "init", "."}).
		WithExec([]string{"git", "config", "user.email", "test@test.com"}).
		WithExec([]string{"git", "config", "user.name", "test"}).
		WithExec([]string{"git", "add", "."}).
		WithExec([]string{"git", "commit", "-m", "\"init\""}).
		WithWorkdir(workDir).
		WithEnvVariable("KUBEBUILDER_ASSETS", filepath.Join(workDir, apiServerPath))

	var test *dagger.Container
	if t.ID == TestAllArg {
		test = prepareTest.
			WithExec([]string{"go", "test", "-v", TestAllArg, "-coverprofile", "cover.out"})
	} else {
		if t.Package == "" {
			test = prepareTest.
				WithExec([]string{"go", "test", "-v", "-run", t.ID})
		} else {
			test = prepareTest.
				WithExec([]string{"go", "test", "-v", "./" + t.Package, "-run", t.ID})
		}
	}

	return &stepResult{
		container: test.WithWorkdir(workDir),
	}, nil
}
