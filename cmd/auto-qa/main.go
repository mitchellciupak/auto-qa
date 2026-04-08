package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"auto-qa/internal/applier"
	"auto-qa/internal/config"
	"auto-qa/internal/report"
	"auto-qa/internal/scenario"
	"auto-qa/internal/scheduler"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

var Version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	// Load configuration and setup logging.
	settings := config.MustLoad()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: settings.LogLevel,
	}))
	slog.SetDefault(logger)
	slog.Info("starting auto-qa", "version", Version)

	// Build Kubernetes clients.
	restCfg, err := buildRESTConfig(settings.Kubeconfig)
	if err != nil {
		slog.Error("building kubernetes config", "error", err)
		return 1
	}

	k8sClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		slog.Error("creating kubernetes client", "error", err)
		return 1
	}

	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		slog.Error("creating dynamic client", "error", err)
		return 1
	}

	// Discover scenarios.
	scenarios, err := scenario.Discover(settings.ScenariosRoot)
	if err != nil {
		slog.Error("discovering scenarios", "root", settings.ScenariosRoot, "error", err)
		return 1
	}
	if len(scenarios) == 0 {
		slog.Warn("no scenarios found", "root", settings.ScenariosRoot)
		return 0
	}
	slog.Info("discovered scenarios", "count", len(scenarios), "root", settings.ScenariosRoot)

	// Shared context — no top-level timeout; each scenario derives its own
	// from runner.yaml or the DefaultTimeout fallback.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := &scenario.Runner{
		Applier:        applier.New(dynClient, k8sClient.Discovery()),
		Scheduler:      scheduler.New(k8sClient, dynClient),
		Namespace:      settings.Namespace,
		DefaultTimeout: settings.Timeout,
	}

	results := make([]scenario.Result, len(scenarios))
	var wg sync.WaitGroup

	for i, sc := range scenarios {
		wg.Add(1)
		go func(idx int, s scenario.Scenario) {
			defer wg.Done()
			results[idx] = runner.Run(ctx, s)
		}(i, sc)
	}

	wg.Wait()

	// Report results and determine overall exit code.
	anyFailed := false
	passed := 0
	failed := 0
	var totalDuration time.Duration
	for _, r := range results {
		fields := []any{
			"scenario", r.Name,
			"succeeded", r.Succeeded,
			"duration", r.Duration.Round(time.Millisecond),
		}
		if r.Err != nil {
			fields = append(fields, "error", r.Err)
		}
		if r.Succeeded {
			slog.Info("scenario result", fields...)
			passed++
		} else {
			slog.Error("scenario result", fields...)
			anyFailed = true
			failed++
		}
		totalDuration += r.Duration
		for _, sr := range r.TestSuites {
			testSuiteFields := []any{
				"scenario", r.Name,
				"test_suite", sr.Name,
				"succeeded", sr.Succeeded,
				"skipped", sr.Skipped,
				"duration", sr.Duration.Round(time.Millisecond),
			}
			if sr.Skipped {
				slog.Info("test suite skipped", testSuiteFields...)
			} else if sr.Succeeded {
				slog.Info("test suite result", testSuiteFields...)
			} else {
				slog.Error("test suite result", testSuiteFields...)
			}
		}
	}

	// Print aggregate summary.
	fmt.Fprintf(os.Stderr, "\n%d scenarios: %d passed, %d failed (%s)\n",
		len(results), passed, failed, totalDuration.Round(time.Millisecond))

	// Write JSON report if REPORT_PATH is set.
	if settings.ReportPath != "" {
		if err := report.WriteJSON(results, settings.ReportPath); err != nil {
			slog.Error("writing report", "path", settings.ReportPath, "error", err)
		} else {
			slog.Info("report written", "path", settings.ReportPath)
		}
	}

	if anyFailed {
		return 1
	}

	return 0
}

func defaultKubeconfig() string {
	if home := homedir.HomeDir(); home != "" {
		return filepath.Join(home, ".kube", "config")
	}
	return ""
}

func buildRESTConfig(explicitKubeconfig string) (*rest.Config, error) {
	if explicitKubeconfig != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", explicitKubeconfig)
		if err != nil {
			return nil, fmt.Errorf("building kubeconfig from %q: %w", explicitKubeconfig, err)
		}
		return cfg, nil
	}

	if kubeconfig := defaultKubeconfig(); kubeconfig != "" {
		if _, err := os.Stat(kubeconfig); err == nil {
			cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
			if err != nil {
				return nil, fmt.Errorf("building kubeconfig from %q: %w", kubeconfig, err)
			}
			return cfg, nil
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("checking default kubeconfig %q: %w", kubeconfig, err)
		}
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("building in-cluster config: %w", err)
	}

	return cfg, nil
}
