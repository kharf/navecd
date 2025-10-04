package cue

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/kharf/navecd/pkg/cloud"
	"github.com/kharf/navecd/pkg/kube"
	"github.com/kharf/navecd/pkg/oci"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ModuleManager loads a navecd project cue module to a local path.
type ModuleManager struct {
	KubeClient kube.Client[unstructured.Unstructured, unstructured.Unstructured]
	log        logr.Logger

	// Namespace the controller runs in.
	Namespace string

	// Endpoint to the microsoft azure login server.
	// Default is usually: https://login.microsoftonline.com/.
	AzureLoginURL string

	// Endpoint to the google metadata server, which provides access tokens.
	// Default is: http://metadata.google.internal.
	GCPMetadataServerURL string
}

type Module struct {
}

func (manager *ModuleManager) Load(ctx context.Context, repositoryName string, ref string, repositoryDir string, auth cloud.Auth) (*Module, error) {
	creds, err := cloud.ReadCredentials(
		ctx,
		repositoryName,
		auth,
		manager.KubeClient,
		cloud.WithNamespace(manager.Namespace),
		cloud.WithCustomAzureLoginURL(manager.AzureLoginURL),
		cloud.WithCustomGCPMetadataServerURL(manager.GCPMetadataServerURL),
	)
	if err != nil {
		return nil, err
	}

	ociClient, err := oci.NewRepositoryClient(repositoryName)
	if err != nil {
		return nil, err
	}

	authOption := oci.WithBasicAuth(creds.Username, creds.Password)

	image, err := ociClient.Image(ref, authOption)
	if err != nil {
		return nil, err
	}
	layers, err := image.Layers()
	if err != nil {
		return nil, err
	}

	for _, layer := range layers {
		media, err := layer.MediaType()
		if err != nil {
			return nil, err
		}

		fmt.Println("media", media)
	}

	// repoWriter, err := os.Open(repositoryDir)
	// if err != nil {
	// 	return nil, err
	// }

	// _, err = io.Copy(repoWriter, modReader)
	// if err != nil {
	// 	return nil, err
	// }

	return &Module{}, nil
}
