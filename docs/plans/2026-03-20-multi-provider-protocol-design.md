# Multi-Provider Protocol Proxy Design

## Goal

Evolve the current single-provider OpenAI-compatible proxy into a multi-provider proxy with provider-specific routing, provider-scoped configuration, provider-level model mapping, native Claude support, and provider-gated reasoning-effort suffix handling.

The new contract must support both:

- provider-scoped OpenAI-style routes, with provider id immediately after the domain
- a legacy compatibility alias for existing `/v1/*` clients through exactly one default provider

All three public request surfaces must be supported by the proxy layer:

1. OpenAI Chat Completions
2. OpenAI Responses
3. Anthropic Messages

The upstream transport assumption for this design is:

- **proxy input to upstream remains Responses-native**
- the proxy may expose three downstream contracts, but upstream dispatch is still built around the Responses interface

This design adopts the following validated defaults:

1. **Provider route placement**: provider id appears immediately after the domain, before protocol suffixes
2. **Legacy compatibility**: `/v1/*` remains supported and resolves to exactly one configured default provider
3. **Config topology**: one config file per provider
4. **Claude scope**: add a native Anthropic Messages endpoint instead of collapsing Claude into the OpenAI public contract
5. **Model suffix handling**: `-low`, `-medium`, `-high`, `-xhigh` are provider-level opt-in behavior, not global behavior

## Why This Shape

The current codebase is a fixed-route, single-upstream adapter:

- downstream routes are hard-coded under `/v1/*`
- runtime config is loaded from process env only
- upstream transport is a single base URL and a single `/responses` family
- no provider abstraction exists

Adding provider-aware routes, provider-specific model aliases, provider-native Claude handling, and per-provider reasoning behavior on top of that requires a new architectural seam:

- a **provider registry** for config and lookup
- a **route resolver** that extracts provider identity from URL shape
- a **request normalization pipeline** that can apply provider-specific model and reasoning transforms
- distinct public protocol contracts for **OpenAI Responses/Chat** and **Anthropic Messages**

## Public API Surface

### OpenAI-style provider-scoped routes

- `POST /{providerId}/v1/chat/completions`
- `POST /{providerId}/v1/responses`
- `GET /{providerId}/v1/models`

These are the primary routes for provider-specific traffic.

### Legacy compatibility routes

- `POST /v1/chat/completions`
- `POST /v1/responses`
- `GET /v1/models`

These remain as aliases to the configured default provider.

Rules:

- there may be **at most one** default provider
- if legacy routes are enabled, exactly one default provider must be resolvable at startup
- if no default provider is configured, legacy `/v1/*` routes must fail fast at startup or be disabled explicitly

### Claude-native provider-scoped route

- `POST /{providerId}/v1/messages`

This route exposes Anthropic-style request and response semantics directly **to downstream callers**, while the proxy may still normalize that request into its internal canonical form and dispatch upstream using the Responses transport.

It is intentionally separate from OpenAI-style routes because:

- request shapes differ (`messages` + top-level `system` vs Responses `input`)
- SSE event names differ
- stop/status semantics differ
- reasoning controls differ

## Compatibility Model

The proxy is now a **three-surface adapter over one upstream transport family**.

- downstream surface A: OpenAI Chat Completions
- downstream surface B: OpenAI Responses
- downstream surface C: Anthropic Messages
- upstream transport: Responses

This means public compatibility must remain contract-specific, but upstream dispatch can remain transport-unified where safe.

### OpenAI-style public contracts

For `/{providerId}/v1/*` and legacy `/v1/*` routes:

- preserve OpenAI-style request and response shapes at the edge
- preserve Responses SSE event names on `/v1/responses`
- preserve Chat chunk semantics on `/v1/chat/completions`
- allow provider-specific internal routing and transforms without changing the public contract

### Anthropic public contract

For `/{providerId}/v1/messages`:

- preserve Anthropic Messages request shape
- preserve Anthropic SSE event names and sequencing
- preserve native `stop_reason`, `usage`, and content-block semantics
- do not emit OpenAI-style chunks on Anthropic routes

Internally, this route may still normalize into the same canonical request model and then emit a Responses-style upstream request body.

## Provider Routing Model

### Route resolution

The first non-empty path segment selects provider identity for provider-scoped routes.

Examples:

