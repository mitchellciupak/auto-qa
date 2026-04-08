package scenario

import "time"

// TestSuiteResult captures the outcome of a single test suite within a scenario.
type TestSuiteResult struct {
	// Name matches TestSuiteSpec.Name.
	Name string `json:"name"`
	// Succeeded is true when the test Job exited 0.
	Succeeded bool `json:"succeeded"`
	// Skipped is true when the suite is disabled in runner.yaml.
	Skipped bool `json:"skipped,omitempty"`
	// Duration is the wall-clock time for the full test suite orchestration,
	// including setup and teardown around the test Job.
	// When marshaled to JSON it is encoded as an integer nanosecond count.
	Duration time.Duration `json:"duration_ns"`
	// Logs contains the raw stdout+stderr captured from the test runner pod.
	Logs string `json:"logs"`
}

// Result captures the outcome of a full scenario run, including all test suites.
type Result struct {
	// Name is the scenario directory name.
	Name string `json:"name"`
	// Succeeded is true only when every test suite in the scenario passed.
	Succeeded bool `json:"succeeded"`
	// Err is set when orchestration itself failed (e.g. YAML apply error,
	// job creation error) rather than a test suite failure.
	Err error `json:"-"`
	// Duration is the wall-clock time for the entire scenario.
	// When marshaled to JSON it is encoded as an integer nanosecond count.
	Duration time.Duration `json:"duration_ns"`
	// TestSuites contains one entry per test suite that was attempted, in order.
	// If orchestration failed before any test suite ran, this will be empty.
	TestSuites []TestSuiteResult `json:"test_suites"`
}
