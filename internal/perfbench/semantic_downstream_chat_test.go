package perfbench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

type semanticChatToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type semanticChatUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	PromptDetails    struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type semanticChatChoice struct {
	Message struct {
		ReasoningContent string                 `json:"reasoning_content"`
		ToolCalls        []semanticChatToolCall `json:"tool_calls"`
	} `json:"message"`
	Delta struct {
		ReasoningContent string                 `json:"reasoning_content"`
		ToolCalls        []semanticChatToolCall `json:"tool_calls"`
	} `json:"delta"`
	FinishReason string `json:"finish_reason"`
}

type semanticChatPayload struct {
	ID      string               `json:"id"`
	Choices []semanticChatChoice `json:"choices"`
	Usage   semanticChatUsage    `json:"usage"`
}

func parseSemanticChat(body []byte) (semanticDownstreamResult, error) {
	result := semanticDownstreamResult{Usage: map[string]int64{}}
	toolParts := make(map[int]semanticTool)
	frames := [][]byte{body}
	if looksLikeSSE(body) {
		frames = frames[:0]
		for _, event := range parseSemanticSSE(body) {
			if !bytes.Equal(event.Data, []byte("[DONE]")) {
				frames = append(frames, event.Data)
			}
		}
	}
	for _, frame := range frames {
		var payload semanticChatPayload
		if err := json.Unmarshal(frame, &payload); err != nil {
			return semanticDownstreamResult{}, fmt.Errorf("decode chat output: %w", err)
		}
		if payload.ID != "" {
			result.ResponseID = payload.ID
		}
		result.Usage = chatUsageMap(payload.Usage, result.Usage)
		for _, choice := range payload.Choices {
			if choice.Message.ReasoningContent != "" {
				result.Reasoning = []string{choice.Message.ReasoningContent}
			}
			if choice.Delta.ReasoningContent != "" {
				result.Reasoning = appendSemanticText(result.Reasoning, choice.Delta.ReasoningContent)
			}
			mergeSemanticChatTools(toolParts, choice.Message.ToolCalls)
			mergeSemanticChatTools(toolParts, choice.Delta.ToolCalls)
			if choice.FinishReason != "" {
				result.FinishReason = choice.FinishReason
			}
		}
	}
	indices := make([]int, 0, len(toolParts))
	for index := range toolParts {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		result.Tools = append(result.Tools, toolParts[index])
	}
	return result, nil
}

func mergeSemanticChatTools(parts map[int]semanticTool, calls []semanticChatToolCall) {
	for _, call := range calls {
		part := parts[call.Index]
		if call.ID != "" {
			part.ID = call.ID
		}
		part.Name += call.Function.Name
		part.Arguments += call.Function.Arguments
		parts[call.Index] = part
	}
}

func chatUsageMap(usage semanticChatUsage, previous map[string]int64) map[string]int64 {
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 && usage.TotalTokens == 0 {
		return previous
	}
	return map[string]int64{
		"cached_tokens":     usage.PromptDetails.CachedTokens,
		"completion_tokens": usage.CompletionTokens,
		"prompt_tokens":     usage.PromptTokens,
		"reasoning_tokens":  usage.CompletionDetails.ReasoningTokens,
		"total_tokens":      usage.TotalTokens,
	}
}
