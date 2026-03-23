package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openai-compat-proxy/internal/testutil"
)

func TestResponsesStreamClosesSyntheticReasoningWithoutRealReasoning(t *testing.T) {
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
	addedIdx := strings.Index(body, `event: response.output_item.added`)
	doneIdx := strings.LastIndex(body, `event: response.output_item.done`)
	textIdx := strings.Index(body, `event: response.output_text.delta`)
	if addedIdx == -1 || doneIdx == -1 {
		t.Fatalf("expected synthetic reasoning item to be opened and closed, got %s", body)
	}
	if textIdx == -1 {
		t.Fatalf("expected output text event, got %s", body)
	}
	if !(addedIdx < textIdx && textIdx < doneIdx) {
		t.Fatalf("expected synthetic reasoning to close after text if no real reasoning appears, got %s", body)
	}
}
