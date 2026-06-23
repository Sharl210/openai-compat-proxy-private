package upstream

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/syntaxrepair"
)

const (
	// opencode 伪装：来自 @ai-sdk/provider-utils 的真实 User-Agent 格式
	// 格式：opencode/{version} ai-sdk/provider-utils/{version} runtime/{runtime}/{version}
	// 验证来源：issue #8444 (anomalyco/opencode), issue #12799/PR #12800 (vercel/ai)
	opencodeDefaultClientVersion = "1.17.8"
	opencodeOriginator           = "opencode"

	// claude 伪装：必须用 claude-cli/ 格式才能通过 sub2api 的 isClaudeCodeClient 检测
	// sub2api 的检测 regex：^claude-cli/\d+\.\d+\.\d+（需同时有 metadata.user_id）
	// 真实 Claude Code CLI 发的是 claude-code/（不匹配），但 sub2api 接受 claude-cli/ 作为有效标识
	// 来源：sub2api gateway_service.go 的 claudeCliUserAgentRe + DefaultHeaders (constants.go)
	claudeCodeDefaultClientVersion = "2.1.183"
	claudeCodeXApp                 = "cli"
	// beta header：与 sub2api 的 FullClaudeCodeMimicryBetas/当前 CLI 抓包对齐
	claudeCodeBeta                = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,prompt-caching-scope-2026-01-05,effort-2025-11-24,context-management-2025-06-27,extended-cache-ttl-2025-04-11"
	claudeCodeSystemPrompt        = "You are Claude Code, Anthropic's official CLI for Claude."
	claudeCodeBillingSystemMarker = "x-anthropic-billing-header\ncc_entrypoint=cli"

	// codex 伪装：来自 codex-rs/login/src/auth/default_client.rs 的 get_codex_user_agent() 与 default_headers()
	// 格式：codex_cli_rs/{version} ({OS_TYPE} {OS_VERSION}; {ARCHITECTURE}) {TERMINAL_INFO}
	// 示例：codex_cli_rs/0.141.0 (Linux 6.1; x86_64) iTerm.app
	codexDefaultClientVersion = "0.141.0"

	anthropicBetaCompact20260112           = "compact-2026-01-12"
	anthropicBetaContextManagement20250627 = "context-management-2025-06-27"
)

type RequestValidationError struct {
	Message string
}

func (e *RequestValidationError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (c *Client) endpointType() string {
	if c == nil {
		return config.UpstreamEndpointTypeResponses
	}
	return normalizeEndpointType(c.upstreamEndpointType)
}

func normalizeEndpointType(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case config.UpstreamEndpointTypeChat:
		return config.UpstreamEndpointTypeChat
	case config.UpstreamEndpointTypeAnthropic:
		return config.UpstreamEndpointTypeAnthropic
	default:
		return config.UpstreamEndpointTypeResponses
	}
}

func endpointPathForType(endpointType string) string {
	switch normalizeEndpointType(endpointType) {
	case config.UpstreamEndpointTypeChat:
		return "/chat/completions"
	case config.UpstreamEndpointTypeAnthropic:
		return "/messages"
	default:
		return "/responses"
	}
}

func EffectiveMasqueradeUserAgent(masqueradeTarget string, masqueradeClientVersion string) string {
	version := strings.TrimSpace(masqueradeClientVersion)
	switch masqueradeTarget {
	case config.MasqueradeTargetOpenCode:
		if version == "" {
			version = opencodeDefaultClientVersion
		}
		return "opencode/" + version + " ai-sdk/provider-utils/4.0.27 runtime/bun/1.3.14"
	case config.MasqueradeTargetClaude:
		if version == "" {
			version = claudeCodeDefaultClientVersion
		}
		return "claude-cli/" + version + " (external, cli)"
	case config.MasqueradeTargetCodex:
		if version == "" {
			version = codexDefaultClientVersion
		}
		return "codex_cli_rs/" + version + " (Linux 6.1; x86_64) iTerm.app"
	default:
		return ""
	}
}

func FinalMasqueradeUserAgent(userAgent string, masqueradeTarget string, masqueradeClientVersion string) string {
	if masqueradeTarget == "" || masqueradeTarget == config.MasqueradeTargetNone {
		return ""
	}
	if trimmed := strings.TrimSpace(userAgent); trimmed != "" {
		return trimmed
	}
	return EffectiveMasqueradeUserAgent(masqueradeTarget, masqueradeClientVersion)
}

func applyUpstreamHeaders(httpReq *http.Request, endpointType string, authorization string, anthropicVersion string, anthropicBeta string, userAgent string, masqueradeTarget string, masqueradeClientVersion string) {
	if httpReq == nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	switch masqueradeTarget {
	case config.MasqueradeTargetOpenCode:
		httpReq.Header.Set("User-Agent", EffectiveMasqueradeUserAgent(masqueradeTarget, masqueradeClientVersion))
		httpReq.Header.Set("originator", opencodeOriginator)
	case config.MasqueradeTargetClaude:
		httpReq.Header.Set("User-Agent", EffectiveMasqueradeUserAgent(masqueradeTarget, masqueradeClientVersion))
		httpReq.Header.Set("X-App", claudeCodeXApp)
		httpReq.Header.Set("anthropic-beta", claudeCodeBeta)
		httpReq.Header.Set("X-Stainless-Lang", "js")
		httpReq.Header.Set("X-Stainless-Package-Version", "0.94.0")
		httpReq.Header.Set("X-Stainless-OS", "Linux")
		httpReq.Header.Set("X-Stainless-Arch", "arm64")
		httpReq.Header.Set("X-Stainless-Runtime", "node")
		httpReq.Header.Set("X-Stainless-Runtime-Version", "v24.3.0")
		httpReq.Header.Set("X-Stainless-Timeout", "600")
		httpReq.Header.Set("X-Stainless-Retry-Count", "0")
		httpReq.Header.Set("Accept", "application/json")
		httpReq.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
		httpReq.Header.Set("Anthropic-Dangerous-Direct-Browser-Access", "true")
	case config.MasqueradeTargetCodex:
		httpReq.Header.Set("User-Agent", EffectiveMasqueradeUserAgent(masqueradeTarget, masqueradeClientVersion))
		httpReq.Header.Set("originator", "codex_cli_rs")
		httpReq.Header.Set("x-openai-internal-codex-residency", "us")
	case config.MasqueradeTargetNone:
		// no-op：不注入任何伪装 header
	}
	if userAgent != "" {
		httpReq.Header.Set("User-Agent", strings.TrimSpace(userAgent))
	}
	if normalizeEndpointType(endpointType) == config.UpstreamEndpointTypeAnthropic {
		version := strings.TrimSpace(anthropicVersion)
		if version == "" {
			version = "2023-06-01"
		}
		httpReq.Header.Set("anthropic-version", version)
		if beta := mergeAnthropicBetaHeaders(httpReq.Header.Get("anthropic-beta"), anthropicBeta); beta != "" {
			httpReq.Header.Set("anthropic-beta", beta)
		}
		if apiKey := upstreamAPIKeyFromAuthorization(authorization); apiKey != "" {
			httpReq.Header.Set("x-api-key", apiKey)
		}
		return
	}
	if authorization != "" {
		httpReq.Header.Set("Authorization", authorization)
	}
}

func mergeAnthropicBetaHeaders(values ...string) string {
	seen := map[string]struct{}{}
	merged := make([]string, 0)
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, ok := seen[part]; ok {
				continue
			}
			seen[part] = struct{}{}
			merged = append(merged, part)
		}
	}
	if len(merged) == 0 {
		return ""
	}
	sort.Strings(merged)
	return strings.Join(merged, ",")
}

func upstreamAPIKeyFromAuthorization(authorization string) string {
	trimmed := strings.TrimSpace(authorization)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "bearer ") {
		return strings.TrimSpace(trimmed[7:])
	}
	return trimmed
}

// extractOriginalToolIDs extracts tool call IDs from the request messages by index order.
// This is used to preserve original tool IDs when upstream reassigns them.
func extractOriginalToolIDs(req model.CanonicalRequest) map[int]string {
	result := make(map[int]string)
	index := 0
	for _, msg := range req.Messages {
		for _, call := range msg.ToolCalls {
			if call.ID != "" {
				result[index] = call.ID
			}
			index++
		}
	}
	return result
}

func buildRequestBodyForEndpoint(req model.CanonicalRequest, endpointType string, masqueradeTarget string, injectMetadataUserID bool, injectSystemPrompt bool, upstreamCacheControl string, xmlToolCallStyle ...string) ([]byte, error) {
	if err := validateRequestForEndpoint(req, endpointType); err != nil {
		return nil, err
	}
	switch normalizeEndpointType(endpointType) {
	case config.UpstreamEndpointTypeChat:
		return buildChatRequestBody(req, xmlToolCallStyle...)
	case config.UpstreamEndpointTypeAnthropic:
		return buildAnthropicRequestBody(req, masqueradeTarget, injectMetadataUserID, injectSystemPrompt, upstreamCacheControl)
	default:
		return buildResponsesRequestBodyWithMasquerade(req, config.ResponsesToolCompatModePreserve, masqueradeTarget)
	}
}

func validateRequestForEndpoint(req model.CanonicalRequest, endpointType string) error {
	if normalizeEndpointType(endpointType) == config.UpstreamEndpointTypeAnthropic {
		_, err := anthropicBetaHeaderForRequest(req)
		return err
	}
	return nil
}

func anthropicBetaHeaderForRequest(req model.CanonicalRequest) (string, error) {
	raw, ok := req.PreservedTopLevelFields["context_management"]
	if !ok {
		return "", nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return "", &RequestValidationError{Message: "context_management must be an object"}
	}
	editsRaw, ok := obj["edits"]
	if !ok {
		return "", &RequestValidationError{Message: "context_management.edits is required"}
	}
	edits, ok := editsRaw.([]any)
	if !ok || len(edits) == 0 {
		return "", &RequestValidationError{Message: "context_management.edits must be a non-empty array"}
	}
	betaHeaders := make([]string, 0, len(edits))
	for _, rawEdit := range edits {
		edit, ok := rawEdit.(map[string]any)
		if !ok {
			return "", &RequestValidationError{Message: "context_management.edits entries must be objects"}
		}
		editType, _ := edit["type"].(string)
		switch strings.TrimSpace(editType) {
		case "compact_20260112":
			betaHeaders = append(betaHeaders, anthropicBetaCompact20260112)
		case "clear_tool_uses_20250919", "clear_thinking_20251015":
			betaHeaders = append(betaHeaders, anthropicBetaContextManagement20250627)
		default:
			return "", &RequestValidationError{Message: fmt.Sprintf("unsupported context_management edit type: %s", editType)}
		}
	}
	return mergeAnthropicBetaHeaders(betaHeaders...), nil
}

