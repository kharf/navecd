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

package component

import "sort"

// InstanceLayer holds a slice of unrelated components, which can be processed concurrently.
type InstanceLayer struct {
	Components []Instance
}

// Layer takes a topologically sorted slice of components and packs them into layers.
// A layer holds a slice of unrelated components, which can be processed concurrently.
// The zeroth layer contains components with no dependencies.
// Components are assigned to layers based on their dependencies' layers.
// If a dependency is in layer 0, the component gets placed into layer 1.
// If a dependency is in layer 0 and another dependency in layer 2, the component gets placed into layer 3.
func Layer(instances []Instance) []InstanceLayer {
	layerAssignments := make(map[string]int)
	depLayersByNumber := make(map[int]InstanceLayer)

	for _, instance := range instances {
		highestParentLayerNumber := 0
		for _, dep := range instance.GetDependencies() {
			parentLayerNumber := layerAssignments[dep]

			if parentLayerNumber > highestParentLayerNumber {
				highestParentLayerNumber = parentLayerNumber
			}
		}

		layerNumber := highestParentLayerNumber + 1

		layer, exists := depLayersByNumber[layerNumber]
		if !exists {
			layer = InstanceLayer{
				Components: []Instance{instance},
			}
		} else {
			layer.Components = append(layer.Components, instance)
		}

		depLayersByNumber[layerNumber] = layer
		layerAssignments[instance.GetID()] = layerNumber
	}

	layerNumbers := make([]int, 0, len(depLayersByNumber))
	for layerNumber := range depLayersByNumber {
		layerNumbers = append(layerNumbers, layerNumber)
	}
	sort.Ints(layerNumbers)

	layers := make([]InstanceLayer, 0, len(layerNumbers))
	for _, layer := range layerNumbers {
		layers = append(layers, depLayersByNumber[layer])
	}

	return layers
}
