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

func TestCapacityReportDir_skips_without_environment_value(t *testing.T) {
	// Given
	lookup := func(string) string { return "" }

	// When
	directory, skipped, err := capacityReportDir(lookup)

	// Then
	if err != nil || !skipped || directory != "" {
		t.Fatalf("report directory = %q/%t/%v", directory, skipped, err)
	}
}

func TestCapacityReportDir_rejects_non_absolute_or_missing_directory(t *testing.T) {
	for name, value := range map[string]string{
		"relative": "reports",
		"missing":  "/tmp/perfbench-capacity-report-directory-that-does-not-exist",
	} {
		t.Run(name, func(t *testing.T) {
			// When
			_, skipped, err := capacityReportDir(func(string) string { return value })

			// Then
			if err == nil || skipped {
				t.Fatalf("capacity report directory %q = skipped=%t err=%v", value, skipped, err)
			}
		})
	}
}

func TestCapacityCommand(t *testing.T) {
	// Given
	directory, skipped, err := capacityReportDir(os.Getenv)
	if err != nil {
		t.Fatalf("read %s: %v", capacityReportDirectory, err)
	}
	if skipped {
		t.Skip("set PERFBENCH_CAPACITY_REPORT_DIR to an absolute existing directory to write a local capacity report")
	}

	// When
	result, err := runCapacityReport(directory, runCapacityWorkload)

	// Then
	if err != nil {
		t.Fatalf("run capacity report: %v", err)
	}
	t.Log(result.HumanSummary)
}

func TestCapacityReport_run_writes_fixed_cohort_with_child_attribution_and_failure_samples(t *testing.T) {
	// Given
	directory := t.TempDir()
	calls := 0
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
				PID:               10_000 + calls,
				AttributionStatus: "child_pid",
				Heap:              capacityHeapSnapshot{Status: "available", Attribution: "child_worker_ipc_frame", HeapAlloc: 101, HeapInuse: 102},
				AtFullGateRSS:     capacityProcessMemory{Status: "available", Attribution: "child_pid_procfs_at_full_gate_snapshot", VmRSS: 103, RssAnon: 104},
				CPUDelta:          capacityCPUTime{Status: "available", Attribution: "child_pid_delta", UserNS: 105, SystemNS: 106, TotalNS: 211},
			},
		}
		if calls == 2 {
			measurement.PeakInFlight = workload.Concurrency - 1
			measurement.ErrorCount = 1
			measurement.Failure = &capacityReportFailure{Kind: "gate", Message: "peak did not match requested concurrency"}
		}
		return measurement
	}

	// When
	result, err := runCapacityReport(directory, runner)

	// Then
	if err != nil {
		t.Fatalf("run capacity report: %v", err)
	}
	if calls != 70 {
		t.Fatalf("runner calls = %d, want 70", calls)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read report directory: %v", err)
	}
	if len(entries) != 3 || entries[0].Name() != "capacity.v1.jsonl" || entries[1].Name() != "capacity.v1.summary.json" || entries[2].Name() != "capacity.v1.summary.txt" {
		t.Fatalf("report artifacts = %+v", entries)
	}
	if result.Summary.SchemaVersion != capacityReportSchemaVersion || result.Summary.Cohort.WorkloadCount != 14 || result.Summary.Cohort.RepetitionsPerWorkload != capacityRepetitions || len(result.Summary.Tiers) != 14 {
		t.Fatalf("summary identity/tier count = %+v", result.Summary)
	}
	payload, err := os.ReadFile(result.RawPath)
	if err != nil {
		t.Fatalf("read raw report: %v", err)
	}
	if bytes.Contains(payload, []byte("parent_")) {
		t.Fatalf("raw capacity report contains parent metrics: %s", payload)
	}
	summaryPayload, err := os.ReadFile(result.SummaryPath)
	if err != nil {
		t.Fatalf("read summary report: %v", err)
	}
	if bytes.Contains(summaryPayload, []byte("parent_")) {
		t.Fatalf("summary capacity report contains parent metrics: %s", summaryPayload)
	}
	lines := splitJSONLLines(t, payload)
	if len(lines) != 70 {
		t.Fatalf("raw sample count = %d, want 70", len(lines))
	}
	failures := 0
	for _, line := range lines {
		var sample capacityReportSample
		if err := json.Unmarshal(line, &sample); err != nil {
			t.Fatalf("decode raw capacity sample: %v", err)
		}
		if sample.SchemaVersion != capacityReportSchemaVersion || sample.TransportScope != capacityTransportScope {
			t.Fatalf("sample identity = %+v", sample.capacityReportIdentity)
		}
		if sample.Workload.Scenario.ID == "" || sample.Workload.Traffic == "" || sample.Workload.Concurrency < 1 || sample.Repetition < 1 || sample.Repetition > capacityRepetitions {
			t.Fatalf("sample workload identity = %+v", sample)
		}
		if sample.Measurement.Child.PID <= 0 || sample.Measurement.Child.AttributionStatus != "child_pid" {
			t.Fatalf("child attribution = %+v", sample.Measurement.Child)
		}
		if sample.Failure != nil {
			failures++
			if sample.Measurement.GateValid {
				t.Fatalf("failure sample was marked gate-valid: %+v", sample)
			}
		}
	}
	if failures != 1 {
		t.Fatalf("failure samples = %d, want 1", failures)
	}
	for _, tier := range result.Summary.Tiers {
		if tier.RepetitionCount != capacityRepetitions || tier.RequestTTFB == nil || tier.RequestTTFB.Min <= 0 || tier.RequestTTFB.P50 <= 0 || tier.RequestTTFB.P95 <= 0 || tier.RequestTTFB.Max < tier.RequestTTFB.P95 || tier.AllRepetitionRequestErrorRate == nil || tier.SuccessfulThroughput == nil {
			t.Fatalf("tier summary = %+v", tier)
		}
	}
	text, err := os.ReadFile(result.TextPath)
	if err != nil {
		t.Fatalf("read text report: %v", err)
	}
	if !strings.Contains(string(text), "httptest_loopback_fake_upstream") || !strings.Contains(string(text), "not a production capacity guarantee") {
		t.Fatalf("text report omitted local scope warning: %s", text)
	}
}
