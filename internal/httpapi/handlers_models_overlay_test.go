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
