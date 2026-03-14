# Go OpenAI Compatibility Proxy Design

## Goal

Build a deployable Go single-binary proxy that sits in front of a flaky upstream OpenAI-like Responses API and exposes two stable downstream endpoints:

- `POST /v1/responses`
- `POST /v1/chat/completions`

The proxy must internally normalize all requests to upstream streaming `POST /v1/responses`, add non-streaming compatibility by aggregating SSE, and support three auth modes:

1. proxy access key for clients calling the proxy
2. client-provided upstream Bearer key pass-through
3. server-side default upstream API key fallback

This revised design expands v1 scope beyond text-only. The proxy now targets:

- tools / function calling compatibility
- reasoning field compatibility
- image input and multimodal content-part support

The compatibility rule is a **mixed mode**:

- **tools**: strong compatibility
- **reasoning**: strong compatibility
- **image / multimodal**: best-effort pass-through plus necessary translation

## Non-Goals for v1

The first version still does **not** promise:

- full byte-for-byte OpenAI parity across every obscure field
- guaranteed lossless conversion of every exotic multimodal output into Chat format
- hosted tools such as web search / file search / computer use unless the upstream shape is already sufficiently compatible
- `n > 1`
- background responses lifecycle features

The v1 promise is: stable protocol adaptation, stream/non-stream correctness, strong tools/reasoning compatibility, multimodal input support, predictable failures, and explicit rejection when safe mapping is impossible.

## Why This Shape

The upstream is reported to behave like a Responses API but is not fully compliant, some clients hang waiting for a terminal event, and non-streaming is not supported. A blind reverse proxy would simply preserve those failures. The proxy must therefore be a protocol adapter with:

- timeout control
- SSE normalization
- aggregation for non-stream callers
- route-specific response mapping
- validation around unsupported or ambiguous mappings

The cleanest architecture remains:

- two downstream adapters
- one canonical internal request/response model
- one upstream transport path
- two downstream response mappers
- one aggregation state machine

This keeps Chat and Responses behavior aligned while containing complexity in dedicated translation layers.

## Deployment Model

- One Go binary
- Environment-variable based configuration
- No external state store
- Stateless request handling
- Can run behind Nginx, Caddy, systemd, Docker, or bare process manager

## API Surface

### Downstream endpoints

- `POST /v1/responses`
- `POST /v1/chat/completions`
- `GET /healthz`

Optional future additions such as `/readyz` or `/metrics` can be added later.

### Upstream endpoint

- `POST {UPSTREAM_BASE_URL}/responses`

Internally the proxy always requests upstream with `stream=true`, even when the downstream caller requested non-streaming.

## Canonical Internal Model

Both downstream APIs are translated into a single internal request form.

### CanonicalRequest

- `Model string`
- `Stream bool`
- `Instructions string`
- `Messages []CanonicalMessage`
- `Temperature *float64`
- `TopP *float64`
- `MaxOutputTokens *int`
- `Stop []string`
- `Tools []CanonicalTool`
- `ToolChoice CanonicalToolChoice`
- `Reasoning *CanonicalReasoning`
- `RequestID string`
- `AuthMode string`

### CanonicalMessage

- `Role string`
- `Parts []CanonicalContentPart`

### CanonicalContentPart

- `Type string` (`text`, `image_url`, `input_image`, `output_text`, or passthrough-compatible internal marker)
- `Text string`
- `ImageURL string`
- `MimeType string`
- `Raw map[string]any`

### CanonicalTool

- `Type string`
- `Name string`
- `Description string`
- `Parameters map[string]any`
- `Raw map[string]any`

### CanonicalToolChoice

- `Mode string`
- `Name string`
- `Raw map[string]any`

### CanonicalReasoning

- `Effort string`
- `Summary string`
- `Raw map[string]any`

## Mapping Rules

### Chat Completions → CanonicalRequest

- `model` → `Model`
- `stream` → `Stream`
- `messages`:
  - `system` and `developer` messages are concatenated into `Instructions` when safe
  - `user` and `assistant` messages become `Messages`
  - text content maps to `CanonicalContentPart{Type:"text"}`
  - image items map to `CanonicalContentPart{Type:"image_url"}` or equivalent canonical input image part
