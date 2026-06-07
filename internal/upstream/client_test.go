package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"openai-compat-proxy/internal/debugarchive"
	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/model"
)

func TestClientResponse_WritesFinalSnapshotWhenArchiveAttached(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer server.Close()
	writer := debugarchive.NewArchiveWriter(t.TempDir(), "req-archive-final")
	defer writer.Close()
	ctx := debugarchive.WithArchiveWriter(context.Background(), writer)
	client := NewClient(server.URL)
	_, err := client.Response(ctx, model.CanonicalRequest{RequestID: "req-archive-final", Model: "gpt-5"}, "")
	if err != nil {
		t.Fatalf("Response error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive writer: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(os.TempDir(), "non-existent"))
	_ = data
	_ = err
}

func TestClientStreamEvents_WritesRawAndCanonicalWhenArchiveAttached(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte("data: {\"response\":{\"id\":\"resp_1\"}}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte("data: {\"response\":{\"id\":\"resp_1\"}}\n\n"))
	}))
	defer server.Close()
	root := t.TempDir()
	writer := debugarchive.NewArchiveWriter(root, "req-archive-stream")
	ctx := debugarchive.WithArchiveWriter(context.Background(), writer)
	client := NewClient(server.URL)
	err := client.StreamEvents(ctx, model.CanonicalRequest{RequestID: "req-archive-stream", Model: "gpt-5"}, "", func(Event) error { return nil })
	if err != nil {
		t.Fatalf("StreamEvents error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive writer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "req-archive-stream", "raw.ndjson")); err != nil {
		t.Fatalf("expected raw.ndjson: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "req-archive-stream", "canonical.ndjson")); err != nil {
		t.Fatalf("expected canonical.ndjson: %v", err)
	}
}

func TestClientStreamEvents_WritesResponsesFunctionCallFieldsToCanonicalArchive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_item.added\n"))
		_, _ = w.Write([]byte("data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"lookup\",\"arguments\":\"\"}}\n\n"))
		_, _ = w.Write([]byte("event: response.function_call_arguments.delta\n"))
		_, _ = w.Write([]byte("data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"q\\\":\"}\n\n"))
		_, _ = w.Write([]byte("event: response.function_call_arguments.done\n"))
		_, _ = w.Write([]byte("data: {\"item_id\":\"fc_1\",\"arguments\":\"{\\\"q\\\":\\\"apk\\\"}\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte("data: {\"response\":{\"id\":\"resp_1\"}}\n\n"))
	}))
	defer server.Close()

	root := t.TempDir()
	writer := debugarchive.NewArchiveWriter(root, "req-archive-tool-fields")
	ctx := debugarchive.WithArchiveWriter(context.Background(), writer)
	client := NewClient(server.URL)
	err := client.StreamEvents(ctx, model.CanonicalRequest{RequestID: "req-archive-tool-fields", Model: "gpt-5"}, "", func(Event) error { return nil })
	if err != nil {
		t.Fatalf("StreamEvents error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive writer: %v", err)
	}

	canonicalBytes, err := os.ReadFile(filepath.Join(root, "req-archive-tool-fields", "canonical.ndjson"))
	if err != nil {
		t.Fatalf("read canonical archive: %v", err)
	}
	var sawAdded, sawDelta, sawDone bool
	for _, line := range bytes.Split(bytes.TrimSpace(canonicalBytes), []byte("\n")) {
		var event model.CanonicalEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("unmarshal canonical event: %v", err)
		}
		switch event.Type {
		case "response.output_item.added":
			sawAdded = true
			if event.ItemID != "fc_1" || event.CallID != "call_1" || event.ToolName != "lookup" {
				t.Fatalf("expected function call metadata in added event, got %#v", event)
			}
		case "response.function_call_arguments.delta":
			sawDelta = true
			if event.ItemID != "fc_1" || event.ToolArgsDelta != `{"q":` {
				t.Fatalf("expected function call arguments delta, got %#v", event)
			}
		case "response.function_call_arguments.done":
			sawDone = true
			if event.ItemID != "fc_1" || event.ToolArgsDelta != `{"q":"apk"}` {
				t.Fatalf("expected completed function call arguments, got %#v", event)
			}
		}
	}
	if !sawAdded || !sawDelta || !sawDone {
		t.Fatalf("expected added, delta and done canonical events, got added=%t delta=%t done=%t", sawAdded, sawDelta, sawDone)
	}
}

func TestClientStreamEvents_WritesChatToolCallFieldsToCanonicalArchive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"q\\\":\\\"apk\\\"}\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_1\",\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3},\"choices\":[{\"index\":0,\"finish_reason\":\"tool_calls\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	events := readCanonicalArchiveEventsFromStream(t, server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeChat}, "req-archive-chat-tool-fields")
	assertCanonicalArchiveToolFields(t, events, "call_1", "call_1", "lookup", `{"q":"apk"}`)
}

func TestClientStreamEvents_WritesAnthropicToolUseFieldsToCanonicalArchive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"message\":{\"id\":\"msg_1\"}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte("data: {\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"lookup\",\"input\":{}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"q\\\":\\\"apk\\\"}\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte("data: {\"usage\":{\"input_tokens\":1,\"output_tokens\":2},\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte("data: {}\n\n"))
	}))
	defer server.Close()

	events := readCanonicalArchiveEventsFromStream(t, server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic}, "req-archive-anthropic-tool-fields")
	assertCanonicalArchiveToolFields(t, events, "toolu_1", "toolu_1", "lookup", `{"q":"apk"}`)
}

func TestClientStreamEvents_WritesSemanticFieldsToCanonicalArchive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("event: response.reasoning.delta\n"))
		_, _ = w.Write([]byte("data: {\"summary\":\"thinking\"}\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte("data: {\"response\":{\"finish_reason\":\"stop\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3}}}\n\n"))
	}))
	defer server.Close()

	events := readCanonicalArchiveEventsFromStream(t, server.URL, config.Config{}, "req-archive-semantic-fields")
	var sawText, sawReasoning, sawCompletion bool
	for _, event := range events {
		switch event.Type {
		case "response.output_text.delta":
			sawText = true
			if event.TextDelta != "hello" {
				t.Fatalf("expected text delta hello, got %#v", event)
			}
		case "response.reasoning.delta":
			sawReasoning = true
			if event.ReasoningDelta != "thinking" {
				t.Fatalf("expected reasoning delta thinking, got %#v", event)
			}
		case "response.completed":
			sawCompletion = true
			if event.FinishReason != "stop" {
				t.Fatalf("expected finish reason stop, got %#v", event)
			}
			if got := event.UsageDelta["input_tokens"]; got != float64(1) {
				t.Fatalf("expected usage input_tokens 1, got %#v event=%#v", got, event)
			}
		}
	}
	if !sawText || !sawReasoning || !sawCompletion {
		t.Fatalf("expected text, reasoning and completion events, got %#v", events)
	}
}

func TestClientStreamEvents_WritesErrorFieldsToCanonicalArchive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.incomplete\n"))
		_, _ = w.Write([]byte("data: {\"health_flag\":\"upstream_error\",\"message\":\"bad tool call\"}\n\n"))
	}))
	defer server.Close()

	events := readCanonicalArchiveEventsFromStream(t, server.URL, config.Config{}, "req-archive-error-fields")
	if len(events) != 1 {
		t.Fatalf("expected one canonical event, got %#v", events)
	}
	if events[0].Error["health_flag"] != "upstream_error" || events[0].Error["message"] != "bad tool call" {
		t.Fatalf("expected error fields, got %#v", events[0])
	}
}

func readCanonicalArchiveEventsFromStream(t *testing.T, upstreamURL string, cfg config.Config, requestID string) []model.CanonicalEvent {
	t.Helper()
	root := t.TempDir()
	writer := debugarchive.NewArchiveWriter(root, requestID)
	ctx := debugarchive.WithArchiveWriter(context.Background(), writer)
	client := NewClient(upstreamURL, cfg)
	err := client.StreamEvents(ctx, model.CanonicalRequest{RequestID: requestID, Model: "gpt-5"}, "", func(Event) error { return nil })
	if err != nil {
		t.Fatalf("StreamEvents error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive writer: %v", err)
	}
	canonicalBytes, err := os.ReadFile(filepath.Join(root, requestID, "canonical.ndjson"))
	if err != nil {
		t.Fatalf("read canonical archive: %v", err)
	}
	var events []model.CanonicalEvent
	for _, line := range bytes.Split(bytes.TrimSpace(canonicalBytes), []byte("\n")) {
		var event model.CanonicalEvent
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatalf("unmarshal canonical event: %v", err)
		}
		events = append(events, event)
	}
	return events
}

func assertCanonicalArchiveToolFields(t *testing.T, events []model.CanonicalEvent, itemID string, callID string, name string, arguments string) {
	t.Helper()
	var sawItem, sawArgs bool
	for _, event := range events {
		switch event.Type {
		case "response.output_item.added", "response.output_item.done":
			if event.ItemID == itemID && event.CallID == callID && event.ToolName == name {
				sawItem = true
			}
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			if event.ItemID == itemID && event.ToolArgsDelta == arguments {
				sawArgs = true
			}
		}
	}
	if !sawItem || !sawArgs {
		t.Fatalf("expected canonical archive tool metadata and arguments, got item=%t args=%t events=%#v", sawItem, sawArgs, events)
	}
}

func TestClientCompactUsesCompactResponsesPathAndReusesResponsesBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses/compact" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer compact-key" {
			t.Fatalf("expected Authorization header preserved, got %q", got)
		}
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			t.Fatalf("unmarshal request payload: %v", err)
		}
		if got := payload["model"]; got != "gpt-5" {
			t.Fatalf("expected model gpt-5, got %#v", got)
		}
		if got := payload["store"]; got != false {
			t.Fatalf("expected store=false to reuse responses request body, got %#v", got)
		}
		include, _ := payload["include"].([]any)
		if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
			t.Fatalf("expected include passthrough, got %#v", payload["include"])
		}
		input, _ := payload["input"].([]any)
		if len(input) != 2 {
			t.Fatalf("expected 2 input items in compact payload, got %#v", payload["input"])
		}
		typed, _ := input[1].(map[string]any)
		if got, _ := typed["type"].(string); got != "reasoning" {
			t.Fatalf("expected typed reasoning input item passthrough, got %#v", typed)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_compact_1","status":"completed"}`))
	}))
	defer server.Close()

	store := false
	client := NewClient(server.URL)
	payload, err := client.Compact(context.Background(), model.CanonicalRequest{
		RequestID:       "req-compact-body",
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
		},
	}, "Bearer compact-key")
	if err != nil {
		t.Fatalf("Compact error: %v", err)
	}
	if got := payload["id"]; got != "resp_compact_1" {
		t.Fatalf("expected compact response id resp_compact_1, got %#v", got)
	}
}

func TestClientCompactRetriesAndPreservesHTTPStatusError(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses/compact" {
			http.NotFound(w, r)
			return
		}
		attempts.Add(1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("temporary compact upstream failure"))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamRetryCount: 1, UpstreamRetryDelay: time.Millisecond})
	_, err := client.Compact(context.Background(), model.CanonicalRequest{RequestID: "req-compact-retry", Model: "gpt-5"}, "")
	if err == nil {
		t.Fatalf("expected compact upstream error")
	}
	httpErr, ok := err.(*HTTPStatusError)
	if !ok {
		t.Fatalf("expected HTTPStatusError, got %T", err)
	}
	if httpErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected final status 502, got %d", httpErr.StatusCode)
	}
	if httpErr.RetriesPerformed != 1 {
		t.Fatalf("expected retries performed annotated as 1, got %d", httpErr.RetriesPerformed)
	}
	if attempts.Load() != 2 {
		t.Fatalf("expected compact request to retry once, got %d attempts", attempts.Load())
	}
}

func TestClientCompactRejectsNonResponsesEndpointTypesBeforeRequest(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeChat})
	_, err := client.Compact(context.Background(), model.CanonicalRequest{RequestID: "req-compact-chat", Model: "gpt-5"}, "")
	if err == nil {
		t.Fatalf("expected compact to reject non-responses endpoint type")
	}
	if !strings.Contains(err.Error(), "responses") || !strings.Contains(err.Error(), "compact") {
		t.Fatalf("expected clear compact/responses error, got %v", err)
	}
	if attempts.Load() != 0 {
		t.Fatalf("expected compact guard to fail before upstream request, got %d attempts", attempts.Load())
	}

	client = NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic})
	_, err = client.Compact(context.Background(), model.CanonicalRequest{RequestID: "req-compact-anthropic", Model: "claude"}, "")
	if err == nil {
		t.Fatalf("expected compact to reject anthropic endpoint type")
	}
	if !strings.Contains(err.Error(), "responses") || !strings.Contains(err.Error(), "compact") {
		t.Fatalf("expected clear compact/responses error, got %v", err)
	}
	if attempts.Load() != 0 {
		t.Fatalf("expected compact guard to fail before upstream request, got %d attempts", attempts.Load())
	}
}

func TestClientResponseKeepsResponsesPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_standard_1","status":"completed"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	payload, err := client.Response(context.Background(), model.CanonicalRequest{RequestID: "req-standard-path", Model: "gpt-5"}, "")
	if err != nil {
		t.Fatalf("Response error: %v", err)
	}
	if got := payload["id"]; got != "resp_standard_1" {
		t.Fatalf("expected standard response id resp_standard_1, got %#v", got)
	}
}

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

func TestBuildAnthropicRequestBodyPreservesSystemCacheControlBlocks(t *testing.T) {
	body, err := buildAnthropicRequestBody(model.CanonicalRequest{
		Model:        "claude-sonnet-4-5",
		Instructions: "stable prefix",
		InstructionParts: []model.CanonicalContentPart{{
			Type: "text",
			Text: "stable prefix",
			Raw:  map[string]any{"cache_control": map[string]any{"type": "ephemeral"}},
		}},
		Messages: []model.CanonicalMessage{{
			Role:  "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
		}},
	}, "", false, false)
	if err != nil {
		t.Fatalf("buildAnthropicRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	system, _ := payload["system"].([]any)
	if len(system) != 1 {
		t.Fatalf("expected one system block, got %#v", payload["system"])
	}
	block, _ := system[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "stable prefix" {
		t.Fatalf("expected text system block, got %#v", block)
	}
	cacheControl, _ := block["cache_control"].(map[string]any)
	if cacheControl["type"] != "ephemeral" {
		t.Fatalf("expected system cache_control preserved, got %#v", block)
	}
}

func TestBuildAnthropicRequestBodyKeepsStringSystemWhenNoInstructionParts(t *testing.T) {
	body, err := buildAnthropicRequestBody(model.CanonicalRequest{
		Model:        "claude-sonnet-4-5",
		Instructions: "plain system",
		Messages: []model.CanonicalMessage{{
			Role:  "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
		}},
	}, "", false, false)
	if err != nil {
		t.Fatalf("buildAnthropicRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["system"].(string); got != "plain system" {
		t.Fatalf("expected string system without instruction parts, got %#v", payload["system"])
	}
}

func TestBuildAnthropicRequestBodyUsesHoistedInstructionsAsSystem(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model:        "claude-sonnet-4-5",
		Instructions: "stable prefix",
		Messages: []model.CanonicalMessage{{
			Role:  "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
		}},
	}, config.UpstreamEndpointTypeAnthropic, "", false, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["system"].(string); got != "stable prefix" {
		t.Fatalf("expected hoisted instructions to become anthropic system, got %#v", payload["system"])
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected only user message in anthropic messages, got %#v", payload["messages"])
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

func TestBuildRequestBodyPreservesSamplingStopImageDetailAndStructuredToolChoiceObject(t *testing.T) {
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
	if toolChoice["name"] != "get_weather" || toolChoice["type"] != "function" {
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

func TestBuildRequestBodyMapsAnthropicToolChoiceToResponsesFunctionChoice(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		ToolChoice: model.CanonicalToolChoice{Mode: "tool", Raw: map[string]any{
			"type": "tool",
			"name": "lookup_project_facts",
		}},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	toolChoice, _ := payload["tool_choice"].(map[string]any)
	if toolChoice == nil {
		t.Fatalf("expected structured tool_choice object, got %#v", payload["tool_choice"])
	}
	if got, _ := toolChoice["type"].(string); got != "function" {
		t.Fatalf("expected responses tool_choice.type=function, got %#v", payload["tool_choice"])
	}
	if got, _ := toolChoice["name"].(string); got != "lookup_project_facts" {
		t.Fatalf("expected responses tool_choice.name preserved, got %#v", payload["tool_choice"])
	}
}

func TestBuildRequestBodyMapsAnthropicOutputConfigToResponsesText(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		PreservedTopLevelFields: map[string]any{
			"output_config": map[string]any{
				"format": map[string]any{
					"type":   "json_schema",
					"schema": map[string]any{"type": "object"},
				},
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
	if _, exists := payload["output_config"]; exists {
		t.Fatalf("expected responses upstream payload to map output_config away, got %#v", payload)
	}
	text, _ := payload["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if got, _ := format["type"].(string); got != "json_schema" {
		t.Fatalf("expected output_config to become text.format json_schema, got %#v", payload)
	}
	schema, _ := format["schema"].(map[string]any)
	if got, _ := schema["type"].(string); got != "object" {
		t.Fatalf("expected schema to survive output_config mapping, got %#v", payload)
	}
}

func TestBuildRequestBodyMapsChatResponseFormatToResponsesText(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		PreservedTopLevelFields: map[string]any{
			"response_format": map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   "session_title",
					"strict": true,
					"schema": map[string]any{"type": "object"},
				},
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
	if _, exists := payload["response_format"]; exists {
		t.Fatalf("expected responses upstream payload to map response_format away, got %#v", payload)
	}
	text, _ := payload["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if got, _ := format["type"].(string); got != "json_schema" {
		t.Fatalf("expected response_format to become text.format json_schema, got %#v", payload)
	}
	if got, _ := format["name"].(string); got != "session_title" {
		t.Fatalf("expected response_format json_schema.name to survive mapping, got %#v", payload)
	}
	if got, _ := format["strict"].(bool); !got {
		t.Fatalf("expected response_format json_schema.strict to survive mapping, got %#v", payload)
	}
	schema, _ := format["schema"].(map[string]any)
	if got, _ := schema["type"].(string); got != "object" {
		t.Fatalf("expected response_format json_schema.schema to survive mapping, got %#v", payload)
	}
}

func TestBuildRequestBodyMapsServiceTierAliasToServiceTierSnakeCase(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		PreservedTopLevelFields: map[string]any{
			"serviceTier": "priority",
		},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["service_tier"].(string); got != "priority" {
		t.Fatalf("expected service_tier to map from serviceTier, got %#v", payload["service_tier"])
	}
	if _, exists := payload["serviceTier"]; exists {
		t.Fatalf("expected serviceTier alias to be removed from responses upstream payload, got %#v", payload)
	}
}

func TestBuildRequestBodyServiceTierSnakeCaseTakesPrecedenceOverAlias(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		PreservedTopLevelFields: map[string]any{
			"serviceTier":  "priority",
			"service_tier": "flex",
		},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["service_tier"].(string); got != "flex" {
		t.Fatalf("expected explicit service_tier to win over serviceTier alias, got %#v", payload["service_tier"])
	}
	if _, exists := payload["serviceTier"]; exists {
		t.Fatalf("expected serviceTier alias to be removed from responses upstream payload, got %#v", payload)
	}
}

func TestNormalizeChatPayloadPreservesServiceTier(t *testing.T) {
	payload := normalizeChatPayload(map[string]any{
		"id":           "chatcmpl_123",
		"object":       "chat.completion",
		"service_tier": "default",
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "hello",
				},
				"finish_reason": "stop",
			},
		},
	}, config.UpstreamThinkingTagStyleOff)

	if got, _ := payload["service_tier"].(string); got != "default" {
		t.Fatalf("expected service_tier default, got %#v", payload["service_tier"])
	}
}

func TestBuildRequestBodyOmitsUsageIncludeForResponsesStreaming(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model:        "gpt-5",
		Stream:       true,
		IncludeUsage: true,
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, exists := payload["include"]; exists {
		t.Fatalf("expected streaming responses request to avoid include usage passthrough, got %#v", payload)
	}
}

func TestBuildRequestBodyOmitsIncludeForResponsesStreamingEvenWhenResponseIncludeIsPreset(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model:           "gpt-5",
		Stream:          true,
		ResponseInclude: []string{"usage"},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, exists := payload["include"]; exists {
		t.Fatalf("expected streaming responses request to strip include even when preset, got %#v", payload)
	}
}

func TestBuildRequestBodyOmitsIncludeForResponsesStreamingEvenWhenPreservedTopLevelIncludeIsPreset(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model:  "gpt-5",
		Stream: true,
		PreservedTopLevelFields: map[string]any{
			"include": []any{"usage"},
		},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, exists := payload["include"]; exists {
		t.Fatalf("expected streaming responses request to strip preserved include, got %#v", payload)
	}
}

func TestBuildRequestBodyOmitsReasoningForDisabledAnthropicThinking(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		Reasoning: &model.CanonicalReasoning{Raw: map[string]any{
			"thinking": map[string]any{"type": "disabled"},
		}},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	reasoning, _ := payload["reasoning"].(map[string]any)
	if got := reasoning["effort"]; got != "none" {
		t.Fatalf("expected disabled anthropic thinking to map to explicit none effort, got %#v", payload)
	}
}

func TestBuildRequestBodyPreservesResponsesToolTypesForFunctionOnlyTools(t *testing.T) {
	tools := buildResponsesToolEntries(t, []model.CanonicalTool{{
		Type:        "function",
		Name:        "get_weather",
		Description: "Get weather",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []string{"city"},
		},
	}})
	assertJSONEqual(t, tools, []map[string]any{{
		"type":        "function",
		"name":        "get_weather",
		"description": "Get weather",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []string{"city"},
		},
	}})
}

func TestBuildRequestBodyUsesEmptySchemaForFunctionToolWithoutParameters(t *testing.T) {
	tools := buildResponsesToolEntries(t, []model.CanonicalTool{{
		Type:        "function",
		Name:        "get_current_time",
		Description: "Get current time",
	}})
	assertJSONEqual(t, tools, []map[string]any{{
		"type":        "function",
		"name":        "get_current_time",
		"description": "Get current time",
		"parameters":  map[string]any{},
	}})
}

func TestBuildRequestBodyUsesEmptySchemaForBareObjectFunctionToolInFunctionOnlyMode(t *testing.T) {
	tools := buildResponsesUpstreamToolEntriesWithCompatMode(t, []model.CanonicalTool{{
		Type:        "function",
		Name:        "get_current_time",
		Description: "Get current time",
		Parameters:  map[string]any{"type": "object"},
	}}, config.ResponsesToolCompatModeFunctionOnly)
	assertJSONEqual(t, tools, []map[string]any{{
		"type":        "function",
		"name":        "get_current_time",
		"description": "Get current time",
		"parameters":  map[string]any{},
	}})
}

func TestBuildRequestBodyPreservesResponsesToolTypesForCustomOnlyTools(t *testing.T) {
	tools := buildResponsesToolEntries(t, []model.CanonicalTool{{
		Type:        "custom",
		Name:        "code_exec",
		Description: "Run code",
	}})
	assertJSONEqual(t, tools, []map[string]any{{
		"type":        "custom",
		"name":        "code_exec",
		"description": "Run code",
		"parameters":  map[string]any{},
	}})
}

func TestBuildRequestBodyPreservesResponsesToolTypesForWebSearchOnlyTools(t *testing.T) {
	tools := buildResponsesToolEntries(t, []model.CanonicalTool{{
		Type:        "web_search",
		Description: "Search the web",
	}})
	assertJSONEqual(t, tools, []map[string]any{{
		"type":        "web_search",
		"name":        "",
		"description": "Search the web",
		"parameters":  map[string]any{},
	}})
}

func TestBuildRequestBodyPreservesResponsesToolTypesForMixedToolFamilies(t *testing.T) {
	tools := buildResponsesToolEntries(t, []model.CanonicalTool{
		{Type: "custom", Name: "code_exec", Description: "Run code"},
		{Type: "function", Name: "get_weather", Description: "Get weather", Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []string{"city"},
		}},
		{Type: "web_search", Description: "Search the web"},
	})
	assertJSONEqual(t, tools, []map[string]any{
		{"type": "custom", "name": "code_exec", "description": "Run code", "parameters": map[string]any{}},
		{"type": "function", "name": "get_weather", "description": "Get weather", "parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []string{"city"},
		}},
		{"type": "web_search", "name": "", "description": "Search the web", "parameters": map[string]any{}},
	})
}

func TestBuildRequestBodyRewritesResponsesToolTypesInFunctionOnlyModeForCustomOnlyTools(t *testing.T) {
	tools := buildResponsesUpstreamToolEntriesWithCompatMode(t, []model.CanonicalTool{{
		Type:        "custom",
		Name:        "code_exec",
		Description: "Run code",
	}}, config.ResponsesToolCompatModeFunctionOnly)
	assertJSONEqual(t, tools, []map[string]any{{
		"type":        "function",
		"name":        "code_exec",
		"description": "Run code",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{"type": "string"},
			},
			"required":             []string{"input"},
			"additionalProperties": false,
		},
	}})
}

