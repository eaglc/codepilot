# CodePilot Plan 模式与多 Agent 演进规划

状态：已被取代，仅保留用于追溯，不作为开发指导文档
形成日期：2026-08-24
适用基线：当前模块化架构中的 `agent`、`agent/session`、`codingagent`、`codingstore`、`ui` 与 `app`；`_legacy` 不作为新功能依赖
前置文档：[模块化架构与迁移计划](../architecture/modular-architecture-migration.md)、[产品补全路线图](product-completion-roadmap.md)、[架构评估报告](../archive/2026-08-24-architecture-evaluation-report.md)

> 本文档记录早期技术方案，其中关于 Auto Router、Plan 入口和 Plan 与 Workflow 关系的假设已经不再代表当前需求。后续产品设计必须以 [Plan 模式、子 Agent 与 Workflow 产品需求](../design/plan-mode-and-multi-agent-requirements.md) 为准，实施顺序与验收门槛以 [Plan、Workflow 与多 Agent 可持续交付方案](plan-workflow-multi-agent-delivery-plan.md) 为准。

## 1. 文档结论

CodePilot 后续应按两步演进：

1. **P3：先完成可恢复、可审计的 Plan 模式。** 用户可以通过 `codepilot --mode plan` 强制进入 Plan 模式，也可以在会话中选择 `auto`，由模型在只读边界内判断当前任务应直接执行还是先规划。Plan 模式只允许检查和分析，不允许修改工作区；计划必须结构化持久化，经过明确确认后才能进入执行阶段。
2. **P4：在同一套计划和工作流模型上增加多 Agent。** 模型可以建议任务拆分、依赖和并行机会，但是否创建 Agent、并发数量、读写范围及调度顺序由可信调度器校验和决定。第一版只支持串行子 Agent，之后开放并行只读 Agent，最后才开放隔离工作树中的并行写 Agent。

这两步的共同基础是：**产品级 Turn、Agent Run 和 Workflow Node 必须成为不同身份。** 当前实现把 `codingagent.TurnID` 直接映射为 `agent.RunID`，适合单 Agent 单循环，但不能正确表达一次用户请求中的“路由 → 规划 → 执行 → 复核”，也不能表达多个子 Agent。P3 的第一项工程必须先解除这一耦合。

推荐的最终关系如下：

```text
Coding Session
└── Product Turn（一次用户请求）
    └── Workflow（该请求的执行工作流）
        ├── Route Node  ─── Agent Run
        ├── Plan Node   ─── Agent Run
        ├── Task Node A ─── Child Agent Session ─── Agent Run(s)
        ├── Task Node B ─── Child Agent Session ─── Agent Run(s)
        └── Review Node ─── Agent Run
```

一个 Product Turn 可以包含多个 Run；一个子 Agent 可以因工具调用、审批、重试和恢复产生一个或多个 Run；用户会话仍是对外唯一入口。

## 2. 当前架构基线与约束

当前代码已经具备以下可复用能力：

- `internal/agent` 已有 provider-neutral 的模型/工具循环、预算、重试、中断、恢复和标准化事件。
- `internal/agent/session` 已有 append-only Entry/Record、Lane、Run、工具恢复分析和分支能力。
- `internal/codingagent` 已经是产品策略边界，拥有 Workspace/Worktree、Prompt、权限、安全、Session 与产品事件。
- `internal/codingagent/tools` 已通过可信 `ToolScope` 创建工作区范围内工具，并在执行边界实施权限和敏感路径控制。
- `internal/codingstore/file` 与 `memory` 已提供产品数据的文件/内存实现，可继续承载 Turn、Plan 与 Workflow 元数据。
- `internal/ui` 只依赖 `codingagent` 产品接口，命令注册、事件桥、审批、恢复和 Picker 均有现成扩展点。
- `internal/app` 是唯一组合根，适合注入路由器、计划器、工作流存储和调度限制。

新增能力必须继续遵守以下约束：

1. `agent` 只负责单个通用 Agent Run，不认识 Plan、Git、Worktree、PermissionMode 或多 Agent 角色。
2. `codingagent` 决定 Plan 模式、模型路由、Coding 任务拆分、工具能力和安全策略。
3. UI 只消费 `codingagent.Snapshot` 与 `codingagent.Event`，不得读取 Agent Session 或 Workflow 存储 DTO。
4. 模型只能提出策略和计划，不能直接改变权限、工具集、并发上限、恢复策略或持久化策略。
5. 仓库文件、`AGENTS.md`、工具结果和子 Agent 输出仍是不可信数据，不能触发提权或绕过 Plan 边界。
6. Event 是临时观察信号；Plan、Workflow、Node 状态和审批必须有 durable source of truth。
7. Plan 审批只表示“同意按这个方案推进”，不等于授予文件修改、命令执行或 Git 写操作权限。

## 3. 目标与非目标

### 3.1 P3 Plan 模式目标

- 支持 CLI 启动参数 `--mode direct|plan|auto`。
- 支持 TUI 内持久会话模式切换，以及一次性强制规划。
- `plan` 模式中模型可以检查仓库、澄清约束并形成结构化计划，但无法看到或调用写工具。
- `auto` 模式中，模型在受限的路由阶段决定 `direct` 或 `plan`；决定经过结构校验、规则覆盖并持久化。
- 计划可展示、修改、批准、拒绝、取消和版本化。
- 批准后在同一个 Product Turn 中执行，不创建伪造的第二条用户请求。
- 计划执行前和执行中检测代码基线漂移，必要时暂停并重新规划。
- 崩溃后可恢复到“正在路由、正在规划、等待计划确认、正在执行或正在复核”的准确阶段。

### 3.2 P4 多 Agent 目标

