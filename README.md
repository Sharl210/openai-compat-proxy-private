# openai-compat-proxy

如果你是在 1Panel 里做反向代理，先把这一段加到站点反代配置的 `location` 里：

```nginx
proxy_connect_timeout 1200s;
proxy_send_timeout 1200s;
proxy_read_timeout 1200s;
send_timeout 1200s;
```

改完重载 OpenResty，以避免网关超时。

一个 Go 单二进制的 OpenAI 兼容代理。代理层会为每个 provider 统一选择一种正规上游协议，然后再对外导出兼容端点，给不同客户端和协议风格使用。项目当前支持多提供商配置，主要导出三类接口：**Anthropic**、**OpenAI Responses**、**OpenAI Chat Completions（兼容接口）**，并附带模型列表接口。

#### 😇孩子们建议不要开日志记录，没啥用其实，之前调试一个问题加的没任何优化，开销很大而且心理上也会有强迫症🤣
#### 还有没事不建议用cherry，（个人建议）bug有点多我自测，就是rikkahub没问题，最朴素的curl也没问题ai也看不出所以然但是cherry就是有问题，特别是极长思考下容易思考分块，且不显示正文，可能实现不一致。。。。而且这应用现在越更新越卡，给我卡飞力/没有贬低的意思，但是我体感就是卡飞了😭实在要用尽可能用chat兼容接口，思维链选项设置为关闭，用本代理层的模型推理强度后缀功能控制推理🥵（response和Anthropic他这边会把文件例如pdf直接透传，本地不做解析，但有的上游/中转站的不支持，所以说尽可能不要用这两个端口在这个APP里）

## 项目实现的几个大功能

### 1. 多提供商路由

- 支持通过 `providers/*.env` 配置多个 provider
- 支持按 provider id 访问：`/{providerId}/v1/...`
- 支持设置默认 provider，兼容裸 `/v1/*` 路由
- 一个实例可以同时代理多个不同上游

### 2. 统一包装 provider 选定的上游协议

- provider 可通过 `UPSTREAM_ENDPOINT_TYPE` 统一选择自己的上游正规协议：`responses` / `chat` / `anthropic`
- **项目对外主线导出三个端点族**：
  - **Anthropic**：`POST /v1/messages`
  - **OpenAI Responses**：`POST /v1/responses`
  - **OpenAI Chat Completions（兼容接口）**：`POST /v1/chat/completions`
- 对应的实际路径分别是：
  - `POST /v1/chat/completions`
  - `POST /v1/responses`
  - `POST /v1/messages`
  - `GET /v1/models`
- 支持 tool / function calling、多轮消息、部分多模态输入

### 3. 真流式转发与推理展示增强

- `chat/completions` 与 `messages` 都支持边收上游 SSE 边向下游 flush
- 在正文开始前支持发送轻量占位状态，减少“长时间无首字”的空白体验
- 支持把上游 reasoning 摘要映射为下游可展示字段
- 支持在 usage 中透传 `reasoning_tokens`、`cached_tokens`

### 4. 模型映射与 reasoning 后缀

- 支持 provider 级 `MODEL_MAP_JSON`
- `ENABLE_REASONING_EFFORT_SUFFIX=true` 开启后，key 和 value 都支持 `-low/-medium/-high/-xhigh` 后缀
- `*-suffix` 通配 key：匹配所有以该后缀结尾的请求模型
- effort 以 **value 的后缀**为准（value 有后缀则用，无则空）
- 开关关闭时：后缀只做字符串替换，不解析 effort
- 匹配顺序：`*-suffix` 通配 key（优先） → 精确 key → strip 后缀后精确 key → `*` 通配 key

### 5. 错误透传与运行日志

- 非流式请求会尽量保留上游 JSON 错误体和状态码；当代理需要补充重试说明时，错误消息会附带代理侧重试摘要
- 支持代理访问鉴权和调用方自带上游鉴权
- 支持结构化日志、日志轮转、可选 body 记录
- 提供 `healthz`、部署脚本、重启脚本、卸载脚本

