package perfbench

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
)

func validateSemanticCaptureSet(item scenario, captures []capturedSemanticRequest) error {
	wantAttempts := 1
	if item.Profile == profileRetryOnce {
		wantAttempts = 2
	}
	if len(captures) != wantAttempts {
		return fmt.Errorf("upstream captures = %d, want %d", len(captures), wantAttempts)
	}
	wantPath := semanticUpstreamPath(item.Upstream)
	for index, capture := range captures {
		if capture.Method != http.MethodPost || capture.Path != wantPath {
			return fmt.Errorf("attempt %d endpoint = %s %s, want POST %s", index+1, capture.Method, capture.Path, wantPath)
		}
		if capture.ContentLength != int64(len(capture.Body)) {
			return fmt.Errorf("attempt %d content length = %d, body bytes = %d", index+1, capture.ContentLength, len(capture.Body))
		}
		wantStream := item.Delivery != deliveryUpstreamNonStream
		hasStream := bytes.Contains(capture.Body, []byte(`"stream":true`))
		if hasStream != wantStream {
			return fmt.Errorf("attempt %d upstream stream = %t, want %t", index+1, hasStream, wantStream)
		}
	}
	if item.Profile == profileRetryOnce && !bytes.Equal(captures[0].Body, captures[1].Body) {
		return fmt.Errorf("retry attempt body changed")
	}
	cacheKey := semanticPromptCacheKey(captures[0].Body)
	if item.Upstream == upstreamResponses && cacheKey == "" {
		return fmt.Errorf("responses upstream prompt_cache_key missing")
	}
	if item.Upstream != upstreamResponses && cacheKey != "" {
		return fmt.Errorf("non-responses upstream prompt_cache_key = %q", cacheKey)
	}
	if item.Downstream == downstreamResponses && item.Upstream == upstreamResponses && item.Profile == profilePlain &&
		cacheKey != "perf-explicit-cache-key" {
		return fmt.Errorf("explicit prompt_cache_key = %q", cacheKey)
	}
	return nil
}

func semanticUpstreamPath(protocol upstreamProtocol) string {
	switch protocol {
	case upstreamResponses:
		return "/responses"
	case upstreamChat:
		return "/chat/completions"
	case upstreamAnthropic:
		return "/messages"
	default:
		return ""
	}
}

func semanticAttemptDigests(captures []capturedSemanticRequest) []semanticBodyDigest {
	result := make([]semanticBodyDigest, 0, len(captures))
	for _, capture := range captures {
		result = append(result, semanticBodyDigest{
			SHA256: sha256Hex(capture.Body), Bytes: int64(len(capture.Body)),
		})
	}
	return result
}

func stableSemanticProxyHeaders(header http.Header) map[string]string {
	excluded := map[string]bool{
		"X-Provider-Today-Cache-Rate": true, "X-Provider-History-Cache-Rate": true,
		"X-Root-Provider-Today-Cache-Rate": true, "X-Root-Provider-History-Cache-Rate": true,
		"X-This-Usage-Cache-Write-Tokens": true,
		"X-Provider-Today-Cache-Write-Coverage": true, "X-Provider-History-Cache-Write-Coverage": true,
		"X-Root-Provider-Today-Cache-Write-Coverage": true, "X-Root-Provider-History-Cache-Write-Coverage": true,
		"X-Client-To-Proxy-Reasoning-Mode": true, "X-Proxy-To-Upstream-Reasoning-Mode": true,
		"X-Root-Env-Version": true, "X-Provider-Version": true,
	}
	result := make(map[string]string, len(header))
	for name, values := range header {
		canonical := http.CanonicalHeaderKey(name)
		if !strings.HasPrefix(canonical, "X-") || excluded[canonical] {
			continue
		}
		result[canonical] = string(normalizeSemanticDynamicBytes([]byte(strings.Join(values, ","))))
	}
	return result
}

