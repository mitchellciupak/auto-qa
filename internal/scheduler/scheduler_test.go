package scheduler_test

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"auto-qa/internal/applier"
	"auto-qa/internal/scheduler"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// deploymentGVR is the GVR used by the fake dynamic client for Deployments.
var deploymentGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

func testSpec(scenario string) scheduler.JobSpec {
	return scheduler.JobSpec{
		ScenarioName: scenario,
		Image:        "busybox:latest",
		Command:      []string{"echo", "hello"},
		Namespace:    "test-ns",
	}
}

// newDynClient returns a fake dynamic client that knows about the deployment GVR.
func newDynClient(objects ...runtime.Object) *dynfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{
		deploymentGVR: "DeploymentList",
	}
	return dynfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objects...)
}

// availableDeployment returns an unstructured Deployment with Available=True.
func availableDeployment(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": name, "namespace": namespace},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Available", "status": "True"},
			},
		},
	}}
}

// notAvailableDeployment returns an unstructured Deployment with Available=False.
func notAvailableDeployment(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": name, "namespace": namespace},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Available", "status": "False"},
			},
		},
	}}
}

// readyDeployment returns an unstructured Deployment with Ready=True.
func readyDeployment(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": name, "namespace": namespace},
		"status": map[string]interface{}{
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True"},
			},
		},
	}}
}

// noConditionsDeployment returns an unstructured Deployment with no conditions.
func noConditionsDeployment(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": name, "namespace": namespace},
		"status":     map[string]interface{}{},
	}}
}

// emptyConditionsDeployment returns an unstructured Deployment with an explicit
// but empty status.conditions list.
func emptyConditionsDeployment(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]interface{}{"name": name, "namespace": namespace},
		"status": map[string]interface{}{
			"conditions": []interface{}{},
		},
	}}
}

func deployRef(name, namespace string) applier.WorkloadRef {
	return applier.WorkloadRef{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Namespace:  namespace,
		Name:       name,
	}
}

// fakeDiscoveryWithDeployments returns a fake discovery client that advertises
// apps/v1 Deployments, enabling the REST mapper inside the Scheduler to resolve
// the correct GVR without a live cluster.
func fakeDiscoveryWithDeployments() *fakediscovery.FakeDiscovery {
	client := fake.NewSimpleClientset()
	disc := client.Discovery().(*fakediscovery.FakeDiscovery)
	disc.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{
					Name:       "deployments",
					Kind:       "Deployment",
					Namespaced: true,
				},
			},
		},
	}
	return disc
}

// TestCreateJob_CreatesJobInNamespace verifies that CreateJob produces a
// batch/v1 Job in the expected namespace with the correct labels and image.
func TestCreateJob_CreatesJobInNamespace(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := scheduler.New(client, nil)

	jobName, err := s.CreateJob(context.Background(), testSpec("minimal"))
	if err != nil {
		t.Fatalf("CreateJob returned unexpected error: %v", err)
	}
	if jobName == "" {
		t.Fatal("CreateJob returned empty job name")
	}

	job, err := client.BatchV1().Jobs("test-ns").Get(context.Background(), jobName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Job not found in fake client after CreateJob: %v", err)
	}

	if job.Labels["scenario"] != "minimal" {
		t.Errorf("expected label scenario=minimal, got %q", job.Labels["scenario"])
	}
	if job.Labels["managed-by"] != "auto-qa" {
		t.Errorf("expected label managed-by=auto-qa, got %q", job.Labels["managed-by"])
	}

	containers := job.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	if containers[0].Image != "busybox:latest" {
		t.Errorf("expected image busybox:latest, got %q", containers[0].Image)
	}
}