- 将计划编译为有向无环任务图（DAG）。
- 根据依赖、作用域和读写冲突自动决定串行或并行。
- 每个子 Agent 使用独立 Agent Session、独立上下文和明确能力范围。
- 支持统一预算、取消、审批、恢复、结果汇总和失败重规划。
- 并行只读任务可安全共享当前 Worktree；并行写任务必须进入 CodePilot 管理的隔离 Worktree。
- 所有子 Agent 的关键结果和证据可审计，内部完整对话默认不灌入主会话上下文。

### 3.3 暂不纳入首版

- 不在 P3 首版自动批准并直接执行模型生成的计划。
- 不在 P4 首版允许多个 Agent 并发修改同一个 Worktree。
- 不允许模型自行决定无限 Agent 数量、无限预算或动态创建任意工具。
- 不在第一阶段实现跨机器分布式 Agent、远程队列或云端协作协议。
- 不用自由文本或模型 chain-of-thought 作为路由、计划、恢复或审计的权威数据。
- 不把 Plan 文本当作权限声明，也不允许计划扩大用户原始请求范围。

## 4. 核心概念与状态模型

### 4.1 模式策略与运行阶段分离

“模式”是用户选择的策略，“阶段”是当前 Turn 的运行状态，两者不能混为一个字段。

```go
type ModePolicy string

const (
    ModeDirect ModePolicy = "direct"
    ModePlan   ModePolicy = "plan"
    ModeAuto   ModePolicy = "auto"
)

type TurnPhase string

const (
    PhaseRouting              TurnPhase = "routing"
    PhasePlanning             TurnPhase = "planning"
    PhaseAwaitingPlanApproval TurnPhase = "awaiting_plan_approval"
    PhaseExecuting            TurnPhase = "executing"
    PhaseReviewing            TurnPhase = "reviewing"
    PhaseCompleted            TurnPhase = "completed"
    PhaseFailed               TurnPhase = "failed"
    PhaseCancelled            TurnPhase = "cancelled"
)
```

建议语义：

| `ModePolicy` | 行为 |
|---|---|
| `direct` | 跳过模型路由和计划，按当前行为直接进入执行 Agent；仍受 PermissionMode 和工具审批约束。 |
| `plan` | 强制进入只读规划，生成计划后停在计划确认点。 |
| `auto` | 先进入受限路由阶段，由模型提出 direct/plan，可信规则校验后执行。 |

模式优先级从高到低：单次 Turn override、CLI 显式参数、Session 持久设置、应用默认值。模型只能在最终有效策略为 `auto` 时提出路由结果。

### 4.2 Product Turn、Agent Run 与 Node

建议新增产品级 `Turn`：

```go
type Turn struct {
    ID              TurnID
    SessionID       SessionID
    ModePolicy      ModePolicy
    Phase           TurnPhase
    Strategy        *StrategyDecision
    WorkflowID      WorkflowID
    ActivePlanID    PlanID
    ActivePlanVer   int
    BaseRevision    WorkspaceRevision
    CreatedAt       time.Time
    UpdatedAt       time.Time
    FinishedAt      time.Time
}
```

关键规则：

- `TurnID` 由 `codingagent` 创建，不能再从 `agent.RunID` 强制转换。
- `RunID` 只表示一个 Agent Runtime operation。
- `WorkflowNodeID` 表示路由、计划、执行子任务或复核节点。
- Node 持久记录其 `AgentSessionID` 和 `RunID`，用于事件关联和崩溃恢复。
- `AgentEventAdapter` 构造时显式接收 `TurnID`、`NodeID`、`AgentID`，不再用 `source.RunID` 推导产品 Turn。

### 4.3 Plan 生命周期

```text
draft ──▶ ready ──▶ awaiting_approval ──▶ approved ──▶ executing ──▶ completed
  │          │              │                 │             │
  │          └── revise ────┴──▶ superseded   │             ├──▶ failed
  └───────────────────────────────────────────┴─────────────▶ cancelled
```

计划一旦进入 `approved` 就不可原地修改。任何修改都创建 `version + 1`，旧版本标记为 `superseded`。执行、审批和结果都引用精确的 `PlanID + Version + Digest`，防止 UI 展示内容与实际执行内容发生漂移。

## 5. CLI 与 TUI 产品设计

### 5.1 CLI 入口

在现有启动参数中增加：

```text
codepilot --mode direct
codepilot --mode plan
codepilot --mode auto
```

建议与当前 `--permission` 行为一致：显式 `--mode` 更新本次打开的活动 Session 设置；未提供时沿用 Session 已保存值。为平滑发布，第一阶段新旧 Session 默认 `direct`，Auto 路由通过验收后再将新 Session 默认值切到 `auto`。

暂不把 `codepilot plan` 设计为子命令，因为当前主入口是全屏 TUI，`doctor`、`repair` 才是一次性命令。未来增加 headless 运行协议时，再提供 `codepilot run --mode plan --json`。

### 5.2 TUI 命令

建议增加：

```text
/mode                 打开 direct / plan / auto Picker
/mode direct
/mode plan
/mode auto
/plan <request>       仅对这一次请求强制 Plan，不修改 Session 模式
```

`/plan <request>` 是白名单命令，解析后调用 `StartTurn` 并传入 `ModeOverride=plan`；它不能作为普通 prompt 原样送给模型。没有 request 时展示用法，不隐式复用上一条输入。

### 5.3 Plan 展示与交互

计划应作为结构化时间线卡片展示，而不是只显示一段 assistant Markdown。最少包含：

- 目标和范围。
- 假设、风险与待确认项。
- 有顺序和依赖的步骤。
- 每步预期修改范围、验证方法和完成标准。
- 当前计划版本、生成模型和工作区基线。

等待计划确认时提供明确动作：

- `e`：执行当前计划。
- `r`：输入反馈并生成新版本。
- `c`：取消当前 Turn。
- `v`：展开完整计划或查看版本差异。

计划批准后仍按当前 `PermissionMode` 处理每个副作用。后续可以增加“同时创建精确路径/动作范围授权”的独立选项，但必须作为单独、可见且可撤销的权限决定。

## 6. Plan 模式详细设计

### 6.1 受信任的 Turn Coordinator

