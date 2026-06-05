# openai-compat-proxy Project Instructions

## 仓库概览

- 这是一个 Go 单二进制代理，入口在 `cmd/proxy/main.go`，主流程是 `httpapi -> adapter -> upstream -> aggregate`。
- 运行时配置来自根 `.env` 与 `providers/*.env`；provider 模板与用户说明统一放在 `providers/`。
- 核心复杂度集中在 `internal/httpapi`、`internal/upstream`、`internal/config`、`internal/adapter`。
- 部署与启停统一走 `scripts/*.sh`，实际逻辑大多收敛在 `scripts/lib/runtime.sh`。

## 目录速览

| 路径 | 作用 | 何时优先看 |
|---|---|---|
| `cmd/proxy` | 进程入口、装配 runtime store 与 HTTP server | 想知道程序如何启动 |
| `internal/httpapi` | 路由、鉴权、三套下游接口、流式处理 | 改接口、状态流、headers |
| `internal/adapter` | OpenAI / Anthropic 协议互转 | 改字段映射、tool/reasoning 兼容 |
| `internal/upstream` | 上游请求、SSE 解析、伪装 header、重试 | 改上游交互、超时、伪装 |
| `internal/config` | 根配置与 provider 配置、热加载 | 改 `.env` 语义、加载规则 |
| `providers` | provider 模板、真实配置、prompt 文件 | 增加配置项、改模板注释 |
| `scripts` | 部署、重启、停服、卸载 | 改运维流程 |

## 常用命令

```bash
go test ./...
go test -v -count=1 ./internal/httpapi/...
go test -v -count=1 ./internal/config/...
go test -v -count=1 ./internal/upstream/...
go test -v -count=1 ./scripts/...
go build -o bin/openai-compat-proxy ./cmd/proxy
go vet ./...
bash scripts/deploy-linux.sh
curl http://127.0.0.1:21021/healthz
```

## 子目录 AGENTS 边界

- `internal/httpapi/AGENTS.md`：只讲 HTTP 层、路由、流式与 handler 约束。
- `internal/config/AGENTS.md`：只讲配置校验、热加载、模板联动。
- `internal/upstream/AGENTS.md`：只讲上游请求、SSE、重试、伪装。
- `internal/adapter/AGENTS.md`：只讲 canonical request/response 与三协议映射。
- `providers/AGENTS.md`：只讲配置模板、真实 `*.env`、prompt 文件。
- `scripts/AGENTS.md`：只讲部署脚本与 `scripts/lib/runtime.sh` 的约束。

## 额外工作约定

- `internal/httpapi` 测试最密，改入口行为优先补或跑该目录测试，不要一上来只跑全量。
- `internal/upstream/protocol.go`、`internal/upstream/client.go`、`internal/httpapi/streaming.go` 都是超大文件；修改时要先定位精确函数，再小范围改。
- `providers/prompt.md` 属于 provider 级人工维护文件；除非任务明确要求，不要顺手改内容。

## 指令继承说明

- 当前仓库除本文件外，还会继承 `/home/harl/.config/opencode/AGENTS.md` 的根级工作区规则。
- 当根级规则与本仓库规则冲突时，以本文件为准。

## 项目构建与验证命令

- 编译主程序：`go build -o bin/openai-compat-proxy ./cmd/proxy`
- 全量测试：`go test -count=1 ./...`
- 配置相关改动优先补跑：`go test -count=1 ./internal/config ./scripts`
- HTTP / 协议改动优先补跑：`go test -count=1 ./internal/httpapi ./internal/upstream ./internal/adapter/...`
- 提交前至少应运行与本次改动直接相关的测试；如果改动跨配置、协议或脚本边界，默认补跑 `go test -count=1 ./...`

## 任务完成定义

- 声称完成前，必须提供本轮新鲜验证证据，不能复用旧结果。
- 涉及 Go 代码改动时，至少确保相关测试通过且 `go build -o bin/openai-compat-proxy ./cmd/proxy` 成功。
- 涉及配置语义或模板注释改动时，必须同步检查 `README.md`、`.env.example`、`providers/*.env.example` 是否仍一致。
- 不要修改真实 `.env`、`providers/*.env` 中的用户运行配置，除非当前任务明确要求迁移或上线配置。

