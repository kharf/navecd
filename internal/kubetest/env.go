// Copyright 2024 Google LLC
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

package kubetest

import (
	"context"
	"os"
	"testing"

	goRuntime "runtime"

	"gotest.tools/v3/assert"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	gitops "github.com/kharf/declcd/api/v1beta1"
	"github.com/kharf/declcd/internal/gittest"
	"github.com/kharf/declcd/internal/helmtest"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/go-logr/logr"
	"github.com/kharf/declcd/pkg/garbage"
	"github.com/kharf/declcd/pkg/inventory"
	"github.com/kharf/declcd/pkg/kube"
	"github.com/kharf/declcd/pkg/secret"
	"github.com/kharf/declcd/pkg/vcs"
	_ "github.com/kharf/declcd/test/workingdir"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/kubectl/pkg/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

type Environment struct {
	ControlPlane          *envtest.Environment
	ControllerManager     manager.Manager
	HelmEnv               helmtest.Environment
	TestKubeClient        client.Client
	DynamicTestKubeClient *kube.DynamicClient
	GarbageCollector      garbage.Collector
	InventoryManager      *inventory.Manager
	RepositoryManager     vcs.RepositoryManager
	SecretManager         secret.Manager
	Ctx                   context.Context
	clean                 func()
}

func (env Environment) Stop() {
	env.clean()
}

type helmOption struct {
	enabled bool
	oci     bool
	private bool
}

var _ Option = (*helmOption)(nil)

func (opt helmOption) apply(opts *options) {
	opts.helm = opt
}

type projectOption struct {
	repo        *gittest.LocalGitRepository
	testProject string
	testRoot    string
}

var _ Option = (*projectOption)(nil)

func (opt projectOption) apply(opts *options) {
	opts.project = opt
}

type decryptionKeyCreated bool

var _ Option = (*decryptionKeyCreated)(nil)

func (opt decryptionKeyCreated) apply(opts *options) {
	opts.decryptionKeyCreated = bool(opt)
}

type vcsSSHKeyCreated bool

var _ Option = (*vcsSSHKeyCreated)(nil)

func (opt vcsSSHKeyCreated) apply(opts *options) {
	opts.vcsSSHKeyCreated = bool(opt)
}

type options struct {
	enabled              bool
	helm                 helmOption
	decryptionKeyCreated bool
	vcsSSHKeyCreated     bool
	project              projectOption
}

type Option interface {
	apply(*options)
}

func WithHelm(enabled bool, oci bool, private bool) helmOption {
	return helmOption{
		enabled: enabled,
		oci:     oci,
		private: private,
	}
}

func WithDecryptionKeyCreated() decryptionKeyCreated {
	return true
}

func WithVCSSSHKeyCreated() vcsSSHKeyCreated {
	return true
}

func WithProject(
	repo *gittest.LocalGitRepository,
	testProject string,
	testRoot string,
) projectOption {
	return projectOption{
		repo:        repo,
		testProject: testProject,
		testRoot:    testRoot,
	}
}

func StartKubetestEnv(t testing.TB, log logr.Logger, opts ...Option) *Environment {
	logf.SetLogger(log)
	options := &options{
		helm: helmOption{
			enabled: false,
			oci:     false,
			private: false,
		},
		enabled:              true,
		decryptionKeyCreated: false,
		vcsSSHKeyCreated:     false,
	}
	for _, o := range opts {
		o.apply(options)
	}
	if !options.enabled {
		return nil
	}
	testEnv := &envtest.Environment{
		ErrorIfCRDPathMissing: false,
	}
	var err error
	// cfg is defined in this file globally.
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatal(err)
	}
	err = gitops.AddToScheme(scheme.Scheme)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: server.Options{
			BindAddress: "0",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.TODO())
	testClient, err := client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		t.Fatal(err)
	}
	helmEnv := helmtest.Environment{}
	if options.helm.enabled {
		helmEnv = helmtest.StartHelmEnv(
			t,
			cfg,
			helmtest.WithOCI(options.helm.oci),
			helmtest.WithPrivate(options.helm.private),
			helmtest.WithProject(
				options.project.repo,
				options.project.testProject,
				options.project.testRoot,
			),
		)
	}
	client, err := kube.NewDynamicClient(testEnv.Config)
	assert.NilError(t, err)
	inventoryPath, err := os.MkdirTemp(options.project.testRoot, "inventory-*")
	assert.NilError(t, err)
	invManager := &inventory.Manager{
		Log:  log,
		Path: inventoryPath,
	}
	gc := garbage.Collector{
		Log:              log,
		Client:           client,
		KubeConfig:       cfg,
		InventoryManager: invManager,
		WorkerPoolSize:   goRuntime.GOMAXPROCS(0),
	}
	nsStr := "test"
	declNs := corev1.Namespace{
		TypeMeta: v1.TypeMeta{
			Kind:       "Namespace",
			APIVersion: "v1",
		},
		ObjectMeta: v1.ObjectMeta{
			Name: nsStr,
		},
	}
	err = testClient.Create(ctx, &declNs)
	assert.NilError(t, err)
	if options.decryptionKeyCreated {
		privKey := "AGE-SECRET-KEY-1EYUZS82HMQXK0S83AKAP6NJ7HPW6KMV70DHHMH4TS66S3NURTWWS034Q34"
		decSec := corev1.Secret{
			TypeMeta: v1.TypeMeta{
				Kind:       "Secret",
				APIVersion: "v1",
			},
			ObjectMeta: v1.ObjectMeta{
				Name:      secret.K8sSecretName,
				Namespace: nsStr,
			},
			Data: map[string][]byte{
				secret.K8sSecretDataKey: []byte(privKey),
			},
		}
		err = testClient.Create(ctx, &decSec)
		assert.NilError(t, err)
	}
	if options.vcsSSHKeyCreated {
		sec := corev1.Secret{
			TypeMeta: v1.TypeMeta{
				Kind:       "Secret",
				APIVersion: "v1",
			},
			ObjectMeta: v1.ObjectMeta{
				Name:      vcs.K8sSecretName,
				Namespace: nsStr,
			},
			Data: map[string][]byte{
				vcs.K8sSecretDataAuthType: []byte(vcs.K8sSecretDataAuthTypeSSH),
				vcs.SSHKey: []byte(`-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtz
c2gtZWQyNTUxOQAAACDrGFnmApwnObDTPK8nepGtlPKhhrA1u6Ox2hD5LAq5+gAA
AIh1qzZ4das2eAAAAAtzc2gtZWQyNTUxOQAAACDrGFnmApwnObDTPK8nepGtlPKh
hrA1u6Ox2hD5LAq5+gAAAEDiqr5GEHcp1oHqJCNhc+LBYF9LDmuJ9oL0LUw5pYZy
9OsYWeYCnCc5sNM8ryd6ka2U8qGGsDW7o7HaEPksCrn6AAAAAAECAwQF
-----END OPENSSH PRIVATE KEY-----`),
				vcs.SSHKnownHosts: []byte(vcs.GitHubSSHKey),
				vcs.SSHPubKey:     []byte("ssh-ed25519 AAAA"),
			},
		}
		err = testClient.Create(ctx, &sec)
		assert.NilError(t, err)
	}
	manager := secret.NewManager(
		options.project.testProject,
		nsStr,
		client,
		goRuntime.GOMAXPROCS(0),
	)
	repositoryManger := vcs.NewRepositoryManager("test", client, log)
	return &Environment{
		ControlPlane:          testEnv,
		ControllerManager:     mgr,
		HelmEnv:               helmEnv,
		TestKubeClient:        testClient,
		DynamicTestKubeClient: client,
		GarbageCollector:      gc,
		InventoryManager:      invManager,
		RepositoryManager:     repositoryManger,
		SecretManager:         manager,
		Ctx:                   ctx,
		clean: func() {
			testEnv.Stop()
			helmEnv.Close()
			cancel()
		},
	}
}

type FakeDynamicClient struct {
	Err error
}

var _ kube.Client[unstructured.Unstructured] = (*FakeDynamicClient)(nil)

func (client *FakeDynamicClient) Apply(
	ctx context.Context,
	obj *unstructured.Unstructured,
	fieldManager string,
	opts ...kube.ApplyOption,
) error {
	return client.Err
}

func (client *FakeDynamicClient) Update(
	ctx context.Context,
	obj *unstructured.Unstructured,
	fieldManager string,
	opts ...kube.ApplyOption,
) error {
	return client.Err
}

func (client *FakeDynamicClient) Delete(ctx context.Context, obj *unstructured.Unstructured) error {
	return client.Err
}

func (client *FakeDynamicClient) Get(
	ctx context.Context,
	obj *unstructured.Unstructured,
) (*unstructured.Unstructured, error) {
	return nil, client.Err
}

func (client *FakeDynamicClient) RESTMapper() meta.RESTMapper {
	return nil
}
