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

package version_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"cuelabs.dev/go/oci/ociregistry"
	"github.com/Masterminds/semver/v3"
	"github.com/go-logr/logr"
	"github.com/kharf/navecd/internal/cloudtest"
	"github.com/kharf/navecd/internal/dnstest"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/internal/ocitest"
	"github.com/kharf/navecd/pkg/cloud"
	"github.com/kharf/navecd/pkg/helm"
	"github.com/kharf/navecd/pkg/version"
	"github.com/opencontainers/go-digest"
	"go.uber.org/zap/zapcore"
	"gotest.tools/v3/assert"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/repo"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/yaml"
)

type scanTestCase struct {
	name                    string
	haveUpdateInstruction   version.UpdateInstruction
	haveCloudProvider       cloud.ProviderID
	havePrivateRegistry     bool
	haveCredentialsSecret   *corev1.Secret
	haveRemoteVersions      map[string][]string
	haveRemoteChartVersions map[string][]string
	haveRemoteURLs          map[string]string
	haveRemoteChartURLs     map[string]string
	wantImageScan           *version.ImageScan
	wantErr                 string
}

var (
	newContainerVersion = scanTestCase{
		name: "New-Container-Version",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<1.17.x",
			Auth:       nil,
			File:       "myfile",
			Line:       5,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.15.0@sha256:1ff6c18fbef2045af6b9c16bf034cc421a29027b800e4f9b68ae9b1cb3e9ae07",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.15.0@sha256:1ff6c18fbef2045af6b9c16bf034cc421a29027b800e4f9b68ae9b1cb3e9ae07",
				},
				UnstructuredKey: "image",
			},
		},
		haveRemoteVersions: map[string][]string{
			"myimage": {"1.14.0", "1.16.5", "1.15.1", "1.15.2", "other", "latest"},
		},
		haveRemoteURLs: map[string]string{
			"myimage": "https://test",
		},
		wantImageScan: &version.ImageScan{
			CurrentVersion: "1.15.0@sha256:1ff6c18fbef2045af6b9c16bf034cc421a29027b800e4f9b68ae9b1cb3e9ae07",
			NewVersion:     "1.16.5@sha256:2d93689cbcdda92b425bfd82f87f5b656791a8a3e96c8eb2d702c6698987629a",
			URL:            "https://test",
		},
	}

	newHelmOciVersion = scanTestCase{
		name: "New-Helm-Oci-Version",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "1.16.x",
			Auth:       nil,
			File:       "myfile",
			Line:       5,
			Target: &version.ChartUpdateTarget{
				Chart: &helm.Chart{
					Name:    "mychart",
					RepoURL: "oci://",
					Version: "1.15.0@sha256:1ff6c18fbef2045af6b9c16bf034cc421a29027b800e4f9b68ae9b1cb3e9ae07",
					Auth:    nil,
				},
			},
		},
		haveRemoteVersions: map[string][]string{
			"mychart": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
		},
		haveRemoteURLs: map[string]string{
			"mychart": "https://test",
		},
		wantImageScan: &version.ImageScan{
			CurrentVersion: "1.15.0@sha256:1ff6c18fbef2045af6b9c16bf034cc421a29027b800e4f9b68ae9b1cb3e9ae07",
			NewVersion:     "1.16.5@sha256:2d93689cbcdda92b425bfd82f87f5b656791a8a3e96c8eb2d702c6698987629a",
			URL:            "https://test",
		},
	}

	newHelmHttpVersion = scanTestCase{
		name: "New-Helm-Http-Version",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:    version.SemVer,
			Constraint:  "1.16.x",
			Auth:        nil,
			File:        "myfile",
			Line:        5,
			Integration: version.Direct,
			Target: &version.ChartUpdateTarget{
				Chart: &helm.Chart{
					Name:    "mychart",
					RepoURL: "https://",
					Version: "1.15.0@bbbb",
					Auth:    nil,
				},
			},
		},
		haveRemoteChartVersions: map[string][]string{
			"mychart": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
		},
		haveRemoteChartURLs: map[string]string{
			"mychart": "https://test2",
		},
		wantImageScan: &version.ImageScan{
			CurrentVersion: "1.15.0@bbbb",
			NewVersion:     "1.16.5@cccc",
			URL:            "https://test2",
		},
	}

	secretAuthRegistry = scanTestCase{
		name: "Secret-Auth-Registry",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<1.17.x",
			Auth: &cloud.Auth{
				SecretRef: &cloud.SecretRef{
					Name: "creds",
				},
			},
			File: "myfile",
			Line: 5,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.15.0",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.15.0",
				},
				UnstructuredKey: "image",
			},
		},
		havePrivateRegistry: true,
		haveCredentialsSecret: &corev1.Secret{
			TypeMeta: v1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: v1.ObjectMeta{
				Name:      "creds",
				Namespace: "navecd-system",
			},
			Data: map[string][]byte{
				"username": []byte("navecd"),
				"password": []byte("abcd"),
			},
		},
		haveRemoteVersions: map[string][]string{
			"myimage": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
		},
		wantImageScan: &version.ImageScan{
			CurrentVersion: "1.15.0",
			NewVersion:     "1.16.5",
		},
	}

	workloadIdentityAuthGCP = scanTestCase{
		name: "WorkloadIdentity-Auth-GCP",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<1.17.x",
			Auth: &cloud.Auth{
				WorkloadIdentity: &cloud.WorkloadIdentity{
					Provider: cloud.GCP,
				},
			},
			File: "myfile",
			Line: 5,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.15.0",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.15.0",
				},
				UnstructuredKey: "image",
			},
		},
		havePrivateRegistry: true,
		haveCloudProvider:   cloud.GCP,
		haveRemoteVersions: map[string][]string{
			"myimage": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
			"mychart": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
		},
		haveRemoteChartVersions: map[string][]string{
			"mychart": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
		},
		wantImageScan: &version.ImageScan{
			CurrentVersion: "1.15.0",
			NewVersion:     "1.16.5",
		},
	}

	workloadIdentityAuthAWS = scanTestCase{
		name: "WorkloadIdentity-Auth-AWS",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<1.17.x",
			Auth: &cloud.Auth{
				WorkloadIdentity: &cloud.WorkloadIdentity{
					Provider: cloud.AWS,
				},
			},
			File: "myfile",
			Line: 5,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.15.0",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.15.0",
				},
				UnstructuredKey: "image",
			},
		},
		havePrivateRegistry: true,
		haveCloudProvider:   cloud.AWS,
		haveRemoteVersions: map[string][]string{
			"myimage": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
			"mychart": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
		},
		haveRemoteChartVersions: map[string][]string{
			"mychart": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
		},
		wantImageScan: &version.ImageScan{
			CurrentVersion: "1.15.0",
			NewVersion:     "1.16.5",
		},
	}

	workloadIdentityAuthAzure = scanTestCase{
		name: "WorkloadIdentity-Auth-Azure",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<1.17.x",
			Auth: &cloud.Auth{
				WorkloadIdentity: &cloud.WorkloadIdentity{
					Provider: cloud.Azure,
				},
			},
			File: "myfile",
			Line: 5,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.15.0",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.15.0",
				},
				UnstructuredKey: "image",
			},
		},
		havePrivateRegistry: true,
		haveCloudProvider:   cloud.Azure,
		haveRemoteVersions: map[string][]string{
			"myimage": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
			"mychart": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
		},
		haveRemoteChartVersions: map[string][]string{
			"mychart": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
		},
		wantImageScan: &version.ImageScan{
			CurrentVersion: "1.15.0",
			NewVersion:     "1.16.5",
		},
	}

	secretAuthHelmHttpRepo = scanTestCase{
		name: "Secret-Auth-Helm-Http-Repo",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<1.17.x",
			File:       "myfile",
			Line:       5,
			Target: &version.ChartUpdateTarget{
				Chart: &helm.Chart{
					Name:    "mychart",
					RepoURL: "https://",
					Version: "1.15.0",
					Auth: &cloud.Auth{
						SecretRef: &cloud.SecretRef{
							Name: "creds",
						},
					},
				},
			},
		},
		havePrivateRegistry: true,
		haveCredentialsSecret: &corev1.Secret{
			TypeMeta: v1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: v1.ObjectMeta{
				Name:      "creds",
				Namespace: "navecd-system",
			},
			Data: map[string][]byte{
				"username": []byte("navecd"),
				"password": []byte("abcd"),
			},
		},
		haveRemoteChartVersions: map[string][]string{
			"mychart": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
		},
		wantImageScan: &version.ImageScan{
			CurrentVersion: "1.15.0",
			NewVersion:     "1.16.5",
		},
	}

	secretAuthHelmHttpRepoSecretNotFound = scanTestCase{
		name: "Secret-Auth-Helm-Http-Repo-Secret-Not-Found",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<1.17.x",
			File:       "myfile",
			Line:       5,
			Target: &version.ChartUpdateTarget{
				Chart: &helm.Chart{
					Name:    "mychart",
					RepoURL: "https://",
					Version: "1.15.0",
					Auth: &cloud.Auth{
						SecretRef: &cloud.SecretRef{
							Name: "mysecret",
						},
					},
				},
			},
		},
		havePrivateRegistry: true,
		haveCredentialsSecret: &corev1.Secret{
			TypeMeta: v1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: v1.ObjectMeta{
				Name:      "creds",
				Namespace: "navecd-system",
			},
			Data: map[string][]byte{
				"username": []byte("navecd"),
				"password": []byte("abcd"),
			},
		},
		wantErr: "secrets \"mysecret\" not found",
	}

	secretAuthHelmHttpRepoWrongCredentials = scanTestCase{
		name: "Secret-Auth-Helm-Http-Repo-Wrong-Credentials",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<1.17.x",
			File:       "myfile",
			Line:       5,
			Target: &version.ChartUpdateTarget{
				Chart: &helm.Chart{
					Name:    "mychart",
					RepoURL: "https://",
					Version: "1.15.0",
					Auth: &cloud.Auth{
						SecretRef: &cloud.SecretRef{
							Name: "creds",
						},
					},
				},
			},
		},
		havePrivateRegistry: true,
		haveCredentialsSecret: &corev1.Secret{
			TypeMeta: v1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: v1.ObjectMeta{
				Name:      "creds",
				Namespace: "navecd-system",
			},
			Data: map[string][]byte{
				"username": []byte("abcd"),
				"password": []byte("abcd"),
			},
		},
		wantErr: "Unexpected response: wrong credentials: got Basic YWJjZDphYmNk, expected Basic bmF2ZWNkOmFiY2Q=",
	}

	secretAuthRegistryWrongCredentials = scanTestCase{
		name: "Secret-Auth-Registry-Wrong-Credentials",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<1.17.x",
			Auth: &cloud.Auth{
				SecretRef: &cloud.SecretRef{
					Name: "creds",
				},
			},
			File: "myfile",
			Line: 5,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.15.0",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.15.0",
				},
				UnstructuredKey: "image",
			},
		},
		havePrivateRegistry: true,
		haveCredentialsSecret: &corev1.Secret{
			TypeMeta: v1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: v1.ObjectMeta{
				Name:      "creds",
				Namespace: "navecd-system",
			},
			Data: map[string][]byte{
				"username": []byte("abcd"),
				"password": []byte("abcd"),
			},
		},
		wantErr: "unexpected status code 401 Unauthorized",
	}

	secretAuthRegistrySecretNotFound = scanTestCase{
		name: "Secret-Auth-Registry-Secret-Not-Found",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<1.17.x",
			Auth: &cloud.Auth{
				SecretRef: &cloud.SecretRef{
					Name: "mysecret",
				},
			},
			File: "myfile",
			Line: 5,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.15.0",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.15.0",
				},
				UnstructuredKey: "image",
			},
		},
		havePrivateRegistry: true,
		haveCredentialsSecret: &corev1.Secret{
			TypeMeta: v1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Secret",
			},
			ObjectMeta: v1.ObjectMeta{
				Name:      "creds",
				Namespace: "navecd-system",
			},
			Data: map[string][]byte{
				"username": []byte("abcd"),
				"password": []byte("abcd"),
			},
		},
		wantErr: "secrets \"mysecret\" not found",
	}

	badConstraint = scanTestCase{
		name: "Bad-Constraint",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "wrong",
			Auth:       nil,
			File:       "myfile",
			Line:       5,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.15.0",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.15.0",
				},
				UnstructuredKey: "image",
			},
		},
		haveRemoteVersions: map[string][]string{
			"myimage": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
		},
		wantErr: "improper constraint: wrong",
	}

	containerNotFound = scanTestCase{
		name: "Container-Not-Found",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<3.0",
			Auth:       nil,
			File:       "myfile",
			Line:       5,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.15.0",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.15.0",
				},
				UnstructuredKey: "image",
			},
		},
		wantErr: "repository name not known to registry",
	}

	invalidCurrentSemverVersion = scanTestCase{
		name: "Invalid-Current-Semver-Version",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<3.0",
			Auth:       nil,
			File:       "myfile",
			Line:       5,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:latest",
				UnstructuredNode: map[string]any{
					"image": "myimage:latest",
				},
				UnstructuredKey: "image",
			},
		},
		haveRemoteVersions: map[string][]string{
			"myimage": {"1.14.0", "1.15.1", "1.15.2", "1.16.5", "other", "latest"},
		},
		wantErr: semver.ErrInvalidSemVer.Error(),
	}

	noRemoteSemverVersion = scanTestCase{
		name: "No-Remote-Semver-Version",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<3.0",
			Auth:       nil,
			File:       "myfile",
			Line:       5,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:latest",
				UnstructuredNode: map[string]any{
					"image": "myimage:latest",
				},
				UnstructuredKey: "image",
			},
		},
		haveRemoteVersions: map[string][]string{
			"myimage": {"other", "latest"},
		},
	}

	noNewVersion = scanTestCase{
		name: "No-New-Version",
		haveUpdateInstruction: version.UpdateInstruction{
			Strategy:   version.SemVer,
			Constraint: "<3.0",
			Auth:       nil,
			File:       "myfile",
			Line:       5,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.15.0",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.15.0",
				},
				UnstructuredKey: "image",
			},
		},
		haveRemoteVersions: map[string][]string{
			"myimage": {"1.14", "1.13"},
		},
	}
)