## 配置文件语言规则

- 项目内面向用户的配置模板文件注释默认使用**简体中文**。
- 适用范围至少包括：`.env.example`、`providers/*.env.example`、以及后续新增的配置模板文件。
- 配置项名、路径、命令、协议名、代码标识符保持原样，不做翻译。
- 布尔或可选开关不要通过“整行注释掉变量”来表达关闭或未设置。
  必须使用显式占位值，例如：
  - `PROXY_API_KEY=`
  - `ENABLE_REASONING_EFFORT_SUFFIX=false`
  - `MODEL_MAP_JSON=`

## 文档同步规则

- 当运行时配置语义发生变化时，需要同步更新：
  - `README.md`
  - `.env.example`
  - `providers/*.env.example`
- 对于**不能热加载**的配置，必须在文档和配置模板里显式强调“修改后需要重启”。

## Git 提交说明规则

- 本项目后续 **git 提交说明统一使用简体中文**。
- 提交说明应简洁、明确，优先描述单一改动的目的，不要中英混写。

## 子代理派遣批次规则

- 默认应尽量多使用子代理并发执行，尤其是各任务之间没有共享状态或先后依赖时。
- 单次派遣最多只提交 5 个子代理任务，不要单批提交超过 5 个。
- 如果总任务量超过 5 个，且后续批次不依赖前一批结果，应按批次连续提交给系统，例如先提交 5 个，再继续提交 5 个，直到所有可并发批次全部提交完。
- 在这类连续分批提交过程中，中途不要穿插额外说明、总结或等待动作；应先把所有可并发批次连续提交完成，再开始统一等待结果。
- 只有当后续批次对前一批存在明确线性依赖，也就是“下一批必须等待上一批完成后才能继续”时，才允许暂停并等待上一批结果后再提交下一批。

## "mypush" 执行流程规范

- 当用户明确说出 **"mypush"** 时，视为要求执行一整套发布流程，而不只是本地提交。
- 当用户明确说出 **"开始 xx 版本正式发布流程"**（例如“开始 5.0 版本正式发布流程”）时，默认视为要求执行以下完整发布链路：
  1. 先为当前发布点创建一个空提交，提交说明使用对应版本名，例如 `5.0正式发布`。
  2. 基于当前发布点把仓库打包成 tar 产物。
  3. 用该版本名创建 GitHub release，并上传 tar 产物。
  4. 再继续执行下面定义的 `mypush` 全流程（推送、服务器拉取、部署、健康检查）。
- 标准执行顺序如下：
  1. 如果本次改动包含**新增特性或新功能**，先修订 `README.md`，把新行为、接口、配置或响应语义写清楚，再进入后续发布步骤。
  2. 先完成本地必要验证，例如与本次改动直接相关的测试、构建或健康检查命令。
  3. 将当前改动按合理粒度提交到 git，提交说明使用简体中文。
  4. 推送到远程仓库。
  5. 登录项目对应服务器，在服务器仓库中执行 `git pull --ff-only` 拉取最新代码。
  6. 如果本次改动涉及配置模板或新增配置字段，需要执行**配置迁移**：把服务器上现有 `.env` 和 `providers/*.env` 的真实值，套进最新模板格式，生成迁移后的配置文件，再上传替换。
     迁移流程（强制顺序）：
     1. **先改模板**（`.env.example`、`providers/*.env.example`），确保模板格式、分隔线、注释说明已更新到位。
     2. **读取旧配置**：SSH 到服务器，用 `grep -v "^#" | grep -v "^$"` 提取所有配置项的实际值。
     3. **生成迁移文件**：在本地用新模板格式生成完整配置文件（把第 2 步读到的值填入），文件存到 `/tmp/migrated_*`。
     4. **统一 UTF-8 编码**：用 `LANG=C.UTF-8 LC_ALL=C.UTF-8` 环境变量确保所有迁移文件以 UTF-8 编码保存，防止多字节字符乱码。
     5. **上传替换**：用 `scp` 把迁移后文件上传到服务器对应路径。
     6. **验证**：确认文件行数、分隔线、关键字段值正确，且文件为 UTF-8 编码后，再进行部署。

     **迁移原则：**
     - 迁移后的配置文件 = 新模板格式 + 服务器旧配置的真实值。
     - **值必须完整搬运**：迁移配置时不能丢失任何已有值，所有配置了的字段都必须完整保留到新文件中；如果某个字段在新模板中不存在但旧配置中有值，需要保留或上报用户，不能擅自丢弃。
     - 所有注释说明、分隔线、字段顺序均以模板为准，不保留旧配置中的冗余注释或残留字段（如 `APP_NAME`）。
     - 服务器上的 provider 级目录内现有 `*.md` 文件默认视为本地说明文档或人工维护文件；除非用户在当前任务里明确要求，否则不要覆写，也不要修改。
     - 生成迁移文件时，值从服务器读取，不从本地猜测；所有配置项的真实值以服务器当前运行中为准。
     - **编码规范**：所有配置文件（`.env`、`providers/*.env` 等）必须以 UTF-8 编码保存；迁移文件生成和上传前需确保编码正确。
  7. **在服务器上运行 `bash scripts/deploy-linux.sh`**。该脚本会自动：检查环境、编译二进制、停旧进程、启新进程、健康检查、失败回滚。禁止从本地 SCP 预编译二进制到服务器。
  8. 执行服务健康检查，并确认进程、端口或 `healthz` 响应正常。
