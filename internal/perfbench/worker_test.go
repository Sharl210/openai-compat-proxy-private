package perfbench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

type workerAction string

const (
	workerActionRoundTrip workerAction = "round_trip"
	workerActionFail      workerAction = "fail"
	workerActionBlock     workerAction = "block"
)

type workerRequest struct {
	Action   workerAction  `json:"action"`
	Scenario scenario      `json:"scenario"`
	Timeout  time.Duration `json:"-"`
}

func executeWorker(request workerRequest) (workerResult, error) {
	switch request.Action {
	case workerActionFail:
		return workerResult{ScenarioID: request.Scenario.ID}, errors.New("requested worker failure")
	case workerActionBlock:
		_, err := io.Copy(io.Discard, os.Stdin)
		if err != nil {
			return workerResult{}, fmt.Errorf("wait for parent input: %w", err)
		}
		return workerResult{}, errors.New("parent input closed before cancellation")
	case workerActionRoundTrip:
		evidence, memory, err := executeMeasuredRoundTrip(request.Scenario)
		if err != nil {
			return workerResult{}, err
		}
		return workerResult{
			ScenarioID: request.Scenario.ID,
			Metrics: workerMetrics{
				Idle: memory.idle, Retained: memory.retained,
				PeakDuringOperation: memory.peak,
				RetainedDelta:       memoryDeltaBetween(memory.idle, memory.retained),
				PeakDelta:           memoryDeltaBetween(memory.idle, memory.peak),
				TTFB:                evidence.ttfb, TotalDuration: evidence.total,
				ObservedRequestBytes:      evidence.observedRequestBytes,
				ObservedRequestBodySHA256: evidence.observedRequestHash,
				ResponseBytes:             int64(len(evidence.response)),
				ResponseBodySHA256:        sha256Hex(evidence.response),
				Connections:               evidence.connections,
			},
		}, nil
	default:
		return workerResult{}, fmt.Errorf("unsupported worker action %q", request.Action)
	}
}

func TestPerfWorker_protocol_round_trip(t *testing.T) {
	t.Setenv("PERFBENCH_HELPER_TOKEN", "inherited-must-be-stripped")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := workerRequest{Action: workerActionRoundTrip, Scenario: scenarioCatalog()[0]}
	run, err := runPerfWorker(ctx, request, nil)
	if err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if !run.Ready || !run.Exited || run.Sentinel == "" {
		t.Fatalf("worker lifecycle = %+v", run)
	}
	metrics := run.Result.Metrics
	if metrics.Idle.HeapAlloc == 0 || metrics.Retained.HeapInuse == 0 || metrics.Idle.Mallocs == 0 {
		t.Fatalf("incomplete heap metrics: %+v", metrics)
	}
	if metrics.PeakDuringOperation.HeapInuse < metrics.Idle.HeapInuse || metrics.PeakDelta.HeapInuse < 0 {
		t.Fatalf("invalid peak metrics: %+v", metrics)
	}
	if metrics.Idle.ProcessMemorySupported && (metrics.Idle.RssAnon == 0 || metrics.Idle.VmRSS == 0) {
		t.Fatalf("incomplete process metrics: %+v", metrics)
	}
	if metrics.TTFB <= 0 || metrics.TotalDuration <= 0 {
		t.Fatalf("incomplete latency metrics: %+v", metrics)
	}
	if metrics.ObservedRequestBytes <= request.Scenario.ImageBytes || metrics.ResponseBytes == 0 {
		t.Fatalf("incomplete byte metrics: %+v", metrics)
	}
	if len(metrics.ObservedRequestBodySHA256) != 64 || len(metrics.ResponseBodySHA256) != 64 {
		t.Fatalf("invalid body hashes: %+v", metrics)
	}
	if metrics.Connections.New == 0 || metrics.Connections.Closed == 0 {
		t.Fatalf("incomplete connection metrics: %+v", metrics.Connections)
	}
}
