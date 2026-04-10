# openai-compat-proxy

一个面向正式长期使用场景的 Go 单二进制代理：**每个 provider 内部只走一种上游正规协议**，但对外统一提供 OpenAI Responses、Chat Completions、Anthropic Messages 三套兼容入口，并由代理层完成协议互转、语义补全与流式兜底。

> 适合把不同上游站点收敛到一套稳定入口：三套下游入口 × 三种上游协议类型可交叉组合，代理层负责多 provider 路由、热加载、流式容错、工具调用适配、reasoning / thinking 兼容和部署运维兜底。

---

## ✨ 现在能做什么

| 能力 | 当前状态 | 说明 |
|---|---|---|
| 多 provider 路由 | ✅ | 支持 `providers/*.env`、显式 `/{providerId}/v1/*` 和默认 provider overlay 裸 `/v1/*` |
| 三套兼容入口 | ✅ | `POST /v1/responses`、`POST /v1/chat/completions`、`POST /v1/messages`、`GET /v1/models` 全部可用 |
| `3×3` 协议矩阵 | ✅ | 三个下游入口可分别对接 `responses / chat / anthropic` 三种上游协议类型 |
| provider 内部统一走一种上游协议 | ✅ | `UPSTREAM_ENDPOINT_TYPE=responses/chat/anthropic`，代理层负责跨协议适配 |
| 流式 / 非流式 | ✅ | 支持 SSE 转发、超时兜底、错误终态补发，也支持 `proxy_buffer` / `upstream_non_stream` |
| 工具调用与多轮 tool result 回传 | ✅ | 三套入口都已实现；其中 `/v1/responses` 的高层语义覆盖最全面 |
| reasoning / thinking 兼容 | ✅ | 支持请求体参数、模型 suffix、Anthropic thinking、上游 reasoning 内容回写与流式拆分 |
| Responses compact 端点 | ✅ | `/v1/responses/compact` 非流式 Responses 专用，上游须为 `responses` 类型；普通 `/v1/responses` SSE 流现可携带 compaction items |
| 无 /v1 路由别名 | ✅ | `/responses`、`/chat/completions`、`/messages`、`/models`、`/responses/compact` 及其 `/{providerId}/...` 形式均可等效访问对应 `/v1/*` 入口；复用同一套 handler 与鉴权语义 |
| responses 工具兼容模式 | ✅ | provider 级 `RESPONSES_TOOL_COMPAT_MODE=function_only` 将 `custom` / `web_search` 工具在发往 responses 上游前重写成普通 function 类型，以兼容不完整上游；代价是丢失原生 free-form / grammar 约束（custom）或原生 citations/sources 语义（web_search）；默认 `preserve` |
| 模型映射 | ✅ | 支持 `MODEL_MAP` 通配符、`$0` 与 `$1..$N` 占位符、`MANUAL_MODELS` |
| provider 级系统提示词 | ✅ | `SYSTEM_PROMPT_FILES` + `SYSTEM_PROMPT_POSITION` |
| 伪装客户端（实验性） | ✅ | 支持 `opencode` / `claude` / `codex` / `none` |
| 调试归档 | ✅ | 仅当 `LOG_ENABLE=true` 且 `OPENAI_COMPAT_DEBUG_ARCHIVE_DIR` 非空时写出 `request/raw/canonical/final.ndjson`；保留数量由 `OPENAI_COMPAT_DEBUG_ARCHIVE_MAX_REQUESTS` 独立控制（与 `LOG_MAX_REQUESTS` 解耦） |
| 健康检查与 Linux 部署脚本 | ✅ | 自带 `healthz`、deploy / restart / stop / uninstall |
| 内置 Web 管理台 | ✅ | 裸根路径 `/` 提供 Material 3 风格管理界面：文件浏览 / 文件编辑 / 运行状态 |

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
DEFAULT_PROVIDER=openai,azure
ENABLE_LEGACY_V1_ROUTES=true