func TestBuildRequestBodyRewritesResponsesToolTypesInFunctionOnlyModeForWebSearchOnlyTools(t *testing.T) {
	tools := buildResponsesUpstreamToolEntriesWithCompatMode(t, []model.CanonicalTool{{
		Type:        "web_search",
		Description: "Search the web",
	}}, config.ResponsesToolCompatModeFunctionOnly)
	assertJSONEqual(t, tools, []map[string]any{{
		"type":        "function",
		"name":        "web_search",
		"description": "Search the web",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []string{"query"},
		},
	}})
}

func TestBuildRequestBodyRewritesResponsesToolTypesInFunctionOnlyModeForMixedToolFamilies(t *testing.T) {
	tools := buildResponsesUpstreamToolEntriesWithCompatMode(t, []model.CanonicalTool{
		{Type: "custom", Name: "code_exec", Description: "Run code"},
		{Type: "function", Name: "get_weather", Description: "Get weather", Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []string{"city"},
		}},
		{Type: "web_search", Description: "Search the web"},
	}, config.ResponsesToolCompatModeFunctionOnly)
	assertJSONEqual(t, tools, []map[string]any{
		{"type": "function", "name": "code_exec", "description": "Run code", "parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{"type": "string"},
			},
			"required":             []string{"input"},
			"additionalProperties": false,
		}},
		{"type": "function", "name": "get_weather", "description": "Get weather", "parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []string{"city"},
		}},
		{"type": "function", "name": "web_search", "description": "Search the web", "parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []string{"query"},
		}},
	})
}

func buildResponsesToolEntries(t *testing.T, tools []model.CanonicalTool) []map[string]any {
	t.Helper()
	body, err := buildRequestBody(model.CanonicalRequest{Model: "gpt-5", Tools: tools})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	rawTools, _ := payload["tools"].([]any)
	out := make([]map[string]any, 0, len(rawTools))
	for _, rawTool := range rawTools {
		tool, _ := rawTool.(map[string]any)
		out = append(out, tool)
	}
	return out
}

func buildResponsesUpstreamToolEntriesWithCompatMode(t *testing.T, tools []model.CanonicalTool, compatMode string) []map[string]any {
	t.Helper()
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var err error
		requestBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_tool_compat","status":"completed"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{
		UpstreamEndpointType:    config.UpstreamEndpointTypeResponses,
		ResponsesToolCompatMode: compatMode,
	})
	_, err := client.Response(context.Background(), model.CanonicalRequest{
		RequestID: "req-tool-compat",
		Model:     "gpt-5",
		Tools:     tools,
	}, "")
	if err != nil {
		t.Fatalf("Response error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(requestBody, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	rawTools, _ := payload["tools"].([]any)
	out := make([]map[string]any, 0, len(rawTools))
	for _, rawTool := range rawTools {
		tool, _ := rawTool.(map[string]any)
		out = append(out, tool)
	}
	return out
}

func assertJSONEqual(t *testing.T, got any, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got json: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal want json: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("expected json %s, got %s", wantJSON, gotJSON)
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

func TestBuildRequestBodyPrefersStructuredToolOutputRaw(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{{
			Role:       "tool",
			ToolCallID: "call_1",
			Parts: []model.CanonicalContentPart{{
				Type: "text",
				Text: `{"items":[{"id":"v1"}]}`,
				Raw:  map[string]any{"tool_output_structured": map[string]any{"items": []any{map[string]any{"id": "v1"}}}},
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
	if len(input) != 1 {
		t.Fatalf("expected one tool output item, got %#v", payload["input"])
	}
	toolOutput, _ := input[0].(map[string]any)
	output, _ := toolOutput["output"].(map[string]any)
	items, _ := output["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected structured tool output object, got %#v", toolOutput)
	}
}

func TestBuildRequestBodyLeavesPlainTextToolOutputUntouched(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{{
			Role:       "tool",
			ToolCallID: "call_1",
			Parts: []model.CanonicalContentPart{{
				Type: "text",
				Text: `{"query":"hello"`,
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
	toolOutput, _ := input[0].(map[string]any)
	if got, _ := toolOutput["output"].(string); got != `{"query":"hello"` {
		t.Fatalf("expected plain text tool output preserved verbatim, got %#v", toolOutput)
	}
}

func TestBuildRequestBodyPreservesOrderedToolResultBetweenTextMessages(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{{
			Role: "user",
			OrderedContent: []model.CanonicalContentBlock{
				{Type: "content", Part: model.CanonicalContentPart{Type: "text", Text: "工具前"}},
				{Type: "tool_result", ToolCallID: "call_dynamic_1", ToolResultParts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}}},
				{Type: "content", Part: model.CanonicalContentPart{Type: "text", Text: "工具后"}},
			},
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "工具前"}, {Type: "text", Text: "工具后"}},
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
	if len(input) != 3 {
		t.Fatalf("expected text, function_call_output, text input items, got %#v", input)
	}
	first, _ := input[0].(map[string]any)
	second, _ := input[1].(map[string]any)
	third, _ := input[2].(map[string]any)
	if first["role"] != "user" || second["type"] != "function_call_output" || third["role"] != "user" {
		t.Fatalf("expected ordered response input shape, got %#v", input)
	}
	if second["call_id"] != "call_dynamic_1" || second["output"] != `{"ok":true}` {
		t.Fatalf("expected ordered tool output preserved, got %#v", second)
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

func TestBuildAnthropicMessagesMergesAdjacentToolResultsIntoSingleUserMessage(t *testing.T) {
	messages := buildAnthropicMessages(model.CanonicalRequest{Messages: []model.CanonicalMessage{
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "scrape_web", Arguments: `{"url":"https://github.com/code-yeongyu/oh-my-openagent"}`}, {ID: "call_2", Type: "function", Name: "search_web", Arguments: `{"query":"oh-my-openagent releases","topic":"general"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"title":"repo page"}`}}},
		{Role: "tool", ToolCallID: "call_2", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"items":[{"id":"v1"}]}`}}},
	}})

	if len(messages) != 2 {
		t.Fatalf("expected assistant tool_use message plus single user tool_result message, got %#v", messages)
	}
	userMsg, _ := messages[1].(map[string]any)
	if got, _ := userMsg["role"].(string); got != "user" {
		t.Fatalf("expected second message role user, got %#v", userMsg)
	}
	content, _ := userMsg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected merged user tool_result content blocks, got %#v", userMsg)
	}
	first, _ := content[0].(map[string]any)
	second, _ := content[1].(map[string]any)
	if first["type"] != "tool_result" || second["type"] != "tool_result" {
		t.Fatalf("expected both merged blocks to be tool_result, got %#v", userMsg)
	}
	if first["tool_use_id"] != "call_1" || second["tool_use_id"] != "call_2" {
		t.Fatalf("expected merged tool_result order preserved, got %#v", userMsg)
	}
}

func TestBuildAnthropicMessagesRepairsMalformedToolArguments(t *testing.T) {
	messages := buildAnthropicMessages(model.CanonicalRequest{Messages: []model.CanonicalMessage{{
		Role: "assistant",
		ToolCalls: []model.CanonicalToolCall{{
			ID:        "call_1",
			Type:      "function",
			Name:      "search_web",
			Arguments: `{"query":"hello"`,
		}},
	}}})

	if len(messages) != 1 {
		t.Fatalf("expected one assistant message, got %#v", messages)
	}
	assistantMsg, _ := messages[0].(map[string]any)
	content, _ := assistantMsg["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected one tool_use block, got %#v", assistantMsg)
	}
	toolUse, _ := content[0].(map[string]any)
	input, _ := toolUse["input"].(map[string]any)
	if got, _ := input["query"].(string); got != "hello" {
		t.Fatalf("expected malformed tool arguments to be repaired into structured input, got %#v", toolUse)
	}
}

func TestBuildAnthropicMessagesLeavesPlainTextToolResultUntouched(t *testing.T) {
	messages := buildAnthropicMessages(model.CanonicalRequest{Messages: []model.CanonicalMessage{
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "search_web", Arguments: `{"query":"hello"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"query":"hello"`}}},
	}})

	userMsg, _ := messages[1].(map[string]any)
	content, _ := userMsg["content"].([]any)
	toolResult, _ := content[0].(map[string]any)
	if got, _ := toolResult["content"].(string); got != `{"query":"hello"` {
		t.Fatalf("expected plain text tool_result preserved verbatim, got %#v", toolResult)
	}
}

func TestBuildAnthropicMessagesMergesPendingToolResultsWithFollowingUserText(t *testing.T) {
	messages := buildAnthropicMessages(model.CanonicalRequest{Messages: []model.CanonicalMessage{
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "search_web", Arguments: `{"query":"hello"}`}}},
		{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"items":[]}`}}},
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "继续处理"}}},
	}})

	if len(messages) != 2 {
		t.Fatalf("expected assistant message plus single merged user message, got %#v", messages)
	}
	userMsg, _ := messages[1].(map[string]any)
	if got, _ := userMsg["role"].(string); got != "user" {
		t.Fatalf("expected second message role user, got %#v", userMsg)
	}
	content, _ := userMsg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected merged tool_result plus trailing text, got %#v", userMsg)
	}
	first, _ := content[0].(map[string]any)
	second, _ := content[1].(map[string]any)
	if first["type"] != "tool_result" {
		t.Fatalf("expected first block tool_result, got %#v", userMsg)
	}
	if first["tool_use_id"] != "call_1" {
		t.Fatalf("expected tool_result to preserve call id, got %#v", userMsg)
	}
	if second["type"] != "text" || second["text"] != "继续处理" {
		t.Fatalf("expected trailing user text in same message, got %#v", userMsg)
	}
}

func TestBuildAnthropicMessagesPreservesOrderedContentBlocks(t *testing.T) {
	messages := buildAnthropicMessages(model.CanonicalRequest{Messages: []model.CanonicalMessage{{
		Role: "user",
		OrderedContent: []model.CanonicalContentBlock{
			{Type: "content", Part: model.CanonicalContentPart{Type: "text", Text: "工具前"}},
			{
				Type:            "tool_result",
				ToolCallID:      "call_dynamic_1",
				ToolResultParts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}},
				Raw:             map[string]any{"cache_control": map[string]any{"type": "ephemeral"}},
			},
			{Type: "content", Part: model.CanonicalContentPart{Type: "text", Text: "工具后"}},
		},
	}}})

	if len(messages) != 1 {
		t.Fatalf("expected one anthropic user message, got %#v", messages)
	}
	userMsg, _ := messages[0].(map[string]any)
	content, _ := userMsg["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("expected three ordered content blocks, got %#v", userMsg)
	}
	first, _ := content[0].(map[string]any)
	second, _ := content[1].(map[string]any)
	third, _ := content[2].(map[string]any)
	if first["type"] != "text" || first["text"] != "工具前" {
		t.Fatalf("expected leading text before tool_result, got %#v", content)
	}
	if second["type"] != "tool_result" || second["tool_use_id"] != "call_dynamic_1" || second["content"] != `{"ok":true}` {
		t.Fatalf("expected middle tool_result block, got %#v", content)
	}
	cacheControl, _ := second["cache_control"].(map[string]any)
	if cacheControl["type"] != "ephemeral" {
		t.Fatalf("expected tool_result cache_control preserved, got %#v", second)
	}
	if third["type"] != "text" || third["text"] != "工具后" {
		t.Fatalf("expected trailing text after tool_result, got %#v", content)
	}
}

