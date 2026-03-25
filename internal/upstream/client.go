package upstream

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/model"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

var TestOnlyRetryAttempts = 5
var TestOnlyRetryDelay = 3 * time.Second
var sseScannerInitialBufferSize = 64 * 1024
var sseScannerMaxTokenSize = 8 * 1024 * 1024

type HTTPStatusError struct {
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("upstream status %d: %s", e.StatusCode, e.Body)
}

func NewClient(baseURL string, cfgs ...config.Config) *Client {
	var cfg config.Config
	if len(cfgs) > 0 {
		cfg = cfgs[0]
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: newHTTPClient(cfg),
	}
}

func newHTTPClient(cfg config.Config) *http.Client {
	return &http.Client{Transport: newTransport(cfg)}
}

func newTransport(cfg config.Config) *http.Transport {
	return newTransportWithDialer(cfg, (&net.Dialer{}).DialContext)
}

func newTransportWithDialer(cfg config.Config, baseDialContext func(ctx context.Context, network, addr string) (net.Conn, error)) *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.FirstByteTimeout > 0 {
		transport.ResponseHeaderTimeout = cfg.FirstByteTimeout
	}
	if cfg.IdleTimeout > 0 {
		transport.IdleConnTimeout = cfg.IdleTimeout
	}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialCtx := ctx
		var cancel context.CancelFunc
		if cfg.ConnectTimeout > 0 {
			dialCtx, cancel = context.WithTimeout(ctx, cfg.ConnectTimeout)
			defer cancel()
		}
		conn, err := baseDialContext(dialCtx, network, addr)
		if err != nil {
			return nil, err
		}
		if cfg.IdleTimeout > 0 {
			return &idleTimeoutConn{Conn: conn, timeout: cfg.IdleTimeout}, nil
		}
		return conn, nil
	}
	return transport
}

type idleTimeoutConn struct {
	net.Conn
	timeout time.Duration
}

func (c *idleTimeoutConn) Read(p []byte) (int, error) {
	if c.timeout > 0 {
		if err := c.Conn.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Read(p)
}

func (c *Client) Stream(ctx context.Context, req model.CanonicalRequest, authorization string) ([]Event, error) {
	body, err := buildRequestBody(req)
	if err != nil {
		return nil, err
	}
	attrs := map[string]any{
		"request_id":    req.RequestID,
		"auth_mode":     req.AuthMode,
		"model":         req.Model,
		"stream":        true,
		"body_hash":     hashBytes(body),
		"body_size":     len(body),
		"message_count": len(req.Messages),
		"tool_count":    len(req.Tools),
		"body":          string(body),
	}
	for k, v := range upstreamBodyLogAttrs(body) {
		attrs[k] = v
	}
	logging.Event("upstream_request_built", attrs)

	var lastErr error
	for attempt := 1; attempt <= TestOnlyRetryAttempts; attempt++ {
		events, err := c.streamOnce(ctx, body, authorization)
		if err == nil {
			cachedTokens := cachedTokensFromEvents(events)
			logging.Event("upstream_stream_usage_observed", map[string]any{
				"request_id":     req.RequestID,
				"upstream_event": "response.completed",
				"cached_tokens":  cachedTokens,
				"streaming":      false,
			})
			logging.Event("upstream_response_completed", map[string]any{
				"request_id":    req.RequestID,
				"attempt":       attempt,
				"event_count":   len(events),
				"cached_tokens": cachedTokens,
			})
			return events, nil
		}
		lastErr = err

		if !shouldRetry(lastErr) || attempt == TestOnlyRetryAttempts {
			break
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(TestOnlyRetryDelay):
		}
	}

	return nil, lastErr
}

func (c *Client) StreamEvents(ctx context.Context, req model.CanonicalRequest, authorization string, onEvent func(Event) error) error {
	body, err := buildRequestBody(req)
	if err != nil {
		return err
	}
	attrs := map[string]any{
		"request_id":    req.RequestID,
		"auth_mode":     req.AuthMode,
		"model":         req.Model,
		"stream":        true,
		"body_hash":     hashBytes(body),
		"body_size":     len(body),
		"message_count": len(req.Messages),
		"tool_count":    len(req.Tools),
		"body":          string(body),
	}
	for k, v := range upstreamBodyLogAttrs(body) {
		attrs[k] = v
	}
	logging.Event("upstream_request_built", attrs)
	var eventCount int
	var cachedTokens any
	err = c.streamEventsOnce(ctx, body, authorization, func(evt Event) error {
		eventCount++
		if tokens := cachedTokensFromEvent(evt); tokens != nil {
			cachedTokens = tokens
			logging.Event("upstream_stream_usage_observed", map[string]any{
				"request_id":     req.RequestID,
				"upstream_event": evt.Event,
				"cached_tokens":  tokens,
			})
		}
		return onEvent(evt)
	})
	if err == nil {
		logging.Event("upstream_response_completed", map[string]any{
			"request_id":    req.RequestID,
			"attempt":       1,
			"event_count":   eventCount,
			"cached_tokens": cachedTokens,
			"streaming":     true,
		})
	}
	return err
}

func (c *Client) Models(ctx context.Context, authorization string) (int, []byte, string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return 0, nil, "", err
	}
	if authorization != "" {
		httpReq.Header.Set("Authorization", authorization)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, "", err
	}

	return resp.StatusCode, body, resp.Header.Get("Content-Type"), nil
}

