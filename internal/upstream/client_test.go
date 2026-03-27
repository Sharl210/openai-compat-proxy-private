package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/model"
)

func TestBuildRequestBodyOmitsCacheControlForUpstreamCompatibility(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{{
			Role: "user",
			Parts: []model.CanonicalContentPart{{
				Type: "text",
				Text: "hello",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if _, exists := payload["cache_control"]; exists {
		t.Fatalf("expected top-level cache_control to be omitted, got %#v", payload["cache_control"])
	}

	input, _ := payload["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected one input item, got %#v", input)
	}
	message, _ := input[0].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected one content item, got %#v", content)
	}
	part, _ := content[0].(map[string]any)
	if _, exists := part["cache_control"]; exists {
		t.Fatalf("expected content cache_control to be omitted, got %#v", part["cache_control"])
	}
	if got := part["text"]; got != "hello" {
		t.Fatalf("expected content text hello, got %#v", got)
	}
}

func TestBuildRequestBodyPreservesResponsesMetadataAndTypedInputItems(t *testing.T) {
	store := false
	body, err := buildRequestBody(model.CanonicalRequest{
		Model:           "gpt-5",
		ResponseStore:   &store,
		ResponseInclude: []string{"reasoning.encrypted_content"},
		ResponseInputItems: []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{{
					"type": "input_text",
					"text": "hello",
				}},
			},
			{
				"type":              "reasoning",
				"id":                "rs_123",
				"encrypted_content": "enc_123",
			},
			{
				"type":    "function_call_output",
				"call_id": "call_123",
				"output":  "{}",
			},
		},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if got, ok := payload["store"].(bool); !ok || got {
		t.Fatalf("expected store=false in upstream payload, got %#v", payload["store"])
	}
	include, _ := payload["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("expected include reasoning.encrypted_content, got %#v", payload["include"])
	}
	input, _ := payload["input"].([]any)
	if len(input) != 3 {
		t.Fatalf("expected 3 input items, got %#v", payload["input"])
	}
	typed, _ := input[1].(map[string]any)
	if got, _ := typed["type"].(string); got != "reasoning" {
		t.Fatalf("expected reasoning item passthrough, got %#v", typed)
	}
	toolOutput, _ := input[2].(map[string]any)
	if got, _ := toolOutput["type"].(string); got != "function_call_output" {
		t.Fatalf("expected function_call_output passthrough, got %#v", toolOutput)
	}
}

func TestBuildRequestBodyPreservesSamplingStopImageDetailAndToolChoiceObject(t *testing.T) {
	temperature := 0.2
	topP := 0.7
	maxTokens := 321
	body, err := buildRequestBody(model.CanonicalRequest{
		Model:           "gpt-5",
		Temperature:     &temperature,
		TopP:            &topP,
		MaxOutputTokens: &maxTokens,
		Stop:            []string{"END"},
		ToolChoice: model.CanonicalToolChoice{Mode: "tool", Raw: map[string]any{
			"type": "tool",
			"name": "get_weather",
		}},
		Messages: []model.CanonicalMessage{{
			Role: "user",
			Parts: []model.CanonicalContentPart{{
				Type:     "image_url",
				ImageURL: "https://example.com/cat.png",
				Raw:      map[string]any{"image_url": map[string]any{"url": "https://example.com/cat.png", "detail": "high"}},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got := payload["temperature"]; got != temperature {
		t.Fatalf("expected temperature %v, got %#v", temperature, got)
	}
	if got := payload["top_p"]; got != topP {
		t.Fatalf("expected top_p %v, got %#v", topP, got)
	}
	if got := payload["max_output_tokens"]; got != float64(maxTokens) {
		t.Fatalf("expected max_output_tokens %d, got %#v", maxTokens, got)
	}
	stop, _ := payload["stop"].([]any)
	if len(stop) != 1 || stop[0] != "END" {
		t.Fatalf("expected stop END, got %#v", payload["stop"])
	}
	toolChoice, _ := payload["tool_choice"].(map[string]any)
	if toolChoice["name"] != "get_weather" || toolChoice["type"] != "tool" {
		t.Fatalf("expected structured tool_choice object, got %#v", payload["tool_choice"])
	}
	input, _ := payload["input"].([]any)
	msg, _ := input[0].(map[string]any)
	content, _ := msg["content"].([]any)
	image, _ := content[0].(map[string]any)
	if got, _ := image["image_url"].(string); got != "https://example.com/cat.png" {
		t.Fatalf("expected image_url string preserved, got %#v", image)
	}
	if got := image["detail"]; got != "high" {
		t.Fatalf("expected image detail high preserved, got %#v", image)
	}
}

func TestBuildRequestBodyPreservesInputFileAndStructuredToolOutput(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{
			{
				Role: "user",
				Parts: []model.CanonicalContentPart{{
					Type: "input_file",
					Raw:  map[string]any{"input_file": map[string]any{"file_id": "file_123"}},
				}},
			},
			{
				Role:       "tool",
				ToolCallID: "call_1",
				Parts:      []model.CanonicalContentPart{{Type: "text", Text: "看图"}, {Type: "image_url", Raw: map[string]any{"image_url": map[string]any{"url": "https://example.com/tool.png"}}}},
			},
		},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	input, _ := payload["input"].([]any)
	user, _ := input[0].(map[string]any)
	content, _ := user["content"].([]any)
	filePart, _ := content[0].(map[string]any)
	inputFile, _ := filePart["input_file"].(map[string]any)
	if got := inputFile["file_id"]; got != "file_123" {
		t.Fatalf("expected input_file preserved, got %#v", filePart)
	}
	toolOutput, _ := input[1].(map[string]any)
	if got, _ := toolOutput["output"].(string); !strings.Contains(got, `"type":"input_image"`) {
		t.Fatalf("expected structured tool output JSON to preserve image content, got %#v", toolOutput)
	}
}

func TestBuildRequestBodyForwardsAssistantReasoningContentAsReasoningSummary(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{{
			Role:             "assistant",
			ReasoningContent: "先想一下",
			Parts: []model.CanonicalContentPart{{
				Type: "text",
				Text: "最终答案",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	input, _ := payload["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("expected reasoning item plus assistant message, got %#v", payload["input"])
	}
	reasoning, _ := input[0].(map[string]any)
	if got, _ := reasoning["type"].(string); got != "reasoning" {
		t.Fatalf("expected first item to be reasoning, got %#v", reasoning)
	}
	summary, _ := reasoning["summary"].([]any)
	if len(summary) != 1 {
		t.Fatalf("expected one reasoning summary part, got %#v", reasoning)
	}
	summaryPart, _ := summary[0].(map[string]any)
	if got, _ := summaryPart["type"].(string); got != "summary_text" {
		t.Fatalf("expected summary_text part, got %#v", summaryPart)
	}
	if got, _ := summaryPart["text"].(string); got != "先想一下" {
		t.Fatalf("expected reasoning summary text preserved, got %#v", summaryPart)
	}
	message, _ := input[1].(map[string]any)
	if got, _ := message["role"].(string); got != "assistant" {
		t.Fatalf("expected second item assistant message, got %#v", message)
	}
}

func TestParseSSEAcceptsLargeEventPayload(t *testing.T) {
	large := strings.Repeat("x", 128*1024)
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader("event: response.output_item.done\n" +
			"data: {\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"" + large + "\"}}\n\n")),
	}

	events, err := parseSSE(resp)
	if err != nil {
		t.Fatalf("parseSSE error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one event, got %#v", events)
	}
	item, _ := events[0].Data["item"].(map[string]any)
	if got, _ := item["encrypted_content"].(string); got != large {
		t.Fatalf("expected encrypted_content length %d, got %d", len(large), len(got))
	}
}

func TestParseSSEStreamingAcceptsLargeEventPayload(t *testing.T) {
	large := strings.Repeat("y", 128*1024)
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader("event: response.output_item.done\n" +
			"data: {\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"" + large + "\"}}\n\n")),
	}

	var seen Event
	err := parseSSEStreaming(resp, func(evt Event) error {
		seen = evt
		return nil
	})
	if err != nil {
		t.Fatalf("parseSSEStreaming error: %v", err)
	}
	item, _ := seen.Data["item"].(map[string]any)
	if got, _ := item["encrypted_content"].(string); got != large {
		t.Fatalf("expected encrypted_content length %d, got %d", len(large), len(got))
	}
}

func TestParseSSEAcceptsEventAndDataWithoutTrailingSpace(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader("event:response.output_text.delta\n" +
			"data:{\"delta\":\"hello\"}\n\n" +
			"event:response.completed\n" +
			"data:{\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")),
	}

	events, err := parseSSE(resp)
	if err != nil {
		t.Fatalf("parseSSE error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected two events, got %#v", events)
	}
	if events[0].Event != "response.output_text.delta" {
		t.Fatalf("expected first event response.output_text.delta, got %#v", events[0])
	}
}

func TestStreamRetriesBeforeAnyEventUsingProviderRetryConfig(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		attempt := attempts.Add(1)
		if attempt <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("temporary upstream failure"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamRetryCount: 2, UpstreamRetryDelay: time.Millisecond})
	events, err := client.Stream(context.Background(), model.CanonicalRequest{Model: "gpt-5"}, "")
	if err != nil {
		t.Fatalf("expected stream to succeed after retries, got %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 upstream attempts, got %d", attempts.Load())
	}
	if len(events) != 1 || events[0].Event != "response.completed" {
		t.Fatalf("expected response.completed after retries, got %#v", events)
	}
}

func TestOpenEventStreamRetriesBeforeAnyEventUsingProviderRetryConfig(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		attempt := attempts.Add(1)
		if attempt <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("temporary upstream failure"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamRetryCount: 2, UpstreamRetryDelay: time.Millisecond})
	stream, err := client.OpenEventStream(context.Background(), model.CanonicalRequest{Model: "gpt-5"}, "")
	if err != nil {
		t.Fatalf("expected open event stream to succeed after retries, got %v", err)
	}
	defer stream.Close()
	if attempts.Load() != 3 {
		t.Fatalf("expected 3 upstream attempts, got %d", attempts.Load())
	}
	var seen []Event
	if err := stream.Consume(func(evt Event) error {
		seen = append(seen, evt)
		return nil
	}); err != nil {
		t.Fatalf("consume stream error: %v", err)
	}
	if len(seen) != 1 || seen[0].Event != "response.completed" {
		t.Fatalf("expected response.completed after retries, got %#v", seen)
	}
}

func TestStreamEventsDoesNotRetryAfterFirstEventArrives(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.output_text.delta\n" +
			"data: {broken json}\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamRetryCount: 2, UpstreamRetryDelay: time.Millisecond})
	err := client.StreamEvents(context.Background(), model.CanonicalRequest{Model: "gpt-5"}, "", func(Event) error { return nil })
	if err == nil {
		t.Fatalf("expected malformed stream error")
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected no retry after first event, got %d attempts", attempts.Load())
	}
}

func TestStreamDoesNotRetryAfterFirstEventArrives(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.output_text.delta\n" +
			"data: {broken json}\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamRetryCount: 2, UpstreamRetryDelay: time.Millisecond})
	_, err := client.Stream(context.Background(), model.CanonicalRequest{Model: "gpt-5"}, "")
	if err == nil {
		t.Fatalf("expected malformed stream error")
	}
	if attempts.Load() != 1 {
		t.Fatalf("expected no retry after first event, got %d attempts", attempts.Load())
	}
}

func TestResponseLogsRetryAndFinalFailureEvidence(t *testing.T) {
	logPath, cleanup := initUpstreamTestLogger(t)
	defer cleanup()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		attempts.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("temporary upstream failure"))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamRetryCount: 1, UpstreamRetryDelay: time.Millisecond})
	_, err := client.Response(context.Background(), model.CanonicalRequest{RequestID: "req-retry-fail", Model: "gpt-5"}, "")
	if err == nil {
		t.Fatalf("expected upstream error")
	}

	records := readUpstreamTestLogRecords(t, logPath)
	assertHasLogRecord(t, records, "upstream_request_retry", func(record map[string]any) bool {
		return record["request_id"] == "req-retry-fail" && record["attempt"] == float64(1) && record["status_code"] == float64(http.StatusBadGateway)
	})
	assertHasLogRecord(t, records, "upstream_request_failed", func(record map[string]any) bool {
		return record["request_id"] == "req-retry-fail" && record["status_code"] == float64(http.StatusBadGateway) && record["health_flag"] == "upstream_error"
	})
	if attempts.Load() != 2 {
		t.Fatalf("expected retry to reach upstream twice, got %d", attempts.Load())
	}
}

func TestStreamLogsTimeoutFailureBeforeFirstEvent(t *testing.T) {
	logPath, cleanup := initUpstreamTestLogger(t)
	defer cleanup()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{FirstByteTimeout: 50 * time.Millisecond})
	_, err := client.Stream(context.Background(), model.CanonicalRequest{RequestID: "req-timeout", Model: "gpt-5"}, "")
	if err == nil {
		t.Fatalf("expected timeout error")
	}

	records := readUpstreamTestLogRecords(t, logPath)
	assertHasLogRecord(t, records, "upstream_request_failed", func(record map[string]any) bool {
		return record["request_id"] == "req-timeout" && record["health_flag"] == "upstream_timeout" && record["streaming"] == true
	})
}

