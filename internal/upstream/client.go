package upstream

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/debugarchive"
	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/model"
)

type Client struct {
	baseURL                        string
	httpClient                     *http.Client
	retryCount                     int
	retryDelay                     time.Duration
	upstreamEndpointType           string
	anthropicVersion               string
	upstreamUserAgent              string
	masqueradeTarget               string
	injectClaudeCodeMetadataUserID bool
	injectClaudeCodeSystemPrompt   bool
	upstreamThinkingTagStyle       string
}

type EventStream struct {
	resp          *http.Response
	scanner       *bufio.Scanner
	pendingEvents []Event
	readNext      func(*bufio.Scanner) ([]Event, error)
	archive       *debugarchive.ArchiveWriter
	seq           int64
}

func (s *EventStream) FirstPendingResponseID() string {
	if s == nil {
		return ""
	}
	for _, evt := range s.pendingEvents {
		if evt.Event != "response.created" {
			continue
		}
		response, _ := evt.Data["response"].(map[string]any)
		if response == nil {
			continue
		}
		if id, _ := response["id"].(string); id != "" {
			return id
		}
	}
	return ""
}

var sseScannerInitialBufferSize = 64 * 1024
var sseScannerMaxTokenSize = 8 * 1024 * 1024

const preservedResponsesTopLevelFieldsKey = "__openai_compat_responses_top_level"

type HTTPStatusError struct {
	StatusCode       int
	ContentType      string
	BodyBytes        []byte
	Body             string
	RetriesPerformed int
	RetryDelay       time.Duration
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
		baseURL:                        strings.TrimRight(baseURL, "/"),
		httpClient:                     newHTTPClient(cfg),
		retryCount:                     cfg.UpstreamRetryCount,
		retryDelay:                     cfg.UpstreamRetryDelay,
		upstreamEndpointType:           normalizeEndpointType(cfg.UpstreamEndpointType),
		anthropicVersion:               strings.TrimSpace(cfg.AnthropicVersion),
		upstreamUserAgent:              strings.TrimSpace(cfg.UpstreamUserAgent),
		masqueradeTarget:               cfg.MasqueradeTarget,
		injectClaudeCodeMetadataUserID: cfg.InjectClaudeCodeMetadataUserID,
		injectClaudeCodeSystemPrompt:   cfg.InjectClaudeCodeSystemPrompt,
		upstreamThinkingTagStyle:       cfg.UpstreamThinkingTagStyle,
	}
}

func newHTTPClient(cfg config.Config) *http.Client {
	return &http.Client{Transport: newTransport(cfg)}
}

func (c *Client) configuredRetryCount() int {
	if c.retryCount < 0 {
		return 0
	}
	return c.retryCount
}

func (c *Client) configuredRetryDelay() time.Duration {
	if c.retryDelay < 0 {
		return 0
	}
	return c.retryDelay
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
	endpointType := c.endpointType()
	body, err := buildStreamingRequestBody(req, endpointType, c.masqueradeTarget, c.injectClaudeCodeMetadataUserID, c.injectClaudeCodeSystemPrompt)
	if err != nil {
		return nil, err
	}
	originalToolIDs := extractOriginalToolIDs(req)
	attrs := map[string]any{
		"request_id":    req.RequestID,
		"model":         req.Model,
		"endpoint_type": endpointType,
		"stream":        true,
		"body_size":     len(body),
		"body_preview":  previewBodyForLog(body),
		"tool_count":    len(req.Tools),
	}
	for k, v := range upstreamBodyLogAttrs(body) {
		attrs[k] = v
	}
	logging.Event("proxyToUpstreamRequest", attrs)
	stream, err := c.openEventStreamWithRetry(ctx, req.RequestID, endpointType, body, authorization, originalToolIDs, true)
	if err != nil {
		return nil, annotateRetryExhaustion(err, c.configuredRetryCount(), c.configuredRetryDelay())
	}
	defer stream.Close()
	var events []Event
	if err := stream.Consume(func(evt Event) error {
		events = append(events, evt)
		return nil
	}); err != nil {
		logging.Event("upstreamStreamBroken", mergeLogAttrs(map[string]any{
			"request_id":  req.RequestID,
			"streaming":   true,
			"event_count": len(events),
		}, failureLogAttrs(err, "upstreamStreamBroken")))
		return nil, err
	}
	cachedTokens := cachedTokensFromEvents(events)
	logging.Event("upstreamStreamUsageObserved", map[string]any{
		"request_id":     req.RequestID,
		"upstream_event": "response.completed",
		"cached_tokens":  cachedTokens,
		"streaming":      false,
	})
	logging.Event("upstreamToProxyResponse", map[string]any{
		"request_id":    req.RequestID,
		"attempt":       1,
		"event_count":   len(events),
		"cached_tokens": cachedTokens,
	})
	return events, nil
}

