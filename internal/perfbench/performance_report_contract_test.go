package perfbench

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPerformanceReport_writeJSONL_preserves_versioned_measurements(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "samples.jsonl")
	samples := reportFixtureSamples()

	// When
	err := writePerformanceReportJSONL(path, samples)

	// Then
	if err != nil {
		t.Fatalf("write JSONL report: %v", err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read JSONL report: %v", err)
	}
	var lines []map[string]json.RawMessage
	for _, line := range splitJSONLLines(t, payload) {
		var value map[string]json.RawMessage
		if err := json.Unmarshal(line, &value); err != nil {
			t.Fatalf("decode JSONL sample: %v", err)
		}
		lines = append(lines, value)
	}
	if len(lines) != 4 {
		t.Fatalf("JSONL sample count = %d, want 4", len(lines))
	}
	assertJSONField(t, lines[0], "schema_version", `"perfbench.v1"`)
	assertJSONField(t, lines[0], "measurement_mode", `"latency"`)
	assertJSONField(t, lines[0], "iteration", "1")
	assertJSONField(t, lines[0], "transport_scope", `"httptest_loopback_fake_upstream"`)
	assertJSONField(t, lines[0], "scenario", `{"id":"responses-responses-stream-1mib-plain","downstream":"responses","upstream":"responses","delivery":"stream","image_bytes":1048576,"profile":"plain"}`)
	assertJSONField(t, lines[0], "environment", `{"go_version":"go1.test","os":"linux","arch":"amd64","gomaxprocs":4}`)
	assertJSONField(t, lines[0], "metrics", `{"mode":"latency","latency":{"ttfb_ns":10,"total_duration_ns":30},"observed_request_bytes":104,"response_bytes":55,"observed_request_body_sha256":"request-a","response_body_sha256":"response-a","connections":{"new":1,"active":2,"idle":3,"closed":4}}`)
	assertJSONField(t, lines[1], "metrics", `{"mode":"allocation_retained","allocation_retained":{"pre_operation":{"heap_alloc":10,"heap_inuse":11,"total_alloc":12,"mallocs":13,"num_gc":1,"goroutines":2},"post_operation_before_gc":{"heap_alloc":20,"heap_inuse":21,"total_alloc":30,"mallocs":31,"num_gc":2,"goroutines":2},"post_operation_after_gc":{"heap_alloc":15,"heap_inuse":16,"total_alloc":30,"mallocs":31,"num_gc":2,"goroutines":2},"operation_allocation_delta":{"total_alloc":18,"mallocs":18,"num_gc":1}},"observed_request_bytes":0,"response_bytes":0,"observed_request_body_sha256":"","response_body_sha256":"","connections":{"new":0,"active":0,"idle":0,"closed":0}}`)
	assertJSONField(t, lines[2], "metrics", `{"mode":"sampled_peak","sampled_peak_during_operation":{"worker_heap":{"heap_alloc":40,"heap_inuse":41,"interval_ns":1000000,"sample_count":5},"parent_process":{"vm_rss":50,"rss_anon":51},"parent_process_supported":true,"parent_sample_interval_ns":1000000,"parent_sample_count":6},"observed_request_bytes":0,"response_bytes":0,"observed_request_body_sha256":"","response_body_sha256":"","connections":{"new":0,"active":0,"idle":0,"closed":0}}`)
	assertJSONField(t, lines[3], "failure", `{"kind":"round_trip","message":"upstream failed"}`)
}

func TestPerformanceReport_writeJSONL_rejects_relative_output_path(t *testing.T) {
	// Given
	path := "perfbench-samples.jsonl"

	// When
	err := writePerformanceReportJSONL(path, reportFixtureSamples()[:1])

	// Then
	if err == nil {
		t.Fatal("relative report output path was accepted")
	}
}

func TestPerformanceReport_comparisonEligibility_rejects_incompatible_identity(t *testing.T) {
	// Given
	baseline := summarizePerformanceReport(reportFixtureSamples())
	cases := []struct {
		name   string
		mutate func(*performanceReportSummary)
	}{
		{"schema", func(summary *performanceReportSummary) { summary.SchemaVersion = "perfbench.v2" }},
		{"cohort", func(summary *performanceReportSummary) { summary.Cohort.ScenarioCatalogDigest = "different" }},
		{"environment", func(summary *performanceReportSummary) { summary.Environment.GOARCH = "arm64" }},
		{"transport", func(summary *performanceReportSummary) { summary.TransportScope = "production_network" }},
	}

	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			candidate := baseline
			item.mutate(&candidate)

			// When
			err := validatePerformanceReportComparison(baseline, candidate)

			// Then
			if err == nil {
				t.Fatalf("incompatible %s report was eligible", item.name)
			}
		})
	}
}

