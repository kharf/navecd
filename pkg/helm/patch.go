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

package helm

import (
	"bytes"
	"io"
	"strings"

	"github.com/kharf/navecd/pkg/kube"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v4/pkg/postrenderer"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Patches allow to overwrite rendered manifests before installing/upgrading.
// Additionally they can be used to attach build attributes to fields.
type Patches struct {
	Unstructureds map[string]kube.ExtendedUnstructured
}

func NewPatches() *Patches {
	return &Patches{
		Unstructureds: map[string]kube.ExtendedUnstructured{},
	}
}

func (p *Patches) Put(unstructured kube.ExtendedUnstructured) {
	var namespace string
	if unstructured.GetNamespace() == "" {
		namespace = "default"
	} else {
		namespace = unstructured.GetNamespace()
	}

	sb := strings.Builder{}
	sb.WriteString(unstructured.GetAPIVersion())
	sb.WriteString("-")
	sb.WriteString(unstructured.GetKind())
	sb.WriteString("-")
	sb.WriteString(namespace)
	sb.WriteString("-")
	sb.WriteString(unstructured.GetName())

	p.Unstructureds[sb.String()] = unstructured
}

func (p *Patches) Get(
	name string,
	namespace string,
	typeMeta v1.TypeMeta,
) *kube.ExtendedUnstructured {
	if namespace == "" {
		namespace = "default"
	}

	sb := strings.Builder{}
	sb.WriteString(typeMeta.APIVersion)
	sb.WriteString("-")
	sb.WriteString(typeMeta.Kind)
	sb.WriteString("-")
	sb.WriteString(namespace)
	sb.WriteString("-")
	sb.WriteString(name)

	unstr, found := p.Unstructureds[sb.String()]
	if !found {
		return nil
	}

	return &unstr
}

type PostRenderer struct {
	Patches   *Patches
	JsonPaths []string
}

func (pr *PostRenderer) Run(
	renderedManifests *bytes.Buffer,
) (modifiedManifests *bytes.Buffer, err error) {
	dec := yaml.NewDecoder(renderedManifests)
	modifiedManifests = &bytes.Buffer{}
	enc := yaml.NewEncoder(modifiedManifests)

	for {
		var renderedUnstrObj map[string]any
		if err := dec.Decode(&renderedUnstrObj); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		renderedunstr := unstructured.Unstructured{
			Object: renderedUnstrObj,
		}

		patchedExtendedUnstr := pr.Patches.Get(
			renderedunstr.GetName(),
			renderedunstr.GetNamespace(),
			v1.TypeMeta{
				APIVersion: renderedunstr.GetAPIVersion(),
				Kind:       renderedunstr.GetKind(),
			},
		)

		if patchedExtendedUnstr != nil {
			mergeMaps(renderedUnstrObj, patchedExtendedUnstr.Object)
			for _, jsonPath := range pr.JsonPaths {
				if err := kube.RemoveIgnoredFields(jsonPath, renderedUnstrObj, *patchedExtendedUnstr.Metadata); err != nil {
					return nil, err
				}
			}
		}

		if err := enc.Encode(renderedUnstrObj); err != nil {
			return nil, err
		}
	}

	return
}

var _ postrenderer.PostRenderer = (*PostRenderer)(nil)

func mergeMaps(dst map[string]any, src map[string]any) {
	for srcKey, srcValue := range src {
		dstValue, dstKeyFound := dst[srcKey]
		if srcValueMap, ok := srcValue.(map[string]any); ok && dstKeyFound {
			if dstValueMap, ok := dstValue.(map[string]any); ok {
				mergeMaps(dstValueMap, srcValueMap)
			} else {
				dst[srcKey] = srcValue
			}
		} else {
			dst[srcKey] = srcValue
		}
	}
}
