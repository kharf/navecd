package build

import (
	github "cue.dev/x/githubactions"
)

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
	name: string | *"Checkout code"
	uses: "actions/checkout@v4.2.2"
	with: {
		[string]: string | number | bool
		token:    "${{ secrets.PAT }}"
	}
}

#setupGo: {
	name: "Setup Go"
	uses: "actions/setup-go@v5.5.0"
	with: {
		"go-version-file":       "go.mod"
		"cache-dependency-path": "go.sum"
	}
}

#dagger: {
	name: string
	uses: "dagger/dagger-for-github@8.0.0"
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
		_name: "E2E"
		workflow: github.#Workflow & {
			on: {
				workflow_dispatch: null
				push: {
					branches: [
						"main",
					]
					"tags-ignore": [
						"*",
					]
				}
				pull_request: {
					branches: [
						"main",
					]
					"tags-ignore": [
						"*",
					]
				}
			}

			jobs: "\(_name)": {
				strategy: matrix: cluster: [
					{
						name:  "e2e1"
						image: "kindest/node:v1.33.1@sha256:050072256b9a903bd914c0b2866828150cb229cea0efe5892e2b644d5dd3b34f"
					},
					{
						name:  "e2e2"
						image: "kindest/node:v1.32.5@sha256:e3b2327e3a5ab8c76f5ece68936e4cafaa82edf58486b769727ab0b3b97a5b0d"
					},
					{
						name:  "e2e3"
						image: "kindest/node:v1.31.9@sha256:b94a3a6c06198d17f59cca8c6f486236fa05e2fb359cbd75dabbfc348a10b211"
					},
				]
				steps: [
					#checkoutCode & {
						with: {
							ref: "${{ github.head_ref || github.ref_name }}"
						}
					},
					{
						name: "Create Kubernetes Cluster"
						uses: "helm/kind-action@v1.12.0"
						with: {
							cluster_name: "${{ matrix.cluster.name }}"
							node_image:   "${{ matrix.cluster.image }}"
						}
					},
					#dagger & {
						name: "Build"
						with: call: "build --source=. export --path=./dist"
					},
					{
						name: "Move binary"
						run:  "mv ./dist/cli_linux_amd64_v1/navecd /usr/local/bin/navecd"
					},
					#checkoutCode & {
						name: "Clone E2E Repository"
						with: {
							repository: "kharf/navecd-e2e"
							path:       "e2e"
						}
					},
					{
						name: "Init Navecd"
						run: """
						navecd init github.com/kharf/navecd-e2e
						"""
					},
					{
						name: "Install Navecd"
						run:  "navecd install -u git@github.com:kharf/navecd-e2e.git -t ${{ secrets.E2E_TOKEN}} -b ${{ matrix.cluster.name }} --name ${{ matrix.cluster.name }}"
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
