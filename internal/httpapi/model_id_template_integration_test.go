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

func TestExplicitProviderResponsesRejectsRawModelIDWhenTemplateConfigured(t *testing.T) {
	var upstreamModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			if r.URL.Path == "/models" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model"}]}`))
				return
			}
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

	rawReq := httptest.NewRequest(http.MethodPost, "/packy/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello"}`))
	rawReq.Header.Set("Content-Type", "application/json")
	rawReq.Header.Set("Authorization", "Bearer test-key")
	rawRec := httptest.NewRecorder()

	server.ServeHTTP(rawRec, rawReq)

	if rawRec.Code != http.StatusBadRequest || !strings.Contains(rawRec.Body.String(), "invalid_model") {
		t.Fatalf("expected raw provider id to be rejected with invalid_model, got %d body=%s", rawRec.Code, rawRec.Body.String())
	}
	if upstreamModel != "" {
		t.Fatalf("expected rejected raw provider id not to reach upstream, got upstream model %q", upstreamModel)
	}

	templatedReq := httptest.NewRequest(http.MethodPost, "/packy/v1/responses", strings.NewReader(`{"model":"packy-gpt-5.5","input":"hello"}`))
	templatedReq.Header.Set("Content-Type", "application/json")
	templatedReq.Header.Set("Authorization", "Bearer test-key")
	templatedRec := httptest.NewRecorder()

	server.ServeHTTP(templatedRec, templatedReq)

	if templatedRec.Code != http.StatusOK {
		t.Fatalf("expected templated provider id status 200, got %d body=%s", templatedRec.Code, templatedRec.Body.String())
	}
	if upstreamModel != "real-gpt-5.5" {
		t.Fatalf("expected templated provider id to unwrap then map to real-gpt-5.5, got %q", upstreamModel)
	}
}

func TestBareResponsesRejectsRawModelIDWhenTemplateConfigured(t *testing.T) {
	var upstreamModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model"}]}`))
			return
		case "/responses":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("decode upstream request: %v body=%s", err, string(body))
			}
			upstreamModel, _ = payload["model"].(string)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
			return
		default:
			http.NotFound(w, r)
			return
		}
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
			SupportsModels:    true,
			SupportsResponses: true,
			ManualModels:      []string{"gpt-5.5"},
			ModelIDTemplate:   "packy-{{model}}-vip",
			ModelMap:          []config.ModelMapEntry{config.NewModelMapEntry("gpt-5.5", "real-gpt-5.5")},
		}},
	})

	rawReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello"}`))
	rawReq.Header.Set("Content-Type", "application/json")
	rawReq.Header.Set("Authorization", "Bearer test-key")
	rawRec := httptest.NewRecorder()

	server.ServeHTTP(rawRec, rawReq)

	if rawRec.Code != http.StatusBadRequest || !strings.Contains(rawRec.Body.String(), "invalid_model") {
		t.Fatalf("expected raw bare id to be rejected with invalid_model, got %d body=%s", rawRec.Code, rawRec.Body.String())
	}
	if upstreamModel != "" {
		t.Fatalf("expected rejected raw bare id not to reach upstream, got upstream model %q", upstreamModel)
	}

	templatedReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"packy-gpt-5.5-vip","input":"hello"}`))
	templatedReq.Header.Set("Content-Type", "application/json")
	templatedReq.Header.Set("Authorization", "Bearer test-key")
	templatedRec := httptest.NewRecorder()

	server.ServeHTTP(templatedRec, templatedReq)

	if templatedRec.Code != http.StatusOK {
		t.Fatalf("expected templated bare id status 200, got %d body=%s", templatedRec.Code, templatedRec.Body.String())
	}
	if upstreamModel != "real-gpt-5.5" {
		t.Fatalf("expected templated bare id to unwrap then map to real-gpt-5.5, got %q", upstreamModel)
	}
}

func TestResponsesTemplateUnwrapsBeforeRawHiddenModelRules(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model"}]}`))
		case "/responses":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "packy",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                          "packy",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			SupportsModels:              true,
			SupportsResponses:           true,
			ManualModels:                []string{"gpt-5.5"},
			HiddenModels:                []string{"#reason_suffix:gpt-5.5"},
			ModelIDTemplate:             "packy-{{model}}-vip",
			EnableReasoningEffortSuffix: true,
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"packy-gpt-5.5-low-vip","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_model") {
		t.Fatalf("expected templated id for hidden raw model to be rejected with invalid_model, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestResponsesTemplateWithTrailingLiteralUnwrapsBeforeReasoningAndNoPrompt(t *testing.T) {
	var upstreamModel string
	var upstreamReasoning map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.5","object":"model"}]}`))
			return
		case "/responses":
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("decode upstream request: %v body=%s", err, string(body))
			}
			upstreamModel, _ = payload["model"].(string)
			upstreamReasoning, _ = payload["reasoning"].(map[string]any)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_123","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
			return
		default:
			http.NotFound(w, r)
			return
		}
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:           "packy",
		EnableLegacyV1Routes:      true,
		EnableNoPromptModelSuffix: true,
		Providers: []config.ProviderConfig{{
			ID:                          "packy",
			Enabled:                     true,
			UpstreamBaseURL:             upstream.URL,
			UpstreamAPIKey:              "test-key",
			SupportsModels:              true,
			SupportsResponses:           true,
			ManualModels:                []string{"gpt-5.5"},
			ModelIDTemplate:             "packy-{{model}}-vip",
			ModelMap:                    []config.ModelMapEntry{config.NewModelMapEntry("gpt-5.5", "real-gpt-5.5")},
			EnableReasoningEffortSuffix: true,
			EnableNoPromptModelSuffix:   true,
			SystemPromptText:            "provider prompt must be skipped",
		}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"packy-gpt-5.5-low-noprompt-vip","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected templated suffix+noprompt model status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if upstreamModel != "real-gpt-5.5" {
		t.Fatalf("expected suffix+noprompt templated id to unwrap and MODEL_MAP raw base to real-gpt-5.5, got %q", upstreamModel)
	}
	if effort, _ := upstreamReasoning["effort"].(string); effort != "low" {
		t.Fatalf("expected reasoning effort low after template unwrap, got %#v", upstreamReasoning)
	}
	if got := rec.Header().Get(headerClientToProxyNoPrompt); got != "true" {
		t.Fatalf("expected %s true after template unwrap, got %q", headerClientToProxyNoPrompt, got)
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
