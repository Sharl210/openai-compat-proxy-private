package perfbench

import (
	"fmt"
	"runtime"
	"time"
)

type measurementMode string

const (
	measurementModeLatency            measurementMode = "latency"
	measurementModeAllocationRetained measurementMode = "allocation_retained"
	measurementModeSampledPeak        measurementMode = "sampled_peak"
	measurementSampleInterval                         = time.Millisecond
)

type runtimeSnapshot struct {
	HeapAlloc  uint64 `json:"heap_alloc"`
	HeapInuse  uint64 `json:"heap_inuse"`
	TotalAlloc uint64 `json:"total_alloc"`
	Mallocs    uint64 `json:"mallocs"`
	NumGC      uint32 `json:"num_gc"`
	Goroutines int    `json:"goroutines"`
}

type operationAllocationDelta struct {
	TotalAlloc uint64 `json:"total_alloc"`
	Mallocs    uint64 `json:"mallocs"`
	NumGC      uint32 `json:"num_gc"`
}

type latencyMetrics struct {
	TTFB          time.Duration `json:"ttfb_ns"`
	TotalDuration time.Duration `json:"total_duration_ns"`
}

type allocationRetainedMetrics struct {
	PreOperation             runtimeSnapshot          `json:"pre_operation"`
	PostOperationBeforeGC    runtimeSnapshot          `json:"post_operation_before_gc"`
	PostOperationAfterGC     runtimeSnapshot          `json:"post_operation_after_gc"`
	OperationAllocationDelta operationAllocationDelta `json:"operation_allocation_delta"`
}

type sampledHeapPeak struct {
	HeapAlloc   uint64        `json:"heap_alloc"`
	HeapInuse   uint64        `json:"heap_inuse"`
	Interval    time.Duration `json:"interval_ns"`
	SampleCount uint64        `json:"sample_count"`
}

type sampledPeakMetrics struct {
	WorkerHeap             sampledHeapPeak `json:"worker_heap"`
	ParentProcess          processMemory   `json:"parent_process"`
	ParentProcessSupported bool            `json:"parent_process_supported"`
	ParentSampleInterval   time.Duration   `json:"parent_sample_interval_ns"`
	ParentSampleCount      uint64          `json:"parent_sample_count"`
}

type workerMetrics struct {
	Mode                       measurementMode            `json:"mode"`
	Latency                    *latencyMetrics            `json:"latency,omitempty"`
	AllocationRetained         *allocationRetainedMetrics `json:"allocation_retained,omitempty"`
	SampledPeakDuringOperation *sampledPeakMetrics        `json:"sampled_peak_during_operation,omitempty"`
	ObservedRequestBytes       int64                      `json:"observed_request_bytes"`
	ResponseBytes              int64                      `json:"response_bytes"`
	ObservedRequestBodySHA256  string                     `json:"observed_request_body_sha256"`
	ResponseBodySHA256         string                     `json:"response_body_sha256"`
	Connections                connectionMetrics          `json:"connections"`
}

func captureRuntimeSnapshot() runtimeSnapshot {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return runtimeSnapshot{
		HeapAlloc: memory.HeapAlloc, HeapInuse: memory.HeapInuse,
		TotalAlloc: memory.TotalAlloc, Mallocs: memory.Mallocs,
		NumGC: memory.NumGC, Goroutines: runtime.NumGoroutine(),
	}
}

func allocationDelta(before, after runtimeSnapshot) operationAllocationDelta {
	return operationAllocationDelta{
		TotalAlloc: after.TotalAlloc - before.TotalAlloc,
		Mallocs:    after.Mallocs - before.Mallocs,
		NumGC:      after.NumGC - before.NumGC,
	}
}

func (metrics workerMetrics) validateModeContract() error {
	valid := false
	switch metrics.Mode {
	case measurementModeLatency:
		valid = metrics.Latency != nil && metrics.AllocationRetained == nil && metrics.SampledPeakDuringOperation == nil
	case measurementModeAllocationRetained:
		valid = metrics.Latency == nil && metrics.AllocationRetained != nil && metrics.SampledPeakDuringOperation == nil
	case measurementModeSampledPeak:
		valid = metrics.Latency == nil && metrics.AllocationRetained == nil && metrics.SampledPeakDuringOperation != nil
	}
	if !valid {
		return fmt.Errorf("ambiguous metrics for measurement mode %q", metrics.Mode)
	}
	return nil
}
