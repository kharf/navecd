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

package controller

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	gitops "github.com/kharf/navecd/api/v1beta1"
	"github.com/kharf/navecd/internal/gittest"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/internal/projecttest"
	"github.com/kharf/navecd/internal/testtemplates"
	"github.com/kharf/navecd/pkg/project"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

func useProjectOneTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/controller/projectone@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/toola/namespace.cue --
package toola

import (
	"github.com/kharf/navecd/schema/component"
)

#namespace: {
	apiVersion: "v1"
	kind:       "Namespace"
	metadata: name: "toola"
}

ns: component.#Manifest & {
	content: #namespace
}
`, testtemplates.ModuleVersion)
}

func useProjectTwoTemplate() string {
	return fmt.Sprintf(`
-- cue.mod/module.cue --
module: "github.com/kharf/navecd/internal/controller/projecttwo@v0"
language: version: "%s"
deps: {
	"github.com/kharf/navecd/schema@v0": {
		v: "v0.0.99"
	}
}

-- infra/toolb/namespace.cue --
package toolb

import (
	"github.com/kharf/navecd/schema/component"
)

#namespace: {
	apiVersion: "v1"
	kind:       "Namespace"
	metadata: name: "toolb"
}

ns: component.#Manifest & {
	content: #namespace
}
`, testtemplates.ModuleVersion)
}

// Define utility constants for object names and testing timeouts/durations and intervals.
const (
	gitOpsProjectNamespace = "navecd-system"

	duration          = time.Second * 30
	intervalInSeconds = 5
	assertionInterval = (intervalInSeconds + 1) * time.Second
)

var _ = Describe("GitOpsProject controller", Ordered, func() {

	When("Creating a GitOpsProject", func() {

		var (
			env        projecttest.Environment
			kubernetes *kubetest.Environment
			k8sClient  client.Client
		)

		BeforeEach(func() {
			env = projecttest.InitTestEnvironment(test, []byte(useProjectOneTemplate()))
			kubernetes = kubetest.StartKubetestEnv(test, env.Log, kubetest.WithEnabled(true))
			k8sClient = kubernetes.TestKubeClient
		})

		AfterEach(func() {
			err := kubernetes.Stop()
			Expect(err).NotTo(HaveOccurred())
			metrics.Registry = prometheus.NewRegistry()
			err = os.RemoveAll("/podinfo")
			Expect(err).NotTo(HaveOccurred())
		})

		When("The pull interval is less than 5 seconds", func() {

			It("Should not allow a pull interval less than 5 seconds", func() {
				gitOpsProjectName := "test"

				err := project.Init(
					"github.com/kharf/navecd/controller",
					"primary",
					"image",
					false,
					env.LocalTestProject,
					"1.0.0",
				)
				Expect(err).NotTo(HaveOccurred())

				gitServer, httpClient := gittest.MockGitProvider(
					test,
					"owner/repo",
					fmt.Sprintf("navecd-%s", gitOpsProjectName),
					nil,
					nil,
				)
				defer gitServer.Close()

				installAction := project.NewInstallAction(
					kubernetes.DynamicTestKubeClient.DynamicClient(),
					httpClient,
					env.LocalTestProject,
				)

				err = installAction.Install(
					context.Background(),
					project.InstallOptions{
						Url:      env.TestProject,
						Branch:   "main",
						Name:     gitOpsProjectName,
						Shard:    "primary",
						Interval: 0,
						Token:    "abcd",
					},
				)
				Expect(err).To(HaveOccurred())
				Expect(
					err.Error(),
				).To(Equal("GitOpsProject.gitops.navecd.io \"" + "test" + "\" " +
					"is invalid: spec.pullIntervalSeconds: " +
					"Invalid value: 0: spec.pullIntervalSeconds in body should be greater than or equal to 5"))
			})
		})

		When("The pull interval is greater than or equal to 5 seconds", func() {

			It(
				"Should reconcile the declared cluster state with the current cluster state",
				func() {
					gitOpsProjectName := "test"
					setupPodInfo(gitOpsProjectName)

					ctx := context.Background()
					err := project.Init(
						"github.com/kharf/navecd/controller",
						"primary",
						"image",
						false,
						env.LocalTestProject,
						"0.0.99",
					)
					Expect(err).NotTo(HaveOccurred())

					gitServer, httpClient := gittest.MockGitProvider(
						test,
						"owner/repo",
						fmt.Sprintf("navecd-%s", gitOpsProjectName),
						nil,
						nil,
					)
					defer gitServer.Close()

					installAction := project.NewInstallAction(
						kubernetes.DynamicTestKubeClient.DynamicClient(),
						httpClient,
						env.LocalTestProject,
					)

					err = installAction.Install(
						ctx,
						project.InstallOptions{
							Url:      env.TestProject,
							Branch:   "main",
							Name:     gitOpsProjectName,
							Shard:    "primary",
							Interval: intervalInSeconds,
							Token:    "abcd",
						},
					)
					Expect(err).NotTo(HaveOccurred())

					mgr, scheduler, err := Setup(
						kubernetes.ControlPlane.Config,
						InsecureSkipTLSverify(true),
						MetricsAddr("0"),
					)
					Expect(err).NotTo(HaveOccurred())

					go func() {
						defer GinkgoRecover()
						scheduler.Start()
						_ = mgr.Start(ctx)
					}()

					Eventually(func(g Gomega) {
						var project gitops.GitOpsProject
						err := k8sClient.Get(
							ctx,
							types.NamespacedName{
								Name:      gitOpsProjectName,
								Namespace: gitOpsProjectNamespace,
							},
							&project,
						)
						g.Expect(err).ToNot(HaveOccurred())
						g.Expect(project.Spec.PullIntervalSeconds).To(Equal(intervalInSeconds))
						suspend := false
						g.Expect(project.Spec.Suspend).To(Equal(&suspend))
						g.Expect(project.Spec.URL).To(Equal(env.TestProject))
					}, duration, assertionInterval).Should(Succeed())

					Eventually(func() (string, error) {
						var namespace corev1.Namespace
						if err := k8sClient.Get(ctx, types.NamespacedName{Name: "toola", Namespace: ""}, &namespace); err != nil {
							return "", err
						}
						return namespace.GetName(), nil
					}, duration, assertionInterval).Should(Equal("toola"))

					Eventually(func(g Gomega) {
						var updatedGitOpsProject gitops.GitOpsProject
						err := k8sClient.Get(
							ctx,
							types.NamespacedName{
								Name:      gitOpsProjectName,
								Namespace: gitOpsProjectNamespace,
							},
							&updatedGitOpsProject,
						)
						g.Expect(err).ToNot(HaveOccurred())
						g.Expect(updatedGitOpsProject.Status.Revision.CommitHash).ToNot(BeEmpty())
						g.Expect(updatedGitOpsProject.Status.Revision.ReconcileTime.IsZero()).
							To(BeFalse())
						g.Expect(len(updatedGitOpsProject.Status.Conditions)).To(Equal(2))
					}, duration, assertionInterval).Should(Succeed())
				},
			)
		})
	})

	When("Creating multiple GitOpsProjects", func() {

		var (
			envs          map[string]projecttest.Environment
			kubernetes    *kubetest.Environment
			k8sClient     client.Client
			installAction project.InstallAction
		)

		BeforeAll(func() {
			kubernetes = kubetest.StartKubetestEnv(test, logr.Discard(), kubetest.WithEnabled(true))
			k8sClient = kubernetes.TestKubeClient

			gitServer, httpClient := gittest.MockGitProvider(
				test,
				"owner/repo",
				fmt.Sprintf("navecd-%s", "test"),
				nil,
				nil,
			)
			defer gitServer.Close()

			ctx := context.Background()

			projectTemplates := []string{
				useProjectOneTemplate(), useProjectTwoTemplate(),
			}

			setupPodInfo("multitenancy")

			envs = make(map[string]projecttest.Environment, 2)
			for i, projectTemplate := range projectTemplates {
				env := projecttest.InitTestEnvironment(test, []byte(projectTemplate))
				installAction = project.NewInstallAction(
					kubernetes.DynamicTestKubeClient.DynamicClient(),
					httpClient,
					env.LocalTestProject,
				)

				err := project.Init(
					"github.com/kharf/navecd/controller",
					"primary",
					"image",
					false,
					env.LocalTestProject,
					"0.0.99",
				)
				Expect(err).NotTo(HaveOccurred())

				projectName := fmt.Sprintf("%s%v", "project", i)

				err = installAction.Install(
					ctx,
					project.InstallOptions{
						Url:      env.TestProject,
						Branch:   "main",
						Name:     projectName,
						Shard:    "primary",
						Interval: intervalInSeconds,
						Token:    "abcd",
					},
				)
				Expect(err).NotTo(HaveOccurred())

				envs[projectName] = env
			}

			mgr, scheduler, err := Setup(
				kubernetes.ControlPlane.Config,
				InsecureSkipTLSverify(true),
				MetricsAddr("0"),
			)
			Expect(err).NotTo(HaveOccurred())

			go func() {
				defer GinkgoRecover()
				scheduler.Start()
				_ = mgr.Start(ctx)
			}()
		})

		AfterAll(func() {
			err := kubernetes.Stop()
			Expect(err).NotTo(HaveOccurred())
			err = os.RemoveAll("/podinfo")
			Expect(err).NotTo(HaveOccurred())
		})

		It(
			"Should reconcile the declared cluster state with the current cluster state",
			func() {
				ctx := context.Background()

				Eventually(func(g Gomega) {
					var project gitops.GitOpsProject
					err := k8sClient.Get(
						ctx,
						types.NamespacedName{
							Name:      "project0",
							Namespace: gitOpsProjectNamespace,
						},
						&project,
					)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(project.Spec.PullIntervalSeconds).To(Equal(intervalInSeconds))
					suspend := false
					g.Expect(project.Spec.Suspend).To(Equal(&suspend))
					g.Expect(project.Spec.URL).To(Equal(envs["project0"].TestProject))
				}, duration, assertionInterval).Should(Succeed())

				Eventually(func(g Gomega) {
					var project gitops.GitOpsProject
					err := k8sClient.Get(
						ctx,
						types.NamespacedName{
							Name:      "project1",
							Namespace: gitOpsProjectNamespace,
						},
						&project,
					)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(project.Spec.PullIntervalSeconds).To(Equal(intervalInSeconds))
					suspend := false
					g.Expect(project.Spec.Suspend).To(Equal(&suspend))
					g.Expect(project.Spec.URL).To(Equal(envs["project1"].TestProject))
				}, duration, assertionInterval).Should(Succeed())

				Eventually(func(g Gomega) {
					var updatedGitOpsProject gitops.GitOpsProject
					err := k8sClient.Get(
						ctx,
						types.NamespacedName{
							Name:      "project0",
							Namespace: gitOpsProjectNamespace,
						},
						&updatedGitOpsProject,
					)
					g.Expect(err).ToNot(HaveOccurred())
				}, duration, assertionInterval).Should(Succeed())

				Eventually(func(g Gomega) {
					var updatedGitOpsProject gitops.GitOpsProject
					err := k8sClient.Get(
						ctx,
						types.NamespacedName{
							Name:      "project1",
							Namespace: gitOpsProjectNamespace,
						},
						&updatedGitOpsProject,
					)
					g.Expect(err).ToNot(HaveOccurred())
					g.Expect(updatedGitOpsProject.Status.Revision.CommitHash).ToNot(BeEmpty())
					g.Expect(updatedGitOpsProject.Status.Revision.ReconcileTime.IsZero()).
						To(BeFalse())
					g.Expect(len(updatedGitOpsProject.Status.Conditions)).To(Equal(2))
				}, duration, assertionInterval).Should(Succeed())

				Eventually(func() (string, error) {
					var namespace corev1.Namespace
					if err := k8sClient.Get(ctx, types.NamespacedName{Name: "toola", Namespace: ""}, &namespace); err != nil {
						return "", err
					}
					return namespace.GetName(), nil
				}, duration, assertionInterval).Should(Equal("toola"))

				Eventually(func() (string, error) {
					var namespace corev1.Namespace
					if err := k8sClient.Get(ctx, types.NamespacedName{Name: "toolb", Namespace: ""}, &namespace); err != nil {
						return "", err
					}
					return namespace.GetName(), nil
				}, duration, assertionInterval).Should(Equal("toolb"))
			},
		)
	})
})

func setupPodInfo(name string) {
	err := os.Mkdir("/podinfo", 0700)
	Expect(err).NotTo(HaveOccurred())
	err = os.WriteFile("/podinfo/name", []byte(name), 0600)
	Expect(err).NotTo(HaveOccurred())
	err = os.WriteFile("/podinfo/namespace", []byte(project.ControllerNamespace), 0600)
	Expect(err).NotTo(HaveOccurred())
	err = os.WriteFile("/podinfo/shard", []byte("primary"), 0600)
	Expect(err).NotTo(HaveOccurred())
}
