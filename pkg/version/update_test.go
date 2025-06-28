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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/kharf/navecd/internal/gittest"
	inttxtar "github.com/kharf/navecd/internal/txtar"
	"github.com/kharf/navecd/pkg/vcs"
	"github.com/kharf/navecd/pkg/version"
	"go.uber.org/zap/zapcore"
	"golang.org/x/tools/txtar"
	"gotest.tools/v3/assert"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type updateTestCase struct {
	name                  string
	haveFiles             string
	haveAvailableUpdate   version.AvailableUpdate
	haveBranch            string
	haveBranchWithChanges map[string]string
	havePullRequest       *vcs.PullRequestRequest
	wantUpdate            *version.Update
	wantPullRequest       *vcs.PullRequestRequest
	wantFiles             string
	wantErr               string
}

var (
	updates = updateTestCase{
		name: "Update",
		haveFiles: `
-- apps/myapp.cue --
image: "myimage:1.15.0@sha256:sha256:2d93689cbcdda92b425bfd82f87f5b656791a8a3e96c8eb2d702c6698987629a"
`,
		wantFiles: `
-- apps/myapp.cue --
image: "myimage:1.16.5@sha256:digest"
`,
		haveAvailableUpdate: version.AvailableUpdate{
			ImageScan: version.ImageScan{
				CurrentVersion: "1.15.0@sha256:sha256:2d93689cbcdda92b425bfd82f87f5b656791a8a3e96c8eb2d702c6698987629a",
				NewVersion:     "1.16.5@sha256:digest",
			},
			Integration: version.Direct,
			File:        "apps/myapp.cue",
			Line:        1,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.15.0@sha256:sha256:2d93689cbcdda92b425bfd82f87f5b656791a8a3e96c8eb2d702c6698987629a",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.15.0@sha256:sha256:2d93689cbcdda92b425bfd82f87f5b656791a8a3e96c8eb2d702c6698987629a",
				},
				UnstructuredKey: "image",
			},
		},
		wantUpdate: &version.Update{
			CommitHash: "",
			NewVersion: "1.16.5@sha256:digest",
			IsPR:       false,
		},
	}

	existingBranch = updateTestCase{
		name: "Existing-Branch",
		haveFiles: `
-- apps/myapp.cue --
image: "myimage:1.14.0"
`,
		wantFiles: `
-- apps/myapp.cue --
image: "myimage:1.14.0"
`,
		haveAvailableUpdate: version.AvailableUpdate{
			ImageScan: version.ImageScan{
				CurrentVersion: "1.14.0",
				NewVersion:     "1.15.0",
			},
			Integration: version.PR,
			File:        "apps/myapp.cue",
			Line:        1,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.14.0",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.14.0",
				},
				UnstructuredKey: "image",
			},
		},
		haveBranch: "navecd/update-myimage",
		wantPullRequest: &vcs.PullRequestRequest{
			RepoID:     vcs.DefaultRepoID,
			Title:      "chore(update): bump myimage to 1.15.0",
			Branch:     "navecd/update-myimage",
			BaseBranch: "main",
		},
		wantUpdate: &version.Update{
			CommitHash: "",
			NewVersion: "1.15.0",
			IsPR:       true,
		},
	}

	existingBranchWithChanges = updateTestCase{
		name: "Existing-Branch-With-Changes",
		haveFiles: `
-- apps/myapp.cue --
image: "myimage:1.14.0"
`,
		wantFiles: `
-- apps/myapp.cue --
image: "myimage:1.14.0"
`,
		haveBranchWithChanges: map[string]string{
			"navecd/update-myimage": `
-- apps/myapp.cue --
image: "myimage:1.15.0"
`,
		},
		haveAvailableUpdate: version.AvailableUpdate{
			ImageScan: version.ImageScan{
				CurrentVersion: "1.14.0",
				NewVersion:     "1.15.0",
			},
			Integration: version.PR,
			File:        "apps/myapp.cue",
			Line:        1,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.14.0",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.14.0",
				},
				UnstructuredKey: "image",
			},
		},
		wantPullRequest: nil,
		wantUpdate:      nil,
	}

	prIntegrationNoChanges = updateTestCase{
		name: "PR-Integration-No-Changes",
		haveFiles: `
-- apps/myapp.cue --
image: "myimage:1.15.0"
`,
		wantFiles: `
-- apps/myapp.cue --
image: "myimage:1.15.0"
`,
		haveAvailableUpdate: version.AvailableUpdate{
			ImageScan: version.ImageScan{
				CurrentVersion: "1.14.0",
				NewVersion:     "1.15.0",
			},
			Integration: version.PR,
			File:        "apps/myapp.cue",
			Line:        1,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.14.0",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.14.0",
				},
				UnstructuredKey: "image",
			},
		},
		wantPullRequest: nil,
		wantUpdate:      nil,
	}

	existingPullRequest = updateTestCase{
		name: "Existing-PullRequest",
		haveFiles: `
-- apps/myapp.cue --
image: "myimage:1.14.0"
`,
		wantFiles: `
-- apps/myapp.cue --
image: "myimage:1.14.0"
`,
		havePullRequest: &vcs.PullRequestRequest{
			Branch:     "navecd/update-myimage",
			BaseBranch: "main",
		},
		wantPullRequest: &vcs.PullRequestRequest{
			RepoID:     vcs.DefaultRepoID,
			Title:      "chore(update): bump myimage to 1.15.0",
			Branch:     "navecd/update-myimage",
			BaseBranch: "main",
		},
		haveAvailableUpdate: version.AvailableUpdate{
			ImageScan: version.ImageScan{
				CurrentVersion: "1.14.0",
				NewVersion:     "1.15.0",
			},
			Integration: version.PR,
			File:        "apps/myapp.cue",
			Line:        1,
			Target: &version.ContainerUpdateTarget{
				Image: "myimage:1.14.0",
				UnstructuredNode: map[string]any{
					"image": "myimage:1.14.0",
				},
				UnstructuredKey: "image",
			},
		},
		wantUpdate: nil,
	}
)

