package scenario

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

const runnerConfigFileName = "runner.yaml"

// TestSuiteFileSpec describes a single file to mount into a test suite's container.
// The runner synthesizes a ConfigMap from all files declared on the suite,
// then injects the matching volume and volumeMount automatically.
type TestSuiteFileSpec struct {
	// Src is the path to the file, relative to the scenario directory. Required.
	Src string `json:"src"`
	// MountPath is the exact file path the file will appear at inside the
	// container (e.g. "/tests/example_pytest_test.py"). Required.
	MountPath string `json:"mountPath"`
}

// TestSuiteSpec describes a single test suite within a scenario.
type TestSuiteSpec struct {
	// Name identifies the suite in logs and the final summary. Required.
	Name string `json:"name"`
	// Enabled controls whether this suite should run. When omitted, the suite
	// is enabled by default. Set to false to skip this suite.
	Enabled *bool `json:"enabled,omitempty"`
	// Image is the container image for the test runner. Required.
	Image string `json:"image"`
	// Command overrides the container entrypoint. At least one of Command or
	// Args must be set. Maps to the K8s container command field.
	Command []string `json:"command,omitempty"`
	// Args are passed to the container command. Maps to the K8s container
	// args field. Can be used alone (image entrypoint + args) or combined
	// with Command.
	Args []string `json:"args,omitempty"`
	// Env holds additional environment variables to inject into the test container.
	Env []corev1.EnvVar `json:"env,omitempty"`
	// VolumeMounts are mounted into the test container.
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
	// Volumes are attached to the pod and available for mounting.
	Volumes []corev1.Volume `json:"volumes,omitempty"`
	// Files lists local files to make available inside the container. The
	// runner creates a ConfigMap from these files and injects the volume and
	// per-file volumeMounts automatically — no manual volumes/volumeMounts
	// entries are needed for file-backed content.
	Files []TestSuiteFileSpec `json:"files,omitempty"`
	// Priority controls concurrent execution grouping. Suites that share the
	// same priority value run concurrently with each other. Groups are
	// executed in ascending order — all suites in priority group N must
	// complete before group N+1 starts. If any suite in a group fails,
	// subsequent groups are not started (fail-fast between groups).
	//
	// Suites that omit priority run sequentially in declaration order after
	// all explicit-priority groups have finished, preserving the default
	// fail-fast sequential behaviour.
	//
	// Priority must be >= 0. Optional.
	Priority *int `json:"priority,omitempty"`
}

// RunnerConfig holds the test suite definitions for a scenario.
type RunnerConfig struct {
	// Timeout is the maximum wall-clock time allowed for this scenario,
	// expressed as a Go duration string (e.g. "10m", "90s"). Optional.
	// When omitted the Runner's DefaultTimeout is used.
	Timeout string `json:"timeout,omitempty"`
	// TimeoutDur is the parsed form of Timeout. Populated by validate().
	// Zero means "not set; use the runner default".
	TimeoutDur time.Duration
	TestSuites []TestSuiteSpec `json:"test_suites"`
}

// LoadRunnerConfig reads and validates the runner.yaml at path.
func LoadRunnerConfig(path string) (*RunnerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading runner config %q: %w", path, err)
	}

	var cfg RunnerConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing runner config %q: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid runner config %q: %w", path, err)
	}

	return &cfg, nil
}

// validate checks all required fields across all suites.
func (c *RunnerConfig) validate() error {
	if len(c.TestSuites) == 0 {
		return errors.New("test_suites list is empty; at least one suite is required")
	}

	var errs []error

	// Validate and parse the optional Timeout field.
	if c.Timeout != "" {
		d, err := time.ParseDuration(c.Timeout)
		if err != nil {
			errs = append(errs, fmt.Errorf("timeout %q is not a valid duration (e.g. \"10m\", \"90s\"): %w", c.Timeout, err))
		} else if d <= 0 {
			errs = append(errs, fmt.Errorf("timeout %q must be positive", c.Timeout))
		} else {
			c.TimeoutDur = d
		}
	}

	seen := make(map[string]bool, len(c.TestSuites))

	for i, s := range c.TestSuites {
		prefix := fmt.Sprintf("test_suite[%d]", i)

		if s.Name == "" {
			errs = append(errs, fmt.Errorf("%s: name is required", prefix))
		} else if seen[s.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate test suite name %q", prefix, s.Name))
		} else {
			seen[s.Name] = true
		}

		if s.Image == "" {
			errs = append(errs, fmt.Errorf("%s: image is required", prefix))
		}

		if len(s.Command) == 0 && len(s.Args) == 0 {
			errs = append(errs, fmt.Errorf("%s: at least one of command or args must be set", prefix))
		}

		if s.Priority != nil && *s.Priority < 0 {
			errs = append(errs, fmt.Errorf("%s: priority must be >= 0, got %d", prefix, *s.Priority))
		}

		for j, f := range s.Files {
			fprefix := fmt.Sprintf("%s.files[%d]", prefix, j)
			if f.Src == "" {
				errs = append(errs, fmt.Errorf("%s: src is required", fprefix))
			}
			if f.MountPath == "" {
				errs = append(errs, fmt.Errorf("%s: mountPath is required", fprefix))
			} else {
				if !strings.HasPrefix(f.MountPath, "/") {
					errs = append(errs, fmt.Errorf("%s: mountPath must be an absolute path starting with '/', got %q", fprefix, f.MountPath))
				}
				if strings.IndexByte(f.MountPath, 0) >= 0 {
					errs = append(errs, fmt.Errorf("%s: mountPath must not contain NUL bytes", fprefix))
				}
			}
		}
	}

	return errors.Join(errs...)
}
