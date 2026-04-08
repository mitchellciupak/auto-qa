package report_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"auto-qa/internal/report"
	"auto-qa/internal/scenario"
)

func makeResults() []scenario.Result {
	return []scenario.Result{
		{
			Name:      "alpha",
			Succeeded: true,
			Duration:  2 * time.Second,
			TestSuites: []scenario.TestSuiteResult{
				{Name: "api-tests", Succeeded: true, Duration: 1500 * time.Millisecond, Logs: "1 passed"},
			},
		},
		{
			Name:      "beta",
			Succeeded: false,
			Duration:  3 * time.Second,
			TestSuites: []scenario.TestSuiteResult{
				{Name: "smoke", Succeeded: false, Duration: 2800 * time.Millisecond, Logs: "1 failed"},
				{Name: "slow-e2e", Skipped: true, Duration: 0, Logs: ""},
			},
		},
	}
}

func TestBuild_Counts(t *testing.T) {
	r := report.Build(makeResults())

	if r.TotalScenarios != 2 {
		t.Errorf("TotalScenarios: got %d, want 2", r.TotalScenarios)
	}
	if r.Passed != 1 {
		t.Errorf("Passed: got %d, want 1", r.Passed)
	}
	if r.Failed != 1 {
		t.Errorf("Failed: got %d, want 1", r.Failed)
	}
}

func TestBuild_DurationMS(t *testing.T) {
	r := report.Build(makeResults())
	// alpha=2000ms + beta=3000ms = 5000ms
	if r.DurationMS != 5000 {
		t.Errorf("DurationMS: got %d, want 5000", r.DurationMS)
	}
}

func TestBuild_Scenarios(t *testing.T) {
	r := report.Build(makeResults())

	if len(r.Scenarios) != 2 {
		t.Fatalf("len(Scenarios): got %d, want 2", len(r.Scenarios))
	}

	alpha := r.Scenarios[0]
	if alpha.Name != "alpha" {
		t.Errorf("Scenarios[0].Name: got %q, want %q", alpha.Name, "alpha")
	}
	if !alpha.Succeeded {
		t.Error("Scenarios[0].Succeeded: got false, want true")
	}
	if alpha.DurationMS != 2000 {
		t.Errorf("Scenarios[0].DurationMS: got %d, want 2000", alpha.DurationMS)
	}
	if len(alpha.TestSuites) != 1 {
		t.Fatalf("Scenarios[0].TestSuites: got %d, want 1", len(alpha.TestSuites))
	}
	if alpha.TestSuites[0].Name != "api-tests" {
		t.Errorf("TestSuites[0].Name: got %q, want %q", alpha.TestSuites[0].Name, "api-tests")
	}
	if alpha.TestSuites[0].DurationMS != 1500 {
		t.Errorf("TestSuites[0].DurationMS: got %d, want 1500", alpha.TestSuites[0].DurationMS)
	}
	if alpha.TestSuites[0].Logs != "1 passed" {
		t.Errorf("TestSuites[0].Logs: got %q, want %q", alpha.TestSuites[0].Logs, "1 passed")
	}

	beta := r.Scenarios[1]
	if len(beta.TestSuites) != 2 {
		t.Fatalf("Scenarios[1].TestSuites: got %d, want 2", len(beta.TestSuites))
	}
	if !beta.TestSuites[1].Skipped {
		t.Errorf("Scenarios[1].TestSuites[1].Skipped: got %v, want true", beta.TestSuites[1].Skipped)
	}
}

func TestBuild_ErrorField(t *testing.T) {
	results := []scenario.Result{
		{
			Name:      "broken",
			Succeeded: false,
			Err:       os.ErrNotExist,
			Duration:  100 * time.Millisecond,
		},
	}
	r := report.Build(results)
	if r.Scenarios[0].Error == "" {
		t.Error("Error field: got empty string, want non-empty")
	}
}

func TestBuild_EmptyResults(t *testing.T) {
	r := report.Build(nil)

	if r.TotalScenarios != 0 {
		t.Errorf("TotalScenarios: got %d, want 0", r.TotalScenarios)
	}
	if r.Passed != 0 || r.Failed != 0 {
		t.Errorf("Passed/Failed: got %d/%d, want 0/0", r.Passed, r.Failed)
	}
	if r.DurationMS != 0 {
		t.Errorf("DurationMS: got %d, want 0", r.DurationMS)
	}
	if r.GeneratedAt.IsZero() {
		t.Error("GeneratedAt: should not be zero")
	}
}

func TestWriteJSON_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "results.json")

	if err := report.WriteJSON(makeResults(), path); err != nil {
		t.Fatalf("WriteJSON: unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var got report.Report
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.TotalScenarios != 2 {
		t.Errorf("TotalScenarios: got %d, want 2", got.TotalScenarios)
	}
	if got.Passed != 1 || got.Failed != 1 {
		t.Errorf("Passed/Failed: got %d/%d, want 1/1", got.Passed, got.Failed)
	}
}

func TestWriteJSON_InvalidPath(t *testing.T) {
	err := report.WriteJSON(makeResults(), "/nonexistent/dir/results.json")
	if err == nil {
		t.Error("expected error for non-existent directory, got nil")
	}
}
