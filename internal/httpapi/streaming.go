package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"openai-compat-proxy/internal/aggregate"
	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/contextoverflow"
	"openai-compat-proxy/internal/logging"
	"openai-compat-proxy/internal/model"
	reasoningtext "openai-compat-proxy/internal/reasoning"
	"openai-compat-proxy/internal/syntaxrepair"
	"openai-compat-proxy/internal/upstream"
)

const initialSyntheticReasoningLeadTime = 350 * time.Millisecond

var syntheticReasoningTickInterval = 250 * time.Millisecond
var sseHeartbeatInterval = 15 * time.Second

const syntheticReasoningPlaceholder = "**推理中**\n\n代理层占位，以兼容不同上游情况，便于客户端记录推理时长"
const invisibleSyntheticReasoningDelta = "\u200b"
const internalReasoningFormatItemIDKey = "_proxy_reasoning_format_item_id"
const projectedReasoningFormatItemIDPrefix = "proxy-reasoning-summary"

func syntheticReasoningPrelude() string {
	return syntheticReasoningPlaceholder + "\n\n"
}

func invisibleSyntheticReasoningPrelude() string {
	return invisibleSyntheticReasoningDelta
}

func sanitizeSyntheticReasoningText(text string) string {
	if strings.Contains(text, syntheticReasoningPlaceholder) {
		return invisibleSyntheticReasoningPrelude()
	}
	return text
}

func isInvisibleSyntheticReasoningText(text string) bool {
	stripped := strings.ReplaceAll(text, invisibleSyntheticReasoningDelta, "")
	return strings.TrimSpace(stripped) == ""
}

type usageRecorderFunc func(map[string]any)

type anthropicStreamState struct {
	messageStarted     bool
	messageID          string
	modelName          string
	textStarted        bool
	textIndex          int
	textStopped        bool
	textTail           visibleTextTailBuffer
	thinkingStarted    bool
	thinkingStopped    bool
	signatureSent      bool
	thinkingIndex      int
	thinkingType       string
	thinkingSignature  string
	toolStarted        bool
	toolIndex          int
	toolStopped        bool
	stopReason         string
	nextIndex          int
	realThinkingSeen   bool
	reasoningText      strings.Builder
	reasoningSummaries map[reasoningSummaryKey]*reasoningSummaryState
	planningSent       bool
	toolStatusSent     bool
	toolItemID         string
	toolDeltaSent      bool
	pendingToolArgs    map[string]string
	toolMeta           map[string]map[string]string
	emittedToolItems   map[string]bool
	terminalSeen       bool
	terminalFailure    *aggregate.TerminalFailureError
}

type responsesStreamState struct {
	createdSent                bool
	createdResponseID          string
	modelName                  string
	textStarted                bool
	realReasoningSeen          bool
	planningSent               bool
	toolStatusSent             bool
	syntheticReasoningStarted  bool
	syntheticReasoningClosed   bool
	realReasoningStarted       bool
	realReasoningClosed        bool
	realReasoningItemID        string
	realReasoningSummary       strings.Builder
	reasoningSummaryParts      map[string]map[int]*strings.Builder
	reasoningSummaryPartClosed map[string]map[int]bool
	reasoningSummaryTextDone   map[string]map[int]bool
	activeReasoningItems       map[string]bool
	reasoningItemsClosed       map[string]bool
	formattedReasoning         map[string]*strings.Builder
	syntheticInjected          bool
	syntheticSummary           strings.Builder
	toolItems                  map[string]*responsesToolItemState
	toolIDAliases              map[string]string
	toolOrder                  []string
	outputItems                map[string]map[string]any
	outputItemOrder            []string
	textItemStarted            bool
	textOutputs                map[responseTextTailKey]*strings.Builder
	textTailBuffers            map[responseTextTailKey]*visibleTextTailBuffer
	completedTextItems         map[string]bool
	completedTextParts         map[responseTextTailKey]bool
	upstreamEndpointType       string
	requestID                  string
	terminalSeen               bool
	terminalFailure            *aggregate.TerminalFailureError
}

type responseTextTailKey struct {
	itemID       string
	contentIndex int
}

type reasoningSummaryKey struct {
	itemID       string
	summaryIndex int
}

type reasoningSummaryState struct {
	emitted strings.Builder
	done    bool
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
	createdSent                bool
	createdResponseID          string
	modelName                  string
	toolIDAliases              map[string]string
	toolItems                  map[string]*responsesToolItemState
	toolOrder                  []string
	outputItems                map[string]map[string]any
	outputItemOrder            []string
	textItemStarted            bool
	textOutputs                map[responseTextTailKey]*strings.Builder
	textTailBuffers            map[responseTextTailKey]*visibleTextTailBuffer
	completedTextItems         map[string]bool
	completedTextParts         map[responseTextTailKey]bool
	syntheticReasoningStarted  bool
	syntheticReasoningClosed   bool
	realReasoningStarted       bool
	realReasoningClosed        bool
	realReasoningItemID        string
	realReasoningSummary       *strings.Builder
	reasoningSummaryParts      map[string]map[int]*strings.Builder
	reasoningSummaryPartClosed map[string]map[int]bool
	reasoningSummaryTextDone   map[string]map[int]bool
	activeReasoningItems       map[string]bool
	reasoningItemsClosed       map[string]bool
	formattedReasoning         map[string]*strings.Builder
	syntheticInjected          bool
	realReasoningSeen          bool
	compactionLifecycleStarted bool
	syntheticSummary           *strings.Builder
	requestID                  string
	upstreamEndpointType       string
	terminalSeen               bool
	terminalFailure            *aggregate.TerminalFailureError
}

type responseEventWriterHelper struct {
	downstreamType              string
	upstreamEndpointType        string
	createdSent                 bool
	createdResponseID           string
	modelName                   string
	toolIDAliases               map[string]string
	toolItems                   map[string]*responsesToolItemState
	toolOrder                   []string
	outputItems                 map[string]map[string]any
	outputItemOrder             []string
	textItemStarted             bool
	textOutputs                 map[responseTextTailKey]*strings.Builder
	textTailBuffers             map[responseTextTailKey]*visibleTextTailBuffer
	completedTextItems          map[string]bool
	completedTextParts          map[responseTextTailKey]bool
	syntheticReasoningStarted   bool
	syntheticReasoningClosed    bool
	realReasoningStarted        bool
	realReasoningClosed         bool
	realReasoningItemID         string
	realReasoningSummary        *strings.Builder
	reasoningFormatPhase        int
	reasoningFormatItemID       string
	reasoningFormatAliasPending bool
	reasoningFormatAliases      map[reasoningSummaryKey]string
	reasoningSummaryParts       map[string]map[int]*strings.Builder
	reasoningSummaryPartClosed  map[string]map[int]bool
	reasoningSummaryTextDone    map[string]map[int]bool
	activeReasoningItems        map[string]bool
	reasoningItemsClosed        map[string]bool
	formattedReasoning          map[string]*strings.Builder
	syntheticInjected           bool
	realReasoningSeen           bool
	compactionLifecycleStarted  bool
	syntheticSummary            *strings.Builder
	requestID                   string
	terminalSeen                bool
	terminalFailure             *aggregate.TerminalFailureError
	events                      []processEventCommand
}

func reasoningFormatItemID(data map[string]any) string {
	if itemID := stringValue(data[internalReasoningFormatItemIDKey]); itemID != "" {
		return itemID
	}
	if itemID := stringValue(data["item_id"]); itemID != "" {
		return itemID
	}
	return stringValue(data["id"])
}

func reasoningFormatSummaryIndex(data map[string]any, summaryIndex int) int {
	if stringValue(data[internalReasoningFormatItemIDKey]) != "" {
		return 0
	}
	return summaryIndex
}

func (h *responseEventWriterHelper) beginReasoningFormatPhase() string {
	if h.reasoningFormatItemID == "" {
		h.reasoningFormatPhase++
		h.reasoningFormatItemID = fmt.Sprintf("%s-%d", projectedReasoningFormatItemIDPrefix, h.reasoningFormatPhase)
		h.reasoningFormatAliasPending = true
	}
	return h.reasoningFormatItemID
}

func (h *responseEventWriterHelper) associateReasoningFormatItem(data map[string]any, allowBootstrap bool) {
	if h.downstreamType == "responses" {
		return
	}
	itemID := stringValue(data["item_id"])
	if itemID == "" {
		return
	}
	summaryIndex, ok := intValue(data["summary_index"])
	if !ok {
		summaryIndex = 0
	}
	key := reasoningSummaryKey{itemID: itemID, summaryIndex: summaryIndex}
	if h.reasoningFormatAliases == nil {
		h.reasoningFormatAliases = map[reasoningSummaryKey]string{}
	}
	formatItemID, exists := h.reasoningFormatAliases[key]
	if !exists {
		if h.reasoningFormatAliasPending && h.reasoningFormatItemID != "" {
			formatItemID = h.reasoningFormatItemID
			h.reasoningFormatAliasPending = false
		} else {
			formatItemID = h.reasoningFormatAliases[reasoningSummaryKey{itemID: itemID}]
			if formatItemID == "" {
				for aliasKey, candidate := range h.reasoningFormatAliases {
					if aliasKey.itemID == itemID && candidate != "" {
						formatItemID = candidate
						break
					}
				}
			}
			if formatItemID == "" {
				if !allowBootstrap {
					return
				}
				formatItemID = h.beginReasoningFormatPhase()
				h.reasoningFormatAliasPending = false
			}
		}
		h.reasoningFormatAliases[key] = formatItemID
	}
	data[internalReasoningFormatItemIDKey] = formatItemID
}

func (h *responseEventWriterHelper) associateReasoningFormatOutputItem(item map[string]any) {
	if h.downstreamType == "responses" || len(h.reasoningFormatAliases) == 0 {
		return
	}
	itemID := stringValue(item["id"])
	if itemID == "" {
		return
	}
	summary, _ := item["summary"].([]any)
	for summaryIndex := range summary {
		if formatItemID := h.reasoningFormatAliases[reasoningSummaryKey{itemID: itemID, summaryIndex: summaryIndex}]; formatItemID != "" {
			item[internalReasoningFormatItemIDKey] = formatItemID
			return
		}
	}
}

func (h *responseEventWriterHelper) retireReasoningFormatAliases(item map[string]any) {
	if h.downstreamType == "responses" || len(h.reasoningFormatAliases) == 0 {
		return
	}
	itemID := stringValue(item["id"])
	if itemID == "" {
		return
	}
	for key := range h.reasoningFormatAliases {
		if key.itemID == itemID {
			delete(h.reasoningFormatAliases, key)
		}
	}
}

func (h *responseEventWriterHelper) formatReasoningContentDelta(key, delta string) string {
	if h.formattedReasoning == nil {
		h.formattedReasoning = map[string]*strings.Builder{}
	}
	previous := h.formattedReasoning[key]
	if previous == nil {
		previous = &strings.Builder{}
		h.formattedReasoning[key] = previous
	}
	formattedDelta, combined := reasoningtext.FormatDelta(previous.String(), delta)
	previous.Reset()
	previous.WriteString(combined)
	return formattedDelta
}

