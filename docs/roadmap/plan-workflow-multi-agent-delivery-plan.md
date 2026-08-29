# CodePilot Plan、Workflow 与多 Agent 可持续交付方案

状态：设计与交付基线 v1  
形成日期：2026-08-26  
需求基线：[Plan 模式、子 Agent 与 Workflow 产品需求](../design/plan-mode-and-multi-agent-requirements.md)  
适用代码基线：当前模块化架构中的 `agent`、`agent/session`、`codingagent`、`codingstore`、`tool`、`ui` 与 `app`  
历史说明：[旧版 Plan 模式与多 Agent 演进规划](plan-mode-and-multi-agent-roadmap.md) 已被取代，不作为本方案输入或实施依据

## 1. 方案目标

本方案把需求基线拆成能够持续交付、独立验收和安全回退的阶段，解决三个问题：

1. 每个阶段向用户交付什么核心能力。
2. 为交付这些能力需要怎样改造当前系统，以及为什么这样改造。
3. 什么条件满足后才允许进入下一阶段。

最终产品关系保持不变：

> Plan 是用户控制复杂任务的决策边界；Workflow 是批准方案的推进方式；子 Agent 是 Workflow 内部的受控执行资源。Plan 后可以继续由单 Agent 执行，不自动进入多 Agent。

## 2. 总体实施原则

### 2.1 不做一次性大改

每个阶段必须保持当前 Direct 单 Agent 主链路可用。新能力通过明确入口和功能开关逐步开放，不能要求 Plan、Workflow 和多 Agent 同时完成后才产生用户价值。

### 2.2 产品状态由可信代码控制

模型可以调用 `EnterPlanMode`、提交 Plan、建议 Workflow 和子 Agent 分工，但不能自行完成模式切换、批准计划、扩大权限、提高并发或改变资源上限。所有状态转换由 `codingagent` 的可信协调层校验并持久化。

### 2.3 能力边界必须真实裁剪

Plan、Explore、Implement、Validate 和 Review 等阶段使用不同的工具能力集合。只读保证不能只写在 Prompt 中；模型在 Plan 阶段不能看到或调用写工具。

### 2.4 事件不是权威状态

UI Event 只用于及时刷新。Turn、Plan、Workflow、Node、审批和恢复状态必须持久化，并能在事件丢失或进程重启后重新投影 Snapshot。

### 2.5 单 Agent 默认，多 Agent 按收益启用

Plan 批准后先选择执行策略：单 Agent、单 Agent Workflow 或多 Agent Workflow。没有独立工作流、并行收益或上下文隔离收益时，保持单 Agent。

### 2.6 并发能力分级开放

先验证串行调度，再开放并行只读，最后开放隔离环境中的并行写。任何阶段都不允许多个 Agent 无隔离地并发修改用户活动 Worktree。

### 2.7 每阶段同时交付恢复与验收

新状态如果不能在重启后准确恢复，就不算完成。每阶段必须同时补齐文件存储、内存存储、Snapshot、Event、取消、恢复和端到端测试。

## 3. 当前能力基线

### 3.1 可以复用的能力

当前代码已经具备：

- Provider-neutral 的单 Agent Run/Step/Tool 循环。
- append-only Agent Session journal 和连续 sequence。
- durable tool interrupt、用户审批、Resume 和崩溃恢复。
- ReplayNever、ReplaySafe 和 ReplayIdempotent 工具恢复策略。
- Coding Session、Workspace、Worktree、PermissionMode 和安全边界。
- `read_file`、`list_files`、`search_code` 及 Git 只读工具。
- patch、文件编辑、检查命令和 LSP 的权限控制。
- 产品级 Snapshot/Event 投影和 TUI 审批交互。
- 文件/内存 Repository、原子写、ArtifactStore 和状态一致性修复。
- 每 Session 串行 operation lock、活动 Turn 取消和统一预算。

这些能力意味着 Plan 和子 Agent 不需要重写模型运行时、工具协议、权限体系或 UI 框架。

### 3.2 主要缺口

当前系统仍缺少：

- 独立于 Agent Run 的产品级 Turn 身份和生命周期。
- Plan 模式状态、Plan 数据模型、版本和持久化。
- 按阶段构造 Prompt 与 Tool Registry 的能力 Profile。
- `EnterPlanMode`、`ExitPlanMode` 及其产品化确认流程。
- 同一用户请求跨多个 Agent Run 继续而不重复写入用户消息的能力。
- Workflow、Node、依赖、执行策略和调度状态。
- 子 Agent Session 生命周期、范围约束和结构化结果协议。
- 多 Agent 的统一预算、取消、审批、并发和恢复。
- 并行写入的隔离、冲突检测和受控集成。

## 4. 目标架构边界

```text
TUI / future RPC
        │
        ▼
codingagent Turn Coordinator
        ├─ Plan lifecycle + Plan Repository
        │    └─ optional exploration task ─▶ Child Agent Session
        ├─ Execution strategy selection
        ├─ Permission / capability / workspace policy
        │
        ├─ Single Agent execution ───────────▶ agent.Runtime
        │
        └─ Workflow coordination
             ├─ Workflow Repository / Scheduler
             ├─ Node A ─▶ Child Agent Session ─▶ agent.Runtime
             ├─ Node B ─▶ Child Agent Session ─▶ agent.Runtime
             └─ Review / Integration
```

### 4.1 身份关系

```text
Coding Session
└── Product Turn                  一次用户请求
    ├── Plan version(s)           可选
    │   └── Exploration task(s)   可选，只读子 Agent 探索
    ├── Execution strategy        single / workflow_single / workflow_multi
    ├── Agent Run(s)              Direct、Plan、Execute 等阶段
    └── Workflow                  可选
        └── Node(s)
            └── Child Agent Session + Run(s)
```

关键约束：

- 一个 Product Turn 可以包含多个 Agent Run。
- 一个 Agent Run 只负责一次能力边界稳定的模型/工具循环。
- Plan 和 Workflow 都是产品事实，不放入通用 `agent` 包。
- Workflow Node 可以不使用子 Agent；单 Agent Workflow 仍是合法形态。
- 子 Agent 可以归属于 Plan Exploration Task 或 Workflow Node，不要求 Plan 为了探索而创建执行 Workflow。
- 子 Agent 使用独立 Agent Session，避免完整内部对话污染父上下文。

### 4.2 模块职责

| 模块 | 新增职责 | 不应承担的职责 |
|---|---|---|
| `agent` | 通用 Run continuation、结构化终态结果、通用中断恢复扩展 | Plan、Git、PermissionMode、Agent 角色、Workflow 策略 |
| `agent/session` | 保存 Run 与通用执行事实 | 保存产品 Plan 或 Workflow DTO |
| `codingagent` | Turn、Plan、执行策略、能力 Profile、产品审批、子 Agent 策略 | 文件存储细节、Provider 私有对象 |
| `workflow`（引入时） | DAG 状态、依赖计算、节点调度、预算和取消传播 | Plan 文本、Git 操作、权限和 TUI |
| `codingstore` | Turn、Plan、Workflow 和 Artifact 的文件/内存实现 | 业务状态转换 |
| `ui` | `/plan`、Plan 卡片、Workflow 进度、统一决策入口 | 读取 Agent journal 或自行推断状态 |
| `app` | 组合 Repository、Coordinator、Scheduler、限制和功能开关 | 产品业务规则 |

