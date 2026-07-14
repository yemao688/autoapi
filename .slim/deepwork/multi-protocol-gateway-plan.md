# Autoapi 多协议网关规划

## 目标

在保持现有 Chat Completions 与 failover 行为稳定的前提下，规划对市面主流 LLM API 协议的入站兼容、原生上游透传和受控协议转换。重点参考：

- QuantumNous/new-api
- Wei-Shaw/sub2api
- OpenAI 官方 Responses / Chat Completions
- Anthropic Messages
- Google Gemini generateContent

## 当前阶段

Phase 0 执行中：冻结现有行为，补齐 characterization tests。

## 已确认背景

- 当前工作区已实现第一阶段原生 `/v1/responses`，仅路由到显式启用 Responses 能力的 Provider。
- `/v1/chat/completions` 仍是现有稳定入口。
- 当前共享执行层已有模型规则、候选顺序、重试、熔断、首字节前 failover、流提交后禁止 failover、usage 与 request chain。
- Responses、Chat、Anthropic Messages、Gemini 在工具调用、reasoning/thinking、流事件和有状态语义上并非无损等价。
- 目标原则：原生透传优先；转换仅覆盖明确能力子集；不支持能力明确报错，不静默降级。

## Autoapi 本地架构调研结论

来源：`exp-2`，关键代码集中于 `internal/proxy/proxy.go`、`handlers.go`、`matcher.go`、`streamusage.go`、`usage.go`。

- 可直接复用：认证、Model Rule、候选规划、重试、熔断、首字节预算、提交前 failover、提交后禁止 failover、attempt chain、metrics。
- 当前关键假设：`forwardWithFailover` 使用入站 `r.URL.Path` 作为上游路径；每个候选只通过 `rewriteBodyModel` 修改 JSON。协议转换后该假设不成立。
- 自然接缝 A：入口 handler 解析请求并生成协议元数据与能力要求。
- 自然接缝 B：每个 candidate attempt 发出前决定上游协议、路径和请求转换；响应写回客户端前进行非流式或事件级转换。
- 当前 Provider capability 只有扁平布尔 `ResponsesEnabled`，不足以扩展 Messages、Gemini、Chat 及各类功能能力。
- `streamUsageAccumulator` 已混合 OpenAI/Responses/Anthropic usage 旁路解析，但不是可承担双向协议转换的完整 SSE 状态机。
- 不应为每个协议复制 `forwardWithFailover`；应让 candidate 携带 client protocol、upstream protocol、translation mode、upstream path，并复用公共执行器。
- 预计新增独立 adapter/codec 包，而不是继续把转换堆入 `handlers.go` 或 `proxy.go`。

## new-api 调研结论

来源：`lib-2`，关键参考为 `types/relay_format.go`、`service/request_converter.go`、`service/relayconvert/`、`relay/common/relay_info.go`、各 channel adaptor。

- 支持 OpenAI Chat、Responses、Anthropic Messages、Gemini GenerateContent 等入口，并通过 typed conversion graph 组合转换。
- 不是一个万能 IR：保留各协议强类型 DTO，以 `GeneralOpenAIRequest` 作为常用 pivot，同时 Responses/Claude/Gemini 仍有专属 DTO。
- `RelayInfo` 显式记录入口协议、转换链、最终上游协议、stream 状态和 billing 请求语义；这对日志、debug、usage 和计费非常有价值。
- 转换逻辑正从 provider adaptor 集中到 `service/relayconvert`；provider adaptor 主要负责 URL、header、HTTP/SSE 和供应商特例。
- Responses → Claude/Gemini 常采用 Responses → Chat → 目标协议的 pivot，工程复用高但会累积语义损失，因此必须有 capability/loss 检查。
- 流式 Chat → Responses、Chat ↔ Claude 均需要显式状态机，维护 item/block index、工具参数缓冲、reasoning 状态、usage 和唯一 terminal event。
- 客户端 usage、上游原始 usage、billing usage 分离，避免转换后的响应 usage 被误用于真实上游成本。
- 对 `previous_response_id`、conversation 等 stateful Responses 字段，在降级到 Chat 时明确拒绝；原生 Responses 上游可以透传。
- Gemini thinking、thought signature、Claude cache control 等 provider-specific 语义保留在 adaptor/policy 层，不强行抹平。
- 不宜照搬：分散式 capability、过多 ChannelType/RelayMode/model-name 分支、Advanced Custom 任意路由、以 OpenAI Chat DTO 作为永久万能 IR。
- 可借鉴的核心：协议枚举与 conversion trace、独立转换 registry、独立 stream state machine、client/provider/billing usage 分离、不可转换能力的 preflight rejection。