在 `codingagent.Service.StartTurn` 之上增加 Turn Coordinator。它负责阶段转换，不把模式控制交给 Prompt 自觉执行。

```text
StartTurn
  ├─ effective mode = direct ───────────────▶ Execute
  ├─ effective mode = plan ─────────────────▶ Plan ─▶ Await Approval
  └─ effective mode = auto ─▶ Route
                                ├─ direct ───▶ Execute
                                └─ plan ─────▶ Plan ─▶ Await Approval
```

Coordinator 在每次外部调用前先持久化阶段意图，在完成后再持久化结果。模型调用、Plan 保存、阶段更新之间出现崩溃时，恢复器可以根据 Workflow Event 与 Agent journal 进行幂等补全，而不是猜测进度。

### 6.2 Agent 通用结构化输出契约

路由、计划和子 Agent 结果不能依赖自由文本解析。建议给通用 `agent.Runtime` 增加 provider-neutral 的 `OutputContract`：

```go
type OutputContract struct {
    Kind          string
    SchemaVersion string
    Definition    llm.ToolDefinition
    MaxBytes      int
}

type StructuredOutput struct {
    Kind          string
    SchemaVersion string
    Payload       json.RawMessage
    Digest        string
}
```

实现原则：

1. Provider 仍通过已有 tool-calling 能力看到一个合成的“终态输出工具”。
2. Agent 识别该名称后不把它当普通 Tool 执行，而是校验 JSON、大小和唯一性，经过 `DataPolicy` 脱敏后写入 durable Record。
3. 建议新增通用 `RecordOutputProduced`，只记录 `kind/schema_version/payload/digest`，不引入 Plan 类型。
4. `RunResult` 返回 `StructuredOutput`；`codingagent` 再按产品 schema 严格解码。
5. 一个模型消息不能同时提交终态输出和调用有副作用的工具；否则 Run 失败。
6. 无输出、重复输出、未知字段、超限、schema 版本不匹配都不能静默降级为有效计划。

该机制后续可同时承载 `turn_strategy/v1`、`coding_plan/v1`、`agent_task_result/v1` 和 `review_result/v1`，避免为每种 Agent 角色重复实现 JSON 截取器。

### 6.3 Auto 路由

路由输出建议采用以下最小结构：

```json
{
  "decision": "direct|plan",
  "reason_codes": ["cross_package", "ambiguous_scope"],
  "summary": "需要先确认三个包之间的接口迁移顺序",
  "estimated_steps": 5,
  "independent_workstreams": 2,
  "confidence": 0.86
}
```

只保存可展示的简要理由和枚举 reason code，不请求、不存储模型 chain-of-thought。

路由阶段的可信约束：

- 工具集仅允许有界只读检查；简单任务可配置为零工具快速路由。
- 不能提供 edit、patch、replace、check command、Git 写或进程启动能力。
- 输出只是建议，Coordinator 仍应用确定性规则。
- 用户强制 `plan` 时不调用 Router；用户强制 `direct` 时不允许 Router 改成 Plan。
- 高风险改动、明显跨包迁移、数据格式迁移、权限/安全边界修改、并行候选和范围不确定任务可由规则强制升级为 Plan。
- 路由输出无效或调用失败时，对可能修改代码且无法可靠分类的请求安全降级为 Plan；纯解释/查询请求可以降级为 Direct read-only。
- 仓库 guidance 和工具结果不能修改 ModePolicy、路由 schema 或强制规则。

建议的 Plan reason code 初始集合：

```text
ambiguous_scope
cross_package
public_api_change
data_migration
security_sensitive
multiple_workstreams
high_change_surface
requires_ordering
unknown_dependencies
user_requested
```

不要把“预计文件数大于 N”作为唯一判断标准；文件数、依赖跨度、不可逆风险和验证复杂度应共同决定。

### 6.4 Plan 工具能力

当前 `ToolFactory` 会创建完整 Registry，再由 `PermissionMode` 在执行时拒绝部分副作用。Plan 模式需要更强的能力裁剪：**模型根本不应看到写工具定义。**

建议在 `ToolScope` 中增加受信任的能力 Profile：

```go
type ToolCapability string

const (
    CapabilityInspect ToolCapability = "inspect"
    CapabilityEdit    ToolCapability = "edit"
    CapabilityCheck   ToolCapability = "check"
    CapabilityGitWrite ToolCapability = "git_write"
)

type ToolScope struct {
    // existing fields...
    Capabilities []ToolCapability
}
```

P3 Plan Profile 只注册：

- `read_file`
- `list_files`
- `search_code`
- `git_status`
- `git_diff`
- `git_log`
- `git_branches`
- `git_show_commit`

第一版不注册 `apply_patch`、`edit_file`、`replace_file`、`run_checks`，也不自动启动 LSP 进程。已经运行且可证明无副作用的导航能力可以后续单独加入 `inspect_process` 能力。除工具裁剪外，再把 Plan Run 的有效 Permission 强制封顶为 read-only，形成双层防护。

### 6.5 Plan 数据结构

建议计划 schema 从第一版就兼容未来 DAG，但 P3 执行器可以先按顺序单 Agent 执行：

```go
type Plan struct {
    ID                 PlanID
    TurnID             TurnID
    Version            int
    Status             PlanStatus
    Goal               string
    Scope              PlanScope
    Assumptions        []string
    Risks              []string
    Steps              []PlanStep
    AcceptanceCriteria []string
    BaseRevision       WorkspaceRevision
    SourceRunID        agentSession.RunID
    Digest             string
    CreatedAt          time.Time
    ApprovedAt         time.Time
}

type PlanStep struct {
    ID                 PlanStepID
    Title              string
    Objective          string
    DependsOn          []PlanStepID
    ReadPaths          []string
    WritePaths         []string
    Capability         string
    ParallelHint       bool
    Validation         []string
    AcceptanceCriteria []string
}
```

校验规则至少包括：

