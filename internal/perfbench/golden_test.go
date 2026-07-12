package perfbench

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	semanticGoldenPath     = "testdata/no_regression_semantic_baseline.json"
	semanticGoldenMaxBytes = 2 << 20
	updateGoldenEnv        = "UPDATE_NO_REGRESSION_GOLDEN"
)

type semanticBaseline struct {
	CatalogVersion string           `json:"scenario_catalog_version"`
	CatalogDigest  string           `json:"scenario_catalog_digest"`
	Scenarios      []semanticRecord `json:"scenarios"`
}

func TestNoRegressionSemanticMatrix_matches_frozen_golden(t *testing.T) {
	// Given
	update := os.Getenv(updateGoldenEnv) == "1"
	var frozen []byte
	if !update {
		var err error
		frozen, err = os.ReadFile(semanticGoldenPath)
		if errors.Is(err, os.ErrNotExist) {
			t.Fatalf("semantic golden missing: run %s=1 go test -run '^%s$' -count=1 ./internal/perfbench",
				updateGoldenEnv, t.Name())
		}
		if err != nil {
			t.Fatalf("read semantic golden: %v", err)
		}
	}

	// When
	records, err := collectSemanticMatrix()
	if err != nil {
		t.Fatalf("collect semantic matrix: %v", err)
	}
	version, digest := scenarioCatalogCanonicalDigest(scenarioCatalog())
	actual, err := marshalSemanticBaseline(semanticBaseline{
		CatalogVersion: version,
		CatalogDigest:  digest,
		Scenarios:      records,
	})
	if err != nil {
		t.Fatalf("marshal semantic baseline: %v", err)
	}
	if err := assertSafeSemanticGolden(actual); err != nil {
		t.Fatalf("unsafe semantic golden: %v", err)
	}

	// Then
	if update {
		if err := os.MkdirAll(filepath.Dir(semanticGoldenPath), 0o755); err != nil {
			t.Fatalf("create semantic golden directory: %v", err)
		}
		if err := os.WriteFile(semanticGoldenPath, actual, 0o644); err != nil {
			t.Fatalf("write semantic golden: %v", err)
		}
		return
	}
	if !bytes.Equal(frozen, actual) {
		t.Fatalf("semantic golden changed: frozen_sha256=%s actual_sha256=%s",
			sha256Hex(frozen), sha256Hex(actual))
	}
}

func TestAssertSafeSemanticGolden_rejects_raw_image_bytes(t *testing.T) {
	// Given
	golden := append([]byte(`{"payload":"`), generatedImageFixture(96)...)
	golden = append(golden, []byte(`"}`)...)

	// When
	err := assertSafeSemanticGolden(golden)

	// Then
	if err == nil {
		t.Fatal("assertSafeSemanticGolden accepted raw image fixture bytes")
	}
	if !strings.Contains(err.Error(), "raw image bytes") {
		t.Fatalf("error = %q, want raw image bytes rejection", err)
	}
}

func marshalSemanticBaseline(baseline semanticBaseline) ([]byte, error) {
	encoded, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal indented golden: %w", err)
	}
	return append(encoded, '\n'), nil
}

func assertSafeSemanticGolden(golden []byte) error {
	text := string(golden)
	lower := strings.ToLower(text)
	for _, forbidden := range []string{
		"perf-proxy-secret", "perf-upstream-secret", "Authorization", "data:image/", "file://",
		filepath.ToSlash(os.TempDir()) + "/", `\\tmp\\`, `\\Temp\\`,
	} {
		if forbidden != "" && strings.Contains(text, forbidden) {
			return fmt.Errorf("contains forbidden value %q", forbidden)
		}
	}
	for _, forbidden := range []string{"base64", "authorization", "api_key", "apikey"} {
		if strings.Contains(lower, forbidden) {
			return fmt.Errorf("contains forbidden value %q", forbidden)
		}
	}
	sentinel := base64.StdEncoding.EncodeToString(generatedImageFixture(96))
	if strings.Contains(text, sentinel) {
		return errors.New("contains image Base64 sentinel")
	}
	if bytes.Contains(golden, generatedImageFixture(96)) {
		return errors.New("contains raw image bytes")
	}
	if len(golden) > semanticGoldenMaxBytes {
		return fmt.Errorf("golden size %d exceeds semantic golden limit %d", len(golden), semanticGoldenMaxBytes)
	}
	return nil
}
