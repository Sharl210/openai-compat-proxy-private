package responses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

func TestDecodeRequestAcceptsStringInputAsSingleUserMessage(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":"hello"
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.Messages) != 1 {
		t.Fatalf("expected one canonical message, got %#v", canon.Messages)
	}
	if canon.Messages[0].Role != "user" {
		t.Fatalf("expected user role, got %#v", canon.Messages[0])
	}
	if len(canon.Messages[0].Parts) != 1 || canon.Messages[0].Parts[0].Text != "hello" {
		t.Fatalf("expected single text part hello, got %#v", canon.Messages[0])
	}
	if len(canon.ResponseInputItems) != 1 {
		t.Fatalf("expected one preserved input item, got %#v", canon.ResponseInputItems)
	}
	if got, _ := canon.ResponseInputItems[0]["role"].(string); got != "user" {
		t.Fatalf("expected preserved role user, got %#v", canon.ResponseInputItems[0])
	}
}

func TestDecodeRequestAcceptsSingleObjectInput(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":{"role":"user","content":"hello"}
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.Messages) != 1 {
		t.Fatalf("expected one canonical message, got %#v", canon.Messages)
	}
	if canon.Messages[0].Role != "user" {
		t.Fatalf("expected user role, got %#v", canon.Messages[0])
	}
	if len(canon.ResponseInputItems) != 1 {
		t.Fatalf("expected one preserved input item, got %#v", canon.ResponseInputItems)
	}
}

