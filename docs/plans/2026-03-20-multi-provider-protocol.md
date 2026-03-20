# Multi-Provider Protocol Proxy Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add provider-scoped routing, per-provider config files, provider-level model mapping, native Claude Messages output, and provider-gated reasoning suffix behavior while keeping upstream dispatch unified on the Responses interface.

**Architecture:** The proxy will expose three downstream contracts — OpenAI Chat, OpenAI Responses, and Anthropic Messages — but still normalize all requests into one canonical request model and dispatch upstream through the Responses transport. Provider resolution happens from `/{providerId}/...` routes or the legacy `/v1/*` default-provider alias.

**Tech Stack:** Go 1.22+, standard library `net/http`, `httptest`, `encoding/json`, existing integration test suite, env-file parsing for provider files.

---

### Task 1: Add provider-aware global config and startup validation

**Files:**
- Modify: `internal/config/config.go`
- Modify: `cmd/proxy/main.go`
- Modify: `.env.example`
- Test: `tests/integration/config_test.go`

**Step 1: Write the failing tests**

Add tests covering:

- `DEFAULT_PROVIDER` is loaded from env
- `ENABLE_LEGACY_V1_ROUTES` is loaded from env
- missing `DEFAULT_PROVIDER` is rejected when legacy routes are enabled
- duplicate or ambiguous default providers are rejected during config validation

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run 'TestLoadFromEnv|TestProviderConfigValidation' -v`
Expected: FAIL because provider-aware global config and validation do not exist yet.

**Step 3: Write minimal implementation**

Extend `Config` with fields such as:

- `ProvidersDir string`
- `DefaultProvider string`
- `EnableLegacyV1Routes bool`

Add a validation step during startup so invalid provider/default combinations fail fast.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run 'TestLoadFromEnv|TestProviderConfigValidation' -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/config/config.go cmd/proxy/main.go .env.example tests/integration/config_test.go
git commit -m "feat: add provider-aware global config and validation"
```

### Task 2: Add provider registry and per-provider env file loading

**Files:**
- Create: `internal/config/providers.go`
- Create: `internal/config/provider.go`
- Create: `tests/integration/provider_config_test.go`
- Create: `providers/openai.env.example`
- Create: `providers/anthropic.env.example`
- Create: `providers/custom.env.example`

**Step 1: Write the failing tests**

Add tests covering:

- provider files are loaded from `PROVIDERS_DIR`
- provider ids are unique
- disabled providers are present but not routable
- provider example files are documentation only and not treated as live config

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestProviderConfigLoading -v`
Expected: FAIL because provider registry loading does not exist.

**Step 3: Write minimal implementation**

Create a provider schema that supports:

- provider id
- enabled flag
- default flag
- upstream base URL and API key
- supported downstream contracts
- model mapping source
- reasoning suffix switch

Create provider example files with:

- full field list
- required/optional comments
- allowed-value comments
- example mapping comments

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestProviderConfigLoading -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/config/providers.go internal/config/provider.go tests/integration/provider_config_test.go providers/openai.env.example providers/anthropic.env.example providers/custom.env.example
git commit -m "feat: add provider registry and documented provider env templates"
```

### Task 3: Add provider-aware route resolution and legacy `/v1` aliasing

**Files:**
- Modify: `internal/httpapi/server.go`
- Create: `internal/httpapi/routes.go`
- Modify: `internal/httpapi/middleware.go`
- Test: `tests/integration/provider_routes_test.go`

**Step 1: Write the failing tests**

Add tests covering:

- `/{providerId}/v1/chat/completions` resolves provider correctly
- `/{providerId}/v1/responses` resolves provider correctly
- `/{providerId}/v1/models` resolves provider correctly
- `/{providerId}/v1/messages` resolves provider correctly
- `/v1/*` uses exactly the configured default provider
- unknown provider returns structured error

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestProviderScopedRoutes -v`
Expected: FAIL because fixed-route matching is still hard-coded.

**Step 3: Write minimal implementation**

Add a route parser that determines:

- contract family
- provider id
- whether default-provider aliasing was used

Thread provider identity into request handling without breaking `/healthz`.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestProviderScopedRoutes -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/httpapi/server.go internal/httpapi/routes.go internal/httpapi/middleware.go tests/integration/provider_routes_test.go
git commit -m "feat: add provider-scoped routes and default-provider aliases"
```

### Task 4: Extend the canonical model with provider and contract context

**Files:**
- Modify: `internal/model/canonical.go`
- Test: `tests/integration/canonical_model_test.go`

**Step 1: Write the failing test**

Add assertions for canonical request fields such as:

- `ProviderID`
- `Contract`
- `RequestedModel`
- `ResolvedModel`

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestCanonicalModelSupportsProviderContext -v`
Expected: FAIL because canonical provider context does not exist.

**Step 3: Write minimal implementation**

Extend `CanonicalRequest` with provider-aware routing and model fields without weakening existing request behavior.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestCanonicalModelSupportsProviderContext -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/model/canonical.go tests/integration/canonical_model_test.go
git commit -m "feat: add provider and contract context to canonical requests"
```

