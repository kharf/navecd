package keda

import (
	corev1 "k8s.io/api/core/v1"
	"github.com/kharf/declcd/api/v1"
	"github.com/kharf/decldc-test-repo/infra/linkerd"
)

_release: v1.#HelmRelease & {
	name:      "keda"
	namespace: _namespace.metadata.name
	chart: {
		name:    "keda"
		repoURL: "https://kedacore.github.io/charts"
		version: "2.13.1"
	}
}

_namespace: corev1.#Namespace & {
	apiVersion: "v1"
	kind:       "Namespace"
	metadata: {
		name: "keda"
		annotations: {
			"linkerd.io/inject": "disabled"
		}
	}
}

v1.#Component & {
	keda: {
		dependencies: [
			linkerd.linkerd.id,
		]
		manifests: [
			_namespace,
		]
		helmReleases: [
			_release,
		]
	}
}