DOWNSTREAM_NON_STREAM_STRATEGY=proxy_buffer

CONNECT_TIMEOUT=30s
FIRST_BYTE_TIMEOUT=20m
IDLE_TIMEOUT=3m
TOTAL_TIMEOUT=1h

LOG_ENABLE=true
LOG_FILE_PATH=logs
LOG_MAX_BODY_SIZE_MB=5
LOG_MAX_REQUESTS=200

OPENAI_COMPAT_DEBUG_ARCHIVE_DIR=OPENAI_COMPAT_DEBUG_ARCHIVE_DIR
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

### 5）打开内置 Web 管理台

浏览器直接访问：

```text
http://127.0.0.1:21021/
```

登录密码就是当前根 `.env` 里的 `PROXY_API_KEY`。

管理台当前提供：

- **文件浏览**：按项目根目录浏览目录，以及 `.env / *.env / *.txt / *.json / *.ndjson` 文件；`.example` 模板文件默认不直接显示在文件列表里；`.md` 文件只会在当前 `PROVIDERS_DIR` 顶层显示，根目录和 provider 子目录都不会直接显示；日志目录与调试归档目录都会按最新修改时间优先显示，浏览页顶部支持手动刷新当前目录列表
- **文件编辑**：紧凑顶栏显示当前文件，点击文件名可查看完整路径；顶部文件名会自动截断，避免把保存按钮挤出屏幕；编辑器默认不自动换行，可横向滚动查看长内容
- **`.env` 专用双模式编辑**：支持“条目式 / 源文式”滑动切换；条目式里字段名固定、注释只读；`LISTEN_ADDR` 为纯端口时无需手写前导冒号；移动端也支持继续缩小编辑器字号，以便同屏显示更多内容
- **管理动作**：
  - 根目录只有在 `.env` **不存在**时才显示“新建 env”，点击后会直接按根模板创建 `.env`
  - 当前 `PROVIDERS_DIR` 顶层的“新建 env”默认以 `<PROVIDERS_DIR>/openai.env.example` 作为示例模板，创建结果是新的 `*.env`
  - 当前 `PROVIDERS_DIR` 顶层还提供“新增 md”，输入时不用带 `.md` 后缀，管理台会自动创建空白 `.md` 文件
  - 长按文件条目可选择重命名或删除；provider `.env` 改文件名会自动同步 `PROVIDER_ID`，直接改 `PROVIDER_ID` 保存时也会自动同步文件名
- **运行状态**：查看健康状态、服务启动时间、结构化日志目录、当前脚本任务，并执行 `部署 / 重启 / 停止 / 卸载`；状态页在页面保持可见时会每 3 秒自动刷新一次，同时保留手动“刷新状态”能力；脚本任务会在页面内持续显示运行态和输出，`重启` / `部署` 会尽量自动恢复当前管理台会话

> 管理台的目标是“改错配置后界面尽量还能继续用”。保存文件后会立刻返回热加载 / 重启校验结果，但不会因为配置校验失败就把当前网页界面直接做没；如果浏览器还停留在旧样式，先强制刷新一次页面，确保重新拉取最新的管理台静态资源。

### 6）WebUI 使用建议

- 首次进入时先确认根 `.env` 里的 `PROXY_API_KEY` 已设置，否则虽然页面能打开，但无法登录
- 推荐优先在“文件编辑”里改配置，再去“运行状态”里做 `重启` 或 `部署`
- 如果你只是想补一个新的 provider 配置，先到当前 `PROVIDERS_DIR` 顶层用“新建 env”从 `<PROVIDERS_DIR>/openai.env.example` 复制出新文件，再填自己的 `PROVIDER_ID / UPSTREAM_*`
- 如果你要补 provider 级说明或提示词文件，也是在当前 `PROVIDERS_DIR` 顶层使用“新增 md”；该入口不会出现在根目录或 provider 子目录
- 如果你想初始化根配置，则在根目录确保 `.env` 不存在后再使用“新建 env”，这样会直接创建根 `.env`
- 如果移动端上觉得源码字体太大，可以继续双指缩小编辑器字号；源码模式默认保留横向滚动，不会强制换行

