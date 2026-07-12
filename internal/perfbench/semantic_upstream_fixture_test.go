package perfbench

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

type capturedSemanticRequest struct {
	Method              string
	Path                string
	Header              http.Header
	Body                []byte
	ContentLength       int64
	ResponseContentType string
	ResponseMode        string
}

type semanticFakeUpstream struct {
	item     scenario
	gate     *capacityUpstreamGate
	server   *httptest.Server
	mu       sync.Mutex
	requests []capturedSemanticRequest
}

func newSemanticFakeUpstream(item scenario) *semanticFakeUpstream {
	return newSemanticFakeUpstreamWithGate(item, nil)
}

func newSemanticFakeUpstreamWithGate(item scenario, gate *capacityUpstreamGate) *semanticFakeUpstream {
	fake := &semanticFakeUpstream{item: item, gate: gate}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.handle))
	return fake
}

func (f *semanticFakeUpstream) close() {
	f.server.Close()
}

func (f *semanticFakeUpstream) url() string {
	return f.server.URL
}

func (f *semanticFakeUpstream) capturedRequests() []capturedSemanticRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	result := make([]capturedSemanticRequest, len(f.requests))
	for index, request := range f.requests {
		result[index] = capturedSemanticRequest{
			Method:              request.Method,
			Path:                request.Path,
			Header:              request.Header.Clone(),
			Body:                append([]byte(nil), request.Body...),
			ContentLength:       request.ContentLength,
			ResponseContentType: request.ResponseContentType,
			ResponseMode:        request.ResponseMode,
		}
	}
	return result
}

