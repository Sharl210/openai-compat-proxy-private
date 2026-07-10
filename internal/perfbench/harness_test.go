package perfbench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type workerRun struct {
	PID    int
	Exited bool
	Result workerResult
}

type connectionMetrics struct {
	New    int64 `json:"new"`
	Active int64 `json:"active"`
	Idle   int64 `json:"idle"`
	Closed int64 `json:"closed"`
}

type workerMetrics struct {
	HeapAlloc          uint64            `json:"heap_alloc"`
	HeapInuse          uint64            `json:"heap_inuse"`
	TotalAlloc         uint64            `json:"total_alloc"`
	Mallocs            uint64            `json:"mallocs"`
	NumGC              uint32            `json:"num_gc"`
	RssAnon            uint64            `json:"rss_anon"`
	VmRSS              uint64            `json:"vm_rss"`
	Goroutines         int               `json:"goroutines"`
	TTFB               time.Duration     `json:"ttfb_ns"`
	TotalDuration      time.Duration     `json:"total_duration_ns"`
	RequestBytes       int64             `json:"request_bytes"`
	ResponseBytes      int64             `json:"response_bytes"`
	RequestBodySHA256  string            `json:"request_body_sha256"`
	ResponseBodySHA256 string            `json:"response_body_sha256"`
	Connections        connectionMetrics `json:"connections"`
}

type workerResult struct {
	ScenarioID string        `json:"scenario_id"`
	Metrics    workerMetrics `json:"metrics"`
	Error      string        `json:"error,omitempty"`
}

func runPerfWorker(ctx context.Context, request workerRequest, started chan<- int) (workerRun, error) {
	executable, err := os.Executable()
	if err != nil {
		return workerRun{}, fmt.Errorf("locate test binary: %w", err)
	}
	command := exec.CommandContext(ctx, executable, "-test.run=^TestPerfWorkerHelperProcess$")
	command.Env = append(os.Environ(),
		"PERFBENCH_HELPER_PROCESS=1",
		"GOGC=100",
		"GOMAXPROCS=4",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	stdin, err := command.StdinPipe()
	if err != nil {
		return workerRun{}, fmt.Errorf("open worker stdin: %w", err)
	}
	if err := command.Start(); err != nil {
		if closeErr := stdin.Close(); closeErr != nil {
			return workerRun{}, errors.Join(err, closeErr)
		}
		return workerRun{}, fmt.Errorf("start worker: %w", err)
	}
	run := workerRun{PID: command.Process.Pid}
	if started != nil {
		select {
		case started <- run.PID:
		case <-ctx.Done():
		}
	}

	writeErr := json.NewEncoder(stdin).Encode(request)
	stdinOpen := true
	var closeErr error
	if request.Action != workerActionBlock {
		closeErr = stdin.Close()
		stdinOpen = false
	}
	if writeErr != nil {
		killErr := command.Process.Kill()
		waitErr := command.Wait()
		if stdinOpen {
			closeErr = stdin.Close()
		}
		run.Exited = command.ProcessState != nil
		return run, fmt.Errorf("send worker request: %w", errors.Join(writeErr, killErr, waitErr, closeErr))
	}

	waitErr := command.Wait()
	if stdinOpen {
		closeErr = stdin.Close()
	}
	run.Exited = command.ProcessState != nil
	if ctxErr := ctx.Err(); ctxErr != nil {
		return run, fmt.Errorf("worker context: %w", ctxErr)
	}
	if waitErr != nil {
		return run, fmt.Errorf("wait for worker: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	if closeErr != nil {
		return run, fmt.Errorf("close worker stdin: %w", closeErr)
	}

	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&run.Result); err != nil {
		return run, fmt.Errorf("decode worker result: %w: %s", err, strings.TrimSpace(stdout.String()))
	}
	if run.Result.Error != "" {
		return run, fmt.Errorf("worker failed: %s", run.Result.Error)
	}
	return run, nil
}

func TestPerfWorker_child_cleanup_on_failure(t *testing.T) {
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := workerRequest{Action: workerActionFail, Scenario: scenarioCatalog()[0]}

	// When
	run, err := runPerfWorker(ctx, request, nil)

	// Then
	if err == nil {
		t.Fatal("worker failure returned nil error")
	}
	if run.PID <= 0 || !run.Exited {
		t.Fatalf("failed worker was not reaped: %+v", run)
	}
}

func TestPerfWorker_child_cleanup_on_timeout(t *testing.T) {
	// Given
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	request := workerRequest{Action: workerActionBlock, Scenario: scenarioCatalog()[0]}

	// When
	run, err := runPerfWorker(ctx, request, nil)

	// Then
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("worker error = %v, want deadline exceeded", err)
	}
	if run.PID <= 0 || !run.Exited {
		t.Fatalf("timed-out worker was not reaped: %+v", run)
	}
}

func TestPerfWorker_child_cleanup_on_cancellation(t *testing.T) {
	// Given
	deadline, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(deadline)
	started := make(chan int, 1)
	type outcome struct {
		run workerRun
		err error
	}
	finished := make(chan outcome, 1)

	// When
	go func() {
		run, err := runPerfWorker(ctx, workerRequest{
			Action:   workerActionBlock,
			Scenario: scenarioCatalog()[0],
		}, started)
		finished <- outcome{run: run, err: err}
	}()
	select {
	case pid := <-started:
		if pid <= 0 {
			t.Fatalf("worker PID = %d", pid)
		}
		cancel()
	case <-deadline.Done():
		t.Fatal("worker did not start before deadline")
	}

	// Then
	select {
	case result := <-finished:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("worker error = %v, want context canceled", result.err)
		}
		if result.run.PID <= 0 || !result.run.Exited {
			t.Fatalf("canceled worker was not reaped: %+v", result.run)
		}
	case <-deadline.Done():
		t.Fatal("canceled worker was not reaped before deadline")
	}
}
