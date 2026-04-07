package scheduler

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

// JobSpec describes the containerized test job to run.
type JobSpec struct {
	// ScenarioName is used as a label and as part of the generated Job name.
	ScenarioName string
	// Image is the container image to run (e.g. "pytest-runner:v1.2.3-rc.1").
	Image string
	// Command overrides the container entrypoint. Leave nil to use the image default.
	Command []string
	// Args are passed to the container command.
	Args []string
	// Namespace is the K8s namespace the Job will run in.
	// If the namespace does not exist it will be created.
	Namespace string
}

// JobResult holds the outcome of a completed Job.
type JobResult struct {
	Succeeded bool
	// Failed is the number of failed pod attempts recorded by K8s.
	Failed   int32
	Duration time.Duration
}

// Scheduler creates, watches, and deletes K8s batch/v1 Jobs.
type Scheduler struct {
	client       kubernetes.Interface
	pollInterval time.Duration
}

// New returns a Scheduler backed by the given Kubernetes client.
func New(client kubernetes.Interface) *Scheduler {
	return &Scheduler{
		client:       client,
		pollInterval: 5 * time.Second,
	}
}

// EnsureNamespace creates the namespace if it does not already exist.
// It never deletes namespaces.
func (s *Scheduler) EnsureNamespace(ctx context.Context, namespace string) error {
	_, err := s.client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err == nil {
		return nil // already exists
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("checking namespace %q: %w", namespace, err)
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"managed-by": "auto-qa",
			},
		},
	}
	_, err = s.client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating namespace %q: %w", namespace, err)
	}
	return nil
}

// CreateJob ensures the namespace exists and submits a batch/v1 Job.
// Returns the name of the created Job.
func (s *Scheduler) CreateJob(ctx context.Context, spec JobSpec) (string, error) {
	if err := s.EnsureNamespace(ctx, spec.Namespace); err != nil {
		return "", err
	}

	jobName := fmt.Sprintf("qa-%s-%d", spec.ScenarioName, time.Now().UnixMilli())

	backoffLimit := int32(0) // no retries — we want a clean pass/fail signal
	ttl := int32(300)        // auto-clean completed Jobs after 5 min

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: spec.Namespace,
			Labels: map[string]string{
				"managed-by": "auto-qa",
				"scenario":   spec.ScenarioName,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"managed-by": "auto-qa",
						"scenario":   spec.ScenarioName,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:    "test-runner",
							Image:   spec.Image,
							Command: spec.Command,
							Args:    spec.Args,
						},
					},
				},
			},
		},
	}

	created, err := s.client.BatchV1().Jobs(spec.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("creating job for scenario %q: %w", spec.ScenarioName, err)
	}
	return created.Name, nil
}

// WatchJob blocks until the named Job succeeds, fails, or ctx is cancelled.
// It uses the K8s Watch API with a field selector on the job name.
func (s *Scheduler) WatchJob(ctx context.Context, namespace, jobName string) (JobResult, error) {
	start := time.Now()

	watcher, err := s.client.BatchV1().Jobs(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("metadata.name=%s", jobName),
	})
	if err != nil {
		return JobResult{}, fmt.Errorf("starting watch for job %q: %w", jobName, err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return JobResult{}, fmt.Errorf("watch cancelled for job %q: %w", jobName, ctx.Err())

		case event, ok := <-watcher.ResultChan():
			if !ok {
				return JobResult{}, fmt.Errorf("watch channel closed unexpectedly for job %q", jobName)
			}
			if event.Type == watch.Error {
				return JobResult{}, fmt.Errorf("watch error event for job %q", jobName)
			}

			job, ok := event.Object.(*batchv1.Job)
			if !ok {
				continue
			}

			if result, done := jobFinished(job, start); done {
				return result, nil
			}
		}
	}
}

// DeleteJob deletes the named Job and propagates deletion to its pods.
func (s *Scheduler) DeleteJob(ctx context.Context, namespace, jobName string) error {
	propagation := metav1.DeletePropagationForeground
	err := s.client.BatchV1().Jobs(namespace).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting job %q: %w", jobName, err)
	}
	return nil
}

// jobFinished inspects a Job's conditions and returns (result, true) when the
// Job has reached a terminal state.
func jobFinished(job *batchv1.Job, start time.Time) (JobResult, bool) {
	for _, cond := range job.Status.Conditions {
		switch cond.Type {
		case batchv1.JobComplete:
			if cond.Status == corev1.ConditionTrue {
				return JobResult{
					Succeeded: true,
					Failed:    job.Status.Failed,
					Duration:  time.Since(start),
				}, true
			}
		case batchv1.JobFailed:
			if cond.Status == corev1.ConditionTrue {
				return JobResult{
					Succeeded: false,
					Failed:    job.Status.Failed,
					Duration:  time.Since(start),
				}, true
			}
		}
	}
	return JobResult{}, false
}
