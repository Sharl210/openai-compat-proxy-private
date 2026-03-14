package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"openai-compat-proxy/internal/model"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type HTTPStatusError struct {
	StatusCode int
	Body       string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("upstream status %d: %s", e.StatusCode, e.Body)
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{},
	}
}

func (c *Client) Stream(ctx context.Context, req model.CanonicalRequest, authorization string) ([]Event, error) {
	body, err := buildRequestBody(req)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		events, err := c.streamOnce(ctx, body, authorization)
		if err == nil {
			return events, nil
		}
		lastErr = err

		if !shouldRetry(lastErr) || attempt == 3 {
			break
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
		}
	}

	return nil, lastErr
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
	if len(req.Messages) > 0 {
		var input []map[string]any
		for _, msg := range req.Messages {
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
			item["content"] = content
			input = append(input, item)
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
				"parameters":  tool.Parameters,
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
	if req.Reasoning != nil && req.Reasoning.Effort != "" {
		payload["reasoning"] = map[string]any{"effort": req.Reasoning.Effort}
	}
	return json.Marshal(payload)
}

func textPartTypeForRole(role string) string {
	switch role {
	case "assistant":
		return "output_text"
	default:
		return "input_text"
	}
}

func parseSSE(resp *http.Response) ([]Event, error) {
	var events []Event
	var currentEvent string
	var dataLines []string

	scanner := bufio.NewScanner(resp.Body)
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
