# openai-compat-proxy

一个 Go 单二进制代理：**每个 provider 只选一种上游正规协议**，但对外继续统一提供 OpenAI Responses、Chat Completions 和 Anthropic Messages 三套兼容入口。

> 适合把不同上游站点收敛到一套稳定入口，同时保留多 provider 路由、热加载、流式转发、工具调用、reasoning 兼容和部署脚本。

---

## ✨ 现在能做什么

| 能力 | 当前状态 | 说明 |
|---|---|---|
| 多 provider 路由 | ✅ | 支持 `providers/*.env`、显式 `/{providerId}/v1/*` 和默认 provider 裸 `/v1/*` |
| 三套兼容入口 | ✅ | `POST /v1/responses`、`POST /v1/chat/completions`、`POST /v1/messages`、`GET /v1/models` |
| provider 内部统一走一种上游协议 | ✅ | `UPSTREAM_ENDPOINT_TYPE=responses/chat/anthropic` |
| 流式 / 非流式 | ✅ | 支持 SSE 转发，也支持 `proxy_buffer` / `upstream_non_stream` |
| 工具调用与多轮 tool result 回传 | ✅ | `/v1/responses` 主路径最完整 |
| reasoning / thinking 兼容 | ✅ | 支持 reasoning 映射、suffix → effort、Anthropic thinking 映射 |
| 模型映射 | ✅ | 支持 `MODEL_MAP` 通配符、`$0/$1/$2` 占位符、`MANUAL_MODELS` |
| provider 级系统提示词 | ✅ | `SYSTEM_PROMPT_FILES` + `SYSTEM_PROMPT_POSITION` |
| 伪装客户端（实验性） | ✅ | 支持 `opencode` / `claude` / `codex` / `none` |
| 调试归档 | ✅ | `OPENAI_COMPAT_DEBUG_ARCHIVE_DIR` 写出 `request/raw/canonical/final.ndjson` |
| 健康检查与 Linux 部署脚本 | ✅ | 自带 `healthz`、deploy / restart / stop / uninstall |

---

## 🧭 工作方式

```mermaid
flowchart LR
    A[客户端] --> B[/responses\n/chat/completions\n/messages\n/models]
    B --> C{按路由选 provider}
    C --> D[provider A\nresponses/chat/anthropic]
    C --> E[provider B\nresponses/chat/anthropic]
    D --> F[上游站点]
    E --> F
    B --> G[协议兼容 / streaming / tool 调用 / reasoning / usage]
```

核心思路很简单：

1. **路由先选 provider**
2. **provider 决定内部走哪条上游正规协议**
3. **代理层继续统一对外暴露三套兼容接口**

---

## 🚀 快速启动

### 1）准备根配置

```bash
cp .env.example .env
```

最小示例：

```env
LISTEN_ADDR=:21021
PROXY_API_KEY=

PROVIDERS_DIR=./providers
DEFAULT_PROVIDER=openai
ENABLE_LEGACY_V1_ROUTES=true

DOWNSTREAM_NON_STREAM_STRATEGY=proxy_buffer

CONNECT_TIMEOUT=10s
FIRST_BYTE_TIMEOUT=20m
IDLE_TIMEOUT=3m
TOTAL_TIMEOUT=1h

LOG_ENABLE=true
LOG_FILE_PATH=logs
LOG_MAX_BODY_SIZE_MB=5
LOG_MAX_REQUESTS=50

OPENAI_COMPAT_DEBUG_ARCHIVE_DIR=
```

### 2）准备 provider

```bash
cp providers/openai.env.example providers/openai.env
```

最小示例：

```env
PROVIDER_ID=openai
PROVIDER_ENABLED=true

UPSTREAM_BASE_URL=https://example-provider.com/v1
UPSTREAM_API_KEY=
UPSTREAM_ENDPOINT_TYPE=responses

SUPPORTS_CHAT=true
SUPPORTS_RESPONSES=true
SUPPORTS_MODELS=true
SUPPORTS_ANTHROPIC_MESSAGES=true
```

### 3）启动

```bash
chmod +x scripts/*.sh
bash scripts/deploy-linux.sh
```

### 4）健康检查

```bash
curl http://127.0.0.1:21021/healthz
```

---

## 🌐 路由规则

### 推荐：显式 provider 路由

- `/{providerId}/v1/responses`
- `/{providerId}/v1/chat/completions`
- `/{providerId}/v1/messages`
- `/{providerId}/v1/models`

例如：

- `/openai/v1/responses`
- `/openai/v1/chat/completions`
- `/claude/v1/messages`

### 兼容：默认 provider 裸路由

当 `ENABLE_LEGACY_V1_ROUTES=true` 且 `DEFAULT_PROVIDER` 可用时，也支持：

- `/v1/responses`
- `/v1/chat/completions`
- `/v1/messages`
- `/v1/models`

---

## 🔐 鉴权约定

