# 相邻工具调用正文丢失与缓存结构复现修复计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用生产请求完整复刻 `/v1/responses` 相邻工具调用后正文不输出的问题，并建立“修复、提交、部署、服务器重打请求、读取最新日志验证”的闭环。

**Architecture:** 先把服务器调试归档转换成可脱敏、可回放、可断言的测试夹具，再用本地假上游复现代理层结构转换。修复完成后必须部署到服务器，按同类触发步骤重新请求并用最新 request id 的归档与日志确认结果。

**Tech Stack:** Go、`httptest.Server`、现有 `internal/debugarchive`、`internal/adapter/responses`、`internal/upstream`、`internal/httpapi` 测试工具、服务器 systemd 部署脚本。

---

## 事实基线

当前证据来自服务器 `/root/openai-compat-proxy-private/OPENAI_COMPAT_DEBUG_ARCHIVE_DIR/`：

- `req-1781731449828543605-25`
  - 路径：`POST /v1/responses`
  - 模型：`deepseek-v4-flash`
  - `stream=true`
  - `request.ndjson` 存在，`raw.ndjson` 和 `canonical.ndjson` 为空，`final.ndjson` 只有 `{"status_code":200}`。
  - 输入结构为：`user → user → reasoning(id=rs_proxy, summary 含真实推理文本) → function_call → function_call_output → function_call → function_call_output`。
  - 服务日志显示 canonical 已构建成 6 条消息：`user user assistant tool assistant tool`，其中两条 assistant 都有 1 个 tool call。

- `req-1781731359945113979-20`
  - 路径：`POST /v1/responses`
  - 模型：`gpt-5.5`
  - `stream=true`
  - `raw.ndjson` 和 `canonical.ndjson` 有完整事件，`final.ndjson` 只有状态码。
  - 输入中存在多个 `reasoning(id=rs_proxy)`，其中部分 summary 只有零宽字符 `\u200b`，还有一段真实 `rs_*` reasoning。
  - `response.completed` usage 显示 `input_tokens=11049`、`cached_tokens=0`。

用户已明确纠正：`cache_control` 是 Claude 官方上游特有能力，当前第三方 Claude 兼容上游大概率不使用它。后续不能把它当根因，根因调查应聚焦代理层结构是否打乱历史、thinking/reasoning、tool_use/tool_result 顺序或消息边界。

---

## 文件职责

- `docs/superpowers/plans/2026-06-18-adjacent-tool-replay-fix.md`
  - 本计划文档。
- `internal/adapter/responses/request_test.go`
  - 增加生产请求形态的入站解码回归测试，确保 `rs_proxy` 真实 reasoning 不丢，纯零宽 `rs_proxy` 不污染 canonical。
- `internal/upstream/client_test.go` 或 `internal/upstream/protocol_test.go`
  - 增加 Responses 到 Anthropic/Claude 兼容上游的 payload 结构测试，断言相邻工具调用被转换为合法、稳定的 `assistant(tool_use) → user(tool_result) → assistant(tool_use) → user(tool_result)` 序列。
- `internal/httpapi/*_test.go`
  - 增加 handler 级 replay 测试，用假上游返回正文事件，断言相邻工具调用后的正文能向下游输出。
- `internal/debugarchive/*_test.go`
  - 只在现有归档能力不足时补测试；默认优先复用现有 `request.ndjson/raw.ndjson/canonical.ndjson/final.ndjson` 格式，不新增通用 replay 引擎。
- `internal/adapter/responses/request.go`、`internal/upstream/protocol.go`、`internal/httpapi/responses_history.go`
  - 只在失败测试钉住根因后做最小修复。

---

## Task 1: 导出并脱敏生产复现样本

**Files:**
- Create: `testdata/replay/req-1781731449828543605-25/request.ndjson`
- Create: `testdata/replay/req-1781731359945113979-20/request.ndjson`
- Optional Create: `testdata/replay/req-1781731359945113979-20/raw.ndjson`
- Optional Create: `testdata/replay/req-1781731359945113979-20/canonical.ndjson`

