# openai-compat-proxy

一个 Go 单二进制的 OpenAI 兼容代理。**这个项目上游只对接 `Responses` 接口**，代理内部统一把请求转给上游 `/responses`，然后再对外导出兼容端点，给不同客户端和协议风格使用。项目当前支持多提供商配置，主要导出三类接口：**Anthropic**、**OpenAI Responses**、**OpenAI Chat Completions（兼容接口）**，并附带模型列表接口。

## 项目实现的几个大功能

### 1. 多提供商路由

- 支持通过 `providers/*.env` 配置多个 provider
- 支持按 provider id 访问：`/{providerId}/v1/...`
- 支持设置默认 provider，兼容裸 `/v1/*` 路由
- 一个实例可以同时代理多个不同上游

### 2. 统一包装上游 Responses

- 代理内部统一请求上游 `/responses`
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
- 支持精确模型映射和 `*` 通配符映射
- 支持 `-low / -medium / -high / -xhigh` 后缀自动转 reasoning effort
- 支持在 `/models` 中暴露映射模型名和扩展后的后缀模型名

### 5. 错误透传与运行日志

- 非流式请求支持透传上游 JSON 错误体和状态码
- 支持代理访问鉴权和调用方自带上游鉴权
- 支持结构化日志、日志轮转、可选 body 记录
- 提供 `healthz`、部署脚本、重启脚本、卸载脚本

### 6. Claude 提示缓存兼容

- 支持 Anthropic / Claude 兼容请求中的 `cache_control`
- 支持顶层 `cache_control` 透传
- 支持文本内容块上的 `cache_control` 透传
- 适配 Claude 客户端里的“提示缓存（cache_control）”开关

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
APP_NAME=openai-compat-proxy
LISTEN_ADDR=:21021
PROVIDERS_DIR=./providers
DEFAULT_PROVIDER=openai
ENABLE_LEGACY_V1_ROUTES=true
PROXY_API_KEY=

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

### 4. 执行脚本

#### `scripts/deploy-linux.sh`

作用：首次部署或重新部署。

它会做这些事：

- 检查根目录 `.env` 是否存在
- 自动安装 Go（仅在当前机器缺少 Go 且支持 `apt-get` 时）
- 编译 `./cmd/proxy`
- 停掉旧进程
- 后台启动代理
- 写入 `.proxy.pid`
- 把运行日志写到 `.proxy.log`
- 自动请求 `healthz` 做启动检查

使用方式：

```bash
chmod +x scripts/*.sh
./scripts/deploy-linux.sh
```

#### `scripts/restart-linux.sh`

作用：重启服务。

它本质上等于：

1. 执行 `scripts/uninstall-linux.sh`
2. 再执行 `scripts/deploy-linux.sh`

使用方式：

```bash
./scripts/restart-linux.sh
```

#### `scripts/uninstall-linux.sh`

作用：停止并清理当前部署产物。

它会：

- 停止 `.proxy.pid` 对应的进程
- 删除 `.proxy.pid`
- 删除 `.proxy.log`
- 删除 `bin/openai-compat-proxy`

使用方式：

```bash
./scripts/uninstall-linux.sh
```

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

### 默认 provider 路由

如果你要兼容历史客户端，使用裸 `/v1/*` 路由时应保证存在默认 provider。

默认 provider 只通过全局 `.env` 中的 `DEFAULT_PROVIDER` 指定。

注意：`DEFAULT_PROVIDER` 必须对应一个已存在且启用的 provider。

## 鉴权约定

- 代理鉴权：`Authorization: Bearer <proxy-key>`
- 也支持：`X-API-Key` / `Api-Key`
- 上游鉴权透传：`X-Upstream-Authorization: Bearer <upstream-key>`
- 如果请求里没有传 `X-Upstream-Authorization`，则回退到当前选中 provider 的 `UPSTREAM_API_KEY`

## 全局配置 `.env` 字段说明

### 基础字段

- `APP_NAME`：应用名，可选
- `LISTEN_ADDR`：监听地址，例如 `:21021`
- `PROXY_API_KEY`：代理自身访问 key，可选；设置后调用方必须带代理鉴权

### 多 provider 相关字段

- `PROVIDERS_DIR`：provider 配置目录，例如 `./providers`
- `DEFAULT_PROVIDER`：默认 provider 的 id
- `ENABLE_LEGACY_V1_ROUTES`：是否把裸 `/v1/*` 作为默认 provider 的兼容入口

### 日志字段

- `LOG_ENABLE`：是否启用结构化日志
- `LOG_FILE_PATH`：日志文件路径，默认 `.proxy.requests.jsonl`
- `LOG_INCLUDE_BODIES`：是否记录请求和响应 body，只有 `true` / `1` 才会开启
- `LOG_MAX_SIZE_MB`：单个日志文件最大大小，默认 `100`
- `LOG_MAX_BACKUPS`：最多保留多少个轮转归档，默认 `10`

## provider 配置 `providers/*.env` 字段说明

### 基础字段

- `PROVIDER_ID`：provider 唯一标识，会出现在路由里
- `PROVIDER_ENABLED`：是否启用该 provider
- `UPSTREAM_BASE_URL`：这个 provider 对应的上游基础地址
- `UPSTREAM_API_KEY`：这个 provider 对应的上游 key

### 能力开关

- `SUPPORTS_CHAT`：是否支持 OpenAI Chat Completions（兼容接口）
- `SUPPORTS_RESPONSES`：是否支持 OpenAI Responses
- `SUPPORTS_MODELS`：是否支持 `models`
- `SUPPORTS_ANTHROPIC_MESSAGES`：是否支持 Anthropic `messages`

### 模型映射字段

- `MODEL_MAP_JSON`：provider 级模型映射 JSON

示例：

```json
{"gpt-5":"gpt-5.4","*":"gpt-5"}
```

含义：

- 请求 `gpt-5` 时映射到 `gpt-5.4`
- 其他没有单独写出来的模型名，全部通过 `*` 映射到 `gpt-5`
- 匹配顺序是：**先精确匹配，再匹配 `*`，最后才透传原模型名**

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

## 许可证

如需补充许可证，请按仓库实际策略维护。