func (c *Client) StreamEvents(ctx context.Context, req model.CanonicalRequest, authorization string, onEvent func(Event) error) error {
	stream, err := c.OpenEventStream(ctx, req, authorization)
	if err != nil {
		return err
	}
	defer stream.Close()
	var eventCount int
	var cachedTokens any
	err = stream.Consume(func(evt Event) error {
		eventCount++
		if tokens := cachedTokensFromEvent(evt); tokens != nil {
			cachedTokens = tokens
			logging.Event("upstreamStreamUsageObserved", map[string]any{
				"request_id":     req.RequestID,
				"upstream_event": evt.Event,
				"cached_tokens":  tokens,
			})
		}
		return onEvent(evt)
	})
	if err != nil {
		logging.Event("upstreamStreamBroken", mergeLogAttrs(map[string]any{
			"request_id":  req.RequestID,
			"streaming":   true,
			"event_count": eventCount,
		}, failureLogAttrs(err, "upstreamStreamBroken")))
	}
	if err == nil {
		logging.Event("upstreamToProxyResponse", map[string]any{
			"request_id":    req.RequestID,
			"attempt":       1,
			"event_count":   eventCount,
			"cached_tokens": cachedTokens,
			"streaming":     true,
		})
	}
	return err
}

func (c *Client) OpenEventStream(ctx context.Context, req model.CanonicalRequest, authorization string) (*EventStream, error) {
	return c.openPreparedEventStream(ctx, req, authorization, true)
}

func (c *Client) OpenEventStreamLazy(ctx context.Context, req model.CanonicalRequest, authorization string) (*EventStream, error) {
	return c.openPreparedEventStream(ctx, req, authorization, false)
}

func (c *Client) openPreparedEventStream(ctx context.Context, req model.CanonicalRequest, authorization string, primeFirstEvent bool) (*EventStream, error) {
	endpointType := c.endpointType()
	body, err := buildStreamingRequestBody(req, endpointType, c.masqueradeTarget, c.injectClaudeCodeMetadataUserID, c.injectClaudeCodeSystemPrompt)
	if err != nil {
		return nil, err
	}
	originalToolIDs := extractOriginalToolIDs(req)
	attrs := map[string]any{
		"request_id":    req.RequestID,
		"auth_mode":     req.AuthMode,
		"model":         req.Model,
		"stream":        true,
		"body":          string(body),
		"body_probe":    "enabled",
		"body_preview":  previewBodyForLog(body),
		"body_hash":     hashBytes(body),
		"body_size":     len(body),
		"message_count": len(req.Messages),
		"tool_count":    len(req.Tools),
	}
	for k, v := range upstreamBodyLogAttrs(body) {
		attrs[k] = v
	}
	logging.Event("proxyToUpstreamRequest", attrs)
	stream, err := c.openEventStreamWithRetry(ctx, req.RequestID, endpointType, body, authorization, originalToolIDs, primeFirstEvent)
	if err != nil {
		return nil, annotateRetryExhaustion(err, c.configuredRetryCount(), c.configuredRetryDelay())
	}
	return stream, nil
}

