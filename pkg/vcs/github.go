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
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/go-github/v75/github"
)

type GithubRepository struct {
	GenericRepository
	client githubClient
}

func (g *GithubRepository) CreatePullRequest(title, desc, branch, targetBranch string) error {
	return g.client.CreatePullRequest(
		context.Background(),
		PullRequestRequest{
			RepoID:      g.RepoID(),
			Title:       title,
			Description: desc,
			Branch:      branch,
			BaseBranch:  targetBranch,
		},
	)
}

var _ Repository = (*GithubRepository)(nil)

type githubClient struct {
	client *github.Client
}

func (g *githubClient) CreatePullRequest(
	ctx context.Context,
	req PullRequestRequest,
) error {
	newPR := &github.NewPullRequest{
		Title:               &req.Title,
		Body:                &req.Description,
		Head:                &req.Branch,
		Base:                &req.BaseBranch,
		MaintainerCanModify: github.Bool(true),
	}

	owner, repo, err := parseGithubRepoID(req.RepoID)
	if err != nil {
		return err
	}

	_, resp, err := g.client.PullRequests.Create(ctx, owner, repo, newPR)
	if err != nil {
		if resp != nil {
			defer resp.Body.Close()
			msg, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}

			if strings.Contains(string(msg), "already exists") {
				return fmt.Errorf("%w: %s", ErrPRAlreadyExists, msg)
			}
		}

		return err
	}

	return nil
}

func (g *githubClient) CreateDeployKey(
	ctx context.Context,
	id string,
	opts ...deployKeyOption,
) (*deployKey, error) {
	deployKey, err := genDeployKey(opts...)
	if err != nil {
		return nil, err
	}

	owner, repo, err := parseGithubRepoID(id)
	if err != nil {
		return nil, err
	}

	keyReqBody := github.Key{
		Title: &deployKey.title,
		Key:   &deployKey.publicKeyOpenSSH,
	}

	_, _, err = g.client.Repositories.CreateKey(ctx, owner, repo, &keyReqBody)
	if err != nil {
		return nil, err
	}

	return deployKey, nil
}

func parseGithubRepoID(id string) (owner string, repo string, err error) {
	idSplit := strings.Split(id, "/")
	if len(idSplit) != 2 {
		return "", "", fmt.Errorf(
			"%w: %s doesn't correspond to the owner/repo format",
			ErrRepositoryID,
			id,
		)
	}

	owner = idSplit[0]
	repo = idSplit[1]
	err = nil

	return
}

func NewGithubClient(httpClient *http.Client, token string) *githubClient {
	client := github.NewClient(httpClient).WithAuthToken(token)
	return &githubClient{
		client: client,
	}
}

var _ providerClient = (*githubClient)(nil)
