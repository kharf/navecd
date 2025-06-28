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

type project struct {
	uid         string
	haveImages  []image
	wantCommits []string
}

type scheduleTestCase struct {
	name         string
	projects     []project
	haveTags     map[string][]string
	wantJobCount int
}

var (
	newJobs = scheduleTestCase{
		name: "New-Jobs",
		haveTags: map[string][]string{
			"myimage":  {"1.16.5"},
			"myimage2": {"1.17.5"},
		},
		projects: []project{
			{
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
			},
		},
		wantJobCount: 2,
	}

	missingSchedule = scheduleTestCase{
		name: "Missing-Schedule",
		haveTags: map[string][]string{
			"myimage":  {"1.16.5"},
			"myimage2": {"1.17.5"},
		},
		projects: []project{
			{
				haveImages: []image{
					{
						name:       "myimage:1.15.0",
						schedule:   "",
						constraint: "1.16.5",
					},
				},
			},
		},
		wantJobCount: 0,
	}

	multipleProjects = scheduleTestCase{
		name: "MultipleProjects",
		haveTags: map[string][]string{
			"myimage":  {"1.16.5"},
			"myimage2": {"1.17.5"},
			"myimage3": {"1.16.5"},
			"myimage4": {"1.17.5"},
		},
		projects: []project{
			{
				uid: "a",
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
			},
			{
				uid: "b",
				haveImages: []image{
					{
						name:       "myimage3:1.15.0",
						schedule:   "* * * * * *",
						constraint: "1.16.5",
					},
					{
						name:       "myimage4:1.16.0",
						schedule:   "* * * * * *",
						constraint: "1.17.5",
					},
				},
				wantCommits: []string{
					"chore(update): bump myimage3 to 1.16.5",
					"chore(update): bump myimage4 to 1.17.5",
				},
			},
		},
		wantJobCount: 4,
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
		multipleProjects,
	}

	logOpts := zap.Options{
		Development: true,
		Level:       zapcore.Level(-1),
	}
	log := zap.New(zap.UseFlagOptions(&logOpts))

	fakeClock := clockwork.NewFakeClock()
	scheduler, quitChan, err := version.NewUpdateScheduler(log, gocron.WithClock(fakeClock))
	assert.NilError(t, err)
	scheduler.Start()
	defer func() {
		scheduler.Shutdown()
		quitChan <- struct{}{}
	}()

	dnsServer, err := dnstest.NewDNSServer()
	assert.NilError(t, err)
	defer dnsServer.Close()

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checks := []func(t poll.LogT) poll.Result{}
			var repoNames []string

			haveTags := make(map[string][]string, len(tc.haveTags))

			for key, value := range tc.haveTags {
				repoName, _, _, err := parsers.ParseImageName(key)
				assert.NilError(t, err)

				haveTags[repoName] = value
			}

			fakeOciClient := &ocitest.FakeClient{
				WantTags: haveTags,
			}

			for _, project := range tc.projects {
				projectDir := t.TempDir()

				updateInstructions := make([]version.UpdateInstruction, 0, len(project.haveImages))
				sb := strings.Builder{}
				sb.Write([]byte("-- apps/myapp.cue --\n"))
				for i, image := range project.haveImages {
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

				repository := &gittest.FakeRepository{
					RepoPath: projectDir,
				}

				request := version.ScheduleRequest{
					ProjectUID: project.uid,
					Scanner: version.Scanner{
						Log:        log,
						KubeClient: &kubetest.FakeDynamicClient{},
						OCIClient:  fakeOciClient,
						Namespace:  "test",
					},
					Repository:   repository,
					Branch:       "main",
					Instructions: updateInstructions,
				}

				_, err := scheduler.Schedule(ctx, request)
				assert.NilError(t, err)

				check := func(t poll.LogT) poll.Result {
					commitsMade := len(repository.CommitsMade)
					wantCommits := len(project.wantCommits)
					if commitsMade != wantCommits {
						return poll.Continue(
							fmt.Sprintf(
								"have %v commits, want %v commits for project %s",
								commitsMade,
								wantCommits,
								project.uid,
							),
						)
					}

					for _, wantCommit := range project.wantCommits {
						if !slices.Contains(repository.CommitsMade, wantCommit) {
							return poll.Continue("missing commit")
						}
					}

					return poll.Success()
				}

				checks = append(checks, check)
			}

			assert.Equal(t, len(scheduler.Jobs()), tc.wantJobCount)
			if len(scheduler.Jobs()) != 0 {
				fakeClock.BlockUntilContext(ctx, 1)
				fakeClock.Advance(1 * time.Second)
			}

			for _, check := range checks {
				poll.WaitOn(t, check, poll.WithDelay(1*time.Second))
			}

			if tc.wantJobCount != 0 {
				for _, repoName := range repoNames {
					assert.Assert(t, slices.Contains(fakeOciClient.ListTagCalls, repoName))
				}
			} else {
				assert.Equal(t, len(fakeOciClient.ListTagCalls), 0)
			}
		})
	}
}