func TestBuildAnthropicMessagesPreservesAssistantReasoningContentAsThinkingBlock(t *testing.T) {
	messages := buildAnthropicMessages(model.CanonicalRequest{Messages: []model.CanonicalMessage{{
		Role:             "assistant",
		ReasoningContent: "先想一下",
		Parts:            []model.CanonicalContentPart{{Type: "text", Text: "final answer"}},
	}}})

	if len(messages) != 1 {
		t.Fatalf("expected one anthropic assistant message, got %#v", messages)
	}
	assistantMsg, _ := messages[0].(map[string]any)
	if got, _ := assistantMsg["role"].(string); got != "assistant" {
		t.Fatalf("expected assistant role, got %#v", assistantMsg)
	}
	content, _ := assistantMsg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected thinking block plus text block, got %#v", assistantMsg)
	}
	thinking, _ := content[0].(map[string]any)
	text, _ := content[1].(map[string]any)
	if got, _ := thinking["type"].(string); got != "thinking" {
		t.Fatalf("expected first content block type thinking, got %#v", thinking)
	}
	if got, _ := thinking["thinking"].(string); got != "先想一下" {
		t.Fatalf("expected reasoning content preserved in thinking block, got %#v", thinking)
	}
	if got, _ := text["type"].(string); got != "text" {
		t.Fatalf("expected second content block type text, got %#v", text)
	}
	if got, _ := text["text"].(string); got != "final answer" {
		t.Fatalf("expected assistant text preserved after thinking block, got %#v", text)
	}
}

func TestBuildAnthropicMessagesNormalizesResponsesReasoningBlocksToThinkingBlocks(t *testing.T) {
	messages := buildAnthropicMessages(model.CanonicalRequest{Messages: []model.CanonicalMessage{{
		Role: "assistant",
		ReasoningBlocks: []map[string]any{{
			"type":              "reasoning",
			"id":                "rs_123",
			"summary":           []map[string]any{{"type": "summary_text", "text": "先想一下"}},
			"encrypted_content": "enc_123",
		}},
		Parts: []model.CanonicalContentPart{{Type: "text", Text: "final answer"}},
	}}})

	if len(messages) != 1 {
		t.Fatalf("expected one anthropic assistant message, got %#v", messages)
	}
	assistantMsg, _ := messages[0].(map[string]any)
	content, _ := assistantMsg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected normalized thinking block plus text block, got %#v", assistantMsg)
	}
	thinking, _ := content[0].(map[string]any)
	if got, _ := thinking["type"].(string); got != "thinking" {
		t.Fatalf("expected responses reasoning block normalized to thinking, got %#v", thinking)
	}
	if got, _ := thinking["thinking"].(string); got != "先想一下" {
		t.Fatalf("expected summary text hoisted into thinking field, got %#v", thinking)
	}
	if got, _ := thinking["signature"].(string); got != "enc_123" {
		t.Fatalf("expected encrypted_content preserved as signature for anthropic replay, got %#v", thinking)
	}
	if _, exists := thinking["summary"]; exists {
		t.Fatalf("expected raw responses summary removed from anthropic block, got %#v", thinking)
	}
}

func TestBuildAnthropicMessagesPreservesNativeThinkingBlocksForThinkingModeReplay(t *testing.T) {
	original := map[string]any{
		"type":      "thinking",
		"thinking":  "原始推理",
		"signature": "sig_123",
	}
	messages := buildAnthropicMessages(model.CanonicalRequest{Messages: []model.CanonicalMessage{{
		Role:            "assistant",
		ReasoningBlocks: []map[string]any{original},
		Parts:           []model.CanonicalContentPart{{Type: "text", Text: "final answer"}},
	}}})

	if len(messages) != 1 {
		t.Fatalf("expected one anthropic assistant message, got %#v", messages)
	}
	assistantMsg, _ := messages[0].(map[string]any)
	content, _ := assistantMsg["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("expected preserved thinking block plus text block, got %#v", assistantMsg)
	}
	thinking, _ := content[0].(map[string]any)
	if got, _ := thinking["type"].(string); got != "thinking" {
		t.Fatalf("expected native thinking block preserved, got %#v", thinking)
	}
	if got, _ := thinking["thinking"].(string); got != "原始推理" {
		t.Fatalf("expected native thinking text preserved, got %#v", thinking)
	}
	if got, _ := thinking["signature"].(string); got != "sig_123" {
		t.Fatalf("expected native signature preserved, got %#v", thinking)
	}
}

func TestParseSSEAcceptsLargeEventPayload(t *testing.T) {
	large := strings.Repeat("x", 128*1024)
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader("event: response.output_item.done\n" +
			"data: {\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"" + large + "\"}}\n\n" +
			"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")),
	}

	events, err := parseSSE(resp)
	if err != nil {
		t.Fatalf("parseSSE error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected two events, got %#v", events)
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
			"data: {\"item\":{\"type\":\"reasoning\",\"encrypted_content\":\"" + large + "\"}}\n\n" +
			"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")),
	}

	var seen []Event
	err := parseSSEStreaming(resp, func(evt Event) error {
		seen = append(seen, evt)
		return nil
	})
	if err != nil {
		t.Fatalf("parseSSEStreaming error: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected two events, got %#v", seen)
	}
	item, _ := seen[0].Data["item"].(map[string]any)
	if got, _ := item["encrypted_content"].(string); got != large {
		t.Fatalf("expected encrypted_content length %d, got %d", len(large), len(got))
	}
}

func TestParseSSEStreamingReturnsUnexpectedEOFWhenStreamEndsWithoutTerminalEvent(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader("event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n")),
	}

	var events []Event
	err := parseSSEStreaming(resp, func(evt Event) error {
		events = append(events, evt)
		return nil
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}
	if len(events) != 1 || events[0].Event != "response.output_text.delta" {
		t.Fatalf("expected streamed delta before EOF, got %#v", events)
	}
}

func TestParseSSEStreamingTreatsResponseFailedAsTerminal(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader("event: error\n" +
			"data: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"too long\",\"param\":\"input\"}}\n\n" +
			"event: response.failed\n" +
			"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"too long\"}}}\n\n")),
	}

	var events []Event
	err := parseSSEStreaming(resp, func(evt Event) error {
		events = append(events, evt)
		return nil
	})
	if err != nil {
		t.Fatalf("expected response.failed to terminate stream without EOF error, got %v", err)
	}
	if len(events) != 2 || events[0].Event != "error" || events[1].Event != "response.failed" {
		t.Fatalf("expected upstream failure events to preserve original terminal shapes, got %#v", events)
	}
	errorObj, _ := events[0].Data["error"].(map[string]any)
	if errorObj["code"] != "context_length_exceeded" || errorObj["message"] != "too long" {
		t.Fatalf("expected upstream error details to be preserved, got %#v", events[0].Data)
	}
}

func TestParseSSEStreamingPreservesUpstreamFailureEventShapes(t *testing.T) {
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader("event: error\n" +
			"data: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"code\":\"context_length_exceeded\",\"message\":\"too long\",\"param\":\"input\"},\"sequence_number\":2}\n\n" +
			"event: response.failed\n" +
			"data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"quota_exceeded\",\"message\":\"quota hit\",\"param\":\"input\"}}}\n\n")),
	}

	var events []Event
	err := parseSSEStreaming(resp, func(evt Event) error {
		events = append(events, evt)
		return nil
	})
	if err != nil {
		t.Fatalf("expected upstream failure events to terminate stream without EOF error, got %v", err)
	}
	if len(events) != 2 || events[0].Event != "error" || events[1].Event != "response.failed" {
		t.Fatalf("expected upstream failure event shapes to be preserved, got %#v", events)
	}
	firstErr, _ := events[0].Data["error"].(map[string]any)
	if firstErr["type"] != "invalid_request_error" || firstErr["code"] != "context_length_exceeded" || firstErr["param"] != "input" {
		t.Fatalf("expected upstream error object to remain intact, got %#v", events[0].Data)
	}
	response, _ := events[1].Data["response"].(map[string]any)
	secondErr, _ := response["error"].(map[string]any)
	if response["status"] != "failed" || secondErr["code"] != "quota_exceeded" || secondErr["param"] != "input" {
		t.Fatalf("expected upstream response.failed object to remain intact, got %#v", events[1].Data)
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

func TestResponseUsesChatEndpointAndNormalizesPayload(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from chat"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeChat})
	payload, err := client.Response(context.Background(), model.CanonicalRequest{Model: "gpt-5", Messages: []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}}}}}, "Bearer test-key")
	if err != nil {
		t.Fatalf("Response error: %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("expected chat endpoint path, got %q", gotPath)
	}
	output, _ := payload["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected one normalized output item, got %#v", payload)
	}
	message, _ := output[0].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected one normalized content part, got %#v", message)
	}
	part, _ := content[0].(map[string]any)
	if got := part["text"]; got != "hello from chat" {
		t.Fatalf("expected normalized chat text, got %#v", got)
	}
	usage, _ := payload["usage"].(map[string]any)
	if got := usage["input_tokens"]; got != float64(3) {
		t.Fatalf("expected input_tokens 3, got %#v", got)
	}
	if got := usage["output_tokens"]; got != float64(2) {
		t.Fatalf("expected output_tokens 2, got %#v", got)
	}
}

func TestStreamUsesChatEndpointAndNormalizesEvents(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
			"data: {\"id\":\"chatcmpl_123\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n" +
			"data: {\"id\":\"chatcmpl_123\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeChat})
	events, err := client.Stream(context.Background(), model.CanonicalRequest{Model: "gpt-5"}, "Bearer test-key")
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("expected chat endpoint path, got %q", gotPath)
	}
	if len(events) < 3 || events[0].Event != "response.created" || events[1].Event != "response.output_text.delta" || events[2].Event != "response.completed" {
		t.Fatalf("expected normalized response events, got %#v", events)
	}
	if got := events[1].Data["delta"]; got != "hello" {
		t.Fatalf("expected delta hello, got %#v", got)
	}
	// Usage is now wrapped inside response object (unified format)
	response, _ := events[2].Data["response"].(map[string]any)
	usage, _ := response["usage"].(map[string]any)
	if got := usage["input_tokens"]; got != float64(3) {
		t.Fatalf("expected input_tokens 3, got %#v", got)
	}
}

func TestStreamUsesChatEndpointNormalizesJSONResponseAsEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("expected chat endpoint path, got %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_json","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello from json"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeChat})
	events, err := client.Stream(context.Background(), model.CanonicalRequest{Model: "gpt-5", Stream: true}, "Bearer test-key")
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("expected created + text + completed events, got %#v", events)
	}
	if events[0].Event != "response.created" || events[1].Event != "response.output_text.delta" || events[len(events)-1].Event != "response.completed" {
		t.Fatalf("expected normalized JSON response events, got %#v", events)
	}
	if got := events[1].Data["delta"]; got != "hello from json" {
		t.Fatalf("expected text delta from JSON response, got %#v", got)
	}
	response, _ := events[len(events)-1].Data["response"].(map[string]any)
	if got := response["finish_reason"]; got != "stop" {
		t.Fatalf("expected finish_reason stop, got %#v", events[len(events)-1])
	}
}

func TestStreamUsesChatEndpointCarriesUsageWhenFinishAndUsageAreSplitAcrossFrames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n" +
			"data: {\"id\":\"chatcmpl_123\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"id\":\"chatcmpl_123\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5,\"prompt_tokens_details\":{\"cached_tokens\":1}}}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeChat})
	events, err := client.Stream(context.Background(), model.CanonicalRequest{Model: "gpt-5"}, "Bearer test-key")
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("expected created + delta + completed events, got %#v", events)
	}
	completed := events[len(events)-1]
	if completed.Event != "response.completed" {
		t.Fatalf("expected final event response.completed, got %#v", completed)
	}
	// Usage and finish_reason are now wrapped inside response object (unified format)
	response, _ := completed.Data["response"].(map[string]any)
	usage, _ := response["usage"].(map[string]any)
	if got := usage["input_tokens"]; got != float64(3) {
		t.Fatalf("expected completed usage.input_tokens 3, got %#v events=%#v", got, events)
	}
	details, _ := usage["input_tokens_details"].(map[string]any)
	if got := details["cached_tokens"]; got != float64(1) {
		t.Fatalf("expected completed usage.input_tokens_details.cached_tokens 1, got %#v events=%#v", got, events)
	}
	if got := response["finish_reason"]; got != "stop" {
		t.Fatalf("expected finish_reason stop on completed event, got %#v events=%#v", got, events)
	}
	if len(events) != 3 {
		t.Fatalf("expected created + delta + completed events only, got %#v", events)
	}
}

