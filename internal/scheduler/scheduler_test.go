package scheduler_test

import (
	"context"
	"testing"
	"time"

	"auto-qa/internal/scheduler"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func testSpec(scenario string) scheduler.JobSpec {
	return scheduler.JobSpec{
		ScenarioName: scenario,
		Image:        "busybox:latest",
		Command:      []string{"echo", "hello"},
		Namespace:    "test-ns",
	}
}

// TestCreateJob_CreatesJobInNamespace verifies that CreateJob produces a
// batch/v1 Job in the expected namespace with the correct labels and image.
func TestCreateJob_CreatesJobInNamespace(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := scheduler.New(client)

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

// TestCreateJob_CreatesNamespaceIfMissing verifies that EnsureNamespace is
// called implicitly and the namespace is created when absent.
func TestCreateJob_CreatesNamespaceIfMissing(t *testing.T) {
	client := fake.NewSimpleClientset()
	s := scheduler.New(client)

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
	s := scheduler.New(client)

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

	s := scheduler.New(client)

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

	s := scheduler.New(client)

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

	s := scheduler.New(client)

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
	s := scheduler.New(client)

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
	s := scheduler.New(client)

	if err := s.DeleteJob(context.Background(), "test-ns", "nonexistent-job"); err != nil {
		t.Fatalf("DeleteJob should not error on not-found, got: %v", err)
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