// TestCreateJob_UsesSeparateScenarioAndSuiteLabels verifies that scenario and
// test suite metadata are represented by separate labels.
func TestCreateJob_UsesSeparateScenarioAndSuiteLabels(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := scheduler.New(client, nil)

	spec := testSpec("checkout")
	spec.TestSuiteName = "ui-tests"

	jobName, err := s.CreateJob(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateJob returned unexpected error: %v", err)
	}

	job, err := client.BatchV1().Jobs("test-ns").Get(context.Background(), jobName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Job not found in fake client after CreateJob: %v", err)
	}

	if got := job.Labels["scenario"]; got != "checkout" {
		t.Errorf("scenario label: got %q, want %q", got, "checkout")
	}
	if got := job.Labels["test-suite"]; got != "ui-tests" {
		t.Errorf("test-suite label: got %q, want %q", got, "ui-tests")
	}
}

// TestCreateJob_CreatesNamespaceIfMissing verifies that EnsureNamespace is
// called implicitly and the namespace is created when absent.
func TestCreateJob_CreatesNamespaceIfMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := scheduler.New(client, nil)

	_, err := s.CreateJob(context.Background(), testSpec("minimal"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = client.CoreV1().Namespaces().Get(context.Background(), "test-ns", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("namespace 'test-ns' was not created: %v", err)
	}
}

// TestCreateJob_NamespaceAlreadyExists verifies that CreateJob does not fail
// when the namespace already exists.
func TestCreateJob_NamespaceAlreadyExists(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-ns"}}
	client := fake.NewSimpleClientset(ns)
	s := scheduler.New(client, nil)

	_, err := s.CreateJob(context.Background(), testSpec("minimal"))
	if err != nil {
		t.Fatalf("CreateJob should not fail when namespace already exists: %v", err)
	}
}

// TestWatchJob_Succeeds verifies that WatchJob returns a successful result
// when the fake watch emits a JobComplete condition.
func TestWatchJob_Succeeds(t *testing.T) {
	client := fake.NewSimpleClientset()
	fakeWatcher := watch.NewFake()

	client.PrependWatchReactor("jobs", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatcher, nil
	})

	s := scheduler.New(client, nil)

	go func() {
		time.Sleep(10 * time.Millisecond)
		fakeWatcher.Modify(succeededJob("my-job", "test-ns"))
	}()

	result, err := s.WatchJob(context.Background(), "test-ns", "my-job")
	if err != nil {
		t.Fatalf("WatchJob returned unexpected error: %v", err)
	}
	if !result.Succeeded {
		t.Error("expected result.Succeeded to be true")
	}
}

// TestWatchJob_Fails verifies that WatchJob returns a non-succeeded result
// when the fake watch emits a JobFailed condition.
func TestWatchJob_Fails(t *testing.T) {
	client := fake.NewSimpleClientset()
	fakeWatcher := watch.NewFake()

	client.PrependWatchReactor("jobs", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatcher, nil
	})

	s := scheduler.New(client, nil)

	go func() {
		time.Sleep(10 * time.Millisecond)
		fakeWatcher.Modify(failedJob("my-job", "test-ns", 1))
	}()

	result, err := s.WatchJob(context.Background(), "test-ns", "my-job")
	if err != nil {
		t.Fatalf("WatchJob returned unexpected error: %v", err)
	}
	if result.Succeeded {
		t.Error("expected result.Succeeded to be false")
	}
	if result.Failed != 1 {
		t.Errorf("expected Failed=1, got %d", result.Failed)
	}
}

// TestWatchJob_ContextCancelled verifies that WatchJob respects context
// cancellation and returns an error.
func TestWatchJob_ContextCancelled(t *testing.T) {
	client := fake.NewSimpleClientset()
	fakeWatcher := watch.NewFake()

	client.PrependWatchReactor("jobs", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatcher, nil
	})

	s := scheduler.New(client, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := s.WatchJob(ctx, "test-ns", "my-job")
	if err == nil {
		t.Fatal("expected an error when context is cancelled, got nil")
	}
}