func TestScanner_Scan(t *testing.T) {
	ctx := context.Background()

	testCases := []scanTestCase{
		newContainerVersion,
		newHelmOciVersion,
		newHelmHttpVersion,
		badConstraint,
		containerNotFound,
		invalidCurrentSemverVersion,
		noRemoteSemverVersion,
		noNewVersion,
		// OCI
		secretAuthRegistry,
		secretAuthRegistrySecretNotFound,
		secretAuthRegistryWrongCredentials,
		workloadIdentityAuthGCP,
		workloadIdentityAuthAWS,
		workloadIdentityAuthAzure,
		// Helm HTTP
		secretAuthHelmHttpRepo,
		secretAuthHelmHttpRepoSecretNotFound,
		secretAuthHelmHttpRepoWrongCredentials,
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runScanTestCase(t, ctx, tc)
		})
	}
}

func runScanTestCase(
	t *testing.T,
	ctx context.Context,
	tc scanTestCase,
) {
	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)

	kubernetes := kubetest.StartKubetestEnv(t, logr.Discard())
	namespace := corev1.Namespace{
		TypeMeta: v1.TypeMeta{
			APIVersion: "",
			Kind:       "Namespace",
		},
		ObjectMeta: v1.ObjectMeta{
			Name: "navecd-system",
		},
	}

	err = kubernetes.TestKubeClient.Create(ctx, &namespace)
	assert.NilError(t, err)

	logOpts := zap.Options{
		Development: true,
		Level:       zapcore.Level(-1),
	}
	log := zap.New(zap.UseFlagOptions(&logOpts))

	scanner := &version.Scanner{
		Log:        log,
		KubeClient: kubernetes.DynamicTestKubeClient.DynamicClient(),
		Namespace:  namespace.Name,
	}

	defer func() {
		dnsServer.Close()
		kubernetes.Stop()
	}()

	tlsRegistry, err := ocitest.NewTLSRegistry(tc.havePrivateRegistry, tc.haveCloudProvider)
	assert.NilError(t, err)
	defer tlsRegistry.Close()

	var aws *cloudtest.AWSEnvironment
	if tc.haveCloudProvider != "" {
		switch tc.haveCloudProvider {
		case cloud.GCP:
			gcp, err := cloudtest.NewGCPEnvironment()
			assert.NilError(t, err)
			defer gcp.Close()
			scanner.GCPMetadataServerURL = gcp.HttpsServer.URL

		case cloud.AWS:
			aws, err = cloudtest.NewAWSEnvironment(tlsRegistry.Addr())
			assert.NilError(t, err)
			defer aws.Close()

		case cloud.Azure:
			azure, err := cloudtest.NewAzureEnvironment()
			assert.NilError(t, err)
			defer azure.Close()
			scanner.AzureLoginURL = azure.OIDCIssuerServer.URL
		}
	}

	if tc.haveCredentialsSecret != nil {
		err = kubernetes.TestKubeClient.Create(ctx, tc.haveCredentialsSecret)
		assert.NilError(t, err)
	}

	for container, versions := range tc.haveRemoteVersions {
		for _, version := range versions {
			manifest := ociregistry.Manifest{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Annotations: map[string]string{
					"org.opencontainers.image.url": tc.haveRemoteURLs[container],
				},
				Config: ociregistry.Descriptor{
					Digest: digest.FromString(""),
				},
			}

			bytes, err := json.Marshal(&manifest)
			assert.NilError(t, err)
			_, err = tlsRegistry.PushManifest(
				ctx,
				container,
				version,
				bytes,
				"application/vnd.docker.distribution.manifest.v2+json",
			)
			assert.NilError(t, err)
		}
	}

	indexFile := &repo.IndexFile{
		Entries: map[string]repo.ChartVersions{},
	}
	for chartName, versions := range tc.haveRemoteChartVersions {
		chartVersions := make(repo.ChartVersions, 0, len(versions))
		for _, version := range versions {
			chartVersions = append(chartVersions, &repo.ChartVersion{
				Digest: "cccc",
				Metadata: &chart.Metadata{
					Version: version,
					Home:    tc.haveRemoteChartURLs[chartName],
				},
			})
		}
		indexFile.Entries[chartName] = chartVersions
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/index.yaml", func(w http.ResponseWriter, r *http.Request) {
		if tc.havePrivateRegistry {
			auth, found := r.Header["Authorization"]
			if !found {
				w.WriteHeader(401)
				return
			}

			if len(auth) != 1 {
				w.WriteHeader(401)
				return
			}

			var expectedCreds string
			switch tc.haveCloudProvider {
			case cloud.GCP:
				expectedCreds = "Basic b2F1dGgyYWNjZXNzdG9rZW46YWFhYQ=="

			case cloud.AWS:
				expectedCreds = "Basic bmF2ZWNkOmFiY2Q="

			case cloud.Azure:
				expectedCreds = "Basic MDAwMDAwMDAtMDAwMC0wMDAwLTAwMDAtMDAwMDAwMDAwMDAwOmFhYWE="

			default:
				expectedCreds = "Basic bmF2ZWNkOmFiY2Q="
			}
			// navecd:abcd
			if auth[0] != expectedCreds {
				w.WriteHeader(401)
				_, _ = fmt.Fprintf(
					w,
					"wrong credentials: got %s, expected %s",
					auth[0],
					expectedCreds,
				)
				return
			}

			bytes, err := yaml.Marshal(indexFile)
			if err != nil {
				w.WriteHeader(500)
				w.Write([]byte(err.Error()))
			}
			_, _ = w.Write(bytes)
		} else {
			bytes, err := yaml.Marshal(indexFile)
			if err != nil {
				w.WriteHeader(500)
				w.Write([]byte(err.Error()))
			}
			_, _ = w.Write(bytes)
		}
	})
	mux.HandleFunc(
		"POST /oauth2/exchange",
		func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				return
			}

			if !strings.HasPrefix(
				string(body),
				"access_token=nottheacrtoken&grant_type=access_token&service=127.0.0.1",
			) {
				w.WriteHeader(500)
				return
			}

			w.WriteHeader(200)
			_, err = w.Write([]byte(`{"refresh_token": "aaaa"}`))
			if err != nil {
				w.WriteHeader(500)
				return
			}
		},
	)
	helmServer := httptest.NewTLSServer(mux)
	defer helmServer.Close()

	patchedInstruction := patchInstruction(
		tc.haveUpdateInstruction,
		tlsRegistry,
		helmServer,
		aws,
	)
	imageScan, hasUpdate, err := scanner.Scan(ctx, patchedInstruction)
	if tc.wantErr != "" {
		assert.ErrorContains(t, err, tc.wantErr)
		return
	}
	assert.NilError(t, err)

	if tc.wantImageScan == nil {
		assert.Equal(t, hasUpdate, false)
		return
	}

	assert.Assert(t, imageScan != nil)

	assert.DeepEqual(t, *imageScan, *tc.wantImageScan)
}