func TestDecodeRequestPreservesResponsesStatefulFields(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"store":false,
		"include":["reasoning.encrypted_content"],
		"input":[
			{"role":"user","content":"hello"},
			{"type":"reasoning","id":"rs_123","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"enc_123"},
			{"type":"function_call_output","call_id":"call_123","output":"{}"}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if canon.ResponseStore == nil || *canon.ResponseStore {
		t.Fatalf("expected ResponseStore=false, got %#v", canon.ResponseStore)
	}
	if len(canon.ResponseInclude) != 1 || canon.ResponseInclude[0] != "reasoning.encrypted_content" {
		t.Fatalf("expected ResponseInclude to preserve reasoning.encrypted_content, got %#v", canon.ResponseInclude)
	}
	if len(canon.ResponseInputItems) != 3 {
		t.Fatalf("expected 3 ResponseInputItems, got %#v", canon.ResponseInputItems)
	}
	if got, _ := canon.ResponseInputItems[1]["type"].(string); got != "reasoning" {
		t.Fatalf("expected reasoning input item to be preserved, got %#v", canon.ResponseInputItems[1])
	}
	if got, _ := canon.ResponseInputItems[2]["type"].(string); got != "function_call_output" {
		t.Fatalf("expected function_call_output input item to be preserved, got %#v", canon.ResponseInputItems[2])
	}
	if got, _ := canon.ResponseInputItems[2]["call_id"].(string); got != "call_123" {
		t.Fatalf("expected function_call_output call_id to be preserved, got %#v", canon.ResponseInputItems[2])
	}
	if len(canon.Messages) != 3 {
		t.Fatalf("expected canonical user, reasoning, and tool result messages, got %#v", canon.Messages)
	}
	if canon.Messages[0].Role != "user" {
		t.Fatalf("expected first canonical message to remain user, got %#v", canon.Messages)
	}
	if canon.Messages[1].Role != "assistant" || len(canon.Messages[1].ReasoningBlocks) != 1 {
		t.Fatalf("expected reasoning input item to also become canonical assistant reasoning, got %#v", canon.Messages[1])
	}
	if got := canon.Messages[1].ReasoningContent; got != "thinking" {
		t.Fatalf("expected reasoning summary to become canonical reasoning_content, got %q", got)
	}
	if canon.Messages[2].Role != "tool" || canon.Messages[2].ToolCallID != "call_123" {
		t.Fatalf("expected function_call_output to also become canonical tool message, got %#v", canon.Messages[2])
	}
}

func TestDecodeRequestDropsSyntheticProxyReasoningWhitespaceResidue(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[
			{"role":"user","content":"hello"},
			{"type":"reasoning","id":"rs_proxy","summary":[{"type":"summary_text","text":"\u200b \ufeff\n\t"}]}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if !canon.HasSyntheticReasoningReplay {
		t.Fatalf("expected rs_proxy residue to be marked synthetic replay")
	}
	if len(canon.Messages) != 1 || canon.Messages[0].Role != "user" {
		t.Fatalf("expected only user message after dropping synthetic residue, got %#v", canon.Messages)
	}
	for _, item := range canon.ResponseInputItems {
		if itemType, _ := item["type"].(string); itemType == "reasoning" {
			t.Fatalf("expected synthetic residue reasoning input not to be preserved, got %#v", canon.ResponseInputItems)
		}
	}
}

func TestDecodeRequestPreservesCompactionItemsWithoutCanonicalMessages(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[
			{"role":"user","content":"hello"},
			{"type":"compaction","id":"cmp_123","encrypted_content":"enc_payload","summary":[{"type":"summary_text","text":"condensed"}]}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.ResponseInputItems) != 2 {
		t.Fatalf("expected preserved user item plus preserved compaction item, got %#v", canon.ResponseInputItems)
	}
	if got, _ := canon.ResponseInputItems[1]["type"].(string); got != "compaction" {
		t.Fatalf("expected compaction item to remain in ResponseInputItems, got %#v", canon.ResponseInputItems[1])
	}
	if got, _ := canon.ResponseInputItems[1]["encrypted_content"].(string); got != "enc_payload" {
		t.Fatalf("expected compaction encrypted_content to remain opaque, got %#v", canon.ResponseInputItems[1])
	}
	if len(canon.Messages) != 1 {
		t.Fatalf("expected compaction item not to create canonical messages, got %#v", canon.Messages)
	}
	if canon.Messages[0].Role != "user" {
		t.Fatalf("expected only original user message to remain canonical, got %#v", canon.Messages)
	}
	if len(canon.Messages[0].ToolCalls) != 0 {
		t.Fatalf("expected no canonical tool calls from compaction item, got %#v", canon.Messages[0])
	}
}

func TestDecodeRequestTurnsFunctionCallOutputIntoCanonicalToolMessage(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[
			{"role":"assistant","content":[],"tool_calls":[{"id":"call_123","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"weather\"}"}}]},
			{"type":"function_call_output","call_id":"call_123","output":"{\"temperature\":26}"}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.Messages) != 2 {
		t.Fatalf("expected assistant message plus canonical tool result message, got %#v", canon.Messages)
	}
	toolMsg := canon.Messages[1]
	if toolMsg.Role != "tool" {
		t.Fatalf("expected canonical tool role, got %#v", toolMsg)
	}
	if toolMsg.ToolCallID != "call_123" {
		t.Fatalf("expected canonical tool call id call_123, got %#v", toolMsg)
	}
	if len(toolMsg.Parts) != 1 || toolMsg.Parts[0].Type != "text" || toolMsg.Parts[0].Text != `{"temperature":26}` {
		t.Fatalf("expected canonical tool text output preserved, got %#v", toolMsg)
	}
	if len(canon.ResponseInputItems) != 2 {
		t.Fatalf("expected original response input items preserved too, got %#v", canon.ResponseInputItems)
	}
}

func TestDecodeRequestTurnsResponsesFunctionCallItemIntoCanonicalAssistantToolCall(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[
			{"type":"reasoning","id":"rs_123","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"enc_123"},
			{"type":"function_call","id":"call_123","call_id":"call_123","name":"search_web","arguments":"{\"query\":\"weather\"}"}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.Messages) != 1 {
		t.Fatalf("expected reasoning and function_call output items to merge into one assistant message, got %#v", canon.Messages)
	}
	msg := canon.Messages[0]
	if msg.Role != "assistant" {
		t.Fatalf("expected assistant message, got %#v", msg)
	}
	if len(msg.ReasoningBlocks) != 1 {
		t.Fatalf("expected reasoning block preserved, got %#v", msg)
	}
	if got := msg.ReasoningContent; got != "thinking" {
		t.Fatalf("expected reasoning summary to become canonical reasoning_content, got %q", got)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("expected function_call item preserved as tool call, got %#v", msg)
	}
	if got := msg.ToolCalls[0].ID; got != "call_123" {
		t.Fatalf("expected tool call id call_123, got %#v", msg.ToolCalls[0])
	}
}

func TestDecodeRequestUsesResponsesFunctionCallIDWhenCallIDMissing(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[
			{"type":"reasoning","id":"rs_123","summary":[{"type":"summary_text","text":"thinking"}],"encrypted_content":"enc_123"},
			{"type":"function_call","id":"call_123","name":"search_web","arguments":"{\"query\":\"weather\"}"},
			{"type":"function_call_output","call_id":"call_123","output":"{\"ok\":true}"}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.Messages) != 2 {
		t.Fatalf("expected assistant tool call plus tool result, got %#v", canon.Messages)
	}
	if got := canon.Messages[0].ToolCalls[0].ID; got != "call_123" {
		t.Fatalf("expected function_call id fallback to produce call_123, got %#v", canon.Messages[0].ToolCalls[0])
	}
	if got := canon.Messages[1].ToolCallID; got != "call_123" {
		t.Fatalf("expected matching tool result call_123, got %#v", canon.Messages[1])
	}
}

func TestDecodeRequestPreservesAssistantMessagesAsOutputText(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[
			{"role":"user","content":"第一句"},
			{"role":"assistant","content":"第二句"},
			{"role":"user","content":"第三句"}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.ResponseInputItems) != 3 {
		t.Fatalf("expected 3 ResponseInputItems, got %#v", canon.ResponseInputItems)
	}
	assistant, _ := canon.ResponseInputItems[1]["content"].([]map[string]any)
	if len(assistant) != 1 {
		t.Fatalf("expected assistant content preserved, got %#v", canon.ResponseInputItems[1])
	}
	if got, _ := assistant[0]["type"].(string); got != "output_text" {
		t.Fatalf("expected assistant content type output_text, got %#v", canon.ResponseInputItems[1])
	}
}

func TestDecodeRequestPreservesAssistantStructuredMessagesAsOutputText(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[
			{"role":"assistant","content":[{"type":"text","text":"第二句"}]}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	assistant, _ := canon.ResponseInputItems[0]["content"].([]map[string]any)
	if got, _ := assistant[0]["type"].(string); got != "output_text" {
		t.Fatalf("expected assistant structured content type output_text, got %#v", canon.ResponseInputItems[0])
	}
}

func TestDecodeRequestPreservesAssistantReasoningBlocksWhenContentAlsoHasToolUse(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[
			{"role":"assistant","content":[
				{"type":"reasoning","id":"rs_123","summary":[{"type":"summary_text","text":"internal reasoning"}],"encrypted_content":"enc_123"},
				{"type":"output_text","text":"我先搜一下"}
			],"tool_calls":[{"id":"call_123","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"weather\"}"}}]}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.Messages) != 1 {
		t.Fatalf("expected one canonical assistant message, got %#v", canon.Messages)
	}
	msg := canon.Messages[0]
	if msg.Role != "assistant" {
		t.Fatalf("expected assistant role, got %#v", msg)
	}
	if len(msg.ReasoningBlocks) != 1 {
		t.Fatalf("expected reasoning block preserved alongside tool call, got %#v", msg)
	}
	if got, _ := msg.ReasoningBlocks[0]["type"].(string); got != "reasoning" {
		t.Fatalf("expected reasoning block type preserved, got %#v", msg.ReasoningBlocks)
	}
	if got, _ := msg.ReasoningBlocks[0]["encrypted_content"].(string); got != "enc_123" {
		t.Fatalf("expected encrypted_content preserved, got %#v", msg.ReasoningBlocks)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_123" {
		t.Fatalf("expected tool call preserved, got %#v", msg)
	}
	if len(msg.Parts) != 1 || msg.Parts[0].Text != "我先搜一下" {
		t.Fatalf("expected assistant text preserved, got %#v", msg)
	}
}

func TestDecodeRequestDropsSyntheticTopLevelReasoningInputItem(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[
			{"role":"user","content":"hello"},
			{"type":"reasoning","id":"rs_proxy","summary":[{"type":"summary_text","text":"**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长"}]},
			{"type":"function_call","call_id":"call_1","name":"search_web","arguments":"{\"query\":\"weather\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	for _, item := range canon.ResponseInputItems {
		if got, _ := item["id"].(string); got == "rs_proxy" {
			t.Fatalf("expected synthetic rs_proxy reasoning item to be dropped from preserved input items, got %#v", canon.ResponseInputItems)
		}
	}
	for _, msg := range canon.Messages {
		if len(msg.ReasoningBlocks) != 0 {
			t.Fatalf("expected synthetic rs_proxy reasoning item to stay out of canonical reasoning blocks, got %#v", canon.Messages)
		}
	}
}

func TestDecodeRequestPreservesRealReasoningFromProxyReasoningBeforeConsecutiveToolCalls(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[
			{"role":"user","content":"hello"},
			{"type":"reasoning","id":"rs_proxy","summary":[{"type":"summary_text","text":"​真实推理\n"}]},
			{"type":"function_call","call_id":"call_1","name":"search_web","arguments":"{\"query\":\"weather\"}"},
			{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"},
			{"type":"function_call","call_id":"call_2","name":"search_web","arguments":"{\"query\":\"news\"}"},
			{"type":"function_call_output","call_id":"call_2","output":"{\"ok\":true}"}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.Messages) != 4 {
		t.Fatalf("expected user, one multi-tool assistant, and two tool results, got %#v", canon.Messages)
	}
	firstCall := canon.Messages[1]
	if firstCall.Role != "assistant" || len(firstCall.ToolCalls) != 2 || firstCall.ToolCalls[0].ID != "call_1" || firstCall.ToolCalls[1].ID != "call_2" {
		t.Fatalf("expected one assistant message with both tool calls, got %#v", firstCall)
	}
	if firstCall.ReasoningContent != "真实推理\n" {
		t.Fatalf("expected real proxy reasoning to be preserved before first tool call, got %#v", firstCall)
	}
	if len(firstCall.ReasoningBlocks) != 1 {
		t.Fatalf("expected real proxy reasoning block to be preserved, got %#v", firstCall)
	}
	if canon.Messages[2].Role != "tool" || canon.Messages[2].ToolCallID != "call_1" || canon.Messages[3].Role != "tool" || canon.Messages[3].ToolCallID != "call_2" {
		t.Fatalf("expected both tool results to remain ordered after assistant tool calls, got %#v", canon.Messages)
	}
	for _, item := range canon.ResponseInputItems {
		if got, _ := item["id"].(string); got == "rs_proxy" {
			t.Fatalf("expected proxy reasoning item to stay out of preserved input items, got %#v", canon.ResponseInputItems)
		}
	}
}

func TestDecodeRequestReplaysAdjacentToolProductionShape(t *testing.T) {
	req := readReplayRequestBody(t, "../../../testdata/replay/req-1781731449828543605-25/request.ndjson")

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.Messages) != 5 {
		t.Fatalf("expected 5 canonical messages, got %#v", canon.Messages)
	}
	roles := make([]string, 0, len(canon.Messages))
	for _, msg := range canon.Messages {
		roles = append(roles, msg.Role)
	}
	if want := []string{"user", "user", "assistant", "tool", "tool"}; !reflect.DeepEqual(roles, want) {
		t.Fatalf("unexpected message roles: got %#v want %#v", roles, want)
	}
	if len(canon.Messages[2].ToolCalls) != 2 {
		t.Fatalf("expected one assistant message with two tool calls, got %#v", canon.Messages)
	}
	if canon.Messages[2].ToolCalls[0].ID != "call_00_QYKD16UQaFTdlwHq7x6I5004" || canon.Messages[2].ToolCalls[1].ID != "call_01_NI7fF0whLahOJEM0DxjG2203" {
		t.Fatalf("expected adjacent tool call order preserved, got %#v", canon.Messages[2].ToolCalls)
	}
	if canon.Messages[3].Role != "tool" || canon.Messages[3].ToolCallID != "call_00_QYKD16UQaFTdlwHq7x6I5004" || canon.Messages[4].Role != "tool" || canon.Messages[4].ToolCallID != "call_01_NI7fF0whLahOJEM0DxjG2203" {
		t.Fatalf("expected adjacent tool results to remain ordered, got %#v", canon.Messages)
	}
	if strings.TrimSpace(canon.Messages[2].ReasoningContent) == "" {
		t.Fatalf("expected first adjacent tool call to retain real reasoning, got %#v", canon.Messages[2])
	}
	for _, item := range canon.ResponseInputItems {
		if got, _ := item["id"].(string); got == "rs_proxy" {
			t.Fatalf("expected proxy reasoning replay to stay out of preserved input items, got %#v", canon.ResponseInputItems)
		}
	}
}

func TestDecodeRequestPreservesToolOrder(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"tools":[
			{"type":"function","name":"workspace_shell","description":"shell","parameters":{"type":"object"}},
			{"type":"function","name":"search_web","description":"search","parameters":{"type":"object"}}
		],
		"input":[{"role":"user","content":"hello"}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %#v", canon.Tools)
	}
	if canon.Tools[0].Name != "workspace_shell" || canon.Tools[1].Name != "search_web" {
		t.Fatalf("expected tool order preserved, got %#v", canon.Tools)
	}
}

func readReplayRequestBody(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replay fixture: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("decode replay row: %v", err)
		}
		body, _ := row["request_body"].(string)
		if body == "" {
			t.Fatalf("replay row missing request_body")
		}
		return body
	}
	t.Fatalf("empty replay fixture")
	return ""
}

func TestDecodeRequestDropsSyntheticReasoningBlockInsideAssistantMessage(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[
			{"role":"assistant","content":[
				{"type":"reasoning","id":"rs_proxy","summary":[{"type":"summary_text","text":"**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长"}]},
				{"type":"output_text","text":"final answer"}
			]}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.ResponseInputItems) != 1 {
		t.Fatalf("expected assistant item preserved without synthetic reasoning, got %#v", canon.ResponseInputItems)
	}
	content, _ := canon.ResponseInputItems[0]["content"].([]map[string]any)
	if len(content) != 1 {
		t.Fatalf("expected only assistant text content preserved, got %#v", canon.ResponseInputItems[0])
	}
	if got, _ := content[0]["type"].(string); got != "output_text" {
		t.Fatalf("expected output_text content, got %#v", content)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].ReasoningBlocks) != 0 {
		t.Fatalf("expected synthetic reasoning block to stay out of canonical messages, got %#v", canon.Messages)
	}
}

func TestDecodeRequestExtractsInstructionInputMessages(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"instructions":"existing instructions",
		"input":[
			{"type":"message","role":"system","content":[{"type":"input_text","text":"system one"}]},
			{"role":"developer","content":[{"type":"text","text":"developer two"}]},
			{"role":"system","content":"system three"},
			{"role":"user","content":[{"type":"input_text","text":"hello"}]}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if canon.Instructions != "system one\n\ndeveloper two\n\nsystem three\n\nexisting instructions" {
		t.Fatalf("expected instruction input messages prepended to instructions, got %q", canon.Instructions)
	}
	if len(canon.ResponseInputItems) != 1 {
		t.Fatalf("expected only user item preserved in ResponseInputItems, got %#v", canon.ResponseInputItems)
	}
	if got, _ := canon.ResponseInputItems[0]["role"].(string); got != "user" {
		t.Fatalf("expected user input item to remain, got %#v", canon.ResponseInputItems[0])
	}
	if len(canon.Messages) != 1 || canon.Messages[0].Role != "user" {
		t.Fatalf("expected only user canonical message to remain, got %#v", canon.Messages)
	}
}

func TestDecodeRequestAcceptsResponsesInputFileContent(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[{"role":"user","content":[{"type":"input_file","input_file":{"file_id":"file_123"}}]}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 1 {
		t.Fatalf("expected one input_file part, got %#v", canon.Messages)
	}
	fileRaw, _ := canon.Messages[0].Parts[0].Raw["input_file"].(map[string]any)
	if got := fileRaw["file_id"]; got != "file_123" {
		t.Fatalf("expected input_file file_id preserved, got %#v", canon.Messages[0].Parts[0])
	}
}

func TestDecodeRequestAcceptsResponsesInputAudioContent(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"YWJj","format":"wav"}}]}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 1 {
		t.Fatalf("expected one input_audio part, got %#v", canon.Messages)
	}
	audioRaw, _ := canon.Messages[0].Parts[0].Raw["input_audio"].(map[string]any)
	if got := audioRaw["format"]; got != "wav" {
		t.Fatalf("expected input_audio format preserved, got %#v", canon.Messages[0].Parts[0])
	}
}

