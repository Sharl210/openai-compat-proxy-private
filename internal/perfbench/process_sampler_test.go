package perfbench

import (
	"fmt"
	"time"
)

type sampledProcessPeak struct {
	process   processMemory
	supported bool
	interval  time.Duration
	count     uint64
	err       error
}

type processPeakSampler interface {
	Stop() (sampledProcessPeak, error)
}

type processSamplerConfig struct {
	pid        int
	initial    processMemory
	sample     func(int) (processMemory, bool, error)
	ticks      <-chan time.Time
	interval   time.Duration
	stopTicker func()
}

type parentProcessSampler struct {
	config  processSamplerConfig
	stop    chan struct{}
	stopped chan struct{}
	result  chan sampledProcessPeak
}

type staticProcessSampler struct {
	result sampledProcessPeak
}

func newParentProcessSampler(pid int) (processPeakSampler, error) {
	initial, supported, err := readProcessMemoryPID(pid)
	if err != nil && !supported {
		return staticProcessSampler{result: sampledProcessPeak{}}, nil
	}
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(measurementSampleInterval)
	return startParentProcessSampler(processSamplerConfig{
		pid: pid, initial: initial, sample: readProcessMemoryPID,
		ticks: ticker.C, interval: measurementSampleInterval, stopTicker: ticker.Stop,
	}), nil
}

func startParentProcessSampler(config processSamplerConfig) *parentProcessSampler {
	sampler := &parentProcessSampler{
		config: config, stop: make(chan struct{}), stopped: make(chan struct{}),
		result: make(chan sampledProcessPeak, 1),
	}
	go sampler.run()
	return sampler
}

func (sampler *parentProcessSampler) run() {
	defer close(sampler.stopped)
	peak := sampler.config.initial
	count := uint64(1)
	finish := func(err error) {
		sampler.result <- sampledProcessPeak{
			process: peak, supported: true, interval: sampler.config.interval,
			count: count, err: err,
		}
	}
	for {
		select {
		case <-sampler.stop:
			finish(nil)
			return
		default:
		}
		select {
		case <-sampler.stop:
			finish(nil)
			return
		case <-sampler.config.ticks:
			select {
			case <-sampler.stop:
				finish(nil)
				return
			default:
			}
			current, _, err := sampler.config.sample(sampler.config.pid)
			if err != nil {
				finish(fmt.Errorf("sample child process memory: %w", err))
				return
			}
			peak = maxProcessMemory(peak, current)
			count++
		}
	}
}

func (sampler *parentProcessSampler) Stop() (sampledProcessPeak, error) {
	if sampler.config.stopTicker != nil {
		sampler.config.stopTicker()
	}
	close(sampler.stop)
	result := <-sampler.result
	<-sampler.stopped
	return result, result.err
}

func (sampler staticProcessSampler) Stop() (sampledProcessPeak, error) {
	return sampler.result, sampler.result.err
}

func maxProcessMemory(left, right processMemory) processMemory {
	return processMemory{
		RssAnon: max(left.RssAnon, right.RssAnon),
		VmRSS:   max(left.VmRSS, right.VmRSS),
	}
}
