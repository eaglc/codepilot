# CodePilot 模块化架构与迁移计划

逐项实施和完成标记见 [产品补全路线图](../roadmap/product-completion-roadmap.md)。

状态：实施中；通用核心、可恢复写入、单栏 TUI 和 CLI 运行链路已落地，当前进入发布与跨平台质量阶段
适用范围：`internal` 重构、Agent Loop、LLM/Provider、工具、上下文、会话、Workspace/Worktree、持久化、恢复和 TUI 事件
约束：本文依据项目源码形成；迁移期间旧实现完整保存在 `_legacy/internal`，不作为新架构的运行时依赖

## 1. 项目主要目标

CodePilot 是一个本地优先、可恢复、可审计的 Coding Agent。它应当能够：

1. 在受信任的 Git workspace/worktree 中读取、搜索、修改和验证代码。
2. 支持多个模型供应商，同时避免 Agent、Session、Tool 和 UI 依赖具体供应商 SDK。
3. 完整保存用户消息、assistant 消息、tool call、tool result、模型和 usage 信息。
4. 对长会话进行可持久化、可追踪、可复用的上下文压缩。
5. 在进程中断后判断任务执行到了哪里，并在安全规则允许时恢复。
6. 将权限、审批、路径约束和命令执行限制放在可信边界中，不能由模型输入绕过。
7. 让通用 LLM、Provider、Tool、Agent、Context 和 Agent Session 模块不认识 Git、Workspace、LSP、Patch 或 TUI。
8. 让 Coding Agent 作为最外层业务模块，通过依赖注入组合底层通用能力。
9. 对外提供稳定的产品事件，不向 TUI 泄漏 LLM SDK、Provider 或 Agent Runtime 的原始流事件。

## 2. 非目标

当前迁移不以以下事项为目标：

- 不拆分成多个 Go module；项目继续使用一个 `go.mod`。
- 不在没有实际需求前实现远程 client/server/protocol。
- 不要求旧 session 文件立即原地升级；可以先提供显式、可测试的迁移器。
- 不让工具自行写 activity、session 或 UI event。
- 不把 Eino 类型作为公共接口暴露给 `llm`、`agent`、`codingagent` 或 TUI。
- 不为了目录对称创建没有行为的 interface 或空 package。

## 3. 总体设计原则

### 3.1 稳定协议在内，易变实现向外

`llm` 定义模型无关协议，Provider 实现该协议；`agent/session` 定义存储契约，File/SQLite backend 实现该契约；`codingagent` 定义产品需要的能力接口，Workspace、LSP、Approval 实现这些能力。

### 3.2 机制与业务策略分离

- `agent` 负责模型与工具循环、step、重试、中断、journal 和恢复。
- `codingagent` 负责 Coding prompt、Git/worktree、语言、LSP、权限策略和具体工具。
- 通用 Agent 不出现 `GitStatus`、`PatchRecord`、`WorktreeID` 或 `PermissionMode`。

### 3.3 数据所有权唯一

- 模型消息协议由 `llm` 拥有。
- 工具执行协议由 `tool` 拥有。
- durable Entry/Record 由 `agent/session` 拥有。
- Workspace/Worktree 和 Coding 产品 session 绑定由 `codingagent` 拥有。
- UI 展示状态由 UI 自己维护，但其权威输入只能是 `codingagent.Snapshot` 和 `codingagent.Event`。

### 3.4 接口定义在使用方

遵循 Go 的 consumer-owned interface 原则。例如：

- `agent` 定义自己需要的 `ModelFactory`、`Journal` 和 `ToolExecutor`。
- `contextmanager` 定义自己需要的 `Summarizer`。
- `codingagent` 定义自己需要的 Workspace、ModelCatalog、Approval 和 Store 接口。
- 具体实现位于 Provider、SessionStore、Workspace、Credential 等适配包。

### 3.5 事件、持久化记录和快照分离

- Event：用于运行时观察，可以丢失，不作为恢复的唯一依据。
- Entry：进入对话树并可能参与模型上下文。
- Record：记录 operation、step、tool、approval、usage 等执行事实。
- Snapshot：由 durable 数据投影得到，是客户端和 TUI 的权威状态。

## 4. 目标目录结构

```text
codepilot/
├── cmd/
│   └── codepilot/
│       └── main.go
├── internal/
│   ├── llm/
│   │   ├── content.go
│   │   ├── message.go
│   │   ├── model.go
│   │   ├── request.go
│   │   ├── response.go
│   │   ├── stream.go
│   │   ├── tool.go
│   │   ├── usage.go
│   │   ├── client.go
│   │   └── errors.go
│   ├── provider/
│   │   ├── adapter.go
│   │   ├── service.go
│   │   ├── profile.go
│   │   ├── catalog.go
│   │   ├── credential.go
│   │   ├── repository.go
│   │   ├── openai/
│   │   ├── deepseek/
│   │   └── ollama/
│   ├── credential/
│   ├── tool/
│   │   ├── tool.go
│   │   ├── call.go
│   │   ├── result.go
│   │   ├── interrupt.go
│   │   ├── registry.go
│   │   └── middleware.go
│   ├── contextmanager/
│   │   ├── manager.go
│   │   ├── request.go
│   │   ├── policy.go
│   │   ├── tokenizer.go
│   │   ├── compaction.go
│   │   ├── summarizer.go
│   │   └── summary.go
│   ├── agent/
│   │   ├── agent.go
│   │   ├── loop.go
│   │   ├── request.go
│   │   ├── result.go
│   │   ├── step.go
│   │   ├── state.go
│   │   ├── event.go
│   │   ├── toolrunner.go
│   │   ├── recovery.go
│   │   ├── ports.go
│   │   ├── session/
│   │   │   ├── session.go
│   │   │   ├── entry.go
│   │   │   ├── record.go
│   │   │   ├── turn.go
│   │   │   ├── branch.go
│   │   │   ├── compaction.go
│   │   │   ├── context.go
│   │   │   ├── repository.go
│   │   │   ├── journal.go
│   │   │   └── recovery.go
│   │   └── eino/
│   │       ├── runtime.go
│   │       ├── model.go
│   │       ├── tool.go
│   │       ├── checkpoint.go
│   │       └── events.go
│   ├── sessionstore/
│   │   ├── file/
│   │   └── sqlite/
│   ├── codingagent/
│   │   ├── agent.go
│   │   ├── factory.go
│   │   ├── service.go
│   │   ├── session.go
│   │   ├── request.go
│   │   ├── result.go
│   │   ├── event.go
│   │   ├── config.go
│   │   ├── prompt/
│   │   ├── workspace/
│   │   ├── language/
│   │   ├── lsp/
│   │   ├── approval/
│   │   └── tools/
│   ├── codingstore/
│   │   ├── file/
│   │   └── memory/
│   ├── ui/
│   └── app/
└── _legacy/
    └── internal/
```

目录是目标边界，不要求第一步创建全部空文件。只有当一个 package 获得可测试的职责时才创建它。

## 5. 模块职责摘要

### 5.1 `llm`

