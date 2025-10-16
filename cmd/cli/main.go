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

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/kharf/navecd/pkg/component"
	"github.com/kharf/navecd/pkg/kube"
	"github.com/kharf/navecd/pkg/oci"
	"github.com/kharf/navecd/pkg/project"
	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

var Version string
var OS string
var Arch string

func main() {
	root := RootCommandBuilder{}
	if err := root.Build().Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
		return
	}
}

type RootCommandBuilder struct {
	initCommandBuilder         InitCommandBuilder
	verifyCommandBuilder       VerifyCommandBuilder
	versionCommandBuilder      VersionCommandBuilder
	installCommandBuilder      InstallCommandBuilder
	pushArtifactCommandBuilder PushArtifactCommandBuilder
}

func (builder RootCommandBuilder) Build() *cobra.Command {
	rootCmd := cobra.Command{
		Use:   "navecd",
		Short: "A GitOps Declarative Continuous Delivery toolkit",
	}
	rootCmd.AddCommand(builder.initCommandBuilder.Build())
	rootCmd.AddCommand(builder.verifyCommandBuilder.Build())
	rootCmd.AddCommand(builder.versionCommandBuilder.Build())
	rootCmd.AddCommand(builder.installCommandBuilder.Build())
	rootCmd.AddCommand(builder.pushArtifactCommandBuilder.Build())
	return &rootCmd
}

type InitCommandBuilder struct{}

func (builder InitCommandBuilder) Build() *cobra.Command {
	var shard string
	var isSecondary bool
	var image string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Init a Navecd Project in the current directory",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cobraCmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			return project.Init(
				args[0],
				shard,
				image,
				isSecondary,
				cwd,
				Version,
			)
		},
	}
	cmd.Flags().
		StringVar(&shard, "shard", "primary", "Instance of the Navecd Project")
	cmd.Flags().
		BoolVar(&isSecondary, "secondary", false, "Indicates a secondary Navecd instance")
	cmd.Flags().
		StringVar(&image, "image", "ghcr.io/kharf/navecd", "Navecd controller image to use")
	return cmd
}

type VerifyCommandBuilder struct{}

func (builder VerifyCommandBuilder) Build() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Validate Navecd Configuration in specified directory",
		Args:  cobra.MinimumNArgs(0),
		RunE: func(cobraCmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			projectManager := project.NewManager(
				component.NewBuilder(),
				-1,
			)

			instance, err := projectManager.Load(context.Background(), cwd, dir)
			if err != nil {
				return err
			}

			_, err = instance.Dag.TopologicalSort()
			if err != nil {
				return err
			}

			return nil
		},
	}
	cmd.Flags().
		StringVar(&dir, "dir", ".", "Dir of the GitOps Repository containing project configuration")
	return cmd
}

type VersionCommandBuilder struct{}

func (builder VersionCommandBuilder) Build() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print navecd version",
		Args:  cobra.MinimumNArgs(0),
		RunE: func(cobraCmd *cobra.Command, args []string) error {
			fmt.Printf("navecd v%s\non %s_%s\n", Version, OS, Arch)
			return nil
		},
	}
	return cmd
}

type InstallCommandBuilder struct{}

func (builder InstallCommandBuilder) Build() *cobra.Command {
	ctx := context.Background()
	var ref string
	var url string
	var dir string
	var name string
	var interval int
	var shard string
	var wip string
	var secretRef string
	var insecureRegistry bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install Navecd onto a Kubernetes Cluster",
		Args:  cobra.MinimumNArgs(0),
		RunE: func(cobraCmd *cobra.Command, args []string) error {
			kubeConfig, err := config.GetConfig()
			if err != nil {
				return err
			}

			client, err := kube.NewDynamicClient(kubeConfig)
			if err != nil {
				return err
			}

			wd, err := os.Getwd()
			if err != nil {
				return err
			}
			httpClient := http.DefaultClient

			action := project.NewInstallAction(client, httpClient, wd)
			if _, err := action.Install(ctx,
				project.InstallOptions{
					Url:              url,
					Ref:              ref,
					Dir:              dir,
					Name:             name,
					Interval:         interval,
					Shard:            shard,
					WIP:              wip,
					SecretRef:        secretRef,
					InsecureRegistry: insecureRegistry,
				},
			); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&url, "url", "u", "", "Url to the OCI GitOps Repository")
	cmd.Flags().StringVarP(&ref, "ref", "r", "main", "Ref to the OCI GitOps Repository")
	cmd.Flags().StringVar(&dir, "dir", ".", "Dir of the GitOps Project Configuration inside the OCI GitOps Repository")
	cmd.Flags().StringVar(&name, "name", "", "Name of the GitOps Project")
	cmd.Flags().IntVarP(&interval, "interval", "i", 30, "Definition of how often Navecd will reconcile its cluster state. Value is defined in seconds")
	cmd.Flags().StringVar(&shard, "shard", "primary", "Navecd Instance/Shard responsible for reconciliation")
	cmd.Flags().StringVar(&wip, "wip", "", "Workload Identity Provider used for OCI registry access. Supported values are 'aws', 'azure' and 'gcp'")
	cmd.Flags().StringVar(&secretRef, "secret", "", "Reference to the Kubernetes secret containing the OCI registry credentials in the Navecd controller namespace")
	cmd.Flags().BoolVar(&insecureRegistry, "insecure", false, "Insecure allows communicating with OCI registries without TLS")

	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("url")
	_ = cmd.MarkFlagRequired("ref")
	return cmd
}

type PushArtifactCommandBuilder struct{}

func (builder PushArtifactCommandBuilder) Build() *cobra.Command {
	var ref string
	var url string
	var insecureRegistry bool
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Builds and pushes a Navecd Project OCI artifact to the specified OCI Repository",
		Args:  cobra.MinimumNArgs(0),
		RunE: func(cobraCmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			ociClient, err := oci.NewRepositoryClient(url, insecureRegistry)
			if err != nil {
				return err
			}
			projectClient := oci.NewProjectClient(ociClient)

			digest, err := projectClient.PushImageFromPath(
				ref,
				cwd,
				oci.WithRepositoryOption(
					oci.WithInsecure(insecureRegistry),
				),
			)
			if err != nil {
				return err
			}
			fmt.Printf("pushed %s:%s with digest %s\n", url, ref, digest)
			return nil
		},
	}
	cmd.Flags().StringVarP(&url, "url", "u", "", "Url to the OCI GitOps Repository")
	cmd.Flags().StringVarP(&ref, "ref", "r", "main", "Ref to the OCI GitOps Repository")
	cmd.Flags().BoolVar(&insecureRegistry, "insecure", false, "Insecure allows communicating with OCI registries without TLS")

	_ = cmd.MarkFlagRequired("url")
	_ = cmd.MarkFlagRequired("ref")
	return cmd
}