func TestPerformanceReport_summarize_uses_deterministic_percentiles_without_unsupported_RSS(t *testing.T) {
	// Given
	samples := reportFixtureSamples()[:1]
	samples[0].Metrics.Latency = &latencyMetrics{TTFB: 30 * time.Nanosecond, TotalDuration: 30 * time.Nanosecond}
	for _, latency := range []time.Duration{10 * time.Nanosecond, 20 * time.Nanosecond} {
		sample := samples[0]
		sample.Iteration++
		sample.Metrics.Latency = &latencyMetrics{TTFB: latency, TotalDuration: latency}
		samples = append(samples, sample)
	}
	unsupported := reportFixtureSamples()[2]
	unsupported.Metrics.SampledPeakDuringOperation.ParentProcessSupported = false
	samples = append(samples, unsupported)

	// When
	summary := summarizePerformanceReport(samples)
	reversed := summarizePerformanceReport([]performanceReportSample{samples[3], samples[2], samples[1], samples[0]})
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	reversedEncoded, err := json.Marshal(reversed)
	if err != nil {
		t.Fatalf("marshal reversed summary: %v", err)
	}

	// Then
	if summary.SampleCount != 4 || summary.FailureCount != 0 {
		t.Fatalf("summary counts = %d/%d", summary.SampleCount, summary.FailureCount)
	}
	if got := summary.Metrics.TTFB; got == nil || *got != (performanceReportDistribution{Count: 3, Min: 10, P50: 20, P95: 30, Max: 30}) {
		t.Fatalf("TTFB summary = %+v", got)
	}
	if got := summary.Metrics.TotalDuration; got == nil || *got != (performanceReportDistribution{Count: 3, Min: 10, P50: 20, P95: 30, Max: 30}) {
		t.Fatalf("total duration summary = %+v", got)
	}
	if summary.Metrics.ParentVmRSS != nil || summary.Metrics.ParentRssAnon != nil {
		t.Fatalf("unsupported parent RSS was summarized: %+v", summary.Metrics)
	}
	if string(encoded) != string(reversedEncoded) {
		t.Fatalf("summary depends on sample order:\n%s\n%s", encoded, reversedEncoded)
	}
}

func TestPerformanceReport_summarize_counts_failures_without_metric_zeroes(t *testing.T) {
	// Given
	samples := reportFixtureSamples()

	// When
	summary := summarizePerformanceReport(samples)

	// Then
	if summary.SampleCount != 4 || summary.FailureCount != 1 {
		t.Fatalf("summary counts = %d/%d, want 4/1", summary.SampleCount, summary.FailureCount)
	}
	if summary.Metrics.ObservedRequestBytes == nil || summary.Metrics.ObservedRequestBytes.Count != 1 {
		t.Fatalf("request bytes summary = %+v", summary.Metrics.ObservedRequestBytes)
	}
	if summary.Metrics.ParentVmRSS == nil || summary.Metrics.ParentVmRSS.Min != 50 {
		t.Fatalf("parent VmRSS summary = %+v", summary.Metrics.ParentVmRSS)
	}
}

func splitJSONLLines(t *testing.T, payload []byte) [][]byte {
	t.Helper()
	lines := make([][]byte, 0, 4)
	for _, line := range bytes.Split(payload, []byte{'\n'}) {
		if len(line) != 0 {
			lines = append(lines, line)
		}
	}
	return lines
}

func assertJSONField(t *testing.T, value map[string]json.RawMessage, field string, want string) {
	t.Helper()
	if got := string(value[field]); got != want {
		t.Fatalf("%s = %s, want %s", field, got, want)
	}
}

func reportFixtureSamples() []performanceReportSample {
	identity := performanceReportIdentity{
		SchemaVersion:  "perfbench.v1",
		Cohort:         performanceReportCohort{ScenarioCatalogVersion: "v1", ScenarioCatalogDigest: "catalog-a"},
		TransportScope: "httptest_loopback_fake_upstream",
		Environment: performanceReportEnvironment{
			GoVersion: "go1.test", GOOS: "linux", GOARCH: "amd64", GOMAXPROCS: 4,
		},
	}
	item := scenario{ID: "responses-responses-stream-1mib-plain", Downstream: downstreamResponses, Upstream: upstreamResponses, Delivery: deliveryStream, ImageBytes: 1 << 20, Profile: profilePlain}
	return []performanceReportSample{
		{performanceReportIdentity: identity, Scenario: item, MeasurementMode: measurementModeLatency, Iteration: 1, Metrics: workerMetrics{Mode: measurementModeLatency, Latency: &latencyMetrics{TTFB: 10 * time.Nanosecond, TotalDuration: 30 * time.Nanosecond}, ObservedRequestBytes: 104, ResponseBytes: 55, ObservedRequestBodySHA256: "request-a", ResponseBodySHA256: "response-a", Connections: connectionMetrics{New: 1, Active: 2, Idle: 3, Closed: 4}}},
		{performanceReportIdentity: identity, Scenario: item, MeasurementMode: measurementModeAllocationRetained, Iteration: 2, Metrics: workerMetrics{Mode: measurementModeAllocationRetained, AllocationRetained: &allocationRetainedMetrics{PreOperation: runtimeSnapshot{HeapAlloc: 10, HeapInuse: 11, TotalAlloc: 12, Mallocs: 13, NumGC: 1, Goroutines: 2}, PostOperationBeforeGC: runtimeSnapshot{HeapAlloc: 20, HeapInuse: 21, TotalAlloc: 30, Mallocs: 31, NumGC: 2, Goroutines: 2}, PostOperationAfterGC: runtimeSnapshot{HeapAlloc: 15, HeapInuse: 16, TotalAlloc: 30, Mallocs: 31, NumGC: 2, Goroutines: 2}, OperationAllocationDelta: operationAllocationDelta{TotalAlloc: 18, Mallocs: 18, NumGC: 1}}}},
		{performanceReportIdentity: identity, Scenario: item, MeasurementMode: measurementModeSampledPeak, Iteration: 3, Metrics: workerMetrics{Mode: measurementModeSampledPeak, SampledPeakDuringOperation: &sampledPeakMetrics{WorkerHeap: sampledHeapPeak{HeapAlloc: 40, HeapInuse: 41, Interval: time.Millisecond, SampleCount: 5}, ParentProcess: processMemory{VmRSS: 50, RssAnon: 51}, ParentProcessSupported: true, ParentSampleInterval: time.Millisecond, ParentSampleCount: 6}}},
		{performanceReportIdentity: identity, Scenario: item, MeasurementMode: measurementModeLatency, Iteration: 4, Failure: &performanceReportFailure{Kind: "round_trip", Message: "upstream failed"}},
	}
}
