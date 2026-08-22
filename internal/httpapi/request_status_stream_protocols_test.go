package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestChatStreamFailureWritesTerminalChunk(t *testing.T) {
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
	_ = requestID // status endpoint removed; we only assert stream output
}
func TestChatStreamFailureClearsReasoningChunkContext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.reasoning.delta\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"**标题**\"}\n\n"))
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte("data: {\"health_flag\":\"upstream_error\",\"message\":\"failed\"}\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", SupportsChat: true, SupportsResponses: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatal("missing request ID")
	}
	if _, ok := requestReasoningChunkContexts.tail(requestID); ok {
		t.Fatalf("failed stream left request context behind: %q", requestID)
	}
}

func TestResponsesNonStreamReturnClearsReasoningChunkContext(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_cleanup","object":"chat.completion","created":1,"model":"gpt-5","choices":[{"index":0,"message":{"role":"assistant","reasoning_content":"**标题**","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeChat, SupportsChat: true, SupportsResponses: true}}})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(rec, req)
		close(done)
	}()

	<-started
	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		close(release)
		<-done
		t.Fatal("missing request ID")
	}
	requestReasoningChunkContexts.formatForSend(requestID, "**标题**", false)
	close(release)
	<-done
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := requestReasoningChunkContexts.tail(requestID); ok {
		t.Fatalf("non-stream response left request context behind: %q", requestID)
	}
}
func TestChatStreamCanceledRequestClearsReasoningChunkContext(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.reasoning.delta\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"**标题**\"}\n\n"))
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer upstream.Close()

	server := NewServer(config.Config{DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", SupportsChat: true, SupportsResponses: true}}})
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`)).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(rec, req)
		close(done)
	}()
	<-started
	cancel()
	<-done

	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatal("missing request ID")
	}
	if _, ok := requestReasoningChunkContexts.tail(requestID); ok {
		t.Fatalf("canceled request left request context behind: %q", requestID)
	}
}

func TestMessagesStreamFailureWritesTerminalEvent(t *testing.T) {
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
	_ = requestID // status endpoint removed; we only assert stream output
}

func TestChatStreamMissingTerminalEventWritesTerminalChunk(t *testing.T) {
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
	_ = requestID // status endpoint removed; we only assert stream output
}

func TestMessagesStreamMissingTerminalEventWritesTerminalEvent(t *testing.T) {
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
	_ = requestID // status endpoint removed; we only assert stream output
}

func TestChatStreamUpstreamIncompleteTimeoutPreservesTerminalFailureFlagInStream(t *testing.T) {
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
	_ = requestID // status endpoint removed; we only assert stream output
}

func TestMessagesStreamUpstreamIncompleteTimeoutPreservesTerminalFailureFlagInStream(t *testing.T) {
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
	_ = requestID // status endpoint removed; we only assert stream output
}
