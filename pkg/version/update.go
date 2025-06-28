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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-logr/logr"
	"golang.org/x/sync/errgroup"

	"github.com/kharf/navecd/pkg/cloud"
	"github.com/kharf/navecd/pkg/helm"
	"github.com/kharf/navecd/pkg/vcs"
)

// UpdateIntegration defines the method on how to push updates to the version control system.
type UpdateIntegration int

const (
	// PR indicates to push updates to a separate update branch and create a pull request. Updates are not applied immediately, only after the PR has been merged and the changes were pulled.
	PR UpdateIntegration = iota
	// Direct indicates to push updates directly to the base branch and reconcile them in the same run.
	Direct
)

var (
	// ErrUnexpectedResponse is returned when an unexpected response is received from a repository.
	ErrUnexpectedResponse = errors.New("Unexpected response")
	// ErrChartNotFound is returned when a chart is not found in the repository.
	ErrChartNotFound = errors.New("Chart not found")
)

// ContainerUpdateTarget defines the container image to be updated.
type ContainerUpdateTarget struct {
	// Image value of the 'tagged' field.
	// It has the format 'repository:tag@digest'.
	Image string

	// Reference to the struct holding repository and version fields.
	UnstructuredNode map[string]any

	// Field key or label of the version field.
	UnstructuredKey string
}

func (c *ContainerUpdateTarget) Name() string {
	imageParts := strings.Split(c.Image, "@")
	lidx := strings.LastIndex(imageParts[0], ":")
	if lidx == -1 {
		return imageParts[0]
	}
	return c.Image[:lidx]
}

func (c *ContainerUpdateTarget) GetStructValue() string {
	return c.UnstructuredNode[c.UnstructuredKey].(string)
}

var _ UpdateTarget = (*ContainerUpdateTarget)(nil)

// ChartUpdateTarget defines the helm chart to be updated.
type ChartUpdateTarget struct {
	Chart *helm.Chart
}

func (c *ChartUpdateTarget) Name() string {
	return c.Chart.Name
}

func (c *ChartUpdateTarget) GetStructValue() string {
	return c.Chart.Version
}

var _ UpdateTarget = (*ChartUpdateTarget)(nil)

// Object to be updated.
type UpdateTarget interface {
	// Name returns the name of the update target.
	// It is either a container name or a helm chart.
	Name() string
	// GetStructValue retrieves the current value of the struct field.
	// It is either an image field in an unstructured manifest or a version field in a helm chart.
	GetStructValue() string
}

// UpdateInstruction represents the instruction for updating a target, such as a container image or a Helm chart.
type UpdateInstruction struct {
	// Strategy defines the method to update the target.
	Strategy UpdateStrategy
	// Constraint specifies any constraints that need to be considered during the update process.
	Constraint string
	// Auth contains authentication details required for accessing and updating the target.
	// Only relevant for manifest components. For Helm Charts, auth is taken from the component def.
	Auth *cloud.Auth

	// Integration defines the method on how to push updates to the version control system.
	Integration UpdateIntegration

	// Schedule is a string in cron format with an additional seconds field and defines when the target is scanned for updates.
	Schedule string

	// File is a relative path to the file where the version value is located.
	File string
	// Line number in the file where the version value resides.
	Line int

	// Target specifies what needs to be updated, which can be a container image or a Helm chart.
	// A container image follows the format 'repository:tag@digest'.
	// A Helm repository can either be of type 'oci' or 'https'.
	Target UpdateTarget
}

// Update represents the result of an update operation.
type Update struct {
	// CommitHash contains the SHA1 of the commit.
	CommitHash string

	// NewVersion contains the updated version.
	NewVersion string

	// IsPR tells whether this update is a direct commit or a pull request.
	IsPR bool
}

func UpdateCommitMessage(targetName, newVersion string) string {
	return fmt.Sprintf(
		"chore(update): bump %s to %s",
		targetName,
		newVersion,
	)
}

// Updater accepts update information that tell which images to update.
// It pushes its changes to remote before returning.
type Updater struct {
	Log logr.Logger
}

func (updater *Updater) ListenForUpdates(
	ctx context.Context,
) (chan AvailableUpdate, chan struct{}) {
	eg := &errgroup.Group{}
	eg.SetLimit(1)
	quitChan := make(chan struct{}, 1)
	updateChan := make(chan AvailableUpdate, 50)
	_ = eg.TryGo(func() error {
		for {
			select {
			case availableUpdate := <-updateChan:
				updater := Updater{
					Log: updater.Log,
				}

				_, err := availableUpdate.Repository.Pull()
				if err != nil {
					updater.Log.Error(
						err,
						"Unable to pull gitops project repository for update",
					)

					continue
				}

				_, err = updater.Update(ctx, availableUpdate)
				if err != nil {
					updater.Log.Error(
						err,
						"Unable to update version",
						"target",
						availableUpdate.Target.Name(),
						"newVersion",
						availableUpdate.ImageScan.NewVersion,
						"file",
						availableUpdate.File,
					)
				}

			case <-quitChan:
				return nil
			}
		}
	})

	return updateChan, quitChan
}

