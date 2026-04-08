package scheduler

import (
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// Keep fetched logs bounded so noisy test pods cannot blow up process memory.
	maxFetchedLogBytes int64 = 1 << 20 // 1 MiB
	// Prefer the most recent lines when logs exceed server-side limits.
	maxFetchedLogTailLines int64 = 5000
	logTruncatedMarker           = "\n...[logs truncated]\n"
)

// FetchJobLogs returns the combined stdout+stderr from the first pod created
// by the named Job. It lists pods with the label selector `job-name=<jobName>`,
// takes the first result, and streams its logs.
//
// If no pods are found, or if log streaming fails, the error is returned and
// the caller should treat it as a warning (logs are best-effort).
func (s *Scheduler) FetchJobLogs(ctx context.Context, namespace, jobName string) (string, error) {
	pods, err := s.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("job-name=%s", jobName),
	})
	if err != nil {
		return "", fmt.Errorf("listing pods for job %q: %w", jobName, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods found for job %q", jobName)
	}

	podName := pods.Items[0].Name
	limitBytes := maxFetchedLogBytes
	tailLines := maxFetchedLogTailLines
	req := s.client.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		LimitBytes: &limitBytes,
		TailLines:  &tailLines,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("streaming logs for pod %q (job %q): %w", podName, jobName, err)
	}
	defer stream.Close()

	logs, err := readLogStreamCapped(stream, maxFetchedLogBytes)
	if err != nil {
		return "", fmt.Errorf("reading logs for pod %q (job %q): %w", podName, jobName, err)
	}

	return logs, nil
}

func readLogStreamCapped(r io.Reader, maxBytes int64) (string, error) {
	if maxBytes <= 0 {
		return "", nil
	}

	limited := &io.LimitedReader{R: r, N: maxBytes + 1}
	raw, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}

	if int64(len(raw)) <= maxBytes {
		return string(raw), nil
	}

	return string(raw[:maxBytes]) + logTruncatedMarker, nil
}
