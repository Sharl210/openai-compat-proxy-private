package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestReasoningModeHeadersDescribeEffectiveMode(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		configure        func(*config.Config)
		wantClientMode   string
		wantUpstreamMode string
	}{
		{
			name: "no mode",
			body: `{"model":"model","input":"hello"}`,
			configure: func(cfg *config.Config) {
				cfg.DefaultProReasoningModeSet = true
				cfg.DefaultProReasoningMode = false
			},
		},
		{
			name: "suffix overrides body",
			body: `{"model":"model-pro","reasoning":{"mode":"standard"},"input":"hello"}`,
			configure: func(cfg *config.Config) {
				cfg.EnableReasoningModeSuffixSet = true
				cfg.EnableReasoningModeSuffix = true
				cfg.DefaultProReasoningModeSet = true
				cfg.DefaultProReasoningMode = false
				cfg.Providers[0].EnableReasoningModeSuffixSet = true
				cfg.Providers[0].EnableReasoningModeSuffix = true
			},
			wantClientMode:   "pro",
			wantUpstreamMode: "pro",
		},
		{
			name: "proxy default",
			body: `{"model":"model","input":"hello"}`,
			configure: func(cfg *config.Config) {
				cfg.DefaultProReasoningModeSet = true
				cfg.DefaultProReasoningMode = true
			},
			wantUpstreamMode: "pro",
		},
		{
			name: "proxy default exclusion",
			body: `{"model":"model","input":"hello"}`,
			configure: func(cfg *config.Config) {
				cfg.DefaultProReasoningModeSet = true
				cfg.DefaultProReasoningMode = true
				cfg.DefaultProReasoningModeExcludedModels = []config.ModelPatternRule{{Pattern: "model", IsExact: true}}
			},
		},
		{
			name: "known unsupported suffix",
			body: `{"model":"model-pro","input":"hello"}`,
			configure: func(cfg *config.Config) {
				cfg.EnableReasoningModeSuffixSet = true
				cfg.EnableReasoningModeSuffix = true
				cfg.DefaultProReasoningModeSet = true
				cfg.DefaultProReasoningMode = false
				cfg.Providers[0].EnableReasoningModeSuffixSet = true
				cfg.Providers[0].EnableReasoningModeSuffix = true
				cfg.Providers[0].ReasoningModeProCapability = config.ReasoningModeProCapabilityUnsupported
			},
			wantClientMode:   "pro",
			wantUpstreamMode: "pro_failed:model_unsupported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if writeReasoningModeFallbackModelsResponse(w, request) {
					return
				}
				writeReasoningModeFallbackResponse(w)
			}))
			defer upstream.Close()

			cfg := reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses)
			tt.configure(&cfg)
			server := NewServer(cfg)
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			assertReasoningModeHeaders(t, rec, tt.wantClientMode, tt.wantUpstreamMode)
		})
	}
}

func TestReasoningModeHeadersReportProbeFallback(t *testing.T) {
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if writeReasoningModeFallbackModelsResponse(w, request) {
			return
		}
		attempts++
		if attempts == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","param":"reasoning.mode"}}`))
			return
		}
		writeReasoningModeFallbackResponse(w)
	}))
	defer upstream.Close()

	cfg := reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses)
	cfg.EnableReasoningModeSuffixSet = true
	cfg.EnableReasoningModeSuffix = true
	cfg.DefaultProReasoningModeSet = true
	cfg.DefaultProReasoningMode = false
	cfg.Providers[0].EnableReasoningModeSuffixSet = true
	cfg.Providers[0].EnableReasoningModeSuffix = true
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-pro","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if attempts != 2 {
		t.Fatalf("expected probe fallback to issue two requests, got %d", attempts)
	}
	assertReasoningModeHeaders(t, rec, "pro", "pro_failed:model_unsupported")
	if got := rec.Header().Get(headerProxyToUpstreamReasoningParameters); got != "" {
		t.Fatalf("expected fallback header to match the retry without reasoning parameters, got %q", got)
	}
}

func TestModelMapTargetModeAndEffort_applyUpstreamWithoutChangingClientMode(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		configure func(*config.Config)
		wantModel string
	}{
		{
			name:      "bare root route",
			path:      "/v1/responses",
			wantModel: "root-target",
			configure: func(cfg *config.Config) {
				cfg.V1ModelMap = []config.ModelMapEntry{config.NewModelMapEntry("client", "root-target-low-pro")}
			},
		},
		{
			name:      "explicit provider route bypasses root map",
			path:      "/provider/v1/responses",
			wantModel: "provider-target",
			configure: func(cfg *config.Config) {
				cfg.V1ModelMap = []config.ModelMapEntry{config.NewModelMapEntry("client", "root-target-low-pro")}
				cfg.Providers[0].ModelMap = []config.ModelMapEntry{config.NewModelMapEntry("client", "provider-target-low-pro")}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			var upstreamPayload map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if err := json.NewDecoder(req.Body).Decode(&upstreamPayload); err != nil {
					t.Fatalf("decode upstream request: %v", err)
				}
				writeReasoningModeFallbackResponse(w)
			}))
			defer upstream.Close()

			cfg := reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses)
			cfg.DefaultProReasoningModeSet = true
			cfg.DefaultProReasoningMode = false
			cfg.Providers[0].SupportsModels = false
			cfg.Providers[0].ManualModels = []string{"root-target", "provider-target"}
			tt.configure(&cfg)
			server := NewServer(cfg)
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(`{"model":"client","input":"hello"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get(headerClientToProxyReasoningMode); got != "" {
				t.Fatalf("expected client mode to remain empty, got %q", got)
			}
			if got := rec.Header().Get(headerProxyToUpstreamReasoningMode); got != "pro" {
				t.Fatalf("expected mapped target pro mode upstream, got %q", got)
			}
			if got := rec.Header().Get(headerProxyToUpstreamReasoningEffort); got != "low" {
				t.Fatalf("expected mapped target low effort upstream, got %q", got)
			}
			if got, _ := upstreamPayload["model"].(string); got != tt.wantModel {
				t.Fatalf("expected mapped upstream model %q, got %#v", tt.wantModel, upstreamPayload)
			}
		})
	}
}

func assertReasoningModeHeaders(t *testing.T, rec *httptest.ResponseRecorder, wantClientMode string, wantUpstreamMode string) {
	t.Helper()
	for _, header := range []string{headerClientToProxyReasoningMode, headerProxyToUpstreamReasoningMode} {
		if _, exists := rec.Result().Header[http.CanonicalHeaderKey(header)]; !exists {
			t.Fatalf("expected header %s to be present", header)
		}
	}
	if got := rec.Header().Get(headerClientToProxyReasoningMode); got != wantClientMode {
		t.Fatalf("expected client reasoning mode %q, got %q", wantClientMode, got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamReasoningMode); got != wantUpstreamMode {
		t.Fatalf("expected upstream reasoning mode %q, got %q", wantUpstreamMode, got)
	}
}
