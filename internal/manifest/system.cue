package navecd

import (
	"github.com/kharf/navecd/schema/component"
)

_{{.Shard}}Labels: {
	"\(_controlPlaneKey)": "{{.Name}}"
	"\(_shardKey)":   "{{.Shard}}"
}

{{.Shard}}ServiceAccount: component.#Manifest & {
	dependencies: [ns.id]
	content: {
		apiVersion: "v1"
		kind:       "ServiceAccount"
		metadata: {
			name:      "{{.Name}}"
			namespace: ns.content.metadata.name
			labels:    _{{.Shard}}Labels
		}
	}
}

{{.Shard}}ClusteRoleBinding: component.#Manifest & {
	dependencies: [
		ns.id,
		clusterRole.id,
		{{.Shard}}ServiceAccount.id,
	]
	content: {
		apiVersion: "rbac.authorization.k8s.io/v1"
		kind:       "ClusterRoleBinding"
		metadata: {
			name:   "{{.Name}}"
			labels: _{{.Shard}}Labels
		}
		roleRef: {
			apiGroup: "rbac.authorization.k8s.io"
			kind:     clusterRole.content.kind
			name:     clusterRole.content.metadata.name
		}
		subjects: [
			{
				kind:      {{.Shard}}ServiceAccount.content.kind
				name:      {{.Shard}}ServiceAccount.content.metadata.name
				namespace: {{.Shard}}ServiceAccount.content.metadata.namespace
			},
		]
	}
}

_{{.Shard}}LeaderRoleName: "{{.Shard}}-leader-election"
{{.Shard}}LeaderRole: component.#Manifest & {
	dependencies: [ns.id]
	content: {
		apiVersion: "rbac.authorization.k8s.io/v1"
		kind:       "Role"
		metadata: {
			name:      _{{.Shard}}LeaderRoleName
			namespace: ns.content.metadata.name
			labels:    _{{.Shard}}Labels
		}
		rules: [
			{
				apiGroups: ["coordination.k8s.io"]
				resources: ["leases"]
				verbs: [
					"get",
					"create",
					"update",
				]
			},
			{
				apiGroups: [""]
				resources: ["events"]
				verbs: [
					"create",
					"patch",
				]
			},
		]
	}
}

{{.Shard}}LeaderRoleBinding: component.#Manifest & {
	dependencies: [
		ns.id,
		{{.Shard}}LeaderRole.id,
	]
	content: {
		apiVersion: "rbac.authorization.k8s.io/v1"
		kind:       "RoleBinding"
		metadata: {
			name:      _{{.Shard}}LeaderRoleName
			namespace: ns.content.metadata.name
			labels:    _{{.Shard}}Labels
		}
		roleRef: {
			apiGroup: "rbac.authorization.k8s.io"
			kind:     {{.Shard}}LeaderRole.content.kind
			name:     {{.Shard}}LeaderRole.content.metadata.name
		}
		subjects: [
			{
				kind:      {{.Shard}}ServiceAccount.content.kind
				name:      {{.Shard}}ServiceAccount.content.metadata.name
				namespace: {{.Shard}}ServiceAccount.content.metadata.namespace
			},
		]
	}
}

{{.Shard}}PVC: component.#Manifest & {
	dependencies: [
		ns.id,
		knownHostsCm.id,
	]
	content: {
		apiVersion: "v1"
		kind:       "PersistentVolumeClaim"
		metadata: {
			name:      "{{.Shard}}"
			namespace: ns.content.metadata.name
			labels:    _{{.Shard}}Labels
		}
		spec: {
			accessModes: [
				"ReadWriteOnce",
			]
			resources: {
				requests: {
					storage: "200Mi"
				}
			}
		}
	}
}

{{.Shard}}ProjectControllerDeployment: component.#Manifest & {
	dependencies: [
		ns.id,
		{{.Shard}}PVC.id,
		knownHostsCm.id,
	]
	content: {
		apiVersion: "apps/v1"
		kind:       "Deployment"
		metadata: {
			name:      "{{.Name}}"
			namespace: ns.content.metadata.name
			labels:    _{{.Shard}}Labels
		}
		spec: {
			selector: matchLabels: _{{.Shard}}Labels
			replicas: 1
			template: {
				metadata: {
					labels: _{{.Shard}}Labels
				}
				spec: {
					serviceAccountName: "{{.Name}}"
					securityContext: {
						runAsNonRoot:        true
						fsGroup:             65532
						fsGroupChangePolicy: "OnRootMismatch"
					}
					volumes: [
						{
							name: "{{.Shard}}"
							persistentVolumeClaim: claimName: "{{.Shard}}"
						},
						{
							name: "podinfo"
							downwardAPI: {
								items: [
									{
										path: "namespace"
										fieldRef: fieldPath: "metadata.namespace"
									},
									{
										path: "name"
										fieldRef: fieldPath: "metadata.labels['\(_controlPlaneKey)']"
									},
									{
										path: "shard"
										fieldRef: fieldPath: "metadata.labels['\(_shardKey)']"
									},
								]
							}
						},
						{
							name: "ssh"
							configMap: name: knownHostsCm.content.metadata.name
						},
						{
							name: "cache"
							emptyDir: {}
						},
					]
					containers: [
						{
							name:  "{{.Name}}"
							image: "{{.Image}}:{{.Version}}"
							command: [
								"/controller",
							]
							args: [
								"--log-level=0",
							]
							env: [
								{
									name: "SSH_KNOWN_HOSTS"
									value: "/.ssh/known_hosts"
								},
								{
									name: "CUE_REGISTRY"
									value: "github.com/kharf/navecd/schema=ghcr.io/kharf,registry.cue.works"
								},
							]
							securityContext: {
								allowPrivilegeEscalation: false
								capabilities: {
									drop: [
										"ALL",
									]
								}
							}
							resources: {
								limits: {
									memory: "250Mi"
								}
								requests: {
									memory: "250Mi"
									cpu:    "500m"
								}
							}
							ports: [
								{
									name:          "http"
									protocol:      "TCP"
									containerPort: 8080
								},
							]
							volumeMounts: [
								{
									name:      "{{.Shard}}"
									mountPath: "/inventory"
								},
								{
									name:      "podinfo"
									mountPath: "/podinfo"
									readOnly:  true
								},
								{
									name:      "ssh"
									mountPath: "/.ssh"
									readOnly:  true
								},
								{
									name:      "cache"
									mountPath: "/.cache"
								},
							]
						},
					]
				}
			}
		}
	}
}

{{.Shard}}Service: component.#Manifest & {
	dependencies: [
		ns.id,
		{{.Shard}}ProjectControllerDeployment.id,
	]
	content: {
		apiVersion: "v1"
		kind:       "Service"
		metadata: {
			name:      "{{.Name}}"
			namespace: ns.content.metadata.name
			labels:    _{{.Shard}}Labels
		}
		spec: {
			clusterIP: "None"
			selector:  _{{.Shard}}Labels
			ports: [
				{
					name:       "http"
					protocol:   "TCP"
					port:       8080
					targetPort: "http"
				},
			]
		}
	}
}
