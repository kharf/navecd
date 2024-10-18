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

package component_test

import (
	"slices"
	"testing"

	"github.com/kharf/navecd/pkg/component"
	"gotest.tools/v3/assert"
)

func TestLayer(t *testing.T) {
	graph := component.NewDependencyGraph()
	err := graph.Insert(
		&component.Manifest{
			ID:           "prometheus___Namespace",
			Dependencies: []string{},
		},
		&component.Manifest{
			ID:           "linkerd___Namespace",
			Dependencies: []string{"certmanager___Namespace"},
		},
		&component.Manifest{
			ID:           "certmanager___Namespace",
			Dependencies: []string{},
		},
		&component.Manifest{
			ID:           "emissaryingress___Namespace",
			Dependencies: []string{"certmanager___Namespace"},
		},
		&component.Manifest{
			ID:           "loki___Namespace",
			Dependencies: []string{"certmanager___Namespace", "keda___Namespace"},
		},
		&component.Manifest{
			ID: "keda___Namespace",
			Dependencies: []string{
				"prometheus___Namespace",
				"emissaryingress___Namespace",
				"certmanager___Namespace",
			},
		},
	)
	assert.NilError(t, err)
	result, err := graph.TopologicalSort()

	wantLayers := []component.InstanceLayer{
		{
			Components: []component.Instance{
				&component.Manifest{
					ID:           "prometheus___Namespace",
					Dependencies: []string{},
				},
				&component.Manifest{
					ID:           "certmanager___Namespace",
					Dependencies: []string{},
				},
			},
		},
		{
			Components: []component.Instance{
				&component.Manifest{
					ID:           "linkerd___Namespace",
					Dependencies: []string{"certmanager___Namespace"},
				},
				&component.Manifest{
					ID:           "emissaryingress___Namespace",
					Dependencies: []string{"certmanager___Namespace"},
				},
			},
		},
		{
			Components: []component.Instance{
				&component.Manifest{
					ID: "keda___Namespace",
					Dependencies: []string{
						"prometheus___Namespace",
						"emissaryingress___Namespace",
						"certmanager___Namespace",
					},
				},
			},
		},
		{
			Components: []component.Instance{
				&component.Manifest{
					ID:           "loki___Namespace",
					Dependencies: []string{"certmanager___Namespace", "keda___Namespace"},
				},
			},
		},
	}

	layers := component.Layer(result)

	for layerIdx, wantLayer := range wantLayers {
		for _, wantComponent := range wantLayer.Components {
			assert.Assert(
				t,
				slices.ContainsFunc(
					layers[layerIdx].Components,
					func(haveComponent component.Instance) bool {
						return haveComponent.GetID() == wantComponent.GetID()
					},
				),
			)
		}
	}
}
