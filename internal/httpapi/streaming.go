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
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/syntaxrepair"
	"openai-compat-proxy/internal/upstream"
)

const initialSyntheticReasoningLeadTime = 350 * time.Millisecond

var syntheticReasoningTickInterval = 250 * time.Millisecond
var sseHeartbeatInterval = 15 * time.Second

const syntheticReasoningPlaceholder = "**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长"

func syntheticReasoningPrelude() string {
	return syntheticReasoningPlaceholder + "\n\n"
}

type usageRecorderFunc func(map[string]any)

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
	toolMeta         map[string]map[string]string
	terminalSeen     bool
	terminalFailure  *aggregate.TerminalFailureError
}

type responsesStreamState struct {
	createdSent          bool
	createdResponseID    string
	modelName            string
	textStarted          bool
	realReasoningSeen    bool
	planningSent         bool
	toolStatusSent       bool
	reasoningStarted     bool
	reasoningClosed      bool
	syntheticInjected    bool
	syntheticSummary     strings.Builder
	toolItems            map[string]*responsesToolItemState
	toolIDAliases        map[string]string
	toolOrder            []string
	upstreamEndpointType string
	requestID            string
	terminalSeen         bool
	terminalFailure      *aggregate.TerminalFailureError
}

type responsesToolItemState struct {
	item      map[string]any
	arguments strings.Builder
	addedSent bool
	doneSent  bool
}

type processResponseEventResult struct {
	skipWrite     bool
	writeNow      *processEventCommand
	writeNowItems []processEventCommand
}

type processEventCommand struct {
	Event string
	Data  map[string]any
}

type processedResponseEvents struct {
	skipWrite       bool
	events          []processEventCommand
	terminalSeen    bool
	terminalFailure *aggregate.TerminalFailureError
}

type responseProjectionState struct {
	createdSent          bool
	createdResponseID    string
	modelName            string
	toolIDAliases        map[string]string
	toolItems            map[string]*responsesToolItemState
	toolOrder            []string
	reasoningStarted     bool
	reasoningClosed      bool
	syntheticInjected    bool
	realReasoningSeen    bool
	syntheticSummary     *strings.Builder
	requestID            string
	upstreamEndpointType string
	terminalSeen         bool
	terminalFailure      *aggregate.TerminalFailureError
}

type responseEventWriterHelper struct {
	downstreamType       string
	upstreamEndpointType string
	createdSent          bool
	createdResponseID    string
	modelName            string
	toolIDAliases        map[string]string
	toolItems            map[string]*responsesToolItemState
	toolOrder            []string
	reasoningStarted     bool
	reasoningClosed      bool
	syntheticInjected    bool
	realReasoningSeen    bool
	syntheticSummary     *strings.Builder
	requestID            string
	terminalSeen         bool
	terminalFailure      *aggregate.TerminalFailureError
	events               []processEventCommand
}

func (h *responseEventWriterHelper) ensureToolItemState(itemID string) *responsesToolItemState {
	if h.toolItems == nil {
		h.toolItems = map[string]*responsesToolItemState{}
	}
	toolState, ok := h.toolItems[itemID]
	if !ok {
		toolState = &responsesToolItemState{}
		h.toolItems[itemID] = toolState
		h.toolOrder = append(h.toolOrder, itemID)
	}
	return toolState
}

func (h *responseEventWriterHelper) canonicalToolItemID(itemID string) string {
	if itemID == "" {
		return itemID
	}
	if h.toolIDAliases != nil {
		if mapped, ok := h.toolIDAliases[itemID]; ok && mapped != "" {
			return mapped
		}
	}
	return itemID
}

func (h *responseEventWriterHelper) addEvent(event string, data map[string]any) {
	h.events = append(h.events, processEventCommand{Event: event, Data: data})
}

func (h *responseEventWriterHelper) addToolItemAddedEvent(item map[string]any) {
	item = withParsedToolParameters(item)
	h.addEvent("response.output_item.added", map[string]any{"item": item})
}

func (h *responseEventWriterHelper) addToolItemDoneEvent(item map[string]any) {
	item = withParsedToolParameters(item)
	h.addEvent("response.output_item.done", map[string]any{"item": item})
}

func (h *responseEventWriterHelper) addFunctionCallArgumentsDoneEvent(itemID, arguments string) {
	h.addEvent("response.function_call_arguments.done", map[string]any{"item_id": itemID, "arguments": arguments})
}

func (h *responseEventWriterHelper) addCreatedEvent(id string) {
	if id == "" {
		id = h.currentResponseID()
	}
	response := map[string]any{"id": id, "object": "response", "status": "in_progress"}
	if h.modelName != "" {
		response["model"] = h.modelName
	}
	h.addEvent("response.created", map[string]any{"response": response})
	h.createdSent = true
	h.createdResponseID = id
}

func (h *responseEventWriterHelper) currentResponseID() string {
	for _, itemID := range h.toolOrder {
		toolState := h.toolItems[itemID]
		if toolState == nil || toolState.item == nil {
			continue
		}
		if callID, _ := toolState.item["call_id"].(string); callID != "" {
			return "resp_" + callID
		}
		if id, _ := toolState.item["id"].(string); id != "" {
			return "resp_" + id
		}
	}
	if h.requestID != "" {
		return "resp_" + h.requestID
	}
	return "resp_proxy"
}

func (h *responseEventWriterHelper) flushPendingFunctionCalls() {
	compatCompleteToolArgs := h.shouldBufferCompatToolArgs()
	for _, itemID := range h.toolOrder {
		toolState := h.toolItems[itemID]
		if toolState == nil || toolState.doneSent || toolState.item == nil {
			continue
		}
		itemCopy := cloneJSONValueForResponse(toolState.item).(map[string]any)
		arguments := toolState.arguments.String()
		if toolState.arguments.Len() > 0 {
			if repaired, ok := syntaxrepair.RepairJSON(arguments); ok {
				arguments = repaired
			}
			itemCopy["arguments"] = arguments
		}
		if compatCompleteToolArgs && toolState.arguments.Len() > 0 && !isValidToolArgumentsJSON(arguments) {
			continue
		}
		if compatCompleteToolArgs && !toolState.addedSent {
			addedItem := cloneJSONValueForResponse(toolState.item).(map[string]any)
			delete(addedItem, "arguments")
			delete(addedItem, "parameters")
			h.addToolItemAddedEvent(addedItem)
			toolState.addedSent = true
		}
		h.addToolItemDoneEvent(itemCopy)
		if compatCompleteToolArgs && toolState.arguments.Len() > 0 {
			h.addFunctionCallArgumentsDoneEvent(itemID, arguments)
		}
		toolState.doneSent = true
	}
}