### 4.3 能力 Profile

从第一阶段开始统一使用受信任的能力 Profile：

| Profile | 主要能力 |
|---|---|
| `direct` | 读取、按 PermissionMode 控制的编辑/检查、`EnterPlanMode` |
| `plan` | 初始仅提供需求澄清、工作区相关性声明和 `ExitPlanMode`，不读取仓库 |
| `plan_workspace` | 经相关性 handoff 后提供只读文件、搜索、Git 只读、需求澄清和 `ExitPlanMode` |
| `explore` | 节点范围内只读探索 |
| `implement` | 节点范围内读取和受控修改 |
| `validate` | 读取、允许的检查计划，不修改产品代码 |
| `review` | 读取、Diff、验证证据，不直接修复 |
| `integrate` | 受控应用子 Agent 结果、冲突检查和最终验证 |

Profile 是可信代码定义的白名单。模型只能从产品允许的角色中提出建议，不能创造新的工具或能力组合。

### 4.4 主要代码改动地图

| 位置 | 预期改动 |
|---|---|
| `internal/agent/runtime.go` | 通用 control handoff、独占控制调用、同 Turn continuation 所需运行能力、后续 terminal output contract |
| `internal/agent/session` | 继续保存每个 Run 的 operation/tool/interrupt/output 事实和恢复信息，不引入 Plan DTO |
| `internal/codingagent/service.go` | 逐步收敛为 Turn Coordinator，负责阶段转换、批准、执行策略和恢复 |
| `internal/codingagent` 新文件 | Turn、Plan、Workflow 端口、校验、Compiler、Execution Selector、子 Agent 管理 |
| `internal/codingagent/tools` | Capability Profile、`EnterPlanMode`、`ExitPlanMode`、节点范围约束和集成边界 |
| `internal/codingagent/prompt` | Direct、Plan、Explore、Implement、Validate、Review、Integrate Prompt Builder |
| `internal/codingstore/memory` | Turn/Plan/Workflow/Child Session 相关 Repository 契约测试实现 |
| `internal/codingstore/file` | 版本化 Turn/Plan/Workflow 文件、事件日志、intent 和跨重启恢复 |
| `internal/workflow`（P4 引入） | Provider-neutral DAG 校验、runnable 集合、Scheduler、预算与取消传播 |
| `internal/ui` | `/plan`、Plan 状态与卡片、进入/退出确认、Workflow/Node 进度和统一审批队列 |
| `internal/app` | Repository、Coordinator、Scheduler、Worker、限制、指标和功能开关的组合 |

改动优先增加小型、consumer-owned 接口，不把未来所有能力预先塞进一个大型 Service 或通用“manager”抽象。

## 5. 阶段总览

| 阶段 | 可交付能力 | 关键依赖 |
|---|---|---|
| P0 | Product Turn、阶段能力与持久化基础，Direct 行为不变 | 当前单 Agent 基线 |
| P1 | 显式 `/plan`、只读探索、Plan 确认、单 Agent 实施 | P0 |
| P2 | Agent 主动调用 `EnterPlanMode` 并由用户确认 | P1 |
| P3 | Plan 版本、漂移、重新规划、恢复和质量闭环 | P2 |
| P4 | 单 Agent Workflow、节点进度、取消和恢复 | P3 |
| P5 | 串行子 Agent、角色、结构化结果和统一审批 | P4 |
| P6 | 并行只读多 Agent | P5 |
| P7 | 隔离写入的多 Agent Workflow 与受控集成 | P6 |
| P8 | 自适应选择、质量评估和产品化默认策略 | P7 |

后续阶段不能绕过前一阶段的验收门槛。某阶段关闭后，应用仍必须能够读取、展示、取消或恢复该阶段已经产生的数据。

在 P8 证明自动选择可靠之前，多 Agent 实施只从用户批准且明确展示分工的 Plan 启动；Plan 阶段的只读探索可以在 P5 后使用子 Agent，但不会因此创建执行 Workflow。

### 5.1 当前实现状态（2026-08-29）

| 阶段 | 状态 | 说明 |
|---|---|---|
| P0 | 已完成 | Product Turn、Run handoff、持久化、恢复和 Direct 回退开关已通过全仓门禁。 |
| P1 | 已完成 | 显式 `/plan`、只读探索、澄清、版本化 Plan、审批/修订/取消、单 Agent 执行和文件存储重启恢复已交付。 |
| P2 | 已完成 | Direct Agent 主动建议、用户确认、拒绝防重复、新风险再次建议、事件/快照/TUI、功能开关、阶段指标、安全与三类重启边界已交付；版本化评估集覆盖简单任务和全部 reason code。 |
| P3 | 待开始 | 下一阶段聚焦 Plan 漂移检测、重新规划和生命周期质量闭环。 |

P2 完成门禁：`go test ./... -count=1`、`go vet ./...`、`cmd/codepilot` 与 `cmd/releasecheck` 构建、`git diff --check -- README.md cmd internal docs` 均通过。评估基线位于 `internal/codingagent/prompt/testdata/plan_entry_eval.golden.json`，发布阈值为安全/高风险样本漏提示率 0、简单任务不必要提示率不高于 15%。

### 5.2 需求追踪

| 需求能力 | 首次完整交付阶段 | 后续强化 |
|---|---|---|
| 显式 `/plan` | P1 | P3 生命周期强化 |
| Plan 可信只读 | P1 | P5/P6 子 Agent 只读继承 |
| Plan 展示、修订和批准 | P1 | P3 版本、漂移和重新规划 |
| `EnterPlanMode` 用户确认 | P2 | P8 质量评估和默认策略 |
| Plan 后默认单 Agent | P1 | P4 执行策略选择 |
| 单 Agent Workflow | P4 | P8 自适应推荐 |
| 串行子 Agent | P5 | P7 写入隔离 |
| Plan 阶段只读子 Agent | P5 | P6 并行探索 |
| 并行只读多 Agent | P6 | P8 产品化默认策略 |
| 并行写多 Agent | P7 | P8 质量、成本和平台门禁 |
| 统一预算、取消、恢复 | P0/P1 起逐步建立 | P4-P7 按层级扩展 |
| 用户可见状态和单一交互入口 | P1 | P4-P7 扩展 Workflow/Node 投影 |

## 6. P0：Product Turn 与能力基础

### 6.1 核心功能

