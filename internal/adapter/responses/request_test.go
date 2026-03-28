package responses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

func TestDecodeRequestAcceptsStringInputAsSingleUserMessage(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":"hello"
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.Messages) != 1 {
		t.Fatalf("expected one canonical message, got %#v", canon.Messages)
	}
	if canon.Messages[0].Role != "user" {
		t.Fatalf("expected user role, got %#v", canon.Messages[0])
	}
	if len(canon.Messages[0].Parts) != 1 || canon.Messages[0].Parts[0].Text != "hello" {
		t.Fatalf("expected single text part hello, got %#v", canon.Messages[0])
	}
	if len(canon.ResponseInputItems) != 1 {
		t.Fatalf("expected one preserved input item, got %#v", canon.ResponseInputItems)
	}
	if got, _ := canon.ResponseInputItems[0]["role"].(string); got != "user" {
		t.Fatalf("expected preserved role user, got %#v", canon.ResponseInputItems[0])
	}
}

func TestDecodeRequestAcceptsSingleObjectInput(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":{"role":"user","content":"hello"}
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.Messages) != 1 {
		t.Fatalf("expected one canonical message, got %#v", canon.Messages)
	}
	if canon.Messages[0].Role != "user" {
		t.Fatalf("expected user role, got %#v", canon.Messages[0])
	}
	if len(canon.ResponseInputItems) != 1 {
		t.Fatalf("expected one preserved input item, got %#v", canon.ResponseInputItems)
	}
}

func TestDecodeRequestPreservesResponsesStatefulFields(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"store":false,
		"include":["reasoning.encrypted_content"],
		"input":[
			{"role":"user","content":"hello"},
			{"type":"reasoning","id":"rs_123","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"enc_123"},
			{"type":"function_call_output","call_id":"call_123","output":"{}"}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if canon.ResponseStore == nil || *canon.ResponseStore {
		t.Fatalf("expected ResponseStore=false, got %#v", canon.ResponseStore)
	}
	if len(canon.ResponseInclude) != 1 || canon.ResponseInclude[0] != "reasoning.encrypted_content" {
		t.Fatalf("expected ResponseInclude to preserve reasoning.encrypted_content, got %#v", canon.ResponseInclude)
	}
	if len(canon.ResponseInputItems) != 3 {
		t.Fatalf("expected 3 ResponseInputItems, got %#v", canon.ResponseInputItems)
	}
	if got, _ := canon.ResponseInputItems[1]["type"].(string); got != "reasoning" {
		t.Fatalf("expected reasoning input item to be preserved, got %#v", canon.ResponseInputItems[1])
	}
	if got, _ := canon.ResponseInputItems[2]["type"].(string); got != "function_call_output" {
		t.Fatalf("expected function_call_output input item to be preserved, got %#v", canon.ResponseInputItems[2])
	}
	if got, _ := canon.ResponseInputItems[2]["call_id"].(string); got != "call_123" {
		t.Fatalf("expected function_call_output call_id to be preserved, got %#v", canon.ResponseInputItems[2])
	}
	if len(canon.Messages) != 1 || canon.Messages[0].Role != "user" {
		t.Fatalf("expected canonical user message to still be decoded, got %#v", canon.Messages)
	}
}

func TestDecodeRequestPreservesAssistantMessagesAsOutputText(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[
			{"role":"user","content":"第一句"},
			{"role":"assistant","content":"第二句"},
			{"role":"user","content":"第三句"}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.ResponseInputItems) != 3 {
		t.Fatalf("expected 3 ResponseInputItems, got %#v", canon.ResponseInputItems)
	}
	assistant, _ := canon.ResponseInputItems[1]["content"].([]map[string]any)
	if len(assistant) != 1 {
		t.Fatalf("expected assistant content preserved, got %#v", canon.ResponseInputItems[1])
	}
	if got, _ := assistant[0]["type"].(string); got != "output_text" {
		t.Fatalf("expected assistant content type output_text, got %#v", canon.ResponseInputItems[1])
	}
}

func TestDecodeRequestPreservesAssistantStructuredMessagesAsOutputText(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[
			{"role":"assistant","content":[{"type":"text","text":"第二句"}]}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	assistant, _ := canon.ResponseInputItems[0]["content"].([]map[string]any)
	if got, _ := assistant[0]["type"].(string); got != "output_text" {
		t.Fatalf("expected assistant structured content type output_text, got %#v", canon.ResponseInputItems[0])
	}
}

func TestDecodeRequestAcceptsResponsesInputFileContent(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[{"role":"user","content":[{"type":"input_file","input_file":{"file_id":"file_123"}}]}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 1 {
		t.Fatalf("expected one input_file part, got %#v", canon.Messages)
	}
	fileRaw, _ := canon.Messages[0].Parts[0].Raw["input_file"].(map[string]any)
	if got := fileRaw["file_id"]; got != "file_123" {
		t.Fatalf("expected input_file file_id preserved, got %#v", canon.Messages[0].Parts[0])
	}
}

func TestDecodeRequestAcceptsResponsesInputAudioContent(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"YWJj","format":"wav"}}]}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 1 {
		t.Fatalf("expected one input_audio part, got %#v", canon.Messages)
	}
	audioRaw, _ := canon.Messages[0].Parts[0].Raw["input_audio"].(map[string]any)
	if got := audioRaw["format"]; got != "wav" {
		t.Fatalf("expected input_audio format preserved, got %#v", canon.Messages[0].Parts[0])
	}
}

