package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func mustStatusRequestPathFromHeader(t *testing.T, statusURL string) string {
	t.Helper()
	parsed, err := url.Parse(statusURL)
	if err != nil {
		t.Fatalf("parse status url %q: %v", statusURL, err)
	}
	if parsed.Path == "" {
		t.Fatalf("expected status url path, got %q", statusURL)
	}
	if parsed.Query().Get("key") != "" {
		t.Fatalf("expected status url to avoid raw key query, got %q", statusURL)
	}
	if parsed.Query().Get("token") == "" {
		t.Fatalf("expected status url token query, got %q", statusURL)
	}
	return parsed.RequestURI()
}

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
	statusURL := rec.Header().Get("X-STATUS-CHECK-URL")
	if !strings.HasPrefix(statusURL, "http://example.com/openai/v1/requests/"+requestID+"?") {
		t.Fatalf("expected provider-scoped status URL, got %q", statusURL)
	}
	if strings.Contains(statusURL, "proxy-secret") {
		t.Fatalf("expected status URL to hide real proxy key, got %q", statusURL)
	}
	if got := rec.Header().Get("X-RESPONSE-PROCESS-HEALTH-FLAG"); got != "health" {
		t.Fatalf("expected health flag health, got %q", got)
	}

	statusReq := httptest.NewRequest(http.MethodGet, mustStatusRequestPathFromHeader(t, statusURL), nil)
	statusRec := httptest.NewRecorder()
	server.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected status endpoint 200, got %d body=%s", statusRec.Code, statusRec.Body.String())
	}
	nextStatusURL := statusRec.Header().Get("X-STATUS-CHECK-URL")
	if nextStatusURL == "" {
		t.Fatalf("expected status response to issue next status URL")
	}
	if nextStatusURL == statusURL {
		t.Fatalf("expected one-time token status URL to rotate, got %q", nextStatusURL)
	}
	var status requestStatus
	if err := json.Unmarshal(statusRec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status response: %v body=%s", err, statusRec.Body.String())
	}
	if status.RequestID != requestID || status.ProviderID != "openai" || status.Status != "completed" || status.HealthFlag != "health" {
		t.Fatalf("unexpected status payload: %#v", status)
	}

	reusedTokenReq := httptest.NewRequest(http.MethodGet, mustStatusRequestPathFromHeader(t, statusURL), nil)
	reusedTokenRec := httptest.NewRecorder()
	server.ServeHTTP(reusedTokenRec, reusedTokenReq)
	if reusedTokenRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected reused one-time token lookup to 401, got %d body=%s", reusedTokenRec.Code, reusedTokenRec.Body.String())
	}

	rotatedTokenReq := httptest.NewRequest(http.MethodGet, mustStatusRequestPathFromHeader(t, nextStatusURL), nil)
	rotatedTokenRec := httptest.NewRecorder()
	server.ServeHTTP(rotatedTokenRec, rotatedTokenReq)
	if rotatedTokenRec.Code != http.StatusOK {
		t.Fatalf("expected rotated one-time token lookup to 200, got %d body=%s", rotatedTokenRec.Code, rotatedTokenRec.Body.String())
	}

	wrongProviderReq := httptest.NewRequest(http.MethodGet, "/other/v1/requests/"+requestID, nil)
	wrongProviderReq.Header.Set("Authorization", "Bearer proxy-secret")
	wrongProviderRec := httptest.NewRecorder()
	server.ServeHTTP(wrongProviderRec, wrongProviderReq)
	if wrongProviderRec.Code != http.StatusNotFound {
		t.Fatalf("expected wrong provider scoped status lookup to 404, got %d body=%s", wrongProviderRec.Code, wrongProviderRec.Body.String())
	}

	rawKeyReq := httptest.NewRequest(http.MethodGet, "/openai/v1/requests/"+requestID+"?key=proxy-secret", nil)
	rawKeyRec := httptest.NewRecorder()
	server.ServeHTTP(rawKeyRec, rawKeyReq)
	if rawKeyRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected raw key query status lookup to 401, got %d body=%s", rawKeyRec.Code, rawKeyRec.Body.String())
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
	statusURL := rec.Header().Get("X-STATUS-CHECK-URL")
	if !strings.HasPrefix(statusURL, "http://example.com/openai/v1/requests/"+requestID+"?") {
		t.Fatalf("expected provider-scoped status URL, got %q", statusURL)
	}
	if strings.Contains(statusURL, "proxy-secret") {
		t.Fatalf("expected status URL to hide real proxy key, got %q", statusURL)
	}
	if got := rec.Header().Get("X-RESPONSE-PROCESS-HEALTH-FLAG"); got != "streaming" {
		t.Fatalf("expected health flag streaming for active stream response, got %q", got)
	}

	statusReq := httptest.NewRequest(http.MethodGet, mustStatusRequestPathFromHeader(t, statusURL), nil)
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
	if got := rec.Header().Get("X-RESPONSE-PROCESS-HEALTH-FLAG"); got != "streaming" {
		t.Fatalf("expected health flag streaming for stream header, got %q", got)
	}
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