---

## 🌐 路由规则

### 推荐：显式 provider 路由

- `/{providerId}/v1/responses`
- `/{providerId}/v1/responses/compact`
- `/{providerId}/v1/chat/completions`
- `/{providerId}/v1/messages`
- `/{providerId}/v1/models`

例如：

- `/openai/v1/responses`
- `/openai/v1/responses/compact`
- `/openai/v1/chat/completions`
- `/claude/v1/messages`

### 兼容：默认 provider 裸路由

当 `ENABLE_LEGACY_V1_ROUTES=true` 且 `DEFAULT_PROVIDER` 可用时，也支持：

- `/v1/responses`
- `/v1/responses/compact`
- `/v1/chat/completions`
- `/v1/messages`
- `/v1/models`

其中 `DEFAULT_PROVIDER` 支持逗号分隔多个 provider，例如 `openai,azure`：

- 越靠后的 provider 优先级越高
- 同名模型按 overlay 规则以后面的 provider 为准
- 裸 `/v1/models` 返回这组 overlay 后的可见模型
- 裸 `/v1/responses`、`/v1/chat/completions`、`/v1/messages` 会按模型归属把请求转发到真正拥有该模型的上游 provider
- 这些 bare 请求也会严格遵循 bare `/v1/models` 的可见模型集合；不在列表中的模型会直接报错，不再走隐式 wildcard fallback
- bare 默认分组保留根路径特权入口语义：只要根 `PROXY_API_KEY` 通过，bare `/v1/*` 就可以继续访问默认分组 overlay 后的能力，不会再按真正命中的 provider 重新套一层 `PROXY_API_KEY_OVERRIDE` 限制
- 这也意味着 bare `/v1/models` 展示的是完整默认分组 overlay 结果；如果你需要某个 provider 严格按它自己的私有鉴权隔离，请使用显式 `/{providerId}/v1/*` 路由

如果启用了 `ENABLE_DEFAULT_PROVIDER_MODEL_TAGS=true`，则默认分组会切到“标签模式”：

- 这时会放弃上面的 overlay 覆盖模式，不再用“后面的 provider 覆盖前面的 provider”来隐藏同名模型
- `ENABLE_ALL_DEFAULT_PROVIDER_MODEL_TAGS=false`（默认）时：只有冲突/重叠模型显示成 `[providerId]model`；唯一模型仍保留原名
- `ENABLE_ALL_DEFAULT_PROVIDER_MODEL_TAGS=true` 时：默认分组下所有模型都显示成 `[providerId]model`
- 标签模式开启后，裸 `/v1/responses`、`/v1/chat/completions`、`/v1/messages` 里的带标签模型会强制路由到对应 provider，并在真正发给上游前去掉标签
- 标签模式开启后，未带标签的冲突模型不会再回退到 overlay 顶层 provider；客户端必须显式指定标签

### 管理台裸根路径

- `/`：内置 Web 管理台入口
- `/_admin/assets/*`：管理台静态资源
- `/_admin/api/*`：管理台内部接口

### 无 /v1 路由别名

以下路径均可等效访问对应 `/v1/*` 入口，复用同一套 handler 与鉴权语义：

- `/responses`
- `/responses/compact`
- `/chat/completions`
- `/messages`
- `/models`

以及对应的显式 provider 路由形式：

- `/{providerId}/responses`
- `/{providerId}/responses/compact`
- `/{providerId}/chat/completions`
- `/{providerId}/messages`
- `/{providerId}/models`

这些别名是精确路径映射回 canonical `/v1/*`，不会引入额外的 handler 或重复的鉴权逻辑。

---

## 🔐 鉴权约定

代理层支持以下 header：

- `Authorization: Bearer <proxy-key>`
- `X-API-Key: <proxy-key>`
- `Api-Key: <proxy-key>`

补充说明：

