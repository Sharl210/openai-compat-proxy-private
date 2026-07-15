package httpapi

import (
	"crypto/sha256"
	"encoding"
	"errors"
	"hash"
	"math"
	"reflect"
	"strings"
	"testing"

	"openai-compat-proxy/internal/model"
)

func TestNestedCachedTokensReadsTopLevelCachedTokens(t *testing.T) {
	usage := map[string]any{"cached_tokens": 321}
	if got := nestedCachedTokens(usage); got != 321 {
		t.Fatalf("expected top-level cached_tokens to be returned, got %#v", got)
	}
}

func TestNestedCachedTokensReadsTopLevelCacheReadInputTokens(t *testing.T) {
	usage := map[string]any{"cache_read_input_tokens": 654}
	if got := nestedCachedTokens(usage); got != 654 {
		t.Fatalf("expected top-level cache_read_input_tokens to be returned, got %#v", got)
	}
}

func TestHashCanonicalMessagePrefixesMatchesLegacyHashes(t *testing.T) {
	tests := []struct {
		name     string
		messages []model.CanonicalMessage
	}{
		{name: "empty", messages: []model.CanonicalMessage{}},
		{name: "nil and empty slices", messages: []model.CanonicalMessage{{Role: "user"}, {Role: "assistant", Parts: []model.CanonicalContentPart{}, ToolCalls: []model.CanonicalToolCall{}, ReasoningBlocks: []map[string]any{}}}},
		{name: "escaped and nested content", messages: []model.CanonicalMessage{{
			Role: "user",
			OrderedContent: []model.CanonicalContentBlock{{Type: "raw", Raw: map[string]any{
				"<tag>": "line\n\"quoted\"\\snowman-☃",
				"empty": []any{},
				"nil":   nil,
			}}},
			Parts:           []model.CanonicalContentPart{{Type: "text", Text: "<&>\u2028", Raw: map[string]any{"z": 1, "a": true}}},
			ReasoningBlocks: []map[string]any{{"type": "thinking", "thinking": "分析"}},
		}}},
		{name: "tool calls and recovery", messages: []model.CanonicalMessage{
			{Role: "assistant", ReasoningContent: "thinking", ToolCalls: []model.CanonicalToolCall{{ID: "call-1", Name: "lookup", Arguments: `{"q":"x"}`}, {ID: "call-2", Name: "fetch", Arguments: `{}`}}, RecoveredToolCall: &model.CanonicalToolCall{ID: "recovered", Name: "bash", Arguments: `{"cmd":"pwd"}`}},
			{Role: "tool", ToolCallID: "call-1", Parts: []model.CanonicalContentPart{{Type: "text", Text: "result"}}},
		}},
		{name: "marshal error", messages: []model.CanonicalMessage{{Role: "user"}, {Role: "assistant", ReasoningBlocks: []map[string]any{{"invalid": math.Inf(1)}}}, {Role: "user"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertLegacyCanonicalMessagePrefixHashes(t, test.messages, hashCanonicalMessagePrefixes(test.messages))
		})
	}
}

func TestHashCanonicalMessagePrefixesWithHasherFallsBackWithoutStateSnapshot(t *testing.T) {
	messages := []model.CanonicalMessage{{Role: "user"}, {Role: "assistant"}}
	got := hashCanonicalMessagePrefixesWithHasher(messages, noCanonicalSnapshotHash{Hash: sha256.New()})
	assertLegacyCanonicalMessagePrefixHashes(t, messages, got)
}

func TestHashCanonicalMessagePrefixesWithHasherFallsBackWhenOptimizedPathFails(t *testing.T) {
	messages := []model.CanonicalMessage{{Role: "user"}, {Role: "assistant"}}
	failure := errors.New("injected hash failure")
	tests := []struct {
		name   string
		hasher hash.Hash
	}{
		{name: "write", hasher: failingCanonicalWriteHash{Hash: sha256.New(), err: failure}},
		{name: "snapshot", hasher: failingCanonicalSnapshotHash{Hash: sha256.New(), err: failure}},
		{name: "restore", hasher: failingCanonicalRestoreHash{Hash: sha256.New(), err: failure}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := hashCanonicalMessagePrefixesWithHasher(messages, test.hasher)
			assertLegacyCanonicalMessagePrefixHashes(t, messages, got)
		})
	}
}

func BenchmarkCanonicalLogAttrsLongConversation(b *testing.B) {
	messages := make([]model.CanonicalMessage, 256)
	for index := range messages {
		messages[index] = model.CanonicalMessage{
			Role:  "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: strings.Repeat("x", 1024)}},
		}
	}
	req := model.CanonicalRequest{Messages: messages}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = canonicalLogAttrs(req)
	}
}

func assertLegacyCanonicalMessagePrefixHashes(t *testing.T, messages []model.CanonicalMessage, got []string) {
	t.Helper()
	want := legacyCanonicalMessagePrefixHashes(messages)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("prefix hashes = %#v, want %#v", got, want)
	}
}

type noCanonicalSnapshotHash struct {
	hash.Hash
}

type failingCanonicalWriteHash struct {
	hash.Hash
	err error
}

func (h failingCanonicalWriteHash) Write([]byte) (int, error) {
	return 0, h.err
}

func (h failingCanonicalWriteHash) MarshalBinary() ([]byte, error) {
	return h.Hash.(encoding.BinaryMarshaler).MarshalBinary()
}

func (h failingCanonicalWriteHash) UnmarshalBinary(data []byte) error {
	return h.Hash.(encoding.BinaryUnmarshaler).UnmarshalBinary(data)
}

type failingCanonicalSnapshotHash struct {
	hash.Hash
	err error
}

func (h failingCanonicalSnapshotHash) MarshalBinary() ([]byte, error) {
	return nil, h.err
}

func (h failingCanonicalSnapshotHash) UnmarshalBinary(data []byte) error {
	return h.Hash.(encoding.BinaryUnmarshaler).UnmarshalBinary(data)
}

type failingCanonicalRestoreHash struct {
	hash.Hash
	err error
}

func (h failingCanonicalRestoreHash) MarshalBinary() ([]byte, error) {
	return h.Hash.(encoding.BinaryMarshaler).MarshalBinary()
}

func (h failingCanonicalRestoreHash) UnmarshalBinary([]byte) error {
	return h.err
}