### Task 5: Add provider-level model mapping

**Files:**
- Create: `internal/modelmap/modelmap.go`
- Modify: `internal/httpapi/handlers_chat.go`
- Modify: `internal/httpapi/handlers_responses.go`
- Modify: `internal/upstream/client.go`
- Test: `tests/integration/provider_model_mapping_test.go`

**Step 1: Write the failing tests**

Add tests covering:

- the same public model maps differently per provider
- unmapped models pass through unchanged
- resolved upstream model is visible in logs or request construction hooks

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestProviderModelMapping -v`
Expected: FAIL because model mapping does not exist.

**Step 3: Write minimal implementation**

Add a provider-local alias resolver that maps `RequestedModel` to `ResolvedModel` after provider resolution.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestProviderModelMapping -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/modelmap/modelmap.go internal/httpapi/handlers_chat.go internal/httpapi/handlers_responses.go internal/upstream/client.go tests/integration/provider_model_mapping_test.go
git commit -m "feat: add provider-scoped model mapping"
```

### Task 6: Add provider-gated reasoning suffix parsing

**Files:**
- Create: `internal/reasoning/suffix.go`
- Modify: `internal/httpapi/handlers_chat.go`
- Modify: `internal/httpapi/handlers_responses.go`
- Test: `tests/integration/reasoning_suffix_test.go`

**Step 1: Write the failing tests**

Add tests covering:

- `-low`, `-medium`, `-high`, `-xhigh` are parsed only when provider switch is enabled
- suffix is stripped before provider alias lookup when enabled
- suffix is left untouched when disabled
- unknown suffixes remain part of the model

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestReasoningSuffixHandling -v`
Expected: FAIL because suffix handling does not exist.

**Step 3: Write minimal implementation**

Implement a helper that:

- examines the requested model
- conditionally strips supported suffixes
- records the selected reasoning effort
- leaves the model unchanged if the provider has not enabled the feature

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestReasoningSuffixHandling -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/reasoning/suffix.go internal/httpapi/handlers_chat.go internal/httpapi/handlers_responses.go tests/integration/reasoning_suffix_test.go
git commit -m "feat: add provider-gated reasoning suffix mapping"
```

### Task 7: Apply provider-aware upstream Responses dispatch to chat and responses handlers

**Files:**
- Modify: `internal/httpapi/handlers_chat.go`
- Modify: `internal/httpapi/handlers_responses.go`
- Modify: `internal/httpapi/handlers_models.go`
- Modify: `internal/upstream/client.go`
- Test: `tests/integration/provider_dispatch_test.go`

**Step 1: Write the failing tests**

Add tests covering:

- provider-scoped chat requests hit the correct upstream base URL
- provider-scoped responses requests hit the correct upstream base URL
- provider-scoped models requests hit the correct upstream base URL
- legacy `/v1/*` routes dispatch through the default provider

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestProviderDispatch -v`
Expected: FAIL because handlers still build a single fixed client from one global upstream.

**Step 3: Write minimal implementation**

Move upstream client creation behind provider-aware config lookup so all OpenAI-style routes dispatch using the selected provider while keeping upstream transport on Responses.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestProviderDispatch -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/httpapi/handlers_chat.go internal/httpapi/handlers_responses.go internal/httpapi/handlers_models.go internal/upstream/client.go tests/integration/provider_dispatch_test.go
git commit -m "feat: make openai-style handlers dispatch through provider configs"
```

### Task 8: Add Anthropic Messages request adapter into canonical form

**Files:**
- Create: `internal/adapter/anthropic/request.go`
- Modify: `internal/model/canonical.go`
- Test: `tests/integration/anthropic_request_mapping_test.go`

**Step 1: Write the failing tests**

Add tests covering:

- top-level `system` maps safely into canonical instructions
- Anthropic `messages` content maps into canonical messages
- `max_tokens` maps to canonical output limit
- tool-use input structures are preserved enough for upstream Responses translation

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestAnthropicRequestMapping -v`
Expected: FAIL because Anthropic request decoding does not exist.

**Step 3: Write minimal implementation**

Create a request adapter that converts Anthropic Messages input into canonical form without losing downstream contract intent.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestAnthropicRequestMapping -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/adapter/anthropic/request.go internal/model/canonical.go tests/integration/anthropic_request_mapping_test.go
git commit -m "feat: add anthropic messages request mapping"
```

### Task 9: Add Anthropic Messages response and streaming adapters from upstream Responses events

**Files:**
- Create: `internal/adapter/anthropic/response.go`
- Create: `internal/httpapi/handlers_anthropic.go`
- Modify: `internal/httpapi/streaming.go`
- Modify: `internal/aggregate/collector.go`
- Test: `tests/integration/anthropic_messages_test.go`
- Test: `tests/integration/anthropic_streaming_test.go`

