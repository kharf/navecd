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

import (
	"errors"
	"fmt"
)

var (
	ErrCyclicDependency     = errors.New("Cyclic dependency detected")
	ErrDuplicateComponentID = errors.New("Duplicate Component ID")
	ErrUnknownComponentID   = errors.New("Unknown Component ID")
)

// DependencyGraph is an adjacency list which represents the directed acyclic graph of component dependencies.
// The Dependencies field in the Node struct holds a list of other component ids to which the current component has edges.
type DependencyGraph struct {
	set map[string]Instance
}

func NewDependencyGraph() DependencyGraph {
	return DependencyGraph{
		set: make(map[string]Instance),
	}
}

// Insert places given Nodes into the DependencyGraph.
// It returns an error if a given Node id / component id already exists in the graph.
func (graph *DependencyGraph) Insert(nodes ...Instance) error {
	for _, node := range nodes {
		if _, found := graph.set[node.GetID()]; found {
			return fmt.Errorf(
				"%w: id %s already exists in graph",
				ErrDuplicateComponentID,
				node.GetID(),
			)
		}
		graph.set[node.GetID()] = node
	}
	return nil
}

func (graph *DependencyGraph) Delete(componentID string) {
	delete(graph.set, componentID)
}

// Get returns the Component if it has been identified by its id.
// It returns nil if no Node has been found.
func (graph *DependencyGraph) Get(componentID string) Instance {
	node, found := graph.set[componentID]
	if !found {
		return nil
	}
	return node
}

// TopologicalSort performs a topological sort on the component dependency graph and returns the sorted order.
// It returns an error if a cycle is detected.
func (dag *DependencyGraph) TopologicalSort() ([]Instance, error) {
	inProcessing := make(map[string]struct{})
	visited := make(map[string]struct{}, len(dag.set))
	result := make([]Instance, 0, len(dag.set))
	var walk func(nodeID string) error
	walk = func(nodeID string) error {
		if _, found := inProcessing[nodeID]; found {
			return fmt.Errorf("%w for %s", ErrCyclicDependency, nodeID)
		}

		if _, found := visited[nodeID]; found {
			return nil
		}

		inProcessing[nodeID] = struct{}{}

		node := dag.set[nodeID]
		if node == nil {
			return fmt.Errorf("%w: %s not found in dependency graph", ErrUnknownComponentID, nodeID)
		}

		for _, depNode := range node.GetDependencies() {
			if err := walk(depNode); err != nil {
				return err
			}
		}

		delete(inProcessing, nodeID)

		visited[nodeID] = struct{}{}

		result = append(result, node)

		return nil
	}

	for node := range dag.set {
		if err := walk(node); err != nil {
			return nil, err
		}
	}
	return result, nil
}
