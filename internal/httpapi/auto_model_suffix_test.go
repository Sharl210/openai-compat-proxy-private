package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestAutoModelSuffixDropsReasoningAcrossEntrypoints(t *testing.T) {
	entrypoints := []struct {
		name       string
		path       string
		body       string
		setHeaders func(*http.Request)
	}{
		{
			name:       "responses",
			path:       "/v1/responses",
			body:       `{"model":"client-noprompt-auto-pro-high","max_output_tokens":128,"reasoning":{"effort":"high","mode":"pro","summary":"detailed"},"input":"hello"}`,
			setHeaders: func(*http.Request) {},
		},
		{
			name:       "chat",
			path:       "/v1/chat/completions",
			body:       `{"model":"client-high-pro-noprompt-auto","max_tokens":128,"reasoning":{"effort":"high","mode":"pro"},"messages":[{"role":"user","content":"hello"}]}`,
			setHeaders: func(*http.Request) {},
		},
		{
			name:       "messages",
			path:       "/v1/messages",
			body:       `{"model":"client-pro-auto-high-noprompt","max_tokens":128,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hello"}]}`,
			setHeaders: func(req *http.Request) { req.Header.Set("anthropic-version", "2023-06-01") },
		},
	}

	for _, entrypoint := range entrypoints {
		t.Run(entrypoint.name, func(t *testing.T) {
			upstreamHits := 0
			var upstreamPayload map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHits++
				if r.URL.Path != "/messages" {
					t.Fatalf("expected final Anthropic upstream path, got %q", r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&upstreamPayload); err != nil {
					t.Fatalf("decode upstream payload: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"msg_123","type":"message","role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`))
			}))
			defer upstream.Close()

			server := NewServer(config.Config{
				DefaultProvider:             "provider",
				EnableLegacyV1Routes:        true,
				EnableNoPromptModelSuffix:   true,
				DefaultProReasoningModeSet:  true,
				DefaultProReasoningMode:     false,
				DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
				V1ModelMap:                  []config.ModelMapEntry{config.NewModelMapEntry("client", "provider-target")},
				Providers: []config.ProviderConfig{{
					ID: "provider", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key",
					UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, SupportsResponses: true, SupportsChat: true,
					SupportsAnthropicMessages: true, EnableReasoningEffortSuffix: true, EnableNoPromptModelSuffix: true,
					MapReasoningSuffixToAnthropicThinking: false, ManualModels: []string{"provider-target"},
					ModelMap: []config.ModelMapEntry{config.NewModelMapEntry("provider-target", "claude-opus-4-6")},
				}},
			})
			req := httptest.NewRequest(http.MethodPost, entrypoint.path, strings.NewReader(entrypoint.body))
			req.Header.Set("Content-Type", "application/json")
			entrypoint.setHeaders(req)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK || upstreamHits != 1 {
				t.Fatalf("expected one successful upstream request, status=%d hits=%d body=%s", rec.Code, upstreamHits, rec.Body.String())
			}
			if got := upstreamPayload["model"]; got != "claude-opus-4-6" {
				t.Fatalf("expected final mapped model without auto suffix, got %#v", upstreamPayload)
			}
			for _, field := range []string{"reasoning", "reasoning_effort", "thinking", "output_config"} {
				if _, exists := upstreamPayload[field]; exists {
					t.Fatalf("auto suffix must omit %s from upstream payload, got %#v", field, upstreamPayload)
				}
			}
			if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "true" {
				t.Fatalf("expected composed noprompt suffix to remain active, got %q", got)
			}
		})
	}
}

func TestAutoModelSuffixPreservesExactLiteralModelPrecedence(t *testing.T) {
	upstreamHits := 0
	var upstreamPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		if err := json.NewDecoder(r.Body).Decode(&upstreamPayload); err != nil {
			t.Fatalf("decode upstream payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider: "provider", EnableLegacyV1Routes: true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers:                   []config.ProviderConfig{{ID: "provider", Enabled: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key", UpstreamEndpointType: config.UpstreamEndpointTypeResponses, SupportsResponses: true, ManualModels: []string{"vendor-auto", "vendor"}}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"vendor-auto","reasoning":{"effort":"high"},"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || upstreamHits != 1 || upstreamPayload["model"] != "vendor-auto" {
		t.Fatalf("expected exact literal vendor-auto to bypass suffix parsing, status=%d calls=%d payload=%#v", rec.Code, upstreamHits, upstreamPayload)
	}
	if _, exists := upstreamPayload["reasoning"]; !exists {
		t.Fatal("exact literal model must preserve explicit reasoning parameters")
	}
}
