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
	var processSampler processPeakSampler
	var processPeak sampledProcessPeak
	stopProcessSampler := func() error {
		if processSampler == nil {
			return nil
		}
		peak, stopErr := processSampler.Stop()
		processSampler = nil
		processPeak = peak
		return stopErr
	}
	if request.Action == workerActionRoundTrip {
		if request.Mode == measurementModeSampledPeak {
			processSampler, err = newParentProcessSampler(run.PID)
			if err != nil {
				return reapFailedWorker(command, run, fmt.Errorf("start parent process sampler: %w", err))
			}
		}
		if _, err := io.WriteString(stdin, operationStartSignal); err != nil {
			sampleErr := stopProcessSampler()
			return reapFailedWorker(command, run, errors.Join(fmt.Errorf("start worker operation: %w", err), sampleErr))
		}
	}
	if request.Action != workerActionBlock && request.Action != workerActionRoundTrip {
		if err := stdin.Close(); err != nil {
			sampleErr := stopProcessSampler()
			return reapFailedWorker(command, run, errors.Join(fmt.Errorf("close worker stdin: %w", err), sampleErr))
		}
		stdinOpen = false
	}

	timeoutResult := make(chan error, 1)
	var timeout *time.Timer
	if request.Timeout > 0 {
		timeout = time.AfterFunc(request.Timeout, func() {
			timeoutResult <- command.Process.Kill()
		})
	}
	var boundaryErr error
	if request.Action == workerActionRoundTrip {
		boundaryErr = readExactSignal(stdout, operationStopSignal)
		boundaryErr = errors.Join(boundaryErr, stopProcessSampler())
		if _, err := io.WriteString(stdin, operationStopAck); err != nil {
			boundaryErr = errors.Join(boundaryErr, fmt.Errorf("acknowledge worker operation stop: %w", err))
		}
		if err := stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			boundaryErr = errors.Join(boundaryErr, fmt.Errorf("close worker stdin after stop: %w", err))
		}
		stdinOpen = false
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
	if boundaryErr != nil {
		return run, boundaryErr
	}
	run.Result = result
	if err := finalizeWorkerResult(request, &run.Result, processPeak); err != nil {
		return run, err
	}
	return run, nil
}

func finalizeWorkerResult(request workerRequest, result *workerResult, processPeak sampledProcessPeak) error {
	if result.Error != "" {
		return fmt.Errorf("worker failed: %s", result.Error)
	}
	if request.Mode != measurementModeSampledPeak {
		return nil
	}
	peak := result.Metrics.SampledPeakDuringOperation
	if peak == nil {
		return errors.New("sampled-peak worker returned no sampled peak metrics")
	}
	peak.ParentProcess = processPeak.process
	peak.ParentProcessSupported = processPeak.supported
	peak.ParentSampleInterval = processPeak.interval
	peak.ParentSampleCount = processPeak.count
	return nil
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
