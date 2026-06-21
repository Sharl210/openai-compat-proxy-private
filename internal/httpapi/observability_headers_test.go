package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestResponsesRouteExposesDirectionalObservabilityHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableNoPromptModelSuffix:   true,
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeChat,
			SupportsResponses:           true,
			SupportsChat:                true,
			EnableReasoningEffortSuffix: true,
			ModelMap:                    []config.ModelMapEntry{config.NewModelMapEntry("gpt-5", "claude-sonnet-4-5")},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-high","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertDirectionalObservabilityHeaders(t, rec, directionalHeaderExpectation{
		clientModel:               "gpt-5-high",
		clientNoPrompt:            "false",
		clientServiceTier:         "",
		clientReasoningParameters: map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}},
		clientReasoningEffort:     "high",
		proxyUpstreamModel:        "claude-sonnet-4-5",
		proxyUpstreamServiceTier:  "",
		proxyReasoningEffort:      "high",
		proxyReasoningPayload:     map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}},
	})
	if !strings.Contains(rec.Body.String(), `"output_text"`) {
		t.Fatalf("expected responses payload, got %s", rec.Body.String())
	}
}

func TestProviderConfigForIDMasqueradeClientVersionOverridesRootAndInheritsEmpty(t *testing.T) {
	snapshot := &config.RuntimeSnapshot{Config: config.Config{
		UpstreamMasqueradeClientVersion: "root-version",
		Providers: []config.ProviderConfig{
			{ID: "inherit", Enabled: true},
			{ID: "override", Enabled: true, MasqueradeClientVersion: "provider-version"},
		},
	}}

	if got := providerConfigForID(snapshot, "inherit").UpstreamMasqueradeClientVersion; got != "root-version" {
		t.Fatalf("expected empty provider masquerade client version to inherit root, got %q", got)
	}
	if got := providerConfigForID(snapshot, "override").UpstreamMasqueradeClientVersion; got != "provider-version" {
		t.Fatalf("expected provider masquerade client version to override root, got %q", got)
	}
}

func TestRouteObservabilityHeadersExposeMasqueradeUserAgent(t *testing.T) {
	var upstreamUserAgent string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamUserAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:                 "openai",
		EnableLegacyV1Routes:            true,
		UpstreamMasqueradeClientVersion: "9.8.7",
		DownstreamNonStreamStrategy:     config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			MasqueradeTarget:     config.MasqueradeTargetOpenCode,
			SupportsResponses:    true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	want := "opencode/9.8.7 ai-sdk/provider-utils/4.0.27 runtime/bun/1.3.14"
	if got := rec.Header().Get(headerProxyToUpstreamMasqueradeUserAgent); got != want {
		t.Fatalf("expected masquerade user-agent observability header %q, got %q", want, got)
	}
	if upstreamUserAgent != want {
		t.Fatalf("expected upstream to receive masquerade user-agent %q, got %q", want, upstreamUserAgent)
	}
}

func TestRouteObservabilityHeadersExposeEmptyMasqueradeUserAgentWhenDisabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			MasqueradeTarget:     config.MasqueradeTargetNone,
			SupportsResponses:    true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertHeaderPresence(t, rec, headerProxyToUpstreamMasqueradeUserAgent)
	if got := rec.Header().Get(headerProxyToUpstreamMasqueradeUserAgent); got != "" {
		t.Fatalf("expected disabled masquerade user-agent observability header to be empty, got %q", got)
	}
}

