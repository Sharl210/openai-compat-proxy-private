package perfbench

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type capacityRequestResult struct {
	evidence roundTripEvidence
	err      error
}

func runCapacityWorkload(ctx context.Context, workload capacityWorkload, _ int) (measurement capacityMeasurement) {
	measurement.RequestedRequests = workload.Concurrency
	gate := newCapacityUpstreamGate(workload.Concurrency)
	fixture, err := newCapacityProxyFixture(workload.Scenario, gate)
	if err != nil {
		measurement.Failure = &capacityReportFailure{Kind: "fixture", Message: err.Error()}
		return measurement
	}
	defer func() {
		if closeErr := fixture.close(); closeErr != nil && measurement.Failure == nil {
			measurement.Failure = &capacityReportFailure{Kind: "fixture_close", Message: closeErr.Error()}
		}
	}()
	defer gate.release()

	body, err := semanticScenarioRequestBody(workload.Scenario)
	if err != nil {
		measurement.Failure = &capacityReportFailure{Kind: "request", Message: err.Error()}
		return measurement
	}
	clients, closeClients, err := newCapacityClients(workload.Traffic, workload.Concurrency)
	if err != nil {
		measurement.Failure = &capacityReportFailure{Kind: "traffic", Message: err.Error()}
		return measurement
	}
	defer closeClients()

	resources, cpuCheckpoint := beginCapacityChildResources(fixture.proxyPID)
	results := make(chan capacityRequestResult, workload.Concurrency)
	started := time.Now()
	for _, client := range clients {
		go func(client *http.Client) {
			evidence, requestErr := performProxyRuntimeRoundTripURL(client, fixture.proxyURL, workload.Scenario, body)
			results <- capacityRequestResult{evidence: evidence, err: requestErr}
		}(client)
	}
	if err := gate.waitForPeak(ctx); err != nil {
		measurement.Failure = &capacityReportFailure{Kind: "gate", Message: fmt.Sprintf("wait for full concurrency: %v", err)}
	} else {
		measurement.PeakInFlight = gate.peakInFlight()
		if measurement.PeakInFlight != workload.Concurrency {
			measurement.Failure = &capacityReportFailure{Kind: "gate", Message: fmt.Sprintf("peak in-flight = %d, want %d", measurement.PeakInFlight, workload.Concurrency)}
		} else {
			captureCapacityChildResourcesAtFullGate(&resources, fixture)
		}
	}
	gate.release()
	for range clients {
		result := <-results
		if result.err != nil {
			measurement.ErrorCount++
			continue
		}
		measurement.SuccessfulRequests++
		measurement.TTFB = append(measurement.TTFB, result.evidence.ttfb)
		measurement.TotalLatency = append(measurement.TotalLatency, result.evidence.total)
	}
	measurement.Elapsed = time.Since(started)
	measurement.Child = resources
	finishCapacityChildCPU(&measurement.Child, cpuCheckpoint)
	if measurement.PeakInFlight == 0 {
		measurement.PeakInFlight = gate.peakInFlight()
	}
	return normalizeCapacityMeasurement(measurement)
}

func newCapacityClients(traffic capacityTraffic, concurrency int) ([]*http.Client, func(), error) {
	if concurrency < 1 {
		return nil, nil, fmt.Errorf("capacity concurrency must be positive: %d", concurrency)
	}
	switch traffic {
	case capacityTrafficManyUsers:
		clients := make([]*http.Client, 0, concurrency)
		for range concurrency {
			clients = append(clients, &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()})
		}
		return clients, func() {
			for _, client := range clients {
				client.CloseIdleConnections()
			}
		}, nil
	case capacityTrafficOneUserBurst:
		client := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
		clients := make([]*http.Client, concurrency)
		for index := range clients {
			clients[index] = client
		}
		return clients, client.CloseIdleConnections, nil
	default:
		return nil, nil, fmt.Errorf("unsupported capacity traffic %q", traffic)
	}
}