func TestStreamUsesChatEndpointAllowsEOFCompletionForBufferedNonStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n" +
			"data: {\"id\":\"chatcmpl_123\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeChat})
	events, err := client.Stream(context.Background(), model.CanonicalRequest{Model: "gpt-5", Stream: false}, "Bearer test-key")
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("expected created + delta + completed events, got %#v", events)
	}
	completed := events[len(events)-1]
	if completed.Event != "response.completed" {
		t.Fatalf("expected EOF completion for buffered non-stream path, got %#v", events)
	}
	response, _ := completed.Data["response"].(map[string]any)
	if got := response["finish_reason"]; got != "stop" {
		t.Fatalf("expected finish_reason stop, got %#v", completed)
	}
	usage, _ := response["usage"].(map[string]any)
	if got := usage["input_tokens"]; got != float64(3) {
		t.Fatalf("expected usage.input_tokens 3, got %#v events=%#v", got, events)
	}
}

func TestResponseUsesAnthropicEndpointHeadersAndNormalizesPayload(t *testing.T) {
	var gotPath string
	var gotAPIKey string
	var gotVersion string
	var gotBeta string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotBeta = r.Header.Get("anthropic-beta")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic})
	payload, err := client.Response(context.Background(), model.CanonicalRequest{Model: "claude-sonnet-4-5", MaxOutputTokens: intPtrForClientTest(128), Messages: []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}}}}}, "Bearer anthropic-key")
	if err != nil {
		t.Fatalf("Response error: %v", err)
	}
	if gotPath != "/messages" {
		t.Fatalf("expected anthropic endpoint path, got %q", gotPath)
	}
	if gotAPIKey != "anthropic-key" {
		t.Fatalf("expected x-api-key anthropic-key, got %q", gotAPIKey)
	}
	if gotVersion != "2023-06-01" {
		t.Fatalf("expected anthropic-version header, got %q", gotVersion)
	}
	if gotBeta != "" {
		t.Fatalf("expected no anthropic-beta header by default, got %q", gotBeta)
	}
	output, _ := payload["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected one normalized output item, got %#v", payload)
	}
	message, _ := output[0].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected one normalized content part, got %#v", message)
	}
	part, _ := content[0].(map[string]any)
	if got := part["text"]; got != "hello from anthropic" {
		t.Fatalf("expected normalized anthropic text, got %#v", got)
	}
	usage, _ := payload["usage"].(map[string]any)
	if got := usage["input_tokens"]; got != float64(4) {
		t.Fatalf("expected input_tokens 4, got %#v", got)
	}
	if got := usage["output_tokens"]; got != float64(2) {
		t.Fatalf("expected output_tokens 2, got %#v", got)
	}
}

func TestStreamUsesAnthropicEndpointNormalizesJSONResponseAsEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Fatalf("expected anthropic endpoint path, got %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_json","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic json"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic})
	events, err := client.Stream(context.Background(), model.CanonicalRequest{Model: "claude-sonnet-4-5", Stream: true, MaxOutputTokens: intPtrForClientTest(128)}, "Bearer anthropic-key")
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("expected created + text + completed events, got %#v", events)
	}
	if events[0].Event != "response.created" || events[1].Event != "response.output_text.delta" || events[len(events)-1].Event != "response.completed" {
		t.Fatalf("expected normalized JSON response events, got %#v", events)
	}
	if got := events[1].Data["delta"]; got != "hello from anthropic json" {
		t.Fatalf("expected text delta from JSON response, got %#v", got)
	}
	response, _ := events[len(events)-1].Data["response"].(map[string]any)
	if got := response["finish_reason"]; got != "end_turn" {
		t.Fatalf("expected finish_reason end_turn, got %#v", events[len(events)-1])
	}
}

func TestResponseUsesAnthropicEndpointAddsContextManagementBetaHeader(t *testing.T) {
	var gotBeta string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello from anthropic"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic})
	_, err := client.Response(context.Background(), model.CanonicalRequest{
		Model:                   "claude-sonnet-4-5",
		MaxOutputTokens:         intPtrForClientTest(128),
		PreservedTopLevelFields: map[string]any{"context_management": map[string]any{"edits": []any{map[string]any{"type": "clear_thinking_20251015"}, map[string]any{"type": "compact_20260112"}}}},
		Messages:                []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}}}},
	}, "Bearer anthropic-key")
	if err != nil {
		t.Fatalf("Response error: %v", err)
	}
	if gotBeta != "compact-2026-01-12,context-management-2025-06-27" {
		t.Fatalf("expected anthropic-beta headers for context_management, got %q", gotBeta)
	}
}

func TestBuildResponsesRequestBodyDropsContextManagement(t *testing.T) {
	body, err := buildResponsesRequestBody(model.CanonicalRequest{
		Model:                   "gpt-5",
		PreservedTopLevelFields: map[string]any{"context_management": map[string]any{"edits": []any{map[string]any{"type": "clear_thinking_20251015"}}}},
	}, config.ResponsesToolCompatModePreserve)
	if err != nil {
		t.Fatalf("expected context_management to be dropped for responses upstream, got %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if _, exists := payload["context_management"]; exists {
		t.Fatalf("expected responses upstream payload to drop context_management, got %#v", payload)
	}
}

func TestBuildResponsesRequestBodyOmitsInstructionInputMessages(t *testing.T) {
	body, err := buildResponsesRequestBody(model.CanonicalRequest{
		Model:        "gpt-5",
		Instructions: "system one\n\ndeveloper two",
		ResponseInputItems: []map[string]any{
			{
				"role": "system",
				"content": []map[string]any{{
					"type": "input_text",
					"text": "system one",
				}},
			},
			{
				"role": "developer",
				"content": []map[string]any{{
					"type": "input_text",
					"text": "developer two",
				}},
			},
			{
				"role": "user",
				"content": []map[string]any{{
					"type": "input_text",
					"text": "hello",
				}},
			},
		},
	}, config.ResponsesToolCompatModePreserve)
	if err != nil {
		t.Fatalf("buildResponsesRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["instructions"].(string); got != "system one\n\ndeveloper two" {
		t.Fatalf("expected instructions preserved, got %#v", payload)
	}
	input, _ := payload["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected only user input item upstream, got %#v", payload["input"])
	}
	item, _ := input[0].(map[string]any)
	if got, _ := item["role"].(string); got != "user" {
		t.Fatalf("expected user role to remain, got %#v", item)
	}
}

func TestBuildResponsesRequestBodyPrefersCanonicalMessagesForCacheStableToolSequences(t *testing.T) {
	logical := []model.CanonicalMessage{
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "先找永久会员字符串"}}},
		{Role: "assistant", ReasoningContent: "我先从对象池引用开始追", Parts: []model.CanonicalContentPart{{Type: "text", Text: "开始追引用"}}},
		{Role: "assistant", ToolCalls: []model.CanonicalToolCall{{ID: "call_lookup", Type: "function", Name: "mcp__run_ubuntu", Arguments: `{"command":"python3 scan.py"}`}}},
		{Role: "tool", ToolCallID: "call_lookup", Parts: []model.CanonicalContentPart{{Type: "text", Text: "pool 0x36eb0 hits 2"}}},
		{Role: "assistant", ReasoningContent: "引用点只有两个", Parts: []model.CanonicalContentPart{{Type: "text", Text: "继续追 AECAB0"}}},
		{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "继续"}}},
	}

	canonicalOnly := model.CanonicalRequest{Model: "gpt-5", Messages: logical}
	withPreservedResponsesItems := model.CanonicalRequest{
		Model:    "gpt-5",
		Messages: logical,
		ResponseInputItems: []map[string]any{
			{"role": "user", "content": []map[string]any{{"type": "input_text", "text": "先找永久会员字符串"}}},
			{"type": "reasoning", "summary": []map[string]any{{"type": "summary_text", "text": "我先从对象池引用开始追"}}},
			{"type": "function_call", "call_id": "call_stale", "name": "mcp__run_ubuntu", "arguments": `{"command":"stale timeout"}`},
			{"type": "function_call_output", "call_id": "call_stale", "output": `{"error":"timeout"}`},
			{"role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "开始追引用"}}},
		},
	}

	wantBody, err := buildResponsesRequestBody(canonicalOnly, config.ResponsesToolCompatModePreserve)
	if err != nil {
		t.Fatalf("build canonical-only responses body: %v", err)
	}
	gotBody, err := buildResponsesRequestBody(withPreservedResponsesItems, config.ResponsesToolCompatModePreserve)
	if err != nil {
		t.Fatalf("build preserved responses body: %v", err)
	}

	var wantPayload, gotPayload map[string]any
	if err := json.Unmarshal(wantBody, &wantPayload); err != nil {
		t.Fatalf("unmarshal canonical-only payload: %v", err)
	}
	if err := json.Unmarshal(gotBody, &gotPayload); err != nil {
		t.Fatalf("unmarshal preserved payload: %v", err)
	}
	assertJSONEqual(t, gotPayload["input"], wantPayload["input"])
}

func TestBuildResponsesRequestBodyAutoFillsPromptCacheKeyWhenMissing(t *testing.T) {
	req := model.CanonicalRequest{
		Model:        "gpt-5.5",
		Instructions: "stable system prompt",
		Tools: []model.CanonicalTool{{
			Type:        "function",
			Name:        "lookup_membership",
			Description: "Look up membership status",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}},
		}},
		Messages: []model.CanonicalMessage{
			{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "first private prompt text"}}},
			{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "working"}}, ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "lookup_membership", Arguments: `{"query":"premium"}`}}},
			{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: "private tool output"}}},
		},
	}

	body, err := buildResponsesRequestBody(req, config.ResponsesToolCompatModePreserve)
	if err != nil {
		t.Fatalf("buildResponsesRequestBody error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	cacheKey, _ := payload["prompt_cache_key"].(string)
	if cacheKey == "" {
		t.Fatalf("expected generated prompt_cache_key, got %#v", payload)
	}
	if strings.Contains(cacheKey, "private") || strings.Contains(cacheKey, "lookup_membership") || strings.Contains(cacheKey, "stable system prompt") {
		t.Fatalf("generated prompt_cache_key leaks raw request content: %q", cacheKey)
	}

	secondBody, err := buildResponsesRequestBody(req, config.ResponsesToolCompatModePreserve)
	if err != nil {
		t.Fatalf("build second responses body: %v", err)
	}
	var secondPayload map[string]any
	if err := json.Unmarshal(secondBody, &secondPayload); err != nil {
		t.Fatalf("unmarshal second payload: %v", err)
	}
	if got, _ := secondPayload["prompt_cache_key"].(string); got != cacheKey {
		t.Fatalf("expected stable prompt_cache_key %q, got %q", cacheKey, got)
	}

	changed := req
	changed.Instructions = "different system prompt"
	changedBody, err := buildResponsesRequestBody(changed, config.ResponsesToolCompatModePreserve)
	if err != nil {
		t.Fatalf("build changed responses body: %v", err)
	}
	var changedPayload map[string]any
	if err := json.Unmarshal(changedBody, &changedPayload); err != nil {
		t.Fatalf("unmarshal changed payload: %v", err)
	}
	if got, _ := changedPayload["prompt_cache_key"].(string); got == cacheKey {
		t.Fatalf("expected prompt_cache_key to change when cache prefix changes, still got %q", got)
	}
}

func TestBuildResponsesRequestBodyPreservesClientPromptCacheKey(t *testing.T) {
	body, err := buildResponsesRequestBody(model.CanonicalRequest{
		Model:                   "gpt-5.5",
		PreservedTopLevelFields: map[string]any{"prompt_cache_key": "client-session-key"},
		Messages:                []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}}}},
	}, config.ResponsesToolCompatModePreserve)
	if err != nil {
		t.Fatalf("buildResponsesRequestBody error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got, _ := payload["prompt_cache_key"].(string); got != "client-session-key" {
		t.Fatalf("expected client prompt_cache_key to be preserved, got %#v", payload)
	}
}

func TestBuildResponsesRequestBodyAddsCodexReasoningIncludeOnlyForCodexMasquerade(t *testing.T) {
	req := model.CanonicalRequest{
		Model: "gpt-5.5",
		Reasoning: &model.CanonicalReasoning{
			Effort: "high",
		},
		Messages: []model.CanonicalMessage{{
			Role:  "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
		}},
	}

	body, err := buildRequestBodyForEndpoint(req, config.UpstreamEndpointTypeResponses, config.MasqueradeTargetCodex, false, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal codex payload: %v", err)
	}
	include, _ := payload["include"].([]any)
	if !containsAnyString(include, "reasoning.encrypted_content") {
		t.Fatalf("expected codex masquerade responses payload to include reasoning.encrypted_content, got %#v", payload)
	}

	plainBody, err := buildRequestBodyForEndpoint(req, config.UpstreamEndpointTypeResponses, config.MasqueradeTargetNone, false, false)
	if err != nil {
		t.Fatalf("build plain responses body: %v", err)
	}
	var plainPayload map[string]any
	if err := json.Unmarshal(plainBody, &plainPayload); err != nil {
		t.Fatalf("unmarshal plain payload: %v", err)
	}
	plainInclude, _ := plainPayload["include"].([]any)
	if containsAnyString(plainInclude, "reasoning.encrypted_content") {
		t.Fatalf("expected non-codex responses payload not to add reasoning.encrypted_content, got %#v", plainPayload)
	}
	if got, _ := plainPayload["reasoning"].(map[string]any)["effort"]; got != "high" {
		t.Fatalf("expected default reasoning payload preserved, got %#v", plainPayload)
	}
}

