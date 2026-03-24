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
	if !(addedIdx < doneIdx && doneIdx < textIdx) {
		t.Fatalf("expected synthetic reasoning item to close before text without extra late reasoning, got %s", body)
	}
	lateTail := body[textIdx:]
	if strings.Contains(lateTail, `response.output_item.done`) || strings.Contains(lateTail, `已完成思考`) {
		t.Fatalf("expected no late fallback reasoning text after output text, got %s", body)
	}
	if strings.Contains(body, `分析中…`) || strings.Contains(body, `正在组织回答…`) || strings.Contains(body, `正在调用工具…`) {
		t.Fatalf("expected synthetic fallback reasoning to use only one generic phrase, got %s", body)
	}
	if !strings.Contains(body, `"summary":[{"text":"**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长\n"`) {
		t.Fatalf("expected synthetic reasoning done item to include non-empty summary text, got %s", body)
	}
}
