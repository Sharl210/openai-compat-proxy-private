//go:build !linux

package perfbench

import (
	"errors"
	"testing"
)

func readProcessCPUTime() (processCPUTime, error) {
	return processCPUTime{}, errProcessCPUTimeUnsupported
}

func readProcessCPUTimePID(pid int) (processCPUTime, error) {
	return processCPUTime{}, errProcessCPUTimeUnsupported
}

func readProcessCPUTimePIDWithClock(pid int, readClockTicks clockTicksReader) (processCPUTime, error) {
	return processCPUTime{}, errProcessCPUTimeUnsupported
}

type clockTicksReader func() (uint64, error)

func TestReadProcessCPUTime_reports_unsupported_on_non_linux(t *testing.T) {
	// When
	got, err := readProcessCPUTime()

	// Then
	if !errors.Is(err, errProcessCPUTimeUnsupported) {
		t.Fatalf("unsupported platform error = %v", err)
	}
	if got.Supported || got.User != 0 || got.System != 0 || got.Total != 0 {
		t.Fatalf("unsupported platform CPU time = %+v", got)
	}
}
