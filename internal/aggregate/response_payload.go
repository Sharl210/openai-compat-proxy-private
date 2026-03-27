package aggregate

import "fmt"

func ResultFromResponsePayload(payload map[string]any) (Result, error) {
	if len(payload) == 0 {
		return Result{}, fmt.Errorf("empty upstream response payload")
	}
	result := Result{}
	if usage, _ := payload["usage"].(map[string]any); len(usage) > 0 {
		result.Usage = cloneMap(usage)
	}
	if reasoning, _ := payload["reasoning"].(map[string]any); len(reasoning) > 0 {
		result.Reasoning = cloneMap(reasoning)
	}
	output, _ := payload["output"].([]any)
	for _, rawItem := range output {
		item, _ := rawItem.(map[string]any)
		if item == nil {
			continue
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
				} else if partType != "" {
					result.UnsupportedContentTypes = append(result.UnsupportedContentTypes, partType)
				}
			}
		case "reasoning":
			if summary := reasoningSummaryFromItem(item); summary != "" {
				if result.Reasoning == nil {
					result.Reasoning = map[string]any{}
				}
				result.Reasoning["summary"] = stringValue(result.Reasoning["summary"]) + summary
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
