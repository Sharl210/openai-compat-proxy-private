package perfbench

import (
	"runtime"
	"testing"
	"time"
)

type heapGauge struct {
	heapAlloc uint64
	heapInuse uint64
}

type heapPeakSampler interface {
	Stop() (sampledHeapPeak, error)
}

type runtimeHeapSampler struct {
	stop       chan struct{}
	stopped    chan struct{}
	result     chan sampledHeapPeak
	stopTicker func()
}

type heapSamplerConfig struct {
	sample     func() heapGauge
	ticks      <-chan time.Time
	interval   time.Duration
	stopTicker func()
}

func newRuntimeHeapSampler() (heapPeakSampler, error) {
	ticker := time.NewTicker(measurementSampleInterval)
	return startHeapSampler(heapSamplerConfig{
		sample: captureHeapGauge, ticks: ticker.C,
		interval: measurementSampleInterval, stopTicker: ticker.Stop,
	}), nil
}

func startHeapSampler(config heapSamplerConfig) *runtimeHeapSampler {
	initial := config.sample()
	sampler := &runtimeHeapSampler{
		stop: make(chan struct{}), stopped: make(chan struct{}),
		result: make(chan sampledHeapPeak, 1), stopTicker: config.stopTicker,
	}
	go func() {
		defer close(sampler.stopped)
		peak := initial
		count := uint64(1)
		for {
			select {
			case <-config.ticks:
				peak = maxHeapGauge(peak, config.sample())
				count++
			case <-sampler.stop:
				peak = maxHeapGauge(peak, config.sample())
				count++
				sampler.result <- sampledHeapPeak{
					HeapAlloc: peak.heapAlloc, HeapInuse: peak.heapInuse,
					Interval: config.interval, SampleCount: count,
				}
				return
			}
		}
	}()
	return sampler
}

func (sampler *runtimeHeapSampler) Stop() (sampledHeapPeak, error) {
	if sampler.stopTicker != nil {
		sampler.stopTicker()
	}
	close(sampler.stop)
	result := <-sampler.result
	<-sampler.stopped
	return result, nil
}

func captureHeapGauge() heapGauge {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return heapGauge{heapAlloc: memory.HeapAlloc, heapInuse: memory.HeapInuse}
}

func maxHeapGauge(left, right heapGauge) heapGauge {
	return heapGauge{
		heapAlloc: max(left.heapAlloc, right.heapAlloc),
		heapInuse: max(left.heapInuse, right.heapInuse),
	}
}

func TestHeapSampler_stops_without_goroutine_leak_and_reports_only_gauges(t *testing.T) {
	// Given
	values := []heapGauge{{10, 20}, {40, 50}, {70, 80}}
	index := 0
	sampled := make(chan struct{}, len(values))
	ticks := make(chan time.Time, len(values))
	sampler := startHeapSampler(heapSamplerConfig{
		sample: func() heapGauge {
			value := values[index]
			index++
			sampled <- struct{}{}
			return value
		},
		ticks: ticks, interval: time.Millisecond,
	})
	<-sampled
	ticks <- time.Time{}
	<-sampled

	// When
	peak, err := sampler.Stop()

	// Then
	if err != nil {
		t.Fatalf("stop heap sampler: %v", err)
	}
	if peak != (sampledHeapPeak{HeapAlloc: 70, HeapInuse: 80, Interval: time.Millisecond, SampleCount: 3}) {
		t.Fatalf("heap peak = %+v", peak)
	}
	select {
	case <-sampler.stopped:
	default:
		t.Fatal("heap sampler goroutine remained active")
	}
}
