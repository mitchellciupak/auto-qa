# Configuration

All configuration is loaded from environment variables in `internal/config/config.go`.

## Pattern

Defaults are set as a struct literal at the top of the private `load()` function in `internal/config/config.go`. To find them, look for the `&ApplicationSettings{...}` literal at the start of `load()`. Each env var then conditionally overwrites its field only when non-empty. All validation errors are accumulated and returned together via `errors.Join`.

`config.Load()` uses `sync.Once` so env vars are read exactly once per process. `config.MustLoad()` is the panic-on-error variant used at startup.
