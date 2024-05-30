package prometheus

import (
	"github.com/kharf/declcd/schema/component"
	"github.com/kharf/declcd/test/testdata/simple/infra/linkerd"
)

release: component.#HelmRelease & {
	dependencies: [
		ns.id,
		linkerd.ns.id,
	]
	name:      "{{.Name}}"
	namespace: #namespace.metadata.name
	chart: {
		name:    "test"
		repoURL: "{{.RepoUrl}}"
		version: "{{.Version}}"
	}
	values: {
		autoscaling: enabled: true
	}
}
