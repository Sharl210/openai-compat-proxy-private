# Dynamic Token Estimator Design Spec (2026-06-09)

## 背景

当前代理层 `MODEL_LIMIT_CONTEXT_TOKENS` 的本地判断仍依赖启发式估算。虽然最近已经修掉了 Responses 与 Anthropic 的重复计数问题，但真实生产请求已经证明：仅靠当前的本地估算仍会系统性低估 `/v1/responses` 下 reasoning-heavy、tool-heavy、structured input 很重的请求。

本次用户给出的核心目标不是“补一条统计日志”，而是让代理本地逐步形成一套**按 provider / 最终上游模型持续演化的 token 估算能力**：

1. 每个 provider、每个最终发向上游的原始模型名，都保留自己的历史观测状态。
2. 随着真实请求越用越多，本地估算器能越来越接近对应模型 / 协议的真实 usage。
3. 这套状态必须落在 `providers` 目录下，可直接通过现有管理台浏览、查看、删除。
4. 删除某个 provider 或某个模型的估算状态文件后，应当视为该桶重新冷启动。
5. 整个能力必须能解释、能重置、能演化，而不是一套不可追踪的黑箱倍率。
6. 本轮设计确认后，后续实际开发、发布、服务器部署和健康检查都按项目“老规矩”完整执行。

## 问题定义

当前问题至少分成三层，不能混成一句“字符数和 token 数不成比例”：

1. **基础 tokenizer 差异**
   - 不同厂商、不同模型，文本字符与 token 的关系天然不同。

2. **payload 结构差异**
   - 同一个最终模型，在 `responses`、`chat`、`anthropic` 三种上游协议下，请求体结构不同，token 开销也会明显不同。
   - tool schema、tool result、reasoning block、structured input item、multimodal/file item 都会带来额外偏差。

3. **缓存口径差异**
   - 上游返回的 `input_tokens` 里可能包含大量 `cached_tokens`。
   - 若不区分 total / cached / uncached，这些历史缓存 token 会污染本地“字符 → token”映射的学习结果。

因此，本项目需要的不是“一个固定公式”或“一个历史倍率”，而是：

- 一套**理论驱动的基础估算器**
- 一套**按桶持久化的统计状态**
- 一套**保守、可控、可解释的动态修正层**

## 目标与非目标

### 目标

1. 提供一套可持久化的 token estimator 状态系统，按 provider / endpoint / 最终上游模型分桶。
2. 记录本地基础估算值与上游真实 usage 之间的长期偏差。
3. 在管理台中可直接查看对应 provider / model 的状态文件，并允许通过删除文件或目录重置学习历史。
4. 一期先完成“基础估算器增强 + 持久化观测 + 推荐修正值”，为后续安全接入 runtime correction 做准备。
5. 设计必须能兼容项目现有的 `Cache_Info`、`debugarchive`、`adminui`、`reloader` 边界，不引入新的热加载抖动。

### 非目标

1. 一期**不直接**让历史学习结果参与 `MODEL_LIMIT_CONTEXT_TOKENS` 的硬拦截判断。
2. 一期不引入数据库、外部训练服务或复杂离线机器学习流水线。
3. 一期不试图一次性精确还原每家厂商每个模型的真实 tokenizer 全部细节。
4. 一期不把这套状态做成不可解释的黑箱“自学习公式”。

## 设计原则

1. **以最终上游模型为边界**
   - 统计分桶以“真正发向上游的最终模型名”为准，而不是客户端传给代理的原始模型名。
   - 这是因为真正决定 usage 的，是最终上游协议构造和最终上游模型。

2. **以协议类型隔离 shape 差异**
   - 仅按 provider + model 分桶不够，必须至少再加 `upstream_endpoint_type` 维度。
   - 同一最终模型在 `responses`、`chat`、`anthropic` 三种协议下，其结构开销与 usage 口径可能完全不同。

3. **总 token 与非缓存 token 分离**
   - 必须同时记录：
     - `input_tokens`
     - `cached_tokens`
     - `uncached_input_tokens = input_tokens - cached_tokens`
   - 动态校正主学习目标优先看 `uncached_input_tokens`，上下文窗口风险观察则保留 `input_tokens`。

4. **基础估算器先可解释，再做历史修正**
   - 先把估算器拆成理论可解释的分项结构，再用历史 usage 去修正残差。
   - 不允许一上来就用一个总倍率覆盖全部误差来源。

