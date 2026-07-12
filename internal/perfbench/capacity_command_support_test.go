package perfbench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const capacityReportDirectory = "PERFBENCH_CAPACITY_REPORT_DIR"

const capacityWorkloadTimeout = 2 * time.Minute

type capacityWorkloadRunner func(context.Context, capacityWorkload, int) capacityMeasurement

func capacityReportDir(lookup func(string) string) (string, bool, error) {
	directory := lookup(capacityReportDirectory)
	if directory == "" {
		return "", true, nil
	}
	if !filepath.IsAbs(directory) {
		return "", false, fmt.Errorf("%s must be an absolute existing directory: %q", capacityReportDirectory, directory)
	}
	info, err := os.Stat(directory)
	if err != nil {
		return "", false, fmt.Errorf("stat %s: %w", capacityReportDirectory, err)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("%s is not a directory: %q", capacityReportDirectory, directory)
	}
	return directory, false, nil
}

func runCapacityReport(directory string, runner capacityWorkloadRunner) (capacityReportResult, error) {
	if _, skipped, err := capacityReportDir(func(string) string { return directory }); err != nil || skipped {
		return capacityReportResult{}, err
	}
	workloads, err := capacityWorkloads()
	if err != nil {
		return capacityReportResult{}, err
	}
	identity := currentCapacityReportIdentity()
	samples := make([]capacityReportSample, 0, len(workloads)*capacityRepetitions)
	for _, workload := range workloads {
		for repetition := 1; repetition <= workload.Repetitions; repetition++ {
			ctx, cancel := context.WithTimeout(context.Background(), capacityWorkloadTimeout)
			measurement := runner(ctx, workload, repetition)
			cancel()
			measurement = normalizeCapacityReportMeasurement(workload, measurement)
			samples = append(samples, capacityReportSample{
				capacityReportIdentity: identity, Workload: workload, Repetition: repetition, Measurement: measurement, Failure: measurement.Failure,
			})
		}
	}
	summary := summarizeCapacityReport(samples)
	result := capacityReportResult{
		RawPath:     filepath.Join(directory, "capacity.v1.jsonl"),
		SummaryPath: filepath.Join(directory, "capacity.v1.summary.json"),
		TextPath:    filepath.Join(directory, "capacity.v1.summary.txt"),
		Summary:     summary,
	}
	result.HumanSummary = formatCapacityReportSummary(summary)
	if err := writeCapacityReportSet(result, samples); err != nil {
		return capacityReportResult{}, fmt.Errorf("write capacity report set: %w", err)
	}
	return result, nil
}

func normalizeCapacityReportMeasurement(workload capacityWorkload, measurement capacityMeasurement) capacityMeasurement {
	if measurement.RequestedRequests == 0 {
		measurement.RequestedRequests = workload.Concurrency
	}
	if measurement.ErrorCount > 0 && measurement.Failure == nil {
		measurement.Failure = &capacityReportFailure{Kind: "request", Message: fmt.Sprintf("request errors = %d", measurement.ErrorCount)}
	}
	if measurement.RequestedRequests != workload.Concurrency && measurement.Failure == nil {
		measurement.Failure = &capacityReportFailure{Kind: "request_count", Message: fmt.Sprintf("requested requests = %d, want %d", measurement.RequestedRequests, workload.Concurrency)}
	}
	if measurement.PeakInFlight != workload.Concurrency && measurement.Failure == nil {
		measurement.Failure = &capacityReportFailure{Kind: "gate", Message: fmt.Sprintf("peak in-flight = %d, want %d", measurement.PeakInFlight, workload.Concurrency)}
	}
	measurement.Child = normalizeCapacityChildResources(measurement.Child)
	return normalizeCapacityMeasurement(measurement)
}

func normalizeCapacityChildResources(resources capacityChildResources) capacityChildResources {
	if resources.PID <= 0 {
		resources.AttributionStatus = "unavailable"
	} else if resources.AttributionStatus == "" {
		resources.AttributionStatus = "child_pid"
	}
	resources.Heap = normalizeCapacityHeap(resources.Heap)
	resources.AtFullGateRSS = normalizeCapacityMemory(resources.AtFullGateRSS)
	resources.CPUDelta = normalizeCapacityCPU(resources.CPUDelta)
	return resources
}

func normalizeCapacityHeap(snapshot capacityHeapSnapshot) capacityHeapSnapshot {
	if snapshot.Status == "" {
		return capacityHeapSnapshot{Status: "unavailable", Reason: "not_collected", Attribution: "child_worker_ipc_frame"}
	}
	return snapshot
}

func normalizeCapacityMemory(memory capacityProcessMemory) capacityProcessMemory {
	if memory.Status == "" {
		return capacityProcessMemory{Status: "unavailable", Reason: "not_collected", Attribution: "child_pid_procfs_at_full_gate_snapshot"}
	}
	return memory
}

func normalizeCapacityCPU(cpu capacityCPUTime) capacityCPUTime {
	if cpu.Status == "" {
		return capacityCPUTime{Status: "unavailable", Reason: "not_collected", Attribution: "child_pid_delta"}
	}
	return cpu
}
