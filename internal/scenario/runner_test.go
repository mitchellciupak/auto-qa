package scenario_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	fakediscovery "k8s.io/client-go/discovery/fake"
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

// minimalRunnerYAML is a valid runner.yaml with one suite.
const minimalRunnerYAML = `test_suites:
  - name: unit
    image: busybox:latest
    args: ["echo", "ok"]
`

// twoSuiteRunnerYAML is a valid runner.yaml with two suites.
const twoSuiteRunnerYAML = `test_suites:
  - name: unit
    image: busybox:latest
    args: ["echo", "unit"]
  - name: integration
    image: busybox:latest
    args: ["echo", "integration"]
`

const oneDisabledOneEnabledRunnerYAML = `test_suites:
  - name: disabled-suite
    enabled: false
    image: busybox:latest
    args: ["echo", "disabled"]
  - name: enabled-suite
    image: busybox:latest
    args: ["echo", "enabled"]
`

const allDisabledRunnerYAML = `test_suites:
  - name: disabled-a
    enabled: false
    image: busybox:latest
    args: ["echo", "a"]
  - name: disabled-b
    enabled: false
    image: busybox:latest
    args: ["echo", "b"]
`

// writeScenario writes a minimal scenario.yaml and an optional runner.yaml to a
// temp dir and returns the Scenario pointing at them.
func writeScenario(t *testing.T, name string, runnerYAML string) scenario.Scenario {
	t.Helper()
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "scenario.yaml")
	if err := os.WriteFile(yamlPath, []byte("---\n"), 0o644); err != nil {
		t.Fatalf("write scenario.yaml: %v", err)
	}
	runnerPath := filepath.Join(dir, "runner.yaml")
	if err := os.WriteFile(runnerPath, []byte(runnerYAML), 0o644); err != nil {
		t.Fatalf("write runner.yaml: %v", err)
	}
	return scenario.Scenario{
		Name:             name,
		YAMLPath:         yamlPath,
		RunnerConfigPath: runnerPath,
	}
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

// minimalDynClient returns a fake dynamic client that knows no GVRs.
// Suitable for tests whose scenario.yaml has no namespaced resources.
func minimalDynClient() *fake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	return fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{})
}

// fakeDiscoveryWithDeployments configures the fake discovery on client to
// advertise apps/v1 Deployments. Returns the configured FakeDiscovery so the
// caller can pass it to scheduler.NewWithDiscovery.
func fakeDiscoveryWithDeployments(client *k8sfake.Clientset) *fakediscovery.FakeDiscovery {
	disc := client.Discovery().(*fakediscovery.FakeDiscovery)
	disc.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{Name: "deployments", Kind: "Deployment", Namespaced: true},
			},
		},
	}
	return disc
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