代理层支持以下 header：

- `Authorization: Bearer <proxy-key>`
- `X-API-Key: <proxy-key>`
- `Api-Key: <proxy-key>`

上游 key 透传支持：

- `X-Upstream-Authorization: Bearer <real-upstream-key>`

如果请求里没有 `X-Upstream-Authorization`：

- 当当前路由**不要求代理鉴权**时，`Authorization` 可能直接作为上游鉴权透传
- 对 Anthropic / Claude 风格客户端，当当前路由**不要求代理鉴权**时，`X-API-Key` / `x-api-key` 也可以直接作为上游 key
- 否则回退到 provider 自己的 `UPSTREAM_API_KEY`

---

## 🧩 关键能力说明

### 1. provider 内部统一上游协议

每个 provider 通过：

```env
UPSTREAM_ENDPOINT_TYPE=responses
```

可选值：

- `responses`
- `chat`
- `anthropic`

它只决定**代理内部如何请求上游**，不影响对外公开的三套兼容入口。

### 2. 流式与非流式策略

`DOWNSTREAM_NON_STREAM_STRATEGY` / `DOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE` 支持：

- `proxy_buffer`：下游非流时，代理继续向上游请求 SSE，再本地聚合
- `upstream_non_stream`：下游非流时，代理直接向上游请求非流 JSON
- `UPSTREAM_THINKING_TAG_STYLE=true/false`：当 `UPSTREAM_ENDPOINT_TYPE=chat` 时，决定是否把 `<think>` / `<thinking>` 标签拆成 reasoning 内容

### 3. 模型映射与 reasoning suffix

支持：

- `MODEL_MAP` 通配符映射
- `$0/$1/$2` 占位符替换
- `MANUAL_MODELS` 手动补模型
- `ENABLE_REASONING_EFFORT_SUFFIX=true` 后解析 `-low/-medium/-high/-xhigh`
- `EXPOSE_REASONING_SUFFIX_MODELS=true` 后在 `/models` 里暴露 suffix 变体
- `MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING=true` 时，把 suffix effort 自动映射到 Anthropic thinking

### 4. provider 级系统提示词

支持：

- `SYSTEM_PROMPT_FILES=prompt.md,...`
- `SYSTEM_PROMPT_POSITION=prepend|append`

对应文件内容支持热加载。

### 5. 调试归档与日志

- `LOG_ENABLE` / `LOG_FILE_PATH` / `LOG_MAX_BODY_SIZE_MB` / `LOG_MAX_REQUESTS`：结构化日志
- `OPENAI_COMPAT_DEBUG_ARCHIVE_DIR`：按 `request_id` 写出：
  - `request.ndjson`
  - `raw.ndjson`
  - `canonical.ndjson`
  - `final.ndjson`

---

## 🧪 伪装客户端（实验性）

通过根 `.env` 的 `UPSTREAM_MASQUERADE_TARGET`，或 provider `.env` 的 `MASQUERADE_TARGET` 控制：

| 值 | 作用 |
|---|---|
| `opencode` | 注入 OpenCode 风格 `User-Agent` + `originator` |
| `claude` | 注入 Claude Code 风格 `User-Agent`、`X-App`、`anthropic-beta`、`X-Stainless-*` |
| `codex` | 注入 Codex CLI 风格 `User-Agent`、`originator`、residency header |
| `none` | 显式禁用伪装 |
| 留空 | provider 级留空表示继承根配置 |

Claude 相关还有两个配套开关：

- 根级：
  - `UPSTREAM_INJECT_METADATA_USER_ID`
  - `UPSTREAM_INJECT_CLAUDE_SYSTEM_PROMPT`
- provider 级：
  - `INJECT_CLAUDE_CODE_METADATA_USER_ID`
  - `INJECT_CLAUDE_CODE_SYSTEM_PROMPT`

其中 provider 级这两个字段现在支持：

- **留空 = 继承根配置**
- **显式 true / false = 覆盖根配置**

> 根级和 provider 级的 `UPSTREAM_USER_AGENT` 都会优先于伪装目标对 User-Agent 的修改，但不会改变其他伪装专属头。

---

## ⚙️ 配置与热加载摘要

完整字段以 `.env.example` 和 `providers/openai.env.example` 为准。这里只列最关键的运行时语义。

### 根 `.env`

