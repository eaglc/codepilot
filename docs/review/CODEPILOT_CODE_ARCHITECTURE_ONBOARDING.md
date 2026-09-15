# CodePilot 代码架构与研发上手指南

> 本文仅根据当前源码、Go 包结构、构建入口、测试和配置推断项目现状，不依赖项目中的任务文档、路线图或设计文档。文中的“当前”指本文生成时工作区中的代码形态。

## 1. 项目定位

CodePilot 是一个以 Go 编写的终端交互式 Coding Agent 产品。它不是一个只负责调用大模型的薄 CLI，而是将以下能力组合成一个可恢复的本地产品：

- 通过 Bubble Tea 提供终端会话、流式输出、审批、澄清、计划和工作区选择界面。
- 通过 provider-neutral 的 LLM 抽象连接 OpenAI、DeepSeek、Ollama 等模型服务。
- 通过通用 Agent Runtime 执行模型步骤、工具调用、重试、中断、恢复和运行限制。
- 通过 Coding Agent 产品层管理 Session、Turn、Plan、Workflow、Workspace、权限和产品事件。
- 通过文件存储持久化会话、执行日志、计划、工作流、子 Agent 和工作区绑定。
- 通过 Git 工作区探测、敏感路径策略、权限模式和工具边界控制模型对本地代码的访问。

从代码组织看，项目当前的核心目标是让一次用户请求具备明确的产品身份、可审计的执行过程和崩溃后可恢复的生命周期。

## 2. 当前代码形态概览

项目是一个 Go module：

```text
github.com/eaglc/codepilot
```

主要运行方式是：

```text
cmd/codepilot/main.go
        │
        ▼
internal/app.New
        │  组装存储、Provider、Runtime、Workspace、LSP、UI
        ▼
internal/ui.Model / Bubble Tea
        │  调用 Client，并接收 EventBridge 的产品事件
        ▼
internal/codingagent.Service
        │  管理产品状态、权限、计划、工作流和恢复
        ▼
internal/agent.Runtime
        │  执行 provider-neutral 的模型步骤和工具循环
        ├── internal/contextmanager
        ├── internal/llm
        ├── internal/tool
        └── internal/agent/session
```

应用启动时，`internal/app` 是唯一的组合根。业务包不负责自行寻找全局依赖，而是通过构造函数和小型 interface 接收依赖。这样既能在测试中注入 fake 实现，也能把产品规则与具体存储、模型供应商和终端框架隔离开。

## 3. 目录结构与职责

以下目录是新研发人员最需要掌握的部分；测试文件通常与被测实现位于同一目录。

```text
.
├── cmd/
│   ├── codepilot/             # 主 CLI 入口、参数解析、退出码和启动交互
│   └── releasecheck/          # 发布前检查工具入口
├── internal/
│   ├── app/                   # 组合根：解析路径、组装所有运行时依赖、管理生命周期
│   ├── agent/                 # 通用 Agent Runtime，不包含 Coding 产品语义
│   │   └── session/           # Agent 会话树、执行日志、Lane、恢复数据模型
│   ├── codingagent/           # Coding 产品领域层和服务编排
│   │   ├── language/          # 语言识别、语言策略和代码相关能力注册
│   │   ├── lsp/               # LSP 进程/stdio 管理和协议适配
│   │   ├── prompt/            # 系统提示词和不可信仓库上下文构造
│   │   ├── roleprofile/       # Explore/Implement/Validate/Review/Integrate 角色策略
│   │   ├── tools/              # Coding 工具工厂、文件/Git/进程/安全边界
│   │   └── workspace/          # Git worktree 定位、路径和跨平台处理
│   ├── codingstore/           # Coding 产品数据的存储契约及实现
│   │   ├── file/               # JSON 文件存储、原子写入和版本/事件数据
│   │   └── memory/             # 内存实现，主要用于测试
│   ├── contextmanager/         # 上下文选择、摘要、压缩、token/预算管理
│   ├── llm/                    # 模型无关的消息、内容、流、工具声明和模型接口
│   ├── provider/               # Provider、凭证、Profile、模型工厂和适配器
│   │   ├── credential/         # 环境变量、系统 Keyring、链式凭证读取
│   │   ├── file/               # Provider Profile 文件存储
│   │   ├── openai/             # OpenAI 适配器
│   │   ├── deepseek/           # DeepSeek 适配器
│   │   ├── ollama/             # Ollama 适配器
│   │   └── internal/           # Provider 发现、默认配置和 Eino 适配
│   ├── releasecheck/           # 发布/版本检查业务实现
│   ├── sessionstore/            # 通用 Agent Session 的文件存储和状态租约
│   │   └── file/
│   ├── tool/                   # 通用 Tool 协议、注册表、结果和恢复语义
│   ├── ui/                     # Bubble Tea 模型、事件桥和终端交互组件
│   └── workflow/               # Provider-neutral 的持久化 Workflow 状态和串行调度规则
├── testdata/                   # 测试用 Git 仓库占位目录
├── .github/workflows/          # CI 与 Release 自动化
├── docs/                       # 项目文档；本文位于 docs/review
├── go.mod / go.sum             # Go 模块及依赖
└── .goreleaser.yml             # 发布产物配置
```

