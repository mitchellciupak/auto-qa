package scenario_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"auto-qa/internal/scenario"
)

func writeRunnerConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "runner.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write runner.yaml: %v", err)
	}
	return p
}

func TestLoadRunnerConfig_HappyPath(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest", "tests/"]
  - name: e2e
    image: playwright:latest
    args: ["npx", "playwright", "test"]
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.TestSuites) != 2 {
		t.Fatalf("len(TestSuites): got %d, want 2", len(cfg.TestSuites))
	}

	s0 := cfg.TestSuites[0]
	if s0.Name != "api-tests" {
		t.Errorf("TestSuites[0].Name: got %q, want %q", s0.Name, "api-tests")
	}
	if s0.Image != "pytest:latest" {
		t.Errorf("TestSuites[0].Image: got %q, want %q", s0.Image, "pytest:latest")
	}
	if len(s0.Command) != 2 || s0.Command[0] != "pytest" {
		t.Errorf("TestSuites[0].Command: got %v", s0.Command)
	}

	s1 := cfg.TestSuites[1]
	if s1.Name != "e2e" {
		t.Errorf("TestSuites[1].Name: got %q, want %q", s1.Name, "e2e")
	}
	if len(s1.Args) != 3 || s1.Args[0] != "npx" {
		t.Errorf("TestSuites[1].Args: got %v", s1.Args)
	}
}

func TestLoadRunnerConfig_CommandAndArgsTogether(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: mixed
    image: gotest:latest
    command: ["go"]
    args: ["test", "./...", "-v"]
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := cfg.TestSuites[0]
	if len(s.Command) != 1 || s.Command[0] != "go" {
		t.Errorf("Command: got %v", s.Command)
	}
	if len(s.Args) != 3 || s.Args[0] != "test" {
		t.Errorf("Args: got %v", s.Args)
	}
}

func TestLoadRunnerConfig_MissingFile(t *testing.T) {
	_, err := scenario.LoadRunnerConfig("/nonexistent/runner.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadRunnerConfig_InvalidYAML(t *testing.T) {
	p := writeRunnerConfig(t, "this: is: {{invalid")
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestLoadRunnerConfig_EmptySuites(t *testing.T) {
	p := writeRunnerConfig(t, "test_suites: []\n")
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error for empty suites, got nil")
	}
}

func TestLoadRunnerConfig_MissingSuiteName(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - image: pytest:latest
    command: ["pytest"]
`)
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error for missing test suite name, got nil")
	}
}

func TestLoadRunnerConfig_MissingSuiteImage(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: tests
    command: ["pytest"]
`)
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error for missing test suite image, got nil")
	}
}

func TestLoadRunnerConfig_MissingCommandAndArgs(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: tests
    image: pytest:latest
`)
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error when both command and args are absent, got nil")
	}
}

func TestLoadRunnerConfig_Enabled_OmittedIsNil(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: tests
    image: pytest:latest
    command: ["pytest"]
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TestSuites[0].Enabled != nil {
		t.Errorf("Enabled: expected nil when omitted, got %v", cfg.TestSuites[0].Enabled)
	}
}

func TestLoadRunnerConfig_Enabled_True(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: tests
    image: pytest:latest
    command: ["pytest"]
    enabled: true
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TestSuites[0].Enabled == nil || !*cfg.TestSuites[0].Enabled {
		t.Errorf("Enabled: got %v, want true", cfg.TestSuites[0].Enabled)
	}
}

func TestLoadRunnerConfig_Enabled_False(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: tests
    image: pytest:latest
    command: ["pytest"]
    enabled: false
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TestSuites[0].Enabled == nil || *cfg.TestSuites[0].Enabled {
		t.Errorf("Enabled: got %v, want false", cfg.TestSuites[0].Enabled)
	}
}

func TestLoadRunnerConfig_DuplicateSuiteNames(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: same
    image: pytest:latest
    command: ["pytest"]
  - name: same
    image: gotest:latest
    command: ["go", "test"]
`)
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error for duplicate test suite names, got nil")
	}
}