// TestDeleteJob_DeletesExistingJob verifies that DeleteJob removes the job.
func TestDeleteJob_DeletesExistingJob(t *testing.T) {
	existing := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "my-job", Namespace: "test-ns"},
	}
	client := fake.NewSimpleClientset(existing)
	s := scheduler.New(client, nil)

	if err := s.DeleteJob(context.Background(), "test-ns", "my-job"); err != nil {
		t.Fatalf("DeleteJob returned unexpected error: %v", err)
	}

	jobs, _ := client.BatchV1().Jobs("test-ns").List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Errorf("expected job list to be empty after delete, got %d items", len(jobs.Items))
	}
}

// TestDeleteJob_NotFoundIsNoOp verifies that DeleteJob does not error when
// the job is already gone.
func TestDeleteJob_NotFoundIsNoOp(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := scheduler.New(client, nil)

	if err := s.DeleteJob(context.Background(), "test-ns", "nonexistent-job"); err != nil {
		t.Fatalf("DeleteJob should not error on not-found, got: %v", err)
	}
}

// TestCreateJob_VolumesAndMountsPropagated verifies that volumes and volume
// mounts declared in the JobSpec are correctly wired into the pod template.
func TestCreateJob_VolumesAndMountsPropagated(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := scheduler.New(client, nil)

	spec := scheduler.JobSpec{
		ScenarioName: "vol-test",
		Image:        "busybox:latest",
		Command:      []string{"sh"},
		Namespace:    "test-ns",
		Volumes: []corev1.Volume{
			{
				Name: "test-data",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: "my-cm"},
					},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "test-data", MountPath: "/data"},
		},
	}

	jobName, err := s.CreateJob(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	job, err := client.BatchV1().Jobs("test-ns").Get(context.Background(), jobName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get job: %v", err)
	}

	podSpec := job.Spec.Template.Spec
	if len(podSpec.Volumes) != 1 || podSpec.Volumes[0].Name != "test-data" {
		t.Errorf("Volumes: got %+v, expected [{Name:test-data ...}]", podSpec.Volumes)
	}
	if len(podSpec.Containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(podSpec.Containers))
	}
	mounts := podSpec.Containers[0].VolumeMounts
	if len(mounts) != 1 || mounts[0].Name != "test-data" || mounts[0].MountPath != "/data" {
		t.Errorf("VolumeMounts: got %+v", mounts)
	}
}

// TestCreateJob_EnvVarsPropagated verifies that env vars in the JobSpec reach
// the container definition.
func TestCreateJob_EnvVarsPropagated(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := scheduler.New(client, nil)

	spec := scheduler.JobSpec{
		ScenarioName: "env-test",
		Image:        "busybox:latest",
		Command:      []string{"sh"},
		Namespace:    "test-ns",
		Env: []corev1.EnvVar{
			{Name: "API_BASE_URL", Value: "http://test-api.test-app.svc.cluster.local"},
		},
	}

	jobName, err := s.CreateJob(context.Background(), spec)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	job, err := client.BatchV1().Jobs("test-ns").Get(context.Background(), jobName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Get job: %v", err)
	}

	containers := job.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	env := containers[0].Env
	if len(env) != 1 || env[0].Name != "API_BASE_URL" {
		t.Errorf("Env: got %+v", env)
	}
	if env[0].Value != "http://test-api.test-app.svc.cluster.local" {
		t.Errorf("Env[0].Value: got %q", env[0].Value)
	}
}

