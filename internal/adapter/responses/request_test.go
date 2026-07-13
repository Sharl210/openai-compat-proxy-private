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

func TestDecodeRequestRecordsBodyReasoningModeWithoutDiscardingUnknownValues(t *testing.T) {
	for _, testCase := range []struct {
		name string
		mode string
	}{
		{name: "known mode", mode: "pro"},
		{name: "unknown mode", mode: "experimental"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// Given
			requestBody := `{"model":"model","reasoning":{"mode":"` + testCase.mode + `","effort":"low","summary":"detailed","vendor_option":"keep"},"input":"hello"}`

			// When
			canon, err := DecodeRequest(strings.NewReader(requestBody))

			// Then
			if err != nil {
				t.Fatalf("DecodeRequest error: %v", err)
			}
			if canon.Reasoning == nil || canon.Reasoning.Raw["mode"] != testCase.mode || canon.Reasoning.Raw["vendor_option"] != "keep" {
				t.Fatalf("expected body reasoning payload preserved, got %#v", canon.Reasoning)
			}
			mode := reflect.ValueOf(canon.Reasoning).Elem().FieldByName("Mode")
			if !mode.IsValid() || mode.Type().Name() != "ReasoningMode" || mode.String() != testCase.mode {
				t.Fatalf("expected typed mode %q, got %#v", testCase.mode, canon.Reasoning)
			}
			origin := reflect.ValueOf(canon).FieldByName("ReasoningModeOrigin")
			if !origin.IsValid() || origin.Type().Name() != "ReasoningModeOrigin" || origin.String() != "body" {
				t.Fatalf("expected body mode origin, got %#v", canon)
			}
		})
	}
}

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

func TestDecodeRequestSerializesStringInputThroughCanonicalResponsesPath(t *testing.T) {
	// Given
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()

	// When
	canon, err := DecodeRequest(strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	client := upstream.NewClient(server.URL)
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("client.Response error: %v", err)
	}

	// Then
	input, _ := received["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected one canonical upstream input item, got %#v", received)
	}
	item, _ := input[0].(map[string]any)
	content, _ := item["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected canonical content array for string input, got %#v", item)
	}
	part, _ := content[0].(map[string]any)
	if got, _ := part["type"].(string); got != "input_text" {
		t.Fatalf("expected canonical input_text part, got %#v", part)
	}
	if got, _ := part["text"].(string); got != "hello" {
		t.Fatalf("expected canonical hello text, got %#v", part)
	}
	if canon.ResponseInputItemsAreOriginal {
		t.Fatalf("expected synthesized string input not to be marked original")
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

func TestDecodeRequestPreservesParallelToolCallsTriState(t *testing.T) {
	trueValue, falseValue := true, false
	for _, tc := range []struct {
		name string
		body string
		want *bool
	}{
		{name: "unspecified", body: `{"model":"gpt-5.6","input":"hello"}`},
		{name: "allowed", body: `{"model":"gpt-5.6","input":"hello","parallel_tool_calls":true}`, want: &trueValue},
		{name: "disabled", body: `{"model":"gpt-5.6","input":"hello","parallel_tool_calls":false}`, want: &falseValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			canon, err := DecodeRequest(strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("DecodeRequest error: %v", err)
			}
			if !reflect.DeepEqual(canon.ParallelToolCalls, tc.want) {
				t.Fatalf("expected ParallelToolCalls %#v, got %#v", tc.want, canon.ParallelToolCalls)
			}
		})
	}
}

