package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	modelpkg "openai-compat-proxy/internal/model"
)

func exactScopedRule(pattern string, tokens int) config.ScopedIntRule {
	return config.ScopedIntRule{Pattern: pattern, Tokens: tokens, IsExact: true}
}

func regexScopedRule(pattern string, tokens int) config.ScopedIntRule {
	return config.ScopedIntRule{Pattern: pattern, Tokens: tokens, PatternRE: regexp.MustCompile("^(?:" + strings.TrimPrefix(pattern, "#re:") + ")$")}
}

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

func TestProviderMaxOutputTokensClampsAnthropicThinkingBudget(t *testing.T) {
	payload, rec := serveProviderMaxOutputTokensRequest(t, providerMaxOutputTokensScenario{
		path:                         "/v1/responses",
		body:                         `{"model":"gpt-5-xhigh","input":[{"role":"user","content":"hello"}]}`,
		providerMaxOutputTokens:      20000,
		anthropicMaxThinkingBudget:   32000,
		forceProviderMaxOutputTokens: true,
	})

	if got := numericJSONValue(payload["max_tokens"]); got != 20000 {
		t.Fatalf("expected provider max_tokens 20000, got %#v payload=%#v", payload["max_tokens"], payload)
	}
	thinking, _ := payload["thinking"].(map[string]any)
	if got := numericJSONValue(thinking["budget_tokens"]); got != 19999 {
		t.Fatalf("expected provider max output tokens to clamp thinking budget below max_tokens, got %#v payload=%#v", thinking["budget_tokens"], payload)
	}
	if got := rec.Header().Get(headerProxyToUpstreamMaxOutputTokens); got != "20000" {
		t.Fatalf("expected %s 20000, got %q", headerProxyToUpstreamMaxOutputTokens, got)
	}
}

func TestApplyProviderMaxOutputTokensScopedRulesUseClientModel(t *testing.T) {
	canon := modelpkg.CanonicalRequest{Model: "claude-sonnet-4-5"}
	provider := config.ProviderConfig{
		UpstreamMaxOutputTokens: 64000,
		UpstreamMaxOutputTokenRules: []config.ScopedIntRule{
			exactScopedRule("gpt-5.5", 128000),
			regexScopedRule("#re:.*gpt-.*", 100000),
		},
	}
	applyProviderMaxOutputTokens(&canon, provider, "gpt-5.5")
	if canon.MaxOutputTokens == nil || *canon.MaxOutputTokens != 128000 {
		t.Fatalf("expected client model exact rule to set 128000, got %#v", canon.MaxOutputTokens)
	}
}

func TestProviderScopedMaxOutputRulesApplyBeforeResolvedModelRewrite(t *testing.T) {
	canon := modelpkg.CanonicalRequest{Model: "gpt-5.5"}
	provider := config.ProviderConfig{
		ModelMap:                []config.ModelMapEntry{config.NewModelMapEntry("gpt-5.5", "claude-sonnet-4-5")},
		UpstreamMaxOutputTokens: 64000,
		UpstreamMaxOutputTokenRules: []config.ScopedIntRule{
			exactScopedRule("gpt-5.5", 128000),
			regexScopedRule("#re:.*gpt-.*", 100000),
		},
	}
	clientModel := canon.Model
	canon.Model = provider.ResolveModel(canon.Model, true)
	applyProviderMaxOutputTokens(&canon, provider, clientModel)
	if canon.MaxOutputTokens == nil || *canon.MaxOutputTokens != 128000 {
		t.Fatalf("expected scoped rule to use original client model before resolved model rewrite, got %#v model=%q", canon.MaxOutputTokens, canon.Model)
	}
}

func TestProviderMaxOutputTokensScopedRulesPreferSpecificMatch(t *testing.T) {
	payload, rec := serveProviderMaxOutputTokensRequest(t, providerMaxOutputTokensScenario{
		path:                        "/v1/responses",
		body:                        `{"model":"gpt-5.5","input":[{"role":"user","content":"hello"}]}`,
		providerMaxOutputTokens:     64000,
		providerMaxOutputTokenRules: []config.ScopedIntRule{exactScopedRule("gpt-5.5", 128000), regexScopedRule("#re:.*gpt-.*", 100000)},
	})
	if got := rec.Header().Get(headerProxyToUpstreamMaxOutputTokens); got != "128000" {
		t.Fatalf("expected %s 128000, got %q", headerProxyToUpstreamMaxOutputTokens, got)
	}
	if got := numericJSONValue(payload["max_tokens"]); got != 128000 {
		t.Fatalf("expected exact model rule to win with 128000, got %#v payload=%#v", payload["max_tokens"], payload)
	}
}