func (h *responseEventWriterHelper) shouldBufferCompatToolArgs() bool {
	return h.downstreamType == "responses" && normalizeHTTPAPIUpstreamEndpointType(h.upstreamEndpointType) != config.UpstreamEndpointTypeResponses
}

func newResponsesStreamState(requestID, upstreamEndpointType string) *responsesStreamState {
	return &responsesStreamState{
		toolItems:            map[string]*responsesToolItemState{},
		toolIDAliases:        map[string]string{},
		upstreamEndpointType: upstreamEndpointType,
		requestID:            requestID,
	}
}

func cloneResponsesStreamState(initialState *responsesStreamState, requestID, upstreamEndpointType string) *responsesStreamState {
	state := &responsesStreamState{
		toolItems:            map[string]*responsesToolItemState{},
		toolIDAliases:        map[string]string{},
		upstreamEndpointType: upstreamEndpointType,
		requestID:            requestID,
	}
	if initialState == nil {
		return state
	}
	state.createdSent = initialState.createdSent
	state.createdResponseID = initialState.createdResponseID
	state.textStarted = initialState.textStarted
	state.realReasoningSeen = initialState.realReasoningSeen
	state.planningSent = initialState.planningSent
	state.toolStatusSent = initialState.toolStatusSent
	state.reasoningStarted = initialState.reasoningStarted
	state.reasoningClosed = initialState.reasoningClosed
	state.syntheticInjected = initialState.syntheticInjected
	state.toolItems = initialState.toolItems
	state.toolIDAliases = initialState.toolIDAliases
	state.toolOrder = initialState.toolOrder
	state.terminalSeen = initialState.terminalSeen
	state.terminalFailure = initialState.terminalFailure
	if summary := initialState.syntheticSummary.String(); summary != "" {
		state.syntheticSummary.WriteString(summary)
	}
	if state.toolItems == nil {
		state.toolItems = map[string]*responsesToolItemState{}
	}
	if state.toolIDAliases == nil {
		state.toolIDAliases = map[string]string{}
	}
	if state.requestID == "" {
		state.requestID = requestID
	}
	if state.upstreamEndpointType == "" {
		state.upstreamEndpointType = upstreamEndpointType
	}
	return state
}

