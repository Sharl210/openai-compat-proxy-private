package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

func DefaultClaudeCodeMetadataDeviceID(seed string) string {
	trimmedSeed := strings.TrimSpace(seed)
	if trimmedSeed == "" {
		trimmedSeed = "root"
	}
	deviceHash := sha256.Sum256([]byte("claude-code-metadata-device:" + trimmedSeed))
	return hex.EncodeToString(deviceHash[:])
}

func DefaultClaudeCodeMetadataAccountUUID(seed string) string {
	trimmedSeed := strings.TrimSpace(seed)
	if trimmedSeed == "" {
		trimmedSeed = "root"
	}
	accountHash := sha256.Sum256([]byte("claude-code-metadata-account:" + trimmedSeed))
	return uuidStringFromBytes(accountHash[:16])
}

func ValidateClaudeCodeMetadataDeviceID(value string, key string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if !isHexString(trimmed, 64) {
		return ErrInvalidConfig(fmt.Sprintf("invalid %s: must be 64 hex characters", key))
	}
	return nil
}

func ValidateClaudeCodeMetadataAccountUUID(value string, key string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if !isUUIDLike(trimmed) {
		return ErrInvalidConfig(fmt.Sprintf("invalid %s: must be UUID-like", key))
	}
	return nil
}

func isHexString(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}

func isUUIDLike(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

func uuidStringFromBytes(input []byte) string {
	b := make([]byte, 16)
	copy(b, input)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
