# openai-compat-proxy

一个面向正式长期使用场景的 Go 单二进制代理：**每个 provider 内部只走一种上游正规协议**，但对外统一提供 OpenAI Responses、Chat Completions、Anthropic Messages 三套兼容入口，并由代理层完成协议互转、语义补全与流式兜底。

> 适合把不同上游站点收敛到一套稳定入口：三套下游入口 × 三种上游协议类型可交叉组合，代理层负责多 provider 路由、热加载、流式容错、工具调用适配、reasoning / thinking 兼容和部署运维兜底。

---

## ✨ 现在能做什么

| 能力 | 当前状态 | 说明 |
|---|---|---|
| 多 provider 路由 | ✅ | 支持 `providers/*.env`、显式 `/{providerId}/v1/*` 和默认 provider overlay 裸 `/v1/*` |
| 三套兼容入口 | ✅ | `POST /v1/responses`、`POST /v1/chat/completions`、`POST /v1/messages`、`GET /v1/models` 全部可用 |
| OpenAI Images / Embeddings / Rerank 透传端口 | ✅ | 默认导出 `POST /v1/images/generations`、`/v1/images/edits`、`/v1/images/variations`、`/v1/embeddings`、`/v1/rerank`，按 provider 选择与模型映射透传到上游 |
| `3×3` 协议矩阵 | ✅ | 三个下游入口可分别对接 `responses / chat / anthropic` 三种上游协议类型 |
| provider 内部统一走一种上游协议 | ✅ | `UPSTREAM_ENDPOINT_TYPE=responses/chat/anthropic`，代理层负责跨协议适配 |
| 流式 / 非流式 | ✅ | 支持 SSE 转发、超时兜底、错误终态补发，也支持 `proxy_buffer` / `upstream_non_stream` |
| 工具调用与多轮 tool result 回传 | ✅ | 三套入口都已实现；其中 `/v1/responses` 的高层语义覆盖最全面 |
| reasoning / thinking 兼容 | ✅ | 支持请求体参数、模型 suffix、Anthropic thinking、上游 reasoning 内容回写与流式拆分 |
| Responses compact 端点 | ✅ | `/v1/responses/compact` 非流式 Responses 专用，上游须为 `responses` 类型；普通 `/v1/responses` SSE 流现可携带 compaction items |
| 无 /v1 路由别名 | ✅ | `/responses`、`/chat/completions`、`/messages`、`/models`、`/responses/compact` 及其 `/{providerId}/...` 形式均可等效访问对应 `/v1/*` 入口；复用同一套 handler 与鉴权语义 |
| responses 工具兼容模式 | ✅ | provider 级 `RESPONSES_TOOL_COMPAT_MODE=function_only` 将 `custom` / `web_search` 工具在发往 responses 上游前重写成普通 function 类型，以兼容不完整上游；代价是丢失原生 free-form / grammar 约束（custom）或原生 citations/sources 语义（web_search）；默认 `preserve` |
| chat 上游 XML 工具调用兼容 | ✅ | provider 级 `UPSTREAM_XML_TOOL_CALL_STYLE=true` 可把完整 XML 工具调用正文恢复成结构化 function call；默认开启 |
| 模型映射 | ✅ | 支持 `MODEL_MAP` / `V1_MODEL_MAP` 字面量映射、`#re:` 正则 pattern、`$0-$9` 捕获占位符、`MANUAL_MODELS` 正则扩展、`HIDDEN_MODELS` 正则隐藏；`MODEL_MAP` 不会自动注入 `/models` |
| 输出与上下文限制 | ✅ | 支持 root/provider 级 `UPSTREAM_MAX_OUTPUT_TOKENS`、`FORCE_UPSTREAM_MAX_OUTPUT_TOKENS` 和 `MODEL_LIMIT_CONTEXT_TOKENS`，均支持 scoped 模型规则 |
| provider 级系统提示词 | ✅ | `SYSTEM_PROMPT_FILES` + `SYSTEM_PROMPT_POSITION` |
| 伪装客户端（实验性） | ✅ | 支持 `opencode` / `claude` / `codex` / `none` |
| 调试归档 | ✅ | 仅当 `LOG_ENABLE=true` 且 `OPENAI_COMPAT_DEBUG_ARCHIVE_DIR` 非空时写出 `request/raw/canonical/final.ndjson`；保留数量由 `OPENAI_COMPAT_DEBUG_ARCHIVE_MAX_REQUESTS` 独立控制（与 `LOG_MAX_REQUESTS` 解耦） |
| 动态 token estimator（phase-1） | ✅ | 基于 `provider + upstream_endpoint_type + 最终上游模型` 持久化记录真实 usage，生成 JSON/TXT 观测状态与建议修正；当前只做学习与展示，不直接参与上下文拦截准入 |
| 健康检查与 Linux 部署脚本 | ✅ | 自带 `healthz`、deploy / restart / stop / uninstall |
| 内置 Web 管理台 | ✅ | 裸根路径 `/` 提供 Material 3 风格管理界面：文件浏览 / 文件编辑 / 运行状态 |

---

## 🧭 工作方式

