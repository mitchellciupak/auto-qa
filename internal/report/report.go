package report

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"auto-qa/internal/scenario"
)

// Test suite is the JSON representation of a single test suite result.
type TestSuite struct {
	Name      string `json:"name"`
	Succeeded bool   `json:"succeeded"`
	Skipped   bool   `json:"skipped,omitempty"`
	// DurationMS is the wall-clock time for the test suite in milliseconds.
	DurationMS int64  `json:"duration_ms"`
	Logs       string `json:"logs"`
}

// Scenario is the JSON representation of a single scenario result.
type Scenario struct {
	Name      string `json:"name"`
	Succeeded bool   `json:"succeeded"`
	// DurationMS is the wall-clock time for the scenario in milliseconds.
	DurationMS int64       `json:"duration_ms"`
	Error      string      `json:"error,omitempty"`
	TestSuites []TestSuite `json:"test_suites"`
}

// Report is the top-level JSON structure written to the report file.
type Report struct {
	// GeneratedAt is the UTC time the report was written.
	GeneratedAt time.Time `json:"generated_at"`
	// TotalScenarios is the number of scenarios that were run.
	TotalScenarios int `json:"total_scenarios"`
	// Passed is the number of scenarios that succeeded.
	Passed int `json:"passed"`
	// Failed is the number of scenarios that failed.
	Failed int `json:"failed"`
	// DurationMS is the sum of per-scenario durations in milliseconds.
	DurationMS int64      `json:"duration_ms"`
	Scenarios  []Scenario `json:"scenarios"`
}

// Build constructs a Report from a slice of scenario results.
func Build(results []scenario.Result) Report {
	r := Report{
		GeneratedAt:    time.Now().UTC(),
		TotalScenarios: len(results),
		Scenarios:      make([]Scenario, 0, len(results)),
	}

	for _, res := range results {
		sc := Scenario{
			Name:       res.Name,
			Succeeded:  res.Succeeded,
			DurationMS: res.Duration.Milliseconds(),
			TestSuites: make([]TestSuite, 0, len(res.TestSuites)),
		}
		if res.Err != nil {
			sc.Error = res.Err.Error()
		}
		for _, sr := range res.TestSuites {
			sc.TestSuites = append(sc.TestSuites, TestSuite{
				Name:       sr.Name,
				Succeeded:  sr.Succeeded,
				Skipped:    sr.Skipped,
				DurationMS: sr.Duration.Milliseconds(),
				Logs:       sr.Logs,
			})
		}
		if res.Succeeded {
			r.Passed++
		} else {
			r.Failed++
		}
		r.DurationMS += res.Duration.Milliseconds()
		r.Scenarios = append(r.Scenarios, sc)
	}

	return r
}

// WriteJSON builds a report from results and writes it as indented JSON to path.
func WriteJSON(results []scenario.Result, path string) error {
	r := Build(results)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("report: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("report: write %s: %w", path, err)
	}
	return nil
}
