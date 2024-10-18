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
	"path/filepath"
	"time"

	"dagger.io/dagger"
)

type Bench struct {
	ID      string
	Package string
}

const BenchAllArg = "./..."

var BenchAll = Test{ID: "./..."}

var _ step = (*Bench)(nil)

func (t Bench) name() string {
	if t.ID != TestAllArg {
		return "Benchmark " + t.ID
	}
	return "Benchmarks"
}

func (t Bench) run(ctx context.Context, request stepRequest) (*stepResult, error) {
	benchBase := request.container.
		WithExec(
			[]string{"go", "install", "sigs.k8s.io/controller-runtime/tools/setup-envtest@latest"},
		).
		WithExec([]string{envTest, "use", "1.26.1", "--bin-dir", localBin, "-p", "path"})
	apiServerPath, err := benchBase.Stdout(ctx)
	if err != nil {
		return nil, err
	}
	prepareBench := benchBase.WithWorkdir(workDir).
		WithEnvVariable("KUBEBUILDER_ASSETS", filepath.Join(workDir, apiServerPath)).
		WithEnvVariable("CACHEBUSTER", time.Now().String())
	var test *dagger.Container
	if t.ID == BenchAllArg {
		test = prepareBench.
			WithExec([]string{"go", "test", "-run=^$", "-bench=.", TestAllArg})
	} else {
		test = prepareBench.
			WithExec([]string{"go", "test", "-run=^$", "-count=5", "-cpu=1,2,4", "-bench=" + t.ID, "./" + t.Package})
	}
	return &stepResult{
		container: test.WithWorkdir(workDir),
	}, nil
}