`internal` 目录意味着这些包只服务于本仓库内的产品，不作为稳定的外部 Go SDK 暴露。新增功能优先寻找现有领域边界，而不是直接在 `cmd` 或 `ui` 中堆叠业务逻辑。

## 4. 分层架构

### 4.1 CLI 与组合根：`cmd`、`internal/app`

`cmd/codepilot/main.go` 负责：

- 注册 `--workspace`、`--provider`、`--model`、`--permission` 等启动参数。
- 处理 `doctor` 和 `repair` 维护命令。
- 处理 Workspace 信任和 Worktree relocation 等需要用户确认的启动分支。
- 创建 `app.Application`，调用 `Run`，最后调用 `Close`。

`internal/app/app.go` 负责真实组装，包括：

1. 定位当前 Git worktree，解析 config/state 目录。
2. 获取状态租约，避免多个进程同时写本地状态。
3. 创建 Coding store、Agent session store 和 Provider profile store。
4. 创建 Provider Service、Context Manager、通用 Agent Runtime。
5. 创建 LSP Manager、Workspace Manager、Tool Factory、Prompt Builder 和角色注册表。
6. 创建 `codingagent.Service`，将所有能力注入进去。
7. 创建 `ui.EventBridge` 和 Bubble Tea Model。

这一层是理解启动链路和替换基础设施的首要入口。一般业务功能不应从这里开始实现，而应在下层定义清晰的端口后回到此处接线。

### 4.2 通用 Agent Runtime：`internal/agent`

`internal/agent` 是可复用的模型-工具执行内核。它不理解 Plan、Workflow、Workspace 或具体 Coding 工具，只接收以下抽象：

- `llm.ModelFactory`：创建模型。
- `contextmanager.ContextProcessor`：生成本次调用所需上下文。
- `agent/session.Repository`：持久化会话树和执行日志。
- `tool.Registry`：当前 Run 可用的工具集合。
- `DataPolicy`：对消息、工具参数、工具结果和流式文本做清洗。
- `RunLimits`：步骤数、时长、Token、工具调用、重复调用和输出大小限制。

Runtime 的主要执行入口是：

- `Run`：追加用户消息并开始一次新的 Agent Run。
- `Continue`：在已有会话中继续执行，但不伪造新的用户消息。
- `Resume`：恢复一个已经产生外部中断的 Run。
- `Recover`：根据持久化日志分析未完成的工具或操作并执行恢复策略。

一次 Run 会以 append-only 记录形式保存操作开始、模型步骤、工具开始/结束、中断、审批、检查点、用量和操作结束等事实。这样 UI 的流式事件不是唯一事实来源，进程崩溃后仍可从存储重建状态。

### 4.3 Agent Session：`internal/agent/session` 与 `internal/sessionstore`

