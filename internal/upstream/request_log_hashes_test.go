package upstream

import (
	"crypto/sha256"
	"encoding"
	"encoding/json"
	"errors"
	"hash"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestHashInputItemsMatchesLegacyJSONHashes(t *testing.T) {
	// Given
	input := []any{
		map[string]any{"content": []any{map[string]any{"text": "first", "type": "input_text"}}, "role": "user"},
		map[string]any{"arguments": `{"path":"/tmp"}`, "call_id": "call-1", "name": "bash", "type": "function_call"},
		map[string]any{"content": []any{map[string]any{"text": "done", "type": "output_text"}}, "role": "assistant"},
	}
	wantItemHashes := make([]string, 0, len(input))
	wantPrefixHashes := make([]string, 0, len(input))
	for index := range input {
		wantItemHashes = append(wantItemHashes, hashAny(input[index]))
		wantPrefixHashes = append(wantPrefixHashes, hashAny(input[:index+1]))
	}

	// When
	itemHashes, prefixHashes := hashInputItems(input)

	// Then
	if !reflect.DeepEqual(itemHashes, wantItemHashes) {
		t.Fatalf("item hashes = %#v, want %#v", itemHashes, wantItemHashes)
	}
	if !reflect.DeepEqual(prefixHashes, wantPrefixHashes) {
		t.Fatalf("prefix hashes = %#v, want %#v", prefixHashes, wantPrefixHashes)
	}
}

func TestUpstreamBodyLogAttrsPreservesLegacyInputHashes(t *testing.T) {
	// Given
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"first"}]},{"type":"function_call","call_id":"call-1","name":"bash","arguments":"{}"}]}`)
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode test body: %v", err)
	}
	input, _ := payload["input"].([]any)
	wantItemHashes, wantPrefixHashes := legacyInputHashes(input)

	// When
	attrs := upstreamBodyLogAttrs(body)

	// Then
	gotItemHashes, _ := attrs["input_item_hashes"].([]string)
	if !reflect.DeepEqual(gotItemHashes, wantItemHashes) {
		t.Fatalf("input item hashes = %#v, want %#v", gotItemHashes, wantItemHashes)
	}
	gotPrefixHashes, _ := attrs["input_prefix_hashes"].([]string)
	if !reflect.DeepEqual(gotPrefixHashes, wantPrefixHashes) {
		t.Fatalf("input prefix hashes = %#v, want %#v", gotPrefixHashes, wantPrefixHashes)
	}
}

func TestHashInputItemsWithHasherFallsBackWithoutStateSnapshot(t *testing.T) {
	// Given
	input := []any{map[string]any{"role": "user", "text": "first"}, map[string]any{"role": "user", "text": "second"}}
	wantItemHashes, wantPrefixHashes := legacyInputHashes(input)

	// When
	itemHashes, prefixHashes := hashInputItemsWithHasher(input, noSnapshotHash{Hash: sha256.New()})

	// Then
	if !reflect.DeepEqual(itemHashes, wantItemHashes) {
		t.Fatalf("item hashes = %#v, want %#v", itemHashes, wantItemHashes)
	}
	if !reflect.DeepEqual(prefixHashes, wantPrefixHashes) {
		t.Fatalf("prefix hashes = %#v, want %#v", prefixHashes, wantPrefixHashes)
	}
}

func TestHashInputItemsWithHasherFallsBackWhenOptimizedPathFails(t *testing.T) {
	input := []any{map[string]any{"role": "user", "text": "first"}, map[string]any{"role": "user", "text": "second"}}
	failure := errors.New("injected hash failure")
	tests := []struct {
		name   string
		hasher hash.Hash
	}{
		{name: "write", hasher: failingWriteSnapshotHash{Hash: sha256.New(), err: failure}},
		{name: "snapshot", hasher: failingSnapshotHash{Hash: sha256.New(), err: failure}},
		{name: "restore", hasher: failingRestoreSnapshotHash{Hash: sha256.New(), err: failure}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			itemHashes, prefixHashes := hashInputItemsWithHasher(input, test.hasher)
			assertLegacyInputHashes(t, input, itemHashes, prefixHashes)
		})
	}
}

func TestHashInputItemsFallsBackWhenItemJSONCannotMarshal(t *testing.T) {
	input := []any{math.Inf(1)}
	itemHashes, prefixHashes := hashInputItems(input)
	assertLegacyInputHashes(t, input, itemHashes, prefixHashes)
}

func BenchmarkHashInputItemsLongInput(b *testing.B) {
	input := make([]any, 128)
	text := strings.Repeat("x", 4<<10)
	for index := range input {
		input[index] = map[string]any{
			"content": []any{map[string]any{"text": text, "type": "input_text"}},
			"role":    "user",
		}
	}

	b.Run("legacy_prefix_marshal", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			_, _ = legacyInputHashes(input)
		}
	})
	b.Run("incremental_hash_state", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			_, _ = hashInputItems(input)
		}
	})
}

func assertLegacyInputHashes(t *testing.T, input []any, itemHashes []string, prefixHashes []string) {
	t.Helper()
	wantItemHashes, wantPrefixHashes := legacyInputHashes(input)
	if !reflect.DeepEqual(itemHashes, wantItemHashes) {
		t.Fatalf("item hashes = %#v, want %#v", itemHashes, wantItemHashes)
	}
	if !reflect.DeepEqual(prefixHashes, wantPrefixHashes) {
		t.Fatalf("prefix hashes = %#v, want %#v", prefixHashes, wantPrefixHashes)
	}
}

type noSnapshotHash struct {
	hash.Hash
}

type failingWriteSnapshotHash struct {
	hash.Hash
	err error
}

func (h failingWriteSnapshotHash) Write([]byte) (int, error) {
	return 0, h.err
}

func (h failingWriteSnapshotHash) MarshalBinary() ([]byte, error) {
	return h.Hash.(encoding.BinaryMarshaler).MarshalBinary()
}

func (h failingWriteSnapshotHash) UnmarshalBinary(data []byte) error {
	return h.Hash.(encoding.BinaryUnmarshaler).UnmarshalBinary(data)
}

type failingSnapshotHash struct {
	hash.Hash
	err error
}

func (h failingSnapshotHash) MarshalBinary() ([]byte, error) {
	return nil, h.err
}

func (h failingSnapshotHash) UnmarshalBinary(data []byte) error {
	return h.Hash.(encoding.BinaryUnmarshaler).UnmarshalBinary(data)
}

type failingRestoreSnapshotHash struct {
	hash.Hash
	err error
}

func (h failingRestoreSnapshotHash) MarshalBinary() ([]byte, error) {
	return h.Hash.(encoding.BinaryMarshaler).MarshalBinary()
}

func (h failingRestoreSnapshotHash) UnmarshalBinary([]byte) error {
	return h.err
}
