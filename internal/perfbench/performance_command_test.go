package perfbench

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPerformanceBaseline_reportDir_skips_without_environment_value(t *testing.T) {
	// Given
	lookup := func(string) string { return "" }

	// When
	directory, skipped, err := performanceBaselineReportDir(lookup)

	// Then
	if err != nil || !skipped || directory != "" {
		t.Fatalf("report directory = %q/%t/%v", directory, skipped, err)
	}
}

func TestPerformanceBaseline_reportDir_rejects_relative_directory(t *testing.T) {
	// Given
	lookup := func(string) string { return "reports" }

	// When
	_, skipped, err := performanceBaselineReportDir(lookup)

	// Then
	if err == nil || skipped {
		t.Fatalf("relative directory = skipped=%t err=%v", skipped, err)
	}
}

func TestPerformanceBaselineCommand(t *testing.T) {
	// Given
	directory, skipped, err := performanceBaselineReportDir(os.Getenv)
	if err != nil {
		t.Fatalf("read PERFBENCH_REPORT_DIR: %v", err)
	}
	if skipped {
		t.Skip("set PERFBENCH_REPORT_DIR to an absolute existing directory to write a local httptest baseline")
	}

	// When
	result, err := runPerformanceBaseline(directory, runPerfWorker)

	// Then
	if err != nil {
		t.Fatalf("run performance baseline: %v", err)
	}
	t.Log(result.HumanSummary)
}

func TestPerformanceBaseline_run_writes_fixed_cohort_artifacts_and_records_failures(t *testing.T) {
	// Given
	directory := t.TempDir()
	calls := 0
	runner := func(_ context.Context, request workerRequest, _ chan<- int) (workerRun, error) {
		calls++
		if calls == 2 {
			return workerRun{}, errors.New("synthetic worker failure")
		}
		return workerRun{Result: workerResult{Metrics: performanceBaselineMetrics(request.Mode)}}, nil
	}

	// When
	result, err := runPerformanceBaseline(directory, runner)

	// Then
	if err != nil {
		t.Fatalf("run performance baseline: %v", err)
	}
	if calls != 18 || !result.ComparisonEligible {
		t.Fatalf("calls/eligibility = %d/%t, want 18/true", calls, result.ComparisonEligible)
	}
	if !filepath.IsAbs(result.RawPath) || !filepath.IsAbs(result.SummaryPath) || !filepath.IsAbs(result.TextPath) {
		t.Fatalf("report paths must be absolute: %+v", result)
	}
	for _, path := range []string{result.RawPath, result.SummaryPath, result.TextPath} {
		if filepath.Dir(path) != directory {
			t.Fatalf("report path %q escaped %q", path, directory)
		}
	}
	payload, err := os.ReadFile(result.RawPath)
	if err != nil {
		t.Fatalf("read JSONL: %v", err)
	}
	iterations := map[string]map[int]bool{}
	failures := 0
	for _, line := range splitJSONLLines(t, payload) {
		var sample performanceReportRawSample
		if err := json.Unmarshal(line, &sample); err != nil {
			t.Fatalf("decode JSONL sample: %v", err)
		}
		key := sample.Scenario.ID + "/" + string(sample.MeasurementMode)
		if iterations[key] == nil {
			iterations[key] = map[int]bool{}
		}
		iterations[key][sample.Iteration] = true
		if sample.Failure != nil {
			failures++
			if sample.Metrics.Mode != sample.MeasurementMode {
				t.Fatalf("failure sample mode = %q, want %q", sample.Metrics.Mode, sample.MeasurementMode)
			}
		}
	}
	if len(iterations) != 6 || failures != 1 {
		t.Fatalf("cohort mode count/failures = %d/%d, want 6/1", len(iterations), failures)
	}
	for key, values := range iterations {
		if len(values) != 3 || !values[1] || !values[2] || !values[3] {
			t.Fatalf("iterations for %s = %v", key, values)
		}
	}
	summaryPayload, err := os.ReadFile(result.SummaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	var summary performanceReportSummary
	if err := json.Unmarshal(summaryPayload, &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary.SampleCount != 18 || summary.FailureCount != 1 || summary.TransportScope != "httptest_loopback_fake_upstream" {
		t.Fatalf("summary = %+v", summary)
	}
	text, err := os.ReadFile(result.TextPath)
	if err != nil {
		t.Fatalf("read text summary: %v", err)
	}
	for _, field := range []string{"ttfb", "total", "allocation", "retained_heap", "worker_heap", "parent_vm_rss", "parent_rss_anon", "connections_new", "connections_closed"} {
		if !strings.Contains(string(text), field) {
			t.Fatalf("human summary omits %q: %s", field, text)
		}
	}
}
