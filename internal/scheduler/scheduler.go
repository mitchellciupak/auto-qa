package scheduler

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"

	"auto-qa/internal/applier"
	"auto-qa/internal/constants"
)

const (
	// Kubernetes label values must be <= 63 chars and start/end with alphanumeric.
	maxLabelValueLen = 63
	// Keep generated job names DNS-label-safe and short enough for controllers/pods.
	maxJobNameLen = 63
)

var (
	nonDNSLabelChars   = regexp.MustCompile(`[^a-z0-9-]+`)
	nonLabelValueChars = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)
	multiHyphen        = regexp.MustCompile(`-{2,}`)
	trimNonAlnum       = regexp.MustCompile(`^[^A-Za-z0-9]+|[^A-Za-z0-9]+$`)
	trimNonDNSAlnum    = regexp.MustCompile(`^[^a-z0-9]+|[^a-z0-9]+$`)
)

// JobSpec describes the containerized test job to run.
type JobSpec struct {
	// ScenarioName is used as the scenario label and as part of the generated Job name.
	ScenarioName string
	// TestSuiteName is optional and, when provided, is added as a dedicated
	// label and included in the generated Job name.
	TestSuiteName string
	// Image is the container image to run (e.g. "pytest-runner:v1.2.3-rc.1").
	Image string
	// Command overrides the container entrypoint. Leave nil to use the image default.
	Command []string
	// Args are passed to the container command.
	Args []string
	// Env holds additional environment variables injected into the test container.
	Env []corev1.EnvVar
	// VolumeMounts are mounted into the test container at the specified paths.
	VolumeMounts []corev1.VolumeMount
	// Volumes are attached to the pod and made available for mounting.
	Volumes []corev1.Volume
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
	client    kubernetes.Interface
	dynClient dynamic.Interface
	discovery discovery.DiscoveryInterface

	restMapperMu sync.RWMutex
	restMapper   meta.RESTMapper
}

// New returns a Scheduler backed by the given Kubernetes and dynamic clients.
func New(client kubernetes.Interface, dynClient dynamic.Interface) *Scheduler {
	return &Scheduler{
		client:    client,
		dynClient: dynClient,
		discovery: client.Discovery(),
	}
}

// NewWithDiscovery returns a Scheduler with an explicit discovery client,
// overriding the one derived from client. Intended for testing only.
func NewWithDiscovery(client kubernetes.Interface, dynClient dynamic.Interface, disc discovery.DiscoveryInterface) *Scheduler {
	s := New(client, dynClient)
	s.discovery = disc
	return s
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
			Name:   namespace,
			Labels: managedByLabels(),
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

	timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())
	uniqueSuffix := fmt.Sprintf("%s-%s", timestamp, uuid.NewString()[:8])
	jobName := buildJobName(jobNameScope(spec.ScenarioName, spec.TestSuiteName), uniqueSuffix)
	labels := scenarioLabels(spec.ScenarioName, spec.TestSuiteName)

	backoffLimit := int32(0) // no retries — we want a clean pass/fail signal
	ttl := int32(300)        // auto-clean completed Jobs after 5 min

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: spec.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Volumes:       spec.Volumes,
					Containers: []corev1.Container{
						{
							Name:         "test-runner",
							Image:        spec.Image,
							Command:      spec.Command,
							Args:         spec.Args,
							Env:          spec.Env,
							VolumeMounts: spec.VolumeMounts,
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

// CreateConfigMap creates or updates a ConfigMap in the given namespace.
// If a ConfigMap with the same name already exists it is replaced entirely.
// The data map maps filename keys to file content values.
func (s *Scheduler) CreateConfigMap(ctx context.Context, namespace, name string, data map[string]string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    managedByLabels(),
		},
		Data: data,
	}

	_, err := s.client.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating configmap %q in namespace %q: %w", name, namespace, err)
	}

	existing, err := s.client.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting existing configmap %q in namespace %q: %w", name, namespace, err)
	}
	cm.ResourceVersion = existing.ResourceVersion

	// Already exists — replace it so stale keys are removed.
	_, err = s.client.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating configmap %q in namespace %q: %w", name, namespace, err)
	}
	return nil
}