**Step 1: Write the failing tests**

Add tests covering:

- Anthropic non-streaming message response shape
- Anthropic streaming event order
- stop reason preservation
- tool-use delta handling
- provider capability rejection when Anthropic Messages is unsupported

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run 'TestAnthropicMessages|TestAnthropicStreaming' -v`
Expected: FAIL because Anthropic response mapping and route handling do not exist.

**Step 3: Write minimal implementation**

Implement a native Anthropic downstream encoder that consumes canonical aggregates and upstream Responses events while emitting Anthropic-compatible JSON and SSE events.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run 'TestAnthropicMessages|TestAnthropicStreaming' -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/adapter/anthropic/response.go internal/httpapi/handlers_anthropic.go internal/httpapi/streaming.go internal/aggregate/collector.go tests/integration/anthropic_messages_test.go tests/integration/anthropic_streaming_test.go
git commit -m "feat: add anthropic messages output over responses upstream"
```

### Task 10: Register the third public contract in the router

**Files:**
- Modify: `internal/httpapi/server.go`
- Modify: `internal/httpapi/routes.go`
- Test: `tests/integration/provider_routes_test.go`

**Step 1: Write the failing test**

Add a router test proving `POST /{providerId}/v1/messages` reaches the Anthropic handler and bare `/v1/*` still only aliases the OpenAI-style legacy routes.

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestAnthropicRouteRegistration -v`
Expected: FAIL because the route is not registered.

**Step 3: Write minimal implementation**

Register the Anthropic route without adding new alias forms or breaking existing routes.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestAnthropicRouteRegistration -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/httpapi/server.go internal/httpapi/routes.go tests/integration/provider_routes_test.go
git commit -m "feat: register anthropic provider route"
```

### Task 11: Add provider-aware docs and example coverage

**Files:**
- Modify: `README.md`
- Modify: `docs/部署文档.md`
- Modify: `docs/功能报告.md`
- Test: `tests/integration/samples_test.go`

**Step 1: Write the failing tests**

Add sample/contract tests covering:

- provider-scoped curl examples
- legacy `/v1/*` default-provider behavior
- provider example file expectations

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run TestSamplesDocumentation -v`
Expected: FAIL because docs and examples do not describe provider-scoped behavior.

**Step 3: Write minimal implementation**

Update docs to explain:

- route shapes
- default provider aliasing
- provider env file layout
- provider `.env.example` usage
- three downstream contracts over one upstream Responses transport

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run TestSamplesDocumentation -v`
Expected: PASS

**Step 5: Commit**

```bash
git add README.md docs/部署文档.md docs/功能报告.md tests/integration/samples_test.go
git commit -m "docs: describe provider routes and provider config examples"
```

### Task 12: Run full verification and branch the work by feature

**Files:**
- No source changes required

**Step 1: Run focused integration suites**

Run:

```bash
go test ./tests/integration -run 'TestProvider|TestAnthropic|TestReasoningSuffix|TestSamplesDocumentation' -v
```

Expected: PASS

**Step 2: Run full test suite**

Run:

```bash
go test ./... 
```

Expected: PASS

**Step 3: Verify feature branch boundaries**

Split commits into these feature branches in dependency order:

1. `feat/provider-config-routing`
   - Tasks 1-4
2. `feat/provider-model-mapping`
   - Task 5
3. `feat/provider-three-surface-output`
   - Tasks 7-10
4. `feat/provider-reasoning-suffix`
   - Task 6
5. `docs/provider-config-examples` (optional if docs need a separate review lane)
   - Task 11

If strict “one branch per feature” is required, fold docs into the relevant feature branch instead of keeping branch 5.

**Step 4: Commit / verify branch tips**

Ensure each branch is independently testable at its dependency level before opening PRs.

**Step 5: Commit**

No new commit required if prior tasks are already committed cleanly.

---

## TDD Acceptance Matrix

### Feature 3 branch: provider config + routing foundation

- red: provider config load/validation tests fail
- green: provider registry and route parsing work
- refactor: clean route/context plumbing

### Feature 2 branch: provider model mapping

- red: per-provider alias tests fail
- green: mapped upstream model selection works
- refactor: isolate mapping helper and logs

### Feature 1 branch: three-surface output over Responses upstream

- red: provider-scoped responses/chat/anthropic contract tests fail
- green: three public contracts all adapt through upstream Responses
- refactor: remove duplication across adapters and streaming helpers

### Feature 4 branch: reasoning suffix mapping

- red: suffix toggle/order tests fail
- green: suffix handling works only when enabled
- refactor: centralize suffix parsing and capability checks

## Branch Commit Strategy

Use small atomic commits inside each branch.

Recommended commit ordering:

1. failing tests
2. minimal implementation
3. route/logging cleanup
4. docs/examples for that feature

Do not mix Anthropic output work into the foundation branch, and do not mix suffix parsing into the model mapping branch until the basic provider alias layer is already merged.