func startResponsesSyntheticPrelude(w http.ResponseWriter, flusher http.Flusher, req model.CanonicalRequest, upstreamEndpointType, thinkingTagStyle string) (*responsesStreamState, error) {
	state := newResponsesStreamState(req.RequestID, upstreamEndpointType)
	if err := writeSSEPadding(w, flusher); err != nil {
		return nil, err
	}
	if shouldInjectSyntheticResponsesReasoning(upstreamEndpointType, thinkingTagStyle) {
		if err := writeSyntheticResponsesReasoningWithState(w, flusher, state, syntheticReasoningPrelude()); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func (h *responseEventWriterHelper) closeSyntheticReasoning() {
	if !h.syntheticInjected || !h.reasoningStarted || h.reasoningClosed {
		return
	}
	if h.downstreamType != "responses" {
		h.reasoningClosed = true
		return
	}
	summary := []any{}
	if text := h.syntheticSummary.String(); text != "" {
		summary = append(summary, map[string]any{"type": "summary_text", "text": text})
	}
	h.addEvent("response.output_item.done", map[string]any{"item": map[string]any{
		"id":      "rs_proxy",
		"type":    "reasoning",
		"summary": summary,
	}})
	h.reasoningClosed = true
}

func (h *responseEventWriterHelper) markRealReasoningSeen() {
	if h.realReasoningSeen {
		return
	}
	h.realReasoningSeen = true
	h.closeSyntheticReasoning()
}

func (h *responseEventWriterHelper) shouldMergeChatReasoningIntoSynthetic() bool {
	return h.downstreamType == "responses" &&
		h.syntheticInjected &&
		normalizeHTTPAPIUpstreamEndpointType(h.upstreamEndpointType) == config.UpstreamEndpointTypeChat
}

func doProcessResponseEvent(h *responseEventWriterHelper, evt upstream.Event) (processResponseEventResult, error) {
	result := processResponseEventResult{}
	compatCompleteToolArgs := h.shouldBufferCompatToolArgs()
	if h.toolIDAliases == nil {
		h.toolIDAliases = map[string]string{}
	}

	item, _ := evt.Data["item"].(map[string]any)

	switch evt.Event {
	case "response.created":
		if h.createdSent {
			response, _ := evt.Data["response"].(map[string]any)
			if response != nil && stringValue(response["id"]) == h.createdResponseID {
				result.skipWrite = true
				return result, nil
			}
		}
		if response, _ := evt.Data["response"].(map[string]any); response != nil {
			if id := stringValue(response["id"]); id != "" {
				h.createdResponseID = id
			}
			if _, ok := response["object"]; !ok {
				response["object"] = "response"
			}
			if _, ok := response["status"]; !ok {
				response["status"] = "in_progress"
			}
			if model := stringValue(response["model"]); model != "" {
				h.modelName = model
			} else if h.modelName != "" {
				response["model"] = h.modelName
			}
		}
		h.createdSent = true
	case "response.output_item.added", "response.output_item.done":
		if itemType, _ := item["type"].(string); itemType == "reasoning" {
			h.markRealReasoningSeen()
			break
		}
		if itemType, _ := item["type"].(string); itemType == "function_call" {
			itemID, _ := item["id"].(string)
			if itemID == "" {
				itemID, _ = item["call_id"].(string)
			}
			if callID, _ := item["call_id"].(string); callID != "" {
				if rawID, _ := item["id"].(string); rawID != "" && rawID != callID {
					h.toolIDAliases[rawID] = callID
				}
				itemID = callID
			}
			if itemID != "" {
				toolState := h.ensureToolItemState(itemID)
				toolState.item = cloneJSONValueForResponse(item).(map[string]any)
				if args, _ := item["arguments"].(string); args != "" {
					if repaired, ok := syntaxrepair.RepairJSON(args); ok {
						args = repaired
						item["arguments"] = repaired
					}
					toolState.arguments.Reset()
					toolState.arguments.WriteString(args)
				}
				if compatCompleteToolArgs {
					result.skipWrite = true
					return result, nil
				}
			}
		}
	case "response.output_text.delta":
		if compatCompleteToolArgs {
			h.flushPendingFunctionCalls()
		}
		h.closeSyntheticReasoning()
		h.reasoningStarted = true
	case "response.reasoning.delta", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		if compatCompleteToolArgs {
			h.flushPendingFunctionCalls()
		}
		if h.shouldMergeChatReasoningIntoSynthetic() && evt.Event == "response.reasoning.delta" {
			if summary, ok := evt.Data["summary"].(string); ok && summary != "" {
				if h.syntheticSummary != nil {
					h.syntheticSummary.WriteString(summary)
					if !strings.HasSuffix(summary, "\n") {
						h.syntheticSummary.WriteString("\n")
					}
				}
				h.addEvent("response.reasoning_summary_text.delta", map[string]any{"delta": summary})
			}
			result.skipWrite = true
			break
		}
		h.markRealReasoningSeen()
		// Convert response.reasoning.delta (summary format) to response.reasoning_summary_text.delta (delta format) for responses SSE
		if evt.Event == "response.reasoning.delta" {
			if summary, ok := evt.Data["summary"].(string); ok && summary != "" {
				h.addEvent("response.reasoning_summary_text.delta", map[string]any{"delta": summary})
			}
			result.skipWrite = true
		}
	case "response.function_call_arguments.delta":
		itemID, _ := evt.Data["item_id"].(string)
		itemID = h.canonicalToolItemID(itemID)
		if itemID != "" {
			evt.Data["item_id"] = itemID
		}
		delta, _ := evt.Data["delta"].(string)
		if itemID != "" {
			toolState := h.ensureToolItemState(itemID)
			if delta != "" {
				toolState.arguments.WriteString(delta)
			}
		}
		if compatCompleteToolArgs {
			result.skipWrite = true
			return result, nil
		}
	case "response.completed":
		h.terminalSeen = true
		h.reasoningStarted = true
		if compatCompleteToolArgs {
			h.flushPendingFunctionCalls()
		}
		response, _ := evt.Data["response"].(map[string]any)
		if response == nil {
			response = map[string]any{}
			evt.Data["response"] = response
		}
		if _, ok := response["id"]; !ok {
			response["id"] = h.currentResponseID()
		}
		if _, ok := response["object"]; !ok {
			response["object"] = "response"
		}
		if _, ok := response["status"]; !ok {
			response["status"] = "completed"
		}
		if model := stringValue(response["model"]); model != "" {
			h.modelName = model
		} else if h.modelName != "" {
			response["model"] = h.modelName
		}
		if usage, _ := evt.Data["usage"].(map[string]any); len(usage) > 0 {
			if _, ok := response["usage"]; !ok {
				response["usage"] = cloneMap(usage)
			}
		}
		h.closeSyntheticReasoning()
	case "response.done":
		if h.terminalFailure != nil {
			result.skipWrite = true
			return result, nil
		}
		h.terminalSeen = true
		// Mirror top-level usage into response object for compatibility
		response, _ := evt.Data["response"].(map[string]any)
		if response == nil {
			response = map[string]any{}
			evt.Data["response"] = response
		}
		if _, ok := response["id"]; !ok {
			response["id"] = h.currentResponseID()
		}
		if _, ok := response["object"]; !ok {
			response["object"] = "response"
		}
		if _, ok := response["status"]; !ok {
			response["status"] = "completed"
		}
		if model := stringValue(response["model"]); model != "" {
			h.modelName = model
		} else if h.modelName != "" {
			response["model"] = h.modelName
		}
		if usage, _ := evt.Data["usage"].(map[string]any); len(usage) > 0 {
			if _, ok := response["usage"]; !ok {
				response["usage"] = cloneMap(usage)
			}
		}
		h.closeSyntheticReasoning()
	case "response.incomplete":
		h.terminalSeen = true
		h.reasoningStarted = true
		if compatCompleteToolArgs {
			h.flushPendingFunctionCalls()
		}
		healthFlag, _ := evt.Data["health_flag"].(string)
		message, _ := evt.Data["message"].(string)
		if healthFlag == "" {
			healthFlag = "upstreamStreamBroken"
		}
		if message == "" {
			message = "upstream response incomplete"
		}
		h.terminalFailure = &aggregate.TerminalFailureError{HealthFlag: healthFlag, Message: message}
		evt.Data["health_flag"] = healthFlag
		evt.Data["message"] = message
		h.closeSyntheticReasoning()
	}
	return result, nil
}

// EventWriter 接口：统一 Responses 事件输出
type EventWriter interface {
	WriteEvent(event string, data map[string]any) error
	WriteComment(comment string) error
	DownstreamType() string
}

// ResponsesEventWriter - 直接写 Responses SSE
type ResponsesEventWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func (w *ResponsesEventWriter) WriteEvent(event string, data map[string]any) error {
	if _, err := fmt.Fprintf(w.w, "event: %s\n", event); err != nil {
		return err
	}
	payload, err := responseStreamPayload(event, data)
	if err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := fmt.Fprintf(w.w, "data: %s\n\n", payload); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprint(w.w, "data: {}\n\n"); err != nil {
			return err
		}
	}
	if w.flusher != nil {
		w.flusher.Flush()
	}
	return nil
}

func (w *ResponsesEventWriter) WriteComment(comment string) error {
	return writeSSEComment(w.w, w.flusher, comment)
}

func (w *ResponsesEventWriter) DownstreamType() string {
	return "responses"
}

func (w *ResponsesEventWriter) WriteSSERaw(event string, payload []byte) error {
	if _, err := fmt.Fprintf(w.w, "event: %s\n", event); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := fmt.Fprintf(w.w, "data: %s\n\n", payload); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprint(w.w, "data: {}\n\n"); err != nil {
			return err
		}
	}
	if w.flusher != nil {
		w.flusher.Flush()
	}
	return nil
}

