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

package testtemplates

import (
	"bytes"
	"text/template"
)

// when changed, the renovate customManager has also to be updated.
const ModuleVersion = "v0.11.1"

type Template interface {
	Template() string
	Data() any
}

func Parse(tmpl Template) ([]byte, error) {
	parsedTemplate, err := template.New("").Parse(tmpl.Template())
	if err != nil {
		return nil, err
	}

	buf := &bytes.Buffer{}
	err = parsedTemplate.Execute(buf, tmpl.Data())
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