- [ ] **Step 1: 从服务器导出两个请求归档**

```bash
sshpass -p "$SERVER_PASSWORD" scp -o PreferredAuthentications=password -o PubkeyAuthentication=no -o StrictHostKeyChecking=no -r \
  root@186.241.107.35:/root/openai-compat-proxy-private/OPENAI_COMPAT_DEBUG_ARCHIVE_DIR/req-1781731449828543605-25 \
  /tmp/req-1781731449828543605-25

sshpass -p "$SERVER_PASSWORD" scp -o PreferredAuthentications=password -o PubkeyAuthentication=no -o StrictHostKeyChecking=no -r \
  root@186.241.107.35:/root/openai-compat-proxy-private/OPENAI_COMPAT_DEBUG_ARCHIVE_DIR/req-1781731359945113979-20 \
  /tmp/req-1781731359945113979-20
```

Expected: `/tmp/req-1781731449828543605-25/request.ndjson` 和 `/tmp/req-1781731359945113979-20/request.ndjson` 存在。

- [ ] **Step 2: 脱敏但保留结构**

脱敏脚本只允许替换文本内容和凭证，不允许改变数组长度、字段存在性、tool call id、call_id、name、事件顺序。

```bash
python3 - <<'PY'
import json
from pathlib import Path

cases = [
    "req-1781731449828543605-25",
    "req-1781731359945113979-20",
]

def scrub_value(key, value):
    if isinstance(value, str):
        lower = key.lower()
        if "authorization" in lower or "api_key" in lower or "token" in lower or "cookie" in lower:
            return "<redacted>"
        if len(value) > 800 and key in {"instructions", "content", "text", "output", "request_body"}:
            return value[:240] + "\n<redacted-text>\n" + value[-240:]
    return value

def scrub(obj, key=""):
    if isinstance(obj, dict):
        return {k: scrub(scrub_value(k, v), k) for k, v in obj.items()}
    if isinstance(obj, list):
        return [scrub(v, key) for v in obj]
    return obj

for case in cases:
    src = Path("/tmp") / case / "request.ndjson"
    dst = Path("testdata/replay") / case / "request.ndjson"
    dst.parent.mkdir(parents=True, exist_ok=True)
    with src.open("r", encoding="utf-8", errors="replace") as f, dst.open("w", encoding="utf-8") as out:
        for line in f:
            if not line.strip():
                continue
            obj = json.loads(line)
            out.write(json.dumps(scrub(obj), ensure_ascii=False, separators=(",", ":")) + "\n")
PY
```

Expected: 两个 `testdata/replay/*/request.ndjson` 文件存在，并且每个文件仍只有一条请求记录。

- [ ] **Step 3: 校验脱敏样本结构不失真**

```bash
python3 - <<'PY'
import json
from pathlib import Path

for path in Path("testdata/replay").glob("*/request.ndjson"):
    rows = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]
    assert len(rows) == 1, path
    body = rows[0].get("request_body")
    if isinstance(body, str):
        body = json.loads(body)
    assert body["stream"] is True
    assert isinstance(body["input"], list)
    assert len(body.get("tools", [])) == 8
    print(path, body["model"], [item.get("type") or item.get("role") for item in body["input"]])
PY
```

Expected:
- `req-1781731449828543605-25` 输出包含 `reasoning,function_call,function_call_output,function_call,function_call_output`。
- `req-1781731359945113979-20` 输出包含多个 `reasoning` 与历史 `assistant/user` 项。

---

## Task 2: 复现入站解码结构

**Files:**
- Modify: `internal/adapter/responses/request_test.go`
- Test: `internal/adapter/responses/request_test.go`

- [ ] **Step 1: 写失败测试，读取脱敏 request.ndjson**

在 `internal/adapter/responses/request_test.go` 增加测试。测试应读取 `testdata/replay/req-1781731449828543605-25/request.ndjson`，取出 `request_body` 后调用 `DecodeRequest`。

