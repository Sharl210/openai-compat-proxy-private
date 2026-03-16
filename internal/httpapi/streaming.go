package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

func startSSE(w http.ResponseWriter) http.Flusher {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	return flusher
}

func writeResponsesSSE(w http.ResponseWriter, flusher http.Flusher, events []upstream.Event) error {
	for _, evt := range events {
		if _, err := fmt.Fprintf(w, "event: %s\n", evt.Event); err != nil {
			return err
		}
		if len(evt.Raw) > 0 {
			if _, err := fmt.Fprintf(w, "data: %s\n\n", evt.Raw); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprint(w, "data: {}\n\n"); err != nil {
				return err
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	return nil
}

type chatStreamState struct {
	roleSent          bool
	textStarted       bool
	realReasoningSeen bool
	planningSent      bool
	toolStatusSent    bool
	toolMeta          map[string]map[string]string
	toolIndex         map[string]int
	toolSent          map[string]bool
	nextToolIx        int
}

func writeChatSSELive(ctx context.Context, client *upstream.Client, w http.ResponseWriter, flusher http.Flusher, req model.CanonicalRequest, authorization string) error {
	state := chatStreamState{
		toolMeta:  map[string]map[string]string{},
		toolIndex: map[string]int{},
		toolSent:  map[string]bool{},
	}
	return client.StreamEvents(ctx, req, authorization, func(evt upstream.Event) error {
		return writeChatEvent(w, flusher, &state, evt, req.IncludeUsage)
	})
}

func writeChatSSE(w http.ResponseWriter, flusher http.Flusher, events []upstream.Event, includeUsage bool) error {
	state := chatStreamState{
		toolMeta:  map[string]map[string]string{},
		toolIndex: map[string]int{},
		toolSent:  map[string]bool{},
	}
	for _, evt := range events {
		if err := writeChatEvent(w, flusher, &state, evt, includeUsage); err != nil {
			return err
		}
	}
	return nil
}

func writeChatEvent(w http.ResponseWriter, flusher http.Flusher, state *chatStreamState, evt upstream.Event, includeUsage bool) error {
	switch evt.Event {
	case "response.created":
	case "response.output_item.added", "response.output_item.done":
		item, _ := evt.Data["item"].(map[string]any)
		if !state.textStarted {
			if phase := stringValue(item["phase"]); phase == "final_answer" && !state.planningSent && !state.realReasoningSeen {
				if err := writeChatChunk(w, flusher, syntheticReasoningStatus("正在组织回答…"), "", nil); err != nil {
					return err
				}
				state.planningSent = true
			}
		}
		if reasoningContent := reasoningSummaryFromItem(item); reasoningContent != "" {
			state.realReasoningSeen = true
			if err := writeChatChunk(w, flusher, map[string]any{"reasoning_content": reasoningContent}, "", nil); err != nil {
				return err
			}
		}
		if itemType, _ := item["type"].(string); itemType == "function_call" {
			if !state.textStarted && !state.realReasoningSeen && !state.toolStatusSent {
				if err := writeChatChunk(w, flusher, syntheticReasoningStatus("正在调用工具…"), "", nil); err != nil {
					return err
				}
				state.toolStatusSent = true
			}
			itemID, _ := item["id"].(string)
			if itemID != "" {
				if _, ok := state.toolIndex[itemID]; !ok {
					state.toolIndex[itemID] = state.nextToolIx
					state.nextToolIx++
				}
				state.toolMeta[itemID] = map[string]string{
					"name":    stringValue(item["name"]),
					"call_id": stringValue(item["call_id"]),
				}
				if !state.toolSent[itemID] {
					toolDelta := chatToolDelta(state.toolIndex[itemID], stringValue(item["call_id"]), stringValue(item["name"]), "", true)
					if err := writeChatChunk(w, flusher, toolDelta, "", nil); err != nil {
						return err
					}
					state.toolSent[itemID] = true
				}
			}
		}
	case "response.output_text.delta":
		state.textStarted = true
		if !state.roleSent {
			if err := writeChatChunk(w, flusher, map[string]any{"role": "assistant"}, "", nil); err != nil {
				return err
			}
			state.roleSent = true
		}
		if delta := stringValue(evt.Data["delta"]); delta != "" {
			if err := writeChatChunk(w, flusher, map[string]any{"content": delta}, "", nil); err != nil {
				return err
			}
		}
	case "response.reasoning.delta":
		if delta := reasoningContentValue(evt.Data); delta != "" {
			state.realReasoningSeen = true
			if err := writeChatChunk(w, flusher, map[string]any{"reasoning_content": delta}, "", nil); err != nil {
				return err
			}
		}
	case "response.function_call_arguments.delta":
		itemID := stringValue(evt.Data["item_id"])
		delta := stringValue(evt.Data["delta"])
		index := state.toolIndex[itemID]
		toolDelta := chatToolDelta(index, "", "", delta, false)
		if err := writeChatChunk(w, flusher, toolDelta, "", nil); err != nil {
			return err
		}
	case "response.completed", "response.done":
		cachedTokens := nestedCachedTokens(usageFromEventData(evt.Data))
		logging.Event("upstream_stream_usage_observed", map[string]any{
			"upstream_event":       evt.Event,
			"cached_tokens":        cachedTokens,
			"stream_include_usage": includeUsage,
		})
		if includeUsage {
			if usage := chatUsage(usageFromEventData(evt.Data)); usage != nil {
				logging.Event("downstream_stream_usage_mapped", map[string]any{
					"upstream_event": evt.Event,
					"cached_tokens":  nestedCachedTokens(mapUsageForLogging(usage)),
				})
				if err := writeChatChunk(w, flusher, nil, "", usage); err != nil {
					return err
				}
			}
		}
		if err := writeChatChunk(w, flusher, map[string]any{}, "stop", nil); err != nil {
			return err
		}
		if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	return nil
}

func mapUsageForLogging(usage any) map[string]any {
	mapped, _ := usage.(map[string]any)
	return mapped
}

func syntheticReasoningStatus(text string) map[string]any {
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return map[string]any{"reasoning_content": text}
}

func writeChatChunk(w http.ResponseWriter, flusher http.Flusher, delta map[string]any, finishReason string, usage any) error {
	chunk := map[string]any{"object": "chat.completion.chunk"}
	if delta == nil {
		chunk["choices"] = []any{}
	} else {
		chunk["choices"] = []map[string]any{{
			"index": 0,
			"delta": delta,
		}}
	}
	if finishReason != "" {
		chunk["choices"].([]map[string]any)[0]["finish_reason"] = finishReason
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", encoded); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func chatToolDelta(index int, callID, name, arguments string, includeMetadata bool) map[string]any {
	toolCall := map[string]any{
		"index": index,
		"function": map[string]any{
			"arguments": arguments,
		},
	}
	if includeMetadata {
		toolCall["id"] = callID
		toolCall["type"] = "function"
		toolCall["function"].(map[string]any)["name"] = name
	}
	return map[string]any{"tool_calls": []map[string]any{toolCall}}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func reasoningContentValue(data map[string]any) string {
	for _, key := range []string{"reasoning_content", "summary", "content", "delta"} {
		if text, _ := data[key].(string); text != "" {
			return text
		}
	}
	return ""
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

func chatUsage(usage map[string]any) any {
	if len(usage) == 0 {
		return nil
	}
	result := map[string]any{}
	if promptTokens, ok := usage["input_tokens"]; ok {
		result["prompt_tokens"] = promptTokens
	}
	if completionTokens, ok := usage["output_tokens"]; ok {
		result["completion_tokens"] = completionTokens
	}
	if totalTokens, ok := usage["total_tokens"]; ok {
		result["total_tokens"] = totalTokens
	}
	if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
		result["prompt_tokens_details"] = cloneMap(details)
	}
	if details, _ := usage["output_tokens_details"].(map[string]any); len(details) > 0 {
		result["completion_tokens_details"] = cloneMap(details)
	}
	if len(result) == 0 {
		return nil
	}
	return result
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
