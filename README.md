# auto-qa

A Kubernetes-native QA orchestrator that automates the deploy-test-teardown lifecycle for integration and end-to-end test suites against real cluster workloads.

## How it works

For each discovered scenario, auto-qa:

1. Deploys the app stack defined in `scenario.yaml` using Kubernetes server-side apply
2. Waits for all deployed workloads to become ready (`Available=True`)
3. Runs test suites (respecting `priority` groups and `enabled` flags), each enabled suite as a `batch/v1 Job`; local test files are packaged into a ConfigMap and mounted automatically
4. Tears down the app stack — always, even on failure or timeout
5. Reports pass/fail with duration at the scenario and test suite level; exits `1` if anything failed

All scenarios are run concurrently.

## Scenario layout

A scenario is any subdirectory under `SCENARIOS_ROOT` that contains both required files:

```
scenarios/
└── my-scenario/
    ├── scenario.yaml  # Kubernetes resources to deploy (Deployments, Services, Ingress, etc.)
    ├── runner.yaml    # Test suite definitions (image, command, optional file injection)
    └── tests/         # Test source files (mounted into test containers via ConfigMap, can be placed anywhere)
        ├── test_api.py
        └── requirements.txt
```

See [`scenarios/basic-example/`](scenarios/basic-example/) for a complete, working example with annotated config files.

## Getting started

Prerequisites: Go 1.26+, a running Kubernetes cluster, and `kubectl` configured.

```sh
git clone <repo-url>
cd auto-qa

# Point at the bundled example scenario
export SCENARIOS_ROOT=./scenarios
export NAMESPACE=auto-qa   # namespace for test Jobs (created automatically)

go run ./cmd/auto-qa
```

Using [`just`](https://github.com/casey/just):

```sh
just run
```

## Running with Docker

```sh
just buildd   # builds the image: auto-qa

# Runs with your local kubeconfig mounted
just rund
```

Or manually:

```sh
docker build -t auto-qa .
docker run --rm \
  -e SCENARIOS_ROOT=/scenarios \
  -e NAMESPACE=auto-qa \
  -v ~/.kube/config:/root/.kube/config:ro \
  -v "$(pwd)/scenarios":/scenarios \
  auto-qa
```

## Configuration

See [`docs/configuration.md`](docs/configuration.md) for implementation details.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for local dev setup.
