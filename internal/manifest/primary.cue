package navecd

import (
	"github.com/kharf/navecd/schema/component"
)

_controlPlaneKey: "navecd/control-plane"
_shardKey: "navecd/shard"

// _crd is autogenerated into crd.cue
crd: component.#Manifest & {
	content: _crd & {
		metadata: labels: _{{.Shard}}Labels
	}
}

ns: component.#Manifest & {
	dependencies: [crd.id]
	content: {
		apiVersion: "v1"
		kind:       "Namespace"
		metadata: {
			name:   "navecd-system"
			labels: _{{.Shard}}Labels
		}
	}
}

clusterRole: component.#Manifest & {
	dependencies: [ns.id]
	content: {
		apiVersion: "rbac.authorization.k8s.io/v1"
		kind:       "ClusterRole"
		metadata: {
			name:   "project-controller"
			labels: _{{.Shard}}Labels
		}
		rules: [
			{
				apiGroups: ["gitops.navecd.io"]
				resources: ["gitopsprojects"]
				verbs: [
					"list",
					"watch",
				]
			},
			{
				apiGroups: ["gitops.navecd.io"]
				resources: ["gitopsprojects/status"]
				verbs: [
					"get",
					"patch",
					"update",
				]
			},
			{
				apiGroups: ["*"]
				resources: ["*"]
				verbs: [
					"*",
				]
			},
		]
	}
}


knownHostsCm: component.#Manifest & {
	dependencies: [
		ns.id,
	]
	content: {
		apiVersion: "v1"
		kind:       "ConfigMap"
		metadata: {
			name:      "known-hosts"
			namespace: ns.content.metadata.name
			labels:    _{{.Shard}}Labels
		}
		data: {
			"known_hosts": """
				github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
				gitlab.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAfuCHKVTjquxvt6CM6tdG4SLp1Btn/nOeHHE5UOzRdf
				"""
		}
	}
}
