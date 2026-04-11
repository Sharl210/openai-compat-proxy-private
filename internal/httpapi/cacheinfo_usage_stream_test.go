package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/cacheinfo"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/upstream"
)

func TestChatStreamRecordsUsageToCacheInfoWithoutIncludeUsage(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: response.output_text.delta\n" +
				"data: {\"delta\":\"hello\"}\n\n" +
				"event: response.completed\n" +
				"data: {\"response\":{\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"total_tokens\":18,\"input_tokens_details\":{\"cached_tokens\":5}}}}\n\n",
		))
	}))
	defer upstreamServer.Close()

	providersDir := t.TempDir()
	manager := cacheinfo.NewManager(providersDir, time.UTC, []string{"openai"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	defer manager.Stop()
	server := NewServerWithStore(config.NewStaticRuntimeStore(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstreamServer.URL,
			UpstreamAPIKey:    "test-key",
			SupportsChat:      true,
			SupportsResponses: true,
		}},
	}), manager)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"messages":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	cancel()
	manager.Stop()

	stats, err := cacheinfo.LoadProviderStats(providersDir, "openai")
	if err != nil {
		t.Fatalf("LoadProviderStats: %v", err)
	}
	if stats == nil {
		t.Fatalf("expected cache info stats to be written")
	}
	if stats.Today.InputTokens != 11 || stats.Today.OutputTokens != 7 || stats.Today.TotalTokens != 18 || stats.Today.CachedTokens != 5 {
		t.Fatalf("expected cache info totals to record stream usage, got %#v", stats.Today)
	}
}

func TestWriteChatEventCallsUsageRecorderOnCompletionWithoutIncludeUsage(t *testing.T) {
	state := &chatStreamState{
		toolMeta:        map[string]map[string]string{},
		toolIndex:       map[string]int{},
		toolSent:        map[string]bool{},
		pendingToolArgs: map[string]string{},
	}
	w := httptest.NewRecorder()
	var recorded map[string]any

	err := writeChatEvent(w, nil, state, upstream.Event{
		Event: "response.completed",
		Data: map[string]any{
			"response": map[string]any{
				"usage": map[string]any{
					"input_tokens":  3,
					"output_tokens": 4,
					"total_tokens":  7,
				},
			},
		},
	}, false, func(usage map[string]any) {
		recorded = usage
	})
	if err != nil {
		t.Fatalf("writeChatEvent: %v", err)
	}
	if recorded == nil {
		t.Fatalf("expected usage recorder to be called on completion without include_usage")
	}
	if recorded["input_tokens"] != 3 || recorded["output_tokens"] != 4 || recorded["total_tokens"] != 7 {
		t.Fatalf("expected raw usage map to be recorded, got %#v", recorded)
	}
}

func TestWriteChatEventDoesNotExtractReasoningTagsWhenThinkingTagStyleOff(t *testing.T) {
	state := &chatStreamState{
		toolMeta:         map[string]map[string]string{},
		toolIndex:        map[string]int{},
		toolSent:         map[string]bool{},
		pendingToolArgs:  map[string]string{},
		thinkingTagStyle: config.UpstreamThinkingTagStyleOff,
	}
	w := httptest.NewRecorder()

	err := writeChatEvent(w, nil, state, upstream.Event{
		Event: "response.output_text.delta",
		Data:  map[string]any{"delta": "<reasoning>literal reasoning</reasoning>final"},
	}, false, nil)
	if err != nil {
		t.Fatalf("writeChatEvent: %v", err)
	}
	body := w.Body.String()
	if strings.Contains(body, `"reasoning_content":"literal reasoning"`) {
		t.Fatalf("expected literal reasoning tags to remain in content when thinkingTagStyle=off, got %s", body)
	}
	if !strings.Contains(body, `literal reasoning`) || !strings.Contains(body, `\u003creasoning\u003e`) || !strings.Contains(body, `\u003c/reasoning\u003e`) {
		t.Fatalf("expected literal reasoning tags to stay in downstream content, got %s", body)
	}
}

