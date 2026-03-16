# openai-compat-proxy

`openai-compat-proxy` 是一个 Go 单二进制 OpenAI 兼容代理，用来把一个不稳定、兼容性不完整的上游 Responses 接口包装成更稳定、对客户端更友好的下游接口。

当前主推接口：

- `POST /v1/chat/completions`

辅助接口：

- `GET /v1/models`
- `GET /healthz`

当前项目的主要维护目标是把上游 `/responses` 包装成更稳定的 `chat/completions` 单端口体验。

代理内部统一请求上游 `/v1/responses`，当前重点兼容：

- non-streaming 聚合
- chat 真正流式 chunk 翻译
- tools / function calling
- reasoning
- 多模态输入

## 已真实验证的能力

基于真实上游 `https://api-vip.codex-for.me/v1` 联调验证通过，当前最主要验证目标集中在 `chat/completions`：

- 文本 `chat/completions`
- 多轮 `chat/completions` assistant 历史消息透传
- 多模态输入
- 工具调用
- `chat` 真正边读边写的流式 chunk 输出
- `chat` 扩展字段 `reasoning_content`
- `chat` usage 中的 `reasoning_tokens`

兼容保留但非主线维护能力：

- `GET /v1/models`
- `POST /v1/responses`

推荐模型：

- `gpt-5`
- `gpt-5.4`

不建议继续把 `gpt-4.1` 当默认模型。

## 快速开始

### 1. 准备正式 `.env`

```bash
cp .env.example .env
```

至少填写：

```bash
LISTEN_ADDR=:18082
UPSTREAM_BASE_URL=https://api-vip.codex-for.me/v1
UPSTREAM_API_KEY=<your-upstream-key>
```

### 2. 一键部署

```bash
chmod +x scripts/deploy-linux.sh
./scripts/deploy-linux.sh
```

脚本会：

- 拒绝在没有正式 `.env` 的情况下执行
- 构建最新二进制
- 停掉旧进程
- 后台启动新服务
- 自动做一次 `healthz` 检查

## 环境变量

- `APP_NAME`
- `LISTEN_ADDR`
- `UPSTREAM_BASE_URL`
- `UPSTREAM_API_KEY`
- `PROXY_API_KEY`（可选）

## 鉴权约定

- 代理访问：`Authorization: Bearer <proxy-key>`
- 上游透传：`X-Upstream-Authorization: Bearer <upstream-key>`
- 若未提供 `X-Upstream-Authorization`，则回退到 `UPSTREAM_API_KEY`

## 请求示例

### responses（兼容保留）

```bash
curl http://127.0.0.1:18082/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5",
    "input": [{
      "role":"user",
      "content":[{"type":"input_text","text":"Say hello in one word."}]
    }],
    "stream": false
  }'
```

### chat

```bash
curl http://127.0.0.1:18082/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5",
    "messages": [
      {"role": "user", "content": "Say hello in one word."}
    ],
    "stream": false
  }'
```

### chat 多轮历史

当前版本会在转发到上游 `/v1/responses` 时按角色重写文本 content type：

- `user` / `system` / `developer` → `input_text`
- `assistant` → `output_text`

这可以避免部分上游在重放 assistant 历史消息时返回类似 `Invalid value 'input_text'` 的 400 错误。

### chat reasoning_content 扩展

当前版本在 `chat/completions` 上额外提供一个 **de facto 扩展字段**：

- non-stream: `choices[0].message.reasoning_content`
- stream: `choices[0].delta.reasoning_content`

它不会把推理内容混进普通 `content`，而是单独暴露，兼容许多主流 OpenAI-compatible 网关/客户端的扩展读取方式。

当前可见 reasoning 来源有两类：

- 上游直接发 `response.reasoning.delta`
- 上游在 reasoning output item 的 `summary[]` 中返回 `summary_text`

注意：这不是 OpenAI 官方 `chat/completions` 标准字段，而是兼容生态中的常见扩展。

### chat 真正流式转发

当前版本的 `chat/completions` 在 `stream=true` 时，不再先把上游 SSE 全部读完再统一输出，而是：

- 上游 event 一到就立刻转成下游 `chat.completion.chunk`
- 每个 chunk 立即 flush 给客户端
- 结束时再补 usage chunk（如果 `stream_options.include_usage=true`）和 `[DONE]`

这会显著改善“很久没首字、然后一下子吐很多 token”的体验。

### chat 预正文状态流

为了减少正文开始前的长时间空白，当前版本会在正文 token 出现前，按轻量兼容扩展发送少量状态提示：

- `分析中…`
- `正在组织回答…`

这些状态通过 `delta.reasoning_content` 发出；一旦正文 `delta.content` 开始输出，就停止伪造状态流。

只有在上游**没有返回真实可见 reasoning** 时，代理才会发送这些状态提示；如果上游已经返回了真实 reasoning delta 或 reasoning summary，代理不会覆盖它。

它们是体验优化信号，不代表模型原始 chain-of-thought。

### chat SSE 反缓冲头

当前 `chat` 流式响应会显式设置：

- `Content-Type: text/event-stream`
- `Cache-Control: no-cache, no-transform`
- `Connection: keep-alive`
- `X-Accel-Buffering: no`

这用于减少代理层 / 网关层的额外缓冲。

### chat reasoning 请求透传

当前版本**不会代理层强行指定必须推理**，但会把 chat 兼容请求转换成更适合上游 `/responses` 的 reasoning 形态。

它的行为是：

- 如果调用方没有传 `reasoning` / `reasoning_effort`，代理不会主动打开推理
- 如果调用方传了 `reasoning_effort`，代理会把它转换成上游 `reasoning.effort`
- chat 兼容请求默认补 `reasoning.summary: "auto"`
- 如果调用方直接传了 `reasoning` 对象，代理会尽量按原样透传；若缺少 `summary`，代理会补 `summary: "auto"`

