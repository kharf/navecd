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
	"strconv"
	"strings"

	"github.com/go-co-op/gocron/v2"
	"github.com/go-logr/logr"
	"github.com/kharf/navecd/pkg/vcs"
)

// AvailableUpdate represents the result of a positive version scanning operation.
// It holds details about the current and new version, as well as the file and line at which these versions were found and the desired update integration method.
type AvailableUpdate struct {
	Repository vcs.Repository
	Branch     string
	ImageScan  ImageScan

	// Integration defines the method on how to push updates to the version control system.
	Integration UpdateIntegration

	// File where the versions were found.
	File string
	// Line number within the file where the versions were found.
	Line   int
	Target UpdateTarget
}

// UpdateScheduler runs background tasks periodically to update Container or Helm Charts.
type UpdateScheduler struct {
	gocron.Scheduler
	log        logr.Logger
	updateChan chan AvailableUpdate
}

func NewUpdateScheduler(
	log logr.Logger,
	options ...gocron.SchedulerOption,
) (updateScheduler *UpdateScheduler, quitChan chan struct{}, err error) {
	scheduler, err := gocron.NewScheduler(options...)
	if err != nil {
		return nil, nil, err
	}

	updater := Updater{
		Log: log,
	}

	updateChan, quitChan := updater.ListenForUpdates(context.Background())

	updateScheduler = &UpdateScheduler{
		log:        log,
		Scheduler:  scheduler,
		updateChan: updateChan,
	}

	return
}

type ScheduleRequest struct {
	ProjectUID   string
	Scanner      Scanner
	Repository   vcs.Repository
	Branch       string
	Instructions []UpdateInstruction
}

func (scheduler *UpdateScheduler) Schedule(
	ctx context.Context,
	request ScheduleRequest,
) (int, error) {
	for _, job := range scheduler.Jobs() {
		if strings.HasPrefix(job.Name(), fmt.Sprintf("%s-", request.ProjectUID)) &&
			!haveJobForInstructions(job, request.ProjectUID, request.Instructions) {
			scheduler.log.V(1).Info("Removing cron job", "name", job.Name())
			if err := scheduler.RemoveJob(job.ID()); err != nil {
				scheduler.log.Error(err, "Unable to remove job", "name", job.Name())
			}
		}
	}

	for _, instruction := range request.Instructions {
		cronJob := gocron.CronJob(instruction.Schedule, true)
		task := gocron.NewTask(
			scheduler.scan,
			ctx,
			instruction,
			request.Scanner,
			request.Repository,
			request.Branch,
			scheduler.updateChan,
		)

		if err := scheduler.upsertJob(request.ProjectUID, instruction, cronJob, task); err != nil {
			scheduler.log.Error(err, "Unable to upsert job", "name", instruction.Target.Name())
		}
	}

	return len(scheduler.Jobs()), nil
}

func (scheduler *UpdateScheduler) upsertJob(
	projectUID string,
	instruction UpdateInstruction,
	cronJob gocron.JobDefinition,
	task gocron.Task,
) error {
	log := scheduler.log.V(1).WithValues(
		"project",
		projectUID,
		"name",
		instruction.Target.Name(),
		"schedule",
		instruction.Schedule,
	)

	scheduleTag := keyValueTag("schedule", instruction.Schedule)
	fileTag := keyValueTag("file", instruction.File)
	lineTag := keyValueTag("line", strconv.Itoa(instruction.Line))

	identifiers := []gocron.JobOption{
		gocron.WithName(jobName(projectUID, instruction)),
		gocron.WithTags(
			scheduleTag, fileTag, lineTag,
		),
	}

	for _, job := range scheduler.Jobs() {
		if job.Name() == jobName(projectUID, instruction) {
			matchedFile := false
			matchedLine := false

			for _, tag := range job.Tags() {
				if tag == fileTag {
					matchedFile = true
				}

				if tag == lineTag {
					matchedLine = true
				}
			}

			if matchedFile && matchedLine {
				log.Info("Updating cron job")
				if _, err := scheduler.Update(
					job.ID(),
					cronJob,
					task,
					identifiers...,
				); err != nil {
					return err
				}

				return nil
			}
		}
	}

	log.Info("Adding cron job")

	_, err := scheduler.NewJob(
		cronJob,
		task,
		identifiers...,
	)

	return err
}

func keyValueTag(key, value string) string {
	return fmt.Sprintf("%s:%s", key, value)
}

func haveJobForInstructions(
	job gocron.Job,
	projectUID string,
	updateInstructions []UpdateInstruction,
) bool {
	for _, instruction := range updateInstructions {
		fileTag := keyValueTag("file", instruction.File)
		lineTag := keyValueTag("line", strconv.Itoa(instruction.Line))

		if job.Name() == jobName(projectUID, instruction) {
			matchedFile := false
			matchedLine := false

			for _, tag := range job.Tags() {
				if tag == fileTag {
					matchedFile = true
				}

				if tag == lineTag {
					matchedLine = true
				}
			}

			if matchedFile && matchedLine {
				return true
			}
		}
	}

	return false
}

func (scheduler *UpdateScheduler) scan(
	ctx context.Context,
	instruction UpdateInstruction,
	scanner Scanner,
	repository vcs.Repository,
	branch string,
	updateChan chan<- AvailableUpdate,
) {
	log := scheduler.log.V(1).WithValues("target", instruction.Target.Name())
	log.Info("Scanning for version updates")

	imageScan, hasUpdate, err := scanner.Scan(ctx, instruction)
	if err != nil {
		log.Error(
			err,
			"Unable to scan for version updates",
		)
	}

	if hasUpdate {
		updateChan <- AvailableUpdate{
			Repository:  repository,
			Branch:      branch,
			ImageScan:   *imageScan,
			Integration: instruction.Integration,
			File:        instruction.File,
			Line:        instruction.Line,
			Target:      instruction.Target,
		}
	}
}

func jobName(projectUID string, instruction UpdateInstruction) string {
	return fmt.Sprintf("%s-%s", projectUID, instruction.Target.Name())
}