- ID 唯一，依赖存在且无环。
- 步骤数、文本、路径数和总字节数有硬上限。
- 路径必须是规范化的 Worktree 相对路径或明确的逻辑范围；禁止绝对路径、遍历和敏感路径。
- `WritePaths` 只是预期范围，不形成授权。
- 空或未知写范围自动标记为 `scope_unknown`，执行时按高冲突处理。
- 每个执行步骤至少有一个验收条件；整个计划必须有最终验收条件。
- Plan digest 对 canonical JSON 计算，审批和执行都引用 digest。

### 6.6 批准、执行与修订

新增产品接口建议为：

```go
StartTurn(ctx, TurnRequest) (TurnResult, error)
ApprovePlan(ctx, ApprovePlanRequest) (TurnResult, error)
RevisePlan(ctx, RevisePlanRequest) (TurnResult, error)
CancelTurn(ctx, SessionID) error
```

`ApprovePlan` 必须提交 `TurnID`、`PlanID`、`Version` 和 `Digest`。Service 重新加载 authoritative Plan 后进行 compare-and-swap；旧 UI、旧版本或已漂移 digest 的批准请求被拒绝。

执行阶段把用户请求、批准计划和必要的计划步骤作为低信任结构化上下文传给执行 Agent。即使计划已由用户批准，它仍不能进入系统 Prompt、改变工具 Registry 或覆盖权限策略。

`RevisePlan` 保存用户反馈并创建新的 Planner Run；旧版本保留用于 diff 和审计。修订扩大原始任务范围、增加敏感路径或新增高风险动作时，必须在新计划中明确显示并重新批准。

### 6.7 工作区漂移与重规划

生成计划时记录 `WorkspaceRevision`：

- Git HEAD object ID。
- Git status/diff 的有界摘要 digest。
- 计划实际读取的关键文件 digest。
- Worktree ID 和 Repository fingerprint。

批准和每个执行步骤开始前检查相关基线：

- 未涉及计划范围的普通变化只产生提示，不应粗暴阻断所有任务。
- 计划涉及的文件发生变化、HEAD 改变导致依赖失效或 Worktree identity 变化时暂停执行。
- 轻微且可证明安全的漂移可重新绑定 baseline；其余情况进入 `needs_replan`，创建新 Plan 版本。
- 不允许模型自行忽略 drift；是否继续由可信规则和用户决定。

## 7. Plan 与 Workflow 持久化

### 7.1 Repository 边界

在 `codingagent` 定义 consumer-owned 接口，由 `codingstore/file` 和 `codingstore/memory` 实现：

```go
type TurnRepository interface {
    CreateTurn(context.Context, Turn) error
    LoadTurn(context.Context, TurnID) (TurnSnapshot, error)
    AppendTurnEvent(context.Context, TurnID, uint64, TurnEventRecord) (uint64, error)
    ListUnfinishedTurns(context.Context, SessionID) ([]TurnID, error)
}

type PlanRepository interface {
    SavePlan(context.Context, Plan) error
    LoadPlan(context.Context, PlanID, int) (Plan, error)
    ListPlanVersions(context.Context, PlanID) ([]Plan, error)
}
```

`AppendTurnEvent` 使用 expected revision，保证并发子 Agent 完成时不会覆盖彼此状态。Plan 采用不可变版本写入；同一 ID/version 只允许内容完全一致的幂等重写。

### 7.2 文件布局建议

```text
StateDir/
├── coding-turns/
│   └── <turn-id>/
│       ├── metadata.json
│       └── events.jsonl
├── coding-plans/
│   └── <plan-id>/
│       ├── v0001.json
│       └── v0002.json
└── coding-workflows/
    └── <workflow-id>/
        ├── metadata.json
        └── events.jsonl
```

所有路径继续经过严格 ID 校验；JSONL 采用现有 Agent store 的尾残片恢复原则。Plan JSON 使用版本 envelope、原子写和内容 digest。不要只保存渲染后的 Markdown；Markdown 应由结构化 Plan 投影生成。

### 7.3 崩溃恢复顺序

每个阶段使用 durable-first 顺序：

1. 写 `NodeScheduled/NodeStarted`，包含预分配的 Agent Session ID 和 Run ID。
2. 调用 Agent Runtime；Agent 自己先写 operation record，再产生模型或工具副作用。
3. Agent 写入 `RecordOutputProduced` 或 terminal record。
4. Coordinator 幂等保存 Strategy/Plan/TaskResult。
5. 写 `NodeCompleted` 和下一阶段状态。

如果进程在第 3、4 步之间退出，恢复器从 Agent journal 找到带 digest 的结构化输出并补写产品存储；如果在模型调用前退出，可安全重启尚未开始的节点。副作用工具仍遵循现有 replay policy，Workflow 层不能自行猜测工具是否执行成功。

## 8. 多 Agent 目标架构

### 8.1 引入时机与包边界

P3 只有线性 Route/Plan/Execute 时，Coordinator 可以先留在 `codingagent`，避免提前创建空抽象。P4 出现通用 DAG、并发和节点恢复后，再引入 `internal/orchestrator`：

```text
agent ───────────────────────────────▶ 单个 Agent Run
orchestrator ────────────────────────▶ Workflow DAG、调度、预算、取消、恢复
codingagent ─────────────────────────▶ Coding 计划、角色、工具、Workspace、安全策略
orchestrationstore/file|memory ──────▶ orchestrator Repository
ui ──────────────────────────────────▶ codingagent 产品 DTO
app ─────────────────────────────────▶ 组合全部实现
```

`orchestrator` 不认识 Git、Plan 文本、PermissionMode 或 TUI。它只调度节点处理器：

```go
type NodeHandler interface {
    ExecuteNode(context.Context, NodeExecution) (NodeResult, error)
    RecoverNode(context.Context, NodeRecovery) (NodeResult, error)
}
```

`codingagent` 提供 Coding NodeHandler，把节点转换为 Agent Session、Prompt、ToolScope 和 Workspace scope。

