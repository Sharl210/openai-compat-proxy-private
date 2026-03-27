package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

const initialSyntheticReasoningLeadTime = 350 * time.Millisecond

var syntheticReasoningTickInterval = 250 * time.Millisecond
var sseHeartbeatInterval = 15 * time.Second

const syntheticReasoningPlaceholder = "**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长"

type anthropicStreamState struct {
	messageStarted   bool
	messageID        string
	modelName        string
	textStarted      bool
	textIndex        int
	textStopped      bool
	thinkingStarted  bool
	thinkingStopped  bool
	signatureSent    bool
	thinkingIndex    int
	toolStarted      bool
	toolIndex        int
	toolStopped      bool
	stopReason       string
	nextIndex        int
	realThinkingSeen bool
	planningSent     bool
	toolStatusSent   bool
	toolItemID       string
	toolDeltaSent    bool
	pendingToolArgs  map[string]string
	terminalSeen     bool
	terminalFailure  *aggregate.TerminalFailureError
}

type responsesStreamState struct {
	textStarted       bool
	realReasoningSeen bool
	planningSent      bool
	toolStatusSent    bool
	reasoningStarted  bool
	reasoningClosed   bool
	syntheticSummary  strings.Builder
	terminalSeen      bool
	terminalFailure   *aggregate.TerminalFailureError
}

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
		payload, err := responseStreamPayload(evt.Event, evt.Data)
		if err != nil {
			return err
		}
		if len(payload) > 0 {
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
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

func writeResponsesTerminalFailure(w http.ResponseWriter, flusher http.Flusher, requestID string, healthFlag string, message string) error {
	if _, err := fmt.Fprint(w, "event: response.incomplete\n"); err != nil {
		return err
	}
	payload, err := responseStreamPayload("response.incomplete", map[string]any{
		"type":        "response.incomplete",
		"request_id":  requestID,
		"health_flag": healthFlag,
		"message":     message,
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func writeResponsesSSELive(ctx context.Context, stream *upstream.EventStream, w http.ResponseWriter, flusher http.Flusher, req model.CanonicalRequest) error {
	state := responsesStreamState{}
	if err := writeSyntheticResponsesReasoningWithState(w, flusher, &state, syntheticReasoningPlaceholder); err != nil {
		return err
	}
	if err := waitSyntheticLeadTime(ctx); err != nil {
		return err
	}
	err := streamLiveWithSyntheticTicks(ctx, stream.Consume,
		func() bool { return true },
		nil,
		func() error { return writeSSEComment(w, flusher, "keep-alive") },
		func(evt upstream.Event) error {
			return writeResponsesEvent(w, flusher, &state, evt)
		},
	)
	if err != nil {
		return err
	}
	if !state.terminalSeen {
		return io.ErrUnexpectedEOF
	}
	if state.terminalFailure != nil {
		return state.terminalFailure
	}
	return nil
}

func writeResponsesEvent(w http.ResponseWriter, flusher http.Flusher, state *responsesStreamState, evt upstream.Event) error {
	item, _ := evt.Data["item"].(map[string]any)
	writeStreamEvent := func(event string, data map[string]any) error {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
		payload, err := responseStreamPayload(event, data)
		if err != nil {
			return err
		}
		if len(payload) > 0 {
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
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
		return nil
	}
	closeSyntheticReasoning := func() error {
		if !state.reasoningStarted || state.reasoningClosed {
			return nil
		}
		summary := []any{}
		if text := state.syntheticSummary.String(); text != "" {
			summary = append(summary, map[string]any{"type": "summary_text", "text": text})
		}
		payload, err := responseStreamPayload("response.output_item.done", map[string]any{
			"item": map[string]any{
				"id":      "rs_proxy",
				"type":    "reasoning",
				"summary": summary,
			},
		})
		if err != nil {
			return err
		}
		if _, err := fmt.Fprint(w, "event: response.output_item.done\n"); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		state.reasoningClosed = true
		return nil
	}
	markRealReasoningSeen := func() error {
		if state.realReasoningSeen {
			return nil
		}
		state.realReasoningSeen = true
		return closeSyntheticReasoning()
	}
	switch evt.Event {
	case "response.output_item.added", "response.output_item.done":
		if itemType, _ := item["type"].(string); itemType == "reasoning" {
			if err := markRealReasoningSeen(); err != nil {
				return err
			}
		}
	case "response.output_text.delta":
		if err := closeSyntheticReasoning(); err != nil {
			return err
		}
		state.textStarted = true
	case "response.reasoning.delta", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		if err := markRealReasoningSeen(); err != nil {
			return err
		}
	case "response.completed", "response.done":
		state.terminalSeen = true
		if err := closeSyntheticReasoning(); err != nil {
			return err
		}
	case "response.incomplete":
		state.terminalSeen = true
		healthFlag, _ := evt.Data["health_flag"].(string)
		message, _ := evt.Data["message"].(string)
		if healthFlag == "" {
			healthFlag = "upstream_stream_broken"
		}
		if message == "" {
			message = "upstream response incomplete"
		}
		state.terminalFailure = &aggregate.TerminalFailureError{HealthFlag: healthFlag, Message: message}
		if err := closeSyntheticReasoning(); err != nil {
			return err
		}
	}
	return writeStreamEvent(evt.Event, evt.Data)
}

func writeSyntheticResponsesReasoning(w http.ResponseWriter, flusher http.Flusher, text string) error {
	return writeSyntheticResponsesReasoningWithState(w, flusher, nil, text)
}

func writeSyntheticResponsesReasoningWithState(w http.ResponseWriter, flusher http.Flusher, state *responsesStreamState, text string) error {
	if state != nil && !state.reasoningStarted {
		payload, err := responseStreamPayload("response.output_item.added", map[string]any{
			"item": map[string]any{
				"id":      "rs_proxy",
				"type":    "reasoning",
				"summary": []any{},
			},
		})
		if err != nil {
			return err
		}
		if _, err := fmt.Fprint(w, "event: response.output_item.added\n"); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		state.reasoningStarted = true
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if state != nil {
		state.syntheticSummary.WriteString(text)
	}
	payload := map[string]any{"type": "response.reasoning.delta", "summary": text}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, "event: response.reasoning.delta\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", encoded); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	summaryPayload := map[string]any{"type": "response.reasoning_summary_text.delta", "delta": text}
	encodedSummary, err := json.Marshal(summaryPayload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, "event: response.reasoning_summary_text.delta\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", encodedSummary); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func responseStreamPayload(event string, data map[string]any) ([]byte, error) {
	if len(data) == 0 {
		return json.Marshal(map[string]any{"type": event})
	}
	clone := make(map[string]any, len(data)+1)
	for k, v := range data {
		clone[k] = v
	}
	if _, ok := clone["type"]; !ok {
		clone["type"] = event
	}
	return json.Marshal(clone)
}

func writeAnthropicSSELive(ctx context.Context, stream *upstream.EventStream, w http.ResponseWriter, flusher http.Flusher, req model.CanonicalRequest, state *anthropicStreamState) error {
	if state == nil {
		state = &anthropicStreamState{}
	}
	state.messageID = req.RequestID
	state.modelName = req.Model
	if state.pendingToolArgs == nil {
		state.pendingToolArgs = map[string]string{}
	}
	if err := writeSSEPadding(w, flusher); err != nil {
		return err
	}
	if err := startAnthropicUnreasonedPlaceholder(w, flusher, state); err != nil {
		return err
	}
	if err := waitSyntheticLeadTime(ctx); err != nil {
		return err
	}
	err := streamLiveWithSyntheticTicks(ctx, stream.Consume,
		func() bool { return state.textStarted || state.realThinkingSeen },
		func() error {
			if state.textStarted || state.realThinkingSeen {
				return nil
			}
			if err := startAnthropicUnreasonedPlaceholder(w, flusher, state); err != nil {
				return err
			}
			return writeAnthropicSSEEvent(w, flusher, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": state.thinkingIndex,
				"delta": map[string]any{"type": "thinking_delta", "thinking": "\u200b"},
			})
		},
		func() error { return writeSSEComment(w, flusher, "keep-alive") },
		func(evt upstream.Event) error {
			return writeAnthropicEvent(w, flusher, state, evt)
		},
	)
	if err != nil {
		return err
	}
	if !state.terminalSeen {
		return io.ErrUnexpectedEOF
	}
	if state.terminalFailure != nil {
		return state.terminalFailure
	}
	return nil
}

func startAnthropicUnreasonedPlaceholder(w http.ResponseWriter, flusher http.Flusher, state *anthropicStreamState) error {
	if state.messageStarted {
		return nil
	}
	if err := writeAnthropicSSEEvent(w, flusher, "message_start", map[string]any{
		"type":    "message_start",
		"message": anthropicMessageStartMessage(state),
	}); err != nil {
		return err
	}
	state.messageStarted = true
	state.thinkingStarted = true
	state.thinkingIndex = state.nextIndex
	state.nextIndex++
	if err := writeAnthropicSSEEvent(w, flusher, "content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": state.thinkingIndex,
		"content_block": map[string]any{
			"type":     "thinking",
			"thinking": "",
		},
	}); err != nil {
		return err
	}
	return writeAnthropicSSEEvent(w, flusher, "content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": state.thinkingIndex,
		"delta": map[string]any{"type": "thinking_delta", "thinking": syntheticReasoningPlaceholder + "\n"},
	})
}

func writeAnthropicEvent(w http.ResponseWriter, flusher http.Flusher, state *anthropicStreamState, evt upstream.Event) error {
	var closeThinkingBlock func() error
	startMessage := func() error {
		if state.messageStarted {
			return nil
		}
		state.messageStarted = true
		return writeAnthropicSSEEvent(w, flusher, "message_start", map[string]any{
			"type":    "message_start",
			"message": anthropicMessageStartMessage(state),
		})
	}
	startTextBlock := func() error {
		if state.textStarted && !state.textStopped {
			return nil
		}
		state.stopReason = ""
		if err := closeThinkingBlock(); err != nil {
			return err
		}
		if err := closeToolBlock(state, w, flusher); err != nil {
			return err
		}
		if err := startMessage(); err != nil {
			return err
		}
		state.textStarted = true
		state.textStopped = false
		state.textIndex = state.nextIndex
		state.nextIndex++
		return writeAnthropicSSEEvent(w, flusher, "content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": state.textIndex,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		})
	}
	startThinkingBlock := func() error {
		if state.thinkingStarted && !state.thinkingStopped {
			return nil
		}
		state.stopReason = ""
		if err := closeTextBlock(state, w, flusher); err != nil {
			return err
		}
		if err := closeToolBlock(state, w, flusher); err != nil {
			return err
		}
		if err := startMessage(); err != nil {
			return err
		}
		state.thinkingStarted = true
		state.thinkingStopped = false
		state.signatureSent = false
		state.thinkingIndex = state.nextIndex
		state.nextIndex++
		return writeAnthropicSSEEvent(w, flusher, "content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": state.thinkingIndex,
			"content_block": map[string]any{
				"type":     "thinking",
				"thinking": "",
			},
		})
	}
	closeThinkingBlock = func() error {
		if !state.thinkingStarted || state.thinkingStopped {
			return nil
		}
		if !state.signatureSent {
			if err := writeAnthropicSSEEvent(w, flusher, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": state.thinkingIndex,
				"delta": map[string]any{"type": "signature_delta", "signature": "proxy_signature"},
			}); err != nil {
				return err
			}
			state.signatureSent = true
		}
		if err := writeAnthropicSSEEvent(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": state.thinkingIndex}); err != nil {
			return err
		}
		state.thinkingStopped = true
		return nil
	}
	startToolBlock := func(item map[string]any) error {
		itemID := stringValue(item["id"])
		if state.toolStarted && !state.toolStopped {
			if state.toolItemID == itemID && itemID != "" {
				if state.toolDeltaSent {
					return nil
				}
				arguments := stringValue(item["arguments"])
				if arguments == "" {
					return nil
				}
				state.toolDeltaSent = true
				return writeAnthropicSSEEvent(w, flusher, "content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": state.toolIndex,
					"delta": map[string]any{"type": "input_json_delta", "partial_json": arguments},
				})
			}
			if err := closeToolBlock(state, w, flusher); err != nil {
				return err
			}
		}
		if err := closeThinkingBlock(); err != nil {
			return err
		}
		if err := closeTextBlock(state, w, flusher); err != nil {
			return err
		}
		if err := startMessage(); err != nil {
			return err
		}
		state.toolStarted = true
		state.toolStopped = false
		state.toolItemID = itemID
		state.toolDeltaSent = false
		state.stopReason = "tool_use"
		state.toolIndex = state.nextIndex
		state.nextIndex++
		if err := writeAnthropicSSEEvent(w, flusher, "content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": state.toolIndex,
			"content_block": map[string]any{
				"type":  "tool_use",
				"id":    stringValue(item["call_id"]),
				"name":  stringValue(item["name"]),
				"input": map[string]any{},
			},
		}); err != nil {
			return err
		}
		arguments := state.pendingToolArgs[itemID]
		if directArguments := stringValue(item["arguments"]); directArguments != "" {
			arguments += directArguments
		}
		delete(state.pendingToolArgs, itemID)
		if arguments != "" {
			state.toolDeltaSent = true
			return writeAnthropicSSEEvent(w, flusher, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": state.toolIndex,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": arguments},
			})
		}
		return nil
	}

	switch evt.Event {
	case "response.output_item.added", "response.output_item.done":
		item, _ := evt.Data["item"].(map[string]any)
		if itemType, _ := item["type"].(string); itemType == "function_call" {
			return startToolBlock(item)
		}
		if itemType, _ := item["type"].(string); itemType == "reasoning" {
			if summary := reasoningSummaryFromItem(item); summary != "" {
				state.realThinkingSeen = true
				if err := startThinkingBlock(); err != nil {
					return err
				}
				return writeAnthropicSSEEvent(w, flusher, "content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": state.thinkingIndex,
					"delta": map[string]any{"type": "thinking_delta", "thinking": summary},
				})
			}
		}
	case "response.output_text.delta":
		if err := startTextBlock(); err != nil {
			return err
		}
		state.textStarted = true
		state.textStopped = false
		delta := stringValue(evt.Data["delta"])
		if delta == "" {
			return nil
		}
		return writeAnthropicSSEEvent(w, flusher, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": state.textIndex,
			"delta": map[string]any{"type": "text_delta", "text": delta},
		})
	case "response.reasoning.delta", "response.reasoning_summary_text.delta":
		if delta := reasoningContentValue(evt.Data); delta != "" {
			state.realThinkingSeen = true
			if err := startThinkingBlock(); err != nil {
				return err
			}
			return writeAnthropicSSEEvent(w, flusher, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": state.thinkingIndex,
				"delta": map[string]any{"type": "thinking_delta", "thinking": delta},
			})
		}
	case "response.function_call_arguments.delta":
		itemID := stringValue(evt.Data["item_id"])
		if itemID == "" {
			return nil
		}
		partial := stringValue(evt.Data["delta"])
		if partial == "" {
			return nil
		}
		if itemID != "" && state.toolStarted && !state.toolStopped {
			if state.toolItemID != "" && state.toolItemID != itemID {
				state.pendingToolArgs[itemID] += partial
				return nil
			}
			state.toolDeltaSent = true
			return writeAnthropicSSEEvent(w, flusher, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": state.toolIndex,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": partial},
			})
		}
		state.pendingToolArgs[itemID] += partial
	case "response.completed", "response.done":
		state.terminalSeen = true
		if err := closeTextBlock(state, w, flusher); err != nil {
			return err
		}
		if err := closeToolBlock(state, w, flusher); err != nil {
			return err
		}
		if err := closeThinkingBlock(); err != nil {
			return err
		}
		for _, block := range []struct {
			started bool
			index   int
		}{
			{false, state.textIndex},
			{false, state.toolIndex},
		} {
			if !block.started {
				continue
			}
			if err := writeAnthropicSSEEvent(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": block.index}); err != nil {
				return err
			}
		}
		stopReason := "end_turn"
		if state.stopReason != "" {
			stopReason = state.stopReason
		}
		if err := writeAnthropicSSEEvent(w, flusher, "message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
			"usage": anthropicUsageFromEvent(evt.Data),
		}); err != nil {
			return err
		}
		if err := writeAnthropicSSEEvent(w, flusher, "message_stop", map[string]any{"type": "message_stop"}); err != nil {
			return err
		}
	case "response.incomplete":
		state.terminalSeen = true
		terminalFailure := terminalFailureFromEventData(evt.Data)
		state.terminalFailure = terminalFailure
		if err := closeTextBlock(state, w, flusher); err != nil {
			return err
		}
		if err := closeToolBlock(state, w, flusher); err != nil {
			return err
		}
		if err := closeThinkingBlock(); err != nil {
			return err
		}
		return writeAnthropicTerminalFailure(w, flusher, state, stringValue(evt.Data["request_id"]), terminalFailure.HealthFlag, terminalFailure.Message)
	}
	return nil
}

