package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/testutil"
)

func TestResponsesStreamIncludesTypedChunks(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\n" +
			"data: {\"delta\":\"hello\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(testResponsesConfig(upstream.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "event: response.output_item.added") {
		t.Fatalf("expected synthetic reasoning output item start in stream body, got %s", body)
	}
	if !strings.Contains(body, `"id":"rs_proxy"`) || !strings.Contains(body, `"type":"reasoning"`) {
		t.Fatalf("expected synthetic reasoning item payload in stream body, got %s", body)
	}
	if !strings.Contains(body, `"type":"response.reasoning.delta"`) {
		t.Fatalf("expected synthetic reasoning chunk type in stream body, got %s", body)
	}
	if !strings.Contains(body, `"type":"response.reasoning_summary_text.delta"`) {
		t.Fatalf("expected synthetic reasoning summary chunk type in stream body, got %s", body)
	}
	if !strings.Contains(body, `"type":"response.output_text.delta"`) {
		t.Fatalf("expected output_text chunk type in stream body, got %s", body)
	}
}

func TestResponsesStreamKeepsSingleContinuousReasoningItem(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"alpha\"}]}}\n\n",
		"event: response.reasoning.delta\n" +
			"data: {\"summary\":\"beta\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n",
	})
	defer upstream.Close()

	server := NewServer(testResponsesConfig(upstream.URL))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{
		"model":"gpt-5",
		"stream":true,
		"input":[{"role":"user","content":"hello"}]
	}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	body := rec.Body.String()

	if count := strings.Count(body, `event: response.output_item.added`); count != 1 {
		t.Fatalf("expected one reasoning item start, got %d body=%s", count, body)
	}
	if count := strings.Count(body, `event: response.output_item.done`); count != 1 {
		t.Fatalf("expected one reasoning item completion, got %d body=%s", count, body)
	}
	alphaIdx := strings.Index(body, `"summary":"alpha"`)
	betaIdx := strings.Index(body, `"summary":"beta"`)
	doneIdx := strings.LastIndex(body, `event: response.output_item.done`)
	if alphaIdx == -1 || betaIdx == -1 || doneIdx == -1 {
		t.Fatalf("expected merged reasoning stream to include alpha, beta, and a single done event, got %s", body)
	}
	if doneIdx < betaIdx {
		t.Fatalf("expected reasoning item to stay open until after beta summary delta, got %s", body)
	}
	if !strings.Contains(body, `"summary":[{"text":"`) {
		t.Fatalf("expected final reasoning item to include merged summary payload, got %s", body)
	}
	if strings.Contains(body, `"id":"rs_1"`) {
		t.Fatalf("expected upstream reasoning item boundaries to be coalesced into proxy reasoning item, got %s", body)
	}
}

func testResponsesConfig(upstreamURL string) config.Config {
	return config.Config{
		DefaultProvider: "openai",
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstreamURL,
			UpstreamAPIKey:    "test-key",
			SupportsResponses: true,
		}},
	}
}
