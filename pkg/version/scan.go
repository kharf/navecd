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

package version

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-logr/logr"
	"github.com/kharf/navecd/internal/slices"
	"github.com/kharf/navecd/pkg/cloud"
	"github.com/kharf/navecd/pkg/helm"
	"github.com/kharf/navecd/pkg/kube"
	"github.com/kharf/navecd/pkg/oci"
	"github.com/opencontainers/image-spec/specs-go/v1"
	"helm.sh/helm/v3/pkg/registry"
	"helm.sh/helm/v3/pkg/repo"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/kubernetes/pkg/util/parsers"
	"sigs.k8s.io/yaml"
)

// Scanner is the system for performing version scanning operations.
// It takes update instructions and contacts image registries to fetch remote tags and calculates the latest tag based on the provided update strategy.
// If the latest tag is greater than the current tag, it returns an AvailableUpdate.
type Scanner struct {
	Log        logr.Logger
	KubeClient kube.Client[unstructured.Unstructured, unstructured.Unstructured]

	OCIClient oci.Client

	// Kubernetes namespace where the registry credential secret is stored.
	Namespace string

	// Endpoint to the microsoft azure login server.
	// Default is: https://login.microsoftonline.com/.
	AzureLoginURL string

	// Endpoint to the google metadata server, which provides access tokens.
	// Default is: http://metadata.google.internal.
	GCPMetadataServerURL string
}

// ImageScan represents the result of a version scanning operation.
// It holds details about the current and new version.
type ImageScan struct {
	// The current version that is being scanned for updates.
	// Format: tag@digest.
	// Digest is optional.
	CurrentVersion string
	// The new version that has been found.
	// Format: tag@digest.
	// Digest is optional.
	NewVersion string
	// URL to find more information on the update/package.
	URL string
}

func (scanner *Scanner) Scan(
	ctx context.Context,
	updateInstr UpdateInstruction,
) (*ImageScan, bool, error) {
	strategy := getStrategy(updateInstr.Strategy, updateInstr.Constraint)

	scanResult, err := scanner.scanTarget(ctx, updateInstr.Target, updateInstr.Auth)
	if err != nil {
		return nil, false, err
	}

	newVersion, hasNewVersion, idx, err := strategy.HasNewerRemoteVersion(
		scanResult.currentVersion,
		scanResult.pkg.versions,
	)
	if err != nil {
		return nil, false, err
	}
	if !hasNewVersion {
		return nil, false, nil
	}

	pkgMetadata, err := scanResult.pkg.loadMetadata(idx)
	if err != nil {
		return nil, false, err
	}

	currentVersion := scanResult.currentVersion
	if scanResult.currentDigest != "" {
		newVersion = fmt.Sprintf("%s@%s", newVersion, pkgMetadata.digest)
		currentVersion = fmt.Sprintf("%s@%s", currentVersion, scanResult.currentDigest)
	}

	return &ImageScan{
		URL:            pkgMetadata.infoURL,
		CurrentVersion: currentVersion,
		NewVersion:     newVersion,
	}, true, nil
}

type pkgMetadata struct {
	infoURL string
	digest  string
}

type pkg struct {
	versions     VersionIter[string]
	loadMetadata func(versionsIdx int) (*pkgMetadata, error)
}

func (scanner *Scanner) scanTarget(
	ctx context.Context,
	target UpdateTarget,
	auth *cloud.Auth,
) (*scanResult, error) {
	var currentVersion, currentDigest string
	var err error
	var pkg *pkg

	switch target := target.(type) {
	case *ContainerUpdateTarget:
		var repo string
		repo, currentVersion, currentDigest, err = parsers.ParseImageName(target.Image)
		if err != nil {
			return nil, err
		}
		idx := strings.LastIndex(repo, "/")
		host := repo[:idx]

		pkg, err = scanner.scanContainer(
			ctx,
			host,
			repo,
			auth,
		)

	case *ChartUpdateTarget:
		currentVersion, currentDigest = helm.ParseVersion(target.Chart.Version)

		if registry.IsOCI(target.Chart.RepoURL) {
			host, _ := strings.CutPrefix(target.Chart.RepoURL, "oci://")
			repo := fmt.Sprintf(
				"%s/%s",
				host,
				target.Chart.Name,
			)

			pkg, err = scanner.scanContainer(
				ctx,
				host,
				repo,
				target.Chart.Auth,
			)
		} else {
			pkg, err = scanner.scanHTTPHelmChart(
				ctx,
				target.Chart.RepoURL,
				target.Chart.Name,
				target.Chart.Auth,
			)
		}
	}

	if err != nil {
		return nil, err
	}

	return &scanResult{
		currentVersion: currentVersion,
		currentDigest:  currentDigest,
		pkg:            *pkg,
	}, nil
}

