package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestModelsOverlayReturnsLastWinsVisibleModels(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai,azure",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{
			{
				ID:              "openai",
				Enabled:         true,
				SupportsModels:  true,
				UpstreamBaseURL: "https://openai.test",
				ModelMap: []config.ModelMapEntry{
					config.NewModelMapEntry("openai-only", "gpt-openai"),
					config.NewModelMapEntry("shared-model", "gpt-shared-openai"),
				},
			},
			{
				ID:              "azure",
				Enabled:         true,
				SupportsModels:  true,
				UpstreamBaseURL: "https://azure.test",
				ModelMap: []config.ModelMapEntry{
					config.NewModelMapEntry("shared-model", "gpt-shared-azure"),
					config.NewModelMapEntry("azure-only", "gpt-azure"),
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected overlay /v1/models to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode overlay /v1/models response: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	owners := map[string]string{}
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		ids = append(ids, id)
		owner, _ := entry["owned_by"].(string)
		owners[id] = owner
	}
	if want := []string{"shared-model", "azure-only", "openai-only"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("expected overlay model ids %v, got %v", want, ids)
	}
	if got := owners["shared-model"]; got != "azure" {
		t.Fatalf("expected shared-model owner %q, got %q", "azure", got)
	}
}

func TestModelsOverlayIncludesRealtimeUpstreamModelsFromAllDefaultProviders(t *testing.T) {
	minimaxUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer minimax-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"MiniMax-M2.7","object":"model","owned_by":"minimax-upstream"}]}`))
	}))
	defer minimaxUpstream.Close()
	codexUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer codex-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.4","object":"model","owned_by":"codex-upstream"}]}`))
	}))
	defer codexUpstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "minimax,codex",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{
			{
				ID:              "minimax",
				Enabled:         true,
				SupportsModels:  true,
				UpstreamBaseURL: minimaxUpstream.URL,
				UpstreamAPIKey:  "minimax-key",
				ManualModels:    []string{"MiniMax-M2.7"},
			},
			{
				ID:                          "codex",
				Enabled:                     true,
				SupportsModels:              true,
				UpstreamBaseURL:             codexUpstream.URL,
				UpstreamAPIKey:              "codex-key",
				EnableReasoningEffortSuffix: true,
				ExposeReasoningSuffixModels: true,
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected overlay /v1/models to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode overlay /v1/models response: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		ids = append(ids, entry["id"].(string))
	}
	if !containsString(ids, "MiniMax-M2.7") || !containsString(ids, "gpt-5.4") {
		t.Fatalf("expected overlay to merge manual and realtime upstream models, got %v", ids)
	}
}

func TestModelsOverlayUsesEachProvidersOwnUpstreamAuthorization(t *testing.T) {
	minimaxUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer minimax-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"MiniMax-M2.7","object":"model","owned_by":"minimax-upstream"}]}`))
	}))
	defer minimaxUpstream.Close()

	codexForUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer codex-for-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.1","object":"model","owned_by":"codex-for-upstream"}]}`))
	}))
	defer codexForUpstream.Close()

	codexUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer codex-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.4","object":"model","owned_by":"codex-upstream"}]}`))
	}))
	defer codexUpstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "minimax,codex-for,codex",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{
			{ID: "minimax", Enabled: true, SupportsModels: true, UpstreamBaseURL: minimaxUpstream.URL, UpstreamAPIKey: "minimax-key"},
			{ID: "codex-for", Enabled: true, SupportsModels: true, UpstreamBaseURL: codexForUpstream.URL, UpstreamAPIKey: "codex-for-key"},
			{ID: "codex", Enabled: true, SupportsModels: true, UpstreamBaseURL: codexUpstream.URL, UpstreamAPIKey: "codex-key"},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected overlay /v1/models to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode overlay /v1/models response: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		ids = append(ids, entry["id"].(string))
	}
	for _, want := range []string{"MiniMax-M2.7", "gpt-5.1", "gpt-5.4"} {
		if !containsString(ids, want) {
			t.Fatalf("expected overlay to keep provider-specific upstream auth and include %q, got %v", want, ids)
		}
	}
}

