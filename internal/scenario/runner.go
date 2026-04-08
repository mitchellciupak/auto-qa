package scenario

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"auto-qa/internal/applier"
	"auto-qa/internal/scheduler"
)

const cleanupTimeout = 30 * time.Second

type runnerApplier interface {
	ApplyFile(ctx context.Context, path string) error
	DeleteFile(ctx context.Context, path string) error
}

type runnerWorkloadReader interface {
	WorkloadsFromFile(path string) ([]applier.WorkloadRef, error)
}

type runnerScheduler interface {
	EnsureNamespace(ctx context.Context, namespace string) error
	WaitForWorkloads(ctx context.Context, refs []applier.WorkloadRef) error
	CreateConfigMap(ctx context.Context, namespace, name string, data map[string]string) error
	DeleteConfigMap(ctx context.Context, namespace, name string) error
	CreateJob(ctx context.Context, spec scheduler.JobSpec) (string, error)
	WatchJob(ctx context.Context, namespace, jobName string) (scheduler.JobResult, error)
	FetchJobLogs(ctx context.Context, namespace, jobName string) (string, error)
	DeleteJob(ctx context.Context, namespace, jobName string) error
}

// Runner orchestrates a single scenario end-to-end:
// load runner config, apply the scenario YAML, run test suites by priority
// (concurrent within a priority group, sequential across groups and for
// no-priority suites), collect logs, then always tear down regardless of outcome.
type Runner struct {
	Applier   runnerApplier
	Scheduler runnerScheduler
	// Namespace is the Kubernetes namespace Jobs run in.
	Namespace string
	// DefaultTimeout is the per-scenario timeout applied when runner.yaml does
	// not specify its own timeout. Must be positive.
	DefaultTimeout time.Duration
}

// Run executes the full lifecycle for sc within the given context:
//  1. Ensure the job namespace exists (so scenario YAML can reference it).
//  2. Load runner.yaml to discover test suites.
//  3. Derive a per-scenario timeout context (from runner.yaml or DefaultTimeout).
//  4. Apply scenario.yaml to bring up the app stack.
//  5. Wait for all namespaced workloads in the scenario namespaces to become available.
//  6. Run test suites in two phases:
//     a. Explicit-priority groups run in ascending priority order; suites in the
//     same group run concurrently.
//     b. Suites with no priority run sequentially in declaration order.
//     c. For each executed test suite: if files are declared, create a ConfigMap
//     and inject volume/mounts; then create Job → watch → fetch logs → delete
//     Job → delete ConfigMap (if any).
//     d. Fail-fast between priority groups and in the sequential tail.
//  7. Always tear down: delete scenario.yaml resources.
func (r *Runner) Run(ctx context.Context, sc Scenario) Result {
	start := time.Now()
	log := slog.With("scenario", sc.Name)
	scenarioDir := filepath.Dir(sc.YAMLPath)

	// Ensure job namespace exists
	if err := r.Scheduler.EnsureNamespace(ctx, r.Namespace); err != nil {
		log.Error("ensuring namespace", "namespace", r.Namespace, "error", err)
		return Result{Name: sc.Name, Err: err}
	}

	// Load runner config
	cfg, err := LoadRunnerConfig(sc.RunnerConfigPath)
	if err != nil {
		log.Error("loading runner config", "error", err)
		return Result{Name: sc.Name, Err: err}
	}

	// Derive per-scenario timeout context
	timeout := cfg.TimeoutDur
	if timeout <= 0 {
		if r.DefaultTimeout <= 0 {
			err := fmt.Errorf("invalid timeout configuration: runner.yaml timeout is not set and Runner.DefaultTimeout must be > 0, got %s", r.DefaultTimeout)
			log.Error("invalid timeout configuration", "default_timeout", r.DefaultTimeout, "error", err)
			return Result{Name: sc.Name, Err: err}
		}
		timeout = r.DefaultTimeout
	}
	scenarioCtx, scenarioCancel := context.WithTimeout(ctx, timeout)
	defer scenarioCancel()
	log.Info("scenario timeout set", "timeout", timeout)

	// Apply scenario YAML
	log.Info("applying scenario yaml", "path", sc.YAMLPath)
	if err := r.Applier.ApplyFile(scenarioCtx, sc.YAMLPath); err != nil {
		log.Error("applying scenario yaml", "error", err)
		r.teardown(log, sc.YAMLPath)
		return Result{Name: sc.Name, Err: err, Duration: time.Since(start)}
	}

	// Wait for workloads to become available
	refs, err := r.workloadsFromFile(sc.YAMLPath)
	if err != nil {
		log.Error("reading workloads from scenario yaml", "error", err)
		r.teardown(log, sc.YAMLPath)
		return Result{Name: sc.Name, Err: err, Duration: time.Since(start)}
	}
	if len(refs) > 0 {
		log.Info("waiting for scenario resources", "refs", refs)
		if err := r.Scheduler.WaitForWorkloads(scenarioCtx, refs); err != nil {
			log.Error("waiting for scenario resources", "error", err)
			r.teardown(log, sc.YAMLPath)
			return Result{Name: sc.Name, Err: err, Duration: time.Since(start)}
		}
	}

	// Run test suites grouped by priority.
	// Phase 1: explicit-priority groups in ascending order, each run concurrently.
	// Phase 2: test suites with no priority set, run sequentially in declaration order.
	testSuites, allPassed, testSuiteErr := r.runSuitesByPriority(scenarioCtx, log, sc.Name, cfg.TestSuites, scenarioDir)
	if testSuiteErr != nil {
		r.teardown(log, sc.YAMLPath)
		return Result{
			Name:       sc.Name,
			Err:        testSuiteErr,
			Duration:   time.Since(start),
			TestSuites: testSuites,
		}
	}

	// Always tear down
	r.teardown(log, sc.YAMLPath)

	return Result{
		Name:       sc.Name,
		Succeeded:  allPassed,
		Duration:   time.Since(start),
		TestSuites: testSuites,
	}
}