func (c *Client) Response(ctx context.Context, req model.CanonicalRequest, authorization string) (map[string]any, error) {
	endpointType := c.endpointType()
	body, err := buildRequestBodyForEndpoint(req, endpointType, c.masqueradeTarget, c.injectClaudeCodeMetadataUserID, c.injectClaudeCodeSystemPrompt)
	if err != nil {
		return nil, err
	}
	attrs := map[string]any{
		"request_id":    req.RequestID,
		"model":         req.Model,
		"endpoint_type": endpointType,
		"stream":        true,
		"body_size":     len(body),
		"body_preview":  previewBodyForLog(body),
		"tool_count":    len(req.Tools),
	}
	for k, v := range upstreamBodyLogAttrs(body) {
		attrs[k] = v
	}
	logging.Event("proxyToUpstreamRequest", attrs)
	payload, err := c.responseWithRetry(ctx, req.RequestID, endpointType, body, authorization)
	if err != nil {
		return nil, annotateRetryExhaustion(err, c.configuredRetryCount(), c.configuredRetryDelay())
	}
	if archive := debugarchive.ArchiveWriterFromContext(ctx); archive != nil {
		_ = archive.WriteFinalSnapshot(debugarchive.FinalSnapshot{StatusCode: http.StatusOK, Response: payload})
	}
	logging.Event("upstreamToProxyResponse", map[string]any{
		"request_id": req.RequestID,
		"attempt":    1,
		"streaming":  false,
		"response":   payload,
	})
	return payload, nil
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

func (c *Client) streamEventsOnce(ctx context.Context, requestID string, body []byte, authorization string, onEvent func(Event) error) error {
	stream, err := c.openEventStreamWithRetry(ctx, requestID, c.endpointType(), body, authorization, nil, true)
	if err != nil {
		return err
	}
	defer stream.Close()
	return stream.Consume(onEvent)
}

func (c *Client) openEventStreamWithRetry(ctx context.Context, requestID string, endpointType string, body []byte, authorization string, originalToolIDs map[int]string, primeFirstEvent bool) (*EventStream, error) {
	retryCount := c.configuredRetryCount()
	retryDelay := c.configuredRetryDelay()
	var lastErr error
	for attempt := 1; attempt <= retryCount+1; attempt++ {
		stream, err := c.openEventStream(ctx, endpointType, body, authorization, originalToolIDs, requestID, primeFirstEvent)
		if err == nil {
			return stream, nil
		}
		lastErr = err
		if !shouldRetryRequestFailure(lastErr) || attempt > retryCount {
			logging.Event("upstreamRequestFailed", mergeLogAttrs(map[string]any{
				"request_id":         requestID,
				"attempt":            attempt,
				"retries_performed":  attempt - 1,
				"configured_retries": retryCount,
				"streaming":          true,
			}, failureLogAttrs(lastErr, classifyRequestFailure(lastErr))))
			break
		}
		logging.Event("upstreamRequestRetry", mergeLogAttrs(map[string]any{
			"request_id":         requestID,
			"attempt":            attempt,
			"next_attempt":       attempt + 1,
			"configured_retries": retryCount,
			"retry_delay":        retryDelay.String(),
			"streaming":          true,
		}, failureLogAttrs(lastErr, classifyRequestFailure(lastErr))))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if retryDelay > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay):
			}
		}
	}
	return nil, lastErr
}

func (c *Client) responseWithRetry(ctx context.Context, requestID string, endpointType string, body []byte, authorization string) (map[string]any, error) {
	retryCount := c.configuredRetryCount()
	retryDelay := c.configuredRetryDelay()
	var lastErr error
	for attempt := 1; attempt <= retryCount+1; attempt++ {
		payload, err := c.responseOnce(ctx, endpointType, body, authorization)
		if err == nil {
			return payload, nil
		}
		lastErr = err
		if !shouldRetryRequestFailure(lastErr) || attempt > retryCount {
			logging.Event("upstreamRequestFailed", mergeLogAttrs(map[string]any{
				"request_id":         requestID,
				"attempt":            attempt,
				"retries_performed":  attempt - 1,
				"configured_retries": retryCount,
				"streaming":          false,
			}, failureLogAttrs(lastErr, classifyRequestFailure(lastErr))))
			break
		}
		logging.Event("upstreamRequestRetry", mergeLogAttrs(map[string]any{
			"request_id":         requestID,
			"attempt":            attempt,
			"next_attempt":       attempt + 1,
			"configured_retries": retryCount,
			"retry_delay":        retryDelay.String(),
			"streaming":          false,
		}, failureLogAttrs(lastErr, classifyRequestFailure(lastErr))))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if retryDelay > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay):
			}
		}
	}
	return nil, lastErr
}

func (c *Client) responseOnce(ctx context.Context, endpointType string, body []byte, authorization string) (map[string]any, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpointPathForType(endpointType), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyUpstreamHeaders(httpReq, endpointType, authorization, c.anthropicVersion, c.upstreamUserAgent, c.masqueradeTarget)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, readHTTPStatusError(resp)
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return nil, err
	}
	return normalizeResponsePayload(endpointType, payload, c.upstreamThinkingTagStyle), nil
}