5. **状态优先于公式**
   - 落盘的是统计状态，不是单个静态公式。
   - 公式只是状态推导出的当前运行建议值。

6. **先观测，再接入 runtime**
   - 一期只做 persistence + observation + suggested correction。
   - 二期样本稳定后，再保守接入 runtime correction。

## 架构总览

本设计采用三层结构。

### 第 1 层：基础估算器

在现有 `context_limit.go` 的基础上，逐步演进为“按结构分项”的可解释估算器，而不是单一 `chars / 4`。

一期建议至少拆成以下估算维度：

1. 文本内容估算
2. structured responses input item 开销估算
3. tool schema 开销估算
4. tool result / function call 开销估算
5. reasoning block / reasoning item 开销估算
6. multimodal / file / audio item 开销估算

这些项共同构成 `base_estimate`，作为动态校正之前的理论基线。

### 第 2 层：分桶统计状态

按固定桶键记录长期观测状态：

- `provider_id`
- `upstream_endpoint_type`
- `final_upstream_raw_model`

每个桶独立维护自己的统计状态文件、可视文本摘要、更新时间、样本量和推荐修正值。

### 第 3 层：动态修正层

使用历史 usage 对 `base_estimate` 的系统性偏差做保守修正，但一期只落盘和展示，不直接接入 admission 控制。

二期若要接入运行时，采用：

`corrected_estimate = base_estimate × learned_factor(shape-aware, bounded, confidence-gated)`

## 目录结构

参考现有 `providers/Cache_Info` 的 JSON + TXT 双写模式，本设计采用独立目录：

```text
providers/
  Token_Estimator/
    SYSTEM_JSON_FILES/
      <provider_id>/
        <endpoint_type>/
          <safe_model_name>.json
    <provider_id>/
      <endpoint_type>/
        <safe_model_name>.txt
```

### 选择该结构的原因

1. 与 `Cache_Info` 的长期资产目录风格一致，便于维护者理解。
2. JSON 文件供程序恢复状态、供后续算法迭代读取。
3. TXT 文件供现有管理台直接浏览。
4. 用户可以通过管理台：
   - 删除整个 `Token_Estimator/<provider_id>/` 目录来重置该 provider 全部学习历史。
   - 删除某个 `<safe_model_name>.json/.txt` 来重置单个 endpoint/model 桶。
5. 该目录位于 `providers` 根下，但不属于根 `.env` / `providers/*.env` / provider prompt 文件监控集合，不会触发热加载抖动。

## safe model name 规则

最终上游模型名可能包含路径、空格、冒号或不适合文件名的字符，因此需要做稳定映射。

要求：

1. 文件系统上使用 `safe_model_name`。
2. JSON/TXT 内容内部必须同时保留原始 `final_upstream_raw_model`，供 UI 和调试显示。
3. `safe_model_name` 必须稳定、可逆或至少可通过文件内容恢复原模型名。
4. 模型名映射规则需要带版本号，避免未来修改转义算法后造成旧文件歧义。

## 状态文件模型

每个 JSON 文件表示一个 bucket 的统计状态，而不是单条原始日志。

### 元信息

- `schema_version`
- `estimator_version`
- `provider_id`
- `endpoint_type`
- `final_upstream_raw_model`
- `safe_model_name`
- `created_at`
- `updated_at`
- `sample_count`
- `usable_sample_count`
- `discarded_sample_count`

### usage 统计

- `avg_input_tokens`
- `avg_cached_tokens`
- `avg_uncached_input_tokens`
- `p50_input_tokens`
- `p90_input_tokens`
- `max_input_tokens`

### 本地估算偏差统计

- `avg_base_estimate`
- `avg_total_ratio`
- `avg_uncached_ratio`
- `rolling_total_correction`
- `rolling_uncached_correction`
- `max_seen_ratio`
- `min_seen_ratio`

### shape 特征统计

一期至少记录以下特征：

- `avg_text_chars`
- `avg_input_item_count`
- `avg_reasoning_item_count`
- `avg_tool_call_count`
- `avg_tool_result_count`
- `avg_multimodal_item_count`

### 稳健性字段

- `outlier_count`
- `protocol_changed_resets`
- `last_protocol_signature`
- `last_estimator_signature`
- `recent_samples_summary`（有上限的短窗口摘要）

## recent sample 设计

为保证状态可解释且能重新估计 robust metrics，每个 bucket 需要保留一个**有界样本窗口**，但不能无限增长。

一期建议：

