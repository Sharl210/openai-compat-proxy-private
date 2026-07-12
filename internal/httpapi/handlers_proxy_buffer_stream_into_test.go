package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestProxyBufferHandlersReturnCompletedOutput(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n" +
			"data: {\"response\":{\"id\":\"resp_proxy_buffer\"}}\n\n" +
			"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello from proxy buffer\"}\n\n" +
			"event: response.completed\n" +
			"data: {\"response\":{\"finish_reason\":\"stop\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyProxyBuffer,
		Providers: []config.ProviderConfig{{
			ID:                        "openai",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeResponses,
			SupportsResponses:         true,
			SupportsChat:              true,
			SupportsAnthropicMessages: true,
		}},
	})

	tests := []struct {
		name    string
		path    string
		body    string
		headers map[string]string
	}{
		{
			name: "chat",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"gpt-5","input":"hello"}`,
		},
		{
			name: "anthropic",
			path: "/v1/messages",
			body: `{"model":"gpt-5","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`,
			headers: map[string]string{
				"anthropic-version": "2023-06-01",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			for name, value := range test.headers {
				req.Header.Set(name, value)
			}
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "hello from proxy buffer") {
				t.Fatalf("expected completed proxy-buffer output, got %s", rec.Body.String())
			}
		})
	}
}