func TestDecodeRequestNormalizesRequiredToolChoice(t *testing.T) {
	canon, err := DecodeRequest(strings.NewReader(`{"model":"gpt-5.6","input":"hello","tool_choice":"required"}`))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if got := string(canon.ToolChoice.Requirement); got != "required_any" {
		t.Fatalf("expected required_any tool choice, got %#v", canon.ToolChoice)
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
	if !canon.ResponseInputItemsAreOriginal {
		t.Fatalf("expected typed Responses input array to retain raw-first origin")
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

func TestDecodeRequestPreservesResponsesItemGraphThroughResponsesUpstreamWhenCanonicalMessagesExist(t *testing.T) {
	// Given
	requestBody := `{
		"model":"gpt-5.6",
		"previous_response_id":"resp_previous",
		"include":["reasoning.encrypted_content"],
		"input":[
			{"type":"message","id":"msg_user","role":"user","phase":"analysis","content":[{"type":"input_text","text":"continue","annotation":{"source":"client"}}],"vendor_message":{"opaque":"keep"}},
			{"type":"reasoning","id":"rs_proxy","phase":"analysis","context":"ctx_opaque","encrypted_content":"enc_real","vendor_reasoning":{"opaque":"keep"}},
			{"type":"compaction","id":"cmp_1","phase":"compaction","context":"compact_ctx","encrypted_content":"enc_compact","vendor_compaction":{"opaque":"keep"}},
			{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"README.md\"}","phase":"analysis","vendor_call":{"opaque":"keep"}},
			{"type":"function_call_output","id":"fco_1","call_id":"call_1","output":"{\"ok\":true}","phase":"analysis","vendor_output":{"opaque":"keep"}},
			{"type":"item_reference","id":"item_prior","phase":"analysis","vendor_reference":{"opaque":"keep"}},
			{"type":"vendor_future_item","id":"future_1","phase":"analysis","payload":{"opaque":"keep"}}
		]
	}`
	var original map[string]any
	if err := json.Unmarshal([]byte(requestBody), &original); err != nil {
		t.Fatalf("unmarshal original request body: %v", err)
	}

	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if r.URL.Path != "/responses" {
			t.Fatalf("expected Responses upstream path, got %q", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream request body: %v", err)
		}
		if err := json.Unmarshal(body, &received); err != nil {
			t.Fatalf("unmarshal upstream request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()

	// When
	canon, err := DecodeRequest(strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) < 3 || canon.Messages[0].Role != "user" {
		t.Fatalf("expected canonical sidecar to remain available, got %#v", canon.Messages)
	}
	client := upstream.NewClient(server.URL)
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("client.Response error: %v", err)
	}

	// Then
	wantInput, _ := original["input"].([]any)
	gotInput, _ := received["input"].([]any)
	if !reflect.DeepEqual(gotInput, wantInput) {
		t.Fatalf("expected original Responses item graph and order upstream, got %#v want %#v", gotInput, wantInput)
	}
	if got, _ := received["previous_response_id"].(string); got != "resp_previous" {
		t.Fatalf("expected previous_response_id preserved upstream, got %#v", received)
	}
	if !reflect.DeepEqual(received["include"], original["include"]) {
		t.Fatalf("expected reasoning.encrypted_content include preserved upstream, got %#v", received)
	}
}

func TestDecodeRequestRejectsMalformedResponsesItemGraphFields(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
	}{
		{
			name: "item reference missing id",
			body: `{"model":"gpt-5","input":[{"type":"item_reference"}]}`,
		},
		{
			name: "item reference has empty id",
			body: `{"model":"gpt-5","input":[{"type":"item_reference","id":""}]}`,
		},
		{
			name: "item reference has legacy item id only",
			body: `{"model":"gpt-5","input":[{"type":"item_reference","item_id":"item_prior"}]}`,
		},
		{
			name: "duplicate function call id",
			body: `{"model":"gpt-5","input":[{"type":"function_call","id":"fc_1","call_id":"call_123","name":"search","arguments":"{}"},{"type":"function_call","id":"fc_2","call_id":"call_123","name":"fetch","arguments":"{}"}]}`,
		},
		{
			name: "function call output missing call id",
			body: `{"model":"gpt-5","input":[{"type":"function_call_output","output":"ok"}]}`,
		},
		{
			name: "function call output has empty call id",
			body: `{"model":"gpt-5","input":[{"type":"function_call_output","call_id":"","output":"ok"}]}`,
		},
		{
			name: "function call output has oversized call id",
			body: `{"model":"gpt-5","input":[{"type":"function_call_output","call_id":"` + strings.Repeat("a", 65) + `","output":"ok"}]}`,
		},
		{
			name: "function call output missing output",
			body: `{"model":"gpt-5","input":[{"type":"function_call_output","call_id":"call_123"}]}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			// Given
			requestBody := testCase.body

			// When
			_, err := DecodeRequest(strings.NewReader(requestBody))

			// Then
			if err == nil {
				t.Fatalf("expected malformed Responses item graph to be rejected")
			}
		})
	}
}

func TestDecodeRequestAllowsPersistedResponsesItemReferencesOutsideCurrentInput(t *testing.T) {
	// Given
	requestBody := `{
		"model":"gpt-5",
		"previous_response_id":"resp_prior",
		"input":[
			{"type":"item_reference","id":"item_prior"},
			{"type":"function_call_output","call_id":"call_prior","output":"ok"}
		]
	}`

	// When
	canon, err := DecodeRequest(strings.NewReader(requestBody))

	// Then
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	var reference, output map[string]any
	for _, item := range canon.ResponseInputItems {
		switch item["type"] {
		case "item_reference":
			reference = item
		case "function_call_output":
			output = item
		}
	}
	if got, _ := reference["id"].(string); got != "item_prior" {
		t.Fatalf("expected item_reference id preserved, got %#v", reference)
	}
	if got, _ := output["call_id"].(string); got != "call_prior" {
		t.Fatalf("expected persisted function_call_output preserved, got %#v", output)
	}
	if len(canon.Messages) != 1 || canon.Messages[0].ToolCallID != "call_prior" {
		t.Fatalf("expected persisted function_call_output accepted without a current definition, got %#v", canon.Messages)
	}
}

func TestDecodeRequestPreservesOfficialCodexToolShapes(t *testing.T) {
	req := `{
		"model":"gpt-5.5",
		"stream":true,
		"tools":[
			{
				"type":"function",
				"name":"shell_command",
				"description":"Run a shell command",
				"strict":true,
				"parameters":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"],"additionalProperties":false}
			},
			{
				"type":"custom",
				"name":"apply_patch",
				"description":"Apply a patch",
				"format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}
			},
			{
				"type":"namespace",
				"name":"mcp__node_repl",
				"description":"Node REPL tools",
				"tools":[{"type":"function","name":"execute","description":"Execute code","parameters":{"type":"object","properties":{"code":{"type":"string"}},"required":["code"],"additionalProperties":false}}]
			},
			{
				"type":"tool_search",
				"description":"Search installed tools",
				"execution":{"type":"server"},
				"parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}
			},
			{
				"type":"web_search",
				"external_web_access":true,
				"search_content_types":["webpage"]
			}
		],
		"input":[{"role":"user","content":"hello"}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Tools) != 5 {
		t.Fatalf("expected 5 tools, got %#v", canon.Tools)
	}

	functionTool := canon.Tools[0]
	if got, _ := functionTool.Raw["strict"].(bool); !got {
		t.Fatalf("expected function tool strict=true to survive, got %#v", functionTool.Raw)
	}

	customTool := canon.Tools[1]
	format, _ := customTool.Raw["format"].(map[string]any)
	if got, _ := format["type"].(string); got != "grammar" {
		t.Fatalf("expected custom tool format grammar to survive, got %#v", customTool.Raw)
	}

	namespaceTool := canon.Tools[2]
	nestedTools, _ := namespaceTool.Raw["tools"].([]any)
	if len(nestedTools) != 1 {
		t.Fatalf("expected namespace nested tools to survive, got %#v", namespaceTool.Raw)
	}

	toolSearch := canon.Tools[3]
	if got, _ := toolSearch.Raw["name"].(string); got != "" {
		t.Fatalf("expected tool_search not to synthesize a name, got %#v", toolSearch.Raw)
	}
	execution, _ := toolSearch.Raw["execution"].(map[string]any)
	if got, _ := execution["type"].(string); got != "server" {
		t.Fatalf("expected tool_search execution to survive, got %#v", toolSearch.Raw)
	}

	webSearch := canon.Tools[4]
	if _, exists := webSearch.Raw["name"]; exists {
		t.Fatalf("expected web_search without name to remain nameless, got %#v", webSearch.Raw)
	}
	if got, _ := webSearch.Raw["external_web_access"].(bool); !got {
		t.Fatalf("expected web_search external_web_access to survive, got %#v", webSearch.Raw)
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

func TestDecodeRequestPreservesAssistantMessageInputShape(t *testing.T) {
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
	if got, _ := canon.ResponseInputItems[1]["content"].(string); got != "第二句" {
		t.Fatalf("expected assistant string content preserved without rewriting, got %#v", canon.ResponseInputItems[1])
	}
}

func TestDecodeRequestPreservesAssistantStructuredMessageInputShape(t *testing.T) {
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

	assistant, _ := canon.ResponseInputItems[0]["content"].([]any)
	if len(assistant) != 1 {
		t.Fatalf("expected one assistant structured content item, got %#v", canon.ResponseInputItems[0])
	}
	part, _ := assistant[0].(map[string]any)
	if got, _ := part["type"].(string); got != "text" {
		t.Fatalf("expected assistant structured content type text preserved without rewriting, got %#v", canon.ResponseInputItems[0])
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

	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()

	client := upstream.NewClient(server.URL)
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("client.Response error: %v", err)
	}
	for _, raw := range received["input"].([]any) {
		item, _ := raw.(map[string]any)
		if item["id"] == "rs_proxy" {
			t.Fatalf("expected synthetic rs_proxy reasoning item omitted from upstream payload, got %#v", received["input"])
		}
	}
}

func TestDecodeRequestSerializesLegacyRoleToolMessageThroughCanonicalResponsesPath(t *testing.T) {
	// Given
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()

	// When
	canon, err := DecodeRequest(strings.NewReader(`{
		"model":"gpt-5",
		"input":[{"role":"tool","tool_call_id":"call_123","content":"{\"temperature\":26}"}]
	}`))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	client := upstream.NewClient(server.URL)
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("client.Response error: %v", err)
	}

	// Then
	if canon.ResponseInputItemsAreOriginal {
		t.Fatalf("expected legacy role tool message to disable raw-first forwarding")
	}
	input, _ := received["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected one canonical tool output item, got %#v", received)
	}
	item, _ := input[0].(map[string]any)
	if item["type"] != "function_call_output" || item["call_id"] != "call_123" || item["output"] != `{"temperature":26}` {
		t.Fatalf("expected canonical function_call_output, got %#v", item)
	}
}

func TestDecodeRequestSerializesLegacyAssistantToolCallsAndRoleToolResultThroughCanonicalResponsesPath(t *testing.T) {
	// Given
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()

	// When
	canon, err := DecodeRequest(strings.NewReader(`{
		"model":"gpt-5",
		"input":[
			{"role":"assistant","content":[],"tool_calls":[{"id":"call_123","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Beijing\"}"}}]},
			{"role":"tool","tool_call_id":"call_123","content":"{\"temperature\":26}"}
		]
	}`))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	client := upstream.NewClient(server.URL)
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("client.Response error: %v", err)
	}

	// Then
	if canon.ResponseInputItemsAreOriginal {
		t.Fatalf("expected legacy Chat tool sequence to disable raw-first forwarding")
	}
	input, _ := received["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("expected canonical function call and result, got %#v", received)
	}
	call, _ := input[0].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "call_123" || call["name"] != "weather" {
		t.Fatalf("expected canonical function_call, got %#v", call)
	}
	output, _ := input[1].(map[string]any)
	if output["type"] != "function_call_output" || output["call_id"] != "call_123" || output["output"] != `{"temperature":26}` {
		t.Fatalf("expected canonical function_call_output, got %#v", output)
	}
}

func TestDecodeRequestReplaysLegacyToolSequenceWithOpaqueProxyReasoningState(t *testing.T) {
	// Given
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()

	const summary = "**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长"
	const requestBody = `{
		"model":"gpt-5",
		"input":[
			{"role":"user","content":"查北京天气"},
			{"type":"reasoning","id":"rs_proxy","summary":[{"type":"summary_text","text":"**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长"}],"encrypted_content":"enc_opaque","context":"ctx_opaque","phase":"analysis","vendor_reasoning":{"opaque":"keep"}},
			{"role":"assistant","content":[],"tool_calls":[{"id":"call_123","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Beijing\"}"}}]},
			{"role":"tool","tool_call_id":"call_123","content":"{\"temperature\":26}"}
		]
	}`

	// When
	canon, err := DecodeRequest(strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	client := upstream.NewClient(server.URL)
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("client.Response error: %v", err)
	}

	// Then
	if canon.ResponseInputItemsAreOriginal {
		t.Fatalf("expected legacy Chat tool sequence to disable raw-first forwarding")
	}
	input, _ := received["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("expected user, real reasoning, function call, and result, got %#v", received)
	}
	reasoning, _ := input[1].(map[string]any)
	if reasoning["type"] != "reasoning" || reasoning["id"] != "rs_proxy" || reasoning["encrypted_content"] != "enc_opaque" || reasoning["context"] != "ctx_opaque" || reasoning["phase"] != "analysis" {
		t.Fatalf("expected real opaque rs_proxy reasoning preserved, got %#v", reasoning)
	}
	summaryItems, _ := reasoning["summary"].([]any)
	if len(summaryItems) != 1 {
		t.Fatalf("expected placeholder-looking summary preserved, got %#v", reasoning)
	}
	summaryItem, _ := summaryItems[0].(map[string]any)
	if summaryItem["type"] != "summary_text" || summaryItem["text"] != summary {
		t.Fatalf("expected placeholder-looking summary preserved, got %#v", reasoning)
	}
	vendor, _ := reasoning["vendor_reasoning"].(map[string]any)
	if vendor["opaque"] != "keep" {
		t.Fatalf("expected opaque vendor reasoning state preserved, got %#v", reasoning)
	}
	call, _ := input[2].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "call_123" || call["name"] != "weather" {
		t.Fatalf("expected canonical function_call after reasoning, got %#v", call)
	}
	output, _ := input[3].(map[string]any)
	if output["type"] != "function_call_output" || output["call_id"] != "call_123" || output["output"] != `{"temperature":26}` {
		t.Fatalf("expected canonical function_call_output, got %#v", output)
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
	foundRealReasoning := false
	for _, item := range canon.ResponseInputItems {
		if got, _ := item["id"].(string); got == "rs_proxy" {
			foundRealReasoning = true
		}
	}
	if !foundRealReasoning {
		t.Fatalf("expected real rs_proxy reasoning item preserved in raw input graph, got %#v", canon.ResponseInputItems)
	}
}

func TestDecodeRequestPreservesProxyReasoningStateWithoutSummaryOrEncryption(t *testing.T) {
	// Given
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()

	const requestBody = `{
		"model":"gpt-5",
		"input":[
			{"role":"user","content":"continue"},
			{"type":"reasoning","id":"rs_proxy","context":"ctx_opaque","phase":"analysis","vendor_reasoning":{"opaque":"keep"}}
		]
	}`

	// When
	canon, err := DecodeRequest(strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	client := upstream.NewClient(server.URL)
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("client.Response error: %v", err)
	}

	// Then
	input, _ := received["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("expected real rs_proxy reasoning item upstream, got %#v", received)
	}
	reasoning, _ := input[1].(map[string]any)
	if reasoning["context"] != "ctx_opaque" || reasoning["phase"] != "analysis" {
		t.Fatalf("expected real reasoning state preserved, got %#v", reasoning)
	}
	vendor, _ := reasoning["vendor_reasoning"].(map[string]any)
	if vendor["opaque"] != "keep" {
		t.Fatalf("expected opaque vendor state preserved, got %#v", reasoning)
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
	foundRealReasoning := false
	for _, item := range canon.ResponseInputItems {
		if got, _ := item["id"].(string); got == "rs_proxy" {
			foundRealReasoning = true
		}
	}
	if !foundRealReasoning {
		t.Fatalf("expected real rs_proxy reasoning replay preserved in raw input graph, got %#v", canon.ResponseInputItems)
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
	content, _ := canon.ResponseInputItems[0]["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected only assistant text content preserved, got %#v", canon.ResponseInputItems[0])
	}
	part, _ := content[0].(map[string]any)
	if got, _ := part["type"].(string); got != "output_text" {
		t.Fatalf("expected output_text content, got %#v", content)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].ReasoningBlocks) != 0 {
		t.Fatalf("expected synthetic reasoning block to stay out of canonical messages, got %#v", canon.Messages)
	}
}

func TestDecodeRequestPreservesNonReasoningRSProxyIDInsideAssistantMessage(t *testing.T) {
	// Given
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()

	// When
	canon, err := DecodeRequest(strings.NewReader(`{
		"model":"gpt-5",
		"input":[{"role":"assistant","content":[{"type":"output_text","id":"rs_proxy","text":"final answer"}]}]
	}`))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	client := upstream.NewClient(server.URL)
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("client.Response error: %v", err)
	}

	// Then
	input, _ := received["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected assistant message upstream, got %#v", received)
	}
	item, _ := input[0].(map[string]any)
	content, _ := item["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected non-reasoning rs_proxy item preserved, got %#v", item)
	}
	part, _ := content[0].(map[string]any)
	if part["type"] != "output_text" || part["id"] != "rs_proxy" || part["text"] != "final answer" {
		t.Fatalf("expected opaque non-reasoning item unchanged, got %#v", part)
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
	if _, exists := canon.PreservedTopLevelFields["prompt_cache_key"]; exists {
		t.Fatalf("expected prompt_cache_key to stay out of generic passthrough, got %#v", canon.PreservedTopLevelFields)
	}
	if got := string(canon.ResponsePromptCacheKey); got != `"client-session-key"` {
		t.Fatalf("expected typed prompt_cache_key preserved, got %q", got)
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

func TestDecodeRequestForwardsTypedPromptCacheControlsWithoutGenericDuplicates(t *testing.T) {
	// Given
	const requestBody = `{
		"model":"gpt-5.6",
		"prompt_cache_key":"client-session-key",
		"prompt_cache_options":{"mode":"explicit","vendor_option":{"scope":"tenant-a"}},
		"vendor_top_level":{"keep":"yes"},
		"input":[{"role":"user","content":"hello"}]
	}`
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()

	// When
	canon, err := DecodeRequest(strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if _, exists := canon.PreservedTopLevelFields["prompt_cache_key"]; exists {
		t.Fatalf("expected prompt_cache_key to be typed instead of generic passthrough, got %#v", canon.PreservedTopLevelFields)
	}
	if _, exists := canon.PreservedTopLevelFields["prompt_cache_options"]; exists {
		t.Fatalf("expected prompt_cache_options to be typed instead of generic passthrough, got %#v", canon.PreservedTopLevelFields)
	}
	if got, _ := canon.PreservedTopLevelFields["vendor_top_level"].(map[string]any); got["keep"] != "yes" {
		t.Fatalf("expected unknown top-level field to remain generic, got %#v", canon.PreservedTopLevelFields)
	}
	if got := string(canon.ResponsePromptCacheKey); got != `"client-session-key"` {
		t.Fatalf("expected typed prompt_cache_key raw value, got %q", got)
	}
	if got := string(canon.ResponsePromptCacheOptions); got != `{"mode":"explicit","vendor_option":{"scope":"tenant-a"}}` {
		t.Fatalf("expected typed prompt_cache_options raw value, got %q", got)
	}
	client := upstream.NewClient(server.URL)
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("client.Response error: %v", err)
	}

	// Then
	if got, _ := received["prompt_cache_key"].(string); got != "client-session-key" {
		t.Fatalf("expected explicit prompt_cache_key to override automatic generation, got %#v", received)
	}
	options, _ := received["prompt_cache_options"].(map[string]any)
	if got, _ := options["mode"].(string); got != "explicit" {
		t.Fatalf("expected prompt_cache_options.mode preserved, got %#v", received)
	}
	vendorOption, _ := options["vendor_option"].(map[string]any)
	if got, _ := vendorOption["scope"].(string); got != "tenant-a" {
		t.Fatalf("expected nested prompt_cache_options preserved, got %#v", received)
	}
}

func TestDecodeRequestGeneratesStablePromptCacheKeyForSameOrderedRawItemGraph(t *testing.T) {
	// Given
	var received []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		received = append(received, payload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()
	client := upstream.NewClient(server.URL)
	requestBodies := []string{
		`{"model":"gpt-5.6","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"stable prefix"}],"vendor_state":{"position":1}},{"type":"message","role":"user","content":[{"type":"input_text","text":"dynamic suffix"}],"vendor_state":{"position":2}}]}`,
		`{"model":"gpt-5.6","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"stable prefix"}],"vendor_state":{"position":1}},{"type":"message","role":"user","content":[{"type":"input_text","text":"dynamic suffix"}],"vendor_state":{"position":2}}]}`,
		`{"model":"gpt-5.6","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"dynamic suffix"}],"vendor_state":{"position":2}},{"type":"message","role":"user","content":[{"type":"input_text","text":"stable prefix"}],"vendor_state":{"position":1}}]}`,
	}

	// When
	for _, requestBody := range requestBodies {
		canon, err := DecodeRequest(strings.NewReader(requestBody))
		if err != nil {
			t.Fatalf("DecodeRequest error: %v", err)
		}
		if !canon.ResponseInputItemsAreOriginal {
			t.Fatalf("expected raw Responses item graph to remain original")
		}
		if _, err := client.Response(context.Background(), canon, ""); err != nil {
			t.Fatalf("client.Response error: %v", err)
		}
	}

	// Then
	if len(received) != 3 {
		t.Fatalf("expected three upstream requests, got %d", len(received))
	}
	firstKey, _ := received[0]["prompt_cache_key"].(string)
	secondKey, _ := received[1]["prompt_cache_key"].(string)
	thirdKey, _ := received[2]["prompt_cache_key"].(string)
	if firstKey == "" || secondKey == "" || thirdKey == "" {
		t.Fatalf("expected automatic prompt_cache_key values, got %#v", received)
	}
	if firstKey != secondKey {
		t.Fatalf("expected same ordered raw graph to produce stable key, got %q and %q", firstKey, secondKey)
	}
	if firstKey == thirdKey {
		t.Fatalf("expected reordered raw graph to produce a distinct key, got %q", firstKey)
	}
	if _, exists := received[0]["prompt_cache_options"]; exists {
		t.Fatalf("expected no prompt_cache_options manufacture, got %#v", received[0])
	}
}

func TestDecodeRequestKeepsAutomaticPromptCacheKeyStableWhenOnlyItemIDsChange(t *testing.T) {
	// Given
	var received []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		received = append(received, payload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()
	client := upstream.NewClient(server.URL)
	requestBodies := []string{
		`{"model":"gpt-5.6","input":[{"type":"message","id":"msg_first","role":"user","content":[{"type":"input_text","text":"stable prefix"}]},{"type":"function_call","id":"fc_first","call_id":"call_first","name":"lookup","arguments":"{}"},{"type":"function_call_output","id":"fco_first","call_id":"call_first","output":"{}"}]}`,
		`{"model":"gpt-5.6","input":[{"type":"message","id":"msg_second","role":"user","content":[{"type":"input_text","text":"stable prefix"}]},{"type":"function_call","id":"fc_second","call_id":"call_second","name":"lookup","arguments":"{}"},{"type":"function_call_output","id":"fco_second","call_id":"call_second","output":"{}"}]}`,
	}

	// When
	for _, requestBody := range requestBodies {
		canon, err := DecodeRequest(strings.NewReader(requestBody))
		if err != nil {
			t.Fatalf("DecodeRequest error: %v", err)
		}
		if _, err := client.Response(context.Background(), canon, ""); err != nil {
			t.Fatalf("client.Response error: %v", err)
		}
	}

	// Then
	if len(received) != 2 {
		t.Fatalf("expected two upstream requests, got %d", len(received))
	}
	firstKey, _ := received[0]["prompt_cache_key"].(string)
	secondKey, _ := received[1]["prompt_cache_key"].(string)
	if firstKey == "" || secondKey == "" || firstKey != secondKey {
		t.Fatalf("expected dynamic item IDs not to affect automatic prompt_cache_key, got %q and %q", firstKey, secondKey)
	}
	firstInput, _ := received[0]["input"].([]any)
	secondInput, _ := received[1]["input"].([]any)
	if firstInput[0].(map[string]any)["id"] != "msg_first" || secondInput[0].(map[string]any)["id"] != "msg_second" {
		t.Fatalf("expected upstream input item IDs to remain unmodified, got %#v %#v", firstInput, secondInput)
	}
}

func TestDecodeRequestPreservesInputImageDetailVariantsThroughResponsesUpstream(t *testing.T) {
	// Given
	const requestBody = `{
		"model":"gpt-5.6",
		"input":[{"role":"user","content":[
			{"type":"input_image","image_url":{"url":"https://example.com/omitted.png"}},
			{"type":"input_image","image_url":{"url":"https://example.com/auto.png","detail":"auto"}},
			{"type":"input_image","image_url":{"file_id":"file_original","detail":"original"}},
			{"type":"input_image","image_url":{"url":"https://example.com/low.png","detail":"low","vendor_image":"keep"}},
			{"type":"input_image","image_url":{"file_id":"file_high","detail":"high"}}
		]}]
	}`
	var original map[string]any
	if err := json.Unmarshal([]byte(requestBody), &original); err != nil {
		t.Fatalf("unmarshal original request: %v", err)
	}
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()

	// When
	canon, err := DecodeRequest(strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 5 {
		t.Fatalf("expected five canonical image parts, got %#v", canon.Messages)
	}
	for index, want := range []struct {
		detail    string
		hasDetail bool
		url       string
		fileID    string
	}{
		{url: "https://example.com/omitted.png"},
		{detail: "auto", hasDetail: true, url: "https://example.com/auto.png"},
		{detail: "original", hasDetail: true, fileID: "file_original"},
		{detail: "low", hasDetail: true, url: "https://example.com/low.png"},
		{detail: "high", hasDetail: true, fileID: "file_high"},
	} {
		imageRaw, _ := canon.Messages[0].Parts[index].Raw["image_url"].(map[string]any)
		_, hasDetail := imageRaw["detail"]
		if hasDetail != want.hasDetail || (want.hasDetail && imageRaw["detail"] != want.detail) {
			t.Fatalf("expected image detail at index %d to preserve presence/value, got %#v", index, imageRaw)
		}
		if want.url != "" && imageRaw["url"] != want.url {
			t.Fatalf("expected image URL at index %d preserved, got %#v", index, imageRaw)
		}
		if want.fileID != "" && imageRaw["file_id"] != want.fileID {
			t.Fatalf("expected image file_id at index %d preserved, got %#v", index, imageRaw)
		}
	}
	client := upstream.NewClient(server.URL)
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("client.Response error: %v", err)
	}

	// Then
	wantInput, _ := original["input"].([]any)
	gotInput, _ := received["input"].([]any)
	if !reflect.DeepEqual(gotInput, wantInput) {
		t.Fatalf("expected image URL/file_id, detail variants, and raw image fields preserved, got %#v want %#v", gotInput, wantInput)
	}
}

func TestDecodeRequestSeparatesMultiAgentFromGenericPassthrough(t *testing.T) {
	// Given
	const requestBody = `{
		"model":"gpt-5.6",
		"multi_agent":{"enabled":true,"vendor_option":{"team":"research"}},
		"vendor_top_level":{"keep":"yes"},
		"input":"hello"
	}`

	// When
	canon, err := DecodeRequest(strings.NewReader(requestBody))

	// Then
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if got := string(canon.ResponseMultiAgent); got != `{"enabled":true,"vendor_option":{"team":"research"}}` {
		t.Fatalf("expected typed multi_agent raw payload, got %q", got)
	}
	if _, exists := canon.PreservedTopLevelFields["multi_agent"]; exists {
		t.Fatalf("expected multi_agent to stay out of generic passthrough, got %#v", canon.PreservedTopLevelFields)
	}
	if got, _ := canon.PreservedTopLevelFields["vendor_top_level"].(map[string]any); got["keep"] != "yes" {
		t.Fatalf("expected unknown top-level field to remain generic, got %#v", canon.PreservedTopLevelFields)
	}
}

func TestDecodeRequestPreservesProgrammaticToolAndProgramItemsThroughResponsesUpstream(t *testing.T) {
	// Given
	const requestBody = `{
		"model":"gpt-5.6",
		"multi_agent":{"enabled":true},
		"tools":[{"type":"programmatic_tool_calling","allowed_callers":[{"type":"agent","name":"researcher"}],"vendor_tool":"keep"}],
		"input":[
			{"type":"program","id":"prog_1","code":"print('hello')","caller":"researcher"},
			{"type":"program_output","id":"prog_out_1","program_id":"prog_1","output":"hello","vendor_output":{"keep":true}}
		]
	}`
	var original map[string]any
	if err := json.Unmarshal([]byte(requestBody), &original); err != nil {
		t.Fatalf("unmarshal original request: %v", err)
	}
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode upstream request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_ok"}`))
	}))
	defer server.Close()

	// When
	canon, err := DecodeRequest(strings.NewReader(requestBody))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	client := upstream.NewClient(server.URL)
	if _, err := client.Response(context.Background(), canon, ""); err != nil {
		t.Fatalf("client.Response error: %v", err)
	}

	// Then
	wantTools, _ := original["tools"].([]any)
	gotTools, _ := received["tools"].([]any)
	if !reflect.DeepEqual(gotTools, wantTools) {
		t.Fatalf("expected programmatic tool payload preserved, got %#v want %#v", gotTools, wantTools)
	}
	wantInput, _ := original["input"].([]any)
	gotInput, _ := received["input"].([]any)
	if !reflect.DeepEqual(gotInput, wantInput) {
		t.Fatalf("expected program and program_output items preserved, got %#v want %#v", gotInput, wantInput)
	}
}
