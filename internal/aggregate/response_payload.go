package aggregate

import (
	"fmt"

	"openai-compat-proxy/internal/syntaxrepair"
	"openai-compat-proxy/internal/upstream"
)

func ResultFromResponsePayload(payload map[string]any) (Result, error) {
	if len(payload) == 0 {
		return Result{}, fmt.Errorf("empty upstream response payload")
	}
	result := Result{}
	if responseID, _ := payload["id"].(string); responseID != "" {
		result.ResponseID = responseID
	}
	// finish_reason takes priority; stop_reason is a fallback
	if finishReason, _ := payload["finish_reason"].(string); finishReason != "" {
		result.FinishReason = finishReason
	} else if stopReason, _ := payload["stop_reason"].(string); stopReason != "" {
		result.FinishReason = stopReason
	}
	if usage, _ := payload["usage"].(map[string]any); len(usage) > 0 {
		result.Usage = cloneMap(usage)
	}
	if reasoning, _ := payload["reasoning"].(map[string]any); len(reasoning) > 0 {
		result.Reasoning = cloneMap(reasoning)
	}
	if len(result.Usage) > 0 {
		if result.Reasoning == nil {
			result.Reasoning = map[string]any{}
		}
		result.Reasoning["usage"] = cloneMap(result.Usage)
	}
	output, _ := payload["output"].([]any)
	for _, rawItem := range output {
		item, _ := rawItem.(map[string]any)
		if item == nil {
			continue
		}
		if itemType, _ := item["type"].(string); itemType == "function_call" {
			if arguments, _ := item["arguments"].(string); arguments != "" {
				if repaired, ok := syntaxrepair.RepairJSON(arguments); ok {
					item = cloneMap(item)
					item["arguments"] = repaired
				}
			}
		}
		result.ResponseOutputItems = append(result.ResponseOutputItems, cloneMap(item))
		switch itemType, _ := item["type"].(string); itemType {
		case "message":
			content, _ := item["content"].([]any)
			for _, rawPart := range content {
				part, _ := rawPart.(map[string]any)
				if part == nil {
					continue
				}
				result.ResponseMessageContent = append(result.ResponseMessageContent, cloneMap(part))
				if partType, _ := part["type"].(string); partType == "output_text" {
					if text, _ := part["text"].(string); text != "" {
						result.Text += text
					}
				} else if partType == "refusal" {
					if refusal, _ := part["refusal"].(string); refusal != "" {
						result.Refusal += refusal
					}
				} else if partType != "" {
					result.UnsupportedContentTypes = append(result.UnsupportedContentTypes, partType)
				}
			}
		case "reasoning":
			if summary := reasoningSummaryFromItem(item); summary != "" {
				if result.Reasoning == nil {
					result.Reasoning = map[string]any{}
				}
				if existing := stringValue(result.Reasoning["summary"]); existing == "" {
					result.Reasoning["summary"] = summary
				}
			}
		case "function_call":
			call := ToolCall{}
			if id, _ := item["id"].(string); id != "" {
				call.ID = id
			}
			if callID, _ := item["call_id"].(string); callID != "" {
				call.CallID = callID
			}
			if name, _ := item["name"].(string); name != "" {
				call.Name = name
			}
			if arguments, _ := item["arguments"].(string); arguments != "" {
				if repaired, ok := syntaxrepair.RepairJSON(arguments); ok {
					arguments = repaired
				}
				call.Arguments = arguments
			}
			result.ToolCalls = append(result.ToolCalls, call)
		}
	}
	return result, nil
}

func cloneMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(input))
	for k, v := range input {
		cloned[k] = v
	}
	return cloned
}

// PayloadToSyntheticCanonicalEvents converts a non-stream upstream payload into
// synthetic upstream events so that the Collector can aggregate them the same way
// it aggregates real streaming events. Each emitted event carries
// ProviderMeta["synthetic"]=true to mark its synthetic origin.
func PayloadToSyntheticCanonicalEvents(payload map[string]any) []upstream.Event {
	if len(payload) == 0 {
		return nil
	}

	var events []upstream.Event
	syntheticMeta := map[string]any{"synthetic": true}

	responseID, _ := payload["id"].(string)
	if responseID != "" {
		events = append(events, upstream.Event{
			Event: "response.created",
			Data: map[string]any{
				"response":      map[string]any{"id": responseID},
				"provider_meta": syntheticMeta,
			},
		})
	}

	output, _ := payload["output"].([]any)
	for _, rawItem := range output {
		item, ok := rawItem.(map[string]any)
		if !ok || item == nil {
			continue
		}
		itemCopy := cloneMap(item)
		events = append(events, upstream.Event{
			Event: "response.output_item.done",
			Data: map[string]any{
				"item":          itemCopy,
				"provider_meta": syntheticMeta,
			},
		})
	}

	if reasoning, _ := payload["reasoning"].(map[string]any); len(reasoning) > 0 {
		events = append(events, upstream.Event{
			Event: "response.reasoning.delta",
			Data: map[string]any{
				"summary":       reasoning["summary"],
				"provider_meta": syntheticMeta,
			},
		})
	}

	finishReason := ""
	if fr, _ := payload["finish_reason"].(string); fr != "" {
		finishReason = fr
	} else if sr, _ := payload["stop_reason"].(string); sr != "" {
		finishReason = sr
	}
	usage, _ := payload["usage"].(map[string]any)

	respData := map[string]any{}
	if finishReason != "" {
		respData["finish_reason"] = finishReason
	}
	if usage != nil {
		respData["usage"] = cloneMap(usage)
	}

	completedData := map[string]any{
		"provider_meta": syntheticMeta,
	}
	if len(respData) > 0 {
		completedData["response"] = respData
	}
	if finishReason != "" {
		completedData["finish_reason"] = finishReason
	}
	if usage != nil {
		completedData["usage"] = cloneMap(usage)
	}

	events = append(events, upstream.Event{
		Event: "response.completed",
		Data:  completedData,
	})

	return events
}