func (r *Runner) workloadsFromFile(path string) ([]applier.WorkloadRef, error) {
	if reader, ok := r.Applier.(runnerWorkloadReader); ok {
		return reader.WorkloadsFromFile(path)
	}
	return applier.WorkloadsFromFile(path)
}

// runSuitesByPriority organises test suites into execution phases and runs them:
//
//  1. Phase 1 — explicit-priority groups: test suites with a Priority set are
//     grouped by their priority value and run in ascending order. Within each
//     group all test suites run concurrently. If any test suite in a group fails, the
//     remaining groups are skipped (fail-fast between groups), but every test suite
//     in the current group is always allowed to finish.
//
//  2. Phase 2 — sequential tail: test suites with no Priority set run one at a time
//     in declaration order, preserving the original fail-fast behaviour.
//
// Disabled suites (enabled: false) are skipped and reported with Skipped=true.
// They are not treated as failures.
//
// Returns the accumulated results, a boolean indicating whether all non-skipped
// test suites passed, and any orchestration error (distinct from a test suite
// failing).
func (r *Runner) runSuitesByPriority(ctx context.Context, log *slog.Logger, scenarioName string, specs []TestSuiteSpec, scenarioDir string) ([]TestSuiteResult, bool, error) {
	// Separate explicit-priority test suites from the sequential tail.
	type groupedSpec struct{ spec TestSuiteSpec }

	priorityGroups := make(map[int][]groupedSpec) // priority value -> specs in that group
	var sortedPriorities []int
	seenPriority := make(map[int]bool)

	var sequentialTail []TestSuiteSpec

	for _, s := range specs {
		if s.Priority == nil {
			sequentialTail = append(sequentialTail, s)
		} else {
			p := *s.Priority
			priorityGroups[p] = append(priorityGroups[p], groupedSpec{spec: s})
			if !seenPriority[p] {
				seenPriority[p] = true
				sortedPriorities = append(sortedPriorities, p)
			}
		}
	}
	sort.Ints(sortedPriorities)

	var allResults []TestSuiteResult
	allPassed := true

	// Phase 1: run each priority group concurrently, in ascending order.
	for _, p := range sortedPriorities {
		group := priorityGroups[p]
		log.Info("running priority group", "priority", p, "test_suites", len(group))

		type testSuiteOutcome struct {
			result TestSuiteResult
			err    error
		}

		outcomes := make([]testSuiteOutcome, len(group))
		var wg sync.WaitGroup
		for i, entry := range group {
			if !testSuiteEnabled(entry.spec) {
				outcomes[i] = testSuiteOutcome{result: TestSuiteResult{Name: entry.spec.Name, Skipped: true}}
				log.Info("skipping disabled test suite", "name", entry.spec.Name)
				continue
			}
			wg.Add(1)
			go func(idx int, testSuite TestSuiteSpec) {
				defer wg.Done()
				sr, err := r.runTestSuite(ctx, log, scenarioName, testSuite, scenarioDir)
				outcomes[idx] = testSuiteOutcome{sr, err}
			}(i, entry.spec)
		}
		wg.Wait()

		// Collect results; propagate any orchestration error immediately.
		for _, o := range outcomes {
			if o.err != nil {
				return allResults, false, o.err
			}
			allResults = append(allResults, o.result)
			if !o.result.Skipped && !o.result.Succeeded {
				allPassed = false
			}
		}

		// Fail-fast: don't start the next priority group if this one had failures.
		if !allPassed {
			return allResults, false, nil
		}
	}

	// Phase 2: run the sequential tail in declaration order.
	for _, testSuite := range sequentialTail {
		if !testSuiteEnabled(testSuite) {
			allResults = append(allResults, TestSuiteResult{Name: testSuite.Name, Skipped: true})
			log.Info("skipping disabled test suite", "name", testSuite.Name)
			continue
		}
		sr, err := r.runTestSuite(ctx, log, scenarioName, testSuite, scenarioDir)
		if err != nil {
			return allResults, false, err
		}
		allResults = append(allResults, sr)
		if !sr.Skipped && !sr.Succeeded {
			return allResults, false, nil
		}
	}

	return allResults, allPassed, nil
}

