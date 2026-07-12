package perfbench

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"openai-compat-proxy/internal/logging"
)

var (
	semanticRequestIDPattern               = regexp.MustCompile(`req-[0-9]+-[0-9]+`)
	semanticUUIDPattern                    = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`)
	semanticRFC3339Pattern                 = regexp.MustCompile(`[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.+-]+Z?`)
	semanticUnixTimePattern                = regexp.MustCompile(`("(?:created|created_at|timestamp|ts)"\s*:\s*)[0-9]+(?:\.[0-9]+)?`)
	semanticTempPathPattern                = regexp.MustCompile(`(?:/tmp/|/var/tmp/)[^"\s]+`)
	semanticIPv4PortPattern                = regexp.MustCompile(`((?:127\.0\.0\.1|localhost):)[0-9]+`)
	semanticIPv6PortPattern                = regexp.MustCompile(`(\[::1\]:)[0-9]+`)
	semanticBase64WordPattern              = regexp.MustCompile(`(?i)base64`)
	semanticTruncatedAnthropicImagePattern = regexp.MustCompile(`("source"\s*:\s*\{[^{}]*"type"\s*:\s*"base64"[^{}]*"data"\s*:\s*")[A-Za-z0-9+/=]+\.\.\.\[TRUNCATED\]$`)
)

func normalizeSemanticOutput(contentType string, body []byte) (string, error) {
	if strings.Contains(contentType, "text/event-stream") {
		return normalizeSemanticSSE(body)
	}
	normalized := normalizeSemanticDynamicBytes(body)
	var compact bytes.Buffer
	if err := json.Compact(&compact, normalized); err != nil {
		return "", fmt.Errorf("compact downstream JSON: %w", err)
	}
	return compact.String(), nil
}

func normalizeSemanticSSE(body []byte) (string, error) {
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	for index, line := range lines {
		normalized := string(normalizeSemanticDynamicBytes([]byte(line)))
		if !strings.HasPrefix(normalized, "data:") {
			lines[index] = normalized
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(normalized, "data:"))
		if data == "" || data == "[DONE]" {
			lines[index] = "data: " + data
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(data)); err != nil {
			return "", fmt.Errorf("compact downstream SSE data: %w", err)
		}
		lines[index] = "data: " + compact.String()
	}
	return strings.Join(lines, "\n"), nil
}

func normalizeSemanticDynamicBytes(value []byte) []byte {
	normalized := append([]byte(nil), value...)
	normalized = semanticRequestIDPattern.ReplaceAll(normalized, []byte("req-<id>"))
	normalized = semanticUUIDPattern.ReplaceAll(normalized, []byte("<uuid>"))
	normalized = semanticRFC3339Pattern.ReplaceAll(normalized, []byte("<timestamp>"))
	normalized = semanticUnixTimePattern.ReplaceAll(normalized, []byte(`${1}"<timestamp>"`))
	normalized = semanticTempPathPattern.ReplaceAll(normalized, []byte("<temp-path>"))
	normalized = semanticIPv4PortPattern.ReplaceAll(normalized, []byte(`${1}<port>`))
	normalized = semanticIPv6PortPattern.ReplaceAll(normalized, []byte(`${1}<port>`))
	return normalized
}

func collectSemanticArchiveEvidence(rootDir, requestID string) (map[string]fileDigest, error) {
	result := make(map[string]fileDigest, 4)
	for _, name := range []string{"request.ndjson", "raw.ndjson", "canonical.ndjson", "final.ndjson"} {
		body, err := os.ReadFile(filepath.Join(rootDir, requestID, name))
		if err != nil {
			return nil, fmt.Errorf("read archive %s: %w", name, err)
		}
		normalized, records, err := normalizeSemanticNDJSON(body)
		if err != nil {
			return nil, fmt.Errorf("normalize archive %s: %w", name, err)
		}
		result[name] = fileDigest{Records: records, SHA256: sha256Hex(normalized)}
	}
	return result, nil
}

func normalizeSemanticNDJSON(body []byte) ([]byte, int, error) {
	var normalized bytes.Buffer
	records := 0
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, normalizeSemanticDynamicBytes(line)); err != nil {
			return nil, 0, err
		}
		normalized.Write(compact.Bytes())
		normalized.WriteByte('\n')
		records++
	}
	return normalized.Bytes(), records, nil
}

