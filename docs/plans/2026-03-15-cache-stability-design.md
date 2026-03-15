# Cache Stability Mode Design

## Goal

Keep upstream prompt caching stable when traffic consistently flows through `openai-compat-proxy`.

This design does **not** try to preserve byte-for-byte compatibility with historical direct-to-upstream requests. Instead, it formalizes the proxy as a **stable normalization gateway**: the same downstream request shape must always produce the same upstream prompt-bearing prefix, routing identity, and observable cache accounting.

## Non-Goals

This design does **not** promise:

- reuse of upstream cache entries created before clients switched to the proxy
- transparent direct-connect equivalence
- elimination of all request rewriting
- changing current compatibility behavior unless required for determinism or cache observability

## Current State

The proxy already behaves more like a deterministic adapter than a transparent relay:

- all upstream model calls are rebuilt as `POST /responses`
- upstream requests are forced to `stream=true`
- chat history is normalized into canonical messages
- assistant text history is emitted as `output_text`
- reasoning payloads may gain `summary: "auto"`
- tool schemas may be normalized for array `items`

This is acceptable for cache stability **if and only if** the normalization remains fixed and test-locked.

## Problem Statement

Two gaps remain:

1. The project relies on behavior that is deterministic in practice, but not explicitly protected as a cache-stability contract.
2. The proxy rewrites usage for downstream chat responses, but does not currently preserve upstream cached-token accounting fields, making cache hits hard to observe and verify.

## Design Principles

### 1. Stable normalization over transparent pass-through

The proxy should keep its current normalization strategy when that strategy is already needed for compatibility. The key requirement is that the normalization must be:

- deterministic
- unconditional for a given input shape
- free of time-based or request-unique prompt mutations
- covered by tests that lock output shape

### 2. Stable upstream cache scope

Requests that should share cache must keep a stable upstream cache scope. That means the proxy must avoid accidental drift in:

- upstream base URL / deployment target
- upstream authorization source selection
- model identifier used in the upstream request

The current auth policy is acceptable because it is deterministic per request, but the design will document that cache stability assumes a stable upstream account/project/model path in deployment.

### 3. Full cache observability

The proxy must expose upstream cache accounting fields instead of silently dropping them. This does not affect whether the upstream cache hits, but it is required to validate that the proxy is not degrading cache behavior.

## Chosen Approach

The proxy will adopt an explicit **cache stability mode** by tightening and documenting the existing normalization contract rather than replacing it.

This means:

- keep current request normalization behavior for chat/history/tools/reasoning
- add tests that lock the exact prompt-bearing upstream body shape for cache-sensitive cases
- preserve and map upstream cache-related usage fields into downstream responses

## Request-Side Contract

The following behaviors are treated as intentional and stable:

- upstream transport uses `/responses`
- upstream request body always includes `stream: true`
- assistant replay text maps to `output_text`
- user/system/developer text maps to `input_text`
- `reasoning.summary` defaults to `auto` when reasoning is present but summary is omitted
- tool schema normalization adds `items: {}` for array nodes missing `items`

These rules must not silently drift. If future changes are needed, they should be treated as compatibility-version changes because they can alter upstream cache keys.

## Response-Side Contract

The proxy should preserve cache observability by mapping upstream usage details into downstream usage payloads.

At minimum, when present upstream, downstream responses should preserve:

- `cached_tokens`
- other prompt/input token detail fields relevant to cache accounting

For `chat/completions`, these should appear under the appropriate `prompt_tokens_details` or equivalent downstream-compatible structure rather than being dropped.

For `responses`, usage should remain available without lossy filtering.

## Explicit Normalization Version

The proxy should expose the active normalization contract in a machine-readable way for normalized routes.

For the current contract, `POST /v1/chat/completions` and `POST /v1/responses` return:

- `X-Proxy-Normalization-Version: v1`

This gives operators a concrete signal for cache-prefix compatibility. Any future change to prompt-bearing normalization rules should increment this version rather than silently mutating `v1` semantics.

## Testing Strategy

The implementation will use TDD and add tests in two groups.

### Group A: request normalization stability

Lock the upstream request body for cache-sensitive transformations:

- reasoning object with omitted summary becomes stable `summary: "auto"`
- assistant history remains `output_text`
- tool schema array normalization remains stable
- repeated equivalent chat requests produce the same upstream body

### Group B: cache usage observability

Lock downstream response mapping so upstream cache fields survive translation:

- non-stream chat response preserves `cached_tokens`
- stream chat usage chunk preserves `cached_tokens`
- responses route preserves upstream usage/cache fields where available

## Alternatives Considered

### Option A — Recommended: stable normalization + observability

Pros:

- lowest regression risk
- preserves current compatibility behavior
- directly aligned with proxy-only cache stability

Cons:

- does not preserve direct-connect cache identity

### Option B — make proxy more transparent

Pros:

- potentially closer to upstream-native request shapes

Cons:

- higher compatibility regression risk
- more likely to break existing chat/request handling assumptions
- unnecessary for the stated goal

### Option C — observability only

Pros:

- smallest code change

Cons:

- does not formally protect normalization stability
- leaves cache-key drift risk unbounded

## Recommendation

Implement Option A.

That gives the project a clear engineering contract: **the proxy owns normalization, and normalization is stable enough to preserve upstream cache behavior for clients that always use the proxy**.

## Rollout Notes

- No config flag is required for the first version because the chosen behavior mostly formalizes existing behavior.
- If future compatibility changes threaten cache stability, introduce an explicit versioned normalization mode instead of silently changing body shape.