// newRunner constructs a Runner with a fake k8s client. Each call to Watch on
// jobs dequeues the next watcher from watchers (cycling through them). This
// allows multi-suite tests to supply one watcher per suite.
func newRunner(t *testing.T, app *applier.Applier, watchers ...*watch.FakeWatcher) (*scenario.Runner, *k8sfake.Clientset) {
	t.Helper()
	k8sClient := k8sfake.NewSimpleClientset()
	if len(watchers) == 0 {
		watchers = []*watch.FakeWatcher{watch.NewFake()}
	}
	idx := 0
	k8sClient.PrependWatchReactor("jobs", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		w := watchers[idx%len(watchers)]
		idx++
		return true, w, nil
	})
	// Minimal fake dynamic client — tests that use "---\n" scenario.yaml
	// produce zero WorkloadRefs so the dynamic client is never called.
	dynScheme := runtime.NewScheme()
	dynClient := fake.NewSimpleDynamicClientWithCustomListKinds(dynScheme, map[schema.GroupVersionResource]string{})
	sched := scheduler.New(k8sClient, dynClient)
	runner := &scenario.Runner{
		Applier:        app,
		Scheduler:      sched,
		Namespace:      "test-ns",
		DefaultTimeout: time.Minute, // generous default; overridden per test where needed
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

type recordingApplier struct {
	applyErr    error
	applyCalls  int
	deleteCalls int
	deletePath  string
}

func (a *recordingApplier) ApplyFile(_ context.Context, _ string) error {
	a.applyCalls++
	return a.applyErr
}

func (a *recordingApplier) DeleteFile(_ context.Context, path string) error {
	a.deleteCalls++
	a.deletePath = path
	return nil
}

type noopScheduler struct{}

func (noopScheduler) EnsureNamespace(context.Context, string) error { return nil }

func (noopScheduler) WaitForWorkloads(context.Context, []applier.WorkloadRef) error { return nil }

func (noopScheduler) CreateConfigMap(context.Context, string, string, map[string]string) error {
	return nil
}

func (noopScheduler) DeleteConfigMap(context.Context, string, string) error { return nil }

func (noopScheduler) CreateJob(context.Context, scheduler.JobSpec) (string, error) {
	return "", errors.New("unexpected CreateJob call")
}

func (noopScheduler) WatchJob(context.Context, string, string) (scheduler.JobResult, error) {
	return scheduler.JobResult{}, errors.New("unexpected WatchJob call")
}

func (noopScheduler) FetchJobLogs(context.Context, string, string) (string, error) {
	return "", errors.New("unexpected FetchJobLogs call")
}

func (noopScheduler) DeleteJob(context.Context, string, string) error { return nil }

type createConfigMapFailingScheduler struct {
	createErr      error
	deleteCalls    int
	deletedName    string
	deletedNS      string
	createdName    string
	createdNS      string
	createdDataLen int
}

func (s *createConfigMapFailingScheduler) EnsureNamespace(context.Context, string) error { return nil }

func (s *createConfigMapFailingScheduler) WaitForWorkloads(context.Context, []applier.WorkloadRef) error {
	return nil
}

func (s *createConfigMapFailingScheduler) CreateConfigMap(_ context.Context, namespace, name string, data map[string]string) error {
	s.createdNS = namespace
	s.createdName = name
	s.createdDataLen = len(data)
	return s.createErr
}

func (s *createConfigMapFailingScheduler) DeleteConfigMap(_ context.Context, namespace, name string) error {
	s.deleteCalls++
	s.deletedNS = namespace
	s.deletedName = name
	return nil
}

func (s *createConfigMapFailingScheduler) CreateJob(context.Context, scheduler.JobSpec) (string, error) {
	return "", errors.New("unexpected CreateJob call")
}

func (s *createConfigMapFailingScheduler) WatchJob(context.Context, string, string) (scheduler.JobResult, error) {
	return scheduler.JobResult{}, errors.New("unexpected WatchJob call")
}

func (s *createConfigMapFailingScheduler) FetchJobLogs(context.Context, string, string) (string, error) {
	return "", errors.New("unexpected FetchJobLogs call")
}

func (s *createConfigMapFailingScheduler) DeleteJob(context.Context, string, string) error {
	return nil
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

func TestRunner_Run_SingleSuiteSucceeds(t *testing.T) {
	sc := writeScenario(t, "happy", minimalRunnerYAML)
	app := newApplier(t)
	fw := watch.NewFake()
	runner, _ := newRunner(t, app, fw)

	go func() {
		time.Sleep(10 * time.Millisecond)
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
	if len(result.TestSuites) != 1 {
		t.Fatalf("expected 1 TestSuiteResult, got %d", len(result.TestSuites))
	}
	if result.TestSuites[0].Name != "unit" {
		t.Errorf("suite name: got %q, want %q", result.TestSuites[0].Name, "unit")
	}
	if !result.TestSuites[0].Succeeded {
		t.Error("expected suite Succeeded=true")
	}
}

func TestRunner_Run_SingleSuiteFails(t *testing.T) {
	sc := writeScenario(t, "fail-case", minimalRunnerYAML)
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
	if len(result.TestSuites) != 1 {
		t.Fatalf("expected 1 TestSuiteResult, got %d", len(result.TestSuites))
	}
	if result.TestSuites[0].Succeeded {
		t.Error("expected suite Succeeded=false")
	}
}

func TestRunner_Run_FailFastStopsSubsequentSuites(t *testing.T) {
	// Two suites: first fails → second must NOT run.
	sc := writeScenario(t, "fail-fast", twoSuiteRunnerYAML)
	app := newApplier(t)
	fw := watch.NewFake()
	runner, _ := newRunner(t, app, fw)

	go func() {
		time.Sleep(10 * time.Millisecond)
		fw.Modify(failedJob("any-job", "test-ns", 1))
	}()

	result := runner.Run(context.Background(), sc)

	if result.Succeeded {
		t.Error("expected Succeeded=false")
	}
	// Only the first suite should have been attempted.
	if len(result.TestSuites) != 1 {
		t.Errorf("expected 1 suite attempted (fail-fast), got %d", len(result.TestSuites))
	}
}

func TestRunner_Run_TwoSuitesBothSucceed(t *testing.T) {
	sc := writeScenario(t, "two-pass", twoSuiteRunnerYAML)
	app := newApplier(t)
	// Provide two separate watchers — one per suite watch call.
	fw1 := watch.NewFake()
	fw2 := watch.NewFake()
	runner, _ := newRunner(t, app, fw1, fw2)

	go func() {
		time.Sleep(10 * time.Millisecond)
		fw1.Modify(succeededJob("any-job", "test-ns"))
	}()
	go func() {
		time.Sleep(10 * time.Millisecond)
		fw2.Modify(succeededJob("any-job", "test-ns"))
	}()

	result := runner.Run(context.Background(), sc)

	if !result.Succeeded {
		t.Errorf("expected Succeeded=true, got false; err=%v", result.Err)
	}
	if len(result.TestSuites) != 2 {
		t.Fatalf("expected 2 TestSuiteResults, got %d", len(result.TestSuites))
	}
	for _, sr := range result.TestSuites {
		if !sr.Succeeded {
			t.Errorf("suite %q: expected Succeeded=true", sr.Name)
		}
	}
}

func TestRunner_Run_DisabledSuiteIsSkipped(t *testing.T) {
	sc := writeScenario(t, "skip-one", oneDisabledOneEnabledRunnerYAML)
	app := newApplier(t)
	fw := watch.NewFake()
	runner, _ := newRunner(t, app, fw)

	go func() {
		time.Sleep(10 * time.Millisecond)
		fw.Modify(succeededJob("any-job", "test-ns"))
	}()

	result := runner.Run(context.Background(), sc)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true")
	}
	if len(result.TestSuites) != 2 {
		t.Fatalf("expected 2 TestSuiteResults, got %d", len(result.TestSuites))
	}
	if result.TestSuites[0].Name != "disabled-suite" || !result.TestSuites[0].Skipped {
		t.Errorf("suite[0]: got name=%q skipped=%v, want disabled-suite skipped=true", result.TestSuites[0].Name, result.TestSuites[0].Skipped)
	}
	if result.TestSuites[1].Name != "enabled-suite" || result.TestSuites[1].Skipped || !result.TestSuites[1].Succeeded {
		t.Errorf("suite[1]: got name=%q skipped=%v succeeded=%v, want enabled-suite skipped=false succeeded=true", result.TestSuites[1].Name, result.TestSuites[1].Skipped, result.TestSuites[1].Succeeded)
	}
}

func TestRunner_Run_AllSuitesDisabled_Succeeds(t *testing.T) {
	sc := writeScenario(t, "skip-all", allDisabledRunnerYAML)
	app := newApplier(t)
	runner, _ := newRunner(t, app)

	result := runner.Run(context.Background(), sc)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true when all suites are skipped")
	}
	if len(result.TestSuites) != 2 {
		t.Fatalf("expected 2 TestSuiteResults, got %d", len(result.TestSuites))
	}
	for _, sr := range result.TestSuites {
		if !sr.Skipped {
			t.Errorf("suite %q: expected Skipped=true", sr.Name)
		}
	}
}

func TestRunner_Run_ApplyFails_ReturnsError(t *testing.T) {
	// Point at a non-existent YAML file so ApplyFile errors immediately.
	sc := scenario.Scenario{
		Name:             "bad-yaml",
		YAMLPath:         "/nonexistent/scenario.yaml",
		RunnerConfigPath: "/nonexistent/runner.yaml",
	}
	app := newApplier(t)
	fw := watch.NewFake()
	runner, _ := newRunner(t, app, fw)

	result := runner.Run(context.Background(), sc)

	if result.Err == nil {
		t.Fatal("expected error when runner config is missing, got nil")
	}
	if result.Succeeded {
		t.Error("expected Succeeded=false on error")
	}
}

func TestRunner_Run_ApplyFails_AttemptsTeardown(t *testing.T) {
	sc := writeScenario(t, "apply-fails", minimalRunnerYAML)
	sc.YAMLPath = filepath.Join(t.TempDir(), "missing-scenario.yaml")

	app := &recordingApplier{applyErr: errors.New("apply failed")}
	runner := &scenario.Runner{
		Applier:        app,
		Scheduler:      noopScheduler{},
		Namespace:      "test-ns",
		DefaultTimeout: time.Minute,
	}

	result := runner.Run(context.Background(), sc)

	if result.Err == nil {
		t.Fatal("expected error when apply fails, got nil")
	}
	if app.applyCalls != 1 {
		t.Fatalf("expected ApplyFile to be called once, got %d", app.applyCalls)
	}
	if app.deleteCalls != 1 {
		t.Fatalf("expected DeleteFile to be called once for teardown, got %d", app.deleteCalls)
	}
	if app.deletePath != sc.YAMLPath {
		t.Fatalf("expected teardown path %q, got %q", sc.YAMLPath, app.deletePath)
	}
}

func TestRunner_Run_ContextTimeout_CleansUp(t *testing.T) {
	sc := writeScenario(t, "timeout-case", minimalRunnerYAML)
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

func TestRunner_Run_DefaultTimeoutNonPositive_WhenRunnerTimeoutOmitted_ReturnsClearError(t *testing.T) {
	sc := writeScenario(t, "invalid-default-timeout", minimalRunnerYAML) // no timeout field

	app := &recordingApplier{}
	runner := &scenario.Runner{
		Applier:        app,
		Scheduler:      noopScheduler{},
		Namespace:      "test-ns",
		DefaultTimeout: 0,
	}

	result := runner.Run(context.Background(), sc)

	if result.Err == nil {
		t.Fatal("expected error when both runner timeout and DefaultTimeout are non-positive, got nil")
	}
	if !strings.Contains(result.Err.Error(), "Runner.DefaultTimeout must be > 0") {
		t.Fatalf("expected clear timeout configuration error, got %v", result.Err)
	}
	if result.Succeeded {
		t.Error("expected Succeeded=false on invalid timeout configuration")
	}
	if app.applyCalls != 0 {
		t.Fatalf("expected ApplyFile not to be called on invalid timeout configuration, got %d", app.applyCalls)
	}
}

func TestRunner_Run_ResultNameMatchesScenario(t *testing.T) {
	sc := writeScenario(t, "named-scenario", minimalRunnerYAML)
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

func TestRunner_Run_ConfigMapsCreatedAndTornDown(t *testing.T) {
	// Write a scenario whose suite declares files inline.
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "scenario.yaml")
	if err := os.WriteFile(yamlPath, []byte("---\n"), 0o644); err != nil {
		t.Fatalf("write scenario.yaml: %v", err)
	}

	// Create the local tests/ directory with one file.
	testsDir := filepath.Join(dir, "tests")
	if err := os.Mkdir(testsDir, 0o755); err != nil {
		t.Fatalf("mkdir tests: %v", err)
	}
	if err := os.WriteFile(filepath.Join(testsDir, "test_hello.py"), []byte("def test_hello(): pass\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	runnerYAML := `test_suites:
  - name: unit
    image: busybox:latest
    args: ["echo", "ok"]
    files:
      - src: tests/test_hello.py
        mountPath: /tests/test_hello.py
`
	runnerPath := filepath.Join(dir, "runner.yaml")
	if err := os.WriteFile(runnerPath, []byte(runnerYAML), 0o644); err != nil {
		t.Fatalf("write runner.yaml: %v", err)
	}
	sc := scenario.Scenario{
		Name:             "cm-test",
		YAMLPath:         yamlPath,
		RunnerConfigPath: runnerPath,
	}

	app := newApplier(t)
	fw := watch.NewFake()
	runner, k8sClient := newRunner(t, app, fw)

	go func() {
		time.Sleep(10 * time.Millisecond)
		fw.Modify(succeededJob("any-job", "test-ns"))
	}()

	result := runner.Run(context.Background(), sc)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true")
	}

	// After teardown the auto-generated ConfigMap should be gone.
	cms, err := k8sClient.CoreV1().ConfigMaps("test-ns").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("listing configmaps: %v", err)
	}
	for _, cm := range cms.Items {
		if cm.Name == "cm-test-unit-files" {
			t.Errorf("ConfigMap %q still exists after teardown", cm.Name)
		}
	}
}

func TestRunner_Run_CreateConfigMapError_AttemptsConfigMapCleanup(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "scenario.yaml")
	if err := os.WriteFile(yamlPath, []byte("---\n"), 0o644); err != nil {
		t.Fatalf("write scenario.yaml: %v", err)
	}

	testsDir := filepath.Join(dir, "tests")
	if err := os.Mkdir(testsDir, 0o755); err != nil {
		t.Fatalf("mkdir tests: %v", err)
	}
	if err := os.WriteFile(filepath.Join(testsDir, "test_hello.py"), []byte("def test_hello(): pass\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	runnerYAML := `test_suites:
  - name: unit
    image: busybox:latest
    args: ["echo", "ok"]
    files:
      - src: tests/test_hello.py
        mountPath: /tests/test_hello.py
`
	runnerPath := filepath.Join(dir, "runner.yaml")
	if err := os.WriteFile(runnerPath, []byte(runnerYAML), 0o644); err != nil {
		t.Fatalf("write runner.yaml: %v", err)
	}

	sc := scenario.Scenario{
		Name:             "cm-cleanup",
		YAMLPath:         yamlPath,
		RunnerConfigPath: runnerPath,
	}

	sched := &createConfigMapFailingScheduler{createErr: context.DeadlineExceeded}
	runner := &scenario.Runner{
		Applier:        newApplier(t),
		Scheduler:      sched,
		Namespace:      "test-ns",
		DefaultTimeout: time.Minute,
	}

	result := runner.Run(context.Background(), sc)

	if result.Err == nil {
		t.Fatal("expected error from CreateConfigMap, got nil")
	}
	if !strings.Contains(result.Err.Error(), "creating configmap") {
		t.Fatalf("expected configmap creation error, got %v", result.Err)
	}
	if sched.createdName == "" {
		t.Fatal("expected CreateConfigMap to be called")
	}
	if sched.createdDataLen != 1 {
		t.Fatalf("expected one injected file in ConfigMap data, got %d", sched.createdDataLen)
	}
	if sched.deleteCalls != 1 {
		t.Fatalf("expected one best-effort DeleteConfigMap call, got %d", sched.deleteCalls)
	}
	if sched.deletedName != sched.createdName {
		t.Fatalf("expected DeleteConfigMap name %q, got %q", sched.createdName, sched.deletedName)
	}
	if sched.deletedNS != "test-ns" {
		t.Fatalf("expected DeleteConfigMap namespace %q, got %q", "test-ns", sched.deletedNS)
	}
}

func TestRunner_Run_FilesDuplicateSanitizedKeyReturnsError(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "scenario.yaml")
	if err := os.WriteFile(yamlPath, []byte("---\n"), 0o644); err != nil {
		t.Fatalf("write scenario.yaml: %v", err)
	}

	if err := os.Mkdir(filepath.Join(dir, "a"), 0o755); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a", "b.txt"), []byte("from nested path\n"), 0o644); err != nil {
		t.Fatalf("write a/b.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a-b.txt"), []byte("from dashed path\n"), 0o644); err != nil {
		t.Fatalf("write a-b.txt: %v", err)
	}

	runnerYAML := `test_suites:
  - name: collision
    image: busybox:latest
    args: ["echo", "ok"]
    files:
      - src: a/b.txt
        mountPath: /tests/a-b-from-slash.txt
      - src: a-b.txt
        mountPath: /tests/a-b-from-dash.txt
`
	runnerPath := filepath.Join(dir, "runner.yaml")
	if err := os.WriteFile(runnerPath, []byte(runnerYAML), 0o644); err != nil {
		t.Fatalf("write runner.yaml: %v", err)
	}

	sc := scenario.Scenario{
		Name:             "duplicate-cm-key",
		YAMLPath:         yamlPath,
		RunnerConfigPath: runnerPath,
	}

	app := newApplier(t)
	fw := watch.NewFake()
	runner, _ := newRunner(t, app, fw)

	result := runner.Run(context.Background(), sc)

	if result.Err == nil {
		t.Fatal("expected duplicate ConfigMap key error, got nil")
	}
	if !strings.Contains(result.Err.Error(), "duplicate ConfigMap data key") {
		t.Fatalf("expected duplicate key error, got %v", result.Err)
	}
	if result.Succeeded {
		t.Error("expected Succeeded=false when duplicate file keys are detected")
	}
}

func TestRunner_Run_FilesDerivedConfigMapKeyEmptyReturnsError(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "scenario.yaml")
	if err := os.WriteFile(yamlPath, []byte("---\n"), 0o644); err != nil {
		t.Fatalf("write scenario.yaml: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "!!!"), []byte("bad key source\n"), 0o644); err != nil {
		t.Fatalf("write !!!: %v", err)
	}

	runnerYAML := `test_suites:
  - name: invalid-key
    image: busybox:latest
    args: ["echo", "ok"]
    files:
      - src: "!!!"
        mountPath: /tests/injected.txt
`
	runnerPath := filepath.Join(dir, "runner.yaml")
	if err := os.WriteFile(runnerPath, []byte(runnerYAML), 0o644); err != nil {
		t.Fatalf("write runner.yaml: %v", err)
	}

	sc := scenario.Scenario{
		Name:             "invalid-cm-key",
		YAMLPath:         yamlPath,
		RunnerConfigPath: runnerPath,
	}

	app := newApplier(t)
	fw := watch.NewFake()
	runner, _ := newRunner(t, app, fw)

	result := runner.Run(context.Background(), sc)

	if result.Err == nil {
		t.Fatal("expected invalid ConfigMap data key error, got nil")
	}
	if !strings.Contains(result.Err.Error(), "invalid ConfigMap data key derived from Src") {
		t.Fatalf("expected invalid key error, got %v", result.Err)
	}
	if !strings.Contains(result.Err.Error(), "empty key") {
		t.Fatalf("expected empty key reason, got %v", result.Err)
	}
	if result.Succeeded {
		t.Error("expected Succeeded=false when an invalid derived ConfigMap key is detected")
	}
}

func TestRunner_Run_FilesRejectPathTraversalAndAbsoluteSrc(t *testing.T) {
	tests := []struct {
		name       string
		src        string
		errSubstr  string
		setupExtra func(t *testing.T, baseDir string)
	}{
		{
			name:      "path traversal",
			src:       "../outside.txt",
			errSubstr: "path escapes scenario directory",
			setupExtra: func(t *testing.T, baseDir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(baseDir, "outside.txt"), []byte("secret\n"), 0o644); err != nil {
					t.Fatalf("write outside.txt: %v", err)
				}
			},
		},
		{
			name:      "absolute path",
			src:       "/etc/hosts",
			errSubstr: "absolute paths are not allowed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			baseDir := t.TempDir()
			scenarioDir := filepath.Join(baseDir, "scenario")
			if err := os.Mkdir(scenarioDir, 0o755); err != nil {
				t.Fatalf("mkdir scenario: %v", err)
			}

			yamlPath := filepath.Join(scenarioDir, "scenario.yaml")
			if err := os.WriteFile(yamlPath, []byte("---\n"), 0o644); err != nil {
				t.Fatalf("write scenario.yaml: %v", err)
			}

			if tc.setupExtra != nil {
				tc.setupExtra(t, baseDir)
			}

			runnerYAML := "test_suites:\n" +
				"  - name: traversal\n" +
				"    image: busybox:latest\n" +
				"    args: [\"echo\", \"ok\"]\n" +
				"    files:\n" +
				"      - src: " + tc.src + "\n" +
				"        mountPath: /tests/injected.txt\n"
			runnerPath := filepath.Join(scenarioDir, "runner.yaml")
			if err := os.WriteFile(runnerPath, []byte(runnerYAML), 0o644); err != nil {
				t.Fatalf("write runner.yaml: %v", err)
			}

			sc := scenario.Scenario{
				Name:             "path-guard",
				YAMLPath:         yamlPath,
				RunnerConfigPath: runnerPath,
			}

			app := newApplier(t)
			fw := watch.NewFake()
			runner, _ := newRunner(t, app, fw)

			result := runner.Run(context.Background(), sc)

			if result.Err == nil {
				t.Fatal("expected invalid file src error, got nil")
			}
			if !strings.Contains(result.Err.Error(), tc.errSubstr) {
				t.Fatalf("expected error containing %q, got %v", tc.errSubstr, result.Err)
			}
			if result.Succeeded {
				t.Error("expected Succeeded=false when invalid file src is provided")
			}
		})
	}
}

// TestRunner_Run_ScenarioTimeout_UsesRunnerYAML verifies that the timeout
// declared in runner.yaml governs the scenario independently of the parent
// context and the Runner's DefaultTimeout.
func TestRunner_Run_ScenarioTimeout_UsesRunnerYAML(t *testing.T) {
	// runner.yaml declares a very short timeout — the watch never fires,
	// so the scenario must time out on its own without relying on the caller's ctx.
	const yamlTimeout = "50ms"

	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "scenario.yaml")
	if err := os.WriteFile(yamlPath, []byte("---\n"), 0o644); err != nil {
		t.Fatalf("write scenario.yaml: %v", err)
	}
	runnerYAML := "timeout: " + yamlTimeout + "\ntest_suites:\n  - name: unit\n    image: busybox:latest\n    args: [\"echo\", \"ok\"]\n"
	runnerPath := filepath.Join(dir, "runner.yaml")
	if err := os.WriteFile(runnerPath, []byte(runnerYAML), 0o644); err != nil {
		t.Fatalf("write runner.yaml: %v", err)
	}
	sc := scenario.Scenario{
		Name:             "yaml-timeout",
		YAMLPath:         yamlPath,
		RunnerConfigPath: runnerPath,
	}

	app := newApplier(t)
	fw := watch.NewFake() // never fires — scenario must time out on its own
	// Give a very long DefaultTimeout and a long-lived parent context so that
	// neither of them is the one triggering the timeout.
	k8sClient := k8sfake.NewSimpleClientset()
	k8sClient.PrependWatchReactor("jobs", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		return true, fw, nil
	})
	sched := scheduler.New(k8sClient, minimalDynClient())
	runner := &scenario.Runner{
		Applier:        app,
		Scheduler:      sched,
		Namespace:      "test-ns",
		DefaultTimeout: time.Hour, // deliberately large — must NOT be the one that fires
	}

	result := runner.Run(context.Background(), sc)

	if result.Err == nil {
		t.Fatal("expected error due to scenario-level timeout, got nil")
	}
	if result.Succeeded {
		t.Error("expected Succeeded=false on timeout")
	}
}

// TestRunner_Run_ScenarioTimeout_FallsBackToDefault verifies that when
// runner.yaml has no timeout, the Runner's DefaultTimeout is applied.
func TestRunner_Run_ScenarioTimeout_FallsBackToDefault(t *testing.T) {
	sc := writeScenario(t, "default-timeout", minimalRunnerYAML) // no timeout field

	app := newApplier(t)
	fw := watch.NewFake() // never fires

	k8sClient := k8sfake.NewSimpleClientset()
	k8sClient.PrependWatchReactor("jobs", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		return true, fw, nil
	})
	sched := scheduler.New(k8sClient, minimalDynClient())
	runner := &scenario.Runner{
		Applier:        app,
		Scheduler:      sched,
		Namespace:      "test-ns",
		DefaultTimeout: 50 * time.Millisecond, // short — must fire quickly
	}

	result := runner.Run(context.Background(), sc)

	if result.Err == nil {
		t.Fatal("expected error from DefaultTimeout, got nil")
	}
	if result.Succeeded {
		t.Error("expected Succeeded=false on timeout")
	}
}

// ---------------------------------------------------------------------------
// Priority execution tests
// ---------------------------------------------------------------------------

// priorityRunnerYAML returns a runner.yaml where two suites share priority 0
// (run concurrently) and one suite has priority 1 (runs after them).
const samePriorityRunnerYAML = `test_suites:
  - name: concurrent-a
    image: busybox:latest
    args: ["echo", "a"]
    priority: 0
  - name: concurrent-b
    image: busybox:latest
    args: ["echo", "b"]
    priority: 0
  - name: after
    image: busybox:latest
    args: ["echo", "after"]
    priority: 1
`

// sequentialTailRunnerYAML: two suites with explicit priority 0 run first,
// then one suite with no priority runs sequentially after.
const sequentialTailRunnerYAML = `test_suites:
  - name: explicit-a
    image: busybox:latest
    args: ["echo", "a"]
    priority: 0
  - name: explicit-b
    image: busybox:latest
    args: ["echo", "b"]
    priority: 0
  - name: tail
    image: busybox:latest
    args: ["echo", "tail"]
`

// allOmittedRunnerYAML: three suites all without priority — must run
// sequentially in declaration order.
const allOmittedRunnerYAML = `test_suites:
  - name: first
    image: busybox:latest
    args: ["echo", "first"]
  - name: second
    image: busybox:latest
    args: ["echo", "second"]
  - name: third
    image: busybox:latest
    args: ["echo", "third"]
`

// TestRunner_Run_SamePriorityRunsConcurrently: two suites at priority 0 plus
// one at priority 1 — all three must complete and succeed.
func TestRunner_Run_SamePriorityRunsConcurrently(t *testing.T) {
	sc := writeScenario(t, "concurrent", samePriorityRunnerYAML)
	app := newApplier(t)
	// Three suites need three watchers: two for the concurrent group, one for after.
	fw1 := watch.NewFake()
	fw2 := watch.NewFake()
	fw3 := watch.NewFake()
	runner, _ := newRunner(t, app, fw1, fw2, fw3)

	// Signal all three watchers to succeed after a short delay.
	for _, fw := range []*watch.FakeWatcher{fw1, fw2, fw3} {
		fw := fw
		go func() {
			time.Sleep(10 * time.Millisecond)
			fw.Modify(succeededJob("any-job", "test-ns"))
		}()
	}

	result := runner.Run(context.Background(), sc)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true")
	}
	if len(result.TestSuites) != 3 {
		t.Fatalf("expected 3 TestSuiteResults, got %d", len(result.TestSuites))
	}
	for _, sr := range result.TestSuites {
		if !sr.Succeeded {
			t.Errorf("suite %q: expected Succeeded=true", sr.Name)
		}
	}
}

// TestRunner_Run_FailInPriorityGroupStopsNextGroup: one suite in the priority-0
// group fails — the priority-1 suite must NOT run.
func TestRunner_Run_FailInPriorityGroupStopsNextGroup(t *testing.T) {
	// Two suites at priority 0: one passes, one fails.
	// One suite at priority 1: must be skipped.
	const yaml = `test_suites:
  - name: pass-a
    image: busybox:latest
    args: ["echo", "a"]
    priority: 0
  - name: fail-b
    image: busybox:latest
    args: ["echo", "b"]
    priority: 0
  - name: should-not-run
    image: busybox:latest
    args: ["echo", "c"]
    priority: 1
`
	sc := writeScenario(t, "group-fail-fast", yaml)
	app := newApplier(t)
	fw1 := watch.NewFake() // pass-a
	fw2 := watch.NewFake() // fail-b
	runner, _ := newRunner(t, app, fw1, fw2)

	go func() {
		time.Sleep(10 * time.Millisecond)
		fw1.Modify(succeededJob("any-job", "test-ns"))
	}()
	go func() {
		time.Sleep(10 * time.Millisecond)
		fw2.Modify(failedJob("any-job", "test-ns", 1))
	}()

	result := runner.Run(context.Background(), sc)

	if result.Succeeded {
		t.Error("expected Succeeded=false")
	}
	// Only the 2 suites in the first group should have run; the priority-1 suite is skipped.
	if len(result.TestSuites) != 2 {
		t.Errorf("expected 2 suite results (group 0 only), got %d", len(result.TestSuites))
	}
	names := map[string]bool{}
	for _, sr := range result.TestSuites {
		names[sr.Name] = true
	}
	if names["should-not-run"] {
		t.Error("priority-1 suite should not have run after group-0 failure")
	}
}

// TestRunner_Run_ExplicitPriorityGroupsRunBeforeSequentialTail: two suites with
// explicit priority 0 run first, then the tail suite runs after — all succeed.
func TestRunner_Run_ExplicitGroupsThenSequentialTail(t *testing.T) {
	sc := writeScenario(t, "tail-test", sequentialTailRunnerYAML)
	app := newApplier(t)
	fw1 := watch.NewFake() // explicit-a
	fw2 := watch.NewFake() // explicit-b
	fw3 := watch.NewFake() // tail
	runner, _ := newRunner(t, app, fw1, fw2, fw3)

	for _, fw := range []*watch.FakeWatcher{fw1, fw2, fw3} {
		fw := fw
		go func() {
			time.Sleep(10 * time.Millisecond)
			fw.Modify(succeededJob("any-job", "test-ns"))
		}()
	}

	result := runner.Run(context.Background(), sc)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true")
	}
	if len(result.TestSuites) != 3 {
		t.Fatalf("expected 3 TestSuiteResults, got %d", len(result.TestSuites))
	}
	// Verify the tail suite was included in results.
	lastSuite := result.TestSuites[len(result.TestSuites)-1]
	if lastSuite.Name != "tail" {
		t.Errorf("last suite: got %q, want %q", lastSuite.Name, "tail")
	}
}

// TestRunner_Run_FailInExplicitGroupStopsTail: a suite in the explicit-priority
// group fails — the sequential tail suite must NOT run.
func TestRunner_Run_FailInExplicitGroupStopsTail(t *testing.T) {
	sc := writeScenario(t, "group-fail-stops-tail", sequentialTailRunnerYAML)
	app := newApplier(t)
	fw1 := watch.NewFake() // explicit-a — fails
	fw2 := watch.NewFake() // explicit-b — passes
	runner, _ := newRunner(t, app, fw1, fw2)

	go func() {
		time.Sleep(10 * time.Millisecond)
		fw1.Modify(failedJob("any-job", "test-ns", 1))
	}()
	go func() {
		time.Sleep(10 * time.Millisecond)
		fw2.Modify(succeededJob("any-job", "test-ns"))
	}()

	result := runner.Run(context.Background(), sc)

	if result.Succeeded {
		t.Error("expected Succeeded=false")
	}
	// Only the 2 explicit-priority suites ran; tail was skipped.
	if len(result.TestSuites) != 2 {
		t.Errorf("expected 2 suite results, got %d", len(result.TestSuites))
	}
	for _, sr := range result.TestSuites {
		if sr.Name == "tail" {
			t.Error("tail suite should not have run after explicit-group failure")
		}
	}
}

// TestRunner_Run_AllOmittedPriorityRunsSequentially: all suites without
// priority run in declaration order and all succeed.
func TestRunner_Run_AllOmittedPriorityRunsSequentially(t *testing.T) {
	sc := writeScenario(t, "all-sequential", allOmittedRunnerYAML)
	app := newApplier(t)
	fw1 := watch.NewFake()
	fw2 := watch.NewFake()
	fw3 := watch.NewFake()
	runner, _ := newRunner(t, app, fw1, fw2, fw3)

	for _, fw := range []*watch.FakeWatcher{fw1, fw2, fw3} {
		fw := fw
		go func() {
			time.Sleep(10 * time.Millisecond)
			fw.Modify(succeededJob("any-job", "test-ns"))
		}()
	}

	result := runner.Run(context.Background(), sc)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true")
	}
	if len(result.TestSuites) != 3 {
		t.Fatalf("expected 3 TestSuiteResults, got %d", len(result.TestSuites))
	}
	// Verify declaration order is preserved.
	wantOrder := []string{"first", "second", "third"}
	for i, want := range wantOrder {
		if result.TestSuites[i].Name != want {
			t.Errorf("TestSuites[%d].Name: got %q, want %q", i, result.TestSuites[i].Name, want)
		}
	}
}

// TestRunner_Run_OmittedPriorityFailFast: when suites have no priority and the
// first fails, the second must NOT run (original sequential fail-fast preserved).
func TestRunner_Run_OmittedPriorityFailFast(t *testing.T) {
	sc := writeScenario(t, "omitted-fail-fast", twoSuiteRunnerYAML) // both have no priority
	app := newApplier(t)
	fw := watch.NewFake()
	runner, _ := newRunner(t, app, fw)

	go func() {
		time.Sleep(10 * time.Millisecond)
		fw.Modify(failedJob("any-job", "test-ns", 1))
	}()

	result := runner.Run(context.Background(), sc)

	if result.Succeeded {
		t.Error("expected Succeeded=false")
	}
	if len(result.TestSuites) != 1 {
		t.Errorf("expected 1 suite attempted (fail-fast), got %d", len(result.TestSuites))
	}
}

// workloads in the scenario YAML before starting suites. The test wires a
// not-ready Deployment via the dynamic client, fires a watch event making it
// ready after a short delay, then expects the suite to succeed.
func TestRunner_Run_WaitsForWorkloads(t *testing.T) {
	// Write a scenario.yaml that declares a Deployment in "app-ns".
	const scenarioYAML = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-api
  namespace: app-ns
spec:
  replicas: 1
  selector:
    matchLabels:
      app: test-api
  template:
    metadata:
      labels:
        app: test-api
    spec:
      containers:
        - name: api
          image: busybox:latest
`
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "scenario.yaml")
	if err := os.WriteFile(yamlPath, []byte(scenarioYAML), 0o644); err != nil {
		t.Fatalf("write scenario.yaml: %v", err)
	}
	runnerPath := filepath.Join(dir, "runner.yaml")
	if err := os.WriteFile(runnerPath, []byte(minimalRunnerYAML), 0o644); err != nil {
		t.Fatalf("write runner.yaml: %v", err)
	}
	sc := scenario.Scenario{
		Name:             "wait-workloads",
		YAMLPath:         yamlPath,
		RunnerConfigPath: runnerPath,
	}

	// Build the fake dynamic client — used for both the Applier (patch) and
	// the Scheduler (Get/Watch on the deployment).
	scheme := runtime.NewScheme()
	gvrMap := map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "namespaces"}:      "NamespaceList",
		{Group: "", Version: "v1", Resource: "configmaps"}:      "ConfigMapList",
		{Group: "apps", Version: "v1", Resource: "deployments"}: "DeploymentList",
	}
	dynClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrMap)

	// Applier reactor: accept server-side-apply patches.
	dynClient.Fake.PrependReactor("patch", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.(k8stesting.PatchAction).GetPatchType() == types.ApplyPatchType {
			return true, nil, nil
		}
		return false, nil, nil
	})

	// Scheduler Get reactor: returns a not-ready Deployment.
	notReadyObj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": "test-api", "namespace": "app-ns"},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Available", "status": "False"},
			},
		},
	}}
	dynClient.Fake.PrependReactor("get", "deployments", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		return true, notReadyObj, nil
	})

	// Scheduler Watch reactor.
	depWatcher := watch.NewFake()
	dynClient.Fake.PrependWatchReactor("deployments", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		return true, depWatcher, nil
	})

	m := meta.NewDefaultRESTMapper(nil)
	m.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"}, meta.RESTScopeRoot)
	m.Add(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)
	m.Add(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, meta.RESTScopeNamespace)
	app := applier.NewWithMapper(dynClient, m)

	k8sClient := k8sfake.NewSimpleClientset()
	jobWatcher := watch.NewFake()
	k8sClient.PrependWatchReactor("jobs", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		return true, jobWatcher, nil
	})

	sched := scheduler.NewWithDiscovery(k8sClient, dynClient, fakeDiscoveryWithDeployments(k8sClient))
	runner := &scenario.Runner{
		Applier:        app,
		Scheduler:      sched,
		Namespace:      "test-ns",
		DefaultTimeout: time.Minute,
	}

	// After a short delay: make the Deployment ready via the dynamic watch,
	// then succeed the job.
	go func() {
		time.Sleep(20 * time.Millisecond)
		readyObj := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]interface{}{"name": "test-api", "namespace": "app-ns"},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{"type": "Available", "status": "True"},
				},
			},
		}}
		depWatcher.Modify(readyObj)
		time.Sleep(10 * time.Millisecond)
		jobWatcher.Modify(&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "any-job", Namespace: "test-ns"},
			Status: batchv1.JobStatus{
				Conditions: []batchv1.JobCondition{
					{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
				},
			},
		})
	}()

	result := runner.Run(context.Background(), sc)

	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if !result.Succeeded {
		t.Error("expected Succeeded=true after workload became ready")
	}
}
