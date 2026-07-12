package perfbench

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestProcessCPUTimeParser_reads_user_system_and_total_when_stat_is_normal(t *testing.T) {
	// Given
	stat := "123 (worker) S 0 0 0 0 0 0 0 0 0 0 120 45"

	// When
	got, err := parseProcessCPUTimeStat(stat, 100)

	// Then
	if err != nil {
		t.Fatalf("parse process stat: %v", err)
	}
	want := processCPUTime{
		User:      1200 * time.Millisecond,
		System:    450 * time.Millisecond,
		Total:     1650 * time.Millisecond,
		Supported: true,
	}
	if got != want {
		t.Fatalf("process CPU time = %+v, want %+v", got, want)
	}
}

func TestProcessCPUTimeParser_reads_user_system_and_total_when_process_name_has_spaces_and_parentheses(t *testing.T) {
	// Given
	stat := "456 (worker (staging) child) R 0 0 0 0 0 0 0 0 0 0 25 75"

	// When
	got, err := parseProcessCPUTimeStat(stat, 100)

	// Then
	if err != nil {
		t.Fatalf("parse process stat: %v", err)
	}
	want := processCPUTime{
		User:      250 * time.Millisecond,
		System:    750 * time.Millisecond,
		Total:     time.Second,
		Supported: true,
	}
	if got != want {
		t.Fatalf("process CPU time = %+v, want %+v", got, want)
	}
}

func TestProcessCPUTimeParser_rejects_malformed_stat_or_invalid_clock_rate(t *testing.T) {
	tests := map[string]struct {
		stat           string
		ticksPerSecond uint64
	}{
		"missing closing process name delimiter": {stat: "123 worker S 0 0 0", ticksPerSecond: 100},
		"too few fields":                         {stat: "123 (worker) S 0", ticksPerSecond: 100},
		"non numeric user ticks":                 {stat: "123 (worker) S 0 0 0 0 0 0 0 0 0 0 bad 45", ticksPerSecond: 100},
		"missing clock rate":                     {stat: "123 (worker) S 0 0 0 0 0 0 0 0 0 0 120 45", ticksPerSecond: 0},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// When
			_, err := parseProcessCPUTimeStat(test.stat, test.ticksPerSecond)

			// Then
			if err == nil {
				t.Fatal("invalid process stat was accepted")
			}
		})
	}
}

func TestProcessCPUTimeDelta_reports_component_durations_when_both_samples_are_supported(t *testing.T) {
	// Given
	before := processCPUTime{User: 2 * time.Second, System: 3 * time.Second, Total: 5 * time.Second, Supported: true}
	after := processCPUTime{User: 5500 * time.Millisecond, System: 4250 * time.Millisecond, Total: 9750 * time.Millisecond, Supported: true}

	// When
	got := processCPUTimeDelta(before, after)

	// Then
	want := processCPUTime{User: 3500 * time.Millisecond, System: 1250 * time.Millisecond, Total: 4750 * time.Millisecond, Supported: true}
	if got != want {
		t.Fatalf("CPU delta = %+v, want %+v", got, want)
	}
}

func TestProcessCPUTimeDelta_is_unsupported_when_a_sample_is_unavailable_or_regresses(t *testing.T) {
	tests := map[string]struct {
		before processCPUTime
		after  processCPUTime
	}{
		"start unsupported": {
			before: processCPUTime{},
			after:  processCPUTime{User: time.Second, System: time.Second, Total: 2 * time.Second, Supported: true},
		},
		"end unsupported": {
			before: processCPUTime{User: time.Second, System: time.Second, Total: 2 * time.Second, Supported: true},
			after:  processCPUTime{},
		},
		"user time regresses": {
			before: processCPUTime{User: 2 * time.Second, System: time.Second, Total: 3 * time.Second, Supported: true},
			after:  processCPUTime{User: time.Second, System: 2 * time.Second, Total: 3 * time.Second, Supported: true},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// When
			got := processCPUTimeDelta(test.before, test.after)

			// Then
			if got.Supported || got.User != 0 || got.System != 0 || got.Total != 0 {
				t.Fatalf("unsupported CPU delta = %+v", got)
			}
		})
	}
}

func TestReadProcessCPUTime_reports_support_explicitly(t *testing.T) {
	// When
	got, err := readProcessCPUTime()

	// Then
	if runtime.GOOS != "linux" {
		if !errors.Is(err, errProcessCPUTimeUnsupported) {
			t.Fatalf("unsupported platform error = %v", err)
		}
		if got.Supported || got.User != 0 || got.System != 0 || got.Total != 0 {
			t.Fatalf("unsupported platform CPU time = %+v", got)
		}
		return
	}
	if err != nil {
		t.Fatalf("read process CPU time: %v", err)
	}
	if !got.Supported || got.Total != got.User+got.System {
		t.Fatalf("supported process CPU time = %+v", got)
	}
}