## sub2api 调研结论

来源：`lib-3`，关键参考为 `backend/internal/pkg/apicompat/`、gateway handlers/services、OpenAI capability/scheduler、thinking protocol。

- 支持 OpenAI Chat/Responses、Anthropic Messages、Gemini native、Codex/Responses WS 等多种入口和上游组合。
- 协议与平台/账号能力分开，调度同时考虑模型、协议、transport、previous response ownership、sticky、负载和失败排除。
- 常以 Responses 作为 Chat/Anthropic 之间的高保真 pivot，避免 Chat 过早丢失 thinking、cache、tool lifecycle；但仍保留各协议专属类型和 provider extension。
- 流式转换是显式有状态对象，维护 block/item、tool arguments、role、usage、stop reason 和唯一 terminal。
- thinking 协议必须按映射后的上游模型/能力判断，而不是客户端模型名；支持 strict/passback/unknown 等策略。
- `previous_response_id` 通过 response ownership 和 sticky account 约束调度；这比无约束 failover 安全，但引入状态存储。
- 非流式有时通过聚合上游 SSE 构建 JSON，复用事件解析但增加内存、延迟与 buffer 风险。
- 不宜照搬：官方 CLI 指纹模拟、复杂 Responses WebSocket v2/Redis lease、模型名前缀硬编码、同一路径按 platform 隐式改变过多语义。
- 可借鉴：能力驱动候选过滤、协议与 provider 解耦、mapped upstream model 参与 reasoning/thinking policy、sticky/failover 边界、消息时序不变量测试。

## 两个参考项目的交叉结论

- 两者都证明流式协议转换不能是逐行 JSON 替换，必须是状态机。
- 两者都保留协议专属 DTO；不存在真正覆盖所有语义的单一万能 IR。
- `new-api` 更适合借鉴 conversion registry/trace 与 client-provider-billing usage 分离。
- `sub2api` 更适合借鉴 Responses 高保真 pivot、capability scheduler、thinking policy 和 state affinity。
- Autoapi 应控制范围：先 HTTP JSON/SSE、公开 API key 上游、无状态转换；暂不做 OAuth 客户端伪装、WS v2、任意路由脚本。

## 目标架构草案 v1

### 协议族

- `openai_chat`
- `openai_responses`
- `anthropic_messages`
- `gemini_generate_content`

媒体、embeddings、audio、images 暂作为独立 endpoint family，不纳入第一版 text conversion graph。

### 核心对象

- `RequestEnvelope`: 入站协议、endpoint、公开模型、stream、raw body、所需能力、状态性标记。
- `CandidatePlan`: Provider/Target、映射后模型、上游协议/path、conversion path、能力证明、loss profile。
- `ConversionTrace`: inbound、转换边、最终 upstream、降级/拒绝原因。
- `UsageReport`: client usage、provider usage、billing/normalized usage、来源与完整性。
- `StreamResult`: terminal state、usage、上游错误、是否已提交客户端。

### 分层

1. Ingress codec：协议专属解析、校验、错误 envelope。
2. Feature inspector：从请求提取 tools/vision/reasoning/state/structured output 等要求。
3. Capability resolver：Provider 默认 + Target override + mapped model policy + converter capability 的交集。
4. Conversion registry：有向边及其支持能力/损失声明；原生边成本最低。
5. Shared executor：复用现有 retry/failover/breaker，但 attempt 使用 CandidatePlan 决定 URL、body、header 和 response pipeline。
6. Stream codec/state machine：解析上游事件并编码客户端事件；commit 后禁止 failover。
7. Usage/error/log：协议语义化提取并记录 conversion trace。

### 初始转换图建议

- 原生透传：四协议各自到同协议。
- 第一转换核心：Anthropic Messages ↔ OpenAI Responses。
- OpenAI Chat ↔ OpenAI Responses。
- Gemini ↔ Responses 延后；Gemini 特有 thought signature 作为 provider policy。
- 多跳允许但必须计算 capability/loss；默认最多两条转换边，避免隐式复杂链。

