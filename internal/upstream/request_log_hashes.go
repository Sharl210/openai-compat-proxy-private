package upstream

import (
	"crypto/sha256"
	"encoding"
	"encoding/json"
	"fmt"
	"hash"
)

type hashStateSnapshot interface {
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
}

func hashInputItems(input []any) ([]string, []string) {
	return hashInputItemsWithHasher(input, sha256.New())
}

func hashInputItemsWithHasher(input []any, prefixHasher hash.Hash) ([]string, []string) {
	itemHashes := make([]string, 0, len(input))
	prefixHashes := make([]string, 0, len(input))
	state, ok := prefixHasher.(hashStateSnapshot)
	if !ok || !writeHashBytes(prefixHasher, []byte{'['}) {
		return legacyInputHashes(input)
	}
	for index := range input {
		encodedItem, err := json.Marshal(input[index])
		if err != nil {
			return legacyInputHashes(input)
		}
		itemHashes = append(itemHashes, hashBytes(encodedItem))
		if index > 0 && !writeHashBytes(prefixHasher, []byte{','}) {
			return legacyInputHashes(input)
		}
		if !writeHashBytes(prefixHasher, encodedItem) {
			return legacyInputHashes(input)
		}
		checkpoint, err := state.MarshalBinary()
		if err != nil || !writeHashBytes(prefixHasher, []byte{']'}) {
			return legacyInputHashes(input)
		}
		sum := prefixHasher.Sum(nil)
		prefixHashes = append(prefixHashes, fmt.Sprintf("%x", sum[:8]))
		if err := state.UnmarshalBinary(checkpoint); err != nil {
			return legacyInputHashes(input)
		}
	}
	return itemHashes, prefixHashes
}

func writeHashBytes(hasher hash.Hash, value []byte) bool {
	_, err := hasher.Write(value)
	return err == nil
}

func legacyInputHashes(input []any) ([]string, []string) {
	itemHashes := make([]string, 0, len(input))
	prefixHashes := make([]string, 0, len(input))
	for index := range input {
		itemHashes = append(itemHashes, hashAny(input[index]))
		prefixHashes = append(prefixHashes, hashAny(input[:index+1]))
	}
	return itemHashes, prefixHashes
}
