package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"openai-compat-proxy/internal/errorsx"
	"openai-compat-proxy/internal/upstream"
)

func writeUpstreamError(w http.ResponseWriter, err error) bool {
	var httpErr *upstream.HTTPStatusError
	if !errors.As(err, &httpErr) {
		return false
	}
	contentType := httpErr.ContentType
	if contentType == "" {
		contentType = detectRawErrorContentType(httpErr.BodyBytes)
	}
	errorsx.WriteRaw(w, httpErr.StatusCode, contentType, httpErr.BodyBytes)
	return true
}

func detectRawErrorContentType(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "text/plain; charset=utf-8"
	}
	if json.Valid(trimmed) {
		return "application/json"
	}
	return "text/plain; charset=utf-8"
}