```go
func TestDecodeRequestReplaysAdjacentToolProductionShape(t *testing.T) {
    body := readReplayRequestBody(t, "../../../testdata/replay/req-1781731449828543605-25/request.ndjson")

    canon, err := DecodeRequest(strings.NewReader(body))
    if err != nil {
        t.Fatalf("DecodeRequest error: %v", err)
    }

    if len(canon.Messages) != 6 {
        t.Fatalf("expected 6 canonical messages, got %#v", canon.Messages)
    }
    if got := []string{canon.Messages[0].Role, canon.Messages[1].Role, canon.Messages[2].Role, canon.Messages[3].Role, canon.Messages[4].Role, canon.Messages[5].Role}; !reflect.DeepEqual(got, []string{"user", "user", "assistant", "tool", "assistant", "tool"}) {
        t.Fatalf("unexpected message roles: %#v", got)
    }
    if len(canon.Messages[2].ToolCalls) != 1 || len(canon.Messages[4].ToolCalls) != 1 {
        t.Fatalf("expected two assistant tool calls, got %#v", canon.Messages)
    }
    if strings.TrimSpace(canon.Messages[2].ReasoningContent) == "" {
        t.Fatalf("expected first adjacent tool call to retain real reasoning, got %#v", canon.Messages[2])
    }
}
```

同时增加测试辅助函数：

```go
func readReplayRequestBody(t *testing.T, path string) string {
    t.Helper()
    data, err := os.ReadFile(path)
    if err != nil {
        t.Fatalf("read replay fixture: %v", err)
    }
    for _, line := range strings.Split(string(data), "\n") {
        if strings.TrimSpace(line) == "" {
            continue
        }
        var row map[string]any
        if err := json.Unmarshal([]byte(line), &row); err != nil {
            t.Fatalf("decode replay row: %v", err)
        }
        body, _ := row["request_body"].(string)
        if body == "" {
            t.Fatalf("replay row missing request_body")
        }
        return body
    }
    t.Fatalf("empty replay fixture")
    return ""
}
```

- [ ] **Step 2: 运行测试确认失败或暴露现状**

```bash
go test -count=1 ./internal/adapter/responses -run TestDecodeRequestReplaysAdjacentToolProductionShape
```

Expected: 如果当前解码仍有结构问题，应 FAIL，并显示消息数量、角色顺序或 reasoning 缺失。如果 PASS，说明入站解码不是当前生产 bug 的剩余根因，继续 Task 3。

---

## Task 3: 复现 Responses 到 Claude 兼容上游的消息结构

**Files:**
- Modify: `internal/upstream/client_test.go`
- Test: `internal/upstream/client_test.go`

- [ ] **Step 1: 写 payload 结构断言测试**

测试从同一个 replay request 解码 canonical，再调用 `buildRequestBodyForEndpoint(..., config.UpstreamEndpointTypeAnthropic, ...)`，断言相邻工具调用变成稳定序列。

```go
func TestBuildAnthropicRequestBodyReplaysAdjacentToolProductionShape(t *testing.T) {
    body := readReplayRequestBody(t, "../../testdata/replay/req-1781731449828543605-25/request.ndjson")
    canon, err := responses.DecodeRequest(strings.NewReader(body))
    if err != nil {
        t.Fatalf("DecodeRequest error: %v", err)
    }

    upstreamBody, err := buildRequestBodyForEndpoint(canon, config.UpstreamEndpointTypeAnthropic, "", false, false, config.UpstreamCacheControlNoChange)
    if err != nil {
        t.Fatalf("buildRequestBodyForEndpoint error: %v", err)
    }

    var payload map[string]any
    if err := json.Unmarshal(upstreamBody, &payload); err != nil {
        t.Fatalf("unmarshal upstream body: %v", err)
    }
    messages, _ := payload["messages"].([]any)
    if len(messages) < 6 {
        t.Fatalf("expected replay messages to keep adjacent tool sequence, got %#v", messages)
    }
    assertAnthropicToolSequence(t, messages)
}
```

