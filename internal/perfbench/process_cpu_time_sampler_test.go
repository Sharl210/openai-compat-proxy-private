package perfbench

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
	"time"
)

var errProcessCPUTimeUnsupported = errors.New("process CPU time metrics unsupported")

type processCPUTime struct {
	User      time.Duration
	System    time.Duration
	Total     time.Duration
	Supported bool
}

func parseProcessCPUTimeStat(stat string, ticksPerSecond uint64) (processCPUTime, error) {
	openName := strings.IndexByte(stat, '(')
	closeName := strings.LastIndexByte(stat, ')')
	if openName < 0 || closeName <= openName {
		return processCPUTime{}, errors.New("invalid process stat name field")
	}
	fields := strings.Fields(stat[closeName+1:])
	if len(fields) < 13 {
		return processCPUTime{}, fmt.Errorf("process stat has %d fields after name, want at least 13", len(fields))
	}
	userTicks, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return processCPUTime{}, fmt.Errorf("parse process user ticks: %w", err)
	}
	systemTicks, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return processCPUTime{}, fmt.Errorf("parse process system ticks: %w", err)
	}
	user, err := clockTicksDuration(userTicks, ticksPerSecond)
	if err != nil {
		return processCPUTime{}, fmt.Errorf("convert process user ticks: %w", err)
	}
	system, err := clockTicksDuration(systemTicks, ticksPerSecond)
	if err != nil {
		return processCPUTime{}, fmt.Errorf("convert process system ticks: %w", err)
	}
	if user > time.Duration(math.MaxInt64)-system {
		return processCPUTime{}, errors.New("process total CPU time overflows duration")
	}
	return processCPUTime{User: user, System: system, Total: user + system, Supported: true}, nil
}

func clockTicksDuration(ticks, ticksPerSecond uint64) (time.Duration, error) {
	if ticksPerSecond == 0 {
		return 0, errors.New("clock ticks per second is zero")
	}
	seconds := ticks / ticksPerSecond
	if seconds > uint64(math.MaxInt64)/uint64(time.Second) {
		return 0, errors.New("clock ticks exceed duration range")
	}
	nanosecondsHigh, nanosecondsLow := bits.Mul64(ticks%ticksPerSecond, uint64(time.Second))
	nanoseconds, _ := bits.Div64(nanosecondsHigh, nanosecondsLow, ticksPerSecond)
	total := seconds * uint64(time.Second)
	if nanoseconds > uint64(math.MaxInt64)-total {
		return 0, errors.New("clock ticks exceed duration range")
	}
	return time.Duration(total + nanoseconds), nil
}

func processCPUTimeDelta(before, after processCPUTime) processCPUTime {
	if !before.Supported || !after.Supported || after.User < before.User || after.System < before.System {
		return processCPUTime{}
	}
	user := after.User - before.User
	system := after.System - before.System
	if user > time.Duration(math.MaxInt64)-system {
		return processCPUTime{}
	}
	return processCPUTime{User: user, System: system, Total: user + system, Supported: true}
}
