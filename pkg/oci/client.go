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

package oci

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/kharf/navecd/internal/tgz"
)

type UnrecoverableError struct {
	Err error
}

var _ error = (*UnrecoverableError)(nil)

func (d *UnrecoverableError) Error() string {
	return "Unrecoverable error"
}

func (d *UnrecoverableError) Unwrap() error {
	return d.Err
}

type RecoverableError struct {
	Err        error
	BackupPath string
}

var _ error = (*RecoverableError)(nil)

func (d *RecoverableError) Error() string {
	return "Recoverable error"
}

func (d *RecoverableError) Unwrap() error {
	return d.Err
}

const (
	ContentLayerMediaType = "application/vnd.navecd.content.v1.tar+gzip"
	ConfigMediaType       = "application/vnd.navecd.config.v1+json"
)

var (
	ErrWrongMediaType = errors.New("Wrong media type")
)

type basicAuthOpt struct {
	user     string
	password string
}

type options struct {
	auth     *basicAuthOpt
	insecure bool
}

type Option func(opts *options)

func WithBasicAuth(user, password string) Option {
	return func(opts *options) {
		opts.auth = &basicAuthOpt{
			user:     user,
			password: password,
		}
	}
}

func WithInsecure(insecure bool) Option {
	return func(opts *options) {
		opts.insecure = insecure
	}
}

type Client interface {
	ListTags(opts ...Option) ([]string, error)
	Image(tag string, opts ...Option) (v1.Image, error)
	PushImage(img v1.Image, tag string, path string, opts ...Option) (string, error)
}

func NewRepositoryClient(repoName string) (Client, error) {
	repository, err := name.NewRepository(repoName)
	if err != nil {
		return nil, err
	}

	return &repositoryClient{
		repo: repository,
	}, nil
}

type repositoryClient struct {
	repo name.Repository
}

func (d *repositoryClient) Image(tag string, opts ...Option) (v1.Image, error) {
	image, err := remote.Image(d.repo.Tag(tag), evalRemoteOpts(opts)...)
	if err != nil {
		return nil, err
	}

	return image, nil
}

func (d *repositoryClient) ListTags(opts ...Option) ([]string, error) {
	remoteVersions, err := remote.List(d.repo, evalRemoteOpts(opts)...)
	if err != nil {
		return nil, err
	}

	return remoteVersions, nil
}

func (d *repositoryClient) PushImage(img v1.Image, ref string, path string, opts ...Option) (string, error) {
	if err := crane.Push(img, fmt.Sprintf("%s:%s", d.repo.Name(), ref), evalCraneOpts(opts)...); err != nil {
		return "", err
	}

	digest, err := img.Digest()
	if err != nil {
		return "", err
	}

	return digest.String(), nil
}

var _ Client = (*repositoryClient)(nil)

func evalRemoteOpts(opts []Option) []remote.Option {
	options := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(options)
		}
	}

	var remoteOptions []remote.Option
	if options.auth != nil {
		remoteOptions = append(remoteOptions, remote.WithAuth(&authn.Basic{
			Username: options.auth.user,
			Password: options.auth.password,
		}))
	}

	return remoteOptions
}

func evalCraneOpts(opts []Option) []crane.Option {
	options := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(options)
		}
	}

	var craneOptions []crane.Option
	if options.auth != nil {
		craneOptions = append(craneOptions, crane.WithAuth(&authn.Basic{
			Username: options.auth.user,
			Password: options.auth.password,
		}))
	}

	if options.insecure {
		craneOptions = append(craneOptions, crane.Insecure)
	}

	return craneOptions
}

type projectClientOptions struct {
	cacheDir string
	repoOpts []Option
}

type ProjectClientOption func(opts *projectClientOptions)

func WithCacheDir(dir string) ProjectClientOption {
	return func(opts *projectClientOptions) {
		opts.cacheDir = dir
	}
}

func WithRepositoryOption(option Option) ProjectClientOption {
	return func(opts *projectClientOptions) {
		opts.repoOpts = append(opts.repoOpts, option)
	}
}

func NewProjectClient(ociClient Client) *ProjectClient {
	return &ProjectClient{
		Client: ociClient,
	}
}

type ProjectClient struct {
	Client
}

func (client *ProjectClient) PushImageFromPath(tag string, path string, opts ...ProjectClientOption) (string, error) {
	options := &projectClientOptions{}
	for _, opt := range opts {
		opt(options)
	}

	if options.cacheDir == "" {
		dir, err := os.MkdirTemp("", "navecd-*")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		options.cacheDir = dir
	}

	archive := filepath.Join(options.cacheDir, "navecd.tgz")
	if err := tgz.Create(path, archive); err != nil {
		return "", err
	}

	contentLayer, err := tarball.LayerFromFile(archive, tarball.WithMediaType(ContentLayerMediaType), tarball.WithCompressedCaching)
	if err != nil {
		return "", err
	}

	img := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, ConfigMediaType)

	img, err = mutate.Append(img, mutate.Addendum{Layer: contentLayer})
	if err != nil {
		return "", err
	}

	return client.PushImage(img, tag, path, options.repoOpts...)
}

