package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	modelpkg "openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/testutil"
	"openai-compat-proxy/internal/tokenestimator"
)

func TestResponsesSuccessSetsModelLimitContextHeader(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:           "openai",
		EnableLegacyV1Routes:      true,
		EnableNoPromptModelSuffix: true,
		Providers: []config.ProviderConfig{{
			ID:                      "openai",
			Enabled:                 true,
			SupportsResponses:       true,
			ManualModels:            []string{"gpt-5.5"},
			ModelLimitContextTokens: -1,
			UpstreamEndpointType:    config.UpstreamEndpointTypeResponses,
			UpstreamBaseURL:         upstream.URL,
			UpstreamAPIKey:          "test-key",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello"}`))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "-1" {
		t.Fatalf("expected context limit header -1, got %q", got)
	}
	if got := rec.Header().Get(headerProxyEstimatedInputTokens); got == "" || got == "0" {
		t.Fatalf("expected estimated input tokens header, got %q", got)
	}
}

func TestResponsesContextLimitReturnsOpenAIOverflowShape(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                      "openai",
			Enabled:                 true,
			SupportsResponses:       true,
			ManualModels:            []string{"gpt-5.5"},
			ModelLimitContextTokens: 1,
			UpstreamEndpointType:    config.UpstreamEndpointTypeResponses,
			UpstreamBaseURL:         "https://upstream.invalid/v1",
			UpstreamAPIKey:          "test-key",
		}},
	})
	body := `{"model":"gpt-5.5","input":[{"role":"user","content":"` + strings.Repeat("hello ", 20) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 context overflow, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "1" {
		t.Fatalf("expected context limit header 1, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "context_length_exceeded") || !strings.Contains(rec.Body.String(), "prompt is too long") {
		t.Fatalf("expected opencode-compatible context overflow body, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "estimated input tokens") || !strings.Contains(rec.Body.String(), "exceed maximum 1") {
		t.Fatalf("expected context overflow body to expose estimated token signal, got %s", rec.Body.String())
	}
}

func TestResponsesContextLimitScopedRulesUseFinalUpstreamModelAfterModelMap(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                      "openai",
			Enabled:                 true,
			SupportsResponses:       true,
			ManualModels:            []string{"client-gpt"},
			ModelMap:                []config.ModelMapEntry{config.NewModelMapEntry("client-gpt", "upstream-gpt")},
			ModelLimitContextTokens: -1,
			ModelLimitContextTokenRules: []config.ScopedIntRule{
				exactScopedRule("upstream-gpt", 999999),
			},
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			UpstreamBaseURL:      "https://upstream.invalid/v1",
			UpstreamAPIKey:       "test-key",
		}},
	})
	body := `{"model":"client-gpt","input":[{"role":"user","content":"` + strings.Repeat("hello ", 20) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected request to pass proxy context limit and fail later on unreachable upstream, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "999999" {
		t.Fatalf("expected context limit header from final upstream model rule, got %q", got)
	}
	if strings.Contains(rec.Body.String(), "context_length_exceeded") {
		t.Fatalf("expected final upstream model rule to avoid proxy context overflow, got %s", rec.Body.String())
	}
}

func TestChatContextLimitReturnsOpenAIOverflowShape(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                      "openai",
			Enabled:                 true,
			SupportsChat:            true,
			ManualModels:            []string{"gpt-5.5"},
			ModelLimitContextTokens: 1,
			UpstreamEndpointType:    config.UpstreamEndpointTypeChat,
			UpstreamBaseURL:         "https://upstream.invalid/v1",
			UpstreamAPIKey:          "test-key",
		}},
	})
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"` + strings.Repeat("hello ", 20) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 context overflow, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "1" {
		t.Fatalf("expected context limit header 1, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "context_length_exceeded") || !strings.Contains(rec.Body.String(), "prompt is too long") {
		t.Fatalf("expected opencode-compatible context overflow body, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "estimated input tokens") || !strings.Contains(rec.Body.String(), "exceed maximum 1") {
		t.Fatalf("expected context overflow body to expose estimated token signal, got %s", rec.Body.String())
	}
}