通用 Agent Session 采用“上下文树 + Lane 指针 + 执行日志”的模型：

- `Entry` 表示消息、模型变更、上下文压缩、分支摘要等上下文节点。
- `Lane` 表示一条可继续执行的分支，默认是 `main`。
- `Record` 表示运行时事实，不与模型消息混在一起。
- `Snapshot` 是用于读取、投影和恢复的隔离视图。
- `session.AnalyzeRecovery` 可以识别未完成 Run、未完成 Tool 和待处理 Interrupt。

`internal/sessionstore/file` 是落盘实现，负责文件布局、摘要、归档和状态租约；`internal/codingstore` 则负责 Coding 产品自己的 Session、Turn、Plan、Workflow、Child Agent、Workspace 和 Artifact 数据。两套存储有意分开：

- Agent Session 记录“模型如何执行”。
- Coding store 记录“产品请求是什么、处于哪个业务阶段、与哪些 Run/Node/Child Agent 关联”。

### 4.4 Coding 产品服务：`internal/codingagent`

`codingagent.Service` 是产品层的核心门面，也是 UI 的 `Client` 实现。它把一次用户请求建模为 Product Turn，并将 Product Turn 与一个或多个通用 Agent Run 绑定起来。

核心对象关系：

```text
Coding Session
  ├── Workspace / Worktree 绑定
  ├── Provider Profile / Model
  ├── Product Turn
  │     ├── RunBinding -> generic Agent Run
  │     ├── Plan revision
  │     ├── Workflow
  │     └── Child Agent bindings
  └── Snapshot projection -> UI
```

当前服务职责包括：

- Session 创建、切换、重命名、归档、权限模式切换和 Lane fork。
- Turn 的启动、继续、恢复、取消、失败和完成。
- Direct 与 Plan 两种入口模式。
- Plan 建议、Plan 审批、Plan 版本和摘要校验。
- Workspace drift 检测与重新规划。
- Workflow 编译、持久化、串行调度、重试、阻塞、回退和完成。
- 只读 Plan Explore 子 Agent 和 Workflow 子 Agent 的创建、执行、回收及结果汇总。
- Provider 配置、模型选择和预检错误投影。
- 崩溃恢复和 Product Turn / Agent Session 的一致性修复。

#### Product Turn 的关键状态

`TurnPhase` 当前包括：

- `direct`：直接执行。
- `awaiting_plan_entry_approval`：等待用户是否进入 Plan。
- `planning`：只读探索和 Plan 编写。
- `awaiting_plan_approval`：等待对某个精确 Plan 版本审批。
- `executing`：执行批准后的计划。
- `needs_replan`：工作区发生实质漂移或执行偏离，需要重新规划。

`Turn` 中同时保存原始请求、策略、当前阶段、Run 绑定、Plan 版本/摘要、Workflow ID、子 Agent ID、修订号和时间戳。它是产品恢复和审计的主索引，而不是普通的临时控制对象。

### 4.5 Workflow：`internal/workflow`

`internal/workflow` 只负责 provider-neutral 的 Workflow 状态和调度规则，产品特有的 Plan 编译位于 `internal/codingagent`。

Workflow 是一个受预算约束的有向依赖图：

- 节点拥有目标、依赖、角色、Capability、读写范围、验收标准和失败策略。
- 节点可以由主 Agent 或独立 Child Agent 执行。
- 当前调度是串行的，最多只有一个活动节点。
- `NextAction` 是纯函数，只根据持久化 Workflow 状态返回下一步可信动作，不直接修改状态。
- 失败策略支持 retry、block、replan、terminate 和 delegated child 失败后回退到主 Agent。

`workflow.Repository` 通过创建 Workflow 和追加带期望 revision 的事件实现并发安全的持久化状态推进。

### 4.6 Provider 与 LLM：`internal/provider`、`internal/llm`

`internal/llm` 定义模型无关协议，包括：

