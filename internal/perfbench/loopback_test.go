package perfbench

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync/atomic"
	"time"
)

type connectionMetrics struct {
	New    int64 `json:"new"`
	Active int64 `json:"active"`
	Idle   int64 `json:"idle"`
	Closed int64 `json:"closed"`
}

type connectionTracker struct {
	newCount    atomic.Int64
	activeCount atomic.Int64
	idleCount   atomic.Int64
	closedCount atomic.Int64
}

func (tracker *connectionTracker) observe(_ net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		tracker.newCount.Add(1)
	case http.StateActive:
		tracker.activeCount.Add(1)
	case http.StateIdle:
		tracker.idleCount.Add(1)
	case http.StateHijacked, http.StateClosed:
		tracker.closedCount.Add(1)
	}
}

func (tracker *connectionTracker) snapshot() connectionMetrics {
	return connectionMetrics{
		New: tracker.newCount.Load(), Active: tracker.activeCount.Load(),
		Idle: tracker.idleCount.Load(), Closed: tracker.closedCount.Load(),
	}
}

type roundTripEvidence struct {
	ttfb                 time.Duration
	total                time.Duration
	observedRequestBytes int64
	observedRequestHash  string
	response             []byte
	connections          connectionMetrics
}

type memoryMeasurements struct {
	idle     memorySnapshot
	retained memorySnapshot
	peak     memorySnapshot
}

func executeMeasuredRoundTrip(item scenario) (roundTripEvidence, memoryMeasurements, error) {
	body, err := buildScenarioRequest(item)
	if err != nil {
		return roundTripEvidence{}, memoryMeasurements{}, err
	}
	tracker := &connectionTracker{}
	server := newObservedLoopbackServer(tracker)
	server.Start()
	serverClosed := false
	defer func() {
		if !serverClosed {
			server.Close()
		}
	}()
	idle, err := captureMemorySnapshot()
	if err != nil {
		return roundTripEvidence{}, memoryMeasurements{}, err
	}
	ticker := time.NewTicker(time.Millisecond)
	sampler, err := startPeakSampler(captureMemorySnapshot, ticker.C)
	if err != nil {
		ticker.Stop()
		return roundTripEvidence{}, memoryMeasurements{}, err
	}
	evidence, operationErr := performLoopbackRoundTrip(server, body)
	server.Close()
	serverClosed = true
	ticker.Stop()
	peak, sampleErr := sampler.Stop()
	retained, retainedErr := captureMemorySnapshot()
	evidence.connections = tracker.snapshot()
	return evidence, memoryMeasurements{idle: idle, retained: retained, peak: peak},
		errors.Join(operationErr, sampleErr, retainedErr)
}

func newObservedLoopbackServer(tracker *connectionTracker) *httptest.Server {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		hasher := sha256.New()
		readBytes, err := io.Copy(hasher, request.Body)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprintf(writer, `{"bytes":%d,"sha256":"%s"}`,
			readBytes, hex.EncodeToString(hasher.Sum(nil))); err != nil {
			return
		}
	}))
	server.Config.ConnState = tracker.observe
	return server
}

func performLoopbackRoundTrip(server *httptest.Server, body []byte) (roundTripEvidence, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/round-trip", bytes.NewReader(body))
	if err != nil {
		return roundTripEvidence{}, fmt.Errorf("create loopback request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	started := time.Now()
	var firstByte atomic.Int64
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() {
		firstByte.CompareAndSwap(0, time.Since(started).Nanoseconds())
	}}
	response, err := server.Client().Do(request.WithContext(httptrace.WithClientTrace(request.Context(), trace)))
	if err != nil {
		return roundTripEvidence{}, fmt.Errorf("perform loopback request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return roundTripEvidence{}, fmt.Errorf("read loopback response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return roundTripEvidence{}, fmt.Errorf("loopback status %d: %s", response.StatusCode, responseBody)
	}
	expectedHash := sha256Hex(body)
	observed, err := verifyLoopbackObservation(responseBody, int64(len(body)), expectedHash)
	if err != nil {
		return roundTripEvidence{}, err
	}
	return roundTripEvidence{
		ttfb: time.Duration(firstByte.Load()), total: time.Since(started),
		observedRequestBytes: observed.Bytes, observedRequestHash: observed.SHA256,
		response: responseBody,
	}, nil
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