func filteredPreservedTopLevelFieldsForEndpoint(fields map[string]any, endpointType string) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	normalizedEndpointType := normalizeEndpointType(endpointType)
	if normalizedEndpointType == config.UpstreamEndpointTypeResponses {
		if !isAnthropicOnlyTopLevelField("context_management", normalizedEndpointType) {
			return fields
		}
		filtered := map[string]any{}
		for key, value := range fields {
			if isAnthropicOnlyTopLevelField(key, normalizedEndpointType) {
				continue
			}
			filtered[key] = value
		}
		if len(filtered) == 0 {
			return nil
		}
		return filtered
	}
	filtered := map[string]any{}
	for key, value := range fields {
		if isResponsesOnlyTopLevelField(key, normalizedEndpointType) || isAnthropicOnlyTopLevelField(key, normalizedEndpointType) {
			continue
		}
		filtered[key] = value
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func isAnthropicOnlyTopLevelField(key string, endpointType string) bool {
	switch key {
	case "context_management", "cache_control":
		return endpointType != config.UpstreamEndpointTypeAnthropic
	default:
		return false
	}
}

func isResponsesOnlyTopLevelField(key string, endpointType string) bool {
	switch key {
	case "output_config", "previous_response_id", "prompt_cache_key", "store", "include", "truncation", "text":
		return true
	case "parallel_tool_calls":
		return endpointType == config.UpstreamEndpointTypeAnthropic
	case "response_format":
		return endpointType == config.UpstreamEndpointTypeAnthropic
	default:
		return false
	}
}

func buildStreamingRequestBody(req model.CanonicalRequest, endpointType string, masqueradeTarget string, injectMetadataUserID bool, injectSystemPrompt bool, upstreamCacheControl string, xmlToolCallStyle ...string) ([]byte, error) {
	req.Stream = true
	return buildRequestBodyForEndpoint(req, endpointType, masqueradeTarget, injectMetadataUserID, injectSystemPrompt, upstreamCacheControl, xmlToolCallStyle...)
}

func normalizeResponsePayload(endpointType string, payload map[string]any, thinkingTagStyle string, xmlToolCallStyle string) map[string]any {
	switch normalizeEndpointType(endpointType) {
	case config.UpstreamEndpointTypeChat:
		return normalizeChatPayload(payload, thinkingTagStyle, xmlToolCallStyle)
	case config.UpstreamEndpointTypeAnthropic:
		return normalizeAnthropicPayload(payload)
	default:
		return payload
	}
}

func eventBatchReaderForType(endpointType string, thinkingTagStyle string, xmlToolCallStyle string, originalToolIDs map[int]string, requestID string, allowEOFCompletion bool) func(*bufio.Scanner) ([]Event, error) {
	switch normalizeEndpointType(endpointType) {
	case config.UpstreamEndpointTypeChat:
		return newChatEventBatchReader(thinkingTagStyle, xmlToolCallStyle, originalToolIDs, requestID, allowEOFCompletion)
	case config.UpstreamEndpointTypeAnthropic:
		return newAnthropicEventBatchReader(originalToolIDs)
	default:
		return readNextResponsesEventBatch
	}
}

func readNextResponsesEventBatch(scanner *bufio.Scanner) ([]Event, error) {
	evt, err := readNextSSEEvent(scanner)
	if err != nil {
		return nil, err
	}
	if evt == nil {
		return nil, nil
	}
	events := []Event{*evt}
	shadowRecordResponses(events, evt.Event)
	return events, nil
}

func shadowRecordResponses(events []Event, originalEvent string) {
	if len(events) == 0 || originalEvent == "[DONE]" {
		return
	}
	rawData := events[0].Raw
	if !json.Valid(rawData) {
		return
	}
	envelope := map[string]any{
		"provider":      "responses",
		"originalEvent": originalEvent,
		"_raw":          json.RawMessage(rawData),
	}
	envelopeBytes, _ := json.Marshal(envelope)
	events[0].Raw = envelopeBytes
}

func consumeSSEScannerWithReader(scanner *bufio.Scanner, readNext func(*bufio.Scanner) ([]Event, error), onEvent func(Event) error) error {
	if readNext == nil {
		readNext = readNextResponsesEventBatch
	}
	seenEvent := false
	seenTerminal := false
	for {
		events, err := readNext(scanner)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			if seenEvent && !seenTerminal {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		for _, evt := range events {
			seenEvent = true
			if isTerminalStreamEvent(evt) {
				seenTerminal = true
			}
			if err := onEvent(evt); err != nil {
				return err
			}
		}
	}
}

type sseFrame struct {
	Event string
	Data  string
}

func readNextSSEFrame(scanner *bufio.Scanner) (*sseFrame, error) {
	var currentEvent string
	var dataLines []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if currentEvent != "" || len(dataLines) > 0 {
				return &sseFrame{Event: currentEvent, Data: strings.Join(dataLines, "\n")}, nil
			}
			currentEvent = ""
			dataLines = nil
			continue
		}
		if rest, ok := strings.CutPrefix(line, "event:"); ok {
			currentEvent = strings.TrimSpace(rest)
			continue
		}
		if rest, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimSpace(rest))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if currentEvent != "" || len(dataLines) > 0 {
		return &sseFrame{Event: currentEvent, Data: strings.Join(dataLines, "\n")}, nil
	}
	return nil, nil
}

func newChatEventBatchReader(thinkingTagStyle string, xmlToolCallStyle string, originalToolIDs map[int]string, requestID string, allowEOFCompletion bool) func(*bufio.Scanner) ([]Event, error) {
	state := &chatNormalizationState{
		toolIDsByIndex:       map[int]string{},
		toolSent:             map[string]bool{},
		pendingItems:         map[string]map[string]any{},
		thinkingTagStyle:     thinkingTagStyle,
		upstreamXMLToolStyle: xmlToolCallStyle,
		originalToolIDs:      originalToolIDs,
		requestID:            requestID,
		provider:             "chat",
	}
	return func(scanner *bufio.Scanner) ([]Event, error) {
		for {
			frame, err := readNextSSEFrame(scanner)
			if err != nil {
				return nil, err
			}
			if frame == nil {
				if allowEOFCompletion {
					if events := finalizeChatTerminalEventsOnEOF(state); len(events) > 0 {
						return events, nil
					}
				}
				return nil, nil
			}
			events, done, err := normalizeChatFrame(frame, state)
			if err != nil {
				return nil, err
			}
			if done && len(events) == 0 {
				return nil, nil
			}
			if len(events) == 0 {
				continue
			}
			return events, nil
		}
	}
}

func finalizeChatTerminalEventsOnEOF(state *chatNormalizationState) []Event {
	if state == nil || state.completed || !state.createdSent {
		return nil
	}
	if state.pendingFinish == "" {
		return nil
	}
	return finalizeChatTerminalEvents(state)
}

func finalizeChatTerminalEvents(state *chatNormalizationState) []Event {
	if state == nil || state.completed || !state.createdSent {
		return nil
	}
	var events []Event
	incomplete := isChatIncompleteFinishReason(state.pendingFinish)
	if state.pendingItems != nil && !incomplete {
		for itemID, pending := range state.pendingItems {
			item := map[string]any{"type": "function_call", "id": itemID, "call_id": itemID, "name": pending["name"]}
			if args, ok := pending["arguments"].(string); ok && args != "" {
				item["arguments"] = args
			}
			events = append(events, Event{Event: "response.output_item.done", Data: map[string]any{"item": item}})
			state.toolSent[itemID] = true
		}
		state.pendingItems = nil
	}
	responseData := map[string]any{"id": state.responseID, "object": "response"}
	if state.pendingFinish != "" {
		responseData["finish_reason"] = state.pendingFinish
	}
	if len(state.usage) > 0 {
		responseData["usage"] = cloneMap(state.usage)
	}
	terminalEvent := "response.completed"
	terminalData := map[string]any{"response": responseData}
	if incomplete {
		terminalEvent = "response.incomplete"
		terminalData["health_flag"] = chatIncompleteHealthFlag(state.pendingFinish)
		terminalData["message"] = fmt.Sprintf("upstream response finished with %s", state.pendingFinish)
	}
	events = append(events, Event{Event: terminalEvent, Data: terminalData})
	state.completed = true
	state.pendingFinish = ""
	return events
}

func chatIncompleteHealthFlag(finishReason string) string {
	switch finishReason {
	case "max_tokens":
		return "upstream_max_tokens"
	case "length":
		return "upstream_length"
	default:
		return "upstream_incomplete"
	}
}

func isChatIncompleteFinishReason(finishReason string) bool {
	switch finishReason {
	case "max_tokens", "length":
		return true
	default:
		return false
	}
}

type chatNormalizationState struct {
	toolIDsByIndex              map[int]string
	toolSent                    map[string]bool
	usage                       map[string]any
	createdSent                 bool
	completed                   bool
	pendingFinish               string
	pendingItems                map[string]map[string]any
	pendingThinkingTag          string
	pendingThinking             string
	pendingLegacyXMLToolText    string
	thinkingTagStyle            string
	upstreamXMLToolStyle        string
	implicitThinkingInitialized bool
	implicitThinkingActive      bool
	suppressBlankTextAfterThink bool
	originalToolIDs             map[int]string
	responseID                  string
	requestID                   string
	provider                    string
}

func shadowRecord(events []Event, frame *sseFrame, provider string) {
	if len(events) == 0 {
		return
	}
	if provider == "" {
		provider = "unknown"
	}
	trimmedData := strings.Trim(strings.TrimSpace(frame.Data), "\r")
	if trimmedData == "[DONE]" {
		return
	}
	rawData := []byte(frame.Data)
	if !json.Valid(rawData) {
		return
	}
	for i := range events {
		envelope := map[string]any{
			"provider":      provider,
			"originalEvent": frame.Event,
			"_raw":          json.RawMessage(rawData),
		}
		envelopeBytes, _ := json.Marshal(envelope)
		events[i].Raw = envelopeBytes
	}
}