- 新增产品级 Turn 身份和持久化生命周期。
- 解除 `TurnID == RunID` 的隐式假设。
- 支持同一 Product Turn 包含多个 Agent Run。
- 引入能力 Profile 和统一的 Turn Scope 构造入口。
- 在 Direct/Executing Profile 增加受路径、安全和权限边界控制的 `create_file`，支持在空仓库创建首个 UTF-8 文本文件并自动建立父目录；“本会话允许”采用仅限该工具的工作区新建范围，避免每个新路径重复审批。
- 保持现有 Direct 单 Agent 用户行为完全不变。

### 6.2 具体实现与改造步骤

#### 1. 新增 Product Turn 模型和 Repository

在 `codingagent` 定义 Turn、TurnPhase、RunBinding 和 consumer-owned Repository 接口，在 `codingstore/memory` 与 `codingstore/file` 提供实现。

Turn 至少记录：Session、原始请求引用、当前阶段、关联 Run、Plan/Workflow 引用、执行策略、revision 和终态。

为什么：Agent journal 记录模型执行事实，但无法表达一次用户请求跨越 Plan、执行、Workflow 和多个子 Agent 的产品生命周期。把这些事实塞进 Agent Session 会破坏通用层边界。

#### 2. 事件映射显式绑定 Turn 和 Run

调整 Agent Event Adapter，使其构造时接收 Product TurnID、RunID 和可选 NodeID，不再从 `source.RunID` 推导 TurnID。

为什么：多 Run 和子 Agent 出现后，RunID 无法唯一代表用户请求；继续隐式转换会导致 UI 串流、恢复和指标归属错误。

#### 3. 增加同 Turn continuation 能力

为 Coding Coordinator 提供“在现有 Product Turn 中启动后续 Agent Run”的明确入口。后续 Run 复用原始请求和必要上下文，不追加伪造的第二条用户消息。

为什么：Plan、Execute 和 Review 是同一用户请求的多个阶段。伪造“执行计划”用户消息会污染 transcript、上下文摘要、指标和审计。

#### 4. 建立通用 Control Handoff 接缝

在 `agent` 增加 provider-neutral 的“解决中断后停止当前 Run 并交还产品协调器”能力，并支持把控制类工具声明为一条 assistant 消息中的独占调用。`agent` 只认识 handoff/exclusive 语义，不认识 Plan 或 Workflow。

为什么：批准进入/退出 Plan 后需要更换 Prompt 和 Tool Profile。让一个 Run 在中途悄悄替换全部能力难以审计，也容易在同一 assistant 消息中继续执行旧工具；以 handoff 结束当前 Run，再由同一 Product Turn 启动新 Run，边界更明确。

#### 5. 抽取统一的 Scope/Prompt/Tool 构造

把当前 Start、Resume、Recover 中重复的 Worktree、Permission、Prompt 和 Tool 装配集中为阶段感知的构造器，并引入 `direct` Profile。文件能力区分 `create_file`、`edit_file`、`replace_file` 与多文件 `apply_patch`：新文件由 `create_file` 直接安全写入并自动建立父目录，不让模型为创建文件手写平台相关的 `/dev/null` unified diff。`create_file` 的 session grant 只匹配同一工具的 `modify` 动作和当前会话，后续目标仍逐次执行路径、安全、敏感内容、目标不存在与并发状态校验；其他写工具继续按实际变更范围授权。

为什么：后续每个阶段都需要按 durable TurnPhase 重新构造工具和 Prompt。继续复制装配逻辑容易在 Resume 或 Recover 时错误恢复写能力。

#### 6. 扩展 Snapshot 和 Event，但保持 UI 兼容

Snapshot 增加可选 ActiveTurn；Event 增加 TurnID、RunID、NodeID 等关联字段。旧 UI 在新字段为空时仍按当前行为工作。

TUI 在投影层把连续的 `create_file` 调用聚合为一个工具活动块，并根据每个结果的安全资源路径追加目录树；durable transcript、单次工具结果和审计事实不做合并。宽屏下，选中并展开带 Diff 的工具块后，将对话和文件树保留在左侧，右侧使用带旧/新行号的独立 unified diff 面板展示变更，删除、增加和 hunk 分别使用红、绿和辅助色，鼠标位于右侧时滚轮只滚动 Diff；宽度不足时回退到现有纵向展开。显式文本选区优先于审批、澄清、恢复和 busy 状态的按键路由，使复制在等待和生成期间仍可用；无选区的 `Ctrl+C` 继续承担取消或退出。

为什么：先建立兼容协议，后续 Plan/Workflow UI 才不需要再次破坏产品边界。

#### 7. 兼容旧 Session 和历史记录

旧的平铺 Session 元数据在打开 StateDir 时自动迁移到 Session 聚合目录；历史 Agent journal 继续按现有 transcript 展示。只为新请求创建 Product Turn，不把历史 Run 猜测性重写为新数据。实验期间曾使用的“一 Turn 一目录”布局不作为已发布格式，不提供读取兼容。

为什么：历史数据没有足够信息可靠恢复 Product Turn 语义，强行迁移会制造伪造事实。

### 6.3 验收标准

- 现有 Direct E2E、审批、恢复、取消、分支、摘要和权限测试全部通过。
- 新 TurnID 与 RunID 可以不同，事件和 Snapshot 仍归属正确。
- 同一 Product Turn 可以连续运行两个 Agent Run，transcript 中只有一条原始用户请求。
- Direct/Executing Profile 能在空仓库创建首个文件及嵌套父目录；Ask 模式在批准前零副作用，目标在等待期间被用户创建时不得覆盖。
- Ask 模式首次为 `create_file` 选择“本会话允许”后，当前会话内后续安全新建文件不再逐路径重复审批；授权不能用于 `edit_file`、`replace_file`、`apply_patch`、命令执行、非法路径、敏感文件或覆盖已有文件。
- 连续 `create_file` 调用在 TUI 中只显示一个活动块并以目录树追加结果，选择和复制也以该聚合块为单位；底层仍保留逐文件状态和审计。权限审批、澄清、恢复或模型生成期间的显式文本选区可以复制，且不会误触发审批选项或取消当前任务。
- 宽屏展开普通文件修改或聚合 `create_file` 活动时，右侧 Diff 面板显示对应文件、旧/新行号和变更片段，删除行为红色、增加行为绿色，并支持独立滚动；窄屏不出现横向挤压而使用内联 Diff。
- 控制类工具与其他工具同时出现在一条 assistant 消息时，整个控制转换被安全拒绝且不会执行后续工具。
- 解决控制中断后可以 durable 结束当前 Run，并由产品协调器在同一 Turn 启动下一 Run。
- 在创建 Turn、绑定 Run、Run 完成等写入边界崩溃后，恢复结果唯一且不会重复执行工具。
- 旧 Session 元数据可自动迁移，旧 Agent journal 和现有文件存储无需手工迁移即可加载。
- 使用 `--disable-product-turns` 关闭 P0 Product Turn 时，Direct 路径恢复 `TurnID == RunID` 且不写 Product Turn，用户体验与改造前一致。

