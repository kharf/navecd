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

package inventory

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	// ErrWrongInventoryKey occurs when a stored object has been read,
	// which doesn't follow the expected format.
	// This can only happen through an incompatible change, like editing the inventory directly.
	ErrWrongInventoryKey     = errors.New("Inventory key is incorrect")
	ErrManifestFieldNotFound = errors.New("Manifest field not found")
)

// Item is a small representation of a stored object.
type Item interface {
	GetName() string
	GetNamespace() string
	// GetID returns a unique identifier.
	GetID() string
}

// HelmReleaseItem is a small inventory representation of a Release.
// Release is a running instance of a Chart.
// When a chart is installed, the ChartReconciler creates a release to track that installation.
type HelmReleaseItem struct {
	Name      string
	Namespace string
	ID        string
}

var _ Item = (*HelmReleaseItem)(nil)

func (hr *HelmReleaseItem) GetName() string {
	return hr.Name
}

func (hr *HelmReleaseItem) GetNamespace() string {
	return hr.Namespace
}

// GetID returns the string representation of the release.
// This is used as an identifier in the inventory.
func (hr *HelmReleaseItem) GetID() string {
	return hr.ID
}

// ManifestItem a small inventory representation of a ManifestItem.
// ManifestItem is a Kubernetes object.
type ManifestItem struct {
	TypeMeta  v1.TypeMeta
	Name      string
	Namespace string
	ID        string
}

var _ Item = (*ManifestItem)(nil)

func (manifest *ManifestItem) GetName() string {
	return manifest.Name
}

func (manifest *ManifestItem) GetNamespace() string {
	return manifest.Namespace
}

// GetID returns the string representation of the manifest.
// This is used as an identifier in the inventory.
func (manifest *ManifestItem) GetID() string {
	return manifest.ID
}

// Storage represents all stored Navecd items.
// It is effectively the current cluster state.
type Storage struct {
	items map[string]Item
}

// Items returns all stored Navecd items.
func (inv Storage) Items() map[string]Item {
	return inv.items
}

// HasItem evaluates whether an item is part of the current cluster state.
func (inv Storage) HasItem(item Item) bool {
	if _, found := inv.items[item.GetID()]; found {
		return true
	}
	return false
}

// Instance is a representation of an inventory.
// It can store, delete and read items.
// The object does not include the storage itself, it only holds a reference to the storage.
type Instance struct {
	Path string
}

// Load returns all the stored components in this inventory.
func (instance *Instance) Load() (*Storage, error) {
	if err := os.MkdirAll(instance.Path, 0700); err != nil {
		return nil, err
	}
	items := make(map[string]Item)
	err := filepath.WalkDir(instance.Path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			key := d.Name()
			identifier := strings.Split(key, "_")
			name := identifier[0]
			namespace := identifier[1]
			if len(identifier) == 3 {
				kind := identifier[2]
				if kind != "HelmRelease" {
					return fmt.Errorf(
						"%w: key with only 3 identifiers is expected to be a HelmRelease",
						ErrWrongInventoryKey,
					)
				}
				items[key] = &HelmReleaseItem{
					Name:      name,
					Namespace: namespace,
					ID:        key,
				}
			} else {
				if len(identifier) != 4 {
					return fmt.Errorf("%w: key '%s' does not contain 4 identifiers", ErrWrongInventoryKey, key)
				}
				file, err := os.Open(path)
				if err != nil {
					return err
				}
				defer file.Close()
				unstr := map[string]interface{}{}
				if err := json.NewDecoder(file).Decode(&unstr); err != nil {
					return err
				}
				kind, found := unstr["kind"].(string)
				if !found {
					return fmt.Errorf("%w: %s not found in inventory item %s", ErrManifestFieldNotFound, "kind", key)
				}
				apiVersion, found := unstr["apiVersion"].(string)
				if !found {
					return fmt.Errorf("%w: %s not found in inventory item %s", ErrManifestFieldNotFound, "apiVersion", key)
				}
				items[key] = &ManifestItem{
					TypeMeta: v1.TypeMeta{
						Kind:       kind,
						APIVersion: apiVersion,
					},
					Name:      name,
					Namespace: namespace,
					ID:        key,
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Storage{
		items: items,
	}, nil
}

// GetItem opens the item file for reading.
// If there is an error, it will be of type *PathError.
func (instance Instance) GetItem(item Item) (io.ReadCloser, error) {
	itemFile, err := os.Open(filepath.Join(instance.Path, itemNs(item), item.GetID()))
	if err != nil {
		return nil, err
	}
	return itemFile, nil
}

// StoreItem persists given item with optional content in the inventory.
func (instance Instance) StoreItem(item Item, contentReader io.Reader) error {
	dir := filepath.Join(instance.Path, itemNs(item))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.Create(filepath.Join(dir, item.GetID()))
	if err != nil {
		return err
	}
	defer file.Close()
	if contentReader != nil {
		bufferedReadWriter := bufio.NewReadWriter(
			bufio.NewReader(contentReader),
			bufio.NewWriter(file),
		)
		for {
			line, err := bufferedReadWriter.ReadString('\n')
			if err == io.EOF {
				break
			}
			_, err = bufferedReadWriter.WriteString(line)
			if err != nil {
				return err
			}
		}
		if err = bufferedReadWriter.Flush(); err != nil {
			return err
		}
	}
	return nil
}

// DeleteItem removes the item from the inventory.
// Navecd will not be tracking its current state anymore.
func (instance Instance) DeleteItem(item Item) error {
	dir := filepath.Join(instance.Path, itemNs(item))
	dirFile, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirFile.Close()
	_, err = dirFile.Readdirnames(1)
	if err == io.EOF {
		if err := os.Remove(dir); err != nil {
			return err
		}
	}
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, item.GetID()))
}

func itemNs(item Item) string {
	ns := item.GetNamespace()
	if ns == "" {
		ns = item.GetName()
	}
	return ns
}