func normalizeChatFrame(frame *sseFrame, state *chatNormalizationState) ([]Event, bool, error) {
	if frame == nil {
		return nil, true, nil
	}
	if strings.Trim(strings.TrimSpace(frame.Data), "\r") == "[DONE]" {
		return finalizeChatTerminalEvents(state), true, nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
		return nil, false, fmt.Errorf("parse chat sse frame: %w", err)
	}
	var events []Event
	if usage := normalizeChatUsage(payload); len(usage) > 0 {
		state.usage = usage
	}
	if !state.createdSent {
		if responseID := stringValue(payload["id"]); responseID != "" {
			state.responseID = responseID
			events = append(events, Event{Event: "response.created", Data: map[string]any{"response": map[string]any{"id": responseID, "object": "response"}}})
			state.createdSent = true
		}
	}
	choices, _ := payload["choices"].([]any)
	// Check if finish_reason is in choices (needs to be processed in the loop below)
	var finishReasonInChoices string
	for _, rawChoice := range choices {
		if choice, ok := rawChoice.(map[string]any); ok {
			if fr := stringValue(choice["finish_reason"]); fr != "" {
				finishReasonInChoices = fr
				break
			}
		}
	}
	if finishReasonInChoices != "" && state.pendingFinish == "" && !state.completed {
		state.pendingFinish = finishReasonInChoices
	}
	for _, rawChoice := range choices {
		choice, _ := rawChoice.(map[string]any)
		if choice == nil {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta != nil {
			if text, _ := delta["content"].(string); text != "" {
				if state.upstreamXMLToolStyle == config.UpstreamXMLToolCallStyleLegacy {
					if prefix, item, suffix, ok := extractLegacyXMLToolCallFromText(text); ok {
						events = appendChatTextEvents(events, prefix, state)
						events = append(events, legacyXMLToolCallEventsForState(item, state)...)
						events = appendChatTextEvents(events, suffix, state)
						continue
					}
					if item, buffered := consumeLegacyXMLToolText(text, state); buffered {
						if item != nil {
							events = append(events, legacyXMLToolCallEventsForState(item, state)...)
						}
						continue
					}
					if item := parseLegacyXMLToolCall(text); item != nil {
						events = append(events, legacyXMLToolCallEventsForState(item, state)...)
						continue
					}
				}
				events = appendChatTextEvents(events, text, state)
			}
			if reasoning, _ := delta["reasoning_content"].(string); reasoning != "" {
				events = append(events, Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": reasoning}})
			}
			toolCalls, _ := delta["tool_calls"].([]any)
			for _, rawTool := range toolCalls {
				tool, _ := rawTool.(map[string]any)
				if tool == nil {
					continue
				}
				index := int(numberValue(tool["index"]))
				itemID := strings.TrimSpace(stringValue(tool["id"]))
				if itemID == "" {
					itemID = state.toolIDsByIndex[index]
				}
				if itemID == "" {
					itemID = state.originalToolIDs[index]
				}
				if itemID == "" {
					itemID = fmt.Sprintf("tool_%d", index)
				}
				state.toolIDsByIndex[index] = itemID
				function, _ := tool["function"].(map[string]any)
				name := stringValue(function["name"])
				arguments := stringValue(function["arguments"])
				if name != "" || stringValue(tool["id"]) != "" {
					emittedDoneWithArguments := false
					if !state.toolSent[itemID] {
						if arguments != "" {
							events = append(events, Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"type": "function_call", "id": itemID, "call_id": itemID, "name": name, "arguments": arguments}}})
							state.toolSent[itemID] = true
							emittedDoneWithArguments = true
						} else {
							events = append(events, Event{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"type": "function_call", "id": itemID, "call_id": itemID, "name": name}}})
							if state.pendingItems == nil {
								state.pendingItems = map[string]map[string]any{}
							}
							state.pendingItems[itemID] = map[string]any{"type": "function_call", "id": itemID, "call_id": itemID, "name": name}
						}
					}
					if arguments != "" && !emittedDoneWithArguments {
						events = append(events, Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": itemID, "delta": arguments}})
					}
				} else if arguments != "" {
					if state.pendingItems != nil {
						if pending, ok := state.pendingItems[itemID]; ok {
							existingArgs, _ := pending["arguments"].(string)
							pending["arguments"] = existingArgs + arguments
						}
					}
					events = append(events, Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": itemID, "delta": arguments}})
				}
			}
		}
		if finishReason := stringValue(choice["finish_reason"]); finishReason != "" && !state.completed {
			state.pendingFinish = finishReason
		}
	}
	if isChatIncompleteFinishReason(state.pendingFinish) {
		events = append(events, finalizeChatTerminalEvents(state)...)
	}
	shadowRecord(events, frame, state.provider)
	return events, false, nil
}

func consumeLegacyXMLToolText(text string, state *chatNormalizationState) (map[string]any, bool) {
	if state == nil {
		return nil, false
	}
	if state.pendingLegacyXMLToolText == "" {
		trimmed := strings.TrimSpace(text)
		if !strings.HasPrefix(trimmed, "<tool_call>") || strings.HasSuffix(trimmed, "</tool_call>") {
			return nil, false
		}
		state.pendingLegacyXMLToolText = text
	} else {
		state.pendingLegacyXMLToolText += text
	}
	if !strings.Contains(state.pendingLegacyXMLToolText, "</tool_call>") {
		return nil, true
	}
	buffered := state.pendingLegacyXMLToolText
	state.pendingLegacyXMLToolText = ""
	return parseLegacyXMLToolCall(buffered), true
}

func appendChatTextEvents(events []Event, text string, state *chatNormalizationState) []Event {
	if text == "" {
		return events
	}
	if state.thinkingTagStyle == config.UpstreamThinkingTagStyleLegacy && !state.implicitThinkingInitialized {
		state.implicitThinkingInitialized = true
		state.implicitThinkingActive = true
	}
	cleanText, reasoningContent := extractContentAndReasoningTagsWithState(text, state)
	state.suppressBlankTextAfterThink = state.suppressBlankTextAfterThink || reasoningContent != ""
	if reasoningContent != "" {
		events = append(events, Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": reasoningContent}})
	}
	cleanText = suppressWhitespaceOnlyTextAfterThinkExtraction(cleanText, state.suppressBlankTextAfterThink)
	if cleanText != "" {
		state.suppressBlankTextAfterThink = false
		events = append(events, Event{Event: "response.output_text.delta", Data: map[string]any{"delta": cleanText}})
	}
	return events
}

func extractLegacyXMLToolCallFromText(text string) (string, map[string]any, string, bool) {
	start := strings.Index(text, "<tool_call>")
	if start < 0 {
		return "", nil, "", false
	}
	end := strings.Index(text[start:], "</tool_call>")
	if end < 0 {
		return "", nil, "", false
	}
	end += start + len("</tool_call>")
	item := parseLegacyXMLToolCall(text[start:end])
	if item == nil {
		return "", nil, "", false
	}
	prefix := text[:start]
	if strings.TrimSpace(prefix) == "" {
		prefix = ""
	}
	suffix := text[end:]
	if strings.TrimSpace(suffix) == "" {
		suffix = ""
	}
	return prefix, item, suffix, true
}

func parseLegacyXMLToolCall(text string) map[string]any {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "<tool_call>") || !strings.HasSuffix(trimmed, "</tool_call>") {
		return nil
	}
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "<tool_call>"), "</tool_call>"))
	if !strings.HasPrefix(body, "<function=") {
		return nil
	}
	nameEnd := strings.Index(body, ">")
	if nameEnd < 0 {
		return nil
	}
	name := strings.TrimSpace(body[len("<function="):nameEnd])
	if name == "" {
		return nil
	}
	paramsText := strings.TrimSpace(body[nameEnd+1:])
	if closeIndex := strings.LastIndex(paramsText, "</function>"); closeIndex >= 0 {
		paramsText = strings.TrimSpace(paramsText[:closeIndex])
	}
	arguments := map[string]any{}
	for paramsText != "" {
		paramsText = strings.TrimSpace(paramsText)
		if !strings.HasPrefix(paramsText, "<parameter=") {
			return nil
		}
		keyEnd := strings.Index(paramsText, ">")
		if keyEnd < 0 {
			return nil
		}
		key := strings.TrimSpace(paramsText[len("<parameter="):keyEnd])
		if key == "" {
			return nil
		}
		value, rest := splitLegacyXMLParameterValue(paramsText[keyEnd+1:])
		arguments[key] = parseLegacyXMLToolValue(value)
		paramsText = rest
	}
	encoded, err := marshalJSONWithoutHTMLEscape(arguments)
	if err != nil {
		return nil
	}
	return map[string]any{"type": "function_call", "id": "legacy_xml_tool_0", "call_id": "legacy_xml_tool_0", "name": name, "arguments": string(encoded)}
}

func splitLegacyXMLParameterValue(text string) (string, string) {
	valueEnd := len(text)
	restStart := len(text)
	if closeParam := strings.Index(text, "</parameter>"); closeParam >= 0 {
		valueEnd = closeParam
		restStart = closeParam + len("</parameter>")
	}
	for _, marker := range []string{"<parameter=", "</function>", "</tool_call>"} {
		if idx := strings.Index(text, marker); idx >= 0 && idx < valueEnd {
			valueEnd = idx
			restStart = idx
		}
	}
	return text[:valueEnd], text[restStart:]
}

func marshalJSONWithoutHTMLEscape(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func parseLegacyXMLToolValue(value string) any {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var parsed any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err == nil {
			return parsed
		}
	}
	if lower == "true" {
		return true
	}
	if lower == "false" {
		return false
	}
	if parsed, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return parsed
	}
	if parsed, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return parsed
	}
	return value
}

func legacyXMLToolCallEvents(item map[string]any) []Event {
	if len(item) == 0 {
		return nil
	}
	return []Event{{Event: "response.output_item.done", Data: map[string]any{"item": item}}}
}

func legacyXMLToolCallEventsForState(item map[string]any, state *chatNormalizationState) []Event {
	if len(item) == 0 {
		return nil
	}
	if state == nil || len(state.pendingItems) == 0 {
		return legacyXMLToolCallEvents(item)
	}
	name := stringValue(item["name"])
	if name == "" {
		return legacyXMLToolCallEvents(item)
	}
	for itemID, pending := range state.pendingItems {
		if stringValue(pending["name"]) != name {
			continue
		}
		completed := map[string]any{"type": "function_call", "id": itemID, "call_id": itemID, "name": name}
		if arguments := stringValue(item["arguments"]); arguments != "" {
			completed["arguments"] = arguments
		}
		delete(state.pendingItems, itemID)
		if state.toolSent != nil {
			state.toolSent[itemID] = true
		}
		return []Event{{Event: "response.output_item.done", Data: map[string]any{"item": completed}}}
	}
	return legacyXMLToolCallEvents(item)
}

