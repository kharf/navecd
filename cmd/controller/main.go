package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"cuelang.org/go/cue/cuecontext"
	"github.com/kharf/declcd/pkg/core"
	"github.com/kharf/declcd/pkg/kube"
	"go.uber.org/zap"
	controllerruntime "sigs.k8s.io/controller-runtime"
)

// WIP
func main() {
	basicLogger, err := initZap()
	if err != nil {
		panic(err)
	}
	defer basicLogger.Sync()
	logger := basicLogger.Sugar()

	repositoryManager := core.NewRepositoryManager()
	rootDir := "/tmp"
	repositoryDir := "decl"
	localRepositoryPath := filepath.Join(rootDir, repositoryDir)
	_, err = repositoryManager.Clone(core.WithUrl("https://github.com/kharf/declcd-test-repo.git"), core.WithTarget(localRepositoryPath))
	if err != nil {
		panic(err)
	}

	fileSystem := os.DirFS(rootDir)
	ctx := cuecontext.New()
	projectManager := core.NewProjectManager(fileSystem, logger)
	project, err := projectManager.Load(repositoryDir)
	if err != nil {
		panic(err)
	}

	// k8s
	config, err := controllerruntime.GetConfig()
	if err != nil {
		panic(err)
	}

	// create the client
	client, err := kube.NewClient(config)

	manifestBuilder := core.NewComponentBuilder(ctx)
	for _, component := range project.MainComponents {
		buildSubComponent(localRepositoryPath, manifestBuilder, component.SubComponents, client)
	}
}

func buildSubComponent(localRepositoryPath string, builder core.ComponentBuilder, subComponents []*core.SubDeclarativeComponent, client *kube.Client) {
	ctx := context.TODO()
	for _, subComponent := range subComponents {
		fmt.Println("component: ", subComponent.Path)
		component, err := builder.Build(core.WithProjectRoot(localRepositoryPath), core.WithComponentPath(subComponent.Path))
		if err != nil {
			panic(err)
		}

		for _, obj := range component.Manifests {
			err = client.Apply(ctx, &obj)
			if err != nil {
				panic(err)
			}
		}

		buildSubComponent(localRepositoryPath, builder, subComponent.SubComponents, client)
	}
}

func initZap() (*zap.Logger, error) {
	zapConfig := zap.NewDevelopmentConfig()
	zapConfig.OutputPaths = []string{"stdout"}
	return zapConfig.Build()
}
