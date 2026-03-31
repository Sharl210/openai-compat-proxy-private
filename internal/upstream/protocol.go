package upstream

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"openai-compat-proxy/internal/config"
	"openai-compat-proxy/internal/model"
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

func normalizeResponsePayload(endpointType string, payload map[string]any) map[string]any {
	switch normalizeEndpointType(endpointType) {
	case config.UpstreamEndpointTypeChat:
		return normalizeChatPayload(payload)
	case config.UpstreamEndpointTypeAnthropic:
		return normalizeAnthropicPayload(payload)
	default:
		return payload
	}
}

func eventBatchReaderForType(endpointType string) func(*bufio.Scanner) ([]Event, error) {
	switch normalizeEndpointType(endpointType) {
	case config.UpstreamEndpointTypeChat:
		return newChatEventBatchReader()
	case config.UpstreamEndpointTypeAnthropic:
		return newAnthropicEventBatchReader()
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
	return []Event{*evt}, nil
}

func consumeSSEScannerWithReader(scanner *bufio.Scanner, readNext func(*bufio.Scanner) ([]Event, error), onEvent func(Event) error) error {
	if readNext == nil {
		readNext = readNextResponsesEventBatch
	}
	for {
		events, err := readNext(scanner)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		for _, evt := range events {
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
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
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

func newChatEventBatchReader() func(*bufio.Scanner) ([]Event, error) {
	state := &chatNormalizationState{toolIDsByIndex: map[int]string{}, toolSent: map[string]bool{}, pendingItems: map[string]map[string]any{}}
	return func(scanner *bufio.Scanner) ([]Event, error) {
		for {
			frame, err := readNextSSEFrame(scanner)
			if err != nil {
				return nil, err
			}
			if frame == nil {
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

type chatNormalizationState struct {
	toolIDsByIndex     map[int]string
	toolSent           map[string]bool
	usage              map[string]any
	createdSent        bool
	completed          bool
	pendingFinish      string
	pendingItems       map[string]map[string]any
	pendingThinkingTag string // incomplete thinking tag waiting for close tag (e.g., "<think>" or "<thinking>")
	pendingThinking    string // accumulated thinking content for the current pending tag
}

func normalizeChatFrame(frame *sseFrame, state *chatNormalizationState) ([]Event, bool, error) {
	if frame == nil {
		return nil, true, nil
	}
	if strings.TrimSpace(frame.Data) == "[DONE]" {
		return nil, true, nil
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
			data := map[string]any{"finish_reason": state.pendingFinish}
			data["usage"] = cloneMap(state.usage)
			events = append(events, Event{Event: "response.completed", Data: data})
			state.pendingFinish = ""
		}
	}
	if !state.createdSent {
		if responseID := stringValue(payload["id"]); responseID != "" {
			events = append(events, Event{Event: "response.created", Data: map[string]any{"response": map[string]any{"id": responseID}}})
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
	// If we have usage but no pendingFinish, and there's a finish_reason in choices coming,
	// emit response.completed now (usage already arrived before finish_reason)
	if len(state.usage) > 0 && state.pendingFinish == "" && !state.completed && finishReasonInChoices != "" {
		state.completed = true
		data := map[string]any{"finish_reason": finishReasonInChoices, "usage": cloneMap(state.usage)}
		events = append(events, Event{Event: "response.completed", Data: data})
	}
	for _, rawChoice := range choices {
		choice, _ := rawChoice.(map[string]any)
		if choice == nil {
			continue
		}
		delta, _ := choice["delta"].(map[string]any)
		if delta != nil {
			if text, _ := delta["content"].(string); text != "" {
				cleanText, reasoningContent, pendingTag, pendingContent := extractContentAndReasoningTagsWithState(text, state.pendingThinkingTag, state.pendingThinking)
				state.pendingThinkingTag = pendingTag
				state.pendingThinking = pendingContent
				if reasoningContent != "" {
					events = append(events, Event{Event: "response.reasoning.delta", Data: map[string]any{"summary": reasoningContent}})
				}
				if cleanText != "" {
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
					itemID = fmt.Sprintf("tool_%d", index)
				}
				state.toolIDsByIndex[index] = itemID
				function, _ := tool["function"].(map[string]any)
				name := stringValue(function["name"])
				arguments := stringValue(function["arguments"])
				if name != "" || stringValue(tool["id"]) != "" {
					if !state.toolSent[itemID] {
						if arguments != "" {
							events = append(events, Event{Event: "response.output_item.done", Data: map[string]any{"item": map[string]any{"type": "function_call", "id": itemID, "call_id": itemID, "name": name, "arguments": arguments}}})
							state.toolSent[itemID] = true
						} else {
							events = append(events, Event{Event: "response.output_item.added", Data: map[string]any{"item": map[string]any{"type": "function_call", "id": itemID, "call_id": itemID, "name": name}}})
							if state.pendingItems == nil {
								state.pendingItems = map[string]map[string]any{}
							}
							state.pendingItems[itemID] = map[string]any{"type": "function_call", "id": itemID, "call_id": itemID, "name": name}
						}
					}
					if arguments != "" {
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
				data := map[string]any{"finish_reason": finishReason, "usage": cloneMap(state.usage)}
				events = append(events, Event{Event: "response.completed", Data: data})
			} else {
				state.pendingFinish = finishReason
			}
		}
	}
	return events, false, nil
}

func newAnthropicEventBatchReader() func(*bufio.Scanner) ([]Event, error) {
	state := &anthropicNormalizationState{toolIDsByIndex: map[int]string{}, usage: map[string]any{}}
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
	toolIDsByIndex map[int]string
	usage          map[string]any
	completed      bool
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
			events = append(events, Event{Event: "response.created", Data: map[string]any{"response": map[string]any{"id": responseID}}})
		}
	case "content_block_start":
		index := int(numberValue(payload["index"]))
		block, _ := payload["content_block"].(map[string]any)
		if blockType := stringValue(block["type"]); blockType == "tool_use" {
			itemID := stringValue(block["id"])
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
			events = append(events, Event{Event: "response.completed", Data: map[string]any{"usage": cloneMap(state.usage), "stop_reason": stopReason}})
		}
	case "message_stop":
		if !state.completed {
			state.completed = true
			events = append(events, Event{Event: "response.completed", Data: map[string]any{"usage": cloneMap(state.usage)}})
		}
	case "error":
		errMap, _ := payload["error"].(map[string]any)
		events = append(events, Event{Event: "response.incomplete", Data: map[string]any{"health_flag": "upstream_error", "message": stringValue(errMap["message"])}})
		state.completed = true
	}
	return events, false, nil
}

func normalizeChatPayload(payload map[string]any) map[string]any {
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
	var pendingTag, pendingContent string
	for _, rawItem := range content {
		if item, ok := rawItem.(map[string]any); ok && stringValue(item["type"]) == "output_text" {
			text := stringValue(item["text"])
			cleanText, extracted, newPendingTag, newPendingContent := extractContentAndReasoningTagsWithState(text, pendingTag, pendingContent)
			pendingTag = newPendingTag
			pendingContent = newPendingContent
			reasoningContent += extracted
			item["text"] = cleanText
		}
	}
	if pendingTag != "" && pendingContent != "" {
		reasoningContent += pendingContent
	}
	existingReasoning := stringValue(message["reasoning_content"])
	if existingReasoning != "" {
		reasoningContent = existingReasoning
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
			payload["reasoning"] = cloneMap(req.Reasoning.Raw)
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
				callID := call.ID
				if callID == "" {
					callID = call.Name
				}
				toolCalls = append(toolCalls, map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": call.Name, "arguments": call.Arguments}})
			}
			entry["tool_calls"] = toolCalls
		}
		messages = append(messages, entry)
	}
	return messages
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
		pendingToolResults = appendPendingToolResults(pendingToolResults)
		content := buildAnthropicContentParts(msg.Parts)
		for _, call := range msg.ToolCalls {
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
			content = append(content, map[string]any{"type": "text", "text": part.Text})
		case "image_url", "input_image":
			if block := buildAnthropicImageBlock(part); block != nil {
				content = append(content, block)
			}
		case "input_file":
			if block := buildAnthropicDocumentBlock(part); block != nil {
				content = append(content, block)
			}
		}
	}
	return content
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
	var decoded any
	if err := json.Unmarshal([]byte(arguments), &decoded); err != nil {
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

// extractContentAndReasoningTags extracts <think>...</think> and <thinking>...</thinking> tags from text,
// returning the cleaned text and the extracted reasoning content.
func extractContentAndReasoningTags(text string) (cleanText string, reasoningContent string) {
	cleanText = text

	// Extract <think>...</think> tags
	const thinkTagOpen = "<think>"
	const thinkTagClose = `
</think>

`
	for {
		openIdx := strings.Index(cleanText, thinkTagOpen)
		if openIdx == -1 {
			break
		}
		closeIdx := strings.Index(cleanText[openIdx:], thinkTagClose)
		if closeIdx == -1 {
			break
		}
		closeIdx += openIdx
		reasoningContent += cleanText[openIdx+len(thinkTagOpen) : closeIdx]
		cleanText = cleanText[:openIdx] + cleanText[closeIdx+len(thinkTagClose):]
	}

	// Extract <thinking>...</thinking> tags
	const thinTagOpen = "<thinking>"
	const thinTagClose = "</thinking>"
	for {
		openIdx := strings.Index(cleanText, thinTagOpen)
		if openIdx == -1 {
			break
		}
		closeIdx := strings.Index(cleanText[openIdx:], thinTagClose)
		if closeIdx == -1 {
			break
		}
		closeIdx += openIdx
		reasoningContent += cleanText[openIdx+len(thinTagOpen) : closeIdx]
		cleanText = cleanText[:openIdx] + cleanText[closeIdx+len(thinTagClose):]
	}

	return cleanText, reasoningContent
}

// extractContentAndReasoningTagsWithState extracts <think>...</think> and <thinking>...</thinking>
// tags from text, handling tags that span across multiple deltas.
// Returns: cleanText, reasoningContent, pendingTag, pendingThinking
// pendingTag is "" if no tag is open, otherwise "<think>" or "<thinking>"
// pendingThinking is the accumulated content waiting for a close tag
func extractContentAndReasoningTagsWithState(text, pendingTag, pendingThinking string) (cleanText, reasoningContent, newPendingTag, newPendingThinking string) {
	cleanText = text
	newPendingTag = pendingTag
	newPendingThinking = pendingThinking

	if pendingTag != "" {
		var closeTag string
		if pendingTag == "<think>" {
			closeTag = `
</think>

`
		} else {
			closeTag = "</thinking>"
		}
		closeIdx := strings.Index(cleanText, closeTag)
		if closeIdx == -1 {
			newPendingThinking += cleanText
			cleanText = ""
			return
		}
		reasoningContent += newPendingThinking + cleanText[:closeIdx]
		cleanText = cleanText[closeIdx+len(closeTag):]
		newPendingTag = ""
		newPendingThinking = ""
	}

	const thinkTagOpen = "<think>"
	const thinkTagClose = `
</think>

`
	const thinTagOpen = "<thinking>"
	const thinTagClose = "</thinking>"

	for {
		var openTag, closeTag string
		openIdx := strings.Index(cleanText, thinkTagOpen)
		if openIdx == -1 {
			openIdx = strings.Index(cleanText, thinTagOpen)
			if openIdx == -1 {
				break
			}
			openTag = thinTagOpen
			closeTag = thinTagClose
		} else {
			thinIdx := strings.Index(cleanText, thinTagOpen)
			if thinIdx != -1 && thinIdx < openIdx {
				openIdx = thinIdx
				openTag = thinTagOpen
				closeTag = thinTagClose
			} else {
				openTag = thinkTagOpen
				closeTag = thinkTagClose
			}
		}

		closeIdx := strings.Index(cleanText[openIdx+len(openTag):], closeTag)
		if closeIdx == -1 {
			newPendingTag = openTag
			newPendingThinking = cleanText[openIdx+len(openTag):]
			cleanText = cleanText[:openIdx]
			break
		}
		closeIdx += openIdx + len(openTag)
		reasoningContent += cleanText[openIdx+len(openTag) : closeIdx]
		cleanText = cleanText[:openIdx] + cleanText[closeIdx+len(closeTag):]
	}

	return cleanText, reasoningContent, newPendingTag, newPendingThinking
}