func TestStreamEventsLogsBrokenStreamAfterFirstEvent(t *testing.T) {
	logPath, cleanup := initUpstreamTestLogger(t)
	defer cleanup()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.output_text.delta\n" +
			"data: {broken json}\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{})
	err := client.StreamEvents(context.Background(), model.CanonicalRequest{RequestID: "req-broken-stream", Model: "gpt-5"}, "", func(Event) error { return nil })
	if err == nil {
		t.Fatalf("expected malformed stream error")
	}

	records := readUpstreamTestLogRecords(t, logPath)
	assertHasLogRecord(t, records, "upstream_stream_broken", func(record map[string]any) bool {
		return record["request_id"] == "req-broken-stream" && record["event_count"] == float64(1)
	})
}

func initUpstreamTestLogger(t *testing.T) (string, func()) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "proxy.jsonl")
	closeFn, err := logging.Init(config.Config{LogEnable: true, LogFilePath: logPath}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("init logger: %v", err)
	}
	return logPath, func() {
		if err := closeFn(); err != nil {
			t.Fatalf("close logger: %v", err)
		}
	}
}

func readUpstreamTestLogRecords(t *testing.T, logPath string) []map[string]any {
	t.Helper()
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	records := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func assertHasLogRecord(t *testing.T, records []map[string]any, event string, match func(map[string]any) bool) {
	t.Helper()
	for _, record := range records {
		if record["event"] == event && match(record) {
			return
		}
	}
	t.Fatalf("expected log event %q in %#v", event, records)
}
