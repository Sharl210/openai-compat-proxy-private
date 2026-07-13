package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
)

func TestReasoningModeFallbackDoesNotRetryNonModeHTTPStatusErrors(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{
			name:       "rate limit",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"type":"rate_limit_error","param":"reasoning.mode"}}`,
		},
		{
			name:       "gateway failure",
			statusCode: http.StatusBadGateway,
			body:       `{"error":{"type":"invalid_request_error","param":"reasoning.mode"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var attempts []map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if writeReasoningModeFallbackModelsResponse(w, request) {
					return
				}
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatalf("decode upstream request: %v", err)
				}
				attempts = append(attempts, payload)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer upstream.Close()

			server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses))
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-pro","reasoning":{"effort":"high"},"input":"hello"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if len(attempts) != 1 {
				t.Fatalf("expected one upstream attempt for %s, got %d", tt.name, len(attempts))
			}
			assertReasoningModeFallbackAttempt(t, attempts[0], true)
		})
	}
}

func TestReasoningModeFallbackDoesNotRetryStatefulResponsesRequests(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "stored response",
			body: `{"model":"model-pro","reasoning":{"effort":"high"},"store":true,"input":"hello"}`,
		},
		{
			name: "previous response",
			body: `{"model":"model-pro","reasoning":{"effort":"high"},"previous_response_id":"resp_previous","input":"hello"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var attempts []map[string]any
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if writeReasoningModeFallbackModelsResponse(w, request) {
					return
				}
				var payload map[string]any
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatalf("decode upstream request: %v", err)
				}
				attempts = append(attempts, payload)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","param":"reasoning.mode"}}`))
			}))
			defer upstream.Close()

			server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses))
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			server.ServeHTTP(rec, req)

			if len(attempts) != 1 {
				t.Fatalf("expected stateful request to use one upstream attempt, got %d", len(attempts))
			}
			assertReasoningModeFallbackAttempt(t, attempts[0], true)
		})
	}
}

func TestReasoningModeFallbackDoesNotRetryBodyOriginModeInProbePolicy(t *testing.T) {
	var attempts []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if writeReasoningModeFallbackModelsResponse(w, request) {
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		attempts = append(attempts, payload)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","param":"reasoning.mode"}}`))
	}))
	defer upstream.Close()

	cfg := reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses)
	cfg.DefaultProReasoningModeSet = true
	cfg.DefaultProReasoningMode = false
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","reasoning":{"mode":"pro","effort":"high"},"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if len(attempts) != 1 {
		t.Fatalf("expected body-origin mode to use one upstream attempt, got %d", len(attempts))
	}
	assertReasoningModeFallbackAttempt(t, attempts[0], true)
}

func TestReasoningModeFallbackCacheKeyChangesWithRuntimeConfigVersion(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	providerCfg := config.Config{
		UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
		UpstreamBaseURL:      "https://upstream.example",
	}
	firstSnapshot := &config.RuntimeSnapshot{
		RootEnvVersion:      "root-v1",
		ProviderVersionByID: map[string]string{"provider": "provider-v1"},
	}
	secondSnapshot := &config.RuntimeSnapshot{
		RootEnvVersion:      "root-v2",
		ProviderVersionByID: map[string]string{"provider": "provider-v2"},
	}

	firstRequest := request.Clone(withRuntimeSnapshot(request.Context(), firstSnapshot))
	secondRequest := request.Clone(withRuntimeSnapshot(request.Context(), secondSnapshot))
	firstKey := reasoningModeFallbackKeyForRequest(firstRequest, "provider", providerCfg, "model", "Bearer upstream-key")
	secondKey := reasoningModeFallbackKeyForRequest(secondRequest, "provider", providerCfg, "model", "Bearer upstream-key")

	if firstKey == secondKey {
		t.Fatalf("expected config versions to isolate negative cache keys, got %#v", firstKey)
	}
}

func TestReasoningModeFallbackDoesNotRetryTimeout(t *testing.T) {
	request := model.CanonicalRequest{
		Reasoning: &model.CanonicalReasoning{Mode: model.ReasoningModePro},
	}
	coordinator := &reasoningModeFallbackCoordinator{
		fallback: cloneRequestWithoutReasoningMode(request),
		eligible: true,
	}
	attempts := 0

	_, _, err := executeWithReasoningModeFallback(request, coordinator, func(model.CanonicalRequest) (struct{}, error) {
		attempts++
		return struct{}{}, context.DeadlineExceeded
	})

	if err == nil {
		t.Fatal("expected timeout error")
	}
	if attempts != 1 {
		t.Fatalf("expected timeout to avoid semantic retry, got %d attempts", attempts)
	}
	if coordinator.retried {
		t.Fatal("expected timeout to leave fallback coordinator unused")
	}
}

func TestReasoningModeFallbackDoesNotRetryAfterStreamStartsPreservesMode(t *testing.T) {
	upstreamCalls := 0
	firstRequestCarriedMode := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if writeReasoningModeFallbackModelsResponse(w, request) {
			return
		}
		upstreamCalls++
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		reasoning, _ := payload["reasoning"].(map[string]any)
		_, firstRequestCarriedMode = reasoning["mode"]
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n" +
			"data: {\"response\":{\"id\":\"resp_1\",\"object\":\"response\"}}\n\n" +
			"event: error\n" +
			"data: {\"error\":{\"type\":\"invalid_request_error\",\"param\":\"reasoning.mode\"}}\n\n"))
	}))
	defer upstream.Close()

	server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-pro","stream":true,"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if upstreamCalls != 1 {
		t.Fatalf("expected one upstream stream attempt, got %d", upstreamCalls)
	}
	if !firstRequestCarriedMode {
		t.Fatal("expected the stream-opening request to retain reasoning.mode")
	}
}