func appendLegacyXMLToolCallsFromContent(content []any, output []any) ([]any, []any) {
	kept := make([]any, 0, len(content))
	for _, raw := range content {
		item, _ := raw.(map[string]any)
		if stringValue(item["type"]) != "output_text" {
			kept = append(kept, raw)
			continue
		}
		toolItem := parseLegacyXMLToolCall(stringValue(item["text"]))
		if toolItem == nil {
			kept = append(kept, raw)
			continue
		}
		toolItem["status"] = "completed"
		output = append(output, toolItem)
	}
	return kept, output
}

func legacyXMLToolCallsFromChatContent(content []any, existingToolCallCount int, enabled bool) []any {
	if !enabled || len(content) != 1 {
		return nil
	}
	part, _ := content[0].(map[string]any)
	if stringValue(part["type"]) != "text" {
		return nil
	}
	item := parseLegacyXMLToolCall(stringValue(part["text"]))
	if item == nil {
		return nil
	}
	callID := fmt.Sprintf("legacy_xml_tool_%d", existingToolCallCount)
	if id := stringValue(item["id"]); id != "" {
		callID = id
	}
	return []any{map[string]any{
		"id":   callID,
		"type": "function",
		"function": map[string]any{
			"name":      stringValue(item["name"]),
			"arguments": sanitizeToolArguments(stringValue(item["arguments"])),
		},
	}}
}

func newAnthropicEventBatchReader(originalToolIDs map[int]string) func(*bufio.Scanner) ([]Event, error) {
	state := &anthropicNormalizationState{toolIDsByIndex: map[int]string{}, usage: map[string]any{}, originalToolIDs: originalToolIDs, provider: "anthropic"}
	return func(scanner *bufio.Scanner) ([]Event, error) {
		for {
			frame, err := readNextSSEFrame(scanner)
			if err != nil {
				return nil, err
			}
			if frame == nil {
				return nil, nil
			}
			events, done, err := normalizeAnthropicFrame(frame, state)
			if err != nil {
				return nil, err
			}
			if done && len(events) == 0 {
				return nil, nil
			}
			if len(events) == 0 {
				continue
			}
			return events, nil
		}
	}
}

type anthropicNormalizationState struct {
	toolIDsByIndex  map[int]string
	usage           map[string]any
	completed       bool
	pendingFinish   string
	pendingItems    map[string]map[string]any
	pendingInitial  map[string]bool
	pendingOrder    []string
	responseID      string
	originalToolIDs map[int]string
	reasoningBlocks map[int]map[string]any
	provider        string
}

