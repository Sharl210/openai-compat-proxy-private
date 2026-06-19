package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/reasoning"
)

func TestRewriteModelsBodyPreservesUpstreamFieldsWithoutExposingModelMapAliases(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"gpt-5.4","object":"model","owned_by":"openai"}]}`)
	provider := config.ProviderConfig{
		ModelMap: []config.ModelMapEntry{
			config.NewModelMapEntry("public-gpt", "gpt-5.4"),
			config.NewModelMapEntry("#re:.*", "gpt-5.4"),
		},
	}

	rewritten := rewriteModelsBody(body, provider)
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("decode rewritten models body: %v", err)
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected only upstream id, got %#v", data)
	}
	entries := map[string]map[string]any{}
	for _, item := range data {
		entry, _ := item.(map[string]any)
		entries[entry["id"].(string)] = entry
	}
	if _, ok := entries[".*"]; ok {
		t.Fatalf("expected regex alias to stay hidden, got %#v", entries)
	}
	if got := entries["gpt-5.4"]["owned_by"]; got != "openai" {
		t.Fatalf("expected upstream entry fields to be preserved, got %#v", entries["gpt-5.4"])
	}
	if _, ok := entries["public-gpt"]; ok {
		t.Fatalf("expected MODEL_MAP alias to stay hidden from /models, got %#v", entries)
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

func TestRewriteModelsBodyAppliesModelIDTemplateByDefault(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model","owned_by":"openai"}]}`)
	provider := config.ProviderConfig{ModelIDTemplate: "packy-{{model}}"}

	rewritten := rewriteModelsBodyForRoute(body, provider, false)
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("decode rewritten models body: %v", err)
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected one model entry, got %#v", data)
	}
	entry, _ := data[0].(map[string]any)
	if got := entry["id"]; got != "packy-gpt-5.5" {
		t.Fatalf("expected provider route to expose templated id by default, got %#v", got)
	}
}

func TestRewriteModelsBodyRootOnlyTemplateStillWrapsProviderRoute(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model","owned_by":"openai"}]}`)
	provider := config.ProviderConfig{ModelIDTemplate: "packy-{{model}}", ModelIDTemplateRootOnly: true}

	providerBody := rewriteModelsBodyForRoute(body, provider, false)
	var providerPayload map[string]any
	if err := json.Unmarshal(providerBody, &providerPayload); err != nil {
		t.Fatalf("decode provider models body: %v", err)
	}
	providerData, _ := providerPayload["data"].([]any)
	providerEntry, _ := providerData[0].(map[string]any)
	if got := providerEntry["id"]; got != "packy-gpt-5.5" {
		t.Fatalf("expected provider route to expose templated id despite root-only compatibility flag, got %#v", got)
	}

	rootBody := rewriteModelsBodyForRoute(body, provider, true)
	var rootPayload map[string]any
	if err := json.Unmarshal(rootBody, &rootPayload); err != nil {
		t.Fatalf("decode root models body: %v", err)
	}
	rootData, _ := rootPayload["data"].([]any)
	rootEntry, _ := rootData[0].(map[string]any)
	if got := rootEntry["id"]; got != "packy-gpt-5.5" {
		t.Fatalf("expected root route to expose templated id when root-only=true, got %#v", got)
	}
}

func TestModelsExplicitProviderRouteExposesOnlyTemplatedIDs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model","owned_by":"upstream"}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider: "packy",
		Providers: []config.ProviderConfig{{
			ID:                      "packy",
			Enabled:                 true,
			UpstreamBaseURL:         upstream.URL,
			UpstreamAPIKey:          "test-key",
			SupportsModels:          true,
			ModelIDTemplate:         "packy-{{model}}-vip",
			ModelIDTemplateRootOnly: true,
		}},
	})

	req := httptest.NewRequest(http.MethodGet, "/packy/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected explicit provider models status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	ids := decodeModelIDsFromBody(t, rec.Body.Bytes())
	if !contains(ids, "packy-gpt-5.5-vip") || contains(ids, "gpt-5.5") {
		t.Fatalf("expected only templated external model ids, got %#v", ids)
	}
}

