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
	"strings"

	repo "helm.sh/helm/v4/pkg/repo/v1"
)

// Parses a Helm Chart version into two parts: version and digest.
// Digest is optional and will be an empty string if not found.
// Input format must be eiter "version" or "version@digest".
func ParseVersion(version string) (string, string) {
	versionParts := strings.Split(version, "@")
	if len(versionParts) >= 2 {
		return versionParts[0], versionParts[1]
	}

	return version, ""
}

type ChartVersionIter struct {
	Versions repo.ChartVersions
}

func (iter *ChartVersionIter) ForEach(do func(item string, idx int)) {
	for idx, item := range iter.Versions {
		do(item.Version, idx)
	}
}
