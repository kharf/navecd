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

package gittest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/go-github/v70/github"
	"github.com/kharf/navecd/pkg/vcs"
	"github.com/xanzy/go-gitlab"
	"gotest.tools/v3/assert"
)

type LocalGitRepository struct {
	Repository *git.Repository
	Worktree   *git.Worktree
	Directory  string
}

func (r *LocalGitRepository) CommitFile(file string, message string) (string, error) {
	worktree := r.Worktree
	if _, err := worktree.Add(file); err != nil {
		return "", err
	}

	hash, err := worktree.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  "John Doe",
			Email: "john@doe.org",
			When:  time.Now(),
		},
	})
	if err != nil {
		return "", err
	}

	if err := r.Repository.Push(&git.PushOptions{}); err != nil {
		return "", err
	}
	return hash.String(), nil
}

func (r *LocalGitRepository) CommitNewFile(file string, message string) (string, error) {
	if err := os.WriteFile(filepath.Join(r.Directory, file), []byte{}, 0664); err != nil {
		return "", err
	}
	return r.CommitFile(file, message)
}

func SetupGitRepository(t testing.TB, branch string) (*LocalGitRepository, error) {
	dir := t.TempDir()

	fileName := "test1"
	r, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		Bare: false,
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.NewBranchReferenceName(branch),
		},
	})
	if err != nil {
		return nil, err
	}

	worktree, err := r.Worktree()
	if err != nil {
		return nil, err
	}

	remoteDir := t.TempDir()
	_, err = git.PlainInitWithOptions(remoteDir, &git.PlainInitOptions{
		Bare: true,
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.NewBranchReferenceName(branch),
		},
	})
	if err != nil {
		return nil, err
	}
	_, err = r.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remoteDir}})
	if err != nil {
		return nil, err
	}

	localRepository := &LocalGitRepository{
		Repository: r,
		Worktree:   worktree,
		Directory:  dir,
	}

	if _, err := localRepository.CommitNewFile(fileName, "first commit"); err != nil {
		return nil, err
	}

	return localRepository, nil
}

func InitGitRepository(
	t testing.TB,
	remoteDir string,
	dir string,
	branch string,
) (*LocalGitRepository, error) {
	_, err := git.PlainInitWithOptions(remoteDir, &git.PlainInitOptions{
		Bare: true,
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.NewBranchReferenceName(branch),
		},
	})
	if err != nil {
		return nil, err
	}

	localRepo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		Bare: false,
		InitOptions: git.InitOptions{
			DefaultBranch: plumbing.NewBranchReferenceName(branch),
		},
	})
	if err != nil {
		return nil, err
	}
	_, err = localRepo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remoteDir}})
	if err != nil {
		return nil, err
	}

	worktree, err := localRepo.Worktree()
	if err != nil {
		return nil, err
	}

	localRepository := &LocalGitRepository{
		Repository: localRepo,
		Worktree:   worktree,
		Directory:  dir,
	}

	if _, err := localRepository.CommitFile(".", "first commit"); err != nil {
		return nil, err
	}

	return localRepository, nil
}

// enforceHostRoundTripper rewrites all requests with the given `Host`.
type enforceHostRoundTripper struct {
	Host                 string
	UpstreamRoundTripper http.RoundTripper
}

func (efrt *enforceHostRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	splitHost := strings.Split(efrt.Host, "://")
	r.URL.Scheme = splitHost[0]
	r.URL.Host = splitHost[1]

	return efrt.UpstreamRoundTripper.RoundTrip(r)
}

type deployKeyRequest struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	CanPush bool   `json:"can_push"`
}