func TestModelsBareDefaultProviderExposesOnlyTemplatedIDs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model","owned_by":"upstream"}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "packy",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                      "packy",
			Enabled:                 true,
			UpstreamBaseURL:         upstream.URL,
			UpstreamAPIKey:          "test-key",
			SupportsModels:          true,
			ModelIDTemplate:         "packy-{{model}}-vip",
			ModelIDTemplateRootOnly: true,
		}},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected bare default models status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	ids := decodeModelIDsFromBody(t, rec.Body.Bytes())
	if !contains(ids, "packy-gpt-5.5-vip") || contains(ids, "gpt-5.5") {
		t.Fatalf("expected only templated external model ids, got %#v", ids)
	}
}

func TestExpandModelIDsKeepsExplicitAliases(t *testing.T) {
	expanded := reasoning.ExpandModelIDs([]string{"public-gpt", "gpt-5.4", "gpt-5.4-high"}, []string{"public-gpt"}, true)
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
	if got["gpt-5.4-high-low"] {
		t.Fatalf("expected already suffixed ids to stop expanding, got %#v", expanded)
	}
}

func TestRewriteModelsBodyDoesNotExposeReasoningSuffixModelsWhenDisabled(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"visible-reasoning","object":"model"}]}`)
	provider := config.ProviderConfig{
		ManualModels:                []string{"visible-reasoning"},
		EnableReasoningEffortSuffix: true,
		ExposeReasoningSuffixModels: false,
	}

	rewritten := rewriteModelsBody(body, provider)
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("decode rewritten models body: %v", err)
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		ids = append(ids, entry["id"].(string))
	}
	if !contains(ids, "visible-reasoning") {
		t.Fatalf("expected base model to stay visible, got %#v", ids)
	}
	for _, hidden := range []string{"visible-reasoning-low", "visible-reasoning-medium", "visible-reasoning-high", "visible-reasoning-xhigh"} {
		if contains(ids, hidden) {
			t.Fatalf("expected suffix model %q to stay hidden from /models when exposure is disabled, got %#v", hidden, ids)
		}
	}
}

func TestRewriteModelsBodyExpandsManualReasonSuffixFamilyIndependentOfExposeFlag(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model","owned_by":"openai"}]}`)
	provider := config.ProviderConfig{
		ManualModels:                []string{"#reason_suffix:gpt-5.5"},
		EnableReasoningEffortSuffix: false,
		ExposeReasoningSuffixModels: false,
	}

	rewritten := rewriteModelsBody(body, provider)
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("decode rewritten models body: %v", err)
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		ids = append(ids, entry["id"].(string))
	}
	for _, want := range []string{"gpt-5.5", "gpt-5.5-none", "gpt-5.5-minimal", "gpt-5.5-low", "gpt-5.5-medium", "gpt-5.5-high", "gpt-5.5-xhigh"} {
		if !contains(ids, want) {
			t.Fatalf("expected manual reason suffix family model %q in rewritten models, got %#v", want, ids)
		}
	}
	for _, item := range data {
		entry, _ := item.(map[string]any)
		if entry["id"] == "#reason_suffix:gpt-5.5" {
			t.Fatalf("expected marker to stay hidden from /models, got %#v", ids)
		}
	}
}

func TestRewriteModelsBodyExpandsManualReasonSuffixRegexFamily(t *testing.T) {
	manual := "#reason_suffix:#re:gpt-5\\..*"
	t.Run(manual, func(t *testing.T) {
		body := []byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model"},{"id":"gpt-4.1","object":"model"}]}`)
		provider := config.ProviderConfig{ManualModels: []string{manual}}

		rewritten := rewriteModelsBody(body, provider)
		var payload map[string]any
		if err := json.Unmarshal(rewritten, &payload); err != nil {
			t.Fatalf("decode rewritten models body: %v", err)
		}
		data, _ := payload["data"].([]any)
		ids := make([]string, 0, len(data))
		for _, item := range data {
			entry, _ := item.(map[string]any)
			ids = append(ids, entry["id"].(string))
		}
		for _, want := range []string{"gpt-5.5", "gpt-5.5-none", "gpt-5.5-minimal", "gpt-5.5-low", "gpt-5.5-medium", "gpt-5.5-high", "gpt-5.5-xhigh"} {
			if !contains(ids, want) {
				t.Fatalf("expected %q to expose %q, got %#v", manual, want, ids)
			}
		}
		if contains(ids, "gpt-4.1") {
			t.Fatalf("expected %q to filter unmatched upstream base gpt-4.1, got %#v", manual, ids)
		}
		if contains(ids, "gpt-4.1-low") {
			t.Fatalf("expected %q not to expand unmatched gpt-4.1 family, got %#v", manual, ids)
		}
	})
}

