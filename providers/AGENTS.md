# providers Guide

## 概览

- 这里放 provider 真实配置、模板和 provider 级 prompt 文件。
- 运行时只读取 `providers/*.env`，忽略 `*.env.example`；模板注释面向用户时默认用简体中文。

## 先看哪里

| 任务 | 文件 |
|---|---|
| provider 模板字段与注释风格 | `providers/openai.env.example` |
| provider 级提示词文件 | `providers/prompt.md` |

## 本目录约定

- 新增字段时，模板必须使用显式占位值，不要通过整行注释表达“关闭”或“未设置”。
- 涉及布尔值、可热加载、需重启等语义时，注释必须明确，不要让用户猜。
- `providers/*.env.example` 是模板；除非任务明确要求，不要把真实密钥写回仓库。
- `prompt.md` 默认视为人工维护说明/提示词文件，非必要不要顺手覆盖。

## 反模式

- 不要在模板里丢字段顺序、分隔线和中文说明一致性。
- 不要假设 provider 新字段只改模板即可；通常还要同步 `internal/config` 与 `README.md`。
- 不要提交真实 `providers/*.env`。