func (scanner *Scanner) scanContainer(
	ctx context.Context,
	host string,
	repoName string,
	auth *cloud.Auth,
) (*pkg, error) {
	var authOption oci.Option
	if auth != nil {
		creds, err := cloud.ReadCredentials(
			ctx,
			host,
			*auth,
			scanner.KubeClient,
			cloud.WithNamespace(scanner.Namespace),
			cloud.WithCustomAzureLoginURL(scanner.AzureLoginURL),
			cloud.WithCustomGCPMetadataServerURL(scanner.GCPMetadataServerURL),
		)
		if err != nil {
			return nil, err
		}

		authOption = oci.WithBasicAuth(creds.Username, creds.Password)
	}

	var ociClient oci.Client
	if scanner.OCIClient == nil {
		var err error
		ociClient, err = oci.NewRepositoryClient(repoName)
		if err != nil {
			return nil, err
		}
	} else {
		ociClient = scanner.OCIClient
	}

	remoteVersions, err := ociClient.ListTags(repoName, authOption)
	if err != nil {
		return nil, err
	}

	return &pkg{
		versions: &slices.Iter[string]{Slice: remoteVersions},
		loadMetadata: func(versionsIdx int) (*pkgMetadata, error) {
			version := remoteVersions[versionsIdx]
			image, err := ociClient.Image(version, authOption)
			if err != nil {
				return nil, err
			}

			manifest, err := image.Manifest()
			if err != nil {
				return nil, err
			}

			digest, err := image.Digest()
			if err != nil {
				return nil, err
			}

			source := manifest.Annotations[v1.AnnotationURL]

			return &pkgMetadata{
				infoURL: source,
				digest:  digest.String(),
			}, nil
		},
	}, nil
}

func (scanner *Scanner) scanHTTPHelmChart(
	ctx context.Context,
	repoURL string,
	name string,
	auth *cloud.Auth,
) (*pkg, error) {
	request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/index.yaml", repoURL), nil)
	if err != nil {
		return nil, err
	}

	if auth != nil {
		creds, err := cloud.ReadCredentials(
			ctx,
			repoURL,
			*auth,
			scanner.KubeClient,
			cloud.WithNamespace(scanner.Namespace),
			cloud.WithCustomAzureLoginURL(scanner.AzureLoginURL),
			cloud.WithCustomGCPMetadataServerURL(scanner.GCPMetadataServerURL),
		)
		if err != nil {
			return nil, err
		}

		request.SetBasicAuth(creds.Username, creds.Password)
	}

	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %s", ErrUnexpectedResponse, body)
	}

	var indexFile repo.IndexFile
	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(bytes, &indexFile); err != nil {
		return nil, err
	}

	chartVersions, found := indexFile.Entries[name]
	if !found {
		return nil, fmt.Errorf("%w: %s", ErrChartNotFound, name)
	}

	return &pkg{
		versions: &helm.ChartVersionIter{Versions: chartVersions},
		loadMetadata: func(versionsIdx int) (*pkgMetadata, error) {
			version := chartVersions[versionsIdx]
			return &pkgMetadata{
				infoURL: version.Home,
				digest:  version.Digest,
			}, nil
		},
	}, nil
}

type scanResult struct {
	currentVersion string
	currentDigest  string
	pkg            pkg
}

type VersionIter[T any] interface {
	ForEach(do func(item T, idx int))
}

var _ VersionIter[string] = (*slices.Iter[string])(nil)
var _ VersionIter[string] = (*helm.ChartVersionIter)(nil)
