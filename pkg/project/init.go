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

package project

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"

	"cuelang.org/go/mod/modfile"
	"github.com/kharf/navecd/internal/manifest"
)

const (
	ControllerNamespace = "navecd-system"
	controllerName      = "project-controller"
)

func Init(
	module string,
	shard string,
	isSecondary bool,
	path string,
	version string,
) error {
	moduleDir := filepath.Join(path, "cue.mod")
	_, err := os.Stat(moduleDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if os.IsNotExist(err) {
		moduleFile := modfile.File{
			Module: module,
			Language: &modfile.Language{
				Version: "v0.13.0",
			},
			Deps: map[string]*modfile.Dep{
				"github.com/kharf/navecd/schema@v0": {
					Version: "v" + version,
				},
			},
		}

		content, err := moduleFile.Format()
		if err != nil {
			return err
		}

		if _, err := modfile.Parse(content, "module.cue"); err != nil {
			return err
		}

		if err := os.MkdirAll(moduleDir, 0755); err != nil {
			return err
		}

		if err := os.WriteFile(filepath.Join(moduleDir, "module.cue"), content, 0666); err != nil {
			return err
		}
	}

	navecdDir := filepath.Join(path, "navecd")
	_, err = os.Stat(navecdDir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if os.IsNotExist(err) {
		if err := os.MkdirAll(navecdDir, 0700); err != nil {
			return err
		}
	}

	if !isSecondary {
		primaryFile := filepath.Join(navecdDir, "primary.cue")

		_, err = os.Stat(primaryFile)
		if err != nil && !os.IsNotExist(err) {
			return err
		}

		if os.IsNotExist(err) {
			tmpl, err := template.New("").Parse(manifest.Primary)
			if err != nil {
				return err
			}

			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, map[string]string{
				"Name":  getControllerName(shard),
				"Shard": shard,
			}); err != nil {
				return err
			}

			if err := os.WriteFile(primaryFile, buf.Bytes(), 0666); err != nil {
				return err
			}
		}

		crdFile := filepath.Join(navecdDir, "crd.cue")

		if err := os.WriteFile(crdFile, []byte(manifest.CRD), 0666); err != nil {
			return err
		}
	}

	shardSystemFile := filepath.Join(navecdDir, fmt.Sprintf("%s_system.cue", shard))
	_, err = os.Stat(shardSystemFile)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	if os.IsNotExist(err) {
		tmpl, err := template.New("").Parse(manifest.System)
		if err != nil {
			return err
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, map[string]string{
			"Name":    getControllerName(shard),
			"Shard":   shard,
			"Version": version,
		}); err != nil {
			return err
		}

		if err := os.WriteFile(shardSystemFile, buf.Bytes(), 0666); err != nil {
			return err
		}
	}

	return nil
}

func getControllerName(shard string) string {
	return fmt.Sprintf("%s-%s", controllerName, shard)
}