这意味着“是否启用推理”仍由客户端请求决定，而“是否请求 summary”在 chat 兼容层会被代理自动补齐，以便更稳定地从上游拿到可展示的摘要。

### chat reasoning_tokens

如果上游没有给可见 reasoning 文本，但在完成事件里给了 token 统计，当前版本会把它映射到 chat usage：

- non-stream: `usage.completion_tokens_details.reasoning_tokens`
- stream: 当请求带 `stream_options.include_usage: true` 时，在最后一个 usage chunk 中返回

这可以让客户端至少知道本次请求确实发生了推理，即使上游只返回加密 reasoning。

### chat stream_options.include_usage

当前版本支持透传并消费：

```json
{"stream_options":{"include_usage":true}}
```

开启后，`chat/completions` 流式响应会在 `data: [DONE]` 之前追加一个 `choices: []` 的 usage chunk。

当前版本还会尽量保留上游 cache 相关 usage 明细；例如当上游返回 `input_tokens_details.cached_tokens` 时，代理会映射到：

- non-stream: `usage.prompt_tokens_details.cached_tokens`
- stream: 最后一个 usage chunk 中的 `usage.prompt_tokens_details.cached_tokens`

### normalization version 契约

为了把代理层的缓存前缀规则固定成显式契约，当前版本对会参与规范化的主线路由返回：

- `X-Proxy-Normalization-Version: v1`

当前 `v1` 代表的规范化规则包括：

- 上游统一走 `/responses`
- 上游统一发送 `stream: true`
- `assistant` 文本历史映射为 `output_text`
- `reasoning_content` 仅作为下游展示扩展字段，不会回灌到上游 assistant prompt
- chat reasoning 缺少 `summary` 时补 `summary: "auto"`
- tool schema 中缺少 `items` 的 array 节点自动补 `items: {}`

这不是直连兼容承诺，而是“始终走代理时缓存前缀稳定”的版本承诺。未来如果这些规则需要变更，应升级 normalization version，而不是静默修改现有 `v1` 语义。

### chat 增量缓存稳定性

当前版本专门修正了一类真实场景下的缓存干扰：

- 多轮对话逐条追加时，`reasoning_content` 不再被重放进上游 assistant prompt

这是因为 `reasoning_content` 在代理里是面向客户端/UI 的展示扩展字段，不属于应该参与上游缓存前缀的稳定历史内容。保留它给下游显示，但把它重新塞回上游 assistant `output_text`，会让真实多轮对话的共享前缀更容易漂移。

### 日志系统

当前版本新增了默认脱敏的双通道日志：

- stdout：摘要日志
- 本地 JSON 文件：结构化日志

新增环境变量：

- `LOG_FILE_PATH`：日志文件路径，默认 `.proxy.requests.jsonl`
- `LOG_INCLUDE_BODIES`：是否记录原文 body；默认关闭，支持 `true` / `1`
- `LOG_MAX_SIZE_MB`：单个日志文件最大大小（MB），默认 `100`
- `LOG_MAX_BACKUPS`：最多保留多少个轮转归档文件，默认 `10`

默认会记录：

- 下游请求摘要
- canonical 请求摘要
- 上游请求摘要
- 上游响应 usage / cached_tokens
- 下游响应摘要
- request id / 路由 / 耗时 / normalization version
- message hash / prefix hash / item hash 等可比对摘要字段
- stream 与 non-stream 两条路径的 usage 观测事件

默认不会明文记录：

- Authorization
- 请求/响应 body

如果显式打开 `LOG_INCLUDE_BODIES=true`，才会把 body 一并写入 JSON 日志。

当前日志文件会自动轮转：

- 超过 `LOG_MAX_SIZE_MB` 后切分为带时间戳的归档文件
- 仅保留最近 `LOG_MAX_BACKUPS` 个归档
- 当前活跃文件始终仍是 `LOG_FILE_PATH`

### restart 脚本

当前版本新增：

- `scripts/restart-linux.sh`

它的行为保持最小：

1. 调用 `scripts/uninstall-linux.sh`
2. 调用 `scripts/deploy-linux.sh`

### models（兼容保留）

```bash
curl http://127.0.0.1:18082/v1/models \
  -H 'Authorization: Bearer <proxy-key>'
```

当前实现会把 `GET /v1/models` 透传到上游并回传结果。

### tools schema 兼容

当前版本会在转发工具定义到上游前做一个最小兼容修复：

- 如果某个 JSON Schema 节点声明了 `"type": "array"`
- 但缺少 `items`
- 代理会自动补成 `"items": {}`

这用于兼容部分上游对 function/tool 参数 schema 的严格校验。

### 多模态

```bash
curl http://127.0.0.1:18082/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5",
    "messages": [{
      "role": "user",
      "content": [
        {"type": "text", "text": "What language logo is this? Answer in one word."},
        {"type": "image_url", "image_url": {"url": "https://raw.githubusercontent.com/github/explore/main/topics/python/python.png"}}
      ]
    }],
    "stream": false
  }'
```

### 工具调用

```bash
curl http://127.0.0.1:18082/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gpt-5",
    "messages": [{"role": "user", "content": "Use the tool to get the weather for Shanghai."}],
    "tools": [{
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get current weather for a city",
        "parameters": {
          "type": "object",
          "properties": {"city": {"type": "string"}},
          "required": ["city"]
        }
      }
    }],
    "tool_choice": "auto",
    "stream": true
  }'
```

## 文档

- 当前文档默认以 `chat/completions` 为主线描述
- 功能报告：`docs/功能报告.md`
- 部署文档：`docs/部署文档.md`
