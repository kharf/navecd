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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/jonboulle/clockwork"
	"github.com/kharf/navecd/internal/dnstest"
	"github.com/kharf/navecd/internal/gittest"
	"github.com/kharf/navecd/internal/kubetest"
	"github.com/kharf/navecd/internal/ocitest"
	inttxtar "github.com/kharf/navecd/internal/txtar"
	"github.com/kharf/navecd/pkg/version"
	"go.uber.org/goleak"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sync/errgroup"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/poll"
	"k8s.io/kubernetes/pkg/util/parsers"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type image struct {
	name       string
	schedule   string
	constraint string
}

type scheduleTestCase struct {
	name         string
	haveImages   []image
	haveTags     map[string][]string
	wantErr      string
	wantCommits  []string
	wantJobCount int
}

var (
	newJobs = scheduleTestCase{
		name: "New-Jobs",
		haveTags: map[string][]string{
			"myimage":  {"1.16.5"},
			"myimage2": {"1.17.5"},
		},
		haveImages: []image{
			{
				name:       "myimage:1.15.0",
				schedule:   "* * * * * *",
				constraint: "1.16.5",
			},
			{
				name:       "myimage2:1.16.0",
				schedule:   "* * * * * *",
				constraint: "1.17.5",
			},
		},
		wantCommits: []string{
			"chore(update): bump myimage to 1.16.5",
			"chore(update): bump myimage2 to 1.17.5",
		},
		wantJobCount: 2,
	}

	missingSchedule = scheduleTestCase{
		name: "Missing-Schedule",
		haveTags: map[string][]string{
			"myimage":  {"1.16.5"},
			"myimage2": {"1.17.5"},
		},
		haveImages: []image{
			{
				name:       "myimage:1.15.0",
				schedule:   "",
				constraint: "1.16.5",
			},
		},
		// Errors are log only.
		wantErr:      "",
		wantJobCount: 0,
	}
)

func TestUpdateScheduler_Schedule(t *testing.T) {
	defer goleak.VerifyNone(
		t,
	)

	ctx := context.Background()

	testCases := []scheduleTestCase{
		newJobs,
		missingSchedule,
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			runScheduleTestCase(t, ctx, tc)
		})
	}
}

func runScheduleTestCase(t *testing.T, ctx context.Context, tc scheduleTestCase) {
	fakeClock := clockwork.NewFakeClock()
	scheduler, err := gocron.NewScheduler(gocron.WithClock(fakeClock))
	assert.NilError(t, err)
	scheduler.Start()
	quitChan := make(chan struct{}, 1)
	schedulerEg := &errgroup.Group{}
	schedulerEg.SetLimit(1)
	defer func() {
		quitChan <- struct{}{}
		_ = scheduler.Shutdown()
	}()

	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	projectDir := t.TempDir()

	repoNames := make([]string, 0, len(tc.haveImages))
	updateInstructions := make([]version.UpdateInstruction, 0, len(tc.haveImages))
	sb := strings.Builder{}
	sb.Write([]byte("-- apps/myapp.cue --\n"))
	for i, image := range tc.haveImages {
		sb.Write([]byte(image.name))
		updateInstructions = append(updateInstructions, version.UpdateInstruction{
			Strategy:    version.SemVer,
			Constraint:  image.constraint,
			Integration: version.Direct,
			Schedule:    image.schedule,
			File:        "apps/myapp.cue",
			Line:        i + 1,
			Target: &version.ContainerUpdateTarget{
				Image: image.name,
				UnstructuredNode: map[string]any{
					"image": image.name,
				},
				UnstructuredKey: "image",
			},
		})

		repoName, _, _, err := parsers.ParseImageName(image.name)
		assert.NilError(t, err)
		repoNames = append(repoNames, repoName)
	}

	_, err = inttxtar.Create(projectDir, bytes.NewReader([]byte(sb.String())))
	assert.NilError(t, err)

	logOpts := zap.Options{
		Development: true,
		Level:       zapcore.Level(-1),
	}
	log := zap.New(zap.UseFlagOptions(&logOpts))

	repository := &gittest.FakeRepository{
		RepoPath: projectDir,
	}

	haveTags := make(map[string][]string, len(tc.haveTags))

	for key, value := range tc.haveTags {
		repoName, _, _, err := parsers.ParseImageName(key)
		assert.NilError(t, err)

		haveTags[repoName] = value
	}

	fakeOciClient := &ocitest.FakeClient{
		WantTags: haveTags,
	}

	updateScheduler := version.UpdateScheduler{
		Log:       log,
		Scheduler: scheduler,
		Scanner: version.Scanner{
			Log:        log,
			KubeClient: &kubetest.FakeDynamicClient{},
			OCIClient:  fakeOciClient,
			Namespace:  "test",
		},
		Updater: version.Updater{
			Log:        log,
			Repository: repository,
			Branch:     "main",
		},
		QuitChan:   quitChan,
		UpdateChan: make(chan version.AvailableUpdate, 10),
		ErrGroup:   schedulerEg,
	}

	jobCount, err := updateScheduler.Schedule(ctx, updateInstructions)
	if tc.wantErr != "" {
		assert.ErrorContains(t, err, tc.wantErr)
		return
	}
	assert.NilError(t, err)

	assert.Equal(t, jobCount, tc.wantJobCount)
	assert.Equal(t, len(scheduler.Jobs()), tc.wantJobCount)
	check := func(t poll.LogT) poll.Result {
		if len(repository.CommitsMade) != len(tc.wantCommits) {
			return poll.Continue("")
		}

		for _, wantCommit := range tc.wantCommits {
			if !slices.Contains(repository.CommitsMade, wantCommit) {
				return poll.Continue("missing commit")
			}
		}

		return poll.Success()
	}

	if jobCount != 0 {
		fakeClock.BlockUntil(1)
		fakeClock.Advance(1 * time.Second)
	}

	poll.WaitOn(t, check, poll.WithDelay(1*time.Second))

	if tc.wantJobCount != 0 {
		for _, repoName := range repoNames {
			assert.Assert(t, slices.Contains(fakeOciClient.ListTagCalls, repoName))
		}
	} else {
		assert.Equal(t, len(fakeOciClient.ListTagCalls), 0)
	}
}
