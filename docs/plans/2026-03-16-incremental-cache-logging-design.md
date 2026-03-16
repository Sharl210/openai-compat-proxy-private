# Incremental Cache Repair, Restart Script, and Logging Design

## Goal

Fix the proxy-side cache miss behavior for real chat conversations that grow one turn at a time, add a minimal restart script, and introduce a production-safe logging system that makes cache-relevant request/response behavior observable.

## Problem Statement

The upstream cache is known to work. The remaining issue appears on the proxy path during real multi-turn chat growth: repeated identical requests can hit cache, but normal conversations that append one message at a time fail to reuse cache as expected.

This strongly suggests the proxy’s compatibility layer is changing the upstream prompt-bearing prefix in ways that are stable for exact replay but unstable for realistic incremental conversation growth.

At the same time, the project lacks a structured logging system for:

- downstream requests entering the proxy
- canonicalized and rebuilt upstream requests leaving the proxy
- upstream usage/cache data returning to the proxy
- correlation between those steps by request id

The project also has install/uninstall scripts but no explicit restart wrapper.

## Scope

This design covers exactly three tasks:

1. Repair cache behavior for `POST /v1/chat/completions` in the real multi-turn append-one-message-at-a-time path.
2. Add a `restart` script that sequentially invokes the uninstall script and install/deploy script.
3. Add a structured logging system with default-redacted output, capturing downstream traffic, upstream traffic, input/output summaries, and cache-related metadata.

## Non-Goals

This design does not include:

- rewriting the proxy into a transparent passthrough
- changing upstream providers or cache policies
- storing full raw prompt/response content by default
- changing the existing responses compatibility contract unless required by the chat cache fix

## Current Relevant Architecture

The cache-sensitive path today is:

- `/v1/chat/completions` in `internal/httpapi/handlers_chat.go`
- decode and canonicalize in `internal/adapter/chat/request.go`
- rebuild upstream `/responses` payload in `internal/upstream/client.go`
- stream or aggregate upstream SSE in `internal/httpapi/streaming.go` and `internal/aggregate/collector.go`
- remap usage back to chat-compatible shapes in `internal/adapter/chat/response.go`

The most important request-side normalizations today are:

- upstream requests always use `/responses`
- upstream requests always force `stream: true`
- assistant history text is rewritten to `output_text`
- non-assistant text is rewritten to `input_text`
- tool messages become `function_call_output`
- assistant tool calls are replayed as separate `function_call` items
- reasoning objects may gain `summary: "auto"`
- tool schemas may be normalized by injecting `items: {}` for arrays

These rules are deterministic, but real multi-turn cache misses suggest one or more of them are producing an unstable or cache-hostile prefix across incremental turns.

## Root-Cause Hypothesis

The likely root cause is not usage remapping. It is upstream request-body construction for growing chat history.

The strongest hypotheses are:

1. chat history replay shape differs from what the upstream cache expects across appended turns
2. replayed assistant/tool/reasoning content causes earlier prefix regions to shift unnecessarily
3. forced upstream streaming or compatibility-only injected fields change the effective cacheable prefix across turns

The repair should therefore begin with executable reproduction tests around appended multi-turn history, then minimize prefix drift in the rebuilt `/responses` body without discarding required compatibility behavior.

## Logging Design

### Logging policy

Logging is dual-channel:

- concise summaries to stdout
- structured JSON records to a local file

Default policy is redacted.

By default the logger records metadata, hashes, counts, lengths, route info, cache/accounting fields, and selected summaries — not full raw prompt/response bodies.

Full raw content is only captured behind an explicit debug switch.

### Logging levels and records

The system should emit structured records for these stages:

1. **downstream_request_received**
   - request id
   - path/method
   - auth mode used
   - normalization version
   - stream/include_usage flags
   - model
   - message/tool counts
   - content lengths and prefix hash

2. **canonical_request_built**
   - canonical message roles/types
   - reasoning/tool-choice presence
   - canonical prefix hash

3. **upstream_request_built**
   - upstream endpoint/path
   - rebuilt request body summary
   - upstream prefix hash
   - request byte length
   - cache-relevant field summary

4. **upstream_response_completed**
   - status code / errors / retry count
   - upstream usage
   - cached token fields if present
   - elapsed time

5. **downstream_response_sent**
   - route kind (chat/responses)
   - stream vs non-stream
   - mapped usage/cache fields sent to client
   - elapsed time

### Redaction rules

Default-redacted logging should include:

- body hash
- prefix hash
- message count
- role sequence
- content byte counts
- tool names (but not arguments by default)
- cache fields and usage fields

Default-redacted logging should exclude:

- full message text
- tool arguments
- upstream/downstream Authorization values

### Insertion points

- `internal/httpapi/server.go`: request-level logging middleware
- `internal/httpapi/streaming.go`: stream event/usage chunk logging
- `internal/upstream/client.go`: upstream request/response logging and retry/status logging
- `cmd/proxy/main.go`: logger initialization and file/stdout wiring

## Restart Script Design

Add one minimal Linux script:

- `scripts/restart-linux.sh`

Behavior:

1. resolve project root
2. invoke `scripts/uninstall-linux.sh`
3. invoke `scripts/deploy-linux.sh`

No extra orchestration is required in the first version.

## Chosen Approach

### Option A — Recommended

- add a failing multi-turn incremental cache reproduction test first
- repair only the cache-hostile normalization behavior exposed by that test
- add structured logging in request/canonical/upstream/downstream stages with default redaction
- add the minimal restart wrapper script

Pros:

- addresses the actual bug, not just observability
- keeps changes scoped and test-driven
- produces the logs needed to validate the cache repair in real traffic

Cons:

- requires touching multiple layers of the request path

### Option B — Logging first, cache fix later

Pros:

- faster initial observability

Cons:

- does not solve the bug
- delays confidence until after more live debugging

### Option C — Reduce compatibility normalization broadly

Pros:

- may move behavior closer to direct upstream requests

Cons:

- high regression risk
- can break existing compatibility assumptions
- too broad for the current evidence

## Recommendation

Implement Option A.

That keeps the work focused on the real bug, gives immediate operational visibility, and avoids speculative rewrites of the compatibility layer.

## Testing Strategy

The implementation should be TDD-first.

### Group 1: incremental cache reproduction

Add tests that build a growing multi-turn chat history and assert the rebuilt upstream request body keeps the previously shared prefix stable across turns where the conversation only appends a new message.

### Group 2: logging behavior

Add tests for:

- request id + route metadata logging
- cache/accounting field logging
- redaction of bodies/authorization by default
- file log creation and JSON record shape

### Group 3: restart wrapper

Add a shell test ensuring restart invokes uninstall then deploy in order.

## Rollout Notes

- logging should default to safe redaction and low operational surprise
- cache repair should preserve current compatibility behavior except where the new failing test proves it is causing cache-hostile prefix drift
- if a safe repair cannot be done without changing the current normalization contract, bump the normalization version rather than silently mutating `v1`