func TestDecodeRequestPreservesInputImageStructuredFields(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"input":[{"role":"user","content":[{"type":"input_image","image_url":{"url":"https://example.com/image.png","detail":"high","file_id":"file_123"}}]}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 1 {
		t.Fatalf("expected one input_image part, got %#v", canon.Messages)
	}
	imageRaw, _ := canon.Messages[0].Parts[0].Raw["image_url"].(map[string]any)
	if got := imageRaw["detail"]; got != "high" {
		t.Fatalf("expected detail preserved, got %#v", canon.Messages[0].Parts[0])
	}
	if got := imageRaw["file_id"]; got != "file_123" {
		t.Fatalf("expected file_id preserved, got %#v", canon.Messages[0].Parts[0])
	}
}

func TestDecodeRequestPreservesPreviousResponseIDMetadataParallelToolCallsTruncationAndText(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"previous_response_id":"resp_123",
		"metadata":{"trace_id":"trace_123","tier":"gold"},
		"parallel_tool_calls":true,
		"truncation":"auto",
		"text":{"format":{"type":"text"}},
		"input":[{"role":"user","content":"hello"}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	preserved := extractPreservedResponsesTopLevelFields(t, canon)
	if got, _ := preserved["previous_response_id"].(string); got != "resp_123" {
		t.Fatalf("expected previous_response_id resp_123, got %#v", preserved)
	}
	metadata, _ := preserved["metadata"].(map[string]any)
	if got, _ := metadata["trace_id"].(string); got != "trace_123" {
		t.Fatalf("expected metadata.trace_id trace_123, got %#v", preserved)
	}
	if got, _ := metadata["tier"].(string); got != "gold" {
		t.Fatalf("expected metadata.tier gold, got %#v", preserved)
	}
	if got, _ := preserved["parallel_tool_calls"].(bool); !got {
		t.Fatalf("expected parallel_tool_calls=true, got %#v", preserved)
	}
	if got, _ := preserved["truncation"].(string); got != "auto" {
		t.Fatalf("expected truncation auto, got %#v", preserved)
	}
	text, _ := preserved["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if got, _ := format["type"].(string); got != "text" {
		t.Fatalf("expected text.format.type text, got %#v", preserved)
	}
}

func TestDecodeRequestPreservesPromptCacheKey(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"prompt_cache_key":"client-session-key",
		"input":[{"role":"user","content":"hello"}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if got, _ := canon.PreservedTopLevelFields["prompt_cache_key"].(string); got != "client-session-key" {
		t.Fatalf("expected prompt_cache_key preserved, got %#v", canon.PreservedTopLevelFields)
	}
}

func TestDecodeRequestPreservesPreviousResponseIDMetadataParallelToolCallsTruncationAndTextThroughUpstreamConstruction(t *testing.T) {
	req := `{
		"model":"gpt-5",
		"previous_response_id":"resp_123",
		"metadata":{"trace_id":"trace_123"},
		"parallel_tool_calls":false,
		"truncation":"auto",
		"text":{"format":{"type":"text"}},
		"input":[{"role":"user","content":"hello"}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, &received); err != nil {
			t.Fatalf("unmarshal request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()

	client := upstream.NewClient(server.URL)
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("client.Response error: %v", err)
	}

	if got, _ := received["previous_response_id"].(string); got != "resp_123" {
		t.Fatalf("expected previous_response_id resp_123 in upstream payload, got %#v", received)
	}
	metadata, _ := received["metadata"].(map[string]any)
	if got, _ := metadata["trace_id"].(string); got != "trace_123" {
		t.Fatalf("expected metadata.trace_id trace_123 in upstream payload, got %#v", received)
	}
	if got, ok := received["parallel_tool_calls"].(bool); !ok || got {
		t.Fatalf("expected parallel_tool_calls=false in upstream payload, got %#v", received)
	}
	if got, _ := received["truncation"].(string); got != "auto" {
		t.Fatalf("expected truncation auto in upstream payload, got %#v", received)
	}
	text, _ := received["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if got, _ := format["type"].(string); got != "text" {
		t.Fatalf("expected text.format.type text in upstream payload, got %#v", received)
	}
	input, _ := received["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected passthrough marker to stay out of upstream input, got %#v", received)
	}
}

func extractPreservedResponsesTopLevelFields(t *testing.T, canon model.CanonicalRequest) map[string]any {
	t.Helper()
	for _, item := range canon.ResponseInputItems {
		if preserved, ok := item[preservedResponsesTopLevelFieldsKey].(map[string]any); ok {
			return preserved
		}
	}
	t.Fatalf("expected preserved top-level responses fields marker, got %#v", canon)
	return nil
}
