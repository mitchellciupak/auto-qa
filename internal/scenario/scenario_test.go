package scenario_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"auto-qa/internal/scenario"
)

// makeRoot creates a temporary directory tree for testing Discover.
// dirs is a map of subdir-name -> options:
//
//	true  = both scenario.yaml and runner.yaml present
//	false = scenario.yaml present, runner.yaml absent
//
// Use makeRootCustom for finer control.
func makeRoot(t *testing.T, dirs map[string]bool) string {
	t.Helper()
	root := t.TempDir()
	for name, withBoth := range dirs {
		dirPath := filepath.Join(root, name)
		if err := os.Mkdir(dirPath, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", dirPath, err)
		}
		yamlPath := filepath.Join(dirPath, "scenario.yaml")
		if err := os.WriteFile(yamlPath, []byte("# placeholder\n"), 0o644); err != nil {
			t.Fatalf("write %q: %v", yamlPath, err)
		}
		if withBoth {
			runnerPath := filepath.Join(dirPath, "runner.yaml")
			if err := os.WriteFile(runnerPath, []byte("test_suites: []\n"), 0o644); err != nil {
				t.Fatalf("write %q: %v", runnerPath, err)
			}
		}
	}
	return root
}

func TestDiscover_FindsAllScenariosWithYAML(t *testing.T) {
	root := makeRoot(t, map[string]bool{
		"alpha": true,
		"beta":  true,
		"gamma": true,
	})

	got, err := scenario.Discover(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 3 {
		t.Errorf("len(scenarios): got %d, want 3", len(got))
	}

	byName := make(map[string]scenario.Scenario, len(got))
	for _, s := range got {
		byName[s.Name] = s
	}

	for _, name := range []string{"alpha", "beta", "gamma"} {
		s, ok := byName[name]
		if !ok {
			t.Errorf("scenario %q not found", name)
			continue
		}
		wantYAML := filepath.Join(root, name, "scenario.yaml")
		if s.YAMLPath != wantYAML {
			t.Errorf("scenario %q YAMLPath: got %q, want %q", name, s.YAMLPath, wantYAML)
		}
		wantRunner := filepath.Join(root, name, "runner.yaml")
		if s.RunnerConfigPath != wantRunner {
			t.Errorf("scenario %q RunnerConfigPath: got %q, want %q", name, s.RunnerConfigPath, wantRunner)
		}
	}
}

func TestDiscover_SkipsDirWithoutScenarioManifest(t *testing.T) {
	root := t.TempDir()

	// "has-both" has both files — should be discovered.
	hasBothDir := filepath.Join(root, "has-both")
	if err := os.Mkdir(hasBothDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hasBothDir, "scenario.yaml"), []byte("# ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hasBothDir, "runner.yaml"), []byte("test_suites: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// "no-yaml" has neither — should be silently skipped.
	noYAMLDir := filepath.Join(root, "no-yaml")
	if err := os.Mkdir(noYAMLDir, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := scenario.Discover(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("len(scenarios): got %d, want 1", len(got))
	}
	if len(got) > 0 && got[0].Name != "has-both" {
		t.Errorf("Name: got %q, want %q", got[0].Name, "has-both")
	}
}

func TestDiscover_ErrorsOnLegacyScenarioYAML(t *testing.T) {
	root := t.TempDir()

	scenarioDir := filepath.Join(root, "legacy-only")
	if err := os.Mkdir(scenarioDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scenarioDir, "senario.yaml"), []byte("# legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scenarioDir, "runner.yaml"), []byte("test_suites: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := scenario.Discover(root)
	if err == nil {
		t.Fatal("expected error for legacy senario.yaml, got nil")
	}
	if got, want := err.Error(), "unsupported legacy scenario manifest"; !strings.Contains(got, want) {
		t.Fatalf("error: got %q, want substring %q", got, want)
	}
}

func TestDiscover_SupportsPreferredScenarioYAML(t *testing.T) {
	root := t.TempDir()

	scenarioDir := filepath.Join(root, "preferred-only")
	if err := os.Mkdir(scenarioDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scenarioDir, "scenario.yaml"), []byte("# preferred\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scenarioDir, "runner.yaml"), []byte("test_suites: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := scenario.Discover(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(scenarios): got %d, want 1", len(got))
	}

	wantPath := filepath.Join(scenarioDir, "scenario.yaml")
	if got[0].YAMLPath != wantPath {
		t.Errorf("YAMLPath: got %q, want %q", got[0].YAMLPath, wantPath)
	}
}

func TestDiscover_ErrorWhenRunnerYAMLMissing(t *testing.T) {
	root := t.TempDir()

	// A dir with scenario.yaml but no runner.yaml — must return an error.
	missingRunner := filepath.Join(root, "missing-runner")
	if err := os.Mkdir(missingRunner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(missingRunner, "scenario.yaml"), []byte("# ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := scenario.Discover(root)
	if err == nil {
		t.Fatal("expected error when runner.yaml is missing, got nil")
	}
}

func TestDiscover_SkipsNonDirectoryEntries(t *testing.T) {
	root := t.TempDir()

	// A plain file at root level — should be ignored.
	if err := os.WriteFile(filepath.Join(root, "stray-file.yaml"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	// A valid scenario subdir with both files.
	subdir := filepath.Join(root, "real-scenario")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "scenario.yaml"), []byte("# ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "runner.yaml"), []byte("test_suites: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := scenario.Discover(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("len(scenarios): got %d, want 1", len(got))
	}
}

func TestDiscover_EmptyRootReturnsEmptySlice(t *testing.T) {
	root := t.TempDir()

	got, err := scenario.Discover(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestDiscover_InvalidRootReturnsError(t *testing.T) {
	_, err := scenario.Discover("/this/path/does/not/exist")
	if err == nil {
		t.Fatal("expected error for non-existent root, got nil")
	}
}
