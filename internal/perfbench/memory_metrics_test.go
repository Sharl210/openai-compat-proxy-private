package perfbench

import (
	"errors"
	"runtime"
	"time"
)

type memorySnapshot struct {
	HeapAlloc              uint64 `json:"heap_alloc"`
	HeapInuse              uint64 `json:"heap_inuse"`
	TotalAlloc             uint64 `json:"total_alloc"`
	Mallocs                uint64 `json:"mallocs"`
	NumGC                  uint32 `json:"num_gc"`
	RssAnon                uint64 `json:"rss_anon"`
	VmRSS                  uint64 `json:"vm_rss"`
	Goroutines             int    `json:"goroutines"`
	ProcessMemorySupported bool   `json:"process_memory_supported"`
}

type memoryDelta struct {
	HeapAlloc  int64 `json:"heap_alloc"`
	HeapInuse  int64 `json:"heap_inuse"`
	TotalAlloc int64 `json:"total_alloc"`
	Mallocs    int64 `json:"mallocs"`
	NumGC      int64 `json:"num_gc"`
	RssAnon    int64 `json:"rss_anon"`
	VmRSS      int64 `json:"vm_rss"`
	Goroutines int64 `json:"goroutines"`
}

type workerMetrics struct {
	Idle                      memorySnapshot    `json:"idle"`
	Retained                  memorySnapshot    `json:"retained"`
	PeakDuringOperation       memorySnapshot    `json:"peak_during_operation"`
	RetainedDelta             memoryDelta       `json:"retained_delta"`
	PeakDelta                 memoryDelta       `json:"peak_delta"`
	TTFB                      time.Duration     `json:"ttfb_ns"`
	TotalDuration             time.Duration     `json:"total_duration_ns"`
	ObservedRequestBytes      int64             `json:"observed_request_bytes"`
	ResponseBytes             int64             `json:"response_bytes"`
	ObservedRequestBodySHA256 string            `json:"observed_request_body_sha256"`
	ResponseBodySHA256        string            `json:"response_body_sha256"`
	Connections               connectionMetrics `json:"connections"`
}

func captureMemorySnapshot() (memorySnapshot, error) {
	var runtimeMemory runtime.MemStats
	runtime.ReadMemStats(&runtimeMemory)
	process, supported, err := readProcessMemory()
	if err != nil && !errors.Is(err, errProcessMemoryUnsupported) {
		return memorySnapshot{}, err
	}
	return memorySnapshot{
		HeapAlloc: runtimeMemory.HeapAlloc, HeapInuse: runtimeMemory.HeapInuse,
		TotalAlloc: runtimeMemory.TotalAlloc, Mallocs: runtimeMemory.Mallocs,
		NumGC: runtimeMemory.NumGC, RssAnon: process.RssAnon, VmRSS: process.VmRSS,
		Goroutines: runtime.NumGoroutine(), ProcessMemorySupported: supported,
	}, nil
}

func memoryDeltaBetween(before, after memorySnapshot) memoryDelta {
	return memoryDelta{
		HeapAlloc:  int64(after.HeapAlloc) - int64(before.HeapAlloc),
		HeapInuse:  int64(after.HeapInuse) - int64(before.HeapInuse),
		TotalAlloc: int64(after.TotalAlloc) - int64(before.TotalAlloc),
		Mallocs:    int64(after.Mallocs) - int64(before.Mallocs),
		NumGC:      int64(after.NumGC) - int64(before.NumGC),
		RssAnon:    int64(after.RssAnon) - int64(before.RssAnon),
		VmRSS:      int64(after.VmRSS) - int64(before.VmRSS),
		Goroutines: int64(after.Goroutines - before.Goroutines),
	}
}

func maxMemorySnapshot(left, right memorySnapshot) memorySnapshot {
	return memorySnapshot{
		HeapAlloc:              max(left.HeapAlloc, right.HeapAlloc),
		HeapInuse:              max(left.HeapInuse, right.HeapInuse),
		TotalAlloc:             max(left.TotalAlloc, right.TotalAlloc),
		Mallocs:                max(left.Mallocs, right.Mallocs),
		NumGC:                  max(left.NumGC, right.NumGC),
		RssAnon:                max(left.RssAnon, right.RssAnon),
		VmRSS:                  max(left.VmRSS, right.VmRSS),
		Goroutines:             max(left.Goroutines, right.Goroutines),
		ProcessMemorySupported: left.ProcessMemorySupported || right.ProcessMemorySupported,
	}
}
