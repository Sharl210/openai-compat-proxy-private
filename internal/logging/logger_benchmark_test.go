package logging_test

import (
	"io"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/logging"
)

func BenchmarkLoggerDownstreamToolEventStream(b *testing.B) {
	const requestID = "req-downstream-tool-event-stream"
	const eventsPerStream = 256
	genericAttrs := map[string]any{
		"request_id":        requestID,
		"downstream_type":   "responses",
		"event":             "response.function_call_arguments.delta",
		"item_id":           "fc_123",
		"arguments_len":     120,
		"arguments_preview": `{"query":"high-frequency tool event"}`,
	}
	typedAttrs := logging.DownstreamToolEventAttrs{
		RequestID:        requestID,
		DownstreamType:   "responses",
		Event:            "response.function_call_arguments.delta",
		ItemID:           "fc_123",
		ArgumentsLen:     120,
		ArgumentsPreview: `{"query":"high-frequency tool event"}`,
	}

	for _, benchmark := range []struct {
		name string
		log  func(*logging.Logger)
	}{
		{name: "generic", log: func(logger *logging.Logger) { logger.Event("downstreamToolEvent", genericAttrs) }},
		{name: "typed", log: func(logger *logging.Logger) { logger.DownstreamToolEvent(typedAttrs) }},
	} {
		b.Run(benchmark.name, func(b *testing.B) {
			logger, closeFn, err := logging.New(config.Config{
				LogFilePath:      b.TempDir(),
				LogMaxRequests:   50,
				LogMaxBodySizeMB: 1,
			}, io.Discard)
			if err != nil {
				b.Fatalf("new logger: %v", err)
			}
			b.Cleanup(func() { _ = closeFn() })

			b.ReportAllocs()
			b.ResetTimer()
			for stream := 0; stream < b.N; stream++ {
				for range eventsPerStream {
					benchmark.log(logger)
				}
				logger.CloseRequest(requestID)
			}
		})
	}
}
