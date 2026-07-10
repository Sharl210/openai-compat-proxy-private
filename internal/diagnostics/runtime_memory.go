package diagnostics

import (
	"bufio"
	"bytes"
	"os"
	"runtime"
	"strconv"
	"strings"
)

const linuxProcessStatusPath = "/proc/self/status"

type RuntimeMemory struct {
	HeapAlloc    uint64 `json:"heap_alloc"`
	HeapInuse    uint64 `json:"heap_inuse"`
	HeapIdle     uint64 `json:"heap_idle"`
	HeapReleased uint64 `json:"heap_released"`
	Sys          uint64 `json:"sys"`
	StackInuse   uint64 `json:"stack_inuse"`
	NumGC        uint32 `json:"num_gc"`
	Goroutines   int    `json:"goroutines"`
	VmRSS        uint64 `json:"vm_rss"`
	RssAnon      uint64 `json:"rss_anon"`
}

func Snapshot() RuntimeMemory {
	return snapshotWithStatusReader(os.ReadFile)
}

func snapshotWithStatusReader(readStatus func(string) ([]byte, error)) RuntimeMemory {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	snapshot := RuntimeMemory{
		HeapAlloc:    mem.HeapAlloc,
		HeapInuse:    mem.HeapInuse,
		HeapIdle:     mem.HeapIdle,
		HeapReleased: mem.HeapReleased,
		Sys:          mem.Sys,
		StackInuse:   mem.StackInuse,
		NumGC:        mem.NumGC,
		Goroutines:   runtime.NumGoroutine(),
	}
	if status, err := readStatus(linuxProcessStatusPath); err == nil {
		snapshot.VmRSS, snapshot.RssAnon = parseLinuxStatus(status)
	}
	return snapshot
}

func parseLinuxStatus(status []byte) (uint64, uint64) {
	var vmRSS uint64
	var rssAnon uint64
	scanner := bufio.NewScanner(bytes.NewReader(status))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || value > ^uint64(0)/1024 {
			continue
		}
		switch fields[0] {
		case "VmRSS:":
			vmRSS = value * 1024
		case "RssAnon:":
			rssAnon = value * 1024
		}
	}
	return vmRSS, rssAnon
}
