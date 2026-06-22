package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
)

func TestResponsesStreamFirstByteTimeoutReturnsGatewayTimeoutJSON(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:          "openai",
		EnableLegacyV1Routes:     true,
		FirstByteTimeout:         50 * time.Millisecond,
		UpstreamThinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected pre-open first-byte timeout to return 504 JSON before SSE starts, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `event: response.output_item.added`) || strings.Contains(body, `"id":"rs_proxy"`) {
		t.Fatalf("expected pre-open first-byte timeout not to start synthetic SSE prelude, got %s", body)
	}
	if strings.Contains(body, `event: response.incomplete`) || strings.Contains(body, `"health_flag":"upstream_timeout"`) {
		t.Fatalf("expected pre-open first-byte timeout not to be converted into terminal SSE event, got %s", body)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode timeout body: %v body=%s", err, rec.Body.String())
	}
	errMap, _ := payload["error"].(map[string]any)
	if got, _ := errMap["code"].(string); got != "upstream_timeout" {
		t.Fatalf("expected upstream_timeout code, got %#v", payload)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("expected JSON content type before SSE starts, got %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "" {
		t.Fatalf("expected SSE headers not to be set before upstream opens, got X-Accel-Buffering=%q", got)
	}
}

func TestResponsesStreamUsesProviderScopedFirstByteTimeoutOverride(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:          "openai",
		EnableLegacyV1Routes:     true,
		FirstByteTimeout:         5 * time.Second,
		UpstreamThinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
		Providers: []config.ProviderConfig{{
			ID:                       "openai",
			Enabled:                  true,
			UpstreamBaseURL:          upstream.URL,
			UpstreamAPIKey:           "test-key",
			SupportsResponses:        true,
			UpstreamFirstByteTimeout: 50 * time.Millisecond,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected provider-scoped pre-open timeout to return 504 JSON before SSE starts, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `event: response.incomplete`) || strings.Contains(body, `"health_flag":"upstream_timeout"`) || strings.Contains(body, `"id":"rs_proxy"`) {
		t.Fatalf("expected provider-scoped pre-open timeout not to start SSE protocol, got %s", body)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode timeout body: %v body=%s", err, rec.Body.String())
	}
	errMap, _ := payload["error"].(map[string]any)
	if got, _ := errMap["code"].(string); got != "upstream_timeout" {
		t.Fatalf("expected upstream_timeout code, got %#v", payload)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("expected JSON content type before SSE starts, got %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "" {
		t.Fatalf("expected SSE headers not to be set before upstream opens, got X-Accel-Buffering=%q", got)
	}
}

func TestResponsesStreamUsesStreamOpenTimeoutBeforeSSEStart(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:          "openai",
		EnableLegacyV1Routes:     true,
		FirstByteTimeout:         5 * time.Second,
		StreamOpenTimeout:        50 * time.Millisecond,
		UpstreamThinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	started := time.Now()
	server.ServeHTTP(rec, req)
	elapsed := time.Since(started)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected stream-open timeout to return 504 JSON before SSE starts, got %d body=%s", rec.Code, rec.Body.String())
	}
	if elapsed >= 140*time.Millisecond {
		t.Fatalf("expected stream-open timeout to return before upstream headers arrive, elapsed=%s", elapsed)
	}
	body := rec.Body.String()
	if strings.Contains(body, `event: response.incomplete`) || strings.Contains(body, `event: response.completed`) || strings.Contains(body, `"id":"rs_proxy"`) {
		t.Fatalf("expected stream-open timeout not to start SSE protocol, got %s", body)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode timeout body: %v body=%s", err, rec.Body.String())
	}
	errMap, _ := payload["error"].(map[string]any)
	if got, _ := errMap["code"].(string); got != "upstream_timeout" {
		t.Fatalf("expected upstream_timeout code, got %#v", payload)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("expected JSON content type before SSE starts, got %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "" {
		t.Fatalf("expected SSE headers not to be set before upstream opens, got X-Accel-Buffering=%q", got)
	}
}

func TestResponsesStreamUsesProviderScopedStreamOpenTimeoutOverride(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:          "openai",
		EnableLegacyV1Routes:     true,
		FirstByteTimeout:         5 * time.Second,
		StreamOpenTimeout:        5 * time.Second,
		UpstreamThinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
		Providers: []config.ProviderConfig{{
			ID:                        "openai",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			SupportsResponses:         true,
			UpstreamStreamOpenTimeout: 50 * time.Millisecond,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	started := time.Now()
	server.ServeHTTP(rec, req)
	elapsed := time.Since(started)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected provider stream-open timeout to return 504 JSON before SSE starts, got %d body=%s", rec.Code, rec.Body.String())
	}
	if elapsed >= 140*time.Millisecond {
		t.Fatalf("expected provider stream-open timeout before upstream headers arrive, elapsed=%s", elapsed)
	}
	if strings.Contains(rec.Body.String(), `event: response.completed`) || strings.Contains(rec.Body.String(), `"id":"rs_proxy"`) {
		t.Fatalf("expected provider stream-open timeout not to start SSE protocol, got %s", rec.Body.String())
	}
}

func TestResponsesStreamIdleTimeoutStoresUpstreamTimeoutStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"hello\"}\n\n"))
		flusher.Flush()
		time.Sleep(250 * time.Millisecond)
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		TotalTimeout:         50 * time.Millisecond,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), `"health_flag":"upstream_timeout"`) {
		t.Fatalf("expected response.failed timeout health flag, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `event: response.failed`) {
		t.Fatalf("expected timeout to surface as response.failed, got %s", rec.Body.String())
	}
}

func TestResponsesNonStreamUpstreamIncompleteTimeoutReturnsGatewayTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.incomplete\n"))
		_, _ = w.Write([]byte("data: {\"request_id\":\"upstream_req\",\"health_flag\":\"upstream_timeout\",\"message\":\"upstream request timed out\"}\n\n"))
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
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":false,"input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504 for upstream timeout terminal event, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode timeout body: %v body=%s", err, rec.Body.String())
	}
	errMap, _ := payload["error"].(map[string]any)
	if got, _ := errMap["code"].(string); got != "upstream_timeout" {
		t.Fatalf("expected upstream_timeout code, got %#v", payload)
	}
}