func testSuiteEnabled(s TestSuiteSpec) bool {
	return s.Enabled == nil || *s.Enabled
}

// runTestSuite creates, watches, collects logs from, and deletes one test Job.
// If the test suite declares files, a ConfigMap is created before the Job and
// deleted after. Volume and volumeMount entries are injected automatically.
// Returns (TestSuiteResult, nil) on normal completion (pass or fail).
// Returns (TestSuiteResult{}, error) only on orchestration failure.
func (r *Runner) runTestSuite(ctx context.Context, log *slog.Logger, scenarioName string, testSuite TestSuiteSpec, scenarioDir string) (TestSuiteResult, error) {
	testSuiteStart := time.Now()
	log = log.With("test_suite", testSuite.Name)

	// If the test suite declares files, build and create the ConfigMap, then inject
	// the volume and per-file volumeMounts into the test suite spec.
	var cmName string
	if len(testSuite.Files) > 0 {
		name, data, err := buildTestSuiteConfigMapData(scenarioName, testSuite.Name, scenarioDir, testSuite.Files)
		if err != nil {
			log.Error("building test suite configmap", "error", err)
			return TestSuiteResult{}, err
		}
		cmName = name
		log.Debug("creating test suite configmap", "name", cmName, "namespace", r.Namespace, "files", len(data))
		if err := r.Scheduler.CreateConfigMap(ctx, r.Namespace, cmName, data); err != nil {
			// Best-effort cleanup in case the API server created the ConfigMap
			// before returning an error (e.g. timeout while waiting for response).
			r.deleteTestSuiteConfigMap(log, cmName)
			return TestSuiteResult{}, fmt.Errorf("creating configmap %q: %w", cmName, err)
		}
		testSuite = injectFilesIntoTestSuite(testSuite, cmName)
	}

	spec := scheduler.JobSpec{
		ScenarioName:  scenarioName,
		TestSuiteName: testSuite.Name,
		Image:         testSuite.Image,
		Command:       testSuite.Command,
		Args:          testSuite.Args,
		Env:           testSuite.Env,
		VolumeMounts:  testSuite.VolumeMounts,
		Volumes:       testSuite.Volumes,
		Namespace:     r.Namespace,
	}

	log.Info("creating test suite job", "image", testSuite.Image, "namespace", r.Namespace)
	jobName, err := r.Scheduler.CreateJob(ctx, spec)
	if err != nil {
		log.Error("creating test suite job", "error", err)
		r.deleteTestSuiteConfigMap(log, cmName)
		return TestSuiteResult{}, err
	}
	log.Info("test suite job created", "job", jobName)

	jobResult, watchErr := r.Scheduler.WatchJob(ctx, r.Namespace, jobName)

	// Fetch logs and delete job using a fresh context so a cancelled parent
	// doesn't prevent cleanup after timeout.
	cleanupCtx, cleanupCancel := cleanupContext()
	defer cleanupCancel()

	logs, logErr := r.Scheduler.FetchJobLogs(cleanupCtx, r.Namespace, jobName)
	if logErr != nil {
		log.Warn("fetching test suite job logs", "job", jobName, "error", logErr)
	}

	if delErr := r.Scheduler.DeleteJob(cleanupCtx, r.Namespace, jobName); delErr != nil {
		log.Warn("deleting test suite job", "job", jobName, "error", delErr)
	} else {
		log.Info("test suite job deleted", "job", jobName)
	}

	// Delete the test suite's ConfigMap now that the job is done.
	r.deleteTestSuiteConfigMap(log, cmName)

	if watchErr != nil {
		log.Error("watching test suite job", "error", watchErr)
		return TestSuiteResult{}, watchErr
	}

	sr := TestSuiteResult{
		Name:      testSuite.Name,
		Succeeded: jobResult.Succeeded,
		Duration:  time.Since(testSuiteStart),
		Logs:      logs,
	}

	if sr.Succeeded {
		log.Info("test suite passed",
			"job", jobName,
			"duration", sr.Duration.Round(time.Millisecond),
		)
	} else {
		log.Error("test suite failed",
			"job", jobName,
			"failed_attempts", jobResult.Failed,
			"duration", sr.Duration.Round(time.Millisecond),
		)
	}

	if logs != "" {
		log.Info("test suite logs\n" + logs)
	}

	return sr, nil
}

