# openai-compat-proxy Project Instructions

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