func validateSemanticProxyHeaders(delivery deliveryMode, headers map[string]string) error {
	allowed := map[string]bool{}
	for _, name := range []string{
		"X-Request-Id", "X-Proxy-Normalization-Version", "X-Accel-Buffering",
		"X-Cache-Info-Timezone", "X-This-Usage-Tokens", "X-Client-To-Proxy-Model",
		"X-Client-To-Proxy-Service-Tier", "X-Client-To-Proxy-Reasoning-Parameters",
		"X-Client-To-Proxy-Reasoning-Effort", "X-Client-To-Proxy-NoPrompt",
		"X-Proxy-To-Upstream-Model", "X-Proxy-Estimated-Input-Tokens",
		"X-Proxy-Model-Limit-Context-Tokens", "X-Proxy-To-Upstream-Service-Tier",
		"X-Proxy-To-Upstream-Max-Output-Tokens", "X-Proxy-To-Upstream-Masquerade-User-Agent",
		"X-Proxy-To-Upstream-Claude-Metadata-Device-Id", "X-Proxy-To-Upstream-Claude-Metadata-Account-Uuid",
		"X-Proxy-To-Upstream-Claude-Metadata-Session-Id", "X-Proxy-To-Upstream-Reasoning-Effort",
		"X-Proxy-To-Upstream-Reasoning-Parameters", "X-Proxy-Upstream-Retry-Count",
		"X-Proxy-Upstream-Retry-Delay", "X-Proxy-Upstream-Anthropic-Cache-Control",
		"X-Provider-Name", "X-System-Prompt-Attach",
	} {
		allowed[http.CanonicalHeaderKey(name)] = true
	}
	for name := range headers {
		if !allowed[name] {
			return fmt.Errorf("unexpected proxy-owned header %s", name)
		}
	}
	required := []string{
		"X-Request-Id", "X-Proxy-Normalization-Version", "X-Cache-Info-Timezone",
		"X-Client-To-Proxy-Model", "X-Client-To-Proxy-NoPrompt", "X-Proxy-To-Upstream-Model",
		"X-Proxy-Estimated-Input-Tokens", "X-Proxy-Model-Limit-Context-Tokens",
		"X-Proxy-To-Upstream-Reasoning-Parameters", "X-Proxy-Upstream-Retry-Count",
		"X-Proxy-Upstream-Retry-Delay", "X-Proxy-Upstream-Anthropic-Cache-Control", "X-Provider-Name",
	}
	if delivery == deliveryStream {
		required = append(required, "X-Accel-Buffering")
	}
	for _, name := range required {
		canonical := http.CanonicalHeaderKey(name)
		if _, exists := headers[canonical]; !exists {
			return fmt.Errorf("required proxy-owned header %s missing from %+v", canonical, headers)
		}
	}
	if headers["X-Request-Id"] != "req-<id>" {
		return fmt.Errorf("normalized request id header = %q", headers["X-Request-Id"])
	}
	return nil
}

