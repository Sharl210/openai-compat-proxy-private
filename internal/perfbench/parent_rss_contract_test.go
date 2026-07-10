//go:build linux

package perfbench

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestPerfWorker_sampled_peak_parent_observes_child_rss(t *testing.T) {
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := workerRequest{
		Action:   workerActionRoundTrip,
		Mode:     measurementModeSampledPeak,
		Scenario: scenarioCatalog()[0],
	}

	// When
	run, err := runPerfWorker(ctx, request, nil)

	// Then
	if err != nil {
		t.Fatalf("run sampled-peak worker: %v", err)
	}
	peak := run.Result.Metrics.SampledPeakDuringOperation
	if peak == nil || !peak.ParentProcessSupported || peak.ParentProcess.RssAnon == 0 || peak.ParentProcess.VmRSS == 0 {
		t.Fatalf("parent process peak = %+v", peak)
	}
	if peak.ParentSampleInterval <= 0 || peak.ParentSampleCount < 2 {
		t.Fatalf("parent sampling contract = %+v", peak)
	}
	if _, statErr := os.Stat(processStatusPath(run.PID)); !os.IsNotExist(statErr) {
		t.Fatalf("worker process %d still exists after Wait: %v", run.PID, statErr)
	}
}

func TestPerfWorker_latency_and_allocation_have_no_sampled_peak_payload(t *testing.T) {
	for _, mode := range []measurementMode{measurementModeLatency, measurementModeAllocationRetained} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			request := workerRequest{Action: workerActionRoundTrip, Mode: mode, Scenario: scenarioCatalog()[0]}

			run, err := runPerfWorker(ctx, request, nil)
			if err != nil {
				t.Fatalf("run %s worker: %v", mode, err)
			}
			if run.Result.Metrics.SampledPeakDuringOperation != nil {
				t.Fatalf("%s returned sampled peak: %+v", mode, run.Result.Metrics)
			}
		})
	}
}
