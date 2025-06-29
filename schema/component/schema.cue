package component

import "strings"

_manifestMetadata: {
	apiVersion!: string & strings.MinRunes(1)
	kind!:       string & strings.MinRunes(1)
	metadata: {
		namespace?: string
		name!:      string & strings.MinRunes(1)
		...
	}
}

// Manifest represents a Kubernetes Object.
#Manifest: {
	type:          "Manifest"
	_groupVersion: strings.Split(content.apiVersion, "/")
	_group:        string | *""
	if len(_groupVersion) >= 2 {
		_group: _groupVersion[0]
	}
	id: "\(content.metadata.name)_\(*content.metadata.namespace | "")_\(_group)_\(content.kind)"
	dependencies: [...string]
	content: {
		_manifestMetadata
		...
	}
}

// HelmRelease is a running instance of a Chart and the current state in a Kubernetes Cluster.
#HelmRelease: {
	type: "HelmRelease"
	id:   "\(name)_\(namespace)_\(type)"
	dependencies: [...string]

	// Name influences the name of the installed objects of a Helm Chart.
	// When set, the installed objects are suffixed with the chart name.
	// Defaults to the chart name.
	name!: string & strings.MinRunes(1)

	// Namespace specifies the Kubernetes namespace to which the Helm Chart is installed to.
	// Defaults to default.
	namespace!: string

	chart!: #HelmChart

	// Values provide a way to override Helm Chart template defaults with custom information.
	values: {...}

	// Patches allow to overwrite rendered manifests before installing/upgrading.
	// Additionally they can be used to attach build attributes to fields.
	patches: [...{
		_manifestMetadata
		...
	}]

	crds: #CRDs
}

// Helm CRD handling configuration.
#CRDs: {
	// Helm only supports installation by default.
	// This option extends Helm to allow Navecd to upgrade CRDs packaged within a Chart on drifts.
	// It does nothing, when ForceUpgrade is true.
	allowUpgrade: bool | *false
	// This option extends Helm to force Navecd to upgrade CRDs packaged within a Chart before drift detection.
	forceUpgrade: bool | *false
}

// A Helm package that contains information
// sufficient for installing a set of Kubernetes resources into a Kubernetes cluster.
#HelmChart: {
	name!: string & strings.MinRunes(1)

	// URL of the repository where the Helm chart is hosted.
	repoURL!: string & strings.HasPrefix("oci://") | strings.HasPrefix("http://") | strings.HasPrefix("https://")

	version!: string & strings.MinRunes(1)
	auth?:    #Auth
}

// Auth contains methods for repository/registry authentication.
#Auth: {
	workloadIdentity: {
		provider: "gcp" | "aws" | "azure"
	}
} | {
	secretRef: {
		name:      string & strings.MinRunes(1)
		namespace: string & strings.MinRunes(1)
	}
}
