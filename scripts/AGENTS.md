# scripts Guide

## 概览

- 这里是部署与运维入口：`deploy-linux.sh`、`restart-linux.sh`、`stop-linux.sh`、`uninstall-linux.sh`。
- 主要逻辑都在 `scripts/lib/runtime.sh`；顶层脚本大多只是薄包装。

## 先看哪里

| 任务 | 文件 |
|---|---|
| 全量部署流程 | `scripts/deploy-linux.sh` |
| 重启 / 停服 / 卸载 | `scripts/restart-linux.sh`, `scripts/stop-linux.sh`, `scripts/uninstall-linux.sh` |
| 锁、构建、进程管理、健康检查 | `scripts/lib/runtime.sh` |
| 脚本回归测试 | `scripts/scripts_test.go` |

## 本目录约定

- 顶层脚本保持薄；复杂逻辑优先沉到 `scripts/lib/runtime.sh` 复用。
- 继续保持 `set -euo pipefail`、加锁、防并发操作、失败回滚这些现有契约。
- 涉及部署行为的改动优先补 `scripts/scripts_test.go`，不要只凭肉眼判断流程安全。
- 服务器部署必须在服务器本地编译，不走本地预编译二进制上传。

## 反模式

- 不要把只在单个脚本内有效的魔法常量散落多处；已有环境变量入口优先复用。
- 不要跳过健康检查、端口检查、PID 清理等保护步骤。
- 不要修改部署脚本却不检查 `runtime.sh` 是否也需要同步。

## 验证

```bash
go test -v -count=1 ./scripts/...
```
