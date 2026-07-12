package perfbench

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCollectSemanticLogEvents_freezes_normalized_attribute_values(t *testing.T) {
	// Given
	const requestID = "req-1700000000000000000-42"
	logDir := t.TempDir()
	line := `{"ts":"2026-01-02T03:04:05Z","event":"proxyToUpstreamRequest","request_id":"req-1700000000000000000-42","endpoint":"http://127.0.0.1:43123/responses","endpoint_type":"responses","model":"perf-model","stream":true,"tool_count":1,"body_preview":"{\"source\":{\"type\":\"base64\",\"data\":\"image\"}}","elapsed_ms":17}` + "\n"
	if err := os.WriteFile(filepath.Join(logDir, requestID+".txt"), []byte(line), 0o600); err != nil {
		t.Fatalf("write fixture log: %v", err)
	}

	// When
	events, err := collectSemanticLogEvents(logDir, requestID)
	if err != nil {
		t.Fatalf("collect log events: %v", err)
	}
	encoded, err := json.Marshal(events[0])
	if err != nil {
		t.Fatalf("marshal projected event: %v", err)
	}
	var projected struct {
		Attrs map[string]string `json:"attrs"`
	}
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatalf("decode projected event: %v", err)
	}

	// Then
	want := map[string]string{
		"body_preview":  `{"source":{"data":"image","type":"<image-encoding>"}}`,
		"endpoint":      "http://127.0.0.1:<port>/responses",
		"endpoint_type": "responses",
		"model":         "perf-model",
		"request_id":    "req-<id>",
		"stream":        "true",
		"tool_count":    "1",
	}
	if !reflect.DeepEqual(projected.Attrs, want) {
		t.Fatalf("attrs = %#v, want %#v", projected.Attrs, want)
	}
}

func TestCollectSemanticLogEvents_rejects_sensitive_attribute_values(t *testing.T) {
	// Given
	const requestID = "req-1700000000000000000-43"
	logDir := t.TempDir()
	imageSentinel := base64.StdEncoding.EncodeToString(generatedImageFixture(96))
	line := fmt.Sprintf(`{"event":"clientToProxyRequest","request_id":"req-1700000000000000000-43","request_body":%q}`, imageSentinel) + "\n"
	if err := os.WriteFile(filepath.Join(logDir, requestID+".txt"), []byte(line), 0o600); err != nil {
		t.Fatalf("write unsafe fixture log: %v", err)
	}

	// When
	_, err := collectSemanticLogEvents(logDir, requestID)

	// Then
	if err == nil {
		t.Fatal("collectSemanticLogEvents accepted image Base64 evidence")
	}
}

func TestCollectSemanticLogEvents_redacts_truncated_messages_source_data(t *testing.T) {
	// Given
	const requestID = "req-1700000000000000000-45"
	logDir := t.TempDir()
	body, err := buildScenarioRequest(scenario{
		Downstream: downstreamMessages,
		Upstream:   upstreamResponses,
		Delivery:   deliveryStream,
		ImageBytes: 1 << 20,
		Profile:    profileLog,
	})
	if err != nil {
		t.Fatalf("build messages request: %v", err)
	}
	if len(body) <= 512 {
		t.Fatalf("request body bytes = %d, want > 512", len(body))
	}
	logRecord := struct {
		Event       string `json:"event"`
		RequestID   string `json:"request_id"`
		Method      string `json:"method"`
		Path        string `json:"path"`
		ContentType string `json:"content_type"`
		RequestBody string `json:"request_body"`
	}{
		Event:       "clientToProxyRequest",
		RequestID:   requestID,
		Method:      http.MethodPost,
		Path:        "/v1/messages",
		ContentType: "application/json",
		RequestBody: string(body[:512]) + "...[TRUNCATED]",
	}
	line, err := json.Marshal(logRecord)
	if err != nil {
		t.Fatalf("marshal log fixture: %v", err)
	}
	line = append(line, '\n')
	if err := os.WriteFile(filepath.Join(logDir, requestID+".txt"), line, 0o600); err != nil {
		t.Fatalf("write fixture log: %v", err)
	}
	imageSentinel := base64.StdEncoding.EncodeToString(generatedImageFixture(96))

	// When
	events, err := collectSemanticLogEvents(logDir, requestID)
	if err != nil {
		t.Fatalf("collect log events: %v", err)
	}

	// Then
	requestBody := events[0].Attrs["request_body"]
	if strings.Contains(requestBody, imageSentinel) {
		t.Fatal("normalized request body retained image Base64 sentinel")
	}
	if !strings.Contains(requestBody, `"data":"image"`) {
		t.Fatalf("normalized request body = %q, want redacted image data", requestBody)
	}
}