func normalizeAnthropicFrame(frame *sseFrame, state *anthropicNormalizationState) ([]Event, bool, error) {
	if frame == nil {
		return nil, true, nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
		return nil, false, fmt.Errorf("parse anthropic sse frame: %w", err)
	}
	var events []Event
	switch frame.Event {
	case "message_start":
		message, _ := payload["message"].(map[string]any)
		mergeUsage(state.usage, normalizeAnthropicUsage(message["usage"]))
		if responseID := stringValue(message["id"]); responseID != "" {
			state.responseID = responseID
			events = append(events, Event{Event: "response.created", Data: map[string]any{"response": map[string]any{"id": responseID, "object": "response"}}})
		}
	case "content_block_start":
		index := int(numberValue(payload["index"]))
		block, _ := payload["content_block"].(map[string]any)
		if blockType := stringValue(block["type"]); blockType == "tool_use" {
			itemID := stringValue(block["id"])
			if itemID == "" {
				itemID = state.toolIDsByIndex[index]
			}
			if itemID == "" {
				itemID = state.originalToolIDs[index]
			}
			if itemID == "" {
				itemID = fmt.Sprintf("tool_%d", index)
			}
			state.toolIDsByIndex[index] = itemID
			if state.pendingItems == nil {
				state.pendingItems = map[string]map[string]any{}
			}
			if _, ok := state.pendingItems[itemID]; !ok {
				state.pendingOrder = append(state.pendingOrder, itemID)
			}
			state.pendingItems[itemID] = map[string]any{"type": "function_call", "id": itemID, "call_id": itemID, "name": stringValue(block["name"])}
			events = append(events, Event{Event: "response.output_item.added", Data: map[string]any{"item": cloneMap(state.pendingItems[itemID])}})
			if input, ok := block["input"].(map[string]any); ok {
				encoded, _ := json.Marshal(input)
				state.pendingItems[itemID]["arguments"] = string(encoded)
				if state.pendingInitial == nil {
					state.pendingInitial = map[string]bool{}
				}
				state.pendingInitial[itemID] = true
				if len(input) > 0 {
					events = append(events, Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": itemID, "delta": string(encoded)}})
				}
			}
		} else if blockType == "thinking" || blockType == "redacted_thinking" {
			if state.reasoningBlocks == nil {
				state.reasoningBlocks = map[int]map[string]any{}
			}
			state.reasoningBlocks[index] = cloneMap(block)
			events = append(events, Event{Event: "response.reasoning.delta", Data: map[string]any{"blocks": []any{cloneMap(block)}}})
		}
	case "content_block_delta":
		index := int(numberValue(payload["index"]))
		delta, _ := payload["delta"].(map[string]any)
		switch stringValue(delta["type"]) {
		case "text_delta":
			if text := stringValue(delta["text"]); text != "" {
				events = append(events, Event{Event: "response.output_text.delta", Data: map[string]any{"delta": text}})
			}
		case "thinking_delta":
			if text := stringValue(delta["thinking"]); text != "" {
				if block := state.reasoningBlocks[index]; block != nil {
					key := "thinking"
					if stringValue(block["type"]) == "redacted_thinking" {
						key = "text"
					}
					block[key] = stringValue(block[key]) + text
				}
				var blocks []any
				if block := state.reasoningBlocks[index]; block != nil {
					blocks = []any{cloneMap(block)}
				}
				events = append(events, Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": text, "blocks": blocks}})
			}
		case "signature_delta":
			if signature := stringValue(delta["signature"]); signature != "" {
				if block := state.reasoningBlocks[index]; block != nil {
					block["signature"] = stringValue(block["signature"]) + signature
					events = append(events, Event{Event: "response.reasoning.delta", Data: map[string]any{"blocks": []any{cloneMap(block)}}})
				}
			}
		case "input_json_delta":
			if partial := stringValue(delta["partial_json"]); partial != "" {
				itemID := state.toolIDsByIndex[index]
				if itemID == "" {
					itemID = state.originalToolIDs[index]
				}
				if itemID == "" {
					itemID = fmt.Sprintf("tool_%d", index)
					state.toolIDsByIndex[index] = itemID
				}
				if state.pendingItems != nil {
					if pending, ok := state.pendingItems[itemID]; ok {
						existingArgs, _ := pending["arguments"].(string)
						if state.pendingInitial[itemID] {
							existingArgs = ""
							state.pendingInitial[itemID] = false
						}
						pending["arguments"] = existingArgs + partial
					}
				}
				events = append(events, Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": itemID, "delta": partial}})
			}
		}
	case "message_delta":
		if usage := normalizeAnthropicUsage(payload["usage"]); len(usage) > 0 {
			mergeUsage(state.usage, usage)
			events = append(events, Event{Event: "usage.update", Data: map[string]any{"usage": usage}, ArchiveOnly: true, RawEventName: frame.Event})
		}
		delta, _ := payload["delta"].(map[string]any)
		if stopReason := stringValue(delta["stop_reason"]); stopReason != "" && !state.completed {
			state.pendingFinish = stopReason
		}
	case "message_stop":
		if !state.completed {
			state.completed = true
			for _, itemID := range state.pendingOrder {
				item := state.pendingItems[itemID]
				if item == nil {
					continue
				}
				events = append(events, Event{Event: "response.output_item.done", Data: map[string]any{"item": cloneMap(item)}})
			}
			state.pendingItems = nil
			state.pendingInitial = nil
			state.pendingOrder = nil
			responseData := map[string]any{"id": state.responseID, "object": "response"}
			if state.pendingFinish != "" {
				responseData["finish_reason"] = state.pendingFinish
			}
			if len(state.usage) > 0 {
				responseData["usage"] = cloneMap(state.usage)
			}
			events = append(events, Event{Event: "response.completed", Data: map[string]any{"response": responseData}})
		}
	case "error":
		errMap, _ := payload["error"].(map[string]any)
		events = append(events, Event{Event: "response.incomplete", Data: map[string]any{"health_flag": "upstream_error", "message": stringValue(errMap["message"])}})
		state.completed = true
	}
	shadowRecord(events, frame, state.provider)
	return events, false, nil
}

func normalizeChatPayload(payload map[string]any, thinkingTagStyle string, xmlToolCallStyle ...string) map[string]any {
	responseID := stringValue(payload["id"])
	if responseID == "" {
		responseID = "resp_proxy"
	}
	result := map[string]any{"id": responseID, "object": "response", "status": "completed"}
	if serviceTier := stringValue(payload["service_tier"]); serviceTier != "" {
		result["service_tier"] = serviceTier
	}
	usage := normalizeChatUsage(payload)
	if len(usage) > 0 {
		result["usage"] = usage
	}
	choices, _ := payload["choices"].([]any)
	if len(choices) == 0 {
		result["output"] = []any{}
		return result
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	output := make([]any, 0, 2)
	messageID := responseID
	if messageID == "" {
		messageID = "msg_proxy"
	}
	messageItem := map[string]any{"id": messageID, "type": "message", "status": "completed", "role": "assistant"}
	content := normalizeChatMessageContent(message["content"])
	if len(xmlToolCallStyle) > 0 && xmlToolCallStyle[0] == config.UpstreamXMLToolCallStyleLegacy {
		content, output = appendLegacyXMLToolCallsFromContent(content, output)
	}
	if refusal := stringValue(message["refusal"]); refusal != "" {
		content = append(content, map[string]any{"type": "refusal", "refusal": refusal})
	}
	if len(content) > 0 {
		messageItem["content"] = content
		output = append(output, messageItem)
	}
	toolCalls, _ := message["tool_calls"].([]any)
	for i, rawTool := range toolCalls {
		tool, _ := rawTool.(map[string]any)
		function, _ := tool["function"].(map[string]any)
		callID := stringValue(tool["id"])
		if callID == "" {
			callID = fmt.Sprintf("call_%d", i)
		}
		output = append(output, map[string]any{"id": callID, "type": "function_call", "status": "completed", "call_id": callID, "name": stringValue(function["name"]), "arguments": stringValue(function["arguments"])})
	}
	var reasoningContent string
	extractState := &chatNormalizationState{thinkingTagStyle: thinkingTagStyle}
	if thinkingTagStyle == config.UpstreamThinkingTagStyleLegacy {
		extractState.implicitThinkingInitialized = true
		extractState.implicitThinkingActive = true
	}
	for _, rawItem := range content {
		if item, ok := rawItem.(map[string]any); ok && stringValue(item["type"]) == "output_text" {
			text := stringValue(item["text"])
			cleanText, extracted := extractContentAndReasoningTagsWithState(text, extractState)
			reasoningContent += extracted
			item["text"] = cleanText
		}
	}
	if extractState.pendingThinkingTag != "" && extractState.pendingThinking != "" {
		reasoningContent += extractState.pendingThinking
	}
	existingReasoning := stringValue(message["reasoning_content"])
	if existingReasoning != "" {
		// Process through extraction to strip any raw thinking tags that might be in reasoning_content
		reasoningState := &chatNormalizationState{thinkingTagStyle: thinkingTagStyle}
		if thinkingTagStyle == config.UpstreamThinkingTagStyleLegacy {
			reasoningState.implicitThinkingInitialized = true
			reasoningState.implicitThinkingActive = true
		}
		_, cleanReasoning := extractContentAndReasoningTagsWithState(existingReasoning, reasoningState)
		if cleanReasoning != "" {
			reasoningContent = cleanReasoning
		} else {
			reasoningContent = existingReasoning
		}
	}
	if reasoningContent != "" {
		result["reasoning"] = map[string]any{"summary": reasoningContent}
	}
	if finishReason := stringValue(choice["finish_reason"]); finishReason != "" {
		result["finish_reason"] = finishReason
	}
	result["output"] = output
	return result
}

func normalizeAnthropicPayload(payload map[string]any) map[string]any {
	result := map[string]any{"id": stringValue(payload["id"]), "object": "response", "status": "completed"}
	usage := normalizeAnthropicUsage(payload["usage"])
	if len(usage) > 0 {
		result["usage"] = usage
	}
	contentBlocks, _ := payload["content"].([]any)
	output := make([]any, 0, len(contentBlocks)+1)
	messageContent := make([]any, 0, len(contentBlocks))
	reasoningBlocks := make([]any, 0, len(contentBlocks))
	var reasoningSummary strings.Builder
	for i, rawBlock := range contentBlocks {
		block, _ := rawBlock.(map[string]any)
		if block == nil {
			continue
		}
		switch stringValue(block["type"]) {
		case "text":
			messageContent = append(messageContent, map[string]any{"type": "output_text", "text": stringValue(block["text"])})
		case "thinking", "redacted_thinking":
			reasoningBlocks = append(reasoningBlocks, cloneMap(block))
			text := stringValue(block["thinking"])
			if text == "" {
				text = stringValue(block["text"])
			}
			if text != "" {
				reasoningSummary.WriteString(text)
			}
		case "tool_use":
			callID := stringValue(block["id"])
			if callID == "" {
				callID = fmt.Sprintf("call_%d", i)
			}
			arguments := "{}"
			if input, _ := block["input"].(map[string]any); len(input) > 0 {
				encoded, _ := json.Marshal(input)
				arguments = string(encoded)
			}
			output = append(output, map[string]any{"id": callID, "type": "function_call", "status": "completed", "call_id": callID, "name": stringValue(block["name"]), "arguments": arguments})
		}
	}
	if len(messageContent) > 0 {
		messageID := stringValue(payload["id"])
		if messageID == "" {
			messageID = "msg_proxy"
		}
		output = append([]any{map[string]any{"id": messageID, "type": "message", "status": "completed", "role": "assistant", "content": messageContent}}, output...)
	}
	if stopReason := stringValue(payload["stop_reason"]); stopReason != "" {
		result["stop_reason"] = stopReason
	}
	if reasoningSummary.Len() > 0 || len(reasoningBlocks) > 0 {
		reasoning := map[string]any{}
		if reasoningSummary.Len() > 0 {
			reasoning["summary"] = reasoningSummary.String()
		}
		if len(reasoningBlocks) > 0 {
			reasoning["blocks"] = reasoningBlocks
		}
		result["reasoning"] = reasoning
	}
	result["output"] = output
	return result
}

func normalizeChatUsage(payload map[string]any) map[string]any {
	usage, _ := payload["usage"].(map[string]any)
	if len(usage) == 0 {
		return nil
	}
	result := map[string]any{}
	if prompt := usage["prompt_tokens"]; prompt != nil {
		result["input_tokens"] = prompt
	}
	if completion := usage["completion_tokens"]; completion != nil {
		result["output_tokens"] = completion
	}
	if total := usage["total_tokens"]; total != nil {
		result["total_tokens"] = total
	}
	if details, _ := usage["prompt_tokens_details"].(map[string]any); len(details) > 0 {
		result["input_tokens_details"] = cloneMap(details)
	}
	if details, _ := usage["completion_tokens_details"].(map[string]any); len(details) > 0 {
		result["output_tokens_details"] = cloneMap(details)
	}
	return result
}

func normalizeAnthropicUsage(raw any) map[string]any {
	usage, _ := raw.(map[string]any)
	if len(usage) == 0 {
		return nil
	}
	result := map[string]any{}
	input := numberValue(usage["input_tokens"])
	output := numberValue(usage["output_tokens"])
	cached := numberValue(usage["cache_read_input_tokens"])
	created := numberValue(usage["cache_creation_input_tokens"])
	totalInput := input + cached + created
	if totalInput != 0 {
		result["input_tokens"] = totalInput
	}
	if output != 0 {
		result["output_tokens"] = output
	}
	if totalInput != 0 || output != 0 {
		result["total_tokens"] = totalInput + output
	}
	details := map[string]any{}
	if cached := usage["cache_read_input_tokens"]; cached != nil {
		details["cached_tokens"] = cached
	}
	if created := usage["cache_creation_input_tokens"]; created != nil {
		details["cache_creation_tokens"] = created
	}
	if len(details) > 0 {
		result["input_tokens_details"] = details
	}
	return result
}

func mergeUsage(dst map[string]any, src map[string]any) {
	for k, v := range src {
		dst[k] = v
	}
	input := numberValue(dst["input_tokens"])
	output := numberValue(dst["output_tokens"])
	if input != 0 || output != 0 {
		dst["total_tokens"] = input + output
	}
}

func normalizeChatMessageContent(raw any) []any {
	if text, ok := raw.(string); ok {
		if text == "" {
			return nil
		}
		return []any{map[string]any{"type": "output_text", "text": text}}
	}
	parts, _ := raw.([]any)
	out := make([]any, 0, len(parts))
	for _, rawPart := range parts {
		part, _ := rawPart.(map[string]any)
		if part == nil {
			continue
		}
		if stringValue(part["type"]) == "text" {
			out = append(out, map[string]any{"type": "output_text", "text": stringValue(part["text"])})
		}
	}
	return out
}

func buildChatRequestBody(req model.CanonicalRequest, xmlToolCallStyle ...string) ([]byte, error) {
	if err := validateRequestForEndpoint(req, config.UpstreamEndpointTypeChat); err != nil {
		return nil, err
	}
	payload := map[string]any{"model": req.Model, "stream": req.Stream}
	for key, value := range filteredPreservedTopLevelFieldsForEndpoint(req.PreservedTopLevelFields, config.UpstreamEndpointTypeChat) {
		payload[key] = cloneJSONValue(value)
	}
	if req.IncludeUsage {
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		payload["top_p"] = *req.TopP
	}
	if !req.OmitMaxOutputTokens && req.MaxOutputTokens != nil {
		payload["max_tokens"] = *req.MaxOutputTokens
	}
	if len(req.Stop) == 1 {
		payload["stop"] = req.Stop[0]
	} else if len(req.Stop) > 1 {
		payload["stop"] = append([]string(nil), req.Stop...)
	}
	if req.Reasoning != nil {
		if len(req.Reasoning.Raw) > 0 {
			if reasoning := normalizeOpenAIReasoningPayload(req.Reasoning); len(reasoning) > 0 {
				payload["reasoning"] = reasoning
			}
		} else if req.Reasoning.Effort != "" {
			payload["reasoning_effort"] = req.Reasoning.Effort
		}
	}
	payload["messages"] = buildChatMessages(req, xmlToolCallStyle...)
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, tool := range sortedCanonicalTools(req.Tools) {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": normalizeFunctionToolJSONSchema(tool)}})
		}
		payload["tools"] = tools
	}
	if req.ToolChoice.Raw != nil {
		if value, ok := req.ToolChoice.Raw["value"]; ok {
			payload["tool_choice"] = value
		} else {
			payload["tool_choice"] = cloneMap(req.ToolChoice.Raw)
		}
	} else if req.ToolChoice.Mode != "" {
		payload["tool_choice"] = req.ToolChoice.Mode
	}
	return json.Marshal(payload)
}

func buildChatMessages(req model.CanonicalRequest, xmlToolCallStyle ...string) []any {
	messages := make([]any, 0, len(req.Messages)+1)
	if req.Instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.Instructions})
	}
	for _, msg := range req.Messages {
		if len(msg.OrderedContent) > 0 {
			messages = append(messages, buildChatOrderedMessages(msg)...)
			continue
		}
		if msg.Role == "tool" {
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": msg.ToolCallID, "content": stringifyToolOutput(buildToolOutput(msg.Parts))})
			continue
		}
		entry := map[string]any{"role": msg.Role}
		content := buildChatContentParts(msg.Parts)
		recoveredToolCalls := legacyXMLToolCallsFromChatContent(content, len(msg.ToolCalls), len(xmlToolCallStyle) > 0 && xmlToolCallStyle[0] == config.UpstreamXMLToolCallStyleLegacy)
		if len(recoveredToolCalls) > 0 {
			content = nil
		}
		if len(content) == 1 {
			if part, _ := content[0].(map[string]any); part != nil && part["type"] == "text" {
				entry["content"] = part["text"]
			} else {
				entry["content"] = content
			}
		} else if len(content) > 1 {
			entry["content"] = content
		} else if len(msg.ToolCalls) == 0 && len(recoveredToolCalls) == 0 {
			entry["content"] = ""
		}
		reasoningContent := msg.ReasoningContent
		if reasoningContent == "" {
			reasoningContent = reasoningContentFromBlocks(msg.ReasoningBlocks)
		}
		if reasoningContent != "" {
			entry["reasoning_content"] = reasoningContent
		}
		if len(msg.ToolCalls) > 0 || len(recoveredToolCalls) > 0 {
			toolCalls := make([]any, 0, len(msg.ToolCalls)+len(recoveredToolCalls))
			toolCalls = append(toolCalls, recoveredToolCalls...)
			for _, call := range msg.ToolCalls {
				if strings.TrimSpace(call.Name) == "" {
					continue
				}
				callID := call.ID
				if callID == "" {
					callID = call.Name
				}
				toolCalls = append(toolCalls, map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": call.Name, "arguments": sanitizeToolArguments(call.Arguments)}})
			}
			if len(toolCalls) > 0 {
				entry["tool_calls"] = toolCalls
			}
		}
		messages = append(messages, entry)
	}
	return messages
}

func buildChatOrderedMessages(msg model.CanonicalMessage) []any {
	var messages []any
	var content []any
	flushContent := func() {
		entry := buildChatMessageEntry(msg.Role, content)
		if entry == nil {
			return
		}
		messages = append(messages, entry)
		content = nil
	}
	for _, block := range msg.OrderedContent {
		switch block.Type {
		case "content":
			content = append(content, buildChatContentParts([]model.CanonicalContentPart{block.Part})...)
		case "tool_use":
			flushContent()
			if strings.TrimSpace(block.ToolCall.Name) == "" {
				continue
			}
			callID := block.ToolCall.ID
			if callID == "" {
				callID = block.ToolCall.Name
			}
			messages = append(messages, map[string]any{"role": msg.Role, "tool_calls": []any{map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": block.ToolCall.Name, "arguments": sanitizeToolArguments(block.ToolCall.Arguments)}}}})
		case "tool_result":
			flushContent()
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": block.ToolCallID, "content": stringifyToolOutput(buildToolOutput(block.ToolResultParts))})
		}
	}
	flushContent()
	return messages
}

func buildChatMessageEntry(role string, content []any) map[string]any {
	if len(content) == 0 {
		return nil
	}
	entry := map[string]any{"role": role}
	if len(content) == 1 {
		if part, _ := content[0].(map[string]any); part != nil && part["type"] == "text" {
			entry["content"] = part["text"]
			return entry
		}
	}
	entry["content"] = content
	return entry
}

func sanitizeToolArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return arguments
	}
	if normalized, ok := syntaxrepair.RepairJSON(trimmed); ok {
		return normalized
	}
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != '{' && trimmed[i] != '[' {
			continue
		}
		if normalized, ok := syntaxrepair.RepairJSON(trimmed[i:]); ok {
			return normalized
		}
	}
	if json.Valid([]byte(trimmed)) {
		return arguments
	}
	return arguments
}

func buildChatContentParts(parts []model.CanonicalContentPart) []any {
	content := make([]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			content = append(content, map[string]any{"type": "text", "text": part.Text})
		case "image_url", "input_image":
			image := map[string]any{"url": part.ImageURL}
			if rawImage, ok := part.Raw["image_url"].(map[string]any); ok && len(rawImage) > 0 {
				image = cloneMap(rawImage)
				if _, ok := image["url"]; !ok && part.ImageURL != "" {
					image["url"] = part.ImageURL
				}
			}
			content = append(content, map[string]any{"type": "image_url", "image_url": image})
		case "input_file":
			if rawFile, ok := part.Raw["input_file"].(map[string]any); ok && len(rawFile) > 0 {
				content = append(content, map[string]any{"type": "file", "file": cloneMap(rawFile)})
			}
		case "input_audio":
			if rawAudio, ok := part.Raw["input_audio"].(map[string]any); ok && len(rawAudio) > 0 {
				content = append(content, map[string]any{"type": "input_audio", "input_audio": cloneMap(rawAudio)})
			}
		}
	}
	return content
}

