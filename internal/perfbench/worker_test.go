package perfbench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
)

type workerAction string

const (
	workerActionRoundTrip workerAction = "round_trip"
	workerActionFail      workerAction = "fail"
	workerActionBlock     workerAction = "block"
	workerActionProxy     workerAction = "proxy_server"
)

type workerRequest struct {
	Action      workerAction    `json:"action"`
	Mode        measurementMode `json:"mode,omitempty"`
	Scenario    scenario        `json:"scenario"`
	ProxyConfig *config.Config  `json:"proxy_config,omitempty"`
	UpstreamURL string          `json:"upstream_url,omitempty"`
	Timeout     time.Duration   `json:"-"`
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
		metrics, err := executeMeasuredProxyRuntimeRoundTrip(request.Scenario, request.Mode)
		if err != nil {
			return workerResult{}, err
		}
		return workerResult{
			ScenarioID: request.Scenario.ID,
			Metrics:    metrics,
		}, nil
	default:
		return workerResult{}, fmt.Errorf("unsupported worker action %q", request.Action)
	}
}

func TestPerfWorker_protocol_round_trip(t *testing.T) {
	t.Setenv("PERFBENCH_HELPER_TOKEN", "inherited-must-be-stripped")
	for _, mode := range []measurementMode{
		measurementModeLatency,
		measurementModeAllocationRetained,
		measurementModeSampledPeak,
	} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			request := workerRequest{Action: workerActionRoundTrip, Mode: mode, Scenario: scenarioCatalog()[0]}
			run, err := runPerfWorker(ctx, request, nil)
			if err != nil {
				t.Fatalf("run worker: %v", err)
			}
			if !run.Ready || !run.Exited || run.Sentinel == "" {
				t.Fatalf("worker lifecycle = %+v", run)
			}
			metrics := run.Result.Metrics
			if err := metrics.validateModeContract(); err != nil {
				t.Fatalf("mode contract: %v", err)
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
		})
	}
}
