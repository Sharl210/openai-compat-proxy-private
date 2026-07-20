package aggregate

import (
	"errors"
	"sort"
	"strings"

	reasoningtext "openai-compat-proxy/internal/reasoning"
	"openai-compat-proxy/internal/syntaxrepair"
	"openai-compat-proxy/internal/upstream"
)

type ToolCall struct {
	ID        string
	Name      string
	CallID    string
	Arguments string
}

type Result struct {
	ResponseID              string
	ServiceTier             string
	Text                    string
	Refusal                 string
	FinishReason            string
	ToolCalls               []ToolCall
	Reasoning               map[string]any
	ReasoningBlocks         []map[string]any
	Usage                   map[string]any
	ResponseOutputItems     []map[string]any
	ResponseMessageContent  []map[string]any
	UnsupportedContentTypes []string
}

type TerminalFailureError struct {
	HealthFlag    string
	Message       string
	UpstreamError map[string]any
}

func (e *TerminalFailureError) Error() string {
	if e == nil || e.Message == "" {
		return "terminal failure"
	}
	return e.Message
}

type Collector struct {
	responseID              string
	text                    strings.Builder
	hasTextDelta            bool
	toolCalls               map[string]*ToolCall
	order                   []string
	reasoning               map[string]any
	serviceTier             string
	responseMessageContent  []map[string]any
	messageContentParts     map[string]map[int]map[string]any
	outputItems             []map[string]any
	unsupportedContentTypes []string
	refusal                 string
	finishReason            string
	completed               bool
	terminalFailure         *TerminalFailureError
}

func NewCollector() *Collector {
	return &Collector{
		toolCalls:           map[string]*ToolCall{},
		reasoning:           map[string]any{},
		messageContentParts: map[string]map[int]map[string]any{},
	}
}

