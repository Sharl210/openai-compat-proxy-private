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

func TestResponseUsesAnthropicEndpointHeadersAndNormalizesPayload(t *testing.T) {
	var gotPath string
	var gotAPIKey string
	var gotVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
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
	if got := reasoning["effort"]; got != "medium" {
		t.Fatalf("expected anthropic thinking to map to medium effort, got %#v", payload)
	}
	if got := reasoning["summary"]; got != "auto" {
		t.Fatalf("expected summary auto, got %#v", payload)
	}
	if _, exists := reasoning["thinking"]; exists {
		t.Fatalf("expected responses upstream reasoning to avoid anthropic thinking field, got %#v", payload)
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
	if got := reasoning["effort"]; got != "medium" {
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

func TestPreviewRequestObservabilityForResponses(t *testing.T) {
	preview, err := PreviewRequestObservability(model.CanonicalRequest{
		Model:     "gpt-5-mini",
		Reasoning: &model.CanonicalReasoning{Effort: "high", Summary: "auto", Raw: map[string]any{"effort": "high", "summary": "auto"}},
	}, config.UpstreamEndpointTypeResponses, "", false, false)
	if err != nil {
		t.Fatalf("PreviewRequestObservability error: %v", err)
	}
	if preview.UpstreamModel != "gpt-5-mini" {
		t.Fatalf("expected upstream model gpt-5-mini, got %#v", preview)
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
	assertPreviewReasoningJSON(t, preview.ReasoningParameters, map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(128)}})
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
	if got, _ := payload["system"].(string); got != claudeCodeSystemPrompt {
		t.Fatalf("expected claude system prompt injection, got %#v", payload)
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
	if got := usage["input_tokens"]; got != float64(12) {
		t.Fatalf("expected final anthropic usage.input_tokens 12, got %#v events=%#v", got, events)
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