// ChatEventWriter - 将 Responses 事件转换为 Chat SSE
type ChatEventWriter struct {
	w             http.ResponseWriter
	flusher       http.Flusher
	chatState     *chatStreamState
	helper        *responseEventWriterHelper
	usageRecorder usageRecorderFunc
}

func NewChatEventWriter(w http.ResponseWriter, flusher http.Flusher, chatState *chatStreamState, h *responseEventWriterHelper, usageRecorder usageRecorderFunc) *ChatEventWriter {
	return &ChatEventWriter{w: w, flusher: flusher, chatState: chatState, helper: h, usageRecorder: usageRecorder}
}

func (w *ChatEventWriter) WriteEvent(event string, data map[string]any) error {
	evt := upstream.Event{Event: event, Data: data}

	skipOriginalWrite := false
	if w.helper != nil {
		result, err := doProcessResponseEvent(w.helper, evt)
		if err != nil {
			return err
		}
		skipOriginalWrite = result.skipWrite

		for _, cmd := range w.helper.events {
			if err := w.writeProcessedEvent(cmd.Event, cmd.Data); err != nil {
				return err
			}
		}
		w.helper.events = nil
	}
	if skipOriginalWrite {
		return nil
	}

	return writeChatEvent(w.w, w.flusher, w.chatState, evt, true, w.usageRecorder)
}

func (w *ChatEventWriter) writeProcessedEvent(event string, data map[string]any) error {
	evt := upstream.Event{Event: event, Data: data}
	return writeChatEvent(w.w, w.flusher, w.chatState, evt, true, nil)
}

func (w *ChatEventWriter) WriteComment(comment string) error {
	return writeSSEComment(w.w, w.flusher, comment)
}

func (w *ChatEventWriter) DownstreamType() string {
	return "chat"
}

// AnthropicEventWriter - 将 Responses 事件转换为 Anthropic SSE
type AnthropicEventWriter struct {
	w              http.ResponseWriter
	flusher        http.Flusher
	anthropicState *anthropicStreamState
	helper         *responseEventWriterHelper
	usageRecorder  usageRecorderFunc
}

func NewAnthropicEventWriter(w http.ResponseWriter, flusher http.Flusher, anthropicState *anthropicStreamState, h *responseEventWriterHelper, usageRecorder usageRecorderFunc) *AnthropicEventWriter {
	return &AnthropicEventWriter{w: w, flusher: flusher, anthropicState: anthropicState, helper: h, usageRecorder: usageRecorder}
}

func (w *AnthropicEventWriter) WriteEvent(event string, data map[string]any) error {
	evt := upstream.Event{Event: event, Data: data}

	if w.helper != nil {
		result, err := doProcessResponseEvent(w.helper, evt)
		if err != nil {
			return err
		}

		for _, cmd := range w.helper.events {
			if err := w.writeProcessedEvent(cmd.Event, cmd.Data); err != nil {
				return err
			}
		}
		w.helper.events = nil

		if result.skipWrite {
			return nil
		}
	}

	return writeAnthropicEvent(w.w, w.flusher, w.anthropicState, evt, w.usageRecorder)
}

func (w *AnthropicEventWriter) writeProcessedEvent(event string, data map[string]any) error {
	evt := upstream.Event{Event: event, Data: data}
	return writeAnthropicEvent(w.w, w.flusher, w.anthropicState, evt, nil)
}

func (w *AnthropicEventWriter) WriteComment(comment string) error {
	return writeSSEComment(w.w, w.flusher, comment)
}