- 保留最近 `N=64` 或 `N=128` 条可用样本摘要。
- 每条摘要只保留必要字段：
  - 时间
  - endpoint type
  - `base_estimate`
  - `input_tokens`
  - `cached_tokens`
  - `uncached_input_tokens`
  - shape 特征简表
  - 是否被当作 outlier 丢弃

不保留完整请求体，不保留原始 prompt 内容，不复用 debugarchive 作为 estimator 历史库。

## shape 分类

一期不做复杂 ML 分类器，但必须把明显不同的 payload 形态区分开来。建议至少做以下粗分类：

1. `plain`
   - 普通文本 / 历史消息为主，无明显 reasoning/tool-heavy 特征。

2. `structured_responses`
   - `/v1/responses` 下 input item 结构明显，存在大量 typed item。

3. `reasoning_heavy`
   - reasoning item / reasoning block 明显高于普通请求。

4. `tool_heavy`
   - function_call / function_call_output / tool schema 很多。

5. `multimodal`
   - 存在 file/image/audio/document 等非纯文本 item。

说明：

- 一期 shape 只用于统计分组和推荐 correction，不用于复杂在线分类优化。
- 若一个请求同时满足多个条件，可记录主分类 + 标志位，而非强制互斥。

## 采样与更新策略

### 采样时机

仅在以下条件同时满足时，才将请求纳入 estimator 学习样本：

1. 请求已完成并拿到稳定 upstream usage。
2. 具备明确的最终上游模型名。
3. 具备本地基础估算值。
4. `input_tokens >= cached_tokens >= 0`。
5. 当前 estimator/version/protocol signature 未与样本严重不兼容。

### 样本来源

当前最可靠的学习信号来自：

- upstream completion usage
- 终态 streaming usage
- raw usage 字段

不从本地模拟超限失败、上游错误终态、usage 缺失请求中学习。

### 更新原则

不采用“每次请求回来后直接更新总平均倍率”的简单策略。

一期采用：

1. **样本过滤**：无效 usage / 负值 / 缺字段样本直接丢弃。
2. **outlier clipping**：将极端 ratio 计入 `outlier_count`，但不直接纳入主统计。
3. **bounded rolling stats**：对 recent sample window 计算裁剪均值、分位数或有界 EWMA。
4. **separate total vs uncached**：
   - `input_tokens` 用于观察窗口风险
   - `uncached_input_tokens` 用于主校正目标

## total / cached / uncached 的语义

### total input tokens

表示上游最终看到的总输入 token。对“真实上下文窗口压力”非常有价值，必须保存。

### cached tokens

表示上游重复命中的缓存部分。不能直接拿它参与字符 → token 的拟合，否则会把历史缓存误当成本地估算器低估。

### uncached input tokens

定义为：

`uncached_input_tokens = input_tokens - cached_tokens`

这是动态修正的主学习目标。一期所有推荐 correction 优先围绕它计算。

## runtime correction 策略

### 一期

一期只做：

1. `base_estimate` 计算
2. bucket 状态文件更新
3. suggested correction 计算
4. 日志 / 管理台展示 suggested correction 和 confidence

**一期不直接将 suggested correction 接入 `MODEL_LIMIT_CONTEXT_TOKENS` 的 admission 判断。**

### 二期

当某个 bucket 满足以下条件后，允许进入 runtime correction 试点：

1. `usable_sample_count` 达到最小门槛
2. 当前协议 signature 与样本相容
3. correction 波动已经收敛到允许区间内
4. outlier 占比低于阈值
5. 管理开关显式允许对该 bucket 或该 provider 启用 runtime correction

运行时采用：

`corrected_estimate = base_estimate × learned_factor`

其中：

- `learned_factor` 不能无界放大或缩小
- 必须经过 confidence gate
- 必须支持 fallback 到 `base_estimate`

## confidence / readiness 设计

一期虽然不接入 runtime，但必须为二期预留 readiness 概念。

每个 bucket 至少需要导出：

- `sample_count`
- `usable_sample_count`
- `confidence_level`
- `runtime_ready`（一期固定 false，二期由规则决定）

这样管理台和日志里都能直观看出：

- 某个模型是否只是刚开始观测
- 还是已经有足够稳定的历史支撑后续 runtime correction

## 管理台语义

管理台不需要一期就做复杂新页面，但必须保证：

1. `providers/Token_Estimator/**` 下的 JSON/TXT 文件能通过现有文件浏览看到。
2. 用户删除这些文件或目录后，系统视为对应 bucket 重新冷启动。
3. 删除后无需额外恢复动作；后续新请求自然重建状态文件。

