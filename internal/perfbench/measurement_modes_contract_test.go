package perfbench

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

type fixedHeapSampler struct {
	peak sampledHeapPeak
}

func (sampler fixedHeapSampler) Stop() (sampledHeapPeak, error) {
	return sampler.peak, nil
}

func TestMeasureOperation_latency_has_no_memory_sampler_or_forced_gc(t *testing.T) {
	// Given
	var samplerStarts int
	var snapshots int
	var forcedGC int
	hooks := measurementHooks{
		captureRuntime: func() runtimeSnapshot { snapshots++; return runtimeSnapshot{} },
		forceGC:        func() { forcedGC++ },
		startHeapSampler: func() (heapPeakSampler, error) {
			samplerStarts++
			return fixedHeapSampler{}, nil
		},
	}

	// When
	metrics, err := measureOperation(measurementModeLatency, func() (roundTripEvidence, error) {
		return roundTripEvidence{ttfb: time.Microsecond, total: 2 * time.Microsecond}, nil
	}, hooks)

	// Then
	if err != nil {
		t.Fatalf("measure latency: %v", err)
	}
	if samplerStarts != 0 || snapshots != 0 || forcedGC != 0 {
		t.Fatalf("latency memory activity: sampler=%d snapshots=%d forced_gc=%d", samplerStarts, snapshots, forcedGC)
	}
	if metrics.Mode != measurementModeLatency || metrics.Latency == nil || metrics.AllocationRetained != nil || metrics.SampledPeakDuringOperation != nil {
		t.Fatalf("latency metrics are ambiguous: %+v", metrics)
	}
}

func TestMeasureOperation_allocation_uses_fixed_boundaries_and_after_gc_retained(t *testing.T) {
	// Given
	events := make([]string, 0, 6)
	snapshots := []runtimeSnapshot{
		{HeapAlloc: 100, HeapInuse: 200, TotalAlloc: 1_000, Mallocs: 100, NumGC: 4},
		{HeapAlloc: 400, HeapInuse: 500, TotalAlloc: 1_600, Mallocs: 160, NumGC: 5},
		{HeapAlloc: 120, HeapInuse: 240, TotalAlloc: 1_700, Mallocs: 170, NumGC: 6},
	}
	snapshotIndex := 0
	hooks := measurementHooks{
		captureRuntime: func() runtimeSnapshot {
			events = append(events, "snapshot")
			value := snapshots[snapshotIndex]
			snapshotIndex++
			return value
		},
		forceGC: func() { events = append(events, "gc") },
		startHeapSampler: func() (heapPeakSampler, error) {
			return nil, errors.New("allocation mode started periodic sampler")
		},
	}

	// When
	metrics, err := measureOperation(measurementModeAllocationRetained, func() (roundTripEvidence, error) {
		events = append(events, "operation")
		return roundTripEvidence{}, nil
	}, hooks)

	// Then
	if err != nil {
		t.Fatalf("measure allocation: %v", err)
	}
	wantEvents := []string{"gc", "snapshot", "operation", "snapshot", "gc", "snapshot"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("measurement order = %v, want %v", events, wantEvents)
	}
	allocation := metrics.AllocationRetained
	if allocation == nil || metrics.Latency != nil || metrics.SampledPeakDuringOperation != nil {
		t.Fatalf("allocation metrics are ambiguous: %+v", metrics)
	}
	if allocation.OperationAllocationDelta != (operationAllocationDelta{TotalAlloc: 600, Mallocs: 60, NumGC: 1}) {
		t.Fatalf("operation allocation delta = %+v", allocation.OperationAllocationDelta)
	}
	if allocation.PostOperationBeforeGC != snapshots[1] || allocation.PostOperationAfterGC != snapshots[2] {
		t.Fatalf("retained boundaries = before %+v after %+v", allocation.PostOperationBeforeGC, allocation.PostOperationAfterGC)
	}
}

func TestMeasureOperation_sampled_peak_contains_only_heap_gauges_and_never_forces_gc(t *testing.T) {
	// Given
	var forcedGC int
	peak := sampledHeapPeak{HeapAlloc: 800, HeapInuse: 900, Interval: time.Millisecond, SampleCount: 3}
	hooks := measurementHooks{
		captureRuntime: func() runtimeSnapshot { return runtimeSnapshot{} },
		forceGC:        func() { forcedGC++ },
		startHeapSampler: func() (heapPeakSampler, error) {
			return fixedHeapSampler{peak: peak}, nil
		},
	}

	// When
	metrics, err := measureOperation(measurementModeSampledPeak, func() (roundTripEvidence, error) {
		return roundTripEvidence{}, nil
	}, hooks)

	// Then
	if err != nil {
		t.Fatalf("measure sampled peak: %v", err)
	}
	if forcedGC != 0 {
		t.Fatalf("sampled peak forced GC %d times", forcedGC)
	}
	if metrics.SampledPeakDuringOperation == nil || metrics.SampledPeakDuringOperation.WorkerHeap != peak {
		t.Fatalf("sampled peak metrics = %+v", metrics)
	}
}

func TestWorkerMetrics_rejects_ambiguous_mode_payloads(t *testing.T) {
	valid := workerMetrics{Mode: measurementModeLatency, Latency: &latencyMetrics{}}
	if err := valid.validateModeContract(); err != nil {
		t.Fatalf("valid latency contract: %v", err)
	}
	valid.AllocationRetained = &allocationRetainedMetrics{}
	if err := valid.validateModeContract(); err == nil {
		t.Fatal("mixed mode metrics were accepted")
	}
}