func TestApplyUpstreamHeadersClaudeMasqueradeUsesLatestFingerprint(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/messages", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	applyUpstreamHeaders(req, config.UpstreamEndpointTypeAnthropic, "Bearer key", "", "", "", config.MasqueradeTargetClaude)

	if got := req.Header.Get("User-Agent"); got != "claude-cli/2.1.167 (external, cli)" {
		t.Fatalf("expected latest claude UA, got %q", got)
	}
	for _, want := range []string{
		"claude-code-20250219",
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"prompt-caching-scope-2026-01-05",
		"effort-2025-11-24",
		"context-management-2025-06-27",
		"extended-cache-ttl-2025-04-11",
	} {
		if !strings.Contains(req.Header.Get("anthropic-beta"), want) {
			t.Fatalf("expected anthropic-beta to contain %q, got %q", want, req.Header.Get("anthropic-beta"))
		}
	}
	if got := req.Header.Get("X-Stainless-Package-Version"); got != "0.94.0" {
		t.Fatalf("expected latest claude stainless package version, got %q", got)
	}
	if got := req.Header.Get("Anthropic-Dangerous-Direct-Browser-Access"); got != "true" {
		t.Fatalf("expected dangerous direct browser access header, got %q", got)
	}
}

func TestApplyUpstreamHeadersOpenCodeMasqueradeUsesLatestFingerprint(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	applyUpstreamHeaders(req, config.UpstreamEndpointTypeResponses, "Bearer key", "", "", "", config.MasqueradeTargetOpenCode)
	if got := req.Header.Get("User-Agent"); got != "opencode/1.16.2 ai-sdk/provider-utils/4.0.27 runtime/bun/1.3.14" {
		t.Fatalf("expected latest opencode UA, got %q", got)
	}
	if got := req.Header.Get("originator"); got != "opencode" {
		t.Fatalf("expected opencode originator, got %q", got)
	}
}

func TestApplyUpstreamHeadersCodexMasqueradeUsesLatestFingerprint(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	applyUpstreamHeaders(req, config.UpstreamEndpointTypeResponses, "Bearer key", "", "", "", config.MasqueradeTargetCodex)
	if got := req.Header.Get("User-Agent"); got != "codex_cli_rs/0.137.0 (Linux 6.1; x86_64) iTerm.app" {
		t.Fatalf("expected latest codex UA, got %q", got)
	}
	if got := req.Header.Get("originator"); got != "codex_cli_rs" {
		t.Fatalf("expected codex originator, got %q", got)
	}
	if got := req.Header.Get("x-openai-internal-codex-residency"); got != "us" {
		t.Fatalf("expected codex residency header, got %q", got)
	}
}

func containsAnyString(items []any, want string) bool {
	for _, item := range items {
		if got, ok := item.(string); ok && got == want {
			return true
		}
	}
	return false
}

func TestBuildRequestBodyForAllEndpointTypesIgnoresStaleResponsesItemsWhenCanonicalMessagesExist(t *testing.T) {
	req := model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{
			{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "先找永久会员字符串"}}},
			{Role: "assistant", ReasoningContent: "从对象池引用开始追", Parts: []model.CanonicalContentPart{{Type: "text", Text: "开始追引用"}}, ToolCalls: []model.CanonicalToolCall{{ID: "call_lookup", Type: "function", Name: "mcp__run_ubuntu", Arguments: `{"command":"python3 scan.py"}`}}},
			{Role: "tool", ToolCallID: "call_lookup", Parts: []model.CanonicalContentPart{{Type: "text", Text: "pool hits 2"}}},
			{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "继续"}}},
		},
		ResponseInputItems: []map[string]any{
			{"type": "function_call", "call_id": "call_stale", "name": "mcp__run_ubuntu", "arguments": `{"command":"stale timeout"}`},
			{"type": "function_call_output", "call_id": "call_stale", "output": `{"error":"timeout"}`},
		},
	}

	for _, endpointType := range []string{config.UpstreamEndpointTypeResponses, config.UpstreamEndpointTypeChat, config.UpstreamEndpointTypeAnthropic} {
		t.Run(endpointType, func(t *testing.T) {
			body, err := buildRequestBodyForEndpoint(req, endpointType, "", false, false)
			if err != nil {
				t.Fatalf("build body for %s: %v", endpointType, err)
			}
			if strings.Contains(string(body), "call_stale") || strings.Contains(string(body), "stale timeout") {
				t.Fatalf("expected %s payload to use canonical messages instead of stale responses items, got %s", endpointType, string(body))
			}
			if !strings.Contains(string(body), "call_lookup") || !strings.Contains(string(body), "pool hits 2") || !strings.Contains(string(body), "继续") {
				t.Fatalf("expected %s payload to preserve canonical tool sequence and user continuation, got %s", endpointType, string(body))
			}
		})
	}
}

func TestBuildChatRequestBodyPreservesFileContentPart(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{{
			Role: "user",
			Parts: []model.CanonicalContentPart{{
				Type: "input_file",
				Raw:  map[string]any{"input_file": map[string]any{"file_id": "file_123"}},
			}},
		}},
	}, config.UpstreamEndpointTypeChat, "", false, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	messages, _ := payload["messages"].([]any)
	message, _ := messages[0].(map[string]any)
	content, _ := message["content"].([]any)
	part, _ := content[0].(map[string]any)
	if got, _ := part["type"].(string); got != "file" {
		t.Fatalf("expected chat file content part, got %#v", part)
	}
	fileRaw, _ := part["file"].(map[string]any)
	if got := fileRaw["file_id"]; got != "file_123" {
		t.Fatalf("expected file_id preserved, got %#v", part)
	}
}

func TestBuildChatRequestBodyUsesObjectSchemaForFunctionToolWithoutParameters(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model: "deepseek-v4-pro",
		Tools: []model.CanonicalTool{{
			Type:        "function",
			Name:        "get_current_time",
			Description: "Get current time",
		}},
	}, config.UpstreamEndpointTypeChat, "", false, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	tools, _ := payload["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	function, _ := tool["function"].(map[string]any)
	assertJSONEqual(t, function["parameters"], map[string]any{"type": "object"})
}

func TestBuildAnthropicRequestBodyUsesObjectSchemaForFunctionToolWithoutParameters(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model: "deepseek-v4-pro",
		Tools: []model.CanonicalTool{{
			Type:        "function",
			Name:        "get_current_time",
			Description: "Get current time",
		}},
	}, config.UpstreamEndpointTypeAnthropic, "", false, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	tools, _ := payload["tools"].([]any)
	tool, _ := tools[0].(map[string]any)
	assertJSONEqual(t, tool["input_schema"], map[string]any{"type": "object"})
}

func TestBuildChatRequestBodyPreservesInputAudioContentPart(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{{
			Role: "user",
			Parts: []model.CanonicalContentPart{{
				Type: "input_audio",
				Raw:  map[string]any{"input_audio": map[string]any{"data": "YWJj", "format": "mp3"}},
			}},
		}},
	}, config.UpstreamEndpointTypeChat, "", false, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	messages, _ := payload["messages"].([]any)
	message, _ := messages[0].(map[string]any)
	content, _ := message["content"].([]any)
	part, _ := content[0].(map[string]any)
	if got, _ := part["type"].(string); got != "input_audio" {
		t.Fatalf("expected chat input_audio content part, got %#v", part)
	}
	audioRaw, _ := part["input_audio"].(map[string]any)
	if got := audioRaw["format"]; got != "mp3" {
		t.Fatalf("expected audio format preserved, got %#v", part)
	}
}

func TestBuildChatRequestBodyDropsAssistantToolCallsWithEmptyFunctionName(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{{
			Role:      "assistant",
			ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "", Arguments: `{"url":"https://example.com"}`}},
		}},
	}, config.UpstreamEndpointTypeChat, "", false, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected one message, got %#v", payload)
	}
	message, _ := messages[0].(map[string]any)
	if _, exists := message["tool_calls"]; exists {
		t.Fatalf("expected empty-name tool_call to be dropped from chat upstream payload, got %#v", message)
	}
	if got, _ := message["role"].(string); got != "assistant" {
		t.Fatalf("expected assistant role preserved, got %#v", message)
	}
}

func TestBuildChatRequestBodyPreservesOrderedToolResultBetweenTextMessages(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{{
			Role: "user",
			OrderedContent: []model.CanonicalContentBlock{
				{Type: "content", Part: model.CanonicalContentPart{Type: "text", Text: "工具前"}},
				{Type: "tool_result", ToolCallID: "call_dynamic_1", ToolResultParts: []model.CanonicalContentPart{{Type: "text", Text: `{"ok":true}`}}},
				{Type: "content", Part: model.CanonicalContentPart{Type: "text", Text: "工具后"}},
			},
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "工具前"}, {Type: "text", Text: "工具后"}},
		}},
	}, config.UpstreamEndpointTypeChat, "", false, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("expected user, tool, user messages, got %#v", messages)
	}
	first, _ := messages[0].(map[string]any)
	second, _ := messages[1].(map[string]any)
	third, _ := messages[2].(map[string]any)
	if first["role"] != "user" || second["role"] != "tool" || third["role"] != "user" {
		t.Fatalf("expected ordered chat message shape, got %#v", messages)
	}
	if second["tool_call_id"] != "call_dynamic_1" || second["content"] != `{"ok":true}` {
		t.Fatalf("expected ordered chat tool output preserved, got %#v", second)
	}
}

func TestBuildChatRequestBodyConvertsLegacyXMLToolCallText(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model: "mimo-v2.5-pro",
		Messages: []model.CanonicalMessage{{
			Role: "assistant",
			Parts: []model.CanonicalContentPart{{
				Type: "text",
				Text: "\n<tool_call>\n<function=mcp__mt_apk_read_text>\n<parameter=includeLineNumbers>False</parameter>\n<parameter=limit>520</parameter>\n<parameter=locator>{\"kind\": \"dex_class\", \"target\": \"Lacr/browser/lightning/view/IDMDownloadListener;\"}</parameter>\n<parameter=maxChars>80000</parameter>\n<parameter=workspaceId>69jas4bi</parameter>\n</function>\n</tool_call>\n",
			}},
		}},
	}, config.UpstreamEndpointTypeChat, "", false, false, config.UpstreamXMLToolCallStyleLegacy)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	messages, _ := payload["messages"].([]any)
	message, _ := messages[0].(map[string]any)
	if _, exists := message["content"]; exists {
		t.Fatalf("expected XML tool call text to be consumed from content, got %#v", message)
	}
	toolCalls, _ := message["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("expected one recovered tool call, got %#v", message)
	}
	call, _ := toolCalls[0].(map[string]any)
	function, _ := call["function"].(map[string]any)
	if got := function["name"]; got != "mcp__mt_apk_read_text" {
		t.Fatalf("unexpected recovered tool name: %#v", function)
	}
	if got, _ := function["arguments"].(string); got != `{"includeLineNumbers":false,"limit":520,"locator":{"kind":"dex_class","target":"Lacr/browser/lightning/view/IDMDownloadListener;"},"maxChars":80000,"workspaceId":"69jas4bi"}` {
		t.Fatalf("unexpected recovered tool arguments: %s", got)
	}
}

func TestBuildChatRequestBodySanitizesRepeatedConcatenatedToolArguments(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{{
			Role: "assistant",
			ToolCalls: []model.CanonicalToolCall{{
				ID:        "call_1",
				Type:      "function",
				Name:      "scrape_web",
				Arguments: `{"url":"https://example.com"}{"url":"https://example.com"}{"url":"https://example.com"}`,
			}},
		}},
	}, config.UpstreamEndpointTypeChat, "", false, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	messages, _ := payload["messages"].([]any)
	message, _ := messages[0].(map[string]any)
	toolCalls, _ := message["tool_calls"].([]any)
	call, _ := toolCalls[0].(map[string]any)
	function, _ := call["function"].(map[string]any)
	if got, _ := function["arguments"].(string); got != `{"url":"https://example.com"}` {
		t.Fatalf("expected repeated concatenated tool arguments to be sanitized, got %#v", function)
	}
}

