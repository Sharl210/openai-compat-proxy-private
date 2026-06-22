package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestResponsesCompactRejectsStream(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_compact","object":"response"}`))
	}))
	defer upstream.Close()

	cfg := testResponsesConfig(upstream.URL)
	cfg.Providers[0].SupportsModels = false
	cfg.Providers[0].ManualModels = []string{"gpt-5"}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5","stream":true,"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusBadRequest, "invalid_request", "responses compact does not support stream=true")
	if called {
		t.Fatal("expected compact stream rejection before any upstream call")
	}
	if got := rec.Header().Get(headerClientToProxyModel); got != "" {
		t.Fatalf("expected early compact rejection to avoid client observability headers, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "" {
		t.Fatalf("expected early compact rejection to avoid upstream observability headers, got %q", got)
	}
}

func TestResponsesCompactFallsBackToChatUpstream(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_compact","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"text","text":"compact summary from chat"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":5,"total_tokens":9}}`))
	}))
	defer upstream.Close()

	cfg := testResponsesConfigWithEndpoint(upstream.URL, config.UpstreamEndpointTypeChat)
	cfg.UpstreamThinkingTagStyle = config.UpstreamThinkingTagStyleOff
	cfg.Providers[0].SupportsModels = false
	cfg.Providers[0].UpstreamThinkingTagStyle = config.UpstreamThinkingTagStyleOff
	cfg.Providers[0].SupportsChat = true
	cfg.Providers[0].ManualModels = []string{"gpt-5"}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Upstream-Authorization", "Bearer upstream-token")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("expected chat compact fallback to call upstream")
	}
	assertResponsesCompactContainsText(t, rec, "compact summary from chat")
}

func TestResponsesCompactFallsBackToAnthropicUpstream(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_compact","type":"message","role":"assistant","content":[{"type":"text","text":"compact summary from anthropic"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":5,"total_tokens":9}}`))
	}))
	defer upstream.Close()

	cfg := testResponsesConfigWithEndpoint(upstream.URL, config.UpstreamEndpointTypeAnthropic)
	cfg.Providers[0].SupportsModels = false
	cfg.Providers[0].SupportsAnthropicMessages = true
	cfg.Providers[0].ManualModels = []string{"gpt-5"}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Upstream-Authorization", "Bearer upstream-token")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("expected anthropic compact fallback to call upstream")
	}
	assertResponsesCompactContainsText(t, rec, "compact summary from anthropic")
}

func TestResponsesCompactRejectsUnsupportedProvider(t *testing.T) {
	cfg := testResponsesConfigWithEndpoint("http://127.0.0.1:1", config.UpstreamEndpointTypeResponses)
	cfg.Providers[0].SupportsResponses = false
	cfg.Providers[0].SupportsModels = false
	cfg.Providers[0].ManualModels = []string{"gpt-5"}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertUnsupportedProviderContract(t, rec, "provider does not support responses")
}

func TestResponsesCompactUsesResponsesOnlyContract(t *testing.T) {
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/responses/compact" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"id":"resp_compact","object":"response","status":"completed","compact_text":"hello"}`))
	}))
	defer upstream.Close()

	cfg := testResponsesConfigWithEndpoint(upstream.URL, config.UpstreamEndpointTypeResponses)
	cfg.DownstreamNonStreamStrategy = config.DownstreamNonStreamStrategyProxyBuffer
	cfg.Providers[0].SupportsModels = false
	cfg.Providers[0].ManualModels = []string{"gpt-5"}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if gotPath != "/responses/compact" {
		t.Fatalf("expected compact route to call /responses/compact, got %q", gotPath)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "gpt-5" {
		t.Fatalf("expected upstream observability model header, got %q", got)
	}
}

