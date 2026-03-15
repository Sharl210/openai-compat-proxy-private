# Cache Stability Mode Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Formalize the proxy as a stable normalization gateway for upstream prompt caching and preserve upstream cache accounting fields in downstream responses.

**Architecture:** Keep the existing `chat/responses -> canonical -> upstream /responses` pipeline, but lock cache-sensitive request normalization with tests and extend usage mapping so cache-hit accounting survives downstream translation. The request shape stays intentionally normalized; the response shape becomes more observable.

**Tech Stack:** Go 1.22, standard library `net/http`, `httptest`, `encoding/json`, existing integration tests under `tests/integration`.

---

### Task 1: Lock cache-sensitive upstream request normalization with tests

**Files:**
- Modify: `tests/integration/upstream_body_test.go`
- Test: `tests/integration/upstream_body_test.go`

**Step 1: Write the failing test**

Add a test that sends two equivalent canonical requests through `upstream.Client` and asserts the upstream stub receives the same cache-sensitive fields both times, including stable reasoning summary defaults and stable assistant/tool replay shape.

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run 'TestUpstreamClient.*Cache.*|TestUpstreamClient.*Reasoning.*|TestUpstreamClient.*Assistant.*' -v`
Expected: FAIL because the new cache-stability assertion does not exist yet.

**Step 3: Write minimal implementation**

If the code already behaves deterministically, only adjust tests. If a drift is found, change the smallest production code path in `internal/upstream/client.go` needed to make the body stable.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run 'TestUpstreamClient.*Cache.*|TestUpstreamClient.*Reasoning.*|TestUpstreamClient.*Assistant.*' -v`
Expected: PASS

### Task 2: Add failing tests for cached token observability on chat responses

**Files:**
- Modify: `tests/integration/chat_test.go`
- Modify: `tests/integration/streaming_handlers_test.go`
- Test: `tests/integration/chat_test.go`
- Test: `tests/integration/streaming_handlers_test.go`

**Step 1: Write the failing tests**

Add one non-stream test asserting `chat/completions` JSON preserves upstream cache accounting in usage, and one stream test asserting the final usage chunk preserves the same fields.

Use upstream completion events whose `usage` contains prompt/input token detail structures with `cached_tokens`.

**Step 2: Run tests to verify they fail**

Run: `go test ./tests/integration -run 'TestChat.*Cached.*|TestStreaming.*Cached.*' -v`
Expected: FAIL because current chat usage mapping drops cache detail fields.

**Step 3: Write minimal implementation**

Update chat usage mapping in:

- `internal/adapter/chat/response.go`
- `internal/httpapi/streaming.go`

Preserve upstream prompt/input token detail fields in a stable downstream structure without removing the existing `reasoning_tokens` mapping.

**Step 4: Run tests to verify they pass**

Run: `go test ./tests/integration -run 'TestChat.*Cached.*|TestStreaming.*Cached.*' -v`
Expected: PASS

### Task 3: Preserve cache accounting on responses route when available

**Files:**
- Modify: `tests/integration/responses_test.go`
- Modify: `internal/adapter/responses/response.go`
- Test: `tests/integration/responses_test.go`

**Step 1: Write the failing test**

Add a test asserting that when upstream completion data includes usage with cache-related detail fields, `/v1/responses` preserves that usage instead of discarding it.

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run 'TestResponses.*Usage.*|TestResponses.*Cached.*' -v`
Expected: FAIL because the current synthesized responses payload does not expose usage.

**Step 3: Write minimal implementation**

Update `internal/adapter/responses/response.go` to include usage when available, preserving upstream cache-related detail fields without inventing new semantics.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run 'TestResponses.*Usage.*|TestResponses.*Cached.*' -v`
Expected: PASS

### Task 4: Run focused verification and full integration verification

**Files:**
- Modify: `internal/adapter/chat/response.go`
- Modify: `internal/httpapi/streaming.go`
- Modify: `internal/adapter/responses/response.go`
- Modify: `tests/integration/upstream_body_test.go`
- Modify: `tests/integration/chat_test.go`
- Modify: `tests/integration/streaming_handlers_test.go`
- Modify: `tests/integration/responses_test.go`

**Step 1: Run focused test set**

Run: `go test ./tests/integration -run 'TestUpstreamClient|TestChat|TestStreaming|TestResponses' -v`
Expected: PASS

**Step 2: Run full test suite**

Run: `go test ./...`
Expected: PASS

**Step 3: Manual QA**

Run a real proxy-backed request path using an integration stub or local test server and confirm the returned JSON/SSE usage includes cache-related fields when upstream provides them.

**Step 4: Verify diagnostics/build state**

Run: `go test ./...`
Expected: PASS with no new errors introduced.