// DeleteConfigMap deletes the named ConfigMap. NotFound is treated as a no-op.
func (s *Scheduler) DeleteConfigMap(ctx context.Context, namespace, name string) error {
	err := s.client.CoreV1().ConfigMaps(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting configmap %q in namespace %q: %w", name, namespace, err)
	}
	return nil
}

// WaitForWorkloads blocks until every resource in refs is ready (or
// immediately ready because it has no status.conditions). Readiness is
// defined as having a condition with type=Available or type=Ready and
// status=True in status.conditions. Resources whose status block carries no
// conditions at all (e.g. Service, ConfigMap, Ingress) are treated as
// immediately ready once a Get confirms they exist.
//
// GVR derivation is done via API discovery and REST mapping.
//
// Returns an error if ctx is cancelled or any watch operation fails.
func (s *Scheduler) WaitForWorkloads(ctx context.Context, refs []applier.WorkloadRef) error {
	if s.dynClient == nil {
		return fmt.Errorf("dynamic client is not configured")
	}

	for _, ref := range refs {
		if err := s.waitOneWorkload(ctx, ref); err != nil {
			return err
		}
	}
	return nil
}

// waitOneWorkload waits for a single workload to become ready.
func (s *Scheduler) waitOneWorkload(ctx context.Context, ref applier.WorkloadRef) error {
	gvr, err := s.gvrForRef(ref)
	if err != nil {
		return fmt.Errorf("resolving GVR for %s/%s: %w", ref.Kind, ref.Name, err)
	}
	ri := s.dynClient.Resource(gvr).Namespace(ref.Namespace)

	obj, err := ri.Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting %s/%s in %q: %w", ref.Kind, ref.Name, ref.Namespace, err)
	}
	ready, hasConditions := workloadReady(obj)
	if !hasConditions || ready {
		return nil
	}

	// Not yet ready — watch until Available=True or ctx cancels.
	watcher, err := ri.Watch(ctx, metav1.ListOptions{
		FieldSelector:   fmt.Sprintf("metadata.name=%s", ref.Name),
		ResourceVersion: obj.GetResourceVersion(),
	})
	if err != nil {
		return fmt.Errorf("watching %s/%s in %q: %w", ref.Kind, ref.Name, ref.Namespace, err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s/%s in %q: %w", ref.Kind, ref.Name, ref.Namespace, ctx.Err())
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed for %s/%s in %q", ref.Kind, ref.Name, ref.Namespace)
			}
			if event.Type == watch.Error {
				return fmt.Errorf("watch error for %s/%s in %q", ref.Kind, ref.Name, ref.Namespace)
			}
			u, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			if ready, _ := workloadReady(u); ready {
				return nil
			}
		}
	}
}

// workloadReady inspects status.conditions on an unstructured object.
// Returns (ready, hasConditions):
//   - (true, true)  - Available=True or Ready=True condition present
//   - (false, true) - conditions key exists but no accepted readiness condition is True (including empty list)
//   - (false, false) - no conditions key; caller treats resource as immediately ready
func workloadReady(obj *unstructured.Unstructured) (ready bool, hasConditions bool) {
	conditions, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return false, false
	}
	if len(conditions) == 0 {
		return false, true
	}
	for _, c := range conditions {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		conditionType, _ := cm["type"].(string)
		status, _ := cm["status"].(string)
		if (conditionType == "Available" || conditionType == "Ready") && strings.EqualFold(status, "True") {
			return true, true
		}
	}
	return false, true
}

