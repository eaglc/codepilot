# CodePilot 产品需求文档（PRD）

> 文档版本：v0.2  
> 状态：MVP 实施基线  
> 更新日期：2026-08-20  
> 产品形态：本地全屏 TUI Coding Agent  
> MVP 范围：Go / Python 项目 Bugfix

## 1. 文档目的

本文档用于明确 CodePilot 的产品目标、MVP 边界、交互方式、会话模型、权限策略、技术栈、项目结构、扩展方式和开发优先级，并作为实现和验收依据。

本项目首先是一个用于学习与面试展示的真实 Coding Agent。它需要完整体现多轮 Agent、LLM Provider、工具调用、代码搜索、补丁、审批、验证、Session、Checkpoint 和终端 UI 等核心工程能力，但不提前建设与 MVP 无关的平台。

## 2. 产品背景与定位

### 2.1 背景

CodePilot 最初定位为面向 Go 项目的 Bugfix Agent：用户提供 issue 和代码仓库，Agent 自动定位、修改并验证代码。

为了让核心能力可以复用于 Python 以及后续的代码审查、测试生成、代码解释和重构，产品调整为可扩展的 Coding Agent。但 MVP 仍只支持 Go / Python Bugfix，避免同时开发过多任务。

### 2.2 产品定位

CodePilot 是运行在开发者本机的交互式 Coding Agent。用户进入 Git 工作目录后执行：

    codepilot

程序直接进入持续的 Coding Session。用户通过自然语言描述问题，Agent 在当前 Worktree 内搜索、读取和修改代码，运行经过授权的检查，并在全屏 TUI 中持续展示对话、工具事件、审批和 Diff。

CodePilot 不使用 codepilot fix 子命令，也不在一次任务结束后退出。一个进程内可以继续补充需求、查看 Diff、切换模型、切换 Session 或切换 Workspace。

### 2.3 目标用户

- 希望在终端内完成 AI 辅助修复的 Go / Python 开发者。
- 希望学习和展示 Agent 工程、Go 架构及 TUI 设计的开发者。
- 希望保留代码审查权，不允许 Agent 自动 commit、push 或执行任意 Shell 的用户。

## 3. 产品目标

### 3.1 终极目标

将 CodePilot 发展为一个本地优先、安全、可验证、可扩展的交互式 Coding Agent，逐步覆盖：

- Bugfix。
- 代码理解与问答。
- 代码审查。
- 测试生成与补全。
- 小范围重构。
- 文档和变更说明生成。
- 远程 issue 与代码托管平台集成。
- 可选 LSP、MCP、隔离 Worktree 和 Sandbox。

这些能力应共享 Session、Provider、权限、Workspace、Agent 运行时、工具和 UI，不应为每一种任务重写基础设施。

### 3.2 MVP 目标

MVP 只解决以下核心问题：

> 用户在本地 Go 或 Python Git Worktree 中执行 codepilot，进入可持续对话的 Coding Session，描述 bug 后，Agent 能在受控权限内完成定位、补丁、验证和 Diff 展示。

MVP 必须具备：

1. codepilot 单入口，全屏交互式 TUI。
2. 持续多轮对话，不因一次 Bugfix 完成而退出。
3. 首次进入 Session 时通过 UI 选择 Provider、输入 Key 并验证。
4. 预置 OpenAI、DeepSeek、Ollama 和 Custom OpenAI-compatible Provider。
5. 同一 Session 内可在 Turn 之间切换 Provider / Model。
6. 一个 Worktree 下可以创建和切换多个持久化 Session。
7. 可以切换其他 Workspace 的 Session，切换后 CodePilot 活动目录随之改变。
8. 支持 Go / Python 项目识别和 Bugfix。
9. 支持仓库内搜索、读取、补丁、Git 状态和受控检查。
10. 无 Sandbox 时采用硬性工具边界、命令策略和审批共同控制权限。
11. 默认提供对话面板和 Diff 面板；MVP 不实现文件树。
12. 最终成功状态由检查证据决定，不由 LLM 自行声明。
13. 不自动 commit、push、创建 PR、安装依赖或修改仓库外文件。

### 3.3 MVP 成功标准

- Go 和 Python 各准备至少 3 个“修改前测试失败、修改后测试通过”的小型缺陷仓库。
- 使用脚本化 ChatModel 时，Session、工具、审批、Provider 切换和 Agent 流程能离线稳定测试。
- 任意文件工具不能读写活动 Worktree 之外的路径。
- ask 模式下未经批准不能应用补丁；执行项目代码在所有模式下都需要审批。选择 auto-edit 视为用户对当前 Session 内、通过硬性校验的 Worktree patch 预授权。
- Session 切换后能正确恢复对话、Provider、权限和 Diff 信息。
- 跨 Workspace Session 切换后，Agent、Git、Diff 和后续命令均使用目标 Worktree。
- 配置文件、Session 文件和日志不保存明文 API Key。
- 没有通过验证时，UI 不得显示“修复成功”。

### 3.4 MVP 非目标

- codepilot fix、codepilot exec 或其他非交互式任务入口。
- Web UI、桌面 UI、IDE 插件。
- 文件树、内置代码编辑器和 side-by-side Diff。
- Session 独立 Git Worktree、Session 并发运行或后台 Agent。
- OS 级 Sandbox、容器隔离和远程执行。
- 任意 Shell、任意网络工具和自动依赖安装。
- 多 Agent、子 Agent、DeepAgent 或任务规划 Agent。
- Eino Graph / Workflow 和通用工作流 DSL。
- 向量数据库、Embedding、RAG 或全仓库预索引。
- GitHub / GitLab issue 拉取、自动分支、commit、push 和 PR。
- 跨进程恢复执行到一半的工具调用。
- MCP、插件市场或自研插件协议。
- 大规模重构、跨仓库修改和生产环境操作。

