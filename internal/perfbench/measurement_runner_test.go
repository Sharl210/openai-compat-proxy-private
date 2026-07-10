package perfbench

import (
	"errors"
	"fmt"
	"runtime"
)

type measurementHooks struct {
	captureRuntime   func() runtimeSnapshot
	forceGC          func()
	startHeapSampler func() (heapPeakSampler, error)
}

func defaultMeasurementHooks() measurementHooks {
	return measurementHooks{
		captureRuntime:   captureRuntimeSnapshot,
		forceGC:          runtime.GC,
		startHeapSampler: newRuntimeHeapSampler,
	}
}

func measureOperation(
	mode measurementMode,
	operation func() (roundTripEvidence, error),
	hooks measurementHooks,
) (workerMetrics, error) {
	metrics := workerMetrics{Mode: mode}
	switch mode {
	case measurementModeLatency:
		evidence, err := operation()
		if err != nil {
			return workerMetrics{}, err
		}
		metrics.Latency = &latencyMetrics{TTFB: evidence.ttfb, TotalDuration: evidence.total}
		applyRoundTripEvidence(&metrics, evidence)
	case measurementModeAllocationRetained:
		hooks.forceGC()
		before := hooks.captureRuntime()
		evidence, err := operation()
		if err != nil {
			return workerMetrics{}, err
		}
		postBeforeGC := hooks.captureRuntime()
		hooks.forceGC()
		postAfterGC := hooks.captureRuntime()
		metrics.AllocationRetained = &allocationRetainedMetrics{
			PreOperation: before, PostOperationBeforeGC: postBeforeGC,
			PostOperationAfterGC:     postAfterGC,
			OperationAllocationDelta: allocationDelta(before, postBeforeGC),
		}
		applyRoundTripEvidence(&metrics, evidence)
	case measurementModeSampledPeak:
		sampler, err := hooks.startHeapSampler()
		if err != nil {
			return workerMetrics{}, fmt.Errorf("start heap sampler: %w", err)
		}
		evidence, operationErr := operation()
		peak, sampleErr := sampler.Stop()
		if err := errors.Join(operationErr, sampleErr); err != nil {
			return workerMetrics{}, err
		}
		metrics.SampledPeakDuringOperation = &sampledPeakMetrics{WorkerHeap: peak}
		applyRoundTripEvidence(&metrics, evidence)
	default:
		return workerMetrics{}, fmt.Errorf("unsupported measurement mode %q", mode)
	}
	return metrics, metrics.validateModeContract()
}

func applyRoundTripEvidence(metrics *workerMetrics, evidence roundTripEvidence) {
	metrics.ObservedRequestBytes = evidence.observedRequestBytes
	metrics.ResponseBytes = int64(len(evidence.response))
	metrics.ObservedRequestBodySHA256 = evidence.observedRequestHash
	metrics.ResponseBodySHA256 = sha256Hex(evidence.response)
	metrics.Connections = evidence.connections
}