// gvrForRef resolves the GroupVersionResource for a WorkloadRef using discovery
// data from a cached REST mapper, producing the correct plural resource name
// (e.g. "ingresses" for Ingress) without static string manipulation.
func (s *Scheduler) gvrForRef(ref applier.WorkloadRef) (schema.GroupVersionResource, error) {
	mapper, err := s.mapper()
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	gvk := schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind)
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, fmt.Errorf("mapping %s: %w", ref.Kind, err)
	}
	return mapping.Resource, nil
}

func (s *Scheduler) mapper() (meta.RESTMapper, error) {
	s.restMapperMu.RLock()
	if s.restMapper != nil {
		mapper := s.restMapper
		s.restMapperMu.RUnlock()
		return mapper, nil
	}
	s.restMapperMu.RUnlock()

	s.restMapperMu.Lock()
	defer s.restMapperMu.Unlock()

	if s.restMapper != nil {
		return s.restMapper, nil
	}

	gr, err := restmapper.GetAPIGroupResources(s.discovery)
	if err != nil {
		return nil, fmt.Errorf("fetching API group resources: %w", err)
	}

	s.restMapper = restmapper.NewDiscoveryRESTMapper(gr)
	return s.restMapper, nil
}

func managedByLabels() map[string]string {
	return map[string]string{
		constants.ManagedByLabelKey: constants.ManagedByLabelValue,
	}
}

func scenarioLabels(scenarioName, testSuiteName string) map[string]string {
	labels := managedByLabels()
	labels[constants.ScenarioLabelKey] = sanitizeLabelValue(scenarioName)
	if strings.TrimSpace(testSuiteName) != "" {
		labels[constants.TestSuiteLabelKey] = sanitizeLabelValue(testSuiteName)
	}
	return labels
}

func jobNameScope(scenarioName, testSuiteName string) string {
	testSuiteName = strings.TrimSpace(testSuiteName)
	if testSuiteName == "" {
		return scenarioName
	}
	return scenarioName + "-" + testSuiteName
}

// buildJobName creates a DNS-label-safe job name in the form qa-<scenario>-<suffix>.
// The suffix is expected to carry high-entropy uniqueness (for example,
// "<unix-millis>-<uuid-fragment>") so concurrent CreateJob calls do not collide.
func buildJobName(scenarioName, suffix string) string {
	// Fixed pieces are "qa-" prefix and "-<suffix>" suffix.
	maxScenarioLen := maxJobNameLen - len("qa-") - len("-") - len(suffix)
	if maxScenarioLen < 1 {
		maxScenarioLen = 1
	}

	safeScenario := sanitizeDNSLabel(scenarioName, "scenario")
	if len(safeScenario) > maxScenarioLen {
		safeScenario = sanitizeDNSLabel(safeScenario[:maxScenarioLen], "scenario")
	}

	return fmt.Sprintf("qa-%s-%s", safeScenario, suffix)
}

func sanitizeLabelValue(v string) string {
	v = strings.TrimSpace(v)
	v = nonLabelValueChars.ReplaceAllString(v, "-")
	v = multiHyphen.ReplaceAllString(v, "-")
	v = trimNonAlnum.ReplaceAllString(v, "")
	if v == "" {
		v = "scenario"
	}
	if len(v) > maxLabelValueLen {
		v = v[:maxLabelValueLen]
		v = trimNonAlnum.ReplaceAllString(v, "")
		if v == "" {
			v = "scenario"
		}
	}
	return v
}

func sanitizeDNSLabel(v, fallback string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = nonDNSLabelChars.ReplaceAllString(v, "-")
	v = multiHyphen.ReplaceAllString(v, "-")
	v = trimNonDNSAlnum.ReplaceAllString(v, "")
	if v == "" {
		v = fallback
	}
	if len(v) > maxJobNameLen {
		v = v[:maxJobNameLen]
		v = trimNonDNSAlnum.ReplaceAllString(v, "")
		if v == "" {
			v = fallback
		}
	}
	return v
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
