package responses

import (
	"strings"
	"testing"
)

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
	if len(canon.Messages) != 1 || canon.Messages[0].Role != "user" {
		t.Fatalf("expected canonical user message to still be decoded, got %#v", canon.Messages)
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