func TestLoadRunnerConfig_AccumulatesMultipleErrors(t *testing.T) {
	// Two suites both missing name and command/args — error should mention both.
	p := writeRunnerConfig(t, `
test_suites:
  - image: pytest:latest
  - image: gotest:latest
`)
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error for multiple invalid test suites, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestSuite-level files
// ---------------------------------------------------------------------------

func TestLoadRunnerConfig_SuiteFiles_HappyPath(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
    files:
      - src: tests/test_api.py
        mountPath: /tests/test_api.py
      - src: tests/requirements.txt
        mountPath: /tests/requirements.txt
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	s := cfg.TestSuites[0]
	if len(s.Files) != 2 {
		t.Fatalf("Files: got %d, want 2", len(s.Files))
	}
	if s.Files[0].Src != "tests/test_api.py" {
		t.Errorf("Files[0].Src: got %q, want %q", s.Files[0].Src, "tests/test_api.py")
	}
	if s.Files[0].MountPath != "/tests/test_api.py" {
		t.Errorf("Files[0].MountPath: got %q, want %q", s.Files[0].MountPath, "/tests/test_api.py")
	}
	if s.Files[1].Src != "tests/requirements.txt" {
		t.Errorf("Files[1].Src: got %q, want %q", s.Files[1].Src, "tests/requirements.txt")
	}
}

func TestLoadRunnerConfig_SuiteFiles_OmittedIsValid(t *testing.T) {
	// A suite with no files field is perfectly valid.
	p := writeRunnerConfig(t, `
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.TestSuites[0].Files) != 0 {
		t.Errorf("Files: got %d entries, want 0", len(cfg.TestSuites[0].Files))
	}
}

func TestLoadRunnerConfig_SuiteFiles_MissingSrc(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
    files:
      - mountPath: /tests/test_api.py
`)
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error for file entry missing src, got nil")
	}
}

func TestLoadRunnerConfig_SuiteFiles_MissingMountPath(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
    files:
      - src: tests/test_api.py
`)
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error for file entry missing mountPath, got nil")
	}
}

func TestLoadRunnerConfig_SuiteFiles_MountPathMustBeAbsolute(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
    files:
      - src: tests/test_api.py
        mountPath: tests/test_api.py
`)
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error for non-absolute mountPath, got nil")
	}
	if !strings.Contains(err.Error(), "mountPath must be an absolute path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRunnerConfig_SuiteFiles_MountPathMustNotContainNUL(t *testing.T) {
	p := writeRunnerConfig(t, "\n"+
		"test_suites:\n"+
		"  - name: api-tests\n"+
		"    image: pytest:latest\n"+
		"    command: [\"pytest\"]\n"+
		"    files:\n"+
		"      - src: tests/test_api.py\n"+
		"        mountPath: \"/tests/test_api.py\\0bad\"\n")
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error for mountPath containing NUL byte, got nil")
	}
	if !strings.Contains(err.Error(), "mountPath must not contain NUL bytes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRunnerConfig_SuiteFiles_MultipleSuitesDifferentFiles(t *testing.T) {
	// Each suite can independently declare its own files.
	p := writeRunnerConfig(t, `
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
    files:
      - src: tests/test_api.py
        mountPath: /tests/test_api.py
  - name: ui-tests
    image: playwright:latest
    args: ["npx", "playwright", "test"]
    files:
      - src: tests/test_ui.py
        mountPath: /tests/test_ui.py
      - src: tests/requirements.txt
        mountPath: /tests/requirements.txt
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.TestSuites[0].Files) != 1 {
		t.Errorf("suite[0].Files: got %d, want 1", len(cfg.TestSuites[0].Files))
	}
	if len(cfg.TestSuites[1].Files) != 2 {
		t.Errorf("suite[1].Files: got %d, want 2", len(cfg.TestSuites[1].Files))
	}
}

// ---------------------------------------------------------------------------
// Timeout
// ---------------------------------------------------------------------------

func TestLoadRunnerConfig_Timeout_HappyPath(t *testing.T) {
	p := writeRunnerConfig(t, `
timeout: 10m
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TimeoutDur != 10*time.Minute {
		t.Errorf("TimeoutDur: got %v, want 10m", cfg.TimeoutDur)
	}
}

func TestLoadRunnerConfig_Timeout_Omitted_IsZero(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TimeoutDur != 0 {
		t.Errorf("TimeoutDur: got %v, want 0 (not set)", cfg.TimeoutDur)
	}
}

func TestLoadRunnerConfig_Timeout_InvalidString(t *testing.T) {
	p := writeRunnerConfig(t, `
timeout: "not-a-duration"
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
`)
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error for invalid timeout string, got nil")
	}
}