- `Message`、`Content`、工具调用和工具结果。
- `ChatModel`、模型流事件和使用量。
- `ModelRef`、Tool Definition、Replay Policy 等执行协议。

`internal/provider` 负责产品级 Provider 管理：

- Profile 和模型配置。
- 凭证读取与保存。
- Provider 预检和错误分类。
- 适配器注册与 `CreateModel`。

具体 Provider 适配器位于 `openai`、`deepseek` 和 `ollama` 子包，底层模型协议通过 `internal/provider/internal/eino` 适配到 Eino。新增 Provider 时，通常应新增一个 Adapter，实现现有 Provider 端口，并在组合根注册，而不是修改 Agent Runtime。

### 4.7 Context Manager：`internal/contextmanager`

Context Manager 将完整会话转换为一次模型请求的上下文，处理：

- 当前 System Prompt、消息和工具定义。
- Token/字节预算。
- 历史摘要和分层摘要。
- 超限时的安全降级。
- 摘要模型调用的用量和持久化边界。

它通过 `Strategy` 管道组合具体策略。Agent Runtime 只依赖 `ContextProcessor`，因此上下文压缩规则可以演进而不污染 Agent 执行循环。

### 4.8 Tool 与 Coding Tools：`internal/tool`、`internal/codingagent/tools`

`internal/tool` 定义通用工具执行协议：

- `Tool`：定义、Replay Policy、Execute。
- `ResumableTool`：支持外部中断后的 Resume。
- `ControlTool`：声明产品协调边界。
- `TerminalOutputTool`：声明结构化 Run 完成边界。
- `Registry`：校验、去重、按名称查找和分发工具。

通用 Tool 不持久化活动，也不直接发布产品事件；日志和事件由 Agent Runtime 统一负责。这样可以保证所有工具都遵循相同的审计、恢复和错误语义。

`internal/codingagent/tools` 负责将产品信任范围编译为本次 Run 的精确工具集合，当前可见职责包括：

- 文件读取、创建、编辑、替换和 Patch。
- Git 读取和差异检查。
- 进程执行及 Windows/Unix 分支。
- LSP/代码导航。
- Artifact 边界、敏感路径、安全边界和权限边界。
- 不同 Role/Capability 下的节点范围限制。

工具不应自行读取全局配置来决定权限；应由 `ToolScope` 在创建时捕获 Worktree、读范围、写范围、权限模式和敏感路径。

### 4.9 Workspace 与 LSP：`internal/codingagent/workspace`、`internal/codingagent/lsp`

Workspace 层负责把用户路径解析为带有 Git 身份的 Worktree：

- 记录 Root、GitDir、GitCommonDir 和仓库指纹。
- 打开前重新探测路径和 Git 历史，避免路径复用到另一个仓库。
- Worktree 不可用时要求显式 relocation，不在读取时静默修复。
- 切换 Worktree 前关闭旧 Worktree 的瞬时进程。

LSP Manager 负责进程和 stdio 协议生命周期，Coding Tools 通过它提供语言相关导航能力。Workspace 是持久化身份，LSP 是可重建的瞬时能力，两者不应混为同一存储对象。

### 4.10 UI 与事件：`internal/ui`

UI 使用 Bubble Tea Model/Update/View 模式。`ui.Model` 保存当前 Snapshot、输入状态、滚动位置、活动工具、审批/澄清/恢复状态以及各类 picker。

UI 与业务的边界由 `ui.Client` 定义，覆盖：

- Session 和 Lane 操作。
- Turn 启动、恢复、取消。
- Provider Profile/Model 操作。
- Workspace 列表和 Worktree relocation。

模型输出通过 `ui.EventBridge` 接收 Coding Agent 的流式产品事件。事件桥只负责有界缓冲和关闭语义，产品事件的定义在 `internal/codingagent/event.go`。UI 读取 Snapshot 作为权威当前状态，读取事件作为增量展示，因此事件丢失或 UI 重绘不应改变持久化事实。

## 5. 典型执行链路

### 5.1 启动链路

