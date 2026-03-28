# openai-compat-proxy

一个 Go 单二进制代理：**每个 provider 只选一种上游正规协议**，代理层继续统一对外提供三类兼容接口。

支持的上游协议类型：

- `responses` → OpenAI `/responses`
- `chat` → OpenAI `/chat/completions`
- `anthropic` → Anthropic `/messages`

对外统一提供：

- `POST /v1/responses`
- `POST /v1/chat/completions`
- `POST /v1/messages`
- `GET /v1/models`

---

## 你现在能得到什么

### 1. 多 provider 路由

- 支持 `providers/*.env` 管理多个 provider
- 支持显式路由：`/{providerId}/v1/*`
- 支持默认 provider，兼容裸 `/v1/*`

### 2. provider 级统一上游协议

每个 provider 通过一个字段决定自己内部统一走哪条上游链：

```env
UPSTREAM_ENDPOINT_TYPE=responses
```

可选值：

- `responses`
- `chat`
- `anthropic`

这个字段**只影响代理内部如何请求上游**，不影响对外公开的三个兼容端口。

### 3. 三出口兼容分发

无论 provider 选的是哪种上游协议，代理层都尽量统一分发成：

- Responses 输出
- Chat Completions 输出
- Anthropic Messages 输出

### 4. 代理增强能力

- 流式与非流式双模式
- tool / function calling 映射
- `/v1/responses` 下游的 `function_call_output` 在上游为 `responses/chat/anthropic` 时都能继续参与多轮工具调用回传
- thinking / reasoning 映射
- refusal 映射
- usage 透传
- provider prompt 注入
- model map + reasoning suffix
- 状态查询与流式失败终态补发

### 5. 多模态支持（当前已接通的主路径）

- 文本
- image URL / input image
- file
- OpenAI 侧 input audio

说明：Anthropic 上游目前对 `input_audio` 走**显式拒绝**，不会再静默吞掉。

---

## 1Panel / Nginx 反代建议

如果你前面还有 1Panel / OpenResty 反代，建议先加：

```nginx
proxy_connect_timeout 1200s;
proxy_send_timeout 1200s;
proxy_read_timeout 1200s;
send_timeout 1200s;
```

否则长思考、长流式场景容易被网关提前掐掉。

---

## 快速启动

### 1. 拉代码

```bash
git clone https://github.com/Sharl210/openai-compat-proxy-private.git
cd openai-compat-proxy-private
```

### 2. 准备根配置

```bash
cp .env.example .env
```

最小示例：

```env
LISTEN_ADDR=:21021
CACHE_INFO_TIMEZONE=Asia/Shanghai
PROXY_API_KEY=

PROVIDERS_DIR=./providers
DEFAULT_PROVIDER=openai
ENABLE_LEGACY_V1_ROUTES=true
DOWNSTREAM_NON_STREAM_STRATEGY=proxy_buffer

CONNECT_TIMEOUT=10s
FIRST_BYTE_TIMEOUT=20m
IDLE_TIMEOUT=3m
TOTAL_TIMEOUT=1h

LOG_ENABLE=false
LOG_FILE_PATH=.proxy.requests.jsonl
LOG_INCLUDE_BODIES=false
LOG_MAX_SIZE_MB=100
LOG_MAX_BACKUPS=10
```

### 3. 准备 provider

```bash
cp providers/openai.env.example providers/openai.env
```

如果你要多个 provider，就复制多份：

```bash
cp providers/openai.env.example providers/openai.env
cp providers/openai.env.example providers/anthropic.env
```

程序只读取 `providers/*.env`，忽略 `providers/*.env.example`。

### 4. 配置 provider

最关键的是这几个字段：

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

### 5. 启动

```bash
chmod +x scripts/*.sh
./scripts/deploy-linux.sh
```

健康检查：

```bash
curl http://127.0.0.1:21021/healthz
```

---

## 路由规则

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

### Anthropic Messages 头要求

请求 `/v1/messages` 或 `/{providerId}/v1/messages` 时，必须带：

```text
anthropic-version: 2023-06-01
```

缺少这个头会直接返回 `400 invalid_request`。

---

## 鉴权约定

代理层支持：

- `Authorization: Bearer <proxy-key>`
- `X-API-Key: <proxy-key>`
- `Api-Key: <proxy-key>`

上游鉴权透传支持：

- `X-Upstream-Authorization: Bearer <real-upstream-key>`

如果请求里没有传 `X-Upstream-Authorization`，则回退到当前 provider 的 `UPSTREAM_API_KEY`。

---

## 配置说明

### 根 `.env`

最常用字段：

| 字段 | 说明 | 热加载 |
|---|---|---|
| `LISTEN_ADDR` | 监听地址 | 否，修改后需重启 |
| `CACHE_INFO_TIMEZONE` | 统计展示时区 | 否，修改后需重启 |
| `PROXY_API_KEY` | 根级代理鉴权 key | 是 |
| `PROVIDERS_DIR` | provider 配置目录 | 是 |
| `DEFAULT_PROVIDER` | 裸 `/v1/*` 默认 provider | 是 |
| `ENABLE_LEGACY_V1_ROUTES` | 是否开启裸 `/v1/*` | 是 |
| `DOWNSTREAM_NON_STREAM_STRATEGY` | 非流时走本地聚合还是直接请求上游非流 | 是 |
| `CONNECT_TIMEOUT` / `FIRST_BYTE_TIMEOUT` / `IDLE_TIMEOUT` / `TOTAL_TIMEOUT` | 上游超时控制 | 是 |
| `LOG_*` | 结构化日志配置 | 否，修改后需重启 |