// teardown deletes the resources described by the scenario YAML.
// Uses a fresh context so teardown is never blocked by a cancelled parent.
// Errors are logged as warnings.
func (r *Runner) teardown(log *slog.Logger, yamlPath string) {
	cleanupCtx, cancel := cleanupContext()
	defer cancel()

	log.Info("tearing down scenario yaml", "path", yamlPath)
	if err := r.Applier.DeleteFile(cleanupCtx, yamlPath); err != nil {
		log.Warn("tearing down scenario yaml", "error", err)
	}
}

// deleteTestSuiteConfigMap deletes a test suite-scoped ConfigMap if name is non-empty.
// Errors are logged as warnings; NotFound is silently ignored by the scheduler.
func (r *Runner) deleteTestSuiteConfigMap(log *slog.Logger, name string) {
	if name == "" {
		return
	}
	cleanupCtx, cancel := cleanupContext()
	defer cancel()

	log.Debug("deleting test suite configmap", "name", name, "namespace", r.Namespace)
	if err := r.Scheduler.DeleteConfigMap(cleanupCtx, r.Namespace, name); err != nil {
		log.Warn("deleting test suite configmap", "name", name, "error", err)
	}
}

// buildTestSuiteConfigMapData reads the files declared on a test suite and returns the
// auto-generated ConfigMap name plus the data map (cmKey → file contents).
// The CM name is "<scenario>-<test-suite>-files" sanitized to a valid k8s name.
// Each file's CM key is a sanitized form of its Src path.
func buildTestSuiteConfigMapData(scenarioName, testSuiteName, scenarioDir string, files []TestSuiteFileSpec) (string, map[string]string, error) {
	cmName := buildTestSuiteConfigMapName(scenarioName, testSuiteName)
	data := make(map[string]string, len(files))
	keySources := make(map[string]string, len(files))
	for _, f := range files {
		filePath, err := resolveScenarioFilePath(scenarioDir, f.Src)
		if err != nil {
			return "", nil, err
		}

		content, err := os.ReadFile(filePath)
		if err != nil {
			return "", nil, fmt.Errorf("reading file %q: %w", f.Src, err)
		}
		key := srcToKey(f.Src)
		if err := validateConfigMapDataKey(key); err != nil {
			return "", nil, fmt.Errorf("invalid ConfigMap data key derived from Src %q: %w", f.Src, err)
		}
		if existingSrc, exists := keySources[key]; exists {
			return "", nil, fmt.Errorf("duplicate ConfigMap data key %q derived from Src values %q and %q", key, existingSrc, f.Src)
		}
		keySources[key] = f.Src
		data[key] = string(content)
	}
	return cmName, data, nil
}

// buildTestSuiteConfigMapName returns a DNS-compatible ConfigMap name for a
// scenario/test-suite pair. It always returns a non-empty value <= 63 chars.
func buildTestSuiteConfigMapName(scenarioName, testSuiteName string) string {
	base := sanitizeDNSLabel(scenarioName + "-" + testSuiteName)
	return buildBoundedName(base, "-files", "files", scenarioName+"\x00"+testSuiteName)
}