- 对显式 `/{providerId}/v1/*` 路由，代理鉴权始终按该 provider 自己的 `PROXY_API_KEY_OVERRIDE` / root `PROXY_API_KEY` 规则执行。
- 对 bare 默认分组 `/v1/*` 路由，代理仍按根 `PROXY_API_KEY` 作为默认分组特权入口来校验；模型归属只影响真正转发到哪个上游 provider，不会把 bare 根路径变成显式 provider 路由那套私有鉴权语义。

内置 Web 管理台也使用同一个 `PROXY_API_KEY` 作为登录密码，但不会把这个密码明文持久化在浏览器里；登录后会下发 HttpOnly 会话 cookie，并对写操作额外做 CSRF 校验。

上游 key 透传支持：

- `X-Upstream-Authorization: Bearer <real-upstream-key>`

如果请求里没有 `X-Upstream-Authorization`：

- 当当前路由**不要求代理鉴权**时，`Authorization` 可能直接作为上游鉴权透传
- 对 Anthropic / Claude 风格客户端，当当前路由**不要求代理鉴权**时，`X-API-Key` / `x-api-key` 也可以直接作为上游 key
- 否则回退到 provider 自己的 `UPSTREAM_API_KEY`

---

## 📡 响应头与透明度

代理会在**鉴权成功**的正常请求里额外返回一组透明度响应头，用来帮助客户端确认：

- 客户端实际把什么模型 / 推理强度 / 服务层级发给了代理
- 代理最终把什么模型 / 推理参数 / 服务层级发给了上游

当前默认返回：

| 响应头 | 作用 | 示例 |
|---|---|---|
| `X-Request-Id` | 本次请求在代理层的唯一追踪 ID | `req-1743870000000000000-1` |
| `X-Cache-Info-Timezone` | 当前运行时使用的 `CACHE_INFO_TIMEZONE`，同时影响 Cache_Info 统计展示和版本时间响应头的格式化时区 | `Asia/Shanghai` |
| `X-Client-To-Proxy-Model` | 客户端发给代理的原始模型名，**保留 suffix**，方便确认 `model-high` 这类写法是否真的进到了代理层 | `gpt-5-high` |
| `X-Client-To-Proxy-Service-Tier` | 客户端发给代理的原始服务层级；没有传时为空字符串 | `priority` |
| `X-Client-To-Proxy-Reasoning-Parameters` | 客户端 → 代理这段链路里，代理按本地优先级（如 suffix 优先于请求体）处理后得到的客户端侧推理参数组；不同下游端口会保持各自协议视角 | `{"thinking":{"type":"enabled","budget_tokens":2048}}` |
| `X-Client-To-Proxy-Reasoning-Effort` | 客户端这一侧最终体现出来的推理强度摘要，便于快速看出 `low/medium/high/xhigh` | `high` |
| `X-Proxy-To-Upstream-Model` | 代理最终实际发给上游的模型名；如果启用了 `MODEL_MAP`，这里会直接展示映射后的结果 | `claude-sonnet-4-5` |
| `X-Proxy-To-Upstream-Service-Tier` | 代理最终实际发给上游的服务层级；如果 provider 配置覆写了该值，这里会显示覆写后的结果；没有值时为空字符串 | `priority` |
| `X-Proxy-To-Upstream-Reasoning-Parameters` | 代理最终实际发给上游的推理参数，按上游协议类型展示为紧凑 JSON 字符串 | `{"reasoning":{"effort":"high","summary":"auto"}}` |
| `X-Env-Version` | 当前根 `.env` 的热加载版本戳 | `2026-03-25T11:03:00.111Z` |
| `X-Provider-Name` | 本次请求命中的 provider ID | `openai` |
| `X-Provider-Version` | 当前 provider 配置的热加载版本戳 | `2026-03-25T11:04:00.222Z` |
| `X-SYSTEM-PROMPT-ATTACH` | 当 provider 级系统提示词真的注入时，展示注入位置与原始文件串 | `prepend:prompt.md, prompts/extra.md` |