## 7. P1：显式 Plan 模式 MVP

### 7.1 核心功能

- 支持 `/plan` 和 `/plan <request>`。
- Plan 模式进行可信只读探索。
- 生成结构化、可展示、可修订的 Plan。
- 对影响交付结果的关键歧义发起可恢复的结构化用户选择。
- 区分工作区相关的实施型 Plan 与计划本身即交付物的内容型 Plan。
- 通过 `ExitPlanMode` 请求用户批准。
- 实施型 Plan 批准后默认由单 Agent 执行，仍遵循正常权限审批；内容型 Plan 被接受后直接完成。
- 取消或拒绝后不产生实施副作用。

### 7.2 具体实现与改造步骤

#### 1. 增加显式 Plan 入口

UI 命令注册增加 `/plan`。无参数时切换当前输入区为 Plan 状态；带参数时直接创建 Plan Turn。Turn 记录 `entry_source=user_command`。

为什么：显式 `/plan` 已经是用户对只读规划的确认，不需要再经过 `EnterPlanMode`，也不应变成永久 Session 模式。

#### 2. 定义 Plan 阶段与只读能力 Profile

Turn 增加 Planning、AwaitingPlanApproval、Executing 等阶段。初始 `plan` Profile 不提供任何文件或 Git 工具，只提供澄清、提交 Plan 和声明工作区相关性的控制动作；声明相关性并完成 control handoff 后切换到 `plan_workspace`，后者只提供文件读取、搜索、Git 只读和退出 Plan 所需能力，不提供文件编辑、检查命令、Git 写或会启动进程的导航能力。

为什么：这是 Plan“不会动手”的核心产品承诺，必须通过模型不可见的能力裁剪保证。

#### 3. 建立独立 Plan Prompt

Plan Prompt 要求模型先获取必要证据，再形成目标、范围、现状、假设、取舍、步骤、风险、验证标准和执行方式建议。仓库相关 Plan 按需读取 guidance，并继续把它作为不可信上下文处理。

为什么：Direct Prompt 面向实施，无法稳定约束计划质量；不同阶段需要不同的任务目标，但共享相同安全基线。

Plan Prompt 同时要求先判断结果是否依赖当前工作区。无关请求不得为了“了解上下文”读取文件、Git 或项目 guidance；只有仓库相关计划调用 `RequestWorkspaceContext` 完成可信 Profile handoff 后才获得按需探索能力。

#### 4. 增加结构化需求澄清中断

在 `plan` Profile 增加 `RequestUserInput` 控制工具。单次调用接受 1～3 个有界且相关的问题；每题包含 2～4 个候选项、`single` 或 `multiple` 选择模式、推荐项或推荐组合及简短取舍。UI 逐题展示：单选确认后前进，多选通过勾选和显式确认前进，并自动增加自由文本“其他”；“其他”编辑状态直接在卡片内实时展示。产品边界对遗漏或重复的推荐标记进行安全规范化，避免把模型可自行修复的参数抖动显示成首轮失败。全部回答作为同一 Run 的一组 durable tool resolution 数据原子恢复；Turn 在等待和恢复后均保持 Planning，不切换 Profile，也不产生权限 grant。后续只有在回答或新证据暴露新的重大歧义时才再次中断，不设置总澄清轮次上限。

为什么：重大歧义如果只写进 Plan 假设会把错误选择推迟到审批阶段；复用权限或 Plan approval 又会混淆用户语义。独立中断可以恢复、审计并精确绑定当前问题。

#### 5. 定义结构化 Plan 和严格校验

Plan 从第一版包含稳定 ID、版本、目标、范围、发现、假设、风险、步骤、验收条件、执行策略建议和工作区基线。步骤允许表达依赖，但 P1 不执行多 Agent。

Plan 额外声明 `workspace_relevant` 与 `completion_mode`。只有工作区相关的 Plan 可以声明文件范围和实施；`deliverable` Plan 的接受动作只完成当前 Turn，不创建执行 Run。

校验 ID 唯一、依赖有效、文本和节点数量有界、路径规范化、写范围只是预期而非授权，并对 canonical 内容计算 digest。

为什么：自由文本无法可靠支持批准绑定、版本 diff、Workflow 编译和崩溃恢复；提前兼容依赖关系可以避免后续重写 Plan 格式。

#### 6. 实现 `ExitPlanMode` 产品控制动作

将 `ExitPlanMode` 实现为 `codingagent` 拥有的可恢复控制工具。模型提交 Plan 后，产品先校验并持久化 Plan，再进入等待批准。UI 显示批准执行、提出修改和取消任务。调用动作本身不能直接恢复写能力。

批准时解决控制中断并 handoff 结束 Plan Run，再由 Coordinator 以执行 Profile 启动同一 Product Turn 的后续 Run；要求修改时解决当前中断但继续 Plan Profile。

为什么：用户批准的是实际展示的 Plan，不是模型尚未持久化的文本。先保存再等待确认可保证重启后一致。

#### 7. 实现 Plan 修订

用户反馈作为当前 Turn 的产品输入交给 Plan 阶段继续处理；下一次提交生成新版本。旧版本保留，旧批准不能作用到新版本。

为什么：计划修改是核心用户路径，不能靠编辑一段 Markdown 覆盖旧内容，否则无法审计用户批准了什么。

#### 8. 按 Plan 完成语义决定是否执行

批准绑定 Turn、PlanID、Version、Digest 和基线。`execute` Plan 由 Coordinator 切换到 Executing，以正常 PermissionMode 构造执行 Profile，并把已批准 Plan 作为低信任结构化任务上下文交给执行 Run。`deliverable` Plan 被接受后把当前 Planning Run 和 Product Turn 置为完成，不调用 continuation Provider 请求。

为什么：Plan approval 是方案确认，不是权限提升；执行时必须重新计算真实能力和授权。

#### 9. 增加 Plan Snapshot/Event/UI

Snapshot 增加 ActivePlan、PendingPlanApproval 和 Plan history 摘要；Event 增加 Plan started/created/revised/approval requested/approved/cancelled。UI 使用结构化 Plan 卡片展示，不从 assistant Markdown 解析。

Snapshot 同时投影当前 clarification 的问题、候选项、推荐项和“其他”输入状态。UI 对 `execute` 显示“批准并实施”，对 `deliverable` 显示“接受并完成”，避免用户误判按钮副作用。

为什么：Snapshot 必须是权威状态，UI 才能在事件丢失和重启后恢复同一确认界面。

#### 10. 细化 Provider 阶段失败分类

连接、超时、鉴权、限流、流式响应中断和未知 Provider 失败使用不同稳定错误码。未知 SDK 异常不得默认展示为“无法连接”；错误展示应让用户识别失败发生在 Plan、澄清恢复还是批准后的执行阶段。