// TestCreateJob_SanitizesScenarioNameForMetadata verifies scenario-derived
// job names and labels obey Kubernetes character and length limits.
func TestCreateJob_SanitizesScenarioNameForMetadata(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := scheduler.New(client, nil)

	longInvalidScenario := "My_SCENARIO/with spaces and symbols !@# and-very-very-long-" + strings.Repeat("x", 120)
	jobName, err := s.CreateJob(context.Background(), testSpec(longInvalidScenario))
	if err != nil {
		t.Fatalf("CreateJob returned unexpected error: %v", err)
	}

	if len(jobName) > 63 {
		t.Fatalf("expected job name length <= 63, got %d (%q)", len(jobName), jobName)
	}

	jobNamePattern := regexp.MustCompile(`^qa-[a-z0-9]([-a-z0-9]*[a-z0-9])?-[0-9]+-[a-f0-9]{8}$`)
	if !jobNamePattern.MatchString(jobName) {
		t.Fatalf("job name %q does not match expected DNS-safe pattern", jobName)
	}

	job, err := client.BatchV1().Jobs("test-ns").Get(context.Background(), jobName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Job not found in fake client after CreateJob: %v", err)
	}

	label := job.Labels["scenario"]
	if len(label) > 63 {
		t.Fatalf("expected scenario label length <= 63, got %d (%q)", len(label), label)
	}

	labelPattern := regexp.MustCompile(`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`)
	if !labelPattern.MatchString(label) {
		t.Fatalf("scenario label %q does not match Kubernetes label value pattern", label)
	}
}

// TestCreateJob_ConcurrentCallsHaveUniqueNames verifies that parallel
// CreateJob invocations do not collide on Job name generation.
func TestCreateJob_ConcurrentCallsHaveUniqueNames(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := scheduler.New(client, nil)

	const workers = 40
	errCh := make(chan error, workers)
	nameCh := make(chan string, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name, err := s.CreateJob(context.Background(), testSpec(fmt.Sprintf("parallel-%d", idx)))
			if err != nil {
				errCh <- err
				return
			}
			nameCh <- name
		}(i)
	}

	wg.Wait()
	close(errCh)
	close(nameCh)

	for err := range errCh {
		t.Fatalf("CreateJob returned unexpected error under concurrency: %v", err)
	}

	seen := make(map[string]struct{}, workers)
	for name := range nameCh {
		if _, exists := seen[name]; exists {
			t.Fatalf("duplicate job name generated: %q", name)
		}
		seen[name] = struct{}{}
	}

	if len(seen) != workers {
		t.Fatalf("expected %d unique job names, got %d", workers, len(seen))
	}
}

// TestCreateConfigMap_CreatesNewConfigMap verifies that CreateConfigMap stores
// the data under the expected name and namespace.
func TestCreateConfigMap_CreatesNewConfigMap(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := scheduler.New(client, nil)

	data := map[string]string{
		"test_foo.py": "def test_foo(): pass\n",
		"conftest.py": "import pytest\n",
	}

	if err := s.CreateConfigMap(context.Background(), "test-ns", "my-tests", data); err != nil {
		t.Fatalf("CreateConfigMap: %v", err)
	}

	cm, err := client.CoreV1().ConfigMaps("test-ns").Get(context.Background(), "my-tests", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}
	if cm.Data["test_foo.py"] != data["test_foo.py"] {
		t.Errorf("test_foo.py: got %q, want %q", cm.Data["test_foo.py"], data["test_foo.py"])
	}
	if cm.Labels["managed-by"] != "auto-qa" {
		t.Errorf("managed-by label: got %q, want %q", cm.Labels["managed-by"], "auto-qa")
	}
}

