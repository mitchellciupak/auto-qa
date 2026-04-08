package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"auto-qa/internal/constants"
)

// ApplicationSettings holds all runtime configuration for the application.
// It is populated once from environment variables and cached for the lifetime
// of the process.
type ApplicationSettings struct {
	// LogLevel controls the minimum log severity emitted. Sourced from LOG_LEVEL.
	// Valid values: "debug", "info", "warn", "error". Defaults to "info".
	LogLevel slog.Level

	// ScenariosRoot is the filesystem path to the top-level scenarios directory.
	// Each immediate subdirectory that contains both scenario.yaml and runner.yaml
	// is treated as a scenario to run. Sourced from SCENARIOS_ROOT. Required.
	ScenariosRoot string

	// Kubeconfig is the path to the kubeconfig file.
	// Sourced from KUBECONFIG. Defaults to ~/.kube/config via client-go conventions.
	Kubeconfig string

	// Namespace is the Kubernetes namespace to run test jobs in.
	// Sourced from NAMESPACE. Defaults to "auto-qa".
	Namespace string

	// Timeout is the default maximum time allowed for an individual scenario run
	// when that scenario does not specify its own timeout in runner.yaml.
	// Sourced from TIMEOUT as a Go duration string (e.g. "5m", "30s").
	// Defaults to 5 minutes.
	Timeout time.Duration

	// ReportPath is the filesystem path where a JSON results report will be
	// written after all scenarios complete. Sourced from REPORT_PATH.
	// When empty (the default), no report file is written.
	ReportPath string
}

var (
	instance *ApplicationSettings
	once     sync.Once
	loadErr  error
)

// Load returns the singleton ApplicationSettings, loading it from environment
// variables on the first call. Subsequent calls return the cached instance.
// An error is returned if required variables are missing or values are invalid.
func Load() (*ApplicationSettings, error) {
	once.Do(func() {
		instance, loadErr = load()
	})
	return instance, loadErr
}

// MustLoad is like Load but panics on error. Use in main() where a config
// failure is unrecoverable.
func MustLoad() *ApplicationSettings {
	s, err := Load()
	if err != nil {
		panic(fmt.Sprintf("config: failed to load application settings: %v", err))
	}
	return s
}

// load reads environment variables and builds a new ApplicationSettings.
func load() (*ApplicationSettings, error) {
	// Defaults placed here
	s := &ApplicationSettings{
		LogLevel:  slog.LevelInfo,
		Namespace: constants.DefaultNamespace,
		Timeout:   5 * time.Minute,
	}

	var errs []error

	// LOG_LEVEL — optional, defaults to info
	if raw := os.Getenv("LOG_LEVEL"); raw != "" {
		if err := s.LogLevel.UnmarshalText([]byte(raw)); err != nil {
			errs = append(errs, fmt.Errorf("LOG_LEVEL %q is not valid (use debug|info|warn|error): %w", raw, err))
		}
	}

	// SCENARIOS_ROOT — required; top-level directory containing all scenario subdirs
	s.ScenariosRoot = os.Getenv("SCENARIOS_ROOT")
	if s.ScenariosRoot == "" {
		errs = append(errs, errors.New("SCENARIOS_ROOT is required but not set"))
	}

	// KUBECONFIG — optional, empty string defers to client-go's default resolution
	s.Kubeconfig = os.Getenv("KUBECONFIG")

	// NAMESPACE — optional
	if v := os.Getenv("NAMESPACE"); v != "" {
		s.Namespace = v
	}

	// TIMEOUT — optional, must be a valid positive Go duration string
	if raw := os.Getenv("TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("TIMEOUT %q is not a valid duration (e.g. \"5m\", \"30s\"): %w", raw, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("TIMEOUT %q must be greater than 0", raw))
		} else {
			s.Timeout = d
		}
	}

	// REPORT_PATH — optional; when set, a JSON report is written to this path
	s.ReportPath = os.Getenv("REPORT_PATH")

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return s, nil
}

// Reset clears the cached singleton. Intended for use in tests only.
func Reset() {
	once = sync.Once{}
	instance = nil
	loadErr = nil
}