## 4. 核心概念模型

### 4.1 Workspace

Workspace 表示一个逻辑 Git 仓库。未来同一个 Git 仓库可能包含多个 Worktree。

MVP 使用规范化的 Git 仓库信息和当前路径登记 Workspace。路径丢失时，Session 标记为 unavailable，切换失败并提示用户，不自动搜索或重定位仓库。

### 4.2 Worktree

Worktree 表示一个实际代码目录，是文件、Git、LSP 和命令执行的权限根。

所有工具必须显式接收 WorktreeRoot，不使用进程级 os.Chdir 作为状态来源。子进程通过明确的 Dir 字段指定目录。

### 4.3 Session

Session 表示一段持久化的对话和 Agent 上下文。每个 Session 创建后绑定一个 Worktree，不允许直接改绑。

核心关系：

    Workspace
    └── Worktree
        ├── Session A
        ├── Session B
        └── Session C

约束：

- 一个 Workspace 可以有多个 Worktree。
- 一个 Worktree 可以有多个 Session。
- 一个 Session 只能属于一个 Worktree。
- MVP 只使用用户现有 Worktree，不自动创建 Git Worktree。
- 同一 Worktree 下的多个 Session 共享真实文件状态。
- Session 不能并发运行；一个 CodePilot 进程只有一个 Active Session。

### 4.4 同 Worktree 的修改共享

Session A 修改文件后切换到 Session B，Session B 会看到修改后的真实文件，即使修改尚未 commit。

因此 UI 必须区分：

- Workspace Diff：当前 Worktree 相对于 Git HEAD 的全部真实变化，是事实来源。
- Session Diff：当前 Session 通过已批准 patch 产生的变化记录。

每次应用 patch 记录修改前后文件 hash 和 patch 内容。如果其他 Session 或用户手动修改同一文件，Session Diff 标记为 drifted 或 mixed，并提示以 Workspace Diff 为准。

### 4.5 Session 状态

Session 至少保存：

    type Session struct {
        ID             string
        Title          string
        WorkspaceID    string
        WorktreeRoot   string
        BaseCommit     string
        BaselineHash   string
        ProviderID     string
        ModelID        string
        PermissionMode string
        Messages       []Message
        Summary        string
        AppliedPatches []PatchRecord
        CreatedAt      time.Time
        UpdatedAt      time.Time
    }

Session title 默认由第一条用户消息截断生成，不额外调用模型；用户可随后重命名。

### 4.6 Eino Checkpoint 与 Session

两者职责不同：

| 数据 | 职责 |
| --- | --- |
| CodePilot Session | 保存跨 Turn 的对话、Worktree、Provider、权限和产品状态 |
| Eino Checkpoint | 保存一次 Agent Run 被审批或输入打断时的运行状态 |

Eino Runner 支持 CheckPointStore，用于 interrupt/resume；它不替代产品 Session。[Eino Runner 与 Checkpoint](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_extension/)

MVP 使用持久化 FileSessionStore 和内存级 Eino CheckPointStore：

- 重启程序后可恢复 Session 对话。
- 重启后不能恢复执行到一半的工具调用。
- Session 只能在 Agent 空闲时切换，因此切换时不迁移 pending checkpoint。

