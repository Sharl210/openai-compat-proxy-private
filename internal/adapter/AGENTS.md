# internal/adapter Guide

## 概览

- 这里把下游三套协议统一到内部 canonical 结构，再把聚合结果编码回各自响应格式。
- 子目录按协议拆分：`responses/`、`chat/`、`anthropic/`；跨协议共识先找现有测试和 `response_core_parity_test.go`。

## 先看哪里

| 任务 | 文件 |
|---|---|
| Responses 请求/响应 | `internal/adapter/responses/request.go`, `internal/adapter/responses/response.go` |
| Chat 请求/响应 | `internal/adapter/chat/request.go`, `internal/adapter/chat/response.go` |
| Anthropic 请求/响应 | `internal/adapter/anthropic/request.go`, `internal/adapter/anthropic/response.go` |
| 跨协议对齐回归 | `internal/adapter/response_core_parity_test.go` |

## 本目录约定

- 先改 canonical 语义，再改各协议映射，不要直接在某个协议里埋隐式特判。
- 对 `tool`、`reasoning`、`usage`、`refusal` 的改动要同步评估三套协议，不要只修当前入口。
- 如果某能力只做兼容输入、不做真实上游透传，要在测试与文档里保持一致表述。

## 反模式

- 不要把协议字段名直接泄漏到别的协议实现里；跨协议信息应通过 canonical 层传递。
- 不要改 `function_call_output`、`cache_control`、`input_audio` 等已知边界行为却不更新对应测试。

## 验证

```bash
go test -v -count=1 ./internal/adapter/...
go test -run 'Test.*Parity|TestDecode|TestEncode' ./internal/adapter/...
```
