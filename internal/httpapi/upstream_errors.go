package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/contextoverflow"
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
	body, bodyContentType := decorateUpstreamErrorBody(httpErr, contentType)
	errorsx.WriteRaw(w, httpErr.StatusCode, bodyContentType, body)
	return true
}

func writeUpstreamErrorForProtocol(w http.ResponseWriter, err error, protocol string) bool {
	var httpErr *upstream.HTTPStatusError
	if errors.As(err, &httpErr) {
		if code, message, ok := normalizeUpstreamHTTPContextOverflow(httpErr); ok {
			writeContextOverflowError(w, protocol, code, message)
			return true
		}
	}
	return writeUpstreamError(w, err)
}

func writeRequestValidationError(w http.ResponseWriter, err error) bool {
	var validationErr *upstream.RequestValidationError
	if !errors.As(err, &validationErr) {
		return false
	}
	errorsx.WriteJSON(w, http.StatusBadRequest, "invalid_request", validationErr.Error())
	return true
}

func writeTerminalFailureError(w http.ResponseWriter, terminalFailure *aggregate.TerminalFailureError, protocol string) bool {
	if terminalFailure == nil {
		return false
	}
	if code, message, ok := normalizeContextOverflowSignal(terminalFailure.HealthFlag, terminalFailure.Message, terminalFailure.UpstreamError); ok {
		writeContextOverflowError(w, protocol, code, message)
		return true
	}
	statusCode := http.StatusBadGateway
	if terminalFailure.HealthFlag == "upstream_timeout" {
		statusCode = http.StatusGatewayTimeout
	}
	errorsx.WriteJSON(w, statusCode, terminalFailure.HealthFlag, terminalFailure.Message)
	return true
}

func writeContextOverflowError(w http.ResponseWriter, protocol string, code string, message string) {
	if protocol == clientReasoningProtocolMessages {
		writeAnthropicContextLimitExceeded(w, message)
		return
	}
	errorsx.WriteJSON(w, http.StatusBadRequest, code, message)
}

func normalizeUpstreamHTTPContextOverflow(httpErr *upstream.HTTPStatusError) (string, string, bool) {
	if httpErr == nil {
		return "", "", false
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(httpErr.BodyBytes, &payload); err != nil {
		return "", "", false
	}
	message := strings.TrimSpace(payload.Error.Message)
	if message == "" {
		message = strings.TrimSpace(payload.Message)
	}
	return contextoverflow.NormalizeCandidates([]string{
		strings.TrimSpace(payload.Error.Code),
		strings.TrimSpace(payload.Error.Type),
		strings.TrimSpace(payload.Code),
		strings.TrimSpace(payload.Type),
	}, message)
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

func decorateUpstreamErrorBody(httpErr *upstream.HTTPStatusError, contentType string) ([]byte, string) {
	body := append([]byte(nil), httpErr.BodyBytes...)
	notice := ""
	if httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= 500 {
		notice = buildRetryNoticeText(httpErr.RetriesPerformed, httpErr.RetryDelay)
	}
	if notice == "" {
		return body, contentType
	}
	trimmed := bytes.TrimSpace(body)
	if json.Valid(trimmed) {
		if decorated, ok := prependNoticeToJSONBody(trimmed, notice); ok {
			return decorated, "application/json"
		}
	}
	return []byte(notice + string(body)), "text/plain; charset=utf-8"
}

func prependNoticeToJSONBody(body []byte, notice string) ([]byte, bool) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false
	}
	if errorMap, ok := payload["error"].(map[string]any); ok {
		if message, ok := errorMap["message"].(string); ok {
			errorMap["message"] = notice + message
			encoded, err := json.Marshal(payload)
			return encoded, err == nil
		}
	}
	if message, ok := payload["message"].(string); ok {
		payload["message"] = notice + message
		encoded, err := json.Marshal(payload)
		return encoded, err == nil
	}
	return nil, false
}

func buildRetryNoticeText(retries int, delay time.Duration) string {
	if retries <= 0 {
		return ""
	}
	total := delay * time.Duration(retries)
	return fmt.Sprintf("本代理层已重试%d遍，每次重试间隔%s，共重试了%s。下面是上游错误原信息：", retries, formatRetryNoticeSeconds(delay), formatRetryNoticeSeconds(total))
}

func formatRetryNoticeSeconds(delay time.Duration) string {
	seconds := delay.Seconds()
	if seconds == float64(int64(seconds)) {
		return fmt.Sprintf("%d秒", int64(seconds))
	}
	text := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", seconds), "0"), ".")
	return text + "秒"
}
