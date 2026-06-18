package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestResponsesStreamReplaysAdjacentToolProductionShapeAndEmitsBody(t *testing.T) {
	var upstreamRequest map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"event: response.output_text.delta\n" +
				"data: {\"delta\":\"最终正文\"}\n\n" +
				"event: response.completed\n" +
				"data: {\"response\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":2,\"total_tokens\":13}}}\n\n",
		))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"deepseek-v4-flash",
		"stream":true,
		"input":[
			{"role":"user","content":"first user"},
			{"role":"user","content":"second user"},
			{"type":"reasoning","id":"rs_proxy","summary":[{"type":"summary_text","text":"\u200breal reasoning"}]},
			{"type":"function_call","call_id":"call_00_QYKD16UQaFTdlwHq7x6I5004","name":"search_web","arguments":"{\"query\":\"first\"}"},
			{"type":"function_call_output","call_id":"call_00_QYKD16UQaFTdlwHq7x6I5004","output":"{\"ok\":true}"},
			{"type":"function_call","call_id":"call_01_NI7fF0whLahOJEM0DxjG2203","name":"search_web","arguments":"{\"query\":\"second\"}"},
			{"type":"function_call_output","call_id":"call_01_NI7fF0whLahOJEM0DxjG2203","output":"{\"ok\":true}"}
		]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "最终正文") {
		t.Fatalf("expected downstream body text after adjacent tool calls, got %s", body)
	}
	if strings.Contains(body, "代理层占位") || strings.Contains(body, "**推理中**") {
		t.Fatalf("expected no visible proxy placeholder, got %s", body)
	}
	assertReplayUpstreamInputHasAdjacentToolShape(t, upstreamRequest)
}

func assertReplayUpstreamInputHasAdjacentToolShape(t *testing.T, request map[string]any) {
	t.Helper()
	input, _ := request["input"].([]any)
	if len(input) < 6 {
		t.Fatalf("expected upstream replay input to retain adjacent tool context, got %#v", request["input"])
	}
	var sequence []string
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if len(item) == 0 {
			continue
		}
		if typ, _ := item["type"].(string); typ == "function_call" || typ == "function_call_output" {
			sequence = append(sequence, typ)
		}
	}
	want := []string{"function_call", "function_call", "function_call_output", "function_call_output"}
	if strings.Join(sequence, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected adjacent tool sequence: got %#v want %#v", sequence, want)
	}
}