func (h *responseEventWriterHelper) formatReasoningEvent(evt *upstream.Event) {
	if evt == nil || evt.Data == nil {
		return
	}
	if evt.Event == "response.reasoning.delta" {
		for _, key := range []string{"summary", "thinking", "reasoning_content", "reasoning", "content", "delta", "text"} {
			if text, ok := evt.Data[key].(string); ok && text != "" {
				evt.Data[key] = h.formatReasoningContentDelta(key, text)
			}
		}
	}
	key := ""
	switch evt.Event {
	case "response.reasoning_summary_text.delta":
		key = "delta"
	case "response.reasoning_summary_text.done":
		key = "text"
	}
	summaryTextEvent := key != ""
	if key != "" && (h.downstreamType == "responses" || !summaryTextEvent) {
		if text, ok := evt.Data[key].(string); ok && text != "" {
			evt.Data[key] = h.formatReasoningContentDelta(key, text)
		}
	}
	if part, ok := evt.Data["part"].(map[string]any); ok {
		evt.Data["part"] = reasoningtext.FormatBlock(part)
	}
	if blocks, ok := evt.Data["blocks"].([]any); ok {
		formatted := make([]any, len(blocks))
		for index, rawBlock := range blocks {
			block, ok := rawBlock.(map[string]any)
			if ok {
				formatted[index] = reasoningtext.FormatBlock(block)
			} else {
				formatted[index] = rawBlock
			}
		}
		evt.Data["blocks"] = formatted
	}
}

func (h *responseEventWriterHelper) resetFormattedReasoning() {
	h.formattedReasoning = nil
	h.reasoningFormatItemID = ""
	h.reasoningFormatAliasPending = false
}

func formatStreamingReasoningDelta(previous *strings.Builder, delta string) string {
	if previous == nil || delta == "" {
		return delta
	}
	formattedDelta, combined := reasoningtext.FormatDelta(previous.String(), delta)
	previous.Reset()
	previous.WriteString(combined)
	return formattedDelta
}

func formatStreamingReasoningSnapshot(previous *strings.Builder, snapshot string) string {
	if previous == nil || snapshot == "" {
		return snapshot
	}
	emitted := previous.String()
	normalized := reasoningtext.FormatText(snapshot)
	if snapshot == emitted || normalized == emitted {
		return ""
	}
	if strings.HasPrefix(snapshot, emitted) {
		return formatStreamingReasoningDelta(previous, strings.TrimPrefix(snapshot, emitted))
	}
	if strings.HasPrefix(normalized, emitted) {
		formattedDelta := strings.TrimPrefix(normalized, emitted)
		previous.Reset()
		previous.WriteString(normalized)
		return formattedDelta
	}
	return ""
}

func formatStreamingReasoningSummary(states *map[reasoningSummaryKey]*reasoningSummaryState, itemID string, summaryIndex int, text string, snapshot bool) string {
	if text == "" {
		return ""
	}
	if itemID == "" {
		itemID = "default-reasoning-summary"
	}
	if *states == nil {
		*states = map[reasoningSummaryKey]*reasoningSummaryState{}
	}
	key := reasoningSummaryKey{itemID: itemID, summaryIndex: summaryIndex}
	state := (*states)[key]
	if state == nil {
		state = &reasoningSummaryState{}
		(*states)[key] = state
	}
	if snapshot {
		if state.done {
			return ""
		}
		state.done = true
		return formatStreamingReasoningSnapshot(&state.emitted, text)
	}
	return formatStreamingReasoningDelta(&state.emitted, text)
}

