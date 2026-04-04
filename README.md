# openai-compat-proxy

一个面向正式长期使用场景的 Go 单二进制代理：**每个 provider 内部只走一种上游正规协议**，但对外统一提供 OpenAI Responses、Chat Completions、Anthropic Messages 三套兼容入口，并由代理层完成协议互转、语义补全与流式兜底。

> 适合把不同上游站点收敛到一套稳定入口：三套下游入口 × 三种上游协议类型可交叉组合，代理层负责多 provider 路由、热加载、流式容错、工具调用适配、reasoning / thinking 兼容和部署运维兜底。

---

## ✨ 现在能做什么

| 能力 | 当前状态 | 说明 |
|---|---|---|
| 多 provider 路由 | ✅ | 支持 `providers/*.env`、显式 `/{providerId}/v1/*` 和默认 provider 裸 `/v1/*` |
| 三套兼容入口 | ✅ | `POST /v1/responses`、`POST /v1/chat/completions`、`POST /v1/messages`、`GET /v1/models` 全部可用 |
| `3×3` 协议矩阵 | ✅ | 三个下游入口可分别对接 `responses / chat / anthropic` 三种上游协议类型 |
| provider 内部统一走一种上游协议 | ✅ | `UPSTREAM_ENDPOINT_TYPE=responses/chat/anthropic`，代理层负责跨协议适配 |
| 流式 / 非流式 | ✅ | 支持 SSE 转发、超时兜底、错误终态补发，也支持 `proxy_buffer` / `upstream_non_stream` |
| 工具调用与多轮 tool result 回传 | ✅ | 三套入口都已实现；其中 `/v1/responses` 的高层语义覆盖最全面 |
| reasoning / thinking 兼容 | ✅ | 支持请求体参数、模型 suffix、Anthropic thinking、上游 reasoning 内容回写与流式拆分 |
| 模型映射 | ✅ | 支持 `MODEL_MAP` 通配符、`$0` 与 `$1..$N` 占位符、`MANUAL_MODELS` |
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
3. **代理层继续统一对外暴露三套兼容接口，并补齐跨协议语义差异**

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

## 📡 响应头与透明度

代理会在**鉴权成功**的正常请求里额外返回一组透明度响应头，用来帮助客户端确认：

- 客户端实际把什么模型 / 推理强度发给了代理
- 代理最终把什么模型 / 推理参数发给了上游

当前默认返回：

| 响应头 | 作用 | 示例 |
|---|---|---|
| `X-Request-Id` | 本次请求在代理层的唯一追踪 ID | `req-1743870000000000000-1` |
| `X-Cache-Info-Timezone` | 当前运行时使用的 `CACHE_INFO_TIMEZONE`，同时影响 Cache_Info 统计展示和版本时间响应头的格式化时区 | `Asia/Shanghai` |
| `X-Client-To-Proxy-Model` | 客户端发给代理的原始模型名，**保留 suffix**，方便确认 `model-high` 这类写法是否真的进到了代理层 | `gpt-5-high` |
| `X-Client-To-Proxy-Reasoning-Parameters` | 客户端 → 代理这段链路里，代理按本地优先级（如 suffix 优先于请求体）处理后得到的客户端侧推理参数组；不同下游端口会保持各自协议视角 | `{"thinking":{"type":"enabled","budget_tokens":2048}}` |
| `X-Client-To-Proxy-Reasoning-Effort` | 客户端这一侧最终体现出来的推理强度摘要，便于快速看出 `low/medium/high/xhigh` | `high` |
| `X-Proxy-To-Upstream-Model` | 代理最终实际发给上游的模型名；如果启用了 `MODEL_MAP`，这里会直接展示映射后的结果 | `claude-sonnet-4-5` |
| `X-Proxy-To-Upstream-Reasoning-Parameters` | 代理最终实际发给上游的推理参数，按上游协议类型展示为紧凑 JSON 字符串 | `{"reasoning":{"effort":"high","summary":"auto"}}` |
| `X-Env-Version` | 当前根 `.env` 的热加载版本戳 | `2026-03-25T11:03:00.111Z` |
| `X-Provider-Name` | 本次请求命中的 provider ID | `openai` |
| `X-Provider-Version` | 当前 provider 配置的热加载版本戳 | `2026-03-25T11:04:00.222Z` |
| `X-SYSTEM-PROMPT-ATTACH` | 当 provider 级系统提示词真的注入时，展示注入位置与原始文件串 | `prepend:prompt.md, prompts/extra.md` |

补充说明：

- 这组透明度响应头**不需要额外配置变量**，默认直接返回。
- `X-Client-To-Proxy-*` 关注的是**客户端 → 代理**这段链路。
- `X-Proxy-To-Upstream-*` 关注的是**代理 → 上游**这段链路。
- `X-Client-To-Proxy-Reasoning-Parameters` 是客户端侧的**主信息**；它展示的是客户端协议视角下、经过本地优先级解析后的参数组。
- `X-Client-To-Proxy-Reasoning-Effort` 是客户端侧的**摘要值**；如果同一请求里模型 suffix 和请求体参数同时存在，代理会按本地优先级先决出最终强度，再把这个摘要值写进来。
- `X-Proxy-To-Upstream-Reasoning-Parameters` 展示的是**实际上游请求体里的最终字段**，所以不同上游协议可能长得不一样：
  - `responses/chat` 常见为 `{"reasoning":{...}}`
  - `anthropic` 常见为 `{"thinking":{...}}` 或同时包含 `output_config`
