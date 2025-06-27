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
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kharf/navecd/pkg/component"
	"github.com/kharf/navecd/pkg/version"
	"golang.org/x/sync/errgroup"
)

var (
	ErrLoadProject = errors.New("Could not load project")
)

// Manager loads a navecd project and resolves the component dependency graph.
type Manager struct {
	componentBuilder component.Builder
	workerPoolSize   int
}

func NewManager(componentBuilder component.Builder, workerPoolSize int) Manager {
	return Manager{
		componentBuilder: componentBuilder,
		workerPoolSize:   workerPoolSize,
	}
}

// Instance represents the loaded project.
type Instance struct {
	Dag                *component.DependencyGraph
	UpdateInstructions []version.UpdateInstruction
}

// Load uses a given path to a project and returns the components as a directed acyclic dependency graph.
func (manager *Manager) Load(
	projectPath string,
	dir string,
) (*Instance, error) {
	projectPath = strings.TrimSuffix(projectPath, "/")
	if _, err := os.Stat(projectPath); errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	producerEg := &errgroup.Group{}
	producerEg.SetLimit(manager.workerPoolSize)

	resultChan := make(chan *Instance, 1)
	packageChan := make(chan string, 250)

	consumerEg := &errgroup.Group{}
	consumerEg.Go(func() error {
		dag := component.NewDependencyGraph()
		var updateInstructions []version.UpdateInstruction
		for packagePath := range packageChan {
			buildResult, err := manager.componentBuilder.Build(
				component.WithProjectRoot(projectPath),
				component.WithPackagePath(packagePath),
			)
			if err != nil {
				return err
			}

			if err := dag.Insert(buildResult.Instances...); err != nil {
				return fmt.Errorf("%w: %w", ErrLoadProject, err)
			}
			updateInstructions = append(updateInstructions, buildResult.UpdateInstructions...)
		}

		resultChan <- &Instance{
			Dag:                &dag,
			UpdateInstructions: updateInstructions,
		}
		return nil
	})

	if err := walkDir(projectPath, dir, producerEg, packageChan); err != nil {
		return nil, err
	}

	if err := producerEg.Wait(); err != nil {
		return nil, err
	}
	close(packageChan)

	if err := consumerEg.Wait(); err != nil {
		return nil, err
	}

	dag := <-resultChan

	return dag, nil
}

func walkDir(
	projectPath string,
	dir string,
	packageGroup *errgroup.Group,
	packageChan chan<- string,
) error {
	var walkPath string
	if dir == "." {
		walkPath = projectPath
	} else {
		walkPath = filepath.Join(projectPath, dir)
	}

	err := filepath.WalkDir(
		walkPath,
		func(path string, dirEntry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if dirEntry.IsDir() {
				// TODO implement a dynamic way for ignoring directories
				if path == filepath.Join(walkPath, "cue.mod") ||
					path == filepath.Join(walkPath, ".git") {
					return filepath.SkipDir
				}

				packageGroup.Go(func() error {
					hasCUE := false
					entries, err := os.ReadDir(path)
					if err != nil {
						return err
					}

					for _, entry := range entries {
						if strings.HasSuffix(entry.Name(), ".cue") {
							hasCUE = true
							break
						}
					}

					if !hasCUE {
						return nil
					}

					relativePath, err := filepath.Rel(projectPath, path)
					if err != nil {
						return err
					}

					packageChan <- relativePath
					return nil
				})

				return nil
			}

			return nil
		},
	)

	return err
}