func TestAnthropicContextLimitReturnsAnthropicOverflowShape(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "openai",
			Enabled:                   true,
			SupportsAnthropicMessages: true,
			ManualModels:              []string{"claude-sonnet-4-5"},
			ModelLimitContextTokens:   1,
			UpstreamEndpointType:      config.UpstreamEndpointTypeAnthropic,
			UpstreamBaseURL:           "https://upstream.invalid",
			UpstreamAPIKey:            "test-key",
		}},
	})
	body := `{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":"` + strings.Repeat("hello ", 20) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 context overflow, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "1" {
		t.Fatalf("expected context limit header 1, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "context_length_exceeded") || !strings.Contains(rec.Body.String(), "prompt is too long") {
		t.Fatalf("expected opencode-compatible context overflow body, got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "estimated input tokens") || !strings.Contains(rec.Body.String(), "exceed maximum 1") {
		t.Fatalf("expected context overflow body to expose estimated token signal, got %s", rec.Body.String())
	}
}

func TestEstimateCanonicalInputTokensDoesNotDoubleCountProjectedResponsesItems(t *testing.T) {
	message := modelpkg.CanonicalMessage{
		Role:  "user",
		Parts: []modelpkg.CanonicalContentPart{{Type: "text", Text: "hello world"}},
	}
	canonFromMessages := modelpkg.CanonicalRequest{
		Model:    "gpt-5.5",
		Messages: []modelpkg.CanonicalMessage{message},
	}
	canonWithDuplicateResponsesItem := modelpkg.CanonicalRequest{
		Model:    "gpt-5.5",
		Messages: []modelpkg.CanonicalMessage{message},
		ResponseInputItems: []map[string]any{{
			"role": "user",
			"content": []map[string]any{{
				"type": "input_text",
				"text": "hello world",
			}},
		}},
	}

	gotMessages := estimateCanonicalInputTokens(canonFromMessages)
	gotWithDuplicate := estimateCanonicalInputTokens(canonWithDuplicateResponsesItem)
	if gotWithDuplicate != gotMessages {
		t.Fatalf("expected preserved responses item projected into canonical messages to not change estimate, got messages=%d with_duplicate=%d", gotMessages, gotWithDuplicate)
	}
}

func TestEstimateCanonicalInputTokensDoesNotDoubleCountAnthropicOrderedContent(t *testing.T) {
	part := modelpkg.CanonicalContentPart{Type: "text", Text: "hello world"}
	canonOrderedOnly := modelpkg.CanonicalRequest{
		Model: "claude-sonnet-4-5",
		Messages: []modelpkg.CanonicalMessage{{
			Role:           "user",
			OrderedContent: []modelpkg.CanonicalContentBlock{{Type: "content", Part: part}},
		}},
	}
	canonWithDuplicateParts := modelpkg.CanonicalRequest{
		Model: "claude-sonnet-4-5",
		Messages: []modelpkg.CanonicalMessage{{
			Role:           "user",
			OrderedContent: []modelpkg.CanonicalContentBlock{{Type: "content", Part: part}},
			Parts:          []modelpkg.CanonicalContentPart{part},
		}},
	}

	gotOrderedOnly := estimateCanonicalInputTokens(canonOrderedOnly)
	gotWithDuplicate := estimateCanonicalInputTokens(canonWithDuplicateParts)
	if gotWithDuplicate != gotOrderedOnly {
		t.Fatalf("expected ordered anthropic content to be counted once, got ordered_only=%d with_duplicate=%d", gotOrderedOnly, gotWithDuplicate)
	}
}


func TestResponsesContextLimitUsesSmallerLearnedGuardBeforeConfiguredLimit(t *testing.T) {
	providersDir := t.TempDir()
	mgr := tokenestimator.NewManager(providersDir, time.UTC, func() []string { return []string{"openai"} })
	if err := mgr.RecordObservation("req-prev-overflow", tokenestimator.Observation{Bucket: tokenestimator.BucketKey{ProviderID: "openai", EndpointType: config.UpstreamEndpointTypeResponses, Model: "gpt-5.5"}, BaseEstimate: 100, InputTokens: 390, CachedTokens: 0, UncachedInputTokens: 390, Shape: tokenestimator.ShapePlain, ProtocolSignature: "responses:v1", EstimatorSignature: "base-estimator:v1"}); err != nil {
		t.Fatalf("RecordObservation error: %v", err)
	}
	if tightened, ok := mgr.ConservativeAdmissionLimit(tokenestimator.BucketKey{ProviderID: "openai", EndpointType: config.UpstreamEndpointTypeResponses, Model: "gpt-5.5"}, 300, tokenestimator.ShapePlain); !ok {
		t.Fatal("expected conservative admission limit")
	} else {
		t.Logf("tightened_limit=%d", tightened)
	}
	server := NewServerWithStore(config.NewStaticRuntimeStore(config.Config{ProvidersDir: providersDir, DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, SupportsResponses: true, ManualModels: []string{"gpt-5.5"}, ModelLimitContextTokens: 300, UpstreamEndpointType: config.UpstreamEndpointTypeResponses, UpstreamBaseURL: "https://upstream.invalid/v1", UpstreamAPIKey: "test-key"}}}), nil, mgr)
	body := `{"model":"gpt-5.5","input":[{"role":"user","content":"` + strings.Repeat("hello ", 220) + `"}]}`
	canon := modelpkg.CanonicalRequest{Model: "gpt-5.5", ResponseInputItems: []map[string]any{{"role": "user", "content": strings.Repeat("hello ", 220)}}}
	t.Logf("estimated_tokens=%d", estimateCanonicalInputTokens(canon))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected learned guard to reject early, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "context_length_exceeded") {
		t.Fatalf("expected context overflow body, got %s", rec.Body.String())
	}
}