func validateCollectedSemanticRecord(record semanticRecord, imageFact semanticImageFact) error {
	if record.DownstreamStatus != http.StatusOK {
		return fmt.Errorf("downstream status = %d", record.DownstreamStatus)
	}
	if record.ObservedBodySHA256 == "" || record.ObservedBodyBytes <= 0 || record.ContentLength != record.ObservedBodyBytes {
		return fmt.Errorf("invalid upstream body evidence")
	}
	if record.DecodedImageSHA256 != imageFact.SHA256 || record.DecodedImageBytes != imageFact.Bytes {
		return fmt.Errorf("decoded image evidence changed")
	}
	wantDownstreamContentType, wantUpstreamContentType, wantUpstreamMode := "application/json", "text/event-stream", "sse"
	if record.Delivery == deliveryStream {
		wantDownstreamContentType = "text/event-stream"
	}
	if record.Delivery == deliveryUpstreamNonStream {
		wantUpstreamContentType, wantUpstreamMode = "application/json", "json"
	}
	if record.DownstreamContentType != wantDownstreamContentType || record.UpstreamContentType != wantUpstreamContentType || record.UpstreamResponseMode != wantUpstreamMode {
		return fmt.Errorf("delivery media mismatch: downstream=%q upstream=%q mode=%q", record.DownstreamContentType, record.UpstreamContentType, record.UpstreamResponseMode)
	}
	if err := validateSemanticProxyHeaders(record.Delivery, record.ProxyHeaders); err != nil {
		return err
	}
	if record.NormalizedOutput == "" || len(record.Reasoning) == 0 || len(record.Tools) == 0 || len(record.Usage) == 0 {
		return fmt.Errorf("incomplete downstream semantics: reasoning=%v tools=%v usage=%v finish=%q output=%s",
			record.Reasoning, record.Tools, record.Usage, record.FinishReason, record.NormalizedOutput)
	}
	if record.Tools[0].Name != "lookup" || record.Tools[0].Arguments != `{"query":"fixture"}` {
		return fmt.Errorf("tool semantics = %+v", record.Tools)
	}
	if record.Downstream == downstreamResponses {
		if record.FinishReason != "" && record.FinishReason != "tool_calls" {
			return fmt.Errorf("responses finish reason = %q", record.FinishReason)
		}
		if record.TerminalStatus != "completed" {
			return fmt.Errorf("responses terminal status = %q", record.TerminalStatus)
		}
	} else if record.FinishReason == "" {
		return fmt.Errorf("downstream finish reason missing")
	}
	if record.Profile == profileRetryOnce {
		if len(record.RetryAttempts) != 2 || record.RetryAttempts[0] != record.RetryAttempts[1] {
			return fmt.Errorf("retry evidence = %+v", record.RetryAttempts)
		}
	}
	if record.Profile == profileHistoryRestore {
		if err := validateSemanticHistoryEvidence(record, imageFact); err != nil {
			return err
		}
	}
	if record.Profile == profileLog || record.Profile == profileLogArchive {
		wantNames := []string{"clientToProxyRequest", "proxyToUpstreamRequest", "upstreamToProxyResponse", "proxyToClientResponse"}
		if record.Delivery == deliveryStream {
			wantNames = []string{"clientToProxyRequest", "proxyToUpstreamRequest", "proxyToClientResponse"}
		}
		if len(record.LogEvents) != len(wantNames) {
			return fmt.Errorf("stable log event count = %d, want %d", len(record.LogEvents), len(wantNames))
		}
		for index, event := range record.LogEvents {
			if event.Name != wantNames[index] || len(event.Attrs) == 0 || event.Attrs["request_id"] != "req-<id>" {
				return fmt.Errorf("stable log event %d = %+v, want %s with normalized values", index, event, wantNames[index])
			}
		}
	}
	if record.Profile == profileLogArchive && len(record.Archives) != 4 {
		return fmt.Errorf("archive evidence files = %d, want 4", len(record.Archives))
	}
	return nil
}

func validateSemanticHistoryEvidence(record semanticRecord, imageFact semanticImageFact) error {
	history := record.HistorySecondRequest
	if history == nil {
		return fmt.Errorf("history second request evidence missing")
	}
	if history.Method != http.MethodPost || history.Endpoint != semanticUpstreamPath(record.Upstream) {
		return fmt.Errorf("history second endpoint = %s %s", history.Method, history.Endpoint)
	}
	if history.BodySHA256 == "" || history.BodyBytes <= 0 || history.ContentLength != history.BodyBytes {
		return fmt.Errorf("invalid history second body evidence")
	}
	if history.RequestContentType != "application/json" || history.DecodedImageSHA256 != imageFact.SHA256 || history.DecodedImageBytes != imageFact.Bytes {
		return fmt.Errorf("history second request/image evidence changed")
	}
	wantContentType, wantMode := "text/event-stream", "sse"
	if record.Delivery == deliveryUpstreamNonStream {
		wantContentType, wantMode = "application/json", "json"
	}
	if history.ContentMode != wantMode || history.UpstreamResponseContentType != wantContentType || history.UpstreamResponseMode != wantMode {
		return fmt.Errorf("history second content evidence = %+v", history)
	}
	if history.RestoredToolName != "lookup" || history.RestoredToolResult != "fixture-result" || history.RestoredUserText != "continue" {
		return fmt.Errorf("history restored semantics = %+v", history)
	}
	if record.Upstream == upstreamResponses && history.PromptCacheKey == "" {
		return fmt.Errorf("history responses prompt cache key missing")
	}
	if record.Upstream != upstreamResponses && history.PromptCacheKey != "" {
		return fmt.Errorf("history non-responses prompt cache key = %q", history.PromptCacheKey)
	}
	return nil
}
