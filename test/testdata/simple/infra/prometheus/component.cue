package prometheus

import "github.com/kharf/declcd/api/v1"

prometheus: v1.#Component & {
	manifests: [
		#namespace,
		secret,
	]
	helmReleases: [
		_release,
	]
}