func collectSemanticLogEvents(logDir, requestID string) ([]semanticLogEvent, error) {
	body, err := os.ReadFile(filepath.Join(logDir, requestID+".txt"))
	if err != nil {
		return nil, fmt.Errorf("read structured log: %w", err)
	}
	stableEvents := map[string]bool{
		"clientToProxyRequest": true, "proxyToUpstreamRequest": true,
		"upstreamToProxyResponse": true, "proxyToClientResponse": true,
	}
	var result []semanticLogEvent
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			return nil, fmt.Errorf("decode structured log line: %w", err)
		}
		var name string
		if err := json.Unmarshal(fields["event"], &name); err != nil {
			return nil, fmt.Errorf("decode structured log event: %w", err)
		}
		if !stableEvents[name] {
			continue
		}
		attrs := make(map[string]string, len(fields)-2)
		for attr, raw := range fields {
			lower := strings.ToLower(attr)
			if attr == "event" || attr == "ts" || attr == "elapsed_ms" ||
				strings.Contains(lower, "authorization") || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") {
				continue
			}
			value, err := normalizeSemanticLogAttr(raw)
			if err != nil {
				return nil, fmt.Errorf("normalize structured log %s.%s: %w", name, attr, err)
			}
			attrs[attr] = value
		}
		event := semanticLogEvent{Name: name, Attrs: attrs}
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, fmt.Errorf("marshal structured log evidence: %w", err)
		}
		if err := assertSafeSemanticLogEvidence(encoded); err != nil {
			return nil, fmt.Errorf("unsafe structured log event %s: %w", name, err)
		}
		result = append(result, event)
	}
	return result, nil
}

func normalizeSemanticLogAttr(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		redacted := logging.RedactImageDataForLog([]byte(text))
		if strings.HasSuffix(redacted, "...[TRUNCATED]") {
			redacted = semanticTruncatedAnthropicImagePattern.ReplaceAllString(redacted, `${1}image"...[TRUNCATED]`)
		}
		normalized := normalizeSemanticDynamicBytes([]byte(redacted))
		return string(semanticBase64WordPattern.ReplaceAll(normalized, []byte("<image-encoding>"))), nil
	}
	var compact bytes.Buffer
	redacted := logging.RedactImageDataForLog(raw)
	normalized := normalizeSemanticDynamicBytes([]byte(redacted))
	normalized = semanticBase64WordPattern.ReplaceAll(normalized, []byte("<image-encoding>"))
	if err := json.Compact(&compact, normalized); err != nil {
		return "", err
	}
	return compact.String(), nil
}

func assertSafeSemanticLogEvidence(encoded []byte) error {
	lower := strings.ToLower(string(encoded))
	for _, forbidden := range []string{
		"data:image/", "base64", "authorization", "api_key", "apikey",
		"perf-proxy-secret", "perf-upstream-secret", `\\tmp\\`, `\\temp\\`,
	} {
		if strings.Contains(lower, forbidden) {
			return fmt.Errorf("contains forbidden value %q", forbidden)
		}
	}
	if semanticTempPathPattern.Match(encoded) || bytes.Contains(encoded, generatedImageFixture(96)) {
		return fmt.Errorf("contains raw temp path or image bytes")
	}
	imageSentinel := base64.StdEncoding.EncodeToString(generatedImageFixture(96))
	if strings.Contains(string(encoded), imageSentinel) {
		return fmt.Errorf("contains image Base64 sentinel")
	}
	sentinel := []byte("cGVyZi11cHN0cmVhbS1zZWNyZXQ=")
	if bytes.Contains(encoded, sentinel) {
		return fmt.Errorf("contains Base64 API key sentinel")
	}
	return nil
}