### provider `.env`

最常用字段：

| 字段 | 说明 |
|---|---|
| `PROVIDER_ID` | provider 唯一 id |
| `PROVIDER_ENABLED` | 是否启用 |
| `UPSTREAM_BASE_URL` | 上游站点根地址 |
| `UPSTREAM_API_KEY` | 默认上游 key |
| `UPSTREAM_ENDPOINT_TYPE` | 当前 provider 内部统一使用的上游协议：`responses/chat/anthropic` |
| `SUPPORTS_CHAT` | 是否开放 chat/completions 公开端口 |
| `SUPPORTS_RESPONSES` | 是否开放 responses 公开端口 |
| `SUPPORTS_MODELS` | 是否开放 models |
| `SUPPORTS_ANTHROPIC_MESSAGES` | 是否开放 messages 公开端口 |
| `UPSTREAM_RETRY_COUNT` / `UPSTREAM_RETRY_DELAY` | 上游刚请求就失败时的安全门重试 |
| `UPSTREAM_FIRST_BYTE_TIMEOUT` | provider 级首字节超时 |
| `DOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE` | provider 级非流模式覆写 |
| `SYSTEM_PROMPT_FILES` / `SYSTEM_PROMPT_POSITION` | provider 级系统提示词注入 |
| `MODEL_MAP_JSON` | 模型映射 |
| `ENABLE_REASONING_EFFORT_SUFFIX` | 是否启用 `-low/-medium/-high/-xhigh` suffix 解析 |
| `EXPOSE_REASONING_SUFFIX_MODELS` | `/models` 是否暴露 suffix 模型名 |

说明：provider 文件里的字段当前都支持热加载。

---

## 非流模式说明

`DOWNSTREAM_NON_STREAM_STRATEGY` / `DOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE` 支持两个值：

- `proxy_buffer`：下游是非流式时，代理继续向上游请求流，再本地聚合成非流输出
- `upstream_non_stream`：下游是非流式时，代理直接向上游请求非流 JSON

默认值是 `proxy_buffer`。

---

## 热加载与重启

### 当前可热加载

- 根 `.env` 中的：
  - `PROXY_API_KEY`
  - `PROVIDERS_DIR`
  - `DEFAULT_PROVIDER`
  - `ENABLE_LEGACY_V1_ROUTES`
  - `DOWNSTREAM_NON_STREAM_STRATEGY`
  - `CONNECT_TIMEOUT`
  - `FIRST_BYTE_TIMEOUT`
  - `IDLE_TIMEOUT`
  - `TOTAL_TIMEOUT`
- provider `.env` 中的大部分运行时字段，包括：
  - `UPSTREAM_BASE_URL`
  - `UPSTREAM_API_KEY`
  - `UPSTREAM_ENDPOINT_TYPE`
  - 能力开关
  - 重试 / timeout / model map / suffix / system prompt 相关字段
- `SYSTEM_PROMPT_FILES` 引用到的文本文件内容

### 当前不能热加载（修改后需要重启）

- `LISTEN_ADDR`
- `CACHE_INFO_TIMEZONE`
- 所有 `LOG_*`

---

## 模型映射与 suffix

`MODEL_MAP_JSON` 支持：

- 精确匹配
- `*-suffix` 通配 key
- `*` 兜底

`ENABLE_REASONING_EFFORT_SUFFIX=true` 时，支持把模型名后缀：

- `-low`
- `-medium`
- `-high`
- `-xhigh`

解析成 reasoning effort。

---

## Claude / Messages 兼容说明

- `cache_control` 当前是**兼容输入**，不是对 Anthropic prompt caching 的真实上游支持
- Anthropic 上游当前支持 text / image / document / tool_use / tool_result 等主路径
- 当下游入口使用 `/v1/responses` 时，`function_call_output` 会继续被归一成内部 tool result 语义；即使 provider 内部上游选的是 `chat` 或 `anthropic`，也能继续把工具结果回传给上游完成多轮工具调用
- Anthropic 上游对 `input_audio` 当前走**显式拒绝**，不会再静默吞掉

---

## 常用脚本

| 脚本 | 作用 |
|---|---|
| `scripts/deploy-linux.sh` | 首次部署 / 重新部署 |
| `scripts/stop-linux.sh` | 停服务，不删产物 |
| `scripts/restart-linux.sh` | 重启服务 |
| `scripts/uninstall-linux.sh` | 停服务并清理部署产物 |

所有脚本都带基础预检、端口检查、健康检查和失败回滚保护。

---

## 一个务实建议

如果你的客户端本身对 Responses / Anthropic 的细节实现不稳定，优先先用 `chat/completions` 兼容接口测通；复杂推理强度可以优先通过模型后缀来控，不一定要依赖客户端自己的“思维链开关”。