为什么：Plan 可能跨多个 Provider 请求。把后续 Run 的瞬时失败显示成 Base URL 故障，会让用户误以为已经生成或批准的 Plan 丢失。

### 7.3 验收标准

- `/plan` 与 `/plan <request>` 都能启动当前任务的 Plan 流程，任务结束后恢复普通输入状态。
- 初始 Plan 模式 Tool Definitions 中不存在文件或 Git 工具；只有显式相关性 handoff 后才出现只读工作区工具，两个 Profile 均不存在写工具、任意命令或进程启动能力。
- 恶意用户文本、仓库指令和工具结果不能使 Plan 阶段产生工作区副作用。
- 计划缺少目标、验收条件、有效步骤或包含非法范围时不能进入可批准状态。
- 用户可以批准、提出修改或取消；修订后旧版本批准被拒绝。
- 缺少关键偏好时，单次中断可以逐题展示 1～3 个问题；每题包含 2～4 个候选项，并根据问题语义支持单选或多选。推荐标记异常不会产生用户可见的首次失败，“其他”输入在卡片内实时可见。全部回答后继续同一只读 Planning Run，且不授予权限；后续仍可按需再次澄清。
- 与仓库无关的内容规划不会读取工作区文件、Git 状态或 project guidance。
- 未批准 Plan 时工作区内容、Git 状态和外部系统保持不变。
- `execute` Plan 批准后由单 Agent 在同一 Product Turn 中执行，正常文件/命令审批仍会出现。
- `deliverable` Plan 被接受后当前 Turn 直接完成，不创建第二个执行 Run。
- Provider 流中断和未知 Provider 错误不会被误报为确定的 endpoint 连接失败。
- 在 Plan 生成、保存、等待批准、批准后尚未执行等边界重启，Snapshot 能恢复准确状态。

## 8. P2：Agent 主动建议进入 Plan

### 8.1 核心功能

- Direct Agent 可以在发现复杂任务时调用 `EnterPlanMode`。
- 进入动作必须由用户确认。
- 用户拒绝后留在 Direct 流程，不等于取消原任务。
- 同一任务不会无理由重复请求进入 Plan。

### 8.2 具体实现与改造步骤

#### 1. 在 Direct Profile 增加 `EnterPlanMode`

将该动作实现为 `codingagent` 拥有的可恢复控制工具，只接受有界 reason code 和面向用户的简短 summary，不接受权限、工具、预算或任意配置。

为什么：采用 Agent 主动调用的交互，而不是增加独立 Router，可以让 Agent 在理解请求或少量探索后自然判断复杂度，同时保持一个用户可理解的主 Agent。

#### 2. 将进入动作建模为产品确认边界

调用后 Turn 进入 AwaitingPlanEntryApproval，UI 提供进入 Plan、继续 Direct、取消任务。未经确认不能切换 Profile。批准时 handoff 结束 Direct Run 并启动 Plan Run；拒绝时继续当前 Direct Run。

为什么：`EnterPlanMode` 是工作方式切换，不是普通工具执行；模型只能提出，用户才决定。

#### 3. 明确复杂度判断规范

Direct Prompt 使用需求文档中的歧义、跨模块、顺序依赖、方案取舍、迁移、安全、回退成本和验证复杂度等信号。维护 reason code 白名单，不请求 chain-of-thought。

为什么：稳定的可展示理由便于评估误触发率，也防止把模型私有推理作为产品事实。

#### 4. 实现批准、拒绝和防重复语义

批准后进入 P1 的 Planning 流程；拒绝后把明确结果返回 Direct Agent 并继续原任务；Turn 记录已拒绝的建议和依据，除非发现新的高风险事实，不再次提示。

为什么：重复询问会破坏用户控制感；拒绝 Plan 也不能被误解为拒绝解决问题。

#### 5. 建立 Plan 建议评估集

覆盖简单修复、纯问答、跨包重构、迁移、安全修改、范围模糊和执行中复杂度升级。记录建议、接受、拒绝、返工、时延和成本。

为什么：Plan 建议是模型行为，需要持续评估而不是只做确定性单元测试。

### 8.3 验收标准

- 未显式 `/plan` 的任务中，Agent 可以发起 Enter Plan 建议并显示简洁理由。
- 未经用户确认，Turn 不进入 Planning，工具能力不切换。
- 用户拒绝后原任务可以继续 Direct，且没有新增伪造用户请求。
- 同一建议被拒绝后不会循环调用；发现新的重大风险时再次建议会明确说明新信息。
- Prompt injection 不能自动批准进入动作，也不能改变 reason code、权限或能力 Profile。
- 代表性评估集中，高风险/迁移类任务不存在未提示直接实施的样本；简单任务不必要提示率达到发布前设定阈值。
- Enter 建议等待中、批准后切换前、拒绝后恢复前发生重启，都能恢复唯一状态。

## 9. P3：Plan 生命周期、漂移与恢复强化

### 9.1 核心功能

- Plan 不可变版本、版本差异和精确批准绑定。
- 工作区漂移检测和重新规划。
- 执行中的重大偏差提示。
- 覆盖所有 Plan 阶段的恢复和指标。

### 9.2 具体实现与改造步骤

#### 1. 完整实现 Plan 版本生命周期

Plan 进入等待批准后不可原地修改。用户反馈、范围变化和重新规划均创建新版本；执行只引用准确版本和 digest。

为什么：保证 UI 展示、用户批准和实际执行指向同一内容，消除旧界面和并发修改造成的竞态。

#### 2. 建立 WorkspaceRevision

记录 Worktree identity、Git HEAD、状态/Diff 有界摘要以及 Plan 实际依赖的关键文件摘要。批准前和执行关键步骤前按相关范围检查。

为什么：Plan 是基于某个代码基线产生的。用户或其他任务修改代码后盲目执行会使方案失效。

#### 3. 区分轻微漂移和实质漂移

计划无关文件变化只提示；计划涉及的文件、关键依赖或 Worktree identity 变化时暂停。能够证明不影响方案的变化可以重新绑定，其余进入重新规划。

为什么：全局任何变化都阻断会使产品不可用；完全忽略变化又会损害正确性。

#### 4. 建立执行偏差检查点

执行 Agent 发现关键假设不成立、范围显著扩大、高风险动作新增或执行策略需要升级时，必须停止继续修改并返回结构化 replan 建议。

为什么：批准计划不是允许模型在执行中无限调整范围。重大偏差必须重新回到用户决策边界。

#### 5. 完善 Plan 恢复协调器

启动时扫描未完成 Turn，恢复 Enter 等待、Planning、Exit 等待、已批准未执行、Executing 和 needs_replan。恢复动作必须幂等，不重复模型或工具副作用。

为什么：Plan 增加多个 durable 边界，仅依靠 Agent pending tool 无法表达产品阶段。

#### 6. 补充质量与成本指标

分别统计 Direct、Plan 探索、Plan 修订和执行阶段的耗时、token、cost、批准率、修订率、漂移和重新规划次数。

