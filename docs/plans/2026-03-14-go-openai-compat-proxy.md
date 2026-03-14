# Go OpenAI Compatibility Proxy Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a Go single-binary proxy that exposes `/v1/responses` and `/v1/chat/completions`, internally talks to an upstream streaming Responses API, adds non-streaming compatibility, and supports tools, reasoning fields, and multimodal image input with mixed-mode compatibility rules.

**Architecture:** The proxy has two downstream adapters and one canonical internal model. Every downstream request is normalized, sent upstream as streaming `/v1/responses`, then either streamed back through a route-specific mapper or aggregated by a state machine into a final JSON object for non-stream callers.

**Tech Stack:** Go 1.22+, standard library `net/http`, `httptest`, `encoding/json`, optional `github.com/google/uuid` or equivalent small helper for request IDs.

---

### Task 1: Scaffold the Go service and configuration model

**Files:**
- Create: `go.mod`
- Create: `cmd/proxy/main.go`
- Create: `internal/config/config.go`
- Create: `internal/httpapi/server.go`
- Create: `internal/httpapi/handlers_health.go`
- Test: `tests/integration/health_test.go`

**Step 1: Write the failing test**

```go
func TestHealthzReturns200(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestHealthzReturns200 -v`
Expected: FAIL because the server bootstrap does not exist yet.

**Step 3: Write minimal implementation**

Create `internal/config/config.go` with:

```go
type Config struct {
	ListenAddr string
	ProxyAPIKey string
	UpstreamBaseURL string
	UpstreamAPIKey string
	ConnectTimeout time.Duration
	FirstByteTimeout time.Duration
	IdleTimeout time.Duration
	TotalTimeout time.Duration
}
```

Create `internal/httpapi/server.go` and `internal/httpapi/handlers_health.go` with a basic `GET /healthz` route.

Create `cmd/proxy/main.go` to load config and call `http.ListenAndServe`.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestHealthzReturns200 -v`
Expected: PASS

**Step 5: Commit**

```bash
git add go.mod cmd/proxy/main.go internal/config/config.go internal/httpapi/server.go internal/httpapi/handlers_health.go tests/integration/health_test.go
git commit -m "feat: scaffold proxy service and health endpoint"
```

### Task 2: Add request ID middleware and OpenAI-style error envelope

**Files:**
- Create: `internal/httpapi/middleware.go`
- Create: `internal/errorsx/errors.go`
- Modify: `internal/httpapi/server.go`
- Test: `tests/integration/error_test.go`

**Step 1: Write the failing test**

```go
func TestUnsupportedRouteReturnsOpenAIStyleError(t *testing.T) {
	server := newTestServer(t)
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/unknown", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if _, ok := body["error"]; !ok {
		t.Fatal("expected error envelope")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestUnsupportedRouteReturnsOpenAIStyleError -v`
Expected: FAIL because unknown routes are not normalized yet.

**Step 3: Write minimal implementation**

Create `internal/errorsx/errors.go` with a helper like:

```go
func WriteJSON(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "proxy_error",
			"param":   nil,
			"code":    code,
		},
	})
}
```

Add middleware to generate request IDs and attach them to context/headers.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestUnsupportedRouteReturnsOpenAIStyleError -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/httpapi/middleware.go internal/errorsx/errors.go internal/httpapi/server.go tests/integration/error_test.go
git commit -m "feat: add request ids and standardized error envelopes"
```

### Task 3: Implement proxy auth and upstream key resolution

**Files:**
- Create: `internal/auth/auth.go`
- Modify: `internal/httpapi/middleware.go`
- Modify: `internal/config/config.go`
- Test: `tests/integration/auth_test.go`

**Step 1: Write the failing tests**

```go
func TestProxyAPIKeyRequiredWhenConfigured(t *testing.T) {
	server := newTestServerWithConfig(t, testConfig(func(c *config.Config) {
		c.ProxyAPIKey = "proxy-secret"
	}))
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"x","input":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}
```

