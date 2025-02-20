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

package vcs

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"golang.org/x/crypto/ssh"
	cryptoSSH "golang.org/x/crypto/ssh"
)

var (
	ErrRepositoryID = errors.New("Unknown repository id")
)

type deployKeyOptions struct {
	keySuffix string
}

type deployKeyOption interface {
	apply(*deployKeyOptions)
}

type WithKeySuffix string

func (s WithKeySuffix) apply(opts *deployKeyOptions) {
	opts.keySuffix = string(s)
}

type PullRequestRequest struct {
	RepoID      string
	Title       string
	Description string
	Branch      string
	BaseBranch  string
}

type providerClient interface {
	CreateDeployKey(ctx context.Context, repoID string, opts ...deployKeyOption) (*deployKey, error)
	CreatePullRequest(
		ctx context.Context,
		req PullRequestRequest,
	) error
}

type Provider string

const (
	GitHub  = "github"
	GitLab  = "gitlab"
	Generic = "generic"
)

func getProviderClient(
	httpClient *http.Client,
	provider Provider,
	token string,
) (providerClient, error) {
	switch provider {
	case GitHub:
		client := NewGithubClient(httpClient, token)
		return client, nil
	case GitLab:
		client, err := NewGitlabClient(httpClient, token)
		if err != nil {
			return nil, err
		}
		return client, nil
	default:
		return &GenericProviderClient{}, nil
	}
}

type GenericProviderClient struct{}

func (g *GenericProviderClient) CreatePullRequest(
	ctx context.Context,
	req PullRequestRequest,
) error {
	panic("unimplemented")
}

func (*GenericProviderClient) CreateDeployKey(
	ctx context.Context,
	repoID string,
	opts ...deployKeyOption,
) (*deployKey, error) {
	return nil, nil
}

func (*GenericProviderClient) GetHostPublicSSHKey() string {
	return ""
}

var _ providerClient = (*GenericProviderClient)(nil)

type deployKey struct {
	title             string
	publicKeyOpenSSH  string
	privateKeyOpenSSH string
}

func genDeployKey(opts ...deployKeyOption) (*deployKey, error) {
	deployKeyOpts := &deployKeyOptions{
		keySuffix: "",
	}
	for _, o := range opts {
		o.apply(deployKeyOpts)
	}

	publicKey, privKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}

	privKeyPemBlock, err := cryptoSSH.MarshalPrivateKey(privKey, "")
	if err != nil {
		return nil, err
	}

	var buf strings.Builder
	if err := pem.Encode(&buf, privKeyPemBlock); err != nil {
		return nil, err
	}

	privKeyString := buf.String()
	sshPublicKey, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		return nil, err
	}

	publicKeyString := fmt.Sprintf(
		"%s %s",
		sshPublicKey.Type(),
		base64.StdEncoding.EncodeToString(sshPublicKey.Marshal()),
	)

	title := "navecd"
	if deployKeyOpts.keySuffix != "" {
		title = fmt.Sprintf("%s-%s", title, deployKeyOpts.keySuffix)
	}

	return &deployKey{
		title:             title,
		publicKeyOpenSSH:  publicKeyString,
		privateKeyOpenSSH: privKeyString,
	}, nil
}