func (f *semanticFakeUpstream) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"perf-model","object":"model"}]}`)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stream := bytes.Contains(body, []byte(`"stream":true`))
	f.mu.Lock()
	attempt := len(f.requests) + 1
	responseContentType, responseMode := "application/json", "json"
	if stream {
		responseContentType, responseMode = "text/event-stream", "sse"
	}
	if f.item.Profile == profileRetryOnce && attempt == 1 {
		responseContentType, responseMode = "text/plain; charset=utf-8", "error"
	}
	f.requests = append(f.requests, capturedSemanticRequest{
		Method:              r.Method,
		Path:                r.URL.Path,
		Header:              r.Header.Clone(),
		Body:                append([]byte(nil), body...),
		ContentLength:       r.ContentLength,
		ResponseContentType: responseContentType,
		ResponseMode:        responseMode,
	})
	f.mu.Unlock()

	if err := validateSemanticUpstreamRequest(f.item.Upstream, r); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if f.gate != nil {
		if err := f.gate.wait(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
	}
	if f.item.Profile == profileRetryOnce && attempt == 1 {
		w.Header().Set("Content-Type", responseContentType)
		http.Error(w, "temporary semantic fixture failure", http.StatusBadGateway)
		return
	}

	writeSemanticUpstreamFixture(w, f.item.Upstream, stream)
}

func validateSemanticUpstreamRequest(protocol upstreamProtocol, r *http.Request) error {
	if r.Method != http.MethodPost {
		return fmt.Errorf("upstream method = %s, want POST", r.Method)
	}
	if protocol == upstreamAnthropic {
		if r.Header.Get("X-Api-Key") != "perf-upstream-secret" {
			return fmt.Errorf("anthropic x-api-key mismatch")
		}
		if r.Header.Get("Anthropic-Version") == "" {
			return fmt.Errorf("anthropic-version missing")
		}
		return nil
	}
	if r.Header.Get("Authorization") != "Bearer perf-upstream-secret" {
		return fmt.Errorf("openai authorization mismatch")
	}
	return nil
}

func writeSemanticUpstreamFixture(w http.ResponseWriter, protocol upstreamProtocol, stream bool) {
	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		switch protocol {
		case upstreamResponses:
			_, _ = io.WriteString(w, semanticResponsesSSE)
		case upstreamChat:
			_, _ = io.WriteString(w, semanticChatSSE)
		case upstreamAnthropic:
			_, _ = io.WriteString(w, semanticAnthropicSSE)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	switch protocol {
	case upstreamResponses:
		_, _ = io.WriteString(w, semanticResponsesJSON)
	case upstreamChat:
		_, _ = io.WriteString(w, semanticChatJSON)
	case upstreamAnthropic:
		_, _ = io.WriteString(w, semanticAnthropicJSON)
	}
}

const semanticResponsesJSON = `{"id":"resp_fixture","object":"response","created_at":1700000000,"status":"completed","finish_reason":"tool_calls","model":"perf-model","output":[{"id":"rs_fixture","type":"reasoning","summary":[{"type":"summary_text","text":"fixture-reasoning"}]},{"id":"fc_fixture","type":"function_call","status":"completed","call_id":"call_fixture","name":"lookup","arguments":"{\"query\":\"fixture\"}"}],"usage":{"input_tokens":111,"input_tokens_details":{"cached_tokens":44},"output_tokens":22,"output_tokens_details":{"reasoning_tokens":7},"total_tokens":133}}`

const semanticResponsesSSE = "event: response.created\n" +
	"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fixture\",\"object\":\"response\",\"created_at\":1700000000,\"status\":\"in_progress\",\"model\":\"perf-model\",\"output\":[]}}\n\n" +
	"event: response.output_item.added\n" +
	"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"rs_fixture\",\"type\":\"reasoning\",\"summary\":[]}}\n\n" +
	"event: response.reasoning_summary_text.delta\n" +
	"data: {\"type\":\"response.reasoning_summary_text.delta\",\"item_id\":\"rs_fixture\",\"output_index\":0,\"summary_index\":0,\"delta\":\"fixture-reasoning\"}\n\n" +
	"event: response.reasoning_summary_text.done\n" +
	"data: {\"type\":\"response.reasoning_summary_text.done\",\"item_id\":\"rs_fixture\",\"output_index\":0,\"summary_index\":0,\"text\":\"fixture-reasoning\"}\n\n" +
	"event: response.output_item.done\n" +
	"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"rs_fixture\",\"type\":\"reasoning\",\"summary\":[{\"type\":\"summary_text\",\"text\":\"fixture-reasoning\"}]}}\n\n" +
	"event: response.output_item.added\n" +
	"data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"id\":\"fc_fixture\",\"type\":\"function_call\",\"status\":\"in_progress\",\"arguments\":\"\",\"call_id\":\"call_fixture\",\"name\":\"lookup\"}}\n\n" +
	"event: response.function_call_arguments.delta\n" +
	"data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"item_id\":\"fc_fixture\",\"delta\":\"{\\\"query\\\":\\\"fixture\\\"}\"}\n\n" +
	"event: response.function_call_arguments.done\n" +
	"data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":1,\"item_id\":\"fc_fixture\",\"arguments\":\"{\\\"query\\\":\\\"fixture\\\"}\"}\n\n" +
	"event: response.output_item.done\n" +
	"data: {\"type\":\"response.output_item.done\",\"output_index\":1,\"item\":{\"id\":\"fc_fixture\",\"type\":\"function_call\",\"status\":\"completed\",\"arguments\":\"{\\\"query\\\":\\\"fixture\\\"}\",\"call_id\":\"call_fixture\",\"name\":\"lookup\"}}\n\n" +
	"event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"response\":" + semanticResponsesJSON + "}\n\n"

const semanticChatJSON = `{"id":"chatcmpl_fixture","object":"chat.completion","created":1700000000,"model":"perf-model","choices":[{"index":0,"message":{"role":"assistant","content":"","reasoning_content":"fixture-reasoning","tool_calls":[{"id":"call_fixture","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"fixture\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":111,"completion_tokens":22,"total_tokens":133,"prompt_tokens_details":{"cached_tokens":44},"completion_tokens_details":{"reasoning_tokens":7}}}`

const semanticChatSSE = "data: {\"id\":\"chatcmpl_fixture\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"perf-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"fixture-reasoning\"}}]}\n\n" +
	"data: {\"id\":\"chatcmpl_fixture\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"perf-model\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_fixture\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"query\\\":\\\"fixture\\\"}\"}}]}}]}\n\n" +
	"data: {\"id\":\"chatcmpl_fixture\",\"object\":\"chat.completion.chunk\",\"created\":1700000000,\"model\":\"perf-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":111,\"completion_tokens\":22,\"total_tokens\":133,\"prompt_tokens_details\":{\"cached_tokens\":44},\"completion_tokens_details\":{\"reasoning_tokens\":7}}}\n\n" +
	"data: [DONE]\n\n"

const semanticAnthropicJSON = `{"id":"msg_fixture","type":"message","role":"assistant","model":"perf-model","content":[{"type":"thinking","thinking":"fixture-reasoning","signature":"sig_fixture"},{"type":"tool_use","id":"call_fixture","name":"lookup","input":{"query":"fixture"}}],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":111,"cache_read_input_tokens":44,"cache_creation_input_tokens":0,"output_tokens":22}}`

const semanticAnthropicSSE = "event: message_start\n" +
	"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fixture\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"perf-model\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":111,\"cache_read_input_tokens\":44,\"cache_creation_input_tokens\":0,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"fixture-reasoning\"}}\n\n" +
	"event: content_block_stop\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_fixture\",\"name\":\"lookup\",\"input\":{}}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"query\\\":\\\"fixture\\\"}\"}}\n\n" +
	"event: content_block_stop\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
	"event: message_delta\n" +
	"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":22}}\n\n" +
	"event: message_stop\n" +
	"data: {\"type\":\"message_stop\"}\n\n"
