package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/upstream"
)

type responseCaptureWriter struct {
	http.ResponseWriter
	status                         int
	body                           bytes.Buffer
	captureBody                    bool
	captureLimit                   int64
	truncated                      bool
	onSuccessfulDownstreamOutput   func()
	successfulDownstreamOutputOnce sync.Once
	successfulDownstreamOutput     bool
	finalDownstreamOutcome         downstreamOutcome
}

type downstreamOutcome uint8

const (
	downstreamOutcomeUnknown downstreamOutcome = iota
	downstreamOutcomeReusable
	downstreamOutcomeNotReusable
)

func (w *responseCaptureWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseCaptureWriter) Write(data []byte) (int, error) {
	isEventStream := strings.HasPrefix(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream")
	if w.captureBody && !isEventStream {
		w.capture(data)
	}
	n, err := w.ResponseWriter.Write(data)
	if !isEventStream && err == nil && n > 0 && responseCaptureStatusAllowsReuse(w.status) {
		w.markSuccessfulDownstreamOutput()
	}
	return n, err
}

func (w *responseCaptureWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseCaptureWriter) markSuccessfulDownstreamOutput() {
	if w == nil {
		return
	}
	w.successfulDownstreamOutput = true
	w.successfulDownstreamOutputOnce.Do(func() {
		if w.onSuccessfulDownstreamOutput != nil {
			w.onSuccessfulDownstreamOutput()
		}
	})
}

func (w *responseCaptureWriter) hasSuccessfulDownstreamOutput() bool {
	return w != nil && w.successfulDownstreamOutput
}

func (w *responseCaptureWriter) markFinalDownstreamOutcome(reusable bool) {
	if w == nil {
		return
	}
	if reusable {
		w.finalDownstreamOutcome = downstreamOutcomeReusable
		return
	}
	w.finalDownstreamOutcome = downstreamOutcomeNotReusable
}

func (w *responseCaptureWriter) finalDownstreamReuseDecision() (bool, bool) {
	if w == nil {
		return false, false
	}
	switch w.finalDownstreamOutcome {
	case downstreamOutcomeReusable:
		return true, true
	case downstreamOutcomeNotReusable:
		return false, true
	default:
		return false, false
	}
}

func (w *responseCaptureWriter) isEventStream() bool {
	return w != nil && strings.HasPrefix(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream")
}

type finalDownstreamOutcomeMarker interface {
	markFinalDownstreamOutcome(reusable bool)
}

func markFinalDownstreamOutcome(w http.ResponseWriter, reusable bool) {
	marker, ok := w.(finalDownstreamOutcomeMarker)
	if ok {
		marker.markFinalDownstreamOutcome(reusable)
	}
}

func markTerminalDownstreamEvent(w http.ResponseWriter, event string) {
	switch strings.TrimSpace(event) {
	case "response.completed", "response.done":
		markFinalDownstreamOutcome(w, true)
	case "error", "response.failed", "response.incomplete":
		markFinalDownstreamOutcome(w, false)
	}
}

type successfulDownstreamOutputMarker interface {
	markSuccessfulDownstreamOutput()
}

func markSuccessfulDownstreamOutput(w http.ResponseWriter) {
	marker, ok := w.(successfulDownstreamOutputMarker)
	if ok {
		marker.markSuccessfulDownstreamOutput()
	}
}

func markSuccessfulChatChunk(w http.ResponseWriter, delta map[string]any) {
	if !commitsChatChunkOutput(delta) {
		return
	}
	markSuccessfulDownstreamOutput(w)
}

func commitsChatChunkOutput(delta map[string]any) bool {
	if len(delta) == 0 {
		return false
	}
	if _, isError := delta["error"]; isError {
		return false
	}
	if hasNonWhitespaceDownstreamValue(delta, "content", "reasoning_content", "refusal") {
		return true
	}
	if toolCalls, ok := delta["tool_calls"]; ok {
		switch calls := toolCalls.(type) {
		case []any:
			if len(calls) > 0 {
				return true
			}
		case []map[string]any:
			if len(calls) > 0 {
				return true
			}
		}
	}
	functionCall, _ := delta["function_call"].(map[string]any)
	return len(functionCall) > 0
}

func markSuccessfulDownstreamEvent(w http.ResponseWriter, event string, data map[string]any) {
	if !commitsHTTPDownstreamOutput(event, data) {
		return
	}
	markSuccessfulDownstreamOutput(w)
}

func markSuccessfulDownstreamRawEvent(w http.ResponseWriter, event string, payload []byte) {
	var data map[string]any
	if err := json.Unmarshal(payload, &data); err != nil {
		return
	}
	markSuccessfulDownstreamEvent(w, event, data)
}

func commitsHTTPDownstreamOutput(event string, data map[string]any) bool {
	switch strings.TrimSpace(event) {
	case "content_block_delta":
		delta, _ := data["delta"].(map[string]any)
		switch strings.TrimSpace(stringValue(delta["type"])) {
		case "text_delta":
			return hasNonWhitespaceDownstreamValue(delta, "text")
		case "thinking_delta":
			return hasNonWhitespaceDownstreamValue(delta, "thinking")
		default:
			return false
		}
	case "response.output_text.delta",
		"response.output_text.done",
		"response.refusal.delta",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done",
		"response.reasoning.delta",
		"response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done",
		"response.content_part.added",
		"response.content_part.done",
		"response.reasoning_summary_part.added",
		"response.reasoning_summary_part.done",
		"response.output_item.added",
		"response.output_item.done":
		return upstream.CommitsDownstreamOutput(upstream.Event{Event: event, Data: data})
	case "response.refusal.done":
		return hasNonWhitespaceDownstreamValue(data, "delta", "text", "refusal")
	default:
		return false
	}
}

func hasNonWhitespaceDownstreamValue(data map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, _ := data[key].(string)
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func responseCaptureStatusAllowsReuse(status int) bool {
	return (status == 0 || status >= http.StatusOK) && status < http.StatusMultipleChoices
}

func (w *responseCaptureWriter) capture(data []byte) {
	if w.captureLimit < 0 {
		_, _ = w.body.Write(data)
		return
	}
	remaining := w.captureLimit - int64(w.body.Len())
	if remaining <= 0 {
		w.truncated = true
		return
	}
	if int64(len(data)) > remaining {
		data = data[:remaining]
		w.truncated = true
	}
	_, _ = w.body.Write(data)
}

type replayReadCloser struct {
	io.Reader
	io.Closer
}

func captureRequestBody(body io.ReadCloser, limit int64) (string, io.ReadCloser) {
	if limit < 0 {
		captured, _ := io.ReadAll(body)
		return string(captured), &replayReadCloser{Reader: bytes.NewReader(captured), Closer: body}
	}
	captured, _ := io.ReadAll(io.LimitReader(body, limit+1))
	replay := &replayReadCloser{Reader: io.MultiReader(bytes.NewReader(captured), body), Closer: body}
	if int64(len(captured)) <= limit {
		return string(captured), replay
	}
	return string(captured[:limit]) + "...[TRUNCATED]", replay
}

func redactCapturedImageDataURLs(body string) string {
	const imageDataPrefix = "data:image/"
	const base64DataDelimiter = ";base64,"

	var redacted strings.Builder
	redacted.Grow(len(body))
	inString := false
	escaped := false
	for index := 0; index < len(body); {
		character := body[index]
		if !inString {
			redacted.WriteByte(character)
			inString = character == '"'
			index++
			continue
		}
		if escaped {
			redacted.WriteByte(character)
			escaped = false
			index++
			continue
		}
		if character == '\\' {
			redacted.WriteByte(character)
			escaped = true
			index++
			continue
		}
		if character == '"' {
			redacted.WriteByte(character)
			inString = false
			index++
			continue
		}
		if !strings.HasPrefix(body[index:], imageDataPrefix) {
			redacted.WriteByte(character)
			index++
			continue
		}
		stringEnd := jsonStringEnd(body, index)
		if !strings.Contains(body[index:stringEnd], base64DataDelimiter) {
			redacted.WriteByte(character)
			index++
			continue
		}
		redacted.WriteString("image")
		index = stringEnd
	}
	return redacted.String()
}

func jsonStringEnd(body string, start int) int {
	escaped := false
	for index := start; index < len(body); index++ {
		if escaped {
			escaped = false
			continue
		}
		if body[index] == '\\' {
			escaped = true
			continue
		}
		if body[index] == '"' {
			return index
		}
	}
	return len(body)
}

func requestCaptureLimit(store *config.RuntimeStore, archiveEnabled bool) int64 {
	if archiveEnabled {
		return archiveCaptureLimit(store)
	}
	return 512
}

func archiveCaptureLimit(store *config.RuntimeStore) int64 {
	if store == nil {
		return -1
	}
	snapshot := store.Active()
	if snapshot == nil || snapshot.Config.LogMaxBodySizeMB <= 0 {
		return -1
	}
	limit := int64(snapshot.Config.LogMaxBodySizeMB * 1024 * 1024)
	if limit < 1 {
		return 1
	}
	return limit
}

func truncateBody(body []byte, maxLen int) string {
	if len(body) <= maxLen {
		return string(body)
	}
	return string(body[:maxLen]) + "...[TRUNCATED]"
}
