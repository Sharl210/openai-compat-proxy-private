package perfbench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/httpapi"
	"openai-compat-proxy/internal/logging"
)

func TestPerformanceProxyRuntimeRoundTrip_collects_proxy_evidence_for_each_measurement_mode(t *testing.T) {
	for _, mode := range []measurementMode{
		measurementModeLatency,
		measurementModeAllocationRetained,
		measurementModeSampledPeak,
	} {
		t.Run(string(mode), func(t *testing.T) {
			// Given
			item := scenarioCatalog()[0]

			// When
			metrics, err := executeMeasuredProxyRuntimeRoundTrip(item, mode)

			// Then
			if err != nil {
				t.Fatalf("measure proxy runtime: %v", err)
			}
			if err := metrics.validateModeContract(); err != nil {
				t.Fatalf("mode contract: %v", err)
			}
			if metrics.ObservedRequestBytes <= item.ImageBytes || metrics.ResponseBytes == 0 {
				t.Fatalf("incomplete proxy evidence: %+v", metrics)
			}
			if len(metrics.ObservedRequestBodySHA256) != 64 || len(metrics.ResponseBodySHA256) != 64 {
				t.Fatalf("invalid proxy body hashes: %+v", metrics)
			}
			if mode == measurementModeLatency && (metrics.Latency == nil || metrics.Latency.TTFB <= 0) {
				t.Fatalf("missing real TTFB: %+v", metrics.Latency)
			}
		})
	}
}

func executeMeasuredProxyRuntimeRoundTrip(item scenario, mode measurementMode) (metrics workerMetrics, err error) {
	// Given
	tempRoot, err := os.MkdirTemp("", "perfbench-proxy-runtime-")
	if err != nil {
		return workerMetrics{}, fmt.Errorf("create proxy runtime temp root: %w", err)
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(tempRoot))
	}()

	fake := newSemanticFakeUpstream(item)
	defer fake.close()
	cfg, err := semanticScenarioConfig(item, fake.url(), tempRoot)
	if err != nil {
		return workerMetrics{}, err
	}
	closeLogger, err := logging.Init(cfg, io.Discard)
	if err != nil {
		return workerMetrics{}, fmt.Errorf("initialize proxy runtime logger: %w", err)
	}
	defer func() {
		_, disableErr := logging.Init(config.Config{}, io.Discard)
		err = errors.Join(err, closeLogger(), disableErr)
	}()

	body, err := semanticScenarioRequestBody(item)
	if err != nil {
		return workerMetrics{}, err
	}
	tracker := &connectionTracker{}
	proxy := httptest.NewUnstartedServer(httpapi.NewServer(cfg))
	proxy.Config.ConnState = tracker.observe
	proxy.Start()
	proxyClosed := false
	defer func() {
		if !proxyClosed {
			proxy.Close()
		}
	}()

	// When
	return measureOperation(mode, func() (roundTripEvidence, error) {
		evidence, operationErr := performProxyRuntimeRoundTrip(proxy, item, body)
		proxy.Close()
		proxyClosed = true
		if operationErr != nil {
			return roundTripEvidence{}, operationErr
		}
		captures := fake.capturedRequests()
		if len(captures) != 1 {
			return roundTripEvidence{}, fmt.Errorf("upstream captures = %d, want 1", len(captures))
		}
		evidence.observedRequestBytes = int64(len(captures[0].Body))
		evidence.observedRequestHash = sha256Hex(captures[0].Body)
		evidence.connections = tracker.snapshot()
		return evidence, nil
	}, defaultMeasurementHooks())
}

func performProxyRuntimeRoundTrip(proxy *httptest.Server, item scenario, body []byte) (roundTripEvidence, error) {
	return performProxyRuntimeRoundTripURL(proxy.Client(), proxy.URL, item, body)
}

func performProxyRuntimeRoundTripURL(client *http.Client, baseURL string, item scenario, body []byte) (roundTripEvidence, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		baseURL+semanticDownstreamPath(item.Downstream),
		bytes.NewReader(body),
	)
	if err != nil {
		return roundTripEvidence{}, fmt.Errorf("create proxy runtime request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer perf-proxy-secret")
	request.Header.Set("Content-Type", "application/json")
	if item.Downstream == downstreamMessages {
		request.Header.Set("Anthropic-Version", "2023-06-01")
	}
	started := time.Now()
	var firstByte atomic.Int64
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() {
		firstByte.CompareAndSwap(0, time.Since(started).Nanoseconds())
	}}
	response, err := client.Do(request.WithContext(httptrace.WithClientTrace(request.Context(), trace)))
	if err != nil {
		return roundTripEvidence{}, fmt.Errorf("perform proxy runtime request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return roundTripEvidence{}, fmt.Errorf("read proxy runtime response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return roundTripEvidence{}, fmt.Errorf("proxy runtime status %d: %s", response.StatusCode, responseBody)
	}
	return roundTripEvidence{
		ttfb: time.Duration(firstByte.Load()), total: time.Since(started), response: responseBody,
	}, nil
}