func (w *AnthropicEventWriter) DownstreamType() string {
	return "anthropic"
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

func shouldInjectSyntheticResponsesReasoning(upstreamEndpointType, thinkingTagStyle string) bool {
	if normalizeHTTPAPIUpstreamEndpointType(upstreamEndpointType) != config.UpstreamEndpointTypeChat {
		return true
	}
	return thinkingTagStyle == config.UpstreamThinkingTagStyleLegacy
}

func shouldEmitSyntheticResponsesCreated(upstreamEndpointType string) bool {
	return normalizeHTTPAPIUpstreamEndpointType(upstreamEndpointType) == config.UpstreamEndpointTypeChat
}

func writeResponsesSSELive(ctx context.Context, stream *upstream.EventStream, w http.ResponseWriter, flusher http.Flusher, req model.CanonicalRequest, upstreamEndpointType string, thinkingTagStyle string, usageRecorder usageRecorderFunc, initialState *responsesStreamState) (aggregate.Result, error) {
	state := cloneResponsesStreamState(initialState, req.RequestID, upstreamEndpointType)
	state.modelName = req.Model
	collector := aggregate.NewCollector()
	writer := &ResponsesEventWriter{w: w, flusher: flusher}
	if syntheticResponseID := stream.FirstPendingResponseID(); shouldEmitSyntheticResponsesCreated(upstreamEndpointType) && syntheticResponseID != "" {
		createdHelper := newResponseEventWriterHelper(writer.DownstreamType(), responseProjectionState{requestID: state.requestID, upstreamEndpointType: state.upstreamEndpointType, createdSent: state.createdSent, modelName: state.modelName})
		createdHelper.addCreatedEvent(syntheticResponseID)
		state.createdSent = createdHelper.createdSent
		state.createdResponseID = createdHelper.createdResponseID
		for _, cmd := range createdHelper.events {
			if err := writer.WriteEvent(cmd.Event, cmd.Data); err != nil {
				return aggregate.Result{}, err
			}
		}
	}
	injectSyntheticReasoning := shouldInjectSyntheticResponsesReasoning(upstreamEndpointType, thinkingTagStyle)
	if injectSyntheticReasoning && !state.syntheticInjected {
		if err := writeSyntheticResponsesReasoningWithState(w, flusher, state, syntheticReasoningPrelude()); err != nil {
			return aggregate.Result{}, err
		}
	}
	err := streamLiveWithSyntheticTicks(ctx, stream.Consume,
		func() bool { return state.textStarted || state.realReasoningSeen },
		nil,
		func() error { return writeSSEComment(w, flusher, "keep-alive") },
		func(evt upstream.Event) error {
			collector.Accept(evt)
			if evt.Event == "response.output_text.delta" {
				state.textStarted = true
			}
			return writeResponsesEvent(writer, state, evt, usageRecorder)
		},
	)
	if err != nil && !state.terminalSeen {
		return aggregate.Result{}, err
	}
	if !state.terminalSeen {
		return aggregate.Result{}, io.ErrUnexpectedEOF
	}
	if state.terminalFailure != nil {
		result, resultErr := collector.Result()
		if resultErr != nil {
			return aggregate.Result{}, state.terminalFailure
		}
		return result, state.terminalFailure
	}
	result, err := collector.Result()
	if err != nil {
		return aggregate.Result{}, err
	}
	return result, nil
}

func writeResponsesEvent(writer EventWriter, state *responsesStreamState, evt upstream.Event, usageRecorder usageRecorderFunc) error {
	h := newResponseEventWriterHelper(writer.DownstreamType(), responseProjectionState{
		createdSent:          state.createdSent,
		createdResponseID:    state.createdResponseID,
		modelName:            state.modelName,
		toolIDAliases:        state.toolIDAliases,
		toolItems:            state.toolItems,
		toolOrder:            state.toolOrder,
		reasoningStarted:     state.reasoningStarted,
		reasoningClosed:      state.reasoningClosed,
		syntheticInjected:    state.syntheticInjected,
		realReasoningSeen:    state.realReasoningSeen,
		syntheticSummary:     &state.syntheticSummary,
		requestID:            state.requestID,
		upstreamEndpointType: state.upstreamEndpointType,
		terminalSeen:         state.terminalSeen,
		terminalFailure:      state.terminalFailure,
	})

	result, err := doProcessResponseEvent(h, evt)
	if err != nil {
		return err
	}

	state.toolIDAliases = h.toolIDAliases
	state.createdSent = h.createdSent
	state.createdResponseID = h.createdResponseID
	state.modelName = h.modelName
	state.toolItems = h.toolItems
	state.toolOrder = h.toolOrder
	state.reasoningStarted = h.reasoningStarted
	state.reasoningClosed = h.reasoningClosed
	state.syntheticInjected = h.syntheticInjected
	state.realReasoningSeen = h.realReasoningSeen
	state.terminalSeen = h.terminalSeen
	state.terminalFailure = h.terminalFailure

	for _, cmd := range h.events {
		logDownstreamToolEvent(state.requestID, writer.DownstreamType(), cmd.Event, cmd.Data)
		if err := writer.WriteEvent(cmd.Event, cmd.Data); err != nil {
			return err
		}
	}

	if result.skipWrite {
		return nil
	}

	if usageRecorder != nil && (evt.Event == "response.completed" || evt.Event == "response.done") {
		if usage := usageFromEventData(evt.Data); len(usage) > 0 {
			usageRecorder(usage)
		}
	}

	logDownstreamToolEvent(state.requestID, writer.DownstreamType(), evt.Event, evt.Data)
	return writer.WriteEvent(evt.Event, evt.Data)
}

func logDownstreamToolEvent(requestID, downstreamType, event string, data map[string]any) {
	if requestID == "" || len(data) == 0 {
		return
	}
	if item, _ := data["item"].(map[string]any); item != nil {
		if itemType, _ := item["type"].(string); itemType == "function_call" {
			arguments, _ := item["arguments"].(string)
			logging.Event("downstreamToolEvent", map[string]any{
				"request_id":        requestID,
				"downstream_type":   downstreamType,
				"event":             event,
				"item_id":           stringValue(item["id"]),
				"call_id":           stringValue(item["call_id"]),
				"name":              stringValue(item["name"]),
				"arguments_len":     len(arguments),
				"arguments_preview": truncateForLog(arguments, 120),
			})
		}
		return
	}
	if event == "response.function_call_arguments.done" || event == "response.function_call_arguments.delta" {
		arguments := stringValue(data["arguments"])
		if arguments == "" {
			arguments = stringValue(data["delta"])
		}
		logging.Event("downstreamToolEvent", map[string]any{
			"request_id":        requestID,
			"downstream_type":   downstreamType,
			"event":             event,
			"item_id":           stringValue(data["item_id"]),
			"arguments_len":     len(arguments),
			"arguments_preview": truncateForLog(arguments, 120),
		})
	}
}

func withParsedToolParameters(item map[string]any) map[string]any {
	if item == nil {
		return item
	}
	if itemType, _ := item["type"].(string); itemType != "function_call" {
		return item
	}
	arguments, _ := item["arguments"].(string)
	if strings.TrimSpace(arguments) == "" {
		return item
	}
	if _, exists := item["parameters"]; exists {
		return item
	}
	parsedMap, normalized, ok := syntaxrepair.ParseJSONObject(arguments)
	if ok && len(parsedMap) > 0 {
		itemCopy := cloneMap(item)
		if normalized != arguments {
			itemCopy["arguments"] = normalized
		}
		itemCopy["parameters"] = parsedMap
		return itemCopy
	}
	return item
}

func isValidToolArgumentsJSON(arguments string) bool {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return true
	}
	_, _, ok := syntaxrepair.ParseJSONValue(trimmed)
	return ok
}

func truncateForLog(text string, max int) string {
	if max <= 0 || len(text) <= max {
		return text
	}
	return text[:max]
}

