//go:build linux

package perfbench

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

type clockTicksReader func() (uint64, error)
type processCPUStatReader func(string) ([]byte, error)

func readProcessCPUTime() (processCPUTime, error) {
	return readProcessCPUTimePID(os.Getpid())
}

func readProcessCPUTimePID(pid int) (processCPUTime, error) {
	return readProcessCPUTimePIDWithClock(pid, readLinuxClockTicksPerSecond)
}

func readProcessCPUTimePIDWithClock(pid int, readClockTicks clockTicksReader) (processCPUTime, error) {
	return readProcessCPUTimePIDWithReaders(pid, readClockTicks, os.ReadFile)
}

func readProcessCPUTimePIDWithReaders(pid int, readClockTicks clockTicksReader, readStat processCPUStatReader) (processCPUTime, error) {
	ticksPerSecond, err := readClockTicks()
	if err != nil {
		return processCPUTime{}, fmt.Errorf("read CLK_TCK: %w", err)
	}
	stat, err := readStat(processStatPath(pid))
	if err != nil {
		return processCPUTime{}, fmt.Errorf("read process stat: %w", err)
	}
	cpuTime, err := parseProcessCPUTimeStat(string(stat), ticksPerSecond)
	if err != nil {
		return processCPUTime{}, err
	}
	return cpuTime, nil
}

func TestReadProcessCPUTimePIDWithReaders_reads_requested_process_stat(t *testing.T) {
	// Given
	const pid = 4321
	var readPath string
	readStat := func(path string) ([]byte, error) {
		readPath = path
		return []byte("4321 (proxy child) S 0 0 0 0 0 0 0 0 0 0 120 45"), nil
	}

	// When
	got, err := readProcessCPUTimePIDWithReaders(pid, func() (uint64, error) {
		return 100, nil
	}, readStat)

	// Then
	if err != nil {
		t.Fatalf("read process CPU time: %v", err)
	}
	if readPath != "/proc/4321/stat" {
		t.Fatalf("process stat path = %q, want %q", readPath, "/proc/4321/stat")
	}
	want := processCPUTime{User: 1200 * time.Millisecond, System: 450 * time.Millisecond, Total: 1650 * time.Millisecond, Supported: true}
	if got != want {
		t.Fatalf("process CPU time = %+v, want %+v", got, want)
	}
}

func readLinuxClockTicksPerSecond() (uint64, error) {
	output, err := exec.Command("getconf", "CLK_TCK").Output()
	if err != nil {
		return 0, fmt.Errorf("run getconf CLK_TCK: %w", err)
	}
	ticksPerSecond, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse getconf CLK_TCK: %w", err)
	}
	if ticksPerSecond == 0 {
		return 0, fmt.Errorf("parse getconf CLK_TCK: zero value")
	}
	return ticksPerSecond, nil
}

func processStatPath(pid int) string {
	return fmt.Sprintf("/proc/%d/stat", pid)
}

func TestReadProcessCPUTimePIDWithClock_reports_unsupported_when_clock_rate_is_missing_or_invalid(t *testing.T) {
	tests := map[string]clockTicksReader{
		"missing clock rate": func() (uint64, error) {
			return 0, errors.New("getconf unavailable")
		},
		"invalid clock rate": func() (uint64, error) {
			return 0, nil
		},
	}
	for name, readClockTicks := range tests {
		t.Run(name, func(t *testing.T) {
			// When
			got, err := readProcessCPUTimePIDWithClock(1, readClockTicks)

			// Then
			if err == nil {
				t.Fatal("invalid clock rate was accepted")
			}
			if got.Supported || got.User != 0 || got.System != 0 || got.Total != 0 {
				t.Fatalf("unsupported process CPU time = %+v", got)
			}
		})
	}
}
