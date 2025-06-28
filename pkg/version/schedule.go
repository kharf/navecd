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

	"github.com/go-co-op/gocron/v2"
	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"
)

// UpdateScheduler runs background tasks periodically to update Container or Helm Charts.
type UpdateScheduler struct {
	Log logr.Logger

	Scanner Scanner
	Updater Updater

	Scheduler gocron.Scheduler

	ErrGroup   *errgroup.Group
	QuitChan   chan struct{}
	UpdateChan chan AvailableUpdate
}

func (scheduler *UpdateScheduler) Schedule(
	ctx context.Context,
	projectUID string,
	updateInstructions []UpdateInstruction,
) (int, error) {
	for _, job := range scheduler.Scheduler.Jobs() {
		if !haveJobForInstructions(job, projectUID, updateInstructions) {
			scheduler.Log.V(1).Info("Removing cron job", "name", job.Name())
			if err := scheduler.Scheduler.RemoveJob(job.ID()); err != nil {
				scheduler.Log.Error(err, "Unable to remove job", "name", job.Name())
			}
		}
	}

	for _, instruction := range updateInstructions {
		cronJob := gocron.CronJob(instruction.Schedule, true)
		task := gocron.NewTask(
			scheduler.scan,
			ctx,
			instruction,
			scheduler.UpdateChan,
		)

		if err := scheduler.upsertJob(projectUID, instruction, cronJob, task); err != nil {
			scheduler.Log.Error(err, "Unable to upsert job", "name", instruction.Target.Name())
		}
	}

	_ = scheduler.ErrGroup.TryGo(func() error {
		for {
			select {
			case availableUpdate := <-scheduler.UpdateChan:
				_, err := scheduler.Updater.Repository.Pull()
				if err != nil {
					scheduler.Log.Error(
						err,
						"Unable to pull gitops project repository for update",
					)

					continue
				}

				_, err = scheduler.Updater.Update(ctx, availableUpdate)
				if err != nil {
					scheduler.Log.Error(
						err,
						"Unable to update version",
						"target",
						availableUpdate.Target.Name(),
						"newVersion",
						availableUpdate.NewVersion,
						"file",
						availableUpdate.File,
					)
				}

			case <-scheduler.QuitChan:
				return nil
			}
		}
	})

	return len(scheduler.Scheduler.Jobs()), nil
}

func (scheduler *UpdateScheduler) upsertJob(
	projectUID string,
	instruction UpdateInstruction,
	cronJob gocron.JobDefinition,
	task gocron.Task,
) error {
	log := scheduler.Log.V(1).WithValues(
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
		gocron.WithName(instruction.Target.Name()),
		gocron.WithTags(
			scheduleTag, fileTag, lineTag,
		),
	}

	for _, job := range scheduler.Scheduler.Jobs() {
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
				if _, err := scheduler.Scheduler.Update(
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

	_, err := scheduler.Scheduler.NewJob(
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
	updateChan chan<- AvailableUpdate,
) {
	log := scheduler.Log.V(1).WithValues("target", instruction.Target.Name())
	log.Info("Scanning for version updates")

	availableUpdate, hasUpdate, err := scheduler.Scanner.Scan(ctx, instruction)
	if err != nil {
		log.Error(
			err,
			"Unable to scan for version updates",
		)
	}

	if hasUpdate {
		updateChan <- *availableUpdate
	}
}

func jobName(projectUID string, instruction UpdateInstruction) string {
	return fmt.Sprintf("%s-%s", projectUID, instruction.Target.Name())
}