func TestParseSemanticResponses_separates_finish_reason_from_terminal_status(t *testing.T) {
	// Given
	body := []byte(`{"id":"resp_contract","status":"completed","finish_reason":"tool_calls","output":[{"type":"function_call","call_id":"call_fixture","name":"lookup","arguments":"{\"query\":\"fixture\"}"}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)

	// When
	result, err := parseSemanticResponses(body)
	if err != nil {
		t.Fatalf("parse responses: %v", err)
	}

	// Then
	if result.FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", result.FinishReason)
	}
	field, exists := reflect.TypeOf(result).FieldByName("TerminalStatus")
	if !exists {
		t.Fatal("semanticDownstreamResult has no distinct TerminalStatus field")
	}
	terminalStatus := reflect.ValueOf(result).FieldByIndex(field.Index).String()
	if terminalStatus != "completed" {
		t.Fatalf("terminal status = %q, want completed", terminalStatus)
	}
}

func TestStableSemanticProxyHeaders_includes_normalized_proxy_owned_headers(t *testing.T) {
	// Given
	header := http.Header{
		"X-Request-Id":                                   {"req-1700000000000000000-44"},
		"X-Proxy-Normalization-Version":                  {"v1"},
		"X-Proxy-Estimated-Input-Tokens":                 {"123"},
		"X-Proxy-Upstream-Retry-Count":                   {"1"},
		"X-Proxy-Upstream-Retry-Delay":                   {"1ms"},
		"X-Proxy-Upstream-Anthropic-Cache-Control":       {"nochange"},
		"X-Proxy-To-Upstream-Claude-Metadata-Session-Id": {"11111111-1111-4111-8111-111111111111"},
		"X-Provider-Today-Cache-Rate":                    {"12.34 %"},
		"X-Root-Env-Version":                             {"2026-01-02T03:04:05Z"},
	}

	// When
	projected := stableSemanticProxyHeaders(header)

	// Then
	want := map[string]string{
		"X-Request-Id":                                   "req-<id>",
		"X-Proxy-Normalization-Version":                  "v1",
		"X-Proxy-Estimated-Input-Tokens":                 "123",
		"X-Proxy-Upstream-Retry-Count":                   "1",
		"X-Proxy-Upstream-Retry-Delay":                   "1ms",
		"X-Proxy-Upstream-Anthropic-Cache-Control":       "nochange",
		"X-Proxy-To-Upstream-Claude-Metadata-Session-Id": "<uuid>",
	}
	if !reflect.DeepEqual(projected, want) {
		t.Fatalf("proxy headers = %#v, want %#v", projected, want)
	}
}

func TestCollectSemanticScenario_freezes_delivery_media_types(t *testing.T) {
	seen := map[deliveryMode]bool{}
	for _, item := range scenarioCatalog() {
		if seen[item.Delivery] {
			continue
		}
		seen[item.Delivery] = true

		// Given
		fixture := generatedImageFixture(item.ImageBytes)

		// When
		record, err := collectSemanticScenario(item, semanticImageFact{SHA256: sha256Hex(fixture), Bytes: int64(len(fixture))})
		if err != nil {
			t.Fatalf("collect %s: %v", item.ID, err)
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal %s: %v", item.ID, err)
		}
		var media struct {
			DownstreamContentType string `json:"downstream_content_type"`
			UpstreamContentType   string `json:"upstream_content_type"`
			UpstreamResponseMode  string `json:"upstream_response_mode"`
		}
		if err := json.Unmarshal(encoded, &media); err != nil {
			t.Fatalf("decode media evidence: %v", err)
		}

		// Then
		wantDownstream, wantUpstream, wantMode := "application/json", "text/event-stream", "sse"
		if item.Delivery == deliveryStream {
			wantDownstream = "text/event-stream"
		}
		if item.Delivery == deliveryUpstreamNonStream {
			wantUpstream, wantMode = "application/json", "json"
		}
		if media.DownstreamContentType != wantDownstream || media.UpstreamContentType != wantUpstream || media.UpstreamResponseMode != wantMode {
			t.Fatalf("%s media = %+v, want downstream=%q upstream=%q mode=%q", item.Delivery, media, wantDownstream, wantUpstream, wantMode)
		}
	}
}

func TestCollectSemanticScenario_freezes_typed_history_second_request(t *testing.T) {
	for _, item := range scenarioCatalog() {
		if item.Profile != profileHistoryRestore {
			continue
		}

		// Given
		fixture := generatedImageFixture(item.ImageBytes)

		// When
		record, err := collectSemanticScenario(item, semanticImageFact{SHA256: sha256Hex(fixture), Bytes: int64(len(fixture))})
		if err != nil {
			t.Fatalf("collect %s: %v", item.ID, err)
		}
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal history record: %v", err)
		}
		var projected struct {
			History struct {
				Method             string `json:"method"`
				Endpoint           string `json:"endpoint"`
				BodySHA256         string `json:"body_sha256"`
				BodyBytes          int64  `json:"body_bytes"`
				ContentLength      int64  `json:"content_length"`
				RequestContentType string `json:"request_content_type"`
				DecodedImageSHA256 string `json:"decoded_image_sha256"`
				DecodedImageBytes  int64  `json:"decoded_image_bytes"`
				PromptCacheKey     string `json:"prompt_cache_key"`
				ContentMode        string `json:"content_mode"`
				RestoredToolName   string `json:"restored_tool_name"`
				RestoredToolResult string `json:"restored_tool_result"`
				RestoredUserText   string `json:"restored_user_text"`
			} `json:"history_second_request"`
		}
		if err := json.Unmarshal(encoded, &projected); err != nil {
			t.Fatalf("decode history evidence: %v", err)
		}

		// Then
		history := projected.History
		if history.Method != http.MethodPost || history.Endpoint != semanticUpstreamPath(item.Upstream) {
			t.Fatalf("history endpoint = %s %s", history.Method, history.Endpoint)
		}
		if history.BodySHA256 == "" || history.BodyBytes <= 0 || history.ContentLength != history.BodyBytes {
			t.Fatalf("history body evidence = %+v", history)
		}
		if history.RequestContentType != "application/json" || history.DecodedImageSHA256 != sha256Hex(fixture) || history.DecodedImageBytes != int64(len(fixture)) {
			t.Fatalf("history request/image evidence = %+v", history)
		}
		if history.RestoredToolName != "lookup" || history.RestoredToolResult != "fixture-result" || history.RestoredUserText != "continue" {
			t.Fatalf("history restored semantics = %+v", history)
		}
		return
	}
	t.Fatal("history_restore scenario not found")
}