模型无关的数据和调用协议。只允许依赖标准库。

拥有：

- `Message`、`ContentBlock`、`UserMessage`、`AssistantMessage`、`ToolResultMessage`
- `ToolDefinition`、`ToolCall`
- `ModelRef`、`ModelMetadata`、`Usage`、`StopReason`
- `ChatRequest`、`ChatResponse`、`StreamEvent`
- `ChatModel` 或最小 `ModelFactory` 接口

不拥有：工具执行、Provider profile、凭证、session、重试策略、workspace 或产品事件。

### 5.2 `provider`

负责模型供应商适配、profile、catalog、凭证引用和模型构造。Provider 子包把厂商 SDK 数据显式转换为 `llm` 数据。

根 `provider` 包不得导入具体子包；由 `app` 构造 OpenAI、DeepSeek、Ollama adapter 后注册，避免父子 package 循环。

### 5.3 `tool`

拥有可执行工具协议、Registry、Result、Interrupt 和执行中间件。`llm.ToolDefinition` 是模型看到的声明；`tool.Tool` 是 Agent 可以执行的能力，两者不能混为一个接口。

工具实现不保存 activity。Agent ToolRunner 在执行前后统一写 Record 和发布 Agent Event。

### 5.4 `contextmanager`

负责上下文选择、token 估算、裁剪、摘要和 compaction。它接收模型无关消息和稳定边界 ID，通过 `Summarizer` 接口请求摘要，不依赖具体 Provider。

### 5.5 `agent`

通用 Agent Runtime。一次 turn 是一次用户触发的完整运行；一个 turn 可以包含多个 model step 和多个 tool execution。

负责：

- turn/run 状态机
- model step 循环
- tool call 调度
- tool activity 生成
- retry、abort、interrupt、resume
- 将 LLM 事件归一化为 Agent Event
- 在不可逆动作前后写 journal

不负责 Coding prompt、文件系统、Git、LSP、Patch 或具体权限类型。

### 5.6 `agent/session`

通用、可持久化的 Agent Session。它定义 Entry、Record、branch、lane、compaction 和 repository 契约。

它不知道 Coding workspace，但必须保存足以重建模型上下文和判断恢复策略的数据。

### 5.7 `sessionstore`

实现 `agent/session` 的 Repository、Journal 和 CheckpointStore。File backend 使用版本化 JSON/JSONL 与原子写；App 组合根在打开这些 writer 前获取 State root 跨进程 lease，SQLite backend 后续使用数据库事务和 writer lease。

### 5.8 `codingagent`

最终业务模块。它把通用 Agent 转换成 Coding Agent，拥有：

- Coding system prompt
- Workspace/Worktree
- Git、文件、搜索、Patch、命令、检查
- Language 与 LSP
- Approval 与 PermissionMode
- Coding session 元数据
- Coding 产品事件和 snapshot

### 5.9 `ui`

TUI 展示适配器，只依赖 `codingagent` 的公开 service、snapshot 和 event。不得 import `llm`、Provider SDK、Eino、`agent/eino` 或 SessionStore DTO。

### 5.10 `app`

唯一组合根。负责创建具体 Provider、Store、Credential、Agent Runtime、Coding Agent 和 UI，并负责生命周期关闭顺序。

## 6. 模块依赖关系

箭头表示左侧依赖右侧：

```text
provider ───────────────▶ llm
credential ─────────────▶ provider
tool ───────────────────▶ llm
contextmanager ─────────▶ llm
agent/session ──────────▶ llm

agent ──────────────────▶ llm
agent ──────────────────▶ tool
agent ──────────────────▶ contextmanager
agent ──────────────────▶ agent/session
agent/eino ─────────────▶ agent + llm + tool

sessionstore ───────────▶ agent/session

codingagent ────────────▶ agent + agent/session + llm + tool
codingagent ────────────▶ provider 的抽象服务
codingagent/tools ──────▶ tool + codingagent/workspace/lsp/language/approval

ui ─────────────────────▶ codingagent
app ────────────────────▶ 所有具体实现
cmd ────────────────────▶ app
```

禁止的依赖：

```text
llm            -X-> provider/agent/codingagent/ui
provider       -X-> agent/codingagent/ui
tool           -X-> codingagent/workspace/ui/sessionstore
contextmanager -X-> provider/codingagent/ui
agent          -X-> codingagent/workspace/lsp/ui
agent/session  -X-> codingagent/ui/sessionstore
codingagent    -X-> ui/app
ui             -X-> llm/provider/eino/sessionstore
```

## 7. 消息、Entry 与 Record

### 7.1 LLM 消息

模型消息必须支持：

- User：text、image。
- Assistant：text、thinking、tool call。
- ToolResult：tool call ID、tool name、text/image、details、usage、isError。
- Assistant 元数据：provider、model、response model、usage、stop reason、timestamp。

Provider 私有 replay/signature 字段可以在 `llm` 内容块上以明确、受控字段保存，但不得原样把 SDK 对象持久化。

### 7.2 Context Entry

Entry 位于对话树中，可能参与后续模型上下文：

- `message`
- `model_change`
- `thinking_level_change`
- `active_tools_change`
- `compaction`
- `branch_summary`
- `custom_message`

每个 Entry 至少拥有：`ID`、`Sequence`、`ParentID`、`Timestamp` 和类型。

### 7.3 Operation Record

Record 不一定进入模型上下文，但用于恢复、审计和 usage 统计：

- `operation_started`
- `abort_requested`
- `operation_finished`
- `step_started`
- `step_finished`
- `tool_started`
- `tool_finished`
- `approval_requested`
- `approval_resolved`
- `queue_enqueued`
- `queue_cancelled`
- `checkpoint_saved`
- `usage`

`tool_started` 至少保存：run ID、assistant entry ID、tool call ID、tool name、有效参数、预留 result entry ID 和 replay policy。

`tool_finished` 至少保存：对应 tool call ID、状态、是否错误、result entry ID、usage 和安全摘要。

## 8. 上下文与摘要设计

### 8.1 摘要输入

摘要输入必须包含会影响后续推理的历史：

- 用户消息
- assistant 文本和关键 thinking 信息
- assistant tool call 的名称、参数和 ID
- tool result 的有效内容、错误和截断标记
- 已存在的 compaction summary
- 模型变更、重要约束、文件变化和未解决事项

不输入纯展示进度、重复 delta、spinner、UI 状态和内部堆栈。

### 8.2 CompactionEntry

持久化摘要至少保存：

- summary 文本
- 覆盖的起止 Entry ID/Sequence
- retained tail 的起点
- source digest
- strategy name/version
- 摘要模型引用
- token usage
- 创建时间
- 可选结构化 details

摘要内容应保持 provider-neutral。切换主模型时默认复用已有摘要，不因为生成摘要的模型不同而强制重算。只有以下情况重新生成：

- strategy/schema 版本不兼容
- 摘要覆盖范围与历史不一致
- source digest 校验失败
- 摘要缺少当前策略要求的关键结构
- 用户明确要求重算

摘要生成模型只用于审计，不作为摘要复用 key 的必要组成。

