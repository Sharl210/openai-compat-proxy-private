package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
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
	imageURL, _ := image["image_url"].(map[string]any)
	if imageURL["detail"] != "high" {
		t.Fatalf("expected image detail high preserved, got %#v", imageURL)
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