// Update accepts available updates that tell which images or chart to update and returns update results.
// The update result can be nil in case a PR for the update currently already exists.
func (updater *Updater) Update(
	ctx context.Context,
	availableUpdate AvailableUpdate,
) (*Update, error) {
	if availableUpdate.ImageScan.CurrentVersion == availableUpdate.ImageScan.NewVersion {
		return nil, nil
	}

	targetName := availableUpdate.Target.Name()

	commitMessage := UpdateCommitMessage(targetName, availableUpdate.ImageScan.NewVersion)

	log := updater.Log.WithValues(
		"target",
		targetName,
		"newVersion",
		availableUpdate.ImageScan.NewVersion,
		"file",
		availableUpdate.File,
	)

	var update *Update
	switch availableUpdate.Integration {
	case PR:
		log.V(1).Info(
			"Creating Update-PullRequest",
		)

		var err error
		update, err = updater.createPR(targetName, commitMessage, availableUpdate)
		if err != nil &&
			!errors.Is(err, vcs.ErrPRAlreadyExists) {
			log.Error(err, "Unable to create Update-PullRequest")
			return nil, err
		}

	case Direct:
		log.V(1).Info(
			"Updating",
		)

		update, err := updater.update(commitMessage, availableUpdate)
		if err != nil && !errors.Is(err, git.ErrEmptyCommit) {
			log.Error(err, "Unable to update")
			return nil, err
		}

		if errors.Is(err, git.ErrEmptyCommit) {
			return nil, nil
		}

		if err := availableUpdate.Repository.Push(availableUpdate.Branch, availableUpdate.Branch); err != nil &&
			!errors.Is(err, git.NoErrAlreadyUpToDate) {
			updater.Log.Error(err, "Unable to push updates")
		}

		return update, nil
	}

	if err := availableUpdate.Repository.SwitchBranch(availableUpdate.Branch, false); err != nil {
		// return error, because we can't proceed with updates on the wrong branch.
		return nil, err
	}

	return update, nil
}

func (updater *Updater) createPR(
	targetName string,
	commitMessage string,
	availableUpdate AvailableUpdate,
) (*Update, error) {
	srcBranch := fmt.Sprintf("navecd/update-%s", targetName)
	if err := availableUpdate.Repository.SwitchBranch(srcBranch, true); err != nil {
		return nil, err
	}

	defer func() {
		if err := availableUpdate.Repository.DeleteLocalBranch(srcBranch); err != nil {
			updater.Log.Error(err, "Unable to delete local branch", "branch", srcBranch)
		}
	}()

	update, err := updater.update(commitMessage, availableUpdate)
	if err != nil && !errors.Is(err, git.ErrEmptyCommit) {
		return nil, err
	}

	if errors.Is(err, git.ErrEmptyCommit) {
		return nil, nil
	}

	if err := availableUpdate.Repository.Push(srcBranch, srcBranch); err != nil &&
		!errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil, err
	}

	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil, nil
	}

	if err := availableUpdate.Repository.CreatePullRequest(commitMessage, availableUpdate.ImageScan.URL, srcBranch, availableUpdate.Branch); err != nil {
		return nil, err
	}

	update.IsPR = true
	return update, nil
}

func (updater *Updater) update(
	commitMessage string,
	availableUpdate AvailableUpdate,
) (*Update, error) {
	if err := updater.updateVersion(availableUpdate); err != nil {
		return nil, err
	}

	hash, err := availableUpdate.Repository.Commit(availableUpdate.File,
		commitMessage,
	)
	if err != nil {
		return nil, err
	}

	return &Update{
		CommitHash: hash,
		NewVersion: availableUpdate.ImageScan.NewVersion,
	}, nil
}

func (updater *Updater) updateVersion(
	availableUpdate AvailableUpdate,
) error {
	dstFilePath := filepath.Join(availableUpdate.Repository.Path(), availableUpdate.File)
	file, err := os.Open(dstFilePath)
	if err != nil {
		return err
	}
	defer file.Close()

	newFile, err := os.CreateTemp("", "update-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(newFile.Name())
	}()

	scanner := bufio.NewScanner(file)
	writer := bufio.NewWriter(newFile)

	currLineNumber := 1
	for scanner.Scan() {
		var currLine string
		if currLineNumber == availableUpdate.Line {
			newValue := strings.Replace(
				availableUpdate.Target.GetStructValue(),
				availableUpdate.ImageScan.CurrentVersion,
				availableUpdate.ImageScan.NewVersion,
				1,
			)
			currLine = strings.Replace(
				scanner.Text(),
				availableUpdate.Target.GetStructValue(),
				newValue,
				1,
			)
		} else {
			currLine = scanner.Text()
		}

		_, err = writer.WriteString(currLine + "\n")
		if err != nil {
			return err
		}

		currLineNumber++
	}
	if err := writer.Flush(); err != nil {
		return err
	}

	if err := newFile.Close(); err != nil {
		return err
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	if err := overwriteFile(newFile.Name(), dstFilePath); err != nil {
		return err
	}

	return nil
}

func overwriteFile(src string, dst string) error {
	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	return nil
}
