package httpapi

import (
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
)

func TestRefreshDefaultProviderOverlayCacheFromRequest_usesActiveSnapshotWhenRefreshFails(t *testing.T) {
	// Given
	store := config.NewStaticRuntimeStore(config.Config{DefaultProvider: "packy", Providers: []config.ProviderConfig{{ID: "packy", Enabled: true}}})
	active := store.Active()
	stale := &config.RuntimeSnapshot{}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req = req.Clone(withRuntimeStore(withRuntimeSnapshot(req.Context(), stale), store))

	// When
	refreshDefaultProviderOverlayCacheFromRequestWithRefresh(req, func(*config.RuntimeStore, time.Time) error {
		return errors.New("refresh failed")
	})

	// Then
	resolved, ok := runtimeSnapshotFromRequest(req)
	if !ok || resolved != active {
		t.Fatalf("expected active snapshot after failed refresh, got %#v", resolved)
	}
}

func TestResolveV1ProxyModelIntentForLegacyRequest_refreshesOverlayBeforeParsingTarget(t *testing.T) {
	// Given
	store := config.NewStaticRuntimeStore(config.Config{
		DefaultProvider:      "alpha,beta",
		EnableLegacyV1Routes: true,
		V1ModelMap:           []config.ModelMapEntry{config.NewModelMapEntry("client", "vendor-pro")},
		Providers: []config.ProviderConfig{
			{ID: "alpha", Enabled: true},
			{ID: "beta", Enabled: true},
		},
	})
	store.Active().Config.Providers[0].ManualModels = []string{"vendor-pro"}
	req := httptest.NewRequest("POST", "/v1/responses", nil)
	req = req.Clone(withRuntimeStore(withRuntimeSnapshot(req.Context(), store.Active()), store))

	// When
	intent, ok := resolveV1ProxyModelIntentForLegacyRequest(req, "client")

	// Then
	if !ok {
		t.Fatal("expected root MODEL_MAP to resolve")
	}
	if intent.BaseModel != "vendor-pro" || intent.ReasoningMode != "" {
		t.Fatalf("expected refreshed literal target to win over -pro parsing, got %#v", intent)
	}
}
