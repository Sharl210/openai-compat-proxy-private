package perfbench

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"testing"

	"openai-compat-proxy/internal/diagnostics"
	"openai-compat-proxy/internal/httpapi"
	"openai-compat-proxy/internal/logging"
)

const (
	readySignal          = "PERFBENCH_READY_V2\n"
	operationStartSignal = "PERFBENCH_START_V2\n"
	operationStopSignal  = "PERFBENCH_STOP_V2\n"
	operationStopAck     = "PERFBENCH_STOP_ACK_V2\n"
	proxyShutdownSignal  = "PERFBENCH_PROXY_SHUTDOWN_V2\n"
	maxProxyControlBytes = 64
)

var helperArgSentinel = flag.String("perfbench-helper-sentinel", "", "internal perfbench helper sentinel")

func TestPerfWorkerHelperProcess(t *testing.T) {
	if !helperActivated(*helperArgSentinel, os.Getenv(helperTokenEnvironment)) {
		return
	}

	var request workerRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		t.Fatalf("decode worker request: %v", err)
	}
	if request.Action == workerActionProxy {
		result, err := executeProxyServerWorker(request)
		if err != nil {
			readyFrame, frameErr := encodeWorkerReadyFrame(workerReady{Error: err.Error()})
			if frameErr != nil {
				t.Fatalf("encode proxy worker startup error: %v", frameErr)
			}
			if _, frameErr := os.Stdout.Write(readyFrame); frameErr != nil {
				t.Fatalf("write proxy worker startup error: %v", frameErr)
			}
			os.Exit(0)
		}
		frame, err := encodeWorkerResultFrame(result)
		if err != nil {
			t.Fatalf("encode proxy worker result: %v", err)
		}
		if _, err := os.Stdout.Write(frame); err != nil {
			t.Fatalf("write proxy worker result frame: %v", err)
		}
		os.Exit(0)
	}
	if _, err := io.WriteString(os.Stdout, readySignal); err != nil {
		t.Fatalf("write ready signal: %v", err)
	}
	if request.Action == workerActionRoundTrip {
		if err := readExactSignal(os.Stdin, operationStartSignal); err != nil {
			t.Fatalf("read operation start signal: %v", err)
		}
	}

	result, err := executeWorker(request)
	if err != nil {
		result.Error = err.Error()
	}
	if request.Action == workerActionRoundTrip {
		if _, err := io.WriteString(os.Stdout, operationStopSignal); err != nil {
			t.Fatalf("write operation stop signal: %v", err)
		}
		if err := readExactSignal(os.Stdin, operationStopAck); err != nil {
			t.Fatalf("read operation stop acknowledgement: %v", err)
		}
	}
	frame, err := encodeWorkerResultFrame(result)
	if err != nil {
		t.Fatalf("encode worker result frame: %v", err)
	}
	if _, err := os.Stdout.Write(frame); err != nil {
		t.Fatalf("write worker result frame: %v", err)
	}
	os.Exit(0)
}

func executeProxyServerWorker(request workerRequest) (workerResult, error) {
	if request.ProxyConfig == nil {
		return workerResult{}, fmt.Errorf("proxy worker config is missing")
	}
	if request.UpstreamURL == "" {
		return workerResult{}, fmt.Errorf("proxy worker upstream URL is missing")
	}
	cfg := *request.ProxyConfig
	if len(cfg.Providers) != 1 {
		return workerResult{}, fmt.Errorf("proxy worker provider count = %d, want 1", len(cfg.Providers))
	}
	cfg.Providers[0].UpstreamBaseURL = request.UpstreamURL
	closeLogger, err := logging.Init(cfg, io.Discard)
	if err != nil {
		return workerResult{}, fmt.Errorf("initialize proxy worker logger: %w", err)
	}
	defer closeLogger()

	proxy := httptest.NewUnstartedServer(httpapi.NewServer(cfg))
	proxy.Start()
	proxyClosed := false
	defer func() {
		if !proxyClosed {
			proxy.Close()
		}
	}()
	readyFrame, err := encodeWorkerReadyFrame(workerReady{BaseURL: proxy.URL, PID: os.Getpid()})
	if err != nil {
		return workerResult{}, err
	}
	if _, err := os.Stdout.Write(readyFrame); err != nil {
		return workerResult{}, fmt.Errorf("write proxy worker ready frame: %w", err)
	}
	if err := readExactSignal(os.Stdin, operationStartSignal); err != nil {
		return workerResult{}, err
	}
	control := bufio.NewReaderSize(os.Stdin, maxProxyControlBytes)
	for {
		signal, err := readProxyControlSignal(control)
		if err != nil {
			return workerResult{}, err
		}
		switch signal {
		case heapSnapshotSignal:
			runtimeMemory := diagnostics.Snapshot()
			frame, err := encodeWorkerHeapSnapshotFrame(workerHeapSnapshot{HeapAlloc: runtimeMemory.HeapAlloc, HeapInuse: runtimeMemory.HeapInuse})
			if err != nil {
				return workerResult{}, err
			}
			if _, err := os.Stdout.Write(frame); err != nil {
				return workerResult{}, fmt.Errorf("write proxy worker heap snapshot: %w", err)
			}
		case proxyShutdownSignal:
			proxy.Close()
			proxyClosed = true
			if _, err := io.WriteString(os.Stdout, operationStopSignal); err != nil {
				return workerResult{}, fmt.Errorf("write proxy worker stop signal: %w", err)
			}
			if err := readExactSignal(control, operationStopAck); err != nil {
				return workerResult{}, err
			}
			return workerResult{Kind: workerResultKindProxy, ScenarioID: request.Scenario.ID}, nil
		}
	}
}

func readProxyControlSignal(reader *bufio.Reader) (string, error) {
	signal, err := reader.ReadSlice('\n')
	if err != nil {
		return "", fmt.Errorf("read proxy worker control signal: %w", err)
	}
	if len(signal) > maxProxyControlBytes {
		return "", fmt.Errorf("proxy worker control signal exceeds %d bytes", maxProxyControlBytes)
	}
	value := string(signal)
	if value != heapSnapshotSignal && value != proxyShutdownSignal {
		return "", fmt.Errorf("invalid proxy worker control signal %q", value)
	}
	return value, nil
}

func readReadySignal(reader io.Reader) error {
	return readExactSignal(reader, readySignal)
}

func readExactSignal(reader io.Reader, expected string) error {
	actual := make([]byte, len(expected))
	_, err := io.ReadFull(reader, actual)
	if err != nil {
		return fmt.Errorf("read signal %q: %w", expected, err)
	}
	if string(actual) != expected {
		return fmt.Errorf("invalid signal %q, want %q", actual, expected)
	}
	return nil
}