### 8.3 上下文投影

构建模型上下文时：

1. 沿当前 branch 从 leaf 回溯。
2. 找到最新有效 compaction。
3. 将 summary 投影为受控的上下文前缀。
4. 拼接 retained tail 的完整消息，其中包括 tool call 和 tool result。
5. 应用 Provider 兼容转换。
6. 最后执行 hard limit 防护。

## 9. 事件层级与隔离

事件必须分三层。层与层之间使用显式 adapter 复制允许字段，不允许类型别名直接透传。

```text
Provider SDK event
        │
        ▼ 显式转换
llm.StreamEvent
        │
        ▼ Agent 聚合、校验、归一化
agent.Event
        │
        ▼ Coding 语义映射、脱敏、限长
codingagent.Event
        │
        ▼ UI bridge
TUI Message / ViewModel
```

### 9.1 LLM Stream Event

仅供 Provider 与 Agent Runtime 使用：

- `response_started`
- `content_started`
- `text_delta`
- `thinking_delta`
- `tool_call_delta`
- `content_finished`
- `usage_updated`
- `response_finished`
- `response_failed`

字段可以包含 provider/model、response ID、content index、原始 stop reason 和签名信息。该层事件不得进入 TUI。

### 9.2 Agent Runtime Event

描述通用 Agent 行为：

- `run_started`
- `step_started`
- `assistant_text_delta`
- `assistant_thinking_changed`
- `tool_started`
- `tool_progress`
- `tool_finished`
- `compaction_started`
- `compaction_finished`
- `retry_scheduled`
- `run_interrupted`
- `run_resumed`
- `run_aborting`
- `run_finished`
- `run_failed`

Agent Event 不暴露 Provider SDK 对象。`assistant_text_delta` 必须经过 UTF-8 校验和批量合并；tool event 使用稳定的 call ID、name、status 和安全 details。

### 9.3 Coding Agent Product Event

这是 `codingagent` 对 UI、CLI、RPC 等展示层提供的唯一事件协议：

- `session_activated`
- `session_updated`
- `turn_started`
- `assistant_output_delta`
- `assistant_output_finished`
- `tool_activity_started`
- `tool_activity_updated`
- `tool_activity_finished`
- `approval_requested`
- `approval_resolved`
- `patch_applied`
- `workspace_changed`
- `diff_changed`
- `context_compaction_started`
- `context_compaction_finished`
- `turn_interrupted`
- `turn_resumed`
- `turn_completed`
- `turn_cancelled`
- `turn_failed`
- `persistence_warning`

统一 Event envelope 至少包含：

- Event ID
- 单调 Sequence
- Snapshot Revision
- Session ID
- Turn ID，可选
- Timestamp
- Kind
- 对应的强类型 payload

产品事件不得携带：

- Provider SDK 对象
- Eino event/checkpoint
- 原始凭证、header 或请求体
- 未截断的命令输出
- Workspace 绝对路径，除非明确属于受信任的本地-only UI 字段
- 原始 thinking 内容；默认只暴露 thinking 状态，是否展示内容由明确产品策略决定

### 9.4 TUI 消费规则

- TUI 只 import `codingagent`，不 import `llm`、`provider`、`agent/eino` 或 storage DTO。
- 首次 attach/activate 必须先读取 `codingagent.Snapshot`。
- Event 是增量提示，Snapshot 是权威状态。
- 如果 Sequence 跳号或 Revision 不连续，TUI 重新读取 Snapshot，而不是猜测缺失状态。
- UI 自己负责 delta 合并和展示节流，但不改变业务状态。
- UI 不能依据显示文本推断 tool 成功、turn 完成或 session 是否可恢复。

## 10. Tool Activity 生成原则

Tool 不自行记录 activity。统一流程如下：

```text
Agent 收到 llm.ToolCall
    │
    ├── 校验定义与参数
    ├── 预留 ToolResult Entry ID
    ├── 持久化 tool_started Record
    ├── 发布 agent.tool_started
    ├── 调用 tool.Tool
    ├── 持久化 ToolResult Message Entry
    ├── 持久化 tool_finished Record
    ├── 发布 agent.tool_finished
    └── 将 llm.ToolResultMessage 交给下一 model step
```

Coding Agent adapter 再把 Agent Event 转换为 `tool_activity_*` 产品事件。这样工具实现只需要返回结果，记录、恢复和 UI 都没有侵入工具。

## 11. Workspace、Worktree 与 Session

### 11.1 Workspace

逻辑 Git repository，由 Git common directory 等稳定事实标识。

### 11.2 Worktree

一个具体 checkout。保存 workspace ID、规范化 root、git dir、创建时间和最后使用时间。路径解析、安全检查和命令执行属于 `codingagent/workspace`。

### 11.3 Coding Session

产品级 session 保存：

- CodingSession ID
- AgentSession ID
- Workspace ID
- Worktree ID
- Provider profile ID
- Model ID
- Permission mode
- Base commit
- Title、archive 和时间信息

通用 Agent Session 只保存对话、操作和恢复记录，不保存 Git 或 Worktree 业务字段。

### 11.4 Turn 与 Step

- Turn：一次用户输入触发的完整 Agent 运行，直到完成、失败、取消或中断。
- Step：Turn 内的一次模型请求/响应。
- 一个 Turn 可以包含多个 Step；每个 Step 可以产生零个或多个 ToolCall。

## 12. 恢复模型

恢复不能只依赖最终 user/assistant 消息。启动时根据 Entry、Record 和 checkpoint 重建：

1. 找到已开始但没有 `operation_finished` 的 operation。
2. 找到最后完成的 model step。
3. 检查是否存在 `tool_started` 但没有 `tool_finished`。
4. 根据工具 replay policy 处理：
   - `never`：不自动重放，标记需要用户确认。
   - `safe`：验证前置条件后允许自动重放。
   - `idempotent`：使用相同 idempotency key 重试。
5. 恢复 approval、queue 和 checkpoint。
6. 将恢复结果发布为 Coding Agent product event，并生成新的权威 snapshot revision。

File/SQLite Store 必须保证同一个 session 的 Sequence 单调且 append 原子。工具副作用发生前必须先持久化 `tool_started`。

## 13. 迁移阶段与验收标准

### 阶段 0：冻结设计与基线

任务：

- 保存本文档。
- 记录旧代码测试基线。
- 将旧 `internal` 移到 `_legacy/internal`。
- 新实现不得 import `_legacy/internal`。

验收：旧实现完整存在；Go 默认包扫描不会包含备份目录。

### 阶段 1：LLM 与 Tool 契约

任务：

- 实现 `llm` 消息、内容块、tool call/result、usage 和 stream event。
- 实现 `tool` 执行接口、registry、result、interrupt。
- 提供结构校验和 defensive copy 测试。

验收：没有 Provider SDK 类型出现在公共 API；tool call/result 能无损序列化。

### 阶段 2：Agent Session 与持久化

任务：