func formatStreamingReasoningItemSummary(states *map[reasoningSummaryKey]*reasoningSummaryState, item map[string]any) []string {
	parts, _ := item["summary"].([]any)
	if len(parts) == 0 {
		return nil
	}
	itemID := reasoningFormatItemID(item)
	if stringValue(item[internalReasoningFormatItemIDKey]) != "" {
		var summary strings.Builder
		for _, rawPart := range parts {
			part, _ := rawPart.(map[string]any)
			if part != nil {
				summary.WriteString(stringValue(part["text"]))
			}
		}
		if delta := formatStreamingReasoningSummary(states, itemID, 0, summary.String(), true); delta != "" {
			return []string{delta}
		}
		return nil
	}
	deltas := make([]string, 0, len(parts))
	for summaryIndex, rawPart := range parts {
		part, _ := rawPart.(map[string]any)
		if part == nil {
			continue
		}
		if delta := formatStreamingReasoningSummary(states, itemID, summaryIndex, stringValue(part["text"]), true); delta != "" {
			deltas = append(deltas, delta)
		}
	}
	return deltas
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

func (h *responseEventWriterHelper) ensureOutputItems() {
	if h.outputItems == nil {
		h.outputItems = map[string]map[string]any{}
	}
}

func (h *responseEventWriterHelper) ensureReasoningItemStates() {
	if h.activeReasoningItems == nil {
		h.activeReasoningItems = map[string]bool{}
	}
	if h.reasoningItemsClosed == nil {
		h.reasoningItemsClosed = map[string]bool{}
	}
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
	item = cloneJSONValueForResponse(item).(map[string]any)
	item = withParsedToolParameters(item)
	if isResponseToolCallItemType(stringValue(item["type"])) {
		if _, ok := item["arguments"]; !ok {
			item["arguments"] = ""
		}
		item["status"] = "in_progress"
	}
	h.ensureCreatedEvent()
	h.addEvent("response.output_item.added", h.outputItemEventData(item, map[string]any{"item": item}))
}

func (h *responseEventWriterHelper) addToolItemDoneEvent(item map[string]any) {
	item = cloneJSONValueForResponse(item).(map[string]any)
	item = withParsedToolParameters(item)
	if isResponseToolCallItemType(stringValue(item["type"])) {
		item["status"] = "completed"
	}
	h.addEvent("response.output_item.done", h.outputItemEventData(item, map[string]any{"item": item}))
}

func (h *responseEventWriterHelper) addFunctionCallArgumentsDoneEvent(itemID, arguments string) {
	h.addEvent("response.function_call_arguments.done", h.toolEventData(itemID, map[string]any{"item_id": itemID, "arguments": arguments}))
}

func (h *responseEventWriterHelper) addCreatedEvent(id string) {
	if id == "" {
		id = h.currentResponseID()
	}
	response := map[string]any{"id": id, "object": "response", "status": "in_progress", "output": []any{}}
	if h.modelName != "" {
		response["model"] = h.modelName
	}
	h.addEvent("response.created", map[string]any{"response": response})
	h.createdSent = true
	h.createdResponseID = id
}

func (h *responseEventWriterHelper) ensureCreatedEvent() {
	if h.downstreamType != "responses" || h.createdSent {
		return
	}
	h.addCreatedEvent("")
}

func (h *responseEventWriterHelper) currentResponseID() string {
	if h.createdResponseID != "" {
		return h.createdResponseID
	}
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

func responseToolItemStateID(item map[string]any) string {
	if item == nil {
		return ""
	}
	if callID := stringValue(item["call_id"]); callID != "" {
		return callID
	}
	return stringValue(item["id"])
}

func (h *responseEventWriterHelper) outputIndexForItem(itemID string) (int, bool) {
	if itemID == "" {
		return 0, false
	}
	canonicalID := h.canonicalToolItemID(itemID)
	for index, existingID := range h.outputItemOrder {
		if existingID == canonicalID || existingID == itemID {
			return index, true
		}
	}
	return 0, false
}

func (h *responseEventWriterHelper) ensureOutputItemIndex(itemID string) (int, bool) {
	if itemID == "" {
		return 0, false
	}
	if index, ok := h.outputIndexForItem(itemID); ok {
		return index, true
	}
	index := len(h.outputItemOrder)
	h.outputItemOrder = append(h.outputItemOrder, h.canonicalToolItemID(itemID))
	return index, true
}

func (h *responseEventWriterHelper) rememberOutputItem(item map[string]any) {
	itemID := responseToolItemStateID(item)
	if itemID == "" {
		return
	}
	h.ensureOutputItems()
	h.outputItems[h.canonicalToolItemID(itemID)] = cloneJSONValueForResponse(item).(map[string]any)
}

func (h *responseEventWriterHelper) responseOutputSnapshot() []any {
	if len(h.outputItemOrder) == 0 {
		return []any{}
	}
	out := make([]any, 0, len(h.outputItemOrder))
	for _, itemID := range h.outputItemOrder {
		item := h.outputItems[itemID]
		if item == nil {
			continue
		}
		cloned := cloneJSONValueForResponse(item)
		if itemMap, _ := cloned.(map[string]any); itemID == "msg_proxy" && stringValue(itemMap["type"]) == "message" {
			itemMap["status"] = "completed"
		}
		out = append(out, cloned)
	}
	return out
}

func (h *responseEventWriterHelper) completedResponseOutput(rawOutput any) []any {
	snapshot := h.responseOutputSnapshot()
	rawItems, _ := rawOutput.([]any)
	if len(rawItems) == 0 {
		return trimResponseOutputTextLineEndings(snapshot)
	}
	if len(snapshot) == 0 {
		return trimResponseOutputTextLineEndings(cloneJSONValueForResponse(rawItems).([]any))
	}

	completeByID := make(map[string]map[string]any, len(snapshot))
	realReasoning := make([]map[string]any, 0, len(snapshot))
	for _, rawItem := range snapshot {
		item, _ := rawItem.(map[string]any)
		if stringValue(item["type"]) != "reasoning" {
			continue
		}
		if itemID := responseToolItemStateID(item); itemID != "" {
			completeByID[itemID] = item
		}
		if !model.IsSyntheticResponsesReasoningPlaceholder(item) {
			realReasoning = append(realReasoning, item)
		}
	}

	merged := make([]any, 0, len(rawItems))
	realReasoningIndex := 0
	for _, rawItem := range rawItems {
		item, _ := rawItem.(map[string]any)
		if stringValue(item["type"]) != "reasoning" {
			merged = append(merged, cloneJSONValueForResponse(rawItem))
			continue
		}
		itemID := responseToolItemStateID(item)
		complete := completeByID[itemID]
		if itemID == "" && realReasoningIndex < len(realReasoning) {
			complete = realReasoning[realReasoningIndex]
			realReasoningIndex++
		}
		if complete == nil {
			merged = append(merged, cloneJSONValueForResponse(rawItem))
			continue
		}
		combined := cloneJSONValueForResponse(item).(map[string]any)
		for key, value := range complete {
			combined[key] = cloneJSONValueForResponse(value)
		}
		merged = append(merged, combined)
	}
	return trimResponseOutputTextLineEndings(merged)
}

func trimResponseOutputTextLineEndings(output []any) []any {
	trimmed := cloneJSONValueForResponse(output).([]any)
	for itemIndex := len(trimmed) - 1; itemIndex >= 0; itemIndex-- {
		item, _ := trimmed[itemIndex].(map[string]any)
		if stringValue(item["type"]) != "message" {
			continue
		}
		content, _ := item["content"].([]any)
		for contentIndex := len(content) - 1; contentIndex >= 0; contentIndex-- {
			part, _ := content[contentIndex].(map[string]any)
			if stringValue(part["type"]) != "output_text" {
				continue
			}
			part["text"] = trimTrailingTextLineEndings(stringValue(part["text"]))
			return trimmed
		}
	}
	return trimmed
}

func (h *responseEventWriterHelper) outputItemEventData(item map[string]any, data map[string]any) map[string]any {
	if h.downstreamType != "responses" {
		return data
	}
	h.ensureCreatedEvent()
	itemID := responseToolItemStateID(item)
	if outputIndex, exists := intValue(data["output_index"]); exists {
		if itemID != "" {
			h.ensureOutputItems()
			canonicalID := h.canonicalToolItemID(itemID)
			for len(h.outputItemOrder) <= outputIndex {
				h.outputItemOrder = append(h.outputItemOrder, "")
			}
			h.outputItemOrder[outputIndex] = canonicalID
			h.rememberOutputItem(item)
		}
		return data
	}
	if outputIndex, ok := h.ensureOutputItemIndex(itemID); ok {
		data["output_index"] = outputIndex
	}
	h.rememberOutputItem(item)
	return data
}

func (h *responseEventWriterHelper) toolEventData(itemID string, data map[string]any) map[string]any {
	if h.downstreamType != "responses" {
		return data
	}
	if _, exists := data["output_index"]; exists {
		return data
	}
	if outputIndex, ok := h.outputIndexForItem(itemID); ok {
		data["output_index"] = outputIndex
	}
	return data
}

func (h *responseEventWriterHelper) outputTextDeltaData(data map[string]any) map[string]any {
	if h.downstreamType != "responses" {
		return data
	}
	h.ensureCreatedEvent()
	itemID := stringValue(data["item_id"])
	if itemID == "" {
		itemID = "msg_proxy"
		data["item_id"] = itemID
	}
	if !h.textItemStarted && itemID == "msg_proxy" {
		item := map[string]any{
			"id":     "msg_proxy",
			"type":   "message",
			"status": "in_progress",
			"role":   "assistant",
			"content": []any{
				map[string]any{"type": "output_text", "text": ""},
			},
		}
		h.addEvent("response.output_item.added", h.outputItemEventData(item, map[string]any{"item": item}))
		h.textItemStarted = true
	}
	contentIndex, ok := intValue(data["content_index"])
	if !ok {
		contentIndex = 0
		data["content_index"] = contentIndex
	}
	key := responseTextTailKey{itemID: itemID, contentIndex: contentIndex}
	if delta := stringValue(data["delta"]); delta != "" {
		if h.textTailBuffers == nil {
			h.textTailBuffers = map[responseTextTailKey]*visibleTextTailBuffer{}
		}
		buffer := h.textTailBuffers[key]
		if buffer == nil {
			buffer = &visibleTextTailBuffer{}
			h.textTailBuffers[key] = buffer
		}
		delta = buffer.Push(delta)
		data["delta"] = delta
		if h.textOutputs == nil {
			h.textOutputs = map[responseTextTailKey]*strings.Builder{}
		}
		output := h.textOutputs[key]
		if output == nil {
			output = &strings.Builder{}
			h.textOutputs[key] = output
		}
		output.WriteString(delta)
		if item := h.outputItems[itemID]; item != nil {
			setResponseOutputText(item, contentIndex, output.String())
		}
	}
	if _, exists := data["output_index"]; !exists {
		if outputIndex, ok := h.ensureOutputItemIndex(itemID); ok {
			data["output_index"] = outputIndex
		}
	}
	return data
}

func setResponseOutputText(item map[string]any, contentIndex int, text string) {
	content, _ := item["content"].([]any)
	for len(content) <= contentIndex {
		content = append(content, map[string]any{"type": "output_text", "text": ""})
	}
	part, _ := content[contentIndex].(map[string]any)
	if part == nil || stringValue(part["type"]) != "output_text" {
		part = map[string]any{"type": "output_text"}
		content[contentIndex] = part
	}
	part["text"] = text
	item["content"] = content
}

func (h *responseEventWriterHelper) flushTextTailBuffer(key responseTextTailKey) {
	buffer := h.textTailBuffers[key]
	if buffer == nil || buffer.pending == "" {
		return
	}
	delta := buffer.pending
	buffer.Discard()
	if output := h.textOutputs[key]; output != nil {
		output.WriteString(delta)
	}
	if item := h.outputItems[key.itemID]; item != nil {
		if output := h.textOutputs[key]; output != nil {
			setResponseOutputText(item, key.contentIndex, output.String())
		}
	}
	data := map[string]any{"item_id": key.itemID, "content_index": key.contentIndex, "delta": delta}
	if outputIndex, ok := h.outputIndexForItem(key.itemID); ok {
		data["output_index"] = outputIndex
	}
	h.addEvent("response.output_text.delta", data)
}

func (h *responseEventWriterHelper) completedTextTailKeys(excludeItemID string) []responseTextTailKey {
	keys := make([]responseTextTailKey, 0)
	for key, buffer := range h.textTailBuffers {
		if key.itemID == excludeItemID || buffer == nil || buffer.pending == "" || !h.completedTextItems[key.itemID] {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		leftOutputIndex, leftOK := h.outputIndexForItem(keys[left].itemID)
		rightOutputIndex, rightOK := h.outputIndexForItem(keys[right].itemID)
		if leftOK && rightOK && leftOutputIndex != rightOutputIndex {
			return leftOutputIndex < rightOutputIndex
		}
		if keys[left].itemID != keys[right].itemID {
			return keys[left].itemID < keys[right].itemID
		}
		return keys[left].contentIndex < keys[right].contentIndex
	})
	return keys
}

func (h *responseEventWriterHelper) flushCompletedTextTailBuffers(excludeItemID string) {
	for _, key := range h.completedTextTailKeys(excludeItemID) {
		h.flushTextTailBuffer(key)
	}
}

func (h *responseEventWriterHelper) flushCompletedTextTailBuffersExcept(exclude responseTextTailKey) {
	for _, key := range h.completedTextTailKeys("") {
		if key == exclude {
			continue
		}
		h.flushTextTailBuffer(key)
	}
}

func (h *responseEventWriterHelper) markTextPartCompleted(itemID string, contentIndex int) {
	if itemID == "" {
		return
	}
	if h.completedTextParts == nil {
		h.completedTextParts = map[responseTextTailKey]bool{}
	}
	h.completedTextParts[responseTextTailKey{itemID: itemID, contentIndex: contentIndex}] = true
}

func (h *responseEventWriterHelper) flushCompletedTextParts(exclude responseTextTailKey) {
	keys := make([]responseTextTailKey, 0)
	for key := range h.completedTextParts {
		if key == exclude {
			continue
		}
		if buffer := h.textTailBuffers[key]; buffer != nil && buffer.pending != "" {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(left, right int) bool {
		leftOutputIndex, leftOK := h.outputIndexForItem(keys[left].itemID)
		rightOutputIndex, rightOK := h.outputIndexForItem(keys[right].itemID)
		if leftOK && rightOK && leftOutputIndex != rightOutputIndex {
			return leftOutputIndex < rightOutputIndex
		}
		if keys[left].itemID != keys[right].itemID {
			return keys[left].itemID < keys[right].itemID
		}
		return keys[left].contentIndex < keys[right].contentIndex
	})
	for _, key := range keys {
		h.flushTextTailBuffer(key)
	}
}

func (h *responseEventWriterHelper) terminalVisibleTextTailKey() (responseTextTailKey, bool) {
	for outputIndex := len(h.outputItemOrder) - 1; outputIndex >= 0; outputIndex-- {
		itemID := h.outputItemOrder[outputIndex]
		item := h.outputItems[itemID]
		if stringValue(item["type"]) != "message" {
			continue
		}
		var selected responseTextTailKey
		found := false
		for key, buffer := range h.textTailBuffers {
			if h.canonicalToolItemID(key.itemID) != itemID || buffer == nil || buffer.pending == "" {
				continue
			}
			if !found || key.contentIndex > selected.contentIndex {
				selected, found = key, true
			}
		}
		return selected, found
	}
	return responseTextTailKey{}, false
}

func (h *responseEventWriterHelper) flushSuccessfulTextTailBuffers() {
	terminalKey, hasTerminalKey := h.terminalVisibleTextTailKey()
	if !hasTerminalKey {
		h.flushCompletedTextTailBuffers("")
		h.flushCompletedTextParts(responseTextTailKey{})
		return
	}
	h.flushCompletedTextTailBuffersExcept(terminalKey)
	h.flushCompletedTextParts(terminalKey)
	h.textTailBuffers[terminalKey].Discard()
}

func (h *responseEventWriterHelper) discardTextTailBuffers() {
	for key := range h.textTailBuffers {
		delete(h.textTailBuffers, key)
	}
}

func stripStreamingTextSnapshots(event string, data map[string]any) map[string]any {
	switch event {
	case "response.output_text.done":
		if _, ok := data["text"]; ok {
			data = cloneMap(data)
			delete(data, "text")
		}
	case "response.content_part.done":
		part, _ := data["part"].(map[string]any)
		if stringValue(part["type"]) == "output_text" {
			data = cloneMap(data)
			clonedPart := cloneMap(part)
			delete(clonedPart, "text")
			data["part"] = clonedPart
		}
	case "response.output_item.done":
		item, _ := data["item"].(map[string]any)
		if stringValue(item["type"]) == "message" {
			data = cloneMap(data)
			data["item"] = stripMessageOutputText(item)
		}
	}
	return data
}

func stripResponseOutputText(response map[string]any) map[string]any {
	cloned := cloneMap(response)
	items, ok := cloned["output"].([]any)
	if !ok {
		return cloned
	}
	out := make([]any, 0, len(items))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if stringValue(item["type"]) == "message" {
			out = append(out, stripMessageOutputText(item))
			continue
		}
		out = append(out, cloneJSONValueForResponse(raw))
	}
	cloned["output"] = out
	return cloned
}

func stripMessageOutputText(item map[string]any) map[string]any {
	cloned := cloneMap(item)
	content, ok := cloned["content"].([]any)
	if !ok {
		return cloned
	}
	out := make([]any, 0, len(content))
	for _, rawPart := range content {
		part, _ := rawPart.(map[string]any)
		if stringValue(part["type"]) == "output_text" {
			clonedPart := cloneMap(part)
			delete(clonedPart, "text")
			out = append(out, clonedPart)
			continue
		}
		out = append(out, cloneJSONValueForResponse(rawPart))
	}
	cloned["content"] = out
	return cloned
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
		toolItems:                  map[string]*responsesToolItemState{},
		toolIDAliases:              map[string]string{},
		outputItems:                map[string]map[string]any{},
		outputItemOrder:            []string{},
		textOutputs:                map[responseTextTailKey]*strings.Builder{},
		textTailBuffers:            map[responseTextTailKey]*visibleTextTailBuffer{},
		completedTextItems:         map[string]bool{},
		completedTextParts:         map[responseTextTailKey]bool{},
		reasoningSummaryParts:      map[string]map[int]*strings.Builder{},
		reasoningSummaryPartClosed: map[string]map[int]bool{},
		reasoningSummaryTextDone:   map[string]map[int]bool{},
		activeReasoningItems:       map[string]bool{},
		reasoningItemsClosed:       map[string]bool{},
		upstreamEndpointType:       upstreamEndpointType,
		requestID:                  requestID,
	}
}

func cloneResponsesStreamState(initialState *responsesStreamState, requestID, upstreamEndpointType string) *responsesStreamState {
	state := newResponsesStreamState(requestID, upstreamEndpointType)
	if initialState == nil {
		return state
	}
	state.createdSent = initialState.createdSent
	state.createdResponseID = initialState.createdResponseID
	state.textStarted = initialState.textStarted
	state.realReasoningSeen = initialState.realReasoningSeen
	state.planningSent = initialState.planningSent
	state.toolStatusSent = initialState.toolStatusSent
	state.syntheticReasoningStarted = initialState.syntheticReasoningStarted
	state.syntheticReasoningClosed = initialState.syntheticReasoningClosed
	state.realReasoningStarted = initialState.realReasoningStarted
	state.realReasoningClosed = initialState.realReasoningClosed
	state.realReasoningItemID = initialState.realReasoningItemID
	state.reasoningSummaryParts = initialState.reasoningSummaryParts
	state.reasoningSummaryPartClosed = initialState.reasoningSummaryPartClosed
	state.reasoningSummaryTextDone = initialState.reasoningSummaryTextDone
	state.activeReasoningItems = initialState.activeReasoningItems
	state.reasoningItemsClosed = initialState.reasoningItemsClosed
	state.formattedReasoning = initialState.formattedReasoning
	state.syntheticInjected = initialState.syntheticInjected
	state.toolItems = initialState.toolItems
	state.toolIDAliases = initialState.toolIDAliases
	state.toolOrder = initialState.toolOrder
	state.outputItems = initialState.outputItems
	state.outputItemOrder = append([]string(nil), initialState.outputItemOrder...)
	state.textItemStarted = initialState.textItemStarted
	state.textOutputs = initialState.textOutputs
	state.textTailBuffers = initialState.textTailBuffers
	state.completedTextItems = initialState.completedTextItems
	state.completedTextParts = initialState.completedTextParts
	state.terminalSeen = initialState.terminalSeen
	state.terminalFailure = initialState.terminalFailure
	if summary := initialState.syntheticSummary.String(); summary != "" {
		state.syntheticSummary.WriteString(summary)
	}
	if summary := initialState.realReasoningSummary.String(); summary != "" {
		state.realReasoningSummary.WriteString(summary)
	}
	if state.toolItems == nil {
		state.toolItems = map[string]*responsesToolItemState{}
	}
	if state.toolIDAliases == nil {
		state.toolIDAliases = map[string]string{}
	}
	if state.outputItems == nil {
		state.outputItems = map[string]map[string]any{}
	}
	if state.reasoningSummaryParts == nil {
		state.reasoningSummaryParts = map[string]map[int]*strings.Builder{}
	}
	if state.reasoningSummaryPartClosed == nil {
		state.reasoningSummaryPartClosed = map[string]map[int]bool{}
	}
	if state.reasoningSummaryTextDone == nil {
		state.reasoningSummaryTextDone = map[string]map[int]bool{}
	}
	if state.activeReasoningItems == nil {
		state.activeReasoningItems = map[string]bool{}
	}
	if state.reasoningItemsClosed == nil {
		state.reasoningItemsClosed = map[string]bool{}
	}
	if state.textTailBuffers == nil {
		state.textTailBuffers = map[responseTextTailKey]*visibleTextTailBuffer{}
	}
	if state.textOutputs == nil {
		state.textOutputs = map[responseTextTailKey]*strings.Builder{}
	}
	if state.completedTextItems == nil {
		state.completedTextItems = map[string]bool{}
	}
	if state.completedTextParts == nil {
		state.completedTextParts = map[responseTextTailKey]bool{}
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
	if !h.syntheticInjected || !h.syntheticReasoningStarted || h.syntheticReasoningClosed {
		return
	}
	if h.downstreamType != "responses" {
		h.syntheticReasoningClosed = true
		return
	}
	item := map[string]any{
		"id":      "rs_proxy",
		"type":    "reasoning",
		"summary": []any{},
	}
	h.addEvent("response.output_item.done", h.outputItemEventData(item, map[string]any{"item": item, aggregate.InternalReasoningSourceKey: aggregate.ReasoningSourceSynthetic}))
	h.syntheticReasoningClosed = true
}

func (h *responseEventWriterHelper) markRealReasoningSeen() {
	if h.realReasoningSeen {
		return
	}
	h.realReasoningSeen = true
	h.closeSyntheticReasoning()
}

func (h *responseEventWriterHelper) ensureRealReasoningLifecycleStarted() {
	if h.downstreamType != "responses" || h.realReasoningStarted {
		return
	}
	h.realReasoningItemID = "rs_chat_reasoning"
	h.ensureReasoningLifecycleStarted(h.realReasoningItemID, -1, nil)
	h.realReasoningStarted = true
}

func (h *responseEventWriterHelper) closeRealReasoningLifecycle() {
	if h.downstreamType != "responses" || h.realReasoningItemID == "" || h.realReasoningClosed {
		return
	}
	h.closeReasoningLifecycle(h.realReasoningItemID)
	h.realReasoningClosed = true
}

func (h *responseEventWriterHelper) closeAllReasoningLifecycles() {
	if h.downstreamType != "responses" {
		return
	}
	itemIDs := make([]string, 0, len(h.activeReasoningItems))
	for itemID := range h.activeReasoningItems {
		itemIDs = append(itemIDs, itemID)
	}
	sort.Slice(itemIDs, func(left, right int) bool {
		leftIndex, leftOK := h.outputIndexForItem(itemIDs[left])
		rightIndex, rightOK := h.outputIndexForItem(itemIDs[right])
		if leftOK && rightOK && leftIndex != rightIndex {
			return leftIndex < rightIndex
		}
		return itemIDs[left] < itemIDs[right]
	})
	for _, itemID := range itemIDs {
		h.closeReasoningLifecycle(itemID)
	}
	if h.realReasoningItemID != "" {
		h.realReasoningClosed = true
	}
}

func (h *responseEventWriterHelper) closeReasoningLifecycle(itemID string) {
	if h.downstreamType != "responses" || itemID == "" || !h.activeReasoningItems[itemID] {
		return
	}
	outputIndex, _ := h.outputIndexForItem(itemID)
	summary := h.reasoningSummaryPartsSnapshot(itemID)
	h.closeAllReasoningSummaryParts(itemID, outputIndex)
	if len(summary) == 0 && h.realReasoningSummary != nil && itemID == h.realReasoningItemID {
		if text := h.realReasoningSummary.String(); text != "" {
			summary = []any{map[string]any{"type": "summary_text", "text": text}}
		}
	}
	item := h.outputItems[h.canonicalToolItemID(itemID)]
	if item == nil {
		item = map[string]any{"id": itemID, "type": "reasoning", "summary": []any{}}
	} else {
		item = cloneJSONValueForResponse(item).(map[string]any)
	}
	if len(summary) > 0 {
		item["summary"] = summary
	}
	item["id"] = itemID
	item["type"] = "reasoning"
	h.addEvent("response.output_item.done", h.outputItemEventData(item, map[string]any{"item": item, aggregate.InternalReasoningSourceKey: aggregate.ReasoningSourceUpstream}))
	delete(h.activeReasoningItems, itemID)
	if h.reasoningItemsClosed == nil {
		h.reasoningItemsClosed = map[string]bool{}
	}
	h.reasoningItemsClosed[itemID] = true
}

func (h *responseEventWriterHelper) ensureReasoningLifecycleStarted(itemID string, upstreamOutputIndex int, sourceItem map[string]any) int {
	if h.downstreamType != "responses" || itemID == "" {
		return 0
	}
	if outputIndex, ok := h.outputIndexForItem(itemID); ok {
		if h.reasoningItemsClosed != nil {
			delete(h.reasoningItemsClosed, itemID)
		}
		if h.activeReasoningItems == nil {
			h.activeReasoningItems = map[string]bool{}
		}
		h.activeReasoningItems[itemID] = true
		return outputIndex
	}
	item := map[string]any{"id": itemID, "type": "reasoning", "summary": []any{}}
	for key, value := range sourceItem {
		if key == "summary" {
			continue
		}
		item[key] = cloneJSONValueForResponse(value)
	}
	item["id"] = itemID
	item["type"] = "reasoning"
	item["summary"] = []any{}
	data := map[string]any{"item": item, aggregate.InternalReasoningSourceKey: aggregate.ReasoningSourceUpstream}
	if upstreamOutputIndex >= 0 {
		data["output_index"] = upstreamOutputIndex
	}
	h.addEvent("response.output_item.added", h.outputItemEventData(item, data))
	if h.activeReasoningItems == nil {
		h.activeReasoningItems = map[string]bool{}
	}
	h.activeReasoningItems[itemID] = true
	outputIndex, _ := h.outputIndexForItem(itemID)
	return outputIndex
}

func (h *responseEventWriterHelper) ensureReasoningSummaryPartStarted(itemID string, outputIndex, summaryIndex int) {
	if h.downstreamType != "responses" || itemID == "" {
		return
	}
	if h.reasoningSummaryPartStarted(itemID, summaryIndex) {
		return
	}
	h.markReasoningSummaryPartStarted(itemID, summaryIndex)
	h.addEvent("response.reasoning_summary_part.added", map[string]any{
		"item_id":                            itemID,
		"output_index":                       outputIndex,
		"summary_index":                      summaryIndex,
		"part":                               map[string]any{"type": "summary_text", "text": ""},
		aggregate.InternalReasoningSourceKey: aggregate.ReasoningSourceUpstream,
	})
}

func (h *responseEventWriterHelper) reasoningSummaryPartStarted(itemID string, summaryIndex int) bool {
	return h.reasoningSummaryParts != nil && h.reasoningSummaryParts[itemID][summaryIndex] != nil
}

func (h *responseEventWriterHelper) markReasoningSummaryPartStarted(itemID string, summaryIndex int) {
	if h.reasoningSummaryParts == nil {
		h.reasoningSummaryParts = map[string]map[int]*strings.Builder{}
	}
	parts := h.reasoningSummaryParts[itemID]
	if parts == nil {
		parts = map[int]*strings.Builder{}
		h.reasoningSummaryParts[itemID] = parts
	}
	parts[summaryIndex] = &strings.Builder{}
	if closed := h.reasoningSummaryPartClosed[itemID]; closed != nil {
		delete(closed, summaryIndex)
	}
	if done := h.reasoningSummaryTextDone[itemID]; done != nil {
		delete(done, summaryIndex)
	}
}

func (h *responseEventWriterHelper) markReasoningSummaryPartClosed(itemID string, summaryIndex int) {
	if h.reasoningSummaryPartClosed == nil {
		h.reasoningSummaryPartClosed = map[string]map[int]bool{}
	}
	closed := h.reasoningSummaryPartClosed[itemID]
	if closed == nil {
		closed = map[int]bool{}
		h.reasoningSummaryPartClosed[itemID] = closed
	}
	closed[summaryIndex] = true
}

func (h *responseEventWriterHelper) closeReasoningSummaryPart(itemID string, outputIndex, summaryIndex int) {
	if h.downstreamType != "responses" || itemID == "" || !h.reasoningSummaryPartStarted(itemID, summaryIndex) {
		return
	}
	text := h.reasoningSummaryPartText(itemID, summaryIndex)
	if text == "" && h.realReasoningSummary != nil && itemID == h.realReasoningItemID {
		text = h.realReasoningSummary.String()
	}
	h.closeReasoningSummaryPartWithText(itemID, outputIndex, summaryIndex, text)
}

func (h *responseEventWriterHelper) closeAllReasoningSummaryParts(itemID string, outputIndex int) {
	parts := h.reasoningSummaryParts[itemID]
	if len(parts) == 0 {
		return
	}
	indices := make([]int, 0, len(parts))
	for summaryIndex := range parts {
		indices = append(indices, summaryIndex)
	}
	sort.Ints(indices)
	for _, summaryIndex := range indices {
		h.closeReasoningSummaryPart(itemID, outputIndex, summaryIndex)
	}
}

func (h *responseEventWriterHelper) reasoningSummaryPartsSnapshot(itemID string) []any {
	parts := h.reasoningSummaryParts[itemID]
	if len(parts) == 0 {
		return nil
	}
	indices := make([]int, 0, len(parts))
	for summaryIndex := range parts {
		indices = append(indices, summaryIndex)
	}
	sort.Ints(indices)
	summary := make([]any, 0, len(indices))
	for _, summaryIndex := range indices {
		text := h.reasoningSummaryPartText(itemID, summaryIndex)
		summary = append(summary, map[string]any{"type": "summary_text", "text": text})
	}
	return summary
}

func (h *responseEventWriterHelper) appendReasoningSummaryPartText(itemID string, summaryIndex int, text string) {
	if text == "" || !h.reasoningSummaryPartStarted(itemID, summaryIndex) {
		return
	}
	h.reasoningSummaryParts[itemID][summaryIndex].WriteString(text)
}

func (h *responseEventWriterHelper) setReasoningSummaryPartText(itemID string, summaryIndex int, text string) {
	if !h.reasoningSummaryPartStarted(itemID, summaryIndex) {
		return
	}
	builder := h.reasoningSummaryParts[itemID][summaryIndex]
	builder.Reset()
	builder.WriteString(text)
}

func (h *responseEventWriterHelper) reasoningSummaryPartText(itemID string, summaryIndex int) string {
	if !h.reasoningSummaryPartStarted(itemID, summaryIndex) {
		return ""
	}
	return h.reasoningSummaryParts[itemID][summaryIndex].String()
}

func (h *responseEventWriterHelper) closeReasoningSummaryPartWithText(itemID string, outputIndex, summaryIndex int, text string) {
	if h.downstreamType != "responses" || itemID == "" || !h.reasoningSummaryPartStarted(itemID, summaryIndex) {
		return
	}
	if h.reasoningSummaryPartClosedFor(itemID, summaryIndex) {
		return
	}
	if !h.reasoningSummaryTextDoneFor(itemID, summaryIndex) {
		h.addEvent("response.reasoning_summary_text.done", map[string]any{
			"item_id":                            itemID,
			"output_index":                       outputIndex,
			"summary_index":                      summaryIndex,
			"text":                               text,
			aggregate.InternalReasoningSourceKey: aggregate.ReasoningSourceUpstream,
		})
		h.markReasoningSummaryTextDone(itemID, summaryIndex)
	}
	if !h.reasoningSummaryPartClosedFor(itemID, summaryIndex) {
		h.addEvent("response.reasoning_summary_part.done", map[string]any{
			"item_id":                            itemID,
			"output_index":                       outputIndex,
			"summary_index":                      summaryIndex,
			"part":                               map[string]any{"type": "summary_text", "text": text},
			aggregate.InternalReasoningSourceKey: aggregate.ReasoningSourceUpstream,
		})
	}
	h.markReasoningSummaryPartClosed(itemID, summaryIndex)
}

func (h *responseEventWriterHelper) reasoningSummaryPartClosedFor(itemID string, summaryIndex int) bool {
	return h.reasoningSummaryPartClosed != nil && h.reasoningSummaryPartClosed[itemID][summaryIndex]
}

func (h *responseEventWriterHelper) reasoningSummaryTextDoneFor(itemID string, summaryIndex int) bool {
	return h.reasoningSummaryTextDone != nil && h.reasoningSummaryTextDone[itemID][summaryIndex]
}

func (h *responseEventWriterHelper) markReasoningSummaryTextDone(itemID string, summaryIndex int) {
	if h.reasoningSummaryTextDone == nil {
		h.reasoningSummaryTextDone = map[string]map[int]bool{}
	}
	done := h.reasoningSummaryTextDone[itemID]
	if done == nil {
		done = map[int]bool{}
		h.reasoningSummaryTextDone[itemID] = done
	}
	done[summaryIndex] = true
}

func (h *responseEventWriterHelper) mergeStoredReasoningSummary(itemID string, item map[string]any) {
	if reasoningSummaryFromItem(item) != "" {
		return
	}
	if summary := h.reasoningSummaryPartsSnapshot(itemID); len(summary) > 0 {
		item["summary"] = summary
	}
}

func (h *responseEventWriterHelper) beginCompactionLifecycle() {
	if h.compactionLifecycleStarted {
		return
	}
	h.compactionLifecycleStarted = true
	h.closeSyntheticReasoning()
}

func doProcessResponseEvent(h *responseEventWriterHelper, evt upstream.Event) (processResponseEventResult, error) {
	result := processResponseEventResult{}
	if h.terminalSeen {
		result.skipWrite = true
		return result, nil
	}
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
			if _, ok := response["output"]; !ok {
				response["output"] = []any{}
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
		itemType, _ := item["type"].(string)
		if itemType == "reasoning" {
			h.associateReasoningFormatOutputItem(item)
			if h.downstreamType == "responses" {
				item = reasoningtext.FormatBlock(item)
				evt.Data["item"] = item
			}
			itemID := responseToolItemStateID(item)
			upstreamOutputIndex, hasUpstreamOutputIndex := intValue(evt.Data["output_index"])
			if !hasUpstreamOutputIndex {
				upstreamOutputIndex = -1
			}
			if h.downstreamType == "responses" && itemID != "" && evt.Event == "response.output_item.added" {
				evt.Data = h.outputItemEventData(item, evt.Data)
				if h.activeReasoningItems == nil {
					h.activeReasoningItems = map[string]bool{}
				}
				h.activeReasoningItems[itemID] = true
			}
			if h.downstreamType == "responses" && itemID != "" && evt.Event == "response.output_item.done" {
				h.ensureReasoningItemStates()
				if h.reasoningItemsClosed[itemID] {
					result.skipWrite = true
					return result, nil
				}
				if !h.activeReasoningItems[itemID] {
					h.activeReasoningItems[itemID] = true
				}
				_, hadReasoningItem := h.outputIndexForItem(itemID)
				outputIndex := h.ensureReasoningLifecycleStarted(itemID, upstreamOutputIndex, item)
				if summary := reasoningSummaryFromItem(item); summary != "" && (!hadReasoningItem || h.reasoningSummaryPartStarted(itemID, 0)) {
					h.ensureReasoningSummaryPartStarted(itemID, outputIndex, 0)
					h.closeReasoningSummaryPartWithText(itemID, outputIndex, 0, summary)
				}
				h.mergeStoredReasoningSummary(itemID, item)
				if outputIndex, ok := h.outputIndexForItem(itemID); ok {
					evt.Data["output_index"] = outputIndex
				}
				h.closeAllReasoningSummaryParts(itemID, outputIndex)
				delete(h.activeReasoningItems, itemID)
				h.reasoningItemsClosed[itemID] = true
				if itemID == h.realReasoningItemID {
					h.realReasoningClosed = true
				}
			}
			if h.downstreamType == "responses" {
				evt.Data = h.outputItemEventData(item, evt.Data)
			}
			if h.downstreamType == "responses" {
				if _, ok := evt.Data[aggregate.InternalReasoningSourceKey]; !ok {
					evt.Data[aggregate.InternalReasoningSourceKey] = aggregate.ReasoningSourceUpstream
				}
			}
			h.markRealReasoningSeen()
			if evt.Event == "response.output_item.done" && h.downstreamType != "responses" {
				h.retireReasoningFormatAliases(item)
				h.resetFormattedReasoning()
			}
			break
		}
		if itemType == "compaction" && h.downstreamType == "responses" {
			h.beginCompactionLifecycle()
			evt.Data = h.outputItemEventData(item, evt.Data)
		}
		if itemType == "message" && h.downstreamType == "responses" {
			if evt.Event == "response.output_item.done" {
				itemID := responseToolItemStateID(item)
				if itemID != "" {
					if h.completedTextItems == nil {
						h.completedTextItems = map[string]bool{}
					}
					h.completedTextItems[itemID] = true
					h.flushCompletedTextTailBuffers(itemID)
				}
			}
			evt.Data = h.outputItemEventData(item, evt.Data)
		}
		if isResponseToolCallItemType(itemType) {
			h.resetFormattedReasoning()
			if evt.Event == "response.output_item.added" {
				if _, ok := item["arguments"]; !ok {
					item["arguments"] = ""
				}
				if _, ok := item["status"]; !ok {
					item["status"] = "in_progress"
				}
			}
			if evt.Event == "response.output_item.done" {
				if _, ok := item["status"]; !ok {
					item["status"] = "completed"
				}
			}
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
				if h.downstreamType == "responses" {
					evt.Data = h.outputItemEventData(item, evt.Data)
				}
			}
		}
	case "response.output_text.delta":
		if compatCompleteToolArgs {
			h.flushPendingFunctionCalls()
		}
		h.closeRealReasoningLifecycle()
		h.resetFormattedReasoning()
		h.closeSyntheticReasoning()
		evt.Data = h.outputTextDeltaData(evt.Data)
		if stringValue(evt.Data["delta"]) == "" {
			result.skipWrite = true
		}
	case "response.output_text.done", "response.content_part.done":
		if h.downstreamType == "responses" {
			itemID := stringValue(evt.Data["item_id"])
			contentIndex, ok := intValue(evt.Data["content_index"])
			if !ok {
				contentIndex = 0
			}
			if evt.Event == "response.content_part.done" {
				part, _ := evt.Data["part"].(map[string]any)
				if stringValue(part["type"]) != "output_text" {
					break
				}
			}
			h.markTextPartCompleted(itemID, contentIndex)
			h.flushCompletedTextParts(responseTextTailKey{itemID: itemID, contentIndex: contentIndex})
		}
	case "response.reasoning.delta", "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done", "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		if compatCompleteToolArgs {
			h.flushPendingFunctionCalls()
		}
		if h.downstreamType == "responses" {
			if _, ok := evt.Data[aggregate.InternalReasoningSourceKey]; !ok {
				evt.Data[aggregate.InternalReasoningSourceKey] = aggregate.ReasoningSourceUpstream
			}
		}
		h.markRealReasoningSeen()
		h.formatReasoningEvent(&evt)
		if evt.Event == "response.reasoning.delta" {
			summary := reasoningContentValue(evt.Data)
			if summary != "" {
				h.ensureRealReasoningLifecycleStarted()
				if h.realReasoningSummary != nil && h.realReasoningItemID != "" {
					h.realReasoningSummary.WriteString(summary)
				}
				outputIndex, _ := h.outputIndexForItem(h.realReasoningItemID)
				h.ensureReasoningSummaryPartStarted(h.realReasoningItemID, outputIndex, 0)
				h.appendReasoningSummaryPartText(h.realReasoningItemID, 0, summary)
				converted := map[string]any{
					"item_id":                            h.realReasoningItemID,
					"output_index":                       outputIndex,
					"summary_index":                      0,
					"delta":                              summary,
					aggregate.InternalReasoningSourceKey: aggregate.ReasoningSourceUpstream,
				}
				if h.downstreamType != "responses" {
					converted[internalReasoningFormatItemIDKey] = h.beginReasoningFormatPhase()
				}
				if blocks, ok := evt.Data["blocks"]; ok {
					converted["blocks"] = blocks
				}
				h.addEvent("response.reasoning_summary_text.delta", converted)
			}
			result.skipWrite = true
		} else if h.downstreamType == "responses" {
			itemID := stringValue(evt.Data["item_id"])
			if itemID != "" {
				upstreamOutputIndex, ok := intValue(evt.Data["output_index"])
				if !ok {
					upstreamOutputIndex = -1
				}
				outputIndex := h.ensureReasoningLifecycleStarted(itemID, upstreamOutputIndex, nil)
				summaryIndex, ok := intValue(evt.Data["summary_index"])
				if !ok {
					summaryIndex = 0
				}
				switch evt.Event {
				case "response.reasoning_summary_part.added":
					h.markReasoningSummaryPartStarted(itemID, summaryIndex)
				case "response.reasoning_summary_text.done":
					if h.reasoningSummaryTextDoneFor(itemID, summaryIndex) {
						result.skipWrite = true
						return result, nil
					}
					h.ensureReasoningSummaryPartStarted(itemID, outputIndex, summaryIndex)
					if text := reasoningContentValue(evt.Data); text != "" {
						h.setReasoningSummaryPartText(itemID, summaryIndex, text)
					}
					if text := h.reasoningSummaryPartText(itemID, summaryIndex); text != "" {
						evt.Data["text"] = text
					}
					h.markReasoningSummaryTextDone(itemID, summaryIndex)
				case "response.reasoning_summary_part.done":
					if h.reasoningSummaryPartClosedFor(itemID, summaryIndex) {
						result.skipWrite = true
						return result, nil
					}
					h.ensureReasoningSummaryPartStarted(itemID, outputIndex, summaryIndex)
					if part, _ := evt.Data["part"].(map[string]any); part != nil {
						if text := reasoningContentValue(part); text != "" {
							h.setReasoningSummaryPartText(itemID, summaryIndex, text)
						}
					}
					text := h.reasoningSummaryPartText(itemID, summaryIndex)
					if !h.reasoningSummaryTextDoneFor(itemID, summaryIndex) {
						h.addEvent("response.reasoning_summary_text.done", map[string]any{
							"item_id":                            itemID,
							"output_index":                       outputIndex,
							"summary_index":                      summaryIndex,
							"text":                               text,
							aggregate.InternalReasoningSourceKey: aggregate.ReasoningSourceUpstream,
						})
					}
					evt.Data["part"] = map[string]any{"type": "summary_text", "text": text}
					h.markReasoningSummaryTextDone(itemID, summaryIndex)
					h.markReasoningSummaryPartClosed(itemID, summaryIndex)
				default:
					h.ensureReasoningSummaryPartStarted(itemID, outputIndex, summaryIndex)
					if evt.Event == "response.reasoning_summary_text.delta" {
						h.appendReasoningSummaryPartText(itemID, summaryIndex, reasoningContentValue(evt.Data))
					}
				}
				evt.Data["item_id"] = itemID
				evt.Data["output_index"] = outputIndex
				evt.Data["summary_index"] = summaryIndex
			}
		} else if evt.Event == "response.reasoning_summary_text.delta" || evt.Event == "response.reasoning_summary_text.done" {
			h.associateReasoningFormatItem(evt.Data, evt.Event == "response.reasoning_summary_text.delta")
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
		evt.Data = h.toolEventData(itemID, evt.Data)
	case "response.completed":
		h.terminalSeen = true
		h.flushSuccessfulTextTailBuffers()
		if compatCompleteToolArgs {
			h.flushPendingFunctionCalls()
		}
		h.closeAllReasoningLifecycles()
		h.closeSyntheticReasoning()
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
		normalizeResponsesCompletedFinishReason(response)
		response["output"] = h.completedResponseOutput(response["output"])
		if model := stringValue(response["model"]); model != "" {
			h.modelName = model
		} else if h.modelName != "" {
			response["model"] = h.modelName
		}
		if serviceTier, _ := evt.Data["service_tier"].(string); serviceTier != "" {
			if _, ok := response["service_tier"]; !ok {
				response["service_tier"] = serviceTier
			}
			delete(evt.Data, "service_tier")
		}
		if usage, _ := evt.Data["usage"].(map[string]any); len(usage) > 0 {
			if _, ok := response["usage"]; !ok {
				response["usage"] = cloneMap(usage)
			}
		}
	case "response.done":
		h.terminalSeen = true
		h.flushSuccessfulTextTailBuffers()
		h.closeAllReasoningLifecycles()
		h.closeSyntheticReasoning()
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
		normalizeResponsesCompletedFinishReason(response)
		response["output"] = h.completedResponseOutput(response["output"])
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
	case "error", "response.failed", "response.incomplete":
		h.terminalSeen = true
		h.flushCompletedTextTailBuffers("")
		h.discardTextTailBuffers()
		if compatCompleteToolArgs {
			h.flushPendingFunctionCalls()
		}
		terminalFailure := terminalFailureFromEventData(evt.Data)
		h.terminalFailure = terminalFailure
		if contextoverflow.IsSignal(terminalFailure.HealthFlag, terminalFailure.Message) {
			evt.Event = "response.failed"
			evt.Data = responsesTerminalFailureData(evt.Event, h.currentResponseID(), terminalFailure.HealthFlag, terminalFailure.Message, upstreamErrorObjectFromEventData(evt.Data))
		} else if evt.Event == "response.incomplete" {
			evt.Event = responsesTerminalFailureEvent(terminalFailure.HealthFlag)
			if evt.Event == "response.failed" {
				evt.Data = responsesTerminalFailureData(evt.Event, h.currentResponseID(), terminalFailure.HealthFlag, terminalFailure.Message, upstreamErrorObjectFromEventData(evt.Data))
			} else {
				evt.Data["health_flag"] = terminalFailure.HealthFlag
				evt.Data["message"] = terminalFailure.Message
			}
		} else {
			evt.Data["health_flag"] = terminalFailure.HealthFlag
			evt.Data["message"] = terminalFailure.Message
		}
		h.closeAllReasoningLifecycles()
		h.closeSyntheticReasoning()
		if h.downstreamType == "responses" {
			result.writeNow = &processEventCommand{Event: evt.Event, Data: evt.Data}
		}
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
		if result.writeNow != nil {
			evt = upstream.Event{Event: result.writeNow.Event, Data: result.writeNow.Data}
		}

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
		if result.writeNow != nil {
			evt = upstream.Event{Event: result.writeNow.Event, Data: result.writeNow.Data}
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
	event := responsesTerminalFailureEvent(healthFlag)
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	payload, err := responseStreamPayload(event, responsesTerminalFailureData(event, requestID, healthFlag, message, nil))
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

func responsesTerminalFailureEvent(healthFlag string) string {
	if contextoverflow.IsSignal(healthFlag, "") {
		return "response.failed"
	}
	switch healthFlag {
	case "upstream_max_tokens", "upstream_length":
		return "response.incomplete"
	default:
		return "response.failed"
	}
}

func responsesTerminalFailureData(event string, requestID string, healthFlag string, message string, upstreamError map[string]any) map[string]any {
	healthFlag, message, upstreamError = normalizeContextOverflowTerminalFailure(healthFlag, message, upstreamError)
	data := map[string]any{
		"type":        event,
		"request_id":  requestID,
		"health_flag": healthFlag,
		"message":     message,
	}
	if event != "response.failed" {
		return data
	}
	errObj := cloneMap(upstreamError)
	if len(errObj) == 0 {
		errObj = map[string]any{
			"type":    "proxy_error",
			"code":    healthFlag,
			"message": message,
		}
	} else {
		if _, ok := errObj["code"]; !ok && healthFlag != "" {
			errObj["code"] = healthFlag
		}
		if _, ok := errObj["message"]; !ok && message != "" {
			errObj["message"] = message
		}
	}
	data["response"] = map[string]any{
		"id":     requestID,
		"status": "failed",
		"error":  errObj,
	}
	return data
}

func shouldInjectSyntheticResponsesReasoning(upstreamEndpointType, thinkingTagStyle string) bool {
	if normalizeHTTPAPIUpstreamEndpointType(upstreamEndpointType) != config.UpstreamEndpointTypeChat {
		return true
	}
	return thinkingTagStyle == config.UpstreamThinkingTagStyleLegacy
}

func shouldInjectSyntheticResponsesReasoningBeforeText(upstreamEndpointType string, state *responsesStreamState, evt upstream.Event) bool {
	if normalizeHTTPAPIUpstreamEndpointType(upstreamEndpointType) != config.UpstreamEndpointTypeChat {
		return false
	}
	if state == nil || state.syntheticInjected || state.realReasoningSeen || state.textStarted {
		return false
	}
	delta := stringValue(evt.Data["delta"])
	if containsRawThinkingTag(delta) {
		return false
	}
	return strings.TrimSpace(delta) != ""
}

func containsRawThinkingTag(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "<think") || strings.Contains(lower, "</think>") || strings.Contains(lower, "<thinking") || strings.Contains(lower, "</thinking>") || strings.Contains(lower, "<reasoning") || strings.Contains(lower, "</reasoning>")
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
		func() error { return writeSSEHeartbeat(w, flusher, state.terminalSeen) },
		func(evt upstream.Event) error {
			collector.Accept(evt)
			if evt.Event == "response.output_text.delta" && shouldInjectSyntheticResponsesReasoningBeforeText(upstreamEndpointType, state, evt) {
				if err := writeSyntheticResponsesReasoningWithState(w, flusher, state, syntheticReasoningPrelude()); err != nil {
					return err
				}
			}
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
		createdSent:                state.createdSent,
		createdResponseID:          state.createdResponseID,
		modelName:                  state.modelName,
		toolIDAliases:              state.toolIDAliases,
		toolItems:                  state.toolItems,
		toolOrder:                  state.toolOrder,
		outputItems:                state.outputItems,
		outputItemOrder:            state.outputItemOrder,
		textItemStarted:            state.textItemStarted,
		textOutputs:                state.textOutputs,
		textTailBuffers:            state.textTailBuffers,
		completedTextItems:         state.completedTextItems,
		completedTextParts:         state.completedTextParts,
		syntheticReasoningStarted:  state.syntheticReasoningStarted,
		syntheticReasoningClosed:   state.syntheticReasoningClosed,
		realReasoningStarted:       state.realReasoningStarted,
		realReasoningClosed:        state.realReasoningClosed,
		realReasoningItemID:        state.realReasoningItemID,
		realReasoningSummary:       &state.realReasoningSummary,
		reasoningSummaryParts:      state.reasoningSummaryParts,
		reasoningSummaryPartClosed: state.reasoningSummaryPartClosed,
		reasoningSummaryTextDone:   state.reasoningSummaryTextDone,
		activeReasoningItems:       state.activeReasoningItems,
		reasoningItemsClosed:       state.reasoningItemsClosed,
		formattedReasoning:         state.formattedReasoning,
		syntheticInjected:          state.syntheticInjected,
		realReasoningSeen:          state.realReasoningSeen,
		syntheticSummary:           &state.syntheticSummary,
		requestID:                  state.requestID,
		upstreamEndpointType:       state.upstreamEndpointType,
		terminalSeen:               state.terminalSeen,
		terminalFailure:            state.terminalFailure,
	})

	result, err := doProcessResponseEvent(h, evt)
	if err != nil {
		return err
	}
	if result.writeNow != nil {
		evt = upstream.Event{Event: result.writeNow.Event, Data: result.writeNow.Data}
	}

	state.toolIDAliases = h.toolIDAliases
	state.createdSent = h.createdSent
	state.createdResponseID = h.createdResponseID
	state.modelName = h.modelName
	state.toolItems = h.toolItems
	state.toolOrder = h.toolOrder
	state.outputItems = h.outputItems
	state.outputItemOrder = h.outputItemOrder
	state.textItemStarted = h.textItemStarted
	state.textOutputs = h.textOutputs
	state.textTailBuffers = h.textTailBuffers
	state.completedTextItems = h.completedTextItems
	state.completedTextParts = h.completedTextParts
	state.syntheticReasoningStarted = h.syntheticReasoningStarted
	state.syntheticReasoningClosed = h.syntheticReasoningClosed
	state.realReasoningStarted = h.realReasoningStarted
	state.realReasoningClosed = h.realReasoningClosed
	state.realReasoningItemID = h.realReasoningItemID
	state.reasoningSummaryParts = h.reasoningSummaryParts
	state.reasoningSummaryPartClosed = h.reasoningSummaryPartClosed
	state.reasoningSummaryTextDone = h.reasoningSummaryTextDone
	state.activeReasoningItems = h.activeReasoningItems
	state.reasoningItemsClosed = h.reasoningItemsClosed
	state.formattedReasoning = h.formattedReasoning
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

	if writer.DownstreamType() == "responses" {
		evt.Data = stripStreamingTextSnapshots(evt.Event, evt.Data)
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
		if itemType, _ := item["type"].(string); isResponseToolCallItemType(itemType) {
			arguments, _ := item["arguments"].(string)
			logging.DownstreamToolEvent(logging.DownstreamToolEventAttrs{
				RequestID:          requestID,
				DownstreamType:     downstreamType,
				Event:              event,
				ItemID:             stringValue(item["id"]),
				CallID:             stringValue(item["call_id"]),
				ToolName:           stringValue(item["name"]),
				ArgumentsLen:       len(arguments),
				ArgumentsPreview:   truncateForLog(arguments, 120),
				IncludeCallDetails: true,
			})
		}
		return
	}
	if event == "response.function_call_arguments.done" || event == "response.function_call_arguments.delta" {
		arguments := stringValue(data["arguments"])
		if arguments == "" {
			arguments = stringValue(data["delta"])
		}
		logging.DownstreamToolEvent(logging.DownstreamToolEventAttrs{
			RequestID:        requestID,
			DownstreamType:   downstreamType,
			Event:            event,
			ItemID:           stringValue(data["item_id"]),
			ArgumentsLen:     len(arguments),
			ArgumentsPreview: truncateForLog(arguments, 120),
		})
	}
}

func withParsedToolParameters(item map[string]any) map[string]any {
	if item == nil {
		return item
	}
	if itemType, _ := item["type"].(string); !isResponseToolCallItemType(itemType) {
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
	helper := &responseEventWriterHelper{
		downstreamType:             downstreamType,
		upstreamEndpointType:       state.upstreamEndpointType,
		createdSent:                state.createdSent,
		createdResponseID:          state.createdResponseID,
		modelName:                  state.modelName,
		toolIDAliases:              state.toolIDAliases,
		toolItems:                  state.toolItems,
		toolOrder:                  state.toolOrder,
		outputItems:                state.outputItems,
		outputItemOrder:            state.outputItemOrder,
		textItemStarted:            state.textItemStarted,
		textOutputs:                state.textOutputs,
		textTailBuffers:            state.textTailBuffers,
		completedTextItems:         state.completedTextItems,
		completedTextParts:         state.completedTextParts,
		syntheticReasoningStarted:  state.syntheticReasoningStarted,
		syntheticReasoningClosed:   state.syntheticReasoningClosed,
		realReasoningStarted:       state.realReasoningStarted,
		realReasoningClosed:        state.realReasoningClosed,
		realReasoningItemID:        state.realReasoningItemID,
		realReasoningSummary:       state.realReasoningSummary,
		reasoningSummaryParts:      state.reasoningSummaryParts,
		reasoningSummaryPartClosed: state.reasoningSummaryPartClosed,
		reasoningSummaryTextDone:   state.reasoningSummaryTextDone,
		activeReasoningItems:       state.activeReasoningItems,
		reasoningItemsClosed:       state.reasoningItemsClosed,
		formattedReasoning:         state.formattedReasoning,
		syntheticInjected:          state.syntheticInjected,
		realReasoningSeen:          state.realReasoningSeen,
		syntheticSummary:           state.syntheticSummary,
		requestID:                  state.requestID,
		terminalSeen:               state.terminalSeen,
		terminalFailure:            state.terminalFailure,
	}
	if downstreamType == "responses" {
		helper.ensureReasoningItemStates()
	}
	return helper
}

func writeSyntheticResponsesReasoning(w http.ResponseWriter, flusher http.Flusher, text string) error {
	return writeSyntheticResponsesReasoningWithState(w, flusher, nil, text)
}

func writeSyntheticResponsesReasoningWithState(w http.ResponseWriter, flusher http.Flusher, state *responsesStreamState, text string) error {
	if state != nil && !state.syntheticReasoningStarted {
		item := map[string]any{
			"id":      "rs_proxy",
			"type":    "reasoning",
			"summary": []any{},
		}
		helper := newResponseEventWriterHelper("responses", responseProjectionState{createdSent: state.createdSent, createdResponseID: state.createdResponseID, modelName: state.modelName, requestID: state.requestID, outputItems: state.outputItems, outputItemOrder: state.outputItemOrder})
		payloadData := helper.outputItemEventData(item, map[string]any{
			"item":                               item,
			aggregate.InternalReasoningSourceKey: aggregate.ReasoningSourceSynthetic,
		})
		helper.addEvent("response.output_item.added", payloadData)
		state.createdSent = helper.createdSent
		state.createdResponseID = helper.createdResponseID
		state.outputItems = helper.outputItems
		state.outputItemOrder = helper.outputItemOrder
		for _, cmd := range helper.events {
			payload, err := responseStreamPayload(cmd.Event, cmd.Data)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "event: %s\n", cmd.Event); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return err
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		state.syntheticReasoningStarted = true
		state.syntheticInjected = true
	}
	text = sanitizeSyntheticReasoningText(text)
	if !isInvisibleSyntheticReasoningText(text) && !strings.HasSuffix(text, "\n\n") {
		if strings.HasSuffix(text, "\n") {
			text += "\n"
		} else {
			text += "\n\n"
		}
	}
	if state != nil && !isInvisibleSyntheticReasoningText(text) {
		state.syntheticSummary.WriteString(text)
	}
	if isInvisibleSyntheticReasoningText(text) {
		return nil
	}
	payload := map[string]any{"type": "response.reasoning.delta", "summary": text, aggregate.InternalReasoningSourceKey: aggregate.ReasoningSourceSynthetic}
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
	summaryPayload := map[string]any{"type": "response.reasoning_summary_text.delta", "delta": text, aggregate.InternalReasoningSourceKey: aggregate.ReasoningSourceSynthetic}
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
	return nil
}

func responseStreamPayload(event string, data map[string]any) ([]byte, error) {
	if len(data) == 0 {
		return json.Marshal(map[string]any{"type": event})
	}
	clone := make(map[string]any, len(data)+1)
	for k, v := range data {
		if k == aggregate.InternalReasoningSourceKey {
			continue
		}
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
	if state.emittedToolItems == nil {
		state.emittedToolItems = map[string]bool{}
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
			return startAnthropicUnreasonedPlaceholder(w, flusher, state)
		},
		func() error { return writeSSEHeartbeat(w, flusher, state.terminalSeen) },
		func(evt upstream.Event) error {
			return writer.WriteEvent(evt.Event, evt.Data)
		},
	)
	if err != nil && !state.terminalSeen {
		return err
	}
	if !state.terminalSeen {
		return io.ErrUnexpectedEOF
	}
	if state.terminalFailure != nil {
		return nil
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
	state.thinkingType = "thinking"
	state.nextIndex++
	if err := writeAnthropicSSEEvent(w, flusher, "content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": state.thinkingIndex,
		"content_block": map[string]any{
			"type":     state.thinkingType,
			"thinking": "",
		},
	}); err != nil {
		return err
	}
	return nil
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
	writeTextDelta := func(delta string) error {
		return writeAnthropicSSEEvent(w, flusher, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": state.textIndex,
			"delta": map[string]any{"type": "text_delta", "text": delta},
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
		if state.thinkingType == "" {
			state.thinkingType = "thinking"
		}
		state.thinkingIndex = state.nextIndex
		state.nextIndex++
		return writeAnthropicSSEEvent(w, flusher, "content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": state.thinkingIndex,
			"content_block": map[string]any{
				"type":     state.thinkingType,
				"thinking": "",
			},
		})
	}
	closeThinkingBlock = func() error {
		if !state.thinkingStarted || state.thinkingStopped {
			return nil
		}
		if !state.signatureSent {
			signature := state.thinkingSignature
			if signature == "" {
				signature = "proxy_signature"
			}
			if err := writeAnthropicSSEEvent(w, flusher, "content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": state.thinkingIndex,
				"delta": map[string]any{"type": "signature_delta", "signature": signature},
			}); err != nil {
				return err
			}
			state.signatureSent = true
		}
		if err := writeAnthropicSSEEvent(w, flusher, "content_block_stop", map[string]any{"type": "content_block_stop", "index": state.thinkingIndex}); err != nil {
			return err
		}
		state.thinkingStopped = true
		state.reasoningText.Reset()
		state.thinkingType = ""
		state.thinkingSignature = ""
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
		if itemID != "" {
			state.emittedToolItems[itemID] = true
		}
		if rawItemID != "" {
			state.emittedToolItems[rawItemID] = true
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
		if itemType, _ := item["type"].(string); isResponseToolCallItemType(itemType) {
			itemID := anthropicToolStateKey(item)
			if evt.Event == "response.output_item.done" && itemID != "" && state.emittedToolItems[itemID] {
				if !(state.toolStarted && !state.toolStopped && state.toolItemID == itemID) {
					return nil
				}
			}
			return startToolBlock(item)
		}
		if itemType, _ := item["type"].(string); itemType == "reasoning" {
			for _, summary := range formatStreamingReasoningItemSummary(&state.reasoningSummaries, item) {
				state.realThinkingSeen = true
				if err := startThinkingBlock(); err != nil {
					return err
				}
				if err := writeAnthropicSSEEvent(w, flusher, "content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": state.thinkingIndex,
					"delta": map[string]any{"type": "thinking_delta", "thinking": summary},
				}); err != nil {
					return err
				}
			}
			return nil
		}
	case "response.output_text.delta":
		delta := stringValue(evt.Data["delta"])
		if delta == "" {
			return nil
		}
		delta = state.textTail.Push(delta)
		if delta == "" {
			return nil
		}
		if err := startTextBlock(); err != nil {
			return err
		}
		state.textStarted = true
		state.textStopped = false
		return writeTextDelta(delta)
	case "response.reasoning.delta":
		if block := firstReasoningBlock(evt.Data); len(block) > 0 {
			if blockType, _ := block["type"].(string); blockType != "" {
				state.thinkingType = blockType
			}
			if signature, _ := block["signature"].(string); signature != "" {
				state.thinkingSignature = signature
			}
		}
		delta := formatStreamingReasoningDelta(&state.reasoningText, reasoningContentRawValue(evt.Data))
		if delta != "" {
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
	case "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		if block := firstReasoningBlock(evt.Data); len(block) > 0 {
			if blockType, _ := block["type"].(string); blockType != "" {
				state.thinkingType = blockType
			}
			if signature, _ := block["signature"].(string); signature != "" {
				state.thinkingSignature = signature
			}
		}
		itemID := reasoningFormatItemID(evt.Data)
		summaryIndex, ok := intValue(evt.Data["summary_index"])
		if !ok {
			summaryIndex = 0
		}
		summaryIndex = reasoningFormatSummaryIndex(evt.Data, summaryIndex)
		delta := formatStreamingReasoningSummary(&state.reasoningSummaries, itemID, summaryIndex, reasoningContentRawValue(evt.Data), evt.Event == "response.reasoning_summary_text.done")
		if delta != "" {
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
		state.textTail.Discard()
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
			usage = map[string]any{
				"input_tokens":  0,
				"output_tokens": 0,
			}
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
	case "error", "response.failed", "response.incomplete":
		state.terminalSeen = true
		state.textTail.Discard()
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
		return writeAnthropicTerminalFailure(w, flusher, state, stringValue(evt.Data["request_id"]), terminalFailure.HealthFlag, terminalFailure.Message, upstreamErrorObjectFromEventData(evt.Data))
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

func writeAnthropicTerminalFailure(w http.ResponseWriter, flusher http.Flusher, state *anthropicStreamState, requestID string, healthFlag string, message string, upstreamError map[string]any) error {
	healthFlag, message, upstreamError = normalizeContextOverflowTerminalFailure(healthFlag, message, upstreamError)
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
	if len(upstreamError) == 0 {
		upstreamError = map[string]any{"message": message}
	}
	if err := writeAnthropicSSEEvent(w, flusher, "error", map[string]any{
		"type":        "error",
		"request_id":  requestID,
		"health_flag": healthFlag,
		"error":       upstreamError,
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
		anthropicInput := input
		if total, ok := usageNumberAsFloatForStreaming(input); ok {
			cachedTotal := 0.0
			if details, _ := usage["input_tokens_details"].(map[string]any); len(details) > 0 {
				if cached, ok := usageNumberAsFloatForStreaming(details["cached_tokens"]); ok {
					cachedTotal += cached
				}
				if created, ok := usageNumberAsFloatForStreaming(details["cache_creation_tokens"]); ok {
					cachedTotal += created
				}
			}
			if diff := total - cachedTotal; diff >= 0 {
				anthropicInput = diff
			}
		}
		out["input_tokens"] = anthropicInput
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

func firstReasoningBlock(data map[string]any) map[string]any {
	rawBlocks, _ := data["blocks"].([]any)
	for _, rawBlock := range rawBlocks {
		block, _ := rawBlock.(map[string]any)
		if len(block) == 0 {
			continue
		}
		return block
	}
	return nil
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
	reasoningText       strings.Builder
	reasoningSummaries  map[reasoningSummaryKey]*reasoningSummaryState
	thinkingTagStyle    string
	planningSent        bool
	toolStatusSent      bool
	toolIDAliases       map[string]string
	toolMeta            map[string]map[string]string
	toolIndex           map[string]int
	toolSent            map[string]bool
	pendingToolArgs     map[string]string
	nextToolIx          int
	reasoningTextActive bool
	textTail            visibleTextTailBuffer
	terminalSeen        bool
	terminalFailure     *aggregate.TerminalFailureError
	pendingReasoningTag string
}

func writeChatSSELive(ctx context.Context, stream *upstream.EventStream, w http.ResponseWriter, flusher http.Flusher, req model.CanonicalRequest, upstreamEndpointType string, thinkingTagStyle string, usageRecorder usageRecorderFunc) (aggregate.Result, error) {
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
		return aggregate.Result{}, err
	}
	collector := aggregate.NewCollector()
	err := streamLiveWithSyntheticTicks(ctx, stream.Consume,
		func() bool { return state.textStarted || state.realReasoningSeen },
		func() error {
			if state.textStarted || state.realReasoningSeen {
				return nil
			}
			return nil
		},
		func() error { return writeSSEHeartbeat(w, flusher, state.terminalSeen) },
		func(evt upstream.Event) error {
			collector.Accept(evt)
			return writer.WriteEvent(evt.Event, evt.Data)
		},
	)
	if err != nil && !state.terminalSeen {
		return aggregate.Result{}, err
	}
	if !state.terminalSeen {
		return aggregate.Result{}, io.ErrUnexpectedEOF
	}
	if state.terminalFailure != nil {
		return aggregate.Result{}, state.terminalFailure
	}
	result, err := collector.Result()
	if err != nil {
		return aggregate.Result{}, err
	}
	return result, nil
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
	seenEvent := false

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
				seenEvent = true
				if err := onEvent(*sig.evt); err != nil {
					return err
				}
				continue
			}
			if sig.done {
				if sig.err == nil && !seenEvent {
					return io.ErrUnexpectedEOF
				}
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

func shouldBufferChatToolArguments(name string) bool {
	return name == "attempt_completion"
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
		arguments := state.pendingToolArgs[itemID]
		if repaired, ok := syntaxrepair.RepairJSON(state.pendingToolArgs[itemID]); ok {
			arguments = repaired
		}
		if shouldBufferChatToolArguments(meta["name"]) {
			if err := writeChatChunk(w, flusher, state, chatToolMetadataDelta(state.toolIndex[itemID], meta["call_id"], meta["name"]), "", nil); err != nil {
				return err
			}
			if arguments != "" {
				if err := writeChatChunk(w, flusher, state, chatToolDelta(state.toolIndex[itemID], "", "", arguments, false), "", nil); err != nil {
					return err
				}
			}
		} else {
			toolDelta := chatToolDelta(state.toolIndex[itemID], meta["call_id"], meta["name"], arguments, true)
			if err := writeChatChunk(w, flusher, state, toolDelta, "", nil); err != nil {
				return err
			}
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
		if itemType, _ := item["type"].(string); itemType == "reasoning" {
			for _, reasoningContent := range formatStreamingReasoningItemSummary(&state.reasoningSummaries, item) {
				state.realReasoningSeen = true
				if err := ensureRoleSent(); err != nil {
					return err
				}
				if err := writeChatChunk(w, flusher, state, map[string]any{"reasoning_content": reasoningContent}, "", nil); err != nil {
					return err
				}
			}
		}
		if itemType, _ := item["type"].(string); isResponseToolCallItemType(itemType) {
			state.reasoningTextActive = false
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
					if shouldBufferChatToolArguments(state.toolMeta[itemID]["name"]) && evt.Event != "response.output_item.done" {
						return nil
					}
					if err := flushPendingToolCall(itemID); err != nil {
						return err
					}
				}
			}
		}
	case "response.output_text.delta":
		state.reasoningTextActive = false
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
			if err := ensureRoleSent(); err != nil {
				return err
			}
			if err := writeChatChunk(w, flusher, state, map[string]any{"reasoning_content": reasoningContent}, "", nil); err != nil {
				return err
			}
		}
		cleanContent = state.textTail.Push(cleanContent)
		if cleanContent != "" {
			if err := ensureRoleSent(); err != nil {
				return err
			}
			state.textStarted = true
			if err := writeChatChunk(w, flusher, state, map[string]any{"content": cleanContent}, "", nil); err != nil {
				return err
			}
		}
	case "response.reasoning.delta":
		if !state.reasoningTextActive {
			state.reasoningText.Reset()
			state.reasoningTextActive = true
		}
		delta := formatStreamingReasoningDelta(&state.reasoningText, reasoningContentRawValue(evt.Data))
		if delta != "" {
			state.realReasoningSeen = true
			if err := ensureRoleSent(); err != nil {
				return err
			}
			if err := writeChatChunk(w, flusher, state, map[string]any{"reasoning_content": delta}, "", nil); err != nil {
				return err
			}
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
		itemID := reasoningFormatItemID(evt.Data)
		summaryIndex, ok := intValue(evt.Data["summary_index"])
		if !ok {
			summaryIndex = 0
		}
		summaryIndex = reasoningFormatSummaryIndex(evt.Data, summaryIndex)
		delta := formatStreamingReasoningSummary(&state.reasoningSummaries, itemID, summaryIndex, reasoningContentRawValue(evt.Data), evt.Event == "response.reasoning_summary_text.done")
		if delta != "" {
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
			if shouldBufferChatToolArguments(state.toolMeta[itemID]["name"]) {
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
		state.textTail.Discard()
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
	case "error", "response.failed", "response.incomplete":
		state.terminalSeen = true
		state.textTail.Discard()
		state.terminalFailure = terminalFailureFromEventData(evt.Data)
		return writeChatTerminalFailure(w, flusher, state.terminalFailure.HealthFlag, state.terminalFailure.Message, upstreamErrorObjectFromEventData(evt.Data))
	}
	return nil
}

func terminalFailureFromEventData(data map[string]any) *aggregate.TerminalFailureError {
	healthFlag, _ := data["health_flag"].(string)
	message, _ := data["message"].(string)
	errObj := upstreamErrorObjectFromEventData(data)
	if healthFlag == "" {
		healthFlag = stringValue(data["code"])
	}
	if healthFlag == "" {
		healthFlag = stringValue(data["type"])
	}
	if healthFlag == "" {
		healthFlag = stringValue(errObj["code"])
		if healthFlag == "" {
			healthFlag = stringValue(errObj["type"])
		}
	}
	if message == "" {
		message = stringValue(errObj["message"])
	}
	if healthFlag == "" {
		healthFlag = "upstreamStreamBroken"
	}
	if message == "" {
		message = "upstream response incomplete"
	}
	healthFlag, message, errObj = normalizeContextOverflowTerminalFailure(healthFlag, message, errObj)
	return &aggregate.TerminalFailureError{HealthFlag: healthFlag, Message: message}
}

func normalizeResponsesCompletedFinishReason(response map[string]any) {
	if response == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(stringValue(response["finish_reason"]))) {
	case "end_turn":
		response["finish_reason"] = "stop"
	case "tool_use":
		response["finish_reason"] = "tool_calls"
	}
}

func normalizeContextOverflowTerminalFailure(healthFlag string, message string, upstreamError map[string]any) (string, string, map[string]any) {
	normalizedHealthFlag, normalizedMessage, ok := normalizeContextOverflowSignal(healthFlag, message, upstreamError)
	if !ok {
		return healthFlag, message, upstreamError
	}
	if upstreamError == nil {
		upstreamError = map[string]any{}
	} else {
		upstreamError = cloneMap(upstreamError)
	}
	upstreamError["type"] = "invalid_request_error"
	upstreamError["code"] = normalizedHealthFlag
	upstreamError["message"] = normalizedMessage
	if _, ok := upstreamError["param"]; !ok {
		upstreamError["param"] = "input"
	}
	return normalizedHealthFlag, normalizedMessage, upstreamError
}

func normalizeContextOverflowSignal(healthFlag string, message string, upstreamError map[string]any) (string, string, bool) {
	if upstreamMessage := stringValue(upstreamError["message"]); message == "" {
		message = upstreamMessage
	}
	return contextoverflow.NormalizeCandidates([]string{
		healthFlag,
		stringValue(upstreamError["code"]),
		stringValue(upstreamError["type"]),
	}, message)
}

func upstreamErrorObjectFromEventData(data map[string]any) map[string]any {
	if len(data) == 0 {
		return nil
	}
	if errObj, _ := data["error"].(map[string]any); len(errObj) > 0 {
		return cloneMap(errObj)
	}
	response, _ := data["response"].(map[string]any)
	if errObj, _ := response["error"].(map[string]any); len(errObj) > 0 {
		return cloneMap(errObj)
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

func writeChatTerminalFailure(w http.ResponseWriter, flusher http.Flusher, healthFlag string, message string, upstreamError map[string]any) error {
	healthFlag, message, upstreamError = normalizeContextOverflowTerminalFailure(healthFlag, message, upstreamError)
	errObj := map[string]any{"health_flag": healthFlag, "message": message}
	for key, value := range upstreamError {
		errObj[key] = value
	}
	if _, ok := errObj["health_flag"]; !ok {
		errObj["health_flag"] = healthFlag
	}
	if _, ok := errObj["message"]; !ok {
		errObj["message"] = message
	}
	if err := writeChatChunk(w, flusher, nil, map[string]any{"error": errObj}, "error", nil); err != nil {
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

func writeSSEHeartbeat(w http.ResponseWriter, flusher http.Flusher, terminalSeen bool) error {
	if terminalSeen {
		return nil
	}
	return writeSSEComment(w, flusher, "keep-alive")
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

func chatToolMetadataDelta(index int, callID, name string) map[string]any {
	return map[string]any{"tool_calls": []map[string]any{{
		"index": index,
		"id":    callID,
		"type":  "function",
		"function": map[string]any{
			"name": name,
		},
	}}}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func intValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, typed >= 0
	case int64:
		return int(typed), typed >= 0
	case float64:
		if typed < 0 || typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
}

func isResponseToolCallItemType(itemType string) bool {
	switch itemType {
	case "function_call", "web_search_call", "file_search_call", "computer_call", "custom_tool_call", "code_interpreter_call":
		return true
	default:
		return false
	}
}

func reasoningContentValue(data map[string]any) string {
	return reasoningtext.FormatText(reasoningContentRawValue(data))
}

func reasoningContentRawValue(data map[string]any) string {
	for _, key := range []string{"thinking", "reasoning_content", "summary", "reasoning", "content", "delta", "text"} {
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
	return reasoningtext.FormatText(builder.String())
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
