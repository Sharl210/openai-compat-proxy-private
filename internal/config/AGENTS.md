# internal/config Guide

## 概览

- 这里负责根 `.env`、`providers/*.env`、prompt 文件和热加载快照。
- 改这里通常意味着配置语义变更；代码、README、模板三处要一起想。

## 先看哪里

| 任务 | 文件 |
|---|---|
| 根配置字段与校验 | `internal/config/config.go` |
| provider 配置字段与解析 | `internal/config/provider.go` |
| 运行时快照与版本信息 | `internal/config/snapshot.go` |
| 热加载 / fsnotify 监听 | `internal/config/reloader.go` |

## 本目录约定

- 新增根配置字段时，同时考虑：默认值、启动校验、热加载行为、README 说明、`.env.example` 注释。
- 新增 provider 字段时，同时考虑：模板占位值、provider 解析、热加载边界、README 表格说明。
- 根配置里“非法值直接校验失败”是既有契约，不要悄悄回退默认值掩盖错误。
- `PROVIDERS_DIR` 有“部分热加载”语义；涉及 Cache_Info 落盘位置时要明确是否需重启。

## 反模式

- 不要只在代码里加字段，不同步 `.env.example`、`providers/*.env.example`、`README.md`。
- 不要把“关闭”表达成注释掉整行变量；模板必须保留显式空值或布尔值。
- 不要模糊“可热加载”和“需重启”的边界，文档必须写清楚。

## 验证

```bash
go test -v -count=1 ./internal/config/...
go test -run 'TestConfig|TestProvider|TestReloader' ./internal/config/...
```