补充说明：

- 这组透明度响应头**不需要额外配置变量**，默认直接返回。
- `X-Client-To-Proxy-*` 关注的是**客户端 → 代理**这段链路。
- `X-Proxy-To-Upstream-*` 关注的是**代理 → 上游**这段链路。
- 服务层级响应头在没有值时也会保留为空字符串，便于客户端区分“头不存在”和“本次请求未设置服务层级”。
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

### 3. responses 上游工具兼容模式

每个 provider 可通过 `RESPONSES_TOOL_COMPAT_MODE` 控制 responses 上游请求体中的工具类型处理策略：

- `preserve`（默认）：保留原始工具类型（`custom` / `web_search` / `function`），原样发给 responses 上游
- `function_only`：将 `custom` 和 `web_search` 工具在发往 responses 上游前重写成普通 `function` 类型，以兼容不完全支持这些工具类型的上游

**代价说明**：

- `custom` 变成 `function` 后，会丢失原生 free-form / grammar 约束语义
- `web_search` 变成 `function` 后，会丢失原生 citations / sources / agentic search 语义

这个字段只在 `UPSTREAM_ENDPOINT_TYPE=responses` 时生效；`function_only` 仅影响 responses-upstream 请求体构造，不会改变 chat / anthropic 上游的请求体。

### 3.5 OpenAI 协议服务层级覆写

每个 provider 现在还可以通过 `OPENAI_SERVICE_TIER` 控制发往 OpenAI 协议上游的服务层级：

- 留空：代理不自动携带服务层级，沿用下游请求里的 `service_tier` / `serviceTier`
- `auto` / `default` / `flex` / `priority`：忽略下游传入值，统一使用 provider 配置发往 OpenAI `responses` 或 `chat` 上游

补充说明：

- 这个字段仅在 `UPSTREAM_ENDPOINT_TYPE=responses` 或 `chat` 时生效
- `Fast` 模式对应的就是 `priority`
- 写成非法值时，provider 配置会直接校验失败，不会静默回退

### 4. 模型映射与 reasoning suffix

支持：

- `MODEL_MAP` 通配符映射
- `$0` 与 `$1..$N` 占位符替换（按通配符捕获数量动态生效，没有写死到 `$2`）
- `MANUAL_MODELS` 手动补模型
- `HIDDEN_MODELS` 手动隐藏模型（支持通配符）
- `ENABLE_REASONING_EFFORT_SUFFIX=true` 后解析 `-low/-medium/-high/-xhigh`
- `EXPOSE_REASONING_SUFFIX_MODELS=true` 后在 `/models` 里暴露 suffix 变体
- `MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING=true` 时，把 suffix 或请求体里解析出的 effort 自动映射到 Anthropic thinking

这些变量的实际含义：

- `MODEL_MAP`：把下游请求里的模型名重写成上游真正要调用的模型名
- `MANUAL_MODELS`：当上游不返回 `/models` 列表或列表不完整时，手动补齐可展示模型
- `HIDDEN_MODELS`：从当前 provider 的可见模型列表里手动移除模型；支持 `*` 通配符，主要用于 overlay / 标签模式下做精细屏蔽
- `ENABLE_REASONING_EFFORT_SUFFIX`：允许像 `model-high` 这样的模型后缀直接表示推理强度
- `EXPOSE_REASONING_SUFFIX_MODELS`：让 `/models` 也把这些后缀变体展示给客户端
- `MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING`：当上游是 anthropic 协议时，把 effort 自动翻译成 `thinking` / `output_config`

当前实现里，**请求准入会遵循代理实际对外返回的 `/models` 列表**：

- 默认分组 bare `/v1/*` 只允许请求当前 bare `/v1/models` 里可见的模型
- 显式 `/{providerId}/v1/*` 也只允许请求该 provider 自己 `/models` 列表里可见的模型
- 不在对应 `/models` 列表里的模型，请求会直接返回 `400 invalid_model`
- 如果同时开启了 `EXPOSE_REASONING_SUFFIX_MODELS=true`，那么 suffix 变体是否允许请求，也严格跟随 `/models` 里的可见结果