- `max_tokens` → `MaxOutputTokens`
- `temperature`, `top_p`, `stop` map directly
- `tools` and `tool_choice` map into canonical tools
- reasoning-related request fields map into `CanonicalReasoning`

If a content shape or tool shape cannot be safely mapped, reject with `400` rather than silently degrading.

### Responses → CanonicalRequest

- `model` → `Model`
- `stream` → `Stream`
- `instructions` → `Instructions`
- `input` message or content-part arrays map into `Messages`
- `max_output_tokens` → `MaxOutputTokens`
- `temperature`, `top_p`, `stop` map directly
- `tools`, `tool_choice` map into canonical tools
- `reasoning.*` maps into `CanonicalReasoning`

Complex but structurally valid multimodal input is accepted as long as the canonical model can preserve enough structure to re-emit it upstream.

## Request Flow

Every request passes through the same pipeline:

1. assign `request_id`
2. validate proxy access key if enabled
3. parse downstream route type (`responses` or `chat`)
4. validate supported v1 fields
5. translate to `CanonicalRequest`
6. resolve upstream Bearer token
7. build upstream streaming responses request
8. stream-read upstream SSE
9. route to:
   - streaming mapper, or
   - aggregation state machine + final JSON writer

## Authentication Strategy

### Layer 1: Proxy access control

If `PROXY_API_KEY` is configured, the caller must send:

`Authorization: Bearer <proxy-key>`

If not configured, proxy-level auth is disabled.

### Layer 2: Upstream key resolution

After proxy access is authorized, the upstream key is selected:

1. use client-provided upstream key if pass-through is enabled and supplied via dedicated header or configured mode
2. otherwise use `UPSTREAM_API_KEY`
3. if neither exists, reject with `401` or `500` depending on configuration error vs caller omission

### Practical header policy

Recommended v1 contract:

- `Authorization: Bearer <proxy-key>` for proxy access
- `X-Upstream-Authorization: Bearer <upstream-key>` for caller-supplied upstream auth

If proxy auth is disabled, `Authorization` may be used directly as upstream auth.

## Streaming Strategy

The upstream is treated as the streaming Responses source of truth.

### Responses downstream, `stream=true`

- Read upstream SSE events
- Normalize malformed-but-recoverable framing when possible
- Forward supported Responses-style events
- Preserve tools and reasoning events where safely possible
- Preserve multimodal output events on the Responses route when they can be represented directly
- Ensure the downstream caller always receives a terminal event or explicit proxy error

### Chat downstream, `stream=true`

- Read upstream SSE events
- Extract text deltas into `chat.completion.chunk`
- Translate tool-call deltas into `delta.tool_calls`
- Emit a final chunk with `finish_reason`
- Emit `data: [DONE]`

If an upstream multimodal output cannot be safely represented in Chat chunk form, the proxy must fail explicitly instead of emitting a misleading partial structure.

## Non-Streaming Strategy

When `stream=false`, the proxy still makes a streaming upstream request and aggregates the result.

### Aggregation responsibilities

The collector is no longer a simple text buffer. It is a state machine that tracks:

- assistant text deltas
- tool call lifecycle
- tool argument deltas
- content parts / multimodal output items
- reasoning metadata
- completion / incomplete status
- finish-reason inference

### Responses downstream, `stream=false`

Return a synthesized OpenAI-style response object:

- `object: "response"`
- `status: "completed"` when fully aggregated
- `output` array preserving assistant message content, tool outputs when applicable, and reasoning fields when safely representable

### Chat downstream, `stream=false`

Return a synthesized chat completion object:

- `object: "chat.completion"`
- one `choices[0]`
- `message.role = "assistant"`
- `message.content = <aggregated text or compatible content parts>`
- `message.tool_calls` when tool calls occurred
- `finish_reason` inferred conservatively

Reasoning is strongly mapped where a sane correspondence exists, but not fabricated if upstream semantics do not match Chat expectations.

## Tools Compatibility Strategy

### Request-side

- Accept Chat `tools` and `tool_choice`
- Accept Responses `tools` and `tool_choice`
- Normalize into canonical tools with raw preservation for upstream re-emission

### Streaming response-side

- On Responses route: preserve or normalize tool-related events in Responses style
- On Chat route: convert tool call deltas into `delta.tool_calls[*]` chunks
- Maintain stable tool-call indices during stream translation

