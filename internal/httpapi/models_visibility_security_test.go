package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestSecurity_DefaultGroupRejectsWildcardBypassOutsideVisibleModels(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	server := NewServer(testLegacyModelRoutingConfig(alpha.URL, beta.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"owned-999","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected wildcard bypass outside visible models to be rejected, got %d body=%s", rec.Code, rec.Body.String())
	}
	if alpha.Hits() != 0 || beta.Hits() != 0 {
		t.Fatalf("expected no upstream hit for wildcard bypass attempt, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestSecurity_DefaultGroupRejectsTaggedBypassOutsideVisibleModels(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	cfg := testLegacyModelRoutingConfig(alpha.URL, beta.URL)
	cfg.EnableDefaultProviderModelTags = true
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"[alpha]owned-999","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected tagged bypass outside visible models to be rejected, got %d body=%s", rec.Code, rec.Body.String())
	}
	if alpha.Hits() != 0 || beta.Hits() != 0 {
		t.Fatalf("expected no upstream hit for tagged bypass attempt, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
}

func TestSecurity_ExplicitProviderHiddenUpstreamModelCannotBeRequested(t *testing.T) {
	var responsesHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"public-model","object":"model"},{"id":"admin-secret-model","object":"model"}]}`))
		case "/responses":
			responsesHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_openai","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			HiddenModels:         []string{"admin-*"},
		}},
	})

	modelsReq := httptest.NewRequest(http.MethodGet, "/openai/v1/models", nil)
	modelsReq.Header.Set("Authorization", "Bearer test-key")
	modelsRec := httptest.NewRecorder()
	server.ServeHTTP(modelsRec, modelsReq)
	if modelsRec.Code != http.StatusOK {
		t.Fatalf("expected explicit provider models request 200, got %d body=%s", modelsRec.Code, modelsRec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(modelsRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode models payload: %v body=%s", err, modelsRec.Body.String())
	}
	data, _ := payload["data"].([]any)
	for _, item := range data {
		entry, _ := item.(map[string]any)
		if got, _ := entry["id"].(string); got == "admin-secret-model" {
			t.Fatalf("expected hidden upstream model to be removed from /models list, got %s", modelsRec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"admin-secret-model","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected hidden upstream model request to be rejected, got %d body=%s", rec.Code, rec.Body.String())
	}
	if responsesHits.Load() != 0 {
		t.Fatalf("expected no upstream responses hit for hidden model attack, got %d", responsesHits.Load())
	}
}

func TestSecurity_BareSingleDefaultProviderRejectsHiddenUpstreamModelRequest(t *testing.T) {
	var responsesHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"public-model","object":"model"},{"id":"admin-secret-model","object":"model"}]}`))
		case "/responses":
			responsesHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_openai","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:             "openai",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			SupportsResponses:    true,
			SupportsModels:       true,
			HiddenModels:         []string{"admin-*"},
		}},
	})

	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelsRec := httptest.NewRecorder()
	server.ServeHTTP(modelsRec, modelsReq)
	if modelsRec.Code != http.StatusOK {
		t.Fatalf("expected bare models request 200, got %d body=%s", modelsRec.Code, modelsRec.Body.String())
	}
	if strings.Contains(modelsRec.Body.String(), "admin-secret-model") {
		t.Fatalf("expected hidden upstream model removed from bare /v1/models, got %s", modelsRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"admin-secret-model","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected bare hidden upstream model request to be rejected, got %d body=%s", rec.Code, rec.Body.String())
	}
	if responsesHits.Load() != 0 {
		t.Fatalf("expected no upstream responses hit for bare hidden model attack, got %d", responsesHits.Load())
	}
}
