package scenario_test

import (
	"os"
	"path/filepath"
	"testing"

	"auto-qa/internal/scenario"
)

// makeRoot creates a temporary directory tree for testing Discover.
// dirs is a map of subdir-name → whether to include a senario.yaml.
func makeRoot(t *testing.T, dirs map[string]bool) string {
	t.Helper()
	root := t.TempDir()
	for name, withYAML := range dirs {
		dirPath := filepath.Join(root, name)
		if err := os.Mkdir(dirPath, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", dirPath, err)
		}
		if withYAML {
			yamlPath := filepath.Join(dirPath, "senario.yaml")
			if err := os.WriteFile(yamlPath, []byte("# placeholder\n"), 0o644); err != nil {
				t.Fatalf("write %q: %v", yamlPath, err)
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
		wantPath := filepath.Join(root, name, "senario.yaml")
		if s.YAMLPath != wantPath {
			t.Errorf("scenario %q YAMLPath: got %q, want %q", name, s.YAMLPath, wantPath)
		}
	}
}

func TestDiscover_SkipsDirWithoutYAML(t *testing.T) {
	root := makeRoot(t, map[string]bool{
		"has-yaml":     true,
		"no-yaml":      false,
		"also-no-yaml": false,
	})

	got, err := scenario.Discover(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 1 {
		t.Errorf("len(scenarios): got %d, want 1", len(got))
	}
	if len(got) > 0 && got[0].Name != "has-yaml" {
		t.Errorf("Name: got %q, want %q", got[0].Name, "has-yaml")
	}
}

func TestDiscover_SkipsNonDirectoryEntries(t *testing.T) {
	root := t.TempDir()

	// A plain file at root level — should be ignored
	if err := os.WriteFile(filepath.Join(root, "stray-file.yaml"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	// A valid scenario subdir
	subdir := filepath.Join(root, "real-scenario")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "senario.yaml"), []byte("# ok\n"), 0o644); err != nil {
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
