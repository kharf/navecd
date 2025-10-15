package project

import (
	"context"
	"errors"

	"github.com/kharf/navecd/pkg/cloud"
	"github.com/kharf/navecd/pkg/kube"
	"github.com/kharf/navecd/pkg/oci"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// RecoverableLoadError indicates an issue happened when trying to load a project from a potential remote location.
// It points to a backup path, which might hold an older version of a project.
type RecoverableLoadError struct {
	BackupPath string
	Err        error
}

var _ error = (*RecoverableLoadError)(nil)

func (d *RecoverableLoadError) Error() string {
	return "Load error"
}

func (d *RecoverableLoadError) Unwrap() error {
	return d.Err
}

// OCIRepositoryRef is a storage location for container images and other artifacts.
type OCIRepositoryRef struct {
	Name string
	Ref  string
}

// Digest of the loaded remote project artifact.
type Digest string

// RemoteLoader loads a remote navecd project to a local path.
type RemoteLoader interface {
	Load(ctx context.Context, targetDir string, auth *cloud.Auth) (Digest, error)
}

// OCIRemoteLoader loads a remote navecd project oci image to a local path.
type OCIRemoteLoader struct {
	// Repository to download
	Repository OCIRepositoryRef

	KubeClient kube.Client[unstructured.Unstructured, unstructured.Unstructured]

	// Directory used to cache repositories oci images.
	CacheDir string

	// Namespace the controller runs in.
	Namespace string

	// Endpoint to the microsoft azure login server.
	// Default is usually: https://login.microsoftonline.com/.
	AzureLoginURL string

	// Endpoint to the google metadata server, which provides access tokens.
	// Default is: http://metadata.google.internal.
	GCPMetadataServerURL string
}

var _ RemoteLoader = (*OCIRemoteLoader)(nil)

func (loader *OCIRemoteLoader) Load(
	ctx context.Context,
	targetDir string,
	auth *cloud.Auth,
) (Digest, error) {
	repository := loader.Repository
	var opts []oci.ProjectClientOption
	if auth != nil {
		creds, err := cloud.ReadCredentials(
			ctx,
			repository.Name,
			*auth,
			loader.KubeClient,
			cloud.WithNamespace(loader.Namespace),
			cloud.WithCustomAzureLoginURL(loader.AzureLoginURL),
			cloud.WithCustomGCPMetadataServerURL(loader.GCPMetadataServerURL),
		)
		if err != nil {
			return "", err
		}
		opts = append(opts, oci.WithRepositoryOption(oci.WithBasicAuth(creds.Username, creds.Password)))
	}

	opts = append(opts, oci.WithCacheDir(loader.CacheDir))

	ociClient, err := oci.NewRepositoryClient(repository.Name)
	if err != nil {
		return "", err
	}
	projectClient := oci.NewProjectClient(ociClient)

	digest, err := projectClient.LoadImage(ctx, repository.Ref, targetDir, opts...)
	if err != nil {
		var unrecErr *oci.UnrecoverableError
		if errors.As(err, &unrecErr) {
			return "", err
		}

		backupPath := targetDir
		var recError *oci.RecoverableError
		if errors.As(err, &recError) {
			backupPath = recError.BackupPath
		}

		return "", &RecoverableLoadError{
			Err:        err,
			BackupPath: backupPath,
		}
	}

	return Digest(digest), nil
}
