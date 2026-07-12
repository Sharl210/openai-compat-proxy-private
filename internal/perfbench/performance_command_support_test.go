package perfbench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	performanceBaselineReportDirectory = "PERFBENCH_REPORT_DIR"
	performanceBaselineIterations      = 3
)

type performanceBaselineWorker func(context.Context, workerRequest, chan<- int) (workerRun, error)

type performanceBaselineResult struct {
	RawPath            string
	SummaryPath        string
	TextPath           string
	Summary            performanceReportSummary
	HumanSummary       string
	ComparisonEligible bool
}

func performanceBaselineReportDir(lookup func(string) string) (string, bool, error) {
	directory := lookup(performanceBaselineReportDirectory)
	if directory == "" {
		return "", true, nil
	}
	if !filepath.IsAbs(directory) {
		return "", false, fmt.Errorf("%s must be an absolute directory: %q", performanceBaselineReportDirectory, directory)
	}
	info, err := os.Stat(directory)
	if err != nil {
		return "", false, fmt.Errorf("stat %s: %w", performanceBaselineReportDirectory, err)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("%s is not a directory: %q", performanceBaselineReportDirectory, directory)
	}
	return directory, false, nil
}

func performanceBaselineCohort() ([]scenario, error) {
	wanted := map[string]struct{}{
		"responses-responses-stream-8mib-plain": {},
		"chat-chat-proxy_buffer-1mib-plain":     {},
	}
	cohort := make([]scenario, 0, len(wanted))
	for _, item := range scenarioCatalog() {
		if _, ok := wanted[item.ID]; ok {
			cohort = append(cohort, item)
		}
	}
	if len(cohort) != len(wanted) {
		return nil, fmt.Errorf("fixed performance cohort has %d scenarios, want %d", len(cohort), len(wanted))
	}
	return cohort, nil
}

func runPerformanceBaseline(directory string, worker performanceBaselineWorker) (performanceBaselineResult, error) {
	if _, skipped, err := performanceBaselineReportDir(func(string) string { return directory }); err != nil || skipped {
		return performanceBaselineResult{}, err
	}
	cohort, err := performanceBaselineCohort()
	if err != nil {
		return performanceBaselineResult{}, err
	}
	identity := currentPerformanceReportIdentity()
	samples := make([]performanceReportSample, 0, len(cohort)*3*performanceBaselineIterations)
	for _, item := range cohort {
		for _, mode := range []measurementMode{measurementModeLatency, measurementModeAllocationRetained, measurementModeSampledPeak} {
			for iteration := 1; iteration <= performanceBaselineIterations; iteration++ {
				request := workerRequest{Action: workerActionRoundTrip, Mode: mode, Scenario: item, Timeout: 2 * time.Minute}
				ctx, cancel := context.WithTimeout(context.Background(), request.Timeout)
				run, workerErr := worker(ctx, request, nil)
				cancel()
				sample := performanceReportSample{performanceReportIdentity: identity, Scenario: item, MeasurementMode: mode, Iteration: iteration, Metrics: workerMetrics{Mode: mode}}
				if workerErr != nil {
					sample.Failure = &performanceReportFailure{Kind: "worker", Message: workerErr.Error()}
				} else {
					sample.Metrics = run.Result.Metrics
				}
				samples = append(samples, sample)
			}
		}
	}
	summary := summarizePerformanceReport(samples)
	expected := performanceReportSummary{performanceReportIdentity: identity}
	if err := validatePerformanceReportComparison(expected, summary); err != nil {
		return performanceBaselineResult{}, fmt.Errorf("validate performance baseline comparison: %w", err)
	}
	result := performanceBaselineResult{
		RawPath:     filepath.Join(directory, "performance-baseline.jsonl"),
		SummaryPath: filepath.Join(directory, "performance-baseline.summary.json"),
		TextPath:    filepath.Join(directory, "performance-baseline.summary.txt"),
		Summary:     summary, ComparisonEligible: true,
	}
	result.HumanSummary = formatPerformanceBaselineSummary(summary)
	if err := writePerformanceReportJSONL(result.RawPath, samples); err != nil {
		return performanceBaselineResult{}, err
	}
	if err := writePerformanceBaselineFile(result.SummaryPath, summary); err != nil {
		return performanceBaselineResult{}, err
	}
	if err := os.WriteFile(result.TextPath, []byte(result.HumanSummary), 0o600); err != nil {
		return performanceBaselineResult{}, fmt.Errorf("write performance baseline text summary: %w", err)
	}
	return result, nil
}

func writePerformanceBaselineFile(path string, summary performanceReportSummary) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("performance baseline path must be absolute: %q", path)
	}
	payload, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal performance baseline summary: %w", err)
	}
	if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
		return fmt.Errorf("write performance baseline summary: %w", err)
	}
	return nil
}

func formatPerformanceBaselineSummary(summary performanceReportSummary) string {
	metrics := summary.Metrics
	return strings.Join([]string{
		"scope=local httptest fake upstream",
		fmt.Sprintf("samples=%d failures=%d", summary.SampleCount, summary.FailureCount),
		formatPerformanceBaselineDistribution("ttfb", metrics.TTFB),
		formatPerformanceBaselineDistribution("total", metrics.TotalDuration),
		formatPerformanceBaselineDistribution("allocation", metrics.AllocationBytes),
		formatPerformanceBaselineDistribution("retained_heap", metrics.RetainedHeapAlloc),
		formatPerformanceBaselineDistribution("worker_heap", metrics.SampledWorkerHeap),
		formatPerformanceBaselineDistribution("parent_vm_rss", metrics.ParentVmRSS),
		formatPerformanceBaselineDistribution("parent_rss_anon", metrics.ParentRssAnon),
		formatPerformanceBaselineDistribution("connections_new", metrics.ConnectionNew),
		formatPerformanceBaselineDistribution("connections_closed", metrics.ConnectionClosed),
	}, "\n") + "\n"
}

func formatPerformanceBaselineDistribution(name string, value *performanceReportDistribution) string {
	if value == nil {
		return name + "=unsupported"
	}
	return fmt.Sprintf("%s count=%d p50=%d p95=%d", name, value.Count, value.P50, value.P95)
}

func performanceBaselineMetrics(mode measurementMode) workerMetrics {
	metrics := workerMetrics{Mode: mode, ObservedRequestBytes: 100, ResponseBytes: 50, Connections: connectionMetrics{New: 1, Active: 1, Idle: 1, Closed: 1}}
	switch mode {
	case measurementModeLatency:
		metrics.Latency = &latencyMetrics{TTFB: time.Millisecond, TotalDuration: 2 * time.Millisecond}
	case measurementModeAllocationRetained:
		metrics.AllocationRetained = &allocationRetainedMetrics{PostOperationAfterGC: runtimeSnapshot{HeapAlloc: 20, HeapInuse: 21}, OperationAllocationDelta: operationAllocationDelta{TotalAlloc: 30}}
	case measurementModeSampledPeak:
		metrics.SampledPeakDuringOperation = &sampledPeakMetrics{WorkerHeap: sampledHeapPeak{HeapAlloc: 40, HeapInuse: 41}, ParentProcess: processMemory{VmRSS: 50, RssAnon: 51}, ParentProcessSupported: true}
	}
	return metrics
}