func (c *Client) openEventStream(ctx context.Context, endpointType string, body []byte, authorization string, originalToolIDs map[int]string, requestID string, primeFirstEvent bool) (*EventStream, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpointPathForType(endpointType), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyUpstreamHeaders(httpReq, endpointType, authorization, c.anthropicVersion, c.upstreamUserAgent, c.masqueradeTarget)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := readHTTPStatusError(resp)
		_ = resp.Body.Close()
		return nil, err
	}

	stream := &EventStream{resp: resp, scanner: newSSEScanner(resp.Body), readNext: eventBatchReaderForType(endpointType, c.upstreamThinkingTagStyle, originalToolIDs, requestID), archive: debugarchive.ArchiveWriterFromContext(ctx)}
	if primeFirstEvent {
		if err := stream.prime(); err != nil {
			_ = stream.Close()
			return nil, err
		}
	}
	return stream, nil
}

func (s *EventStream) Consume(onEvent func(Event) error) error {
	if s == nil || s.resp == nil {
		return nil
	}
	for len(s.pendingEvents) > 0 {
		evt := s.pendingEvents[0]
		s.pendingEvents = s.pendingEvents[1:]
		s.recordEvent(evt)
		if err := onEvent(evt); err != nil {
			return err
		}
	}
	return consumeSSEScannerWithReader(s.scanner, s.readNext, func(evt Event) error {
		s.recordEvent(evt)
		return onEvent(evt)
	})
}

func (s *EventStream) Close() error {
	if s == nil || s.resp == nil || s.resp.Body == nil {
		return nil
	}
	return s.resp.Body.Close()
}

func (s *EventStream) prime() error {
	if s == nil || s.scanner == nil {
		return nil
	}
	readNext := s.readNext
	if readNext == nil {
		readNext = readNextResponsesEventBatch
	}
	events, err := readNext(s.scanner)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return io.ErrUnexpectedEOF
	}
	s.pendingEvents = append(s.pendingEvents, events...)
	return nil
}

func (s *EventStream) recordEvent(evt Event) {
	if s == nil || s.archive == nil {
		return
	}
	_ = s.archive.WriteRawEvent(debugarchive.RawEventEnvelope{EventName: evt.Event, Raw: evt.Raw})
	s.seq++
	canonical := model.CanonicalEvent{
		Seq:          s.seq,
		Type:         evt.Event,
		RawPayload:   evt.Raw,
		ProviderMeta: cloneMap(anyMap(evt.Data["provider_meta"])),
	}
	_ = s.archive.WriteCanonicalEvent(canonical)
}

func readHTTPStatusError(resp *http.Response) *HTTPStatusError {
	bodyBytes, _ := io.ReadAll(resp.Body)
	msg := strings.TrimSpace(string(bodyBytes))
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
		bodyBytes = []byte(msg)
	}
	return &HTTPStatusError{
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		BodyBytes:   bodyBytes,
		Body:        msg,
	}
}

func shouldRetryRequestFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if httpErr, ok := err.(*HTTPStatusError); ok {
		return httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= 500
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusTooManyRequests || httpErr.StatusCode >= 500
	}
	return true
}

func annotateRetryExhaustion(err error, retryCount int, retryDelay time.Duration) error {
	if err == nil || retryCount <= 0 {
		return err
	}
	if !shouldRetryRequestFailure(err) {
		return err
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		cloned := *httpErr
		cloned.RetriesPerformed = retryCount
		cloned.RetryDelay = retryDelay
		return &cloned
	}
	return fmt.Errorf("%s%s", buildRetryNotice(retryCount, retryDelay), err.Error())
}

func mergeLogAttrs(base map[string]any, extra map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(extra))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func failureLogAttrs(err error, healthFlag string) map[string]any {
	attrs := map[string]any{
		"health_flag": healthFlag,
		"error":       err,
	}
	var httpErr *HTTPStatusError
	if errors.As(err, &httpErr) {
		attrs["status_code"] = httpErr.StatusCode
		attrs["content_type"] = httpErr.ContentType
	}
	return attrs
}

func classifyRequestFailure(err error) string {
	if isTimeoutError(err) {
		return "upstream_timeout"
	}
	return "upstream_error"
}

