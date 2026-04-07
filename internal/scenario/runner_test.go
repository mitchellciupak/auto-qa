package scenario_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"auto-qa/internal/applier"
	"auto-qa/internal/scenario"
	"auto-qa/internal/scheduler"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// writeYAML writes a minimal senario.yaml to a temp dir and returns the
// Scenario pointing at it.
func writeScenario(t *testing.T, name string) scenario.Scenario {
	t.Helper()
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "senario.yaml")
	if err := os.WriteFile(yamlPath, []byte("---\n"), 0o644); err != nil {
		t.Fatalf("write senario.yaml: %v", err)
	}
	return scenario.Scenario{Name: name, YAMLPath: yamlPath}
}

// coreV1Mapper returns a REST mapper with just the types used by the
// minimal YAML (empty doc — no resources to map), kept for consistency
// with the applier test pattern.
func coreV1Mapper() meta.RESTMapper {
	m := meta.NewDefaultRESTMapper(nil)
	m.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"}, meta.RESTScopeRoot)
	m.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)
	return m
}

// newApplier returns an Applier backed by a fake dynamic client whose Patch
// reactor accepts ApplyPatchType without error.
func newApplier(t *testing.T) *applier.Applier {
	t.Helper()
	scheme := runtime.NewScheme()
	gvrMap := map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "namespaces"}: "NamespaceList",
		{Group: "", Version: "v1", Resource: "configmaps"}: "ConfigMapList",
	}
	dynClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrMap)
	dynClient.Fake.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.(k8stesting.PatchAction).GetPatchType() == types.ApplyPatchType {
			return true, nil, nil
		}
		return false, nil, nil
	})
	return applier.NewWithMapper(dynClient, coreV1Mapper())
}

// newRunner constructs a Runner with a fake k8s client that uses fakeWatcher
// for job watch events, and the provided applier.
func newRunner(t *testing.T, app *applier.Applier, fakeWatcher *watch.FakeWatcher) (*scenario.Runner, *k8sfake.Clientset) {
	t.Helper()
	k8sClient := k8sfake.NewSimpleClientset()
	k8sClient.PrependWatchReactor("jobs", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatcher, nil
	})
	sched := scheduler.New(k8sClient)
	runner := &scenario.Runner{
		Applier:   app,
		Scheduler: sched,
		Image:     "busybox:latest",
		Namespace: "test-ns",
	}
	return runner, k8sClient
}

func succeededJob(name, namespace string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}
}

func failedJob(name, namespace string, failedCount int32) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status: batchv1.JobStatus{
			Failed: failedCount,
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRunner_Run_Succeeds(t *testing.T) {
	sc := writeScenario(t, "happy")
	app := newApplier(t)
	fw := watch.NewFake()
	runner, _ := newRunner(t, app, fw)

	go func() {
		time.Sleep(10 * time.Millisecond)
		// The job name is generated dynamically; we send an event that matches
		// the watch selector by using Modify — the fake watcher broadcasts to
		// all watchers regardless of field selector.
		fw.Modify(succeededJob("any-job", "test-ns"))
	}()

	result := runner.Run(context.Background(), sc)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true")
	}
	if result.Name != "happy" {
		t.Errorf("Name: got %q, want %q", result.Name, "happy")
	}
	if result.Duration <= 0 {
		t.Error("expected non-zero Duration")
	}
}

func TestRunner_Run_JobFails(t *testing.T) {
	sc := writeScenario(t, "fail-case")
	app := newApplier(t)
	fw := watch.NewFake()
	runner, _ := newRunner(t, app, fw)

	go func() {
		time.Sleep(10 * time.Millisecond)
		fw.Modify(failedJob("any-job", "test-ns", 1))
	}()

	result := runner.Run(context.Background(), sc)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if result.Succeeded {
		t.Error("expected Succeeded=false")
	}
}

func TestRunner_Run_ApplyFails_ReturnsError(t *testing.T) {
	// Point at a non-existent YAML file so ApplyFile errors immediately.
	sc := scenario.Scenario{Name: "bad-yaml", YAMLPath: "/nonexistent/senario.yaml"}
	app := newApplier(t)
	fw := watch.NewFake()
	runner, _ := newRunner(t, app, fw)

	result := runner.Run(context.Background(), sc)

	if result.Err == nil {
		t.Fatal("expected error when YAML file is missing, got nil")
	}
	if result.Succeeded {
		t.Error("expected Succeeded=false on apply error")
	}
}

func TestRunner_Run_ContextTimeout_CleansUp(t *testing.T) {
	sc := writeScenario(t, "timeout-case")
	app := newApplier(t)
	fw := watch.NewFake()
	runner, k8sClient := newRunner(t, app, fw)

	// Very short timeout — watch will never receive a terminal event.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	result := runner.Run(ctx, sc)

	if result.Err == nil {
		t.Fatal("expected error on context timeout, got nil")
	}
	if result.Succeeded {
		t.Error("expected Succeeded=false on timeout")
	}

	// Verify the job was still deleted despite the timeout.
	jobs, err := k8sClient.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing jobs: %v", err)
	}
	if len(jobs.Items) != 0 {
		t.Errorf("expected 0 jobs after cleanup, found %d", len(jobs.Items))
	}
}

func TestRunner_Run_ResultNameMatchesScenario(t *testing.T) {
	sc := writeScenario(t, "named-scenario")
	app := newApplier(t)
	fw := watch.NewFake()
	runner, _ := newRunner(t, app, fw)

	go func() {
		time.Sleep(10 * time.Millisecond)
		fw.Modify(succeededJob("any-job", "test-ns"))
	}()

	result := runner.Run(context.Background(), sc)
	if result.Name != sc.Name {
		t.Errorf("Result.Name: got %q, want %q", result.Name, sc.Name)
	}
}
