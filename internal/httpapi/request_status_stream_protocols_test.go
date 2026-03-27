package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestChatStreamFailureWritesTerminalChunkAndFailedStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"hello\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {broken json}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", SupportsChat: true, SupportsResponses: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	requestID := rec.Header().Get("X-Request-Id")
	if !strings.Contains(rec.Body.String(), `"finish_reason":"error"`) {
		t.Fatalf("expected terminal error finish_reason in chat stream, got %s", rec.Body.String())
	}
	status := fetchStatusForTest(t, server, "openai", requestID)
	if status.Status != "failed" || !status.Completed || status.HealthFlag != "upstream_stream_broken" {
		t.Fatalf("unexpected chat failed status: %#v", status)
	}
}

func TestMessagesStreamFailureWritesTerminalEventAndFailedStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.reasoning.delta\n"))
		_, _ = w.Write([]byte("data: {\"summary\":\"alpha\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {broken json}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", SupportsAnthropicMessages: true, SupportsResponses: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4","stream":true,"max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	requestID := rec.Header().Get("X-Request-Id")
	if !strings.Contains(rec.Body.String(), `event: error`) {
		t.Fatalf("expected explicit error event in messages stream, got %s", rec.Body.String())
	}
	status := fetchStatusForTest(t, server, "anthropic", requestID)
	if status.Status != "failed" || !status.Completed || status.HealthFlag != "upstream_stream_broken" {
		t.Fatalf("unexpected messages failed status: %#v", status)
	}
}

func TestChatStreamMissingTerminalEventWritesTerminalChunkAndFailedStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"hello\"}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", SupportsChat: true, SupportsResponses: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	requestID := rec.Header().Get("X-Request-Id")
	if !strings.Contains(rec.Body.String(), `"finish_reason":"error"`) {
		t.Fatalf("expected terminal error finish_reason in chat stream, got %s", rec.Body.String())
	}
	status := fetchStatusForTest(t, server, "openai", requestID)
	if status.Status != "failed" || !status.Completed || status.HealthFlag != "upstream_stream_broken" {
		t.Fatalf("unexpected chat failed status: %#v", status)
	}
}

func TestMessagesStreamMissingTerminalEventWritesTerminalEventAndFailedStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.reasoning.delta\n"))
		_, _ = w.Write([]byte("data: {\"summary\":\"alpha\"}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", SupportsAnthropicMessages: true, SupportsResponses: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4","stream":true,"max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	requestID := rec.Header().Get("X-Request-Id")
	if !strings.Contains(rec.Body.String(), `event: error`) || !strings.Contains(rec.Body.String(), `event: message_stop`) {
		t.Fatalf("expected explicit error and message_stop events in messages stream, got %s", rec.Body.String())
	}
	status := fetchStatusForTest(t, server, "anthropic", requestID)
	if status.Status != "failed" || !status.Completed || status.HealthFlag != "upstream_stream_broken" {
		t.Fatalf("unexpected messages failed status: %#v", status)
	}
}

func TestChatStreamUpstreamIncompleteTimeoutPreservesTerminalFailureStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"hello\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.incomplete\n"))
		_, _ = w.Write([]byte("data: {\"request_id\":\"upstream_req\",\"health_flag\":\"upstream_timeout\",\"message\":\"upstream request timed out\"}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", SupportsChat: true, SupportsResponses: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	requestID := rec.Header().Get("X-Request-Id")
	if !strings.Contains(rec.Body.String(), `"health_flag":"upstream_timeout"`) {
		t.Fatalf("expected chat terminal chunk to preserve upstream_timeout, got %s", rec.Body.String())
	}
	status := fetchStatusForTest(t, server, "openai", requestID)
	if status.Status != "failed" || status.HealthFlag != "upstream_timeout" || status.ErrorCode != "upstream_timeout" {
		t.Fatalf("unexpected chat timeout status: %#v", status)
	}
}

func TestMessagesStreamUpstreamIncompleteTimeoutPreservesTerminalFailureStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.reasoning.delta\n"))
		_, _ = w.Write([]byte("data: {\"summary\":\"alpha\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.incomplete\n"))
		_, _ = w.Write([]byte("data: {\"request_id\":\"upstream_req\",\"health_flag\":\"upstream_timeout\",\"message\":\"upstream request timed out\"}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "anthropic", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "anthropic", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", SupportsAnthropicMessages: true, SupportsResponses: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4","stream":true,"max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	requestID := rec.Header().Get("X-Request-Id")
	if !strings.Contains(rec.Body.String(), `"health_flag":"upstream_timeout"`) {
		t.Fatalf("expected messages error event to preserve upstream_timeout, got %s", rec.Body.String())
	}
	status := fetchStatusForTest(t, server, "anthropic", requestID)
	if status.Status != "failed" || status.HealthFlag != "upstream_timeout" || status.ErrorCode != "upstream_timeout" {
		t.Fatalf("unexpected messages timeout status: %#v", status)
	}
}

func fetchStatusForTest(t *testing.T, server http.Handler, providerID string, requestID string) requestStatus {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/"+providerID+"/v1/requests/"+requestID, nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status endpoint 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var status requestStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status response: %v body=%s", err, rec.Body.String())
	}
	return status
}
