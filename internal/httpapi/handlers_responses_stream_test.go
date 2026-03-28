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

func TestResponsesStreamPreservesRealReasoningItemLifecycle(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.added\n" +
			"data: {\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[]}}\n\n",
		"event: response.reasoning_summary_text.delta\n" +
			"data: {\"item_id\":\"rs_1\",\"summary_index\":0,\"delta\":\"alpha\"}\n\n",
		"event: response.reasoning_summary_text.done\n" +
			"data: {\"item_id\":\"rs_1\",\"summary_index\":0,\"text\":\"alpha\"}\n\n",
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"alpha\"}]}}\n\n",
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

	proxyAddedIdx := strings.Index(body, `{"item":{"id":"rs_proxy"`)
	proxyDoneIdx := strings.Index(body, `event: response.output_item.done`+"\n"+`data: {"item":{"id":"rs_proxy"`)
	realAddedIdx := strings.Index(body, `{"item":{"id":"rs_1","summary":[],"type":"reasoning"},"type":"response.output_item.added"}`)
	realDeltaIdx := strings.Index(body, `{"delta":"alpha","item_id":"rs_1","summary_index":0,"type":"response.reasoning_summary_text.delta"}`)
	realDoneIdx := strings.LastIndex(body, `{"item":{"id":"rs_1","summary":[{"text":"alpha","type":"summary_text"}],"type":"reasoning"},"type":"response.output_item.done"}`)

	if proxyAddedIdx == -1 || proxyDoneIdx == -1 {
		t.Fatalf("expected proxy fallback reasoning item to open and close, got %s", body)
	}
	if realAddedIdx == -1 || realDeltaIdx == -1 || realDoneIdx == -1 {
		t.Fatalf("expected real reasoning item lifecycle and summary delta to be forwarded, got %s", body)
	}
	if !(proxyAddedIdx < proxyDoneIdx && proxyDoneIdx < realAddedIdx && realAddedIdx < realDeltaIdx && realDeltaIdx < realDoneIdx) {
		t.Fatalf("expected fallback reasoning to finish before real reasoning lifecycle begins, got %s", body)
	}
	if strings.Contains(body, `{"item":{"id":"rs_proxy","summary":[{"text":"alpha"`) {
		t.Fatalf("expected real reasoning content to stay on real item instead of being merged into rs_proxy, got %s", body)
	}
}

func TestResponsesStreamReordersFunctionCallLifecycleForClients(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"get_weather\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\\\"Shanghai\\\"}\"}\n\n",
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

	addedIdx := strings.Index(body, `{"item":{"call_id":"call_1","id":"fc_1","name":"get_weather","type":"function_call"},"type":"response.output_item.added"}`)
	deltaIdx := strings.Index(body, `{"delta":"{\"city\":\"Shanghai\"}","item_id":"fc_1","type":"response.function_call_arguments.delta"}`)
	doneIdx := strings.Index(body, `{"item":{"arguments":"{\"city\":\"Shanghai\"}","call_id":"call_1","id":"fc_1","name":"get_weather","type":"function_call"},"type":"response.output_item.done"}`)
	completedIdx := strings.LastIndex(body, `event: response.completed`)

	if addedIdx == -1 || deltaIdx == -1 || doneIdx == -1 || completedIdx == -1 {
		t.Fatalf("expected function call added/delta/done/completed lifecycle, got %s", body)
	}
	if !(addedIdx < deltaIdx && deltaIdx < doneIdx && doneIdx < completedIdx) {
		t.Fatalf("expected function call lifecycle added -> delta -> done -> completed, got %s", body)
	}
}

func TestResponsesStreamAccumulatesMultipleFunctionCallArgumentDeltasBeforeDone(t *testing.T) {
	upstream := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_item.done\n" +
			"data: {\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"search_web\"}}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\"{\\\"query\\\":\\\"Quectel\\\"\"}\n\n",
		"event: response.function_call_arguments.delta\n" +
			"data: {\"item_id\":\"fc_1\",\"delta\":\",\\\"topic\\\":\\\"finance\\\"}\"}\n\n",
		"event: response.completed\n" +
			"data: {\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1," +
			"\"total_tokens\":2}}}\n\n",
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
	if !strings.Contains(body, `{"item":{"arguments":"{\"query\":\"Quectel\",\"topic\":\"finance\"}","call_id":"call_1","id":"fc_1","name":"search_web","type":"function_call"},"type":"response.output_item.done"}`) {
		t.Fatalf("expected final function call item to contain merged arguments, got %s", body)
	}
}

func testResponsesConfig(upstreamURL string) config.Config {
	return config.Config{
		DefaultProvider:      "openai",
		EnableLegacyV1Routes: true,
		Providers: []config.ProviderConfig{{
			ID:                "openai",
			Enabled:           true,
			UpstreamBaseURL:   upstreamURL,
			UpstreamAPIKey:    "test-key",
			SupportsModels:    true,
			SupportsResponses: true,
		}},
	}
}
