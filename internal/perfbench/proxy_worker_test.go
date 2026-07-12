package perfbench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

const workerReadyTimeout = 10 * time.Second

type perfWorkerProcess struct {
	command   *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    *bytes.Buffer
	run       workerRun
	stdinOpen bool
}

func startPerfWorkerProcess(ctx context.Context, request workerRequest, started chan<- int) (_ *perfWorkerProcess, err error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate test binary: %w", err)
	}
	sentinel, err := newHelperSentinel()
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, executable,
		"-test.run=^TestPerfWorkerHelperProcess$",
		"-perfbench-helper-sentinel="+sentinel,
	)
	command.Env = workerEnvironment(os.Environ(), sentinel)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open worker stdout: %w", err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open worker stdin: %w", err)
	}
	process := &perfWorkerProcess{
		command:   command,
		stdin:     stdin,
		stdout:    stdout,
		stderr:    &stderr,
		stdinOpen: true,
	}
	defer func() {
		if process == nil && err != nil {
			_ = stdin.Close()
		}
	}()
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start worker: %w", err)
	}
	process.run = workerRun{PID: command.Process.Pid, Sentinel: sentinel}
	if err := json.NewEncoder(stdin).Encode(request); err != nil {
		return nil, process.reap(fmt.Errorf("send worker request: %w", err))
	}
	type readyOutcome struct {
		ready workerReady
		err   error
	}
	readyResult := make(chan readyOutcome, 1)
	go func() {
		if request.Action == workerActionProxy {
			ready, readyErr := decodeWorkerReadyFrame(stdout)
			readyResult <- readyOutcome{ready: ready, err: readyErr}
			return
		}
		readyResult <- readyOutcome{err: readReadySignal(stdout)}
	}()
	readyTimer := time.NewTimer(workerReadyTimeout)
	defer readyTimer.Stop()
	select {
	case outcome := <-readyResult:
		if outcome.err != nil {
			return nil, process.reap(outcome.err)
		}
		if request.Action == workerActionProxy {
			if outcome.ready.PID != process.run.PID {
				return nil, process.reap(fmt.Errorf("proxy worker ready PID = %d, want %d", outcome.ready.PID, process.run.PID))
			}
			process.run.BaseURL = outcome.ready.BaseURL
		}
	case <-ctx.Done():
		return nil, process.reap(fmt.Errorf("worker readiness context: %w", ctx.Err()))
	case <-readyTimer.C:
		return nil, process.reap(errors.New("worker readiness timeout"))
	}
	process.run.Ready = true
	if started != nil {
		select {
		case started <- process.run.PID:
		case <-ctx.Done():
		}
	}
	return process, nil
}

func (process *perfWorkerProcess) reap(cause error) error {
	if process.stdinOpen {
		_ = process.stdin.Close()
		process.stdinOpen = false
	}
	_, reapErr := reapFailedWorker(process.command, process.run, cause)
	return reapErr
}

func (process *perfWorkerProcess) startOperation() error {
	_, err := io.WriteString(process.stdin, operationStartSignal)
	return err
}

func (process *perfWorkerProcess) requestHeapSnapshot() (workerHeapSnapshot, error) {
	if !process.stdinOpen {
		return workerHeapSnapshot{}, errors.New("proxy worker stdin is closed")
	}
	if _, err := io.WriteString(process.stdin, heapSnapshotSignal); err != nil {
		return workerHeapSnapshot{}, fmt.Errorf("request proxy worker heap snapshot: %w", err)
	}
	snapshot, err := decodeWorkerHeapSnapshotFrame(process.stdout)
	if err != nil {
		return workerHeapSnapshot{}, fmt.Errorf("read proxy worker heap snapshot: %w", err)
	}
	return snapshot, nil
}

func (process *perfWorkerProcess) stopProxy(ctx context.Context) (run workerRun, err error) {
	finished := make(chan error, 1)
	go func() {
		finished <- process.finishProxy()
	}()
	select {
	case err := <-finished:
		return process.run, err
	case <-ctx.Done():
		killErr := process.command.Process.Kill()
		finishErr := <-finished
		return process.run, errors.Join(fmt.Errorf("stop proxy worker: %w", ctx.Err()), killErr, finishErr)
	}
}

func (process *perfWorkerProcess) finishProxy() error {
	if _, err := io.WriteString(process.stdin, proxyShutdownSignal); err != nil {
		return fmt.Errorf("request proxy worker shutdown: %w", err)
	}
	boundaryErr := readExactSignal(process.stdout, operationStopSignal)
	if boundaryErr == nil {
		if _, err := io.WriteString(process.stdin, operationStopAck); err != nil {
			boundaryErr = fmt.Errorf("acknowledge proxy worker stop: %w", err)
		}
	}
	stdinCloseErr := process.stdin.Close()
	process.stdinOpen = false
	if errors.Is(stdinCloseErr, os.ErrClosed) {
		stdinCloseErr = nil
	}
	result, frameErr := decodeWorkerResultFrame(process.stdout)
	waitErr := process.command.Wait()
	process.run.Exited = process.command.ProcessState != nil
	if stdinCloseErr != nil {
		return fmt.Errorf("close proxy worker stdin: %w", stdinCloseErr)
	}
	if waitErr != nil {
		return fmt.Errorf("wait for proxy worker: %w: %s", waitErr, strings.TrimSpace(process.stderr.String()))
	}
	if boundaryErr != nil {
		return boundaryErr
	}
	if frameErr != nil {
		return frameErr
	}
	if result.Error != "" {
		return fmt.Errorf("proxy worker failed: %s", result.Error)
	}
	if result.Kind != workerResultKindProxy {
		return fmt.Errorf("proxy worker result kind = %q", result.Kind)
	}
	process.run.Result = result
	return nil
}