当前已验证的推理强度入口包括：

- `/v1/responses` 请求体 `reasoning.effort`
- `/v1/chat/completions` 请求体 `reasoning_effort` 或 `reasoning.effort`
- 模型名 suffix：`-low / -medium / -high / -xhigh`
- `/v1/messages` 请求体 `thinking` 直传

### 5. provider 级系统提示词

支持：

- `SYSTEM_PROMPT_FILES=prompt.md,...`
- `SYSTEM_PROMPT_POSITION=prepend|append`

对应文件内容支持热加载。

### 6. 调试归档与日志

- `LOG_ENABLE` / `LOG_FILE_PATH` / `LOG_MAX_BODY_SIZE_MB` / `LOG_MAX_REQUESTS`：结构化日志；其中 `LOG_ENABLE` 也是调试归档的父开关
- `OPENAI_COMPAT_DEBUG_ARCHIVE_DIR`：默认值是 `OPENAI_COMPAT_DEBUG_ARCHIVE_DIR`；只有当 `LOG_ENABLE=true` 且该字段非空时，才会按 `request_id` 写出：
  - `request.ndjson`
  - `raw.ndjson`
  - `canonical.ndjson`
  - `final.ndjson`
  - 目录最大保留数量由 `OPENAI_COMPAT_DEBUG_ARCHIVE_MAX_REQUESTS` 独立控制，超过后按目录修改时间自动清理旧 request_id 目录；与 `LOG_MAX_REQUESTS` 完全解耦

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
| 路由与鉴权 | `PROXY_API_KEY`、`DEFAULT_PROVIDER`、`ENABLE_DEFAULT_PROVIDER_MODEL_TAGS`、`ENABLE_ALL_DEFAULT_PROVIDER_MODEL_TAGS`、`ENABLE_LEGACY_V1_ROUTES` | ✅ |
| 下游策略与超时 | `DOWNSTREAM_NON_STREAM_STRATEGY`、`CONNECT_TIMEOUT`、`FIRST_BYTE_TIMEOUT`、`IDLE_TIMEOUT`、`TOTAL_TIMEOUT` | ✅ |
| 上游伪装相关 | `UPSTREAM_USER_AGENT`、`UPSTREAM_MASQUERADE_TARGET`、`UPSTREAM_INJECT_METADATA_USER_ID`、`UPSTREAM_INJECT_CLAUDE_SYSTEM_PROMPT` | ✅ |
| provider 目录 | `PROVIDERS_DIR` | ⚠️ 部分；provider 监听会切换，但 Cache_Info 落盘目录需重启 |
| 启动期字段 | `LISTEN_ADDR`、`CACHE_INFO_TIMEZONE`、`LOG_*`、`OPENAI_COMPAT_DEBUG_ARCHIVE_DIR`、`OPENAI_COMPAT_DEBUG_ARCHIVE_MAX_REQUESTS` | ❌ 修改后需重启 |

### Cache_Info 聚合文件

`<PROVIDERS_DIR>/Cache_Info/` 现在默认会生成三类聚合 TXT：

- `全提供商总计.txt`
- `已启用提供商总计.txt`
- `v1默认分组统计.txt`

其中 `v1默认分组统计.txt` 只汇总当前 `DEFAULT_PROVIDER` 列表里、并且仍处于启用状态的 provider。无论默认分组当前走的是 overlay 模式还是标签模式，这个聚合文件都会按默认分组实际参与的 provider 集合统计。

### provider `.env`