```text
main
  -> parse flags / doctor / repair
  -> app.New
     -> ResolveWorktree
     -> prepare config/state directory
     -> AcquireStateLease
     -> create product and Agent repositories
     -> build Provider Service
     -> build Context Manager
     -> build Agent Runtime
     -> build Workspace/LSP/Tool/Prompt/Role capabilities
     -> build Coding Agent Service
     -> create EventBridge and UI Model
  -> Application.Run
     -> ui.Run / Bubble Tea
  -> Application.Close
```

### 5.2 一次 Direct Turn

```text
UI 输入
  -> codingagent.Service.StartTurn
  -> 创建或刷新 Product Turn
  -> prepareRunEnvironment
     -> 解析权限、Workspace、Prompt、ToolScope
  -> agent.Runtime.Run
     -> 从 Agent Session 读取上下文
     -> Context Manager 压缩/选择上下文
     -> 创建 ChatModel
     -> 循环执行模型步骤和 Tool
     -> 持久化 Entry/Record
     -> 发布 Agent Event
  -> Coding Agent 转换为产品 Event
  -> UI.EventBridge
  -> UI 根据事件更新并按需重新读取 Snapshot
```

### 5.3 Plan/Workflow Turn

```text
用户请求
  -> Planning Agent 只读探索
  -> 创建不可变 Plan version
  -> 用户审批精确 Plan revision
  -> 编译为 durable Workflow
  -> workflow.NextAction
  -> 逐节点执行主 Agent 或 Child Agent
  -> 节点结果/失败/重试/回退追加 Workflow event
  -> 需要时进入 replan 或完成 Turn
```

## 6. 一致性、恢复和安全模型

### 6.1 两套身份必须同时维护

一个产品请求至少关联：

1. Coding `Turn`：描述用户请求、产品阶段、计划和工作流。
2. Generic Agent `Run`：描述一次具体模型执行和工具日志。

`RunBinding` 是两者之间的显式关系。开发新功能时，不要只修改 Agent Session 而忘记更新 Product Turn，也不要把产品状态塞进通用 Agent 包。

### 6.2 持久化优先于内存状态

`Service` 中的内存 map 主要用于当前进程的活动状态、互斥和取消函数。真正可恢复的信息必须写入：

- Coding store：Session、Turn、Plan、Workflow、Child Agent、Workspace。
- Agent session store：Entry、Record、Lane 和恢复所需事实。

写入时普遍使用 revision/expected revision 或 append event，遇到冲突应显式返回错误，不应静默覆盖其他执行结果。

### 6.3 工具执行必须可审计和可恢复

工具开始前后都要由 Runtime 写入日志。工具的 Replay Policy 决定崩溃后能否重放：

- `ReplayNever`：必须等待外部明确决策。
- `ReplaySafe`：验证后可再次执行。
- `ReplayIdempotent`：使用原始幂等键重试。

工具实现只负责业务动作和输入校验；不要自行写 Session、发布 UI 事件或绕过 `Tool.Registry`。

### 6.4 不可信仓库内容与可信控制面分离

Prompt 和 Tool Scope 由产品层根据受信任的 Session、Worktree、权限和 Role 构造。仓库内容只能作为低优先级的不可信上下文传递，不能被拼进可信 System Prompt。新增仓库扫描、代码导航或自动修复能力时应保持这个边界。

## 7. 当前阶段判断

从当前代码而不是任务文档看，项目处在“核心产品可运行、架构正在完成从单 Agent 执行到可恢复产品编排的升级”阶段，具体表现为：

### 已形成的稳定骨架

- CLI、组合根、终端 UI、Provider、LLM、Tool、Session Store 和 Context Manager 均已拆成独立包。
- Direct 单 Agent 路径已经具备完整的 Run 限制、工具执行、流式事件和恢复语义。
- Workspace 信任、Git worktree 绑定、敏感路径和权限模式已经进入产品服务与工具创建链路。
- Provider 配置和多 Provider 适配已有明确端口。
- 文件存储、内存存储和大量契约/单元/E2E 测试已经形成可替换的基础设施层。

