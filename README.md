# openai-compat-proxy

`openai-compat-proxy` 是一个 Go 单二进制 OpenAI 兼容代理，用来把一个不稳定、兼容性不完整的上游 Responses 接口包装成更稳定、对客户端更友好的下游接口。

当前对外暴露：

- `POST /v1/responses`
- `POST /v1/chat/completions`
- `GET /healthz`

代理内部统一请求上游 `/v1/responses`，并兼容：

- non-streaming 聚合
- responses 流式透传
- chat 流式 chunk 翻译
- tools / function calling
- reasoning
- 多模态输入

## 已真实验证的能力

基于真实上游 `https://api-vip.codex-for.me/v1` 联调验证通过：

- 文本 `responses`
- 文本 `chat/completions`
- 多模态输入
- 工具调用
- `responses` 流式透传
- `chat` 流式 chunk 输出

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

### responses

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

- 功能报告：`docs/功能报告.md`
- 部署文档：`docs/部署文档.md`
