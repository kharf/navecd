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

package txtar

import (
	"io"
	"os"
	"path/filepath"

	"golang.org/x/tools/txtar"
)

func Create(rootDir string, txtarData io.Reader) (*txtar.Archive, error) {
	arch, err := Archive(txtarData)
	if err != nil {
		return nil, err
	}

	err = create(rootDir, arch)
	if err != nil {
		return nil, err
	}

	return arch, nil
}

func CreateFromArchive(rootDir string, archive *txtar.Archive) error {
	return create(rootDir, archive)
}

func create(rootDir string, arch *txtar.Archive) error {
	for _, file := range arch.Files {
		absFilePath := filepath.Join(rootDir, file.Name)
		parentDir := filepath.Dir(absFilePath)

		err := os.MkdirAll(parentDir, 0700)
		if err != nil {
			return err
		}

		err = os.WriteFile(absFilePath, file.Data, 0666)
		if err != nil {
			return err
		}
	}
	return nil
}

func Archive(txtarData io.Reader) (*txtar.Archive, error) {
	bytes, err := io.ReadAll(txtarData)
	if err != nil {
		return nil, err
	}

	arch := txtar.Parse(bytes)
	return arch, nil
}
