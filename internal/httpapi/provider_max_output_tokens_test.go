package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestProviderMaxOutputTokensFillsMissingClientLimitForAnthropicUpstream(t *testing.T) {
	for _, tc := range providerMaxOutputTokensRouteCases(0) {
		t.Run(tc.name, func(t *testing.T) {
			payload, rec := serveProviderMaxOutputTokensRequest(t, providerMaxOutputTokensScenario{
				path:                    tc.path,
				body:                    tc.body,
				anthropicVersion:        tc.anthropicVersion,
				providerMaxOutputTokens: 64000,
			})

			if got := numericJSONValue(payload["max_tokens"]); got != 64000 {
				t.Fatalf("expected provider max_tokens 64000, got %#v payload=%#v", payload["max_tokens"], payload)
			}
			if got := rec.Header().Get(headerProxyToUpstreamMaxOutputTokens); got != "64000" {
				t.Fatalf("expected %s 64000, got %q", headerProxyToUpstreamMaxOutputTokens, got)
			}
		})
	}
}

func TestProviderMaxOutputTokensKeepsClientLimitWhenForceFalseForAnthropicUpstream(t *testing.T) {
	for _, tc := range providerMaxOutputTokensRouteCases(2048) {
		t.Run(tc.name, func(t *testing.T) {
			payload, rec := serveProviderMaxOutputTokensRequest(t, providerMaxOutputTokensScenario{
				path:                    tc.path,
				body:                    tc.body,
				anthropicVersion:        tc.anthropicVersion,
				providerMaxOutputTokens: 64000,
			})

			if got := numericJSONValue(payload["max_tokens"]); got != 2048 {
				t.Fatalf("expected client max_tokens 2048, got %#v payload=%#v", payload["max_tokens"], payload)
			}
			if got := rec.Header().Get(headerProxyToUpstreamMaxOutputTokens); got != "2048" {
				t.Fatalf("expected %s 2048, got %q", headerProxyToUpstreamMaxOutputTokens, got)
			}
		})
	}
}

func TestProviderMaxOutputTokensOverridesClientLimitWhenForceTrueForAnthropicUpstream(t *testing.T) {
	for _, tc := range providerMaxOutputTokensRouteCases(2048) {
		t.Run(tc.name, func(t *testing.T) {
			payload, rec := serveProviderMaxOutputTokensRequest(t, providerMaxOutputTokensScenario{
				path:                         tc.path,
				body:                         tc.body,
				anthropicVersion:             tc.anthropicVersion,
				providerMaxOutputTokens:      64000,
				forceProviderMaxOutputTokens: true,
			})

			if got := numericJSONValue(payload["max_tokens"]); got != 64000 {
				t.Fatalf("expected forced provider max_tokens 64000, got %#v payload=%#v", payload["max_tokens"], payload)
			}
			if got := rec.Header().Get(headerProxyToUpstreamMaxOutputTokens); got != "64000" {
				t.Fatalf("expected %s 64000, got %q", headerProxyToUpstreamMaxOutputTokens, got)
			}
		})
	}
}

func TestProviderMaxOutputTokensOmitsMissingClientLimitWhenProviderLimitIsMinusOne(t *testing.T) {
	for _, tc := range providerMaxOutputTokensRouteCases(0) {
		t.Run(tc.name, func(t *testing.T) {
			payload, rec := serveProviderMaxOutputTokensRequest(t, providerMaxOutputTokensScenario{
				path:                    tc.path,
				body:                    tc.body,
				anthropicVersion:        tc.anthropicVersion,
				providerMaxOutputTokens: -1,
			})

			if _, exists := payload["max_tokens"]; exists {
				t.Fatalf("expected provider -1 to omit max_tokens when client did not send a limit, got payload=%#v", payload)
			}
			if got := rec.Header().Get(headerProxyToUpstreamMaxOutputTokens); got != "" {
				t.Fatalf("expected empty %s when provider max output tokens is -1, got %q", headerProxyToUpstreamMaxOutputTokens, got)
			}
		})
	}
}

func TestProviderMaxOutputTokensKeepsClientLimitWhenForceFalseEvenIfProviderLimitIsMinusOne(t *testing.T) {
	for _, tc := range providerMaxOutputTokensRouteCases(2048) {
		t.Run(tc.name, func(t *testing.T) {
			payload, rec := serveProviderMaxOutputTokensRequest(t, providerMaxOutputTokensScenario{
				path:                    tc.path,
				body:                    tc.body,
				anthropicVersion:        tc.anthropicVersion,
				providerMaxOutputTokens: -1,
			})

			if got := numericJSONValue(payload["max_tokens"]); got != 2048 {
				t.Fatalf("expected client max_tokens 2048 to be trusted when force is false, got %#v payload=%#v", payload["max_tokens"], payload)
			}
			if got := rec.Header().Get(headerProxyToUpstreamMaxOutputTokens); got != "2048" {
				t.Fatalf("expected %s 2048, got %q", headerProxyToUpstreamMaxOutputTokens, got)
			}
		})
	}
}