func TestProviderMaxOutputTokensScopedRulesFallbackToRegexThenDefault(t *testing.T) {
	regexPayload, regexRec := serveProviderMaxOutputTokensRequest(t, providerMaxOutputTokensScenario{
		path:                        "/v1/responses",
		body:                        `{"model":"gpt-5.4-mini","input":[{"role":"user","content":"hello"}]}`,
		providerMaxOutputTokens:     64000,
		providerMaxOutputTokenRules: []config.ScopedIntRule{exactScopedRule("gpt-5.5", 128000), regexScopedRule("#re:.*gpt-.*", 100000)},
	})
	if got := regexRec.Header().Get(headerProxyToUpstreamMaxOutputTokens); got != "100000" {
		t.Fatalf("expected %s 100000, got %q", headerProxyToUpstreamMaxOutputTokens, got)
	}
	if got := numericJSONValue(regexPayload["max_tokens"]); got != 100000 {
		t.Fatalf("expected regex rule to apply with 100000, got %#v payload=%#v", regexPayload["max_tokens"], regexPayload)
	}

	defaultPayload, defaultRec := serveProviderMaxOutputTokensRequest(t, providerMaxOutputTokensScenario{
		path:                        "/v1/responses",
		body:                        `{"model":"claude-sonnet-4-5","input":[{"role":"user","content":"hello"}]}`,
		providerMaxOutputTokens:     64000,
		providerMaxOutputTokenRules: []config.ScopedIntRule{exactScopedRule("gpt-5.5", 128000), regexScopedRule("#re:.*gpt-.*", 100000)},
	})
	if got := defaultRec.Header().Get(headerProxyToUpstreamMaxOutputTokens); got != "64000" {
		t.Fatalf("expected %s 64000, got %q", headerProxyToUpstreamMaxOutputTokens, got)
	}
	if got := numericJSONValue(defaultPayload["max_tokens"]); got != 64000 {
		t.Fatalf("expected default limit 64000 when no scoped rule matches, got %#v payload=%#v", defaultPayload["max_tokens"], defaultPayload)
	}
}

func TestProviderMaxOutputTokensScopedRulesTreatModelNameAsLiteral(t *testing.T) {
	payload, rec := serveProviderMaxOutputTokensRequest(t, providerMaxOutputTokensScenario{
		path:                        "/v1/responses",
		body:                        `{"model":"gpt-5.5-high-noprompt","input":[{"role":"user","content":"hello"}]}`,
		providerMaxOutputTokens:     64000,
		providerMaxOutputTokenRules: []config.ScopedIntRule{exactScopedRule("gpt-5.5", 128000), regexScopedRule("#re:.*gpt-.*", 100000)},
	})
	if got := numericJSONValue(payload["max_tokens"]); got != 100000 {
		t.Fatalf("expected literal exact rule not to strip suffixes, so regex rule should win with 100000, got %#v payload=%#v", payload["max_tokens"], payload)
	}
	if got := rec.Header().Get(headerProxyToUpstreamMaxOutputTokens); got != "100000" {
		t.Fatalf("expected %s 100000, got %q", headerProxyToUpstreamMaxOutputTokens, got)
	}
}

func TestDecodeAndResolveResponsesRequestKeepsClientModelAndScopedRules(t *testing.T) {
	store := config.NewStaticRuntimeStore(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			SupportsResponses:           true,
			UpstreamBaseURL:             "https://example.com",
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeAnthropic,
			UpstreamMaxOutputTokens:     64000,
			UpstreamMaxOutputTokenRules: []config.ScopedIntRule{exactScopedRule("gpt-5.5", 128000), regexScopedRule("#re:.*gpt-.*", 100000)},
			ModelMap:                    []config.ModelMapEntry{config.NewModelMapEntry("gpt-5.5", "claude-sonnet-4-5")},
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.Clone(withRuntimeSnapshot(withRouteInfo(req.Context(), routeInfo{ProviderID: "openai", Legacy: true, CanonicalPath: canonicalV1ResponsesPath}), store.Active()))
	rec := httptest.NewRecorder()
	initial, ok := decodeAndResolveResponsesRequest(rec, req)
	if !ok || initial == nil {
		t.Fatalf("expected decodeAndResolveResponsesRequest success, body=%s", rec.Body.String())
	}
	if initial.clientModel != "gpt-5.5" {
		t.Fatalf("expected client model gpt-5.5, got %q", initial.clientModel)
	}
	if len(initial.provider.UpstreamMaxOutputTokenRules) != 2 {
		t.Fatalf("expected provider scoped rules preserved, got %#v", initial.provider.UpstreamMaxOutputTokenRules)
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
	providerMaxOutputTokenRules  []config.ScopedIntRule
	anthropicMaxThinkingBudget   int
	forceProviderMaxOutputTokens bool
}

func serveProviderMaxOutputTokensRequest(t *testing.T, scenario providerMaxOutputTokensScenario) (map[string]any, *httptest.ResponseRecorder) {
	t.Helper()
	anthropicMaxThinkingBudget := scenario.anthropicMaxThinkingBudget
	if anthropicMaxThinkingBudget == 0 {
		anthropicMaxThinkingBudget = 32000
	}

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
		AnthropicMaxThinkingBudget:  32000,
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
			AnthropicMaxThinkingBudget:            anthropicMaxThinkingBudget,
			UpstreamMaxOutputTokens:               scenario.providerMaxOutputTokens,
			UpstreamMaxOutputTokenRules:           scenario.providerMaxOutputTokenRules,
			ForceUpstreamMaxOutputTokens:          scenario.forceProviderMaxOutputTokens,
			ModelMap: []config.ModelMapEntry{
				config.NewModelMapEntry("gpt-5", "claude-sonnet-4-5"),
				config.NewModelMapEntry("gpt-5.5", "claude-sonnet-4-5"),
				config.NewModelMapEntry("gpt-5.4-mini", "claude-sonnet-4-5"),
				config.NewModelMapEntry("gpt-5.5-high-noprompt", "claude-sonnet-4-5"),
				config.NewModelMapEntry("claude-sonnet-4-5", "claude-sonnet-4-5"),
			},
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
