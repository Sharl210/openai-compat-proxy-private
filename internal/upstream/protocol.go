package upstream

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/syntaxrepair"
)

const (
	// opencode 伪装：来自 @ai-sdk/provider-utils 的真实 User-Agent 格式
	// 格式：opencode/{version} ai-sdk/provider-utils/{version} runtime/{runtime}/{version}
	// 验证来源：issue #8444 (anomalyco/opencode), issue #12799/PR #12800 (vercel/ai)
	opencodeUserAgent  = "opencode/1.3.7 ai-sdk/provider-utils/4.0.21 runtime/bun/1.3.11"
	opencodeOriginator = "opencode"

	// claude 伪装：必须用 claude-cli/ 格式才能通过 sub2api 的 isClaudeCodeClient 检测
	// sub2api 的检测 regex：^claude-cli/\d+\.\d+\.\d+（需同时有 metadata.user_id）
	// 真实 Claude Code CLI 发的是 claude-code/（不匹配），但 sub2api 接受 claude-cli/ 作为有效标识
	// 来源：sub2api gateway_service.go 的 claudeCliUserAgentRe + DefaultHeaders (constants.go)
	claudeCodeUserAgent = "claude-cli/2.1.22 (external, cli)"
	claudeCodeXApp      = "cli"
	// beta header：与 sub2api 的 DefaultBetaHeader 对齐
	claudeCodeBeta         = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14"
	claudeCodeSystemPrompt = "You are Claude Code, Anthropic's official CLI for Claude."

	// codex 伪装：来自 codex-rs/login/src/auth/default_client.rs 的 get_codex_user_agent() 与 default_headers()
	// 格式：codex_cli_rs/{version} ({OS_TYPE} {OS_VERSION}; {ARCHITECTURE}) {TERMINAL_INFO}
	// 示例：codex_cli_rs/0.117.0 (Linux 6.1; x86_64) iTerm.app
	codexUserAgent = "codex_cli_rs/0.117.0 (Linux 6.1; x86_64) iTerm.app"
)

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