### 8.2 Workflow 与 Node

```go
type Workflow struct {
    ID             WorkflowID
    TurnID         TurnID
    PlanID         PlanID
    PlanVersion    int
    Status         WorkflowStatus
    Nodes          []WorkflowNode
    MaxParallel    int
    TokenBudget    int
    CostBudget     float64
    DurationBudget time.Duration
}

type WorkflowNode struct {
    ID             NodeID
    Kind           NodeKind
    DependsOn      []NodeID
    Capability     CapabilityProfile
    ReadSet        []string
    WriteSet       []string
    AgentID        AgentID
    AgentSessionID agentSession.ID
    Status         NodeStatus
    Attempt        int
}
```

Node kind 初始集合：

- `route`
- `plan`
- `explore`
- `implement`
- `validate`
- `review`
- `integrate`

角色是受信任的能力模板，不是让模型自由创造的“人格”。模型可以建议 `explore/implement/validate/review`，Coordinator 只能从白名单 Profile 中选择。

### 8.3 自动选择单 Agent 或多 Agent

Planner 输出步骤和依赖；可额外输出 `parallel_hint` 与建议角色。可信 Plan Compiler 将其转换为 Workflow DAG，并应用以下规则：

1. 默认单 Agent；只有至少两个独立、边界清晰且预期收益明显的任务才启用多 Agent。
2. 模型给出的并行提示不是调度命令。
3. DAG 必须无环，节点数、深度和 fan-out 有上限。
4. 作用域未知的节点按写冲突处理，默认串行。
5. 同一文件或重叠目录的写节点串行。
6. 全局 build/test/format 节点必须等待相关写节点完成。
7. 只读节点可在同一 Worktree 并行；写节点只有在隔离 Worktree 准备完成后才可并行。
8. 多 Agent 的预计额外 token/cost 超出收益阈值时回退到单 Agent。

建议初始硬限制：最多 8 个节点、最多 3 个子 Agent、并发度最多 2；全部由 App 配置并可在测试中注入，不散落魔法常量。

### 8.4 串并行冲突规则

| Node A | Node B | 调度决策 |
|---|---|---|
| read | read | 可并行，共享当前 Worktree。 |
| read | write | 范围相交则串行；不相交且写方隔离时可并行。 |
| write | write | 当前 Worktree 永不并行；隔离 Worktree 且 WriteSet 不相交时才可并行。 |
| write | global validate | validate 等待所有相关 write。 |
| unknown | 任意 | 默认串行，要求先补充探索或缩小范围。 |

调度器使用规范化路径集合和目录前缀判断冲突，Windows 下遵循当前路径大小写规则。模型不能通过不同路径拼写绕过冲突检测。

### 8.5 子 Agent 隔离

每个子 Agent 必须拥有：

- 独立 `AgentID` 和 `AgentSessionID`。
- 只包含当前节点目标、依赖摘要、范围、约束和验收标准的 `TaskBrief`。
- 独立 RunLimits 和从父 Workflow 切分出的预算。
- 明确 Tool Capability Profile。
- 只读的父计划版本和 Workspace baseline。
- 独立的结果 schema，不共享可变内存。

子 Agent 不应自动得到整个父对话和其他 Agent 的完整 transcript。依赖只通过结构化 `TaskResult` 传递：

```go
type TaskResult struct {
    NodeID       NodeID
    Status       string
    Summary      string
    Evidence     []EvidenceRef
    ChangedFiles []FileChange
    Checks       []CheckResult
    Risks        []string
    FollowUps    []string
    OutputDigest string
}
```

完整 child session 保留用于审计和故障排查；主上下文只接收有界摘要、证据引用和必要产物，避免多 Agent 导致上下文平方增长。

### 8.6 并行写 Agent 的 Worktree 策略

并行写必须晚于并行只读落地，且使用 CodePilot 管理的 Git Worktree：

```text
StateDir/managed-worktrees/<workflow-id>/<node-id>/
```

安全要求：

- 目录由可信 WorkspaceManager 创建和验证，不能由模型提供绝对路径。
- 每个 managed Worktree 固定到计划 baseline，并记录 Git common dir、fingerprint 和 node binding。
- 子 Agent 工具只作用于该 Worktree；禁止修改 Git common config、hooks 或其他 Worktree。
- 创建、集成和清理由 durable intent 驱动，可在崩溃后继续或回滚。
- 未完成集成前不删除 Worktree；清理必须解析并验证精确路径，不能使用宽泛递归目标。
- 活动用户 Worktree 有未提交改动时，不能直接 merge/cherry-pick 覆盖。

建议第一版集成采用“**隔离 Worktree 生成稳定 patch/commit → Coordinator 在活动 Worktree 上逐个做冲突和 digest 校验 → 通过正常权限边界应用**”。不要让子 Agent 直接修改用户当前分支。后续确认跨平台 Git 语义和恢复可靠后，再评估受控 cherry-pick。

### 8.7 审批、取消和恢复

- 所有审批集中投影到父 Coding Session，UI 不直接连接子 Agent。
- Node 等待审批时只暂停该 Node；若其依赖阻塞，调度器可继续运行无关节点。
- 在 `ask` 模式下，多个并行 Agent 的审批进入有界队列，按创建顺序展示。
- Plan approval 不自动批准节点 Tool；精确 Session grant 仍按现有策略创建和撤销。
- 取消 Product Turn 时先 durable 写 `cancel_requested`，再取消所有活动 Node context，最后汇总 terminal 状态。
- 重启时先恢复 Workflow projection，再逐个调用现有 Agent recovery；只自动执行标记为 safe/idempotent 的动作。
- 单节点失败可以按策略重试、降级为串行、重新规划或终止；扩大范围的重规划必须再次确认。

## 9. Prompt 与模型协议

建议拆分稳定的角色 Prompt Builder：