- 实现 Entry/Record、branch/lane、sequence 和 repository。
- 实现 memory backend 作为契约测试基准。
- 实现 file backend 与迁移器。
- 持久化 compaction 和 checkpoint。

验收：能够完整保存并重建含 tool call/result 的上下文；尾部损坏有明确 recovery warning。

### 阶段 3：Context Manager

任务：

- 使用完整消息而不是 user/assistant string。
- 摘要包含工具调用与结果。
- 持久化摘要覆盖范围和 strategy version。
- 模型切换默认复用 provider-neutral 摘要。

验收：相同历史在重启和切换主模型后可复用摘要；历史变化会使 digest 失效。

### 阶段 4：通用 Agent Runtime

任务：

- 实现 turn/step/tool loop。
- 由 ToolRunner 集中写 activity。
- 实现 abort、retry、interrupt、resume。
- 建立 LLM Event → Agent Event 转换。

验收：测试覆盖无工具、多工具、工具失败、取消、审批中断、恢复和超限。

### 阶段 5：Provider 与 Eino 适配

任务：

- Provider 实现 `llm` 接口。
- 将 Eino 隔离到 `agent/eino` 或 Provider adapter 内。
- 完成 OpenAI、DeepSeek、Ollama 转换。

验收：`agent` 根包不 import Eino 或具体 Provider；Provider 转换有契约测试。

### 阶段 6：Coding Agent

任务：

- 迁移 prompt、workspace、language、LSP、approval 和工具。
- 建立 Coding session 与通用 Agent session 的绑定。
- 建立 Agent Event → Coding Agent Event 转换。

验收：底层模块不出现 Coding 类型；现有 read/search/patch/check 等行为达到旧实现等价。

### 阶段 7：TUI 与组合根

任务：

- TUI 改为只消费 Coding Agent snapshot/event。
- 迁移 session picker、provider picker、approval 和 diff 展示。
- 更新 `app` 和 `cmd`。

验收：静态依赖检查确认 UI 不 import LLM/Provider SDK/Eino/Store DTO；交互测试通过。

### 阶段 8：恢复与清理

任务：

- 加入崩溃点测试和恢复幂等性测试。
- 文档记录无法自动恢复的情况。
- 功能等价前保留 `_legacy/internal`，不删除用户备份。

验收：`gofmt`、`go vet ./...`、`go test ./...` 通过；恢复测试覆盖真实崩溃边界。

实施状态：崩溃恢复、跨仓库一致性诊断与显式修复均已完成。`_legacy/internal` 继续作为源码重构期间的参考备份保留；删除不属于本阶段授权范围。发布/跨平台质量工作归入 P2-05。

## 14. 测试策略

每层至少包含：

- `llm`：消息校验、JSON round-trip、流事件顺序。
- `provider`：每家 Provider 的输入输出转换 golden test。
- `tool`：注册冲突、参数校验、取消、超时和 interrupt。
- `contextmanager`：turn 边界、tool call/result 摘要、hard limit、摘要失效。
- `agent/session`：sequence、branch、compaction、未完成 operation 恢复。
- `agent`：多 step、多 tool、错误、retry、abort、resume。
- `sessionstore`：原子追加、尾部损坏、锁、并发 writer。
- `codingagent`：安全边界、approval、workspace 绑定和 turn 状态。
- `ui`：只根据产品事件和 snapshot 更新，不解析底层事件。

架构测试应扫描 import，阻止禁止依赖重新出现。

## 15. 迁移期间的兼容规则

- `_legacy/internal` 是只读参考备份，新代码禁止 import。
- 不直接复制旧的共享 DTO；迁移时先确认新数据的所有者。
- 每个阶段必须保持新 `internal` 可编译；未迁移的完整产品入口可以暂时明确返回“功能尚未迁移”，但不能静默丢数据或伪造成功。
- 只有通过等价测试后，CLI 才切换到新实现。
- 删除备份必须由用户单独明确授权。

## 16. 架构完成判定

完成必须同时满足：

1. `llm`、`provider`、`tool`、`contextmanager`、`agent` 不 import `codingagent`。
2. TUI 不 import `llm`、Provider SDK、Eino 或 store DTO。
3. 一次完整 turn 的 user、assistant、tool call、tool result、usage 和 operation record 均可持久化。
4. compaction 可跨进程恢复，并可在切换主模型后复用。
5. 未完成工具调用可以被检测，并依据 replay policy 安全处理。
6. Workspace/Worktree 只存在于 Coding 产品层，不泄漏到通用 Agent。
7. Snapshot 是权威状态，事件丢失可以通过重新加载 snapshot 恢复。
8. 所有静态检查和测试通过。

## 17. 2026-08-23 实施状态

本节记录新 `internal` 的实际状态，不代表目标阶段已经全部完成。旧实现已完整移动到 `_legacy/internal`；新代码有架构测试阻止任何 package 重新引用该目录。

### 17.1 已落地的实际包

```text
internal/
├── llm/                         # 标准消息、内容块、tool call/result、usage、流事件、ChatModel
├── tool/                        # 可执行 Tool、Registry、Result、Interrupt、ReplayPolicy
├── contextmanager/              # 策略管线、turn 安全裁剪、滚动摘要、模型摘要器、摘要缓存
├── agent/
│   ├── runtime.go               # run/step/tool loop、中断与同一 run 恢复、事件转换起点
│   └── session/                 # Entry、Record、lane、memory repository、恢复分析
├── provider/
│   ├── provider.go              # profile/credential 引用、adapter registry、llm.ModelFactory
│   ├── file/                    # 版本化、原子替换的非敏感 Provider profile 配置
│   ├── credential/              # 系统 Keyring、只读环境变量回退和组合 Store
│   ├── openai/                  # OpenAI adapter
│   ├── deepseek/                # DeepSeek adapter
│   ├── ollama/                  # Ollama adapter
│   └── internal/eino/           # Eino 与 llm 协议的唯一转换边界
├── sessionstore/file/           # Agent metadata + JSONL journal + summary cache
├── codingagent/
│   ├── service.go               # 产品 session、turn、resume 组合
│   ├── permission.go            # 细粒度 Grant、过期/撤销与精确匹配
│   ├── event.go                 # Agent Event → Coding Event 白名单转换
│   ├── snapshot.go              # TUI 权威产品快照
│   ├── workspace_manager.go     # 产品级验证、Picker 投影和显式重定位
│   ├── prompt/                  # 可信 Coding system prompt
│   ├── tools/                   # 只读工具、可审批 apply_patch 与安全路径边界
│   ├── workspace/               # 不依赖产品层的 Git 定位与 ignore-aware 文件索引
│   ├── language/                # Go/Python/Node 检测与可信 server profile
│   └── lsp/                     # 有界 JSON-RPC、进程生命周期与只读导航
├── codingstore/
│   ├── file/                    # Workspace/Worktree/Coding session 与 content-addressed Artifact
│   └── memory/                  # 测试与临时运行
├── ui/                          # 单栏命令行式 TUI、折叠工具结果、会话内 diff、事件桥
├── architecture/                # 全局 import 方向测试
└── app/                         # 可运行组合根：Provider、Store、Agent、Coding Service、TUI
```