func shortHexSHA1(s string) string {
	sum := sha1.Sum([]byte(s))
	// 10 hex chars (40 bits) keeps names compact while being stable.
	return hex.EncodeToString(sum[:])[:10]
}

// resolveScenarioFilePath validates src as a scenario-relative path and returns
// its absolute location under scenarioDir.
func resolveScenarioFilePath(scenarioDir, src string) (string, error) {
	cleanSrc := filepath.Clean(src)
	if filepath.IsAbs(cleanSrc) {
		return "", fmt.Errorf("invalid file src %q: absolute paths are not allowed", src)
	}

	baseAbs, err := filepath.Abs(scenarioDir)
	if err != nil {
		return "", fmt.Errorf("resolving scenario dir %q: %w", scenarioDir, err)
	}
	targetAbs, err := filepath.Abs(filepath.Join(baseAbs, cleanSrc))
	if err != nil {
		return "", fmt.Errorf("resolving file src %q: %w", src, err)
	}

	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil {
		return "", fmt.Errorf("validating file src %q: %w", src, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid file src %q: path escapes scenario directory", src)
	}

	return targetAbs, nil
}

// injectFilesIntoTestSuite appends an auto-generated volume (backed by cmName) and
// one volumeMount per file (using SubPath for exact file placement) to a copy
// of the test suite spec. The original test suite is not mutated.
func injectFilesIntoTestSuite(testSuite TestSuiteSpec, cmName string) TestSuiteSpec {
	volumeName := buildTestSuiteVolumeName(cmName)
	vol := corev1.Volume{
		Name: volumeName,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
			},
		},
	}
	testSuite.Volumes = append(append([]corev1.Volume{}, testSuite.Volumes...), vol)

	mounts := append([]corev1.VolumeMount{}, testSuite.VolumeMounts...)
	for _, f := range testSuite.Files {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      volumeName,
			MountPath: f.MountPath,
			SubPath:   srcToKey(f.Src),
		})
	}
	testSuite.VolumeMounts = mounts
	return testSuite
}

// buildTestSuiteVolumeName returns a DNS_LABEL-compatible volume name derived
// from the ConfigMap name. It always returns a non-empty value <= 63 chars.
func buildTestSuiteVolumeName(cmName string) string {
	base := sanitizeDNSLabel(cmName + "-vol")
	return buildBoundedName(base, "", "vol", cmName)
}

func buildBoundedName(base, suffix, fallbackPrefix, hashInput string) string {
	const maxNameLen = 63

	hash := shortHexSHA1(hashInput)
	if base == "" {
		return fallbackPrefix + "-" + hash
	}

	name := base + suffix
	if len(name) <= maxNameLen {
		return name
	}

	maxPrefixLen := maxNameLen - len(suffix) - 1 - len(hash)
	prefix := base
	if len(prefix) > maxPrefixLen {
		prefix = prefix[:maxPrefixLen]
		prefix = strings.Trim(prefix, "-")
	}
	if prefix == "" {
		prefix = fallbackPrefix
	}
	return prefix + suffix + "-" + hash
}

// nonAlphaNum matches any character that is not alphanumeric, dot, or hyphen.
var nonAlphaNum = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// multiHyphen matches two or more consecutive hyphens.
var multiHyphen = regexp.MustCompile(`-{2,}`)

// configMapKeyPattern matches Kubernetes ConfigMap key characters.
// See: alphanumerics, '-', '_' and '.'.
var configMapKeyPattern = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

// srcToKey converts a file Src path to a safe ConfigMap data key by replacing
// path separators and other disallowed characters with hyphens.
func srcToKey(src string) string {
	s := strings.ReplaceAll(src, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	s = nonAlphaNum.ReplaceAllString(s, "-")
	s = multiHyphen.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

func validateConfigMapDataKey(key string) error {
	if key == "" {
		return fmt.Errorf("empty key")
	}
	if len(key) > 253 {
		return fmt.Errorf("key length %d exceeds maximum 253", len(key))
	}
	if !configMapKeyPattern.MatchString(key) {
		return fmt.Errorf("key contains invalid characters")
	}
	return nil
}

// sanitizeDNSLabel transforms s into a DNS_LABEL-safe token:
// lowercase alphanumerics and hyphen only.
func sanitizeDNSLabel(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := b.String()
	out = multiHyphen.ReplaceAllString(out, "-")
	out = strings.Trim(out, "-")
	return out
}

func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cleanupTimeout)
}
