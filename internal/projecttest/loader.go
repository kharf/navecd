package projecttest

import (
	"context"
	"github.com/kharf/navecd/pkg/cloud"
	"github.com/kharf/navecd/pkg/project"
)

type FakeRemoteLoader struct {
	Err    error
	Digest string
}

var _ project.RemoteLoader = (*FakeRemoteLoader)(nil)

func (f *FakeRemoteLoader) Load(ctx context.Context, targetDir string, auth *cloud.Auth) (project.Digest, error) {
	if f.Err != nil {
		return "", f.Err
	}

	return project.Digest(f.Digest), nil
}
