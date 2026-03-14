package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"openai-compat-proxy/internal/upstream"
)

func startSSE(w http.ResponseWriter) http.Flusher {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
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
	roleSent   bool
	toolMeta   map[string]map[string]string
	toolIndex  map[string]int
	toolSent   map[string]bool
	nextToolIx int
}

func writeChatSSE(w http.ResponseWriter, flusher http.Flusher, events []upstream.Event) error {
	state := chatStreamState{
		toolMeta:  map[string]map[string]string{},
		toolIndex: map[string]int{},
		toolSent:  map[string]bool{},
	}
	for _, evt := range events {
		switch evt.Event {
		case "response.output_item.added", "response.output_item.done":
			item, _ := evt.Data["item"].(map[string]any)
			if itemType, _ := item["type"].(string); itemType == "function_call" {
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
						if err := writeChatChunk(w, flusher, toolDelta, ""); err != nil {
							return err
						}
						state.toolSent[itemID] = true
					}
				}
			}
		case "response.output_text.delta":
			if !state.roleSent {
				if err := writeChatChunk(w, flusher, map[string]any{"role": "assistant"}, ""); err != nil {
					return err
				}
				state.roleSent = true
			}
			if delta := stringValue(evt.Data["delta"]); delta != "" {
				if err := writeChatChunk(w, flusher, map[string]any{"content": delta}, ""); err != nil {
					return err
				}
			}
		case "response.function_call_arguments.delta":
			itemID := stringValue(evt.Data["item_id"])
			delta := stringValue(evt.Data["delta"])
			index := state.toolIndex[itemID]
			toolDelta := chatToolDelta(index, "", "", delta, false)
			if err := writeChatChunk(w, flusher, toolDelta, ""); err != nil {
				return err
			}
		case "response.completed", "response.done":
			if err := writeChatChunk(w, flusher, map[string]any{}, "stop"); err != nil {
				return err
			}
			if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	return nil
}

func writeChatChunk(w http.ResponseWriter, flusher http.Flusher, delta map[string]any, finishReason string) error {
	chunk := map[string]any{
		"object": "chat.completion.chunk",
		"choices": []map[string]any{{
			"index": 0,
			"delta": delta,
		}},
	}
	if finishReason != "" {
		chunk["choices"].([]map[string]any)[0]["finish_reason"] = finishReason
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
