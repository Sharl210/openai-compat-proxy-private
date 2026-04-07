package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"openai-compat-proxy/internal/config"
)

func TestLegacyChatRouteChoosesProviderByExactModelOwner(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	server := NewServer(testLegacyModelRoutingConfig(alpha.URL, beta.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha-chat","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		t.Fatalf("expected X-Provider-Name alpha, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "alpha-chat-upstream" {
		t.Fatalf("expected %s alpha-chat-upstream, got %q", headerProxyToUpstreamModel, got)
	}
	if alpha.Hits() != 1 || beta.Hits() != 0 {
		t.Fatalf("expected only alpha upstream to be hit, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
	if !strings.Contains(rec.Body.String(), `"choices"`) {
		t.Fatalf("expected chat response body, got %s", rec.Body.String())
	}
}

func TestLegacyResponsesRouteChoosesProviderByWildcardFallback(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	server := NewServer(testLegacyModelRoutingConfig(alpha.URL, beta.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"owned-5","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		t.Fatalf("expected X-Provider-Name alpha, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "alpha-5-upstream" {
		t.Fatalf("expected %s alpha-5-upstream, got %q", headerProxyToUpstreamModel, got)
	}
	if alpha.Hits() != 1 || beta.Hits() != 0 {
		t.Fatalf("expected only alpha upstream to be hit, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
	if !strings.Contains(rec.Body.String(), `"output"`) {
		t.Fatalf("expected responses body, got %s", rec.Body.String())
	}
}

func TestLegacyMessagesRouteChoosesProviderByExactModelOwner(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	server := NewServer(testLegacyModelRoutingConfig(alpha.URL, beta.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"alpha-message","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "alpha" {
		t.Fatalf("expected X-Provider-Name alpha, got %q", got)
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "alpha-message-upstream" {
		t.Fatalf("expected %s alpha-message-upstream, got %q", headerProxyToUpstreamModel, got)
	}
	if alpha.Hits() != 1 || beta.Hits() != 0 {
		t.Fatalf("expected only alpha upstream to be hit, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
	if !strings.Contains(rec.Body.String(), `"type":"message"`) {
		t.Fatalf("expected anthropic message response body, got %s", rec.Body.String())
	}
}

func TestExplicitChatRouteKeepsExplicitProviderEvenWhenAnotherDefaultOwnsModel(t *testing.T) {
	alpha := newResponsesProviderUpstream(t, "alpha")
	defer alpha.Close()
	beta := newResponsesProviderUpstream(t, "beta")
	defer beta.Close()

	server := NewServer(testLegacyModelRoutingConfig(alpha.URL, beta.URL))
	req := httptest.NewRequest(http.MethodPost, "/beta/v1/chat/completions", strings.NewReader(`{"model":"alpha-chat","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Provider-Name"); got != "beta" {
		t.Fatalf("expected X-Provider-Name beta, got %q", got)
	}
	if alpha.Hits() != 0 || beta.Hits() != 1 {
		t.Fatalf("expected only beta upstream to be hit, alpha=%d beta=%d", alpha.Hits(), beta.Hits())
	}
	if got := rec.Header().Get(headerProxyToUpstreamModel); got != "alpha-chat" {
		t.Fatalf("expected explicit provider to keep beta normalization path, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), `"choices"`) {
		t.Fatalf("expected chat response body, got %s", rec.Body.String())
	}
}

func testLegacyModelRoutingConfig(alphaURL, betaURL string) config.Config {
	return config.Config{
		DefaultProvider:             "alpha,beta",
		EnableLegacyV1Routes:        true,
		DownstreamNonStreamStrategy: config.DownstreamNonStreamStrategyUpstreamNonStream,
		Providers: []config.ProviderConfig{
			{
				ID:                        "alpha",
				Enabled:                   true,
				UpstreamBaseURL:           alphaURL,
				UpstreamAPIKey:            "alpha-key",
				UpstreamEndpointType:      config.UpstreamEndpointTypeResponses,
				SupportsChat:              true,
				SupportsResponses:         true,
				SupportsAnthropicMessages: true,
				ModelMap: []config.ModelMapEntry{
					config.NewModelMapEntry("alpha-chat", "alpha-chat-upstream"),
					config.NewModelMapEntry("alpha-message", "alpha-message-upstream"),
					config.NewModelMapEntry("owned-*", "alpha-$1-upstream"),
				},
			},
			{
				ID:                        "beta",
				Enabled:                   true,
				UpstreamBaseURL:           betaURL,
				UpstreamAPIKey:            "beta-key",
				UpstreamEndpointType:      config.UpstreamEndpointTypeResponses,
				SupportsChat:              true,
				SupportsResponses:         true,
				SupportsAnthropicMessages: true,
			},
		},
	}
}

type responsesProviderUpstream struct {
	*httptest.Server
	hits atomic.Int32
}

func newResponsesProviderUpstream(t *testing.T, providerID string) *responsesProviderUpstream {
	t.Helper()
	upstream := &responsesProviderUpstream{}
	upstream.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.hits.Add(1)
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp_%[1]s","object":"response","status":"completed","output":[{"id":"msg_%[1]s","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello from %[1]s"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`, providerID)
	}))
	return upstream
}

func (u *responsesProviderUpstream) Hits() int {
	if u == nil {
		return 0
	}
	return int(u.hits.Load())
}
