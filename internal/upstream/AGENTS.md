# internal/upstream Guide

## 概览

- 这里封装代理到上游的 HTTP 交互：构造请求、选择协议、重试、解析 SSE、处理客户端伪装 header。
- `client.go` 与 `protocol.go` 都很大；优先按函数边界改，不要横扫式重构。

## 先看哪里

| 任务 | 文件 |
|---|---|
| HTTP client / timeout / retry | `internal/upstream/client.go` |
| 上游协议选择与 header 伪装 | `internal/upstream/protocol.go` |
| SSE 事件工具 | `internal/upstream/sse.go` |

## 本目录约定

- 改伪装逻辑时同时检查 `opencode`、`claude`、`codex`、`none` 四类语义，不要只修单一路径。
- 改 SSE 解析时保持现有“首字节前可重试、收到事件后不自动重试”的契约。
- 上游请求体组装优先从 canonical request 推导，不要让 handler 直接拼装 provider 特化字段。
- 失败返回要保留可读上下文；若已存在 `HTTPStatusError` 或 retry notice，就沿用既有格式。

## 反模式

- 不要把 provider 特化行为偷偷塞到 `httpapi`，这类逻辑应在 upstream 层收口。
- 不要在没有对应测试的情况下改 SSE scanner buffer、事件 finalize、header 注入矩阵。
- 不要吞掉上游错误体；已有透传/摘要逻辑优先复用。

## 验证

```bash
go test -v -count=1 ./internal/upstream/...
go test -run 'TestClient|TestProtocol' ./internal/upstream/...
```