- `RoutePromptBuilder`：只说明路由 schema、模式规则和不可提权边界。
- `PlanPromptBuilder`：要求检查事实、列出假设、生成结构化 DAG 计划，不写代码。
- `TaskPromptBuilder`：只描述当前节点，不允许自行扩大范围或创建新 Agent。
- `ReviewPromptBuilder`：依据计划验收条件和实际证据判断 pass/fail/needs_fix。
- 现有 `PromptBuilder` 继续提供所有角色共享的 Coding 安全基线。

共享规则：

1. 角色 Prompt 是可信系统文本；仓库 guidance 仍以 user-role 不可信上下文进入。
2. 路由和计划结果必须通过 `OutputContract` 提交。
3. 模型不能输出新的 PermissionMode、Provider profile、工具名或预算作为有效配置。
4. 不要求模型披露详细思维过程；只要求简短理由、假设、风险和可验证证据。
5. 计划中的命令只是建议，执行时必须由白名单 check plan 或新的安全工具重新解析。

## 10. 产品事件、Snapshot 与指标

### 10.1 新增产品事件

建议新增以下 `codingagent.EventKind`：

```text
turn_strategy_decided
plan_started
plan_created
plan_revision_created
plan_approval_requested
plan_approved
plan_rejected
workflow_started
workflow_node_started
workflow_node_progress_changed
workflow_node_completed
workflow_node_failed
workflow_replanned
workflow_completed
```

事件增加可选 `WorkflowID`、`NodeID`、`AgentID`。所有字段是产品安全 DTO，不暴露 Agent 原始消息、工具参数、绝对 managed-worktree 路径或 Provider 对象。

### 10.2 Snapshot 扩展

```go
type Snapshot struct {
    // existing fields...
    ActiveTurn       *TurnView
    ActivePlan       *PlanView
    Workflow         *WorkflowView
    PendingPlan      *PlanApprovalView
    PendingApprovals []PendingInterrupt
}
```

`PlanView` 和 `WorkflowView` 应限制节点数、文本和 evidence 数量；大结果放入现有 ArtifactStore，只在 UI 中展示安全摘要和引用。

### 10.3 指标与预算

父 Workflow 预算是硬上限，子 Agent 预算从父预算切分而来，不能各自重新获得完整 Session 上限。至少统计：

- 路由、规划、执行、复核各阶段 token、cost 和耗时。
- 每个 Agent 的 step、tool call、retry、context 和输出量。
- 峰值并发、排队时间、阻塞原因和重规划次数。
- 总预算、已使用、预留和剩余预算。
- 计划命中率：生成 Plan 后是否执行、是否修订、是否因 drift/review 返工。

到达父预算时，调度器停止启动新节点，取消可取消工作并生成有界的部分结果；不能等所有子 Agent 各自耗尽预算后才发现超限。

## 11. 分阶段执行路线

### 11.1 P3：Plan 模式

#### P3-00 身份与代码结构前置改造

目标：建立不会被多阶段/多 Agent 推翻的 Turn 基础。

执行步骤：

1. 新增产品 `Turn`、`TurnID` 生成和持久化接口。
2. 移除 `TurnID(result.RunID)` 与 `TurnID(source.RunID)` 这类隐式转换。
3. `AgentEventAdapter` 显式绑定 Product Turn 与 Run。
4. 允许同一 Product Turn 在同一 lane 上启动后续 Agent Run，而不重复追加用户消息；为该能力提供明确 API，不伪造“execute plan”用户消息。
5. 抽取 `buildTurnScope`，统一 Start/Resume/Recover 中 Prompt、Tools 和 Worktree scope 的重复装配。
6. 在不改变现有 direct 行为的前提下完成回归测试。

交付门槛：现有单 Agent E2E、恢复、审批、分支、指标和 TUI 事件测试全部通过；direct 模式用户行为不变。

#### P3-01 ModePolicy 与入口

1. 在 `codingagent.Session` 增加 `ModePolicy`，旧数据缺省映射为 `direct`。
2. 增加 `SetModePolicy` 产品 API、验证和权限无关的持久化。
3. CLI 增加 `--mode`，`app.Options` 传递并采用与 `--permission` 一致的显式覆盖规则。
4. TUI 增加 `/mode` Picker 和状态栏模式标识。
5. 增加 `/plan <request>` 的单 Turn override。

交付门槛：非法模式启动失败且给出安全提示；Session 切换后模式正确恢复；旧 Session 无需迁移即可打开。

#### P3-02 结构化输出与手动 Plan

1. 在 `agent` 增加通用 `OutputContract` 和 durable `RecordOutputProduced`。
2. 为 Plan 定义 `coding_plan/v1` schema、canonical encoding、digest 和严格校验。
3. ToolFactory 增加 Capability Profile，Plan Registry 只包含只读工具。
4. 实现 Planner Prompt、Plan Run 和 `PlanRepository`。
5. Snapshot/Event/UI 显示 Plan 卡片。

交付门槛：强制 Plan 时模型不可见任何写工具；恶意 Prompt、仓库指令和无效 JSON 不能产生可批准计划；崩溃后能恢复已生成但尚未投影的 Plan。

#### P3-03 Plan 审批、修订与执行

1. 实现 Plan immutable version、Approve/Revise/Cancel API。
2. 批准请求绑定 Plan digest 和 WorkspaceRevision。
3. 计划批准后在原 Product Turn 内启动执行 Run。
4. 执行仍使用正常 PermissionMode、Tool approval 和安全边界。
5. 实现 drift 检查、`needs_replan` 与版本 diff。
6. 扩展启动恢复 Coordinator 覆盖 Plan 阶段。

交付门槛：未批准计划不能写工作区；批准旧版本失败；Plan 审批不隐式产生 Tool grant；重启不会重复执行已完成副作用。

#### P3-04 Auto 路由

1. 定义 `turn_strategy/v1` schema 和 reason code。
2. 实现只读 Router Run 与确定性 override 规则。
3. 持久化路由决定、模型摘要、schema version 和来源 Run。
4. 加入失败安全降级、超时和预算。
5. 先以 feature flag/experimental 配置开放 `auto`，保留 `direct` 默认。