| 字段组 | 例子 | 热加载 |
|---|---|---|
| 路由与鉴权 | `PROXY_API_KEY`、`DEFAULT_PROVIDER`、`ENABLE_LEGACY_V1_ROUTES` | ✅ |
| 下游策略与超时 | `DOWNSTREAM_NON_STREAM_STRATEGY`、`CONNECT_TIMEOUT`、`FIRST_BYTE_TIMEOUT`、`IDLE_TIMEOUT`、`TOTAL_TIMEOUT` | ✅ |
| 上游伪装相关 | `UPSTREAM_USER_AGENT`、`UPSTREAM_MASQUERADE_TARGET`、`UPSTREAM_INJECT_METADATA_USER_ID`、`UPSTREAM_INJECT_CLAUDE_SYSTEM_PROMPT` | ✅ |
| provider 目录 | `PROVIDERS_DIR` | ⚠️ 部分；provider 监听会切换，但 Cache_Info 落盘目录需重启 |
| 启动期字段 | `LISTEN_ADDR`、`CACHE_INFO_TIMEZONE`、`LOG_*`、`OPENAI_COMPAT_DEBUG_ARCHIVE_DIR` | ❌ 修改后需重启 |

### provider `.env`

| 字段组 | 例子 | 说明 |
|---|---|---|
| 上游连接 | `UPSTREAM_BASE_URL`、`UPSTREAM_API_KEY`、`UPSTREAM_ENDPOINT_TYPE` | 当前 provider 如何连上游 |
| Anthropic / thinking | `ANTHROPIC_VERSION`、`UPSTREAM_THINKING_TAG_STYLE` | Anthropic 上游版本与 chat 上游 thinking 标签策略 |
| 能力开关 | `SUPPORTS_CHAT`、`SUPPORTS_RESPONSES`、`SUPPORTS_MODELS`、`SUPPORTS_ANTHROPIC_MESSAGES` | 控制公开端口是否开放 |
| 非流 / timeout / retry | `DOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE`、`UPSTREAM_FIRST_BYTE_TIMEOUT`、`UPSTREAM_RETRY_COUNT`、`UPSTREAM_RETRY_DELAY` | provider 级运行时策略 |
| 提示词与模型 | `SYSTEM_PROMPT_FILES`、`SYSTEM_PROMPT_POSITION`、`MODEL_MAP`、`MANUAL_MODELS` | 注入与模型能力 |
| 推理强度 | `ENABLE_REASONING_EFFORT_SUFFIX`、`EXPOSE_REASONING_SUFFIX_MODELS`、`MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING` | suffix / thinking 相关 |
| 鉴权与伪装 | `PROXY_API_KEY_OVERRIDE`、`UPSTREAM_USER_AGENT`、`MASQUERADE_TARGET`、`INJECT_CLAUDE_CODE_*` | provider 级覆写 |

补充：`PROXY_API_KEY_OVERRIDE=empty` 表示这个 provider 的显式分组路由不做代理鉴权；而 provider 级 Claude 注入开关留空则表示继承根配置。

---

## 📌 当前行为边界

这些都是当前代码里的真实边界，建议在接入前明确：

1. **`/v1/responses` 是能力最完整的主路径**  
   `previous_response_id`、`metadata`、`parallel_tool_calls`、`truncation`、`store`、`include` 这类高层字段，优先在这条链完整保留。

2. **当 provider 内部上游不是 `responses` 时，不承诺所有 `responses` 顶层字段一比一透传**  
   尤其像 `store`、`include` 这种字段，在转到 `chat` / `anthropic` 上游时不应理解成“继续原样透传”。

3. **`/v1/messages` 必须带 `anthropic-version`，且当前只校验存在且非空**  
   建议仍然传 `2023-06-01`。

4. **`/v1/messages` 当前对 `input_audio` 走显式拒绝**  
   不会静默吞掉。

5. **`cache_control` 当前是兼容输入，不等于真实上游 prompt caching 支持**  
   代理会接受，但不会把它继续透传为真正的 Anthropic prompt caching 语义。

---

## 🛠️ 部署与运维

| 脚本 | 作用 |
|---|---|
| `bash scripts/deploy-linux.sh` | 预检、编译、停旧、启新、健康检查、失败回滚 |
| `bash scripts/restart-linux.sh` | 重启服务 |
| `bash scripts/stop-linux.sh` | 停止服务 |
| `bash scripts/uninstall-linux.sh` | 卸载部署产物 |

如果前面还有 1Panel / Nginx / OpenResty，建议先把长连接超时放宽：

```nginx
proxy_connect_timeout 1200s;
proxy_send_timeout 1200s;
proxy_read_timeout 1200s;
send_timeout 1200s;
```

---

## ✅ 常用验证命令

```bash
go test -count=1 ./...
go build -o bin/openai-compat-proxy ./cmd/proxy
curl http://127.0.0.1:21021/healthz
```

如果你准备改配置语义、协议兼容或流式行为，建议优先跑：

```bash
go test -count=1 ./internal/config ./internal/httpapi ./internal/upstream ./internal/adapter/...
```

---

## 📂 参考文件

- 根配置模板：`.env.example`
- provider 模板：`providers/openai.env.example`
- 入口：`cmd/proxy/main.go`
- HTTP 入口层：`internal/httpapi`
- 配置层：`internal/config`
- 上游协议与 header：`internal/upstream`
- Linux 部署脚本：`scripts/`