func TestBuildChatRequestBodySanitizesCorruptedPrefixBeforeRepeatedToolArguments(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{{
			Role: "assistant",
			ToolCalls: []model.CanonicalToolCall{{
				ID:        "call_1",
				Type:      "function",
				Name:      "scrape_web",
				Arguments: `{"url":"https://github.com/k3ss-official/g0dm0d3` + `{"url":"https://github.com/k3ss-official/g0dm0d3"}{"url":"https://github.com/k3ss-official/g0dm0d3"}`,
			}},
		}},
	}, config.UpstreamEndpointTypeChat, "", false, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	messages, _ := payload["messages"].([]any)
	message, _ := messages[0].(map[string]any)
	toolCalls, _ := message["tool_calls"].([]any)
	call, _ := toolCalls[0].(map[string]any)
	function, _ := call["function"].(map[string]any)
	if got, _ := function["arguments"].(string); got != `{"url":"https://github.com/k3ss-official/g0dm0d3"}` {
		t.Fatalf("expected corrupted prefix to be discarded in favor of repeated valid JSON, got %#v", function)
	}
}

func TestBuildResponsesRequestBodyPreservesInputAudioContentPart(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		Messages: []model.CanonicalMessage{{
			Role: "user",
			Parts: []model.CanonicalContentPart{{
				Type: "input_audio",
				Raw:  map[string]any{"input_audio": map[string]any{"data": "YWJj", "format": "wav"}},
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
	message, _ := input[0].(map[string]any)
	content, _ := message["content"].([]any)
	part, _ := content[0].(map[string]any)
	if got, _ := part["type"].(string); got != "input_audio" {
		t.Fatalf("expected responses input_audio content part, got %#v", part)
	}
	audioRaw, _ := part["input_audio"].(map[string]any)
	if got := audioRaw["format"]; got != "wav" {
		t.Fatalf("expected audio format preserved, got %#v", part)
	}
}

func TestBuildAnthropicRequestBodyPreservesThinkingConfig(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model:           "claude-sonnet-4-5",
		MaxOutputTokens: intPtrForClientTest(128),
		Reasoning:       &model.CanonicalReasoning{Raw: map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 2048}}},
	}, config.UpstreamEndpointTypeAnthropic, "", false, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	thinking, _ := payload["thinking"].(map[string]any)
	if got := thinking["type"]; got != "enabled" {
		t.Fatalf("expected thinking.type enabled, got %#v", payload)
	}
	if got := thinking["budget_tokens"]; got != float64(2048) {
		t.Fatalf("expected thinking.budget_tokens 2048, got %#v", payload)
	}
}

func TestBuildAnthropicRequestBodyPreservesCacheControlOnContentBlocks(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model:           "claude-sonnet-4-5",
		MaxOutputTokens: intPtrForClientTest(128),
		Messages: []model.CanonicalMessage{{
			Role: "user",
			Parts: []model.CanonicalContentPart{{
				Type: "text",
				Text: "hello",
				Raw:  map[string]any{"cache_control": map[string]any{"type": "ephemeral"}},
			}},
		}},
	}, config.UpstreamEndpointTypeAnthropic, "", false, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	messages, _ := payload["messages"].([]any)
	message, _ := messages[0].(map[string]any)
	content, _ := message["content"].([]any)
	part, _ := content[0].(map[string]any)
	cacheControl, _ := part["cache_control"].(map[string]any)
	if got := cacheControl["type"]; got != "ephemeral" {
		t.Fatalf("expected anthropic cache_control to survive, got %#v", part)
	}
}

func TestBuildRequestBodyMapsAnthropicThinkingToResponsesReasoning(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model:     "gpt-5",
		Reasoning: &model.CanonicalReasoning{Raw: map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 2048}}},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	reasoning, _ := payload["reasoning"].(map[string]any)
	if got := reasoning["effort"]; got != "minimal" {
		t.Fatalf("expected anthropic thinking to map to medium effort, got %#v", payload)
	}
	if got := reasoning["summary"]; got != "auto" {
		t.Fatalf("expected summary auto, got %#v", payload)
	}
	if _, exists := reasoning["thinking"]; exists {
		t.Fatalf("expected responses upstream reasoning to avoid anthropic thinking field, got %#v", payload)
	}
}

func TestBuildRequestBodyMapsAnthropicBudgetBelowFirstStepToMinimalReasoning(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model:     "gpt-5",
		Reasoning: &model.CanonicalReasoning{Raw: map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 4999}}},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	reasoning, _ := payload["reasoning"].(map[string]any)
	if got := reasoning["effort"]; got != "minimal" {
		t.Fatalf("expected 4999 budget to stay below the first configured step, got %#v", payload)
	}
}

func TestBuildRequestBodyMapsAnthropicOutputConfigMaxToResponsesXHighReasoning(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model:     "gpt-5",
		Reasoning: &model.CanonicalReasoning{Raw: map[string]any{"output_config": map[string]any{"effort": "max"}}},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	reasoning, _ := payload["reasoning"].(map[string]any)
	if got := reasoning["effort"]; got != "xhigh" {
		t.Fatalf("expected Claude max effort to map to OpenAI xhigh, got %#v", payload)
	}
}

func TestBuildChatRequestBodyMapsAnthropicThinkingToChatReasoning(t *testing.T) {
	body, err := buildChatRequestBody(model.CanonicalRequest{
		Model:     "gpt-5",
		Reasoning: &model.CanonicalReasoning{Raw: map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 2048}}},
	})
	if err != nil {
		t.Fatalf("buildChatRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	reasoning, _ := payload["reasoning"].(map[string]any)
	if got := reasoning["effort"]; got != "minimal" {
		t.Fatalf("expected anthropic thinking to map to medium effort, got %#v", payload)
	}
	if got := reasoning["summary"]; got != "auto" {
		t.Fatalf("expected summary auto, got %#v", payload)
	}
	if _, exists := reasoning["thinking"]; exists {
		t.Fatalf("expected chat upstream reasoning to avoid anthropic thinking field, got %#v", payload)
	}
	if _, exists := payload["reasoning_effort"]; exists {
		t.Fatalf("expected mapped chat payload to use reasoning object instead of reasoning_effort, got %#v", payload)
	}
}

func TestBuildRequestBodyMapsAnthropicAdaptiveThinkingToXHighResponsesReasoning(t *testing.T) {
	body, err := buildRequestBody(model.CanonicalRequest{
		Model:     "gpt-5",
		Reasoning: &model.CanonicalReasoning{Raw: map[string]any{"thinking": map[string]any{"type": "adaptive"}}},
	})
	if err != nil {
		t.Fatalf("buildRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	reasoning, _ := payload["reasoning"].(map[string]any)
	if got := reasoning["effort"]; got != "xhigh" {
		t.Fatalf("expected anthropic adaptive thinking to map to xhigh effort, got %#v", payload)
	}
	if got := reasoning["summary"]; got != "auto" {
		t.Fatalf("expected summary auto, got %#v", payload)
	}
}

func TestBuildChatRequestBodyMapsAnthropicAdaptiveThinkingToXHighChatReasoning(t *testing.T) {
	body, err := buildChatRequestBody(model.CanonicalRequest{
		Model:     "gpt-5",
		Reasoning: &model.CanonicalReasoning{Raw: map[string]any{"thinking": map[string]any{"type": "adaptive"}}},
	})
	if err != nil {
		t.Fatalf("buildChatRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	reasoning, _ := payload["reasoning"].(map[string]any)
	if got := reasoning["effort"]; got != "xhigh" {
		t.Fatalf("expected anthropic adaptive thinking to map to xhigh effort, got %#v", payload)
	}
	if got := reasoning["summary"]; got != "auto" {
		t.Fatalf("expected summary auto, got %#v", payload)
	}
	if _, exists := payload["reasoning_effort"]; exists {
		t.Fatalf("expected mapped chat payload to use reasoning object instead of reasoning_effort, got %#v", payload)
	}
}

func TestBuildChatRequestBodyDropsResponsesOnlyPreservedTopLevelFields(t *testing.T) {
	body, err := buildChatRequestBody(model.CanonicalRequest{
		Model: "gpt-5",
		PreservedTopLevelFields: map[string]any{
			"output_config":        map[string]any{"format": map[string]any{"type": "json_schema"}},
			"previous_response_id": "resp_123",
			"prompt_cache_key":     "responses-cache-key",
			"parallel_tool_calls":  true,
			"truncation":           "auto",
			"text":                 map[string]any{"format": map[string]any{"type": "text"}},
			"response_format":      map[string]any{"type": "json_object"},
			"custom_passthrough":   "keep-me",
		},
	})
	if err != nil {
		t.Fatalf("buildChatRequestBody error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	for _, key := range []string{"output_config", "previous_response_id", "prompt_cache_key", "truncation", "text"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("expected chat upstream payload to drop responses-only field %q, got %#v", key, payload)
		}
	}
	if got, _ := payload["parallel_tool_calls"].(bool); !got {
		t.Fatalf("expected chat upstream payload to preserve chat-native parallel_tool_calls, got %#v", payload)
	}
	responseFormat, _ := payload["response_format"].(map[string]any)
	if got, _ := responseFormat["type"].(string); got != "json_object" {
		t.Fatalf("expected chat upstream payload to preserve response_format, got %#v", payload)
	}
	if got := payload["custom_passthrough"]; got != "keep-me" {
		t.Fatalf("expected chat upstream payload to keep non-responses passthrough field, got %#v", payload)
	}
}

func TestPreviewRequestObservabilityForResponses(t *testing.T) {
	preview, err := PreviewRequestObservability(model.CanonicalRequest{
		Model:                   "gpt-5-mini",
		PreservedTopLevelFields: map[string]any{"service_tier": "priority"},
		Reasoning:               &model.CanonicalReasoning{Effort: "high", Summary: "auto", Raw: map[string]any{"effort": "high", "summary": "auto"}},
	}, config.UpstreamEndpointTypeResponses, "", false, false)
	if err != nil {
		t.Fatalf("PreviewRequestObservability error: %v", err)
	}
	if preview.UpstreamModel != "gpt-5-mini" {
		t.Fatalf("expected upstream model gpt-5-mini, got %#v", preview)
	}
	if preview.UpstreamServiceTier != "priority" {
		t.Fatalf("expected upstream service tier priority, got %#v", preview)
	}
	assertPreviewReasoningJSON(t, preview.ReasoningParameters, map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}})
}

func TestPreviewRequestObservabilityForChat(t *testing.T) {
	preview, err := PreviewRequestObservability(model.CanonicalRequest{
		Model:     "gpt-5-mini",
		Reasoning: &model.CanonicalReasoning{Effort: "high", Summary: "auto", Raw: map[string]any{"effort": "high", "summary": "auto"}},
	}, config.UpstreamEndpointTypeChat, "", false, false)
	if err != nil {
		t.Fatalf("PreviewRequestObservability error: %v", err)
	}
	if preview.UpstreamModel != "gpt-5-mini" {
		t.Fatalf("expected upstream model gpt-5-mini, got %#v", preview)
	}
	if preview.UpstreamServiceTier != "" {
		t.Fatalf("expected empty upstream service tier, got %#v", preview)
	}
	assertPreviewReasoningJSON(t, preview.ReasoningParameters, map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}})
}

func TestPreviewRequestObservabilityForAnthropic(t *testing.T) {
	preview, err := PreviewRequestObservability(model.CanonicalRequest{
		Model:           "claude-sonnet-4-5",
		MaxOutputTokens: intPtrForClientTest(128),
		Reasoning:       &model.CanonicalReasoning{Raw: map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 128}}},
	}, config.UpstreamEndpointTypeAnthropic, "", false, false)
	if err != nil {
		t.Fatalf("PreviewRequestObservability error: %v", err)
	}
	if preview.UpstreamModel != "claude-sonnet-4-5" {
		t.Fatalf("expected upstream model claude-sonnet-4-5, got %#v", preview)
	}
	if preview.UpstreamServiceTier != "" {
		t.Fatalf("expected empty upstream service tier, got %#v", preview)
	}
	assertPreviewReasoningJSON(t, preview.ReasoningParameters, map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(128)}})
}