func (client *ProjectClient) LoadImage(ctx context.Context, tag string, targetDir string, opts ...ProjectClientOption) (string, error) {
	options := &projectClientOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(options)
		}
	}

	image, err := client.Image(tag, options.repoOpts...)
	if err != nil {
		return "", err
	}

	imgMediaType, err := image.MediaType()
	if err != nil {
		return "", err
	}

	if imgMediaType != types.OCIManifestSchema1 {
		return "", fmt.Errorf("%w: got %s, wanted %s", ErrWrongMediaType, imgMediaType, types.OCIManifestSchema1)
	}

	manifest, err := image.Manifest()
	if err != nil {
		return "", err
	}

	if manifest.Config.MediaType != ConfigMediaType {
		return "", fmt.Errorf("%w: got %s, wanted %s", ErrWrongMediaType, manifest.Config.MediaType, ConfigMediaType)
	}

	imageDigest, err := image.Digest()
	if err != nil {
		return "", err
	}

	completionDir := filepath.Join(options.cacheDir, "completion")
	imageDigestStr := imageDigest.String()
	marker := filepath.Join(completionDir, fmt.Sprintf("%s%s", imageDigestStr, ".complete"))

	if _, err := os.Stat(marker); err == nil {
		return imageDigestStr, nil
	}

	err = prepareDirs(completionDir, targetDir)
	if err != nil {
		return "", err
	}

	targetDirBkp := fmt.Sprintf("%s-bkp", targetDir)
	err = createBackup(targetDir, targetDirBkp)
	if err != nil {
		return "", err
	}

	archiveDir := filepath.Join(options.cacheDir, imageDigestStr)
	archiveFilePath, _, err := downloadImage(image, archiveDir)
	if err != nil {
		return "", &RecoverableError{
			Err:        err,
			BackupPath: targetDirBkp,
		}
	}
	defer os.RemoveAll(archiveDir)

	err = unpack(archiveFilePath, targetDir)
	if err != nil {
		return "", &UnrecoverableError{
			Err: err,
		}
	}

	markerFile, err := os.Create(marker)
	if err != nil {
		return "", err
	}
	defer markerFile.Close()

	return imageDigestStr, nil
}

func prepareDirs(completionDir string, targetDir string) error {
	if err := os.RemoveAll(completionDir); err != nil {
		return err
	}

	if err := os.MkdirAll(completionDir, 0700); err != nil {
		return err
	}

	if err := os.MkdirAll(targetDir, 0700); err != nil {
		return err
	}
	return nil
}

func downloadImage(image v1.Image, targetDir string) (string, int64, error) {
	if err := os.MkdirAll(targetDir, 0700); err != nil {
		return "", 0, err
	}

	archiveFilePath := filepath.Join(targetDir, "navecd.tgz")
	layers, err := image.Layers()
	if err != nil {
		return "", 0, err
	}

	contentLayer := layers[0]

	mediaType, err := contentLayer.MediaType()
	if err != nil {
		return "", 0, err
	}

	if mediaType != ContentLayerMediaType {
		return "", 0, fmt.Errorf("%w: got %s, wanted %s", ErrWrongMediaType, mediaType, ContentLayerMediaType)
	}

	writer, err := os.Create(archiveFilePath)
	if err != nil {
		return "", 0, err
	}
	defer writer.Close()

	reader, err := contentLayer.Compressed()
	if err != nil {
		return "", 0, err
	}
	defer reader.Close()

	size, err := io.Copy(writer, bufio.NewReader(reader))
	if err != nil {
		return "", 0, err
	}
	return archiveFilePath, size, nil
}

func unpack(archiveFilePath string, targetDir string) error {
	if err := tgz.Read(archiveFilePath, targetDir); err != nil {
		return err
	}

	return nil
}

func createBackup(targetDir string, targetDirBkp string) error {
	if err := os.RemoveAll(targetDirBkp); err != nil {
		return err
	}

	if err := os.MkdirAll(targetDirBkp, 0700); err != nil {
		return err
	}

	if err := filepath.Walk(targetDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(targetDir, path)
		if err != nil {
			return err
		}

		target := filepath.Join(targetDirBkp, relPath)
		if info.IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			return nil
		}

		file, err := os.Create(target)
		if err != nil {
			return err
		}
		defer file.Close()

		return nil
	}); err != nil {
		return err
	}
	return nil
}