**Step 2: Run test to verify they fail**

Run: `go test ./tests/integration -run TestProxyAPIKeyRequiredWhenConfigured -v`
Expected: FAIL because auth enforcement does not exist.

**Step 3: Write minimal implementation**

Create `internal/auth/auth.go` with:

```go
func ValidateProxyAuth(r *http.Request, proxyKey string) error
func ResolveUpstreamAuthorization(r *http.Request, cfg config.Config) (string, error)
```

Rules:
- if `ProxyAPIKey` is set, require `Authorization: Bearer <proxy-key>`
- prefer `X-Upstream-Authorization` for caller-supplied upstream auth
- else fall back to `UPSTREAM_API_KEY`

**Step 4: Run test to verify they pass**

Run: `go test ./tests/integration -run TestProxyAPIKeyRequiredWhenConfigured -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/auth/auth.go internal/httpapi/middleware.go internal/config/config.go tests/integration/auth_test.go
git commit -m "feat: add proxy auth and upstream key selection"
```

### Task 4: Define the canonical model for text, tools, reasoning, and multimodal parts

**Files:**
- Create: `internal/model/canonical.go`
- Test: `tests/integration/canonical_model_test.go`

**Step 1: Write the failing test**

```go
func TestCanonicalModelSupportsTextImageToolAndReasoning(t *testing.T) {
	req := model.CanonicalRequest{
		Model: "gpt-x",
		Messages: []model.CanonicalMessage{{
			Role: "user",
			Parts: []model.CanonicalContentPart{{Type: "text", Text: "describe"}, {Type: "image_url", ImageURL: "https://example.com/a.png"}},
		}},
		Tools: []model.CanonicalTool{{Type: "function", Name: "lookup"}},
		Reasoning: &model.CanonicalReasoning{Effort: "medium"},
	}
	if len(req.Messages[0].Parts) != 2 || req.Tools[0].Name != "lookup" || req.Reasoning.Effort != "medium" {
		t.Fatal("canonical model missing expected capability")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestCanonicalModelSupportsTextImageToolAndReasoning -v`
Expected: FAIL because canonical model does not exist.

**Step 3: Write minimal implementation**

Create:
- `CanonicalRequest`
- `CanonicalMessage`
- `CanonicalContentPart`
- `CanonicalTool`
- `CanonicalToolChoice`
- `CanonicalReasoning`