- `X-Cache-Info-Timezone` 展示的是当前运行时实际使用的时区；它不只影响 Cache_Info 统计展示，也会影响 `X-Env-Version` / `X-Provider-Version` 这类版本时间响应头的格式化结果。
- 如果 `/v1/messages` 是直接传 `thinking`，但**没有**显式 effort，也**没有**模型 suffix，代理会保留 `thinking` 参数组到 `X-Client-To-Proxy-Reasoning-Parameters`，同时根据 `thinking` 里的预算或 `output_config` 反推出一个客户端视角的强度摘要，填到 `X-Client-To-Proxy-Reasoning-Effort`。
- 当上游命中 Anthropic adaptive thinking（例如部分 `opus-4-6` 族模型）时，`X-Proxy-To-Upstream-Reasoning-Parameters` 里除了 `thinking` 之外，还可能同时看到 `output_config`，用来展示代理最终发给上游的 adaptive effort 配置。
- `401 unauthorized`、`400 invalid_request` 这类在代理真正建立请求链路之前就失败的响应，不会暴露这组透明度 header。

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

它只决定**代理内部如何请求上游**，不影响对外公开的三套兼容入口。也就是说，真正暴露给客户端的是 `3×3` 组合能力，而不是“每个 provider 只能服务一种下游接口”。

### 2. 流式与非流式策略

`DOWNSTREAM_NON_STREAM_STRATEGY` / `DOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE` 支持：

- `proxy_buffer`：下游非流时，代理继续向上游请求 SSE，再本地聚合
- `upstream_non_stream`：下游非流时，代理直接向上游请求非流 JSON
- `UPSTREAM_THINKING_TAG_STYLE=true/false`：当 `UPSTREAM_ENDPOINT_TYPE=chat` 时，决定是否把 `<think>` / `<thinking>` / `<reasoning>` 标签拆成 reasoning 内容

这层还有几项专门做稳定性的代理侧兜底：

- 首字节前失败可按 provider 配置重试
- 已开始 SSE 后出错会尽量保持下游流协议终态，而不是中途退化成 JSON 错误体
- chat 上游的 reasoning 标签、reasoning_content、tool_calls 会在代理层统一归一后再下发

### 3. 模型映射与 reasoning suffix

支持：

- `MODEL_MAP` 通配符映射
- `$0` 与 `$1..$N` 占位符替换（按通配符捕获数量动态生效，没有写死到 `$2`）
- `MANUAL_MODELS` 手动补模型
- `ENABLE_REASONING_EFFORT_SUFFIX=true` 后解析 `-low/-medium/-high/-xhigh`
- `EXPOSE_REASONING_SUFFIX_MODELS=true` 后在 `/models` 里暴露 suffix 变体
- `MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING=true` 时，把 suffix 或请求体里解析出的 effort 自动映射到 Anthropic thinking

这些变量的实际含义：

- `MODEL_MAP`：把下游请求里的模型名重写成上游真正要调用的模型名
- `MANUAL_MODELS`：当上游不返回 `/models` 列表或列表不完整时，手动补齐可展示模型
- `ENABLE_REASONING_EFFORT_SUFFIX`：允许像 `model-high` 这样的模型后缀直接表示推理强度
- `EXPOSE_REASONING_SUFFIX_MODELS`：让 `/models` 也把这些后缀变体展示给客户端
- `MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING`：当上游是 anthropic 协议时，把 effort 自动翻译成 `thinking` / `output_config`

当前已验证的推理强度入口包括：

- `/v1/responses` 请求体 `reasoning.effort`
- `/v1/chat/completions` 请求体 `reasoning_effort` 或 `reasoning.effort`
- 模型名 suffix：`-low / -medium / -high / -xhigh`
- `/v1/messages` 请求体 `thinking` 直传

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

## 📌 适配策略与已知边界

这些都是当前代码里的真实适配策略，建议在接入前明确：

1. **三套入口都已实现，但 `/v1/responses` 的高层语义覆盖最全面**  
   `previous_response_id`、`metadata`、`parallel_tool_calls`、`truncation`、`store`、`include` 这类字段，代理会优先在 `/v1/responses` 这条链完整保留；并不代表另外两条入口不可用，而是它们会按各自协议语义做兼容转换。

2. **当 provider 内部上游不是 `responses` 时，Responses 顶层字段按代理语义兼容，而不是逐字面透传**  
   尤其像 `store`、`include` 这种字段，在转到 `chat` / `anthropic` 上游时，含义由代理层接管并做协议适配，不应理解成“继续原样透传”。

3. **`/v1/messages` 必须带 `anthropic-version`，且当前只校验存在且非空**  
   建议仍然传 `2023-06-01`。

4. **`/v1/messages` 当前对 `input_audio` 走显式拒绝**  
   不会静默吞掉。

5. **`cache_control` 现在区分“同端点透传”和“跨协议兼容”两种语义**  
   当 `/v1/messages` 直接对接 `anthropic` 上游时，代理会把内容块里的 `cache_control` 一并继续传给上游；但跨协议转换时，它仍然只是兼容输入，不承诺把它翻译成真正的 Anthropic prompt caching 语义。

6. **chat 上游的思维标签当前支持三种写法**  
   当 `UPSTREAM_THINKING_TAG_STYLE=true` 时，代理会把 `<think>`、`<thinking>`、`<reasoning>` 识别为 reasoning 内容，再按目标下游协议重写。

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