func (c *Collector) Accept(evt upstream.Event) {
	if c.completed {
		return
	}
	switch evt.Event {
	case "response.created":
		if response, _ := evt.Data["response"].(map[string]any); response != nil {
			if responseID, _ := response["id"].(string); responseID != "" {
				c.responseID = responseID
			}
			if serviceTier, _ := response["service_tier"].(string); serviceTier != "" {
				c.serviceTier = serviceTier
			}
		}
	case "response.output_text.delta":
		if delta, _ := evt.Data["delta"].(string); delta != "" {
			c.text.WriteString(delta)
			c.hasTextDelta = true
		}
	case "response.content_part.done":
		c.recordMessageContentPart(evt.Data)
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
		for index := range c.outputItems {
			item := c.outputItems[index]
			if item == nil {
				continue
			}
			if itemType, _ := item["type"].(string); !isResponseToolCallItemType(itemType) {
				continue
			}
			if stringValue(item["id"]) != itemID && stringValue(item["call_id"]) != itemID {
				continue
			}
			item["arguments"] = stringValue(item["arguments"]) + delta
		}
	case "response.output_item.done":
		item, _ := evt.Data["item"].(map[string]any)
		if item == nil {
			return
		}
		outputItem := cloneMessageOutputItemForAggregation(item, c.text.String(), c.hasTextDelta)
		if itemType, _ := item["type"].(string); itemType == "message" {
			if itemID, _ := item["id"].(string); itemID != "" {
				outputItem = c.mergeCompletedMessageContentParts(outputItem, itemID)
			}
		}
		if itemType, _ := item["type"].(string); itemType == "reasoning" {
			outputItem = reasoningtext.FormatBlock(outputItem)
		}
		c.outputItems = append(c.outputItems, outputItem)
		if itemType, _ := item["type"].(string); itemType == "message" {
			if content, ok := outputItem["content"].([]any); ok {
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
					if partType == "refusal" {
						if refusal, _ := part["refusal"].(string); refusal != "" {
							c.refusal += refusal
						}
						continue
					}
					if partType == "output_text" {
						if !c.hasTextDelta {
							if text, _ := part["text"].(string); text != "" {
								c.text.WriteString(text)
							}
						}
						continue
					}
					if partType != "" {
						c.unsupportedContentTypes = append(c.unsupportedContentTypes, partType)
					}
				}
			}
		}
		if itemType, _ := item["type"].(string); itemType == "reasoning" {
			if c.reasoning == nil {
				c.reasoning = map[string]any{}
			}
			if _, ok := c.reasoning[InternalReasoningSourceKey]; !ok {
				c.reasoning[InternalReasoningSourceKey] = ReasoningSourceUpstream
			}
			if summary := reasoningSummaryFromItem(item); summary != "" {
				c.reasoning["summary"] = stringValue(c.reasoning["summary"]) + summary
			}
			return
		}
		if itemType, _ := item["type"].(string); !isResponseToolCallItemType(itemType) {
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
			if repaired, ok := syntaxrepair.RepairJSON(arguments); ok {
				arguments = repaired
				item["arguments"] = repaired
			}
			call.Arguments = arguments
		}
	case "response.completed", "response.done":
		if response, _ := evt.Data["response"].(map[string]any); response != nil {
			if fr, _ := response["finish_reason"].(string); fr != "" {
				c.finishReason = fr
			}
			if serviceTier, _ := response["service_tier"].(string); serviceTier != "" {
				c.serviceTier = serviceTier
			}
		}
		if serviceTier, _ := evt.Data["service_tier"].(string); serviceTier != "" {
			c.serviceTier = serviceTier
		}
		if fr, _ := evt.Data["finish_reason"].(string); fr != "" && c.finishReason == "" {
			c.finishReason = fr
		}
		if sr, _ := evt.Data["stop_reason"].(string); sr != "" && c.finishReason == "" {
			c.finishReason = sr
		}
		if usage := usageFromEventData(evt.Data); len(usage) > 0 {
			c.reasoning["usage"] = usage
		}
		c.completed = true
	case "response.incomplete":
		healthFlag, _ := evt.Data["health_flag"].(string)
		message, _ := evt.Data["message"].(string)
		var upstreamError map[string]any
		if errObj, _ := evt.Data["error"].(map[string]any); len(errObj) > 0 {
			upstreamError = cloneOutputItem(errObj)
		} else if response, _ := evt.Data["response"].(map[string]any); len(response) > 0 {
			if errObj, _ := response["error"].(map[string]any); len(errObj) > 0 {
				upstreamError = cloneOutputItem(errObj)
			}
		}
		if healthFlag == "" {
			healthFlag = "upstreamStreamBroken"
		}
		if message == "" {
			message = "upstream response incomplete"
		}
		c.terminalFailure = &TerminalFailureError{HealthFlag: healthFlag, Message: message, UpstreamError: upstreamError}
		c.completed = true
	case "error", "response.failed":
		healthFlag, _ := evt.Data["health_flag"].(string)
		message, _ := evt.Data["message"].(string)
		var upstreamError map[string]any
		if errObj, _ := evt.Data["error"].(map[string]any); len(errObj) > 0 {
			upstreamError = cloneOutputItem(errObj)
		} else if response, _ := evt.Data["response"].(map[string]any); len(response) > 0 {
			if errObj, _ := response["error"].(map[string]any); len(errObj) > 0 {
				upstreamError = cloneOutputItem(errObj)
			}
		}
		if healthFlag == "" && len(upstreamError) > 0 {
			healthFlag = stringValue(upstreamError["code"])
			if healthFlag == "" {
				healthFlag = stringValue(upstreamError["type"])
			}
		}
		if message == "" && len(upstreamError) > 0 {
			message = stringValue(upstreamError["message"])
		}
		if healthFlag == "" {
			healthFlag = "upstreamStreamBroken"
		}
		if message == "" {
			message = "upstream response incomplete"
		}
		c.terminalFailure = &TerminalFailureError{HealthFlag: healthFlag, Message: message, UpstreamError: upstreamError}
		c.completed = true
	case "response.reasoning.delta":
		if c.reasoning == nil {
			c.reasoning = map[string]any{}
		}
		if source, _ := evt.Data[InternalReasoningSourceKey].(string); source != "" {
			if source == ReasoningSourceSynthetic {
				return
			}
			c.reasoning[InternalReasoningSourceKey] = source
		} else {
			c.reasoning[InternalReasoningSourceKey] = ReasoningSourceUpstream
		}
		for k, v := range evt.Data {
			if k == "blocks" {
				rawBlocks, _ := v.([]any)
				if len(rawBlocks) == 0 {
					continue
				}
				existing, _ := c.reasoning[k].([]any)
				merged := append([]any(nil), existing...)
				for _, rawBlock := range rawBlocks {
					block, _ := rawBlock.(map[string]any)
					if len(block) == 0 {
						continue
					}
					merged = append(merged, cloneOutputItem(block))
				}
				if len(merged) > 0 {
					c.reasoning[k] = merged
				}
				continue
			}
			if text, ok := v.(string); ok && shouldAppendReasoningKey(k) {
				combined := stringValue(c.reasoning[k]) + text
				combined = reasoningtext.FormatText(combined)
				c.reasoning[k] = combined
				continue
			}
			c.reasoning[k] = v
		}
	}
}