func closeTextBlock(state *anthropicStreamState, w http.ResponseWriter, flusher http.Flusher) error {
	if !state.textStarted || state.textStopped {
		return nil
	}
	if err := writeAnthropicSSEEvent(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": state.textIndex}); err != nil {
		return err
	}
	state.textStopped = true
	return nil
}

func closeToolBlock(state *anthropicStreamState, w http.ResponseWriter, flusher http.Flusher) error {
	if !state.toolStarted || state.toolStopped {
		return nil
	}
	if err := writeAnthropicSSEEvent(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": state.toolIndex}); err != nil {
		return err
	}
	state.toolStopped = true
	state.toolItemID = ""
	state.toolDeltaSent = false
	return nil
}

func writeAnthropicSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
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

func writeAnthropicTerminalFailure(w http.ResponseWriter, flusher http.Flusher, state *anthropicStreamState, requestID string, healthFlag string, message string) error {
	if state != nil {
		if err := closeTextBlock(state, w, flusher); err != nil {
			return err
		}
		if err := closeToolBlock(state, w, flusher); err != nil {
			return err
		}
		if err := closeThinkingBlockForFailure(state, w, flusher); err != nil {
			return err
		}
	}
	if err := writeAnthropicSSEEvent(w, flusher, "error", map[string]any{
		"type":        "error",
		"request_id":  requestID,
		"health_flag": healthFlag,
		"error":       map[string]any{"message": message},
	}); err != nil {
		return err
	}
	return writeAnthropicSSEEvent(w, flusher, "message_stop", map[string]any{"type": "message_stop"})
}

