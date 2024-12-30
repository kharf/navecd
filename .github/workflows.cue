package build

import "github.com/kharf/cuepkgs/modules/github@v0"

#workflow: {
	_name: string
	workflow: github.#Workflow & {
		name:        _name
		permissions: "read-all"
		jobs: [string]: {
			"runs-on": "ubuntu-latest"
			steps: [
				#checkoutCode,
				...,
			]
		}
	}
	...
}

#checkoutCode: {
	name: "Checkout code"
	uses: "actions/checkout@v4.2.2"
	with: {
		[string]: string | number | bool
		token:    "${{ secrets.PAT }}"
	}
}

#setupGo: {
	name: "Setup Go"
	uses: "actions/setup-go@v5.1.0"
	with: {
		"go-version-file":       "go.mod"
		"cache-dependency-path": "go.sum"
	}
}

#dagger: {
	name: string
	uses: "dagger/dagger-for-github@v7.0.3"
	with: {
		call?: string
		verb?: string
	}
	env?: {
		[string]: string | number | bool
	}
}

workflows: [
	#workflow & {
		_name: "PR-Conformance"
		workflow: github.#Workflow & {
			on: {
				pull_request: {
					branches: [
						"*",
					]
					"tags-ignore": [
						"*",
					]
				}
			}

			jobs: "\(_name)": {
				steps: [
					#checkoutCode & {
						with: {
							ref: "${{ github.head_ref || github.ref_name }}"
						}
					},
					#dagger & {
						name: "Generate Workflows"
						with: call: "gen-workflows --source=. export --path=.github/workflows"
					},
					#dagger & {
						name: "Commit Workflows"
						with: call: "commit-workflows --source=. --token=env:GITHUB_TOKEN"
						env: {
							GITHUB_TOKEN: "${{ secrets.PAT }}"
						}
					},
					#dagger & {
						name: "Test"
						with: call: "test --source=."
					},
				]
			}
		}
	},
	#workflow & {
		_name: "Test"
		workflow: github.#Workflow & {
			on: {
				push: {
					branches: [
						"main",
					]
					"tags-ignore": [
						"*",
					]
				}
			}

			jobs: "\(_name)": {
				steps: [
					#checkoutCode,
					#dagger & {
						name: "Test"
						with: call: "test --source=."
					},
				]
			}
		}
	},
	#workflow & {
		_name: "Release"
		workflow: github.#Workflow & {
			on: {
				workflow_dispatch: {
					branches: [
						"main",
					]
					inputs: {
						version: {
							description: "version to be released"
							required:    true
						}
						"prev-version": {
							description: "previous version to use to calculate the changelog diff from"
							required:    false
							default:     ""
						}
					}
				}
			}

			jobs: "\(_name)": {
				steps: [
					#checkoutCode & {
						with: {
							"fetch-tags":  true
							"fetch-depth": 0
						}
					},
					#setupGo,
					#dagger & {
						name: "Release"
						with: call: "release --source=. --version=${{ inputs.version }} --previous-version=${{ inputs.prev-version}} --user=kharf --token=env:GITHUB_TOKEN"
						env: {
							GITHUB_TOKEN: "${{ secrets.PAT }}"
						}
					},
				]
			}
		}
	},
	#workflow & {
		_name: "Update"
		workflow: github.#Workflow & {
			on: {
				workflow_dispatch: null
				schedule: [{
					cron: "0 5 * * 1-5"
				},
				]
			}

			jobs: "\(_name)": {
				steps: [
					#checkoutCode,
					#dagger & {
						name: "Update"
						with: call: "update --token=env:GITHUB_TOKEN"
						env: {
							GITHUB_TOKEN: "${{ secrets.PAT }}"
						}
					},
				]
			}
		}
	},
]