func TestResponsesCompactReturnsUpstreamPayloadDirectly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses/compact" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_compact","object":"response","status":"completed","compact_text":"hello","custom_nested":{"raw":true}}`))
	}))
	defer upstream.Close()

	cfg := testResponsesConfigWithEndpoint(upstream.URL, config.UpstreamEndpointTypeResponses)
	cfg.Providers[0].SupportsModels = false
	cfg.Providers[0].ManualModels = []string{"gpt-5"}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
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
	if got, _ := payload["compact_text"].(string); got != "hello" {
		t.Fatalf("expected raw compact payload field, got %#v", payload)
	}
	customNested, _ := payload["custom_nested"].(map[string]any)
	if got, _ := customNested["raw"].(bool); !got {
		t.Fatalf("expected nested raw upstream payload to be preserved, got %#v", payload)
	}
	if _, ok := payload["output"].([]any); ok {
		t.Fatalf("expected no normalized output rebuild for compact payload, got %#v", payload)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("expected application/json content type, got %q", got)
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "openai" {
		t.Fatalf("expected provider header openai, got %q", got)
	}
}

func TestResponsesCompactRelaysUpstream404(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses/compact" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"compact route missing upstream","type":"not_found"}}`))
	}))
	defer upstream.Close()

	cfg := testResponsesConfigWithEndpoint(upstream.URL, config.UpstreamEndpointTypeResponses)
	cfg.Providers[0].SupportsModels = false
	cfg.Providers[0].ManualModels = []string{"gpt-5"}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"compact route missing upstream"`) {
		t.Fatalf("expected upstream 404 payload passthrough, got %s", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("expected passthrough content type, got %q", got)
	}
}

func TestResponsesCompactBypassesProxyContextLimit(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.URL.Path != "/responses/compact" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_compact","object":"response","status":"completed","compact_text":"hello"}`))
	}))
	defer upstream.Close()

	cfg := testResponsesConfigWithEndpoint(upstream.URL, config.UpstreamEndpointTypeResponses)
	cfg.Providers[0].SupportsModels = false
	cfg.Providers[0].ManualModels = []string{"gpt-5"}
	cfg.Providers[0].ModelLimitContextTokensSet = true
	cfg.Providers[0].ModelLimitContextTokens = 1
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5","input":"hello hello hello hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected compact route to bypass proxy context limit, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !called {
		t.Fatal("expected compact route to still call upstream despite proxy context limit")
	}
}

func assertErrorResponse(t *testing.T, rec *httptest.ResponseRecorder, statusCode int, code string, message string) {
	t.Helper()
	if rec.Code != statusCode {
		t.Fatalf("expected status %d, got %d body=%s", statusCode, rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v body=%s", err, rec.Body.String())
	}
	errMap, _ := payload["error"].(map[string]any)
	if got, _ := errMap["code"].(string); got != code {
		t.Fatalf("expected error code %q, got %#v", code, payload)
	}
	if got, _ := errMap["message"].(string); got != message {
		t.Fatalf("expected error message %q, got %#v", message, payload)
	}
}

func assertResponsesCompactContainsText(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode compact response: %v body=%s", err, rec.Body.String())
	}
	if got, _ := payload["object"].(string); got != "response" {
		t.Fatalf("expected response object, got %#v body=%s", payload["object"], rec.Body.String())
	}
	output, _ := payload["output"].([]any)
	if len(output) == 0 {
		t.Fatalf("expected output items, got body=%s", rec.Body.String())
	}
	item, _ := output[0].(map[string]any)
	if got, _ := item["type"].(string); got != "message" {
		t.Fatalf("expected message output item, got %#v body=%s", item, rec.Body.String())
	}
	content, _ := item["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("expected message content, got %#v body=%s", item, rec.Body.String())
	}
	part, _ := content[0].(map[string]any)
	if got, _ := part["type"].(string); got != "output_text" {
		t.Fatalf("expected output_text content part, got %#v body=%s", part, rec.Body.String())
	}
	if got, _ := part["text"].(string); got != want {
		t.Fatalf("expected compact text %q, got %#v body=%s", want, part["text"], rec.Body.String())
	}
}