func TestDecodeRequestPreservesInputImageStructuredFields(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[{"role":"user","content":[{"type":"input_image","image_url":{"url":"https://example.com/image.png","detail":"high","file_id":"file_123"}}]}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 1 {
		t.Fatalf("expected one input_image part, got %#v", canon.Messages)
	}
	imageRaw, _ := canon.Messages[0].Parts[0].Raw["image_url"].(map[string]any)
	if got := imageRaw["detail"]; got != "high" {
		t.Fatalf("expected detail preserved, got %#v", canon.Messages[0].Parts[0])
	}
	if got := imageRaw["file_id"]; got != "file_123" {
		t.Fatalf("expected file_id preserved, got %#v", canon.Messages[0].Parts[0])
	}
}

func TestDecodeRequestPreservesPreviousResponseIDMetadataParallelToolCallsTruncationAndText(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"previous_response_id":"resp_123",
		"metadata":{"trace_id":"trace_123","tier":"gold"},
		"parallel_tool_calls":true,
		"truncation":"auto",
		"text":{"format":{"type":"text"}},
		"input":[{"role":"user","content":"hello"}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	preserved := extractPreservedResponsesTopLevelFields(t, canon)
	if got, _ := preserved["previous_response_id"].(string); got != "resp_123" {
		t.Fatalf("expected previous_response_id resp_123, got %#v", preserved)
	}
	metadata, _ := preserved["metadata"].(map[string]any)
	if got, _ := metadata["trace_id"].(string); got != "trace_123" {
		t.Fatalf("expected metadata.trace_id trace_123, got %#v", preserved)
	}
	if got, _ := metadata["tier"].(string); got != "gold" {
		t.Fatalf("expected metadata.tier gold, got %#v", preserved)
	}
	if got, _ := preserved["parallel_tool_calls"].(bool); !got {
		t.Fatalf("expected parallel_tool_calls=true, got %#v", preserved)
	}
	if got, _ := preserved["truncation"].(string); got != "auto" {
		t.Fatalf("expected truncation auto, got %#v", preserved)
	}
	text, _ := preserved["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if got, _ := format["type"].(string); got != "text" {
		t.Fatalf("expected text.format.type text, got %#v", preserved)
	}
}

func TestDecodeRequestPreservesPreviousResponseIDMetadataParallelToolCallsTruncationAndTextThroughUpstreamConstruction(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"previous_response_id":"resp_123",
		"metadata":{"trace_id":"trace_123"},
		"parallel_tool_calls":false,
		"truncation":"auto",
		"text":{"format":{"type":"text"}},
		"input":[{"role":"user","content":"hello"}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, &received); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()

	client := upstream.NewClient(server.URL)
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("client.Response error: %v", err)
	}

	if got, _ := received["previous_response_id"].(string); got != "resp_123" {
		t.Fatalf("expected previous_response_id resp_123 in upstream payload, got %#v", received)
	}
	metadata, _ := received["metadata"].(map[string]any)
	if got, _ := metadata["trace_id"].(string); got != "trace_123" {
		t.Fatalf("expected metadata.trace_id trace_123 in upstream payload, got %#v", received)
	}
	if got, ok := received["parallel_tool_calls"].(bool); !ok || got {
		t.Fatalf("expected parallel_tool_calls=false in upstream payload, got %#v", received)
	}
	if got, _ := received["truncation"].(string); got != "auto" {
		t.Fatalf("expected truncation auto in upstream payload, got %#v", received)
	}
	text, _ := received["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if got, _ := format["type"].(string); got != "text" {
		t.Fatalf("expected text.format.type text in upstream payload, got %#v", received)
	}
	input, _ := received["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected passthrough marker to stay out of upstream input, got %#v", received)
	}
}

func extractPreservedResponsesTopLevelFields(t *testing.T, canon model.CanonicalRequest) map[string]any {
	t.Helper()
	for _, item := range canon.ResponseInputItems {
		if preserved, ok := item[preservedResponsesTopLevelFieldsKey].(map[string]any); ok {
			return preserved
		}
	}
	t.Fatalf("expected preserved top-level responses fields marker, got %#v", canon)
	return nil
}