Coding `language/lsp` 已具备明确契约、可运行实现和离线协议测试；旧格式迁移包仍不创建空包占位。受控 Run Checks 已作为 Coding Tool 实现。后续新包仍必须同时具备明确契约、可运行实现和测试。

### 17.2 已完成能力

#### LLM、Provider 与 Tool

- `llm.Message` 是持久化和上下文组装共用的标准消息，能表达 user、assistant、assistant tool call 和 tool result；tool result 保留 `call_id`、tool name、错误标记、文本/图像内容和 JSON details。
- `provider.Service` 把 profile ID 解析成具体 adapter，但只向 Agent 返回 `llm.ChatModel`。OpenAI、DeepSeek、Ollama 已拆为独立子包。
- Provider profile 使用 `<config-root>/provider-profiles.json` 版本化持久化，只包含 ID、类型、显示名、Base URL、默认模型、凭证引用和验证时间。内建 profile 仅补齐缺失 ID，不覆盖用户修改。
- Credential 使用不透明引用与 profile 关联。读取顺序为系统 Keyring、显式白名单环境变量；保存、覆盖和删除只作用于 Keyring。Keyring backend 错误被转换为不含 backend 细节和 secret 的稳定错误，环境变量 Store 永远只读。
- `provider.Service.Preflight` 在 Agent run 之前依次验证 profile、credential、Base URL、endpoint 和目标模型。OpenAI/DeepSeek 读取带认证的 `/models`，Ollama 读取 `/api/tags`；成功时间写回 profile，模型目录响应有 4 MiB 上限。
- Provider 与私有 Eino adapter 将预检和运行时错误统一为 `not_configured`、`credential_missing`、`connection_failed`、`authentication_failed`、`model_not_found`、`rate_limited`、`timeout`。产品错误只展示安全消息，SDK/HTTP body/header 与 secret 不进入 Error 文本、event 或 journal。
- Eino 只存在于 Provider 私有 adapter 中，负责消息、tool schema、stream chunk、usage 和 stop reason 转换；Eino 不负责 Agent loop 和工具执行。
- 工具实现只执行并返回 `tool.Result`。`tool_started`、progress、tool result message、`tool_finished` 和产品 Tool Activity 全部由 Agent runtime 集中产生。
- Agent 提供通用 `DataPolicy` 注入点，但不认识 Git、`.env` 或 Coding 业务。Coding 组合根注入 `SecurityPolicy`：原始 Tool 参数只在当前内存调用中交给 Tool，assistant tool-call、durable effective args、tool result/details、progress、错误、模型文本事件和后续上下文在跨越 Agent 边界前统一脱敏。安全 JSON 未变化时保留原字节，避免破坏 Tool 恢复 digest；跨多个流分片的文本在终止边界合并后再脱敏。

#### 上下文与摘要

- Context Manager 使用完整 `llm.Message`，不是 user/assistant 纯字符串。
- 摘要输入包含 assistant tool call 的 ID、名称和已脱敏 JSON 参数，也包含 tool result 文本、错误标记和结构化 details；完整语义被编码在标记为不可信对话数据的 JSON 包络中。
- 裁剪以完整 `RunID/TurnID` 为单位，hard limit 不会拆开 tool call 与 tool result。
- 摘要缓存键由 session、源消息 digest、strategy 和 strategy version 组成，不包含当前主模型。因此主模型从 A 切到 B 时，相同源历史会复用已有摘要；`Summary.Model` 只记录当时由哪个模型生成，供审计使用。
- 已实现 `ModelSummarizer`。可以使用当前主模型，也可以固定一个独立摘要模型。
- `rolling-summary/v4` 不再把模型生成摘要拼入 system prompt。摘要先经过产品注入的 text sanitizer 再写 cache/journal，使用时作为 `trust=untrusted_derived_context` 的 user-role 历史消息；Coding system prompt 保持不变，v3 cache identity 不会被复用。

#### Agent Loop、会话与恢复

- 一个 `RunID` 对应一次用户触发的完整 turn；每次模型请求是一个 step。同一 run 可以包含多个 assistant 消息、多个 tool call/result 和多个 step。
- Agent 在同一 append-only journal 中给 Entry 和 Record 分配共享 sequence。Entry 构成模型上下文树；Record 保存 operation、step、tool、interrupt、usage、compaction 和 lane 事实。
- 工具需要审批或其他外部输入时，Agent 只写 `tool_started + interrupt_requested`，不会伪造 tool result。产品提交批准、拒绝或取消后，Agent 从 journal 取回原参数和 interrupt payload；如果工具实现 `ResumableTool`，先调用其恢复入口完成真实动作，再由 Agent 在同一个 run 中写入真实 tool result、`tool_finished + interrupt_resolved`，继续后续模型 step。工具仍不写 activity 或 session。
- `AnalyzeRecovery` 能稳定列出未完成 run、tool 和 interrupt；`BuildRecoveryPlan` 每次只为一个未完成 run 生成下一条 typed action。`ReplaySafe` 在 Tool 自身参数/工作区校验后可自动重放，`ReplayIdempotent` 只有在 journal 保存了原始 key 时才能自动重放，`ReplayNever` 永不自动执行。
- Tool Result entry 已落盘但 `tool_finished` 未落盘时，恢复器只补齐完成记录，不再次执行 Tool。启动 `RecoveryCoordinator` 自动处理这些纯对账和安全重放，随后停在人工“继续 Turn”边界；产品可选择确认已执行、标记失败、人工重试或放弃整个 Turn。
- 文件日志能忽略并报告不完整的最后一行，并在下次追加前修复尾部。恢复测试按 operation start、user entry、step start、assistant entry、step finish、tool start、interrupt、tool result、tool finish、operation finish 的每个 durable 间隙注入快照，并包含真实文件后端的进程重启集成测试。

#### Coding 产品层与事件隔离

