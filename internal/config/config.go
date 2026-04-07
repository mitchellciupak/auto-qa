package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// ApplicationSettings holds all runtime configuration for the application.
// It is populated once from environment variables and cached for the lifetime
// of the process.
type ApplicationSettings struct {
	// LogLevel controls the minimum log severity emitted. Sourced from LOG_LEVEL.
	// Valid values: "debug", "info", "warn", "error". Defaults to "info".
	LogLevel slog.Level

	// ScenarioPath is the filesystem path to the scenarios directory.
	// Sourced from SCENARIO_PATH. Required.
	ScenarioPath string

	// Kubeconfig is the path to the kubeconfig file.
	// Sourced from KUBECONFIG. Defaults to ~/.kube/config via client-go conventions.
	Kubeconfig string

	// Namespace is the Kubernetes namespace to run test jobs in.
	// Sourced from NAMESPACE. Defaults to "auto-qa".
	Namespace string

	// Image is the container image used for the test runner.
	// Sourced from IMAGE. Defaults to "busybox:latest".
	Image string

	// Timeout is the maximum time to wait for a job to complete.
	// Sourced from TIMEOUT as a Go duration string (e.g. "5m", "30s").
	// Defaults to 5 minutes.
	Timeout time.Duration
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
	s := &ApplicationSettings{
		LogLevel:  slog.LevelInfo,
		Namespace: "auto-qa",
		Image:     "busybox:latest",
		Timeout:   5 * time.Minute,
	}

	var errs []error

	// LOG_LEVEL — optional, defaults to info
	if raw := os.Getenv("LOG_LEVEL"); raw != "" {
		if err := s.LogLevel.UnmarshalText([]byte(raw)); err != nil {
			errs = append(errs, fmt.Errorf("LOG_LEVEL %q is not valid (use debug|info|warn|error): %w", raw, err))
		}
	}

	// SCENARIO_PATH — required
	s.ScenarioPath = os.Getenv("SCENARIO_PATH")
	if s.ScenarioPath == "" {
		errs = append(errs, errors.New("SCENARIO_PATH is required but not set"))
	}

	// KUBECONFIG — optional, empty string defers to client-go's default resolution
	s.Kubeconfig = os.Getenv("KUBECONFIG")

	// NAMESPACE — optional
	if v := os.Getenv("NAMESPACE"); v != "" {
		s.Namespace = v
	}

	// IMAGE — optional
	if v := os.Getenv("IMAGE"); v != "" {
		s.Image = v
	}

	// TIMEOUT — optional, must be a valid Go duration string
	if raw := os.Getenv("TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("TIMEOUT %q is not a valid duration (e.g. \"5m\", \"30s\"): %w", raw, err))
		} else {
			s.Timeout = d
		}
	}

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