- `/openai/v1/chat/completions` → provider `openai`, contract `openai-chat`
- `/openai/v1/responses` → provider `openai`, contract `openai-responses`
- `/anthropic/v1/messages` → provider `anthropic`, contract `anthropic-messages`

Legacy routes skip explicit provider lookup and instead use the configured default provider.

### Provider lookup behavior

- unknown provider id → `404` or `400` with structured proxy error
- disabled provider → reject before upstream call
- provider exists but does not support selected contract → `400` with explicit unsupported-provider-contract error

Provider capability flags describe what the proxy may expose downstream for that provider, even if upstream dispatch is always Responses-native.

## Configuration Model

### Global config

The root `.env` keeps only process-wide settings:

- `APP_NAME`
- `LISTEN_ADDR`
- `PROXY_API_KEY`
- `PROVIDERS_DIR`
- `DEFAULT_PROVIDER`
- `ENABLE_LEGACY_V1_ROUTES`
- global logging and timeout settings

The Go process should load the global env as it does today, then load provider files from `PROVIDERS_DIR`.

### Provider config files

Each provider has its own config file, for example:

- `providers/openai.env`
- `providers/anthropic.env`
- `providers/custom.env`

Each provider should also have an example template with full comments and field visibility:

- `providers/openai.env.example`
- `providers/anthropic.env.example`
- `providers/custom.env.example`

Each `*.env.example` must include:

- every supported field for that provider file
- comments explaining what each field does
- comments marking required vs optional fields
- comments showing allowed values where constrained
- examples for model mapping and reasoning suffix behavior

This is required because the current project relies heavily on documented env-based deployment.

### Provider config field groups

Recommended provider file categories:

1. **Identity and enablement**
   - `PROVIDER_ID`
   - `PROVIDER_ENABLED`
   - `PROVIDER_PROTOCOLS` (`openai`, `anthropic`, or both as allowed by implementation)
   - `PROVIDER_IS_DEFAULT`

2. **Upstream targeting**
   - `UPSTREAM_BASE_URL`
   - `UPSTREAM_API_KEY`
   - optional auth mode / header overrides if later needed

3. **Model mapping**
   - `MODEL_MAP_JSON` or equivalent structured mapping source
   - recommended semantics: public model name → upstream model name

4. **Reasoning suffix behavior**
   - `ENABLE_REASONING_EFFORT_SUFFIX`
   - `REASONING_SUFFIX_VALUES` or fixed built-in suffix map
   - provider-specific effort mapping target type (`openai_reasoning`, `anthropic_output_config`, etc.)

5. **Provider capabilities**
   - `SUPPORTS_RESPONSES`
   - `SUPPORTS_CHAT`
   - `SUPPORTS_MODELS`
   - `SUPPORTS_ANTHROPIC_MESSAGES`

The exact field names can be adjusted during implementation, but the shape should remain provider-local rather than returning to one giant shared `.env`.

## Canonical Request and Provider Context

The current canonical model should gain explicit provider context rather than keeping provider selection outside the request pipeline.

Because all three downstream contracts ultimately target one upstream Responses transport, the canonical model becomes the stable bridge between:

- OpenAI Chat request decoding
- OpenAI Responses request decoding
- Anthropic Messages request decoding
- unified upstream Responses request building
- route-specific downstream response encoding

Recommended additions:

- `ProviderID string`
- `PublicRoute string`
- `RequestedModel string`
- `ResolvedModel string`
- `Contract string`

This makes logging, debugging, and future retries/provider-specific transforms much easier.

## Transformation Pipeline

The transformation order must be explicit to avoid subtle model alias bugs.

Recommended order:

1. resolve provider from route or default alias
2. load provider capabilities and switches
3. decode downstream request into canonical request
4. record original public model as `RequestedModel`
5. **conditionally** parse reasoning suffix from model name if provider-level suffix handling is enabled
6. if suffix handling was applied, strip suffix and attach provider-native reasoning intent to canonical request
7. apply provider model mapping using the suffix-stripped base model
8. store final upstream model as `ResolvedModel`
9. build upstream **Responses** request using provider protocol rules

Important rule:

- if provider-level suffix handling is **disabled**, recognized suffixes are not stripped and the model string remains untouched for normal mapping or upstream pass-through

This prevents hidden behavior changes for providers that do not opt into suffix semantics.

## Model Mapping Semantics

Model mapping is provider-scoped only.

Examples:

- public `gpt-5` may map to provider A upstream `gpt-5.4`
- the same public `gpt-5` may map to provider B upstream `claude-sonnet-4-6`
- a provider with no explicit mapping may pass the public model through unchanged

Model mapping should not be global because the same public alias can legitimately resolve to different upstream models per provider.

## Reasoning Suffix Semantics

Supported suffixes:

- `-low`
- `-medium`
- `-high`
- `-xhigh`

Behavior:

- feature is gated per provider
- when enabled and suffix is recognized, the suffix is removed from the model before alias lookup
- the suffix becomes provider-native reasoning intent
- when disabled, the full model string is left unchanged

Provider-native mapping targets:

- OpenAI-style upstreams: map into `reasoning.effort`
- Anthropic-style upstreams: map into provider-specific `thinking` / `output_config.effort` fields as supported

Unknown suffix behavior should be conservative:

- only the four supported suffixes are interpreted specially
- any other suffix remains part of the model name

## Upstream Transport Rule

All provider dispatch in this change set is based on the Responses upstream interface.

That means:

- OpenAI Chat downstream → canonical request → upstream Responses
- OpenAI Responses downstream → canonical request → upstream Responses
- Anthropic Messages downstream → canonical request → upstream Responses

This preserves the current project's strongest architectural asset: one upstream transport path.

It also means Claude support in this change set is a **public contract adapter**, not a requirement to add a second upstream transport family.

## Responses Endpoint Behavior

The existing `/v1/responses` route already exists, but its provider-scoped version must gain the same maturity as chat routing:

- provider resolution
- provider auth resolution
- provider model mapping
- provider-level reasoning suffix handling
- provider-aware upstream selection

The response endpoint should preserve Responses-native semantics while reusing shared upstream reliability and normalization improvements where safe.

## Claude Endpoint Behavior

The new Claude route should be implemented as a native Anthropic Messages contract at the proxy edge, but translated internally into the same canonical request and upstream Responses transport used elsewhere.

Required properties:

- accept Anthropic-compatible headers and request body
- preserve Messages-native streaming events
- preserve tool-use and stop-reason semantics
- use provider configuration to decide whether Anthropic Messages is supported

Internal rule:

- Anthropic downstream request in
- canonical request in the middle
- upstream Responses request out
- upstream Responses stream/result back in
- Anthropic Messages response/stream back out

OpenAI-style routes may still target a Claude-like provider internally through canonical translation, but that does **not** replace the need for a native Claude route.

## Startup Validation Rules

The service should fail fast on invalid config combinations.

Validation rules:

- provider ids must be unique
- at most one provider may declare itself default
- if `ENABLE_LEGACY_V1_ROUTES=true`, exactly one default provider must exist
- provider-scoped route support must match provider capability flags
- required upstream credentials must be present for enabled providers
- provider example files are documentation artifacts and must not be treated as live config

## Logging and Observability

Logs should include provider-aware fields:

- `provider_id`
- `contract`
- `public_route`
- `requested_model`
- `resolved_model`
- `used_default_provider`
- `reasoning_suffix_applied`
- `reasoning_effort`

This is necessary because route, model, and provider behavior will no longer be inferred from a single global upstream.

## Branch Strategy

The user requested one branch per feature. The recommended merge order is therefore:

1. **Feature 3 branch** — provider config files, provider registry, provider-scoped routing, default-provider legacy aliasing, provider `.env.example` templates
2. **Feature 2 branch** — provider-level model mapping
3. **Feature 1 branch** — provider-scoped Responses parity and native Claude endpoint
4. **Feature 4 branch** — provider-level reasoning suffix mapping

This is still one branch per feature, but merged in dependency order.

## Non-Goals for This Change Set

- cross-provider failover between different providers on one request
- automatic conversion of Anthropic Messages SSE into OpenAI SSE on public Anthropic routes
- silent fallback from unknown provider ids to the default provider
- global model alias tables shared across all providers

## Success Criteria

This design is complete when the implementation delivers:

- provider id immediately after domain for scoped routes
- backward-compatible `/v1/*` alias through exactly one default provider
- one provider config file per provider plus fully commented provider `*.env.example` templates
- provider-level model mapping
- native Claude Messages endpoint at the proxy edge, backed by the same upstream Responses transport
- provider-gated reasoning suffix handling
- startup validation for invalid provider/default combinations