// TestCreateConfigMap_UpdatesExisting verifies that CreateConfigMap replaces an
// existing ConfigMap rather than erroring.
func TestCreateConfigMap_UpdatesExisting(t *testing.T) {
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "my-tests", Namespace: "test-ns", ResourceVersion: "7"},
		Data:       map[string]string{"old.py": "old content"},
	}
	client := fake.NewSimpleClientset(existing)
	client.PrependReactor("update", "configmaps", func(action k8stesting.Action) (bool, runtime.Object, error) {
		updateAction, ok := action.(k8stesting.UpdateAction)
		if !ok {
			return false, nil, nil
		}
		cm, ok := updateAction.GetObject().(*corev1.ConfigMap)
		if !ok {
			return false, nil, nil
		}
		if cm.ResourceVersion == "" {
			return true, nil, fmt.Errorf("metadata.resourceVersion: Required value")
		}
		if cm.ResourceVersion != existing.ResourceVersion {
			return true, nil, fmt.Errorf("metadata.resourceVersion: expected %q, got %q", existing.ResourceVersion, cm.ResourceVersion)
		}
		return false, nil, nil
	})
	s := scheduler.New(client, nil)

	newData := map[string]string{"new.py": "new content"}
	if err := s.CreateConfigMap(context.Background(), "test-ns", "my-tests", newData); err != nil {
		t.Fatalf("CreateConfigMap on existing: %v", err)
	}

	cm, err := client.CoreV1().ConfigMaps("test-ns").Get(context.Background(), "my-tests", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("ConfigMap not found after update: %v", err)
	}
	if cm.Data["new.py"] != "new content" {
		t.Errorf("new.py: got %q, want %q", cm.Data["new.py"], "new content")
	}
	// Old key must be gone.
	if _, ok := cm.Data["old.py"]; ok {
		t.Error("old.py still present after update; expected it to be replaced")
	}
}

// TestDeleteConfigMap_DeletesExisting verifies that DeleteConfigMap removes
// a ConfigMap that exists.
func TestDeleteConfigMap_DeletesExisting(t *testing.T) {
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "my-tests", Namespace: "test-ns"},
	}
	client := fake.NewSimpleClientset(existing)
	s := scheduler.New(client, nil)

	if err := s.DeleteConfigMap(context.Background(), "test-ns", "my-tests"); err != nil {
		t.Fatalf("DeleteConfigMap: %v", err)
	}

	cms, _ := client.CoreV1().ConfigMaps("test-ns").List(context.Background(), metav1.ListOptions{})
	if len(cms.Items) != 0 {
		t.Errorf("expected empty configmap list, got %d items", len(cms.Items))
	}
}

// TestDeleteConfigMap_NotFoundIsNoOp verifies that DeleteConfigMap does not
// return an error when the ConfigMap is already absent.
func TestDeleteConfigMap_NotFoundIsNoOp(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := scheduler.New(client, nil)

	if err := s.DeleteConfigMap(context.Background(), "test-ns", "nonexistent"); err != nil {
		t.Fatalf("DeleteConfigMap should not error on not-found, got: %v", err)
	}
}

// --- helpers ---

func succeededJob(name, namespace string) runtime.Object {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		},
	}
}