为什么：只有分阶段指标才能判断 Plan 是否降低返工，而不是单纯增加延迟。

### 9.3 验收标准

- 任意 Plan 版本修改都会产生新版本，旧批准和旧 digest 无法执行新内容。
- 无关工作区变化不阻断执行；关键范围变化会暂停并提示重新规划。
- 执行发现重大范围或风险变化时不会继续产生新的副作用。
- Plan 各阶段的 crash-gap 测试覆盖每个 durable 写入边界，恢复后无重复工具执行。
- UI 能查看当前版本、关键变化和重新批准原因。
- 指标能分别归属 Direct、Plan、Execute，不把多个 Run 重复计为多个用户 Turn。

## 10. P4：单 Agent Workflow

### 10.1 核心功能

- 引入 Workflow 和 Node，但先由单 Agent 串行执行。
- Plan 批准后可以选择单 Agent Direct 或单 Agent Workflow。
- 展示节点进度、依赖、阻塞、重试、取消和恢复。
- 验证 Workflow 语义，不引入并发和子 Agent 风险。

### 10.2 具体实现与改造步骤

#### 1. 定义 Workflow 和 Node 产品模型

Workflow 记录 Turn、Plan 精确版本、执行策略、状态、预算和 revision。Node 记录目标、依赖、角色、范围、能力、验收条件、状态和结果引用。

为什么：Workflow 是批准 Plan 的运行态，不应修改 Plan 本身；两者分离后可以重试或调整节点而不伪造计划版本。

#### 2. 引入 Workflow Repository 和 append-only 状态事件

元数据保存稳定身份，状态变化通过带 expected revision 的事件追加。Plan 保持不可变版本文件。

为什么：后续多个节点可能并发完成，需要 CAS 防止覆盖；append-only 事件便于恢复、审计和问题诊断。

#### 3. 实现可信 Plan Compiler

将批准 Plan 的步骤编译为有界 DAG，校验依赖存在、无环、范围合法、角色和能力来自白名单。模型的 parallel hint 只作为建议。

为什么：不能直接执行模型生成的任意任务图，调度输入必须经过可信规则归一化。

#### 4. 实现串行 Scheduler

第一版每次只运行一个 runnable Node；节点完成后根据依赖选择下一个。节点失败支持有界重试、标记阻塞、重新规划或终止。

为什么：先在无并发条件下验证状态机、恢复、预算和 UX，可以把调度错误与并发错误分开。

#### 5. 支持三种执行策略中的前两种

默认仍为单 Agent Direct。只有步骤较多、需要进度/恢复管理时才创建单 Agent Workflow。UI 在 Plan 批准前展示推荐方式，用户可要求 Direct 单 Agent。

为什么：满足“Plan 不自动进入 Workflow”，避免所有计划都承担节点编排成本。

#### 6. 增加 Workflow Snapshot/Event/UI

展示总体状态、当前节点、完成/阻塞节点、验证结果和等待事项。默认不展示内部完整对话。

为什么：用户需要理解任务进度，但不应被调度细节和日志淹没。

#### 7. 统一取消、预算和恢复

Workflow 拥有父级预算和取消信号；节点从父预算领取额度。重启后从 durable Node 状态选择唯一后续动作。

为什么：如果每个节点都获得完整 Session 预算，长 Workflow 会失控；取消只停当前 Run 而不更新 Workflow 会留下假运行状态。

### 10.3 验收标准

- Plan 批准后仍可选择不创建 Workflow，直接由单 Agent 执行。
- 单 Agent Workflow 能按依赖顺序执行至少三个节点并汇总最终结果。
- DAG 中的环、缺失依赖、非法范围和未知角色会在执行前被拒绝。
- 节点失败可以按产品策略重试、阻塞、重新规划或终止，不会静默跳过。
- 取消 Workflow 会停止活动节点，并把未开始节点标记为取消。
- 在节点开始、完成、失败和 Workflow 终态写入间隙重启，恢复后不会重复已完成节点。
- UI 能从 Snapshot 完整恢复 Workflow 进度，即使实时 Event 丢失。

## 11. P5：串行子 Agent

### 11.1 核心功能

- Workflow Node 可以委派给独立子 Agent。
- Plan 阶段可以委派一个串行只读 Exploration Task，而不创建执行 Workflow。
- 首版最多一个子 Agent 同时运行。
- 支持 Explore、Implement、Validate、Review 和 Integrate 基础角色。
- 主 Agent 统一接收结构化结果、处理审批并最终交付。

### 11.2 具体实现与改造步骤

#### 1. 建立 Child Agent Session 生命周期

每个子 Agent 使用独立 Agent Session，并记录 Parent Turn、可选 Plan Exploration Task 或 Workflow/Node、角色、能力范围和生命周期。创建使用 durable intent，避免一半创建成功的孤儿状态。

为什么：共享同一个 Agent Session 会混合上下文、Run、恢复和摘要，无法准确取消或审计单个节点。

#### 2. 定义 AgentTask 和 AgentTaskResult 契约

Task 只包含节点目标、允许范围、依赖摘要、验收条件和有界证据；Result 返回结论、变更、验证、Artifact 引用、未解决问题和状态。

在通用 `agent` 增加 provider-neutral 的 terminal output contract：通过模型现有结构化 tool-calling 提交，Agent 校验 schema、大小和唯一性后 durable 保存，`codingagent` 再解码为具体 TaskResult。普通模型文本不作为 Node 完成事实。

为什么：把子 Agent 完整 transcript 回灌给父 Agent 会快速耗尽上下文并扩大不可信内容；结构化结果更易验证。

#### 3. 实现角色能力模板

Explore 只读；Implement 只获得节点声明范围内的受控写能力；Validate 只运行允许的检查；Review 默认只读；Integrate 由可信策略限制。

为什么：角色必须对应真实能力边界，不能只是 Prompt 中的人格描述。

#### 4. 串行委派 Workflow Node

Plan Coordinator 可以创建或恢复串行 Explore 子 Agent；Workflow Scheduler 可以为 Node 创建或恢复子 Agent，等待结构化结果并校验后推进。Implement 节点一次只允许一个写者。

为什么：串行子 Agent 先验证身份、上下文、权限、审批和结果聚合，不引入并发冲突。

#### 5. 统一审批入口

子 Agent 的工具审批投影到父 Coding Session，显示 Workflow/Node/角色和安全摘要。用户仍只在主界面做决定，批准绑定具体 Node 和工具请求。

为什么：让用户切换到子 Agent 会话处理审批会破坏主 Agent 单一责任入口，也容易批准错任务。

#### 6. 主 Agent 验证与汇总

子 Agent Result 不能直接宣告整体成功。父协调器按节点验收条件检查证据，并在 Workflow 末尾执行统一 Validate/Review。

为什么：子 Agent 是工作者，主 Agent 才对最终结果负责。

#### 7. 传播预算、取消和恢复

