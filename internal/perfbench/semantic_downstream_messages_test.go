package perfbench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

type semanticMessagesUsage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

type semanticMessagesBlock struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Thinking string          `json:"thinking"`
	Input    json.RawMessage `json:"input"`
}

type semanticMessagesPayload struct {
	ID         string                  `json:"id"`
	Content    []semanticMessagesBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      semanticMessagesUsage   `json:"usage"`
}

type semanticMessagesStreamEnvelope struct {
	Type         string                  `json:"type"`
	Index        int                     `json:"index"`
	Message      semanticMessagesPayload `json:"message"`
	ContentBlock semanticMessagesBlock   `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage semanticMessagesUsage `json:"usage"`
}

type semanticMessagesStreamBlock struct {
	Block       semanticMessagesBlock
	PartialJSON string
}

func parseSemanticMessages(body []byte) (semanticDownstreamResult, error) {
	if !looksLikeSSE(body) {
		var payload semanticMessagesPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			return semanticDownstreamResult{}, fmt.Errorf("decode messages JSON: %w", err)
		}
		return semanticResultFromMessagesPayload(payload)
	}

	result := semanticDownstreamResult{Usage: map[string]int64{}}
	blocks := make(map[int]semanticMessagesStreamBlock)
	usage := semanticMessagesUsage{}
	for _, event := range parseSemanticSSE(body) {
		if bytes.Equal(event.Data, []byte("[DONE]")) {
			continue
		}
		var envelope semanticMessagesStreamEnvelope
		if err := json.Unmarshal(event.Data, &envelope); err != nil {
			return semanticDownstreamResult{}, fmt.Errorf("decode messages SSE event %q: %w", event.Event, err)
		}
		switch envelope.Type {
		case "message_start":
			result.ResponseID = envelope.Message.ID
			usage = envelope.Message.Usage
		case "content_block_start":
			blocks[envelope.Index] = semanticMessagesStreamBlock{Block: envelope.ContentBlock}
		case "content_block_delta":
			block := blocks[envelope.Index]
			block.Block.Thinking += envelope.Delta.Thinking
			block.PartialJSON += envelope.Delta.PartialJSON
			blocks[envelope.Index] = block
		case "message_delta":
			result.FinishReason = envelope.Delta.StopReason
			if envelope.Usage.OutputTokens != 0 {
				usage.OutputTokens = envelope.Usage.OutputTokens
			}
		}
	}

	indices := make([]int, 0, len(blocks))
	for index := range blocks {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		block := blocks[index]
		switch block.Block.Type {
		case "thinking":
			if block.Block.Thinking != "" {
				result.Reasoning = append(result.Reasoning, block.Block.Thinking)
			}
		case "tool_use":
			arguments, err := compactSemanticArguments([]byte(block.PartialJSON))
			if err != nil {
				return semanticDownstreamResult{}, fmt.Errorf("compact messages tool arguments: %w", err)
			}
			result.Tools = append(result.Tools, semanticTool{
				ID: block.Block.ID, Name: block.Block.Name, Arguments: arguments,
			})
		}
	}
	result.Usage = messagesUsageMap(usage)
	if result.ResponseID == "" {
		return semanticDownstreamResult{}, fmt.Errorf("messages stream has no message id")
	}
	return result, nil
}

func semanticResultFromMessagesPayload(payload semanticMessagesPayload) (semanticDownstreamResult, error) {
	if payload.ID == "" {
		return semanticDownstreamResult{}, fmt.Errorf("messages output has no message id")
	}
	result := semanticDownstreamResult{
		ResponseID: payload.ID, Usage: messagesUsageMap(payload.Usage), FinishReason: payload.StopReason,
	}
	for _, block := range payload.Content {
		switch block.Type {
		case "thinking":
			if block.Thinking != "" {
				result.Reasoning = append(result.Reasoning, block.Thinking)
			}
		case "tool_use":
			arguments, err := compactSemanticArguments(block.Input)
			if err != nil {
				return semanticDownstreamResult{}, fmt.Errorf("compact messages tool input: %w", err)
			}
			result.Tools = append(result.Tools, semanticTool{
				ID: block.ID, Name: block.Name, Arguments: arguments,
			})
		}
	}
	return result, nil
}

func compactSemanticArguments(arguments []byte) (string, error) {
	if len(bytes.TrimSpace(arguments)) == 0 {
		return "{}", nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, arguments); err != nil {
		return "", err
	}
	return compact.String(), nil
}

func messagesUsageMap(usage semanticMessagesUsage) map[string]int64 {
	return map[string]int64{
		"cache_creation_input_tokens": usage.CacheCreationInputTokens,
		"cache_read_input_tokens":     usage.CacheReadInputTokens,
		"input_tokens":                usage.InputTokens,
		"output_tokens":               usage.OutputTokens,
	}
}