func anthropicMessageStartMessage(state *anthropicStreamState) map[string]any {
	messageID := ""
	modelName := ""
	if state != nil {
		messageID = state.messageID
		modelName = state.modelName
	}
	return map[string]any{
		"id":            messageID,
		"type":          "message",
		"role":          "assistant",
		"model":         modelName,
		"content":       []any{},
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
	}
}

func closeThinkingBlockForFailure(state *anthropicStreamState, w http.ResponseWriter, flusher http.Flusher) error {
	if state == nil || !state.thinkingStarted || state.thinkingStopped {
		return nil
	}
	if err := writeAnthropicSSEEvent(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": state.thinkingIndex}); err != nil {
		return err
	}
	state.thinkingStopped = true
	return nil
}

func parseAnthropicToolArguments(arguments string) any {
	if arguments == "" {
		return map[string]any{}
	}
	var decoded any
	if err := json.Unmarshal([]byte(arguments), &decoded); err != nil {
		return map[string]any{"raw": arguments}
	}
	return decoded
}

func anthropicUsageFromEvent(data map[string]any) map[string]any {
	usage := usageFromEventData(data)
	out := map[string]any{}
	if _, ok := usage["input_tokens"]; ok {
		out["input_tokens"] = effectiveAnthropicStreamingInputTokens(usage)
	}
	if output, ok := usage["output_tokens"]; ok {
		out["output_tokens"] = output
	}
	if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
		if cached, ok := details["cached_tokens"]; ok {
			out["cache_read_input_tokens"] = cached
		}
		if created, ok := details["cache_creation_tokens"]; ok {
			out["cache_creation_input_tokens"] = created
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func effectiveAnthropicStreamingInputTokens(usage map[string]any) any {
	input, ok := usage["input_tokens"]
	if !ok {
		return nil
	}
	inputFloat, ok := usageNumberAsFloatForStreaming(input)
	if !ok {
		return input
	}
	remaining := inputFloat
	if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
		if cached, ok := usageNumberAsFloatForStreaming(details["cached_tokens"]); ok {
			remaining -= cached
		}
		if created, ok := usageNumberAsFloatForStreaming(details["cache_creation_tokens"]); ok {
			remaining -= created
		}
	}
	if remaining < 0 {
		remaining = 0
	}
	return int(remaining)
}

func usageNumberAsFloatForStreaming(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
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
	pendingToolArgs   map[string]string
	nextToolIx        int
	terminalSeen      bool
	terminalFailure   *aggregate.TerminalFailureError
}

func writeChatSSELive(ctx context.Context, stream *upstream.EventStream, w http.ResponseWriter, flusher http.Flusher, req model.CanonicalRequest) error {
	state := chatStreamState{
		toolMeta:        map[string]map[string]string{},
		toolIndex:       map[string]int{},
		toolSent:        map[string]bool{},
		pendingToolArgs: map[string]string{},
	}
	if err := writeSSEPadding(w, flusher); err != nil {
		return err
	}
	if err := writeChatChunk(w, flusher, map[string]any{"reasoning_content": syntheticReasoningPlaceholder + "\n"}, "", nil); err != nil {
		return err
	}
	if err := waitSyntheticLeadTime(ctx); err != nil {
		return err
	}
	err := streamLiveWithSyntheticTicks(ctx, stream.Consume,
		func() bool { return state.textStarted || state.realReasoningSeen },
		func() error {
			if state.textStarted || state.realReasoningSeen {
				return nil
			}
			return writeChatChunk(w, flusher, map[string]any{"reasoning_content": "\u200b"}, "", nil)
		},
		func() error { return writeSSEComment(w, flusher, "keep-alive") },
		func(evt upstream.Event) error {
			return writeChatEvent(w, flusher, &state, evt, req.IncludeUsage)
		},
	)
	if err != nil {
		return err
	}
	if !state.terminalSeen {
		return io.ErrUnexpectedEOF
	}
	if state.terminalFailure != nil {
		return state.terminalFailure
	}
	return nil
}

func waitSyntheticLeadTime(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(initialSyntheticReasoningLeadTime):
		return nil
	}
}