### 6. Claude 提示缓存兼容

- 兼容 Anthropic / Claude 兼容请求中的 `cache_control`
- 允许 Claude 客户端勾选“提示缓存（cache_control）”后继续正常请求
- 代理层会接收并过滤该字段，避免当前 OpenAI Responses 上游因不支持 `cache_control` 而返回 400
- 这不是对 Anthropic prompt caching 的真实上游支持，而是兼容输入、避免失败
- 少了这个功能也不代表项目有缺陷。对于本项目这种“上游是 OpenAI Responses 格式”的设计来说，Claude 侧的 `cache_control` 本身就不是核心必需能力，因为上游本身已有自己的缓存机制

## 安装与启动

### 1. 拉取项目

```bash
git clone https://github.com/Sharl210/openai-compat-proxy-private.git
cd openai-compat-proxy-private
```

### 2. 准备全局配置 `.env`

先复制根级模板：

```bash
cp .env.example .env
```

最小示例：

```bash
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

### 3. 准备 provider 配置

从通用 provider 模板复制一份真实配置，文件名必须是 `.env`，不能只留 `.env.example`：

```bash
cp providers/openai.env.example providers/openai.env
```

如果你要配置多个 provider，就重复复制这一份模板，按需要改文件名、`PROVIDER_ID` 和能力开关：

```bash
cp providers/openai.env.example providers/openai.env
cp providers/openai.env.example providers/anthropic.env
```

程序只会读取 `providers/*.env`，会忽略 `providers/*.env.example`。

模板里默认还会写入：

- `SYSTEM_PROMPT_FILES=prompt.md`
- `SYSTEM_PROMPT_POSITION=prepend`

同时仓库会提供一个空的 `providers/prompt.md` 示例文件。空文件不会注入任何内容，所以默认效果等同于关闭。你可以直接编辑这个文件，或者把 `SYSTEM_PROMPT_FILES` 留空来彻底关闭 provider 级系统提示词注入。

### 4. 执行脚本

#### `scripts/deploy-linux.sh`

作用：首次部署或重新部署。

它会做这些事：

- 检查根目录 `.env` 是否存在
- 检查 `PROVIDERS_DIR` 是否存在，且至少包含一个 `*.env`
- 自动补齐部署依赖，例如 `curl`、`python3`、`tar`、`ss`，以及缺失时的 Go（**仅在当前机器支持 `apt-get` 时**）
- 先编译候选二进制，再停止旧进程，避免前置检查没过就误伤现网进程
- 停掉旧代理进程后，确认目标监听端口上已经没有旧代理实例，再启动新进程
- 只有在**新 PID 已经监听目标端口且 `healthz` 成功**后，才把部署视为成功
- 如果 `LISTEN_ADDR` 显式写了主机，例如 `127.0.0.2:21021`，脚本会按这个主机做健康检查，不再固定探测 `127.0.0.1`
- 如果目标端口被第三方进程占用，脚本会直接报端口占用，不再误报成通用健康检查失败
- 写入 `.proxy.pid`
- 追加运行日志到 `.proxy.log`
- 如果新版本启动失败，会优先恢复旧二进制；如果旧服务原本在运行，还会尝试回滚启动旧版本

使用方式：

```bash
chmod +x scripts/*.sh
./scripts/deploy-linux.sh
```

#### `scripts/stop-linux.sh`

作用：只停止当前服务，不删除部署产物。

它会：

- 使用共享锁避免与 deploy / restart / uninstall 并发打架
- 优先根据 `.proxy.pid` 定位当前代理进程，并校验 PID 确实属于 `bin/openai-compat-proxy`
- 如果 pid 文件失真，还会继续检查目标监听端口上的代理进程
- 先发送 `SIGTERM`，等待退出；超时后升级到 `SIGKILL`
- 确认目标监听端口上已经没有旧代理实例后再删除 `.proxy.pid`
- **不会**删除 `.proxy.log` 和 `bin/openai-compat-proxy`

使用方式：

```bash
./scripts/stop-linux.sh
```

#### `scripts/restart-linux.sh`

作用：重启服务。

它会：

1. 先执行与 `stop-linux.sh` 相同的可靠停机流程
2. 确认旧进程已经退出、目标端口已经释放
3. 再执行和 `deploy-linux.sh` 相同的预检、构建、启动与健康检查流程

整个过程共用同一把锁，不会和其他 deploy / stop / uninstall 并发执行。旧进程没有死透时，新进程不会启动。

使用方式：

```bash
./scripts/restart-linux.sh
```

#### `scripts/uninstall-linux.sh`

作用：停止并清理当前部署产物。

它会：

- 先执行与 `stop-linux.sh` 相同的可靠停机流程，确认服务已经彻底停止
- 删除 `.proxy.pid`
- 删除 `.proxy.log`
- 删除 `bin/openai-compat-proxy`

只有在确认进程已经退出后，才会执行清理。

使用方式：

```bash
./scripts/uninstall-linux.sh
```

## 热加载说明

当前运行时会监听根级 `.env`、`providers/*.env`，以及 provider 配置里引用到的系统提示词文件。

### 当前可热加载

- `PROXY_API_KEY`
- `PROVIDERS_DIR`
- `DEFAULT_PROVIDER`
- `ENABLE_LEGACY_V1_ROUTES`
- `DOWNSTREAM_NON_STREAM_STRATEGY`
- `CONNECT_TIMEOUT`
- `FIRST_BYTE_TIMEOUT`
- `IDLE_TIMEOUT`
- `TOTAL_TIMEOUT`
- provider 文件中的：
  - `PROVIDER_ID`
  - `PROVIDER_ENABLED`
  - `UPSTREAM_BASE_URL`
  - `UPSTREAM_API_KEY`
  - `UPSTREAM_ENDPOINT_TYPE`
  - `SUPPORTS_CHAT`
  - `SUPPORTS_RESPONSES`
  - `SUPPORTS_MODELS`
  - `SUPPORTS_ANTHROPIC_MESSAGES`
  - `MODEL_MAP_JSON`
  - `ENABLE_REASONING_EFFORT_SUFFIX`
  - `EXPOSE_REASONING_SUFFIX_MODELS`
  - `UPSTREAM_RETRY_COUNT`
  - `UPSTREAM_RETRY_DELAY`
  - `DOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE`
  - `SYSTEM_PROMPT_FILES`
  - `SYSTEM_PROMPT_POSITION`
- provider 配置中 `SYSTEM_PROMPT_FILES` 引用到的文本文件内容

### 当前不能热加载

下面这些全局配置仍然只在进程启动时生效。运行中修改后会被忽略，仍以启动时加载值为准：

- `LISTEN_ADDR`
- `CACHE_INFO_TIMEZONE`
- `LOG_ENABLE`
- `LOG_FILE_PATH`
- `LOG_INCLUDE_BODIES`
- `LOG_MAX_SIZE_MB`
- `LOG_MAX_BACKUPS`

### 版本头语义

- `X-Env-Version`：当前**已生效**根 `.env` 可热加载配置版本对应的文件修改时间
- `X-Provider-Version`：当前请求命中的 provider **已生效版本**对应的修改时间。这个版本时间会取 provider `.env` 和该 provider 引用到的 system prompt 文件中的最新修改时间，所以仅修改 prompt 文件内容并热加载后，这个头也会更新。
- `X-Provider-Name`：当前请求实际命中的 provider id
- `X-SYSTEM-PROMPT-ATTACH`：当当前 provider 实际启用了非空系统提示词注入时返回，值格式为 `<position>:<paths>`，例如 `prepend:prompt.md, prompts/extra.md`。这里只暴露注入方向和配置路径，不会回传原始提示词文本。
- `X-STATUS-CHECK-URL`：当前请求对应的状态查询地址，使用 provider 作用域路径，格式为 `/{providerId}/v1/requests/{requestId}`。这里不回显真实代理 key，也不再附带一次性 token。
- `X-RESPONSE-PROCESS-HEALTH-FLAG`：当前请求处理状态的短标记。非流式成功请求通常是 `health`；流式请求在响应开始时会返回 `streaming`，表示“仍在处理中”，不是最终成功态。流式最终是否成功应以终态事件和请求状态查询接口为准。

### 请求状态查询接口

- 查询路径使用 **provider 作用域**，不提供全局 `/v1/requests/{requestId}`：
  - `GET /{providerId}/v1/requests/{requestId}`
- 这样可以避免不同 provider 之间通过 request id 相互探测状态。
- 状态查询接口现在完全不做鉴权；不会读取 `Authorization`、`X-API-Key`、`Api-Key`，也不会读取 `?key=` 之类的查询参数。

这里的状态查询规则现在是：

- `X-STATUS-CHECK-URL` 只负责给出 provider 作用域路径，不再附带 token，也不暴露真实代理 key
- 状态查询只按 `providerId + requestId` 命中记录；provider 不匹配或请求不存在时返回 `404`
- 即使主请求仍然开启了代理鉴权，状态查询接口也始终允许匿名访问
- 默认 provider 的裸 `/v1/*` 路由和 `/{providerId}/v1/*` 路由现在共享同一套状态查询语义，不再出现“主请求能过、状态查询换另一把 key”的分叉

返回 JSON 至少包含这些字段：

- `request_id`
- `provider`
- `route`
- `status`
- `health_flag`
- `stage`
- `started_at`
- `updated_at`
- `completed`
- 失败时还会返回 `error_code` 和 `error_message`

这里的 `completed` 表示“这个请求是否已经走到终态”，不是“是否成功完成”。

- `status=completed` 时，`completed=true`
- `status=failed` 时，`completed` 现在也会是 `true`，表示请求已经结束，只是结果失败
- 只有仍在处理中、还没到终态时，`completed` 才会是 `false`

当前 `status` / `health_flag` 的基础语义：

- `health`：非流式处理正常
- `streaming`：流式请求已开始处理，但还没到最终态
- `completed`：请求已完成
- `failed`：请求已失败
- `upstream_timeout`：上游超时
- `upstream_error`：上游报错
- `upstream_stream_broken`：上游流中途中断
- `proxy_internal_error`：代理内部处理失败

### 流式失败显式终态

- `responses` 流式请求中途失败时，代理不再直接静默断开，而会补发 `response.incomplete` 事件。
- `chat/completions` 流式请求中途失败时，代理会补发带 `finish_reason="error"` 的终止 chunk，并继续发 `[DONE]`。
- `messages` 流式请求中途失败时，代理会补发 `event: error`，然后补一个 `message_stop`。
- 这些流式失败都会同步写入请求状态查询接口，客户端拿不到完整流时仍然可以去查最终状态。

注意：

- 如果你只改了不能热加载的根配置，例如 `LISTEN_ADDR` 或日志配置，运行时会忽略这些变更，`X-Env-Version` 不会更新。
- 如果新配置写坏了，运行时会继续使用上一份最后可用配置，版本头也保持旧值不变。

### 升级旧版本时的 Cache_Info 迁移说明

如果你是从旧版本升级，并且线上已经有 `.env`、`providers/*.env` 和历史 Cache_Info 文件，建议按下面顺序处理：

1. 先备份现有配置和统计目录，例如：

```bash
cp .env .env.bak
cp providers/openai.env providers/openai.env.bak
cp -R providers/Cache_Info providers/Cache_Info.bak 2>/dev/null || true
```

2. 对照新的 `.env.example` 和 `providers/*.env.example`，把线上 `.env`、`providers/*.env` 的注释与字段说明补齐到最新语义，尤其是 Cache_Info 的 JSON 统计快照现在统一写到 `<PROVIDERS_DIR>/Cache_Info/SYSTEM_JSON_FILES/`，而 TXT 统计仍保留在 `<PROVIDERS_DIR>/Cache_Info/`。
3. 如果你希望把历史统计文件也迁到新目录，可以把旧的 `Cache_Info/*.json` 搬到 `Cache_Info/SYSTEM_JSON_FILES/`；旧的 `Cache_Info/*.txt` 继续保留即可。如果不搬，程序读取 JSON 时仍会兼容旧的 `Cache_Info/*.json`。
4. `CACHE_INFO_TIMEZONE` 不能热加载；如果你在升级时改了它，完成迁移后必须重启服务。

## 路由说明

### base URL 规则

多 provider 模式下，provider id 必须紧跟在域名后面，然后才是协议路径。也就是说，请求路径规则是：

```text
http(s)://<host>/<providerId>/v1/xxx
```

而不是：

```text
http(s)://<host>/v1/<providerId>/xxx
```

例如：

- 正确：`http://127.0.0.1:21021/openai/v1/chat/completions`
- 正确：`http://127.0.0.1:21021/openai/v1/responses`
- 正确：`http://127.0.0.1:21021/openai/v1/models`
- 错误：`http://127.0.0.1:21021/v1/openai/chat/completions`

### provider 路由

推荐使用显式 provider 路由：

- `/{providerId}/v1/chat/completions`
- `/{providerId}/v1/responses`
- `/{providerId}/v1/models`
- `/{providerId}/v1/messages`

例如：

- `/openai/v1/chat/completions`
- `/openai/v1/responses`
- `/claude/v1/messages`

### Anthropic Messages 请求头要求

对 `/v1/messages` 和 `/{providerId}/v1/messages`，请求必须带：

```text
anthropic-version: 2023-06-01
```

如果缺少 `anthropic-version`，代理现在会直接返回 `400 invalid_request`，不会再继续向上游发请求。

### 默认 provider 路由

如果你要兼容历史客户端，使用裸 `/v1/*` 路由时应保证存在默认 provider。

默认 provider 只通过全局 `.env` 中的 `DEFAULT_PROVIDER` 指定。

注意：`DEFAULT_PROVIDER` 必须对应一个已存在且启用的 provider。
另外，只有 `ENABLE_LEGACY_V1_ROUTES=true` 时，裸 `/v1/*` 路由才会生效。

## 鉴权约定

- 代理鉴权：`Authorization: Bearer <proxy-key>`
- 也支持：`X-API-Key` / `Api-Key`
- 上游鉴权透传：`X-Upstream-Authorization: Bearer <upstream-key>`
- 如果请求里没有传 `X-Upstream-Authorization`，则回退到当前选中 provider 的 `UPSTREAM_API_KEY`

## 全局配置 `.env` 字段说明

### 基础字段

- `LISTEN_ADDR`：监听地址，例如 `:21021`。**不能热加载**
- `CACHE_INFO_TIMEZONE`：Cache_Info 统计展示使用的时区，默认 `Asia/Shanghai`。provider 的 JSON 统计快照会写到 `<PROVIDERS_DIR>/Cache_Info/SYSTEM_JSON_FILES/`，TXT 统计仍写到 `<PROVIDERS_DIR>/Cache_Info/`；读取旧数据时仍会兼容历史 `Cache_Info/*.json`。只支持 IANA 时区名称，例如 `Asia/Shanghai`、`UTC`。**不能热加载，修改后需要重启**
- `PROXY_API_KEY`：根级代理访问 key，可选；provider 没有设置 `PROXY_API_KEY_OVERRIDE` 时会继承它。默认 provider 的裸 `/v1/*` 路由也使用这把 key。provider 作用域请求状态查询 `/{providerId}/v1/requests/{requestId}` 不会使用这把 key。**可热加载**

### 多 provider 相关字段

- `PROVIDERS_DIR`：provider 配置目录，例如 `./providers`。**可热加载**
- `DEFAULT_PROVIDER`：默认 provider 的 id。**可热加载**
- `ENABLE_LEGACY_V1_ROUTES`：是否把裸 `/v1/*` 作为默认 provider 的兼容入口。**可热加载**

### 超时字段

- `CONNECT_TIMEOUT`：连接上游时的 TCP 建连超时。**可热加载**
- `FIRST_BYTE_TIMEOUT`：等待上游响应头 / 首字节的超时。根级默认值是 `20m`；provider 没有单独覆写时会继承它。**可热加载**
- `IDLE_TIMEOUT`：读取活跃上游响应体 / 流时允许的最长静默间隔。**可热加载**
- `TOTAL_TIMEOUT`：单次请求总超时。**可热加载**

### 下游非流模式字段

- `DOWNSTREAM_NON_STREAM_STRATEGY`：下游请求本身是非流式时，代理请求上游 `/responses` 的默认模式。支持：
  - `proxy_buffer`：默认值，保持现状。代理继续向上游请求 SSE，再在本地聚合成非流式响应。
  - `upstream_non_stream`：下游非流时，代理直接向上游请求非流式 JSON。

行为说明：

- 这个字段是 **根级** 配置，支持热加载。
- 默认值是 `proxy_buffer`，所以升级后默认行为不变。
- 只有下游请求本身是非流式时，这个字段才会参与裁决；下游流式请求仍然固定走上游 SSE。
- provider 可以再通过 `DOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE` 做覆盖。

### 日志字段

- `LOG_ENABLE`：是否启用结构化日志。**不能热加载**
- `LOG_FILE_PATH`：日志文件路径，默认 `.proxy.requests.jsonl`。**不能热加载**
- `LOG_INCLUDE_BODIES`：是否记录请求和响应 body，只有 `true` / `1` 才会开启。**不能热加载**
- `LOG_MAX_SIZE_MB`：单个日志文件最大大小，默认 `100`。**不能热加载**
- `LOG_MAX_BACKUPS`：最多保留多少个轮转归档，默认 `10`。**不能热加载**

## provider 配置 `providers/*.env` 字段说明

### 基础字段

- `PROVIDER_ID`：provider 唯一标识，会出现在路由里
- `PROVIDER_ENABLED`：是否启用该 provider
- `UPSTREAM_BASE_URL`：这个 provider 对应的上游基础地址
- `UPSTREAM_API_KEY`：这个 provider 对应的上游 key
- `UPSTREAM_ENDPOINT_TYPE`：这个 provider 内部统一使用的上游正规协议。支持 `responses`、`chat`、`anthropic`，默认 `responses`。这个字段只决定代理内部打哪个上游协议，不影响对外公开的三个兼容端口。**可热加载**
- `PROXY_API_KEY_OVERRIDE`：这个 provider 的代理鉴权覆写值。留空表示继承根 `PROXY_API_KEY`；设置普通值表示该 provider 的分组路由只接受自己的 key；设置为 `empty` 表示这个 provider 不需要代理鉴权。

### provider 级代理鉴权字段

行为说明：

- provider 分组路由 `/{providerId}/v1/*` 会优先按当前 provider 的 `PROXY_API_KEY_OVERRIDE` 判断代理鉴权。
- `PROXY_API_KEY_OVERRIDE=` 留空：继承根 `.env` 的 `PROXY_API_KEY`
- `PROXY_API_KEY_OVERRIDE=empty`：这个 provider 不做代理鉴权
- `PROXY_API_KEY_OVERRIDE=<custom>`：这个 provider 的分组路由只接受自己的 key
- 如果这个 provider 同时又是 `DEFAULT_PROVIDER`，那么裸 `/v1/*` 路由仍然允许根 `.env` 的 `PROXY_API_KEY` 访问；它自己的分组路由继续使用自己的 override key。
- provider 作用域状态查询接口 `/{providerId}/v1/requests/{requestId}` 不继承这里的代理鉴权规则，会始终保持无鉴权。

### provider 级系统提示词字段

- `SYSTEM_PROMPT_FILES`：provider 级系统提示词文件列表，使用逗号分隔多个路径；路径相对于当前 provider `.env` 文件所在目录。留空表示关闭注入。
- `SYSTEM_PROMPT_POSITION`：provider 级系统提示词的拼接位置。支持 `prepend` 和 `append`；留空或非法值会回退为 `prepend`。

行为说明：

- 模板默认写 `SYSTEM_PROMPT_FILES=prompt.md`，并配一个空的 `providers/prompt.md` 示例文件。
- 如果文件内容为空，实际效果等同于关闭注入。
- 如果配置了多个文件，会按配置顺序读取并用空行拼接。
- 如果文件不存在、文件为空，或者 `SYSTEM_PROMPT_FILES=` 留空，都不会导致启动或热加载报错，只会回退为“不注入 provider 级系统提示词”。
- 修改这些文件内容后会热加载生效，不需要重启。

拼接规则：

- 如果请求本身带有显式 system / developer / instructions 内容，provider 文本会按 `SYSTEM_PROMPT_POSITION` 拼到前面或后面。
- 对 `/v1/responses` 来说，如果请求同时带了顶层 `instructions` 和 `input` 里的 `system/developer` 项，当前会优先把 provider 文本拼到顶层 `instructions`，不会重复注入两次。
- 如果请求本身没有系统提示词，provider 文本会作为本次请求的系统提示词发送到上游。

### provider 级上游重试字段

- `UPSTREAM_RETRY_COUNT`：上游请求的最后一道安全门重试次数。这里表示“首次请求失败后，额外最多再重试多少遍”，默认 `5`。
- `UPSTREAM_RETRY_DELAY`：每次自动重试之间的间隔，默认 `5s`。

行为说明：

- 这两个字段是 **provider 级** 配置，支持热加载。
- `UPSTREAM_RETRY_COUNT` 必须是大于等于 `0` 的整数；`UPSTREAM_RETRY_DELAY` 必须是合法的 Go duration，且不能为负数。
- 如果 provider 文件里把这两个字段写成非法值，这次 provider 配置变更不会通过校验，不会静默回退到默认值，也不会替换当前已生效快照。
- 重试同时适用于流式和非流式请求。
- 只有在“请求上游后，尚未收到任何上游数据”时才会触发自动重试。
- 一旦已经收到上游首个 event / chunk，后续中途断流、解析失败或其他读流错误都不会再重试，而是直接把当次上游错误返回给客户端。
- 当所有重试都失败时，代理会在最终返回的上游错误信息前加上一句说明：已重试多少遍、每次间隔多少秒、总共重试了多少秒，然后再附上上游原始错误信息。

### provider 级上游首字节超时字段

- `UPSTREAM_FIRST_BYTE_TIMEOUT`：当前 provider 等待上游响应头 / 首字节的超时。留空表示继承根 `.env` 里的 `FIRST_BYTE_TIMEOUT`。

行为说明：

- 这个字段是 **provider 级** 配置，支持热加载。
- 默认继承根级 `FIRST_BYTE_TIMEOUT=20m`。
- 如果某个 provider 可能长时间思考、不出首字，可以只给这个 provider 单独放大，不影响其他 provider。
- 这个字段必须是合法的 Go duration，且必须大于 `0`；写成非法值时，这次 provider 配置变更不会通过校验，也不会替换当前已生效快照。

### provider 级下游非流模式覆写字段

- `DOWNSTREAM_NON_STREAM_STRATEGY_OVERRIDE`：当前 provider 的下游非流模式覆写。支持：
  - 留空：继承根 `.env` 里的 `DOWNSTREAM_NON_STREAM_STRATEGY`
  - `proxy_buffer`：当前 provider 的下游非流请求固定走代理缓冲
  - `upstream_non_stream`：当前 provider 的下游非流请求固定走上游非流 JSON

行为说明：

- 这个字段是 **provider 级** 配置，支持热加载。
- 只有下游请求本身是非流式时，这个字段才会生效；流式请求仍然固定走上游 SSE。
- 留空表示继承根级默认，不额外改动当前 provider 的行为。

### 能力开关

- `SUPPORTS_CHAT`：是否支持 OpenAI Chat Completions（兼容接口）
- `SUPPORTS_RESPONSES`：是否支持 OpenAI Responses
- `SUPPORTS_MODELS`：是否支持 `models`
- `SUPPORTS_ANTHROPIC_MESSAGES`：是否支持 Anthropic `messages`

这些能力开关当前都会在请求进入时实际生效，并且支持热加载。

补充说明：

- 这四个开关都是**弱语义**，只表示“是否开放自己这个公开端口”
- 它们不表示内部实现不能复用其他底层能力。例如 `chat` / `messages` 内部仍然可能统一转上游 `/responses`
- 这些字段现在必须写成合法布尔值，例如 `true` / `false` / `1` / `0`；像 `yes`、`enabled` 这类值会直接导致这份 provider 配置校验失败，不再静默降成 `false`

### 模型映射字段

- `MODEL_MAP_JSON`：provider 级模型映射 JSON

示例：

```json
{"gpt-5":"gpt-5.4","*":"gpt-5"}
```

含义：

- 请求 `gpt-5` 时映射到 `gpt-5.4`
- 其他没有单独写出来的模型名，全部通过 `*` 映射到 `gpt-5`
- 匹配顺序是：**`*-suffix` 通配 key（优先） → 精确 key → 去掉 suffix 后精确 key → `*` 通配 key**

### reasoning 后缀字段

- `ENABLE_REASONING_EFFORT_SUFFIX`：是否开启 `-low / -medium / -high / -xhigh` 后缀识别
- `EXPOSE_REASONING_SUFFIX_MODELS`：是否在 `/models` 中额外展示这些后缀模型名

## 使用建议

### 推荐场景

- 需要把不稳定的上游 `Responses` 接口包装成更稳定的下游协议
- 需要同时接多个 provider，并统一成类似 OpenAI 的调用方式
- 需要更好的流式体验、reasoning 展示和错误透传

### 建议优先使用的入口

- 对 OpenAI 生态客户端：优先 `chat/completions`
- 对 provider 级管理：优先 `/{providerId}/v1/*`
- 对 Anthropic 风格客户端：使用 `/v1/messages` 或 `/{providerId}/v1/messages`

## 健康检查

```bash
curl http://127.0.0.1:21021/healthz
```

`/healthz` 现在表示“当前已生效配置至少具备接请求的基础条件”，不是单纯“进程活着”。

当前会做本地静态检查，不会主动探测外部上游连通性。最小检查范围包括：

- 运行时必须已有可用的 active snapshot
- 至少要有一个启用的 provider
- 启用的 provider 不能缺少 `UPSTREAM_BASE_URL`
- 如果配置了 `DEFAULT_PROVIDER`，它必须存在、已启用，且具备 `UPSTREAM_BASE_URL`

如果这些基础条件不满足，`/healthz` 会返回 `503` 和错误原因；部署脚本也会据此把服务视为未就绪。

## 鸣谢

以下是本项目**实际使用到**，或者**明确面向兼容/适配**的开源项目、协议生态与 GitHub 仓库技术来源：

### 1. 项目实际使用到的开源项目

- [fsnotify/fsnotify](https://github.com/fsnotify/fsnotify)
  - 用于运行时监听根 `.env` 和 `providers/*.env` 文件变化，是当前热加载能力的核心依赖之一。

### 2. 明确兼容的协议与上层生态

- [OpenAI API / Responses API / Chat Completions](https://platform.openai.com/docs/api-reference)
- [Anthropic Messages API / Claude 生态协议](https://docs.anthropic.com/)

本项目对外提供 `chat/completions`、`responses`、`models`、`messages` 等兼容入口，核心目标就是让这些上层协议生态可以尽量直接接入。

### 3. 明确面向适配和修正体验的上层客户端 / GitHub 项目

- [anthropics/claude-code](https://github.com/anthropics/claude-code)
- [CherryHQ/cherry-studio](https://github.com/CherryHQ/cherry-studio)
- [CherryHQ/cherry-studio-app](https://github.com/CherryHQ/cherry-studio-app)
- [rikkahub/rikkahub](https://github.com/rikkahub/rikkahub)

这些项目并不等于本项目直接复用其源码，但本项目的协议兼容、流式展示、reasoning 展示、provider 路由、模型映射、占位推理文本以及错误透传等行为，很多都是围绕这类上层应用的真实接入需求来设计、调试和修正的。

## 许可证

MIT
