package perfbench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
)

type loopbackObservation struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func verifyLoopbackObservation(response []byte, expectedBytes int64, expectedHash string) (loopbackObservation, error) {
	decoder := json.NewDecoder(bytes.NewReader(response))
	decoder.DisallowUnknownFields()
	var observation loopbackObservation
	if err := decoder.Decode(&observation); err != nil {
		return loopbackObservation{}, fmt.Errorf("decode loopback observation: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return loopbackObservation{}, errors.New("loopback observation contains trailing data")
	}
	if observation.Bytes != expectedBytes || observation.SHA256 != expectedHash {
		return loopbackObservation{}, fmt.Errorf(
			"loopback observed bytes/hash %d/%s, want %d/%s",
			observation.Bytes, observation.SHA256, expectedBytes, expectedHash,
		)
	}
	return observation, nil
}

func TestLoopbackObservation_requires_server_byte_and_hash_equality(t *testing.T) {
	// Given
	const expectedBytes = int64(123)
	const expectedHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	valid := []byte(fmt.Sprintf(`{"bytes":%d,"sha256":"%s"}`, expectedBytes, expectedHash))

	// When
	observation, err := verifyLoopbackObservation(valid, expectedBytes, expectedHash)

	// Then
	if err != nil {
		t.Fatalf("verify valid observation: %v", err)
	}
	if observation.Bytes != expectedBytes || observation.SHA256 != expectedHash {
		t.Fatalf("observation = %+v", observation)
	}
	for name, response := range map[string][]byte{
		"bytes": []byte(fmt.Sprintf(`{"bytes":%d,"sha256":"%s"}`, expectedBytes+1, expectedHash)),
		"hash":  []byte(fmt.Sprintf(`{"bytes":%d,"sha256":"%064d"}`, expectedBytes, 0)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifyLoopbackObservation(response, expectedBytes, expectedHash); err == nil {
				t.Fatal("mismatched server observation was accepted")
			}
		})
	}
}
