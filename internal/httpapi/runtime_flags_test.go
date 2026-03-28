package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestChatRejectsProviderWithoutChatSupport(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			UpstreamBaseURL: "https://example.test",
			UpstreamAPIKey:  "test-key",
			SupportsChat:    false,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertUnsupportedProviderContract(t, rec, "provider does not support chat completions")
}

func TestNewServerWithStoreAcceptsCacheManager(t *testing.T) {
	cfg := config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{
			{
				ID:                "openai",
				Enabled:           true,
				UpstreamBaseURL:   "https://example.test",
				UpstreamAPIKey:    "test-key",
				SupportsResponses: true,
			},
		},
	}
	store := config.NewStaticRuntimeStore(cfg)
	manager := &cacheinfo.Manager{}
	server := NewServerWithStore(store, manager)
	if server.CacheInfo != manager {
		t.Fatalf("expected cache manager to be stored on server")
	}
}

func TestResponsesRejectsProviderWithoutResponsesSupport(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   "https://example.test",
			UpstreamAPIKey:    "test-key",
			SupportsResponses: false,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertUnsupportedProviderContract(t, rec, "provider does not support responses")
}

func TestModelsRejectsProviderWithoutModelsSupport(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			UpstreamBaseURL: "https://example.test",
			UpstreamAPIKey:  "test-key",
			SupportsModels:  false,
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertUnsupportedProviderContract(t, rec, "provider does not support models")
}

func TestDisabledDefaultProviderLegacyModelsRouteReturnsNotFound(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         false,
			UpstreamBaseURL: "https://example.test",
			UpstreamAPIKey:  "test-key",
			SupportsModels:  true,
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404 when default provider is disabled, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMessagesRejectsProviderWithoutMessagesSupport(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "anthropic",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           "https://example.test",
			UpstreamAPIKey:            "test-key",
			SupportsAnthropicMessages: false,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	assertUnsupportedProviderContract(t, rec, "provider does not support anthropic messages")
}

func TestMessagesRequiresAnthropicVersionHeader(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "anthropic",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           "https://example.test",
			UpstreamAPIKey:            "test-key",
			SupportsAnthropicMessages: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 when anthropic-version is missing, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "anthropic-version") {
		t.Fatalf("expected missing anthropic-version error, got %s", rec.Body.String())
	}
}

func TestLegacyV1RoutesDisabledReturnsNotFound(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: false,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestResponsesNonStreamCanUseUpstreamNonStreamByRootStrategy(t *testing.T) {
	var upstreamStreamValue atomic.Value
	upstreamStreamValue.Store(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode upstream payload: %v body=%s", err, string(body))
		}
		stream, _ := payload["stream"].(bool)
		upstreamStreamValue.Store(stream)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","output":[{"id":"msg_123","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello from upstream json"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := upstreamStreamValue.Load().(bool); got {
		t.Fatalf("expected upstream non-stream request when root strategy is upstream_non_stream")
	}
	if !strings.Contains(rec.Body.String(), `"text":"hello from upstream json"`) {
		t.Fatalf("expected downstream response to include upstream non-stream body, got %s", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("expected application/json response, got %q", got)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "" {
		t.Fatalf("expected non-stream downstream response to avoid SSE buffering header, got %q", got)
	}
}

func TestChatNonStreamProviderOverrideForcesUpstreamNonStream(t *testing.T) {
	var upstreamStreamValue atomic.Value
	upstreamStreamValue.Store(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode upstream payload: %v body=%s", err, string(body))
		}
		stream, _ := payload["stream"].(bool)
		upstreamStreamValue.Store(stream)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"delta\":\"fallback\"}\n\nevent: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","output":[{"id":"msg_123","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"chat from upstream json"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyProxyBuffer,
		Providers: []config.ProviderConfig{{
			ID:                                     "openai",
			Enabled:                                true,
			UpstreamBaseURL:                        upstream.URL,
			UpstreamAPIKey:                         "test-key",
			SupportsChat:                           true,
			SupportsResponses:                      true,
			DownstreamNonStreamStrategyOverrideSet: true,
			DownstreamNonStreamStrategyOverride:    config.DownstreamNonStreamStrategyUpstreamNonStream,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := upstreamStreamValue.Load().(bool); got {
		t.Fatalf("expected provider override to force upstream non-stream request")
	}
	if !strings.Contains(rec.Body.String(), `"content":"chat from upstream json"`) {
		t.Fatalf("expected chat response to include mapped upstream json text, got %s", rec.Body.String())
	}
}

func TestMessagesNonStreamProviderOverrideCanStayProxyBuffer(t *testing.T) {
	var upstreamStreamValue atomic.Value
	upstreamStreamValue.Store(false)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode upstream payload: %v body=%s", err, string(body))
		}
		stream, _ := payload["stream"].(bool)
		upstreamStreamValue.Store(stream)
		if !stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","output":[{"id":"msg_123","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"should not be used"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\ndata: {\"delta\":\"hello from buffered stream\"}\n\nevent: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                                     "anthropic",
			Enabled:                                true,
			UpstreamBaseURL:                        upstream.URL,
			UpstreamAPIKey:                         "test-key",
			SupportsAnthropicMessages:              true,
			SupportsResponses:                      true,
			DownstreamNonStreamStrategyOverrideSet: true,
			DownstreamNonStreamStrategyOverride:    config.DownstreamNonStreamStrategyProxyBuffer,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(`{"model":"gpt-5.4","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := upstreamStreamValue.Load().(bool); !got {
		t.Fatalf("expected provider override to keep proxy buffer mode")
	}
	if !strings.Contains(rec.Body.String(), `"text":"hello from buffered stream"`) {
		t.Fatalf("expected anthropic response to come from buffered stream aggregation, got %s", rec.Body.String())
	}
}

func assertUnsupportedProviderContract(t *testing.T, rec *httptest.ResponseRecorder, message string) {
	t.Helper()
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v body=%s", err, rec.Body.String())
	}
	errMap, _ := payload["error"].(map[string]any)
	if got, _ := errMap["code"].(string); got != "unsupported_provider_contract" {
		t.Fatalf("expected unsupported_provider_contract code, got %#v", payload)
	}
	if got, _ := errMap["message"].(string); got != message {
		t.Fatalf("expected message %q, got %#v", message, payload)
	}
}