若后续增加专门 UI：

- 可显示每个 bucket 的 sample_count、recent ratio、suggested correction、runtime readiness。
- 但这属于后续增强，不是本 spec 一期的必要项。

## 热加载与运行时边界

1. `Token_Estimator` 目录下的文件不应纳入 `.env` / provider 配置热加载监听链。
2. 频繁写 estimator 状态文件不能触发运行配置抖动。
3. estimator manager 的生命周期可以参考 `Cache_Info` manager：
   - 启动时加载现有状态
   - 运行时更新内存
   - 定期原子落盘
   - 退出前 flush

## 复用现有模式

本项目中最适合复用的模式是 `Cache_Info`：

1. provider 根下长期资产目录
2. JSON + TXT 双写
3. 原子写入
4. 删除文件即可重置
5. 管理台天然可见

不建议复用的模式：

- `debugarchive`：按 request id 归档，适合诊断，不适合长期模型状态
- `logging`：按请求写结构化日志，适合追踪，不适合可演化状态

## 风险与缓解

### 风险 1：shape 混桶

如果只按 provider + model 分桶，会把不同协议、不同 payload 形态的误差混在一起。

**缓解：**
- 强制加入 `upstream_endpoint_type`
- 文件内至少再保留粗 shape 统计

### 风险 2：cached token 污染

如果直接用 total input token 学习 correction，历史缓存会严重污染结果。

**缓解：**
- total / cached / uncached 三套字段必须分开
- 主 correction 只围绕 uncached 学习

### 风险 3：异常样本带歪校正

极长上下文、厂商 usage 波动、协议升级、bug 请求都可能带歪桶状态。

**缓解：**
- outlier clipping
- 最小样本门槛
- estimator/protocol signature
- readiness gate

### 风险 4：协议或构包逻辑变化后旧数据失效

如果未来 upstream body 构造变了，旧 ratio 可能不再适用。

**缓解：**
- estimator version
- protocol signature
- 不兼容变化后降权、切段或重置

### 风险 5：过早接入 runtime correction 误杀请求

**缓解：**
- 一期严格不接 runtime
- 二期仅在 bucket 稳定后分批启用

## 分阶段计划

### Phase 1：观测与状态体系

交付内容：

1. 新增 `Token_Estimator` 目录与 manager
2. 支持 bucket 状态加载 / 更新 / flush / 删除重建
3. 记录基础估算值、upstream usage、cached/uncached、shape 特征
4. 计算 suggested correction，但只用于观测
5. 在日志或可读 txt 里展示 bucket 当前状态

### Phase 2：保守 runtime correction

交付内容：

1. confidence gate
2. bounded learned_factor
3. provider / endpoint / bucket 级别开关
4. corrected estimate 试点接入本地 context-limit admission

### Phase 3：更精细的结构型校正

交付内容：

1. 更强的 shape 感知
2. 更细的分项 estimator
3. 更稳健的异常检测与自动降级

## 一期成功标准

满足以下条件即视为一期成功：

1. 每个 provider / endpoint / 最终上游模型都能独立形成状态文件。
2. 删除文件或目录后，对应 bucket 可自动冷启动重建。
3. estimator 状态写入不会触发配置热加载。
4. 每个 bucket 至少能展示：
   - sample_count
   - avg/base estimate
   - avg upstream input tokens
   - avg cached tokens
   - avg uncached tokens
   - suggested correction
   - runtime readiness=false
5. 一期结束时，系统已具备为二期 runtime correction 提供稳定样本的基础。

## 发布与交付要求

本 spec 对应的后续实现、测试、提交、发版和部署必须遵循本项目既有“老规矩”：

1. 功能完成后先跑针对性测试、全量测试和编译验证。
2. 使用中文 semantic commit。
3. 按版本创建 release，并补完整发布说明。
4. 服务器拉取最新代码后执行正式部署脚本。
5. 额外手动验证 `healthz`、systemd、进程、端口和必要日志，不能只看脚本输出。

## 最终建议

本项目动态 token estimator 的最佳路线不是“给每个模型学一个倍率”，而是：

**结构化基础估算器 + provider/endpoint/model 分桶状态 + 保守动态残差修正**

其中：

- 一期只做 persistence + observation
- 二期再引入 bounded runtime correction
- 全程保持可解释、可删除重置、可演化升级

这是当前项目在理论性、工程可控性、管理台可操作性和长期维护成本之间最平衡的方案。
