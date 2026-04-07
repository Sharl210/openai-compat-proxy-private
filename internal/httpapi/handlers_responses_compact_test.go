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

func TestResponsesCompactRejectsNonResponsesUpstream(t *testing.T) {
	cfg := testResponsesConfigWithEndpoint("http://127.0.0.1:1", config.UpstreamEndpointTypeChat)
	cfg.Providers[0].SupportsModels = false
	cfg.Providers[0].ManualModels = []string{"gpt-5"}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Upstream-Authorization", "Bearer upstream-token")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertErrorResponse(t, rec, http.StatusBadRequest, "invalid_request", "responses compact requires a responses upstream endpoint")
	if got := rec.Header().Get(headerClientToProxyModel); got != "" {
		t.Fatalf("expected non-responses compact rejection to avoid client observability headers, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "" {
		t.Fatalf("expected non-responses compact rejection to avoid upstream observability headers, got %q", got)
	}
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