- Coding session 单独绑定 Agent session、Workspace、Worktree、Provider profile、model 和 permission mode；通用 Agent session 不知道 Git 或工作区。
- `codingagent.Snapshot` 投影 transcript、pending interrupt、白名单 `RecoveryAction` 和恢复 warning；私有 thinking 文本、tool 参数、幂等键和底层 DTO 不进入产品快照。TUI 的恢复界面只提交稳定 action ID 与产品决策，不读取 Agent journal。
- `codingagent.ProviderManager` 是 Provider 配置的产品端口；TUI 只能使用 `ProviderProfile/ProviderModel/ConfigureProviderRequest`，看不到 `provider.Profile`、HTTP adapter、Keyring 或 secret store。模型切换先预检再写 Coding Session，并发布只含 profile/model/permission 的 `session_updated` 事件。
- 已迁移的只读工具为 `read_file`、`list_files`、`search_code`、`git_status`、`git_diff`、`git_log`、`git_branches`、`git_show_commit`；写工具为 `apply_patch`。Git 历史最多 50 条，分支只读 refs，提交只接受完整 object ID 且不返回 patch；不存在 checkout、stage 或 commit 创建入口。补丁有大小/文件数限制、统一 diff 校验、Git dry-run、路径与 symlink 边界检查、审批前文件摘要和审批后漂移复核。`read_only` 始终拒绝；`ask` 在无匹配 Grant 时持久化中断；`auto_edit` 和匹配 Grant 只自动执行策略允许的文本变更，超出自动范围重新进入一次性审批。
- `apply_patch` 审批 payload 使用 `coding_patch_approval_v1` 并由 digest 覆盖 diff、原始参数语义、目标文件和 before-state。Coding Snapshot 只白名单投影 `ProposedChange{summary,diff,files,added,deleted}`；before hash、digest 和原始 interrupt payload 不进入 TUI。审批后仍复核 digest、原 tool call 与 worktree drift。
- `allow once` 是当前 Agent interrupt 的 durable resolution；`allow session` 由 Coding Service 从未决 journal 的 Turn/Interrupt/ToolCall 和白名单审批 payload 派生，不接受 UI 自报 tool/path/action。Session Grant 是 append-only 产品审计，只含稳定 ID、精确 tool/action/worktree-relative paths、来源、创建/过期/撤销时间，默认 8 小时且不超过 24 小时；Tool 参数、patch、命令输出、Result details 和 Credential 均不进入 Grant。ToolFactory 每次只获得当前 Coding Session 的 defensive-copy 授权快照，Tool 不写 Grant。
- `apply_patch` Grant 必须覆盖本次全部精确路径；`run_checks` Grant 同时匹配 tool、execute action 和固定 plan ID。过期、撤销或任一维度不匹配即请求新审批。自动 patch 默认上限为 256 KiB、20 文件、2000 变更行、1 MiB 目标文件；`.git/.codepilot/.codex/.husky`、CI workflow、依赖/构建目录和二进制/归档/媒体类型不自动修改。
- Coding `SecurityPolicy` 默认识别 `.env*`（example/sample/template 除外）、私钥/密钥容器、Credential/Auth 文件和常见账户配置目录，也合并 Coding Session 中规范化、排序、去重的用户敏感路径。`list_files/search_code` 跳过它们；显式 `read_file` 或指定路径的 `git_diff` 使用 `coding_sensitive_read_approval_v1` 逐次审批，批准后仍脱敏且不能形成 session grant。敏感目标或包含可识别 secret 的 patch 直接拒绝。Run Checks 输出先脱敏再进入 inline result 或 Artifact，未知错误也先脱敏再跨事件/产品边界。
- interrupt payload 必须保持原字节供 resume/digest 校验，因此它是受保护的 Agent 私有恢复数据，不进入 Coding Snapshot/TUI。Coding Tool 在生成 interrupt 前承担“payload 不含 secret”的前置约束；当前 patch/check/sensitive-read payload 只包含变更、固定计划或路径与完整性字段，不包含 Credential、文件内容和命令输出。
- Coding system prompt 只由静态产品规则与可信 Tool Registry 生成。项目 guidance 只认常规、非 symlink、位于 Worktree 内且不在敏感/排除目录中的 `AGENTS.md`；根到叶的 JSON 文档显式携带相对 `source/scope/sha256`。内容通过 Agent `UntrustedContext` 作为当前 Run 的 user-role 临时数据加入，参与预算与 DataPolicy 但不落盘；普通仓库文件与 Tool Result 永远不会自动进入 system prompt。
- `AGENTS.md` 只能表达其目录后代内的编码、构建和测试惯例。模型没有修改 `ToolScope`、Permission audit、Provider Session binding、ReplayPolicy、RecoveryPlan 或 Repository 的 Tool/API；这些决定由 Coding Service、Agent runtime 和组合根从 durable/trusted facts 产生，项目 guidance 中同名指令只是数据。
- `list_check_plans/run_checks` 根据 `go.mod`、Python 标记和 `package.json` 的受限 script 名生成稳定 plan ID。模型只能提交 `plan_id`；不能提交 executable、arguments、shell、cwd 或环境变量。Go 支持 test/vet/build，Python 支持 compile/pytest，Node 支持 test/build；仓库 script 文本只用于判断名称存在，不拼入 Tool 参数。
- Run Checks 即使在 `auto_edit` 下也先审批并展示固定命令。执行固定 worktree cwd、环境白名单、5 分钟默认超时、64 KiB inline/8 MiB hard output 上限；Windows 使用新进程组加 `taskkill /T`，Unix 使用 process group kill。超大输出以 SHA-256 存入 `<state-root>/coding-artifacts` 私有文件，journal 只保存 path-free Artifact ref 和有界摘要。
- `codingagent/language` 用有界只读根标记检测 Go、Python、Node/TypeScript，并返回确定排序的多语言 profile。server 命令由策略固定为 allowlist，仓库不能提供 executable/args；profile 只在构造当前 Turn 工具时使用，不作为 Session durable 数据。
- `codingagent/lsp` 按精确 worktree/language/server binding 管理 stdio JSON-RPC 进程，提供 Definition、References、Diagnostics、Document Symbols。它限制文档、协议帧、结果集和请求时间，只返回 worktree 内相对路径；进程状态、已同步文档和诊断缓存仅在内存，按 worktree 或 App 生命周期关闭。
- 四个 LSP Tool 仍经过 Coding permission/security 边界：首次启动需要 durable approval，`read_only` 不启动，session grant 精确到 `execute:lsp:<language>`；敏感路径拒绝，协议/进程错误转换成安全的可降级结果。Agent/TUI 只接收标准 Tool Activity 与白名单 ProposedChange，不接触 LSP 流或 JSON-RPC。
- `codingagent.WorkspaceManager` 属于产品层，依赖纯 `codingagent/workspace` Git locator/index port 和 Workspace Repository。它每次加载都验证 root、Git dir/common dir 与 durable Git anchor；不可用和身份替换只作为动态 Picker 状态，不覆盖 binding。普通 Save 不改路径，显式 relocation 才能在锚点匹配、目标未占用和 expected-root CAS 成立时幂等更新，并先关闭 worktree LSP。
- `codingagent/workspace.IndexFiles` 用 Git exclude engine 统一提供 tracked/untracked、非 ignored 的有界普通文件集合。它不依赖产品 DTO；Tool、Language 和 Prompt 层各自注入敏感路径/扩展名/`AGENTS.md` filter，过滤发生在数量上限之前。vendor、node_modules、虚拟环境、缓存和构建目录即使 tracked 也跳过。
- Tool result 在产品快照中投影为折叠摘要、展开详情和可选的强类型 `InlineDiff`。未知 JSON details 不会直接穿过产品边界。
- 静态架构测试已强制 TUI 不得 import `llm`、`provider`、`agent`、SessionStore 或 Eino。

#### TUI 与应用入口