func TestResponsesContextLimitPresentsDisplayedTokensInConfiguredLimitDomain(t *testing.T) {
	providersDir := t.TempDir()
	mgr := tokenestimator.NewManager(providersDir, time.UTC, func() []string { return []string{"openai"} })
	if err := mgr.RecordObservation("req-prev-overflow-separate-limits", tokenestimator.Observation{Bucket: tokenestimator.BucketKey{ProviderID: "openai", EndpointType: config.UpstreamEndpointTypeResponses, Model: "gpt-5.5"}, BaseEstimate: 100, InputTokens: 390, CachedTokens: 0, UncachedInputTokens: 390, Shape: tokenestimator.ShapePlain, ProtocolSignature: "responses:v1", EstimatorSignature: "base-estimator:v1"}); err != nil {
		t.Fatalf("RecordObservation error: %v", err)
	}
	server := NewServerWithStore(config.NewStaticRuntimeStore(config.Config{ProvidersDir: providersDir, DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, SupportsResponses: true, ManualModels: []string{"gpt-5.5"}, ModelLimitContextTokens: 300, UpstreamEndpointType: config.UpstreamEndpointTypeResponses, UpstreamBaseURL: "https://upstream.invalid/v1", UpstreamAPIKey: "test-key"}}}), nil, mgr)
	body := `{"model":"gpt-5.5","input":[{"role":"user","content":"` + strings.Repeat("hello ", 220) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected learned guard to reject early, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "300" {
		t.Fatalf("expected configured limit header 300, got %q", got)
	}
	displayedEstimate := rec.Header().Get(headerProxyEstimatedInputTokens)
	if displayedEstimate == "" || displayedEstimate == "73" || displayedEstimate == "339" {
		t.Fatalf("expected displayed estimate to be remapped into user-configured limit domain, got %q", displayedEstimate)
	}
	if !strings.Contains(rec.Body.String(), "estimated input tokens "+displayedEstimate+" exceed maximum 300") {
		t.Fatalf("expected body to use same displayed estimate and configured limit domain, got %s", rec.Body.String())
	}
}

func TestChatContextLimitUsesSmallerLearnedGuardBeforeConfiguredLimit(t *testing.T) {
	providersDir := t.TempDir()
	mgr := tokenestimator.NewManager(providersDir, time.UTC, func() []string { return []string{"openai"} })
	if err := mgr.RecordObservation("req-prev-overflow-chat", tokenestimator.Observation{Bucket: tokenestimator.BucketKey{ProviderID: "openai", EndpointType: config.UpstreamEndpointTypeChat, Model: "gpt-5.5"}, BaseEstimate: 100, InputTokens: 390, CachedTokens: 0, UncachedInputTokens: 390, Shape: tokenestimator.ShapePlain, ProtocolSignature: "chat:v1", EstimatorSignature: "base-estimator:v1"}); err != nil {
		t.Fatalf("RecordObservation error: %v", err)
	}
	server := NewServerWithStore(config.NewStaticRuntimeStore(config.Config{ProvidersDir: providersDir, DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, SupportsChat: true, ManualModels: []string{"gpt-5.5"}, ModelLimitContextTokens: 300, UpstreamEndpointType: config.UpstreamEndpointTypeChat, UpstreamBaseURL: "https://upstream.invalid/v1", UpstreamAPIKey: "test-key"}}}), nil, mgr)
	body := `{"model":"gpt-5.5","messages":[{"role":"user","content":"` + strings.Repeat("hello ", 220) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected learned guard to reject early, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAnthropicContextLimitUsesSmallerLearnedGuardBeforeConfiguredLimit(t *testing.T) {
	providersDir := t.TempDir()
	mgr := tokenestimator.NewManager(providersDir, time.UTC, func() []string { return []string{"openai"} })
	if err := mgr.RecordObservation("req-prev-overflow-messages", tokenestimator.Observation{Bucket: tokenestimator.BucketKey{ProviderID: "openai", EndpointType: config.UpstreamEndpointTypeAnthropic, Model: "claude-sonnet-4-5"}, BaseEstimate: 100, InputTokens: 390, CachedTokens: 0, UncachedInputTokens: 390, Shape: tokenestimator.ShapePlain, ProtocolSignature: "anthropic:v1", EstimatorSignature: "base-estimator:v1"}); err != nil {
		t.Fatalf("RecordObservation error: %v", err)
	}
	server := NewServerWithStore(config.NewStaticRuntimeStore(config.Config{ProvidersDir: providersDir, DefaultProvider: "openai", EnableLegacyV1Routes: true, Providers: []config.ProviderConfig{{ID: "openai", Enabled: true, SupportsAnthropicMessages: true, ManualModels: []string{"claude-sonnet-4-5"}, ModelLimitContextTokens: 300, UpstreamEndpointType: config.UpstreamEndpointTypeAnthropic, UpstreamBaseURL: "https://upstream.invalid", UpstreamAPIKey: "test-key"}}}), nil, mgr)
	body := `{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":"` + strings.Repeat("hello ", 220) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected learned guard to reject early, got %d body=%s", rec.Code, rec.Body.String())
	}
}
