package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	anthropicadapter "openai-compat-proxy/internal/adapter/anthropic"
	chatadapter "openai-compat-proxy/internal/adapter/chat"
	responsesadapter "openai-compat-proxy/internal/adapter/responses"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

const bodyReasoningFixture = `{"mode":"standard","effort":"low","summary":"detailed","vendor_option":"keep"}`

func TestProxyModelIntent_preservesBodyReasoningFieldsWhenSuffixOverridesMode(t *testing.T) {
	entrypoints := []struct {
		name   string
		body   string
		decode func(io.Reader) (model.CanonicalRequest, error)
	}{
		{
			name:   "responses",
			body:   `{"model":"model-pro","reasoning":` + bodyReasoningFixture + `,"input":"hello"}`,
			decode: responsesadapter.DecodeRequest,
		},
		{
			name:   "chat",
			body:   `{"model":"model-pro","reasoning":` + bodyReasoningFixture + `,"messages":[{"role":"user","content":"hello"}]}`,
			decode: chatadapter.DecodeRequest,
		},
	}

	for _, entrypoint := range entrypoints {
		t.Run(entrypoint.name, func(t *testing.T) {
			// Given
			canon, err := entrypoint.decode(strings.NewReader(entrypoint.body))
			if err != nil {
				t.Fatalf("decode request: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/test", nil)
			req = req.WithContext(withProxyModelIntent(req.Context(), model.ProxyModelIntent{BaseModel: "model", ReasoningMode: "pro"}))

			// When
			applyProxyModelIntentReasoningMode(req, &canon)
			enforceSuffixReasoningModePrecedence(&canon)

			// Then
			assertCanonicalProReasoning(t, canon)
		})
	}
}

func TestModelProRoutes_sendProModeAndPreserveBodyReasoningFields(t *testing.T) {
	entrypoints := []struct {
		name       string
		path       string
		body       string
		setHeaders func(*http.Request)
	}{
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"model-pro","reasoning":` + bodyReasoningFixture + `,"input":"hello"}`,
		},
		{
			name: "chat",
			path: "/v1/chat/completions",
			body: `{"model":"model-pro","reasoning":` + bodyReasoningFixture + `,"messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name: "messages",
			path: "/v1/messages",
			body: `{"model":"model-pro","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`,
			setHeaders: func(req *http.Request) {
				req.Header.Set("anthropic-version", "2023-06-01")
			},
		},
	}

	for _, entrypoint := range entrypoints {
		t.Run(entrypoint.name, func(t *testing.T) {
			// Given
			var upstreamPayload map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/models" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model","object":"model"}]}`))
					return
				}
				if err := json.NewDecoder(req.Body).Decode(&upstreamPayload); err != nil {
					t.Fatalf("decode upstream request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
			}))
			defer upstream.Close()

			server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses))
			req := httptest.NewRequest(http.MethodPost, entrypoint.path, strings.NewReader(entrypoint.body))
			req.Header.Set("Content-Type", "application/json")
			if entrypoint.setHeaders != nil {
				entrypoint.setHeaders(req)
			}
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			reasoning, _ := upstreamPayload["reasoning"].(map[string]any)
			if got := reasoning["mode"]; got != "pro" {
				t.Fatalf("expected upstream mode pro, got %#v preview=%q", upstreamPayload, rec.Header().Get(headerProxyToUpstreamReasoningParameters))
			}
			if entrypoint.name == "messages" {
				return
			}
			assertUpstreamBodyReasoningFields(t, reasoning)
		})
	}
}

func TestProviderSelection_recordsProIntentForModelPro(t *testing.T) {
	// Given
	store := config.NewStaticRuntimeStore(reasoningModeRouteConfig("https://upstream.invalid/v1", config.UpstreamEndpointTypeResponses))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	req = req.Clone(withRuntimeSnapshot(withRouteInfo(req.Context(), routeInfo{
		ProviderID:    "provider",
		Legacy:        true,
		CanonicalPath: canonicalV1ResponsesPath,
	}), store.Active()))

	// When
	_, _, _, resolvedModel, ok, err := providerSelectionForModelRequest(req, "model-pro")

	// Then
	if err != nil {
		t.Fatalf("provider selection error: %v", err)
	}
	if !ok || resolvedModel != "model" {
		t.Fatalf("expected model-pro to resolve as model, got model=%q ok=%t", resolvedModel, ok)
	}
	intent, ok := proxyModelIntentFromRequest(req)
	if !ok || intent.ReasoningMode != "pro" {
		t.Fatalf("expected provider selection to record pro intent, got %#v found=%t", intent, ok)
	}
}

func TestAnthropicMessagesReasoningModeOrigin_transitionsFromNoneToSuffix(t *testing.T) {
	// Given
	canon, err := decodeAnthropicMessagesRequest(`{"model":"model-pro","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)
	if err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if canon.ReasoningModeOrigin != model.ReasoningModeOriginNone {
		t.Fatalf("expected body mode origin none, got %q", canon.ReasoningModeOrigin)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req = req.WithContext(withProxyModelIntent(req.Context(), model.ProxyModelIntent{BaseModel: "model", ReasoningMode: "pro"}))

	// When
	applyProxyModelIntentReasoningMode(req, &canon)
	enforceSuffixReasoningModePrecedence(&canon)

	// Then
	assertCanonicalProMode(t, canon)
}

func decodeAnthropicMessagesRequest(body string) (model.CanonicalRequest, error) {
	return anthropicadapter.DecodeRequest(strings.NewReader(body))
}

func assertCanonicalProReasoning(t *testing.T, canon model.CanonicalRequest) {
	t.Helper()
	assertCanonicalProMode(t, canon)
	if canon.Reasoning.Effort != "low" {
		t.Fatalf("expected body effort low preserved, got %#v", canon.Reasoning)
	}
	if canon.Reasoning.Summary != "detailed" {
		t.Fatalf("expected body summary detailed preserved, got %#v", canon.Reasoning)
	}
	assertUpstreamBodyReasoningFields(t, canon.Reasoning.Raw)
}

func assertCanonicalProMode(t *testing.T, canon model.CanonicalRequest) {
	t.Helper()
	if canon.Reasoning == nil || canon.Reasoning.Mode != model.ReasoningModePro {
		t.Fatalf("expected suffix mode pro, got %#v", canon.Reasoning)
	}
	if canon.ReasoningModeOrigin != model.ReasoningModeOriginSuffix {
		t.Fatalf("expected suffix mode origin, got %q", canon.ReasoningModeOrigin)
	}
}

func assertUpstreamBodyReasoningFields(t *testing.T, reasoning map[string]any) {
	t.Helper()
	if got := reasoning["mode"]; got != "pro" {
		t.Fatalf("expected mode pro, got %#v", reasoning)
	}
	if got := reasoning["effort"]; got != "low" {
		t.Fatalf("expected effort low, got %#v", reasoning)
	}
	if got := reasoning["summary"]; got != "detailed" {
		t.Fatalf("expected summary detailed, got %#v", reasoning)
	}
	if got := reasoning["vendor_option"]; got != "keep" {
		t.Fatalf("expected vendor_option keep, got %#v", reasoning)
	}
}

func TestModelLowProRoutesToBareResponsesModelWithTypedReasoning(t *testing.T) {
	// Given
	var upstreamPayload map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model","object":"model"}]}`))
			return
		}
		if err := json.NewDecoder(req.Body).Decode(&upstreamPayload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer upstream.Close()
	server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-low-pro","reasoning":{"mode":"standard","effort":"high","summary":"detailed","vendor_option":"keep"},"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := upstreamPayload["model"].(string); got != "model" {
		t.Fatalf("expected suffixes stripped from upstream model, got %#v", upstreamPayload)
	}
	reasoning, _ := upstreamPayload["reasoning"].(map[string]any)
	if got, _ := reasoning["effort"].(string); got != "low" {
		t.Fatalf("expected suffix effort low to override body effort, got %#v", reasoning)
	}
	if got, _ := reasoning["mode"].(string); got != "pro" {
		t.Fatalf("expected suffix mode pro to override body mode, got %#v", reasoning)
	}
	if got, _ := reasoning["vendor_option"].(string); got != "keep" {
		t.Fatalf("expected vendor reasoning field preserved, got %#v", reasoning)
	}
}

func TestDefaultProReasoningMode_routesModelWithoutClientMode(t *testing.T) {
	entrypoints := []struct {
		name       string
		path       string
		body       string
		setHeaders func(*http.Request)
	}{
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"model","input":"hello"}`,
		},
		{
			name: "chat",
			path: "/v1/chat/completions",
			body: `{"model":"model","messages":[{"role":"user","content":"hello"}]}`,
		},
		{
			name: "messages",
			path: "/v1/messages",
			body: `{"model":"model","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`,
			setHeaders: func(req *http.Request) {
				req.Header.Set("anthropic-version", "2023-06-01")
			},
		},
	}

	for _, entrypoint := range entrypoints {
		t.Run(entrypoint.name, func(t *testing.T) {
			// Given
			var upstreamPayload map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path == "/models" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model","object":"model"}]}`))
					return
				}
				if err := json.NewDecoder(req.Body).Decode(&upstreamPayload); err != nil {
					t.Fatalf("decode upstream request: %v", err)
				}
				writeReasoningModeFallbackResponse(w)
			}))
			defer upstream.Close()
			server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses))
			req := httptest.NewRequest(http.MethodPost, entrypoint.path, strings.NewReader(entrypoint.body))
			req.Header.Set("Content-Type", "application/json")
			if entrypoint.setHeaders != nil {
				entrypoint.setHeaders(req)
			}
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			reasoning, _ := upstreamPayload["reasoning"].(map[string]any)
			if got, _ := reasoning["mode"].(string); got != "pro" {
				t.Fatalf("expected default pro mode upstream, got %#v", upstreamPayload)
			}
		})
	}
}

func reasoningModeRouteConfig(upstreamURL string, endpointType string) config.Config {
	return config.Config{
		DefaultProvider:             "provider",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{{
			ID:                          "provider",
			Enabled:                     true,
			ManualModels:                []string{"model"},
			EnableReasoningEffortSuffix: true,
			UpstreamBaseURL:             upstreamURL,
			UpstreamAPIKey:              "test-key",
			UpstreamEndpointType:        endpointType,
			SupportsModels:              true,
			SupportsResponses:           true,
			SupportsChat:                true,
			SupportsAnthropicMessages:   true,
		}},
	}
}
