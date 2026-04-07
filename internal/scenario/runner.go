package scenario

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"auto-qa/internal/applier"
	"auto-qa/internal/scheduler"
)

// Result captures the outcome of a single scenario run.
type Result struct {
	Name      string
	Succeeded bool
	Err       error
	Duration  time.Duration
}

// Runner orchestrates a single scenario end-to-end:
// apply the scenario YAML, create and watch the test Job, then always
// tear down the Job and YAML regardless of outcome.
type Runner struct {
	Applier   *applier.Applier
	Scheduler *scheduler.Scheduler
	// Image is the container image for the test-runner Job.
	Image string
	// Namespace is the Kubernetes namespace the Job runs in.
	Namespace string
}

// Run executes the full lifecycle for sc within the given context:
//  1. Apply senario.yaml (brings up the app stack).
//  2. Create the test Job.
//  3. Watch the Job until it finishes or ctx is cancelled.
//  4. Always delete the Job and tear down senario.yaml afterward.
func (r *Runner) Run(ctx context.Context, sc Scenario) Result {
	start := time.Now()
	log := slog.With("scenario", sc.Name)

	// Apply scenario YAML
	log.Info("applying scenario yaml", "path", sc.YAMLPath)
	if err := r.Applier.ApplyFile(ctx, sc.YAMLPath); err != nil {
		log.Error("applying scenario yaml", "error", err)
		return Result{Name: sc.Name, Err: err}
	}

	// Create test job
	spec := scheduler.JobSpec{
		ScenarioName: sc.Name,
		Image:        r.Image,
		// TODO: replace with the real test-runner command in a future iteration.
		Command:   []string{"sh", "-c", fmt.Sprintf("echo 'running scenario %s' && sleep 2 && echo 'done'", sc.Name)},
		Namespace: r.Namespace,
	}

	log.Info("creating test job", "namespace", spec.Namespace)
	jobName, err := r.Scheduler.CreateJob(ctx, spec)
	if err != nil {
		log.Error("creating test job", "error", err)
		r.teardown(ctx, log, sc.YAMLPath)
		return Result{Name: sc.Name, Err: err}
	}
	log.Info("test job created", "job", jobName)

	// Watch job
	log.Info("watching test job", "job", jobName)
	jobResult, watchErr := r.Scheduler.WatchJob(ctx, spec.Namespace, jobName)

	// Always clean up job and scenario YAML
	// Use a fresh context so cleanup is never blocked by a cancelled parent.
	cleanupCtx := context.Background()
	if delErr := r.Scheduler.DeleteJob(cleanupCtx, spec.Namespace, jobName); delErr != nil {
		log.Warn("failed to delete test job", "job", jobName, "error", delErr)
	} else {
		log.Info("test job deleted", "job", jobName)
	}
	r.teardown(cleanupCtx, log, sc.YAMLPath)

	if watchErr != nil {
		log.Error("watching test job", "error", watchErr)
		return Result{Name: sc.Name, Err: watchErr, Duration: time.Since(start)}
	}

	log.Info("test job finished",
		"job", jobName,
		"succeeded", jobResult.Succeeded,
		"failed_attempts", jobResult.Failed,
		"duration", jobResult.Duration.Round(time.Millisecond),
	)

	return Result{
		Name:      sc.Name,
		Succeeded: jobResult.Succeeded,
		Duration:  time.Since(start),
	}
}

// teardown deletes the resources described by the scenario YAML.
// Errors are logged as warnings but do not affect the Result.
func (r *Runner) teardown(ctx context.Context, log *slog.Logger, yamlPath string) {
	log.Info("tearing down scenario yaml", "path", yamlPath)
	if err := r.Applier.DeleteFile(ctx, yamlPath); err != nil {
		log.Warn("tearing down scenario yaml", "error", err)
	}
}
