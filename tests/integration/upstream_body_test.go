package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"openai-compat-proxy/internal/model"
	"openai-compat-proxy/internal/upstream"
)

func TestUpstreamClientSendsInputListBody(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		input, ok := body["input"].([]any)
		if !ok || len(input) == 0 {
			t.Fatalf("expected non-empty input list, got %#v", body["input"])
		}
		if body["stream"] != true {
			t.Fatalf("expected stream=true, got %#v", body["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), sampleCanonicalRequest(), "Bearer server-key")
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamClientSendsToolsInRequestBody(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		tools, ok := body["tools"].([]any)
		if !ok || len(tools) != 1 {
			t.Fatalf("expected one tool, got %#v", body["tools"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	req := sampleCanonicalRequest()
	req.Tools = []model.CanonicalTool{{
		Type:        "function",
		Name:        "get_weather",
		Description: "Get weather",
		Parameters:  map[string]any{"type": "object"},
	}}

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), req, "Bearer server-key")
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamClientMapsAssistantTextHistoryToOutputText(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}

		input, ok := body["input"].([]any)
		if !ok || len(input) != 2 {
			t.Fatalf("expected two input messages, got %#v", body["input"])
		}

		assistant, ok := input[1].(map[string]any)
		if !ok {
			t.Fatalf("expected assistant message object, got %#v", input[1])
		}
		if assistant["role"] != "assistant" {
			t.Fatalf("expected assistant role, got %#v", assistant["role"])
		}

		content, ok := assistant["content"].([]any)
		if !ok || len(content) != 1 {
			t.Fatalf("expected assistant content list, got %#v", assistant["content"])
		}

		part, ok := content[0].(map[string]any)
		if !ok {
			t.Fatalf("expected assistant content object, got %#v", content[0])
		}
		if part["type"] != "output_text" {
			t.Fatalf("expected assistant text to map to output_text, got %#v", part["type"])
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	req := model.CanonicalRequest{
		Model:  "gpt-x",
		Stream: true,
		Messages: []model.CanonicalMessage{
			{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hi"}}},
			{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}}},
		},
	}

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), req, "Bearer server-key")
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamClientOmitsReasoningWhenNotRequested(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["reasoning"]; ok {
			t.Fatalf("expected no reasoning payload, got %#v", body["reasoning"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	req := model.CanonicalRequest{
		Model:  "gpt-x",
		Stream: true,
		Messages: []model.CanonicalMessage{
			{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hi"}}},
		},
	}

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), req, "Bearer server-key")
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamClientPassesThroughReasoningObject(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		reasoning, ok := body["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("expected reasoning object, got %#v", body["reasoning"])
		}
		if reasoning["effort"] != "high" || reasoning["summary"] != "auto" {
			t.Fatalf("expected pass-through reasoning object, got %#v", reasoning)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	req := model.CanonicalRequest{
		Model:  "gpt-x",
		Stream: true,
		Messages: []model.CanonicalMessage{
			{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hi"}}},
		},
		Reasoning: &model.CanonicalReasoning{Effort: "high", Summary: "auto", Raw: map[string]any{"effort": "high", "summary": "auto"}},
	}

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), req, "Bearer server-key")
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamClientAddsDefaultReasoningSummaryAuto(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		reasoning, ok := body["reasoning"].(map[string]any)
		if !ok {
			t.Fatalf("expected reasoning object, got %#v", body["reasoning"])
		}
		if reasoning["effort"] != "high" || reasoning["summary"] != "auto" {
			t.Fatalf("expected default summary auto, got %#v", reasoning)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	req := model.CanonicalRequest{
		Model:     "gpt-x",
		Stream:    true,
		Messages:  []model.CanonicalMessage{{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hi"}}}},
		Reasoning: &model.CanonicalReasoning{Effort: "high", Raw: map[string]any{"effort": "high"}},
	}

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), req, "Bearer server-key")
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamClientReplaysToolLoopHistory(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		input, ok := body["input"].([]any)
		if !ok || len(input) != 2 {
			t.Fatalf("expected two replay items, got %#v", body["input"])
		}
		functionCall, _ := input[0].(map[string]any)
		if functionCall["type"] != "function_call" || functionCall["call_id"] != "call_1" || functionCall["name"] != "search_web" {
			t.Fatalf("expected function_call replay item, got %#v", functionCall)
		}
		functionOutput, _ := input[1].(map[string]any)
		if functionOutput["type"] != "function_call_output" || functionOutput["call_id"] != "call_1" {
			t.Fatalf("expected function_call_output replay item, got %#v", functionOutput)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	req := model.CanonicalRequest{
		Model:  "gpt-x",
		Stream: true,
		Messages: []model.CanonicalMessage{
			{Role: "assistant", ReasoningContent: "正在调用工具…\n", ToolCalls: []model.CanonicalToolCall{{ID: "call_1", Type: "function", Name: "search_web", Arguments: `{"query":"桂林天气"}`}}},
			{Role: "tool", ToolCallID: "call_1", Parts: []model.CanonicalContentPart{{Type: "text", Text: `{"result":"晴"}`}}},
		},
	}

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), req, "Bearer server-key")
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamClientBuildsStableBodiesForEquivalentRequests(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []map[string]any
	)

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	req := model.CanonicalRequest{
		Model:  "gpt-x",
		Stream: true,
		Messages: []model.CanonicalMessage{
			{Role: "user", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hi"}}},
			{Role: "assistant", Parts: []model.CanonicalContentPart{{Type: "text", Text: "hello"}}},
		},
		Tools: []model.CanonicalTool{{
			Type:       "function",
			Name:       "lookup",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"ids": map[string]any{"type": "array"}}},
		}},
		Reasoning: &model.CanonicalReasoning{Effort: "high", Raw: map[string]any{"effort": "high"}},
	}

	client := upstream.NewClient(stub.URL)
	for i := 0; i < 2; i++ {
		_, err := client.Stream(context.Background(), req, "Bearer server-key")
		if err != nil {
			t.Fatal(err)
		}
	}

	if len(bodies) != 2 {
		t.Fatalf("expected two captured bodies, got %d", len(bodies))
	}
	first, err := json.Marshal(bodies[0])
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(bodies[1])
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("expected stable upstream body, got %s vs %s", first, second)
	}

	reasoning, ok := bodies[0]["reasoning"].(map[string]any)
	if !ok || reasoning["summary"] != "auto" {
		t.Fatalf("expected stable reasoning summary auto, got %#v", bodies[0]["reasoning"])
	}
	tools, ok := bodies[0]["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected one tool, got %#v", bodies[0]["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	parameters, _ := tool["parameters"].(map[string]any)
	properties, _ := parameters["properties"].(map[string]any)
	ids, _ := properties["ids"].(map[string]any)
	if _, ok := ids["items"]; !ok {
		t.Fatalf("expected normalized array schema items, got %#v", ids)
	}
}

func TestUpstreamClientDoesNotReplayReasoningContentAsAssistantPrompt(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}

		input, ok := body["input"].([]any)
		if !ok || len(input) != 1 {
			t.Fatalf("expected one assistant replay item, got %#v", body["input"])
		}
		assistant, ok := input[0].(map[string]any)
		if !ok {
			t.Fatalf("expected assistant object, got %#v", input[0])
		}
		content, ok := assistant["content"].([]any)
		if !ok || len(content) != 1 {
			t.Fatalf("expected only assistant text content, got %#v", assistant["content"])
		}
		part, ok := content[0].(map[string]any)
		if !ok {
			t.Fatalf("expected content object, got %#v", content[0])
		}
		if part["text"] != "final answer" {
			t.Fatalf("expected reasoning_content to be excluded from upstream replay, got %#v", assistant["content"])
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {}\n\n"))
	}))
	defer stub.Close()

	req := model.CanonicalRequest{
		Model:  "gpt-x",
		Stream: true,
		Messages: []model.CanonicalMessage{{
			Role:             "assistant",
			ReasoningContent: "正在组织回答…\n",
			Parts:            []model.CanonicalContentPart{{Type: "text", Text: "final answer"}},
		}},
	}

	client := upstream.NewClient(stub.URL)
	_, err := client.Stream(context.Background(), req, "Bearer server-key")
	if err != nil {
		t.Fatal(err)
	}
}
