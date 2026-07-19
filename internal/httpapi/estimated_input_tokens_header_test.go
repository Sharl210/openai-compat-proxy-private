package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
	"openai-compat-proxy/internal/tokenestimator"
)

func TestDisabledContextLimitUsesWarmedContextAwareEstimateInDecimalHeaderWithoutRejecting(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-ok","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	requestBody := `{"model":"gpt-5.5","input":"hello"}`
	baselineServer := NewServer(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                   "openai",
			Enabled:              true,
			SupportsResponses:    true,
			ManualModels:         []string{"gpt-5.5"},
			UpstreamEndpointType: config.UpstreamEndpointTypeResponses,
			UpstreamBaseURL:      upstream.URL,
			UpstreamAPIKey:       "test-key",
		}},
	})
	baselineReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(requestBody))
	baselineRec := httptest.NewRecorder()
	baselineServer.ServeHTTP(baselineRec, baselineReq)
	if baselineRec.Code != http.StatusOK {
		t.Fatalf("expected baseline 200 response, got %d body=%s", baselineRec.Code, baselineRec.Body.String())
	}
	baseEstimate, err := strconv.Atoi(baselineRec.Header().Get(headerProxyEstimatedInputTokens))
	if err != nil || baseEstimate <= 0 {
		t.Fatalf("expected positive baseline estimate, got %q", baselineRec.Header().Get(headerProxyEstimatedInputTokens))
	}

	mgr := tokenestimator.NewManager(t.TempDir(), time.UTC, func() []string { return []string{"openai"} })
	observation := tokenestimator.Observation{
		Bucket: tokenestimator.BucketKey{
			ProviderID:   "openai",
			EndpointType: config.UpstreamEndpointTypeResponses,
			Model:        "gpt-5.5",
		},
		BaseEstimate:        int64(baseEstimate),
		InputTokens:         int64(baseEstimate * 3),
		CachedTokens:        0,
		UncachedInputTokens: int64(baseEstimate * 3),
		Shape:               tokenestimator.ShapePlain,
		ProtocolSignature:   "responses:v1",
		EstimatorSignature:  "base-estimator:v1",
	}
	if err := mgr.RecordObservation("warm-header-estimate", observation); err != nil {
		t.Fatalf("RecordObservation error: %v", err)
	}

	server := NewServerWithStore(config.NewStaticRuntimeStore(config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                      "openai",
			Enabled:                 true,
			SupportsResponses:       true,
			ManualModels:            []string{"gpt-5.5"},
			ModelLimitContextTokens: -1,
			UpstreamEndpointType:    config.UpstreamEndpointTypeResponses,
			UpstreamBaseURL:         upstream.URL,
			UpstreamAPIKey:          "test-key",
		}},
	}), nil, mgr)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(requestBody))
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected disabled limit to preserve success, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "-1" {
		t.Fatalf("expected context limit header -1, got %q", got)
	}
	if got, want := rec.Header().Get(headerProxyEstimatedInputTokens), strconv.Itoa(baseEstimate*3); got != want {
		t.Fatalf("expected learned decimal estimate %q, got %q", want, got)
	}
}

func TestResponsesSuccessWithDisabledContextLimitPreservesEstimatedInputTokensHeader(t *testing.T) {
	// Given
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()
	server := NewServer(config.Config{
		DefaultProvider:           "openai",
		EnableLegacyV1Routes:      true,
		EnableNoPromptModelSuffix: true,
		Providers: []config.ProviderConfig{{
			ID:                      "openai",
			Enabled:                 true,
			SupportsResponses:       true,
			ManualModels:            []string{"gpt-5.5"},
			ModelLimitContextTokens: -1,
			UpstreamEndpointType:    config.UpstreamEndpointTypeResponses,
			UpstreamBaseURL:         upstream.URL,
			UpstreamAPIKey:          "test-key",
		}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello"}`))
	rec := httptest.NewRecorder()

	// When
	server.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 response, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "-1" {
		t.Fatalf("expected context limit header -1, got %q", got)
	}
	got := rec.Header().Get(headerProxyEstimatedInputTokens)
	estimatedTokens, err := strconv.Atoi(got)
	if err != nil || estimatedTokens <= 0 {
		t.Fatalf("expected positive decimal estimated input tokens header when context limit disabled, got %q", got)
	}
}

func TestDisabledContextLimitPreservesEstimatedInputTokensHeaderAcrossTextProtocols(t *testing.T) {
	for _, tc := range []struct {
		name             string
		path             string
		body             string
		upstreamEndpoint string
		model            string
		response         string
	}{
		{
			name:             "chat completions",
			path:             "/v1/chat/completions",
			body:             `{"model":"gpt-5.5","messages":[{"role":"user","content":"hello"}]}`,
			upstreamEndpoint: config.UpstreamEndpointTypeChat,
			model:            "gpt-5.5",
			response:         `{"id":"chatcmpl_test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		},
		{
			name:             "anthropic messages",
			path:             "/v1/messages",
			body:             `{"model":"claude-sonnet-4-5","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`,
			upstreamEndpoint: config.UpstreamEndpointTypeAnthropic,
			model:            "claude-sonnet-4-5",
			response:         `{"id":"msg_test","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer upstream.Close()
			server := NewServer(config.Config{
				DefaultProvider:             "test",
				EnableLegacyV1Routes:        true,
				DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
				Providers: []config.ProviderConfig{{
					ID:                        "test",
					Enabled:                   true,
					ManualModels:              []string{tc.model},
					ModelLimitContextTokens:   -1,
					UpstreamBaseURL:           upstream.URL,
					UpstreamAPIKey:            "test-key",
					UpstreamEndpointType:      tc.upstreamEndpoint,
					SupportsChat:              true,
					SupportsAnthropicMessages: true,
				}},
			})
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.upstreamEndpoint == config.UpstreamEndpointTypeAnthropic {
				req.Header.Set("anthropic-version", "2023-06-01")
			}
			rec := httptest.NewRecorder()

			// When
			server.ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusOK {
				t.Fatalf("expected successful response, got %d body=%s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get(headerProxyModelLimitContextTokens); got != "-1" {
				t.Fatalf("expected context limit header -1, got %q", got)
			}
			got := rec.Header().Get(headerProxyEstimatedInputTokens)
			estimatedTokens, err := strconv.Atoi(got)
			if err != nil || estimatedTokens <= 0 {
				t.Fatalf("expected positive decimal estimated input tokens header, got %q", got)
			}
		})
	}
}