断言辅助函数只检查结构，不检查真实正文：

```go
func assertAnthropicToolSequence(t *testing.T, messages []any) {
    t.Helper()
    var sequence []string
    for _, raw := range messages {
        msg, _ := raw.(map[string]any)
        role, _ := msg["role"].(string)
        content, _ := msg["content"].([]any)
        for _, rawBlock := range content {
            block, _ := rawBlock.(map[string]any)
            typ, _ := block["type"].(string)
            if typ == "tool_use" || typ == "tool_result" {
                sequence = append(sequence, role+":"+typ)
            }
        }
    }
    want := []string{"assistant:tool_use", "user:tool_result", "assistant:tool_use", "user:tool_result"}
    if !reflect.DeepEqual(sequence, want) {
        t.Fatalf("unexpected tool sequence: got %#v want %#v", sequence, want)
    }
}
```

- [ ] **Step 2: 运行测试确认失败或通过**

```bash
go test -count=1 ./internal/upstream -run TestBuildAnthropicRequestBodyReplaysAdjacentToolProductionShape
```

Expected: 如果 FAIL，按失败输出定位 `internal/upstream/protocol.go` 的结构转换问题。如果 PASS，说明上游 payload 结构不是剩余正文丢失的根因，继续 Task 4。

---

## Task 4: 复现 handler 级正文输出闭环

**Files:**
- Modify: `internal/httpapi/*_test.go`
- Test: `internal/httpapi/...`

- [ ] **Step 1: 用假上游构造相邻工具后的正文输出**

测试用脱敏 request body 请求本地 handler，假上游返回一段包含正文的流式事件，断言下游 SSE 中出现正文文本。

关键断言：

```go
if !strings.Contains(body, "最终正文") {
    t.Fatalf("expected downstream body text after adjacent tool calls, got %s", body)
}
if strings.Contains(body, "代理层占位") || strings.Contains(body, "**推理中**") {
    t.Fatalf("expected no visible proxy placeholder, got %s", body)
}
```

- [ ] **Step 2: 运行 handler 级测试**

```bash
go test -count=1 ./internal/httpapi -run 'Test.*AdjacentTool.*Body'
```

Expected: FAIL 时必须显示下游没有输出 `最终正文`，PASS 时证明本地 handler 级闭环成立。

---

## Task 5: 最小修复

**Files:**
- Modify only the file identified by the first failing task among:
  - `internal/adapter/responses/request.go`
  - `internal/upstream/protocol.go`
  - `internal/httpapi/responses_history.go`
  - `internal/httpapi/streaming.go`

- [ ] **Step 1: 只修第一处失败根因**

修复规则：

- 如果 Task 2 失败，修 Responses 入站解码，不碰上游构造。
- 如果 Task 3 失败，修 Anthropic payload 的 `tool_use/tool_result` 排列，不碰 handler。
- 如果 Task 4 失败，修下游 SSE 输出或聚合状态机，不碰入站解码。
- 不为假设中的未来问题做抽象。

- [ ] **Step 2: 跑对应失败测试直到通过**

```bash
go test -count=1 ./internal/adapter/responses -run TestDecodeRequestReplaysAdjacentToolProductionShape
go test -count=1 ./internal/upstream -run TestBuildAnthropicRequestBodyReplaysAdjacentToolProductionShape
go test -count=1 ./internal/httpapi -run 'Test.*AdjacentTool.*Body'
```

Expected: 失败测试变为 PASS。

---

## Task 6: 本地完整验证

**Files:**
- No new files unless tests require fixtures.

- [ ] **Step 1: 跑受影响包测试**

```bash
go test -count=1 ./internal/adapter/responses ./internal/upstream ./internal/httpapi ./internal/aggregate
```

Expected: 全部 PASS。

- [ ] **Step 2: 跑全量测试**

```bash
go test -count=1 ./...
```

Expected: 全部 PASS。

- [ ] **Step 3: 构建主程序**

