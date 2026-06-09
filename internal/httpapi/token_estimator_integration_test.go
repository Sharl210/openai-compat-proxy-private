package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
	"openai-compat-proxy/internal/tokenestimator"
)

func TestResponsesNonStreamRecordsTokenEstimatorObservation(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-ok","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":240,"input_tokens_details":{"cached_tokens":120},"output_tokens":20,"total_tokens":260}}`))
	}))
	defer upstreamServer.Close()

	mgr := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"openai"} })
	server := NewServerWithStore(config.NewStaticRuntimeStore(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstreamServer.URL,
			UpstreamAPIKey:              "provider-upstream-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsResponses:           true,
			SupportsModels:              true,
			EnableReasoningEffortSuffix: true,
			ManualModels:                []string{"gpt-5.4"},
		}},
	}), nil, mgr)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","input":"hello"}`))
	req.Header.Set("Authorization", "Bearer root-secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	state := mgr.GetBucketState(tokenestimator.BucketKey{ProviderID: "openai", EndpointType: config.UpstreamEndpointTypeResponses, Model: "gpt-5.4"})
	if state == nil || state.SampleCount != 1 {
		t.Fatalf("expected recorded estimator state, got %#v", state)
	}
	if state.AvgCachedTokens != 120 {
		t.Fatalf("expected cached tokens 120, got %#v", state)
	}
}

func TestChatStreamRecordsTokenEstimatorObservationOnResponseCompleted(t *testing.T) {
	upstreamServer := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":240,\"input_tokens_details\":{\"cached_tokens\":120},\"output_tokens\":20,\"total_tokens\":260}}}\n\n",
	})
	defer upstreamServer.Close()

	mgr := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"openai"} })
	server := NewServerWithStore(config.NewStaticRuntimeStore(config.Config{
		ProxyAPIKey:          "root-secret",
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                          "openai",
			Enabled:                     true,
			UpstreamBaseURL:             upstreamServer.URL,
			UpstreamAPIKey:              "provider-upstream-key",
			UpstreamEndpointType:        config.UpstreamEndpointTypeResponses,
			SupportsChat:                true,
			SupportsResponses:           true,
			SupportsModels:              true,
			EnableReasoningEffortSuffix: true,
			ManualModels:                []string{"gpt-5.4"},
		}},
	}), nil, mgr)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer root-secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	state := mgr.GetBucketState(tokenestimator.BucketKey{ProviderID: "openai", EndpointType: config.UpstreamEndpointTypeResponses, Model: "gpt-5.4"})
	if state == nil || state.SampleCount != 1 {
		t.Fatalf("expected recorded estimator state, got %#v", state)
	}
}