func TestRouteObservabilityHeadersExposeClaudeMetadataIdentityWhenClaudeMasqueradeEnabled(t *testing.T) {
	configuredDeviceID := strings.Repeat("d", 64)
	configuredAccountUUID := "00000000-0000-4000-8000-000000000003"
	var upstreamMetadata struct {
		DeviceID    string `json:"device_id"`
		AccountUUID string `json:"account_uuid"`
		SessionID   string `json:"session_id"`
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		metadata, _ := payload["metadata"].(map[string]any)
		userID, _ := metadata["user_id"].(string)
		if err := json.Unmarshal([]byte(userID), &upstreamMetadata); err != nil {
			t.Fatalf("decode metadata.user_id %q: %v", userID, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:                "openai",
		EnableLegacyV1Routes:           true,
		DownstreamNonStreamStrategy:    config.DownstreamNonStreamStrategyUpstreamNonStream,
		UpstreamEndpointType:           config.UpstreamEndpointTypeAnthropic,
		MasqueradeTarget:               config.MasqueradeTargetClaude,
		InjectClaudeCodeMetadataUserID: true,
		ClaudeCodeMetadataDeviceID:     configuredDeviceID,
		ClaudeCodeMetadataAccountUUID:  configuredAccountUUID,
		Providers: []config.ProviderConfig{{
			ID:                        "openai",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			MasqueradeTarget:          config.MasqueradeTargetClaude,
			SupportsAnthropicMessages: true,
			ManualModels:              []string{"claude-sonnet-4-5"},
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertHeaderPresence(t, rec, headerProxyToUpstreamClaudeMetadataDeviceID)
	assertHeaderPresence(t, rec, headerProxyToUpstreamClaudeMetadataAccountUUID)
	assertHeaderPresence(t, rec, headerProxyToUpstreamClaudeMetadataSessionID)
	if got := rec.Header().Get(headerProxyToUpstreamClaudeMetadataDeviceID); got != configuredDeviceID || got != upstreamMetadata.DeviceID {
		t.Fatalf("expected metadata device header/body %q, got header=%q body=%q", configuredDeviceID, got, upstreamMetadata.DeviceID)
	}
	if got := rec.Header().Get(headerProxyToUpstreamClaudeMetadataAccountUUID); got != configuredAccountUUID || got != upstreamMetadata.AccountUUID {
		t.Fatalf("expected metadata account header/body %q, got header=%q body=%q", configuredAccountUUID, got, upstreamMetadata.AccountUUID)
	}
	if got := rec.Header().Get(headerProxyToUpstreamClaudeMetadataSessionID); got == "" || got != upstreamMetadata.SessionID || !validUUIDForHTTPAPITest(got) {
		t.Fatalf("expected metadata session header to match valid body UUID, got header=%q body=%q", got, upstreamMetadata.SessionID)
	}
}

func TestRouteObservabilityHeadersExposeEmptyClaudeMetadataWhenClaudeMasqueradeDisabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		MasqueradeTarget:            config.MasqueradeTargetNone,
		Providers: []config.ProviderConfig{{
			ID:                        "openai",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			MasqueradeTarget:          config.MasqueradeTargetNone,
			SupportsAnthropicMessages: true,
			ManualModels:              []string{"claude-sonnet-4-5"},
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	for _, header := range []string{
		headerProxyToUpstreamClaudeMetadataDeviceID,
		headerProxyToUpstreamClaudeMetadataAccountUUID,
		headerProxyToUpstreamClaudeMetadataSessionID,
	} {
		assertHeaderPresence(t, rec, header)
		if got := rec.Header().Get(header); got != "" {
			t.Fatalf("expected disabled Claude metadata header %s to be empty, got %q", header, got)
		}
	}
}

func TestRouteObservabilityHeadersExposeRetryCacheControlAndCacheRates(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`))
	}))
	defer upstream.Close()

	cacheMgr := cacheinfo.NewManager(t.TempDir(), time.UTC, []string{"openai", "fallback"}, nil)
	if err := cacheMgr.RecordFinalUsage("req-openai", "openai", &cacheinfo.Usage{InputTokens: 100, CachedTokens: 25, TotalTokens: 100}); err != nil {
		t.Fatalf("RecordFinalUsage openai: %v", err)
	}
	if err := cacheMgr.RecordFinalUsage("req-fallback", "fallback", &cacheinfo.Usage{InputTokens: 100, CachedTokens: 50, TotalTokens: 100}); err != nil {
		t.Fatalf("RecordFinalUsage fallback: %v", err)
	}

	server := NewServerWithStore(config.NewStaticRuntimeStore(config.Config{
		DefaultProvider:             "openai,fallback",
		EnableLegacyV1Routes:        true,
		UpstreamRetryCount:          3,
		UpstreamRetryDelay:          5 * time.Second,
		UpstreamCacheControl:        config.UpstreamCacheControl5Min,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{
			{
				ID:                      "openai",
				Enabled:                 true,
				UpstreamBaseURL:         upstream.URL,
				UpstreamAPIKey:          "test-key",
				UpstreamEndpointType:    config.UpstreamEndpointTypeResponses,
				SupportsResponses:       true,
				ManualModels:            []string{"gpt-5"},
				UpstreamRetryCountSet:   true,
				UpstreamRetryCount:      4,
				UpstreamRetryDelaySet:   true,
				UpstreamRetryDelay:      7 * time.Second,
				UpstreamCacheControlSet: true,
				UpstreamCacheControl:    config.UpstreamCacheControl1H,
			},
			{
				ID:                   "fallback",
				Enabled:              true,
				UpstreamBaseURL:      upstream.URL,
				UpstreamAPIKey:       "test-key",
				UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
				SupportsResponses:    true,
				ManualModels:         []string{"fallback-model"},
			},
		},
	}), cacheMgr, nil)

	explicitReq := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	explicitReq.Header.Set("Content-Type", "application/json")
	explicitRec := httptest.NewRecorder()
	server.ServeHTTP(explicitRec, explicitReq)
	if explicitRec.Code != http.StatusOK {
		t.Fatalf("expected explicit route status 200, got %d body=%s", explicitRec.Code, explicitRec.Body.String())
	}
	if got := explicitRec.Header().Get(headerProviderTodayCacheRate); got != "25.00 %" {
		t.Fatalf("expected explicit provider today cache rate 25.00 %%, got %q", got)
	}
	if got := explicitRec.Header().Get(headerRootProviderTodayCacheRate); got != "" {
		t.Fatalf("expected explicit provider route to omit root provider cache rate, got %q", got)
	}

	rootReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	rootReq.Header.Set("Content-Type", "application/json")
	rootRec := httptest.NewRecorder()
	server.ServeHTTP(rootRec, rootReq)
	if rootRec.Code != http.StatusOK {
		t.Fatalf("expected root route status 200, got %d body=%s", rootRec.Code, rootRec.Body.String())
	}
	if got := rootRec.Header().Get(headerProxyUpstreamRetryCount); got != "4" {
		t.Fatalf("expected %s 4, got %q", headerProxyUpstreamRetryCount, got)
	}
	if got := rootRec.Header().Get(headerProxyUpstreamRetryDelay); got != "7s" {
		t.Fatalf("expected %s 7s, got %q", headerProxyUpstreamRetryDelay, got)
	}
	if got := rootRec.Header().Get(headerProxyUpstreamAnthropicCacheControl); got != config.UpstreamCacheControl1H {
		t.Fatalf("expected %s %q, got %q", headerProxyUpstreamAnthropicCacheControl, config.UpstreamCacheControl1H, got)
	}
	if got := rootRec.Header().Get(headerProviderTodayCacheRate); got != "25.00 %" {
		t.Fatalf("expected provider today cache rate 25.00 %%, got %q", got)
	}
	if got := rootRec.Header().Get(headerProviderHistoryCacheRate); got != "25.00 %" {
		t.Fatalf("expected provider history cache rate 25.00 %%, got %q", got)
	}
	if got := rootRec.Header().Get(headerRootProviderTodayCacheRate); got != "37.50 %" {
		t.Fatalf("expected root provider today cache rate 37.50 %%, got %q", got)
	}
	if got := rootRec.Header().Get(headerRootProviderHistoryCacheRate); got != "37.50 %" {
		t.Fatalf("expected root provider history cache rate 37.50 %%, got %q", got)
	}

}

func TestResponsesRouteClientReasoningEffortPrefersSuffixOverRequestBodyParameter(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableNoPromptModelSuffix:   true,
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeChat,
			SupportsResponses:           true,
			SupportsChat:                true,
			EnableReasoningEffortSuffix: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-high","reasoning":{"effort":"low","summary":"auto"},"input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerClientToProxyReasoningEffort); got != "high" {
		t.Fatalf("expected suffix-derived client reasoning effort high, got %q", got)
	}
	assertJSONHeaderEquals(t, rec.Header().Get(headerProxyToUpstreamReasoningParameters), map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}})
	if got := rec.Header().Get(headerProxyToUpstreamReasoningEffort); got != "high" {
		t.Fatalf("expected %s high, got %q", headerProxyToUpstreamReasoningEffort, got)
	}
}

func TestResponsesRouteDirectionalHeadersPreserveClientModelAndFinalUpstreamState(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_2b","object":"response","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableNoPromptModelSuffix:   true,
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			SupportsChat:                true,
			EnableReasoningEffortSuffix: true,
			SystemPromptText:            "provider system",
			SystemPromptPosition:        config.SystemPromptPositionAppend,
			ModelMap: []config.ModelMapEntry{
				config.NewModelMapEntry("gpt-5.4-mini-minimal", "gpt-5.4-mini-low"),
			},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4-mini-minimal-noprompt","reasoning":{"effort":"none","summary":"auto"},"input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertDirectionalObservabilityHeaders(t, rec, directionalHeaderExpectation{
		clientModel:               "gpt-5.4-mini-minimal-noprompt",
		clientNoPrompt:            "true",
		clientServiceTier:         "",
		clientReasoningParameters: map[string]any{"reasoning": map[string]any{"effort": "minimal", "summary": "auto"}},
		clientReasoningEffort:     "minimal",
		proxyUpstreamModel:        "gpt-5.4-mini",
		proxyUpstreamServiceTier:  "",
		proxyReasoningEffort:      "low",
		proxyReasoningPayload:     map[string]any{"reasoning": map[string]any{"effort": "low", "summary": "auto"}},
	})
}

func TestChatStreamExposesDirectionalObservabilityHeaders(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			SupportsChat:                true,
			SupportsResponses:           true,
			EnableReasoningEffortSuffix: true,
			ModelMap:                    []config.ModelMapEntry{config.NewModelMapEntry("gpt-5", "gpt-5-mini")},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5-high","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertDirectionalObservabilityHeaders(t, rec, directionalHeaderExpectation{
		clientModel:               "gpt-5-high",
		clientNoPrompt:            "false",
		clientServiceTier:         "",
		clientReasoningParameters: map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}},
		clientReasoningEffort:     "high",
		proxyUpstreamModel:        "gpt-5-mini",
		proxyUpstreamServiceTier:  "",
		proxyReasoningEffort:      "high",
		proxyReasoningPayload:     map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}},
	})
	if !strings.Contains(rec.Body.String(), `"object":"chat.completion.chunk"`) {
		t.Fatalf("expected chat stream body, got %s", rec.Body.String())
	}
}

func TestResponsesStreamKeepsUsageTokensHeaderEmpty(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1333111,\"input_tokens_details\":{\"cached_tokens\":1111001},\"output_tokens\":1231,\"total_tokens\":1334342}}}\n\n",
	})
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

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Trailer"); strings.Contains(got, headerThisUsageTokens) {
		t.Fatalf("expected stream response not to promise final usage in trailer, got %q", got)
	}
	if _, exists := rec.Header()[headerThisUsageTokens]; exists {
		t.Fatalf("expected stream response not to include %s, got %#v", headerThisUsageTokens, rec.Header()[headerThisUsageTokens])
	}
}

func TestMessagesRouteExposesDirectionalObservabilityHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                                    "anthropic",
			Enabled:                               true,
			UpstreamBaseURL:                       upstream.URL,
			UpstreamAPIKey:                        "test-key",
			UpstreamEndpointType:                  config.UpstreamEndpointTypeAnthropic,
			SupportsAnthropicMessages:             true,
			MapReasoningSuffixToAnthropicThinking: true,
			EnableReasoningEffortSuffix:           true,
			ModelMap:                              []config.ModelMapEntry{config.NewModelMapEntry("gpt-5", "claude-sonnet-4-5")},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5-high","max_tokens":4096,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	assertDirectionalObservabilityHeaders(t, rec, directionalHeaderExpectation{
		clientModel:               "gpt-5-high",
		clientNoPrompt:            "false",
		clientServiceTier:         "",
		clientReasoningParameters: map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(4095)}},
		clientReasoningEffort:     "high",
		proxyUpstreamModel:        "claude-sonnet-4-5",
		proxyUpstreamServiceTier:  "",
		proxyReasoningEffort:      "high",
		proxyReasoningPayload:     map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(4095)}},
	})
}

func TestChatRouteServiceTierHeadersRespectProviderOverride(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_3","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeChat,
			SupportsChat:         true,
			OpenAIServiceTier:    config.OpenAIServiceTierPriority,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","serviceTier":"flex","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerClientToProxyServiceTier); got != "flex" {
		t.Fatalf("expected %s flex, got %q", headerClientToProxyServiceTier, got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamServiceTier); got != "priority" {
		t.Fatalf("expected %s priority, got %q", headerProxyToUpstreamServiceTier, got)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("unmarshal upstream payload: %v body=%s", err, gotBody)
	}
	if got, _ := payload["service_tier"].(string); got != "priority" {
		t.Fatalf("expected upstream service_tier priority, got %#v body=%s", payload["service_tier"], gotBody)
	}
	if _, exists := payload["serviceTier"]; exists {
		t.Fatalf("expected upstream payload to remove serviceTier alias, got %#v body=%s", payload, gotBody)
	}
}

func TestMessagesRouteDirectThinkingInfersClientReasoningEffortHeader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           upstream.URL,
			UpstreamAPIKey:            "test-key",
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			SupportsAnthropicMessages: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5","max_tokens":256,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerClientToProxyModel); got != "claude-sonnet-4-5" {
		t.Fatalf("expected %s claude-sonnet-4-5, got %q", headerClientToProxyModel, got)
	}
	if got := rec.Header().Get(headerClientToProxyReasoningEffort); got != "minimal" {
		t.Fatalf("expected %s minimal for direct thinking without explicit effort, got %q", headerClientToProxyReasoningEffort, got)
	}
	assertJSONHeaderEquals(t, rec.Header().Get(headerClientToProxyReasoningParameters), map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(2048)}})
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "claude-sonnet-4-5" {
		t.Fatalf("expected %s claude-sonnet-4-5, got %q", headerProxyToUpstreamModel, got)
	}
	assertJSONHeaderEquals(t, rec.Header().Get(headerProxyToUpstreamReasoningParameters), map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(2048)}})
	if got := rec.Header().Get(headerProxyToUpstreamReasoningEffort); got != "minimal" {
		t.Fatalf("expected %s minimal, got %q", headerProxyToUpstreamReasoningEffort, got)
	}
}

func TestResponsesRouteWithoutAnthropicThinkingMappingPassesThroughReasoningHeaders(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_4","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "deepseek",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                                    "deepseek",
			Enabled:                               true,
			UpstreamBaseURL:                       upstream.URL,
			UpstreamAPIKey:                        "test-key",
			UpstreamEndpointType:                  config.UpstreamEndpointTypeAnthropic,
			SupportsResponses:                     true,
			SupportsAnthropicMessages:             true,
			EnableReasoningEffortSuffix:           false,
			MapReasoningSuffixToAnthropicThinking: false,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"deepseek-v4-flash","max_output_tokens":4096,"reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerClientToProxyReasoningEffort); got != "xhigh" {
		t.Fatalf("expected %s xhigh, got %q", headerClientToProxyReasoningEffort, got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamReasoningEffort); got != "xhigh" {
		t.Fatalf("expected %s xhigh when mapping is disabled, got %q", headerProxyToUpstreamReasoningEffort, got)
	}
	assertJSONHeaderEquals(t, rec.Header().Get(headerProxyToUpstreamReasoningParameters), map[string]any{"reasoning": map[string]any{"effort": "xhigh", "summary": "auto"}})
	if !strings.Contains(gotBody, `"reasoning":{"effort":"xhigh","summary":"auto"}`) {
		t.Fatalf("expected upstream body to pass through raw reasoning when mapping is disabled, got %s", gotBody)
	}
}

func TestResponsesRouteDefaultAnthropicThinkingMappingExposesUpstreamReasoningHeaders(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_5","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "deepseek",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                                    "deepseek",
			Enabled:                               true,
			UpstreamBaseURL:                       upstream.URL,
			UpstreamAPIKey:                        "test-key",
			UpstreamEndpointType:                  config.UpstreamEndpointTypeAnthropic,
			SupportsResponses:                     true,
			SupportsAnthropicMessages:             true,
			EnableReasoningEffortSuffix:           false,
			MapReasoningSuffixToAnthropicThinking: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"deepseek-v4-flash","max_output_tokens":4096,"reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerClientToProxyReasoningEffort); got != "xhigh" {
		t.Fatalf("expected %s xhigh, got %q", headerClientToProxyReasoningEffort, got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamReasoningEffort); got != "xhigh" {
		t.Fatalf("expected %s xhigh, got %q", headerProxyToUpstreamReasoningEffort, got)
	}
	assertJSONHeaderEquals(t, rec.Header().Get(headerProxyToUpstreamReasoningParameters), map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(4095)}})
	if !strings.Contains(gotBody, `"thinking":{"budget_tokens":4095,"type":"enabled"}`) && !strings.Contains(gotBody, `"thinking":{"type":"enabled","budget_tokens":4095}`) {
		t.Fatalf("expected upstream body to include Anthropic thinking, got %s", gotBody)
	}
}

func TestChatRouteWithoutAnthropicThinkingMappingPassesThroughReasoningHeaders(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_chat_1","type":"message","role":"assistant","model":"deepseek-v4-flash","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "deepseek",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                                    "deepseek",
			Enabled:                               true,
			UpstreamBaseURL:                       upstream.URL,
			UpstreamAPIKey:                        "test-key",
			UpstreamEndpointType:                  config.UpstreamEndpointTypeAnthropic,
			SupportsChat:                          true,
			SupportsAnthropicMessages:             true,
			EnableReasoningEffortSuffix:           false,
			MapReasoningSuffixToAnthropicThinking: false,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"deepseek-v4-flash","reasoning_effort":"high","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerClientToProxyReasoningEffort); got != "high" {
		t.Fatalf("expected %s high, got %q", headerClientToProxyReasoningEffort, got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamReasoningEffort); got != "high" {
		t.Fatalf("expected %s high, got %q", headerProxyToUpstreamReasoningEffort, got)
	}
	if !strings.Contains(gotBody, `"reasoning":{"effort":"high","summary":"auto"}`) {
		t.Fatalf("expected upstream body to pass through reasoning payload when mapping is disabled, got %s", gotBody)
	}
	assertJSONHeaderEquals(t, rec.Header().Get(headerProxyToUpstreamReasoningParameters), map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}})
}

func TestMessagesRouteReasoningSuffixOverridesDisabledThinkingHeaders(t *testing.T) {
	var gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.NotFound(w, r)
			return
		}
		bodyBytes, _ := io.ReadAll(r.Body)
		gotBody = string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_3","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "anthropic",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		EnableNoPromptModelSuffix:   true,
		Providers: []config.ProviderConfig{{
			ID:                                    "anthropic",
			Enabled:                               true,
			UpstreamBaseURL:                       upstream.URL,
			UpstreamAPIKey:                        "test-key",
			UpstreamEndpointType:                  config.UpstreamEndpointTypeAnthropic,
			SupportsAnthropicMessages:             true,
			ManualModels:                          []string{"claude-sonnet-4-5"},
			EnableReasoningEffortSuffix:           true,
			MapReasoningSuffixToAnthropicThinking: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5-low-noprompt","max_tokens":4096,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerClientToProxyModel); got != "claude-sonnet-4-5-low-noprompt" {
		t.Fatalf("expected %s claude-sonnet-4-5-low-noprompt, got %q", headerClientToProxyModel, got)
	}
	if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "true" {
		t.Fatalf("expected %s true, got %q", headerClientToProxyNoPrompt, got)
	}
	if got := rec.Header().Get(headerClientToProxyReasoningEffort); got != "low" {
		t.Fatalf("expected %s low, got %q", headerClientToProxyReasoningEffort, got)
	}
	expected := map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(4000)}}
	assertJSONHeaderEquals(t, rec.Header().Get(headerClientToProxyReasoningParameters), expected)
	assertJSONHeaderEquals(t, rec.Header().Get(headerProxyToUpstreamReasoningParameters), expected)
	if got := rec.Header().Get(headerProxyToUpstreamReasoningEffort); got != "low" {
		t.Fatalf("expected %s low, got %q", headerProxyToUpstreamReasoningEffort, got)
	}
	if strings.Contains(gotBody, `"thinking":{"type":"disabled"}`) {
		t.Fatalf("expected upstream body to override disabled thinking, got %s", gotBody)
	}
}

type directionalHeaderExpectation struct {
	clientModel               string
	clientNoPrompt            string
	clientServiceTier         string
	clientReasoningParameters map[string]any
	clientReasoningEffort     string
	proxyUpstreamModel        string
	proxyUpstreamServiceTier  string
	proxyReasoningEffort      string
	proxyReasoningPayload     map[string]any
}

func assertDirectionalObservabilityHeaders(t *testing.T, rec *httptest.ResponseRecorder, expected directionalHeaderExpectation) {
	t.Helper()
	assertHeaderPresence(t, rec, headerClientToProxyServiceTier)
	assertHeaderPresence(t, rec, headerClientToProxyModel)
	assertHeaderPresence(t, rec, headerClientToProxyNoPrompt)
	assertHeaderPresence(t, rec, headerProxyToUpstreamServiceTier)
	assertHeaderPresence(t, rec, headerProxyToUpstreamModel)
	assertHeaderPresence(t, rec, headerProxyToUpstreamMaxOutputTokens)
	assertHeaderPresence(t, rec, headerClientToProxyReasoningParameters)
	assertHeaderPresence(t, rec, headerClientToProxyReasoningEffort)
	assertHeaderPresence(t, rec, headerProxyToUpstreamReasoningEffort)
	assertHeaderPresence(t, rec, headerProxyToUpstreamReasoningParameters)
	assertHeaderPresence(t, rec, headerProxyToUpstreamClaudeMetadataDeviceID)
	assertHeaderPresence(t, rec, headerProxyToUpstreamClaudeMetadataAccountUUID)
	assertHeaderPresence(t, rec, headerProxyToUpstreamClaudeMetadataSessionID)
	if got := rec.Header().Get(headerClientToProxyModel); got != expected.clientModel {
		t.Fatalf("expected %s %q, got %q", headerClientToProxyModel, expected.clientModel, got)
	}
	if got := rec.Header().Get(headerClientToProxyNoPrompt); got != expected.clientNoPrompt {
		t.Fatalf("expected %s %q, got %q", headerClientToProxyNoPrompt, expected.clientNoPrompt, got)
	}
	if got := rec.Header().Get(headerClientToProxyServiceTier); got != expected.clientServiceTier {
		t.Fatalf("expected %s %q, got %q", headerClientToProxyServiceTier, expected.clientServiceTier, got)
	}
	assertOptionalJSONHeaderEquals(t, rec.Header().Get(headerClientToProxyReasoningParameters), expected.clientReasoningParameters)
	if got := rec.Header().Get(headerClientToProxyReasoningEffort); got != expected.clientReasoningEffort {
		t.Fatalf("expected %s %q, got %q", headerClientToProxyReasoningEffort, expected.clientReasoningEffort, got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != expected.proxyUpstreamModel {
		t.Fatalf("expected %s %q, got %q", headerProxyToUpstreamModel, expected.proxyUpstreamModel, got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamServiceTier); got != expected.proxyUpstreamServiceTier {
		t.Fatalf("expected %s %q, got %q", headerProxyToUpstreamServiceTier, expected.proxyUpstreamServiceTier, got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamReasoningEffort); got != expected.proxyReasoningEffort {
		t.Fatalf("expected %s %q, got %q", headerProxyToUpstreamReasoningEffort, expected.proxyReasoningEffort, got)
	}
	assertJSONHeaderEquals(t, rec.Header().Get(headerProxyToUpstreamReasoningParameters), expected.proxyReasoningPayload)
}

func assertHeaderPresence(t *testing.T, rec *httptest.ResponseRecorder, header string) {
	t.Helper()
	if _, exists := rec.Result().Header[http.CanonicalHeaderKey(header)]; !exists {
		t.Fatalf("expected header %s to be present", header)
	}
}

func validUUIDForHTTPAPITest(value string) bool {
	return regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(value)
}

func assertJSONHeaderEquals(t *testing.T, raw string, expected map[string]any) {
	t.Helper()
	if raw == "" {
		t.Fatalf("expected non-empty JSON header")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal header json: %v raw=%q", err, raw)
	}
	if diffExpected, err := json.Marshal(expected); err != nil {
		t.Fatalf("marshal expected: %v", err)
	} else if diffGot, err := json.Marshal(got); err != nil {
		t.Fatalf("marshal got: %v", err)
	} else if string(diffExpected) != string(diffGot) {
		t.Fatalf("expected header json %s, got %s", diffExpected, diffGot)
	}
}

func assertOptionalJSONHeaderEquals(t *testing.T, raw string, expected map[string]any) {
	t.Helper()
	if len(expected) == 0 {
		if raw != "" {
			t.Fatalf("expected empty JSON header, got %q", raw)
		}
		return
	}
	assertJSONHeaderEquals(t, raw, expected)
}