func TestWriteChatEventExtractsReasoningTagsWhenThinkingTagStyleLegacy(t *testing.T) {
	state := &chatStreamState{
		toolMeta:         map[string]map[string]string{},
		toolIndex:        map[string]int{},
		toolSent:         map[string]bool{},
		pendingToolArgs:  map[string]string{},
		thinkingTagStyle: config.UpstreamThinkingTagStyleLegacy,
	}
	w := httptest.NewRecorder()

	err := writeChatEvent(w, nil, state, upstream.Event{
		Event: "response.output_text.delta",
		Data:  map[string]any{"delta": "<reasoning>literal reasoning</reasoning>final"},
	}, false, nil)
	if err != nil {
		t.Fatalf("writeChatEvent: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"reasoning_content":"literal reasoning"`) {
		t.Fatalf("expected legacy thinking tag style to extract reasoning tags, got %s", body)
	}
	if !strings.Contains(body, `"content":"final"`) {
		t.Fatalf("expected trailing final text preserved after extraction, got %s", body)
	}
}

func TestCacheInfoUsageFromMapSupportsChatUsageShape(t *testing.T) {
	parsed, ok := cacheInfoUsageFromMap(map[string]any{
		"prompt_tokens":     9,
		"completion_tokens": 6,
		"total_tokens":      15,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": 2,
		},
	}, config.UpstreamEndpointTypeResponses)
	if !ok {
		t.Fatalf("expected chat usage shape to parse")
	}
	if parsed.InputTokens != 9 || parsed.OutputTokens != 6 || parsed.TotalTokens != 15 || parsed.CachedTokens != 2 {
		t.Fatalf("unexpected parsed usage: %#v", parsed)
	}
}

func TestCacheInfoUsageFromMapIncludesCacheCreationTokens(t *testing.T) {
	parsed, ok := cacheInfoUsageFromMap(map[string]any{
		"input_tokens":  12,
		"output_tokens": 5,
		"total_tokens":  17,
		"input_tokens_details": map[string]any{
			"cached_tokens":         3,
			"cache_creation_tokens": 4,
		},
	}, config.UpstreamEndpointTypeResponses)
	if !ok {
		t.Fatalf("expected usage shape to parse")
	}
	if parsed.CachedTokens != 3 || parsed.CacheCreationTokens != 4 {
		t.Fatalf("unexpected parsed cache usage: %#v", parsed)
	}
}

func TestCacheInfoUsageFromMapNormalizesAnthropicUsageToOpenAIStyleTotals(t *testing.T) {
	parsed, ok := cacheInfoUsageFromMap(map[string]any{
		"input_tokens":                20,
		"output_tokens":               5,
		"cache_read_input_tokens":     6,
		"cache_creation_input_tokens": 4,
	}, config.UpstreamEndpointTypeAnthropic)
	if !ok {
		t.Fatalf("expected anthropic usage shape to parse")
	}
	if parsed.InputTokens != 30 || parsed.OutputTokens != 5 || parsed.TotalTokens != 35 {
		t.Fatalf("expected anthropic usage normalized to openai-style totals, got %#v", parsed)
	}
	if parsed.CachedTokens != 6 || parsed.CacheCreationTokens != 4 {
		t.Fatalf("expected anthropic cache fields preserved, got %#v", parsed)
	}
}

func TestCacheInfoUsageRecorderPersistsMappedChatUsageShape(t *testing.T) {
	providersDir := t.TempDir()
	manager := cacheinfo.NewManager(providersDir, time.UTC, []string{"openai"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withCacheInfoManager(context.Background(), manager))
	recorder := cacheInfoUsageRecorder(req, "req-1", "openai", config.UpstreamEndpointTypeResponses)
	if recorder == nil {
		t.Fatalf("expected recorder to be created")
	}

	recorder(map[string]any{
		"prompt_tokens":     5,
		"completion_tokens": 4,
		"total_tokens":      9,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": 1,
		},
	})

	cancel()
	manager.Stop()
	stats, err := cacheinfo.LoadProviderStats(providersDir, "openai")
	if err != nil {
		t.Fatalf("LoadProviderStats: %v", err)
	}
	if stats == nil || stats.Today.InputTokens != 5 || stats.Today.OutputTokens != 4 || stats.Today.TotalTokens != 9 || stats.Today.CachedTokens != 1 {
		t.Fatalf("expected recorder to persist mapped chat usage, got %#v", stats)
	}
	if stats.Today.RequestCount != 1 {
		t.Fatalf("expected successful request count to be 1, got %#v", stats.Today)
	}
}

