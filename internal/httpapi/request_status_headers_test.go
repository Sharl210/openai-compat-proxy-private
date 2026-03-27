package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestResponsesRequestSetsProviderScopedStatusHeadersAndStatusEndpoint(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "proxy-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer proxy-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatalf("expected X-Request-Id header")
	}
	if got := rec.Header().Get("X-STATUS-CHECK-URL"); got != "http://example.com/openai/v1/requests/"+requestID+"?key=proxy-secret" {
		t.Fatalf("expected provider-scoped status URL, got %q", got)
	}
	if got := rec.Header().Get("X-RESPONSE-PROCESS-HEALTH-FLAG"); got != "health" {
		t.Fatalf("expected health flag health, got %q", got)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/"+requestID+"?key=proxy-secret", nil)
	statusRec := httptest.NewRecorder()
	server.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected status endpoint 200, got %d body=%s", statusRec.Code, statusRec.Body.String())
	}
	var status requestStatus
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status response: %v body=%s", err, statusRec.Body.String())
	}
	if status.RequestID != requestID || status.ProviderID != "openai" || status.Status != "completed" || status.HealthFlag != "health" {
		t.Fatalf("unexpected status payload: %#v", status)
	}

	wrongProviderReq := httptest.NewRequest(http.MethodGet, "/other/v1/requests/"+requestID+"?key=proxy-secret", nil)
	wrongProviderRec := httptest.NewRecorder()
	server.ServeHTTP(wrongProviderRec, wrongProviderReq)
	if wrongProviderRec.Code != http.StatusNotFound {
		t.Fatalf("expected wrong provider scoped status lookup to 404, got %d body=%s", wrongProviderRec.Code, wrongProviderRec.Body.String())
	}

	missingKeyReq := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/"+requestID, nil)
	missingKeyRec := httptest.NewRecorder()
	server.ServeHTTP(missingKeyRec, missingKeyReq)
	if missingKeyRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected missing key status lookup to 401, got %d body=%s", missingKeyRec.Code, missingKeyRec.Body.String())
	}
}

func TestResponsesStreamRequestSetsStatusHeadersOnSuccessfulStream(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(config.Config{
		ProxyAPIKey:          "proxy-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer proxy-secret")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatalf("expected X-Request-Id header")
	}
	if got := rec.Header().Get("X-STATUS-CHECK-URL"); got != "http://example.com/openai/v1/requests/"+requestID+"?key=proxy-secret" {
		t.Fatalf("expected provider-scoped status URL, got %q", got)
	}
	if got := rec.Header().Get("X-RESPONSE-PROCESS-HEALTH-FLAG"); got != "health" {
		t.Fatalf("expected health flag health, got %q", got)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/"+requestID+"?key=proxy-secret", nil)
	statusRec := httptest.NewRecorder()
	server.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected status endpoint 200, got %d body=%s", statusRec.Code, statusRec.Body.String())
	}
	var status requestStatus
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status response: %v body=%s", err, statusRec.Body.String())
	}
	if status.RequestID != requestID || status.ProviderID != "openai" || status.Status != "completed" || status.HealthFlag != "health" {
		t.Fatalf("unexpected status payload: %#v", status)
	}
}

func TestUnauthorizedResponsesRequestDoesNotExposeStatusCheckHeaders(t *testing.T) {
	server := NewServer(config.Config{
		ProxyAPIKey:          "proxy-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   "http://upstream.example",
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-STATUS-CHECK-URL"); got != "" {
		t.Fatalf("expected no X-STATUS-CHECK-URL on unauthorized response, got %q", got)
	}
	if got := rec.Header().Get("X-RESPONSE-PROCESS-HEALTH-FLAG"); got != "" {
		t.Fatalf("expected no X-RESPONSE-PROCESS-HEALTH-FLAG on unauthorized response, got %q", got)
	}
}

func TestResponsesMissingUpstreamAuthMarksFailedTerminalStatus(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   "https://example.test",
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d body=%s", rec.Code, rec.Body.String())
	}
	requestID := rec.Header().Get("X-Request-Id")
	statusReq := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/"+requestID, nil)
	statusRec := httptest.NewRecorder()
	server.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected status endpoint 200, got %d body=%s", statusRec.Code, statusRec.Body.String())
	}
	var status requestStatus
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status response: %v body=%s", err, statusRec.Body.String())
	}
	if status.Status != "failed" || !status.Completed || status.ErrorCode != "missing_upstream_auth" {
		t.Fatalf("unexpected status payload: %#v", status)
	}
}

func TestAnthropicInvalidRequestMarksFailedTerminalStatus(t *testing.T) {
	server := NewServer(config.Config{
		DefaultProvider:      "anthropic",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                        "anthropic",
			Enabled:                   true,
			UpstreamBaseURL:           "https://example.test",
			UpstreamAPIKey:            "test-key",
			SupportsAnthropicMessages: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.4","messages":[`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	requestID := rec.Header().Get("X-Request-Id")
	statusReq := httptest.NewRequest(http.MethodGet, "/anthropic/v1/requests/"+requestID, nil)
	statusRec := httptest.NewRecorder()
	server.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected status endpoint 200, got %d body=%s", statusRec.Code, statusRec.Body.String())
	}
	var status requestStatus
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status response: %v body=%s", err, statusRec.Body.String())
	}
	if status.Status != "failed" || !status.Completed || status.ErrorCode != "invalid_request" {
		t.Fatalf("unexpected status payload: %#v", status)
	}
}

func TestResponsesStreamFailureWritesTerminalIncompleteEventAndFailedStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"hello\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {broken json}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	requestID := rec.Header().Get("X-Request-Id")
	if !strings.Contains(rec.Body.String(), "event: response.incomplete") {
		t.Fatalf("expected response.incomplete terminal event, got %s", rec.Body.String())
	}
	statusReq := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/"+requestID, nil)
	statusRec := httptest.NewRecorder()
	server.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected status endpoint 200, got %d body=%s", statusRec.Code, statusRec.Body.String())
	}
	var status requestStatus
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status response: %v body=%s", err, statusRec.Body.String())
	}
	if status.Status != "failed" || !status.Completed || status.HealthFlag != "upstream_stream_broken" || status.ErrorCode != "upstream_stream_broken" {
		t.Fatalf("unexpected failed status payload: %#v", status)
	}
}

func TestResponsesStreamUpstreamIncompleteTimeoutPreservesTimeoutStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"delta\":\"hello\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("event: response.incomplete\n"))
		_, _ = w.Write([]byte("data: {\"request_id\":\"upstream_req\",\"health_flag\":\"upstream_timeout\",\"message\":\"upstream request timed out\"}\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	server := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstream.URL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":true,"input":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	requestID := rec.Header().Get("X-Request-Id")
	if strings.Count(rec.Body.String(), "event: response.incomplete") != 1 {
		t.Fatalf("expected exactly one response.incomplete passthrough event, got %s", rec.Body.String())
	}
	statusReq := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/"+requestID, nil)
	statusRec := httptest.NewRecorder()
	server.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected status endpoint 200, got %d body=%s", statusRec.Code, statusRec.Body.String())
	}
	var status requestStatus
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status response: %v body=%s", err, statusRec.Body.String())
	}
	if status.Status != "failed" || !status.Completed || status.HealthFlag != "upstream_timeout" || status.ErrorCode != "upstream_timeout" {
		t.Fatalf("unexpected timeout status payload: %#v", status)
	}
}
