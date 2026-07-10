package perfbench

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
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
	Action   workerAction `json:"action"`
	Scenario scenario     `json:"scenario"`
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
	ttfb         time.Duration
	total        time.Duration
	requestBytes int64
	response     []byte
	requestHash  string
	connections  connectionMetrics
}

func TestPerfWorkerHelperProcess(t *testing.T) {
	if os.Getenv("PERFBENCH_HELPER_PROCESS") != "1" {
		return
	}

	var request workerRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		t.Fatalf("decode worker request: %v", err)
	}
	result, err := executeWorker(request)
	if err != nil {
		result.Error = err.Error()
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		t.Fatalf("encode worker result: %v", err)
	}
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
		evidence, err := executeLoopbackRoundTrip(request.Scenario)
		if err != nil {
			return workerResult{}, err
		}
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		rssAnon, vmRSS, err := readProcMemory()
		if err != nil {
			return workerResult{}, err
		}
		return workerResult{
			ScenarioID: request.Scenario.ID,
			Metrics: workerMetrics{
				HeapAlloc: memory.HeapAlloc, HeapInuse: memory.HeapInuse,
				TotalAlloc: memory.TotalAlloc, Mallocs: memory.Mallocs, NumGC: memory.NumGC,
				RssAnon: rssAnon, VmRSS: vmRSS, Goroutines: runtime.NumGoroutine(),
				TTFB: evidence.ttfb, TotalDuration: evidence.total,
				RequestBytes: evidence.requestBytes, ResponseBytes: int64(len(evidence.response)),
				RequestBodySHA256:  evidence.requestHash,
				ResponseBodySHA256: sha256Hex(evidence.response),
				Connections:        evidence.connections,
			},
		}, nil
	default:
		return workerResult{}, fmt.Errorf("unsupported worker action %q", request.Action)
	}
}

func executeLoopbackRoundTrip(item scenario) (evidence roundTripEvidence, err error) {
	body, err := buildScenarioRequest(item)
	if err != nil {
		return roundTripEvidence{}, err
	}
	tracker := &connectionTracker{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		hasher := sha256.New()
		readBytes, copyErr := io.Copy(hasher, request.Body)
		if copyErr != nil {
			http.Error(w, copyErr.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, writeErr := fmt.Fprintf(w, `{"bytes":%d,"sha256":"%s"}`, readBytes,
			hex.EncodeToString(hasher.Sum(nil))); writeErr != nil {
			return
		}
	}))
	server.Config.ConnState = tracker.observe
	server.Start()
	defer func() {
		server.Close()
		evidence.connections = tracker.snapshot()
	}()

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
	return roundTripEvidence{
		ttfb: time.Duration(firstByte.Load()), total: time.Since(started),
		requestBytes: int64(len(body)), response: responseBody,
		requestHash: sha256Hex(body),
	}, nil
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func readProcMemory() (uint64, uint64, error) {
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return 0, 0, fmt.Errorf("open process status: %w", err)
	}
	defer file.Close()

	var rssAnon uint64
	var vmRSS uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		if fields[0] != "RssAnon:" && fields[0] != "VmRSS:" {
			continue
		}
		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return 0, 0, fmt.Errorf("parse %s: %w", fields[0], parseErr)
		}
		if fields[0] == "RssAnon:" {
			rssAnon = value * 1024
		} else {
			vmRSS = value * 1024
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, fmt.Errorf("scan process status: %w", err)
	}
	if rssAnon == 0 || vmRSS == 0 {
		return 0, 0, fmt.Errorf("process status omitted RSS fields: RssAnon=%d VmRSS=%d", rssAnon, vmRSS)
	}
	return rssAnon, vmRSS, nil
}

func TestPerfWorker_protocol_round_trip(t *testing.T) {
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := workerRequest{
		Action:   workerActionRoundTrip,
		Scenario: scenarioCatalog()[0],
	}

	// When
	run, err := runPerfWorker(ctx, request, nil)

	// Then
	if err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if !run.Exited {
		t.Fatal("worker process was not reaped")
	}
	metrics := run.Result.Metrics
	if metrics.HeapAlloc == 0 || metrics.HeapInuse == 0 || metrics.Mallocs == 0 {
		t.Fatalf("incomplete heap metrics: %+v", metrics)
	}
	if metrics.RssAnon == 0 || metrics.VmRSS == 0 || metrics.Goroutines == 0 {
		t.Fatalf("incomplete process metrics: %+v", metrics)
	}
	if metrics.TTFB <= 0 || metrics.TotalDuration <= 0 {
		t.Fatalf("incomplete latency metrics: %+v", metrics)
	}
	if metrics.RequestBytes <= request.Scenario.ImageBytes || metrics.ResponseBytes == 0 {
		t.Fatalf("incomplete byte metrics: %+v", metrics)
	}
	if len(metrics.RequestBodySHA256) != 64 || len(metrics.ResponseBodySHA256) != 64 {
		t.Fatalf("invalid body hashes: %+v", metrics)
	}
	if metrics.Connections.New == 0 || metrics.Connections.Closed == 0 {
		t.Fatalf("incomplete connection metrics: %+v", metrics.Connections)
	}
}