func MockGitProvider(
	t *testing.T,
	repoID string,
	expectedDeployKeyTitle string,
	expectedPrRequests []vcs.PullRequestRequest,
	havePRs []vcs.PullRequestRequest,
) (*httptest.Server, *http.Client) {
	mux := http.NewServeMux()

	// Github
	mux.HandleFunc(
		fmt.Sprintf("POST /repos/%s/keys", repoID),
		func(w http.ResponseWriter, r *http.Request) {
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				w.Write([]byte(err.Error()))
				return
			}

			authHeader := r.Header["Authorization"]
			assert.Assert(t, len(authHeader) == 1)
			assert.Assert(t, strings.HasPrefix(authHeader[0], "Bearer"))
			assert.Assert(t, authHeader[0] != "Bearer ")
			assert.Assert(t, authHeader[0] != "Bearer")

			var req deployKeyRequest
			err = json.Unmarshal(bodyBytes, &req)
			assert.NilError(t, err)

			assert.Equal(t, req.Title, expectedDeployKeyTitle)
			assert.Assert(t, strings.HasPrefix(req.Key, "ssh-ed25519 AAAA"))

			w.Write([]byte(`{
				"key" : "ssh-rsa AAAA...",
				"id" : 12,
				"title" : "My deploy key",
				"can_push": true,
				"created_at" : "2015-08-29T12:44:31.550Z",
				"expires_at": null
				}
			}`))
		},
	)

	mux.HandleFunc(
		fmt.Sprintf("POST /repos/%s/pulls", repoID),
		func(w http.ResponseWriter, r *http.Request) {
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				w.Write([]byte(err.Error()))
				return
			}

			var req github.NewPullRequest
			err = json.Unmarshal(bodyBytes, &req)
			assert.NilError(t, err)

			assert.Assert(
				t,
				slices.ContainsFunc(expectedPrRequests, func(pr vcs.PullRequestRequest) bool {
					return pr.BaseBranch == *req.Base &&
						pr.Branch == *req.Head &&
						pr.Title == *req.Title
				}),
				fmt.Sprintf("got [base=%s, branch=%s, title=%s]", *req.Base, *req.Head, *req.Title),
			)

			if slices.ContainsFunc(havePRs, func(pr vcs.PullRequestRequest) bool {
				return pr.Branch == *req.Head && pr.BaseBranch == *req.Base
			}) {
				w.WriteHeader(400)
				w.Write([]byte("already exists"))
				return
			}

			w.Write([]byte(`{}`))
		},
	)

	// Gitlab
	mux.HandleFunc(
		fmt.Sprintf("POST /api/v4/projects/%s/deploy_keys", url.PathEscape(repoID)),
		func(w http.ResponseWriter, r *http.Request) {
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				w.Write([]byte(err.Error()))
				return
			}

			authHeader := r.Header["Private-Token"]
			assert.Assert(t, len(authHeader) == 1)
			assert.Assert(t, authHeader[0] != "")

			var req deployKeyRequest
			err = json.Unmarshal(bodyBytes, &req)
			assert.NilError(t, err)

			assert.Equal(t, req.Title, expectedDeployKeyTitle)
			assert.Assert(t, strings.HasPrefix(req.Key, "ssh-ed25519 AAAA"))
			assert.Equal(t, req.CanPush, true)

			w.Write([]byte(`{
				"key" : "ssh-rsa AAAA...",
				"id" : 12,
				"title" : "My deploy key",
				"can_push": true,
				"created_at" : "2015-08-29T12:44:31.550Z",
				"expires_at": null
				}
			}`))
		},
	)

	mux.HandleFunc(
		fmt.Sprintf("POST /api/v4/projects/%s/merge_requests", url.PathEscape(repoID)),
		func(w http.ResponseWriter, r *http.Request) {
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				w.Write([]byte(err.Error()))
				return
			}

			var req gitlab.MergeRequest
			err = json.Unmarshal(bodyBytes, &req)
			assert.NilError(t, err)

			assert.Assert(
				t,
				slices.ContainsFunc(expectedPrRequests, func(pr vcs.PullRequestRequest) bool {
					return pr.BaseBranch == req.TargetBranch &&
						pr.Branch == req.SourceBranch &&
						pr.Title == req.Title
				}),
				fmt.Sprintf(
					"got [base=%s, branch=%s, title=%s]",
					req.TargetBranch,
					req.SourceBranch,
					req.Title,
				),
			)

			if slices.ContainsFunc(havePRs, func(pr vcs.PullRequestRequest) bool {
				return pr.Branch == req.SourceBranch && pr.BaseBranch == req.TargetBranch
			}) {
				w.WriteHeader(400)
				w.Write([]byte("already exists"))
				return
			}

			w.Write([]byte(`{}`))
		},
	)

	server := httptest.NewTLSServer(mux)

	client := server.Client()
	client.Transport = &enforceHostRoundTripper{
		Host:                 server.URL,
		UpstreamRoundTripper: client.Transport,
	}
	return server, client
}

type FakeRepository struct {
	mu sync.Mutex

	RepoPath  string
	PullError error

	CommitsMade []string
}

func (f *FakeRepository) DeleteLocalBranch(branch string) error {
	return nil
}

func (f *FakeRepository) Commit(file string, message string) (string, error) {
	f.mu.Lock()
	f.CommitsMade = append(f.CommitsMade, message)
	f.mu.Unlock()
	return "hash", nil
}

func (f *FakeRepository) CreatePullRequest(
	title string,
	desc string,
	src string,
	dst string,
) error {
	panic("unimplemented")
}

func (f *FakeRepository) CurrentBranch() (string, error) {
	panic("unimplemented")
}

func (f *FakeRepository) Path() string {
	return f.RepoPath
}

func (f *FakeRepository) Pull() (string, error) {
	if f.PullError != nil {
		return "", f.PullError
	}

	return "hash", nil
}

func (f *FakeRepository) Push(src string, dst string) error {
	return nil
}

func (f *FakeRepository) RepoID() string {
	panic("unimplemented")
}

func (f *FakeRepository) SwitchBranch(branch string, create bool) error {
	panic("unimplemented")
}

var _ vcs.Repository = (*FakeRepository)(nil)
