package build

import (
	github "cue.dev/x/githubactions"
)

#workflow: github.#Workflow & {
	name:        string
	permissions: "read-all"
	jobs: [Name=string]: {
		_name:     Name
		"runs-on": "ubuntu-latest"
		steps: [
			#checkoutCode,
			...,
		]
	}
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
		name: "PR-Conformance"
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

		jobs: "Reconcile-Workflows": steps: [
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
		]

		jobs: "Test": {
			steps: [
				#checkoutCode & {
					with: {
						ref: "${{ github.head_ref || github.ref_name }}"
					}
				},
				#dagger & {
					name: "Test"
					with: call: "test --source=."
				},
			]
		}
	},
	#workflow & {
		name: "E2E"
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
		}

		jobs: Prepare: steps: [
			#checkoutCode & {
				name: "Checkout E2E Repository"
				with: {
					repository: "kharf/navecd-e2e"
					path:       "./e2e"
				}
			},
			{
				name:                "Clean"
				"working-directory": "./e2e"
				env: GH_TOKEN: "${{ secrets.PAT }}"
				run: "gh repo deploy-key list --json id -q '.[].id' | xargs -I {} gh repo deploy-key delete {}"
			},
		]

		jobs: "E2E": {
			needs: jobs.Prepare._name
			strategy: {
				"fail-fast": false
				matrix: cluster: [
					{
						name:  "latest"
						image: "kindest/node:v1.33.1@sha256:050072256b9a903bd914c0b2866828150cb229cea0efe5892e2b644d5dd3b34f"
					},
					{
						name:  "previous"
						image: "kindest/node:v1.32.5@sha256:e3b2327e3a5ab8c76f5ece68936e4cafaa82edf58486b769727ab0b3b97a5b0d"
					},
					{
						name:  "legacy"
						image: "kindest/node:v1.31.9@sha256:b94a3a6c06198d17f59cca8c6f486236fa05e2fb359cbd75dabbfc348a10b211"
					},
				]
			}
			steps: [
				#checkoutCode & {
					with: {
						ref: "${{ github.head_ref || github.ref_name }}"
					}
				},
				{
					uses: "actions/setup-go@v5.5.0"
				},
				{
					uses: "cue-lang/setup-cue@v1.0.0"
				},
				{
					name: "Create Kubernetes Cluster"
					uses: "helm/kind-action@v1.12.0"
					id:   "kind"
					with: {
						cluster_name:           "${{ matrix.cluster.name }}"
						node_image:             "${{ matrix.cluster.image }}"
						registry:               true
						registry_name:          "navecdregistry"
						registry_port:          5001
						registry_enable_delete: true
					}
				},
				#dagger & {
					name: "Build"
					with: call: "build --source=. export --path=./dist"
				},
				{
					name: "Publish locally"
					run: """
						mv ./dist/cli_linux_amd64_v1/navecd /usr/local/bin/navecd
						export CUE_REGISTRY=${{ steps.kind.outputs.LOCAL_REGISTRY }}+insecure
						echo "CUE_REGISTRY=$CUE_REGISTRY" >> "$GITHUB_ENV"
						cd schema
						cue mod publish v0.0.0-dev
						cd ..
						docker build . -t ${{ steps.kind.outputs.LOCAL_REGISTRY }}/navecd:0.0.0-dev
						docker push ${{ steps.kind.outputs.LOCAL_REGISTRY }}/navecd:0.0.0-dev
						"""
				},
				#checkoutCode & {
					name: "Checkout E2E Repository"
					with: {
						repository: "kharf/navecd-e2e"
						path:       "./e2e"
					}
				},
				{
					name:                "Switch branch"
					"working-directory": "./e2e"
					run: """
						git switch -c ${{ matrix.cluster.name }}
						git push -u origin ${{ matrix.cluster.name }}
						"""
				},
				{
					name:                "Init Navecd"
					"working-directory": "./e2e"
					run: """
						navecd init github.com/kharf/navecd-e2e --image=${{ steps.kind.outputs.LOCAL_REGISTRY }}/navecd
						"""
				},
				{
					name:                "Install Navecd"
					"working-directory": "./e2e"
					run:                 "navecd install -u git@github.com:kharf/navecd-e2e.git -t ${{ secrets.E2E_TOKEN}} -b ${{ matrix.cluster.name }} --name ${{ matrix.cluster.name }}"
				},
				{
					name: "Test Installation"
					run:  "go test -v ./tests/e2e/ -run TestInstallation -count=1"
				},
				{
					name: "Navecd Logs"
					"if": "always()"
					run:  "kubectl logs -n navecd-system deploy/project-controller-primary"
				},
				{
					name: "Navecd describe"
					"if": "always()"
					run:  "kubectl describe -n navecd-system deploy/project-controller-primary"
				},
			]
		}
	},
	#workflow & {
		_name: "Test"
		name:  _name
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
	},
	#workflow & {
		_name: "Release"
		name:  _name
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
	},
	#workflow & {
		_name: "Update"
		name:  _name
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
	},
]