func TestLoadRunnerConfig_Timeout_NegativeIsInvalid(t *testing.T) {
	p := writeRunnerConfig(t, `
timeout: "-5m"
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
`)
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error for negative timeout, got nil")
	}
}

func TestLoadRunnerConfig_Timeout_VariousFormats(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"30s", 30 * time.Second},
		{"2h", 2 * time.Hour},
		{"1h30m", 90 * time.Minute},
	}
	for _, tc := range cases {
		p := writeRunnerConfig(t, "timeout: "+tc.raw+"\ntest_suites:\n  - name: s\n    image: i\n    command: [\"c\"]\n")
		cfg, err := scenario.LoadRunnerConfig(p)
		if err != nil {
			t.Errorf("timeout=%q: unexpected error: %v", tc.raw, err)
			continue
		}
		if cfg.TimeoutDur != tc.want {
			t.Errorf("timeout=%q: got %v, want %v", tc.raw, cfg.TimeoutDur, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Priority
// ---------------------------------------------------------------------------

func intPtr(v int) *int { return &v }

func TestLoadRunnerConfig_Priority_HappyPath(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: fast
    image: pytest:latest
    command: ["pytest"]
    priority: 0
  - name: slow
    image: playwright:latest
    args: ["npx", "playwright", "test"]
    priority: 1
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TestSuites[0].Priority == nil || *cfg.TestSuites[0].Priority != 0 {
		t.Errorf("suite[0].Priority: got %v, want 0", cfg.TestSuites[0].Priority)
	}
	if cfg.TestSuites[1].Priority == nil || *cfg.TestSuites[1].Priority != 1 {
		t.Errorf("suite[1].Priority: got %v, want 1", cfg.TestSuites[1].Priority)
	}
}

func TestLoadRunnerConfig_Priority_Omitted_IsNil(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TestSuites[0].Priority != nil {
		t.Errorf("Priority: expected nil when omitted, got %v", cfg.TestSuites[0].Priority)
	}
}

func TestLoadRunnerConfig_Priority_ZeroIsValid(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
    priority: 0
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TestSuites[0].Priority == nil || *cfg.TestSuites[0].Priority != 0 {
		t.Errorf("Priority: got %v, want 0", cfg.TestSuites[0].Priority)
	}
}

func TestLoadRunnerConfig_Priority_NegativeIsInvalid(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
    priority: -1
`)
	_, err := scenario.LoadRunnerConfig(p)
	if err == nil {
		t.Fatal("expected error for negative priority, got nil")
	}
}

func TestLoadRunnerConfig_Priority_MixedExplicitAndOmitted(t *testing.T) {
	// Mix of suites with and without priority — both valid together.
	p := writeRunnerConfig(t, `
test_suites:
  - name: concurrent-a
    image: pytest:latest
    command: ["pytest"]
    priority: 0
  - name: concurrent-b
    image: gotest:latest
    command: ["go", "test"]
    priority: 0
  - name: sequential
    image: playwright:latest
    args: ["npx", "playwright", "test"]
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TestSuites[0].Priority == nil || *cfg.TestSuites[0].Priority != 0 {
		t.Errorf("suite[0].Priority: got %v, want 0", cfg.TestSuites[0].Priority)
	}
	if cfg.TestSuites[1].Priority == nil || *cfg.TestSuites[1].Priority != 0 {
		t.Errorf("suite[1].Priority: got %v, want 0", cfg.TestSuites[1].Priority)
	}
	if cfg.TestSuites[2].Priority != nil {
		t.Errorf("suite[2].Priority: expected nil (omitted), got %v", cfg.TestSuites[2].Priority)
	}
}

func TestLoadRunnerConfig_Priority_LargeValueIsValid(t *testing.T) {
	p := writeRunnerConfig(t, `
test_suites:
  - name: api-tests
    image: pytest:latest
    command: ["pytest"]
    priority: 100
`)
	cfg, err := scenario.LoadRunnerConfig(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.TestSuites[0].Priority == nil || *cfg.TestSuites[0].Priority != 100 {
		t.Errorf("Priority: got %v, want 100", cfg.TestSuites[0].Priority)
	}
}
