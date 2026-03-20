package httpapi

import (
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
	if isJSONErrorBody(httpErr.Body) {
		errorsx.WriteRawJSON(w, httpErr.StatusCode, []byte(httpErr.Body))
		return true
	}
	return false
}

func isJSONErrorBody(body string) bool {
	if body == "" {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return false
	}
	_, ok := payload["error"]
	return ok
}
