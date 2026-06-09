package tokenestimator

import (
	"crypto/sha1"
	"encoding/hex"
	"path/filepath"
	"strings"
)

func SafeModelName(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return "unknown-model"
	}
	h := sha1.Sum([]byte(trimmed))
	base := strings.NewReplacer(
		"/", "_",
		"\\", "_",
		":", "_",
		" ", "_",
		"\t", "_",
	).Replace(trimmed)
	return base + "__" + hex.EncodeToString(h[:4])
}

func BucketPaths(providersDir string, key BucketKey) (jsonPath string, txtPath string) {
	safe := SafeModelName(key.Model)
	jsonPath = filepath.Join(providersDir, "Token_Estimator", "SYSTEM_JSON_FILES", key.ProviderID, key.EndpointType, safe+".json")
	txtPath = filepath.Join(providersDir, "Token_Estimator", key.ProviderID, key.EndpointType, safe+".txt")
	return jsonPath, txtPath
}