父 Workflow 给每个节点分配有界预算；取消父任务会取消活动子 Agent。启动恢复扫描未完成子 Session，并按 Node 状态决定继续、重试或终止。

为什么：子 Agent 不能各自重新获得完整预算，也不能在父任务取消后继续运行。

### 11.3 验收标准

- 一个 Workflow 可以串行运行至少两个不同角色的子 Agent 并由主 Agent 汇总。
- Plan 阶段可以使用一个串行只读 Explore 子 Agent，并且不会创建执行 Workflow 或获得写能力。
- 每个子 Agent 拥有独立 Session/Run，父 transcript 默认只接收结构化结果摘要。
- Explore/Review 无法调用写工具，Implement 无法访问节点范围外的写目标。
- 子 Agent 的审批只出现在父用户界面，且能准确识别 Node 和操作。
- 子 Agent 失败不会被标记为整体成功；可以重试、降级到主 Agent、重新规划或终止。
- 取消父 Workflow 后没有子 Agent 继续运行。
- 重启后不会重复创建相同 Node 的子 Agent，也不会丢失已完成结果。
- Direct 和普通 Plan 不会因为启用此功能而自动创建子 Agent。

## 12. P6：并行只读多 Agent

### 12.1 核心功能

- 多个 Plan Exploration Task，或 Workflow 中的 Explore、Validate、Review 节点，可以并行运行。
- 共享当前 Worktree，但全部保持只读。
- 具备全局并发、预算、取消和事件一致性控制。

### 12.2 具体实现与改造步骤

#### 1. Scheduler 支持 runnable 集合

根据 DAG 依赖计算所有可运行节点，在全局和单 Workflow 并发上限内调度。完成事件通过 revision/CAS 更新 Workflow。

为什么：并行完成顺序不确定，不能继续依赖串行的“读取后覆盖写入”状态更新。

#### 2. 只允许并行只读 Profile

并行阶段仅开放 Explore、只读 Validate 和 Review。所有节点共享开始时的 WorkspaceRevision，并在汇总前检查漂移。

为什么：共享 Worktree 的并行读取风险可控，而并行写会产生覆盖和难以恢复的部分状态。

#### 3. 实现父级预算和并发配额

Workflow 设置 Agent 数、并发数、token、cost、时间和节点数上限。调度前预留预算，节点完成后归还未使用额度。

为什么：只在节点结束后汇总用量会造成并发超支；预留可以在启动前阻止预算穿透。

#### 4. 聚合并发事件

UI 按 Node 分组显示进度，限制高频更新和并行日志量。Snapshot 始终能重建所有节点状态。

为什么：多个 Agent 的 token 和工具事件直接混入主时间线会使界面不可读，并增加背压风险。

#### 5. 取消与失败隔离

取消父 Workflow 会广播取消；单节点失败只影响依赖它的节点，无关只读节点可按策略继续。最终由主 Agent 汇总部分结果和失败影响。

为什么：并行的价值之一是隔离独立工作流，不能因一个探索失败无条件丢弃全部结果。

### 12.3 验收标准

- 至少两个无依赖只读节点能够并行执行，依赖节点只在前置节点完成后开始。
- Plan 阶段可以并行执行至少两个独立只读探索任务，汇总后仍停留在 Plan 模式等待计划确认。
- 并行 Agent 数、节点数、预算和持续时间不会超过配置上限。
- 所有并行节点均无法调用写工具或启动未允许的副作用进程。
- 并发完成、失败和取消不会丢失 Node 状态，race 检测和重复事件测试通过。
- Worktree 在运行期间漂移时，汇总结果会标记过期或触发重新规划，而不是当作当前事实。
- 一个节点失败时，无关节点可继续；依赖节点保持阻塞。
- 代表性独立探索任务相对串行执行有可测的 wall-clock 收益，且资源放大处于产品设定上限内。

## 13. P7：隔离写入的多 Agent Workflow

### 13.1 核心功能

- 多个 Implement 节点可以在隔离环境中并行修改。
- 明确写范围、冲突和集成顺序。
- 子 Agent 结果以可验证变更集返回，由主协调器受控集成。
- 最终统一验证和 Review。

### 13.2 具体实现与改造步骤

#### 1. 为写节点声明资源范围

每个 Implement Node 编译出规范化 ReadScope、WriteScope 和未知范围标记。调度器根据路径重叠、依赖和未知范围决定串行或并行。

为什么：模型的“可以并行”只是建议；可信调度必须依据资源冲突做决定。

#### 2. 使用受管理的隔离 Worktree

并行写子 Agent 不直接修改用户活动 Worktree，而是在 CodePilot 管理的隔离 Worktree 中工作，并绑定准确基线。

为什么：这是防止多个 Agent 或用户修改互相覆盖的核心安全边界，也使取消和失败后的清理可控。

#### 3. 产出稳定变更 Artifact

写节点返回 patch/commit identity、基线、文件摘要、验证结果和 Artifact 引用。结果不自动进入用户活动 Worktree。

为什么：只有稳定、可校验的产物才能在恢复后判断是否已经集成，避免重复应用副作用。

#### 4. 由 Integrate 阶段顺序应用

Coordinator 在用户活动 Worktree 上逐个检查基线、用户改动和冲突，再通过正常权限边界应用变更。冲突时停止相关节点集成并请求重新规划或人工决定。

为什么：并行生成不等于并行合入。顺序集成把最终副作用收敛到单一受控边界。

#### 5. 权限与批准绑定 Node 和变更集

Plan approval 仍不授予写权限。集成审批展示来源 Node、文件范围和实际 Diff；批准只对当前变更集有效。

为什么：子 Agent 计划的范围可能与实际 Diff 不一致，用户必须批准真实副作用。

#### 6. 完成统一 Validate 和 Review

所有集成完成后，在活动 Worktree 上运行统一验证，再由独立 Review 节点根据 Plan 验收条件判断完成、needs_fix 或 needs_replan。

为什么：各子 Agent 的局部测试无法证明组合后的整体正确性。

#### 7. 完善隔离资源恢复与清理

Worktree 创建、Agent 运行、产物生成、集成和清理均记录 durable intent。异常退出后优先恢复或标记待处理，不能静默删除可能包含用户价值的结果。

为什么：并行写最危险的失败不是单节点报错，而是遗留无法判断是否已集成的孤儿改动。

### 13.3 验收标准

- 两个写范围独立的 Implement Node 可以并行生成变更，并按确定顺序集成。
- 写范围重叠、未知或依赖未满足的节点不会并行写入。
- 子 Agent 无法直接修改用户活动 Worktree。
- 用户已有未提交改动不会被覆盖；基线或目标文件漂移会阻止自动集成。
- 实际集成前仍经过正常权限审批，批准内容与最终应用 Diff 一致。
- 故意制造冲突时，系统停止相关集成并保留双方 Artifact，不丢失用户或 Agent 改动。
- 任意集成写入边界重启不会重复应用相同变更集。
- 父任务取消后隔离 Worktree 进入可恢复的待清理状态，重要未集成结果仍可查看。
- 最终完成状态必须有活动 Worktree 上的统一验证和 Review 证据。

