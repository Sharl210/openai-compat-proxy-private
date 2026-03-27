package aggregate

import (
	"errors"
	"strings"

	"openai-compat-proxy/internal/upstream"
)

type ToolCall struct {
	ID        string
	Name      string
	CallID    string
	Arguments string
}

type Result struct {
	Text                    string
	ToolCalls               []ToolCall
	Reasoning               map[string]any
	Usage                   map[string]any
	ResponseOutputItems     []map[string]any
	ResponseMessageContent  []map[string]any
	UnsupportedContentTypes []string
}

type TerminalFailureError struct {
	HealthFlag string
	Message    string
}

func (e *TerminalFailureError) Error() string {
	if e == nil || e.Message == "" {
		return "terminal failure"
	}
	return e.Message
}

type Collector struct {
	text                    strings.Builder
	toolCalls               map[string]*ToolCall
	order                   []string
	reasoning               map[string]any
	responseMessageContent  []map[string]any
	outputItems             []map[string]any
	unsupportedContentTypes []string
	completed               bool
	terminalFailure         *TerminalFailureError
}

func NewCollector() *Collector {
	return &Collector{toolCalls: map[string]*ToolCall{}, reasoning: map[string]any{}}
}

func (c *Collector) Accept(evt upstream.Event) {
	switch evt.Event {
	case "response.output_text.delta":
		if delta, _ := evt.Data["delta"].(string); delta != "" {
			c.text.WriteString(delta)
		}
	case "response.function_call_arguments.delta":
		itemID, _ := evt.Data["item_id"].(string)
		delta, _ := evt.Data["delta"].(string)
		if itemID == "" {
			return
		}
		call, ok := c.toolCalls[itemID]
		if !ok {
			call = &ToolCall{ID: itemID}
			c.toolCalls[itemID] = call
			c.order = append(c.order, itemID)
		}
		call.Arguments += delta
	case "response.output_item.done":
		item, _ := evt.Data["item"].(map[string]any)
		if item == nil {
			return
		}
		c.outputItems = append(c.outputItems, cloneOutputItem(item))
		if itemType, _ := item["type"].(string); itemType == "message" {
			if content, ok := item["content"].([]any); ok {
				c.responseMessageContent = nil
				for _, rawPart := range content {
					part, _ := rawPart.(map[string]any)
					if part != nil {
						clone := map[string]any{}
						for k, v := range part {
							clone[k] = v
						}
						c.responseMessageContent = append(c.responseMessageContent, clone)
					}
					partType, _ := part["type"].(string)
					if partType != "" && partType != "output_text" {
						c.unsupportedContentTypes = append(c.unsupportedContentTypes, partType)
					}
				}
			}
		}
		if itemType, _ := item["type"].(string); itemType == "reasoning" {
			if summary := reasoningSummaryFromItem(item); summary != "" {
				c.reasoning["summary"] = stringValue(c.reasoning["summary"]) + summary
			}
			return
		}
		if itemType, _ := item["type"].(string); itemType != "function_call" {
			return
		}
		itemID, _ := item["id"].(string)
		if itemID == "" {
			return
		}
		call, ok := c.toolCalls[itemID]
		if !ok {
			call = &ToolCall{ID: itemID}
			c.toolCalls[itemID] = call
			c.order = append(c.order, itemID)
		}
		if name, _ := item["name"].(string); name != "" {
			call.Name = name
		}
		if callID, _ := item["call_id"].(string); callID != "" {
			call.CallID = callID
		}
		if arguments, _ := item["arguments"].(string); arguments != "" {
			call.Arguments = arguments
		}
	case "response.completed", "response.done":
		if usage := usageFromEventData(evt.Data); len(usage) > 0 {
			c.reasoning["usage"] = usage
		}
		c.completed = true
	case "response.incomplete":
		healthFlag, _ := evt.Data["health_flag"].(string)
		message, _ := evt.Data["message"].(string)
		if healthFlag == "" {
			healthFlag = "upstream_stream_broken"
		}
		if message == "" {
			message = "upstream response incomplete"
		}
		c.terminalFailure = &TerminalFailureError{HealthFlag: healthFlag, Message: message}
		c.completed = true
	case "response.reasoning.delta":
		for k, v := range evt.Data {
			if text, ok := v.(string); ok && shouldAppendReasoningKey(k) {
				c.reasoning[k] = stringValue(c.reasoning[k]) + text
				continue
			}
			c.reasoning[k] = v
		}
	}
}

func (c *Collector) Result() (Result, error) {
	if c.terminalFailure != nil {
		return Result{}, c.terminalFailure
	}
	if !c.completed {
		return Result{}, errors.New("stream did not complete")
	}

	result := Result{Text: c.text.String()}
	for _, id := range c.order {
		result.ToolCalls = append(result.ToolCalls, *c.toolCalls[id])
	}
	if len(c.reasoning) > 0 {
		result.Reasoning = c.reasoning
		if usage, _ := c.reasoning["usage"].(map[string]any); len(usage) > 0 {
			result.Usage = usage
		}
	}
	if len(c.outputItems) > 0 {
		result.ResponseOutputItems = append(result.ResponseOutputItems, c.outputItems...)
	}
	if len(c.responseMessageContent) > 0 {
		result.ResponseMessageContent = append(result.ResponseMessageContent, c.responseMessageContent...)
	}
	if len(c.unsupportedContentTypes) > 0 {
		result.UnsupportedContentTypes = append(result.UnsupportedContentTypes, c.unsupportedContentTypes...)
	}
	return result, nil
}

func cloneOutputItem(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(input))
	for k, v := range input {
		cloned[k] = v
	}
	return cloned
}

func shouldAppendReasoningKey(key string) bool {
	switch key {
	case "summary", "reasoning_content", "content", "delta":
		return true
	default:
		return false
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func reasoningSummaryFromItem(item map[string]any) string {
	parts, _ := item["summary"].([]any)
	if len(parts) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, rawPart := range parts {
		part, _ := rawPart.(map[string]any)
		if part == nil {
			continue
		}
		if text, _ := part["text"].(string); text != "" {
			builder.WriteString(text)
		}
	}
	return builder.String()
}

func usageFromEventData(data map[string]any) map[string]any {
	if usage, _ := data["usage"].(map[string]any); len(usage) > 0 {
		return usage
	}
	if response, _ := data["response"].(map[string]any); response != nil {
		if usage, _ := response["usage"].(map[string]any); len(usage) > 0 {
			return usage
		}
	}
	return nil
}
