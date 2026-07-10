package perfbench

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type memorySampleFunc func() (memorySnapshot, error)

type peakSampler struct {
	stop    chan struct{}
	stopped chan struct{}
	result  chan peakSamplerResult
}

type peakSamplerResult struct {
	peak memorySnapshot
	err  error
}

func startPeakSampler(sample memorySampleFunc, ticks <-chan time.Time) (*peakSampler, error) {
	initial, err := sample()
	if err != nil {
		return nil, fmt.Errorf("sample peak boundary: %w", err)
	}
	sampler := &peakSampler{
		stop: make(chan struct{}), stopped: make(chan struct{}),
		result: make(chan peakSamplerResult, 1),
	}
	go func() {
		defer close(sampler.stopped)
		peak := initial
		for {
			select {
			case <-ticks:
				current, sampleErr := sample()
				if sampleErr != nil {
					sampler.result <- peakSamplerResult{err: fmt.Errorf("sample operation peak: %w", sampleErr)}
					return
				}
				peak = maxMemorySnapshot(peak, current)
			case <-sampler.stop:
				final, sampleErr := sample()
				if sampleErr != nil {
					sampler.result <- peakSamplerResult{err: fmt.Errorf("sample stop boundary: %w", sampleErr)}
					return
				}
				peak = maxMemorySnapshot(peak, final)
				sampler.result <- peakSamplerResult{peak: peak}
				return
			}
		}
	}()
	return sampler, nil
}

func (sampler *peakSampler) Stop() (memorySnapshot, error) {
	close(sampler.stop)
	result := <-sampler.result
	<-sampler.stopped
	return result.peak, result.err
}

func TestPeakSampler_samples_only_between_start_and_stop(t *testing.T) {
	// Given
	values := [...]memorySnapshot{
		{HeapAlloc: 10, HeapInuse: 20, RssAnon: 30},
		{HeapAlloc: 40, HeapInuse: 50, RssAnon: 60},
		{HeapAlloc: 70, HeapInuse: 80, RssAnon: 90},
	}
	var calls atomic.Int64
	sampled := make(chan struct{}, len(values))
	ticks := make(chan time.Time, len(values))
	sample := func() (memorySnapshot, error) {
		index := int(calls.Add(1) - 1)
		sampled <- struct{}{}
		return values[index], nil
	}

	// When
	sampler, err := startPeakSampler(sample, ticks)
	if err != nil {
		t.Fatalf("start sampler: %v", err)
	}
	<-sampled
	ticks <- time.Time{}
	<-sampled
	peak, err := sampler.Stop()
	if err != nil {
		t.Fatalf("stop sampler: %v", err)
	}

	// Then
	if peak != values[2] {
		t.Fatalf("peak = %+v, want stop-boundary %+v", peak, values[2])
	}
	select {
	case <-sampler.stopped:
	default:
		t.Fatal("sampler goroutine remained active after Stop")
	}
	ticks <- time.Time{}
	if calls.Load() != 3 {
		t.Fatalf("samples after Stop = %d, want 3", calls.Load())
	}
}

func TestMemoryDelta_records_retained_and_peak_changes(t *testing.T) {
	// Given
	idle := memorySnapshot{
		HeapAlloc: 10, HeapInuse: 20, TotalAlloc: 30, Mallocs: 40, NumGC: 2,
		RssAnon: 50, VmRSS: 60, Goroutines: 3,
	}
	retained := memorySnapshot{
		HeapAlloc: 7, HeapInuse: 25, TotalAlloc: 50, Mallocs: 70, NumGC: 3,
		RssAnon: 45, VmRSS: 90, Goroutines: 4,
	}

	// When
	delta := memoryDeltaBetween(idle, retained)

	// Then
	if delta.HeapAlloc != -3 || delta.HeapInuse != 5 || delta.TotalAlloc != 20 {
		t.Fatalf("heap delta = %+v", delta)
	}
	if delta.Mallocs != 30 || delta.NumGC != 1 || delta.RssAnon != -5 || delta.VmRSS != 30 {
		t.Fatalf("process delta = %+v", delta)
	}
	if delta.Goroutines != 1 {
		t.Fatalf("goroutine delta = %d, want 1", delta.Goroutines)
	}
}
