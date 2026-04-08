# Runner Configuration

Each scenario directory must contain a `runner.yaml` file that defines the test suites to run against the deployed app stack.

## Top-level fields

| Field | Type | Required | Description |
|---|---|---|---|
| `timeout` | string | no | Maximum wall-clock time for this scenario, as a Go duration string (e.g. `"10m"`, `"90s"`). When omitted, the runner falls back to the global `TIMEOUT` environment variable. |
| `test_suites` | list | yes | One or more test suite definitions. At least one test suite is required. |

## Test Suite fields

| Field | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Unique identifier for this test suite within the scenario. Used in logs and the final summary. |
| `enabled` | boolean | no | Enables or disables this test suite. Defaults to `true` when omitted. Set `false` to skip the suite without removing its config. |
| `image` | string | yes | Container image for the test runner (e.g. `python:3.13-slim`, `mcr.microsoft.com/playwright:v1.59.1-noble`). |
| `command` | list | no* | Overrides the container entrypoint. Maps to the Kubernetes `command` field. At least one of `command` or `args` must be set. |
| `args` | list | no* | Arguments passed to the command. Maps to the Kubernetes `args` field. At least one of `command` or `args` must be set. |
| `env` | list | no | Additional environment variables injected into the test container. Each entry follows the Kubernetes `EnvVar` schema (`name`, `value`). |
| `volumeMounts` | list | no | Kubernetes `VolumeMount` entries to mount into the test container. |
| `volumes` | list | no | Kubernetes `Volume` entries attached to the pod. |
| `files` | list | no | Local files to make available inside the container. The runner creates a ConfigMap from these files and injects the volume and mounts automatically. See [File injection](#file-injection). |
| `priority` | integer | no | Controls concurrent execution grouping. See [Priority and concurrency](#priority-and-concurrency). |

\* At least one of `command` or `args` is required per test suite.

## Priority and concurrency

By default, test suites run **sequentially** in declaration order with fail-fast behaviour — the first failure stops the run. The `priority` field opts test suites into concurrent execution.

### Rules

- `priority` must be a non-negative integer (`>= 0`).
- Test suites that share the same `priority` value form a **group** and run **concurrently** with each other.
- Groups execute in **ascending priority order**. All test suites in group N must complete before group N+1 starts.
- **Fail-fast between groups**: if any test suite in a group fails, every in-flight test suite in that group is still allowed to finish, but the next group is not started.
- Test suites with **no `priority` set** are unaffected by the priority system. They run sequentially in declaration order after all explicit-priority groups have completed. The original fail-fast behaviour is preserved for this tail.

### Execution phases

```
Phase 1 — explicit-priority groups (ascending order, concurrent within group):
  Group 0  →  [suite-a, suite-b]  run concurrently
  Group 1  →  [suite-c]           runs after group 0 passes

Phase 2 — sequential tail (declaration order, fail-fast):
  suite-d  →  runs after all explicit groups pass
  suite-e  →  runs after suite-d passes
```

### Example

```yaml
timeout: 15m

test_suites:
  # Group 0: unit tests and linting run concurrently.
  - name: unit-tests
    enabled: true
    image: golang:1.23
    command: ["go"]
    args: ["test", "./..."]
    priority: 0

  - name: lint
    enabled: true
    image: golangci/golangci-lint:v1.61
    args: ["golangci-lint", "run"]
    priority: 0

  # Group 1: integration tests start only after group 0 passes.
  - name: integration-tests
    enabled: false
    image: python:3.13-slim
    command: ["sh", "-c"]
    args: ["pytest tests/integration/ -v"]
    priority: 1

  # No priority: e2e runs sequentially after all priority groups finish.
  - name: e2e
    image: mcr.microsoft.com/playwright:v1.59.1-noble
    command: ["sh", "-c"]
    args: ["npx playwright test"]
```

In this example the execution order is:

1. `unit-tests` and `lint` run at the same time (group 0).
2. `integration-tests` is skipped because `enabled: false`.
3. `e2e` runs sequentially after the explicit-priority groups complete.

## File injection

The `files` field lets you mount local files into the test container without manually writing volumes or ConfigMaps. The runner reads each listed file, bundles them into a single ConfigMap, and injects the corresponding `volume` and `volumeMount` entries automatically.

### File entry fields

| Field | Type | Required | Description |
|---|---|---|---|
| `src` | string | yes | Path to the file, relative to the scenario directory. |
| `mountPath` | string | yes | Absolute path where the file appears inside the container. |

### Simple Example

```yaml
test_suites:
  - name: api-tests
    image: python:3.13-slim
    command: ["sh", "-c"]
    args: ["pip install -r /requirements.txt --quiet && pytest /tests/"]
    files:
      - src: tests/test_api.py
        mountPath: /tests/test_api.py
      - src: tests/requirements.txt
        mountPath: /requirements.txt
```

## Full reference example

```yaml
timeout: 10m

test_suites:
  - name: api-tests
    image: python:3.13-slim
    command: ["sh", "-c"]
    args:
      - "pip install -r /requirements.txt --quiet && pytest /tests/api_test.py -v --tb=short"
    env:
      - name: API_BASE_URL
        value: "http://my-service.my-namespace.svc.cluster.local"
    files:
      - src: tests/test_api.py
        mountPath: /tests/api_test.py
      - src: tests/requirements.txt
        mountPath: /requirements.txt
    priority: 0

  - name: ui-tests
    image: mcr.microsoft.com/playwright:v1.59.1-noble
    command: ["sh", "-c"]
    args:
      - "cd /playwright && npm ci --quiet && npx playwright test --reporter=line"
    env:
      - name: UI_BASE_URL
        value: "http://my-ui.my-namespace.svc.cluster.local"
    files:
      - src: tests/package.json
        mountPath: /playwright/package.json
      - src: tests/package-lock.json
        mountPath: /playwright/package-lock.json
      - src: tests/playwright.config.ts
        mountPath: /playwright/playwright.config.ts
      - src: tests/ui_test.spec.ts
        mountPath: /playwright/tests/ui_test.spec.ts
    priority: 1
```
