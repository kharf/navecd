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

package kube

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type Difference struct {
	GVK   schema.GroupVersionKind
	Lines []DiffLine
}

func (d *Difference) String() string {
	sb := strings.Builder{}

	for _, diff := range d.Lines {
		var value string
		if diff.Value == "" {
			value = "\n"
		} else {
			value = fmt.Sprintf(" %s\n", diff.Value)
		}
		sb.WriteString(
			fmt.Sprintf(
				"%s%s%s:%s",
				GetMarker(diff.DiffType),
				diff.Indentation,
				diff.Node,
				value,
			),
		)
	}

	return sb.String()
}

type diffType string

const (
	AddDiff    diffType = "add"
	DeleteDiff diffType = "delete"
	UpdateDiff diffType = "update"
	NoDiff     diffType = "noDiff"
)

type DiffLine struct {
	Node        string
	Value       string
	DiffType    diffType
	Indentation string
}

type Differ struct {
	KubeClient   DynamicClient
	FieldManager string
}

func (differ *Differ) Diff(
	ctx context.Context,
	target *unstructured.Unstructured,
) (*Difference, error) {
	actual, err := differ.KubeClient.Get(ctx, target)
	if err != nil {
		switch k8sErrors.ReasonForError(err) {
		case v1.StatusReasonNotFound:
		default:
			return nil, err
		}
	}

	merged, err := differ.KubeClient.Apply(ctx, target, differ.FieldManager, DryRunApply(true))
	if err != nil {
		switch k8sErrors.ReasonForError(err) {
		case v1.StatusReasonConflict:
		default:
			return nil, err
		}
	}

	fmt.Println("reference")
	buf := bytes.Buffer{}
	if err := yaml.NewEncoder(&buf).Encode(merged); err != nil {
		return nil, err
	}
	fmt.Println(buf.String())

	diffLines := compare(actual.Object, merged.Object, "")

	return &Difference{
		GVK:   target.GroupVersionKind(),
		Lines: diffLines,
	}, nil
}

func compare(actual, target map[string]any, indentation string) []DiffLine {
	matchedKeys := map[string]struct{}{}
	keys := sortMapKeys(actual)

	diffLines := make([]DiffLine, 0)

	for _, key := range keys {
		value := actual[key]

		if targetValue, found := target[key]; found {
			if node, ok := value.(map[string]any); ok {
				if targetNode, ok := targetValue.(map[string]any); ok {
					matchedKeys[key] = struct{}{}
					diffLines = append(diffLines, DiffLine{
						Node:        key,
						Indentation: indentation,
						DiffType:    NoDiff,
						Value:       "",
					})
					diffLines = append(diffLines, compare(node, targetNode, indentation+"  ")...)
				} else {
					diffLines = append(diffLines, printNode(targetNode, indentation, DeleteDiff, false)...)
				}
			} else {
				if targetNode, ok := targetValue.(map[string]any); !ok {
					matchedKeys[key] = struct{}{}
					switch real := targetValue.(type) {
					case []any:
						diffLines = append(diffLines, DiffLine{
							Node:        key,
							Indentation: indentation,
							DiffType:    NoDiff,
							Value:       "",
						})
						diffLines = append(diffLines, printSlice(real, indentation+"  ", NoDiff)...)
					default:
						diffLines = append(diffLines, DiffLine{
							Node:        key,
							Indentation: indentation,
							DiffType:    NoDiff,
							Value:       fmt.Sprintf("%v", real),
						})
					}
				} else {
					diffLines = append(diffLines, printNode(targetNode, indentation, UpdateDiff, false)...)
				}
			}
		}
	}

	targetKeys := sortMapKeys(target)
	for _, key := range targetKeys {
		value := target[key]

		if _, ok := matchedKeys[key]; !ok {
			if node, ok := value.(map[string]any); ok {
				diffLines = append(diffLines, printNode(node, indentation, AddDiff, false)...)
			} else {
				diffLines = append(diffLines, DiffLine{
					Node:        key,
					Indentation: indentation,
					DiffType:    AddDiff,
					Value:       fmt.Sprintf("%v", value),
				})
			}
		}
	}

	return diffLines
}

func printSlice(slice []any, indentation string, diffType diffType) []DiffLine {
	diffLines := make([]DiffLine, 0)

	for _, item := range slice {
		switch value := item.(type) {
		case map[string]any:
			diffLines = append(diffLines, printNode(value, indentation, diffType, true)...)
		case []any:
			diffLines = append(diffLines, printSlice(value, indentation, diffType)...)
		default:
			diffLines = append(diffLines, DiffLine{
				Node:        fmt.Sprintf("- %v", item),
				DiffType:    diffType,
				Indentation: indentation,
				Value:       "",
			})
		}
	}

	return diffLines
}

func printNode(
	node map[string]any,
	indentation string,
	diffType diffType,
	isSliceChild bool,
) []DiffLine {
	diffLines := make([]DiffLine, 0)
	keys := sortMapKeys(node)

	isFirst := true
	for _, key := range keys {
		value := node[key]
		switch real := value.(type) {
		case map[string]any:
			line := DiffLine{
				DiffType: diffType,
			}

			if isFirst && isSliceChild {
				line.Node = fmt.Sprintf("- %s", key)
				line.Indentation = indentation
			} else {
				line.Node = key
				line.Indentation = indentation + "  "
			}

			diffLines = append(diffLines, line)
			diffLines = append(diffLines, printNode(real, indentation+"  ", diffType, false)...)
		case []any:
			diffLines = append(diffLines, printSlice(real, indentation+"  ", diffType)...)
		default:
			line := DiffLine{
				DiffType: diffType,
			}

			if isFirst && isSliceChild {
				line.Node = fmt.Sprintf("- %s", key)
				line.Value = fmt.Sprintf("%v", real)
				line.Indentation = indentation
			} else {
				line.Node = key
				line.Value = fmt.Sprintf("%v", real)
				line.Indentation = indentation + "  "
			}

			isFirst = false

			diffLines = append(diffLines, line)
		}
	}

	return diffLines
}

func GetMarker(diffType diffType) string {
	switch diffType {
	case NoDiff:
		return " "
	case AddDiff:
		return "+"
	case UpdateDiff:
		return "~"
	case DeleteDiff:
		return "-"
	}

	return " "
}

func sortMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	return keys
}
