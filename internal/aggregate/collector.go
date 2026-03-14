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
	ResponseMessageContent  []map[string]any
	UnsupportedContentTypes []string
}

type Collector struct {
	text                    strings.Builder
	toolCalls               map[string]*ToolCall
	order                   []string
	reasoning               map[string]any
	responseMessageContent  []map[string]any
	unsupportedContentTypes []string
	completed               bool
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
		c.completed = true
	case "response.reasoning.delta":
		for k, v := range evt.Data {
			c.reasoning[k] = v
		}
	}
}

func (c *Collector) Result() (Result, error) {
	if !c.completed {
		return Result{}, errors.New("stream did not complete")
	}

	result := Result{Text: c.text.String()}
	for _, id := range c.order {
		result.ToolCalls = append(result.ToolCalls, *c.toolCalls[id])
	}
	if len(c.reasoning) > 0 {
		result.Reasoning = c.reasoning
	}
	if len(c.responseMessageContent) > 0 {
		result.ResponseMessageContent = append(result.ResponseMessageContent, c.responseMessageContent...)
	}
	if len(c.unsupportedContentTypes) > 0 {
		result.UnsupportedContentTypes = append(result.UnsupportedContentTypes, c.unsupportedContentTypes...)
	}
	return result, nil
}
