package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

func TestProviderSelectionForModelRequest_resolvesEachRealtimeProxyAxisAndOrder(t *testing.T) {
	// Given
	alpha := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{"other-model"})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ManualModels = []string{"realtime-base"}
	store := config.NewStaticRuntimeStore(cfg)
	tests := []struct {
		name     string
		model    string
		resolved string
		effort   string
		mode     string
		noprompt bool
		ultra    bool
	}{
		{name: "effort", model: "realtime-base-high", resolved: "realtime-base-high", effort: "high"},
		{name: "pro", model: "realtime-base-pro", resolved: "realtime-base", mode: "pro"},
		{name: "noprompt", model: "realtime-base-noprompt", resolved: "realtime-base", noprompt: true},
		{name: "ultra", model: "realtime-base-ultra", resolved: "realtime-base", ultra: true},
		{name: "canonical_order", model: "realtime-base-high-pro-ultra-noprompt", resolved: "realtime-base-high", effort: "high", mode: "pro", noprompt: true, ultra: true},
		{name: "mixed_order", model: "realtime-base-noprompt-ultra-pro-high", resolved: "realtime-base-high", effort: "high", mode: "pro", noprompt: true, ultra: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			req := overlayProviderSelectionRequest(store)
			_, _, providerID, resolvedModel, ok, err := providerSelectionForModelRequest(req, tt.model)

			// Then
			if err != nil || !ok || providerID != "alpha" || resolvedModel != tt.resolved {
				t.Fatalf("unexpected selection provider=%q model=%q ok=%t err=%v", providerID, resolvedModel, ok, err)
			}
			intent, found := proxyModelIntentFromRequest(req)
			if tt.mode != "" || tt.noprompt || tt.ultra {
				intent = mustParseConfiguredProxyModelIntent(t, "realtime-base", tt.model)
				found = true
			}
			if !found || intent.ReasoningEffort != tt.effort || intent.ReasoningMode != tt.mode || intent.HasNoPrompt != tt.noprompt || intent.HasUltra != tt.ultra {
				t.Fatalf("unexpected proxy intent: %#v found=%t", intent, found)
			}
		})
	}
}

func mustParseConfiguredProxyModelIntent(t *testing.T, baseModel string, modelName string) model.ProxyModelIntent {
	t.Helper()
	intent, ok := model.ParseProxyModelIntent(modelName, []string{baseModel}, model.ProxyModelIntentAxes{
		EnableReasoningEffort: true,
		EnablePro:             true,
		EnableNoPrompt:        true,
		EnableUltra:           true,
	})
	if !ok {
		t.Fatalf("expected configured proxy intent for %q", modelName)
	}
	return intent
}

func TestConfiguredProviderSelectionKeepsManualSuffixCandidates(t *testing.T) {
	alpha := newOverlaySuffixUpstream(t, []string{"upstream-only"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{"other-model"})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ManualModels = []string{"manual-base"}
	store := config.NewStaticRuntimeStore(cfg)
	providerID, resolvedModel, intent, ok := configuredDefaultProviderSelection(store.Active(), "manual-base-high-noprompt", "")

	if !ok || providerID != "alpha" || resolvedModel != "manual-base-high" {
		t.Fatalf("expected configured manual suffix to select alpha, got provider=%q model=%q ok=%t", providerID, resolvedModel, ok)
	}
	if intent.BaseModel != "manual-base" || intent.ReasoningEffort != "high" || !intent.HasNoPrompt {
		t.Fatalf("expected manual base high+noprompt intent, got %#v", intent)
	}
}

func TestDefaultOverlaySuffixRouting_resolvesTemplatedRealtimeBase(t *testing.T) {
	// Given
	alpha := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{"other-model"})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ManualModels = []string{"realtime-base"}
	cfg.Providers[0].ModelIDTemplate = "packy-{{model}}-vip"
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"packy-realtime-base-high-pro-ultra-noprompt-vip","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected templated suffix request to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		t.Fatalf("expected alpha owner, got %q", got)
	}
	captured := <-alpha.requests
	if got := captured.body["model"]; got != "realtime-base" {
		t.Fatalf("expected internal base upstream model, got %#v", got)
	}
}

func TestDefaultOverlaySuffixRouting_resolvesTaggedRealtimeBase(t *testing.T) {
	// Given
	alpha := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ManualModels = []string{"realtime-base"}
	cfg.Providers[1].ManualModels = []string{"realtime-base"}
	cfg.EnableDefaultProviderModelTags = true
	cfg.EnableAllDefaultProviderModelTags = true
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"[alpha]realtime-base-high-pro-ultra-noprompt","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected tagged suffix request to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		t.Fatalf("expected tagged alpha owner, got %q", got)
	}
	captured := <-alpha.requests
	if got := captured.body["model"]; got != "realtime-base" {
		t.Fatalf("expected untagged upstream base model, got %#v", got)
	}
}

func TestDefaultOverlaySuffixRouting_resolvesRealtimeCombinationAcrossEntrypoints(t *testing.T) {
	// Given
	alpha := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{"other-model"})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ManualModels = []string{"realtime-base"}
	server := NewServer(cfg)
	requests := []struct {
		name    string
		path    string
		body    string
		headers map[string]string
	}{
		{name: "responses", path: "/v1/responses", body: `{"model":"realtime-base-high-pro-ultra-noprompt","input":"hello"}`},
		{name: "chat", path: "/v1/chat/completions", body: `{"model":"realtime-base-high-pro-ultra-noprompt","messages":[{"role":"user","content":"hello"}]}`},
		{name: "messages", path: "/v1/messages", body: `{"model":"realtime-base-high-pro-ultra-noprompt","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`, headers: map[string]string{"anthropic-version": "2023-06-01"}},
	}

	for _, tt := range requests {
		t.Run(tt.name, func(t *testing.T) {
			// When
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			for key, value := range tt.headers {
				req.Header.Set(key, value)
			}
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("X-Provider-Name") != "alpha" || rec.Header().Get(headerClientToProxyReasoningEffort) != "high" || rec.Header().Get(headerClientToProxyReasoningMode) != "pro" || rec.Header().Get(headerClientToProxyNoPrompt) != "true" {
				t.Fatalf("unexpected routing headers: %#v", rec.Header())
			}
			captured := <-alpha.requests
			if got := captured.body["model"]; got != "realtime-base" {
				t.Fatalf("expected upstream base model, got %#v", got)
			}
		})
	}
}

func TestExplicitProviderSuffixRouting_resolvesRealtimeCombination(t *testing.T) {
	// Given
	alpha := newOverlaySuffixUpstream(t, []string{"realtime-base"})
	defer alpha.Close()
	beta := newOverlaySuffixUpstream(t, []string{"other-model"})
	defer beta.Close()
	cfg := defaultOverlaySuffixConfig(alpha.URL, beta.URL)
	cfg.Providers[0].ManualModels = []string{"realtime-base"}
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/alpha/v1/responses", strings.NewReader(`{"model":"realtime-base-high-pro-ultra-noprompt","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected explicit provider suffix request to succeed, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		t.Fatalf("expected explicit alpha owner, got %q", got)
	}
	captured := <-alpha.requests
	if got := captured.body["model"]; got != "realtime-base" {
		t.Fatalf("expected upstream base model, got %#v", got)
	}
}
