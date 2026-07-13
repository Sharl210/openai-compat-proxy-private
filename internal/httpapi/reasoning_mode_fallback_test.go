package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestReasoningModeFallback_retriesEligibleSuffixRequestAfterModeTargeted400(t *testing.T) {
	// Given
	var attempts []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if writeReasoningModeFallbackModelsResponse(w, req) {
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		attempts = append(attempts, payload)
		if len(attempts) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","param":"reasoning.mode","message":"reasoning mode is unsupported"}}`))
			return
		}
		writeReasoningModeFallbackResponse(w)
	}))
	defer upstream.Close()
	server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-pro","reasoning":{"effort":"high","summary":"detailed"},"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(attempts) != 2 {
		t.Fatalf("expected two upstream attempts, got %d", len(attempts))
	}
	assertReasoningModeFallbackAttempt(t, attempts[0], true)
	assertReasoningModeFallbackAttempt(t, attempts[1], false)
}

func TestReasoningModeFallback_reusesNegativeCacheForEligibleSuffixRequest(t *testing.T) {
	// Given
	var attempts []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if writeReasoningModeFallbackModelsResponse(w, req) {
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		attempts = append(attempts, payload)
		if len(attempts) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","param":"reasoning.mode","message":"reasoning mode is unsupported"}}`))
			return
		}
		writeReasoningModeFallbackResponse(w)
	}))
	defer upstream.Close()
	server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses))

	first := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-pro","reasoning":{"effort":"high"},"input":"first"}`))
	first.Header.Set("Content-Type", "application/json")
	firstRec := httptest.NewRecorder()
	second := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-pro","reasoning":{"effort":"high"},"input":"second"}`))
	second.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()

	// When
	server.ServeHTTP(firstRec, first)
	server.ServeHTTP(secondRec, second)

	// Then
	if firstRec.Code != http.StatusOK || secondRec.Code != http.StatusOK {
		t.Fatalf("expected successful fallback responses, first=%d second=%d", firstRec.Code, secondRec.Code)
	}
	if len(attempts) != 3 {
		t.Fatalf("expected first request to use two attempts and cached request one attempt, got %d", len(attempts))
	}
	assertReasoningModeFallbackAttempt(t, attempts[0], true)
	assertReasoningModeFallbackAttempt(t, attempts[1], false)
	assertReasoningModeFallbackAttempt(t, attempts[2], false)
}

func TestReasoningModeFallback_doesNotReuseNegativeCacheAcrossAuthorizationScopes(t *testing.T) {
	// Given
	var attempts []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if writeReasoningModeFallbackModelsResponse(w, req) {
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		attempts = append(attempts, payload)
		reasoning, _ := payload["reasoning"].(map[string]any)
		if _, hasMode := reasoning["mode"]; hasMode {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","param":"reasoning.mode","message":"reasoning mode is unsupported"}}`))
			return
		}
		writeReasoningModeFallbackResponse(w)
	}))
	defer upstream.Close()
	server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses))

	first := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-pro","reasoning":{"effort":"high"},"input":"first"}`))
	first.Header.Set("Content-Type", "application/json")
	first.Header.Set("X-Upstream-Authorization", "Bearer scope-a")
	firstRec := httptest.NewRecorder()
	second := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-pro","reasoning":{"effort":"high"},"input":"second"}`))
	second.Header.Set("Content-Type", "application/json")
	second.Header.Set("X-Upstream-Authorization", "Bearer scope-b")
	secondRec := httptest.NewRecorder()

	// When
	server.ServeHTTP(firstRec, first)
	server.ServeHTTP(secondRec, second)

	// Then
	if firstRec.Code != http.StatusOK || secondRec.Code != http.StatusOK {
		t.Fatalf("expected successful fallback responses, first=%d second=%d", firstRec.Code, secondRec.Code)
	}
	if len(attempts) != 4 {
		t.Fatalf("expected each authorization scope to probe once, got %d attempts", len(attempts))
	}
	assertReasoningModeFallbackAttempt(t, attempts[0], true)
	assertReasoningModeFallbackAttempt(t, attempts[1], false)
	assertReasoningModeFallbackAttempt(t, attempts[2], true)
	assertReasoningModeFallbackAttempt(t, attempts[3], false)
}

