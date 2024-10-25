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
	uses: "actions/checkout@v4.2.1"
	with: {
		[string]: string | number | bool
		token:    "${{ secrets.PAT }}"
	}
}

#setupGo: {
	name: "Setup Go"
	uses: "actions/setup-go@v5.1.0"
	with: {
		"go-version-file":       "build/go.mod"
		"cache-dependency-path": "build/go.sum"
	}
}

#pipeline: {
	name:                string
	run:                 string
	"working-directory": "./build"
	env?: {
		[string]: string | number | bool
	}
}

workflows: [
	#workflow & {
		_name: "pr-verification"
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
					#setupGo,
					#pipeline & {
						name: "Verification Pipeline"
						run:  "go run cmd/verification/main.go"
						env: {
							GH_PAT: "${{ secrets.PAT }}"
						}
					},
				]
			}
		}
	},
	#workflow & {
		_name: "test"
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
					#setupGo,
					#pipeline & {
						name: "Test Pipeline"
						run:  "go run cmd/test/main.go"
						env: {
							GITHUB_TOKEN: "${{ secrets.PAT }}"
						}
					},
				]
			}
		}
	},
	#workflow & {
		_name: "publish"
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
					#pipeline & {
						name: "Publish Pipeline"
						run:  "go run cmd/publish/main.go ${{ inputs.version }} ${{ inputs.prev-version }}"
						env: {
							GITHUB_TOKEN: "${{ secrets.PAT }}"
						}
					},
				]
			}
		}
	},
	#workflow & {
		_name: "update"
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
					#setupGo,
					#pipeline & {
						name: "Update Pipeline"
						run:  "go run cmd/update/main.go"
						env: {
							RENOVATE_TOKEN: "${{ secrets.PAT }}"
						}
					},
				]
			}
		}
	},
]
