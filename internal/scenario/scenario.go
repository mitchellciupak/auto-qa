package scenario

import (
	"fmt"
	"os"
	"path/filepath"
)

const yamlFileName = "senario.yaml"

// Scenario represents a single test scenario discovered on disk.
type Scenario struct {
	// Name is the directory name of the scenario (e.g. "example").
	Name string
	// YAMLPath is the absolute path to the scenario's senario.yaml file.
	YAMLPath string
}

// Discover walks root and returns one Scenario for every immediate subdirectory
// that contains a senario.yaml file. Non-directory entries at the root level
// are silently skipped. Returns an error if root cannot be read.
func Discover(root string) ([]Scenario, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("reading scenarios root %q: %w", root, err)
	}

	var scenarios []Scenario
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		yamlPath := filepath.Join(root, entry.Name(), yamlFileName)
		if _, err := os.Stat(yamlPath); os.IsNotExist(err) {
			continue // subdir has no senario.yaml — skip it
		} else if err != nil {
			return nil, fmt.Errorf("checking %q: %w", yamlPath, err)
		}

		scenarios = append(scenarios, Scenario{
			Name:     entry.Name(),
			YAMLPath: yamlPath,
		})
	}

	return scenarios, nil
}