- TUI 已改为单栏命令行布局：会话区没有固定边框，底部使用 `❯` 等待输入，不再存在右侧 diff 面板。
- 一次 tool call/result 在显示层合并为一条工具活动；结果默认折叠，鼠标点击或 `Tab` 选择后使用 Enter/Space/左右方向键查看详情。`apply_patch` 成功后的 diff 无论详情是否折叠，都会紧随该工具出现在会话时间线中。审批与所有 Picker 均有完整键盘路径，长审批内容可用 PageUp/PageDown 滚动。
- `/help`、`/model`、`/permissions`、`/new`、`/session`、`/clear` 都由 TUI 命令白名单处理，未知 slash command 不进入 Agent。`/clear` 创建空白 Session 而不删除原 journal；`/permissions` 通过产品 API 持久化模式并撤销当前仍有效的 session grant。
- durable assistant 文本通过 GFM 终端 renderer 展示，围栏代码由 Chroma 按语言高亮；失败回退为普通换行。Token/费用、最近 Step/耗时和最近模型输入 Context token 来自 `codingagent.Snapshot.Metrics`，该结构只投影 active branch 的 durable usage/operation/step facts，不把 LLM DTO 暴露给 UI。
- 输入历史直接由当前 Session 的 durable user transcript 重建，不增加第二份 prompt 明文存储；最多保留最近 100 条，assistant/tool/command/API Key 均不进入。切换 Session 或 `/clear` 后从目标 Snapshot 重建，composer 与 Credential 的可变缓冲会主动清零。
- Provider/Model Picker 支持 profile 列表、新建、编辑、远端/本地模型发现和选择；可由 `/provider`、`/model` 打开。选择缺少凭证的远程 Provider 后只进入 API Key 页面，保存成功才请求模型目录；已有凭证直接进入模型列表，`k` 专门用于替换 API Key，高级 Profile 字段仍由 `e` 隔离编辑。API Key 只显示掩码，不写输入历史、snapshot、event 或 journal，提交后的 UI 与服务副本都会主动清零。
- 模型页以当前 Coding Session 模型、Profile 默认模型、Provider 实时目录的顺序去重合并，因此启动恢复或模型目录暂时不可用时，已配置模型仍然可见。目录结果只代表“已发现”；用户按 Enter 后必须再次执行 `ValidateSelection/Preflight`，认证、接口和目标模型均通过后才更新 Coding Session，失败不会改变现有选择。
- Pending approval 会在会话时间线展示完整可滚动 Proposed Diff、文件列表和增删统计；`y/s/n/c` 分别表示 allow once、allow session、deny、cancel，仅白名单 patch/check 审批展示 session 选项。决定进入 durable interrupt/tool record，批准后实际 applied diff 继续作为强类型 Tool Result 展示。
- `/workspace` Picker 展示所有保存的 Workspace/Worktree 及 `available/unavailable/identity_changed`，支持键盘选择、切换 Session 和为不可用项输入新路径。启动时单一匹配候选也能在 CLI 明示旧/新路径后恢复；用户拒绝时按新 binding 重新走 trust，不静默复用旧 Session。
- failed operation 不再只是瞬时底部状态：`operation_finished` 的失败事实会按 journal sequence 投影成会话内 `failure` 项。Snapshot 刷新不会清除当前错误，因此 Provider 连接失败等问题不会闪现后消失，重启后仍可从 durable journal 看到。
- `internal/app` 已真实组合 Workspace/Worktree、Coding/Agent file store、Context Manager、Provider、Agent Runtime、Coding Service、Event Bridge 和 Bubble Tea。CLI 不再返回迁移占位错误。
- 启动时会复用已信任 workspace、最近活动 Coding session 和 durable pending interrupt。Provider/model/permission 可从 session 恢复，也可用 `--provider`、`--model`、`--permission` 覆盖；API key 优先从系统 Keyring 读取，并兼容显式白名单环境变量回退。
- TUI 创建前会对最终 session 选择执行 10 秒有界 Provider 预检；失败不再终止产品，而是把安全 `ProviderIssue` 交给 TUI 自动打开 Picker。Agent 运行中发生的同类错误仍会经 operation journal 投影为可恢复的会话 failure。

### 17.3 当前实际落盘格式

给定应用 config root 与 state root，当前文件后端使用以下布局：

```text
<config-root>/
└── provider-profiles.json       # version + 非敏感 Provider profile；不含 API Key

<state-root>/
├── coding-artifacts/<sha256>.blob
│                                # 私有、内容寻址的大型 Tool output
├── agent-archives/<agent-session-id>/
│   ├── <sha256>.tar.gz           # session.json + journal.jsonl 的不可变冷副本
│   └── <sha256>.json             # 无路径 archive ref、sequence 和 codec manifest
├── agent-sessions/<agent-session-id>/
│   ├── session.json             # version + 通用 Agent metadata
│   └── journal.jsonl            # Entry/Record 交错、共享 sequence、append-only
├── context-summaries/<sha256>.json
│                                # version + provider-neutral Summary
├── coding-sessions/<coding-session-id>.json
│                                # Coding 绑定、model/permission、敏感路径与 secret-free Grant audit
├── coding-workspaces/<workspace-id>.json
│                                # Git common dir、git-anchor-v1、trust 和展示元数据
└── coding-worktrees/<worktree-id>.json
                                 # checkout root、git dir 和 last-used 时间
```

`journal.jsonl` 中的消息 Entry 直接保存完整 `llm.Message`。assistant tool call 位于 assistant message 的 content block 中；tool result 是独立的 role=tool message Entry。执行 Record 不替代消息，它补充恢复与审计需要的“何时开始、是否结束、使用哪个 replay policy、关联哪个 result entry”等事实。

这里的“完整”指结构完整，不代表未经策略处理的原始 Provider 数据：在 Coding 产品组合下，assistant/tool 文本、tool-call 参数和 tool-result details 会先经过 `SecurityPolicy`，journal、摘要输入和后续模型上下文读取的是同一份已脱敏结构。原始参数仅在当次 Tool 执行栈内短暂存在；Credential 仍只在 Keyring/白名单环境变量 Store 中。Coding Session 保存的 `sensitive_paths` 只是相对路径规则，不保存文件内容或 secret 值。

事件不落盘并且不是恢复依据。TUI 如果丢事件，必须用 `SnapshotRevision` 重新读取快照；`Event.Sequence` 当前只保证一次 adapter 生命周期内有序，不作为跨重启游标。Event Bridge 使用有界队列：锁内只检查/合并/入队，channel delivery 在独立协程且不持锁；相邻同 Turn delta 可合并到最多 64 KiB，满队列形成可取消背压，关闭会唤醒发布方并幂等关闭消费者流。

### 17.4 当前完整性判断

#### 上下文管理：P1 生产基线已完成

当前实现形成四层保护：