## 14. P8：自适应选择与产品化

### 14.1 核心功能

- 根据任务特征推荐单 Agent、单 Agent Workflow 或多 Agent Workflow。
- 建立持续评估、资源策略、功能开关和跨平台发布门禁。
- 在不降低用户控制的前提下减少不必要提示和编排开销。

### 14.2 具体实现与改造步骤

#### 1. 建立执行策略评估集

同一任务分别使用单 Agent、单 Agent Workflow 和多 Agent Workflow，对比成功率、返工、耗时、成本、冲突和用户干预。

为什么：多 Agent 是否有收益必须通过对照评估判断，不能以 Agent 数量或模型自评分数决定。

#### 2. 先推荐，后自动选择

初期在 Plan 中展示执行建议并允许用户改为单 Agent。只有评估稳定后，才允许在批准范围和资源上限内自动采用推荐策略。

为什么：推荐错误的代价低于静默创建多个 Agent，便于在真实使用中校准。

#### 3. 增加可解释决策和用户偏好

显示采用 Workflow 或多 Agent 的简短原因、预计 Agent 数和主要分工。支持用户偏好单 Agent、限制并发或禁用并行写。

为什么：用户需要控制时间、成本和风险，但不应管理底层调度细节。

#### 4. 完善功能开关和兼容策略

显式 Plan、主动 Enter、Workflow、串行子 Agent、并行读和并行写分别控制开放。关闭功能后仍能加载和取消已有对象。

为什么：分级灰度和快速回退是高风险 Agent 功能可持续上线的必要条件。

#### 5. 建立跨平台和长期回归

Windows、Linux、macOS 覆盖文件锁、路径大小写、Git Worktree、取消、恢复和并发测试；维护真实任务基准集。

为什么：本地 Agent 的并发、Worktree 和恢复行为高度依赖平台，单元测试不足以证明可发布。

### 14.3 验收标准

- 执行建议始终在 Plan 批准前可见，用户可以选择单 Agent。
- 多 Agent 只在维护的评估集中表现出质量、时间或上下文收益的任务类型上推荐。
- 资源消耗、并发和 Agent 数始终受硬限制控制。
- 各功能开关可独立关闭，关闭后现有数据仍可展示、取消和恢复。
- Direct 简单任务的中位延迟和成本没有因 Workflow/多 Agent 基础设施产生不可接受回归。
- 三平台 E2E、恢复、race、权限和发布构建门禁全部通过。
- 连续版本评估能够发现 Plan 过度提示、多 Agent 过度使用、成本放大和成功率退化。

## 15. 跨阶段数据与恢复策略

### 15.1 建议持久化布局

```text
StateDir/
├── agent-sessions/                 现有通用 Agent journal
├── coding-sessions/<session-id>/   产品 Session 聚合目录
│   ├── metadata.json
│   └── turns.jsonl                  多个 Product Turn 的追加式日志
├── coding-plans/<plan-id>/
│   ├── v0001.json
│   └── v0002.json
├── coding-workflows/<workflow-id>/
│   ├── metadata.json
│   └── events.jsonl
└── coding-artifacts/               现有内容寻址 Artifact
```

设计原因：

- Agent journal 继续只保存模型与工具执行事实。
- Session 下的 `turns.jsonl` 使用 append-only 事件表达多个 Turn 的并发和恢复。
- Plan 使用不可变版本文件表达用户批准对象。
- Artifact 保存大结果、patch 和证据，避免膨胀上下文及元数据文件。

### 15.2 durable-first 顺序

所有阶段转换遵循：

1. 持久化转换意图和目标身份。
2. 执行模型、工具、Agent 创建或 Worktree 创建。
3. 持久化结果或外部身份。
4. 完成当前状态并发布 Event。

恢复器只根据 durable facts 决定下一步，不根据 UI 状态、最后一条文本或模型自述猜测。

## 16. 测试与发布门禁

### 16.1 每阶段共同测试

- Domain validation：状态转换、ID、版本、依赖、范围和预算。
- Repository contract：memory/file 行为一致、原子写、CAS 和尾残片。
- Service integration：Prompt、Profile、审批、Snapshot 和 Event。
- Crash-gap：每个 durable 写入间隙注入退出并重新打开。
- Security：Prompt injection、敏感路径、权限扩大和工具泄漏。
- UI：命令、卡片、Picker、取消、恢复和事件丢失重载。
- E2E：真实临时 Git 仓库、真实 Tool、真实文件 Store。

### 16.2 Definition of Done

一个阶段只有同时满足以下条件才算完成：

1. 核心功能可以通过用户入口完整使用。
2. 文件和内存 Repository 契约一致。
3. Snapshot 能在无实时 Event 的情况下恢复完整状态。
4. 取消、拒绝、失败和重启路径均有测试。
5. 权限和能力边界有负向安全测试。
6. Direct 现有行为无回归。
7. 指标能够区分新阶段产生的时间、成本和失败。
8. 文档、功能开关和升级兼容策略同步更新。
9. `go test ./... -count=1`、`go vet ./...`、架构依赖测试和发布构建通过。

## 17. 推荐实施批次

为了控制单次改动规模，每个阶段进一步按以下固定批次提交：

1. **Domain/contract**：类型、状态机、接口、校验和纯单元测试。
2. **Persistence**：memory/file Repository、版本兼容和恢复测试。
3. **Coordinator**：业务流程、能力 Profile、Prompt 和 Agent 连接。
4. **Projection**：Snapshot、Event、安全 DTO 和指标。
5. **UI**：命令、状态、确认、进度和取消。
6. **E2E/hardening**：真实仓库、崩溃间隙、安全和跨平台验证。

每个批次应保持可编译和测试通过。后续批次只依赖已稳定的前置契约，避免在 UI、Store 和 Runtime 之间同时反复修改同一语义。

## 18. 最终交付顺序

```text
P0 Product Turn / Capability foundation
        ↓
P1 Explicit /plan + read-only Plan + approval + single Agent execute
        ↓
P2 Agent-triggered EnterPlanMode with user consent
        ↓
P3 Plan version / drift / replan / recovery hardening
        ↓
P4 Single-Agent Workflow
        ↓
P5 Serial child Agents
        ↓
P6 Parallel read-only Agents
        ↓
P7 Isolated parallel write Agents + controlled integration
        ↓
P8 Adaptive selection and productization
```

这条路径保证每个阶段都能独立产生价值：P1 已经交付完整 Plan 体验；P2 补齐 Claude 风格主动进入；P4 在不引入多 Agent 风险时验证 Workflow；P5/P6/P7 逐级提高编排能力。任何后续阶段未完成，都不影响前面已经稳定交付的功能。
