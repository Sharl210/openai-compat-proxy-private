package anthropic

import (
	"strings"
	"testing"
)

func TestDecodeRequestAcceptsCacheControlWithoutPreservingIt(t *testing.T) {
	req := `{
		"model":"claude-sonnet-4-5",
		"max_tokens":128,
		"cache_control":{"type":"ephemeral","ttl":"5m"},
		"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}]
	}`

	canon, err := DecodeRequest(strings.NewReader(req))
	if err != nil {
		t.Fatalf("DecodeRequest error: %v", err)
	}

	if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 1 {
		t.Fatalf("expected one decoded text message, got %#v", canon.Messages)
	}
	if got := canon.Messages[0].Parts[0].Text; got != "hello" {
		t.Fatalf("expected text hello, got %q", got)
	}
	if canon.Messages[0].Parts[0].Raw != nil {
		t.Fatalf("expected cache_control metadata to be ignored for upstream compatibility, got %#v", canon.Messages[0].Parts[0].Raw)
	}
}
