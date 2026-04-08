# Configuration

All configuration is loaded from environment variables in `internal/config/config.go`.

## Pattern

Defaults are set as a struct literal at the top of the private `load()` function in `internal/config/config.go`. To find them, look for the `&ApplicationSettings{...}` literal at the start of `load()`. Each env var then conditionally overwrites its field only when non-empty. All validation errors are accumulated and returned together via `errors.Join`.

`config.Load()` uses `sync.Once` so env vars are read exactly once per process. `config.MustLoad()` is the panic-on-error variant used at startup.

## Environment Variables

| Variable | Required | Default | Description |
|---|---|---|---|
| `SCENARIOS_ROOT` | yes | — | Path to the top-level scenarios directory. Every immediate subdirectory that contains a `scenario.yaml` and a `runner.yaml` is treated as a scenario to run. |
| `KUBECONFIG` | no | `~/.kube/config` | Path to kubeconfig file. Falls back to client-go's in-cluster detection if empty. |
| `NAMESPACE` | no | `auto-qa` | Kubernetes namespace where test Jobs are created. |
| `TIMEOUT` | no | `5m` | Default timeout for each scenario (Go duration string, e.g. `10m`, `90s`). Individual scenarios can override this in `runner.yaml`. |
| `REPORT_PATH` | no | — | Path where the run report is written. |
| `LOG_LEVEL` | no | `info` | Minimum log severity: `debug`, `info`, `warn`, or `error`. |