### 正在成为主路径的能力

- Product Turn 将产品请求与 Generic Agent Run 解耦。
- Plan 具有版本、摘要、审批和工作区漂移检测。
- Workflow 具有持久化节点、预算、串行调度、失败策略和 replan。
- Role Profile 与 Capability Profile 控制不同阶段可见的 Prompt 和工具。
- Child Agent 已有独立生命周期、任务结果和串行委派链路。
- Feature Flags 允许独立关闭 Product Turns、Plan、Workflow 和 Subagents，并保留部分恢复能力。

### 当前工程特征

因此它不是“从零搭建 Agent”的早期原型，也还不是已经完全收敛的稳定平台。更准确的判断是：

> Direct 路径是兼容性和兜底路径；Product Turn + Plan + Workflow + Child Agent 是正在完善的长期架构。

接手开发时应优先确认新功能属于哪条路径，并避免新增第三套并行状态机。涉及跨 Run、审批、恢复或多个节点的功能，应优先落到 Product Turn/Workflow；只影响单次模型循环的功能，才应落到 Generic Agent Runtime。

## 8. 后续可能的演进方向

以下方向是根据当前代码边界推导出的自然演进，不是对未实现功能的承诺。

### 8.1 收敛 Direct 与 Product Turn 的公共执行生命周期

当前代码同时保留 `startLegacyTurn`/Direct 行为和 Product Turn 编排。后续可以让更多 Direct 场景统一经过 Product Turn，而将 Generic Agent Runtime 保持为底层执行引擎。重点是维持旧会话恢复和禁用开关的兼容性，避免一次性删除旧路径。

### 8.2 将 Workflow 调度扩展为更丰富但仍可恢复的执行模型

当前调度器明确采用串行节点执行。后续可能增加：

- 有限并行节点，但需要新的资源、Workspace 和事件一致性约束。
- 更细的节点超时、成本预算和取消传播。
- 更强的结果 Artifact/引用传递。
- Workflow 级别的人工审批和可恢复等待。

在此之前应先稳定节点状态、revision、事件和恢复契约；不宜直接把并发执行塞入现有串行函数。

### 8.3 完善 Child Agent 的隔离和结果协议

Child Agent 已经拥有独立身份和任务结果模型。后续可以继续明确：

- 子 Agent 的 Workspace/读写 Scope 隔离。
- 父 Turn 取消、超时和恢复对子 Agent 的传播。
- 结构化结果和 Artifact 的大小、版本与引用生命周期。
- 子 Agent 失败后是重试、回退主 Agent 还是请求 replan。

这些能力应继续由 Coding Agent Service 和 Workflow 层管理，不能让 Child Agent 直接控制父 Turn。

### 8.4 提升存储演进和可观测性

当前文件存储适合本地单进程产品。随着会话、日志和 Workflow 数量增长，可能需要：

- 更明确的存储 schema/version migration。
- 更高效的索引和分页读取。
- 统一的事件查询、诊断和导出。
- 更强的损坏检测、归档和修复报告。

迁移时应保持 Repository interface 不变或提供兼容适配器，先替换 `codingstore/file` 和 `sessionstore/file`，不要把文件格式细节泄漏到产品服务。

### 8.5 扩展 Provider 与模型能力

Provider 端口已经较清晰，后续可增加更多模型能力，例如结构化输出、视觉内容、模型能力发现和成本估算。建议把差异留在 Provider/LLM 适配层，通过 `llm` 中性协议向上提供能力，避免在 UI 或 Workflow 中出现 Provider 名称判断。

### 8.6 让 UI 进一步变成 Snapshot + Event 的纯投影

当前 UI 已经以 `Snapshot` 和 `EventBridge` 为主要边界。后续可以继续减少 UI 中的产品判断，把审批、恢复、Plan 和 Workflow 状态全部投影为明确的产品事件和快照字段，从而支持：

- 更容易的非交互测试。
- 未来的 Web/API 前端。
- 事件回放和诊断界面。

