package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestResponsesRequestUnpacksExternalModelIDBeforeProviderModelMap(t *testing.T) {
	var upstreamModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode upstream request: %v body=%s", err, string(body))
		}
		upstreamModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "packy",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "packy",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
			SupportsModels:    true,
			ManualModels:      []string{"gpt-5.5"},
			ModelIDTemplate:   "packy-{{model}}",
			ModelMap:          []config.ModelMapEntry{config.NewModelMapEntry("gpt-5.5", "real-gpt-5.5")},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"packy-gpt-5.5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamModel != "real-gpt-5.5" {
		t.Fatalf("expected upstream model to be real-gpt-5.5 after template unpack and MODEL_MAP, got %q", upstreamModel)
	}
}

func TestExplicitProviderResponsesRootOnlyUsesRawModelID(t *testing.T) {
	var upstreamModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode upstream request: %v body=%s", err, string(body))
		}
		upstreamModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider: "packy",
		Providers: []config.ProviderConfig{{
			ID:                      "packy",
			Enabled:                 true,
			UpstreamBaseURL:         upstream.URL,
			UpstreamAPIKey:          "test-key",
			SupportsResponses:       true,
			SupportsModels:          true,
			ManualModels:            []string{"gpt-5.5"},
			ModelIDTemplate:         "packy-{{model}}",
			ModelIDTemplateRootOnly: true,
			ModelMap:                []config.ModelMapEntry{config.NewModelMapEntry("gpt-5.5", "real-gpt-5.5")},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/packy/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamModel != "real-gpt-5.5" {
		t.Fatalf("expected root-only explicit provider route to map raw model to real-gpt-5.5, got %q", upstreamModel)
	}
}

func TestEmbeddingsRequestUnpacksExternalModelIDBeforeProviderModelMap(t *testing.T) {
	var upstreamModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode upstream request: %v body=%s", err, string(body))
		}
		upstreamModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "packy",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "packy",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "test-key",
			SupportsModels:  true,
			ManualModels:    []string{"embed-1"},
			ModelIDTemplate: "packy-{{model}}",
			ModelMap:        []config.ModelMapEntry{config.NewModelMapEntry("embed-1", "real-embed-1")},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"model":"packy-embed-1","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamModel != "real-embed-1" {
		t.Fatalf("expected upstream embeddings model to be real-embed-1 after template unpack and MODEL_MAP, got %q", upstreamModel)
	}
}

func TestImageRequestUnpacksExternalModelIDBeforeProviderModelMap(t *testing.T) {
	var upstreamModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/generations" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode upstream request: %v body=%s", err, string(body))
		}
		upstreamModel, _ = payload["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`))
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "packy",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:              "packy",
			Enabled:         true,
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "test-key",
			SupportsModels:  true,
			ManualModels:    []string{"image-1"},
			ModelIDTemplate: "packy-{{model}}",
			ModelMap:        []config.ModelMapEntry{config.NewModelMapEntry("image-1", "real-image-1")},
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"packy-image-1","prompt":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamModel != "real-image-1" {
		t.Fatalf("expected upstream image model to be real-image-1 after template unpack and MODEL_MAP, got %q", upstreamModel)
	}
}
