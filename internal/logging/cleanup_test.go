package logging_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/logging"
)

func TestLoggerDoesNotRecleanExistingRequestWithinCleanupWindow(t *testing.T) {
	tmpDir := t.TempDir()
	logger, closeFn, err := logging.New(config.Config{
		LogFilePath:      tmpDir,
		LogMaxRequests:   1,
		LogMaxBodySizeMB: 1,
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer closeFn()

	logger.Event("first_event", map[string]any{"request_id": "req-stable"})
	oldPath := filepath.Join(tmpDir, "req-old.txt")
	if err := os.WriteFile(oldPath, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("write old log: %v", err)
	}
	oldTime := time.Unix(1, 0)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("age old log: %v", err)
	}

	logger.Event("second_event", map[string]any{"request_id": "req-stable"})
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("expected append to existing request not to trigger another cleanup: %v", err)
	}

	logger.Event("new_request", map[string]any{"request_id": "req-new"})
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expected newly created request to trigger cleanup, stat err=%v", err)
	}
}

func TestLoggerDoesNotPruneWhenMaxRequestsIsNonPositive(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		maxRequests int
	}{
		{name: "zero", maxRequests: 0},
		{name: "negative", maxRequests: -1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			logger, closeFn, err := logging.New(config.Config{
				LogFilePath:      tmpDir,
				LogMaxRequests:   testCase.maxRequests,
				LogMaxBodySizeMB: 1,
			}, &bytes.Buffer{})
			if err != nil {
				t.Fatalf("new logger: %v", err)
			}
			defer closeFn()

			for _, requestID := range []string{"req-a", "req-b"} {
				logger.Event("event", map[string]any{"request_id": requestID})
			}
			for _, requestID := range []string{"req-a", "req-b"} {
				if _, err := os.Stat(filepath.Join(tmpDir, requestID+".txt")); err != nil {
					t.Fatalf("expected %s to remain when max requests is disabled: %v", requestID, err)
				}
			}
		})
	}
}

func TestLoggerReopensAfterRetentionPrunesAnActiveRequestFile(t *testing.T) {
	tmpDir := t.TempDir()
	logger, closeFn, err := logging.New(config.Config{
		LogFilePath:      tmpDir,
		LogMaxRequests:   1,
		LogMaxBodySizeMB: 1,
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("new logger: %v", err)
	}
	defer closeFn()

	const prunedRequestID = "req-pruned-active"
	prunedAttrs := map[string]any{"request_id": prunedRequestID}
	logger.Event("before_prune", prunedAttrs)
	prunedPath := filepath.Join(tmpDir, prunedRequestID+".txt")
	oldTime := time.Unix(1, 0)
	if err := os.Chtimes(prunedPath, oldTime, oldTime); err != nil {
		t.Fatalf("age active request log: %v", err)
	}

	logger.Event("new_request", map[string]any{"request_id": "req-newer"})
	if _, err := os.Stat(prunedPath); !os.IsNotExist(err) {
		t.Fatalf("expected older active request log to be pruned, stat err=%v", err)
	}

	logger.Event("after_prune", prunedAttrs)
	content, err := os.ReadFile(prunedPath)
	if err != nil {
		t.Fatalf("read recreated request log: %v", err)
	}
	if !bytes.Contains(content, []byte(`"event":"after_prune"`)) {
		t.Fatalf("expected later event to reopen a visible request log, got %s", content)
	}
}
