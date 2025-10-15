package navecd

import (
	"github.com/kharf/navecd/schema/component"
	{{if or (eq .Provider "AWS") (eq .Provider "Azure") (eq .Provider "GCP")}}
	"github.com/kharf/navecd/schema/workloadidentity"
	{{end}}
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
			url:                 "{{.Url}}"
			ref:                 "{{.Ref}}"
			dir:                 "{{.Dir}}"
			{{if or (eq .Provider "AWS") (eq .Provider "Azure") (eq .Provider "GCP")}}
			auth: workloadidentity.#{{.Provider}}
			{{end}}
			{{- if .SecretRef}}
			auth: secretRef: name: "{{.SecretRef}}"
			{{end}}
			pullIntervalSeconds: {{.PullIntervalSeconds}}
			suspend:             false
		}
	}
}