func TestModelsOverlayOnlyShowsAuthorizedDefaultProviders(t *testing.T) {
	privateUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer private-upstream-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"private-model","object":"model","owned_by":"private-upstream"}]}`))
	}))
	defer privateUpstream.Close()
	publicUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer public-upstream-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"public-model","object":"model","owned_by":"public-upstream"}]}`))
	}))
	defer publicUpstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "private,public",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{
			{ID: "private", Enabled: true, SupportsModels: true, UpstreamBaseURL: privateUpstream.URL, UpstreamAPIKey: "private-upstream-key", ProxyAPIKeyOverride: "private-secret", ProxyAPIKeyOverrideSet: true},
			{ID: "public", Enabled: true, SupportsModels: true, UpstreamBaseURL: publicUpstream.URL, UpstreamAPIKey: "public-upstream-key"},
		},
	})

	rootReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rootReq.Header.Set("Authorization", "Bearer root-secret")
	rootRec := httptest.NewRecorder()
	server.ServeHTTP(rootRec, rootReq)
	if rootRec.Code != http.StatusOK {
		t.Fatalf("expected bare models route to stay available for root-authorized providers, got %d body=%s", rootRec.Code, rootRec.Body.String())
	}
	var rootPayload map[string]any
	if err := json.Unmarshal(rootRec.Body.Bytes(), &rootPayload); err != nil {
		t.Fatalf("decode root-authorized overlay response: %v body=%s", err, rootRec.Body.String())
	}
	rootData, _ := rootPayload["data"].([]any)
	rootIDs := make([]string, 0, len(rootData))
	for _, item := range rootData {
		entry, _ := item.(map[string]any)
		rootIDs = append(rootIDs, entry["id"].(string))
	}
	if !containsString(rootIDs, "public-model") || containsString(rootIDs, "private-model") {
		t.Fatalf("expected root auth to expose only root-authorized default providers, got %v", rootIDs)
	}

	privateReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	privateReq.Header.Set("Authorization", "Bearer private-secret")
	privateRec := httptest.NewRecorder()
	server.ServeHTTP(privateRec, privateReq)
	if privateRec.Code != http.StatusOK {
		t.Fatalf("expected bare models route to accept private provider key, got %d body=%s", privateRec.Code, privateRec.Body.String())
	}
	var privatePayload map[string]any
	if err := json.Unmarshal(privateRec.Body.Bytes(), &privatePayload); err != nil {
		t.Fatalf("decode private-authorized overlay response: %v body=%s", err, privateRec.Body.String())
	}
	privateData, _ := privatePayload["data"].([]any)
	privateIDs := make([]string, 0, len(privateData))
	for _, item := range privateData {
		entry, _ := item.(map[string]any)
		privateIDs = append(privateIDs, entry["id"].(string))
	}
	if !containsString(privateIDs, "private-model") || containsString(privateIDs, "public-model") {
		t.Fatalf("expected private auth to expose only private-authorized default providers, got %v", privateIDs)
	}
}

func TestModelsExplicitProviderRouteStillUsesSingleProviderFlow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"upstream-openai","object":"model","owned_by":"upstream"}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai,azure",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{
			{ID: "openai", Enabled: true, SupportsModels: true, UpstreamBaseURL: upstream.URL, UpstreamAPIKey: "test-key"},
			{ID: "azure", Enabled: true, SupportsModels: true, UpstreamBaseURL: "https://azure.test", UpstreamAPIKey: "test-key"},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/openai/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected explicit provider /v1/models to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode explicit provider /v1/models response: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected explicit provider flow to return upstream response, got %#v", data)
	}
	entry, _ := data[0].(map[string]any)
	if got, _ := entry["id"].(string); got != "upstream-openai" {
		t.Fatalf("expected explicit provider flow to preserve upstream model id, got %#v", entry)
	}
}

