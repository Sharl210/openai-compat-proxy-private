package perfbench

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestParentProcessSampler_stop_excludes_post_boundary_samples_and_joins_goroutine(t *testing.T) {
	// Given
	ticks := make(chan time.Time, 2)
	sampled := make(chan struct{}, 1)
	var calls atomic.Int64
	sampler := startParentProcessSampler(processSamplerConfig{
		pid: 42, initial: processMemory{RssAnon: 10, VmRSS: 20},
		ticks: ticks, interval: time.Millisecond,
		sample: func(_ int) (processMemory, bool, error) {
			calls.Add(1)
			sampled <- struct{}{}
			return processMemory{RssAnon: 30, VmRSS: 40}, true, nil
		},
	})
	ticks <- time.Time{}
	<-sampled

	// When
	peak, err := sampler.Stop()
	ticks <- time.Time{}

	// Then
	if err != nil {
		t.Fatalf("stop parent sampler: %v", err)
	}
	if peak.process != (processMemory{RssAnon: 30, VmRSS: 40}) || peak.count != 2 {
		t.Fatalf("process peak = %+v", peak)
	}
	if calls.Load() != 1 {
		t.Fatalf("samples after stop boundary = %d, want 1", calls.Load())
	}
	select {
	case <-sampler.stopped:
	default:
		t.Fatal("parent sampler goroutine remained active")
	}
}

func TestParentProcessSampler_sampling_error_is_returned_without_stop_deadlock(t *testing.T) {
	// Given
	ticks := make(chan time.Time, 1)
	sampled := make(chan struct{}, 1)
	wantErr := errors.New("procfs unavailable")
	sampler := startParentProcessSampler(processSamplerConfig{
		pid: 42, initial: processMemory{RssAnon: 10, VmRSS: 20},
		ticks: ticks, interval: time.Millisecond,
		sample: func(_ int) (processMemory, bool, error) {
			sampled <- struct{}{}
			return processMemory{}, true, wantErr
		},
	})
	ticks <- time.Time{}
	<-sampled

	// When
	_, err := sampler.Stop()

	// Then
	if !errors.Is(err, wantErr) {
		t.Fatalf("sampling error = %v, want %v", err, wantErr)
	}
	select {
	case <-sampler.stopped:
	default:
		t.Fatal("failed parent sampler goroutine remained active")
	}
}

func TestFinalizeWorkerResult_returns_sampled_peak_error_without_nil_payload_panic(t *testing.T) {
	// Given
	result := workerResult{Error: "round trip failed"}
	request := workerRequest{Action: workerActionRoundTrip, Mode: measurementModeSampledPeak}

	// When
	err := finalizeWorkerResult(request, &result, sampledProcessPeak{})

	// Then
	if err == nil || err.Error() != "worker failed: round trip failed" {
		t.Fatalf("finalize error = %v", err)
	}
}