func TestUpdater_Update(t *testing.T) {
	ctx := context.Background()

	testCases := []updateTestCase{
		updates,
		existingBranch,
		existingBranchWithChanges,
		prIntegrationNoChanges,
		existingPullRequest,
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runUpdateTestCase(t, ctx, tc)
		})
	}
}

func runUpdateTestCase(t *testing.T, ctx context.Context, tc updateTestCase) {
	projectDir := t.TempDir()

	haveArch, err := inttxtar.Create(projectDir, bytes.NewReader([]byte(tc.haveFiles)))
	assert.NilError(t, err)

	wantPRs := make([]vcs.PullRequestRequest, 0, 1)
	if tc.wantPullRequest != nil {
		wantPRs = append(wantPRs, *tc.wantPullRequest)
	}
	havePRs := make([]vcs.PullRequestRequest, 0, 1)
	if tc.havePullRequest != nil {
		havePRs = append(havePRs, *tc.havePullRequest)
	}

	server, client := gittest.MockGitProvider(
		t,
		vcs.DefaultRepoID,
		fmt.Sprintf("navecd-%s", `dev`),
		wantPRs,
		havePRs,
	)
	defer server.Close()

	remoteDir := t.TempDir()
	gitRepo, err := gittest.InitGitRepository(t, remoteDir, projectDir, "main")
	assert.NilError(t, err)

	vcsRepository, err := vcs.Open(projectDir, vcs.WithAuth(vcs.Auth{
		Method: nil,
		Token:  "",
	}), vcs.WithProvider(vcs.GitHub), vcs.WithHTTPClient(client))
	assert.NilError(t, err)

	if tc.haveBranch != "" {
		err = vcsRepository.SwitchBranch(tc.haveBranch, true)
		assert.NilError(t, err)
	}

	for branch, files := range tc.haveBranchWithChanges {
		err = vcsRepository.SwitchBranch(branch, true)
		assert.NilError(t, err)
		_, err := inttxtar.Create(projectDir, bytes.NewReader([]byte(files)))
		assert.NilError(t, err)
		_, err = gitRepo.CommitFile(".", "update")
		assert.NilError(t, err)
	}

	err = vcsRepository.SwitchBranch("main", false)
	assert.NilError(t, err)

	logOpts := zap.Options{
		Development: true,
		Level:       zapcore.Level(-1),
	}
	log := zap.New(zap.UseFlagOptions(&logOpts))

	updater := &version.Updater{
		Log: log,
	}

	tc.haveAvailableUpdate.Repository = vcsRepository
	tc.haveAvailableUpdate.Branch = "main"

	update, err := updater.Update(ctx, tc.haveAvailableUpdate)
	if tc.wantErr != "" {
		assert.ErrorContains(t, err, tc.wantErr)
		return
	}
	assert.NilError(t, err)

	if tc.wantUpdate != nil {
		assert.Equal(
			t,
			update.IsPR,
			tc.wantUpdate.IsPR,
		)

		assert.Equal(
			t,
			update.NewVersion,
			tc.wantUpdate.NewVersion,
		)

		assert.Assert(t, update.CommitHash != "")
	} else {
		assert.Assert(t, update == nil)
	}

	wantArch, err := inttxtar.Create(t.TempDir(), bytes.NewReader([]byte(tc.wantFiles)))
	assert.NilError(t, err)

	assert.Equal(
		t,
		len(haveArch.Files),
		len(wantArch.Files),
		"wrong testcase: file count of haveFiles and wantFiles should not differ",
	)

	haveFile, err := os.Open(filepath.Join(projectDir, tc.haveAvailableUpdate.File))
	assert.NilError(t, err)
	haveData, err := io.ReadAll(haveFile)
	assert.NilError(t, err)

	assert.Assert(t, slices.ContainsFunc(wantArch.Files, func(wantFile txtar.File) bool {
		return wantFile.Name == tc.haveAvailableUpdate.File
	}))

	for _, wantFile := range wantArch.Files {
		if wantFile.Name == tc.haveAvailableUpdate.File {
			assert.Equal(t, string(haveData), string(wantFile.Data))
		}
	}

	gitRepository, err := git.PlainClone(t.TempDir(), false,
		&git.CloneOptions{
			URL:           remoteDir,
			Progress:      nil,
			ReferenceName: plumbing.ReferenceName("main"),
		},
	)
	assert.NilError(t, err)

	remote, err := gitRepository.Remote(git.DefaultRemoteName)
	assert.NilError(t, err)
	remoteRefs, err := remote.List(&git.ListOptions{})
	assert.NilError(t, err)

	// check if prs are pushed to remote
	if tc.wantPullRequest != nil {
		assert.Assert(
			t,
			slices.ContainsFunc(remoteRefs, func(ref *plumbing.Reference) bool {
				return tc.wantPullRequest.Branch == ref.Name().Short() && ref.Name().IsBranch()
			}),
		)
	}
}