func (c *Client) streamOnce(ctx context.Context, body []byte, authorization string) ([]Event, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		httpReq.Header.Set("Authorization", authorization)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Body: msg}
	}

	return parseSSE(resp)
}

func (c *Client) streamEventsOnce(ctx context.Context, body []byte, authorization string, onEvent func(Event) error) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		httpReq.Header.Set("Authorization", authorization)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(respBody))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return &HTTPStatusError{StatusCode: resp.StatusCode, Body: msg}
	}

	return parseSSEStreaming(resp, onEvent)
}

func shouldRetry(err error) bool {
	httpErr, ok := err.(*HTTPStatusError)
	if !ok {
		return false
	}
	if httpErr.StatusCode >= 500 && httpErr.StatusCode < 600 {
		return true
	}
	return false
}

func buildRequestBody(req model.CanonicalRequest) ([]byte, error) {
	payload := map[string]any{
		"model":  req.Model,
		"stream": true,
	}
	if req.ResponseStore != nil {
		payload["store"] = *req.ResponseStore
	}
	if len(req.ResponseInclude) > 0 {
		payload["include"] = append([]string(nil), req.ResponseInclude...)
	}
	if req.Instructions != "" {
		payload["instructions"] = req.Instructions
	}
	if len(req.ResponseInputItems) > 0 {
		input := make([]map[string]any, 0, len(req.ResponseInputItems))
		for _, item := range req.ResponseInputItems {
			input = append(input, cloneMap(item))
		}
		payload["input"] = input
	} else if len(req.Messages) > 0 {
		var input []map[string]any
		for _, msg := range req.Messages {
			if msg.Role == "tool" {
				input = append(input, map[string]any{
					"type":    "function_call_output",
					"call_id": msg.ToolCallID,
					"output":  joinTextParts(msg.Parts),
				})
				continue
			}

			item := map[string]any{"role": msg.Role}
			var content []map[string]any
			for _, part := range msg.Parts {
				switch part.Type {
				case "text":
					content = append(content, map[string]any{"type": textPartTypeForRole(msg.Role), "text": part.Text})
				case "image_url", "input_image":
					content = append(content, map[string]any{"type": "input_image", "image_url": part.ImageURL})
				}
			}
			if len(content) > 0 {
				item["content"] = content
				input = append(input, item)
			}

			for _, toolCall := range msg.ToolCalls {
				input = append(input, map[string]any{
					"type":      "function_call",
					"call_id":   toolCall.ID,
					"name":      toolCall.Name,
					"arguments": toolCall.Arguments,
				})
			}
		}
		payload["input"] = input
	}
	if len(req.Tools) > 0 {
		var tools []map[string]any
		for _, tool := range req.Tools {
			entry := map[string]any{
				"type":        tool.Type,
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  normalizeJSONSchema(tool.Parameters),
			}
			tools = append(tools, entry)
		}
		payload["tools"] = tools
	}
	if req.ToolChoice.Mode != "" {
		payload["tool_choice"] = req.ToolChoice.Mode
	} else if req.ToolChoice.Raw != nil {
		if value, ok := req.ToolChoice.Raw["value"]; ok {
			payload["tool_choice"] = value
		}
	}
	if req.Reasoning != nil {
		if len(req.Reasoning.Raw) > 0 {
			reasoning := cloneMap(req.Reasoning.Raw)
			if _, ok := reasoning["summary"]; !ok {
				reasoning["summary"] = "auto"
			}
			payload["reasoning"] = reasoning
		} else if req.Reasoning.Effort != "" || req.Reasoning.Summary != "" {
			reasoning := map[string]any{}
			if req.Reasoning.Effort != "" {
				reasoning["effort"] = req.Reasoning.Effort
			}
			if req.Reasoning.Summary != "" {
				reasoning["summary"] = req.Reasoning.Summary
			} else {
				reasoning["summary"] = "auto"
			}
			if len(reasoning) > 0 {
				payload["reasoning"] = reasoning
			}
		}
	}
	return json.Marshal(payload)
}

func joinTextParts(parts []model.CanonicalContentPart) string {
	var builder strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

func cloneMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(input))
	for k, v := range input {
		cloned[k] = v
	}
	return cloned
}

func textPartTypeForRole(role string) string {
	switch role {
	case "assistant":
		return "output_text"
	default:
		return "input_text"
	}
}