func buildAnthropicRequestBody(req model.CanonicalRequest, masqueradeTarget string, injectMetadataUserID bool, injectSystemPrompt bool, upstreamCacheControl string) ([]byte, error) {
	if err := validateAnthropicRequest(req); err != nil {
		return nil, err
	}
	payload := map[string]any{"model": req.Model, "stream": req.Stream}
	for key, value := range filteredPreservedTopLevelFieldsForEndpoint(req.PreservedTopLevelFields, config.UpstreamEndpointTypeAnthropic) {
		payload[key] = cloneJSONValue(value)
	}
	if req.OmitMaxOutputTokens {
		// 不携带 max_tokens，供 provider 级强制置空语义使用。
	} else if req.MaxOutputTokens != nil {
		payload["max_tokens"] = *req.MaxOutputTokens
	} else {
		payload["max_tokens"] = 1024
	}
	if injectSystemPrompt && masqueradeTarget == config.MasqueradeTargetClaude {
		payload["system"] = buildClaudeMasqueradeSystemParts(req)
	} else if systemParts := buildAnthropicSystemParts(req); len(systemParts) > 0 {
		payload["system"] = systemParts
	} else if system := buildAnthropicSystemPrompt(req); system != "" {
		payload["system"] = system
	}
	if req.Reasoning != nil && len(req.Reasoning.Raw) > 0 {
		if thinking, ok := req.Reasoning.Raw["thinking"]; ok {
			payload["thinking"] = thinking
		}
		if value, exists := req.Reasoning.Raw["output_config"]; exists {
			payload["output_config"] = value
		}
		if _, exists := payload["thinking"]; !exists && req.PassThroughRawReasoning {
			if reasoning := normalizeOpenAIReasoningPayload(req.Reasoning); len(reasoning) > 0 {
				payload["reasoning"] = reasoning
			} else if strings.TrimSpace(req.Reasoning.Effort) != "" {
				payload["reasoning_effort"] = req.Reasoning.Effort
			}
		}
	}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, tool := range sortedCanonicalTools(req.Tools) {
			tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": normalizeAnthropicToolInputSchema(tool)})
		}
		payload["tools"] = tools
	}
	if choice := normalizeAnthropicToolChoice(req.ToolChoice); choice != nil {
		payload["tool_choice"] = choice
	}
	payload["messages"] = buildAnthropicMessages(req)
	applyAnthropicCacheControlMode(payload, upstreamCacheControl)

	if injectMetadataUserID && masqueradeTarget == config.MasqueradeTargetClaude {
		userID, err := claudeMetadataUserID(req.ClaudeMetadata)
		if err != nil {
			return nil, err
		}
		payload["metadata"] = map[string]any{
			"user_id": userID,
		}
	}

	return json.Marshal(payload)
}