func (c *Collector) recordMessageContentPart(data map[string]any) {
	itemID, _ := data["item_id"].(string)
	part, _ := data["part"].(map[string]any)
	if itemID == "" || part == nil {
		return
	}
	contentIndex := 0
	switch value := data["content_index"].(type) {
	case int:
		contentIndex = value
	case float64:
		contentIndex = int(value)
	}
	parts := c.messageContentParts[itemID]
	if parts == nil {
		parts = map[int]map[string]any{}
		c.messageContentParts[itemID] = parts
	}
	parts[contentIndex] = cloneOutputItem(part)
}

func (c *Collector) mergeCompletedMessageContentParts(item map[string]any, itemID string) map[string]any {
	content, _ := item["content"].([]any)
	if len(content) > 0 {
		return item
	}
	parts := c.messageContentParts[itemID]
	if len(parts) == 0 {
		return item
	}
	indexes := make([]int, 0, len(parts))
	for index := range parts {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	content = make([]any, 0, len(indexes))
	for _, index := range indexes {
		content = append(content, cloneOutputItem(parts[index]))
	}
	item["content"] = content
	return item
}

func (c *Collector) Result() (Result, error) {
	if c.terminalFailure != nil {
		return Result{}, c.terminalFailure
	}
	if !c.completed {
		return Result{}, errors.New("stream did not complete")
	}
	result := Result{}
	result.ResponseID = c.responseID
	result.ServiceTier = c.serviceTier
	result.Text = c.text.String()
	result.Refusal = c.refusal
	result.FinishReason = c.finishReason
	for _, id := range c.order {
		call := *c.toolCalls[id]
		if repaired, ok := syntaxrepair.RepairJSON(call.Arguments); ok {
			call.Arguments = repaired
		}
		result.ToolCalls = append(result.ToolCalls, call)
	}
	if len(c.reasoning) > 0 {
		result.Reasoning = c.reasoning
		result.ReasoningBlocks = cloneReasoningBlocks(reasoningBlocksFromMap(c.reasoning))
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
	case "summary", "thinking", "reasoning_content", "reasoning", "content", "delta", "text":
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
	return reasoningtext.FormatText(builder.String())
}

func reasoningBlocksFromMap(reasoning map[string]any) []map[string]any {
	if len(reasoning) == 0 {
		return nil
	}
	rawBlocks, _ := reasoning["blocks"].([]any)
	if len(rawBlocks) == 0 {
		return nil
	}
	blocks := make([]map[string]any, 0, len(rawBlocks))
	for _, rawBlock := range rawBlocks {
		block, _ := rawBlock.(map[string]any)
		if len(block) == 0 {
			continue
		}
		blocks = append(blocks, reasoningtext.FormatBlock(block))
	}
	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

func cloneReasoningBlocks(blocks []map[string]any) []map[string]any {
	if len(blocks) == 0 {
		return nil
	}
	cloned := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		if len(block) == 0 {
			continue
		}
		cloned = append(cloned, reasoningtext.FormatBlock(block))
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
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
