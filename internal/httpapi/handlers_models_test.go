package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/reasoning"
)

func TestRewriteModelsBodyPreservesUpstreamFieldsAndFiltersWildcardAliases(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"gpt-5.4","object":"model","owned_by":"openai"}]}`)
	provider := config.ProviderConfig{
		ModelMap: map[string]string{
			"public-gpt": "gpt-5.4",
			"*":          "gpt-5.4",
		},
	}

	rewritten := rewriteModelsBody(body, provider)
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("decode rewritten models body: %v", err)
	}
	data, _ := payload["data"].([]any)
	if len(data) != 2 {
		t.Fatalf("expected upstream id plus public alias, got %#v", data)
	}
	entries := map[string]map[string]any{}
	for _, item := range data {
		entry, _ := item.(map[string]any)
		entries[entry["id"].(string)] = entry
	}
	if _, ok := entries["*"]; ok {
		t.Fatalf("expected wildcard alias to stay hidden, got %#v", entries)
	}
	if got := entries["gpt-5.4"]["owned_by"]; got != "openai" {
		t.Fatalf("expected upstream entry fields to be preserved, got %#v", entries["gpt-5.4"])
	}
	if got := entries["public-gpt"]["owned_by"]; got != "openai" {
		t.Fatalf("expected alias cloned from upstream shape, got %#v", entries["public-gpt"])
	}
}

func TestExpandModelIDsKeepsExplicitAliasesAndSkipsWildcardPatterns(t *testing.T) {
	expanded := reasoning.ExpandModelIDs([]string{"public-gpt", "gpt-5.4", "*", "gpt-5.4-high"}, []string{"public-gpt", "*"}, true)
	got := map[string]bool{}
	for _, id := range expanded {
		got[id] = true
	}
	if !got["public-gpt"] {
		t.Fatalf("expected explicit alias to remain visible, got %#v", expanded)
	}
	if !got["public-gpt-high"] {
		t.Fatalf("expected explicit alias to expand suffix variants, got %#v", expanded)
	}
	if got["*"] || got["*-high"] {
		t.Fatalf("expected wildcard patterns to stay hidden, got %#v", expanded)
	}
	if got["gpt-5.4-high-low"] {
		t.Fatalf("expected already suffixed ids to stop expanding, got %#v", expanded)
	}
}

func TestModelsUpstreamHTTPErrorMarksFailedStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream exploded"}}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "openai",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "test-key",
			SupportsModels:  true,
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected upstream status 502, got %d body=%s", rec.Code, rec.Body.String())
	}
	status := fetchStatusForTest(t, server, "openai", rec.Header().Get("X-Request-Id"))
	if status.Status != "failed" || status.HealthFlag != "upstream_error" || status.ErrorCode != "upstream_error" {
		t.Fatalf("expected upstream_error failed status, got %#v", status)
	}
}