func TestReasoningModeFallback_doesNotRetryAfterStreamStarts(t *testing.T) {
	// Given
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if writeReasoningModeFallbackModelsResponse(w, req) {
			return
		}
		upstreamCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n" +
			"data: {\"response\":{\"id\":\"resp_1\",\"object\":\"response\"}}\n\n" +
			"event: error\n" +
			"data: {\"error\":{\"type\":\"invalid_request_error\",\"param\":\"reasoning.mode\",\"message\":\"reasoning mode is unsupported\"}}\n\n"))
	}))
	defer upstream.Close()
	server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-pro","stream":true,"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if upstreamCalls != 1 {
		t.Fatalf("expected no semantic retry after a stream event, got %d upstream calls", upstreamCalls)
	}
}

func TestReasoningModeFallback_doesNotRetryUnrelatedBadRequest(t *testing.T) {
	// Given
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if writeReasoningModeFallbackModelsResponse(w, req) {
			return
		}
		attempts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","param":"reasoning.effort","message":"effort is unsupported"}}`))
	}))
	defer upstream.Close()
	server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-pro","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if attempts != 1 {
		t.Fatalf("expected one upstream attempt, got %d", attempts)
	}
}

func TestReasoningModeFallback_doesNotRetryToolRequest(t *testing.T) {
	// Given
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if writeReasoningModeFallbackModelsResponse(w, req) {
			return
		}
		attempts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","param":"reasoning.mode","message":"reasoning mode is unsupported"}}`))
	}))
	defer upstream.Close()
	server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-pro","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if attempts != 1 {
		t.Fatalf("expected one upstream attempt, got %d", attempts)
	}
}

func TestReasoningModeFallback_doesNotRetryRequestWithEncryptedReasoningInclude(t *testing.T) {
	// Given
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if writeReasoningModeFallbackModelsResponse(w, req) {
			return
		}
		attempts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","param":"reasoning.mode","message":"reasoning mode is unsupported"}}`))
	}))
	defer upstream.Close()
	server := NewServer(reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-pro","reasoning":{"effort":"high"},"include":["reasoning.encrypted_content"],"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected mode rejection to remain a 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if attempts != 1 {
		t.Fatalf("expected encrypted reasoning include to prevent fallback retry, got %d attempts", attempts)
	}
}

func TestReasoningModeFallback_skipsModeForKnownUnsupportedSuffixRequest(t *testing.T) {
	// Given
	var attempts []map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if writeReasoningModeFallbackModelsResponse(w, req) {
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		attempts = append(attempts, payload)
		writeReasoningModeFallbackResponse(w)
	}))
	defer upstream.Close()
	cfg := reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses)
	cfg.Providers[0].ReasoningModeProCapability = config.ReasoningModeProCapabilityUnsupported
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model-pro","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(attempts) != 1 {
		t.Fatalf("expected one upstream attempt, got %d", len(attempts))
	}
	assertReasoningModeFallbackAttempt(t, attempts[0], false)
}

func TestReasoningModeFallback_rejectsKnownUnsupportedBodyModeBeforeUpstream(t *testing.T) {
	// Given
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if writeReasoningModeFallbackModelsResponse(w, req) {
			return
		}
		attempts++
		writeReasoningModeFallbackResponse(w)
	}))
	defer upstream.Close()
	cfg := reasoningModeRouteConfig(upstream.URL, config.UpstreamEndpointTypeResponses)
	cfg.DefaultProReasoningModeSet = true
	cfg.DefaultProReasoningMode = false
	cfg.Providers[0].ReasoningModeProCapability = config.ReasoningModeProCapabilityUnsupported
	server := NewServer(cfg)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"model","reasoning":{"mode":"pro"},"input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	if attempts != 0 {
		t.Fatalf("expected no upstream attempt, got %d", attempts)
	}
	if !strings.Contains(rec.Body.String(), "unsupported_reasoning_mode") {
		t.Fatalf("expected unsupported_reasoning_mode response, got %s", rec.Body.String())
	}
}

func assertReasoningModeFallbackAttempt(t *testing.T, payload map[string]any, expectMode bool) {
	t.Helper()
	reasoning, _ := payload["reasoning"].(map[string]any)
	_, hasMode := reasoning["mode"]
	if hasMode != expectMode {
		t.Fatalf("expected reasoning mode present=%t, got %#v", expectMode, reasoning)
	}
	if got, _ := reasoning["effort"].(string); got != "high" && expectMode {
		t.Fatalf("expected first attempt to preserve reasoning effort, got %#v", reasoning)
	}
}

func writeReasoningModeFallbackResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
}

func writeReasoningModeFallbackModelsResponse(w http.ResponseWriter, req *http.Request) bool {
	if req.URL.Path != "/models" {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model","object":"model"}]}`))
	return true
}