func TestProviderMaxOutputTokensOmitsClientLimitWhenForceTrueAndProviderLimitIsMinusOne(t *testing.T) {
	for _, tc := range providerMaxOutputTokensRouteCases(2048) {
		t.Run(tc.name, func(t *testing.T) {
			payload, rec := serveProviderMaxOutputTokensRequest(t, providerMaxOutputTokensScenario{
				path:                         tc.path,
				body:                         tc.body,
				anthropicVersion:             tc.anthropicVersion,
				providerMaxOutputTokens:      -1,
				forceProviderMaxOutputTokens: true,
			})

			if _, exists := payload["max_tokens"]; exists {
				t.Fatalf("expected forced provider -1 to omit max_tokens, got payload=%#v", payload)
			}
			if got := rec.Header().Get(headerProxyToUpstreamMaxOutputTokens); got != "" {
				t.Fatalf("expected empty %s when forced provider max output tokens is -1, got %q", headerProxyToUpstreamMaxOutputTokens, got)
			}
		})
	}
}

func TestForceProviderMaxOutputTokensDoesNothingWhenProviderLimitUnset(t *testing.T) {
	payload, rec := serveProviderMaxOutputTokensRequest(t, providerMaxOutputTokensScenario{
		path:                         "/v1/responses",
		body:                         `{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`,
		forceProviderMaxOutputTokens: true,
	})

	if got := numericJSONValue(payload["max_tokens"]); got != 1024 {
		t.Fatalf("expected existing anthropic upstream fallback max_tokens 1024, got %#v payload=%#v", payload["max_tokens"], payload)
	}
	if got := rec.Header().Get(headerProxyToUpstreamMaxOutputTokens); got != "" {
		t.Fatalf("expected empty %s when canonical max output tokens is unset, got %q", headerProxyToUpstreamMaxOutputTokens, got)
	}
}

func TestProviderMaxOutputTokensFeedsAnthropicThinkingBudget(t *testing.T) {
	payload, rec := serveProviderMaxOutputTokensRequest(t, providerMaxOutputTokensScenario{
		path:                         "/v1/responses",
		body:                         `{"model":"gpt-5-xhigh","input":[{"role":"user","content":"hello"}]}`,
		providerMaxOutputTokens:      20000,
		forceProviderMaxOutputTokens: true,
	})

	if got := numericJSONValue(payload["max_tokens"]); got != 20000 {
		t.Fatalf("expected provider max_tokens 20000, got %#v payload=%#v", payload["max_tokens"], payload)
	}
	thinking, _ := payload["thinking"].(map[string]any)
	if got := numericJSONValue(thinking["budget_tokens"]); got != 16000 {
		t.Fatalf("expected provider max output tokens to feed thinking budget 16000, got %#v payload=%#v", thinking["budget_tokens"], payload)
	}
	if got := rec.Header().Get(headerProxyToUpstreamMaxOutputTokens); got != "20000" {
		t.Fatalf("expected %s 20000, got %q", headerProxyToUpstreamMaxOutputTokens, got)
	}
}

type providerMaxOutputTokensRouteCase struct {
	name             string
	path             string
	body             string
	anthropicVersion string
}

func providerMaxOutputTokensRouteCases(clientLimit int) []providerMaxOutputTokensRouteCase {
	responsesLimit := ""
	chatLimit := ""
	messagesLimit := ""
	if clientLimit > 0 {
		responsesLimit = `,"max_output_tokens":2048`
		chatLimit = `,"max_tokens":2048`
		messagesLimit = `,"max_tokens":2048`
	}
	return []providerMaxOutputTokensRouteCase{
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"gpt-5","input":[{"role":"user","content":"hello"}]` + responsesLimit + `}`,
		},
		{
			name: "chat",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]` + chatLimit + `}`,
		},
		{
			name:             "messages",
			path:             "/v1/messages",
			body:             `{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]` + messagesLimit + `}`,
			anthropicVersion: "2023-06-01",
		},
	}
}

type providerMaxOutputTokensScenario struct {
	path                         string
	body                         string
	anthropicVersion             string
	providerMaxOutputTokens      int
	forceProviderMaxOutputTokens bool
}

func serveProviderMaxOutputTokensRequest(t *testing.T, scenario providerMaxOutputTokensScenario) (map[string]any, *httptest.ResponseRecorder) {
	t.Helper()

	var upstreamPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &upstreamPayload); err != nil {
			t.Fatalf("unmarshal upstream payload: %v body=%s", err, body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                                    "openai",
			Enabled:                               true,
			UpstreamBaseURL:                       upstream.URL,
			UpstreamAPIKey:                        "test-key",
			UpstreamEndpointType:                  config.UpstreamEndpointTypeAnthropic,
			SupportsResponses:                     true,
			SupportsChat:                          true,
			SupportsAnthropicMessages:             true,
			EnableReasoningEffortSuffix:           true,
			MapReasoningSuffixToAnthropicThinking: true,
			UpstreamMaxOutputTokens:               scenario.providerMaxOutputTokens,
			ForceUpstreamMaxOutputTokens:          scenario.forceProviderMaxOutputTokens,
			ModelMap:                              []config.ModelMapEntry{config.NewModelMapEntry("gpt-5", "claude-sonnet-4-5")},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, scenario.path, strings.NewReader(scenario.body))
	req.Header.Set("Content-Type", "application/json")
	if scenario.anthropicVersion != "" {
		req.Header.Set("anthropic-version", scenario.anthropicVersion)
	}
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamPayload == nil {
		t.Fatalf("expected upstream request payload to be captured")
	}
	return upstreamPayload, rec
}

func numericJSONValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	default:
		return 0
	}
}
