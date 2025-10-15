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
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/kharf/navecd/pkg/cloud"
	"github.com/kharf/navecd/pkg/component"
	"golang.org/x/sync/errgroup"
)

type options struct {
	loader RemoteLoader

	// optional auth used when loader is not nil
	auth *cloud.Auth
}

type Option func(opts *options)

func WithRemoteLoader(loader RemoteLoader) Option {
	return func(opts *options) {
		opts.loader = loader
	}
}

func WithAuth(auth *cloud.Auth) Option {
	return func(opts *options) {
		opts.auth = auth
	}
}

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
	Digest    Digest
	Path      string
	LoadError error
	Dag       *component.DependencyGraph
}

// Load uses a given path to a project and returns the components as a directed acyclic dependency graph.
func (manager *Manager) Load(
	ctx context.Context,
	projectPath string,
	dir string,
	opts ...Option,
) (*Instance, error) {
	options := &options{}
	for _, opt := range opts {
		opt(options)
	}

	projectPath = strings.TrimSuffix(projectPath, "/")

	var configPath string
	if dir != "." {
		configPath = filepath.Join(projectPath, dir)
	} else {
		configPath = projectPath
	}

	var digest Digest
	var downloadErr error
	if options.loader != nil {
		result, err := options.loader.Load(ctx, projectPath, options.auth)
		if err != nil {
			var loadErr *RecoverableLoadError
			if !errors.As(err, &loadErr) {
				return nil, fmt.Errorf("%w: %w", ErrLoadProject, err)
			}

			if _, statErr := os.Stat(configPath); errors.Is(statErr, fs.ErrNotExist) {
				return nil, fmt.Errorf("%w: %w", ErrLoadProject, err)
			}

			projectPath = err.(*RecoverableLoadError).BackupPath
			downloadErr = err
		}

		digest = result
	}

	if _, err := os.Stat(configPath); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %w", ErrLoadProject, err)
	}

	producerEg := &errgroup.Group{}
	producerEg.SetLimit(manager.workerPoolSize)

	resultChan := make(chan *component.DependencyGraph, 1)
	packageChan := make(chan string, 250)

	consumerEg := &errgroup.Group{}
	consumerEg.Go(func() error {
		dag := component.NewDependencyGraph()
		for packagePath := range packageChan {
			buildResult, err := manager.componentBuilder.Build(
				component.WithProjectRoot(projectPath),
				component.WithPackagePath(packagePath),
			)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrLoadProject, err)
			}

			if err := dag.Insert(buildResult.Instances...); err != nil {
				return fmt.Errorf("%w: %w", ErrLoadProject, err)
			}
		}

		resultChan <- &dag
		return nil
	})

	if err := walkDir(projectPath, configPath, producerEg, packageChan); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLoadProject, err)
	}

	if err := producerEg.Wait(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLoadProject, err)
	}
	close(packageChan)

	if err := consumerEg.Wait(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLoadProject, err)
	}

	dag := <-resultChan

	return &Instance{
		Digest:    digest,
		Path:      configPath,
		LoadError: downloadErr,
		Dag:       dag,
	}, nil
}

func walkDir(
	projectPath string,
	configPath string,
	packageGroup *errgroup.Group,
	packageChan chan<- string,
) error {
	err := filepath.WalkDir(
		configPath,
		func(path string, dirEntry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if dirEntry.IsDir() {
				// TODO implement a dynamic way for ignoring directories
				if path == filepath.Join(configPath, "cue.mod") ||
					path == filepath.Join(configPath, ".git") {
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