```mermaid
flowchart LR
    A[客户端] --> B[/responses\n/chat/completions\n/messages\n/models\n/images/*\n/embeddings\n/rerank]
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
V1_MODEL_MAP=
ENABLE_LEGACY_V1_ROUTES=true

DOWNSTREAM_NON_STREAM_STRATEGY=proxy_buffer

CONNECT_TIMEOUT=30s
FIRST_BYTE_TIMEOUT=30m
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
- `/{providerId}/v1/images/generations`
- `/{providerId}/v1/images/edits`
- `/{providerId}/v1/images/variations`
- `/{providerId}/v1/embeddings`
- `/{providerId}/v1/rerank`

例如：

- `/openai/v1/responses`
- `/openai/v1/responses/compact`
- `/openai/v1/chat/completions`
- `/claude/v1/messages`
- `/openai/v1/images/generations`
- `/openai/v1/embeddings`
- `/openai/v1/rerank`

### 兼容：默认 provider 裸路由

当 `ENABLE_LEGACY_V1_ROUTES=true` 且 `DEFAULT_PROVIDER` 可用时，也支持：

- `/v1/responses`
- `/v1/responses/compact`
- `/v1/chat/completions`
- `/v1/messages`
- `/v1/models`
- `/v1/images/generations`
- `/v1/images/edits`
- `/v1/images/variations`
- `/v1/embeddings`
- `/v1/rerank`

其中 `DEFAULT_PROVIDER` 支持逗号分隔多个 provider，例如 `openai,azure`：

- 越靠后的 provider 优先级越高
- 同名模型按 overlay 规则以后面的 provider 为准
- 裸 `/v1/models` 返回这组 overlay 后的可见模型
- 裸 `/v1/responses`、`/v1/chat/completions`、`/v1/messages` 会按模型归属把请求转发到真正拥有该模型的上游 provider
- 这些 bare 请求也会严格遵循 bare `/v1/models` 的可见模型集合；不在列表中的模型会直接报错，不再走隐式 pattern fallback
- bare 默认分组保留根路径特权入口语义：只要根 `PROXY_API_KEY` 通过，bare `/v1/*` 就可以继续访问默认分组 overlay 后的能力，不会再按真正命中的 provider 重新套一层 `PROXY_API_KEY_OVERRIDE` 限制
- 这也意味着 bare `/v1/models` 展示的是完整默认分组 overlay 结果；如果你需要某个 provider 严格按它自己的私有鉴权隔离，请使用显式 `/{providerId}/v1/*` 路由

`V1_MODEL_MAP` 是裸 `/v1/*` 专用的根级预映射，格式与 provider 级 `MODEL_MAP` 相同，例如：

```env
V1_MODEL_MAP=gpt-5.5:gpt-5.6,#re:alias-(.*):real-$1
```

它只在 bare `/v1/*` 和无 `/v1` 裸别名上生效，不会影响显式 `/{providerId}/v1/*` 路由。`src` 默认按字面量精确匹配，所以 `gpt-5.5` 里的点就是普通点；只有以 `#re:` 开头时才按 Go regexp 全字符串匹配。捕获替换只支持 `$0-$9`，其中 `$0` 是完整匹配，`$1-$9` 是第 1 到第 9 个 regexp 捕获组，例如 `#re:mini(.*)o:real-$0-$1` 会把 `mini2o` 映射成 `real-mini2o-2`；`$10` 及以上会保留字面值，避免 `$12` 到底表示第 12 组还是 `$1` 后接 `2` 的歧义；如果要输出字面 `$`，在 target 中写成 `\$`，例如 `\$12` 输出 `$12`。根级 `V1_MODEL_MAP` 对 reasoning family 生效：`gpt-5.5`、`gpt-5.5-high`、以及“`gpt-5.5` + 显式 reasoning 参数”的等效请求，都会按同一家族进行预映射；`-noprompt` 仍然只是代理标记，只有在该能力开启时才会在代理层剥离，不参与映射 key。`V1_MODEL_MAP` 不是纠错或兜底机制；如果映射 target 不在默认分组可见模型列表中，请求仍会按现有 `invalid_model` 路径失败。该字段属于根级可热加载配置。

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
- `/images/generations`
- `/images/edits`
- `/images/variations`
- `/embeddings`
- `/rerank`

以及对应的显式 provider 路由形式：

- `/{providerId}/responses`
- `/{providerId}/responses/compact`
- `/{providerId}/chat/completions`
- `/{providerId}/messages`
- `/{providerId}/models`
- `/{providerId}/images/generations`
- `/{providerId}/images/edits`
- `/{providerId}/images/variations`
- `/{providerId}/embeddings`
- `/{providerId}/rerank`

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
| `X-This-Usage-Tokens` | 本次响应的 usage 摘要；普通响应头常驻，上游没有返回可用 usage 时为空字符串；流式请求会同时在 trailer 里补同名值 | `↑ 1,333,111(1,111,001 cached) \| ↓ 1,231` |
| `X-Client-To-Proxy-Model` | 客户端发给代理的原始模型名，**保留 suffix**，方便确认 `model-high` 这类写法是否真的进到了代理层 | `gpt-5-high` |
| `X-Client-To-Proxy-Service-Tier` | 客户端发给代理的原始服务层级；没有传时为空字符串 | `priority` |
| `X-Client-To-Proxy-Reasoning-Parameters` | 客户端 → 代理这段链路里，代理按本地优先级（如 suffix 优先于请求体）处理后得到的客户端侧推理参数组；不同下游端口会保持各自协议视角 | `{"thinking":{"type":"enabled","budget_tokens":2048}}` |
| `X-Client-To-Proxy-Reasoning-Effort` | 客户端这一侧最终体现出来的推理强度摘要，便于快速看出 `none/minimal/low/medium/high/xhigh` | `high` |
| `X-Client-To-Proxy-NoPrompt` | 客户端请求里 `-noprompt` 标记是否在代理层实际生效；标记生效时为 `true`，带了标记但被配置关闭时为 `false` | `true` |
| `X-Proxy-To-Upstream-Model` | 代理最终实际发给上游的模型名；如果启用了 `MODEL_MAP`，这里会直接展示映射后的结果 | `claude-sonnet-4-5` |
| `X-Proxy-To-Upstream-Service-Tier` | 代理最终实际发给上游的服务层级；如果 provider 配置覆写了该值，这里会显示覆写后的结果；没有值时为空字符串 | `priority` |
| `X-Proxy-To-Upstream-Max-Output-Tokens` | 代理经过客户端请求、provider 默认值和强制开关处理后，最终实际发给上游的最大输出 token 数；没有最终值时不返回 | `64000` |
| `X-Proxy-Model-Limit-Context-Tokens` | 代理层对当前最终上游模型命中的上下文窗口限制；`-1` 表示代理层不主动限制 | `400000` |
| `X-Provider-Name` | 本次请求命中的 provider ID | `openai` |
| `X-Provider-Today-Cache-Rate` | 本次请求命中 provider 今日缓存率 | `25.00 %` |
| `X-Provider-History-Cache-Rate` | 本次请求命中 provider 历史缓存率 | `25.00 %` |
| `X-Root-Env-Version` | 当前根 `.env` 的热加载版本戳 | `2026-03-25T11:03:00.111Z` |
| `X-Root-Provider-Today-Cache-Rate` | 本次请求命中根 provider 今日缓存率 | `37.50 %` |
| `X-Root-Provider-History-Cache-Rate` | 本次请求命中根 provider 历史缓存率 | `37.50 %` |
| `X-Proxy-To-Upstream-Reasoning-Effort` | 代理最终实际发给上游的推理强度摘要；响应头常驻，没有实际发给上游时为空字符串 | `high` |
| `X-Proxy-To-Upstream-Reasoning-Parameters` | 代理最终实际发给上游的推理参数，按上游协议类型展示为紧凑 JSON 字符串；响应头常驻，没有实际发给上游时为空字符串 | `{"reasoning":{"effort":"high","summary":"auto"}}` |
| `X-Provider-Version` | 当前 provider 配置的热加载版本戳 | `2026-03-25T11:04:00.222Z` |
| `X-SYSTEM-PROMPT-ATTACH` | 展示本次请求的 provider 级系统提示词附加状态；正常注入时为位置与原始文件串，`-noprompt` 生效或没有可注入内容时保留为空值 | `prepend:prompt.md, prompts/extra.md` |

补充说明：

- 这组透明度响应头**不需要额外配置变量**，默认直接返回。
- `X-Client-To-Proxy-*` 关注的是**客户端 → 代理**这段链路。
- `X-Proxy-To-Upstream-*` 关注的是**代理 → 上游**这段链路。
- 服务层级和 reasoning 透明度响应头在没有值时也会保留为空字符串，便于客户端区分“头不存在”和“本次请求未设置对应链路参数”。
- `X-Client-To-Proxy-Reasoning-Parameters` 是客户端侧的**主信息**；它展示的是客户端协议视角下、经过本地优先级解析后的参数组。
- `X-Client-To-Proxy-Reasoning-Effort` 是客户端侧的**摘要值**；如果同一请求里模型 suffix 和请求体参数同时存在，代理会按本地优先级先决出最终强度，再把这个摘要值写进来。
- `X-SYSTEM-PROMPT-ATTACH` 是本次请求的提示词附加透明度字段：正常注入 provider prompt 时显示 `prepend:prompt.md` 或 `append:...`；当 `-noprompt` 真正生效且 `X-Client-To-Proxy-NoPrompt=true` 时，这个 header 仍会存在但值为空，表示本次已明确跳过 provider prompt 注入；如果 `-noprompt` 没有生效，则继续按实际注入状态显示文件串。
- 模型名里的 reasoning suffix 优先于客户端请求体里的任何 reasoning / thinking 设置；例如 `model-low` 会覆盖客户端传入的 `thinking: {"type":"disabled"}`，`model-none` 会强制关闭推理；当上游是 Anthropic 协议时会发送 `thinking: {"type":"disabled"}`，当上游是 OpenAI 风格协议时会显式携带 `none`。
- `X-Proxy-To-Upstream-Max-Output-Tokens` 展示的是 canonical 请求里最终决定的输出上限；它会体现 `UPSTREAM_MAX_OUTPUT_TOKENS` 和 `FORCE_UPSTREAM_MAX_OUTPUT_TOKENS` 的处理结果。最终不携带输出上限时，这个响应头为空。
- `X-Proxy-Model-Limit-Context-Tokens` 展示的是代理层用于模拟上下文超限的当前命中值；它按最终上游模型匹配，`-1` 表示代理不主动限制，但真实上游仍可能返回自己的上下文超限错误。
- `X-Proxy-To-Upstream-Reasoning-Effort` 是代理到上游侧的**摘要值**；它展示代理实际写进上游请求体的推理强度。比如客户端传了 `reasoning.effort=xhigh`，但 provider 是 Anthropic 上游且 `MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING=false`，代理不会转换成 Anthropic `thinking/output_config`，而是把原始 `reasoning` 透传给上游；此时 `X-Proxy-To-Upstream-Reasoning-Effort=xhigh`，`X-Proxy-To-Upstream-Reasoning-Parameters` 会显示原始 OpenAI 风格 reasoning 字段。
- `X-Proxy-To-Upstream-Reasoning-Parameters` 展示的是**实际上游请求体里的最终字段**，所以不同上游协议可能长得不一样：
  - `responses/chat` 常见为 `{"reasoning":{...}}`
  - `anthropic` 常见为 `{"thinking":{...}}` 或同时包含 `output_config`
- `X-Cache-Info-Timezone` 展示的是当前运行时实际使用的时区；它不只影响 Cache_Info 统计展示，也会影响 `X-Root-Env-Version` / `X-Provider-Version` 这类版本时间响应头的格式化结果。
- 如果 `/v1/messages` 是直接传 `thinking`，但**没有**显式 effort，也**没有**模型 suffix，代理会保留 `thinking` 参数组到 `X-Client-To-Proxy-Reasoning-Parameters`，同时根据 `thinking` 里的预算或 `output_config` 反推出一个客户端视角的强度摘要，填到 `X-Client-To-Proxy-Reasoning-Effort`。
- 如果 `/v1/messages` 传的是 Anthropic adaptive thinking，但最终上游不是 Anthropic 协议，代理会按 `xhigh` 映射成 OpenAI 风格 reasoning；带明确预算或 `output_config.effort` 的 thinking 会继续按对应强度映射。
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
- `UPSTREAM_XML_TOOL_CALL_STYLE=true/false`：当 `UPSTREAM_ENDPOINT_TYPE=chat` 时，决定是否把完整 `<tool_call>` XML 正文恢复成结构化工具调用；默认开启

这层还有几项专门做稳定性的代理侧兜底：

- 首字节前失败可按 provider 配置重试
- 已开始 SSE 后出错会尽量保持下游流协议终态，而不是中途退化成 JSON 错误体
- 上游流式返回 `error` / `response.failed` 时，代理会把上游错误码和错误消息映射到下游终态错误里，避免被泛化成单纯的 `unexpected EOF`
- chat 上游的 reasoning 标签、reasoning_content、tool_calls 会在代理层统一归一后再下发

### 3. responses 上游工具兼容模式

每个 provider 可通过 `RESPONSES_TOOL_COMPAT_MODE` 控制 responses 上游请求体中的工具类型处理策略：

- `preserve`（默认）：保留原始工具类型（`custom` / `web_search` / `function`），原样发给 responses 上游
- `function_only`：将 `custom` 和 `web_search` 工具在发往 responses 上游前重写成普通 `function` 类型，以兼容不完全支持这些工具类型的上游；Anthropic 上游会把 Responses 的 `web_search` / `tool_choice` 翻译成 Anthropic 可用的工具形态

**代价说明**：

- `custom` 变成 `function` 后，会丢失原生 free-form / grammar 约束语义
- `web_search` 变成 `function` 后，会丢失原生 citations / sources / agentic search 语义

这个字段只在 `UPSTREAM_ENDPOINT_TYPE=responses` 时生效；`function_only` 主要影响 responses-upstream 请求体构造。Anthropic 上游会单独做自己的工具兼容翻译，避免把 Responses 内置工具原样送成 Anthropic 看不懂的形态。

### 3.5 OpenAI 协议服务层级覆写

每个 provider 现在还可以通过 `OPENAI_SERVICE_TIER` 控制发往 OpenAI 协议上游的服务层级：

- 留空：代理不自动携带服务层级，沿用下游请求里的 `service_tier` / `serviceTier`
- `auto` / `default` / `flex` / `priority`：忽略下游传入值，统一使用 provider 配置发往 OpenAI `responses` 或 `chat` 上游

补充说明：

- 这个字段仅在 `UPSTREAM_ENDPOINT_TYPE=responses` 或 `chat` 时生效
- `Fast` 模式对应的就是 `priority`
- 写成非法值时，provider 配置会直接校验失败，不会静默回退

### 3.6 根级 / provider 级输出上限

根 `.env` 可通过 `UPSTREAM_MAX_OUTPUT_TOKENS` 设置所有 provider 的默认最大输出 token 数；provider `.env` 里的同名字段留空或不写时继承根配置，显式设置时覆盖根配置：

- 根级留空且 provider 也留空：代理不设置默认值，继续使用客户端请求里的 `max_tokens` / `max_output_tokens`，或协议构造层自己的默认值
- provider 留空：继承根级 `UPSTREAM_MAX_OUTPUT_TOKENS`
- provider 显式设置：覆盖根级默认值
- 单个正整数：当客户端没有携带输出上限时，代理自动补这个值
- 默认值 + scoped 覆写：支持写成 `64000,claude-sonnet-4-5:128000,#re:.*claude-.*:100000`
- `-1`：客户端没有携带输出上限时，代理会主动省略最大输出 token 字段；如果强制开关开启，也会忽略客户端传值并省略该字段
- `FORCE_UPSTREAM_MAX_OUTPUT_TOKENS=true`：只要最终继承或覆盖后的 `UPSTREAM_MAX_OUTPUT_TOKENS` 已设置，就忽略客户端请求里的输出上限，强制使用配置值；配置值为 `-1` 时表示强制不携带该字段

scoped 覆写的匹配规则：

- 按**最终真正发给上游的模型**做字面量/正则匹配，而不是按客户端原始模型名匹配
- 字面量 base 模型名默认代表整一个推理家族，不仅覆盖显式 suffix 成员，也覆盖“模型名本身不带 suffix、但请求体显式带推理参数”的等效成员
- 精确模型匹配优先于正则匹配
- 没有任何 scoped 规则命中时，回落到默认值
- 如果只写 scoped 规则、不写默认值，则未命中任何规则时视为“不设置 provider 默认值”

例如客户端请求 `gpt-5.5` 经 `MODEL_MAP=gpt-5.5:claude-sonnet-4-5` 解析后，最终上游模型是 `claude-sonnet-4-5`，这时 `UPSTREAM_MAX_OUTPUT_TOKENS=claude-sonnet-4-5:128000` 会命中；如果同一请求进一步落到 `claude-sonnet-4-5-high` 这类更具体的成员模型，则 `claude-sonnet-4-5-high:160000` 会优先命中。`-noprompt` 只是代理层跳过 provider prompt 注入的标记，不算独立模型，所以 `gpt-5.5-high-noprompt` 这类请求在这两个限制字段上也会继续命中 `claude-sonnet-4-5-high` / `claude-sonnet-4-5` 这类不带 `-noprompt` 的同一模型规则。

这两个字段在根 `.env` 和 provider `.env` 中都支持热加载。`UPSTREAM_MAX_OUTPUT_TOKENS` 的默认值或 scoped value 写成 `0`、小于 `-1` 或非整数，或者强制开关写成非法布尔值时，新配置会直接校验失败并保留当前已生效配置。

### 3.7 代理层上下文限制

根 `.env` 和 provider `.env` 都支持 `MODEL_LIMIT_CONTEXT_TOKENS`。provider 留空或不写时继承根配置，显式设置时覆盖根配置。

- `-1`：代理层不主动限制上下文，真实上游自己的上下文窗口仍照常生效；这不是“自动探测真实上游最大值”，而是“不由代理层限制”
- 正整数：代理按请求内容估算输入 token，超过该值时直接返回 `context_length_exceeded` / `prompt is too long`
- scoped 规则：支持写成 `-1,claude-sonnet-4-5:400000,#re:.*claude-.*:256000`

匹配规则与输出上限一致，都是按**最终真正发给上游的模型**匹配；字面量 base 模型名默认代表整一个推理家族，精确成员优先于家族 base。例如客户端请求 `gpt-5.5` 经 `MODEL_MAP=gpt-5.5:claude-sonnet-4-5` 解析后，最终上游模型是 `claude-sonnet-4-5`，这时 `MODEL_LIMIT_CONTEXT_TOKENS=claude-sonnet-4-5:400000` 会命中；如果同一请求进一步落到 `claude-sonnet-4-5-high`，这条 base 规则也会命中，但如果你同时写了 `claude-sonnet-4-5-high:200000`，则 high 成员规则优先。`-noprompt` 不参与这两个限制字段的模型计数，带这个标记的请求会直接复用去掉标记后的同一模型规则。

`MODEL_LIMIT_CONTEXT_TOKENS` 和 `UPSTREAM_MAX_OUTPUT_TOKENS` 是两种不同功能：前者限制的是代理估算的**输入上下文窗口**，用于在请求进上游前模拟超限；后者控制的是发给上游的**输出上限请求参数** `max_tokens` / `max_output_tokens`。两者可以同时配置，互不替代，也不会互相抵消。上下文限制主要用于让 OpenCode 这类客户端在本地代理层就触发自动压缩；它不是 tokenizer 精确计数，也不会扩大真实上游上下文，真实上游仍可能更早或更晚返回自己的超限错误。命中限制时，OpenAI 风格入口返回 OpenAI 兼容错误外壳，Anthropic `/v1/messages` 返回 Anthropic 风格错误外壳；当前代理层会保留 `context_length_exceeded` / `prompt is too long` 这些关键词，并在 message 里附带 `estimated input tokens <current> exceed maximum <limit>`，以便 OpenCode / OMO 更稳定地识别为 token-limit 触发源。

### 4. 模型映射与 reasoning suffix

支持：

- `MODEL_MAP` 默认字面量映射，`#re:` source pattern 映射
- `$0-$9` 占位符替换（仅对 `#re:` 的 Go regexp 子匹配有捕获意义；`$10` 及以上保留字面值以避免歧义）
- `MANUAL_MODELS` 手动补模型，也支持从上游 `/models` 列表按 `#re:` 正则匹配扩展，或用 `#reason_suffix:model` 单独生成推理后缀家族
- `HIDDEN_MODELS` 手动隐藏模型（支持 `#re:` 正则）
- `ENABLE_REASONING_EFFORT_SUFFIX=true` 后解析 `-none/-minimal/-low/-medium/-high/-xhigh/-max`
- `EXPOSE_REASONING_SUFFIX_MODELS=true` 后在 `/models` 里暴露 suffix 变体
- `ENABLE_NOPROMPT_MODEL_SUFFIX=true` 后解析 `-noprompt` 代理层标记，用来跳过 provider prompt 注入
- `MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING=true` 时，把 suffix 或请求体里解析出的 effort 自动映射到 Anthropic thinking

这些变量的实际含义：

- `MODEL_MAP`：请求时模型映射。`source:target` 的左侧 source 匹配客户端请求给代理层的模型名，右侧 target 是代理准备发给上游的模型名；source 可以是字面量，也可以用 `#re:` Go regexp 全字符串匹配，target 可用 `$0-$9` 引用正则捕获。这一项里“冒号左侧为原始模型”和“冒号右侧为原始模型”要分开看：
  - 左侧 source 是原始模型 base 时，表示这条 base source 锁定整个客户端推理家族集合。例如 `MODEL_MAP=client-gpt:upstream-gpt` 会覆盖 `model=client-gpt`、`model=client-gpt-high`、`model=client-gpt-low`，也覆盖 `model=client-gpt` + `reasoning.effort=high` / `reasoning_effort=high` / Anthropic `thinking/output_config` 表达的 high；最终发往 `upstream-gpt`，effort 按客户端 suffix 或请求体参数保留。
  - 左侧 source 带 suffix 时，表示定向匹配某个推理档位，base 请求携带同档请求体参数也能反向锁定这条规则。例如 `MODEL_MAP=client-gpt-high:upstream-priority` 既匹配 `model=client-gpt-high`，也匹配 `model=client-gpt` + `reasoning.effort=high`，用于把 high 档定向转到 `upstream-priority`。这个等效匹配只作用于 MODEL_MAP，不会把 `client-gpt-high` 自动加入 `/models`。
  - 右侧 target 是原始上游模型 base 时，表示发给上游的 `model` 不带默认 reasoning suffix，只做模型名映射；最终 effort 继续来自客户端 suffix、请求体显式参数或左侧 source suffix。例如 `MODEL_MAP=client-gpt-high:upstream-gpt` 遇到 `model=client-gpt` + `reasoning.effort=high` 时，会发往 `upstream-gpt`，最终 effort 是 `high`。
  - 如果右侧 target 是原始模型 base，而客户端又显式传了请求体 reasoning 参数，则不信任左侧 source suffix 把自己的档位强灌到结果里。例如 `MODEL_MAP=gpt-5.5-xhigh:gpt-5.5` 且客户端显式传 `reasoning.effort=low` 时，最终发往 `gpt-5.5`，effort 仍是 `low`，不会被左侧 `xhigh` 覆盖。
  - 右侧 target 只有写成带 suffix 的模型时，才会提供默认 effort。例如 `MODEL_MAP=client-gpt:upstream-gpt-low` 遇到 `model=client-gpt` 且请求体没有显式 effort 时，会发往 `upstream-gpt`，最终 effort 是 `low`。
  - 右侧 target 一旦写成带 suffix 的模型，target suffix 就优先于客户端显式请求参数。例如 `MODEL_MAP=client-gpt-high:upstream-gpt-low` 遇到 `model=client-gpt` + `reasoning.effort=high` 时，仍会发往 `upstream-gpt`，但最终 effort 会被定板成 `low`；同理 `MODEL_MAP=gpt-5.4:gpt-5.4-xhigh` 遇到 `model=gpt-5.4` + `reasoning.effort=none` 时，最终也会按 `xhigh` 发给上游。只有右侧 target 是不带 suffix 的 base 模型时，客户端显式请求参数才继续生效。这些配置层 suffix 语义不受 `ENABLE_REASONING_EFFORT_SUFFIX` 限制。
  - 相同 source 多条规则时，越靠后优先级越高；后写的规则覆盖前面同 source 规则。例如 `MODEL_MAP=client-gpt:upstream-a,client-gpt:upstream-b` 最终命中 `upstream-b`。
  - 不同 source 规则不做链式递归映射，始终只对客户端原始请求模型做一次匹配。例如 `MODEL_MAP=model-a:model-c,model-c:model-d` 时，请求 `model-a` 只会得到 `model-c`，不会继续推出 `model-d`。
- `MANUAL_MODELS`：模型列表补齐和筛选。静态模型名会作为字面模型暴露；`#re:` 只从上游 `/models` 返回的原始模型列表里扩展；`#reason_suffix:model` 会为 base model 手动生成推理后缀家族。它不参与请求时 MODEL_MAP 等效匹配，所以不会因为请求体带了 `reasoning.effort=high` 就把 `client-gpt` 当作 `client-gpt-high` 加入列表或准入。`MODEL_MAP` alias 要出现在 `/models`，必须在这里显式写出来。
- `HIDDEN_MODELS`：模型列表隐藏和准入限制。它支持字面量、`#re:` 和 `#reason_suffix` family marker；`#re:` 是普通 Go regexp，不理解 suffix 语义边界。它也不参与请求时 MODEL_MAP 等效匹配，所以 `HIDDEN_MODELS=client-gpt-high` 不会阻止 `model=client-gpt` + `reasoning.effort=high` 命中 `MODEL_MAP=client-gpt-high:upstream-gpt`。
- `ENABLE_REASONING_EFFORT_SUFFIX`：只控制客户端能不能用 `model-high` 这类模型名后缀表达推理强度；它不限制 `MODEL_MAP` source/target 里的配置层 suffix，也不限制请求体里显式传入的 reasoning effort。
- `EXPOSE_REASONING_SUFFIX_MODELS`：只控制 `/models` 是否把这些后缀变体展示给客户端，不控制客户端显式请求 suffix 模型的能力
- `ENABLE_NOPROMPT_MODEL_SUFFIX`：允许像 `model-noprompt`、`model-low-noprompt` 这样的请求跳过 provider 级 `SYSTEM_PROMPT_FILES` 注入；根级默认开启，provider 级同名字段留空时继承根配置，显式 `true/false` 时覆盖根配置；`-noprompt` 会先从模型名剥离，不会出现在上游模型名里，也不会自动额外出现在 `/models` 列表里，除非你在 `MANUAL_MODELS` 里把这个字面模型写出来
- `MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING`：当上游是 anthropic 协议时，把最终解析出的 effort 翻译成 Anthropic 合法请求体字段 `thinking` / `output_config`，默认 `true`。effort 可以来自客户端模型名后缀、请求体显式参数，或 `MODEL_MAP` 的 source/target suffix；其中只有客户端模型名后缀入口受 `ENABLE_REASONING_EFFORT_SUFFIX` 控制。这个开关为 `false` 时，代理不做 Anthropic 风格转换，而是把客户端侧 `reasoning` / `reasoning_effort` 原样透传给上游；如果上游不兼容，应由上游明确返回错误，代理不会静默丢弃推理参数。内部档位是 `none/minimal/low/medium/high/xhigh/max`：`none` 关闭 thinking；旧式 manual thinking 会按 `ANTHROPIC_MAX_THINKING_BUDGET` 动态分配预算，并被 `max_tokens - 1` 夹紧；Claude adaptive thinking 原生支持 `max`，所以 `max` 会保留为 `output_config.effort=max`，而发往 OpenAI 风格上游时会降级成 `xhigh`。
- `ANTHROPIC_MAX_THINKING_BUDGET`：控制 manual Anthropic `thinking.budget_tokens` 的最高预算，根级默认 `32000`，provider 留空继承、显式设置覆盖。Anthropic 官方只约束 `budget_tokens >= 1024` 且普通 manual thinking 下必须小于 `max_tokens`，没有公布全局独立最大值；`32000` 是通用工程默认值，不是官方 hard cap。举例：默认 32000 时，`minimal/low/medium/high/xhigh/max` 的 manual 预算分别约为 `2000/4000/8000/16000/32000/32000`，如果请求 `max_tokens=12000`，最终预算会被夹到 `11999`。

当前实现里，**请求准入会遵循代理实际对外返回的 `/models` 列表**：

- 默认分组 bare `/v1/*` 只允许请求当前 bare `/v1/models` 里可见的模型
- 显式 `/{providerId}/v1/*` 也只允许请求该 provider 自己 `/models` 列表里可见的模型
- 不在对应 `/models` 列表里的模型，请求会直接返回 `400 invalid_model`
- `MODEL_MAP` 不是 `/models` 展示来源。举例：`MODEL_MAP=client-gpt:gpt-5.5` 只让请求 `client-gpt` 映射到上游 `gpt-5.5`；如果没有 `MANUAL_MODELS=client-gpt` 或上游 `/models` 自己返回 `client-gpt`，默认分组和显式 provider 的 `/models` 都不会显示这个 alias。
- suffix 变体是一个例外：只要 `ENABLE_REASONING_EFFORT_SUFFIX=true`、base model 已允许请求，且该 suffix 变体没有被 `HIDDEN_MODELS` 显式隐藏，客户端就可以直接请求 `model-high` / `model-none` 这类模型；`EXPOSE_REASONING_SUFFIX_MODELS=false` 只表示这些 suffix 变体不出现在 `/models` 里
- `MODEL_MAP` 显式 suffix source 优先于 base source。举例：`MODEL_MAP=client-gpt-high:upstream-priority,client-gpt:upstream-base` 且请求 `model=client-gpt` + `reasoning.effort=high` 时，会先合成 `client-gpt-high` 命中第一条，发往 `upstream-priority`。
- `MODEL_MAP` 的 source suffix 等效只作用于映射阶段，不改变模型列表。举例：`MODEL_MAP=client-gpt-high:upstream-gpt` 不会让 `/models` 出现 `client-gpt-high`；如果要展示它，仍要写 `MANUAL_MODELS=client-gpt-high`。
- `#reason_suffix:model` 是手动 family 例外：即使 `ENABLE_REASONING_EFFORT_SUFFIX=false` 或 `EXPOSE_REASONING_SUFFIX_MODELS=false`，它仍会把该 base model 的推理后缀家族放进 `/models` 并允许请求；但它生成的是一个批量 family，不等同于每个档位都被静态手动添加，所以 `HIDDEN_MODELS` 仍可以隐藏其中某个具体档位，例如用 `HIDDEN_MODELS=gpt-5.5-minimal` 只取消 `minimal` 这个变体，也可以用 `HIDDEN_MODELS=#reason_suffix:gpt-5.5` 隐藏整组家族；如果 `MANUAL_MODELS` 和 `HIDDEN_MODELS` 同时写了同一个 `#reason_suffix:gpt-5.5`，手动添加优先，最终仍会显示；极端情况下，如果 `ENABLE_REASONING_EFFORT_SUFFIX=false` 且 `MANUAL_MODELS` 同时写了 `#reason_suffix:gpt-5.5` 和字面 `gpt-5.5-low`，字面模型优先，`gpt-5.5-low` 会按 provider 原生模型处理；如果 `ENABLE_REASONING_EFFORT_SUFFIX=true`，同名 suffix 仍按可解析推理后缀处理
- `HIDDEN_MODELS` 的 `#re:` 是按完整模型名做普通 Go regexp 匹配，不理解“模型名”和“后缀强度”的语义边界；因此 `#re:.*mini.*` 会同时隐藏 `gpt-5.4-mini` 和 `gpt-5.5-minimal`。如果只想隐藏 `mini` 模型而保留 `-minimal` 推理强度，建议写成更精确的规则，例如 `#re:.*(^|-|:)mini($|-|\.).*`，或者直接列出要隐藏的字面模型。
- `MANUAL_MODELS` 与 `HIDDEN_MODELS` 同时命中时按“粒度优先，其次手动优先”处理：手动大范围 family 遇到隐藏小范围档位时，隐藏小范围优先；手动和隐藏是同一范围时，手动优先；隐藏是大范围 family 而手动是小范围字面模型时，手动小范围优先。例如 `MANUAL_MODELS=#reason_suffix:gpt-5.5` + `HIDDEN_MODELS=gpt-5.5-minimal` 会隐藏 `minimal`；两边都写 `#reason_suffix:gpt-5.5` 时 family 仍显示；`MANUAL_MODELS=gpt-5.5-minimal` + `HIDDEN_MODELS=#reason_suffix:gpt-5.5` 时 `minimal` 仍显示。
- `#reason_suffix` 与 `#re:` 可以共存，但顺序必须是 `#reason_suffix:#re:<pattern>`；先写 `#re:#reason_suffix:...` 不会被当作合法组合，因为正则筛选应先作用在上游 `/models` 返回的原始 base model 集合上，然后再对匹配到的每个 base model 展开推理后缀家族。这里的 `#re:` 不会匹配代理层自己生成的模型名、`MODEL_MAP` alias 或其它手动静态模型。`#reason_suffix:-minimal` 这种写法表示只处理所有可见 base model 的 `minimal` 档位，可用于手动补出或隐藏一个全局档位。
- 对全局推理后缀开关来说，`HIDDEN_MODELS` 始终是更细粒度的限制：即使 `ENABLE_REASONING_EFFORT_SUFFIX=true` 允许显式请求 suffix，或 `EXPOSE_REASONING_SUFFIX_MODELS=true` 把 suffix 加进 `/models`，`HIDDEN_MODELS=#reason_suffix:-minimal`、`HIDDEN_MODELS=gpt-5.5-minimal`、`HIDDEN_MODELS=#reason_suffix:gpt-5.5` 仍然可以分别隐藏某个档位、某个模型档位或整组 family。
- `-noprompt` 是另一个代理层后缀：默认开启时，`gpt-5.5-noprompt` 会按 `gpt-5.5` 路由，`gpt-5.5-low-noprompt` 会按 `gpt-5.5-low` 路由并保留 `low` 推理强度，同时跳过 provider prompt 注入；provider 级 `ENABLE_NOPROMPT_MODEL_SUFFIX=false` 会让该 provider 把 `-noprompt` 当普通模型名处理；`HIDDEN_MODELS=gpt-5.5-noprompt` 可以隐藏并禁用该 noprompt 变体；响应头 `X-Client-To-Proxy-NoPrompt: true` 表示该标记已生效，`false` 表示客户端带了 `-noprompt` 但有效配置关闭了该能力，`X-Proxy-To-Upstream-Model` 仍显示最终发给上游的模型名

当前已验证的推理强度入口包括：

- `/v1/responses` 请求体 `reasoning.effort`
- `/v1/chat/completions` 请求体 `reasoning_effort` 或 `reasoning.effort`
- 模型名 suffix：`-none / -minimal / -low / -medium / -high / -xhigh / -max`
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
- 为避免图片 base64、向量数据和 rerank 语料占用不必要空间，`/v1/images/*`、`/v1/embeddings`、`/v1/rerank` 及其无 `/v1` / 显式 provider 路由别名默认**不写结构化日志，也不写调试归档**

### 7. OpenAI Images / Embeddings / Rerank 虚拟透传端口

这三类端口与 `SUPPORTS_CHAT`、`SUPPORTS_RESPONSES`、`SUPPORTS_MODELS`、`SUPPORTS_ANTHROPIC_MESSAGES` 这组 capability 开关**无关**：

- 代理层默认会对所有 provider 导出这些端口
- 即使上游站点本身不支持，代理层也仍然保留端口；客户端强行请求时，直接把请求透传给上游，由上游自己返回 `404` / `400` / `501` 等错误，代理原样透传
- 代理不会为这三类端口做协议兼容折中，也不会把它们翻译成 Responses / Chat / Messages 语义

当前默认导出的 OpenAI 风格透传端口包括：

- Images：
  - `POST /v1/images/generations`
  - `POST /v1/images/edits`
  - `POST /v1/images/variations`
- Embeddings：
  - `POST /v1/embeddings`
- Rerank：
  - `POST /v1/rerank`

这些端口都会继续复用代理现有的：

- provider 选择逻辑（bare 默认分组 / 显式 `/{providerId}` 路由）
- `MODEL_MAP` / `MANUAL_MODELS` / `HIDDEN_MODELS` 约束
- 模型可见性与准入检查
- 上游鉴权透传与 provider 默认 key 回退
- timeout / retry / masquerade 行为

透传细节：

- `/v1/images/generations`、`/v1/embeddings`、`/v1/rerank`：按 JSON 原样透传，请求发往上游 `/images/generations`、`/embeddings`、`/rerank`
- `/v1/images/edits`、`/v1/images/variations`：按 multipart/form-data 原样透传，请求发往上游 `/images/edits`、`/images/variations`
- 代理仅在真正转发前改写请求里的 `model` 字段，使其符合当前 provider 的 `MODEL_MAP` 结果；其它字段保持原样

---

## 🧪 伪装客户端（实验性）

通过根 `.env` 的 `UPSTREAM_MASQUERADE_TARGET`，或 provider `.env` 的 `MASQUERADE_TARGET` 控制：

| 值 | 作用 |
|---|---|
| `opencode` | 注入 OpenCode 风格 `User-Agent` + `originator` |
| `claude` | 注入 Claude Code 风格 `User-Agent`、`X-App`、完整 `anthropic-beta`、`X-Stainless-*`，并可按现有开关注入官方 system prompt / metadata.user_id |
| `codex` | 注入 Codex CLI 风格 `User-Agent`、`originator`、residency header；当请求带 reasoning 时自动补 `include=reasoning.encrypted_content` |
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
| 路由与鉴权 | `PROXY_API_KEY`、`DEFAULT_PROVIDER`、`V1_MODEL_MAP`、`ENABLE_DEFAULT_PROVIDER_MODEL_TAGS`、`ENABLE_ALL_DEFAULT_PROVIDER_MODEL_TAGS`、`ENABLE_NOPROMPT_MODEL_SUFFIX`、`ENABLE_LEGACY_V1_ROUTES` | ✅ |
| 下游策略与超时 | `DOWNSTREAM_NON_STREAM_STRATEGY`、`CONNECT_TIMEOUT`、`FIRST_BYTE_TIMEOUT`、`IDLE_TIMEOUT`、`TOTAL_TIMEOUT` | ✅ |
| 输出、上下文与 Anthropic thinking 预算 | `UPSTREAM_MAX_OUTPUT_TOKENS`、`FORCE_UPSTREAM_MAX_OUTPUT_TOKENS`、`UPSTREAM_ANTHROPIC_CACHE_CONTROL`、`MODEL_LIMIT_CONTEXT_TOKENS`、`ANTHROPIC_MAX_THINKING_BUDGET` | ✅ |
| 上游伪装相关 | `UPSTREAM_USER_AGENT`、`UPSTREAM_MASQUERADE_TARGET`、`UPSTREAM_INJECT_METADATA_USER_ID`、`UPSTREAM_INJECT_CLAUDE_SYSTEM_PROMPT` | ✅ |
| provider 目录 | `PROVIDERS_DIR` | ⚠️ 部分；provider 监听会切换，但 Cache_Info 落盘目录需重启 |
| 启动期字段 | `LISTEN_ADDR`、`CACHE_INFO_TIMEZONE`、`LOG_*`、`OPENAI_COMPAT_DEBUG_ARCHIVE_DIR`、`OPENAI_COMPAT_DEBUG_ARCHIVE_MAX_REQUESTS` | ❌ 修改后需重启 |

### Cache_Info 聚合文件

`<PROVIDERS_DIR>/Cache_Info/` 现在默认会生成三类聚合 TXT：

- `全提供商总计.txt`
- `已启用提供商总计.txt`
- `v1默认分组统计.txt`

其中 `v1默认分组统计.txt` 只汇总当前 `DEFAULT_PROVIDER` 列表里、并且仍处于启用状态的 provider。无论默认分组当前走的是 overlay 模式还是标签模式，这个聚合文件都会按默认分组实际参与的 provider 集合统计。

### Token_Estimator 观测目录

phase-1 动态 token estimator 会在 `<PROVIDERS_DIR>/Token_Estimator/` 下即时落盘两类文件：

- `SYSTEM_JSON_FILES/<provider>/<endpoint>/<safe-model>.json`
- `<provider>/<endpoint>/<safe-model>.txt`

这里的分桶键固定是：

- `provider_id`
- `upstream_endpoint_type`
- `最终真正发给上游的模型名`

当前阶段只做学习、观测和建议修正，不直接反向修改 `MODEL_LIMIT_CONTEXT_TOKENS` 的准入判断。TXT 摘要里会展示样本数、置信度、`runtime_ready`、平均 input/cached/uncached token，以及建议修正系数。用户如果在管理台删除某个 provider 目录、某个 endpoint 目录、或某个模型对应文件，代理会把它当成冷启动重新学习。

### provider `.env`

| 字段组 | 例子 | 说明 |
|---|---|---|
| 上游连接 | `UPSTREAM_BASE_URL`、`UPSTREAM_API_KEY`、`UPSTREAM_ENDPOINT_TYPE` | 当前 provider 如何连上游 |
| Anthropic / thinking | `ANTHROPIC_VERSION`、`ANTHROPIC_MAX_THINKING_BUDGET`、`UPSTREAM_THINKING_TAG_STYLE`、`UPSTREAM_XML_TOOL_CALL_STYLE`、`UPSTREAM_ANTHROPIC_CACHE_CONTROL` | Anthropic 上游版本、manual thinking 预算上限、chat 上游 thinking 标签策略、XML 工具调用兼容与 cache_control 改写 |
| 能力开关 | `SUPPORTS_CHAT`、`SUPPORTS_RESPONSES`、`SUPPORTS_MODELS`、`SUPPORTS_ANTHROPIC_MESSAGES` | 控制公开端口是否开放 |
| 非流 / timeout / retry | `DOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE`、`UPSTREAM_FIRST_BYTE_TIMEOUT`、`UPSTREAM_RETRY_COUNT`、`UPSTREAM_RETRY_DELAY` | provider 级运行时策略 |
| 提示词与模型 | `SYSTEM_PROMPT_FILES`、`SYSTEM_PROMPT_POSITION`、`MODEL_MAP`、`MANUAL_MODELS`、`HIDDEN_MODELS`、`ENABLE_NOPROMPT_MODEL_SUFFIX` | 注入与模型能力；provider 级 noprompt 留空继承根配置 |
| 推理强度 | `ENABLE_REASONING_EFFORT_SUFFIX`、`EXPOSE_REASONING_SUFFIX_MODELS`、`MAP_REASONING_SUFFIX_TO_ANTHROPIC_THINKING` | suffix / thinking 相关，包含 `max` 档互转 |
| OpenAI 服务层级 | `OPENAI_SERVICE_TIER` | 仅 OpenAI `responses/chat` 上游生效；留空时沿用下游传参，非空时强制覆写 |
| 输出与上下文限制 | `UPSTREAM_MAX_OUTPUT_TOKENS`、`FORCE_UPSTREAM_MAX_OUTPUT_TOKENS`、`UPSTREAM_ANTHROPIC_CACHE_CONTROL`、`MODEL_LIMIT_CONTEXT_TOKENS` | 留空继承根配置；显式设置后覆盖根配置 |
| 鉴权与伪装 | `PROXY_API_KEY_OVERRIDE`、`UPSTREAM_USER_AGENT`、`MASQUERADE_TARGET`、`INJECT_CLAUDE_CODE_*` | provider 级覆写 |

补充：`PROXY_API_KEY_OVERRIDE=empty` 表示这个 provider 的显式分组路由不做代理鉴权；provider 级 Claude 注入开关、输出上限字段和上下文限制字段留空都表示继承根配置。

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
当 `/v1/messages` 直接对接 `anthropic` 上游时，代理会把消息内容块和 system block 里的 `cache_control` 一并继续传给上游；纯字符串 system 仍保持字符串形态。跨协议转换时，`cache_control` 仍然只是兼容输入，不承诺翻译成真正的 Anthropic prompt caching 语义。若要统一改写 Anthropic 内容块的 `cache_control`，可以用 `UPSTREAM_ANTHROPIC_CACHE_CONTROL`：`5min` 会统一写成 `{"type":"ephemeral"}`，`1h` 会统一写成 `{"type":"ephemeral","ttl":"1h"}`，`false` 会删除该字段，`nochange` 会保留客户端原样传来的值；根 `.env` 和 provider `.env` 都支持，provider 留空时继承根配置。

6. **Responses 上游会补齐缺失的 `prompt_cache_key`**  
   当目标上游是 `responses`，且客户端没有显式传入 `prompt_cache_key` 时，代理会基于模型、系统提示、工具定义和输入前缀生成不含原文的稳定缓存键，用来提升同前缀请求的缓存路由命中概率；如果客户端已传该字段，代理会原样保留。该字段不会透传到 `chat` 或 `anthropic` 上游，也不承诺上游一定命中缓存。

7. **chat 上游的思维标签当前支持三种写法**
   当 `UPSTREAM_THINKING_TAG_STYLE=true` 时，代理会把 `<think>`、`<thinking>`、`<reasoning>` 识别为 reasoning 内容，再按目标下游协议重写。

8. **chat 上游的 XML 工具调用兼容默认开启**
   当 `UPSTREAM_XML_TOOL_CALL_STYLE=true` 时，代理只会把完整的 `<tool_call><function=...><parameter=...>...</parameter></function></tool_call>` 正文恢复成结构化工具调用；如果上游不会产生这类 XML 工具调用文本，可以显式设置为 `false`。

9. **`/v1/responses/compact` 是 Responses 专用非流端点，不支持 `stream=true`**
   仅在 provider 的 `UPSTREAM_ENDPOINT_TYPE=responses` 时可用；请求 `stream=true` 或上游不是 `responses` 类型时直接返回 `400`。成功时直接返回上游原始 JSON，不再走聚合归一逻辑。

10. **普通 `/v1/responses` SSE 流现可携带 compaction items 和 opaque `encrypted_content`**
   这些字段不会被代理层解析或丢弃，会完整透传给下游客户端。

---

## 🛠️ 部署与运维

| 脚本 | 作用 |
|---|---|
| `bash scripts/deploy-linux.sh` | 预检、编译、注册并启用 systemd 开机自启动、停旧、启新、健康检查、失败回滚 |
| `bash scripts/restart-linux.sh` | 重启服务 |
| `bash scripts/stop-linux.sh` | 停止服务并停用 systemd 开机自启动 |
| `bash scripts/uninstall-linux.sh` | 停止服务、注销 systemd 服务并卸载部署产物 |

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

**强烈建议在当前项目所部署网站的反向代理层，把“允许的域名”、“允许的方法”、“允许携带的请求头”这三个关键跨域选项都直接填写为通配符（"*"），其余同类跨域放行开关也尽量全部打开，以尽可能避免浏览器前端在调用当前项目接口时被跨域策略拦截；同时要确保这些配置实际加在承载当前项目 API 的入口上，而不是只加在静态页面站点上。**

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
