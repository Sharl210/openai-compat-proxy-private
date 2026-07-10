package perfbench

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPerfWorker_child_cleanup_on_failure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run, err := runPerfWorker(ctx, workerRequest{Action: workerActionFail, Scenario: scenarioCatalog()[0]}, nil)
	if err == nil || run.PID <= 0 || !run.Ready || !run.Exited {
		t.Fatalf("failed worker cleanup: run=%+v err=%v", run, err)
	}
}

func TestPerfWorker_child_cleanup_on_timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := workerRequest{Action: workerActionBlock, Scenario: scenarioCatalog()[0], Timeout: 50 * time.Millisecond}
	run, err := runPerfWorker(ctx, request, nil)
	if !errors.Is(err, context.DeadlineExceeded) || run.PID <= 0 || !run.Ready || !run.Exited {
		t.Fatalf("timed-out worker cleanup: run=%+v err=%v", run, err)
	}
}

func TestPerfWorker_child_cleanup_on_cancellation(t *testing.T) {
	deadline, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(deadline)
	started := make(chan int, 1)
	type outcome struct {
		run workerRun
		err error
	}
	finished := make(chan outcome, 1)
	go func() {
		run, err := runPerfWorker(ctx, workerRequest{Action: workerActionBlock, Scenario: scenarioCatalog()[0]}, started)
		finished <- outcome{run: run, err: err}
	}()
	select {
	case <-started:
		cancel()
	case <-deadline.Done():
		t.Fatal("worker did not become ready")
	}
	select {
	case result := <-finished:
		if !errors.Is(result.err, context.Canceled) || !result.run.Ready || !result.run.Exited {
			t.Fatalf("canceled worker cleanup: run=%+v err=%v", result.run, result.err)
		}
	case <-deadline.Done():
		t.Fatal("canceled worker was not reaped")
	}
}
