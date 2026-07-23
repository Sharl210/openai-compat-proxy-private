package logging_test

import (
	"io"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/logging"
)

func BenchmarkLoggerDownstreamToolEventStream(b *testing.B) {
	logger, closeFn, err := logging.New(config.Config{
		LogFilePath:      b.TempDir(),
		LogMaxRequests:   50,
		LogMaxBodySizeMB: 1,
	}, io.Discard)
	if err != nil {
		b.Fatalf("new logger: %v", err)
	}
	b.Cleanup(func() { _ = closeFn() })

	const requestID = "req-downstream-tool-event-stream"
	const eventsPerStream = 256
	attrs := map[string]any{
		"request_id":        requestID,
		"downstream_type":   "responses",
		"event":             "response.function_call_arguments.delta",
		"item_id":           "fc_123",
		"arguments_len":     120,
		"arguments_preview": `{"query":"high-frequency tool event"}`,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for stream := 0; stream < b.N; stream++ {
		for range eventsPerStream {
			logger.Event("downstreamToolEvent", attrs)
		}
		logger.CloseRequest(requestID)
	}
}