func TestRewriteModelsBodyReasonSuffixRegexUsesOnlyUpstreamModels(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"upstream-a","object":"model"}]}`)
	provider := config.ProviderConfig{
		ModelMap:       []config.ModelMapEntry{config.NewModelMapEntry("proxy-alias", "upstream-a")},
		ManualModels:   []string{"#reason_suffix:#re:proxy-.*", "manual-static"},
		HiddenModels:   nil,
		SupportsModels: true,
	}

	rewritten := rewriteModelsBody(body, provider)
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("decode rewritten models body: %v", err)
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		ids = append(ids, entry["id"].(string))
	}
	if contains(ids, "proxy-alias-low") || contains(ids, "manual-static-low") {
		t.Fatalf("expected reason suffix regex to match only upstream /models data, got %#v", ids)
	}
}

func TestRewriteModelsBodyExpandsManualReasonSuffixSelector(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model"},{"id":"gpt-4.1","object":"model"}]}`)
	provider := config.ProviderConfig{ManualModels: []string{"#reason_suffix:-minimal"}}

	rewritten := rewriteModelsBody(body, provider)
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("decode rewritten models body: %v", err)
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		ids = append(ids, entry["id"].(string))
	}
	for _, want := range []string{"gpt-5.5-minimal", "gpt-4.1-minimal"} {
		if !contains(ids, want) {
			t.Fatalf("expected suffix selector to expose %q, got %#v", want, ids)
		}
	}
	if contains(ids, "gpt-5.5-low") {
		t.Fatalf("expected suffix selector not to expose unrelated effort, got %#v", ids)
	}
}

func TestRewriteModelsBodyKeepsManualNoPromptModelLiteral(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model"}]}`)
	provider := config.ProviderConfig{ManualModels: []string{"gpt-5.5-noprompt"}}

	rewritten := rewriteModelsBody(body, provider)
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("decode rewritten models body: %v", err)
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		ids = append(ids, entry["id"].(string))
	}
	if !contains(ids, "gpt-5.5-noprompt") {
		t.Fatalf("expected manually added noprompt model to be visible literally, got %#v", ids)
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

func TestModelsConfiguredManualSupportWithoutUsableUpstream(t *testing.T) {
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
				config.NewModelMapEntry("public-gpt", "gpt-5.4"),
				config.NewModelMapEntry("#re:.*", "gpt-5.4"),
			},
			ManualModels: []string{"public-gpt"},
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected configured manual fallback to return 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode fallback models response: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected exactly one configured public manual model, got %#v", data)
	}
	entry, _ := data[0].(map[string]any)
	if got, _ := entry["id"].(string); got != "public-gpt" {
		t.Fatalf("expected fallback manual model public-gpt, got %#v", entry)
	}
	if _, exists := entry["owned_by"]; exists {
		t.Fatalf("expected synthetic fallback entry without upstream-only fields, got %#v", entry)
	}
}

func TestModelsFallsBackOnGenericNotFoundWhenManualModelsExist(t *testing.T) {
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
			ManualModels: []string{"public-gpt"},
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected generic upstream 404 to fallback to manual models, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode fallback models response: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("expected exactly one configured public manual model, got %#v", data)
	}
	entry, _ := data[0].(map[string]any)
	if got, _ := entry["id"].(string); got != "public-gpt" {
		t.Fatalf("expected fallback manual model public-gpt, got %#v", entry)
	}
}

func TestModelsFallbackDoesNotExposeModelMapOnlyAliases(t *testing.T) {
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
			ModelMap: []config.ModelMapEntry{
				{Key: "public-gpt", Target: "gpt-5.4"},
			},
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected model-map-only fallback to keep upstream 404, got %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "public-gpt") {
		t.Fatalf("expected MODEL_MAP key not to be exposed in fallback models, got %s", rec.Body.String())
	}
}

