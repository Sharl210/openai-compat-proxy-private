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
		ModelMap: []config.ModelMapEntry{
			{Key: "public-gpt", Target: "gpt-5.4"},
			{Key: "*", Target: "gpt-5.4"},
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

func TestRewriteModelsBodyDoesNotEmitDuplicateIDsWhenAliasAlreadyExistsUpstream(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"gpt-5.4","object":"model"},{"id":"public-gpt","object":"model","owned_by":"proxy"}]}`)
	provider := config.ProviderConfig{
		ModelMap: []config.ModelMapEntry{
			{Key: "public-gpt", Target: "gpt-5.4"},
		},
	}

	rewritten := rewriteModelsBody(body, provider)
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("decode rewritten models body: %v", err)
	}
	data, _ := payload["data"].([]any)
	countByID := map[string]int{}
	for _, item := range data {
		entry, _ := item.(map[string]any)
		countByID[entry["id"].(string)]++
	}
	if got := countByID["public-gpt"]; got != 1 {
		t.Fatalf("expected public-gpt to appear once, got count=%d data=%#v", got, data)
	}
}

func TestRewriteModelsBodyHidesAliasWhenMappedTargetIsMissing(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"gpt-5.4","object":"model"}]}`)
	provider := config.ProviderConfig{
		ModelMap: []config.ModelMapEntry{
			{Key: "ghost-alias", Target: "missing-upstream-model"},
		},
	}

	rewritten := rewriteModelsBody(body, provider)
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("decode rewritten models body: %v", err)
	}
	data, _ := payload["data"].([]any)
	for _, item := range data {
		entry, _ := item.(map[string]any)
		if entry["id"] == "ghost-alias" {
			t.Fatalf("expected ghost alias to stay hidden when target is missing, got %#v", data)
		}
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
}

func TestModelsConfiguredAliasSupportWithoutUsableUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"models not supported upstream"}}`))
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
			ModelMap: []config.ModelMapEntry{
				{Key: "public-gpt", Target: "gpt-5.4"},
				{Key: "*", Target: "gpt-5.4"},
			},
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected configured alias fallback to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode fallback models response: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected exactly one configured public alias, got %#v", data)
	}
	entry, _ := data[0].(map[string]any)
	if got, _ := entry["id"].(string); got != "public-gpt" {
		t.Fatalf("expected fallback alias public-gpt, got %#v", entry)
	}
	if _, exists := entry["owned_by"]; exists {
		t.Fatalf("expected synthetic fallback entry without upstream-only fields, got %#v", entry)
	}
}

func TestModelsFallsBackOnGenericNotFoundWhenConfiguredAliasesExist(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"route not found upstream"}}`))
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
			ModelMap: []config.ModelMapEntry{
				{Key: "public-gpt", Target: "gpt-5.4"},
			},
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected generic upstream 404 to fallback to configured aliases, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode fallback models response: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected exactly one configured public alias, got %#v", data)
	}
	entry, _ := data[0].(map[string]any)
	if got, _ := entry["id"].(string); got != "public-gpt" {
		t.Fatalf("expected fallback alias public-gpt, got %#v", entry)
	}
}

func TestModelsFallbackIncludesManualModelsWhenUpstreamNotFound(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"models not found upstream"}}`))
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
			ManualModels:    []string{"manual-alpha", "manual-beta"},
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected manual models fallback to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode fallback models response: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		ids = append(ids, entry["id"].(string))
	}
	if len(ids) != 2 || !contains(ids, "manual-alpha") || !contains(ids, "manual-beta") {
		t.Fatalf("expected manual models in fallback response, got %#v", ids)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