func claudeMetadataUserID(metadata *model.CanonicalClaudeMetadata) (string, error) {
	if metadata == nil {
		metadata = &model.CanonicalClaudeMetadata{
			DeviceID:    config.DefaultClaudeCodeMetadataDeviceID("root"),
			AccountUUID: config.DefaultClaudeCodeMetadataAccountUUID("root"),
			SessionID:   newUUIDString(),
		}
	}
	payload := map[string]string{
		"device_id":    strings.TrimSpace(metadata.DeviceID),
		"account_uuid": strings.TrimSpace(metadata.AccountUUID),
		"session_id":   strings.TrimSpace(metadata.SessionID),
	}
	if payload["device_id"] == "" {
		payload["device_id"] = config.DefaultClaudeCodeMetadataDeviceID("root")
	}
	if payload["account_uuid"] == "" {
		payload["account_uuid"] = config.DefaultClaudeCodeMetadataAccountUUID("root")
	}
	if payload["session_id"] == "" {
		payload["session_id"] = newUUIDString()
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func newUUIDString() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		fallback := sha256.Sum256([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
		copy(b[:], fallback[:16])
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func sortedCanonicalTools(tools []model.CanonicalTool) []model.CanonicalTool {
	if len(tools) < 2 {
		return tools
	}
	sorted := append([]model.CanonicalTool(nil), tools...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left := canonicalToolSortName(sorted[i])
		right := canonicalToolSortName(sorted[j])
		if left == right {
			return sorted[i].Type < sorted[j].Type
		}
		return left < right
	})
	return sorted
}

func canonicalToolSortName(tool model.CanonicalTool) string {
	name := strings.TrimSpace(tool.Name)
	if name != "" {
		return name
	}
	if strings.TrimSpace(tool.Type) == "web_search" {
		return "web_search"
	}
	return name
}

func applyAnthropicCacheControlMode(payload map[string]any, mode string) {
	switch strings.TrimSpace(mode) {
	case "", config.UpstreamCacheControlNoChange:
		return
	case config.UpstreamCacheControlFalse:
		walkAnthropicContentBlocks(payload, func(block map[string]any) {
			delete(block, "cache_control")
		})
	case config.UpstreamCacheControl1H:
		cacheControl := map[string]any{"type": "ephemeral", "ttl": "1h"}
		applyAnthropicCacheControlAtStableBreakpoint(payload, cacheControl)
	default:
		cacheControl := map[string]any{"type": "ephemeral"}
		applyAnthropicCacheControlAtStableBreakpoint(payload, cacheControl)
	}
}

func applyAnthropicCacheControlAtStableBreakpoint(payload map[string]any, cacheControl map[string]any) {
	if len(payload) == 0 || len(cacheControl) == 0 {
		return
	}
	var lastStableMessageBlock map[string]any
	if visitAnthropicContentBlocks(payload["system"], func(block map[string]any) bool {
		block["cache_control"] = cloneMap(cacheControl)
		return false
	}) {
		return
	}
	messages, _ := payload["messages"].([]any)
	for _, rawMessage := range messages {
		message, _ := rawMessage.(map[string]any)
		visitAnthropicContentBlocks(message["content"], func(block map[string]any) bool {
			lastStableMessageBlock = block
			return false
		})
	}
	if lastStableMessageBlock != nil {
		lastStableMessageBlock["cache_control"] = cloneMap(cacheControl)
	}
}

func visitAnthropicContentBlocks(raw any, fn func(map[string]any) bool) bool {
	if fn == nil {
		return false
	}
	content, _ := raw.([]any)
	for _, item := range content {
		block, _ := item.(map[string]any)
		if len(block) == 0 {
			continue
		}
		switch stringValue(block["type"]) {
		case "text", "image", "document", "tool_result":
			if fn(block) {
				return true
			}
		}
	}
	return false
}

func walkAnthropicContentBlocks(payload map[string]any, fn func(map[string]any)) {
	if len(payload) == 0 || fn == nil {
		return
	}
	visitContent := func(raw any) {}
	visitContent = func(raw any) {
		switch content := raw.(type) {
		case []any:
			for _, item := range content {
				block, _ := item.(map[string]any)
				if len(block) == 0 {
					continue
				}
				switch stringValue(block["type"]) {
				case "text", "image", "document", "tool_result":
					fn(block)
				}
			}
		}
	}
	visitContent(payload["system"])
	messages, _ := payload["messages"].([]any)
	for _, rawMessage := range messages {
		message, _ := rawMessage.(map[string]any)
		visitContent(message["content"])
	}
}

func validateAnthropicRequest(req model.CanonicalRequest) error {
	for _, msg := range req.Messages {
		for _, part := range msg.Parts {
			if part.Type == "input_audio" {
				return fmt.Errorf("input_audio is not supported for anthropic upstream")
			}
		}
	}
	return nil
}

func buildAnthropicMessages(req model.CanonicalRequest) []any {
	messages := make([]any, 0, len(req.Messages))
	appendPendingToolResults := func(blocks []any) []any {
		if len(blocks) == 0 {
			return nil
		}
		messages = append(messages, map[string]any{"role": "user", "content": blocks})
		return nil
	}
	var pendingToolResults []any
	emittedOrderedToolResults := map[string]struct{}{}
	for _, msg := range req.Messages {
		if isAnthropicInstructionRole(msg.Role) {
			continue
		}
		if len(msg.OrderedContent) > 0 {
			pendingToolResults = appendPendingToolResults(pendingToolResults)
			content := buildAnthropicOrderedContent(msg.OrderedContent)
			for _, block := range msg.OrderedContent {
				if block.Type == "tool_result" && block.ToolCallID != "" {
					emittedOrderedToolResults[block.ToolCallID] = struct{}{}
				}
			}
			messages = append(messages, map[string]any{"role": msg.Role, "content": content})
			continue
		}
		if msg.Role == "tool" {
			if _, exists := emittedOrderedToolResults[msg.ToolCallID]; exists {
				continue
			}
			pendingToolResults = append(pendingToolResults, map[string]any{"type": "tool_result", "tool_use_id": msg.ToolCallID, "content": buildAnthropicToolResultContent(msg.Parts)})
			continue
		}
		if len(pendingToolResults) > 0 && msg.Role == "user" && len(msg.ToolCalls) == 0 {
			content := append([]any{}, pendingToolResults...)
			content = append(content, buildAnthropicContentParts(msg.Parts)...)
			messages = append(messages, map[string]any{"role": "user", "content": content})
			pendingToolResults = nil
			continue
		}
		pendingToolResults = appendPendingToolResults(pendingToolResults)
		content := buildAnthropicContentParts(msg.Parts)
		if msg.Role == "assistant" && len(msg.ReasoningBlocks) > 0 {
			content = append(cloneAnySliceOfMaps(msg.ReasoningBlocks), content...)
		} else if msg.Role == "assistant" && msg.ReasoningContent != "" && !isSyntheticProxyReasoningContent(msg.ReasoningContent) {
			content = append([]any{map[string]any{"type": "thinking", "thinking": msg.ReasoningContent}}, content...)
		}
		for _, call := range msg.ToolCalls {
			if strings.TrimSpace(call.Name) == "" {
				continue
			}
			callID := call.ID
			if callID == "" {
				callID = call.Name
			}
			content = append(content, map[string]any{"type": "tool_use", "id": callID, "name": call.Name, "input": parseJSONArguments(call.Arguments)})
		}
		if len(content) == 0 {
			continue
		}
		messages = append(messages, map[string]any{"role": msg.Role, "content": content})
	}
	pendingToolResults = appendPendingToolResults(pendingToolResults)
	return messages
}

func isSyntheticProxyReasoningContent(content string) bool {
	return strings.Contains(strings.TrimSpace(content), "代理层占位")
}

func buildAnthropicOrderedContent(blocks []model.CanonicalContentBlock) []any {
	content := make([]any, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "content":
			content = append(content, buildAnthropicContentParts([]model.CanonicalContentPart{block.Part})...)
		case "thinking", "redacted_thinking":
			if len(block.Raw) > 0 {
				content = append(content, cloneMap(block.Raw))
			}
		case "tool_use":
			call := block.ToolCall
			if strings.TrimSpace(call.Name) == "" {
				continue
			}
			callID := call.ID
			if callID == "" {
				callID = call.Name
			}
			content = append(content, map[string]any{"type": "tool_use", "id": callID, "name": call.Name, "input": parseJSONArguments(call.Arguments)})
		case "tool_result":
			result := map[string]any{"type": "tool_result", "tool_use_id": block.ToolCallID, "content": buildAnthropicToolResultContent(block.ToolResultParts)}
			attachAnthropicCacheControlBlock(result, block.Raw)
			content = append(content, result)
		}
	}
	return content
}

func reasoningContentFromBlocks(blocks []map[string]any) string {
	if len(blocks) == 0 {
		return ""
	}
	var builder strings.Builder
	for _, block := range blocks {
		typeName, _ := block["type"].(string)
		if typeName == "redacted_thinking" {
			continue
		}
		text := stringValue(block["thinking"])
		if text == "" {
			text = stringValue(block["text"])
		}
		if text != "" {
			builder.WriteString(text)
		}
	}
	return builder.String()
}

func cloneAnySliceOfMaps(blocks []map[string]any) []any {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]any, 0, len(blocks))
	for _, block := range blocks {
		if len(block) == 0 {
			continue
		}
		normalized := normalizeAnthropicReasoningBlock(block)
		if len(normalized) == 0 {
			continue
		}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeAnthropicReasoningBlock(block map[string]any) map[string]any {
	cloned := cloneMap(block)
	if len(cloned) == 0 {
		return cloned
	}
	if stringValue(cloned["type"]) != "reasoning" {
		return cloned
	}
	signature := stringValue(cloned["encrypted_content"])
	if signature == "" {
		return nil
	}
	text := reasoningTextFromResponsesBlock(cloned)
	if text == "" {
		return nil
	}
	normalized := map[string]any{"type": "thinking"}
	normalized["thinking"] = text
	normalized["signature"] = signature
	return normalized
}

func reasoningTextFromResponsesBlock(block map[string]any) string {
	if text := stringValue(block["thinking"]); text != "" {
		return text
	}
	if text := stringValue(block["text"]); text != "" {
		return text
	}
	var rawSummary []any
	switch typed := block["summary"].(type) {
	case []any:
		rawSummary = typed
	case []map[string]any:
		for _, item := range typed {
			rawSummary = append(rawSummary, item)
		}
	}
	var builder strings.Builder
	for _, raw := range rawSummary {
		item, _ := raw.(map[string]any)
		if len(item) == 0 {
			continue
		}
		if stringValue(item["type"]) == "summary_text" {
			if text := stringValue(item["text"]); text != "" {
				builder.WriteString(text)
				continue
			}
		}
		if text := stringValue(item["text"]); text != "" {
			builder.WriteString(text)
			continue
		}
		if text := stringValue(item["summary_text"]); text != "" {
			builder.WriteString(text)
			continue
		}
		if nested, _ := item["summary_text"].(map[string]any); len(nested) > 0 {
			if text := stringValue(nested["text"]); text != "" {
				builder.WriteString(text)
			}
		}
	}
	return builder.String()
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

func buildAnthropicSystemPrompt(req model.CanonicalRequest) string {
	parts := make([]string, 0, len(req.Messages)+1)
	if req.Instructions != "" {
		parts = append(parts, req.Instructions)
	}
	for _, msg := range req.Messages {
		if !isAnthropicInstructionRole(msg.Role) {
			continue
		}
		if text := joinAnthropicInstructionParts(msg.Parts); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func buildAnthropicSystemParts(req model.CanonicalRequest) []any {
	if len(req.InstructionParts) == 0 {
		return nil
	}
	content := buildAnthropicContentParts(req.InstructionParts)
	for _, msg := range req.Messages {
		if !isAnthropicInstructionRole(msg.Role) {
			continue
		}
		content = append(content, buildAnthropicContentParts(msg.Parts)...)
	}
	return content
}

func buildClaudeMasqueradeSystemParts(req model.CanonicalRequest) []any {
	content := []any{map[string]any{"type": "text", "text": claudeCodeSystemPrompt}, map[string]any{"type": "text", "text": claudeCodeBillingSystemMarker}}
	if systemParts := buildAnthropicSystemParts(req); len(systemParts) > 0 {
		return append(content, systemParts...)
	}
	if system := buildAnthropicSystemPrompt(req); system != "" {
		return append(content, map[string]any{"type": "text", "text": system})
	}
	return content
}

func joinAnthropicInstructionParts(parts []model.CanonicalContentPart) string {
	var builder strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

func isAnthropicInstructionRole(role string) bool {
	return role == "system" || role == "developer"
}

func buildAnthropicContentParts(parts []model.CanonicalContentPart) []any {
	content := make([]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			block := map[string]any{"type": "text", "text": part.Text}
			attachAnthropicCacheControlBlock(block, part.Raw)
			content = append(content, block)
		case "image_url", "input_image":
			if block := buildAnthropicImageBlock(part); block != nil {
				attachAnthropicCacheControlBlock(block, part.Raw)
				content = append(content, block)
			}
		case "input_file":
			if block := buildAnthropicDocumentBlock(part); block != nil {
				attachAnthropicCacheControlBlock(block, part.Raw)
				content = append(content, block)
			}
		}
	}
	return content
}

func attachAnthropicCacheControlBlock(block map[string]any, raw map[string]any) {
	if len(block) == 0 || len(raw) == 0 {
		return
	}
	cacheControl, _ := raw["cache_control"].(map[string]any)
	if len(cacheControl) == 0 {
		return
	}
	block["cache_control"] = cloneMap(cacheControl)
}

func buildAnthropicToolResultContent(parts []model.CanonicalContentPart) any {
	if len(parts) == 1 && parts[0].Type == "text" {
		return parts[0].Text
	}
	return buildAnthropicContentParts(parts)
}

func buildAnthropicImageBlock(part model.CanonicalContentPart) map[string]any {
	if rawImage, ok := part.Raw["image_url"].(map[string]any); ok && len(rawImage) > 0 {
		if fileID := stringValue(rawImage["file_id"]); fileID != "" {
			return map[string]any{"type": "image", "source": map[string]any{"type": "file", "file_id": fileID}}
		}
		if url := stringValue(rawImage["url"]); strings.HasPrefix(url, "data:") {
			if mediaType, data, ok := splitDataURL(url); ok {
				return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": data}}
			}
		}
		if url := stringValue(rawImage["url"]); url != "" {
			return map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": url}}
		}
	}
	if strings.HasPrefix(part.ImageURL, "data:") {
		if mediaType, data, ok := splitDataURL(part.ImageURL); ok {
			return map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": data}}
		}
	}
	if part.ImageURL != "" {
		return map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": part.ImageURL}}
	}
	return nil
}

func buildAnthropicDocumentBlock(part model.CanonicalContentPart) map[string]any {
	rawFile, _ := part.Raw["input_file"].(map[string]any)
	if len(rawFile) == 0 {
		return nil
	}
	if fileID := stringValue(rawFile["file_id"]); fileID != "" {
		return map[string]any{"type": "document", "source": map[string]any{"type": "file", "file_id": fileID}}
	}
	if fileURL := stringValue(rawFile["file_url"]); fileURL != "" {
		return map[string]any{"type": "document", "source": map[string]any{"type": "url", "url": fileURL}}
	}
	if fileData := stringValue(rawFile["file_data"]); fileData != "" {
		if mediaType, data, ok := splitDataURL(fileData); ok {
			return map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": data}}
		}
	}
	return nil
}

func splitDataURL(raw string) (string, string, bool) {
	if !strings.HasPrefix(raw, "data:") {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(raw, "data:"), ",", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	meta := parts[0]
	data := parts[1]
	mediaType := strings.TrimSuffix(meta, ";base64")
	if mediaType == meta {
		return "", "", false
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return "", "", false
	}
	return mediaType, data, true
}

func parseJSONArguments(arguments string) any {
	if arguments == "" {
		return map[string]any{}
	}
	decoded, _, ok := syntaxrepair.ParseJSONValue(arguments)
	if !ok {
		return map[string]any{"raw": arguments}
	}
	return decoded
}

func normalizeAnthropicToolChoice(choice model.CanonicalToolChoice) any {
	if choice.Raw != nil {
		if value, ok := choice.Raw["value"].(string); ok && value != "" {
			return map[string]any{"type": value}
		}
		if value, ok := choice.Raw["value"].(map[string]any); ok {
			choice.Raw = value
		}
		return normalizeAnthropicToolChoiceMap(choice.Raw)
	}
	if choice.Mode != "" {
		return map[string]any{"type": choice.Mode}
	}
	return nil
}

func normalizeAnthropicToolChoiceMap(raw map[string]any) map[string]any {
	choice := cloneMap(raw)
	if stringValue(choice["type"]) == "function" {
		choice["type"] = "tool"
	}
	return choice
}

func normalizeAnthropicToolInputSchema(tool model.CanonicalTool) any {
	schema := normalizeFunctionToolJSONSchema(tool)
	if mapped, ok := schema.(map[string]any); ok && len(mapped) > 0 {
		return mapped
	}
	switch strings.TrimSpace(tool.Type) {
	case "web_search", "web_search_preview":
		return cloneJSONValue(responsesWebSearchFunctionToolSchema)
	default:
		return schema
	}
}

func stringifyToolOutput(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

func numberValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func extractContentAndReasoningTags(text string) (cleanText string, reasoningContent string) {
	cleanText = text

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

func extractContentAndReasoningTagsWithState(text string, state *chatNormalizationState) (cleanText, reasoningContent string) {
	cleanText = text
	if state == nil || state.thinkingTagStyle == config.UpstreamThinkingTagStyleOff {
		return cleanText, ""
	}

	if state.implicitThinkingActive {
		cleanText, reasoningContent = extractImplicitReasoningUntilClose(cleanText, state)
		if cleanText == "" || state.implicitThinkingActive {
			return cleanText, reasoningContent
		}
	}

	extraText, extraReasoning, pendingTag, pendingThinking := extractExplicitReasoningTagsWithState(cleanText, state.pendingThinkingTag, state.pendingThinking)
	state.pendingThinkingTag = pendingTag
	state.pendingThinking = pendingThinking
	cleanText = extraText
	reasoningContent += extraReasoning
	return cleanText, reasoningContent
}

func extractImplicitReasoningUntilClose(text string, state *chatNormalizationState) (cleanText, reasoningContent string) {
	cleanText = text
	if state == nil {
		return cleanText, ""
	}

	if state.pendingThinkingTag == "" {
		switch {
		case strings.HasPrefix(cleanText, "<think>"):
			state.pendingThinkingTag = "<think>"
			cleanText = cleanText[len("<think>"):]
		case strings.HasPrefix(cleanText, "<thinking>"):
			state.pendingThinkingTag = "<thinking>"
			cleanText = cleanText[len("<thinking>"):]
		case strings.HasPrefix(cleanText, "<reasoning>"):
			state.pendingThinkingTag = "<reasoning>"
			cleanText = cleanText[len("<reasoning>"):]
		}
	}

	closeTag := implicitThinkingCloseTag(state.pendingThinkingTag)
	closeIdx := indexOfImplicitCloseTag(cleanText, closeTag)
	if closeIdx == -1 {
		emitted, holdback := splitStreamingReasoningChunkAnyClose(state.pendingThinking+cleanText, implicitHoldbackTags(closeTag)...)
		state.pendingThinking = holdback
		return "", emitted
	}

	reasoningContent = state.pendingThinking + cleanText[:closeIdx]
	cleanText = cleanText[closeIdx+len(closeTag):]
	state.pendingThinkingTag = ""
	state.pendingThinking = ""
	state.implicitThinkingActive = false
	return cleanText, reasoningContent
}

func implicitThinkingCloseTag(openTag string) string {
	switch openTag {
	case "<thinking>":
		return "</thinking>"
	case "<reasoning>":
		return "</reasoning>"
	default:
		return "</think>"
	}
}

func implicitHoldbackTags(closeTag string) []string {
	if closeTag == "</thinking>" {
		return []string{"</thinking>"}
	}
	if closeTag == "</reasoning>" {
		return []string{"</reasoning>"}
	}
	return []string{"</think>", "</thinking>", "</reasoning>"}
}

func indexOfImplicitCloseTag(text string, preferredCloseTag string) int {
	if preferredCloseTag != "" {
		return strings.Index(text, preferredCloseTag)
	}
	thinkIdx := strings.Index(text, "</think>")
	thinkingIdx := strings.Index(text, "</thinking>")
	if thinkIdx == -1 {
		return thinkingIdx
	}
	if thinkingIdx == -1 || thinkIdx < thinkingIdx {
		return thinkIdx
	}
	return thinkingIdx
}

func extractExplicitReasoningTagsWithState(text, pendingTag, pendingThinking string) (cleanText, reasoningContent, newPendingTag, newPendingThinking string) {
	cleanText = text
	newPendingTag = pendingTag
	newPendingThinking = pendingThinking

	if pendingTag != "" {
		var closeTag string
		if pendingTag == "<think>" {
			closeTag = "</think>"
		} else if pendingTag == "<reasoning>" {
			closeTag = "</reasoning>"
		} else {
			closeTag = "</thinking>"
		}
		closeIdx := strings.Index(cleanText, closeTag)
		if closeIdx == -1 {
			emitted, holdback := splitStreamingReasoningChunkAnyClose(newPendingThinking+cleanText, closeTag)
			reasoningContent += emitted
			newPendingThinking = holdback
			cleanText = ""
			return
		}
		reasoningContent += newPendingThinking + cleanText[:closeIdx]
		cleanText = cleanText[closeIdx+len(closeTag):]
		newPendingTag = ""
		newPendingThinking = ""
	}

	const thinkTagOpen = "<think>"
	const thinkTagClose = "</think>"
	const thinTagOpen = "<thinking>"
	const thinTagClose = "</thinking>"
	const reasoningTagOpen = "<reasoning>"
	const reasoningTagClose = "</reasoning>"

	for {
		var openTag, closeTag string
		openIdx := -1
		for _, candidate := range []struct{ open, close string }{
			{open: thinkTagOpen, close: thinkTagClose},
			{open: thinTagOpen, close: thinTagClose},
			{open: reasoningTagOpen, close: reasoningTagClose},
		} {
			idx := strings.Index(cleanText, candidate.open)
			if idx == -1 {
				continue
			}
			if openIdx == -1 || idx < openIdx {
				openIdx = idx
				openTag = candidate.open
				closeTag = candidate.close
			}
		}
		if openIdx == -1 {
			break
		}

		if openIdx > 0 && strings.TrimSpace(cleanText[:openIdx]) != "" {
			break
		}

		closeIdx := strings.Index(cleanText[openIdx+len(openTag):], closeTag)
		if closeIdx == -1 {
			newPendingTag = openTag
			emitted, holdback := splitStreamingReasoningChunkAnyClose(cleanText[openIdx+len(openTag):], closeTag)
			reasoningContent += emitted
			newPendingThinking = holdback
			cleanText = cleanText[:openIdx]
			break
		}
		closeIdx += openIdx + len(openTag)
		reasoningContent += cleanText[openIdx+len(openTag) : closeIdx]
		cleanText = cleanText[:openIdx] + cleanText[closeIdx+len(closeTag):]
	}

	return cleanText, reasoningContent, newPendingTag, newPendingThinking
}

func splitStreamingReasoningChunk(text, closeTag string) (emitted string, holdback string) {
	return splitStreamingReasoningChunkAnyClose(text, closeTag)
}

func splitStreamingReasoningChunkAnyClose(text string, closeTags ...string) (emitted string, holdback string) {
	if text == "" || len(closeTags) == 0 {
		return text, ""
	}
	maxHold := 0
	for _, closeTag := range closeTags {
		if closeTag == "" {
			continue
		}
		if n := len(closeTag) - 1; n > maxHold {
			maxHold = n
		}
	}
	if maxHold <= 0 {
		return text, ""
	}
	if len(text) < maxHold {
		maxHold = len(text)
	}
	holdLen := 0
	for n := 1; n <= maxHold; n++ {
		suffix := text[len(text)-n:]
		for _, closeTag := range closeTags {
			if closeTag != "" && strings.HasPrefix(closeTag, suffix) {
				holdLen = n
				break
			}
		}
	}
	if holdLen == 0 {
		return text, ""
	}
	return text[:len(text)-holdLen], text[len(text)-holdLen:]
}

func suppressWhitespaceOnlyTextAfterThinkExtraction(cleanText string, suppressBlankTextAfterThink bool) string {
	if !suppressBlankTextAfterThink {
		return cleanText
	}
	if strings.TrimSpace(cleanText) == "" {
		return ""
	}
	return cleanText
}