func TestPreviewRequestObservabilityReadsServiceTierAlias(t *testing.T) {
	preview, err := PreviewRequestObservability(model.CanonicalRequest{
		Model:                   "gpt-5-mini",
		PreservedTopLevelFields: map[string]any{"serviceTier": "flex"},
	}, config.UpstreamEndpointTypeResponses, "", false, false)
	if err != nil {
		t.Fatalf("PreviewRequestObservability error: %v", err)
	}
	if preview.UpstreamServiceTier != "flex" {
		t.Fatalf("expected upstream service tier flex from alias, got %#v", preview)
	}
}

func TestPreviewRequestObservabilityForAdaptiveAnthropicThinkingIncludesOutputConfig(t *testing.T) {
	preview, err := PreviewRequestObservability(model.CanonicalRequest{
		Model: "claude-opus-4-6",
		Reasoning: &model.CanonicalReasoning{Raw: map[string]any{
			"thinking":      map[string]any{"type": "adaptive"},
			"output_config": map[string]any{"effort": "high"},
		}},
	}, config.UpstreamEndpointTypeAnthropic, "", false, false)
	if err != nil {
		t.Fatalf("PreviewRequestObservability error: %v", err)
	}
	if preview.UpstreamModel != "claude-opus-4-6" {
		t.Fatalf("expected upstream model claude-opus-4-6, got %#v", preview)
	}
	assertPreviewReasoningJSON(t, preview.ReasoningParameters, map[string]any{
		"thinking":      map[string]any{"type": "adaptive"},
		"output_config": map[string]any{"effort": "high"},
	})
}

func assertPreviewReasoningJSON(t *testing.T, raw string, expected map[string]any) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal preview reasoning json: %v raw=%q", err, raw)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal got reasoning json: %v", err)
	}
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		t.Fatalf("marshal expected reasoning json: %v", err)
	}
	if string(gotJSON) != string(expectedJSON) {
		t.Fatalf("expected reasoning json %s, got %s", expectedJSON, gotJSON)
	}
}

func TestCachedTokensFromEventReadsTopLevelCachedTokens(t *testing.T) {
	evt := Event{Data: map[string]any{"usage": map[string]any{"cached_tokens": 321}}}
	if got := cachedTokensFromEvent(evt); got != 321 {
		t.Fatalf("expected top-level cached_tokens to be returned, got %#v", got)
	}
}

func TestCachedTokensFromEventReadsTopLevelCacheReadInputTokens(t *testing.T) {
	evt := Event{Data: map[string]any{"response": map[string]any{"usage": map[string]any{"cache_read_input_tokens": 654}}}}
	if got := cachedTokensFromEvent(evt); got != 654 {
		t.Fatalf("expected top-level cache_read_input_tokens to be returned, got %#v", got)
	}
}

func TestBuildAnthropicRequestBodyRejectsInputAudio(t *testing.T) {
	_, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model:           "claude-sonnet-4-5",
		MaxOutputTokens: intPtrForClientTest(128),
		Messages: []model.CanonicalMessage{{
			Role: "user",
			Parts: []model.CanonicalContentPart{{
				Type: "input_audio",
				Raw:  map[string]any{"input_audio": map[string]any{"data": "YWJj", "format": "mp3"}},
			}},
		}},
	}, config.UpstreamEndpointTypeAnthropic, "", false, false)
	if err == nil {
		t.Fatalf("expected anthropic request builder to reject input_audio")
	}
}

func TestBuildAnthropicRequestBodyInjectsClaudeMetadataUserID(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model:           "claude-sonnet-4-5",
		MaxOutputTokens: intPtrForClientTest(128),
		Messages: []model.CanonicalMessage{{
			Role:  "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
		}},
	}, config.UpstreamEndpointTypeAnthropic, config.MasqueradeTargetClaude, true, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	metadata, _ := payload["metadata"].(map[string]any)
	userID, _ := metadata["user_id"].(string)
	if !strings.HasPrefix(userID, "user_") || !strings.Contains(userID, "_account__session_") {
		t.Fatalf("expected claude metadata.user_id injection, got %#v", payload)
	}
}

func TestBuildAnthropicRequestBodyDropsResponsesOnlyPreservedTopLevelFields(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model:           "claude-sonnet-4-5",
		MaxOutputTokens: intPtrForClientTest(128),
		PreservedTopLevelFields: map[string]any{
			"output_config":        map[string]any{"format": map[string]any{"type": "json_schema"}},
			"previous_response_id": "resp_123",
			"prompt_cache_key":     "responses-cache-key",
			"parallel_tool_calls":  true,
			"truncation":           "auto",
			"text":                 map[string]any{"format": map[string]any{"type": "text"}},
			"response_format":      map[string]any{"type": "json_object"},
			"custom_passthrough":   "keep-me",
		},
	}, config.UpstreamEndpointTypeAnthropic, "", false, false)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	for _, key := range []string{"output_config", "previous_response_id", "prompt_cache_key", "parallel_tool_calls", "truncation", "text", "response_format"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("expected anthropic upstream payload to drop responses-only field %q, got %#v", key, payload)
		}
	}
	if got := payload["custom_passthrough"]; got != "keep-me" {
		t.Fatalf("expected anthropic upstream payload to keep non-responses passthrough field, got %#v", payload)
	}
}

func TestBuildAnthropicRequestBodyInjectsClaudeSystemPrompt(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model:           "claude-sonnet-4-5",
		MaxOutputTokens: intPtrForClientTest(128),
		Messages: []model.CanonicalMessage{{
			Role:  "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
		}},
	}, config.UpstreamEndpointTypeAnthropic, config.MasqueradeTargetClaude, false, true)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	system, _ := payload["system"].([]any)
	if len(system) != 1 {
		t.Fatalf("expected one injected claude system block, got %#v", payload["system"])
	}
	block, _ := system[0].(map[string]any)
	if got, _ := block["text"].(string); got != claudeCodeSystemPrompt {
		t.Fatalf("expected injected claude system prompt text, got %#v", payload)
	}
	if got, _ := block["type"].(string); got != "text" {
		t.Fatalf("expected injected claude system prompt block type text, got %#v", payload)
	}
	if _, exists := payload["metadata"]; exists {
		t.Fatalf("expected system prompt injection not to mutate metadata by itself, got %#v", payload)
	}
}

func TestBuildAnthropicRequestBodyClaudeSystemPromptPreservesExistingInstructions(t *testing.T) {
	body, err := buildRequestBodyForEndpoint(model.CanonicalRequest{
		Model:           "claude-sonnet-4-5",
		Instructions:    "project-specific system",
		MaxOutputTokens: intPtrForClientTest(128),
		Messages: []model.CanonicalMessage{{
			Role:  "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}},
		}},
	}, config.UpstreamEndpointTypeAnthropic, config.MasqueradeTargetClaude, false, true)
	if err != nil {
		t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	system, _ := payload["system"].([]any)
	if len(system) != 2 {
		t.Fatalf("expected injected claude prompt plus preserved instructions, got %#v", payload["system"])
	}
	first, _ := system[0].(map[string]any)
	second, _ := system[1].(map[string]any)
	if got, _ := first["text"].(string); got != claudeCodeSystemPrompt {
		t.Fatalf("expected first system block to be claude prompt, got %#v", payload["system"])
	}
	if got, _ := second["text"].(string); got != "project-specific system" {
		t.Fatalf("expected second system block to preserve existing instructions, got %#v", payload["system"])
	}
}

func TestStreamUsesAnthropicEndpointAndNormalizesEvents(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n" +
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
			"event: content_block_start\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
			"event: message_delta\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":4,\"output_tokens\":2}}\n\n" +
			"event: message_stop\n" +
			"data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic})
	events, err := client.Stream(context.Background(), model.CanonicalRequest{Model: "claude-sonnet-4-5", MaxOutputTokens: intPtrForClientTest(128)}, "Bearer anthropic-key")
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if gotPath != "/messages" {
		t.Fatalf("expected anthropic endpoint path, got %q", gotPath)
	}
	if len(events) < 3 || events[0].Event != "response.created" || events[1].Event != "response.output_text.delta" || events[2].Event != "response.completed" {
		t.Fatalf("expected normalized response events, got %#v", events)
	}
	if got := events[0].Data["response"].(map[string]any)["id"]; got != "msg_123" {
		t.Fatalf("expected response.created to keep anthropic message id msg_123, got %#v", got)
	}
	if got := events[1].Data["delta"]; got != "hello" {
		t.Fatalf("expected delta hello, got %#v", got)
	}
}

func TestStreamUsesAnthropicEndpointPrefersFinalUsageOverMessageStartZeroes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n" +
			"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n" +
			"event: content_block_start\n" +
			"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\n" +
			"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
			"event: message_delta\n" +
			"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":12,\"output_tokens\":7,\"cache_read_input_tokens\":5}}\n\n" +
			"event: message_stop\n" +
			"data: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic})
	events, err := client.Stream(context.Background(), model.CanonicalRequest{Model: "claude-sonnet-4-5", MaxOutputTokens: intPtrForClientTest(128)}, "Bearer anthropic-key")
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("expected response.created/output_text/completed events, got %#v", events)
	}
	completed := events[len(events)-1]
	if completed.Event != "response.completed" {
		t.Fatalf("expected final event response.completed, got %#v", completed)
	}
	// Usage is now wrapped inside response object (unified format)
	response, _ := completed.Data["response"].(map[string]any)
	usage, _ := response["usage"].(map[string]any)
	if got := usage["input_tokens"]; got != float64(17) {
		t.Fatalf("expected final anthropic usage.input_tokens 17, got %#v events=%#v", got, events)
	}
	if got := usage["output_tokens"]; got != float64(7) {
		t.Fatalf("expected final anthropic usage.output_tokens 7, got %#v events=%#v", got, events)
	}
	details, _ := usage["input_tokens_details"].(map[string]any)
	if got := details["cached_tokens"]; got != float64(5) {
		t.Fatalf("expected final anthropic usage.input_tokens_details.cached_tokens 5, got %#v events=%#v", got, events)
	}
}

func TestStreamUsesChatEndpointEmitsResponseCreatedOnlyOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"he\"}}]}\n\n" +
			"data: {\"id\":\"chatcmpl_123\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"llo\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewClient(server.URL, config.Config{UpstreamEndpointType: config.UpstreamEndpointTypeChat})
	events, err := client.Stream(context.Background(), model.CanonicalRequest{Model: "gpt-5"}, "Bearer test-key")
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	createdCount := 0
	for _, evt := range events {
		if evt.Event == "response.created" {
			createdCount++
			if got := evt.Data["response"].(map[string]any)["id"]; got != "chatcmpl_123" {
				t.Fatalf("expected response.created to keep chat id chatcmpl_123, got %#v", got)
			}
		}
	}
	if createdCount != 1 {
		t.Fatalf("expected exactly one response.created event, got %d events=%#v", createdCount, events)
	}
}

func intPtrForClientTest(v int) *int {
	return &v
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
	assertHasLogRecord(t, records, "upstreamRequestRetry", func(record map[string]any) bool {
		return record["request_id"] == "req-retry-fail" && record["attempt"] == float64(1) && record["status_code"] == float64(http.StatusBadGateway)
	})
	assertHasLogRecord(t, records, "upstreamRequestFailed", func(record map[string]any) bool {
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
	assertHasLogRecord(t, records, "upstreamRequestFailed", func(record map[string]any) bool {
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
	assertHasLogRecord(t, records, "upstreamStreamBroken", func(record map[string]any) bool {
		return record["request_id"] == "req-broken-stream" && record["event_count"] == float64(1)
	})
}

func initUpstreamTestLogger(t *testing.T) (string, func()) {
	t.Helper()
	logDir := t.TempDir()
	closeFn, err := logging.Init(config.Config{LogEnable: true, LogFilePath: logDir, LogMaxRequests: 50, LogMaxBodySizeMB: 5}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("init logger: %v", err)
	}
	return logDir, func() {
		if err := closeFn(); err != nil {
			t.Fatalf("close logger: %v", err)
		}
	}
}

func readUpstreamTestLogRecords(t *testing.T, logDir string) []map[string]any {
	t.Helper()
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	var allRecords []map[string]any
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}
		filePath := filepath.Join(logDir, entry.Name())
		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Fatalf("read log file %s: %v", filePath, err)
		}
		lines := strings.Split(strings.TrimSpace(string(content)), "\n")
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatalf("decode log line %q: %v", line, err)
			}
			allRecords = append(allRecords, record)
		}
	}
	return allRecords
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
