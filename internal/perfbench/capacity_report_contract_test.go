package perfbench

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCapacityReportRun_labelsChildRSSAsAtFullGateSnapshot(t *testing.T) {
	// Given
	directory := t.TempDir()
	runner := func(_ context.Context, workload capacityWorkload, repetition int) capacityMeasurement {
		return capacityMeasurement{
			RequestedRequests:  workload.Concurrency,
			PeakInFlight:       workload.Concurrency,
			TTFB:               []time.Duration{time.Duration(repetition) * time.Millisecond},
			TotalLatency:       []time.Duration{time.Duration(repetition+1) * time.Millisecond},
			SuccessfulRequests: workload.Concurrency,
			Elapsed:            time.Second,
			Child: capacityChildResources{
				PID:               1,
				AttributionStatus: "child_pid",
				Heap:              capacityHeapSnapshot{Status: "available", Attribution: "child_worker_ipc_frame", HeapAlloc: 101, HeapInuse: 102},
				AtFullGateRSS:     capacityProcessMemory{Status: "available", Attribution: "child_pid_procfs_at_full_gate_snapshot", VmRSS: 103, RssAnon: 104},
				CPUDelta:          capacityCPUTime{Status: "available", Attribution: "child_pid_delta", UserNS: 105, SystemNS: 106, TotalNS: 211},
			},
		}
	}

	// When
	result, err := runCapacityReport(directory, runner)

	// Then
	if err != nil {
		t.Fatalf("run capacity report: %v", err)
	}
	raw, err := os.ReadFile(result.RawPath)
	if err != nil {
		t.Fatalf("read raw report: %v", err)
	}
	if bytes.Contains(raw, []byte("peak_rss")) || !bytes.Contains(raw, []byte(`"at_full_gate_rss"`)) || !bytes.Contains(raw, []byte("child_pid_procfs_at_full_gate_snapshot")) {
		t.Fatalf("raw report mislabels RSS snapshot: %s", raw)
	}
	summary, err := os.ReadFile(result.SummaryPath)
	if err != nil {
		t.Fatalf("read summary report: %v", err)
	}
	if bytes.Contains(summary, []byte("child_peak_")) || !bytes.Contains(summary, []byte(`"child_at_full_gate_vm_rss"`)) {
		t.Fatalf("summary report mislabels RSS snapshot: %s", summary)
	}
	text, err := os.ReadFile(result.TextPath)
	if err != nil {
		t.Fatalf("read text report: %v", err)
	}
	if !strings.Contains(string(text), "child_at_full_gate_rss_p50") {
		t.Fatalf("text report mislabels RSS snapshot: %s", text)
	}
}

func TestCapacityReportRun_requestErrorsInvalidateRepetitionAndSummary(t *testing.T) {
	// Given
	directory := t.TempDir()
	calls := 0
	failedWorkload := ""
	runner := func(_ context.Context, workload capacityWorkload, repetition int) capacityMeasurement {
		calls++
		measurement := capacityMeasurement{
			RequestedRequests:  workload.Concurrency,
			PeakInFlight:       workload.Concurrency,
			TTFB:               []time.Duration{time.Duration(repetition) * time.Millisecond},
			TotalLatency:       []time.Duration{time.Duration(repetition+1) * time.Millisecond},
			SuccessfulRequests: workload.Concurrency,
			Elapsed:            time.Second,
			Child: capacityChildResources{
				PID:               1,
				AttributionStatus: "child_pid",
				Heap:              capacityHeapSnapshot{Status: "available", Attribution: "child_worker_ipc_frame", HeapAlloc: 101, HeapInuse: 102},
				AtFullGateRSS:     capacityProcessMemory{Status: "available", Attribution: "child_pid_procfs_at_full_gate_snapshot", VmRSS: 103, RssAnon: 104},
				CPUDelta:          capacityCPUTime{Status: "available", Attribution: "child_pid_delta", UserNS: 105, SystemNS: 106, TotalNS: 211},
			},
		}
		if calls == 1 {
			failedWorkload = workload.Name()
			measurement.ErrorCount = 1
			measurement.SuccessfulRequests--
		}
		return measurement
	}

	// When
	result, err := runCapacityReport(directory, runner)

	// Then
	if err != nil {
		t.Fatalf("run capacity report: %v", err)
	}
	var failedTier *capacityTierSummary
	for index := range result.Summary.Tiers {
		tier := &result.Summary.Tiers[index]
		if tier.Workload.Name() == failedWorkload {
			failedTier = tier
			break
		}
	}
	if failedTier == nil {
		t.Fatalf("missing failed workload tier %q", failedWorkload)
	}
	if failedTier.ValidSampleCount != capacityRepetitions-1 || failedTier.FailureCount != 1 {
		t.Fatalf("failed tier validity = valid %d failures %d, want valid %d failures 1", failedTier.ValidSampleCount, failedTier.FailureCount, capacityRepetitions-1)
	}
	raw, err := os.ReadFile(result.RawPath)
	if err != nil {
		t.Fatalf("read raw report: %v", err)
	}
	var firstSample capacityReportSample
	if err := json.Unmarshal(splitJSONLLines(t, raw)[0], &firstSample); err != nil {
		t.Fatalf("decode first raw sample: %v", err)
	}
	if firstSample.Failure == nil || firstSample.Failure.Kind != "request" || firstSample.Measurement.GateValid {
		t.Fatalf("request error sample was not invalidated: %+v", firstSample)
	}
	summary, err := os.ReadFile(result.SummaryPath)
	if err != nil {
		t.Fatalf("read summary report: %v", err)
	}
	if !bytes.Contains(summary, []byte(`"all_repetition_request_error_rate"`)) {
		t.Fatalf("summary report leaves request error rate scope ambiguous: %s", summary)
	}
	text, err := os.ReadFile(result.TextPath)
	if err != nil {
		t.Fatalf("read text report: %v", err)
	}
	if !strings.Contains(string(text), "all_repetition_request_error_rate_p50") {
		t.Fatalf("text report leaves request error rate scope ambiguous: %s", text)
	}
}
