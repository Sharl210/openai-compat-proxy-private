package anthropic

import (
	"strings"
	"testing"
)

func TestDecodeRequestIgnoresThinkingBlocksInFollowUpMessages(t *testing.T) {
	req := `{
		"model":"claude-sonnet-4-5",
		"max_tokens":1024,
		"messages":[
			{
				"role":"assistant",
				"content":[
					{"type":"thinking","thinking":"internal reasoning","signature":"sig_123"},
					{"type":"text","text":"我刚刚在发呆。"}
				]
			},
			{
				"role":"user",
				"content":"继续说"
			}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 2 {
		t.Fatalf("expected 2 canonical messages, got %#v", canon.Messages)
	}
	if got := canon.Messages[0].Parts[0].Text; got != "我刚刚在发呆。" {
		t.Fatalf("expected assistant text part to survive thinking block, got %#v", canon.Messages[0].Parts)
	}
	if got := canon.Messages[1].Parts[0].Text; got != "继续说" {
		t.Fatalf("expected user follow-up text, got %#v", canon.Messages[1].Parts)
	}
}

func TestDecodeRequestIgnoresRedactedThinkingBlocksInFollowUpMessages(t *testing.T) {
	req := `{
		"model":"claude-sonnet-4-5",
		"max_tokens":1024,
		"messages":[
			{
				"role":"assistant",
				"content":[
					{"type":"redacted_thinking","data":"enc_123"},
					{"type":"text","text":"先这样。"}
				]
			},
			{
				"role":"user",
				"content":"再说一句"
			}
		]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}
	if len(canon.Messages) != 2 {
		t.Fatalf("expected 2 canonical messages, got %#v", canon.Messages)
	}
	if got := canon.Messages[0].Parts[0].Text; got != "先这样。" {
		t.Fatalf("expected assistant text part to survive redacted thinking block, got %#v", canon.Messages[0].Parts)
	}
}
