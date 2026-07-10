package perfbench

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"testing"
)

const readySignal = "PERFBENCH_READY_V1\n"

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

	result, err := executeWorker(request)
	if err != nil {
		result.Error = err.Error()
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
	ready := make([]byte, len(readySignal))
	_, err := io.ReadFull(reader, ready)
	if err != nil {
		return fmt.Errorf("read helper ready signal: %w", err)
	}
	if string(ready) != readySignal {
		return fmt.Errorf("invalid helper ready signal %q", ready)
	}
	return nil
}