交付门槛：确定性模型测试覆盖 direct/plan/无效输出/超时/注入；同一输入和固定上下文下路由可重放审计；路由阶段零工作区副作用。

#### P3-05 质量评估与默认策略

1. 建立代表性任务集：简单修复、跨包重构、迁移、安全修改、纯问答、模糊需求。
2. 评估误规划率、漏规划率、额外延迟、token/cost 和计划执行成功率。
3. 完成 Windows/Linux/macOS 原生恢复与 TUI 验证。
4. 达到阈值后，新 Session 默认改为 `auto`；已有 Session 保持原设置。

建议发布阈值：安全/迁移类任务漏规划率为 0；简单任务不必要规划率低于 15%；Auto 增加的中位首响应延迟有明确上限并可配置。

### 11.2 P4：多 Agent

#### P4-01 Durable Workflow 与串行子 Agent

1. 引入 `orchestrator` 的 Workflow/Node/Repository/Scheduler 最小核心。
2. 每个执行 Node 创建独立 Agent Session 和受限 TaskBrief。
3. 先只允许一个 active Node，验证身份、预算、取消、事件和恢复。
4. 实现 `agent_task_result/v1` 和父会话有界汇总。

交付门槛：串行两个子 Agent 能在一次 Product Turn 内完成；重启后不会重复完成节点；主上下文不包含完整 child transcript。

#### P4-02 自动任务图与复核

1. Plan Compiler 把 PlanStep/DependsOn 转为 DAG。
2. 校验无环、范围、节点/深度上限和能力 Profile。
3. 增加 Review Node，按计划 acceptance criteria 读取证据并输出 `review_result/v1`。
4. 支持节点失败后的 retry、needs_fix、replan 和 fail-fast 策略。

交付门槛：模型无法创建未知角色、未知工具或越界 Node；review 失败不会伪装为 completed。

#### P4-03 并行只读 Agent

1. Scheduler 增加 bounded worker pool、依赖唤醒和 expected-revision append。
2. 仅允许 `CapabilityInspect` 节点并行，共享当前 Worktree。
3. 实现父预算原子预留、取消传播和并发事件排序。
4. 使用 race detector、崩溃注入和慢 Agent 测试验证。

交付门槛：并发度从不超过上限；取消后没有孤儿 Run；同一 Workflow 并发完成不丢事件；结果顺序不影响最终确定性投影。

#### P4-04 隔离 Worktree 中的并行写 Agent

1. WorkspaceManager 增加 managed Worktree intent、创建、验证、集成和清理。
2. Scheduler 实现 ReadSet/WriteSet 冲突矩阵。
3. 子 Agent 在隔离 Worktree 中修改并生成稳定 patch/commit artifact。
4. Integrate Node 在活动 Worktree 上顺序校验和应用，保留当前权限审批。
5. 覆盖 dirty worktree、冲突、HEAD drift、进程崩溃和跨平台路径。

交付门槛：任何两个写 Agent 都不会并发写同一物理 Worktree；冲突不会静默覆盖用户改动；未集成产物和 managed Worktree 可恢复、可审计、可安全清理。

#### P4-05 自适应编排与产品化

1. Router/Planner 输出多 Agent 收益估计，可信规则决定是否采用。
2. 支持角色级模型选择，但第一版默认继承 Session Provider/Model。
3. 增加 Workflow 详情页、Agent/Node 展开、统一审批队列和预算展示。
4. 建立单 Agent 与多 Agent 的质量、成本和时延对照评估。
5. 达到阈值后，才允许 `auto` 自动选择多 Agent；并行写继续保留独立 feature gate。

## 12. 主要代码改动映射

| 范围 | 主要改动 |
|---|---|
| `cmd/codepilot/main.go` | 增加 `--mode`、Usage 和参数测试。 |
| `internal/app/app.go` | 传递 Mode、组合 Turn/Plan/Workflow store、注入路由/规划/调度预算。 |
| `internal/agent/runtime.go` | 增加通用 OutputContract；支持同一产品 Turn 的后续 Run，不引入 Coding 概念。实现时应顺带按既有评估拆分大文件。 |
| `internal/agent/event.go` | 保持 Agent 事件通用；必要时新增 structured output produced 事件。 |
| `internal/agent/session` | 增加通用 output record、校验、clone 和恢复分析。 |
| `internal/codingagent/session.go` | 增加 ModePolicy；保持 Session/Turn/Run 身份分离。 |
| `internal/codingagent/service.go` | 抽 Turn Coordinator 和统一 scope builder；保留 Service 作为产品入口。 |
| `internal/codingagent` 新文件 | `turn.go`、`plan.go`、`strategy.go`、`coordinator.go`、`workflow_projection.go`。仅在出现实际行为时创建。 |
| `internal/codingagent/prompt` | 增加 route/plan/task/review 角色 Builder，共享安全基线。 |
| `internal/codingagent/tools` | 按 Capability Profile 构建 Registry；Plan 模式从定义层移除副作用工具。 |
| `internal/codingstore/file|memory` | 实现 Turn/Plan repository 及契约测试。 |
| `internal/orchestrator` | P4 才创建；拥有 DAG、Scheduler、预算、取消和恢复机制。 |
| `internal/ui` | `/mode`、`/plan`、Plan 卡片、Workflow/Agent 状态、计划审批与统一审批队列。 |
| `internal/architecture` | 更新依赖白名单，继续禁止 UI 越层和 generic Agent 依赖 Coding。 |

## 13. 测试与验收矩阵

### 13.1 单元测试

- Mode 优先级、旧值默认、非法值和 Session 切换。
- Strategy/Plan/TaskResult schema、大小限制、canonical digest、未知字段。
- Plan DAG 无环、依赖存在、路径规范化和敏感路径拒绝。
- Tool Capability Profile 不泄漏写工具定义。
- Scheduler runnable 集合、冲突矩阵、并发上限、预算预留和取消。
- Workflow/Plan file 与 memory backend 契约一致。
- 产品事件映射不再把 RunID 当 TurnID。

