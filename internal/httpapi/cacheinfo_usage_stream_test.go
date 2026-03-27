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

func TestCacheInfoUsageFromMapSupportsChatUsageShape(t *testing.T) {
	parsed, ok := cacheInfoUsageFromMap(map[string]any{
		"prompt_tokens":     9,
		"completion_tokens": 6,
		"total_tokens":      15,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": 2,
		},
	})
	if !ok {
		t.Fatalf("expected chat usage shape to parse")
	}
	if parsed.InputTokens != 9 || parsed.OutputTokens != 6 || parsed.TotalTokens != 15 || parsed.CachedTokens != 2 {
		t.Fatalf("unexpected parsed usage: %#v", parsed)
	}
}

func TestCacheInfoUsageRecorderPersistsMappedChatUsageShape(t *testing.T) {
	providersDir := t.TempDir()
	manager := cacheinfo.NewManager(providersDir, time.UTC, []string{"openai"}, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(withCacheInfoManager(context.Background(), manager))
	recorder := cacheInfoUsageRecorder(req, "req-1", "openai")
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

	stats, err := cacheinfo.LoadProviderStats(providersDir, "openai")
	if err != nil {
		t.Fatalf("LoadProviderStats: %v", err)
	}
	if stats == nil || stats.Today.InputTokens != 5 || stats.Today.OutputTokens != 4 || stats.Today.TotalTokens != 9 || stats.Today.CachedTokens != 1 {
		t.Fatalf("expected recorder to persist mapped chat usage, got %#v", stats)
	}
}
