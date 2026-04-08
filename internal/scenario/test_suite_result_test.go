package scenario

import (
	"encoding/json"
	"testing"
	"time"
)

func TestResultJSONUsesDurationNS(t *testing.T) {
	result := Result{
		Name:      "example",
		Succeeded: true,
		Duration:  1500 * time.Millisecond,
		TestSuites: []TestSuiteResult{
			{
				Name:      "suite-a",
				Succeeded: true,
				Duration:  2 * time.Second,
			},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal: unexpected error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal: unexpected error: %v", err)
	}

	if _, exists := got["duration_ms"]; exists {
		t.Fatalf("unexpected duration_ms field in JSON: %s", data)
	}

	durationNS, ok := got["duration_ns"].(float64)
	if !ok {
		t.Fatalf("duration_ns should be a JSON number, got %T", got["duration_ns"])
	}
	if durationNS != float64((1500 * time.Millisecond).Nanoseconds()) {
		t.Fatalf("duration_ns mismatch: got %v want %d", durationNS, (1500 * time.Millisecond).Nanoseconds())
	}

	testSuites, ok := got["test_suites"].([]any)
	if !ok || len(testSuites) != 1 {
		t.Fatalf("test_suites should contain one suite, got %#v", got["test_suites"])
	}

	suiteObj, ok := testSuites[0].(map[string]any)
	if !ok {
		t.Fatalf("test_suites[0] should be an object, got %T", testSuites[0])
	}

	if _, exists := suiteObj["duration_ms"]; exists {
		t.Fatalf("unexpected suite duration_ms field in JSON: %s", data)
	}

	suiteDurationNS, ok := suiteObj["duration_ns"].(float64)
	if !ok {
		t.Fatalf("suite duration_ns should be a JSON number, got %T", suiteObj["duration_ns"])
	}
	if suiteDurationNS != float64((2 * time.Second).Nanoseconds()) {
		t.Fatalf("suite duration_ns mismatch: got %v want %d", suiteDurationNS, (2 * time.Second).Nanoseconds())
	}
}
