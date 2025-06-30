package navecd

import (
	"github.com/kharf/navecd/schema/component"
)

{{.Name}}: component.#Manifest & {
	dependencies: [
		crd.id,
		ns.id,
	]
	content: {
		apiVersion: "gitops.navecd.io/v1beta1"
		kind:       "GitOpsProject"
		metadata: {
			name:      "{{.Name}}"
			namespace: "{{.Namespace}}"
			labels: _{{.Shard}}Labels
		}
		spec: {
			branch:              "{{.Branch}}"
			dir:                 "{{.Dir}}"
			pullIntervalSeconds: {{.PullIntervalSeconds}}
			suspend:             false
			url:                 "{{.Url}}"
		}
	}
}