| 字段组 | 例子 | 说明 |
|---|---|---|
| 上游连接 | `UPSTREAM_BASE_URL`、`UPSTREAM_API_KEY`、`UPSTREAM_ENDPOINT_TYPE` | 当前 provider 如何连上游 |
| Anthropic / thinking | `ANTHROPIC_VERSION`、`UPSTREAM_THINKING_TAG_STYLE` | Anthropic 上游版本与 chat 上游 thinking 标签策略 |
| 能力开关 | `SUPPORTS_CHAT`、`SUPPORTS_RESPONSES`、`SUPPORTS_MODELS`、`SUPPORTS_ANTHROPIC_MESSAGES` | 控制公开端口是否开放 |
| 非流 / timeout / retry | `DOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE`、`UPSTREAM_FIRST_BYTE_TIMEOUT`、`UPSTREAM_RETRY_COUNT`、`UPSTREAM_RETRY_DELAY` | provider 级运行时策略 |
| 提示词与模型 | `SYSTEM_PROMPT_FILES`、`SYSTEM_PROMPT_POSITION`、`MODEL_MAP`、`MANUAL_MODELS`、`HIDDEN_MODELS` | 注入与模型能力 |
| 推理强度 | `ENABLE_REASONING_EFFORT_SUFFIX`、`EXPOSE_REASONING_SUFFIX_MODELS`、`MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING` | suffix / thinking 相关 |
| OpenAI 服务层级 | `OPENAI_SERVICE_TIER` | 仅 OpenAI `responses/chat` 上游生效；留空时沿用下游传参，非空时强制覆写 |
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

7. **`/v1/responses/compact` 是 Responses 专用非流端点，不支持 `stream=true`**
   仅在 provider 的 `UPSTREAM_ENDPOINT_TYPE=responses` 时可用；请求 `stream=true` 或上游不是 `responses` 类型时直接返回 `400`。成功时直接返回上游原始 JSON，不再走聚合归一逻辑。

8. **普通 `/v1/responses` SSE 流现可携带 compaction items 和 opaque `encrypted_content`**
   这些字段不会被代理层解析或丢弃，会完整透传给下游客户端。

---

## 🛠️ 部署与运维

| 脚本 | 作用 |
|---|---|
| `bash scripts/deploy-linux.sh` | 预检、编译、停旧、启新、健康检查、失败回滚 |
| `bash scripts/restart-linux.sh` | 重启服务 |
| `bash scripts/stop-linux.sh` | 停止服务 |
| `bash scripts/uninstall-linux.sh` | 卸载部署产物 |

如果你主要通过内置管理台维护服务，推荐的工作流是：

1. 在 `/` 登录管理台
2. 先到“文件编辑”里修改 `.env` 或 provider 配置
3. 根据页面里的热加载 / 重启校验结果确认配置是否合法
4. 再到“运行状态”页执行 `重启` 或 `部署`

如果前面还有 1Panel / Nginx / OpenResty，建议先把长连接超时放宽：

```nginx
proxy_connect_timeout 1200s;
proxy_send_timeout 1200s;
proxy_read_timeout 1200s;
send_timeout 1200s;
```

---

## 🔬 缓存测试（对照复测流程）

### 概述

当用户说"缓存测试"时，执行以下对照复测流程。该流程不是内置命令、slash command、脚本别名或 skill，而是写在文档中的人类可阅读规范。

### 核心约束

| 约束项 | 要求 |
|---|---|
| 代理组 | 经过代理层的完整请求路径 |
| 直连上游对照组 | 同一 canonical scenario 直接发往上游，不走代理；作为基线（100%） |
| 会话轮数 | 5 轮 |
| 首轮输入 | >10K 字符 |
| 后续每轮输入 | >5K 字符 |
| 工具调用 | 包含工具定义与工具结果 |
| 上下文累积 | 每轮共享同一长前缀并累积上下文 |

当前 harness 用 `input_chars` 校验长前缀规模，`usage.input_tokens`、raw vendor metrics 和 normalized cache ratio 只作为结果观测，不反过来替代输入阈值 gate。

### Turn 1 与 Turn 2+ 的区分

