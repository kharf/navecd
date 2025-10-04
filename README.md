<br>
<div align="center">
	<img src="./docs/navecd-light.png#gh-light-mode-only">
	<img src="/docs/navecd.png#gh-dark-mode-only">
  <p align="center">
		<strong>A Type Safe Declarative Continuous Delivery Toolkit For Kubernetes</strong>
  </p>
  <p>
		<img src="https://img.shields.io/github/actions/workflow/status/kharf/navecd/test.yaml"/>
		<a href="https://goreportcard.com/report/github.com/kharf/navecd"><img src="https://goreportcard.com/badge/github.com/kharf/navecd"/></a>
  </p>
</div>
<br>

## What is GitOps?
GitOps is a way of implementing Continuous Deployment for cloud native applications by having a repository that contains declarative descriptions of the desired infrastructure and applications and an automated process to reconcile the production environment with the desired state in the repository.

## Why Navecd?
Traditional GitOps tools often rely on YAML for configuration, which can lead to verbosity and complexity.
Navecd leverages [CUE](https://cuelang.org/), a type safe configuration language with a more concise and expressive syntax and the benefits of general-purpose programming languages,
making it easier to define and maintain your desired cluster state.

![Overview](./docs/navecd-flow.png)

## Documentation
To learn more about Navecd, visit [navecd.dev](https://navecd.dev/documentation/overview/)

## Contributions

We welcome contributions! To contribute to Navecd, follow these steps:

1. Fork the repository.
2. Create a new branch for your feature or bug fix.
3. Make your changes.
4. Install Dagger (https://dagger.io/)
5. Create tests and run them in a containerized environment via Dagger with:
    ```bash
    # Run all tests
    dagger call test --source=.

    # Or run a specific test
    dagger call test --source=. --pkg=pkg/mypkg --test=MyTest
    ```
6. Create a PR.
7. Ensure that your code passes the CI/CD checks.

For more information, see [CONTRIBUTING.md]().