### 8.7 增强跨平台与进程能力

项目已经对 Windows/Unix 工具进程和路径做了分支。随着 LSP、命令执行和 Worktree 管理增强，应继续把平台差异收敛在 `workspace`、`tools/process_*` 和 `lsp` 中，不要向 `codingagent.Service` 泄漏操作系统判断。

## 9. 新研发人员上手路径

### 第一步：先跑通构建和测试

```powershell
go test ./...
go run .\cmd\codepilot --help
```

如果只修改某一层，优先运行对应包的测试，例如：

```powershell
go test ./internal/agent/...
go test ./internal/codingagent/...
go test ./internal/ui/...
```

### 第二步：按这条阅读顺序理解代码

1. `cmd/codepilot/main.go`：确认启动参数、维护命令和退出路径。
2. `internal/app/app.go`：确认所有依赖如何被组装。
3. `internal/ui/model.go`：确认用户操作如何转换为 `Client` 调用。
4. `internal/codingagent/service.go`：确认产品会话、Turn 和恢复入口。
5. `internal/codingagent/turn_coordinator.go`：确认一次 Run 的准备、开始、继续和结束。
6. `internal/agent/runtime.go`：确认模型步骤、工具循环和事件/日志。
7. `internal/agent/session/types.go`：确认通用会话树和执行记录格式。
8. `internal/codingstore/file`、`internal/sessionstore/file`：确认落盘边界和 revision。

### 第三步：按功能选择修改层

| 需求 | 首选修改位置 | 不建议直接修改 |
| --- | --- | --- |
| 新增 Provider | `internal/provider/<provider>`、Provider 注册和配置 | `agent.Runtime`、UI 中的 Provider 分支 |
| 新增模型消息/流协议 | `internal/llm` | 具体 Provider 业务层 |
| 新增通用模型执行能力 | `internal/agent` | `codingagent` 中复制一套执行循环 |
| 新增 Coding 工具 | `internal/codingagent/tools` | 工具内部直接写存储或发 UI 事件 |
| 新增权限/路径限制 | `codingagent.ToolScope`、安全策略和工具边界 | 仅在 UI 禁用按钮 |
| 新增 Plan/Workflow 业务 | `internal/codingagent`、`internal/workflow` | 把状态塞进 Generic Agent Session |
| 新增持久化字段 | 领域类型、Repository 契约、file/memory 实现和校验 | 只改 JSON 写入代码 |
| 新增 UI 交互 | `internal/ui` 的 picker/model/event 投影 | 在 UI 内直接调用 Provider 或文件系统 |

### 第四步：修改前必须检查的契约

- 是否同时更新了内存实现、文件实现和 Repository contract test。
- 是否增加了 `Validate` 校验、revision 或并发冲突处理。
- 是否影响恢复、重启、取消、审批或工具重放。
- 是否需要新增产品 Event 和 Snapshot 字段，而不是依赖临时 UI 状态。
- 是否改变了 Tool Scope、可信 Prompt 或敏感路径边界。
- 是否需要在 `internal/app` 中接线或受 Feature Flag 控制。
- 是否保持 Direct 兼容路径和已有旧会话可恢复。

## 10. 总结

CodePilot 当前最重要的架构原则可以概括为四点：

1. **组合根集中组装**：`internal/app` 负责接线，业务包通过 interface 连接。
2. **产品层与执行层分离**：`codingagent` 管产品生命周期，`agent` 管模型/工具循环。
3. **状态和事件持久化**：Snapshot 用于当前投影，Entry/Record/Workflow Event 用于恢复和审计。
4. **能力按信任范围生成**：Workspace、权限、Role、Prompt 和 Tool Scope 在执行前确定，仓库内容保持不可信。

新功能优先复用这些边界，先明确它是“通用执行能力”“Coding 产品状态”“基础设施适配”还是“终端投影”，再选择包和接口。这样可以避免把当前正在收敛的多层架构重新退化成 UI、Agent 和存储相互耦合的单体流程。
