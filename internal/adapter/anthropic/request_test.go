package anthropic

import (
	"strings"
	"testing"
)

func TestDecodeRequestPreservesTopLevelCacheControl(t *testing.T) {
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

	if canon.CacheControl == nil {
		t.Fatal("expected top-level cache_control to be preserved")
	}
	if got := canon.CacheControl["type"]; got != "ephemeral" {
		t.Fatalf("expected cache_control.type ephemeral, got %#v", got)
	}
	if got := canon.CacheControl["ttl"]; got != "5m" {
		t.Fatalf("expected cache_control.ttl 5m, got %#v", got)
	}
	if len(canon.Messages) != 1 || len(canon.Messages[0].Parts) != 1 {
		t.Fatalf("expected one decoded text message, got %#v", canon.Messages)
	}
	if got := canon.Messages[0].Parts[0].Text; got != "hello" {
		t.Fatalf("expected text hello, got %q", got)
	}
	if canon.Messages[0].Parts[0].Raw == nil {
		t.Fatal("expected text part raw metadata to be preserved")
	}
	partCache, _ := canon.Messages[0].Parts[0].Raw["cache_control"].(map[string]any)
	if partCache == nil {
		t.Fatal("expected content part cache_control metadata")
	}
	if got := partCache["type"]; got != "ephemeral" {
		t.Fatalf("expected content part cache_control.type ephemeral, got %#v", got)
	}
}