Preserve `Raw map[string]any` on extensible structures so upstream-specific fields can survive translation.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestCanonicalModelSupportsTextImageToolAndReasoning -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/model/canonical.go tests/integration/canonical_model_test.go
git commit -m "feat: add canonical model for tools reasoning and multimodal inputs"
```

### Task 5: Implement inbound Chat request mapping

**Files:**
- Create: `internal/adapter/chat/request.go`
- Test: `tests/integration/chat_request_mapping_test.go`

**Step 1: Write the failing tests**

```go
func TestChatRequestMapsImageToolsAndReasoning(t *testing.T) {
	body := `{
		"model":"gpt-x",
		"stream":false,
		"messages":[{"role":"user","content":[{"type":"text","text":"what is in this image"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],
		"reasoning_effort":"medium"
	}`

	canon, err := chatadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(canon.Messages[0].Parts) != 2 || len(canon.Tools) != 1 || canon.Reasoning == nil {
		t.Fatal("expected mapped multimodal, tools, and reasoning")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestChatRequestMapsImageToolsAndReasoning -v`
Expected: FAIL because chat request adapter does not exist.

**Step 3: Write minimal implementation**

Decode:
- plain string message content
- content-part arrays with text and image_url
- `tools` / `tool_choice`
- reasoning fields into canonical reasoning

Reject unsupported `n > 1` and malformed content parts.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestChatRequestMapsImageToolsAndReasoning -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/adapter/chat/request.go tests/integration/chat_request_mapping_test.go
git commit -m "feat: map chat requests into canonical multimodal tool-aware model"
```

### Task 6: Implement inbound Responses request mapping

**Files:**
- Create: `internal/adapter/responses/request.go`
- Test: `tests/integration/responses_request_mapping_test.go`

**Step 1: Write the failing tests**

```go
func TestResponsesRequestMapsToolsReasoningAndImageInput(t *testing.T) {
	body := `{
		"model":"gpt-x",
		"stream":false,
		"input":[{"role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"https://example.com/a.png"}]}],
		"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],
		"reasoning":{"effort":"high"}
	}`

	canon, err := responsesadapter.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(canon.Messages) != 1 || len(canon.Tools) != 1 || canon.Reasoning.Effort != "high" {
		t.Fatal("expected mapped responses request")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestResponsesRequestMapsToolsReasoningAndImageInput -v`
Expected: FAIL because responses adapter does not exist.

**Step 3: Write minimal implementation**

Decode text/image input parts, tools, tool_choice, and reasoning into the canonical model. Preserve raw fields when needed for upstream re-emission.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestResponsesRequestMapsToolsReasoningAndImageInput -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/adapter/responses/request.go tests/integration/responses_request_mapping_test.go
git commit -m "feat: map responses requests into canonical multimodal tool-aware model"
```

### Task 7: Build the upstream streaming client and SSE reader

**Files:**
- Create: `internal/upstream/client.go`
- Create: `internal/upstream/sse.go`
- Create: `internal/testutil/upstream_stub.go`
- Test: `tests/integration/upstream_stream_test.go`

**Step 1: Write the failing test**

```go
func TestUpstreamClientReadsTextAndToolEvents(t *testing.T) {
	stub := testutil.NewStreamingUpstream(t, []string{
		"event: response.output_text.delta\ndata: {\"delta\":\"hel\"}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"item_id\":\"call_1\",\"delta\":\"{\\\"id\\\":1\"}\n\n",
		"event: response.completed\ndata: {}\n\n",
	})
	defer stub.Close()

	client := upstream.NewClient(stub.URL)
	events, err := client.Stream(context.Background(), sampleCanonicalRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected streamed events")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestUpstreamClientReadsTextAndToolEvents -v`
Expected: FAIL because upstream transport and SSE parsing are missing.

**Step 3: Write minimal implementation**

Implement:
- upstream request builder that always sends `stream=true`
- SSE scanner/parser for `event:` and `data:` lines
- event struct carrying raw event name and parsed JSON payload

Use `net/http` with per-request context deadlines.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestUpstreamClientReadsTextAndToolEvents -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/upstream/client.go internal/upstream/sse.go internal/testutil/upstream_stub.go tests/integration/upstream_stream_test.go
git commit -m "feat: add upstream streaming client and SSE parser"
```

### Task 8: Implement aggregation state machine for text, tools, reasoning, and multimodal parts

**Files:**
- Create: `internal/aggregate/collector.go`
- Test: `tests/integration/aggregate_test.go`

**Step 1: Write the failing test**

```go
func TestCollectorBuildsTextAndToolCalls(t *testing.T) {
	collector := aggregate.NewCollector()
	collector.Accept(sampleTextDeltaEvent("hel"))
	collector.Accept(sampleTextDeltaEvent("lo"))
	collector.Accept(sampleToolDeltaEvent("call_1", "{\"city\":\"sh"))
	collector.Accept(sampleToolDeltaEvent("call_1", "anghai\"}"))
	collector.Accept(sampleCompletedEvent())

	result, err := collector.Result()
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "hello" || len(result.ToolCalls) != 1 {
		t.Fatal("expected aggregated text and tool call")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestCollectorBuildsTextAndToolCalls -v`
Expected: FAIL because collector does not exist.

**Step 3: Write minimal implementation**

Create collector methods:

```go
type Collector struct { /* state machine fields */ }
func NewCollector() *Collector
func (c *Collector) Accept(evt upstream.Event)
func (c *Collector) Result() (Result, error)
```

Track:
- text deltas
- tool call buffers
- reasoning raw metadata
- content parts
- terminal event detection

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestCollectorBuildsTextAndToolCalls -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/aggregate/collector.go tests/integration/aggregate_test.go
git commit -m "feat: add aggregation state machine for text tool and reasoning events"
```

### Task 9: Implement downstream Responses handler with tools/reasoning/multimodal support

**Files:**
- Create: `internal/adapter/responses/response.go`
- Create: `internal/httpapi/handlers_responses.go`
- Modify: `internal/httpapi/server.go`
- Test: `tests/integration/responses_test.go`

**Step 1: Write the failing tests**

```go
func TestResponsesHandlerReturnsSynthesizedJSONWithToolCalls(t *testing.T) {
	server := newServerWithStubbedUpstream(t, healthyToolStreamingResponsesStub(t))
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"x","input":"hi","stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if body["object"] != "response" {
		t.Fatalf("unexpected object: %v", body["object"])
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestResponsesHandlerReturnsSynthesizedJSONWithToolCalls -v`
Expected: FAIL because responses handler does not exist.

**Step 3: Write minimal implementation**

Implement handler flow:
- validate auth
- decode responses request to canonical model
- call upstream stream client
- if `stream=false`, aggregate and write synthesized response JSON including tool/reasoning content where representable
- if `stream=true`, write SSE headers and forward normalized responses events

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestResponsesHandlerReturnsSynthesizedJSONWithToolCalls -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/adapter/responses/response.go internal/httpapi/handlers_responses.go internal/httpapi/server.go tests/integration/responses_test.go
git commit -m "feat: add responses endpoint with tool and reasoning compatible output"
```

### Task 10: Implement downstream Chat handler with tool call chunk translation

**Files:**
- Create: `internal/adapter/chat/response.go`
- Create: `internal/httpapi/handlers_chat.go`
- Modify: `internal/httpapi/server.go`
- Test: `tests/integration/chat_test.go`

**Step 1: Write the failing tests**

```go
func TestChatHandlerReturnsChatCompletionWithToolCalls(t *testing.T) {
	server := newServerWithStubbedUpstream(t, healthyToolStreamingResponsesStub(t))
	defer server.Close()

	body := `{"model":"x","messages":[{"role":"user","content":"hi"}],"stream":false}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if out["object"] != "chat.completion" {
		t.Fatalf("unexpected object: %v", out["object"])
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestChatHandlerReturnsChatCompletionWithToolCalls -v`
Expected: FAIL because chat handler does not exist.

**Step 3: Write minimal implementation**

Implement chat handler flow:
- validate auth
- decode chat request into canonical model
- call upstream stream client
- if `stream=false`, aggregate and write synthesized `chat.completion`
- if `stream=true`, emit `chat.completion.chunk` SSE frames with text and tool deltas, then `[DONE]`

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestChatHandlerReturnsChatCompletionWithToolCalls -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/adapter/chat/response.go internal/httpapi/handlers_chat.go internal/httpapi/server.go tests/integration/chat_test.go
git commit -m "feat: add chat completions endpoint with tool-aware compatibility mapping"
```

### Task 11: Add multimodal acceptance and safe-failure tests

**Files:**
- Test: `tests/integration/multimodal_test.go`
- Modify: `internal/adapter/chat/response.go`
- Modify: `internal/adapter/responses/response.go`

**Step 1: Write the failing tests**

```go
func TestChatRequestWithImageInputIsAccepted(t *testing.T) {
	server := newServerWithStubbedUpstream(t, healthyTextStreamingResponsesStub(t, "a cat on a sofa"))
	defer server.Close()

	body := `{"model":"x","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}}]}],"stream":false}`
	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestChatRequestWithImageInputIsAccepted -v`
Expected: FAIL because multimodal handling is incomplete.

**Step 3: Write minimal implementation**

Ensure image input survives request mapping and upstream emission.

For Chat output mapping, explicitly fail with a structured error if an upstream multimodal output shape cannot be safely converted.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestChatRequestWithImageInputIsAccepted -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/adapter/chat/response.go internal/adapter/responses/response.go tests/integration/multimodal_test.go
git commit -m "feat: support multimodal image input and safe output mapping failures"
```

### Task 12: Add timeout protection and hanging-upstream tests

**Files:**
- Modify: `internal/upstream/client.go`
- Modify: `internal/aggregate/collector.go`
- Test: `tests/integration/timeout_test.go`

**Step 1: Write the failing tests**

```go
func TestNonStreamingRequestTimesOutWhenUpstreamNeverCompletes(t *testing.T) {
	server := newServerWithStubbedUpstream(t, hangingUpstreamStub(t))
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"x","input":"hi","stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d", resp.StatusCode)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestNonStreamingRequestTimesOutWhenUpstreamNeverCompletes -v`
Expected: FAIL because timeout handling is incomplete.

**Step 3: Write minimal implementation**

Implement:
- connect/first-byte/idle/total timeout handling in upstream client
- timeout classification into proxy error codes
- collector failure when terminal event is never observed

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestNonStreamingRequestTimesOutWhenUpstreamNeverCompletes -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/upstream/client.go internal/aggregate/collector.go tests/integration/timeout_test.go
git commit -m "feat: add timeout protection for hanging upstream streams"
```

### Task 13: Add reasoning-specific output tests and deployment docs

**Files:**
- Test: `tests/integration/reasoning_test.go`
- Create: `README.md`
- Modify: `internal/adapter/chat/response.go`
- Modify: `internal/adapter/responses/response.go`

**Step 1: Write the failing tests**

```go
func TestResponsesRoutePreservesReasoningMetadataWhenAvailable(t *testing.T) {
	server := newServerWithStubbedUpstream(t, healthyReasoningStreamingResponsesStub(t))
	defer server.Close()

	resp, err := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"x","input":"hi","stream":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if _, ok := body["reasoning"]; !ok {
		t.Fatal("expected reasoning metadata")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestResponsesRoutePreservesReasoningMetadataWhenAvailable -v`
Expected: FAIL because reasoning output mapping is incomplete.

**Step 3: Write minimal implementation**

Preserve reasoning metadata on Responses route. On Chat route, only expose reasoning where shape-safe.

Add `README.md` with:
- env vars
- auth header contract
- curl examples for both endpoints
- examples with tools and image input
- documented v1 limitations and failure modes

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestResponsesRoutePreservesReasoningMetadataWhenAvailable -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/adapter/chat/response.go internal/adapter/responses/response.go README.md tests/integration/reasoning_test.go
git commit -m "docs: document expanded v1 compatibility and reasoning behavior"
```

## Final Verification

Run in order:

```bash
go test ./tests/integration -v
go test ./...
go build -o bin/openai-compat-proxy ./cmd/proxy
```

Expected:
- all integration tests pass
- package tests pass
- binary builds successfully

## Notes for the Implementer

- Keep mixed-mode compatibility rules exactly as documented: tools/reasoning strong, multimodal best-effort with explicit safe failure.
- Prefer explicit rejection over lossy translation when mapping is ambiguous.
- Never let a client hang because the upstream stream stalled or missed a terminal event.
- Do not log secrets or raw Authorization headers.
- Match the design doc in `docs/plans/2026-03-14-go-openai-compat-proxy-design.md`.

Plan complete and saved to `docs/plans/2026-03-14-go-openai-compat-proxy.md`. Two execution options:

**1. Subagent-Driven (this session)** - I dispatch fresh subagent per task, review between tasks, fast iteration

**2. Parallel Session (separate)** - Open new session with executing-plans, batch execution with checkpoints

Which approach?
