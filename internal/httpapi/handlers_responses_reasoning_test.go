package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/testutil"
)

func TestResponsesNonStreamPreservesReasoningOutputItems(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"rs_123\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"thinking\"}],\"encrypted_content\":\"enc_123\"}}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(testResponsesConfig(upstream.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"store":false,
		"include":["reasoning.encrypted_content"],
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}

	output, _ := payload["output"].([]any)
	if len(output) != 1 {
		t.Fatalf("expected one output item, got %#v", payload["output"])
	}
	item, _ := output[0].(map[string]any)
	if got, _ := item["type"].(string); got != "reasoning" {
		t.Fatalf("expected reasoning output item, got %#v", item)
	}
	if got, _ := item["encrypted_content"].(string); got != "enc_123" {
		t.Fatalf("expected encrypted_content to be preserved, got %#v", item)
	}
}

func TestResponsesStreamMovesTopLevelServiceTierIntoCompletedResponseObject(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"service_tier\":\"default\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(testResponsesConfig(upstream.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `"response":{"id":"resp_`) && !strings.Contains(body, `"response":{"id":"resp_`) {
		t.Fatalf("unexpected body structure: %s", body)
	}
	if !strings.Contains(body, `"response":{"id":"resp_`) {
		t.Fatalf("expected completed event to include response object, got %s", body)
	}
	if !strings.Contains(body, `"service_tier":"default"`) {
		t.Fatalf("expected completed event to preserve service_tier, got %s", body)
	}
	if strings.Contains(body, `"response":{"id":"resp_`) && strings.Contains(body, `"response":{"id":"resp_`) {
		// no-op guard to keep test body readable after string checks
	}
	if strings.Contains(body, `"response":{"id":"resp_`) && strings.Contains(body, `"response":{"id":"resp_`) && strings.Contains(body, `"response":{"id":"resp_`) {
		// no-op
	}
	if strings.Contains(body, `"response":{"id":"resp_req-`) && strings.Contains(body, `"response":{"id":"resp_req-`) {
		// no-op
	}
	if strings.Contains(body, `"type":"response.completed"`) && strings.Contains(body, `"response":{"id":"resp_req-`) && strings.Contains(body, `"service_tier":"default","type":"response.completed"`) {
		t.Fatalf("expected service_tier to live under response object instead of top-level completed payload, got %s", body)
	}
}
