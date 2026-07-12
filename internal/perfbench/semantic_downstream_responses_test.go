package perfbench

import (
	"bytes"
	"encoding/json"
	"fmt"
)

type semanticResponsesPayload struct {
	ID           string                    `json:"id"`
	Status       string                    `json:"status"`
	FinishReason string                    `json:"finish_reason"`
	Output       []semanticResponsesOutput `json:"output"`
	Usage        semanticResponsesUsage    `json:"usage"`
	Reasoning    struct {
		ReasoningContent string `json:"reasoning_content"`
		Summary          string `json:"summary"`
	} `json:"reasoning"`
}

type semanticResponsesOutput struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Summary   []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

type semanticResponsesUsage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
	InputDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func parseSemanticResponses(body []byte) (semanticDownstreamResult, error) {
	payload := semanticResponsesPayload{}
	var eventReasoning []string
	if looksLikeSSE(body) {
		for _, event := range parseSemanticSSE(body) {
			if bytes.Equal(event.Data, []byte("[DONE]")) {
				continue
			}
			var envelope struct {
				Type     string                   `json:"type"`
				Delta    string                   `json:"delta"`
				Item     semanticResponsesOutput  `json:"item"`
				Response semanticResponsesPayload `json:"response"`
			}
			if err := json.Unmarshal(event.Data, &envelope); err != nil {
				return semanticDownstreamResult{}, fmt.Errorf("decode responses SSE event %q: %w", event.Event, err)
			}
			if envelope.Response.ID != "" {
				payload = envelope.Response
			}
			if envelope.Type == "response.reasoning_summary_text.delta" && envelope.Delta != "" {
				eventReasoning = appendSemanticText(eventReasoning, envelope.Delta)
			}
			if envelope.Item.Type == "reasoning" && len(envelope.Item.Summary) > 0 {
				eventReasoning = eventReasoning[:0]
				for _, summary := range envelope.Item.Summary {
					if summary.Text != "" {
						eventReasoning = append(eventReasoning, summary.Text)
					}
				}
			}
		}
	} else if err := json.Unmarshal(body, &payload); err != nil {
		return semanticDownstreamResult{}, fmt.Errorf("decode responses JSON: %w", err)
	}
	if payload.ID == "" {
		return semanticDownstreamResult{}, fmt.Errorf("responses output has no response id")
	}

	result := semanticDownstreamResult{
		ResponseID:     payload.ID,
		Usage:          responsesUsageMap(payload.Usage),
		FinishReason:   payload.FinishReason,
		TerminalStatus: payload.Status,
	}
	for _, output := range payload.Output {
		switch output.Type {
		case "reasoning":
			for _, summary := range output.Summary {
				if summary.Text != "" {
					result.Reasoning = append(result.Reasoning, summary.Text)
				}
			}
		case "function_call":
			result.Tools = append(result.Tools, semanticTool{
				ID: output.CallID, Name: output.Name, Arguments: output.Arguments,
			})
		}
	}
	if len(result.Reasoning) == 0 && payload.Reasoning.ReasoningContent != "" {
		result.Reasoning = []string{payload.Reasoning.ReasoningContent}
	}
	if len(result.Reasoning) == 0 && payload.Reasoning.Summary != "" {
		result.Reasoning = []string{payload.Reasoning.Summary}
	}
	if len(result.Reasoning) == 0 {
		result.Reasoning = eventReasoning
	}
	return result, nil
}

func responsesUsageMap(usage semanticResponsesUsage) map[string]int64 {
	return map[string]int64{
		"cached_tokens":    usage.InputDetails.CachedTokens,
		"input_tokens":     usage.InputTokens,
		"output_tokens":    usage.OutputTokens,
		"reasoning_tokens": usage.OutputDetails.ReasoningTokens,
		"total_tokens":     usage.TotalTokens,
	}
}