```bash
go build -o bin/openai-compat-proxy ./cmd/proxy
```

Expected: exit 0。

- [ ] **Step 4: LSP 诊断**

Run diagnostics on every modified `.go` file.

Expected: zero errors。

---

## Task 7: 提交、部署、服务器复刻闭环

**Files:**
- Commit only implementation, tests, and sanitized replay fixtures.
- Do not commit `AGENTS.md` or private runtime assets.

- [ ] **Step 1: 按用户需求粒度提交**

```bash
GIT_MASTER=1 git status
GIT_MASTER=1 git diff --stat
GIT_MASTER=1 git add <changed-files>
GIT_MASTER=1 git commit -m "修复(responses): 复现并修复相邻工具调用正文丢失"
```

Expected: commit created。

- [ ] **Step 2: 推送并服务器部署**

```bash
GIT_MASTER=1 git push origin main
sshpass -p "$SERVER_PASSWORD" ssh -o PreferredAuthentications=password -o PubkeyAuthentication=no -o StrictHostKeyChecking=no root@186.241.107.35 \
  "cd /root/openai-compat-proxy-private && git pull --ff-only origin main && bash scripts/deploy-linux.sh"
```

Expected: deploy script exits 0。

- [ ] **Step 3: 服务器健康检查**

```bash
sshpass -p "$SERVER_PASSWORD" ssh -o PreferredAuthentications=password -o PubkeyAuthentication=no -o StrictHostKeyChecking=no root@186.241.107.35 \
  "curl -sS http://127.0.0.1:21021/healthz; systemctl is-active openai-compat-proxy.service; ss -ltnp | grep 21021"
```

Expected: `{"status":"ok"}`、`active`、端口 `21021` 监听。

- [ ] **Step 4: 在服务器上重打真实触发步骤**

按生产触发方式重新发起同类对话：

1. 先触发第一轮工具调用。
2. 让模型连续发起两次相邻工具调用。
3. 工具调用完成后继续问一个必须输出正文的问题。
4. 记录最新 request id。

如果无法自动模拟完整客户端，就用当前真实客户端手工触发一次，但验证必须由代理读取服务器最新日志和归档完成。

- [ ] **Step 5: 读取最新 request id 的归档与日志**

```bash
REQ=<new-request-id>
sshpass -p "$SERVER_PASSWORD" ssh -o PreferredAuthentications=password -o PubkeyAuthentication=no -o StrictHostKeyChecking=no root@186.241.107.35 \
  "ls -l /root/openai-compat-proxy-private/OPENAI_COMPAT_DEBUG_ARCHIVE_DIR/$REQ && journalctl -u openai-compat-proxy.service --no-pager -n 3000 | grep -F $REQ"
```

Expected:

- `request.ndjson` 存在。
- `raw.ndjson` 或 `canonical.ndjson` 中能看到正文输出事件。
- `final.ndjson` 不只是空状态码，或者服务日志能证明正文已向客户端输出。
- 没有可见 `代理层占位` 或 `**推理中**`。

- [ ] **Step 6: 判定闭环结果**

通过标准：

- 本地 replay 测试 PASS。
- 服务器部署后健康检查 PASS。
- 服务器最新同类请求中，相邻工具调用后的正文输出存在。
- 如果缓存仍显示重建，需要把最新 request 的 `request.ndjson/raw.ndjson/canonical.ndjson` 与上一条对比，确认是第三方上游缓存策略问题还是代理层请求结构仍漂移。

---

## Self-review

- Spec coverage: 覆盖了用户要求的立项文档、真实请求复刻、修复后服务器部署、按触发步骤重打日志、读取最新日志验收。
- Placeholder scan: 无 `TBD`、`TODO`、`implement later`；所有步骤有明确命令和预期。
- Scope: 只围绕相邻工具调用正文丢失与同源缓存结构漂移，不再把 `cache_control` 当根因。
- Ambiguity: 如果本地 replay 全部通过但服务器仍失败，计划明确进入服务器最新 request 对比，而不是继续猜测。
