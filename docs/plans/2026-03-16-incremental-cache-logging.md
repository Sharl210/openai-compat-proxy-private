# Incremental Cache Repair, Restart Script, and Logging Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix proxy-side cache misses for real incremental chat growth, add a restart wrapper script, and add default-redacted structured logging for downstream and upstream traffic.

**Architecture:** Start from the existing `chat -> canonical -> upstream /responses` pipeline, add a failing multi-turn incremental prefix-stability test, then make the smallest request-shape repair required by that test. Add structured logging at the request, canonical, upstream, and downstream stages, with stdout summaries and a local JSON log file, plus a simple `restart-linux.sh` wrapper around the current uninstall/deploy scripts.

**Tech Stack:** Go 1.22, standard library `net/http`, `encoding/json`, `log/slog`, existing integration tests under `tests/integration`, shell scripts under `scripts/`, shell tests under `tests/`.

---

### Task 1: Reproduce the incremental chat cache bug with a failing test

**Files:**
- Modify: `tests/integration/upstream_body_test.go`
- Modify: `tests/integration/chat_request_mapping_test.go`
- Test: `tests/integration/upstream_body_test.go`

**Step 1: Write the failing test**

Add a test that constructs two multi-turn chat requests where turn 2 is turn 1 plus one appended user message, then captures the upstream `/responses` body for both and asserts the previously shared conversation prefix remains stable in the rebuilt body. Focus especially on assistant/tool/reasoning replay shape.

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run 'TestUpstreamClient.*Incremental.*|TestChat.*Incremental.*' -v`
Expected: FAIL because current rebuilt body drifts in a cache-hostile way for appended chat turns.

**Step 3: Write minimal implementation**

Inspect and minimally adjust:

- `internal/adapter/chat/request.go`
- `internal/upstream/client.go`

Only change the normalization/rebuild behavior required to keep the previously shared prompt-bearing prefix stable while preserving existing compatibility semantics.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run 'TestUpstreamClient.*Incremental.*|TestChat.*Incremental.*' -v`
Expected: PASS

### Task 2: Add structured logging infrastructure with default redaction

**Files:**
- Modify: `cmd/proxy/main.go`
- Modify: `internal/config/config.go`
- Create: `internal/logging/logger.go`
- Create: `internal/logging/redaction.go`
- Test: `tests/integration/logging_test.go`

**Step 1: Write the failing tests**

Add tests proving:

- structured log records are written to a local JSON log file
- stdout/file logger initialization succeeds with config
- Authorization and message bodies are redacted by default
- cache-related fields and hashes are still logged

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run 'TestLogging.*' -v`
Expected: FAIL because no structured logging subsystem exists yet.

**Step 3: Write minimal implementation**

Implement logger setup and redaction helpers, with config-driven file path / debug-body switch.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run 'TestLogging.*' -v`
Expected: PASS

### Task 3: Log downstream request summaries and upstream request/response summaries

**Files:**
- Modify: `internal/httpapi/server.go`
- Modify: `internal/httpapi/middleware.go`
- Modify: `internal/httpapi/handlers_chat.go`
- Modify: `internal/httpapi/handlers_responses.go`
- Modify: `internal/httpapi/streaming.go`
- Modify: `internal/upstream/client.go`
- Test: `tests/integration/logging_test.go`

**Step 1: Write the failing tests**

Add integration tests proving the logger records:

- downstream route metadata and request id
- canonical/upstream request summaries including model, stream flags, counts, hashes, and cache-relevant fields
- upstream completion usage including cached token fields
- downstream response summaries for stream and non-stream paths

**Step 2: Run test to verify it fails**

Run: `go test ./tests/integration -run 'TestLogging.*Request.*|TestLogging.*Upstream.*|TestLogging.*Cache.*' -v`
Expected: FAIL because handlers/upstream path do not emit structured records yet.

**Step 3: Write minimal implementation**

Add logging hooks at:

- request middleware in `internal/httpapi/server.go`
- streaming completion/usage path in `internal/httpapi/streaming.go`
- upstream request build/send/retry/complete path in `internal/upstream/client.go`

Keep bodies redacted by default; log hashes and counts instead.

**Step 4: Run test to verify it passes**

Run: `go test ./tests/integration -run 'TestLogging.*Request.*|TestLogging.*Upstream.*|TestLogging.*Cache.*' -v`
Expected: PASS

### Task 4: Add restart wrapper script

**Files:**
- Create: `scripts/restart-linux.sh`
- Test: `tests/restart_script_test.sh`

**Step 1: Write the failing test**

Add a shell test asserting the restart script invokes uninstall first and deploy second.

**Step 2: Run test to verify it fails**

Run: `bash tests/restart_script_test.sh`
Expected: FAIL because the restart script does not exist.

**Step 3: Write minimal implementation**

Create `scripts/restart-linux.sh` that resolves the repo root and sequentially runs:

- `scripts/uninstall-linux.sh`
- `scripts/deploy-linux.sh`

**Step 4: Run test to verify it passes**

Run: `bash tests/restart_script_test.sh`
Expected: PASS

### Task 5: Focused verification and full manual QA

**Files:**
- Modify: cache/logging/script files from tasks 1-4

**Step 1: Run focused tests**

Run: `go test ./tests/integration -run 'TestUpstreamClient.*Incremental.*|TestChat.*Incremental.*|TestLogging.*' -v && bash tests/restart_script_test.sh`
Expected: PASS

**Step 2: Run full suite**

Run: `go test ./...`
Expected: PASS

**Step 3: Manual QA**

Run the real proxy locally and verify:

- stdout emits summary logs
- JSON log file is created
- one request produces downstream + upstream records with request id correlation
- cache-related usage fields appear in logs

**Step 4: Verify final state**

Run: `go test ./...`
Expected: PASS with no new regressions