func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func buildRetryNotice(retryCount int, retryDelay time.Duration) string {
	if retryCount <= 0 {
		return ""
	}
	total := retryDelay * time.Duration(retryCount)
	return fmt.Sprintf("本代理层已重试%d遍，每次重试间隔%s，共重试了%s。下面是上游错误原信息：", retryCount, formatRetrySeconds(retryDelay), formatRetrySeconds(total))
}

func formatRetrySeconds(delay time.Duration) string {
	seconds := delay.Seconds()
	if seconds == float64(int64(seconds)) {
		return fmt.Sprintf("%d秒", int64(seconds))
	}
	text := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", seconds), "0"), ".")
	return text + "秒"
}

func buildRequestBody(req model.CanonicalRequest) ([]byte, error) {
	payload := map[string]any{
		"model":  req.Model,
		"stream": req.Stream,
	}
	preservedTopLevelFields, responseInputItems := splitPreservedResponsesTopLevelFields(req.ResponseInputItems)
	for key, value := range preservedTopLevelFields {
		payload[key] = cloneJSONValue(value)
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if req.MaxOutputTokens != nil {
		payload["max_output_tokens"] = *req.MaxOutputTokens
	}
	if len(req.Stop) > 0 {
		payload["stop"] = append([]string(nil), req.Stop...)
	}
	if req.ResponseStore != nil {
		payload["store"] = *req.ResponseStore
	}
	if len(req.ResponseInclude) > 0 {
		payload["include"] = append([]string(nil), req.ResponseInclude...)
	}
	if req.IncludeUsage {
		includeList, _ := payload["include"].([]string)
		hasUsage := false
		for _, v := range includeList {
			if v == "usage" {
				hasUsage = true
				break
			}
		}
		if !hasUsage {
			payload["include"] = append(includeList, "usage")
		}
	}
	if req.Instructions != "" {
		payload["instructions"] = req.Instructions
	}
	if len(responseInputItems) > 0 {
		input := make([]map[string]any, 0, len(responseInputItems))
		for _, item := range responseInputItems {
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
					"output":  buildToolOutput(msg.Parts),
				})
				continue
			}

			if reasoningItem := buildReasoningInputItem(msg); len(reasoningItem) > 0 {
				input = append(input, reasoningItem)
			}

			item := map[string]any{"role": msg.Role}
			var content []map[string]any
			for _, part := range msg.Parts {
				switch part.Type {
				case "text":
					content = append(content, map[string]any{"type": textPartTypeForRole(msg.Role), "text": part.Text})
				case "image_url", "input_image":
					content = append(content, buildInputImageContent(part))
				case "input_file":
					if rawFile, ok := part.Raw["input_file"].(map[string]any); ok && len(rawFile) > 0 {
						content = append(content, map[string]any{"type": "input_file", "input_file": cloneMap(rawFile)})
					}
				case "input_audio":
					if rawAudio, ok := part.Raw["input_audio"].(map[string]any); ok && len(rawAudio) > 0 {
						content = append(content, map[string]any{"type": "input_audio", "input_audio": cloneMap(rawAudio)})
					}
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
	if req.ToolChoice.Raw != nil {
		if value, ok := req.ToolChoice.Raw["value"]; ok {
			payload["tool_choice"] = value
		} else {
			payload["tool_choice"] = cloneMap(req.ToolChoice.Raw)
		}
	} else if req.ToolChoice.Mode != "" {
		payload["tool_choice"] = req.ToolChoice.Mode
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

func buildReasoningInputItem(msg model.CanonicalMessage) map[string]any {
	if msg.Role != "assistant" || msg.ReasoningContent == "" {
		return nil
	}
	return map[string]any{
		"type": "reasoning",
		"summary": []map[string]any{{
			"type": "summary_text",
			"text": msg.ReasoningContent,
		}},
	}
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

func buildToolOutput(parts []model.CanonicalContentPart) any {
	structured := normalizeContentParts(parts)
	if len(structured) == 0 {
		return ""
	}
	allText := true
	for _, part := range structured {
		if part["type"] != "input_text" {
			allText = false
			break
		}
	}
	if allText {
		var builder strings.Builder
		for _, part := range structured {
			if text, _ := part["text"].(string); text != "" {
				builder.WriteString(text)
			}
		}
		return builder.String()
	}
	encoded, err := json.Marshal(structured)
	if err != nil {
		return joinTextParts(parts)
	}
	return string(encoded)
}

func normalizeContentParts(parts []model.CanonicalContentPart) []map[string]any {
	content := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			content = append(content, map[string]any{"type": "input_text", "text": part.Text})
		case "image_url", "input_image":
			content = append(content, buildInputImageContent(part))
		case "input_file":
			if rawFile, ok := part.Raw["input_file"].(map[string]any); ok && len(rawFile) > 0 {
				content = append(content, map[string]any{"type": "input_file", "input_file": cloneMap(rawFile)})
			}
		case "input_audio":
			if rawAudio, ok := part.Raw["input_audio"].(map[string]any); ok && len(rawAudio) > 0 {
				content = append(content, map[string]any{"type": "input_audio", "input_audio": cloneMap(rawAudio)})
			}
		}
	}
	return content
}

func buildInputImageContent(part model.CanonicalContentPart) map[string]any {
	entry := map[string]any{"type": "input_image"}
	if rawImage, ok := part.Raw["image_url"].(map[string]any); ok && len(rawImage) > 0 {
		image := cloneMap(rawImage)
		if fileID, _ := image["file_id"].(string); fileID != "" {
			entry["file_id"] = fileID
			delete(image, "file_id")
			for key, value := range image {
				entry[key] = value
			}
			return entry
		}
		url, _ := image["url"].(string)
		if url == "" {
			url = part.ImageURL
		}
		if url != "" {
			entry["image_url"] = url
			delete(image, "url")
			for key, value := range image {
				entry[key] = value
			}
			return entry
		}
		entry["image_url"] = image
		return entry
	}
	entry["image_url"] = part.ImageURL
	return entry
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

// anyMap safely converts an any value to map[string]any.
func anyMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func splitPreservedResponsesTopLevelFields(items []map[string]any) (map[string]any, []map[string]any) {
	if len(items) == 0 {
		return nil, nil
	}
	fields := map[string]any{}
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		preserved, ok := item[preservedResponsesTopLevelFieldsKey].(map[string]any)
		if !ok {
			filtered = append(filtered, item)
			continue
		}
		for key, value := range preserved {
			fields[key] = value
		}
	}
	if len(fields) == 0 {
		return nil, filtered
	}
	return fields, filtered
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(typed))
		for key, nested := range typed {
			cloned[key] = cloneJSONValue(nested)
		}
		return cloned
	case []any:
		cloned := make([]any, 0, len(typed))
		for _, nested := range typed {
			cloned = append(cloned, cloneJSONValue(nested))
		}
		return cloned
	default:
		return value
	}
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
				if fn, _ := tool["function"].(map[string]any); fn != nil {
					if name, _ := fn["name"].(string); name != "" {
						toolNames = append(toolNames, name)
						continue
					}
				}
				if name, _ := tool["name"].(string); name != "" {
					toolNames = append(toolNames, name)
				}
			}
		}
		attrs["tool_names"] = toolNames
	}
	return attrs
}

func previewBodyForLog(body []byte) string {
	const max = 512
	if len(body) <= max {
		return string(body)
	}
	return string(body[:max])
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
	scanner := newSSEScanner(resp.Body)
	if err := consumeSSEScanner(scanner, func(evt Event) error {
		events = append(events, evt)
		return nil
	}); err != nil {
		return nil, err
	}
	return events, nil
}

func parseSSEStreaming(resp *http.Response, onEvent func(Event) error) error {
	return consumeSSEScanner(newSSEScanner(resp.Body), onEvent)
}

func consumeSSEScanner(scanner *bufio.Scanner, onEvent func(Event) error) error {
	for {
		evt, err := readNextSSEEvent(scanner)
		if err != nil {
			return err
		}
		if evt == nil {
			return nil
		}
		if err := onEvent(*evt); err != nil {
			return err
		}
	}
}

func readNextSSEEvent(scanner *bufio.Scanner) (*Event, error) {
	var currentEvent string
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if currentEvent != "" {
				evt, err := finalizeEvent(currentEvent, dataLines)
				if err != nil {
					return nil, err
				}
				return &evt, nil
			}
			currentEvent = ""
			dataLines = nil
			continue
		}

		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}

		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
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
		return &evt, nil
	}

	return nil, nil
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