type streamSignal struct {
	evt  *upstream.Event
	err  error
	done bool
}

func streamLiveWithSyntheticTicks(
	ctx context.Context,
	consumeFn func(func(upstream.Event) error) error,
	stopTicks func() bool,
	onTick func() error,
	onHeartbeat func() error,
	onEvent func(upstream.Event) error,
) error {
	signals := make(chan streamSignal, 32)
	go func() {
		err := consumeFn(func(evt upstream.Event) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case signals <- streamSignal{evt: &evt}:
				return nil
			}
		})
		signals <- streamSignal{err: err, done: true}
		close(signals)
	}()

	ticker := time.NewTicker(syntheticReasoningTickInterval)
	defer ticker.Stop()
	heartbeatTicker := time.NewTicker(sseHeartbeatInterval)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeatTicker.C:
			if onHeartbeat != nil {
				if err := onHeartbeat(); err != nil {
					return err
				}
			}
		case <-ticker.C:
			if onTick != nil && !stopTicks() {
				if err := onTick(); err != nil {
					return err
				}
			}
		case sig, ok := <-signals:
			if !ok {
				return nil
			}
			if sig.evt != nil {
				if err := onEvent(*sig.evt); err != nil {
					return err
				}
				continue
			}
			if sig.done {
				return sig.err
			}
		}
	}
}