func TestCacheInfoUsageRecorderPersistsCacheCreationTokens(t *testing.T) {
	providersDir := t.TempDir()
	manager := cacheinfo.NewManager(providersDir, time.UTC, []string{"openai"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withCacheInfoManager(context.Background(), manager))
	recorder := cacheInfoUsageRecorder(req, "req-creation", "openai", config.UpstreamEndpointTypeResponses)
	if recorder == nil {
		t.Fatalf("expected recorder to be created")
	}

	recorder(map[string]any{
		"input_tokens":                20,
		"output_tokens":               5,
		"total_tokens":                25,
		"cache_read_input_tokens":     6,
		"cache_creation_input_tokens": 4,
	})

	cancel()
	manager.Stop()
	stats, err := cacheinfo.LoadProviderStats(providersDir, "openai")
	if err != nil {
		t.Fatalf("LoadProviderStats: %v", err)
	}
	if stats == nil || stats.Today.CachedTokens != 6 || stats.Today.CacheCreationTokens != 4 {
		t.Fatalf("expected recorder to persist cache read and creation tokens, got %#v", stats)
	}
}

func TestCacheInfoUsageRecorderNormalizesAnthropicUsagePerRequest(t *testing.T) {
	providersDir := t.TempDir()
	manager := cacheinfo.NewManager(providersDir, time.UTC, []string{"claude"}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withCacheInfoManager(context.Background(), manager))
	recorder := cacheInfoUsageRecorder(req, "req-anthropic", "claude", config.UpstreamEndpointTypeAnthropic)
	if recorder == nil {
		t.Fatalf("expected recorder to be created")
	}

	recorder(map[string]any{
		"input_tokens":                20,
		"output_tokens":               5,
		"cache_read_input_tokens":     6,
		"cache_creation_input_tokens": 4,
	})

	cancel()
	manager.Stop()
	stats, err := cacheinfo.LoadProviderStats(providersDir, "claude")
	if err != nil {
		t.Fatalf("LoadProviderStats: %v", err)
	}
	if stats == nil {
		t.Fatalf("expected anthropic provider stats to be written")
	}
	if stats.Today.InputTokens != 30 || stats.Today.OutputTokens != 5 || stats.Today.TotalTokens != 35 {
		t.Fatalf("expected anthropic usage converted to openai-style totals, got %#v", stats.Today)
	}
	if stats.Today.CachedTokens != 6 || stats.Today.CacheCreationTokens != 4 {
		t.Fatalf("expected anthropic cached and cache-creation tokens preserved, got %#v", stats.Today)
	}
}

func TestCacheInfoUsageFromMapKeepsCanonicalAnthropicTotalsStable(t *testing.T) {
	parsed, ok := cacheInfoUsageFromMap(map[string]any{
		"input_tokens":  30,
		"output_tokens": 5,
		"total_tokens":  35,
		"input_tokens_details": map[string]any{
			"cached_tokens":         6,
			"cache_creation_tokens": 4,
		},
	}, config.UpstreamEndpointTypeAnthropic)
	if !ok {
		t.Fatalf("expected canonical anthropic usage shape to parse")
	}
	if parsed.InputTokens != 30 || parsed.OutputTokens != 5 || parsed.TotalTokens != 35 {
		t.Fatalf("expected canonical anthropic totals to remain stable, got %#v", parsed)
	}
	if parsed.CachedTokens != 6 || parsed.CacheCreationTokens != 4 {
		t.Fatalf("expected anthropic cache fields preserved, got %#v", parsed)
	}
}
