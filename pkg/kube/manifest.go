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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// IgnoreInstruction is an instruction to tell Navecd to ignore fields or structs on certain events when applying Kubernetes Manifests.
type IgnoreInstruction int

const (
	// Default. Navecd will enforce the field/struct.
	None IgnoreInstruction = iota

	// This tells Navecd to omit the field/struct 'tagged' with this value on a retry ssa patch request.
	OnConflict
)

// ManifestMetadata extends unstructured fields, structs or lists with additional information.
type ManifestMetadata struct {
	Field *ManifestFieldMetadata
	Node  map[string]ManifestMetadata
	List  []ManifestMetadata
}

// ManifestFieldMetadata extends unstructured fields with additional information.
type ManifestFieldMetadata struct {
	IgnoreInstr IgnoreInstruction
}

// ExtendedUnstructured enhances Kubernetes Unstructured struct with additional Metadata, like IgnoreAttributes.
type ExtendedUnstructured struct {
	*unstructured.Unstructured
	Metadata *ManifestMetadata `json:"-"`
}

// Manifest represents a Navecd component with its id, dependencies and content.
// It is the Go equivalent of the CUE definition the user interacts with.
// See [unstructured.Unstructured] for more.
type Manifest struct {
	ID           string
	Dependencies []string
	Content      ExtendedUnstructured
}

func (m *Manifest) GetID() string {
	return m.ID
}

func (m *Manifest) GetDependencies() []string {
	return m.Dependencies
}

func (m *Manifest) GetKind() string {
	return m.Content.GetKind()
}

func (m *Manifest) GetAPIVersion() string {
	return m.Content.GetAPIVersion()
}

func (m *Manifest) GetLabels() map[string]string {
	return m.Content.GetLabels()
}

func (m *Manifest) GetName() string {
	return m.Content.GetName()
}

func (m *Manifest) GetNamespace() string {
	return m.Content.GetNamespace()
}
