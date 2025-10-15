package navecd

_crd: {
	apiVersion: "apiextensions.k8s.io/v1"
	kind:       "CustomResourceDefinition"
	metadata: {
		annotations: "controller-gen.kubebuilder.io/version": "v0.19.0"
		name: "gitopsprojects.gitops.navecd.io"
	}
	spec: {
		group: "gitops.navecd.io"
		names: {
			kind:     "GitOpsProject"
			listKind: "GitOpsProjectList"
			plural:   "gitopsprojects"
			shortNames: ["gop"]
			singular: "gitopsproject"
		}
		scope: "Namespaced"
		versions: [{
			name: "v1beta1"
			schema: openAPIV3Schema: {
				description: "GitOpsProject is the Schema for the gitopsprojects API"
				properties: {
					apiVersion: {
						description: """
	APIVersion defines the versioned schema of this representation of an object.
	Servers should convert recognized schemas to the latest internal value, and
	may reject unrecognized values.
	More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#resources
	"""
						type: "string"
					}
					kind: {
						description: """
	Kind is a string value representing the REST resource this object represents.
	Servers may infer this from the endpoint the client submits requests to.
	Cannot be updated.
	In CamelCase.
	More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#types-kinds
	"""
						type: "string"
					}
					metadata: type: "object"
					spec: {
						description: "GitOpsProjectSpec defines the desired state of GitOpsProject"
						properties: {
							auth: {
								description: "Authentication information for private oci repositories."
								properties: {
									secretRef: {
										description: "SecretRef is the reference to the secret containing the repository/registry authentication."
										properties: name: type: "string"
										required: ["name"]
										type: "object"
									}
									workloadIdentity: {
										description: "WorkloadIdentity is a keyless approach used for repository/registry authentication."
										properties: provider: type: "string"
										required: ["provider"]
										type: "object"
									}
								}
								required: [
									"secretRef",
									"workloadIdentity",
								]
								type: "object"
							}
							dir: {
								default: "."
								description: """
	The directory of the gitops repository containing navecd configuration.
	Can be "." for root.
	"""
								minLength: 1
								type:      "string"
							}
							pullIntervalSeconds: {
								description: "This defines how often navecd will try to fetch changes from the gitops repository."
								minimum:     5
								type:        "integer"
							}
							ref: {
								description: "The reference to the gitops repository containing navecd configuration."
								minLength:   1
								type:        "string"
							}
							serviceAccountName: type: "string"
							suspend: {
								description: """
	This flag tells the controller to suspend subsequent executions, it does
	not apply to already started executions.  Defaults to false.
	"""
								type: "boolean"
							}
							url: {
								description: "The url to the gitops repository."
								minLength:   1
								type:        "string"
							}
						}
						required: [
							"dir",
							"pullIntervalSeconds",
							"ref",
							"url",
						]
						type: "object"
					}
					status: {
						description: "GitOpsProjectStatus defines the observed state of GitOpsProject"
						properties: {
							conditions: {
								items: {
									description: "Condition contains details for one aspect of the current state of this API Resource."
									properties: {
										lastTransitionTime: {
											description: """
	lastTransitionTime is the last time the condition transitioned from one status to another.
	This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.
	"""
											format: "date-time"
											type:   "string"
										}
										message: {
											description: """
	message is a human readable message indicating details about the transition.
	This may be an empty string.
	"""
											maxLength: 32768
											type:      "string"
										}
										observedGeneration: {
											description: """
	observedGeneration represents the .metadata.generation that the condition was set based upon.
	For instance, if .metadata.generation is currently 12, but the .status.conditions[x].observedGeneration is 9, the condition is out of date
	with respect to the current state of the instance.
	"""
											format:  "int64"
											minimum: 0
											type:    "integer"
										}
										reason: {
											description: """
	reason contains a programmatic identifier indicating the reason for the condition's last transition.
	Producers of specific condition types may define expected values and meanings for this field,
	and whether the values are considered a guaranteed API.
	The value should be a CamelCase string.
	This field may not be empty.
	"""
											maxLength: 1024
											minLength: 1
											pattern:   "^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$"
											type:      "string"
										}
										status: {
											description: "status of the condition, one of True, False, Unknown."
											enum: [
												"True",
												"False",
												"Unknown",
											]
											type: "string"
										}
										type: {
											description: "type of condition in CamelCase or in foo.example.com/CamelCase."
											maxLength:   316
											pattern:     "^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])$"
											type:        "string"
										}
									}
									required: [
										"lastTransitionTime",
										"message",
										"reason",
										"status",
										"type",
									]
									type: "object"
								}
								type: "array"
							}
							revision: {
								properties: {
									digest: type: "string"
									reconcileTime: {
										format: "date-time"
										type:   "string"
									}
								}
								type: "object"
							}
						}
						type: "object"
					}
				}
				type: "object"
			}
			served:  true
			storage: true
			subresources: status: {}
		}]
	}
}