func patchInstruction(
	instruction version.UpdateInstruction,
	tlsRegistry *ocitest.Registry,
	helmServer *httptest.Server,
	aws *cloudtest.AWSEnvironment,
) version.UpdateInstruction {
	patchedInstruction := instruction
	switch target := instruction.Target.(type) {
	case *version.ContainerUpdateTarget:
		var patchedImage string
		if instruction.Auth != nil && instruction.Auth.WorkloadIdentity != nil {
			switch instruction.Auth.WorkloadIdentity.Provider {
			case cloud.AWS:
				patchedImage = fmt.Sprintf("%s/%s", aws.ECRServer.URL, target.Image)
			case cloud.GCP:
				patchedImage = fmt.Sprintf("%s/%s", tlsRegistry.Addr(), target.Image)
			case cloud.Azure:
				patchedImage = fmt.Sprintf("%s/%s", tlsRegistry.Addr(), target.Image)
			}
		} else {
			patchedImage = fmt.Sprintf("%s/%s", tlsRegistry.Addr(), target.Image)
		}

		target.Image = patchedImage
		target.UnstructuredNode[target.UnstructuredKey] = patchedImage

		patchedInstruction.Target = target

	case *version.ChartUpdateTarget:
		if registry.IsOCI(target.Chart.RepoURL) {
			if target.Chart.Auth != nil && target.Chart.Auth.WorkloadIdentity != nil {
				switch target.Chart.Auth.WorkloadIdentity.Provider {
				case cloud.AWS:
					target.Chart.RepoURL = fmt.Sprintf("oci://%s", aws.ECRServer.URL)
				case cloud.GCP:
					target.Chart.RepoURL = tlsRegistry.URL()
				case cloud.Azure:
					target.Chart.RepoURL = tlsRegistry.URL()
				}
			} else {
				target.Chart.RepoURL = tlsRegistry.URL()
			}
		} else {
			if target.Chart.Auth != nil && target.Chart.Auth.WorkloadIdentity != nil {
				switch target.Chart.Auth.WorkloadIdentity.Provider {
				case cloud.AWS:
					port := helmServer.Listener.Addr().(*net.TCPAddr).Port
					target.Chart.RepoURL = fmt.Sprintf("https://%s:%v", cloudtest.AWSRegistryHost, strconv.Itoa(port))
				case cloud.GCP:
					target.Chart.RepoURL = helmServer.URL
				case cloud.Azure:
					target.Chart.RepoURL = helmServer.URL
				}
			} else {
				target.Chart.RepoURL = helmServer.URL
			}
		}

		patchedInstruction.Target = target
	}

	return patchedInstruction
}