### 第一版拒绝/限制

- 非原生上游不接受 `previous_response_id`、conversation、background、hosted tools、remote MCP、computer use。
- reasoning/thinking 仅在转换器明确声明支持时进入候选。
- structured output strict、custom/free-form tools、provider cache metadata 不宣称无损。
- 已提交任意客户端事件后不得 failover。
- 不做官方 CLI/OAuth 指纹模拟，不做 Responses WS v2。

## 计划草案 v1

1. 基础重构：协议枚举、CandidatePlan、capability 表、conversion trace；锁定现有 Chat/Responses 行为。
2. 提取公共 executor seam：上游 path/body/header/response codec 由 plan 决定，不复制 failover。
3. Anthropic Messages 原生入口与原生上游透传，含 headers、JSON/SSE、usage/error。
4. Anthropic Messages ↔ Responses 非流式转换，覆盖 text/system/tools/tool result/basic reasoning；golden fixtures。
5. 同方向流式状态机与 contract tests。
6. Chat ↔ Responses 非流式与流式转换，复用 Responses pivot。
7. Gemini 原生入口/透传，再按需求实现 Gemini ↔ Responses。
8. 有状态 affinity 单独立项：response ID ownership、sticky、生命周期 endpoints。

## 正在进行的调研

- `lib-2`: new-api 多协议及 adaptor 架构
- `lib-3`: sub2api 多协议及转换架构（首次返回为空，待复用会话补查）
- `exp-2`: 已完成并纳入上述本地架构结论

## 待决策

- 第一批正式支持的协议矩阵。
- 内部是否采用协议中立 IR，若采用，其最小信息模型与 loss reporting。
- Provider/Target capability 的粒度和存储结构。
- 流式转换器接口、事件状态机与 commit/failover 边界。
- 有状态 ID、prompt caching、thinking/reasoning、hosted tools 的明确边界。
- 分阶段实现、测试契约和兼容性声明。

## 审查门

1. 调研结果汇总并形成方案草案。
2. Oracle 审查目标架构。
3. 修订后形成分阶段实施计划。
4. Oracle 审查实施计划后再进入编码。

## Oracle 架构裁决（已接受）

- 不把 Responses 作为全协议万能 IR；采用协议专属 DTO + 最小、能力受限的 semantic IR。
- Responses 是 OpenAI Chat/Responses 的高保真 pivot，也可作为 Messages↔Responses 的明确单跳目标，但 Gemini 不默认经 Responses 多跳。
- 第一版转换 registry 为编译期静态表；只允许原生或单跳，不做图搜索、任意用户转换链或动态插件。
- 现有 `routing.CandidatePlan` 保持路由排序职责；新增 proxy 私有 `AttemptPreparation`/`AttemptSpec` 承担 path/body/header/codec/conversion trace。
- capability 分为协议能力和功能能力；未知协议能力默认不支持，高风险功能未知时默认拒绝。
- 原生流继续由 usage accumulator 旁路观察；转换流使用独立 parser → semantic event → encoder 状态机。
- client usage、provider usage、billing usage 三分；转换后的 client usage 不能覆盖真实 provider usage。
- `/v1/messages` 是第一批新增协议，但先原生透传，再做非流式转换，最后才做流式转换。
- Gemini 先原生透传，跨协议转换另行评估。

## 最终支持矩阵与实施顺序

### Phase 0：冻结现有兼容契约

- 固化 Chat/Responses 原生 JSON、SSE、failover、breaker、usage、chain、旧 DB 升级测试。
- 不引入任何转换。

### Phase 1：最小 executor seam

- 引入 `Protocol`、`AttemptPreparation`、静态 native adapter 和 conversion trace。
- 仅替换每次 attempt 的 path/body/header/response codec 决策；不改候选排序、重试、熔断和首字节预算。
- native Chat/Responses 可通过 feature flag 保留旧路径以便回滚。

### Phase 2：Capability + Anthropic Messages 原生入口

- 新增 `/v1/messages` 原生 JSON/SSE、Anthropic headers、usage/error codec。
- 新增可扩展 provider capability 表，兼容现有 `responses_enabled`。
- 没有 Messages native capability 时在上游请求前拒绝，不自动改发 Chat/Responses。

### Phase 3：非流式 conversion registry

