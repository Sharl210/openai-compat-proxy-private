package httpapi

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/config"
)

type responseCaptureWriter struct {
	http.ResponseWriter
	status       int
	body         bytes.Buffer
	captureBody  bool
	captureLimit int64
	truncated    bool
}

func (w *responseCaptureWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseCaptureWriter) Write(data []byte) (int, error) {
	if w.captureBody && !strings.HasPrefix(strings.ToLower(w.Header().Get("Content-Type")), "text/event-stream") {
		w.capture(data)
	}
	return w.ResponseWriter.Write(data)
}

func (w *responseCaptureWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
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