func TestModelsRewriteDoesNotExposeModelMapOnlyAliasesFromUpstreamModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model","owned_by":"upstream"}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			SupportsModels:              true,
			EnableReasoningEffortSuffix: true,
			ExposeReasoningSuffixModels: true,
			ModelMap: []config.ModelMapEntry{
				{Key: "client-gpt", Target: "gpt-5.5"},
			},
		}},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upstream models response 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode models response: %v body=%s", err, rec.Body.String())
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		ids = append(ids, id)
	}
	for _, hidden := range []string{"client-gpt", "client-gpt-high"} {
		if contains(ids, hidden) {
			t.Fatalf("expected MODEL_MAP-only alias %q not to be exposed, got %v", hidden, ids)
		}
	}
	if !contains(ids, "gpt-5.5") || !contains(ids, "gpt-5.5-high") {
		t.Fatalf("expected upstream base model and its suffix variants to remain visible, got %v", ids)
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

func TestRewriteModelsBodyHidesModelsConfiguredInHiddenModels(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"gpt-4o","object":"model","owned_by":"openai"},{"id":"gpt-5","object":"model","owned_by":"openai"}]}`)
	provider := config.ProviderConfig{
		ModelMap: []config.ModelMapEntry{
			{Key: "public-gpt", Target: "gpt-5"},
		},
		ManualModels: []string{"manual-alpha", "manual-beta"},
		HiddenModels: []string{"#re:gpt-4.*", "manual-alpha", "#re:public-.*"},
	}

	rewritten := rewriteModelsBody(body, provider)
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("decode rewritten models body: %v", err)
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		ids = append(ids, entry["id"].(string))
	}
	if contains(ids, "gpt-4o") || contains(ids, "public-gpt") {
		t.Fatalf("expected hidden models to be removed from rewritten models body, got %#v", ids)
	}
	if !contains(ids, "gpt-5") || !contains(ids, "manual-alpha") || !contains(ids, "manual-beta") {
		t.Fatalf("expected non-hidden models to remain visible, got %#v", ids)
	}
}

func TestRewriteModelsBodyExpandsRegexManualModelsFromUpstreamList(t *testing.T) {
	body := []byte(`{"object":"list","data":[{"id":"1","object":"model"},{"id":"2","object":"model"},{"id":"2.4","object":"model"},{"id":"5","object":"model"}]}`)
	provider := config.ProviderConfig{ManualModels: []string{"#re:2.*"}}

	rewritten := rewriteModelsBody(body, provider)
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("decode rewritten models body: %v", err)
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		ids = append(ids, entry["id"].(string))
	}
	if contains(ids, "1") || contains(ids, "5") || !contains(ids, "2") || !contains(ids, "2.4") {
		t.Fatalf("expected regex manual pattern to expose only matching upstream models 2 and 2.4, got %#v", ids)
	}
}

func TestConfiguredModelsFallbackSkipsRegexManualPatterns(t *testing.T) {
	provider := config.ProviderConfig{ManualModels: []string{"#re:2.*", "literal-model"}}

	body, ok := configuredModelsFallbackBody(provider)
	if !ok {
		t.Fatalf("expected literal manual model to provide fallback body")
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode fallback body: %v", err)
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		ids = append(ids, entry["id"].(string))
	}
	if contains(ids, "2.*") || !contains(ids, "literal-model") {
		t.Fatalf("expected regex manual pattern hidden from fallback while literal remains, got %#v", ids)
	}
}

func TestRewriteModelsBodyKeepsManualModelsEvenWhenHiddenRegexMatches(t *testing.T) {
	body := []byte(`{"object":"list","data":[]}`)
	provider := config.ProviderConfig{
		ManualModels: []string{"manual-alpha"},
		HiddenModels: []string{"#re:.*"},
	}

	rewritten := rewriteModelsBody(body, provider)
	var payload map[string]any
	if err := json.Unmarshal(rewritten, &payload); err != nil {
		t.Fatalf("decode rewritten models body: %v", err)
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		ids = append(ids, entry["id"].(string))
	}
	if len(ids) != 1 || ids[0] != "manual-alpha" {
		t.Fatalf("expected manual model to remain visible, got %#v", ids)
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

func decodeModelIDsFromBody(t *testing.T, body []byte) []string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode models body: %v body=%s", err, string(body))
	}
	data, _ := payload["data"].([]any)
	ids := make([]string, 0, len(data))
	for _, item := range data {
		entry, _ := item.(map[string]any)
		id, _ := entry["id"].(string)
		ids = append(ids, id)
	}
	return ids
}