- 协议专属 DTO + 最小 semantic IR + feature inspector。
- 第一组：Messages ↔ Responses 非流式。
- 第二组：Chat ↔ Responses 非流式。
- 只支持 text/system/basic image/basic function tools/tool result/basic reasoning/usage。
- stateful、hosted tools、strict structured output、cache/thought signature 等不能保留时 preflight reject。

### Phase 4：流式转换状态机

- 顺序建议：Responses→Chat、Chat→Responses、Messages→Responses、Responses→Messages。
- 独立 SSE parser、semantic stream event、客户端 encoder。
- terminal exactly once、tool args 重组、usage 单次、commit 后禁止 failover、断连取消、backpressure、buffer 上限均为发布阻断项。

### Phase 5：Gemini 原生入口

- 支持 `generateContent`、`streamGenerateContent`、basic text/image/function calling、原生 usage/error。
- 不隐式转换到其他协议。

### Phase 6：Gemini 转换评估

- 只有真实需求明确后评估 Gemini ↔ Responses。
- Gemini ↔ Messages、Gemini ↔ Chat 和任意多跳不在当前承诺内。

## 第一版明确排除

- Responses WebSocket v2、官方 CLI/OAuth 指纹模拟。
- background、conversation、previous_response_id 的跨协议降级和状态 ownership。
- hosted tools、remote MCP、computer use。
- arbitrary custom/free-form tools 的无损承诺。
- strict structured output 的跨协议无损承诺。
- prompt/cache metadata、Anthropic signature、Gemini thought signature 的通用转换。
- 任意多跳转换、用户自定义转换图、动态插件。
- 把 embeddings/images/audio/media endpoints 纳入 text conversion graph。

## 回滚设计

- native endpoint 与 translated path 分别 feature flag 控制。
- 关闭 conversion 后仍保留 Chat、Responses、Messages/Gemini native passthrough。
- capability 新表为附加结构，读取失败可回退旧 `responses_enabled` 和 Chat 默认行为。
- executor seam 过渡期保留原 native 路径，稳定后再清理。

## Model 级 Capability 设计

### 问题

同一 Provider 下不同模型的能力不同。例如：

- Provider "OpenAI 官方" 下，`gpt-4o` 支持 vision，`gpt-4` 不支持。
- Provider "Anthropic" 下，`claude-sonnet-4` 支持 extended thinking，`claude-3-haiku` 不支持。
- Provider "自定义网关" 下，某个模型支持 tools，另一个不支持。

如果只看 Provider 级能力，会导致：向不支持某能力的模型发送请求并失败，或者过度限制实际支持的模型。

### 能力分层

```text
Provider capability          （Provider 级默认）
  ↓ intersect
Model capability             （同 Provider 下模型级覆盖）
  ↓ intersect
Converter capability         （转换器能处理的功能子集）
  ↓ intersect
Request requirements         （当前请求需要的特性）
  =
Effective capability         （是否该候选可接受此请求）
```

Model capability 可以覆盖 Provider 默认值：
- Provider 默认 `tools = true`，某模型可以标记 `tools = false`。
- Provider 默认 `vision = unknown`，某模型可以标记 `vision = true`。
- Provider 未配置，模型也未配置时，沿用 Provider 默认（通常 unknown → 按风险策略处理）。

### 数据模型

```sql
CREATE TABLE model_capabilities (
    provider_id TEXT NOT NULL,
    model_name  TEXT NOT NULL,
    feature     TEXT NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    source      TEXT NOT NULL DEFAULT 'manual',
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (provider_id, model_name, feature)
);
```

与 `provider_capabilities` 的关系：
- `provider_capabilities` 回答："这个 Provider 支持哪些协议和默认功能？"
- `model_capabilities` 回答："这个 Provider 下某个具体模型是否支持某个功能？"
- 两者取交集；model 级可以收紧（enabled=0），也可以扩展（enabled=1）。

### 管理策略

- **默认来源**：用户手动配置或从上游 `/v1/models` 探测填充。
- **builtin**：可以为常见 Provider 预置已知模型能力表（如 OpenAI 官方模型），随版本更新。
- **probe**：未来可通过测试请求自动探测，但第一版不做。
- **未配置时**：fallback 到 Provider 级；Provider 也未配置时按风险策略处理（普通文本允许，tools/reasoning/structured_output 默认拒绝）。

### 前端交互