func TestModelsOverlayTaggedModeAddsTaggedAliases(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:                "openai,azure",
		EnableLegacyV1Routes:           true,
		EnableDefaultProviderModelTags: true,
		Providers: []config.ProviderConfig{
			{
				ID:              "openai",
				Enabled:         true,
				SupportsModels:  true,
				UpstreamBaseURL: "https://openai.test",
				ModelMap: []config.ModelMapEntry{
					config.NewModelMapEntry("shared-model", "gpt-shared-openai"),
					config.NewModelMapEntry("openai-only", "gpt-openai"),
				},
			},
			{
				ID:              "azure",
				Enabled:         true,
				SupportsModels:  true,
				UpstreamBaseURL: "https://azure.test",
				ModelMap: []config.ModelMapEntry{
					config.NewModelMapEntry("shared-model", "gpt-shared-azure"),
					config.NewModelMapEntry("azure-only", "gpt-azure"),
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected tagged /v1/models to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode tagged /v1/models response: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	owners := map[string]string{}
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		ids = append(ids, id)
		owner, _ := entry["owned_by"].(string)
		owners[id] = owner
	}
	for _, want := range []string{"openai-only", "azure-only", "[openai]shared-model", "[azure]shared-model"} {
		if !containsString(ids, want) {
			t.Fatalf("expected tagged models response to contain %q, got %v", want, ids)
		}
	}
	if containsString(ids, "shared-model") {
		t.Fatalf("expected overlapping model to be hidden behind tags in conflict-only mode, got %v", ids)
	}
	if containsString(ids, "[openai]openai-only") || containsString(ids, "[azure]azure-only") {
		t.Fatalf("expected unique models to stay untagged when all-tag mode is off, got %v", ids)
	}
	if owners["[openai]shared-model"] != "openai" || owners["[azure]shared-model"] != "azure" {
		t.Fatalf("expected tagged aliases to preserve provider ownership, got %#v", owners)
	}
	if owners["openai-only"] != "openai" || owners["azure-only"] != "azure" {
		t.Fatalf("expected unique untagged models to preserve provider ownership, got %#v", owners)
	}
}

func TestModelsOverlayAllTagModeTagsEveryModel(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:                   "openai,azure",
		EnableLegacyV1Routes:              true,
		EnableDefaultProviderModelTags:    true,
		EnableAllDefaultProviderModelTags: true,
		Providers: []config.ProviderConfig{
			{
				ID:              "openai",
				Enabled:         true,
				SupportsModels:  true,
				UpstreamBaseURL: "https://openai.test",
				ModelMap: []config.ModelMapEntry{
					config.NewModelMapEntry("shared-model", "gpt-shared-openai"),
					config.NewModelMapEntry("openai-only", "gpt-openai"),
				},
			},
			{
				ID:              "azure",
				Enabled:         true,
				SupportsModels:  true,
				UpstreamBaseURL: "https://azure.test",
				ModelMap: []config.ModelMapEntry{
					config.NewModelMapEntry("shared-model", "gpt-shared-azure"),
					config.NewModelMapEntry("azure-only", "gpt-azure"),
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected all-tag /v1/models to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode all-tag /v1/models response: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		ids = append(ids, id)
	}
	for _, want := range []string{"[openai]shared-model", "[azure]shared-model", "[openai]openai-only", "[azure]azure-only"} {
		if !containsString(ids, want) {
			t.Fatalf("expected all-tag response to contain %q, got %v", want, ids)
		}
	}
	for _, hidden := range []string{"shared-model", "openai-only", "azure-only"} {
		if containsString(ids, hidden) {
			t.Fatalf("expected all-tag response to hide untagged model %q, got %v", hidden, ids)
		}
	}
}

func TestModelsOverlayAllTagModeTagsEveryModelForSingleDefaultProvider(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"upstream-openai","object":"model","owned_by":"upstream"}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:                   "openai",
		EnableLegacyV1Routes:              true,
		EnableDefaultProviderModelTags:    true,
		EnableAllDefaultProviderModelTags: true,
		Providers: []config.ProviderConfig{
			{
				ID:              "openai",
				Enabled:         true,
				SupportsModels:  true,
				UpstreamBaseURL: upstream.URL,
				ModelMap: []config.ModelMapEntry{
					config.NewModelMapEntry("openai-only", "gpt-openai"),
				},
				ManualModels:   []string{"manual-only"},
				UpstreamAPIKey: "test-key",
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected single-provider all-tag /v1/models to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode single-provider all-tag /v1/models response: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		ids = append(ids, id)
	}
	for _, want := range []string{"[openai]openai-only", "[openai]manual-only"} {
		if !containsString(ids, want) {
			t.Fatalf("expected single-provider all-tag response to contain %q, got %v", want, ids)
		}
	}
	for _, hidden := range []string{"openai-only", "manual-only", "upstream-openai"} {
		if containsString(ids, hidden) {
			t.Fatalf("expected single-provider all-tag response to hide untagged model %q, got %v", hidden, ids)
		}
	}
}

func containsString(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