func failedJob(name, namespace string, failedCount int32) runtime.Object {
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

// --- WaitForWorkloads tests ---

// TestWaitForWorkloads_AlreadyReady verifies that WaitForWorkloads returns
// immediately when the resource already has Available=True.
func TestWaitForWorkloads_AlreadyReady(t *testing.T) {
	dep := availableDeployment("api", "app-ns")
	dynClient := newDynClient(dep)
	k8sClient := fake.NewSimpleClientset()
	s := scheduler.NewWithDiscovery(k8sClient, dynClient, fakeDiscoveryWithDeployments())

	refs := []applier.WorkloadRef{deployRef("api", "app-ns")}
	if err := s.WaitForWorkloads(context.Background(), refs); err != nil {
		t.Fatalf("WaitForWorkloads: unexpected error: %v", err)
	}
}

// TestWaitForWorkloads_AlreadyReadyWithReadyCondition verifies that
// WaitForWorkloads also returns immediately when the resource has Ready=True.
func TestWaitForWorkloads_AlreadyReadyWithReadyCondition(t *testing.T) {
	dep := readyDeployment("api", "app-ns")
	dynClient := newDynClient(dep)
	k8sClient := fake.NewSimpleClientset()
	s := scheduler.NewWithDiscovery(k8sClient, dynClient, fakeDiscoveryWithDeployments())

	refs := []applier.WorkloadRef{deployRef("api", "app-ns")}
	if err := s.WaitForWorkloads(context.Background(), refs); err != nil {
		t.Fatalf("WaitForWorkloads: unexpected error: %v", err)
	}
}

// TestWaitForWorkloads_NoConditions verifies that a resource with no
// status.conditions is treated as immediately ready.
func TestWaitForWorkloads_NoConditions(t *testing.T) {
	dep := noConditionsDeployment("api", "app-ns")
	dynClient := newDynClient(dep)
	k8sClient := fake.NewSimpleClientset()
	s := scheduler.NewWithDiscovery(k8sClient, dynClient, fakeDiscoveryWithDeployments())

	refs := []applier.WorkloadRef{deployRef("api", "app-ns")}
	if err := s.WaitForWorkloads(context.Background(), refs); err != nil {
		t.Fatalf("WaitForWorkloads: unexpected error: %v", err)
	}
}

// TestWaitForWorkloads_EmptyConditionsWaits verifies that an explicit empty
// status.conditions list is treated as "not ready" and does not return early.
func TestWaitForWorkloads_EmptyConditionsWaits(t *testing.T) {
	dep := emptyConditionsDeployment("api", "app-ns")
	dynClient := newDynClient(dep)
	k8sClient := fake.NewSimpleClientset()

	fakeWatcher := watch.NewFake()
	watchCalled := false
	dynClient.Fake.PrependWatchReactor("deployments", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		watchCalled = true
		return true, fakeWatcher, nil
	})

	s := scheduler.NewWithDiscovery(k8sClient, dynClient, fakeDiscoveryWithDeployments())

	go func() {
		time.Sleep(20 * time.Millisecond)
		fakeWatcher.Modify(availableDeployment("api", "app-ns"))
	}()

	refs := []applier.WorkloadRef{deployRef("api", "app-ns")}
	if err := s.WaitForWorkloads(context.Background(), refs); err != nil {
		t.Fatalf("WaitForWorkloads: unexpected error: %v", err)
	}
	if !watchCalled {
		t.Fatal("expected WaitForWorkloads to watch when conditions list is empty")
	}
}

// TestWaitForWorkloads_EmptyRefs verifies that an empty/nil refs slice is a
// no-op with no error.
func TestWaitForWorkloads_EmptyRefs(t *testing.T) {
	dynClient := newDynClient()
	k8sClient := fake.NewSimpleClientset()
	s := scheduler.New(k8sClient, dynClient)

	if err := s.WaitForWorkloads(context.Background(), nil); err != nil {
		t.Fatalf("WaitForWorkloads with nil refs: %v", err)
	}
	if err := s.WaitForWorkloads(context.Background(), []applier.WorkloadRef{}); err != nil {
		t.Fatalf("WaitForWorkloads with empty refs: %v", err)
	}
}

// TestWaitForWorkloads_NilDynamicClient verifies that a Scheduler built
// without a dynamic client returns a clear error instead of panicking.
func TestWaitForWorkloads_NilDynamicClient(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	s := scheduler.New(k8sClient, nil)

	err := s.WaitForWorkloads(context.Background(), []applier.WorkloadRef{deployRef("api", "app-ns")})
	if err == nil {
		t.Fatal("expected an error when dynamic client is nil, got nil")
	}
	if !strings.Contains(err.Error(), "dynamic client is not configured") {
		t.Fatalf("expected nil dynamic client error, got: %v", err)
	}
}

// TestWaitForWorkloads_BecomesReady verifies that WaitForWorkloads waits until
// a watch event fires with Available=True.
func TestWaitForWorkloads_BecomesReady(t *testing.T) {
	dep := notAvailableDeployment("api", "app-ns")
	dynClient := newDynClient(dep)
	k8sClient := fake.NewSimpleClientset()

	fakeWatcher := watch.NewFake()
	dynClient.Fake.PrependWatchReactor("deployments", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatcher, nil
	})

	s := scheduler.NewWithDiscovery(k8sClient, dynClient, fakeDiscoveryWithDeployments())

	go func() {
		time.Sleep(20 * time.Millisecond)
		fakeWatcher.Modify(availableDeployment("api", "app-ns"))
	}()

	refs := []applier.WorkloadRef{deployRef("api", "app-ns")}
	if err := s.WaitForWorkloads(context.Background(), refs); err != nil {
		t.Fatalf("WaitForWorkloads: unexpected error: %v", err)
	}
}