func normalizeJSONSchema(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(typed)+1)
		for key, nested := range typed {
			clone[key] = normalizeJSONSchema(nested)
		}
		if schemaType, _ := typed["type"].(string); schemaType == "array" {
			if _, ok := clone["items"]; !ok {
				clone["items"] = map[string]any{}
			}
		}
		return clone
	case []any:
		clone := make([]any, 0, len(typed))
		for _, nested := range typed {
			clone = append(clone, normalizeJSONSchema(nested))
		}
		return clone
	default:
		return value
	}
}

func hashBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%x", sum[:8])
}

func cachedTokensFromEvents(events []Event) any {
	for i := len(events) - 1; i >= 0; i-- {
		if cachedTokens := cachedTokensFromEvent(events[i]); cachedTokens != nil {
			return cachedTokens
		}
	}
	return nil
}

func cachedTokensFromEvent(evt Event) any {
	data := evt.Data
	if usage, _ := data["usage"].(map[string]any); len(usage) > 0 {
		if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
			if cachedTokens, ok := details["cached_tokens"]; ok {
				return cachedTokens
			}
		}
	}
	if response, _ := data["response"].(map[string]any); response != nil {
		if usage, _ := response["usage"].(map[string]any); len(usage) > 0 {
			if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
				if cachedTokens, ok := details["cached_tokens"]; ok {
					return cachedTokens
				}
			}
		}
	}
	return nil
}

func upstreamBodyLogAttrs(body []byte) map[string]any {
	attrs := map[string]any{}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		attrs["body_decode_error"] = err.Error()
		return attrs
	}
	if input, _ := payload["input"].([]any); len(input) > 0 {
		attrs["input_item_count"] = len(input)
		itemHashes := make([]string, 0, len(input))
		prefixHashes := make([]string, 0, len(input))
		itemKinds := make([]string, 0, len(input))
		for i := range input {
			itemHashes = append(itemHashes, hashAny(input[i]))
			prefixHashes = append(prefixHashes, hashAny(input[:i+1]))
			if item, _ := input[i].(map[string]any); item != nil {
				if role, _ := item["role"].(string); role != "" {
					itemKinds = append(itemKinds, "role:"+role)
				} else if itemType, _ := item["type"].(string); itemType != "" {
					itemKinds = append(itemKinds, "type:"+itemType)
				}
			}
		}
		attrs["input_item_hashes"] = itemHashes
		attrs["input_prefix_hashes"] = prefixHashes
		attrs["input_item_kinds"] = itemKinds
	}
	if reasoning, _ := payload["reasoning"].(map[string]any); len(reasoning) > 0 {
		attrs["reasoning_keys"] = sortedMapKeys(reasoning)
	}
	if tools, _ := payload["tools"].([]any); len(tools) > 0 {
		toolNames := make([]string, 0, len(tools))
		for _, raw := range tools {
			if tool, _ := raw.(map[string]any); tool != nil {
				if name, _ := tool["name"].(string); name != "" {
					toolNames = append(toolNames, name)
				}
			}
		}
		attrs["tool_names"] = toolNames
	}
	return attrs
}

func hashAny(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "marshal_error"
	}
	return hashBytes(b)
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func parseSSE(resp *http.Response) ([]Event, error) {
	var events []Event
	var currentEvent string
	var dataLines []string

	scanner := newSSEScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if currentEvent != "" {
				evt, err := finalizeEvent(currentEvent, dataLines)
				if err != nil {
					return nil, err
				}
				events = append(events, evt)
			}
			currentEvent = ""
			dataLines = nil
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if currentEvent != "" {
		evt, err := finalizeEvent(currentEvent, dataLines)
		if err != nil {
			return nil, err
		}
		events = append(events, evt)
	}

	return events, nil
}

func parseSSEStreaming(resp *http.Response, onEvent func(Event) error) error {
	var currentEvent string
	var dataLines []string

	scanner := newSSEScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if currentEvent != "" {
				evt, err := finalizeEvent(currentEvent, dataLines)
				if err != nil {
					return err
				}
				if err := onEvent(evt); err != nil {
					return err
				}
			}
			currentEvent = ""
			dataLines = nil
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	if currentEvent != "" {
		evt, err := finalizeEvent(currentEvent, dataLines)
		if err != nil {
			return err
		}
		if err := onEvent(evt); err != nil {
			return err
		}
	}

	return nil
}

func finalizeEvent(name string, dataLines []string) (Event, error) {
	raw := []byte(strings.Join(dataLines, "\n"))
	var parsed map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return Event{}, fmt.Errorf("parse event %s: %w", name, err)
		}
	}
	return Event{Event: name, Data: parsed, Raw: raw}, nil
}

func newSSEScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, sseScannerInitialBufferSize), sseScannerMaxTokenSize)
	return scanner
}