### 13.2 集成测试

- `--mode plan` 启动、持久化和 TUI 模式显示。
- 强制 Plan 全流程：生成 → 修订 → 批准 → 执行 → 复核。
- Auto 路由 direct/plan、模型失败和无效输出降级。
- Plan 生成后、保存前崩溃；批准后、执行前崩溃；节点工具执行中崩溃。
- Workspace drift、用户未提交改动和过期 Plan 批准。
- 多 Agent 串行、并行只读、节点失败重试和父取消。
- 并行事件竞争、状态 revision CAS 和 Artifact 大结果。

### 13.3 安全测试

- 仓库中的 Prompt Injection 不能改变 Mode、Agent 数、工具、预算和权限。
- Plan 和 child result 中的 secret 在 durable store、event、Snapshot 和 UI 全链路脱敏。
- 子 Agent 不能访问未授权路径、其他 managed Worktree 或父进程凭证。
- Plan 审批不等于 Tool approval；模式切换不能复用不再有效的 grant。
- 并行写冲突不能覆盖活动 Worktree 的用户改动。
- 取消、恢复和重试不能重复执行 ReplayNever 工具。

### 13.4 E2E 与质量评估

- 使用真实临时 Git 仓库覆盖 Go/Python 的跨文件 Plan 执行。
- 至少一个任务需要串行依赖，另一个任务包含两个独立只读探索分支。
- P4-04 增加两个独立写分支及一个故意冲突分支。
- 三平台执行 TUI、文件锁、JSONL 尾残片、managed Worktree 和路径大小写测试。
- 保持 `go test ./... -count=1`、`go vet ./...`、架构依赖测试和发布构建为通用门禁。

## 14. 风险与控制措施

| 风险 | 控制措施 |
|---|---|
| Auto 对简单任务过度规划，增加延迟和费用 | direct 默认灰度、Router 小预算、评估集、可见模式切换、失败缓存不跨上下文复用。 |
| 模型给出看似结构化但不可执行的计划 | 严格 schema、Plan Compiler、路径/依赖校验、批准前基线检查、review。 |
| Turn/Run 解耦引入存储和事件回归 | P3-00 单独提交，先保持 direct 行为完全一致，增加映射和恢复契约测试。 |
| 多 Agent 上下文与费用失控 | 父预算硬上限、节点预算预留、结构化结果、完整 transcript 不回灌。 |
| 并发写覆盖或冲突 | 分阶段开放；共享 Worktree 只读；写 Agent 强制 managed Worktree；顺序集成。 |
| 多个审批导致 TUI 混乱 | 父会话统一有界审批队列，审批绑定 Node/Plan revision，允许无关节点继续。 |
| 崩溃后出现孤儿 Agent 或 Worktree | 创建 intent、稳定 ID、Workflow journal、启动扫描和安全清理状态机。 |
| Provider 对结构化输出支持不一致 | 在 Agent 层用现有 tool-calling 归一化 OutputContract，并为各 Provider 做契约测试。 |
| 模型通过 Plan 扩大授权 | Plan 始终是低信任数据；工具 Registry、PermissionMode 和 Scope 由可信代码重新计算。 |

## 15. 发布与功能开关策略

建议按以下顺序开放：

1. `plan_mode`：手动 `--mode plan` 与 `/plan`，默认开启。
2. `auto_plan`：先 experimental，Session 默认仍为 direct。
3. `multi_agent_serial`：仅显式开启，最多两个子 Agent。
4. `multi_agent_parallel_read`：通过 race/恢复和跨平台门禁后开启。
5. `multi_agent_parallel_write`：长期独立开关，只有 managed Worktree 和集成恢复完整后开放。

功能开关只控制是否可用，不应改变 durable schema 的解释。关闭功能后，应用仍必须能够加载、展示和取消已有 Plan/Workflow，不能把数据视为损坏。

## 16. 后续发展方向

完成 P3/P4 后，可以沿以下方向继续演进：

1. **Headless/RPC**：提供 `codepilot run --mode auto --json` 和稳定 Workflow API，TUI 仍只是一个客户端。
2. **角色级模型路由**：Router/Planner 使用低成本模型，Implementer/Reviewer 使用更合适的模型，但保持同一安全和预算边界。
3. **增量重规划**：只替换失败或漂移的子图，不重新生成整个计划；所有修改仍产生新 Plan revision。
4. **可复用 Workflow 模板**：把常见“探索 → 修改 → 测试 → 复核”编译为可信模板，模型只填充任务内容。
5. **跨会话任务恢复与后台执行**：在有稳定 RPC 和通知机制后，让 Workflow 独立于 TUI 生命周期运行。
6. **分布式 Worker**：只有本地多 Agent 的 identity、artifact、approval、recovery 和 budget 协议稳定后，才考虑远程 Agent。
7. **持续评估**：维护真实任务集，对 direct、plan、single-agent、multi-agent 的正确率、返工率、时延和费用做版本回归。

## 17. 必须坚持的实施顺序

最终推荐顺序是：

```text
Turn/Run 解耦
    ↓
手动只读 Plan
    ↓
计划持久化、审批、修订、漂移与恢复
    ↓
Auto 路由
    ↓
Durable Workflow + 串行子 Agent
    ↓
自动 DAG + Reviewer
    ↓
并行只读 Agent
    ↓
隔离 Worktree 中的并行写 Agent
    ↓
自适应模型/Agent 编排与 Headless API
```

不应跳过 Plan 的结构化持久化直接做多 Agent，也不应在工作区隔离和恢复完成前开放并行写。这样可以让每个阶段都形成可独立使用、可回滚、可测试的产品能力，并确保后续扩展不会破坏 CodePilot 已建立的安全、持久化和架构边界。
