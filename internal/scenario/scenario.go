package scenario

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	legacyYAMLFileName = "senario.yaml"
	yamlFileName       = "scenario.yaml"
)

// Scenario represents a single test scenario discovered on disk.
type Scenario struct {
	// Name is the directory name of the scenario (e.g. "example").
	Name string
	// YAMLPath is the absolute path to the scenario manifest file.
	YAMLPath string
	// RunnerConfigPath is the absolute path to the scenario's runner.yaml file.
	RunnerConfigPath string
}

// Discover walks root and returns one Scenario for every immediate subdirectory
// that contains both a scenario manifest and a runner.yaml.
//
// Supported manifest name:
//   - scenario.yaml
//
// Subdirectories that have a scenario manifest but are missing runner.yaml
// return an error. Non-directory entries
// at the root level are silently skipped. Returns an error if root cannot be read.
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

		dirPath := filepath.Join(root, entry.Name())
		yamlPath, found, err := scenarioManifestPath(dirPath)
		if err != nil {
			return nil, err
		}
		if !found {
			continue // subdir has no scenario manifest file — skip it
		}

		runnerPath := filepath.Join(dirPath, runnerConfigFileName)
		if _, err := os.Stat(runnerPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("scenario %q has a scenario manifest but is missing runner.yaml", entry.Name())
		} else if err != nil {
			return nil, fmt.Errorf("checking %q: %w", runnerPath, err)
		}

		scenarios = append(scenarios, Scenario{
			Name:             entry.Name(),
			YAMLPath:         yamlPath,
			RunnerConfigPath: runnerPath,
		})
	}

	return scenarios, nil
}

func scenarioManifestPath(dirPath string) (string, bool, error) {
	manifestPath := filepath.Join(dirPath, yamlFileName)
	if _, err := os.Stat(manifestPath); err == nil {
		return manifestPath, true, nil
	} else if !os.IsNotExist(err) {
		return "", false, fmt.Errorf("checking %q: %w", manifestPath, err)
	}

	legacyPath := filepath.Join(dirPath, legacyYAMLFileName)
	if _, err := os.Stat(legacyPath); err == nil {
		return "", false, fmt.Errorf(
			"unsupported legacy scenario manifest %q; rename it to %q",
			legacyPath,
			yamlFileName,
		)
	} else if !os.IsNotExist(err) {
		return "", false, fmt.Errorf("checking %q: %w", legacyPath, err)
	}

	return "", false, nil
}