func applyUpstreamHeaders(httpReq *http.Request, endpointType string, authorization string, anthropicVersion string, userAgent string, masqueradeTarget string) {
	if httpReq == nil {
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	switch masqueradeTarget {
	case config.MasqueradeTargetOpenCode:
		httpReq.Header.Set("User-Agent", opencodeUserAgent)
		httpReq.Header.Set("originator", opencodeOriginator)
	case config.MasqueradeTargetClaude:
		httpReq.Header.Set("User-Agent", claudeCodeUserAgent)
		httpReq.Header.Set("X-App", claudeCodeXApp)
		httpReq.Header.Set("anthropic-beta", claudeCodeBeta)
		httpReq.Header.Set("X-Stainless-Lang", "js")
		httpReq.Header.Set("X-Stainless-Package-Version", "0.75.0")
		httpReq.Header.Set("X-Stainless-OS", "Linux")
		httpReq.Header.Set("X-Stainless-Arch", "arm64")
		httpReq.Header.Set("X-Stainless-Runtime", "node")
		httpReq.Header.Set("X-Stainless-Runtime-Version", "v24.3.0")
		httpReq.Header.Set("X-Stainless-Timeout", "600")
		httpReq.Header.Set("X-Stainless-Retry-Count", "0")
		httpReq.Header.Set("Accept", "application/json")
		httpReq.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
		// 注意：Anthropic-Dangerous-Direct-Browser-Access 在 HTTP/2 时不发送
	case config.MasqueradeTargetCodex:
		httpReq.Header.Set("User-Agent", codexUserAgent)
		httpReq.Header.Set("originator", "codex_cli_rs")
		httpReq.Header.Set("x-openai-internal-codex-residency", "us")
	case config.MasqueradeTargetNone:
		// no-op：不注入任何伪装 header
	}
	if userAgent != "" {
		httpReq.Header.Set("User-Agent", userAgent)
	}
	if normalizeEndpointType(endpointType) == config.UpstreamEndpointTypeAnthropic {
		version := strings.TrimSpace(anthropicVersion)
		if version == "" {
			version = "2023-06-01"
		}
		httpReq.Header.Set("anthropic-version", version)
		if apiKey := upstreamAPIKeyFromAuthorization(authorization); apiKey != "" {
			httpReq.Header.Set("x-api-key", apiKey)
		}
		return
	}
	if authorization != "" {
		httpReq.Header.Set("Authorization", authorization)
	}
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

func buildRequestBodyForEndpoint(req model.CanonicalRequest, endpointType string, masqueradeTarget string, injectMetadataUserID bool, injectSystemPrompt bool) ([]byte, error) {
	switch normalizeEndpointType(endpointType) {
	case config.UpstreamEndpointTypeChat:
		return buildChatRequestBody(req)
	case config.UpstreamEndpointTypeAnthropic:
		return buildAnthropicRequestBody(req, masqueradeTarget, injectMetadataUserID, injectSystemPrompt)
	default:
		return buildRequestBody(req)
	}
}

func buildStreamingRequestBody(req model.CanonicalRequest, endpointType string, masqueradeTarget string, injectMetadataUserID bool, injectSystemPrompt bool) ([]byte, error) {
	req.Stream = true
	return buildRequestBodyForEndpoint(req, endpointType, masqueradeTarget, injectMetadataUserID, injectSystemPrompt)
}

func normalizeResponsePayload(endpointType string, payload map[string]any, thinkingTagStyle string) map[string]any {
	switch normalizeEndpointType(endpointType) {
	case config.UpstreamEndpointTypeChat:
		return normalizeChatPayload(payload, thinkingTagStyle)
	case config.UpstreamEndpointTypeAnthropic:
		return normalizeAnthropicPayload(payload)
	default:
		return payload
	}
}

func eventBatchReaderForType(endpointType string, thinkingTagStyle string, originalToolIDs map[int]string, requestID string) func(*bufio.Scanner) ([]Event, error) {
	switch normalizeEndpointType(endpointType) {
	case config.UpstreamEndpointTypeChat:
		return newChatEventBatchReader(thinkingTagStyle, originalToolIDs, requestID)
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

func newChatEventBatchReader(thinkingTagStyle string, originalToolIDs map[int]string, requestID string) func(*bufio.Scanner) ([]Event, error) {
	state := &chatNormalizationState{
		toolIDsByIndex:   map[int]string{},
		toolSent:         map[string]bool{},
		pendingItems:     map[string]map[string]any{},
		thinkingTagStyle: thinkingTagStyle,
		originalToolIDs:  originalToolIDs,
		requestID:        requestID,
		provider:         "chat",
	}
	return func(scanner *bufio.Scanner) ([]Event, error) {
		for {
			frame, err := readNextSSEFrame(scanner)
			if err != nil {
				return nil, err
			}
			if frame == nil {
				if events := finalizeChatEventsOnEOF(state); len(events) > 0 {
					return events, nil
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

func finalizeChatEventsOnEOF(state *chatNormalizationState) []Event {
	if state == nil || state.completed || !state.createdSent {
		return nil
	}
	var events []Event
	if state.pendingItems != nil {
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
	events = append(events, Event{Event: "response.completed", Data: map[string]any{"response": responseData}})
	state.completed = true
	state.pendingFinish = ""
	return events
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
	thinkingTagStyle            string
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
		return finalizeChatEventsOnEOF(state), true, nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
		return nil, false, fmt.Errorf("parse chat sse frame: %w", err)
	}
	var events []Event
	if usage := normalizeChatUsage(payload); len(usage) > 0 {
		state.usage = usage
		if state.pendingFinish != "" && !state.completed {
			state.completed = true
			responseData := map[string]any{"id": state.responseID, "object": "response", "finish_reason": state.pendingFinish, "usage": cloneMap(state.usage)}
			events = append(events, Event{Event: "response.completed", Data: map[string]any{"response": responseData}})
			state.pendingFinish = ""
		}
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
	if len(state.usage) > 0 && state.pendingFinish == "" && !state.completed && finishReasonInChoices != "" {
		state.completed = true
		responseData := map[string]any{"id": state.responseID, "object": "response", "finish_reason": finishReasonInChoices, "usage": cloneMap(state.usage)}
		events = append(events, Event{Event: "response.completed", Data: map[string]any{"response": responseData}})
	}
	for _, rawChoice := range choices {
		choice, _ := rawChoice.(map[string]any)
		if choice == nil {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta != nil {
			if text, _ := delta["content"].(string); text != "" {
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
			if len(state.usage) > 0 {
				state.completed = true
				if state.pendingItems != nil {
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
				responseData := map[string]any{"id": state.responseID, "object": "response", "finish_reason": finishReason, "usage": cloneMap(state.usage)}
				events = append(events, Event{Event: "response.completed", Data: map[string]any{"response": responseData}})
			} else {
				state.pendingFinish = finishReason
			}
		}
	}
	shadowRecord(events, frame, state.provider)
	return events, false, nil
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
	responseID      string
	originalToolIDs map[int]string
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
			events = append(events, Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"type": "function_call", "id": itemID, "call_id": itemID, "name": stringValue(block["name"])}}})
			if input, _ := block["input"].(map[string]any); len(input) > 0 {
				encoded, _ := json.Marshal(input)
				events = append(events, Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": itemID, "delta": string(encoded)}})
			}
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
				events = append(events, Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": text}})
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
				events = append(events, Event{Event: "response.function_call_arguments.delta", Data: map[string]any{"item_id": itemID, "delta": partial}})
			}
		}
	case "message_delta":
		mergeUsage(state.usage, normalizeAnthropicUsage(payload["usage"]))
		delta, _ := payload["delta"].(map[string]any)
		if stopReason := stringValue(delta["stop_reason"]); stopReason != "" && !state.completed {
			state.completed = true
			responseData := map[string]any{"id": state.responseID, "object": "response", "finish_reason": stopReason}
			if len(state.usage) > 0 {
				responseData["usage"] = cloneMap(state.usage)
			}
			events = append(events, Event{Event: "response.completed", Data: map[string]any{"response": responseData}})
		}
	case "message_stop":
		if !state.completed {
			state.completed = true
			responseData := map[string]any{"id": state.responseID, "object": "response"}
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

func normalizeChatPayload(payload map[string]any, thinkingTagStyle string) map[string]any {
	responseID := stringValue(payload["id"])
	if responseID == "" {
		responseID = "resp_proxy"
	}
	result := map[string]any{"id": responseID, "object": "response", "status": "completed"}
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
	for i, rawBlock := range contentBlocks {
		block, _ := rawBlock.(map[string]any)
		if block == nil {
			continue
		}
		switch stringValue(block["type"]) {
		case "text":
			messageContent = append(messageContent, map[string]any{"type": "output_text", "text": stringValue(block["text"])})
		case "thinking", "redacted_thinking":
			text := stringValue(block["thinking"])
			if text == "" {
				text = stringValue(block["text"])
			}
			if text != "" {
				result["reasoning"] = map[string]any{"summary": text}
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
	if input != 0 {
		result["input_tokens"] = input
	}
	if output != 0 {
		result["output_tokens"] = output
	}
	if input != 0 || output != 0 {
		result["total_tokens"] = input + output
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

func buildChatRequestBody(req model.CanonicalRequest) ([]byte, error) {
	payload := map[string]any{"model": req.Model, "stream": req.Stream}
	for key, value := range req.PreservedTopLevelFields {
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
	if req.MaxOutputTokens != nil {
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
	payload["messages"] = buildChatMessages(req)
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": tool.Name, "description": tool.Description, "parameters": normalizeJSONSchema(tool.Parameters)}})
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

func buildChatMessages(req model.CanonicalRequest) []any {
	messages := make([]any, 0, len(req.Messages)+1)
	if req.Instructions != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.Instructions})
	}
	for _, msg := range req.Messages {
		if msg.Role == "tool" {
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": msg.ToolCallID, "content": stringifyToolOutput(buildToolOutput(msg.Parts))})
			continue
		}
		entry := map[string]any{"role": msg.Role}
		content := buildChatContentParts(msg.Parts)
		if len(content) == 1 {
			if part, _ := content[0].(map[string]any); part != nil && part["type"] == "text" {
				entry["content"] = part["text"]
			} else {
				entry["content"] = content
			}
		} else if len(content) > 1 {
			entry["content"] = content
		} else if len(msg.ToolCalls) == 0 {
			entry["content"] = ""
		}
		if msg.ReasoningContent != "" {
			entry["reasoning_content"] = msg.ReasoningContent
		}
		if len(msg.ToolCalls) > 0 {
			toolCalls := make([]any, 0, len(msg.ToolCalls))
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

func buildAnthropicRequestBody(req model.CanonicalRequest, masqueradeTarget string, injectMetadataUserID bool, injectSystemPrompt bool) ([]byte, error) {
	if err := validateAnthropicRequest(req); err != nil {
		return nil, err
	}
	payload := map[string]any{"model": req.Model, "stream": req.Stream}
	for key, value := range req.PreservedTopLevelFields {
		payload[key] = cloneJSONValue(value)
	}
	if req.MaxOutputTokens != nil {
		payload["max_tokens"] = *req.MaxOutputTokens
	} else {
		payload["max_tokens"] = 1024
	}
	if injectSystemPrompt && masqueradeTarget == config.MasqueradeTargetClaude {
		payload["system"] = claudeCodeSystemPrompt
	} else if system := buildAnthropicSystemPrompt(req); system != "" {
		payload["system"] = system
	}
	if req.Reasoning != nil && len(req.Reasoning.Raw) > 0 {
		if thinking, ok := req.Reasoning.Raw["thinking"]; ok {
			payload["thinking"] = thinking
			for _, key := range []string{"output_config"} {
				if value, exists := req.Reasoning.Raw[key]; exists {
					payload[key] = value
				}
			}
		} else {
			for k, v := range req.Reasoning.Raw {
				payload[k] = v
			}
		}
	}
	if len(req.Tools) > 0 {
		tools := make([]any, 0, len(req.Tools))
		for _, tool := range req.Tools {
			tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": normalizeJSONSchema(tool.Parameters)})
		}
		payload["tools"] = tools
	}
	if choice := normalizeAnthropicToolChoice(req.ToolChoice); choice != nil {
		payload["tool_choice"] = choice
	}
	payload["messages"] = buildAnthropicMessages(req)

	if injectMetadataUserID && masqueradeTarget == config.MasqueradeTargetClaude {
		payload["metadata"] = map[string]any{
			"user_id": "user_" + strings.Repeat("deadbeef", 8) + "_account__session_" + "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdead",
		}
	}

	return json.Marshal(payload)
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
	for _, msg := range req.Messages {
		if isAnthropicInstructionRole(msg.Role) {
			continue
		}
		if msg.Role == "tool" {
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
		if msg.Role == "assistant" && msg.ReasoningContent != "" {
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
		messages = append(messages, map[string]any{"role": msg.Role, "content": content})
	}
	pendingToolResults = appendPendingToolResults(pendingToolResults)
	return messages
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
		return cloneMap(choice.Raw)
	}
	if choice.Mode != "" {
		return map[string]any{"type": choice.Mode}
	}
	return nil
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
