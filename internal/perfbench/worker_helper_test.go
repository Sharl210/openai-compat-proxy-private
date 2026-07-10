package perfbench

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"testing"
)

const (
	readySignal          = "PERFBENCH_READY_V2\n"
	operationStartSignal = "PERFBENCH_START_V2\n"
	operationStopSignal  = "PERFBENCH_STOP_V2\n"
	operationStopAck     = "PERFBENCH_STOP_ACK_V2\n"
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