- Provider 编辑页面：展示 Provider 级协议开关（Chat/Responses/Messages/Gemini）。
- 模型列表区域：每个模型可以展开标记功能能力（tools/vision/thinking/stream/structured_output）。
- 未标记的模型继承 Provider 默认。
- Model Rule 的 Target 配置不需要重复声明模型能力；模型能力属于 Provider+Model 维度。

### 实施时机

- Phase 2 与 `provider_capabilities` 同步引入 `model_capabilities` 表。
- 初始可为空表，所有模型继承 Provider 默认。
- 前端 UI 可在 Phase 2 做最简版本（per-model 开关），或推迟到 Phase 3 按需补充。
- 不影响 capability resolver 接口设计；resolver 从启动时就按三层交集计算。

## 计划调整记录

- 2026-07-14：将 model 级 capability 从"以后再说"提前到 Phase 2。理由：同一 Provider 下不同模型在第一版就会遇到能力差异（vision、thinking、tools 等），如果 capability resolver 不包含 model 级层，会导致 Phase 3 转换时无法正确过滤候选目标。
- 2026-07-14：Phase 3 按 Oracle 建议缩减为 Messages ↔ Responses 非流式点对点转换；暂不引入 semantic IR、feature inspector、通用 registry 或 Chat 转换。
- 2026-07-14：Phase 3 提交前审计发现 Responses `function_call` 必须使用 `arguments` JSON 字符串而非 `input` 对象；已要求修正双向工具映射、非法参数拒绝、conversion error 记账和流式转换 preflight 测试。
- 2026-07-14：Phase 4 双向流式转换实现后经 Oracle 审查未通过；已修复 terminal guard、active block close、call_id/block index、failed/incomplete 非成功语义和 post-commit provider accounting。剩余发布阻断：转换流 first-visible-event deadline，以及独立 pre/post-commit conversion E2E。
- 2026-07-14：Phase 4 最终 gate PASS（commit 76a9e1e）。已验证 pre-commit 单次计数、partial-byte open-connection deadline failover、Messages→Responses 单 Provider success E2E、原生 Responses streaming 回归。
- 2026-07-14：进入剩余范围评审。待评估：model-level capability resolver、Chat↔Responses 转换、Gemini 跨协议转换；未通过架构 gate 前不扩大转换矩阵。
- 2026-07-14：Phase 6 裁决通过，实施范围锁定为 A→B→C：统一 capability 真相源与 model override、Chat↔Responses 非流式、Chat↔Responses 流式。Gemini conversion 本轮明确延期。
- 2026-07-15：Phase 7.1 PASS（705fd1f + 5cc54b5）。Provider capability 统一为 manual row 权威 + legacy bool fallback；matcher 使用 request-scoped bulk snapshot；ProtocolUnknown 安全限制；Chat 默认兼容保持。
- 2026-07-15：Phase 7.2 PASS（3972341 + 53fea6d）。Model-level capability overrides 支持 provider/model/protocol/feature 四维；migration 027 建表、028 复合 Model FK；bulk 查询分块避免 SQLite 参数上限；model rename/delete cascade。
- 2026-07-15：Phase 7.3 PASS（3a76448 + 44d3752 + 58ed89c）。Request feature inspector 识别 9 canonical features；observe/enforce staged enforcement；native 与 conversion candidate 按 feature capability + converter preservation 双 gate；unsupported feature=422、真正不可用=503；response converter fail-closed 不再静默丢弃 reasoning/refusal/unknown；Provider feature API 校验 + delete inherit + migration 029 FK。
- 2026-07-15：Phase 7.4 PASS（990e38b + 6a37007）。Chat↔Responses 非流式转换；静态 conversion edge registry 按 target order 优先、同 target edge priority；assistant output_text、call_id≠item id、tool output string-only、strict 保留、usage details 映射、content_filter 双向、response allowed-key fail-closed、多 function_call 合并一个 assistant message、交错序列 fail-closed。
- 2026-07-15：Phase 7.5 PASS（a06dbf5 + f060ad9）。Chat↔Responses 流式转换；双向 SSE converter 复用 drain/processEvent 状态机和 streamUsageAccumulator；terminal guard 与 failover 边界正确；缺 [DONE]/缺 terminal 返回 conversion error 不合成成功；多 message/混合 item fail-closed；tool-call ID 一致性校验；call_id 不可 fallback 到 item id。
