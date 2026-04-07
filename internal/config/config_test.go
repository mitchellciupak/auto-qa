package config_test

import (
	"log/slog"
	"testing"
	"time"

	"auto-qa/internal/config"
)

// setenv sets one or more env vars for the duration of a test, restoring
// the originals when the test ends.
func setenv(t *testing.T, pairs ...string) {
	t.Helper()
	if len(pairs)%2 != 0 {
		t.Fatal("setenv: pairs must be even (key, value, key, value, ...)")
	}
	for i := 0; i < len(pairs); i += 2 {
		t.Setenv(pairs[i], pairs[i+1])
	}
}

// each test must call config.Reset() first so the sync.Once is fresh.

func TestLoad_HappyPath(t *testing.T) {
	config.Reset()
	setenv(t,
		"SCENARIOS_ROOT", "/tmp/senarios",
		"LOG_LEVEL", "debug",
		"NAMESPACE", "test-ns",
		"IMAGE", "my-image:v1",
		"TIMEOUT", "10m",
	)

	s, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if s.ScenariosRoot != "/tmp/senarios" {
		t.Errorf("ScenariosRoot: got %q, want %q", s.ScenariosRoot, "/tmp/senarios")
	}
	if s.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel: got %v, want %v", s.LogLevel, slog.LevelDebug)
	}
	if s.Namespace != "test-ns" {
		t.Errorf("Namespace: got %q, want %q", s.Namespace, "test-ns")
	}
	if s.Image != "my-image:v1" {
		t.Errorf("Image: got %q, want %q", s.Image, "my-image:v1")
	}
	if s.Timeout != 10*time.Minute {
		t.Errorf("Timeout: got %v, want %v", s.Timeout, 10*time.Minute)
	}
}

func TestLoad_Defaults(t *testing.T) {
	config.Reset()
	// Only set the required field; everything else should use defaults.
	setenv(t, "SCENARIOS_ROOT", "/tmp/senarios")

	s, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if s.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel default: got %v, want %v", s.LogLevel, slog.LevelInfo)
	}
	if s.Namespace != "auto-qa" {
		t.Errorf("Namespace default: got %q, want %q", s.Namespace, "auto-qa")
	}
	if s.Image != "busybox:latest" {
		t.Errorf("Image default: got %q, want %q", s.Image, "busybox:latest")
	}
	if s.Timeout != 5*time.Minute {
		t.Errorf("Timeout default: got %v, want %v", s.Timeout, 5*time.Minute)
	}
}

func TestLoad_MissingScenarioPath(t *testing.T) {
	config.Reset()
	// Explicitly unset SCENARIOS_ROOT (t.Setenv will restore it after the test).
	t.Setenv("SCENARIOS_ROOT", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing SCENARIOS_ROOT, got nil")
	}
}

func TestLoad_InvalidLogLevel(t *testing.T) {
	config.Reset()
	setenv(t,
		"SCENARIOS_ROOT", "/tmp/senarios",
		"LOG_LEVEL", "verbose", // not a valid slog level
	)

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid LOG_LEVEL, got nil")
	}
}

func TestLoad_InvalidTimeout(t *testing.T) {
	config.Reset()
	setenv(t,
		"SCENARIOS_ROOT", "/tmp/senarios",
		"TIMEOUT", "not-a-duration",
	)

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for invalid TIMEOUT, got nil")
	}
}

func TestLoad_Singleton(t *testing.T) {
	config.Reset()
	setenv(t, "SCENARIOS_ROOT", "/tmp/senarios")

	first, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	second, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}

	if first != second {
		t.Error("Load() returned different pointers; singleton violated")
	}
}

func TestLoad_AllLogLevels(t *testing.T) {
	cases := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"WARN", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			config.Reset()
			setenv(t,
				"SCENARIOS_ROOT", "/tmp/senarios",
				"LOG_LEVEL", tc.input,
			)
			s, err := config.Load()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if s.LogLevel != tc.want {
				t.Errorf("LogLevel: got %v, want %v", s.LogLevel, tc.want)
			}
		})
	}
}

func TestMustLoad_Panics(t *testing.T) {
	config.Reset()
	t.Setenv("SCENARIOS_ROOT", "") // missing required field

	defer func() {
		if r := recover(); r == nil {
			t.Error("MustLoad() did not panic on missing SCENARIOS_ROOT")
		}
	}()
	config.MustLoad()
}