- **Turn 1**：单独展示原始指标与 OpenAI 风格归一化值，**不进入** preservation/loss 主结论
- **Turn 2+**：使用 `proxy_preservation = proxy_ratio / direct_ratio` 与 `proxy_loss = 1 - proxy_preservation` 计算损失率

### OpenAI 风格归一化口径

```
normalized_input_tokens = uncached_input + cache_read_input + cache_creation_input
normalized_cached_tokens = cache_read_input
normalized_cache_ratio = normalized_cached_tokens / normalized_input_tokens
proxy_preservation = proxy_ratio / direct_ratio
proxy_loss = 1 - proxy_preservation
```

### 对照组与配对规则

- 代理组与直连组必须完全同构：同一 round、同一 turn、同一模型、同一工具、同一参数、同一输入结构
- 直连组按 upstream family 运行原生 endpoint（responses/chat/messages），并与同 upstream family 的代理组配对
- 每轮独立重跑，不复用上轮结果

### 结果产物

| 产物 | 说明 |
|---|---|
| 原始指标表 | 各 upstream 家族的 raw vendor metrics |
| 直连基线表 | Direct baseline cache ratio |
| 代理保留率/损失率表 | `proxy_preservation` 与 `proxy_loss`，基于 direct baseline 计算 |
| deferred 问题摘要 | 发现但不在本轮修复的问题清单 |

实际 live 输出固定分成四段：`## Raw Vendor Metrics`、`## Direct Baseline`、`## Proxy Preservation / Loss`、`## Deferred Issues`。其中前两段会同时保留 `turn1` 与 `turn2+` 证据，`Proxy Preservation / Loss` 只汇总 `turn2+`。

### 损失率展示规则

- 当 `direct_ratio` 约等于 0 时，`preservation` 与 `loss` 标记为 `N/A`
- 不在不同 upstream family 之间横向比较原始缓存率
- `cache_creation_tokens` 不记作缓存读取命中率

### deferred 问题处理

- 发现的问题明确记录为 `deferred`
- 不在本流程中修复生产代码
- 后续计划任务会处理这些问题

### 执行入口

详细测试代码与完整实验执行，见 `internal/httpapi/live_cache_matrix_test.go` 中的 live test harness。

实际复跑命令：

```bash
LIVE_CACHE_MATRIX_ENABLED=1 \
LIVE_CACHE_BASE_URL=http://127.0.0.1:21021 \
LIVE_CACHE_API_KEY=your-proxy-key \
LIVE_CACHE_PROVIDER_MATRIX_JSON='[
  {"label":"responses","provider_id":"provider-responses","model":"model-a","direct_base_url":"https://responses.example/v1","direct_api_key":"..."},
  {"label":"chat","provider_id":"provider-chat","model":"model-b","direct_base_url":"https://chat.example/v1","direct_api_key":"..."},
  {"label":"anthropic","provider_id":"provider-anthropic","model":"model-c","direct_base_url":"https://anthropic.example","direct_api_key":"..."}
]' \
go test -run TestLiveCacheMatrix -timeout 40m -v ./internal/httpapi
```

说明：

- `LIVE_CACHE_PROVIDER_MATRIX_JSON` 必须至少包含 `responses`、`chat`、`anthropic` 三个 `label`，且每项都要提供 `provider_id`、`model`、`direct_base_url`、`direct_api_key`
- 未设置 `LIVE_CACHE_MATRIX_ENABLED=1` 时，`TestLiveCacheMatrix` 会直接 skip
- 已开启 live 模式但缺少 `LIVE_CACHE_BASE_URL`、`LIVE_CACHE_API_KEY` 或 `LIVE_CACHE_PROVIDER_MATRIX_JSON` 时，测试会立即失败并提示缺失字段
- `LIVE_CACHE_REQUEST_TIMEOUT` 是 **每个 live 请求自己的 scoped timeout**；单个 combo 卡死时，harness 会按该值快速报错，并在错误输出里保留 `round / execution_path / downstream / upstream / route / turn / stage` 归因，而不是一直等到外层 `go test -timeout` 才整体 panic

---

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