Eino v0.10 alpha 正在增加持久 Session，但官方仍将 Session 与 Checkpoint 作为不同概念，且 alpha API 可能变化。MVP 锁定 v0.9.x 稳定版，不依赖 v0.10 alpha Session API。[Eino v0.10 runtime 说明](https://github.com/cloudwego/eino/discussions/1159)

## 5. 用户交互设计

### 5.1 启动流程

用户在仓库中执行：

    codepilot

启动步骤：

1. 从当前目录向上查找 Git Worktree root。
2. 未找到 Git 仓库时提示错误并退出，MVP 不支持非 Git 目录。
3. 首次访问该 Worktree 时请求用户确认信任目录。
4. 加载该 Worktree 最近使用的 Session；没有 Session 时创建一个，并继承全局最近一次验证成功且凭据仍可用的 Provider / Model。
5. 新 Session 没有可用 Provider / Model 时自动打开与 /model 相同的选择器。
6. 加载 Provider、权限和 Session 历史。
7. 进入全屏 TUI，等待用户输入。

程序不会自动开始分析或修改代码，必须等待第一条用户消息。

### 5.2 TUI 布局

MVP 使用全屏双面板：

    ┌──────────────────────────────────────────────────────────────────────┐
    │ CodePilot │ repo │ branch* │ session │ provider/model │ permission  │
    ├────────────────────────────────────┬─────────────────────────────────┤
    │ CONVERSATION                       │ DIFF                            │
    │                                    │                                 │
    │ › 修复用户不存在时返回 500         │ handler.go                      │
    │                                    │ - return 500                    │
    │ • Searching ErrUserNotFound        │ + return 404                    │
    │ • Reading handler.go               │                                 │
    │                                    │ Workspace | Session | Proposed  │
    │ 找到错误映射缺失……                 │                                 │
    ├────────────────────────────────────┴─────────────────────────────────┤
    │ › 输入消息，或使用 /model /session /permissions                    │
    └──────────────────────────────────────────────────────────────────────┘

布局要求：

- 顶部显示 Workspace、分支、dirty 状态、Session、Provider / Model 和权限模式。
- 左侧展示对话、工具事件、测试输出和 Agent 状态。
- 右侧展示 Proposed / Session / Workspace Diff。
- 底部为多行输入框和快捷键提示；输入 `/` 或当前 token 以 `@` 开头时，在输入框上方显示临时补全列表。
- 宽终端默认 60% 对话、40% Diff。
- 窄终端自动切换为单面板，使用 Tab 在 Conversation 和 Diff 间切换。
- 窗口尺寸变化时局部刷新，不重启 Session。
- Agent 流式事件通过 UI 消息更新，不直接写 stdout 破坏布局。

Bubble Tea v2 支持 full-window、inline 和混合终端应用，并采用 Model / Update / View 的单向状态更新模型，适合承载 Agent 事件流和局部渲染。[Bubble Tea](https://github.com/charmbracelet/bubbletea)

### 5.3 MVP 不实现文件树

文件树移出 MVP。用户仍可通过以下方式理解修改范围：

- Diff 面板中的 changed files 列表。
- 对话面板中的 search / read / patch 工具事件。
- /status 显示已读取和已修改文件摘要。

文件树、文件预览和 Agent/User/Mixed 修改标记列入 P1，不预先创建空 UI 模块。

### 5.4 Slash Commands

MVP 支持：

| 命令 | 作用 |
| --- | --- |
| /model | 选择、配置或切换 Provider / Model |
| /permissions | 切换 read-only、ask、auto-edit |
| /session create | 在当前 Worktree 创建新 Session；`/session new` 保留为兼容别名 |
| /session list | 列出当前 Workspace 的 Session |
| /session list --all | 列出所有已登记 Workspace 的 Session |
| /session switch ID | 切换目标 Session |
| /session rename NAME | 重命名当前 Session |
| /session archive | 归档当前 Session |
| /workspace open PATH | 打开其他本地 Git Worktree，并选择或创建 Session |
| /workspace list | 查看已登记 Workspace |
| /status | 查看 Session、Git、权限、Provider 和运行限制 |
| /diff | 聚焦或切换 Diff 面板 |
| /clear | 清除当前 Session 对话上下文，不修改代码 |
| /help | 显示命令和快捷键 |
| /exit | 保存 Session 并退出 |

命令由 UI Command Handler 解析，不使用 Cobra 子命令体系。用户输入 `/` 时立即展示命令列表；拥有子命令或枚举参数的主命令展开为 `/session create`、`/session list`、`/permissions ask`、`/diff workspace` 等可执行叶子项。继续输入时按完整命令前缀过滤；Up / Down 选择，Enter 或 Tab 插入，Esc 关闭。完整命令按 Enter 执行；从未完整前缀选择候选时先插入命令，再由用户确认执行。

用户在当前输入 token 的开头输入 `@` 时展示活动 Worktree 内 Git 可见的安全文件及由这些文件推导出的目录，并随输入按完整路径前缀、文件名前缀和包含关系过滤。目录以 `/` 结尾；选择目录后保留补全列表以继续下钻，选择文件后插入普通 `@relative/path` 文本。补全不直接读取文件，也不绕过文件工具的路径校验和权限边界。

### 5.5 快捷键

MVP 快捷键：

- Ctrl+C：取消当前 Agent Turn；空闲时不直接退出。
- Ctrl+D：空闲时保存并退出。
- Ctrl+N：在当前 Worktree 创建 Session。
- Ctrl+O：打开 Session Picker。
- Tab：在 Conversation 与 Diff 面板之间切换焦点。
- Esc：关闭选择器、审批框或返回输入框。

快捷键和 Slash Command 必须调用同一应用服务，避免两套业务逻辑。

### 5.6 Session 创建与切换

创建 Session：

    /session create

新 Session：

- 绑定当前 Worktree。
- 记录当前 BaseCommit 和 Workspace baseline。
- 同一 Worktree 内显式创建时继承当前 Session 的 Provider / Model；首次进入新 Worktree 时继承全局最近一次验证成功且凭据仍可用的 Provider / Model。没有可用模型时保持未配置并打开选择器。
- 权限默认回到 ask。
- 不清理、不回滚当前 Worktree 的已有修改。

切换同一 Worktree 的 Session：

- 保存当前 Session。
- 加载目标消息、Provider、权限和 patch 记录。
- 重新构建 Agent。
- 文件系统与 Workspace Diff 保持当前真实状态。

切换不同 Worktree / Workspace 的 Session：

1. Agent 必须空闲；运行中必须先完成或 Ctrl+C。
2. 保存当前 Session。
3. 停止当前 LSP（启用时）。
4. 释放当前 Agent Runner。
5. 展示目标路径并请求确认。
6. 切换 Active WorktreeRoot。
7. 重新加载 Git、Diff、Session 和 Agent。
8. 后续工具和子进程全部显式使用目标 WorktreeRoot。

CodePilot 不调用全局 os.Chdir，因此切换只影响应用内部，退出后父 Shell 的目录不会改变。

### 5.7 Provider 首次配置

没有可用 Provider 时，自动打开：

    Select provider

    > OpenAI
      DeepSeek
      Ollama
      Custom OpenAI-compatible

预置 Provider 流程：

1. 用户选择 Provider。
2. 远程 Provider 使用隐藏输入框输入 API Key。
3. Adapter 自动提供 Base URL，并通过模型目录接口检查 Endpoint 与认证；该步骤不调用模型、不消耗 token。
4. 只展示 Provider 实际返回的模型；代码内推荐模型只有确实存在于返回目录时才能标记为推荐。
5. 用户选择具体模型后，执行该模型的可用性和最小 tool-calling 验证。
6. Endpoint 与认证检查成功后保存非敏感配置和 credential reference；只有所选模型验证成功后才绑定 Session。
7. API Key 保存到 OS Keyring。

Ollama 不需要 Key，直接验证本地服务并读取可用模型。

Custom OpenAI-compatible 是例外，需要输入 Base URL、Model 和 Key。

验证由 Provider Adapter 完成，不由 Agent 用自然语言判断。模型目录检查只产生网络请求；选择具体模型后的最小 tool-calling 验证可能消耗少量 token，UI 必须分别说明。

DeepSeek V4 默认启用 thinking mode。该模式支持 tool calls，但拒绝 `tool_choice` 字段；DeepSeek Adapter 的探测必须只传工具定义并通过提示触发调用，不能强制指定工具。

### 5.8 Session 内切换 Provider

用户通过 /model 切换：

    Current: DeepSeek / deepseek-v4-flash

    Configured models
    > DeepSeek  ·  deepseek-v4-flash  (current)
      Ollama  ·  qwen-coder

    Add provider
      OpenAI
      DeepSeek
      Ollama
      Custom OpenAI-compatible

规则：

- 只能在两个 Turn 之间切换。
- 已配置模型与新增 Provider 入口必须分区显示；相同 Provider kind、规范化 Endpoint 和 ModelID 的重复配置只显示一次。
- 打开选择器时优先选中当前 Session 的 Provider / Model。
- 已配置区的每一行代表具体 Provider / Model，选择后直接验证并切换，不再进入第二层 Model Picker；只有新增 Provider 配置完成后才展示该 Provider 返回的模型列表。
- Agent 正在运行或等待审批时禁止切换。
- 切换后保留消息、Workspace、代码修改和权限模式。
- 使用目标 Provider 重建 ChatModelAgent / Runner。
- Session 保存新的 ProviderID 和 ModelID。
- Provider 间只共享 CodePilot 的中性 user / assistant 消息，不共享厂商私有 tool-call 对象。
- 目标模型上下文不足时压缩或裁剪最早消息，并在 UI 明确提示。

## 6. 功能需求

| 编号 | 功能 | 优先级 | 验收标准 |
| --- | --- | --- | --- |
| FR-01 | codepilot 单入口 | P0 | 在 Git Worktree 中执行后直接进入 TUI |
| FR-02 | 全屏双面板 TUI | P0 | Conversation 和 Diff 可局部刷新、切换焦点并适配窄终端 |
| FR-03 | 首次 Provider 配置 | P0 | UI 选 Provider、隐藏输入 Key、验证成功后创建模型 |
| FR-04 | 多 Provider | P0 | 支持 OpenAI、DeepSeek、Ollama、Custom OpenAI-compatible |
| FR-05 | 会话内切换模型 | P0 | 空闲时通过 /model 切换，历史和工作区保持 |
| FR-06 | 持久 Session | P0 | 退出重启后能列出和恢复对话及元数据 |
| FR-07 | 多 Session | P0 | 同一 Worktree 可 new、list、switch、rename、archive |
| FR-08 | 跨 Workspace Session | P0 | /session list --all 可查看并切换，活动目录随目标 Session 更新 |
| FR-09 | 权限模式 | P0 | /permissions 支持 read-only、ask、auto-edit |
| FR-10 | 仓库预检 | P0 | 校验 Git、dirty 状态、语言、运行时和路径边界 |
| FR-11 | 代码发现 | P0 | Agent 可列举、搜索和分段读取非敏感文本文件 |
| FR-12 | 补丁 | P0 | unified diff 校验后按权限申请或执行，失败不部分写入 |
| FR-13 | Diff 面板 | P0 | 支持 Proposed、Session、Workspace 三种 Diff |
| FR-14 | Go Bugfix | P0 | go/format 格式化，批准后运行 Go 检查 |
| FR-15 | Python Bugfix | P0 | 使用现有 pytest，缺少环境时明确报告且不安装 |
| FR-16 | 有限迭代 | P0 | 检查失败可反馈 Agent，受 step / time 上限约束 |
| FR-17 | 取消 | P0 | Ctrl+C 可取消模型流和工具，保留已批准修改 |
| FR-18 | LSP | P1 | 可选 gopls / pyright，缺失时退化为文本工具 |
| FR-19 | 文件树 | P1 | 展示 Git 与 Agent 修改状态，不影响 MVP 验收 |

## 7. Agent 与工具执行

### 7.1 运行策略

采用“Session 外层控制 + 单 Agent 内层循环”：

    TUI
      -> Session Service
      -> 预检与权限策略
      -> Eino ChatModelAgent + 受控 Tools
      -> Diff / Check 证据
      -> Session 持久化与 UI Event

Session Service 负责多轮消息、Provider 切换、权限、运行限制、成功状态和持久化。LLM 负责理解问题、选择搜索路径、分析根因和提出补丁。

成功状态只能由外层流程根据检查结果生成：

- completed：Agent 正常返回且没有应用补丁；适用于解释、审查、诊断或无需修改的结果，实际结论以最终文本为准。
- verified：要求的检查通过。
- unverified：已生成补丁，但检查被拒绝、跳过或环境不可用。
- failed：无法完成或检查显示本次修改失败。
- cancelled：用户取消当前 Turn。

### 7.2 Eino 使用范围

MVP 使用 Eino v0.9.x 的 ChatModelAgent、Runner、Tool 和 CheckPointStore。Eino 官方将 ChatModelAgent 作为常规单 Agent + Tools 入口，并由 Runner 管理事件和恢复。[Eino](https://github.com/cloudwego/eino)

MVP 不使用：

- DeepAgent。
- 多 Agent。
- Graph / Workflow。
- 持久化 memory middleware。
- 通用 command-line tool。
- Eino v0.10 alpha Session API。

### 7.3 工具清单

| 工具 | 权限 | 说明 |
| --- | --- | --- |
| list_files | 只读 | 列出候选文件，遵守 ignore 和敏感文件规则 |
| search_code | 只读 | 搜索字符串或正则，返回行号和有限上下文 |
| read_file | 只读 | 按行范围读取，限制输出大小 |
| git_status | 只读 | 当前分支和 Worktree 状态 |
| git_diff | 只读 | Proposed / Session / Workspace Diff 数据 |
| apply_patch | 写入 | 校验 unified diff，授权后写入 |
| run_checks | 外部进程 | 仅执行语言策略生成的结构化命令，必须授权 |

不提供 execute、shell、http_request 或 package_install 工具。

### 7.4 Prompt 约束

System prompt 只包含当前必要规则：

- 当前 MVP 以 Bugfix 为目标。
- 先查证再修改，优先最小 patch。
- 不猜测未读取代码。
- 不扩大 issue 范围。
- 使用工具获得事实，不伪造文件和测试结果。
- patch 前说明根因和修改意图。
- 检查失败时判断是否由本次修改引入。
- 不自行声明成功。

Go / Python 差异以简短 language hint 注入，不维护两套 Agent 主流程。

## 8. 无 Sandbox 时的权限与审批

### 8.1 基本结论

审批不是 Sandbox。没有 OS 级 Sandbox 时，任何启动的子进程理论上拥有当前用户权限。

因此 MVP 的安全边界由以下部分共同构成：

1. 模型只能调用预定义高层工具。
2. 文件工具执行 Worktree 路径硬校验。
3. 命令必须是结构化 Command，不经过 Shell。
4. Command Policy 对程序和参数执行 allow / prompt / deny。
5. 有副作用的动作由 Authorizer 请求用户批准。

Codex 的官方安全设计也将 Sandbox Mode 与 Approval Policy 分开处理，说明审批不能代替执行隔离。[Codex Sandbox](https://learn.chatgpt.com/docs/sandboxing)

### 8.2 权限模式

| 模式 | 文件读取 | Worktree patch | 外部检查 |
| --- | --- | --- | --- |
| read-only | 自动 | 拒绝 | 拒绝 |
| ask | 自动 | 每次询问，可本 Session 授权同类 patch | 每次询问 |
| auto-edit | 自动 | 路径和 patch 校验后自动 | 仍然询问 |

MVP 不提供 full-access / yolo 模式。

### 8.3 审批选项

    Allow once
    Allow this exact action for this session
    Deny

Session 级授权只保存在内存中，切换 Session 或退出后失效，不写入全局配置。

即使用户批准，以下规则仍不可绕过：

- 不得写入 Worktree 之外。
- 不得写入 .git。
- 不得执行 Shell。
- 不得执行 git reset、clean、checkout 覆盖、commit、push。
- 不得运行包管理器安装命令。
- 不得运行 curl、wget 等通用网络命令。

### 8.4 命令执行

所有命令使用：

    exec.CommandContext(ctx, program, args...)
    command.Dir = worktreeRoot

允许的 MVP 命令包括：

- git status、diff、ls-files、grep 等只读 Git 操作。
- go test、go vet 等 Go 检查。
- python -m pytest 等 Python 检查。
- 用户通过 UI 明确看到并批准的针对性测试。

Go 源码格式化优先使用标准库 go/format 在进程内完成，减少外部命令。

需要明确提示：用户批准运行 go test 或 pytest 后，仓库测试代码本身仍可能访问网络或系统，因为 MVP 没有 Sandbox。CodePilot 只能限制启动命令，不能限制子进程的系统调用。

### 8.5 文件安全

- WorktreeRoot 转为绝对规范路径。
- 路径 join 后再次检查仍位于根目录。
- 拒绝符号链接逃逸。
- 保护 .git、.agents、.codex 和敏感凭据文件。
- 默认拒绝 .env、私钥、证书、credential、token 文件。
- patch 使用 git apply --check 或等价安全校验后再应用。
- patch 失败不得产生部分写入。
- 不自动回滚，因为 Worktree 可能包含用户原修改。

## 9. 验证与运行限制

### 9.1 验证策略

Go：

- 对修改源码使用 go/format。
- 默认建议 go test ./...。
- 可运行针对性包测试和 go vet。

Python：

- 优先使用项目已有 pytest。
- 默认建议 python -m pytest -q。
- 不自动引入 black、ruff 或 pytest。
- 环境或依赖缺失时只报告限制。

首次修改前可以建议运行 baseline check；用户拒绝 baseline 不阻止修改，但结果无法区分所有既有失败。

最终报告必须区分：

- 本次已修复失败。
- 修改前已经存在的失败。
- 本次新引入失败。
- 被拒绝、超时或环境缺失导致未执行的检查。

### 9.2 运行限制

- 单个 Turn 最多 30 个 Agent step。
- 单个命令默认超时 5 分钟。
- 单个 Turn 默认最长 20 分钟。
- 工具结果按字符和行数截断，并标记原始大小。
- Ctrl+C 取消模型 stream 和正在运行的工具。
- 达到限制后停止当前 Turn，不退出 Session。

## 10. 技术栈

| 类别 | 选型 | 说明 |
| --- | --- | --- |
| 语言 | Go 1.26.x | 单二进制、context、并发和跨平台能力；使用最新稳定补丁。[Go releases](https://go.dev/doc/devel/release) |
| Agent | CloudWeGo Eino v0.9.x | ChatModelAgent、Runner、Tool、Checkpoint；锁定稳定版 |
| 模型适配 | EinoExt | OpenAI-compatible、DeepSeek / OpenAI 映射和 Ollama |
| TUI | Bubble Tea v2.0.x | 全屏、局部渲染、键盘事件、Model / Update / View |
| TUI 组件 | Bubbles v2 + Lip Gloss v2 | textarea、viewport、list、布局和样式 |
| 配置 | YAML v3 | 仅保存非敏感配置 |
| Credential | OS Keyring + go-keyring | 保存 API Key；失败时只做当前进程内存保存。[go-keyring](https://github.com/zalando/go-keyring) |
| Session | 标准库 JSON / JSONL | FileSessionStore；避免提前引入数据库 |
| Git | 系统 Git CLI | status、diff、tracked files、patch check |
| 进程 | os/exec | 结构化参数、明确 Dir、取消、超时和输出限制 |
| 日志 | log/slog | 脱敏诊断；默认不接外部平台 |
| 测试 | testing + testdata | fake adapters、临时 Git 仓库和缺陷 fixtures |

MVP 不再使用 Cobra。程序只有 codepilot 入口，Slash Command 属于 Session UI。

## 11. 架构设计

### 11.1 设计原则

1. 先定义主体流程需要的最小接口，再实现 adapter。
2. 接口由消费方定义，不为每个 struct 创建 interface。
3. Session Service 只依赖 ports，不依赖 Eino、Bubble Tea、Keyring 或具体文件存储。
4. Eino 类型限制在 agent / provider 边界。
5. UI 只处理输入和 Event，不直接读写文件或运行 Agent。
6. 权限校验写在 Go 代码中，不依赖 prompt。
7. 所有工作目录显式传递，不依赖全局 cwd。
8. 第二个真实调用者出现后才抽取通用能力。
9. 不创建 common、utils、base、manager 等职责不清的包。

### 11.2 主体接口

Session 主流程首先依赖：

    type Agent interface {
        RunTurn(ctx context.Context, request TurnRequest, events EventSink) (TurnResult, error)
    }

    type AgentFactory interface {
        Create(ctx context.Context, config AgentConfig) (Agent, error)
    }

    type SessionStore interface {
        Load(ctx context.Context, id string) (*Session, error)
        Save(ctx context.Context, session *Session) error
        List(ctx context.Context, filter SessionFilter) ([]SessionSummary, error)
    }

    type Authorizer interface {
        Authorize(ctx context.Context, action Action) (Decision, error)
    }

    type EventSink interface {
        Publish(ctx context.Context, event Event) error
    }

Provider 边界：

    type Provider interface {
        ID() string
        DisplayName() string
        Validate(ctx context.Context, credential Credential) error
        Models(ctx context.Context, credential Credential) ([]Model, error)
        NewChatModel(ctx context.Context, config ModelConfig, credential Credential) (ChatModel, error)
    }

    type CredentialStore interface {
        Get(ctx context.Context, providerID string) (Credential, error)
        Set(ctx context.Context, providerID string, credential Credential) error
        Delete(ctx context.Context, providerID string) error
    }

接口保持当前用例所需的最小方法。配置值、纯数据 struct 和简单纯函数不创建接口。

### 11.3 依赖关系

    cmd/codepilot
      ├── ui ───────────────> session
      ├── session ports <──── agent
      │                    ├─ provider
      │                    ├─ workspace
      │                    ├─ language
      │                    └─ lsp (P1)
      ├── session ports <──── approval
      ├── session ports <──── sessionstore
      └── provider ports <─── credential

main.go 是组合根，创建具体实现并注入，不使用 DI 框架。

### 11.4 目录结构

    codepilot/
    ├── cmd/
    │   └── codepilot/
    │       └── main.go
    ├── internal/
    │   ├── ui/
    │   │   ├── model.go
    │   │   ├── update.go
    │   │   ├── view.go
    │   │   ├── layout.go
    │   │   ├── keymap.go
    │   │   ├── chat.go
    │   │   ├── diff.go
    │   │   ├── command.go
    │   │   ├── sessionpicker.go
    │   │   ├── providerpicker.go
    │   │   └── approval.go
    │   ├── session/
    │   │   ├── service.go
    │   │   ├── ports.go
    │   │   ├── session.go
    │   │   ├── turn.go
    │   │   └── event.go
    │   ├── agent/
    │   │   ├── runner.go
    │   │   ├── factory.go
    │   │   ├── prompt.go
    │   │   ├── tools.go
    │   │   ├── events.go
    │   │   └── codeintel.go
    │   ├── provider/
    │   │   ├── registry.go
    │   │   ├── provider.go
    │   │   ├── openai.go
    │   │   ├── deepseek.go
    │   │   ├── ollama.go
    │   │   └── compatible.go
    │   ├── credential/
    │   │   ├── keyring.go
    │   │   └── memory.go
    │   ├── approval/
    │   │   ├── policy.go
    │   │   └── session.go
    │   ├── workspace/
    │   │   ├── workspace.go
    │   │   ├── worktree.go
    │   │   ├── files.go
    │   │   ├── search.go
    │   │   ├── patch.go
    │   │   ├── diff.go
    │   │   ├── git.go
    │   │   └── command.go
    │   ├── language/
    │   │   ├── language.go
    │   │   ├── golang.go
    │   │   └── python.go
    │   ├── sessionstore/
    │   │   ├── file.go
    │   │   └── memory.go
    │   ├── config/
    │   │   ├── config.go
    │   │   └── store.go
    │   └── lsp/                    # P1，开始实现时再创建
    │       ├── client.go
    │       ├── manager.go
    │       ├── protocol.go
    │       └── document.go
    ├── testdata/
    │   └── repos/
    │       ├── go/
    │       └── python/
    ├── AGENT.md
    ├── README.md
    ├── prd.md
    ├── go.mod
    └── go.sum

目录约束：

- MVP 不创建 pkg。
- LSP 和文件树未开始开发时不创建空目录。
- UI 面板先保持同一个 ui package，不为每个面板拆 package。
- 单个 Provider 先用同 package 文件分隔，实现显著变大后再拆。
- 测试与代码同目录，端到端 fixtures 放 testdata。

## 12. 配置、Credential 与持久化

### 12.1 配置

使用 os.UserConfigDir 下的 codepilot 目录保存非敏感配置：

    version: 1
    default_provider: deepseek
    default_model: deepseek-v4-flash

    agent:
      max_steps: 30
      max_turn_duration: 20m
      command_timeout: 5m

配置文件不保存 API Key、完整 prompt、代码片段或 Eino checkpoint。

### 12.2 Credential

- API Key 使用 OS Keyring 保存。
- 关于凭据，配置和 Session 只保存 credential reference，不保存 Key 本身。
- Key 输入框关闭 echo。
- 日志和错误统一脱敏。
- Keyring 不可用时使用 MemoryCredentialStore，仅当前进程可用；下次启动重新输入。
- MVP 不用“加密文件”降级，因为没有独立主密钥时只是弱混淆。

### 12.3 SessionStore

多 Session 和重启恢复是 P0，因此正式 MVP 使用 FileSessionStore；MemorySessionStore 仅用于测试。

建议结构：

    state/
    ├── workspaces.json
    └── sessions/
        └── SESSION_ID/
            ├── metadata.json
            ├── messages.jsonl
            └── patches.jsonl

要求：

- metadata 使用临时文件 + rename 原子更新。
- messages / patches 追加写入并在加载时校验。
- 文件权限尽可能限制为当前用户。
- Archive 不删除数据。
- API Key 永远不进入 Session 文件。
- Session 内容可能包含代码摘要，README 必须说明其本地存储位置和清理方式。

## 13. LSP 规划

LSP 可以提供 definition、references、symbols、diagnostics 和 hover，协议使用 JSON-RPC。[LSP 官方说明](https://microsoft.github.io/language-server-protocol/)

LSP 是 P1，不作为 MVP 成败依赖。基础 Bugfix 先使用 search / read / test。

开始实现 LSP 时：

- Agent 消费方先定义 CodeNavigator 小接口。
- internal/lsp 实现该接口。
- Go 使用 gopls。
- Python 使用 pyright 或 basedpyright。
- 找不到 Language Server 时退化为文本工具。
- 不自动安装 Language Server。
- 每个 Active Worktree 按需启动一个进程。
- 切换 Worktree 时关闭旧 LSP，目标 Session 需要时再启动。
- 启动外部 LSP 前按照外部命令申请审批。
- 默认过滤 Worktree 外的 Location。

LSP 不替代搜索、读取、Diff 和测试。

## 14. 后续扩展方式

| 扩展 | 实现方式 | 不改动部分 |
| --- | --- | --- |
| 新语言 | 增加 language adapter | Session、TUI、权限、Workspace |
| 新 Provider | 增加 Provider 实现并注册 | Session 主流程、Workspace |
| LSP | 实现 CodeNavigator 并注入 Agent | Session、Provider、Diff |
| 文件树 | 新增 ui/files.go，消费 Workspace 文件事件 | Agent、Session、权限 |
| Review | 增加 prompt / tool policy 或独立 AgentFactory 策略 | TUI、Session、Provider |
| Test generation | 复用 patch、language 和 check 工具 | Session、Diff、审批 |
| Git Worktree 隔离 | 新增 WorktreeManager，Session 仍绑定 WorktreeRoot | Session 数据模型和 UI |
| 持久 Checkpoint | 替换 Eino CheckPointStore 实现 | 产品 SessionStore |
| Sandbox | 替换 CommandExecutor，实现相同 Command 语义 | Agent、Session、UI |
| GitHub issue | 新增 IssueSource，转换为 Session 用户消息 | Agent 工具和 Workspace |
| PR | 增加独立 publish workflow 和新授权 | 本地修复默认行为 |

### 14.1 P1

- LSP。
- 文件树和文件预览。
- 独立 Git Worktree Session。
- Review。
- Test generation。
- 更好的上下文压缩和 token 统计。

### 14.2 P2

- 容器或 OS Sandbox。
- 远程 issue 和 PR。
- MCP。
- 持久 Eino Checkpoint。
- Session fork 和跨 Worktree 复制上下文。

### 14.3 暂不提前抽象

在真实需求出现前不创建：

- 通用 TaskEngine 或 Workflow DSL。
- 自研 Agent 框架。
- DI 容器和事件总线框架。
- 数据库 repository 层。
- 万能 Provider 配置对象。
- 统一 AST 模型。
- 插件系统。
- 后台任务调度平台。

Interface-first 的含义是先定义当前主体流程所需的行为契约，不是预测所有未来方法。

## 15. 开发优先级与里程碑

### M0：核心契约与工程骨架（P0）

- 初始化 Go module 和 codepilot 入口。
- 定义 Session、Turn、Event、Action、Decision。
- 定义 Agent、AgentFactory、SessionStore、Authorizer、EventSink。
- 使用 fake / memory 实现完成 Session Service 单元测试。
- 建立 context cancellation 和错误约定。

完成标准：不接 Eino 和真实文件系统也能测试多轮 Session 状态流转。

### M1：TUI、Session 与 Provider（P0）

- Bubble Tea 全屏双面板骨架。
- Conversation、Diff 占位、输入框和 Slash Command。
- FileSessionStore 和 Session Picker。
- 当前 Workspace 多 Session 创建、恢复、切换。
- Provider Picker、Keyring、OpenAI / DeepSeek / Ollama。
- Session 内 Provider 切换。

完成标准：可以重启恢复 Session、跨 Session 对话并切换 Provider。

### M2：Workspace、Diff 与审批（P0）

- Worktree 路径边界和 Git 预检。
- Workspace 登记与跨 Workspace Session 切换。
- read-only / ask / auto-edit。
- Authorizer 和命令策略。
- search、read、status、diff、patch。
- Proposed / Session / Workspace Diff 面板。

完成标准：无 LLM 时可测试安全 patch、审批和 Diff 刷新。

### M3：Eino 与 Go Bugfix（P0）

- Eino ChatModelAgent / Runner / MemoryCheckpointStore。
- Agent Event 转换为 UI Event。
- Go 检测、prompt hint、go/format 和受控测试。
- 检查失败反馈和有限迭代。
- 3 个 Go fixtures。

完成标准：Go Bugfix 在交互式 Session 内完整闭环。

### M4：Python Bugfix（P0）

- Python 检测和 pytest 策略。
- 环境缺失和依赖缺失报告。
- 3 个 Python fixtures。

完成标准：Python 与 Go 共用 Session、Agent、权限和 Diff 主流程。

### M5：质量与发布（P0）

- 脏 Worktree、跨 Session drift、符号链接和敏感文件测试。
- Provider 切换、Ctrl+C、命令超时和窗口 resize 测试。
- scripted model 离线集成测试。
- README 安装、隐私、存储、权限和 demo。
- gofmt、go vet、go test 和可选 golangci-lint。

完成标准：满足第 16 节验收清单，发布 v0.1.0。

### P1

顺序建议：

1. LSP。
2. 文件树。
3. 独立 Git Worktree Session。
4. Review。
5. Test generation。

## 16. 测试与验收

### 16.1 测试分层

- 单元测试：Session 状态、接口 fake、配置、Provider registry、权限和成功判定。
- UI 测试：固定 WindowSize 下对 Model / Update / View 做 golden test。
- 集成测试：临时 Git Worktree、搜索、patch、diff、dirty 状态和路径逃逸。
- Session 测试：FileSessionStore 原子写、恢复、archive、跨 Workspace 切换。
- Agent 测试：scripted ChatModel 返回固定 tool call，不访问网络。
- 端到端 fixtures：Go / Python 各至少 3 个。
- 真实模型评估：独立运行，不作为普通 CI 稳定门禁。

### 16.2 MVP 验收清单

- [ ] codepilot 在当前 Git Worktree 直接进入 TUI。
- [ ] 无 Provider 时自动打开 Provider Picker。
- [ ] 预置 Provider 只要求 Key；Custom 才要求 Base URL 和 Model。
- [ ] Key 输入、配置、Session 和日志中无明文泄露。
- [ ] Conversation / Diff 双面板可 resize 和切换焦点。
- [ ] Agent 流式事件不会破坏 TUI 布局。
- [ ] 可以创建、保存、恢复、重命名和归档 Session。
- [ ] 同一 Worktree 多 Session 明确共享文件状态。
- [ ] Workspace Diff 与 Session Diff 可区分。
- [ ] Session 修改被外部覆盖时显示 drift / mixed。
- [ ] /session list --all 可切换其他 Workspace Session。
- [ ] 跨 Workspace 切换后所有工具使用目标 WorktreeRoot。
- [ ] 父 Shell cwd 不受切换影响。
- [ ] /model 可在空闲时切换 Provider，历史和修改保留。
- [ ] Agent 运行或待审批时不能切换 Provider / Session。
- [ ] /permissions 支持 read-only、ask、auto-edit。
- [ ] read-only 不能写文件或执行项目代码。
- [ ] auto-edit 也不能自动执行测试命令。
- [ ] 不存在任意 Shell 工具。
- [ ] Worktree 外路径、.git、敏感文件和符号链接逃逸被拒绝。
- [ ] patch 校验失败不产生部分写入。
- [ ] Go 修改经过 go/format，批准后可运行 go test。
- [ ] Python 能使用已有 pytest，不自动安装依赖。
- [ ] 未运行或未通过检查时不显示 verified。
- [ ] Ctrl+C 可以取消当前 Turn 并保留已批准修改。
- [ ] 达到 step / time 上限只终止 Turn，不退出 Session。
- [ ] 离线 CI 不依赖真实 LLM 或外部网络。
- [ ] MVP 不包含文件树。

## 17. 主要风险

| 风险 | 应对 |
| --- | --- |
| 无 Sandbox，测试可能执行危险代码 | 所有项目进程审批、结构化命令、明确警告；MVP 不宣称隔离 |
| 多 Session 共享 Worktree 导致修改混淆 | Workspace / Session Diff 分离、patch hash、drift / mixed 提示 |
| 跨 Workspace 切换误操作 | 显示绝对路径、dirty 状态和确认；所有工具显式绑定 WorktreeRoot |
| TUI 增加开发量 | MVP 只做 Conversation + Diff，不做文件树和编辑器 |
| LLM 声称成功 | 成功状态由检查证据生成 |
| 大 Diff 或日志影响渲染 | 分页、截断、viewport 和异步 UI Event |
| Provider tool-calling 差异 | 首次配置执行最小验证，错误保留 Provider 上下文 |
| Provider 切换上下文不兼容 | 中性消息模型、只在 Turn 边界切换、必要时压缩 |
| Session 文件包含敏感代码摘要 | 本地文件权限、明确存储位置、可归档清理、不保存 Key |
| Eino API 变化 | 锁定 v0.9.x，框架类型集中在 agent / provider |
| Python 环境差异 | 不自动安装，清晰报告环境限制 |

## 18. 最终设计结论

CodePilot MVP 是一个单入口、全屏双面板、支持持久多 Session 的本地 Coding Agent。

核心边界：

- Session 管理多轮对话和当前 Provider。
- Session 永久绑定具体 Worktree。
- 同一 Worktree 的多个 Session 共享真实代码状态。
- 切换跨 Workspace Session 时，CodePilot 的活动 Worktree 随之切换。
- Diff 面板区分 Proposed、Session 和 Workspace 变化。
- 文件树不进入 MVP。
- Eino Checkpoint 只用于一次 Run 的 interrupt/resume，不替代 SessionStore。
- 无 Sandbox 时，不开放任意 Shell；审批不能绕过硬性策略。
- Provider 可在 Session 的 Turn 之间切换。
- 主体流程先依赖小接口，再分别实现 Eino、文件系统、Keyring、FileStore 和 TUI adapter。

该方案把用户体验所需的 Session 和 Diff 提升为核心能力，同时仍将 MVP 限制在 Go / Python Bugfix，避免多 Agent、RAG、插件系统、文件树和隔离 Worktree 等过早建设。