func newResponseEventWriterHelper(downstreamType string, state responseProjectionState) *responseEventWriterHelper {
	return &responseEventWriterHelper{
		downstreamType:       downstreamType,
		upstreamEndpointType: state.upstreamEndpointType,
		createdSent:          state.createdSent,
		createdResponseID:    state.createdResponseID,
		modelName:            state.modelName,
		toolIDAliases:        state.toolIDAliases,
		toolItems:            state.toolItems,
		toolOrder:            state.toolOrder,
		reasoningStarted:     state.reasoningStarted,
		reasoningClosed:      state.reasoningClosed,
		syntheticInjected:    state.syntheticInjected,
		realReasoningSeen:    state.realReasoningSeen,
		syntheticSummary:     state.syntheticSummary,
		requestID:            state.requestID,
		terminalSeen:         state.terminalSeen,
		terminalFailure:      state.terminalFailure,
	}
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
		state.syntheticInjected = true
	}
	if !strings.HasSuffix(text, "\n\n") {
		if strings.HasSuffix(text, "\n") {
			text += "\n"
		} else {
			text += "\n\n"
		}
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

func writeSyntheticResponsesReasoningTick(w http.ResponseWriter, flusher http.Flusher) error {
	payload := map[string]any{"type": "response.reasoning_summary_text.delta", "delta": "…"}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprint(w, "event: response.reasoning_summary_text.delta\n"); err != nil {
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

func writeAnthropicSSELive(ctx context.Context, stream *upstream.EventStream, w http.ResponseWriter, flusher http.Flusher, req model.CanonicalRequest, state *anthropicStreamState, upstreamEndpointType string, usageRecorder usageRecorderFunc) error {
	if state == nil {
		state = &anthropicStreamState{}
	}
	state.messageID = req.RequestID
	state.modelName = req.Model
	if state.pendingToolArgs == nil {
		state.pendingToolArgs = map[string]string{}
	}
	if state.toolMeta == nil {
		state.toolMeta = map[string]map[string]string{}
	}
	helper := &responseEventWriterHelper{
		downstreamType:       "anthropic",
		upstreamEndpointType: upstreamEndpointType,
		requestID:            req.RequestID,
	}
	writer := NewAnthropicEventWriter(w, flusher, state, helper, usageRecorder)
	if err := writeSSEPadding(w, flusher); err != nil {
		return err
	}
	if err := startAnthropicUnreasonedPlaceholder(w, flusher, state); err != nil {
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
			return writer.WriteEvent(evt.Event, evt.Data)
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
		"delta": map[string]any{"type": "thinking_delta", "thinking": syntheticReasoningPrelude()},
	})
}

func writeAnthropicEvent(w http.ResponseWriter, flusher http.Flusher, state *anthropicStreamState, evt upstream.Event, usageRecorder usageRecorderFunc) error {
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
		rawItemID := stringValue(item["id"])
		itemID := anthropicToolStateKey(item)
		if state.toolMeta == nil {
			state.toolMeta = map[string]map[string]string{}
		}
		meta := map[string]string{
			"id":      rawItemID,
			"call_id": stringValue(item["call_id"]),
			"name":    stringValue(item["name"]),
		}
		if itemID != "" {
			state.toolMeta[itemID] = meta
		}
		if rawItemID != "" {
			state.toolMeta[rawItemID] = meta
		}
		if state.toolStarted && !state.toolStopped {
			if state.toolItemID == itemID && itemID != "" {
				if state.toolDeltaSent {
					return nil
				}
				arguments := stringValue(item["arguments"])
				if arguments == "" {
					return nil
				}
				if repaired, ok := syntaxrepair.RepairJSON(arguments); ok {
					arguments = repaired
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
		if rawItemID != "" && rawItemID != itemID {
			arguments += state.pendingToolArgs[rawItemID]
		}
		if directArguments := stringValue(item["arguments"]); directArguments != "" {
			arguments += directArguments
		}
		if repaired, ok := syntaxrepair.RepairJSON(arguments); ok {
			arguments = repaired
		}
		delete(state.pendingToolArgs, itemID)
		if rawItemID != "" && rawItemID != itemID {
			delete(state.pendingToolArgs, rawItemID)
		}
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
		if meta := state.toolMeta[itemID]; meta != nil {
			return startToolBlock(map[string]any{
				"type":    "function_call",
				"id":      meta["id"],
				"call_id": meta["call_id"],
				"name":    meta["name"],
			})
		}
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
		stopReason := anthropicStreamStopReason(state.stopReason, evt.Data)
		rawUsage := usageFromEventData(evt.Data)
		usage := anthropicUsageFromEvent(evt.Data)
		if usage == nil {
			usage = map[string]any{}
		}
		if err := writeAnthropicSSEEvent(w, flusher, "message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": stopReason},
			"usage": usage,
		}); err != nil {
			return err
		}
		if usageRecorder != nil && len(rawUsage) > 0 {
			usageRecorder(rawUsage)
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

func anthropicToolStateKey(item map[string]any) string {
	if callID := stringValue(item["call_id"]); callID != "" {
		return callID
	}
	return stringValue(item["id"])
}

func normalizeHTTPAPIUpstreamEndpointType(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case config.UpstreamEndpointTypeChat:
		return config.UpstreamEndpointTypeChat
	case config.UpstreamEndpointTypeAnthropic:
		return config.UpstreamEndpointTypeAnthropic
	default:
		return config.UpstreamEndpointTypeResponses
	}
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
		"id":      messageID,
		"type":    "message",
		"role":    "assistant",
		"model":   modelName,
		"content": []any{},
		"usage": map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
		},
		"stop_reason": nil,
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
	if input, ok := usage["input_tokens"]; ok {
		out["input_tokens"] = input
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

func anthropicStreamStopReason(current string, data map[string]any) string {
	// Try stop_reason directly (backward compat), then response.stop_reason, then response.finish_reason (unified format)
	if stopReason, _ := data["stop_reason"].(string); stopReason != "" {
		return normalizeAnthropicStreamStopReason(stopReason)
	}
	if response, _ := data["response"].(map[string]any); response != nil {
		if stopReason, _ := response["stop_reason"].(string); stopReason != "" {
			return normalizeAnthropicStreamStopReason(stopReason)
		}
		// Unified format uses finish_reason instead of stop_reason
		if finishReason, _ := response["finish_reason"].(string); finishReason != "" {
			return normalizeAnthropicStreamStopReason(finishReason)
		}
	}
	if current != "" {
		return normalizeAnthropicStreamStopReason(current)
	}
	return "end_turn"
}

func normalizeAnthropicStreamStopReason(reason string) string {
	switch strings.TrimSpace(reason) {
	case "", "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length", "max_tokens":
		return "max_tokens"
	default:
		return reason
	}
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
	chunkID             string
	modelName           string
	roleSent            bool
	textStarted         bool
	realReasoningSeen   bool
	thinkingTagStyle    string
	planningSent        bool
	toolStatusSent      bool
	toolIDAliases       map[string]string
	toolMeta            map[string]map[string]string
	toolIndex           map[string]int
	toolSent            map[string]bool
	pendingToolArgs     map[string]string
	nextToolIx          int
	terminalSeen        bool
	terminalFailure     *aggregate.TerminalFailureError
	pendingReasoningTag string
}

func writeChatSSELive(ctx context.Context, stream *upstream.EventStream, w http.ResponseWriter, flusher http.Flusher, req model.CanonicalRequest, upstreamEndpointType string, thinkingTagStyle string, usageRecorder usageRecorderFunc) error {
	state := chatStreamState{
		chunkID:          "chatcmpl_proxy",
		modelName:        req.Model,
		toolIDAliases:    map[string]string{},
		toolMeta:         map[string]map[string]string{},
		toolIndex:        map[string]int{},
		toolSent:         map[string]bool{},
		pendingToolArgs:  map[string]string{},
		thinkingTagStyle: thinkingTagStyle,
	}
	if req.RequestID != "" {
		state.chunkID = "chatcmpl_" + req.RequestID
	}
	helper := &responseEventWriterHelper{
		downstreamType:       "chat",
		upstreamEndpointType: upstreamEndpointType,
		requestID:            req.RequestID,
	}
	writer := NewChatEventWriter(w, flusher, &state, helper, usageRecorder)
	if err := writeSSEPadding(w, flusher); err != nil {
		return err
	}
	if err := writeChatChunk(w, flusher, &state, map[string]any{"reasoning_content": syntheticReasoningPrelude()}, "", nil); err != nil {
		return err
	}
	err := streamLiveWithSyntheticTicks(ctx, stream.Consume,
		func() bool { return state.textStarted || state.realReasoningSeen },
		func() error {
			if state.textStarted || state.realReasoningSeen {
				return nil
			}
			return writeChatChunk(w, flusher, &state, map[string]any{"reasoning_content": "\u200b"}, "", nil)
		},
		func() error { return writeSSEComment(w, flusher, "keep-alive") },
		func(evt upstream.Event) error {
			return writer.WriteEvent(evt.Event, evt.Data)
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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

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
		select {
		case <-ctx.Done():
		default:
			signals <- streamSignal{err: err, done: true}
		}
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
				return io.ErrUnexpectedEOF
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
		chunkID:         "chatcmpl_proxy",
		toolIDAliases:   map[string]string{},
		toolMeta:        map[string]map[string]string{},
		toolIndex:       map[string]int{},
		toolSent:        map[string]bool{},
		pendingToolArgs: map[string]string{},
	}
	for _, evt := range events {
		if err := writeChatEvent(w, flusher, &state, evt, includeUsage, nil); err != nil {
			return err
		}
	}
	return nil
}

func chatStreamFinishReason(state *chatStreamState, data map[string]any) string {
	// Try direct finish_reason first, then look inside response wrapper (unified format)
	if finishReason, _ := data["finish_reason"].(string); finishReason != "" {
		return finishReason
	}
	if response, _ := data["response"].(map[string]any); response != nil {
		if finishReason, _ := response["finish_reason"].(string); finishReason != "" {
			return finishReason
		}
	}
	if state.textStarted {
		return "stop"
	}
	if len(state.toolSent) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func writeChatEvent(w http.ResponseWriter, flusher http.Flusher, state *chatStreamState, evt upstream.Event, includeUsage bool, usageRecorder usageRecorderFunc) error {
	if state.toolIDAliases == nil {
		state.toolIDAliases = map[string]string{}
	}
	ensureRoleSent := func() error {
		if state.roleSent {
			return nil
		}
		if err := writeChatChunk(w, flusher, state, map[string]any{"role": "assistant"}, "", nil); err != nil {
			return err
		}
		state.roleSent = true
		return nil
	}
	flushPendingToolCall := func(itemID string) error {
		if mapped, ok := state.toolIDAliases[itemID]; ok && mapped != "" {
			itemID = mapped
		}
		if itemID == "" || state.toolSent[itemID] {
			return nil
		}
		meta, ok := state.toolMeta[itemID]
		if !ok {
			return nil
		}
		if _, ok := state.toolIndex[itemID]; !ok {
			state.toolIndex[itemID] = state.nextToolIx
			state.nextToolIx++
		}
		if err := ensureRoleSent(); err != nil {
			return err
		}
		toolDelta := chatToolDelta(state.toolIndex[itemID], meta["call_id"], meta["name"], state.pendingToolArgs[itemID], true)
		if repaired, ok := syntaxrepair.RepairJSON(state.pendingToolArgs[itemID]); ok {
			toolDelta = chatToolDelta(state.toolIndex[itemID], meta["call_id"], meta["name"], repaired, true)
		}
		if err := writeChatChunk(w, flusher, state, toolDelta, "", nil); err != nil {
			return err
		}
		state.toolSent[itemID] = true
		delete(state.pendingToolArgs, itemID)
		return nil
	}
	flushAllPendingToolCalls := func() error {
		for itemID := range state.toolMeta {
			if err := flushPendingToolCall(itemID); err != nil {
				return err
			}
		}
		return nil
	}
	switch evt.Event {
	case "response.created":
		if response, _ := evt.Data["response"].(map[string]any); response != nil {
			if id := stringValue(response["id"]); id != "" {
				state.chunkID = id
			}
			if model := stringValue(response["model"]); model != "" {
				state.modelName = model
			}
		}
	case "response.output_item.added", "response.output_item.done":
		item, _ := evt.Data["item"].(map[string]any)
		if reasoningContent := reasoningSummaryFromItem(item); reasoningContent != "" {
			state.realReasoningSeen = true
			if err := ensureRoleSent(); err != nil {
				return err
			}
			if err := writeChatChunk(w, flusher, state, map[string]any{"reasoning_content": reasoningContent}, "", nil); err != nil {
				return err
			}
		}
		if itemType, _ := item["type"].(string); itemType == "function_call" {
			rawItemID, _ := item["id"].(string)
			itemID := rawItemID
			if callID, _ := item["call_id"].(string); callID != "" {
				if rawItemID != "" && rawItemID != callID {
					state.toolIDAliases[rawItemID] = callID
					if pending := state.pendingToolArgs[rawItemID]; pending != "" && state.pendingToolArgs[callID] == "" {
						state.pendingToolArgs[callID] = pending
						delete(state.pendingToolArgs, rawItemID)
					}
				}
				itemID = callID
			}
			if itemID != "" {
				if _, ok := state.toolIndex[itemID]; !ok {
					state.toolIndex[itemID] = state.nextToolIx
					state.nextToolIx++
				}
				state.toolMeta[itemID] = map[string]string{
					"name":    stringValue(item["name"]),
					"call_id": stringValue(item["call_id"]),
				}
				if directArgs := stringValue(item["arguments"]); directArgs != "" {
					if repaired, ok := syntaxrepair.RepairJSON(directArgs); ok {
						directArgs = repaired
					}
					state.pendingToolArgs[itemID] = directArgs
				}
				if state.pendingToolArgs[itemID] != "" {
					if err := flushPendingToolCall(itemID); err != nil {
						return err
					}
				}
			}
		}
	case "response.output_text.delta":
		state.textStarted = true
		if err := ensureRoleSent(); err != nil {
			return err
		}
		delta := stringValue(evt.Data["delta"])
		if delta == "" && state.pendingReasoningTag == "" {
			break
		}
		// Prepend any pending incomplete reasoning tag
		if state.pendingReasoningTag != "" {
			delta = state.pendingReasoningTag + delta
			state.pendingReasoningTag = ""
		}
		cleanContent := delta
		reasoningContent := ""
		if state.thinkingTagStyle == config.UpstreamThinkingTagStyleLegacy {
			cleanContent, reasoningContent = extractReasoningTags(delta)
			if tagOpen, openIdx := trailingReasoningOpenTag(cleanContent); tagOpen != "" && openIdx >= 0 {
				state.pendingReasoningTag = cleanContent[openIdx:]
				cleanContent = cleanContent[:openIdx]
			}
		}
		if reasoningContent != "" {
			state.realReasoningSeen = true
			if err := writeChatChunk(w, flusher, state, map[string]any{"reasoning_content": reasoningContent}, "", nil); err != nil {
				return err
			}
		}
		if cleanContent != "" {
			if err := writeChatChunk(w, flusher, state, map[string]any{"content": cleanContent}, "", nil); err != nil {
				return err
			}
		}
	case "response.reasoning.delta", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		if delta := reasoningContentValue(evt.Data); delta != "" {
			state.realReasoningSeen = true
			if err := ensureRoleSent(); err != nil {
				return err
			}
			if err := writeChatChunk(w, flusher, state, map[string]any{"reasoning_content": delta}, "", nil); err != nil {
				return err
			}
		}
	case "response.function_call_arguments.delta":
		itemID := stringValue(evt.Data["item_id"])
		if mapped, ok := state.toolIDAliases[itemID]; ok && mapped != "" {
			itemID = mapped
		}
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
			if err := writeChatChunk(w, flusher, state, toolDelta, "", nil); err != nil {
				return err
			}
			state.toolSent[itemID] = true
			delete(state.pendingToolArgs, itemID)
			return nil
		}
		index := state.toolIndex[itemID]
		toolDelta := chatToolDelta(index, "", "", delta, false)
		if err := writeChatChunk(w, flusher, state, toolDelta, "", nil); err != nil {
			return err
		}
	case "response.completed", "response.done":
		state.terminalSeen = true
		if response, _ := evt.Data["response"].(map[string]any); response != nil {
			if id := stringValue(response["id"]); id != "" {
				state.chunkID = id
			}
			if model := stringValue(response["model"]); model != "" {
				state.modelName = model
			}
		}
		if err := flushAllPendingToolCalls(); err != nil {
			return err
		}
		finishReason := chatStreamFinishReason(state, evt.Data)
		rawUsage := usageFromEventData(evt.Data)
		cachedTokens := nestedCachedTokens(rawUsage)
		logging.Event("upstreamStreamUsageObserved", map[string]any{
			"upstream_event":       evt.Event,
			"cached_tokens":        cachedTokens,
			"stream_include_usage": includeUsage,
		})
		var usagePayload any
		if includeUsage {
			if usage := chatUsage(rawUsage); usage != nil {
				logging.Event("downstream_stream_usage_mapped", map[string]any{
					"upstream_event": evt.Event,
					"cached_tokens":  nestedCachedTokens(mapUsageForLogging(usage)),
				})
				usagePayload = usage
			}
		}
		if err := writeChatChunk(w, flusher, state, map[string]any{}, finishReason, usagePayload); err != nil {
			return err
		}
		if usageRecorder != nil && len(rawUsage) > 0 {
			usageRecorder(rawUsage)
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
		healthFlag = "upstreamStreamBroken"
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

func writeChatChunk(w http.ResponseWriter, flusher http.Flusher, state *chatStreamState, delta map[string]any, finishReason string, usage any) error {
	chunk := map[string]any{"object": "chat.completion.chunk"}
	if state != nil {
		if state.chunkID != "" {
			chunk["id"] = state.chunkID
		}
		if state.modelName != "" {
			chunk["model"] = state.modelName
		}
	}
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
	if err := writeChatChunk(w, flusher, nil, map[string]any{"error": map[string]any{"health_flag": healthFlag, "message": message}}, "error", nil); err != nil {
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
		if cachedTokens, ok := details["cached_tokens"]; ok {
			result["cached_tokens"] = cachedTokens
		}
		if cacheCreationTokens, ok := details["cache_creation_tokens"]; ok {
			result["cache_creation_tokens"] = cacheCreationTokens
		}
	}
	if details, _ := usage["output_tokens_details"].(map[string]any); len(details) > 0 {
		result["completion_tokens_details"] = cloneMap(details)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func extractReasoningTags(text string) (cleanText string, reasoningContent string) {
	cleanText = text
	reasoningContent = ""

	for _, tag := range []struct{ open, close string }{
		{open: "<think>", close: "</think>"},
		{open: "<thinking>", close: "</thinking>"},
		{open: "<reasoning>", close: "</reasoning>"},
	} {
		for {
			openIdx := strings.Index(cleanText, tag.open)
			if openIdx == -1 {
				break
			}
			closeIdx := strings.Index(cleanText[openIdx:], tag.close)
			if closeIdx == -1 {
				break
			}
			closeIdx += openIdx
			reasoningContent += cleanText[openIdx+len(tag.open) : closeIdx]
			cleanText = cleanText[:openIdx] + cleanText[closeIdx+len(tag.close):]
		}
	}

	return cleanText, reasoningContent
}

func trailingReasoningOpenTag(text string) (tag string, openIdx int) {
	for _, candidate := range []string{"<reasoning>", "<thinking>", "<think>"} {
		if strings.HasSuffix(text, candidate) {
			idx := strings.LastIndex(text, candidate)
			if idx >= 0 {
				return candidate, idx
			}
		}
	}
	return "", -1
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