// TestWaitForWorkloads_BecomesReadyOnReadyCondition verifies that
// WaitForWorkloads treats a watch update with Ready=True as ready.
func TestWaitForWorkloads_BecomesReadyOnReadyCondition(t *testing.T) {
	dep := notAvailableDeployment("api", "app-ns")
	dynClient := newDynClient(dep)
	k8sClient := fake.NewSimpleClientset()

	fakeWatcher := watch.NewFake()
	dynClient.Fake.PrependWatchReactor("deployments", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatcher, nil
	})

	s := scheduler.NewWithDiscovery(k8sClient, dynClient, fakeDiscoveryWithDeployments())

	go func() {
		time.Sleep(20 * time.Millisecond)
		fakeWatcher.Modify(readyDeployment("api", "app-ns"))
	}()

	refs := []applier.WorkloadRef{deployRef("api", "app-ns")}
	if err := s.WaitForWorkloads(context.Background(), refs); err != nil {
		t.Fatalf("WaitForWorkloads: unexpected error: %v", err)
	}
}

// TestWaitForWorkloads_WatchStartsAtGetResourceVersion verifies that the watch
// begins from the object resourceVersion read by the initial Get call.
func TestWaitForWorkloads_WatchStartsAtGetResourceVersion(t *testing.T) {
	dep := notAvailableDeployment("api", "app-ns")
	dep.SetResourceVersion("42")

	dynClient := newDynClient(dep)
	k8sClient := fake.NewSimpleClientset()

	fakeWatcher := watch.NewFake()
	dynClient.Fake.PrependWatchReactor("deployments", func(action k8stesting.Action) (bool, watch.Interface, error) {
		watchAction, ok := action.(k8stesting.WatchAction)
		if !ok {
			t.Fatalf("expected WatchAction, got %T", action)
		}
		gotRV := watchAction.GetWatchRestrictions().ResourceVersion
		if gotRV != "42" {
			t.Fatalf("expected watch resourceVersion %q, got %q", "42", gotRV)
		}
		return true, fakeWatcher, nil
	})

	s := scheduler.NewWithDiscovery(k8sClient, dynClient, fakeDiscoveryWithDeployments())

	go func() {
		time.Sleep(20 * time.Millisecond)
		ready := availableDeployment("api", "app-ns")
		ready.SetResourceVersion("43")
		fakeWatcher.Modify(ready)
	}()

	refs := []applier.WorkloadRef{deployRef("api", "app-ns")}
	if err := s.WaitForWorkloads(context.Background(), refs); err != nil {
		t.Fatalf("WaitForWorkloads: unexpected error: %v", err)
	}
}

// TestWaitForWorkloads_ContextCancelled verifies that context cancellation
// returns an error instead of blocking forever.
func TestWaitForWorkloads_ContextCancelled(t *testing.T) {
	dep := notAvailableDeployment("api", "app-ns")
	dynClient := newDynClient(dep)
	k8sClient := fake.NewSimpleClientset()

	fakeWatcher := watch.NewFake()
	dynClient.Fake.PrependWatchReactor("deployments", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		return true, fakeWatcher, nil
	})

	s := scheduler.NewWithDiscovery(k8sClient, dynClient, fakeDiscoveryWithDeployments())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	refs := []applier.WorkloadRef{deployRef("api", "app-ns")}
	if err := s.WaitForWorkloads(ctx, refs); err == nil {
		t.Fatal("expected an error when context is cancelled, got nil")
	}
}
