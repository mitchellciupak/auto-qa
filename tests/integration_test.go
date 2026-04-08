//go:build integration

package tests

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"auto-qa/internal/applier"
	"auto-qa/internal/scenario"
	"auto-qa/internal/scheduler"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// These tests require a real cluster and are skipped automatically when
// KUBECONFIG is not set (and no default ~/.kube/config exists). Run them with:
//
//	just test-integration

// exampleScenarioDir returns the absolute path to scenarios/basic-example/ relative
// to this test file, regardless of the working directory at test time.
func exampleScenarioDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// file is <repo>/tests/integration_test.go
	return filepath.Join(filepath.Dir(file), "..", "scenarios", "basic-example")
}

func scenarioManifestPath(t *testing.T, scenarioDir string) string {
	t.Helper()

	manifestPath := filepath.Join(scenarioDir, "scenario.yaml")
	if _, err := os.Stat(manifestPath); err == nil {
		return manifestPath
	}

	t.Fatalf("scenario.yaml not found in %q", scenarioDir)
	return ""
}

func kubeconfig(t *testing.T) string {
	t.Helper()
	if kc := os.Getenv("KUBECONFIG"); kc != "" {
		return kc
	}
	if home := homedir.HomeDir(); home != "" {
		def := filepath.Join(home, ".kube", "config")
		if _, err := os.Stat(def); err == nil {
			return def
		}
	}
	t.Skip("no kubeconfig available — skipping integration test")
	return ""
}

// TestIntegration_ExampleScenario_BothSuitesParallel runs the full example
// scenario end-to-end against a real cluster. It verifies that:
//   - Both expected test suites (api-tests and ui-tests) are executed and reported.
//   - Both test suites pass.
//   - The scenario result is Succeeded=true with no error.
func TestIntegration_ExampleScenario_BothSuitesParallel(t *testing.T) {
	kc := kubeconfig(t)

	restCfg, err := clientcmd.BuildConfigFromFlags("", kc)
	if err != nil {
		t.Fatalf("building kubeconfig: %v", err)
	}

	k8sClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("creating kubernetes client: %v", err)
	}

	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		t.Fatalf("creating dynamic client: %v", err)
	}

	scenarioDir := exampleScenarioDir(t)

	sc := scenario.Scenario{
		Name:             "example",
		YAMLPath:         scenarioManifestPath(t, scenarioDir),
		RunnerConfigPath: filepath.Join(scenarioDir, "runner.yaml"),
	}

	runner := &scenario.Runner{
		Applier:        applier.New(dynClient, k8sClient.Discovery()),
		Scheduler:      scheduler.New(k8sClient, dynClient),
		Namespace:      "auto-qa-integration",
		DefaultTimeout: 10 * time.Minute,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	result := runner.Run(ctx, sc)

	// Report each test suite regardless of outcome so failures are visible.
	for _, sr := range result.TestSuites {
		t.Logf("test suite %q: succeeded=%v duration=%s", sr.Name, sr.Succeeded, sr.Duration.Round(time.Millisecond))
		if sr.Logs != "" {
			t.Logf("test suite %q logs:\n%s", sr.Name, sr.Logs)
		}
	}

	if result.Err != nil {
		t.Fatalf("scenario orchestration error: %v", result.Err)
	}

	if len(result.TestSuites) != 2 {
		t.Errorf("expected 2 test suite results (both parallel test suites), got %d", len(result.TestSuites))
	}

	// Verify both test suites were actually included (neither was skipped).
	names := make(map[string]bool, len(result.TestSuites))
	for _, sr := range result.TestSuites {
		names[sr.Name] = true
	}
	for _, want := range []string{"api-tests", "ui-tests"} {
		if !names[want] {
			t.Errorf("test suite %q missing from results — was it skipped?", want)
		}
	}

	if !result.Succeeded {
		t.Errorf("expected scenario Succeeded=true, got false")
	}

	t.Logf("scenario completed: succeeded=%v duration=%s", result.Succeeded, result.Duration.Round(time.Millisecond))
}
