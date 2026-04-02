# internal/httpapi Guide

## 概览

- 这里是代理的 HTTP 门面：路由解析、请求鉴权、兼容接口 handler、流式输出、版本头与请求状态都在这里。
- 最大复杂度在 `streaming.go`；先查已有 helper 与测试，再碰状态机。

## 先看哪里

| 任务 | 文件 |
|---|---|
| 启动与 mux 装配 | `internal/httpapi/server.go` |
| 裸 `/v1/*` 与 `/{providerId}/v1/*` 路由 | `internal/httpapi/routes.go` |
| `/responses` 主处理链 | `internal/httpapi/handlers_responses.go` |
| `/chat/completions` | `internal/httpapi/handlers_chat.go` |
| `/messages` | `internal/httpapi/handlers_anthropic.go` |
| 流式状态机与事件输出 | `internal/httpapi/streaming.go` |
| provider prompt / auth / 错误透传 | `internal/httpapi/provider_prompt.go`, `internal/httpapi/auth_helpers.go`, `internal/httpapi/upstream_errors.go` |

## 本目录约定

- 新的入口行为先判断属于哪个公开端口，再决定落到哪个 handler，不要把跨端口分支继续塞进 `server.go`。
- 路由相关改动同时检查 legacy 裸路由与 provider 显式路由，不要只修一种路径。
- 流式改动优先复用现有 event 写出 helper，避免复制一整段 SSE 拼接逻辑。
- 下游兼容行为有大量回归测试；新增条件分支时优先补同文件附近测试。

## 反模式

- 不要把 provider 配置解析逻辑直接写进 handler；这类逻辑应留在 `internal/config`。
- 不要在未经测试的情况下改 `streaming.go` 的事件顺序或终态补发逻辑。
- 不要新增只服务单个协议的公共 helper；如果只给某一路由用，就放回对应 handler 文件。

## 验证

```bash
go test -v -count=1 ./internal/httpapi/...
go test -run 'TestResponses|TestChat|TestAnthropic' ./internal/httpapi/...
```
