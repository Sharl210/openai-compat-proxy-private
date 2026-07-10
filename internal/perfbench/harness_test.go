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
	"time"
)

type workerRun struct {
	PID      int
	Exited   bool
	Ready    bool
	Sentinel string
	Result   workerResult
}

type workerResult struct {
	ScenarioID string        `json:"scenario_id"`
	Metrics    workerMetrics `json:"metrics"`
	Error      string        `json:"error,omitempty"`
}

func runPerfWorker(ctx context.Context, request workerRequest, started chan<- int) (run workerRun, err error) {
	executable, err := os.Executable()
	if err != nil {
		return workerRun{}, fmt.Errorf("locate test binary: %w", err)
	}
	sentinel, err := newHelperSentinel()
	if err != nil {
		return workerRun{}, err
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
		return workerRun{}, fmt.Errorf("open worker stdout: %w", err)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return workerRun{}, fmt.Errorf("open worker stdin: %w", err)
	}
	stdinOpen := true
	defer func() {
		if stdinOpen {
			err = errors.Join(err, stdin.Close())
		}
	}()
	if err := command.Start(); err != nil {
		return workerRun{}, fmt.Errorf("start worker: %w", err)
	}
	run = workerRun{PID: command.Process.Pid, Sentinel: sentinel}
	if err := json.NewEncoder(stdin).Encode(request); err != nil {
		return reapFailedWorker(command, run, fmt.Errorf("send worker request: %w", err))
	}
	if request.Action != workerActionBlock {
		if err := stdin.Close(); err != nil {
			return reapFailedWorker(command, run, fmt.Errorf("close worker stdin: %w", err))
		}
		stdinOpen = false
	}
	if err := readReadySignal(stdout); err != nil {
		return reapFailedWorker(command, run, err)
	}
	run.Ready = true
	if started != nil {
		select {
		case started <- run.PID:
		case <-ctx.Done():
		}
	}

	timeoutResult := make(chan error, 1)
	var timeout *time.Timer
	if request.Timeout > 0 {
		timeout = time.AfterFunc(request.Timeout, func() {
			timeoutResult <- command.Process.Kill()
		})
	}
	result, frameErr := decodeWorkerResultFrame(stdout)
	var stdinCloseErr error
	if stdinOpen {
		stdinCloseErr = stdin.Close()
		stdinOpen = false
		if errors.Is(stdinCloseErr, os.ErrClosed) {
			stdinCloseErr = nil
		}
	}
	waitErr := command.Wait()
	run.Exited = command.ProcessState != nil
	if timeout != nil && !timeout.Stop() {
		if killErr := <-timeoutResult; killErr == nil {
			return run, fmt.Errorf("worker execution timeout: %w", context.DeadlineExceeded)
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return run, fmt.Errorf("worker context: %w", ctxErr)
	}
	if stdinCloseErr != nil {
		return run, fmt.Errorf("close blocked worker stdin: %w", stdinCloseErr)
	}
	if waitErr != nil {
		return run, fmt.Errorf("wait for worker: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	if frameErr != nil {
		return run, frameErr
	}
	run.Result = result
	if result.Error != "" {
		return run, fmt.Errorf("worker failed: %s", result.Error)
	}
	return run, nil
}

func reapFailedWorker(command *exec.Cmd, run workerRun, cause error) (workerRun, error) {
	killErr := command.Process.Kill()
	waitErr := command.Wait()
	run.Exited = command.ProcessState != nil
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	return run, errors.Join(cause, killErr, waitErr)
}