### Non-stream response-side

- Responses route: include tool-related output items where they belong in the synthesized response object
- Chat route: emit `choices[0].message.tool_calls`

### Failure policy

Reject or fail safely when:

- tool arguments are irreparably malformed
- tool delta order is inconsistent
- terminal state is missing and no safe synthesis is possible

The proxy must not silently produce broken tool calls that downstream clients cannot parse.

## Reasoning Compatibility Strategy

### Request-side

- map Chat-side reasoning fields into canonical reasoning
- map Responses-side `reasoning.*` into canonical reasoning

### Response-side

- Responses route: preserve upstream reasoning data where available
- Chat route: preserve safely mappable reasoning metadata only when it does not break Chat shape expectations

Reasoning is a strong compatibility area, but not an excuse to invent semantics the upstream did not provide.

## Multimodal Compatibility Strategy

### Input

Support at minimum:

- text content parts
- image URL style input
- responses-compatible image input parts

### Output

- Responses route: preserve multimodal outputs where representable
- Chat route: perform necessary conversion when possible, otherwise fail explicitly with an unsupported mapping error

This is intentionally not “full multimodal strong normalization.” The proxy supports multimodal input and best-effort output translation, but does not claim perfect Chat representation for every exotic output shape.

## Error Handling

The proxy standardizes errors into an OpenAI-like envelope:

```json
{
  "error": {
    "message": "...",
    "type": "proxy_error",
    "param": null,
    "code": "..."
  }
}
```

### Error classes

- `400` unsupported field or malformed client input
- `401` missing or invalid auth
- `502` malformed upstream response or protocol violation
- `504` upstream connect / idle / total timeout

### Important behavior

- never leave the client hanging on missing upstream terminal event
- never leak upstream API keys in error payloads
- reject unsupported semantics explicitly instead of silently ignoring them
- reject impossible route-specific mappings instead of returning misleading partial success

## Timeouts

Expose independent timeout controls:

- connect timeout
- first-byte timeout
- idle chunk timeout
- total request timeout

This is mandatory because the upstream failure mode includes “no response” / hanging behavior.

## Logging and Redaction

Structured logs should include:

- request id
- route type
- auth mode used
- upstream status class
- total latency
- timeout/error class

Must never log by default:

- `Authorization`
- `X-Upstream-Authorization`
- raw tool arguments if sensitive
- full prompt bodies
- raw upstream payloads with secrets

## Proposed Project Layout

```text
openai-compat-proxy/
  cmd/proxy/main.go
  go.mod
  internal/config/config.go
  internal/httpapi/server.go
  internal/httpapi/middleware.go
  internal/httpapi/handlers_chat.go
  internal/httpapi/handlers_responses.go
  internal/httpapi/handlers_health.go
  internal/auth/auth.go
  internal/model/canonical.go
  internal/adapter/chat/request.go
  internal/adapter/chat/response.go
  internal/adapter/responses/request.go
  internal/adapter/responses/response.go
  internal/upstream/client.go
  internal/upstream/sse.go
  internal/aggregate/collector.go
  internal/errorsx/errors.go
  internal/testutil/upstream_stub.go
  tests/integration/chat_test.go
  tests/integration/responses_test.go
  tests/integration/auth_test.go
  tests/integration/tools_test.go
  tests/integration/multimodal_test.go
  tests/integration/reasoning_test.go
  tests/integration/timeout_test.go
  docs/plans/
```

## Validation Strategy

The implementation should be validated with fake upstream servers that simulate:

- healthy text streaming
- healthy tool-call streaming
- reasoning metadata in normal and weird positions
- image input pass-through acceptance
- missing terminal event
- idle connection without chunks
- invalid SSE frame
- malformed JSON body
- mid-stream disconnect
- malformed tool argument deltas

The acceptance bar is predictable behavior under broken upstream conditions, not just happy-path field translation.

## Open Questions Carried Forward Into Implementation

These are intentionally constrained so implementation does not sprawl:

1. whether to expose `/readyz` in v1 or defer to `/healthz`
2. whether to add `/metrics` in v1 or later
3. whether to permit compatibility fallback from `Authorization` to upstream auth when proxy auth is enabled
4. which exact multimodal output shapes are accepted on Chat route vs rejected as unsupported mapping

None of these block the core proxy.