- 如果本次改动没有涉及配置语义变化，可以跳过"升级 env 配置"这一步，但要在结果里明确说明为什么跳过。
- 如果本次改动涉及配置语义变化，默认不仅要更新仓库内文档与模板，也要同步处理服务器上的现有配置文件，不能只改代码不迁移线上配置。

## 项目经验记忆

- [2026-06-04 CST] 在修复 `UPSTREAM_XML_TOOL_CALL_STYLE` 默认值与工具调用兼容问题时，用户明确纠正：配置语义变化不能只修改 `providers/openai.env.example` 或 README。因为线上服务实际读取的是服务器 `/root/openai-compat-proxy-private/.env` 与 `/root/openai-compat-proxy-private/providers/*.env`，模板改动不会自动进入真实运行配置。以后凡是新增 provider 字段、改变默认值、改变热加载语义或上线配置行为，都必须同时做两件事：第一，仓库内更新代码、模板和文档；第二，发布流程中从服务器读取现有真实配置值，按最新模板格式无损迁移真实 `.env` 和 `providers/*.env`。迁移时必须保留所有已有 key/value，不能丢失用户已配置的模型映射、密钥、provider 覆写或本地说明文件；如果旧配置里存在新模板没有的有效字段，先保留或上报，不得擅自删除。
- [2026-06-04 CST] 在排查 `/v1/messages` 历史工具名被客户端带回成 `search_websearch_web` 时，用户明确要求：任何修复都要尽可能做成通用机制，不能为了应付单个现象而硬编码某个工具名或某个请求样例。以后处理工具名恢复、协议兼容、历史消息清洗、XML 工具调用恢复等类似问题时，必须先找可泛化的规则，例如基于当前 tools 集合、协议结构、重复拼接模式、合法 schema 边界来恢复；禁止只针对 `search_web`、`mcp__...` 或某个 request_id 写点状特判。测试也应覆盖通用模式，不能只用生产里刚好出现的那个名字证明修复。
- [2026-06-05 CST] 用户在 6.7 发布后明确要求：以后每次发布新版本时，必须同步写完整发布说明，范围覆盖“上一个发布版本之后，到当前新版本发布点为止”的所有主要修改，不能只写短摘要或只列最新一个提交。发布说明的组织方式要按用户能理解的语义分类，例如“新增 / 修复 / 调整”（必要时可加“文档 / 运维 / 测试”），不要按内部模块或工程功能区块来分，因为用户通常不关心具体代码分层，只需要快速判断哪些是新增能力、哪些是问题修复、哪些是行为调整。创建 GitHub release 前必须先用 `git log <上一版本>..HEAD` 核对完整提交范围，再把每类改动写清楚。