func writeChatSSE(w http.ResponseWriter, flusher http.Flusher, events []upstream.Event, includeUsage bool) error {
	state := chatStreamState{
		toolMeta:        map[string]map[string]string{},
		toolIndex:       map[string]int{},
		toolSent:        map[string]bool{},
		pendingToolArgs: map[string]string{},
	}
	for _, evt := range events {
		if err := writeChatEvent(w, flusher, &state, evt, includeUsage); err != nil {
			return err
		}
	}
	return nil
}

func writeChatEvent(w http.ResponseWriter, flusher http.Flusher, state *chatStreamState, evt upstream.Event, includeUsage bool) error {
	ensureRoleSent := func() error {
		if state.roleSent {
			return nil
		}
		if err := writeChatChunk(w, flusher, map[string]any{"role": "assistant"}, "", nil); err != nil {
			return err
		}
		state.roleSent = true
		return nil
	}
	switch evt.Event {
	case "response.created":
	case "response.output_item.added", "response.output_item.done":
		item, _ := evt.Data["item"].(map[string]any)
		if reasoningContent := reasoningSummaryFromItem(item); reasoningContent != "" {
			state.realReasoningSeen = true
			if err := ensureRoleSent(); err != nil {
				return err
			}
			if err := writeChatChunk(w, flusher, map[string]any{"reasoning_content": reasoningContent}, "", nil); err != nil {
				return err
			}
		}
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
					if err := ensureRoleSent(); err != nil {
						return err
					}
					toolDelta := chatToolDelta(state.toolIndex[itemID], stringValue(item["call_id"]), stringValue(item["name"]), state.pendingToolArgs[itemID], true)
					if err := writeChatChunk(w, flusher, toolDelta, "", nil); err != nil {
						return err
					}
					state.toolSent[itemID] = true
					delete(state.pendingToolArgs, itemID)
				}
			}
		}
	case "response.output_text.delta":
		state.textStarted = true
		if err := ensureRoleSent(); err != nil {
			return err
		}
		if delta := stringValue(evt.Data["delta"]); delta != "" {
			if err := writeChatChunk(w, flusher, map[string]any{"content": delta}, "", nil); err != nil {
				return err
			}
		}
	case "response.reasoning.delta", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		if delta := reasoningContentValue(evt.Data); delta != "" {
			state.realReasoningSeen = true
			if err := ensureRoleSent(); err != nil {
				return err
			}
			if err := writeChatChunk(w, flusher, map[string]any{"reasoning_content": delta}, "", nil); err != nil {
				return err
			}
		}
	case "response.function_call_arguments.delta":
		itemID := stringValue(evt.Data["item_id"])
		delta := stringValue(evt.Data["delta"])
		if itemID == "" || delta == "" {
			return nil
		}
		if !state.toolSent[itemID] {
			state.pendingToolArgs[itemID] += delta
			if _, ok := state.toolMeta[itemID]; !ok {
				return nil
			}
			if err := ensureRoleSent(); err != nil {
				return err
			}
			toolDelta := chatToolDelta(state.toolIndex[itemID], state.toolMeta[itemID]["call_id"], state.toolMeta[itemID]["name"], state.pendingToolArgs[itemID], true)
			if err := writeChatChunk(w, flusher, toolDelta, "", nil); err != nil {
				return err
			}
			state.toolSent[itemID] = true
			delete(state.pendingToolArgs, itemID)
			return nil
		}
		index := state.toolIndex[itemID]
		toolDelta := chatToolDelta(index, "", "", delta, false)
		if err := writeChatChunk(w, flusher, toolDelta, "", nil); err != nil {
			return err
		}
	case "response.completed", "response.done":
		state.terminalSeen = true
		finishReason := "stop"
		if len(state.toolSent) > 0 {
			finishReason = "tool_calls"
		}
		cachedTokens := nestedCachedTokens(usageFromEventData(evt.Data))
		logging.Event("upstream_stream_usage_observed", map[string]any{
			"upstream_event":       evt.Event,
			"cached_tokens":        cachedTokens,
			"stream_include_usage": includeUsage,
		})
		if err := writeChatChunk(w, flusher, map[string]any{}, finishReason, nil); err != nil {
			return err
		}
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
		if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
	case "response.incomplete":
		state.terminalSeen = true
		state.terminalFailure = terminalFailureFromEventData(evt.Data)
		return writeChatTerminalFailure(w, flusher, state.terminalFailure.HealthFlag, state.terminalFailure.Message)
	}
	return nil
}

func terminalFailureFromEventData(data map[string]any) *aggregate.TerminalFailureError {
	healthFlag, _ := data["health_flag"].(string)
	message, _ := data["message"].(string)
	if healthFlag == "" {
		healthFlag = "upstream_stream_broken"
	}
	if message == "" {
		message = "upstream response incomplete"
	}
	return &aggregate.TerminalFailureError{HealthFlag: healthFlag, Message: message}
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

func writeChatTerminalFailure(w http.ResponseWriter, flusher http.Flusher, healthFlag string, message string) error {
	if err := writeChatChunk(w, flusher, map[string]any{"error": map[string]any{"health_flag": healthFlag, "message": message}}, "error", nil); err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func writeSSEPadding(w http.ResponseWriter, flusher http.Flusher) error {
	return writeSSEComment(w, flusher, strings.Repeat(" ", 2048))
}

func writeSSEComment(w http.ResponseWriter, flusher http.Flusher, text string) error {
	if _, err := fmt.Fprintf(w, ": %s\n\n", text); err != nil {
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
	for _, key := range []string{"reasoning_content", "summary", "content", "delta", "text"} {
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