1. Provider model catalog 返回 `ContextWindow`、`MaxOutput` 和 `TokenizerMetadata{ID,Source}`。OpenAI/DeepSeek 的 `/models` 本身不含这些字段，因此适配器用版本化公开能力目录补全；Ollama 逐模型调用 `/api/show` 取得实际 `context_length` 和 architecture tokenizer 身份。`Provider.Service` 在 list/preflight 后缓存能力，Agent step 通常不再做网络发现。
2. Agent 在组装上下文时把模型能力转换为输入预算：`context window - max output - safety margin`，摘要水位为输入预算的 80%。消息、system prompt 和完整 Tool definition/schema 都参与计量。`ByteTokenizer` 仍明确标记为本地保守估算；不支持精确计数的 Provider 不会被伪装成 exact，5% margin 用于 framing 和估算误差。
3. 裁剪只删除最老的完整 Turn。同一活动 `RunID/TurnID` 下的 assistant tool-call、tool-result 和当前消息构成不可拆分保护块；保护块自身超限时返回带 used/limit 的 `CurrentTurnTooLargeError`，由既有 durable operation failure 投影到会话，不会发送孤立 tool-result。
4. Coding ToolFactory 在通用 Tool 执行边界统一检查规范化 `tool.Result`。超过 64 KiB 的纯文本 Result（含命令输出和 Diff）把完整 `{version,tool_name,call_id,result}` 保存为最大 32 MiB 的 SHA-256 Artifact；模型/journal 只保留 16 KiB 预览、typed detail/diff 和 path-free ref。Wrapper 同时覆盖 Execute/Resume，跳过 interrupt；存储失败保留原 Result，绝不重跑副作用 Tool。

摘要策略现为 `rolling-summary/v4`。摘要输入继续包含 user/assistant/tool 的完整语义结构；生成后从原消息确定性提取 tool name、artifact SHA-256 和 changed-file 事实，任何遗漏都拒绝持久化。摘要模型错误、空输出、事实遗漏、脱敏破坏必要事实或旧缓存校验失败时，系统回退为最老完整 Turn 的安全裁剪，原 journal 保持不变，下次仍可重新摘要。strategy version 参与 cache key，v3 缓存不会被 v4 复用；输入 JSON 格式、信任隔离和事实接受/拒绝集都有测试与 golden fixture。最重要的变化是摘要不再提升到 system prompt，而以明确标记的 user-role 派生历史加入上下文。

Agent Session 归档时，文件 Repository 在 product/agent lifecycle 变更前创建确定性、内容寻址的 `tar+gzip` 冷副本。副本精确包含当时的 `session.json` 与 `journal.jsonl`，读取时校验 SHA-256/size；在线 journal 不旋转、不截断、不删除，归档后仍可追加、恢复和重新摘要。这里的“冷存储”是审计副本策略，不是磁盘回收策略。

#### 会话管理：数据模型、文件后端、单 writer 和跨仓库修复已可用

通用 Agent session 与 Coding product session 已分层并可跨进程重载。App 会先持有 State root 的独占 OS 文件锁，再打开 Coding/Agent Repository；owner 元数据记录 owner ID、PID、主机和获取时间，冲突时只作为有界诊断返回。异常退出依赖操作系统释放锁，恢复方不会删除未知锁文件或 owner 文件来强行抢锁。

Coding Session 创建现在先写入 `SessionCreationIntent`，意图包含完成两侧写入所需的精确 Session binding 和状态。只有 Agent Session 与 Coding Session 都存在且不可变身份一致后，意图才会标记 completed。因此进程可在意图、Agent metadata、Coding metadata 任一写入边界退出，后续仍能由 durable facts 判定下一步。

`codepilot doctor` 在短暂 inspection lease 下读取 intent、Coding Session、Agent Session 和 Worktree，列出 pending creation、orphan Agent Session、悬空 Coding Session和缺失 Worktree；它不修改 Session 数据。`codepilot repair` 必须被用户显式调用并持有 writer lease：pending intent 会补齐缺失的一侧，无法安全补齐的 orphan/悬空 binding 只做可逆 archive，journal、Session 目录和 Worktree metadata 均不删除。缺失 Agent metadata 但已有未知 journal 的目录拒绝接管。

Provider-side 精确 token counting 与费用精度仍属于后续增强，不再属于会话存储缺口。worktree relocation、Session Create/List/Switch/Rename/Archive、同 Worktree Picker 与历史 Entry fork 已完成；`ActiveLane` 属于 Coding Session 的持久产品选择，通用 Agent 仍只理解 lane，不感知 Worktree 或 TUI。

#### 任务恢复：普通工具崩溃恢复主链路已完成

显式 interrupt、普通 Tool 崩溃、Tool Result/Finish 写入间隙和 journal 尾部损坏均有恢复路径。恢复器先由 durable facts 重建 `RecoveryPlan`，再按 replay policy 对账或执行，不直接重放全部 pending Tool。App 启动会运行有界协调器；TUI 能处理所有人工决策并持续显示恢复错误和 warning。

仍缺的是一般运行时 retry/backoff 和非恢复场景的显式 abort API；跨进程 writer lease 已由 App 组合根持有，这些均不再属于 crash RecoveryPlan 主链路缺口。

### 17.5 尚未恢复的产品能力

- 任意 shell 命令仍不开放；可信 plan ID 的 Go/Python/Node run checks 已完成，其他命令必须继续按固定计划扩展。
- Go/Python/Node language strategy、受控检查和 LSP navigation 已完成；hover、rename 与编辑后实时诊断属于后续增强。
- Provider 配置和 model metadata 主链路已完成；仍缺费用信息、Provider-side 精确 token counting 和更细的能力声明。
- TUI 的单栏对话、完整命令帮助、Provider/Model/Workspace/Session/Permission Picker、Markdown/语法高亮、Tool 键盘折叠、审批恢复、durable 用量展示与输入历史恢复均已可用。
- 命令行 `doctor/repair` 和跨进程 writer lease 已完成。

当前 CLI 已可实际运行，但“可运行”仍不等于所有增强能力完成。任意 shell 和 Provider-side 精确计数继续显式列为非目标/后续增强。P2-05 的可选真实 Provider 探测、Coding E2E、崩溃/多进程压力、三平台 CI 实现、可复现构建验收和 Tag 发布配置已经完成；尚未发生的 GitHub-hosted Linux/macOS 原生测试和实际 Tag 发布仍不能写成已验证。

### 17.6 后续开发任务顺序

1. Context model metadata、动态预算、Artifact、摘要事实校验和 journal 冷归档已完成；当前转入 Agent retry/backoff 与运行预算。
2. Run Checks 主链路已完成；新增命令只能扩充可信计划目录，继续禁止模型拼接 shell 字符串。
3. 扩展写工具时继续复用 `ResumableTool + interrupt/resume`，并为每种副作用定义漂移校验和幂等规则。
4. Language/LSP、Workspace relocation/Picker、Git 只读工具、ignore-aware 遍历和 TUI 完整体验已完成。
5. crash `RecoveryPlan/Coordinator`、TUI 恢复界面、多进程 writer lease 和跨 Repository `doctor/repair` 已完成。
6. Session Create/List/Switch/Rename/Archive、历史 Entry fork 和 TUI session picker 已完成；切换使用 generation 隔离旧异步结果，TUI 继续只能消费 `codingagent.Event/Snapshot`。
7. P2-05 的本地实施和发布门禁已完成，等待 GitHub-hosted 三平台 runner 实际执行；通过后才讨论删除 `_legacy/internal`，删除仍需用户单独授权。
