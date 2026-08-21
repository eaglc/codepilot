# CodePilot 详细设计文档

> 文档版本：v0.6  
> 状态：详细设计待评审  
> 更新日期：2026-08-20  
> 对应 PRD：prd.md v0.2

## 1. 文档范围

本文档最终由三部分组成：

| 部分 | 当前状态 | 说明 |
| --- | --- | --- |
| 第一部分：架构设计 | 已完成 | 明确架构风格、层级、模块、依赖与交互边界 |
| 第二部分：逻辑流程图 | 已完成 | 明确启动、Provider 配置、Issue Turn、审批与收尾流程 |
| 第三部分：详细设计 | 已完成，待评审 | 明确存储、完整目录、领域类型、接口方法和实现约束 |

当前版本已经包含架构、逻辑流程和详细设计三部分。

# 第一部分：架构设计

## 2. 架构目标与约束

### 2.1 架构目标

CodePilot 的架构需要同时满足：

1. 支持 codepilot 单入口和持续交互式 Coding Session。
2. 支持 Go / Python Bugfix，并能在后续增加其他语言和任务。
3. 支持持久化多 Session、跨 Workspace 切换和 Session 内切换 Provider。
4. 将 Eino、Bubble Tea、文件系统、Git、Keyring 等第三方或外部能力隔离在明确边界。
5. 在没有 OS Sandbox 的 MVP 中，通过受控工具、硬性策略和审批限制副作用。
6. 允许主体流程先基于 interface 和 fake 实现开发，再逐步接入真实 adapter。
7. 保持 Go 包依赖单向、无循环，避免 DI 框架和过早抽象。
8. 将启动装配集中在轻量组合根中，同时允许模块封装自己的内部构造细节。
9. 将 Coding Session 语义与通用 Agent 调用协议分离，使 Eino 只作为 AgentInvoker 的实现细节。
10. 通过业务无关的 Tool 契约和每 Turn Registry 统一工具注册、模型暴露与调用分发。

### 2.2 设计约束

- 产品是单进程模块化单体，不拆微服务。
- 一个 CodePilot 进程只有一个 Active Session 和一个正在运行的 Agent Turn。
- Session 创建后永久绑定一个 Worktree。
- 同一 Worktree 的多个 Session 共享真实文件状态。
- UI 采用全屏双面板 TUI，MVP 不包含文件树。
- Eino 只负责通用 Invocation 运行；CodePilot 自己管理产品 Session 和 Coding Agent 语义。
- Eino Checkpoint 只恢复单次 Agent Run，不替代 SessionStore。
- 所有工作目录显式传递，不依赖全局 os.Chdir。
- MVP 不提供任意 Shell、full-access 或自动执行项目代码。
- 只有组合根负责连接跨模块依赖；模块只能自行构造本模块的私有组件。
- Tool Registry 只在当前 Turn 内构建和读取，不使用全局注册、init 副作用或反射扫描。

## 3. 架构风格

CodePilot 使用“模块化单体 + Ports and Adapters + 事件驱动 UI”。

- 模块化单体：所有模块编译为一个 Go 二进制，部署和调试简单。
- Ports and Adapters：主体流程依赖小接口，Eino、文件、Keyring 和 TUI 是可替换实现。
- 事件驱动 UI：长时间运行的模型输出、工具状态、审批和 Diff 更新通过 Event 进入 Bubble Tea Update 循环。
- 显式组合根：internal/app 分阶段创建具体实现并完成依赖注入；cmd/codepilot/main.go 只处理进程入口职责。

这不是完整的领域驱动设计，也不引入通用六边形框架。只在已有变化轴和外部边界上使用 interface。

## 4. 架构层级图

### 4.1 核心层级

~~~mermaid
flowchart TB
    subgraph L0["L0 进程入口与应用宿主"]
        CMD["cmd/codepilot<br/>参数、信号与退出码"]
        APP["internal/app<br/>应用生命周期与 Composition Root"]
    end

    subgraph L1["L1 表现层"]
        UI["internal/ui<br/>Bubble Tea 全屏 TUI"]
    end

    subgraph L2["L2 应用层"]
        SESSION["internal/session<br/>Session Service、Turn 生命周期、Ports"]
    end

    subgraph L3["L3 Agent 运行层"]
        AGENT["internal/agent<br/>CodingAgent + 通用 Invocation Runtime"]
        CONTEXT["internal/contextmanager<br/>顺序组合的上下文策略"]
        TOOL["internal/tool<br/>Tool Contract + Registry"]
        PROVIDER["internal/provider<br/>Provider Catalog 与 Model Factory"]
    end

    subgraph L4["L4 业务能力与策略层"]
        WORKSPACE["internal/workspace<br/>Workspace、Git、文件、Patch、Command"]
        LANGUAGE["internal/language<br/>Go / Python 策略"]
        APPROVAL["internal/approval<br/>权限模式与审批"]
        LSP["internal/lsp<br/>Code Intelligence，P1"]
    end

    subgraph L5["L5 基础设施层"]
        STORE["internal/sessionstore<br/>File / Memory SessionStore"]
        CREDENTIAL["internal/credential<br/>Keyring / Memory CredentialStore"]
        CONFIG["internal/config<br/>非敏感配置"]
    end

    subgraph EXT["外部依赖与系统资源"]
        EINO["Eino / EinoExt"]
        LLM["LLM API / Ollama"]
        GIT["Git CLI"]
        OS["File System / os/exec / OS Keyring"]
        LS["gopls / pyright，P1"]
    end

    CMD --> APP
    APP --> UI

    UI --> SESSION
    SESSION --> AGENT
    SESSION --> PROVIDER
    SESSION --> STORE
    SESSION --> APPROVAL
    SESSION --> WORKSPACE

    AGENT --> PROVIDER
    AGENT --> CONTEXT
    AGENT --> TOOL
    AGENT --> WORKSPACE
    AGENT --> LANGUAGE
    AGENT -. "P1" .-> LSP
    WORKSPACE --> APPROVAL

    AGENT --> EINO
    PROVIDER --> EINO
    PROVIDER --> LLM
    PROVIDER --> CREDENTIAL
    PROVIDER --> CONFIG
    WORKSPACE --> GIT
    WORKSPACE --> OS
    STORE --> OS
    CONFIG --> OS
    LSP -. "P1" .-> LS
~~~

图中实线表示主要运行时调用，虚线只表示尚未进入 MVP 的能力。为了保持可读性，本图不展示 internal/app 对所有具体模块的启动装配依赖，装配关系见下一节。

### 4.2 启动装配关系

~~~mermaid
flowchart TB
    CMD["cmd/codepilot/main.go<br/>解析参数、建立根 Context、处理退出码"]
    APP["internal/app<br/>唯一 Composition Root"]

    FOUNDATION["buildFoundation()<br/>Config / SessionStore / CredentialStore / EventBridge"]
    CAPABILITIES["buildCapabilities()<br/>Provider / Approval / Workspace / Language"]
    RUNTIME["buildRuntime()<br/>Context Manager / EinoInvoker / CodingAgent / Session"]
    PRESENTATION["buildPresentation()<br/>Bubble Tea UI"]

    CMD --> APP
    APP --> FOUNDATION
    APP --> CAPABILITIES
    APP --> RUNTIME
    APP --> PRESENTATION

    FOUNDATION -- "Config / CredentialStore" --> CAPABILITIES
    FOUNDATION -- "Config / SessionStore" --> RUNTIME
    CAPABILITIES -- "Model / Tool / Authorizer Ports" --> RUNTIME
    RUNTIME -- "Session API / Event" --> PRESENTATION
~~~

构造按照依赖顺序分为四个阶段，但这些阶段不是新的公共架构层，也不需要建立 foundation、capabilities 等 Go package。MVP 中它们只是 internal/app/build.go 内的私有函数和私有结果结构：

1. buildFoundation 创建配置、状态存储、密钥存储和独立于 TUI Model 的 EventBridge。
2. buildCapabilities 创建 Provider、审批、Workspace 和语言能力。
3. buildRuntime 创建按顺序组合 Strategy 的 Context Manager 和 EinoInvokerFactory，将二者注入 CodingAgentFactory，再将 CodingAgentFactory 和 Store 等 Port 注入 Session。
4. buildPresentation 只使用 Session API 和 Event 通道创建 TUI。

internal/app 统一处理部分构造完成后的失败清理，并在退出时按创建顺序的逆序释放进程、文件句柄和其他资源。

构造边界遵循两条规则：

- 集中装配：只有 internal/app 知道跨模块的具体实现并连接它们。
- 模块内封装：模块可以创建自己的私有组件，但不能创建相邻业务模块。例如 workspace.New 可以创建 PathGuard 和 PatchApplier，但不能创建 approval.Service 或 session.Service。

因此不会形成 UI -> Session -> Agent -> Workspace 的逐层构造链，也不使用 Service Locator、全局服务注册表或 DI 框架。internal/tool.Registry 是单 Turn 的显式值对象，不是全局依赖容器。

### 4.3 Port（跨模块 Go interface）与 Adapter（具体实现）关系

~~~mermaid
flowchart LR
    subgraph CORE["Application Core"]
        SESSION["session.Service"]
        AP["CodingAgent / CodingAgentFactory Port"]
        SP["SessionStore Port"]
        WR["WorkspaceRegistry Port"]
        WV["WorkspaceReader Port"]
        AU["Authorizer Port"]
        ES["EventSink Port"]
        MC["ModelCatalog Port"]

        SESSION --> AP
        SESSION --> SP
        SESSION --> WR
        SESSION --> WV
        SESSION --> AU
        SESSION --> ES
        SESSION --> MC
    end

    subgraph AGENTCORE["Agent Runtime"]
        CODINGAGENT["agent.CodingAgent"]
        INVOKER["AgentInvoker / AgentInvokerFactory Port"]
        EINOAGENT["agent.EinoInvoker<br/>+ EinoInvokerFactory"]
        TOOLREG["tool.Registry<br/>每 Turn 构建"]
        TOOLPORT["tool.Tool interface"]
        WP["WorkspaceTools Port"]
        MF["ModelFactory Port"]
        LR["LanguageResolver Port"]
        CI["CodeNavigator Port，P1"]

        CODINGAGENT --> INVOKER
        CODINGAGENT --> TOOLREG
        TOOLREG --> TOOLPORT
        EINOAGENT --> TOOLREG
        CODINGAGENT --> LR
        CODINGAGENT --> AU
        CODINGAGENT -.-> CI
        EINOAGENT --> MF
    end

    subgraph PROVIDERCORE["Provider Runtime"]
        PROVIDERS["provider.Service"]
        CS["CredentialStore Port"]
        PS["ProviderProfileStore Port"]

        PROVIDERS --> CS
        PROVIDERS --> PS
    end

    subgraph ADAPTERS["Adapters"]
        TUI["ui.EventBridge"]
        FILESTORE["sessionstore.FileStore"]
        MEMORYSTORE["sessionstore.MemoryStore"]
        POLICY["approval.Service"]
        WORKSPACE["workspace.Service"]
        LANG["language.Registry"]
        LSP["lsp.Navigator，P1"]
        FALLBACKKEY["credential.FallbackStore"]
        KEYRING["credential.KeyringStore"]
        MEMORYKEY["credential.MemoryStore"]
        PROFILEFILE["config.ProviderFileStore"]
        CODINGTOOLS["agent.*Tool<br/>MVP Coding Tools"]
    end

    CODINGAGENT -. "implements" .-> AP
    EINOAGENT -. "implements" .-> INVOKER
    CODINGTOOLS -. "implements" .-> TOOLPORT
    CODINGTOOLS --> WP
    FILESTORE -. "implements" .-> SP
    FILESTORE -. "implements" .-> WR
    MEMORYSTORE -. "implements" .-> SP
    MEMORYSTORE -. "implements" .-> WR
    POLICY -. "implements" .-> AU
    TUI -. "implements" .-> ES
    PROVIDERS -. "implements" .-> MC
    PROVIDERS -. "implements" .-> MF
    WORKSPACE -. "implements" .-> WP
    WORKSPACE -. "implements" .-> WV
    LANG -. "implements" .-> LR
    LSP -. "implements" .-> CI
    FALLBACKKEY -. "implements" .-> CS
    FALLBACKKEY --> KEYRING
    FALLBACKKEY --> MEMORYKEY
    PROFILEFILE -. "implements" .-> PS
~~~

Port 定义在消费方附近：

- Session 主流程使用的 CodingAgent / CodingAgentFactory 等 Port 定义在 internal/session。
- CodingAgent 依赖的 AgentInvoker、WorkspaceTools、ModelFactory、LanguageResolver 和 CodeNavigator Port 定义在 internal/agent。
- 通用 Tool interface 与 Registry 位于 internal/tool。Tool 是多个实现共享的稳定执行协议；Registry 是单一具体实现，不为它额外定义 interface。
- Provider 读取密钥和保存非敏感 Profile 所需的 CredentialStore / ProviderProfileStore Port 定义在 internal/provider。
- Adapter 通过 Go 的隐式 interface 实现满足 Port，不需要注册框架。

这里刻意保留两层 Agent 边界：session.CodingAgent 表达“完成一个 Coding Turn”，agent.AgentInvoker 只表达“使用给定消息、模型和工具完成一次可中断的调用”。Bugfix、Worktree、Diff、验证状态等业务概念不能进入 AgentInvoker。

## 5. Go 包依赖规则

### 5.1 允许的依赖方向

| 调用方 | 可以依赖 |
| --- | --- |
| cmd/codepilot | internal/app、标准库 |
| app | 所有 internal 模块，仅用于创建、装配和生命周期管理 |
| ui | session 的公开应用 API、UI 组件库 |
| session | 标准库、自己的领域类型和 Port |
| tool | 标准库；不得依赖 session、agent、workspace 或 Eino |
| agent | session 的 CodingAgent / Event 契约、tool 的 Tool / Registry、自己的 AgentInvoker / WorkspaceTools / ModelFactory / LanguageResolver / CodeNavigator Port、Eino |
| provider | session 的 ModelCatalog 契约、agent 的 ModelFactory 契约、Eino / EinoExt、自己的 CredentialStore / ProviderProfileStore Port |
| workspace | agent 的 WorkspaceTools 契约、session 的 Authorizer 契约、标准库、Git CLI |
| language | agent 的 LanguageResolver 契约、标准库 |
| approval | session 的 Authorizer 契约 |
| sessionstore | session 的实体和 SessionStore 契约 |
| credential | provider 的 CredentialStore 契约、OS Keyring |
| config | provider 的 ProviderProfileStore 契约、标准库、YAML |
| lsp | Agent CodeNavigator 契约、workspace CommandExecutor 契约、JSON-RPC 和语言服务器进程，P1 |

### 5.2 禁止的依赖

- session 不得导入 ui、agent、provider、workspace 或具体存储实现。
- ui 不得直接导入 workspace、Git、Eino 或 Keyring。
- workspace 不得调用 LLM 或构造 Eino Tool。
- provider 不得读写 Workspace。
- agent 不得直接访问配置文件、Keyring 或 Session 文件。
- tool 不得导入任何 CodePilot 业务 package，也不得执行文件、Git、命令或网络操作。
- adapter 不得反向控制 Session 生命周期。
- 除 internal/app 外，模块不得构造其他业务模块的具体实现。
- 模块的 New 函数只能创建本模块的私有组件，并通过参数接收跨模块依赖。
- 不允许 package-level 全局可变依赖。
- 不允许通过 common、utils、Service Locator 或全局服务注册表绕过层级。
- 不允许让 UI 构造 Session、Session 构造 Agent 等方式形成逐层构造链。

### 5.3 为什么 session 是应用核心

Session 是用户体验和运行状态的中心：

- 决定当前 Worktree。
- 管理多轮消息和 Turn 状态。
- 管理当前 Provider / Model。
- 管理 Session 切换和跨 Workspace 切换。
- 持有权限模式和运行限制。
- 调用 CodingAgentFactory 创建当前 CodingAgent。
- 接收 CodingAgent Event 并持久化结果。

CodingAgent 是 Session 使用的业务执行边界，EinoInvoker 是其下层 Invocation Adapter，而不是整个应用的顶层控制器。这样可以避免 UI、Session 和持久化被 Eino 生命周期绑死。

## 6. 核心模块职责

### 6.1 cmd/codepilot：进程入口

职责：

- 解析最少量的启动参数。
- 建立根 context 并监听系统信号。
- 将当前目录和启动选项传给 app.New。
- 调用 App.Run 并处理最终退出码。

不负责：

- 加载业务配置。
- 创建任何具体业务模块。
- Session 业务逻辑。
- 资源生命周期管理。

交互方式：

- 只依赖 internal/app 和标准库。
- 收到系统信号后取消根 context。
- 根据 App.Run 返回的错误决定退出码。

### 6.2 internal/app：应用宿主与组合根

职责：

- 加载配置并解析程序状态目录。
- 按 Foundation、Capabilities、Runtime、Presentation 四个阶段组装模块。
- 将具体 Adapter 注入消费方定义的 Port。
- 创建 App，统一启动 TUI 并管理进程级资源生命周期。
- 构造失败时释放已经创建的资源。
- 正常退出时按逆序关闭 LSP、Checkpoint、Store 等有生命周期的资源。

建议的内部文件：

- app.go：App、Run、Close 和进程生命周期。
- build.go：buildFoundation、buildCapabilities、buildRuntime、buildPresentation。

不负责：

- Session 或 Agent 业务编排。
- Provider、Git、存储和审批的实现细节。
- 保存可在运行期访问的全局依赖容器。
- 通过 Service Locator 向模块动态提供依赖。

交互方式：

- app 可以导入所有具体 internal 模块，但其他模块不得反向导入 app。
- build 函数返回仅在 app 包内使用的私有分组结构，不作为业务 API。
- 模块之间仍通过明确构造参数和小 interface 连接。
- App 构造完成后，不参与正常的 Turn 调度和工具调用。

### 6.3 internal/ui：表现层

职责：

- 实现 Bubble Tea Model、Update、View。
- 渲染 Header、Conversation、Diff、Composer、Picker 和 Approval Overlay。
- 解析 Slash Command 和快捷键。
- 将用户输入转换为 Session Command。
- 将 Session / Agent Event 转换为局部 UI 更新。
- 收集用户的审批、Provider 和 Session 选择。

核心功能：

- Conversation 与 Diff 双面板。
- Provider Picker、Session Picker、Permission Picker。
- Proposed / Session / Workspace Diff 切换。
- Agent streaming、工具状态、错误和取消反馈。
- 窄终端响应式布局。

不负责：

- 决定是否允许危险操作。
- 直接执行 patch、Git 或测试。
- 直接构造 ChatModel。
- 自己保存 Session。

交互方式：

- UI 到 Session：同步提交短命令，异步启动长 Turn。
- Session 到 UI：通过 EventSink 发布事件。
- 审批：显示 ApprovalRequested，用户选择后提交 ApprovalDecision。
- 所有 UI 状态变化必须回到 Bubble Tea Update 循环，后台 goroutine 不直接修改 UI Model。

### 6.4 internal/session：应用编排层

职责：

- 创建、加载、保存、重命名、归档和切换 Session。
- 管理 Active Session 与 Active Turn。
- 校验只能在空闲状态切换 Session / Provider。
- 管理 Session 与 Worktree 的不可变绑定关系。
- 组织用户消息、历史上下文、Provider / Model 和权限模式。
- 调用 CodingAgentFactory 创建 CodingAgent，并调用 CodingAgent.RunTurn。
- 将 Turn 结果、patch 记录和状态写入 SessionStore。
- 对外提供 UI 所需的应用级 Command 和 Query。

核心状态：

- Active Session。
- Turn 状态：idle、running、awaiting-approval、cancelling。
- Provider / Model selection。
- Permission mode。
- Context cancellation handle。

不负责：

- 通用 Invocation 和 Eino Runner 内部实现。
- 文件路径安全算法。
- Provider API 协议。
- JSON / JSONL 文件格式。
- TUI 渲染。

交互方式：

- 使用 CodingAgent / CodingAgentFactory Port 调用 CodingAgent。
- 使用 SessionStore Port 持久化。
- 使用 Authorizer Port 处理副作用审批。
- 使用 ModelCatalog Port 处理 Provider 选择和验证。
- 使用 WorkspaceReader 和 WorkspaceRegistry Port 解析、激活并查询 Worktree。
- 使用 EventSink 发布稳定的产品 Event，不暴露 Eino Event。

### 6.5 internal/tool：通用 Tool 契约与 Registry

职责：

- 定义与 Coding、Workspace、Eino 无关的 Tool、Definition、Result 和 Interrupt 数据契约。
- 使用 Registry 按稳定名称注册、查询和有序列举多个 Tool。
- 在注册时拒绝空名称、重复名称，以及不是合法 JSON object 的 InputSchema。
- 向 AgentInvoker 提供当前 Turn 可用的工具定义和对应执行对象。

不负责：

- 选择当前 Turn 应该启用哪些 Tool。
- 保存全局 Tool 或自动扫描实现。
- 解释 Worktree、权限模式、命令策略或语言能力。
- 将 Tool 转换为 Eino Tool。

交互方式：

- CodingAgent 在每个 Turn 准备阶段创建一个 Registry，并显式注册当前可用 Tool。
- AgentInvoker 只读取 Registry；EinoInvoker 使用 Definitions 投喂模型，并按 ToolCall 名称 Lookup 后执行。
- Registry 进入 Invoke 后不再修改，Turn 结束后随 CodingAgent 当前调用释放。

### 6.6 internal/agent：Coding Agent 与通用 Invocation 运行层

职责：

- 使用 agent.CodingAgent 实现 session.CodingAgent，承接 Coding Session 语义。
- 根据 Session、Provider、Language 和 Worktree 构建 system prompt、中性上下文和受控工具集合。
- 在调用 AgentInvoker 前，通过 Context Manager 顺序应用多个 provider-neutral 上下文策略。
- 将 MVP Coding 能力实现为多个 tool.Tool，并为当前 Turn 组装 Registry。
- 通过业务无关的 AgentInvoker 发起或恢复一次 Invocation。
- 使用 EinoInvoker 实现 AgentInvoker，并在这一实现中封装 ChatModelAgent、Runner、事件流和 Checkpoint API。
- 将中性的 Invocation Event 转换成 CodePilot Event。
- 管理单个 Turn 的 step、timeout、stream、cancellation 和 approval interrupt/resume。
- 配置内存级 Eino CheckPointStore，支持同进程 interrupt/resume。

核心功能：

- list_files、search_code、read_file。
- git_status、git_diff。
- apply_patch。
- run_checks。
- P1 的 definition、references、diagnostics。

不负责：

- 保存产品 Session。
- 直接读取 Keyring。
- 决定 UI 如何展示事件。
- 绕过 Workspace 或 Authorizer 执行副作用。
- 自行将模型回答判定为 verified。
- 让 AgentInvoker 感知 Bugfix、Workspace、Diff 或 SessionStore 等业务概念。

交互方式：

- CodingAgent 通过 WorkspaceTools 和 LanguageResolver 构造 Tool 实现，通过 Context Manager 处理 prompt / messages，再组装 InvocationInput。
- CodingAgent 调用 AgentInvoker.Invoke；审批恢复时调用 AgentInvoker.Resume。
- EinoInvoker 从 ModelFactory 获取当前 ChatModel，并将中性 Tool / Event 适配到 Eino。
- CodingAgent 将事件写入 Session 提供的 EventSink。
- Provider 切换后销毁旧 CodingAgent / AgentInvoker，由 Session 通过 CodingAgentFactory 重建。

### 6.7 internal/provider：模型 Provider 层

职责：

- 维护预置 Provider Catalog。
- 提供 OpenAI、DeepSeek、Ollama 和 Custom OpenAI-compatible 配置。
- 将用户选择转换为 Eino ChatModel。
- 验证认证、模型可用性和最小 tool-calling 能力。
- 枚举可选模型和推荐默认模型。
- 通过 CredentialStore 读取和保存 Key。
- 通过 ProviderProfileStore 读取和保存非敏感 Profile。
- 对 Session 暴露中性 Provider / Model 描述。

核心功能：

- Provider 配置和校验。
- ChatModel 创建。
- Provider-specific error 归一化。
- Secret 脱敏。

不负责：

- 保存 Session 消息。
- 访问 Workspace。
- 执行 Agent Turn。
- 在 UI 中直接弹出选择器。

交互方式：

- 实现 Session 使用的 ModelCatalog。
- 实现 Agent 使用的 ModelFactory。
- 通过 CredentialStore 获取密钥。
- 通过 ProviderProfileStore 持久化 Provider / Model / Base URL 和 credential reference。
- 远程 Provider 访问 LLM API；Ollama 访问本机服务。

### 6.8 internal/workspace：代码工作区能力层

职责：

- 识别 Workspace 和 Worktree。
- 维护显式 WorktreeRoot 边界。
- 提供文件列举、搜索和分段读取。
- 提供 Git status、diff 和 tracked-files。
- 校验和应用 unified diff。
- 生成 Proposed / Session / Workspace Diff 数据。
- 执行结构化、受控的检查命令。
- 执行规范化路径、符号链接和敏感文件校验。

核心安全职责：

- 禁止 Worktree 外路径。
- 禁止写入 .git。
- 禁止任意 Shell。
- 拦截禁止的 program 和参数。
- 在 patch 和命令执行前调用 Authorizer。
- 为子进程设置明确 Dir、timeout、环境和输出上限。

不负责：

- 理解 issue。
- 选择 Provider。
- 决定 Agent 下一步。
- 渲染 Diff。
- 保存 Session 消息。

交互方式：

- 实现 Agent 使用的 WorkspaceTools。
- 使用 Authorizer 获得一次性或 Session 级授权。
- 使用 Git CLI 和 os/exec 访问外部系统。
- 返回结构化结果，不把无限 stdout 直接交给 Agent。

### 6.9 internal/language：语言策略层

职责：

- 识别当前 Worktree 的主要语言。
- 提供语言特定 prompt hint。
- 生成允许执行的格式化和检查 CommandSpec。
- 解释项目中已有的测试配置。

MVP 实现：

- Go：go.mod 检测、go/format、go test、可选 go vet。
- Python：pyproject.toml 等检测、pytest 策略，不自动安装依赖。

不负责：

- 直接启动命令。
- 处理用户审批。
- 调用 LLM。
- 保存语言状态。

交互方式：

- 通过 LanguageResolver 向 Agent 提供 LanguageProfile。
- 向 Workspace 提供结构化 CommandSpec，而不是 Shell 字符串。

### 6.10 internal/approval：权限与审批策略层

职责：

- 实现 read-only、ask、auto-edit 三种 Permission Mode。
- 将 Action 分类为 allow、prompt 或 deny。
- 保存当前进程和当前 Session 的临时授权。
- 将需要用户判断的动作转换为 ApprovalRequest。
- 接收 ApprovalDecision 并恢复等待中的调用。

硬性规则：

- 用户批准不能绕过路径和命令 deny rule。
- auto-edit 只预授权 Worktree 内、通过校验的 patch。
- 执行项目代码始终需要审批。
- Session 切换和程序退出后临时授权失效。

不负责：

- 提供 OS Sandbox。
- 自己执行命令或 patch。
- 将审批结果持久化为永久权限。

交互方式：

- 实现 Session 的 Authorizer Port。
- 通过 EventSink 向 UI 发出 ApprovalRequested。
- 等待 UI 通过 Session Service 提交 Decision。
- 将最终 Decision 返回 Workspace 调用方。

### 6.11 internal/sessionstore：Session 持久化层

职责：

- 实现 SessionStore。
- 保存 Workspace registry、Session metadata、messages 和 patch records。
- 提供 FileStore 正式实现和 MemoryStore 测试实现。
- 负责原子 metadata 更新、追加日志校验和版本迁移入口。

不负责：

- 决定 Session 是否允许切换。
- 解释消息内容。
- 保存 API Key。
- 保存 Eino 内存 Checkpoint。

交互方式：

- Session Service 通过 Store Port 调用。
- FileStore 使用本地文件系统。
- 所有持久化错误返回 Session Service，由 UI 展示可操作错误。

### 6.12 internal/credential：密钥存储层

职责：

- 使用 OS Keyring 保存、读取和删除 API Key。
- 提供 MemoryStore 用于测试和 Keyring 不可用时的当前进程回退。
- 由 FallbackStore 区分可回退的 Keyring 不可用错误和其他凭据错误。
- 保证日志和错误中不输出 secret。

不负责：

- 保存 Provider Base URL 和模型名。
- 验证 Provider。
- 把 Key 写入 YAML 或 Session 文件。

交互方式：

- 实现 Provider 定义的 CredentialStore Port。
- Keyring 不可用时返回可识别错误，由 Provider / UI 提示 Key 仅当前进程有效。

### 6.13 internal/config：程序配置层

职责：

- 解析程序版本化配置。
- 保存默认 Provider / Model 和 Agent 运行限制。
- 以 providers.yaml 实现 ProviderProfileStore。
- 解析系统配置目录和状态目录。
- 使用临时文件 + rename 原子更新配置。

不负责：

- 保存 Session。
- 保存 API Key。
- 保存代码片段和 Eino Checkpoint。

交互方式：

- 启动时由 Composition Root 加载。
- 以不可变配置值注入其他模块。
- 运行期修改默认 Provider 时通过 config.Save 原子更新。
- Provider Service 只通过 ProviderProfileStore Port 访问 providers.yaml，不直接依赖 YAML。

### 6.14 internal/lsp：代码智能层（P1）

职责：

- 实现 Agent 的 CodeNavigator。
- 管理 gopls / pyright JSON-RPC 连接。
- 提供 definition、references、symbols、diagnostics 和 hover。
- Active Worktree 切换时关闭旧进程，目标 Session 使用时再懒加载。

约束：

- 不作为 MVP 成败依赖。
- Language Server 缺失时退化为 search / read。
- 启动外部进程前走 Authorizer。
- 默认过滤 Worktree 外返回位置。

LSP 是编辑器与语言服务器之间的标准 JSON-RPC 协议，适合封装为独立代码智能 adapter。

## 7. 模块间交互方式

### 7.1 同步调用

适用于短操作：

- Session list、status、rename、archive。
- Provider 列表查询。
- Workspace 路径预检。
- 配置读取。

同步方法必须快速返回，不得在 Bubble Tea Update 中直接执行模型调用、网络校验或长时间 Git 命令。

### 7.2 异步 Event

适用于长操作：

- Agent streaming。
- Tool started / completed。
- Check running。
- Diff changed。
- Approval requested。
- Turn completed / failed / cancelled。

统一使用产品 Event，不允许 UI 直接依赖 Eino AgentEvent。

建议事件类别：

| 类别 | 示例 |
| --- | --- |
| SessionEvent | SessionActivated、SessionSaved、WorkspaceChanged |
| TurnEvent | TurnStarted、TurnCompleted、TurnCancelled |
| AgentEvent | AssistantDelta、AgentStatusChanged |
| ToolEvent | ToolStarted、ToolCompleted、ToolFailed |
| ApprovalEvent | ApprovalRequested、ApprovalResolved |
| DiffEvent | ProposedDiffChanged、WorkspaceDiffChanged、SessionDiffDrifted |
| ProviderEvent | ProviderValidationStarted、ModelChanged |

事件只携带 UI 和持久化需要的稳定字段，不携带 context、文件句柄、Eino iterator 或 secret。

### 7.3 请求与响应

UI 命令转换为应用请求：

    UI Input
      -> Session Command
      -> Session Service
      -> Result 或 Event Stream

用户自然语言消息是 StartTurn；Slash Command 是独立应用命令。两者不能混在 Agent prompt 中解析，以免模型获得 Session、Provider 或权限控制权。

### 7.4 审批交互

审批使用 request / decision 模型：

    Workspace side effect
      -> Authorizer.Authorize
      -> ApprovalRequested Event
      -> UI Overlay
      -> ApprovalDecision
      -> Authorizer resumes caller

等待审批期间 Turn 状态为 awaiting-approval。此时禁止切换 Session 和 Provider，但允许 Ctrl+C 取消 Turn。

### 7.5 取消与超时

- 根 context：进程生命周期。
- Session context：Active Session 生命周期。
- Turn context：单个 Agent Turn。
- Tool context：单次工具调用。
- Command context：单个子进程及其 timeout。

父 context 取消必须向下传播。子模块不得保存 context 到长期 struct 中，只保存 cancel function 或由调用者传入 context。

### 7.6 Session 与 Provider 切换

- 切换只允许在 Turn idle 时执行。
- Session Service 先保存旧 Session，再释放旧 CodingAgent。
- 切换 Worktree 时刷新 Workspace 能力和 Diff。
- 切换 Provider 时只重建 CodingAgent / AgentInvoker，不改变 Worktree。
- Provider 切换保留中性 user / assistant 历史，不复用厂商私有 tool-call 对象。

## 8. 数据与状态所有权

| 数据 | 唯一所有者 | 其他模块访问方式 |
| --- | --- | --- |
| 进程级资源生命周期 | app.App | Run / Close、根 Context |
| Active Session / Turn 状态 | session.Service | UI Command / Query、Event |
| 持久 Session 文件 | sessionstore | SessionStore Port |
| Provider Catalog / Profile | provider.Service | ModelCatalog Port |
| API Key | credential store | CredentialStore Port |
| 工作区文件和 Git 状态 | 用户 Worktree / workspace | WorkspaceTools Port |
| 权限模式和临时授权 | session + approval | Authorizer Port |
| Coding Turn 编排 | agent.CodingAgent | CodingAgent Port |
| 当前 Turn Tool Catalog | tool.Registry，由 agent.CodingAgent 创建 | AgentInvoker 只读访问 |
| Invocation / Eino Runner / Checkpoint | agent.EinoInvoker | AgentInvoker Port |
| TUI Model | ui | Bubble Tea Update |
| 程序配置 | config | 启动注入的配置值 |
| LSP 进程 | lsp.Navigator，P1 | CodeNavigator Port |

一个状态只能有一个模块作为最终所有者。UI 可以缓存用于渲染的数据，但不能成为 Session、Git 或权限状态的事实来源。

## 9. 安全边界

### 9.1 应用内硬边界

- WorktreeRoot 绝对路径与规范化校验。
- 符号链接逃逸检查。
- .git 和敏感文件保护。
- 结构化 CommandSpec。
- program / args deny rules。
- patch check 后再写入。
- 命令 timeout 和输出截断。
- secret 不进入 Event、日志和 Session。
- 模型只提交 Tool arguments；TurnScope、WorktreeRoot、权限和执行限制由具体 Tool 实例捕获，不能由 arguments 覆盖。

### 9.2 用户审批边界

- read-only 拒绝写入和项目执行。
- ask 对 patch 和项目执行请求批准。
- auto-edit 只自动允许合法 patch。
- 项目测试在所有模式下都需批准。

### 9.3 MVP 无法提供的边界

MVP 没有 OS Sandbox。获批的测试进程仍拥有当前用户权限，可能访问网络或系统资源。CodePilot 必须明确展示这一限制，不能把审批描述为隔离。

后续 Sandbox 只替换 CommandExecutor adapter，不改变 CodingAgent、Session 和 UI 的调用语义。

## 10. 可扩展性检查

| 变化 | 新增或替换模块 | 不应修改 |
| --- | --- | --- |
| 新语言 | language adapter | Session、UI、Provider |
| 新 Provider | provider adapter | Session Turn、Workspace |
| 文件树 | ui panel + Workspace query | CodingAgent、权限、SessionStore |
| LSP | lsp CodeNavigator adapter | Session、Provider、Diff |
| 独立 Git Worktree Session | WorktreeProvisioner | Session 绑定模型、CodingAgent |
| OS / Container Sandbox | CommandExecutor adapter | Tool schema、Session、UI |
| 持久 Eino Checkpoint | CheckPointStore adapter | SessionStore |
| 新 Coding Tool | 新增 tool.Tool 实现并显式注册 | AgentInvoker、EinoInvoker、Session |
| Review / Test Generation | 新业务 Agent，复用 AgentInvoker | Invocation Runtime、Provider、TUI 框架 |

扩展时优先增加 adapter 或策略实现。只有第二个真实调用者出现并造成重复后，才抽取新的共享接口。

## 11. 架构测试策略

- app：使用临时目录和 fake 外部依赖执行装配 smoke test，验证构造失败清理与逆序 Close。
- session：使用 FakeCodingAgent、MemorySessionStore、FakeAuthorizer、RecordingEventSink 测试主体状态机。
- ui：固定窗口尺寸，对 Model / Update / View 做 golden test。
- tool：测试重复名称、非法 Definition、稳定顺序、Lookup 和返回副本。
- agent：使用 ScriptedInvoker 测试 CodingAgent 的输入组装与事件映射；使用 scripted ChatModel 测试 EinoInvoker 的通用工具循环和 interrupt/resume。
- workspace：使用临时 Git Worktree 测试路径、patch、diff 和命令策略。
- provider：使用 httptest 或 fake transport 测试认证、模型枚举和错误脱敏。
- sessionstore：测试原子写、损坏记录、版本不兼容和恢复。
- approval：表驱动测试三种权限模式和硬性 deny rule。
- credential：MemoryStore 做行为测试；Keyring 做平台集成测试。

架构测试的目标不是验证具体 UI 文案，而是保证模块只能通过声明的 Port 交互，主体流程可以在无网络、无 Eino 真模型、无真实 Keyring 的情况下运行。

## 12. 架构评审检查项

- [ ] 是否认可模块化单体，而不是微服务或插件平台。
- [ ] 是否认可 Session Service 作为应用核心，Eino 作为执行 adapter。
- [ ] 是否认可 session.CodingAgent 与 agent.AgentInvoker 的两层边界，业务语义不进入通用 Invoke 协议。
- [ ] 是否认可 Session 永久绑定 Worktree。
- [ ] 是否认可同一 Worktree 多 Session 共享真实代码状态。
- [ ] 是否认可 UI 只依赖应用 API，不直接调用 Workspace / Provider adapter。
- [ ] 是否认可 Ports 定义在消费方附近。
- [ ] 是否认可 cmd/codepilot 只作为进程入口，internal/app 作为唯一 Composition Root。
- [ ] 是否认可 internal/app 分阶段集中装配，但不引入分层 Builder package 或 DI 框架。
- [ ] 是否认可模块只能构造自己的私有组件，跨模块具体实现只能由 internal/app 连接。
- [ ] 是否认可 FileSessionStore 与 Eino Checkpoint 分离。
- [ ] 是否认可 Provider 切换通过重建 CodingAgent / AgentInvoker 完成。
- [ ] 是否认可无 Sandbox 时仍禁止任意 Shell，并保留项目执行审批。
- [ ] 是否认可 LSP 和文件树不进入 MVP。
- [ ] 是否存在需要在逻辑流程设计前调整的模块边界。

---

# 第二部分：逻辑流程图

## 13. 流程范围与约定

本部分描述从用户执行 codepilot 到完成一个 Issue Turn 的核心逻辑。流程按职责拆为四段：

1. 程序启动、Workspace / Session 激活和首次 Provider 配置。
2. 用户输入 Issue 后的 Session 与 Agent 主循环。
3. 单次工具调用的校验、审批和执行。
4. Turn 状态判定、持久化和 UI 收尾。

流程中的基本约定：

- 一个进程只有一个 Active Session，且同一时间最多运行一个 Turn。
- 自然语言输入转换为 StartTurn；Slash Command 由 UI Command Router 处理，不进入 Agent Prompt。
- 长操作不阻塞 Bubble Tea Update；后台流程只通过 Event 更新 UI。
- Agent 启动后固定使用本 Turn 的 Session、WorktreeRoot、Provider / Model 和 Permission 快照。
- 所有文件和子进程操作显式携带 WorktreeRoot，不调用全局 os.Chdir。
- 审批只能允许通过硬性安全校验的动作，不能覆盖 deny rule。
- Workspace 是代码事实来源；SessionStore 持久化对话和产品状态，不负责回滚代码。

Turn 的主要状态如下：

| 状态 | 含义 | 允许的用户操作 |
| --- | --- | --- |
| idle | 没有正在执行的 Turn | 输入消息、切换 Provider、切换 Session、退出 |
| running | 模型生成或工具执行中 | 查看事件和 Diff、Ctrl+C 取消 |
| awaiting-approval | 等待 patch 或检查命令授权 | 选择审批结果、Ctrl+C 取消 |
| cancelling | 已发出取消，等待模型或工具退出 | 查看状态，不允许启动新 Turn |

## 14. 启动并进入可交互 Session

### 14.1 启动流程图

~~~mermaid
flowchart TD
    START["用户在仓库目录执行 codepilot"]
    MAIN["cmd/codepilot<br/>解析参数、建立根 Context"]
    BUILD["app.New<br/>分阶段构造应用"]
    BUILT{"构造成功？"}
    CLEANUP["逆序释放已创建资源<br/>输出启动错误"]
    EXIT_ERROR["以非零状态退出"]

    RESOLVE["从当前目录向上解析 Git WorktreeRoot"]
    IS_GIT{"找到 Git Worktree？"}
    TRUSTED{"Workspace 已登记并受信任？"}
    TRUST["展示规范化路径、仓库信息和风险说明"]
    TRUST_DECISION{"用户信任？"}
    EXIT_OK["保存必要状态并正常退出"]
    REGISTER["登记或刷新 Workspace 元数据"]

    LOAD_SESSION["查找该 Worktree 最近使用的 Session"]
    HAS_SESSION{"存在可用 Session？"}
    CREATE_SESSION["创建 Session<br/>绑定 Worktree、记录 baseline、权限 ask"]
    RESTORE_SESSION["加载消息、Provider、权限和 patch 记录"]
    ACTIVATE["激活 Session<br/>刷新 Git 与 Diff 状态"]
    START_TUI["启动全屏 TUI"]

    HAS_PROVIDER{"Session 有可用 Provider / Model？"}
    PICKER["自动打开 /model Provider Picker"]
    PICK["用户选择 Provider"]
    CANCEL_PROVIDER{"取消配置？"}
    INPUT_CREDENTIAL["收集 Provider 配置<br/>远程：隐藏 Key；Ollama：无需 Key；Custom：Base URL / Model"]
    CHECK_ACCESS["通过模型目录检查 Endpoint 与认证<br/>不调用模型、不消耗 token"]
    ACCESS{"目录检查成功且存在模型？"}
    SHOW_ACCESS_ERROR["显示认证或网络错误<br/>不记录明文 Key"]
    SAVE_PROVIDER["有 Key 时写入 Keyring<br/>不可用时提示仅存当前进程内存<br/>保存非敏感配置和 credential reference"]
    PICK_MODEL["只展示 Provider 实际返回的模型<br/>用户选择具体 Model"]
    NOTICE_MODEL["提示将执行最小 tool-calling 探测<br/>可能消耗少量 token"]
    VALIDATE_MODEL["Provider Adapter 验证所选模型<br/>可用性和 tool calling"]
    MODEL_VALID{"所选模型验证成功？"}
    SHOW_MODEL_ERROR["显示模型、额度或能力错误<br/>保留模型列表供重新选择"]
    BIND_PROVIDER["保存 Session 的 ProviderID / ModelID<br/>构建 CodingAgent / AgentInvoker"]
    READY["加载历史与 Diff<br/>Turn 状态 idle，输入框可用"]
    NO_PROVIDER["留在 TUI 的未配置状态<br/>提交自然语言时重新打开 /model"]

    START --> MAIN --> BUILD --> BUILT
    BUILT -- "否" --> CLEANUP --> EXIT_ERROR
    BUILT -- "是" --> RESOLVE --> IS_GIT
    IS_GIT -- "否" --> CLEANUP
    IS_GIT -- "是" --> TRUSTED
    TRUSTED -- "是" --> REGISTER
    TRUSTED -- "首次访问" --> TRUST --> TRUST_DECISION
    TRUST_DECISION -- "拒绝" --> EXIT_OK
    TRUST_DECISION -- "信任" --> REGISTER

    REGISTER --> LOAD_SESSION --> HAS_SESSION
    HAS_SESSION -- "否" --> CREATE_SESSION --> ACTIVATE
    HAS_SESSION -- "是" --> RESTORE_SESSION --> ACTIVATE
    ACTIVATE --> START_TUI --> HAS_PROVIDER

    HAS_PROVIDER -- "是" --> BIND_PROVIDER --> READY
    HAS_PROVIDER -- "否" --> PICKER --> PICK --> CANCEL_PROVIDER
    CANCEL_PROVIDER -- "是" --> NO_PROVIDER
    NO_PROVIDER -. "执行 /model 或提交消息" .-> PICKER
    CANCEL_PROVIDER -- "否" --> INPUT_CREDENTIAL --> CHECK_ACCESS --> ACCESS
    ACCESS -- "否" --> SHOW_ACCESS_ERROR --> PICKER
    ACCESS -- "是" --> SAVE_PROVIDER --> PICK_MODEL --> NOTICE_MODEL --> VALIDATE_MODEL --> MODEL_VALID
    MODEL_VALID -- "否" --> SHOW_MODEL_ERROR --> PICK_MODEL
    MODEL_VALID -- "是" --> BIND_PROVIDER
~~~

### 14.2 核心说明

1. cmd/codepilot 不创建业务模块，只建立根 context 并调用 app.New。
2. app.New 任一阶段失败时，必须关闭已经创建的资源，再将启动错误交给入口处理。
3. WorktreeRoot 必须经过绝对路径和符号链接规范化。MVP 在非 Git 目录直接退出，不自动创建仓库。
4. 首次访问 Worktree 时先确认信任，拒绝后不创建 Session，也不执行仓库内容。
5. Session 创建后永久绑定当前 Worktree。已有 Session 恢复其中性消息、Provider、权限和 patch 记录；新 Worktree 没有历史 Session 时，从 Provider Catalog 选择 `ValidatedAt` 最新且凭据仍可用的 Profile / Model 作为默认值。
6. TUI 启动后才进行需要用户交互的 Provider 配置。取消配置不会退出程序，但自然语言 Turn 暂时不可启动。
7. Provider 目录检查与所选模型验证由 Adapter 分阶段返回结构化结果；Agent 不参与验证，也不能接触原始 Key。
8. 到达 READY 后只表示可以接收输入，程序不会自动分析仓库或开始修复。

Provider Picker 将已配置模型和新增 Provider 入口分成两个可见分区。已配置项按规范化后的 Provider kind、Base URL 和 ModelID 去重；当前 Session 使用的 Profile 优先保留，否则保留 `ValidatedAt` 最新的重复项。已配置区的每一行都是完整 Provider / Model 选择，按 Enter 后直接调用 SwitchModel，不再加载第二层 Model Picker；只有完成新增 Provider 配置后才加载其模型目录。当前 Session 的选择保持为默认光标位置，选择当前项直接关闭 Picker。

DeepSeek V4 默认启用 thinking mode。该模式支持 tool calls，但拒绝 OpenAI-compatible `tool_choice` 字段；因此 DeepSeek Adapter 必须在调用时只传入工具定义，通过明确提示触发探测，不得使用 `WithTools` 绑定或显式 tool choice。正式 Agent 循环必须保留并回传带工具调用的 assistant 消息中的 `reasoning_content`。

## 15. 用户输入 Issue 后的 Agent 主循环

### 15.1 Issue Turn 流程图

~~~mermaid
flowchart TD
    READY["Session idle，等待输入"]
    INPUT["用户输入内容并提交"]
    ROUTE{"以 / 开头？"}
    COMMAND["UI Command Router<br/>解析并执行应用命令"]
    COMMAND_RESULT["通过 Result / Event 更新 UI"]

    START_TURN["UI 提交 StartTurn"]
    PRECHECK{"Session 预检通过？"}
    REJECT["显示原因并保留输入<br/>不创建 Turn"]
    SNAPSHOT["固定 Turn 快照<br/>SessionID / Worktree / Model / Permission / Limits"]
    APPEND["追加用户消息并保存 Session"]
    SAVED{"保存成功？"}
    SAVE_ERROR["显示持久化错误<br/>保持 idle，不调用模型"]
    RUNNING["创建 Turn Context<br/>状态改为 running，发布 TurnStarted"]
    PREPARE["CodingAgent 准备中性历史、System Prompt 和 Language Hint"]
    REGISTER["创建当前 Turn 的 tool.Registry<br/>显式注册可用 Tool 实例"]
    MANAGE_CONTEXT["Context Manager 顺序处理<br/>System Prompt 和中性 Messages"]
    INVOKE["调用 AgentInvoker.Invoke<br/>提交业务无关的 InvocationInput"]
    MODEL["EinoInvoker / Runner 调用 ChatModel"]
    EVENT{"收到 Invocation Event"}
    DELTA["发布 AssistantDelta<br/>局部刷新 Conversation"]
    TOOL["进入工具调用子流程<br/>见第 16 节"]
    TOOL_RESULT["结构化 ToolResult 返回 AgentInvoker"]
    LIMIT{"达到 step / time 限制？"}
    STOP_LIMIT["取消 Turn Context<br/>终止原因为 limit"]
    FINAL["收到模型最终响应"]
    ERROR["模型、Provider 或 Invocation 错误"]
    CANCEL["用户 Ctrl+C<br/>状态改为 cancelling"]
    FINISH["进入 Turn 收尾流程<br/>见第 17 节"]

    READY --> INPUT --> ROUTE
    ROUTE -- "是" --> COMMAND --> COMMAND_RESULT --> READY
    ROUTE -- "否" --> START_TURN --> PRECHECK
    PRECHECK -- "否" --> REJECT --> READY
    PRECHECK -- "是" --> SNAPSHOT --> APPEND --> SAVED
    SAVED -- "否" --> SAVE_ERROR --> READY
    SAVED -- "是" --> RUNNING --> PREPARE --> REGISTER --> MANAGE_CONTEXT --> INVOKE --> MODEL --> EVENT

    EVENT -- "AssistantDelta" --> DELTA --> EVENT
    EVENT -- "ToolCall" --> TOOL --> TOOL_RESULT --> LIMIT
    LIMIT -- "否" --> MODEL
    LIMIT -- "是" --> STOP_LIMIT --> FINISH
    EVENT -- "FinalResponse" --> FINAL --> FINISH
    EVENT -- "Error" --> ERROR --> FINISH

    RUNNING -. "任意运行阶段 Ctrl+C" .-> CANCEL
    MODEL -. "Ctrl+C" .-> CANCEL
    TOOL -. "Ctrl+C" .-> CANCEL
    CANCEL --> FINISH
~~~

### 15.2 Session 预检

StartTurn 只有同时满足以下条件才会开始：

- Active Session 存在且状态为 idle。
- Session 绑定的 Workspace 和 WorktreeRoot 当前可访问。
- Provider / Model 已配置，Credential 可获取。
- 用户输入去除空白后不为空。
- 没有未完成的 Session / Provider 切换。
- 上一个 cancelling Turn 已经释放模型流和工具进程。

预检失败属于可恢复的用户操作错误，不写入用户消息，也不调用 CodingAgent。

### 15.3 输入路由与 Turn 快照

- Slash Command 在进入 Session Service 前由 UI 识别，/model、/session、/permissions、/diff 和 /exit 不会被追加到模型上下文。
- Composer 以 `/` 开头时，UI 将多级命令和枚举参数展开为可执行叶子项，并根据包含空格的完整命令前缀本地过滤；选择只修改 Composer 文本，完整命令仍由同一 Command Handler 解析。
- 当前 Composer token 以 `@` 开头时，UI 通过 Session Service 获取最多 500 个 Git 可见安全文件和由这些文件推导出的目录并本地过滤。目录以 `/` 结尾，选择后继续展开其后代；文件选择仅插入中性 `@relative/path` 文本，不直接读取文件，也不绕过后续 Tool 的 PathGuard。
- 自然语言消息先持久化，再启动 CodingAgent，避免模型已经执行但 Issue 没有进入 Session 历史。
- Turn 快照创建后，运行期间不能切换 Session、Provider 或 Permission Mode。
- UI 中途 resize、Diff Tab 切换或滚动不会改变 Turn 快照。
- CodingAgent Event 只负责展示和收集证据，不直接修改 Bubble Tea Model；所有变化通过 Update 循环应用。

### 15.4 Agent 循环

CodingAgent 负责准备 Coding 语义，并为当前 Turn 创建 tool.Registry。Go / Python 共用同一组基础 Tool，只由 LanguageProfile 影响提示词、检查计划和可选能力。调用 AgentInvoker 前，CodingAgent 将 system prompt 和 provider-neutral Messages 交给 Context Manager；Manager 按注册顺序调用全部 Strategy，当前默认只有透传的 NopStrategy。EinoInvoker 使用 Registry.Definitions 向 ChatModelAgent 暴露工具，并完成“分析 -> 调用工具 -> 观察结果 -> 继续分析”的循环。

CodingAgent 将 AgentInvoker 返回的 Invocation Event 转换为稳定的 CodePilot Event：

| Invocation 结果 | CodePilot 行为 |
| --- | --- |
| AssistantDelta | 更新对话流式文本，不作为最终成功结论 |
| ToolCall | 进入受控工具子流程，结果返回当前 AgentInvoker |
| FinalResponse | 结束 Agent 循环，进入外层成功状态判定 |
| Error | 记录错误和已有证据，进入外层收尾 |
| Context cancelled | 停止模型与工具，状态标记为 cancelled |

模型不能直接把 Turn 标记为 verified；verified 必须由 Session 根据真实检查证据生成。

MVP 每个 Turn 按固定顺序注册 list_files、search_code、read_file、git_status、git_diff、apply_patch、run_checks。P1 只有 CodeNavigator 可用时才追加 definition、references、symbols、diagnostics。Registry 中是否存在某个 Tool 只是能力暴露，不是安全授权；每次 Invoke 仍必须经过 Workspace 硬规则和 Authorizer。

## 16. 工具校验、审批与执行

### 16.1 工具子流程图

~~~mermaid
flowchart TD
    CALL["AgentInvoker 产生 ToolCall"]
    LOOKUP{"Registry.Lookup<br/>存在对应 Tool？"}
    SCHEMA{"arguments 符合 Tool Schema<br/>且可解析？"}
    BAD_CALL["返回 ToolError<br/>unknown tool / invalid arguments"]
    CLASSIFY["分类 Action<br/>read / patch / check"]
    HARD["执行硬性安全校验<br/>Worktree 边界、敏感路径、命令 allowlist、参数和限制"]
    SAFE{"硬性校验通过？"}
    HARD_DENY["返回 Denied ToolResult<br/>审批不能覆盖"]

    PROPOSED["patch：发布 ProposedDiffChanged"]
    SIDE_EFFECT{"存在副作用？"}
    EXECUTE["执行受控操作"]
    POLICY["Authorizer 根据 Permission Mode 和临时授权决策"]
    POLICY_RESULT{"决策结果"}
    POLICY_DENY["返回 Denied ToolResult"]

    REQUEST["发布 ApprovalRequested<br/>展示目标、Diff 或命令及无 Sandbox 警告"]
    CHECKPOINT["保存内存 Checkpoint<br/>Turn = awaiting-approval"]
    DECISION{"用户选择"}
    GRANT["记录当前 Session 的精确动作临时授权"]
    RESUME_ALLOW["恢复 Checkpoint<br/>Turn = running"]
    RESUME_DENY["恢复 Checkpoint<br/>Turn = running"]
    CANCEL["取消 Turn Context<br/>丢弃 pending checkpoint"]

    READ["读取 / 搜索 / Git 查询<br/>限制范围和输出大小"]
    PATCH["再次校验文件 hash<br/>原子应用 patch 并记录前后 hash"]
    CHECK["exec.CommandContext<br/>显式 Dir、timeout、env 和输出上限"]
    EXEC_RESULT{"产生有效 ToolResult？<br/>check 非零退出或超时仍属于有效结果"}
    EXEC_ERROR["发布 ToolFailed<br/>返回结构化错误"]
    DIFF["刷新 Proposed / Session / Workspace Diff<br/>发布 Diff Event"]
    SUCCESS["发布 ToolCompleted"]
    RETURN["ToolResult 返回 AgentInvoker"]

    CALL --> LOOKUP
    LOOKUP -- "否" --> BAD_CALL --> RETURN
    LOOKUP -- "是" --> SCHEMA
    SCHEMA -- "否" --> BAD_CALL --> RETURN
    SCHEMA -- "是" --> CLASSIFY --> HARD --> SAFE
    SAFE -- "否" --> HARD_DENY --> RETURN
    SAFE -- "是" --> SIDE_EFFECT

    SIDE_EFFECT -- "否：read" --> READ --> EXEC_RESULT
    SIDE_EFFECT -- "是：patch" --> PROPOSED --> POLICY
    SIDE_EFFECT -- "是：check" --> POLICY
    POLICY --> POLICY_RESULT
    POLICY_RESULT -- "deny" --> POLICY_DENY --> RETURN
    POLICY_RESULT -- "allow" --> EXECUTE
    POLICY_RESULT -- "prompt" --> REQUEST --> CHECKPOINT --> DECISION

    DECISION -- "Deny" --> RESUME_DENY --> POLICY_DENY
    DECISION -- "Allow once" --> RESUME_ALLOW --> EXECUTE
    DECISION -- "Allow exact action for session" --> GRANT --> RESUME_ALLOW
    DECISION -- "Ctrl+C" --> CANCEL

    EXECUTE -->|patch| PATCH --> EXEC_RESULT
    EXECUTE -->|check| CHECK --> EXEC_RESULT
    EXEC_RESULT -- "否" --> EXEC_ERROR --> RETURN
    EXEC_RESULT -- "是，read / check" --> SUCCESS --> RETURN
    EXEC_RESULT -- "是，patch" --> DIFF --> SUCCESS
~~~

### 16.2 硬性校验先于审批

所有工具先验证 schema 和硬性规则，再进入权限策略：

- read：限制为 Worktree 内允许读取的文件，并执行敏感文件和符号链接检查。
- patch：校验路径、.git / 敏感文件、符号链接、unified diff、文件 hash 和原子写能力。
- check：只接受 LanguageProfile 生成的结构化 CommandSpec，不接受 Shell 字符串。
- 所有工具限制输入、输出、执行时间和返回给模型的内容大小。

硬性校验失败直接返回 Denied ToolResult，不弹出审批框。这样用户不会看到一个实际上不应被允许的“确认执行”选项。

### 16.3 Permission Mode 决策

| Action | read-only | ask | auto-edit |
| --- | --- | --- | --- |
| 安全读取 | allow | allow | allow |
| 合法 Worktree patch | deny | prompt，或命中精确临时授权后 allow | allow |
| 合法检查命令 | deny | prompt，或命中精确临时授权后 allow | prompt，或命中精确临时授权后 allow |

项目检查即使在 auto-edit 下也需要审批，因为 MVP 没有 OS Sandbox。ApprovalRequest 必须展示 program、args、WorktreeRoot、timeout，并明确提示项目代码仍拥有当前用户权限。

“Allow exact action for session”只记录在当前进程内存中：

- patch 授权至少绑定规范化路径和 patch 内容摘要。
- command 授权至少绑定 program、args、WorktreeRoot 和限制参数。
- 切换 Session、修改权限模式或退出程序时清除。

### 16.4 ToolResult 与 Diff

- 工具拒绝或执行失败通常作为结构化 ToolResult 返回 AgentInvoker，由模型决定调整方案或结束 Turn，而不是立即终止整个 Session。
- patch 在请求审批前进入 Proposed Diff；拒绝时真实文件不变化。
- patch 成功后记录 Session patch record，并刷新 Session Diff 与 Workspace Diff。
- patch 应用前文件 hash 不匹配时视为 drift，停止写入并要求 Agent 重新读取。
- run_checks 的退出码、截断输出、耗时和超时状态成为验证证据。
- check 返回非零退出码或超时不等于 Tool 基础设施失败；它仍以 ToolCompleted 返回，并携带“检查未通过”或“超时”的证据。
- Ctrl+C 是例外：它直接取消 Turn，不把取消伪装成普通工具错误让模型继续运行。

## 17. Turn 收尾与持续交互

### 17.1 收尾流程图

~~~mermaid
flowchart TD
    TERMINATE["Agent 循环终止<br/>final / error / limit / cancelled"]
    STOP_TOOLS["确认模型流和子进程已停止<br/>释放 Turn 级资源"]
    REFRESH["重新读取 Git status 和 diff<br/>汇总 patch 与 check 证据"]
    REASON{"终止原因或验证证据"}

    CANCELLED["状态 = cancelled"]
    FAILED["状态 = failed"]
    COMPLETED["状态 = completed<br/>未应用 patch，最终文本说明实际结果"]
    VERIFIED["状态 = verified"]
    UNVERIFIED["状态 = unverified"]

    REPORT["生成最终响应<br/>修改摘要、验证结果、限制和后续建议"]
    RECORD["追加最终 Assistant Message、Turn Record、Patch Record 和状态"]
    SAVE["SessionStore 原子保存 / 追加"]
    SAVED{"持久化成功？"}
    SAVE_WARNING["发布 SessionSaveFailed<br/>保留真实代码，不自动回滚<br/>标记待重试"]
    EVENTS["发布最终 Diff Event 和 TurnCompleted / Failed / Cancelled"]
    IDLE["清除 Turn 快照和本次审批等待状态<br/>Session 回到 idle"]
    CONTINUE["TUI 保持打开<br/>用户可继续输入、切换模型或 Session"]

    TERMINATE --> STOP_TOOLS --> REFRESH --> REASON
    REASON -- "用户取消" --> CANCELLED --> REPORT
    REASON -- "Agent / 工具错误，或检查证明修改失败" --> FAILED --> REPORT
    REASON -- "正常返回且没有应用 patch" --> COMPLETED --> REPORT
    REASON -- "要求的检查通过" --> VERIFIED --> REPORT
    REASON -- "已有 patch，但检查被拒绝、跳过、超时或环境不可用" --> UNVERIFIED --> REPORT

    REPORT --> RECORD --> SAVE --> SAVED
    SAVED -- "否" --> SAVE_WARNING --> EVENTS
    SAVED -- "是" --> EVENTS
    EVENTS --> IDLE --> CONTINUE
~~~

### 17.2 状态判定

Session Service 根据事实而不是模型措辞确定 Issue Turn 状态：

| 状态 | 判定条件 |
| --- | --- |
| completed | Agent 正常返回且没有应用 patch；适用于解释、审查、诊断或无需修改的结果，实际结论以最终文本为准 |
| verified | 产生目标 patch，且与本次修改相关的要求检查通过 |
| unverified | 已产生 patch，但检查被拒绝、跳过、超时或因环境问题无法运行 |
| failed | Agent / 工具返回错误，或检查证据表明修改未解决问题 / 引入失败 |
| cancelled | 用户取消 Turn；即使此前已经应用部分 patch，也不得改写为 verified |

达到 step 或时间上限时：有可用 patch 但缺少验证证据可判为 unverified；没有可用 patch 时保留 completed，并由最终报告明确写出限制原因。运行错误仍判为 failed。

### 17.3 持久化失败与代码状态

SessionStore 保存失败不能回滚 Worktree，因为其中可能同时存在用户修改或其他 Session 的修改。此时：

1. 保留真实文件和 Workspace Diff。
2. UI 显示持久化警告，Session 标记为需要重试。
3. 后续保存和 /exit 再次尝试写入。
4. 不把“会话保存失败”描述成“代码修改失败”。

### 17.4 回到持续交互

Turn 完成后程序不退出。Session 回到 idle 后，用户可以：

- 继续补充 Issue 或要求调整当前 patch。
- 在 Proposed、Session、Workspace Diff 之间切换。
- 通过 /model 切换 Provider / Model，并重建 CodingAgent / AgentInvoker。
- 创建或切换 Session；跨 Workspace 切换时 Active WorktreeRoot 随目标 Session 改变。
- 使用 /exit 保存当前 Session 并退出。

Provider 或 Session 切换只允许在 idle 状态执行。切换不会自动回滚代码，也不会把 Slash Command 加入模型历史。

## 18. 跨流程不变量

以下规则必须在后续接口和数据结构设计中保持：

1. main 只管理进程入口；internal/app 只管理装配和生命周期；Session 才是运行期应用核心。
2. UI 不是 Session、权限和 Git 状态的事实来源。
3. 一个 Turn 使用不可变的 Session / Worktree / Provider / Permission 快照。
4. 所有副作用都必须经过 Workspace 硬性校验和 Authorizer，Agent 不能绕过 Tool。
5. Approval 不等于 Sandbox，UI 不能把获批命令描述为已隔离。
6. SessionStore、Eino Checkpoint 和真实 Worktree 分别负责产品状态、单次运行恢复和代码事实，三者互不替代。
7. Agent 最终文本、工具成功和 Turn verified 是三个不同概念。
8. 任何失败只结束当前 Turn；除启动失败、用户 /exit 或不可恢复的 TUI 错误外，不退出 Session。

## 19. 逻辑流程评审项

- [ ] 是否认可首次 Provider 配置在 TUI 内完成，取消后保留未配置 Session。
- [ ] 是否认可自然语言消息先持久化，再启动 Agent。
- [ ] 是否认可 Slash Command 完全绕过 Agent Prompt。
- [ ] 是否认可 Turn 启动后固定 Session、Worktree、Provider 和权限快照。
- [ ] 是否认可硬性安全校验先于 Permission Mode 和用户审批。
- [ ] 是否认可 auto-edit 只自动允许合法 patch，项目检查仍需审批。
- [ ] 是否认可审批等待使用内存 Checkpoint，切换 Session 或退出时不迁移 pending Turn。
- [ ] 是否认可 verified 只能由外层根据真实检查证据生成。
- [ ] 是否认可 SessionStore 失败时保留真实代码并提示重试，而不自动回滚。
- [ ] 是否存在需要在接口和存储设计前调整的流程分支。

---

# 第三部分：详细设计

## 20. 设计约定

详细设计遵循以下代码约定：

1. interface 表达跨模块 Port，定义在消费方 package；纯数据和只有一个实现的内部组件不创建 interface。
2. interface 名称描述能力，例如 SessionStore、ModelFactory、WorkspaceTools；不使用 IService、IManager 等前缀。
3. 方法名使用业务动词，例如 StartTurn、ApplyPatch、SwitchSession；不使用 Handle、Process、Do 等含义不明确的方法。
4. 构造函数使用 NewService、NewFileStore、NewEinoInvoker 等具体名称；只有 package 内唯一核心类型时才使用 New。
5. context.Context 始终是可能阻塞方法的第一个参数，不保存到长期对象字段。
6. 跨模块参数使用 Request、Result、Options、Filter、Summary 等明确后缀，避免 map[string]any。
7. ID 使用不同的命名类型，禁止在主体逻辑中用裸 string 混传 Session、Turn、Workspace 和 Provider ID。
8. 时间统一保存为 UTC RFC3339Nano；UI 展示时再转换为本地时区。
9. 所有写操作都要求幂等 ID 或明确的原子写边界。
10. 不创建 common、utils、base、container 或全局 service registry。

ID 使用 crypto/rand 生成 128 bit 随机值，再编码为无 padding 的小写 Base32，并添加业务前缀：

| 类型 | 示例 | 用途 |
| --- | --- | --- |
| WorkspaceID | ws_k3m7... | Git common directory 对应的仓库 |
| WorktreeID | wt_p8d2... | 一个实际 checkout 根目录 |
| SessionID | ses_t9c4... | 持久 Coding Session |
| TurnID | turn_h2n6... | 一次自然语言执行 |
| MessageID | msg_b4q1... | 中性用户或助手消息 |
| PatchID | patch_f7r5... | 一次成功应用的 patch |
| ProviderProfileID | prv_m8s3... | 一份用户可选择的 Provider 配置 |
| ApprovalRequestID | apr_c2v9... | 一次待审批动作 |

CLI 可以接受不少于 8 个字符的唯一 ID 前缀；存在歧义时必须要求用户输入更长前缀。

## 21. 程序配置与本地状态

### 21.1 路径划分

配置和运行状态分开保存。配置适合人工查看和备份；Session 可能包含代码摘要，不放进配置文件，也不使用缓存目录。

| 平台 | ConfigDir | StateDir |
| --- | --- | --- |
| Windows | %AppData%\CodePilot | %LocalAppData%\CodePilot\State |
| macOS | ~/Library/Application Support/CodePilot | ~/Library/Application Support/CodePilot/State |
| Linux | ${XDG_CONFIG_HOME:-~/.config}/codepilot | ${XDG_STATE_HOME:-~/.local/state}/codepilot |

路径由 internal/config 的 ResolvePaths 统一计算，并以 config.Paths 注入其他模块。测试通过 app.Options 显式覆盖目录，不修改进程级 HOME 或全局环境。

Unix-like 系统中新建目录使用 0700、含用户内容的文件使用 0600。Windows 使用当前用户目录的 ACL，不额外实现自定义加密文件。

### 21.2 config.yaml

ConfigDir/config.yaml 保存程序级、非敏感且跨 Session 生效的配置：

~~~yaml
version: 1

defaults:
  provider_profile_id: prv_m8s3example
  model_id: deepseek-v4-flash

agent:
  max_steps: 30
  max_turn_duration: 20m
  command_timeout: 5m
  tool_result_max_bytes: 65536
  command_output_max_bytes: 262144
~~~

约束：

- 新 Session 的 Permission Mode 固定从 ask 开始，不允许通过全局配置默认开启 auto-edit。
- duration 使用 Go time.ParseDuration 可识别的字符串。
- max_steps、时间和输出上限必须设置安全范围；0 或负数不能表达“无限制”。
- 使用 go.yaml.in/yaml/v3 Decoder.KnownFields(true)；未知字段报错，避免拼写错误静默失效。
- version 高于当前程序支持版本时拒绝启动并保留原文件。
- 文件不存在时使用内置默认值，首次产生持久修改时再写入，不为读取动作创建空文件。

### 21.3 providers.yaml

ConfigDir/providers.yaml 只保存用户配置过的 Provider Profile，不复制内置 Provider Catalog：

~~~yaml
version: 1

profiles:
  - id: prv_m8s3example
    kind: deepseek
    display_name: DeepSeek
    model_id: deepseek-v4-flash
    credential_ref: keyring:provider/prv_m8s3example
    validated_at: 2026-08-20T08:00:00Z

  - id: prv_q7k2example
    kind: openai-compatible
    display_name: Company Gateway
    base_url: https://llm.example.com/v1
    model_id: coder-model
    credential_ref: keyring:provider/prv_q7k2example
    validated_at: 2026-08-20T08:10:00Z

  - id: prv_a3p9example
    kind: ollama
    display_name: Local Ollama
    base_url: http://127.0.0.1:11434
    model_id: qwen-coder
    validated_at: 2026-08-20T08:20:00Z
~~~

Provider Profile 规则：

- kind 只能来自内置 Catalog：openai、deepseek、ollama、openai-compatible。
- OpenAI 和 DeepSeek 的默认 Base URL 来自代码中的 Catalog，不写入文件；自定义兼容 Provider 必须保存 Base URL。
- API Key 不得出现在 YAML。credential_ref 只描述 Keyring 中的账户名。
- Keyring 不可用时，Key 只进入 MemoryStore；不持久化 memory credential reference。下次启动会将该 Profile 标记为需要重新认证。
- validated_at 只表示最近一次验证成功时间，不保证服务当前可用。
- Session 保存 ProviderProfileID 和 ModelID，因此同一 Profile 后续更改默认模型不会静默改变已有 Session。

### 21.4 Credential 存储

OS Keyring 使用以下命名：

| 字段 | 值 |
| --- | --- |
| service | CodePilot |
| account | provider/{ProviderProfileID} |
| secret | Provider API Key 原文 |

密钥生命周期：

1. UI 使用隐藏输入框收集 Key。
2. Provider Adapter 使用 Key 完成验证。
3. 验证成功后写入 CredentialStore；UI 立即清空输入缓冲。
4. 日志、Event、Session、错误详情和诊断信息只能携带脱敏后的 credential reference。
5. 删除 Provider Profile 时同时请求删除对应 Keyring 项；删除失败要提示用户手动清理。

MVP 不使用“加密 JSON 文件”作为降级方案，因为没有独立主密钥时不能提供有意义的安全边界。

### 21.5 StateDir 目录

StateDir 使用 Workspace -> Worktree -> Session 的物理层级：

~~~text
State/
├── .lock
├── registry.json
└── workspaces/
    └── ws_k3m7.../
        ├── workspace.json
        └── worktrees/
            └── wt_p8d2.../
                ├── worktree.json
                └── sessions/
                    └── ses_t9c4.../
                        ├── session.json
                        ├── messages.jsonl
                        ├── turns.jsonl
                        └── patches.jsonl
~~~

该目录只保存索引、对话和 patch 记录，不复制仓库文件，也不创建 Git Worktree。真实代码始终位于 worktree.json 指向的用户目录。

### 21.6 Workspace 与 Worktree 元数据

registry.json 保存全局 schema 版本和最近使用入口：

~~~json
{
  "version": 1,
  "last_active_session_id": "ses_t9c4example",
  "workspace_ids": ["ws_k3m7example"]
}
~~~

workspace.json：

~~~json
{
  "version": 1,
  "id": "ws_k3m7example",
  "display_name": "codepilot",
  "git_common_dir": "H:\\workspace_github\\codepilot\\.git",
  "trusted": true,
  "created_at": "2026-08-20T08:00:00Z",
  "last_used_at": "2026-08-20T08:30:00Z"
}
~~~

worktree.json：

~~~json
{
  "version": 1,
  "id": "wt_p8d2example",
  "workspace_id": "ws_k3m7example",
  "root": "H:\\workspace_github\\codepilot",
  "git_dir": "H:\\workspace_github\\codepilot\\.git",
  "last_session_id": "ses_t9c4example",
  "created_at": "2026-08-20T08:00:00Z",
  "last_used_at": "2026-08-20T08:30:00Z"
}
~~~

Workspace 以 git rev-parse --path-format=absolute --git-common-dir 的规范化结果识别；Worktree 以 git rev-parse --show-toplevel 的规范化结果识别。路径移动后不猜测新位置，记录保持存在但标记 unavailable。

### 21.7 Session 文件

session.json 保存容易覆盖更新的 Session metadata：

~~~json
{
  "version": 1,
  "id": "ses_t9c4example",
  "workspace_id": "ws_k3m7example",
  "worktree_id": "wt_p8d2example",
  "title": "修复用户不存在时返回 500",
  "provider_profile_id": "prv_m8s3example",
  "model_id": "deepseek-v4-flash",
  "permission_mode": "ask",
  "base_commit": "0123456789abcdef",
  "last_turn_status": "verified",
  "archived": false,
  "created_at": "2026-08-20T08:30:00Z",
  "updated_at": "2026-08-20T08:45:00Z"
}
~~~

messages.jsonl 每行一个中性消息：

~~~json
{"id":"msg_b4q1example","session_id":"ses_t9c4example","turn_id":"turn_h2n6example","role":"user","content":"修复用户不存在时返回 500","created_at":"2026-08-20T08:31:00Z"}
{"id":"msg_d8w5example","session_id":"ses_t9c4example","turn_id":"turn_h2n6example","role":"assistant","content":"已补充 ErrUserNotFound 到 404 的映射。","created_at":"2026-08-20T08:44:00Z"}
~~~

turns.jsonl 每行保存一个已结束 Turn 的事实摘要，不保存厂商私有 tool-call 对象：

~~~json
{"id":"turn_h2n6example","session_id":"ses_t9c4example","user_message_id":"msg_b4q1example","status":"verified","termination_reason":"final","provider_profile_id":"prv_m8s3example","model_id":"deepseek-v4-flash","steps":8,"started_at":"2026-08-20T08:31:00Z","completed_at":"2026-08-20T08:44:00Z"}
~~~

patches.jsonl 每行保存一次实际应用成功的 patch：

~~~json
{"id":"patch_f7r5example","session_id":"ses_t9c4example","turn_id":"turn_h2n6example","applied_at":"2026-08-20T08:40:00Z","files":[{"path":"internal/user/handler.go","before_hash":"sha256:...","after_hash":"sha256:..."}],"patch":"*** unified diff text ***"}
~~~

Session 文件不保存：

- API Key 和其他 secret。
- Eino gob checkpoint。
- Provider 私有 tool-call / response 对象。
- 完整工具 stdout；Turn 只保留必要的检查摘要和截断信息。
- 仓库文件副本。

Session archive 只把 archived 改为 true，不移动或删除目录。真正的数据清理不进入 MVP。

### 21.8 写入、恢复和版本规则

- config.yaml、providers.yaml、registry.json、workspace.json、worktree.json 和 session.json 使用同目录临时文件 + Sync + rename 原子替换。
- JSONL 在 FileStore 的单 Session mutex 下，以一次 Write 写入完整 JSON 和换行，再调用 Sync。
- 每条 JSONL 记录拥有随机 ID；重试 Append 时，相同 ID 不重复写入。
- 启动时允许忽略并报告最后一条不完整 JSONL；中间记录损坏则将 Session 标记为 corrupted，不继续静默加载。
- PatchRecord 在 patch 成功后立即追加，不等待 Turn 结束，降低代码已修改但记录完全丢失的概率。
- Session metadata 写入失败不回滚真实代码；UI 显示待重试状态。
- 所有文件带 version。MVP 只实现 version 1 读写和 migration dispatcher；没有真实旧版本时不提前编写迁移逻辑。
- MVP 不支持两个 CodePilot 进程共享同一个 StateDir。buildFoundation 使用 StateDir/.lock 获取进程级独占锁；获取失败时第二个进程明确报错并退出，不进入不完整的只读模式。实现固定使用 github.com/gofrs/flock 的 TryLock / Close，只使用独占锁，不混用共享锁。[gofrs/flock](https://pkg.go.dev/github.com/gofrs/flock)

### 21.9 Eino Checkpoint

Eino Checkpoint 只存在于进程内存，不写入 StateDir。internal/agent 的 MemoryCheckPointStore 实现 Eino 官方 CheckPointStore：

~~~go
type CheckPointStore interface {
    Set(ctx context.Context, key string, value []byte) error
    Get(ctx context.Context, key string) (value []byte, existed bool, err error)
}
~~~

CheckPointID 使用 TurnID。EinoInvoker 在审批 interrupt 时保存状态，并通过 ResumeWithParams 恢复；Turn 完成、取消或 Session 切换时删除对应内存记录。Eino 使用 gob 序列化运行状态，因此进入 interrupt state 的 CodePilot 自定义类型必须集中注册并保持稳定。[Eino Runner 与 Checkpoint](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_extension/)

### 21.10 数据库设计

MVP 不使用数据库，因此没有数据库表结构。

原因：

- 数据规模小、单机使用，JSON / JSONL 足以支持恢复和人工排查。
- FileSessionStore 与 MemorySessionStore 已通过 Port 隔离存储方式。
- 提前引入 SQLite 会增加 schema migration、事务和发布复杂度，但当前没有查询收益。

如果后续 Session 数量、搜索或并发要求证明文件存储不足，只新增 SQLiteSessionStore / SQLiteWorkspaceRegistry Adapter，不修改 Session Service 和 UI 流程。

## 22. 项目目录结构

~~~text
codepilot/
├── cmd/
│   └── codepilot/
│       └── main.go                  # 参数、根 Context、信号、退出码
├── internal/
│   ├── app/
│   │   ├── app.go                  # App.Run / Close 与进程生命周期
│   │   ├── build.go                # 四阶段集中装配
│   │   └── options.go              # 启动参数和测试路径覆盖
│   ├── ui/
│   │   ├── model.go                # Bubble Tea 根 Model
│   │   ├── update.go               # Msg -> 状态更新
│   │   ├── view.go                 # 根 View 和响应式布局
│   │   ├── command.go              # Slash Command 解析与分发
│   │   ├── completion.go           # Slash Command 与 Workspace 路径补全
│   │   ├── event_bridge.go         # Session Event -> tea.Msg
│   │   ├── keymap.go               # 快捷键定义
│   │   ├── conversation.go         # 对话与工具事件面板
│   │   ├── diff.go                 # Proposed / Session / Workspace Diff 面板
│   │   ├── composer.go             # 多行输入框
│   │   ├── provider_picker.go      # Provider / Model 配置与切换
│   │   ├── session_picker.go       # Session 列表与切换
│   │   └── approval.go             # 审批 Overlay
│   ├── session/
│   │   ├── service.go              # 应用用例入口
│   │   ├── ports.go                # 消费方定义的跨模块 interface
│   │   ├── session.go              # Session aggregate 和状态
│   │   ├── turn.go                 # Turn、Result、运行快照
│   │   ├── message.go              # 中性消息模型
│   │   ├── workspace.go            # Workspace / Worktree 记录 DTO
│   │   ├── approval.go             # Action、Request、Decision
│   │   ├── event.go                # 稳定产品 Event
│   │   └── errors.go               # 业务错误码
│   ├── tool/
│   │   ├── tool.go                 # 业务无关的 Tool interface
│   │   ├── definition.go           # Name、Description、InputSchema
│   │   ├── result.go               # Result、Status 与 Interrupt
│   │   └── registry.go             # 单 Turn Tool 注册、查询和有序列举
│   ├── agent/
│   │   ├── factory.go              # 实现 session.CodingAgentFactory
│   │   ├── coding_agent.go         # Coding Turn 语义与 Invocation 编排
│   │   ├── invocation.go           # 业务无关的 Input / Event / Result
│   │   ├── invoker.go              # AgentInvoker Port
│   │   ├── eino_invoker.go         # Eino Runner、interrupt / resume Adapter
│   │   ├── ports.go                # WorkspaceTools、ModelFactory 等 Port
│   │   ├── prompt.go               # System Prompt 与 Language Hint 组合
│   │   ├── toolset.go              # 为当前 Turn 构造并填充 tool.Registry
│   │   ├── tool_list_files.go      # list_files Tool
│   │   ├── tool_search_code.go     # search_code Tool
│   │   ├── tool_read_file.go       # read_file Tool
│   │   ├── tool_git_status.go      # git_status Tool
│   │   ├── tool_git_diff.go        # git_diff Tool
│   │   ├── tool_apply_patch.go     # apply_patch Tool
│   │   ├── tool_run_checks.go      # run_checks Tool
│   │   ├── tool_adapter.go         # tool.Tool -> Eino Tool
│   │   ├── approval_tool.go        # Approval interrupt / resume 适配
│   │   ├── event_adapter.go        # InvocationEvent -> session.Event
│   │   └── checkpoint.go           # MemoryCheckPointStore
│   ├── contextmanager/
│   │   ├── contextmanager.go       # Strategy、Manager、Request / Result 与 NopStrategy
│   │   └── contextmanager_test.go  # 顺序组合与透传隔离测试
│   ├── provider/
│   │   ├── service.go              # 实现 ModelCatalog / ModelFactory
│   │   ├── catalog.go              # 内置 Provider 描述和默认值
│   │   ├── profile.go              # ProviderProfile 与 ValidationResult
│   │   ├── credential_store.go     # CredentialStore Port
│   │   ├── openai.go               # OpenAI Eino Adapter
│   │   ├── deepseek.go             # DeepSeek Eino Adapter
│   │   ├── ollama.go               # Ollama Eino Adapter
│   │   └── compatible.go           # Custom OpenAI-compatible Adapter
│   ├── workspace/
│   │   ├── service.go              # WorkspaceReader / WorkspaceTools 实现
│   │   ├── locator.go              # Git root、git dir、common dir 解析
│   │   ├── files.go                # 文件列举和分段读取
│   │   ├── search.go               # rg / Git grep 搜索与截断
│   │   ├── security.go             # 路径、符号链接、敏感文件硬规则
│   │   ├── git.go                  # 受控 Git 只读调用
│   │   ├── patch.go                # patch 校验与原子应用
│   │   ├── diff.go                 # 三类 Diff 计算
│   │   ├── command.go              # CommandSpec 和检查执行
│   │   └── executor.go             # LocalCommandExecutor
│   ├── language/
│   │   ├── registry.go             # Strategy 注册和解析
│   │   ├── strategy.go             # Language Strategy interface / Profile
│   │   ├── golang.go               # Go 检测、格式化和检查计划
│   │   └── python.go               # Python 检测和 pytest 计划
│   ├── approval/
│   │   ├── service.go              # Policy、pending request 和 decision
│   │   ├── policy.go               # 三种 Permission Mode 决策表
│   │   └── grants.go               # Session 内精确临时授权
│   ├── sessionstore/
│   │   ├── file_store.go           # FileSessionStore / WorkspaceRegistry
│   │   ├── memory_store.go         # 测试用内存实现
│   │   ├── process_lock.go          # StateDir 进程级独占锁
│   │   ├── layout.go               # StateDir 路径映射
│   │   ├── json_file.go            # JSON 原子替换
│   │   └── jsonl.go                # JSONL append、校验和恢复
│   ├── credential/
│   │   ├── keyring_store.go        # OS Keyring Adapter
│   │   ├── memory_store.go         # 当前进程回退与测试实现
│   │   └── fallback_store.go       # Keyring 失败时的显式内存回退
│   ├── config/
│   │   ├── paths.go                # ConfigDir / StateDir 解析
│   │   ├── config.go               # config.yaml 类型、默认值和校验
│   │   └── provider_file.go        # providers.yaml 读写
│   └── lsp/                        # P1：真正实现时再创建
│       ├── navigator.go            # 实现 agent.CodeNavigator
│       ├── client.go               # JSON-RPC 客户端
│       ├── process.go              # gopls / pyright 生命周期
│       └── document.go             # URI、位置和文本同步
├── testdata/
│   └── repos/
│       ├── go-bug/                 # 小型 Go Bugfix fixture
│       └── python-bug/             # 小型 Python Bugfix fixture
├── docs/
│   └── detailed-design.md
├── AGENT.md
├── README.md
├── prd.md
├── go.mod
└── go.sum
~~~

目录约束：

- MVP 不创建 pkg；代码只供当前二进制使用。
- LSP 未进入开发阶段前不创建空目录和占位 interface 实现。
- 测试文件与被测代码同 package 放置，只有跨模块验收 fixture 放 testdata。
- UI 组件先保留在一个 ui package；出现真正复用或 package 过大后再拆子 package。
- Provider 先按文件区分，不为每个 Provider 创建 package。
- internal/tool 只保存通用契约和 Registry；具体 Coding Tool 实现保留在 agent，避免提前增加 codingtool 子包。
- Registry 每 Turn 创建，不使用 package-level 全局实例，也不通过 init 自动注册。
- 不创建 repository 层；SessionStore 本身就是持久化 Port。
- go.sum 仅在加入第一个外部依赖后由 Go 工具生成，不创建空占位文件。

### 22.1 直接 Go 依赖

首次建立 go.mod 时锁定具体版本，不在构建脚本中使用 @latest：

| 依赖 | 基线版本 | 用途 |
| --- | --- | --- |
| github.com/cloudwego/eino | v0.9.13 | ADK ChatModelAgent、Runner、interrupt / resume、Checkpoint |
| cloudwego/eino-ext 对应 model modules | 与 Eino v0.9.13 兼容的固定版本 | OpenAI、DeepSeek、Ollama ChatModel Adapter |
| charm.land/bubbletea/v2 | v2.0.6 | 全屏 TUI 和局部刷新 |
| charm.land/bubbles/v2 | v2.1.0 | textarea、viewport 等基础组件 |
| charm.land/lipgloss/v2 | v2.0.5 | 响应式布局和样式 |
| go.yaml.in/yaml/v3 | v3.0.5 | config.yaml / providers.yaml strict decode |
| github.com/zalando/go-keyring | v0.2.8 | macOS Keychain、Linux Secret Service、Windows Credential Manager |
| github.com/gofrs/flock | v0.13.0 | StateDir 进程级独占锁 |

Eino v0.10 仍为 alpha 时不进入 MVP。Provider model module 在 eino-ext 中分别版本化，必须在一个兼容性测试分支中锁定后再写入 go.mod，不能让不同 Provider 在安装时各自升级 Eino 主模块。[Eino releases](https://github.com/cloudwego/eino/releases) [Bubble Tea v2](https://github.com/charmbracelet/bubbletea/releases) [YAML v3](https://pkg.go.dev/go.yaml.in/yaml/v3) [go-keyring](https://github.com/zalando/go-keyring)

运行期外部程序：Git 是必需依赖；rg 是可选加速器，缺失时退化为 git grep / Go 文件遍历；go、python、gopls、pyright 仅在对应语言检查或 P1 LSP 使用时检测。CodePilot 不自动安装它们。

## 23. 核心领域类型

以下类型展示业务字段和归属，JSON tag 只在持久 DTO 中添加；运行期领域类型不因为文件格式暴露序列化细节。

### 23.1 Session 与 Turn

~~~go
package session

type SessionID string
type TurnID string
type MessageID string
type PatchID string
type WorkspaceID string
type WorktreeID string
type ProviderProfileID string
type ApprovalRequestID string

type PermissionMode string

const (
    PermissionReadOnly PermissionMode = "read-only"
    PermissionAsk     PermissionMode = "ask"
    PermissionAutoEdit PermissionMode = "auto-edit"
)

type Session struct {
    ID                SessionID
    WorkspaceID       WorkspaceID
    WorktreeID        WorktreeID
    Title             string
    ProviderProfileID ProviderProfileID
    ModelID           string
    PermissionMode    PermissionMode
    BaseCommit        string
    LastTurnStatus    TurnStatus
    Archived          bool
    CreatedAt         time.Time
    UpdatedAt         time.Time
}

type RuntimeState string

const (
    RuntimeIdle             RuntimeState = "idle"
    RuntimeRunning          RuntimeState = "running"
    RuntimeAwaitingApproval RuntimeState = "awaiting-approval"
    RuntimeCancelling       RuntimeState = "cancelling"
)

type TurnStatus string

const (
    TurnCompleted  TurnStatus = "completed"
    TurnVerified   TurnStatus = "verified"
    TurnUnverified TurnStatus = "unverified"
    TurnFailed     TurnStatus = "failed"
    TurnCancelled  TurnStatus = "cancelled"
)

type TurnScope struct {
    TurnID             TurnID
    SessionID          SessionID
    WorkspaceID        WorkspaceID
    WorktreeID         WorktreeID
    WorktreeRoot       string
    ProviderProfileID  ProviderProfileID
    ModelID            string
    PermissionMode     PermissionMode
    Limits             RunLimits
}
~~~

TurnScope 在 StartTurn 时创建后只读。权限审批可以增加针对精确 Action 的临时授权，但不能修改 Scope 中的 PermissionMode。

### 23.2 Message、TurnRecord 与 PatchRecord

~~~go
type MessageRole string

const (
    RoleUser      MessageRole = "user"
    RoleAssistant MessageRole = "assistant"
)

type Message struct {
    ID        MessageID
    SessionID SessionID
    TurnID    TurnID
    Role      MessageRole
    Content   string
    CreatedAt time.Time
}

type TurnRecord struct {
    ID                TurnID
    SessionID         SessionID
    UserMessageID     MessageID
    Status            TurnStatus
    TerminationReason string
    ProviderProfileID ProviderProfileID
    ModelID           string
    Steps             int
    CheckSummary      CheckSummary
    StartedAt         time.Time
    CompletedAt       time.Time
}

type PatchRecord struct {
    ID        PatchID
    SessionID SessionID
    TurnID    TurnID
    Patch     string
    Files     []PatchedFile
    AppliedAt time.Time
}

type PatchedFile struct {
    Path       string
    BeforeHash string
    AfterHash  string
}

type CheckOutcome string

const (
    CheckNotRun     CheckOutcome = "not-run"
    CheckPassed     CheckOutcome = "passed"
    CheckFailed     CheckOutcome = "failed"
    CheckInconclusive CheckOutcome = "inconclusive"
    CheckDenied     CheckOutcome = "denied"
    CheckTimedOut   CheckOutcome = "timed-out"
    CheckUnavailable CheckOutcome = "unavailable"
    CheckCancelled  CheckOutcome = "cancelled"
)

type CheckSummary struct {
    Outcome   CheckOutcome
    Summary   string
    Truncated bool
}
~~~

只持久化中性 user / assistant 消息。工具参数、工具结果和模型厂商私有消息由当前 Turn 内的 Agent 使用；需要长期展示的信息压缩进 TurnRecord.CheckSummary 和最终 Assistant Message。CheckSummary.Outcome 用于程序判定 TurnStatus，Summary 只保存有界、脱敏且面向用户的检查结论，程序不得解析 Summary 文本决定状态。

### 23.3 Workspace 与 Diff

~~~go
type WorkspaceRecord struct {
    ID           WorkspaceID
    DisplayName  string
    GitCommonDir string
    Trusted      bool
    CreatedAt    time.Time
    LastUsedAt   time.Time
}

type WorktreeRecord struct {
    ID            WorktreeID
    WorkspaceID   WorkspaceID
    Root          string
    GitDir        string
    LastSessionID SessionID
    CreatedAt     time.Time
    LastUsedAt    time.Time
}

type DiffKind string

const (
    DiffProposed  DiffKind = "proposed"
    DiffSession   DiffKind = "session"
    DiffWorkspace DiffKind = "workspace"
)

type DiffResult struct {
    Kind      DiffKind
    Text      string
    Files     []DiffFile
    Truncated bool
    Drifted   bool
}
~~~

### 23.4 Action 与审批

~~~go
type ActionKind string

const (
    ActionRead       ActionKind = "read"
    ActionApplyPatch ActionKind = "apply-patch"
    ActionRunCheck   ActionKind = "run-check"
)

type Action struct {
    ID           string
    SessionID    SessionID
    TurnID       TurnID
    Kind         ActionKind
    WorktreeRoot string
    Summary      string
    Fingerprint  string
    Patch        *PatchAction
    Command      *CommandAction
}

type AuthorizationOutcome string

const (
    AuthorizationAllow  AuthorizationOutcome = "allow"
    AuthorizationPrompt AuthorizationOutcome = "prompt"
    AuthorizationDeny   AuthorizationOutcome = "deny"
)

type Authorization struct {
    Outcome AuthorizationOutcome
    Request *ApprovalRequest
    Reason  string
}

type ApprovalRequest struct {
    ID        ApprovalRequestID
    SessionID SessionID
    TurnID    TurnID
    Action    Action
    CreatedAt time.Time
}

type ApprovalDecisionKind string

const (
    ApprovalAllowOnce   ApprovalDecisionKind = "allow-once"
    ApprovalAllowSession ApprovalDecisionKind = "allow-session"
    ApprovalDeny        ApprovalDecisionKind = "deny"
)

type ApprovalDecision struct {
    Kind      ApprovalDecisionKind
    DecidedAt time.Time
}

type ApprovalResolution struct {
    RequestID ApprovalRequestID
    SessionID SessionID
    TurnID    TurnID
    Decision  ApprovalDecision
}
~~~

Action.Fingerprint 由已规范化且经过硬性校验的动作内容计算。Session 临时授权只匹配 Fingerprint，不允许“同类命令”模糊放行。

### 23.5 核心 Request / Result

~~~go
type RunLimits struct {
    MaxSteps             int
    MaxTurnDuration      time.Duration
    CommandTimeout       time.Duration
    ToolResultMaxBytes   int
    CommandOutputMaxBytes int
}

type TurnRequest struct {
    Scope       TurnScope
    History     []Message
    UserMessage Message
}

type TurnResult struct {
    FinalText         string
    Steps             int
    TerminationReason string
    CheckSummary      CheckSummary
    AppliedPatches    []PatchRecord
}

type CodingAgentConfig struct {
    SessionID          SessionID
    WorkspaceID        WorkspaceID
    WorktreeID         WorktreeID
    WorktreeRoot       string
    ProviderProfileID  ProviderProfileID
    ModelID            string
    Limits             RunLimits
}

type ModelSelection struct {
    ProviderProfileID ProviderProfileID
    ModelID           string
}

type ConfigureProviderRequest struct {
    Kind            string
    DisplayName     string
    BaseURL         string
    ModelID         string
    CredentialInput []byte
}

type SessionSnapshot struct {
    Session       Session
    RuntimeState  RuntimeState
    Messages      []Message
    Turns         []TurnRecord
    Patches       []PatchRecord
    WorktreeState WorktreeState
}
~~~

CredentialInput 是短生命周期敏感输入：Provider Service 读取后不得复制进 Profile、Event 或 AppError。TurnResult 不包含 TurnStatus，因为最终状态由 Session Service 根据 TerminationReason、CheckSummary 和真实 Diff 计算。

## 24. 模块接口与方法

本节中的 Port 是计划落地为 Go interface 的跨模块边界。具体 struct 的普通方法也会列出，但明确标注“不创建 interface”的模块不会为了测试而机械抽象。

### 24.1 cmd/codepilot

cmd/codepilot 不定义 interface，只保留 main：

~~~go
func main()
~~~

main 的固定步骤：

1. 读取当前工作目录和最少量启动参数。
2. 使用 signal.NotifyContext 建立根 context。
3. 调用 app.New 创建 App。
4. 调用 App.Run。
5. 根据错误类别输出一条用户可读消息并设置退出码。

它不能导入 ui、session、agent 等具体业务 package。

### 24.2 internal/app

app 是具体应用宿主，不定义 App interface：

~~~go
package app

type Options struct {
    WorkingDirectory string

    // 仅供测试和嵌入场景覆盖；空值使用平台默认路径。
    ConfigDir string
    StateDir  string

    TrustWorkspace bool
    Input  io.Reader
    Output io.Writer
}

type App struct {
    // 字段保持私有，只保存顶层运行对象和需要关闭的资源。
}

func New(ctx context.Context, options Options) (*App, error)
func (a *App) Run(ctx context.Context) error
func (a *App) Close() error
~~~

| 方法 | 作用 |
| --- | --- |
| New | 校验 Options，依次执行四个 build 阶段；任一失败立即逆序清理 |
| Run | 启动 Bubble Tea Program，并将根 context 的取消传递给 UI 和 Session |
| Close | 幂等关闭所有已构造资源；多次调用返回第一次关闭错误的组合结果 |

build.go 中的类型和方法不导出：

~~~go
type foundation struct { /* config, stores, credential, event bridge */ }
type capabilities struct { /* provider, approval, workspace, language */ }
type runtime struct { /* agent factory, session service */ }

func buildFoundation(ctx context.Context, options Options) (foundation, error)
func buildCapabilities(ctx context.Context, base foundation) (capabilities, error)
func buildRuntime(ctx context.Context, base foundation, caps capabilities) (runtime, error)
func buildPresentation(base foundation, run runtime) (*ui.Model, error)
~~~

这些私有分组只减少 wiring.go/build.go 的参数噪音，不能传入业务模块或演变成运行期 Service Container。

### 24.3 internal/ui

UI 使用在消费方定义的 SessionClient，测试时可以用 fake 实现：

~~~go
package ui

type TurnController interface {
    StartTurn(ctx context.Context, text string) (session.TurnID, error)
    CancelTurn(ctx context.Context) error
    ResolveApproval(ctx context.Context, request session.ApprovalResolution) error
}

type SessionController interface {
    CurrentSession(ctx context.Context) (session.SessionSnapshot, error)
    CreateSession(ctx context.Context, request session.CreateSessionRequest) (session.SessionSummary, error)
    ListSessions(ctx context.Context, filter session.SessionFilter) ([]session.SessionSummary, error)
    SwitchSession(ctx context.Context, id session.SessionID) error
    RenameSession(ctx context.Context, id session.SessionID, title string) error
    ArchiveSession(ctx context.Context, id session.SessionID) error
}

type ModelController interface {
    ListProviderProfiles(ctx context.Context) ([]session.ProviderProfile, error)
    ConfigureProvider(ctx context.Context, request session.ConfigureProviderRequest) (session.ProviderProfile, error)
    ListModels(ctx context.Context, profileID session.ProviderProfileID) ([]session.ModelOption, error)
    SwitchModel(ctx context.Context, selection session.ModelSelection) error
}

type WorkspaceController interface {
    OpenWorkspace(ctx context.Context, path string) (session.WorktreeSummary, error)
    ListWorkspaces(ctx context.Context) ([]session.WorktreeSummary, error)
    ListWorkspaceFiles(ctx context.Context, limit int) (session.WorkspaceFileList, error)
    ReadDiff(ctx context.Context, kind session.DiffKind) (session.DiffResult, error)
    SetPermissionMode(ctx context.Context, mode session.PermissionMode) error
}

type SessionClient interface {
    TurnController
    SessionController
    ModelController
    WorkspaceController
}
~~~

| 方法组 | 说明 |
| --- | --- |
| TurnController | 启动 / 取消 Turn，提交 UI 审批结果 |
| SessionController | Session Picker、创建、切换、重命名和归档 |
| ModelController | /model 的 Profile 配置、模型列表和 Turn 间切换 |
| WorkspaceController | /workspace、Diff Tab、Permission Picker 和 `@` 文件/目录候选列表 |

session.Service 实现 SessionClient 的全部方法。UI 不通过一个通用 ExecuteCommand(ctx, any) 方法调用应用层，因为那会丢失类型和权限边界。

EventBridge 是具体 Adapter，实现 session.EventSink：

~~~go
type EventBridge struct {
    // bounded channel，字段私有
}

func NewEventBridge(capacity int) *EventBridge
func (b *EventBridge) Publish(ctx context.Context, event session.Event) error
func (b *EventBridge) WaitForEvent() tea.Cmd
func (b *EventBridge) Close() error
~~~

| 方法 | 作用 |
| --- | --- |
| Publish | 将后台 Event 写入有界队列；关键事件等待队列空间并尊重 context 取消 |
| WaitForEvent | 返回读取下一条 Event 的 tea.Cmd；Update 收到后立即安排下一次等待 |
| Close | 幂等关闭桥接器，使等待中的 Cmd 正常退出 |

AssistantDelta 在 agent/event_adapter.go 中按短时间窗口合并，避免 token 粒度事件淹没 UI；ApprovalRequested、DiffChanged 和 TurnCompleted 不允许丢弃。

### 24.4 internal/session

#### 24.4.1 应用服务方法

session.Service 是具体 struct，对 UI 暴露强类型用例方法：

~~~go
package session

type Dependencies struct {
    CodingAgents      CodingAgentFactory
    SessionStore      SessionStore
    WorkspaceRegistry WorkspaceRegistry
    WorkspaceReader   WorkspaceReader
    ModelCatalog      ModelCatalog
    Authorizer        Authorizer
    Events            EventSink
    Limits            RunLimits
}

func NewService(deps Dependencies) (*Service, error)

func (s *Service) Activate(ctx context.Context, workingDirectory string) (SessionSnapshot, error)
func (s *Service) CurrentSession(ctx context.Context) (SessionSnapshot, error)

func (s *Service) StartTurn(ctx context.Context, text string) (TurnID, error)
func (s *Service) CancelTurn(ctx context.Context) error
func (s *Service) ResolveApproval(ctx context.Context, resolution ApprovalResolution) error

func (s *Service) CreateSession(ctx context.Context, request CreateSessionRequest) (SessionSummary, error)
func (s *Service) ListSessions(ctx context.Context, filter SessionFilter) ([]SessionSummary, error)
func (s *Service) SwitchSession(ctx context.Context, id SessionID) error
func (s *Service) RenameSession(ctx context.Context, id SessionID, title string) error
func (s *Service) ArchiveSession(ctx context.Context, id SessionID) error

func (s *Service) OpenWorkspace(ctx context.Context, path string) (WorktreeSummary, error)
func (s *Service) ReadDiff(ctx context.Context, kind DiffKind) (DiffResult, error)

func (s *Service) ListProviderProfiles(ctx context.Context) ([]ProviderProfile, error)
func (s *Service) ConfigureProvider(ctx context.Context, request ConfigureProviderRequest) (ProviderProfile, error)
func (s *Service) ListModels(ctx context.Context, profileID ProviderProfileID) ([]ModelOption, error)
func (s *Service) SwitchModel(ctx context.Context, selection ModelSelection) error
func (s *Service) SetPermissionMode(ctx context.Context, mode PermissionMode) error

func (s *Service) Close() error
~~~

关键方法语义：

| 方法 | 前置条件 | 成功后的状态 |
| --- | --- | --- |
| Activate | 路径指向 Git Worktree；首次访问已获信任 | 恢复或创建 Active Session，RuntimeState=idle |
| StartTurn | idle、Provider 可用、消息非空、Worktree 可访问 | 用户消息已持久化，异步 Turn 开始 |
| CancelTurn | running 或 awaiting-approval | 状态先变为 cancelling，等待 CodingAgent / Tool 真正结束 |
| ResolveApproval | request 属于当前 Session 和 Turn | Decision 交给 Authorizer，不能直接执行动作 |
| SwitchSession | idle，目标存在且 Worktree 可访问 | 保存旧 Session，清理临时授权，重建 CodingAgent |
| OpenWorkspace | idle，目标为受信任 Git Worktree | 选择 / 创建目标 Session 并切换 Active Worktree |
| SwitchModel | idle，目标 Profile 已验证 | 保留中性消息，保存选择并重建 CodingAgent / AgentInvoker |
| SetPermissionMode | idle | 更新 Session；切换模式时清空现有精确授权 |
| Close | 无新的 Turn 可启动 | 取消活动 Turn、保存 Session、关闭 CodingAgent |

StartTurn 启动后台 goroutine 后立即返回 TurnID；结果和进度只通过 EventSink 发布。Service 不在持有主 mutex 时调用 CodingAgent、Store、Provider 或 Workspace 等外部 Port。

新 Session 初始 title 为空。第一次 StartTurn 在保存 user message 的同一业务步骤中，用首行最多 80 个 Unicode rune 生成 title；不额外调用模型。用户重命名后不再自动覆盖。

#### 24.4.2 CodingAgent Port

~~~go
type CodingAgent interface {
    RunTurn(ctx context.Context, request TurnRequest, events EventSink) (TurnResult, error)
    Close() error
}

type CodingAgentFactory interface {
    CreateCodingAgent(ctx context.Context, config CodingAgentConfig) (CodingAgent, error)
}
~~~

| 方法 | 作用 |
| --- | --- |
| RunTurn | 完成一次可 interrupt / resume 的 Eino 运行；阻塞当前 Turn goroutine，但不阻塞 UI |
| Close | 释放 CodingAgent 持有的 AgentInvoker 和当前模型相关资源；幂等 |
| CreateCodingAgent | 根据不可变 Worktree、Provider / Model 和运行限制创建 Session 当前 CodingAgent |

CodingAgentConfig 不包含 API Key；EinoInvoker 通过 ModelFactory 按 ProviderProfileID 获取 ChatModel。

#### 24.4.3 SessionStore Port

~~~go
type SessionStore interface {
    CreateSession(ctx context.Context, value Session) error
    LoadSession(ctx context.Context, id SessionID) (SessionSnapshot, error)
    ListSessions(ctx context.Context, filter SessionFilter) ([]SessionSummary, error)
    SaveSession(ctx context.Context, value Session) error
    AppendMessage(ctx context.Context, value Message) error
    AppendPatch(ctx context.Context, value PatchRecord) error
    CommitTurn(ctx context.Context, commit TurnCommit) error
    ArchiveSession(ctx context.Context, id SessionID, archivedAt time.Time) error
}
~~~

| 方法 | 作用 |
| --- | --- |
| CreateSession | 创建 session.json 和三个 JSONL 文件；SessionID 已存在时返回冲突 |
| LoadSession | 加载 metadata、消息、Turn 和 patch，返回可恢复警告 |
| ListSessions | 按 Workspace、Worktree、archived 条件返回轻量 Summary，不加载完整消息 |
| SaveSession | 原子更新 title、Provider、权限和 last status 等 metadata |
| AppendMessage | 幂等追加一条中性消息；StartTurn 前先写 user message |
| AppendPatch | patch 应用成功事件到达 Session 后立即幂等记录 |
| CommitTurn | 追加最终 assistant message 和 TurnRecord，再更新 Session metadata |
| ArchiveSession | 设置 archived；不删除目录 |

TurnCommit 是业务事务参数，至少包含 Session metadata、最终 Message 和 TurnRecord。FileStore 无法提供跨文件系统事务时，按“append records -> atomic metadata”顺序执行，并在 LoadSession 时报告可恢复的不一致。

#### 24.4.4 WorkspaceRegistry 与 WorkspaceReader Port

~~~go
type WorkspaceRegistry interface {
    SaveWorkspace(ctx context.Context, value WorkspaceRecord) error
    SaveWorktree(ctx context.Context, value WorktreeRecord) error
    LoadWorkspace(ctx context.Context, id WorkspaceID) (WorkspaceRecord, error)
    LoadWorktree(ctx context.Context, id WorktreeID) (WorktreeRecord, error)
    FindWorktreeByRoot(ctx context.Context, normalizedRoot string) (WorktreeRecord, bool, error)
    ListWorktrees(ctx context.Context) ([]WorktreeSummary, error)
    SaveLastActiveSession(ctx context.Context, id SessionID) error
}

type WorkspaceReader interface {
    ResolveWorktree(ctx context.Context, path string) (ResolvedWorktree, error)
    ReadWorktreeState(ctx context.Context, root string) (WorktreeState, error)
    ReadDiff(ctx context.Context, request DiffRequest) (DiffResult, error)
}
~~~

WorkspaceRegistry 管理 CodePilot 自己的登记信息；WorkspaceReader 查询真实 Git / 文件系统。两者分开后，MemoryStore 可以测试 Session 切换，workspace.Service 可以用临时 Git 仓库测试路径和 Diff。

#### 24.4.5 Provider、审批和 Event Port

~~~go
type ModelCatalog interface {
    ListProviderProfiles(ctx context.Context) ([]ProviderProfile, error)
    ConfigureProvider(ctx context.Context, request ConfigureProviderRequest) (ProviderProfile, error)
    ListModels(ctx context.Context, profileID ProviderProfileID) ([]ModelOption, error)
    ValidateSelection(ctx context.Context, selection ModelSelection) (ModelValidation, error)
}

type Authorizer interface {
    Authorize(ctx context.Context, mode PermissionMode, action Action) (Authorization, error)
    WaitDecision(ctx context.Context, requestID ApprovalRequestID) (ApprovalDecision, error)
    Resolve(ctx context.Context, resolution ApprovalResolution) error
    ClearSession(ctx context.Context, sessionID SessionID) error
}

type EventSink interface {
    Publish(ctx context.Context, event Event) error
}
~~~

| 方法 | 作用 |
| --- | --- |
| ConfigureProvider | 验证、保存 Profile 和 Credential；失败时不改变当前选择 |
| ValidateSelection | 在切换前确认 Profile、Credential、Model 和 tool-calling 能力可用 |
| Authorize | 返回 allow / deny / prompt；prompt 同时创建唯一 ApprovalRequest |
| WaitDecision | Invocation 已 interrupt 后等待 UI Decision，context 取消时删除 pending request |
| Resolve | 校验 request 属于当前 Session / Turn，再唤醒等待者 |
| ClearSession | Session 切换、权限切换或退出时清除 pending 和临时授权 |
| Publish | 发布不含 secret 和 Eino 私有对象的稳定产品 Event |

### 24.5 internal/tool

tool package 定义业务无关的工具执行协议。它既不理解 Coding Session，也不依赖 Eino；AgentInvoker 和具体 Coding Tool 通过这一小组稳定类型协作。

#### 24.5.1 Tool interface 与数据类型

~~~go
package tool

type Tool interface {
    Definition() Definition
    Invoke(ctx context.Context, arguments json.RawMessage) (Result, error)
}

type Definition struct {
    Name        string
    Description string
    InputSchema json.RawMessage
}

type ResultStatus string

const (
    ResultCompleted   ResultStatus = "completed"
    ResultDenied      ResultStatus = "denied"
    ResultInvalid     ResultStatus = "invalid"
    ResultFailed      ResultStatus = "failed"
    ResultCancelled   ResultStatus = "cancelled"
    ResultInterrupted ResultStatus = "interrupted"
)

type Result struct {
    Status    ResultStatus
    Content   string
    Data      json.RawMessage
    Interrupt *Interrupt
}

type Interrupt struct {
    ID      string
    Kind    string
    Payload json.RawMessage
}
~~~

| 方法 | 作用 |
| --- | --- |
| Definition | 返回稳定名称、面向模型的说明和 JSON Schema；不得包含 secret 或可信运行参数 |
| Invoke | 解析模型提供的 arguments，执行已经绑定到 Tool 实例的能力，并返回结构化 Result |

Tool 的接口是业务无关的，但具体 Tool 可以是 Coding 业务实现。list_files、read_file 等实例由 CodingAgent 为当前 Turn 构造，并通过私有字段捕获可信 TurnScope、WorkspaceTools 和 LanguageProfile。模型只能控制 arguments，不能覆盖这些字段。

invalid、denied、failed 和 interrupted 等可预期结果放入 Result；error 只用于 context 取消、Registry / Adapter 失效或无法归一化的基础设施错误。Content 是返回模型的有界文本，Data 是可选的结构化结果，两者在离开 Tool 前完成截断和脱敏。

#### 24.5.2 Registry

~~~go
type Registry struct {
    // 按名称索引的 entries + 保持注册顺序的 names；字段私有
}

func NewRegistry() *Registry
func (r *Registry) Register(value Tool) error
func (r *Registry) Lookup(name string) (Tool, bool)
func (r *Registry) List() []Tool
func (r *Registry) Definitions() []Definition
~~~

Registry 规则：

1. Register 拒绝 nil Tool、空名称、重复名称、空说明，以及不是合法 JSON object 的 InputSchema；MVP 不引入完整 JSON Schema 校验器。
2. 名称使用小写 snake_case，并在一个 Registry 内唯一。
3. List 和 Definitions 按注册顺序返回新 slice，调用方不能修改内部状态。
4. CodingAgent 在每个 Turn 创建并完成注册；调用 AgentInvoker.Invoke 后不再写入。
5. EinoInvoker 使用 Definitions 投喂模型，收到 ToolCall 后只通过 Lookup 找到执行对象。
6. Registry 不是权限边界；Tool.Invoke 仍调用 Workspace 硬性校验和 Authorizer。
7. MVP 只有这一个具体 Registry，因此不额外定义 ToolRegistry interface。

不使用 package-level 默认 Registry、init 自动注册、反射扫描、动态插件发现、优先级覆盖或 Tool 中间件链。新增 Tool 的过程固定为“实现 Tool -> 在 CodingAgent.toolset 中显式注册 -> 增加测试”。

### 24.6 internal/agent

agent package 同时包含面向产品的 CodingAgent 和业务无关的 Invocation Runtime。Eino 类型只能出现在 EinoInvoker、ModelFactory 和相应 adapter 内，不能泄露给 session、ui、workspace、sessionstore，也不能进入 AgentInvoker 的方法签名。

#### 24.6.1 CodingAgent 与 AgentInvoker 两层边界

~~~go
package agent

type AgentInvoker interface {
    Invoke(
        ctx context.Context,
        input InvocationInput,
        events InvocationEventSink,
    ) (InvocationResult, error)

    Resume(
        ctx context.Context,
        input ResumeInput,
        events InvocationEventSink,
    ) (InvocationResult, error)

    Close() error
}

type InvocationEventSink interface {
    PublishInvocationEvent(ctx context.Context, event InvocationEvent) error
}

type InvocationInput struct {
    ID             string
    CheckpointID   string
    Model          ModelRef
    SystemPrompt   string
    Messages       []InvocationMessage
    Tools          *tool.Registry
    Limits         InvocationLimits
}

type ResumeInput struct {
    CheckpointID string
    InterruptID  string
    Response     InterruptResponse
}

type ModelRef struct {
    Provider string
    Model    string
}

type InvocationMessage struct {
    Role    InvocationRole
    Content string
}

type InvocationLimits struct {
    MaxSteps    int
    MaxDuration time.Duration
}

type InvocationEvent struct {
    Kind      InvocationEventKind
    Text      string
    Tool      *InvocationToolEvent
    Interrupt *InvocationInterrupt
}

type InvocationResult struct {
    Status            InvocationStatus
    FinalText         string
    Steps             int
    TerminationReason string
    Interrupt         *InvocationInterrupt
}

type InvocationInterrupt struct {
    ID      string
    Kind    string
    Payload json.RawMessage
}

type InterruptResponse struct {
    Kind InterruptResponseKind // approved / rejected / cancelled
}
~~~

`InvocationMessage`、`ModelRef`、`InvocationLimits`、`InvocationEvent`、`InvocationResult` 和 `InterruptResponse` 都是 agent/invocation.go 中的中性类型。其结构不直接声明 SessionID、WorktreeRoot、PatchRecord、PermissionMode、Bugfix 或 Eino 对象；Coding 依赖由 Registry 中已经绑定的 tool.Tool 实例封装。中断 payload 使用 `json.RawMessage` 作为明确的序列化边界，AgentInvoker 只能透传而不能据此执行产品分支，也不使用 `map[string]any`。

| 方法 | 作用 |
| --- | --- |
| Invoke | 执行一次新的模型 / 工具循环，并通过 InvocationEventSink 流式发布中性事件 |
| Resume | 使用 CheckpointID 和已经校验的 InterruptResponse 恢复一次暂停的 Invocation |
| Close | 释放该 Invoker 持有的模型流、Runner 和 Checkpoint 资源；幂等 |

一个 CodingAgent 独占一个 AgentInvoker，同一时刻最多存在一个 active Invocation。Resume 必须发生在原 Invoker 实例上；该实例在 interrupt 期间保留模型、Registry、Tool 对象和事件序号，Eino Checkpoint 只保存可序列化运行状态，不序列化 Tool 实例。进程重启、Session 切换或 Close 后不支持恢复该 Invocation。

两层职责固定如下：

- session.CodingAgent：Session Service 的消费方 Port，输入输出使用 TurnRequest / TurnResult。
- agent.CodingAgent：实现该 Port，理解 Coding Session、LanguageProfile、WorkspaceTools、审批以及 CodePilot Event。
- agent.AgentInvoker：不理解具体 Coding 业务，只运行消息、模型和工具，处理 stream、limit、interrupt 和 resume。
- agent.EinoInvoker：AgentInvoker 的 MVP Adapter，负责所有 Eino API 转换。

AgentInvoker 不设计成 `Invoke(ctx, map[string]any) (any, error)`。业务无关不等于无类型约束；增加新任务时复用中性 Invocation 协议，在 CodingAgent 之上增加新的业务 Agent 即可。

MVP 中 AgentInvoker 仍是 internal/agent 内部 Port，只提供 Invoke、Resume、Close，不建设 LangChain 式 Runnable 继承体系、Chain DSL、中间件管线或动态插件注册。除 EinoInvoker 外只实现测试用 ScriptedInvoker。

#### 24.6.2 WorkspaceTools Port

~~~go
package agent

type CheckPointStore interface {
    Set(ctx context.Context, key string, value []byte) error
    Get(ctx context.Context, key string) ([]byte, bool, error)
    Delete(key string)
}

type WorkspaceTools interface {
    ListFiles(ctx context.Context, request ListFilesRequest) (ListFilesResult, error)
    SearchCode(ctx context.Context, request SearchCodeRequest) (SearchCodeResult, error)
    ReadFile(ctx context.Context, request ReadFileRequest) (ReadFileResult, error)
    GitStatus(ctx context.Context, request GitStatusRequest) (GitStatusResult, error)
    ReadDiff(ctx context.Context, request ReadDiffRequest) (session.DiffResult, error)
    ApplyPatch(ctx context.Context, request ApplyPatchRequest) (ApplyPatchResult, error)
    RunChecks(ctx context.Context, request RunChecksRequest) (RunChecksResult, error)
}
~~~

| 方法 | 模型可提供的参数 | CodePilot 隐式绑定的参数 |
| --- | --- | --- |
| ListFiles | pattern、limit | TurnScope.WorktreeRoot、ignore 和敏感规则 |
| SearchCode | query、是否正则、glob、limit | WorktreeRoot、输出上限 |
| ReadFile | 相对路径、起始行、行数 | WorktreeRoot、路径校验、字节上限 |
| GitStatus | 无 | WorktreeRoot |
| ReadDiff | DiffKind、可选文件 | Session PatchRecord、WorktreeRoot |
| ApplyPatch | unified diff、修改意图 | Session / Turn、PermissionMode、hash 和路径策略 |
| RunChecks | LanguageProfile 中 CheckPlan 的 ID | program、args、Dir、timeout、env 和输出上限 |

模型不能提交绝对 WorktreeRoot、任意 command、env 或 timeout。Tool Adapter 根据 TurnScope 补充这些可信参数。

#### 24.6.3 MVP Coding Tool 实现与注册

MVP 的七个 Tool 都是 internal/agent 中的私有 struct，并显式满足 tool.Tool：

| Tool 名称 | 实现文件 | 调用的强类型能力 |
| --- | --- | --- |
| list_files | tool_list_files.go | WorkspaceTools.ListFiles |
| search_code | tool_search_code.go | WorkspaceTools.SearchCode |
| read_file | tool_read_file.go | WorkspaceTools.ReadFile |
| git_status | tool_git_status.go | WorkspaceTools.GitStatus |
| git_diff | tool_git_diff.go | WorkspaceTools.ReadDiff |
| apply_patch | tool_apply_patch.go | WorkspaceTools.ApplyPatch |
| run_checks | tool_run_checks.go | WorkspaceTools.RunChecks |

~~~go
type readFileTool struct {
    scope      session.TurnScope
    workspaces WorkspaceTools
}

type readFileArguments struct {
    Path      string `json:"path"`
    StartLine int    `json:"start_line,omitempty"`
    LineCount int    `json:"line_count,omitempty"`
}

func (t *readFileTool) Definition() tool.Definition
func (t *readFileTool) Invoke(ctx context.Context, arguments json.RawMessage) (tool.Result, error)

type toolsetDependencies struct {
    Workspaces WorkspaceTools
    CodeIntel  CodeNavigator // P1；MVP 为 nil
}

func buildToolRegistry(
    scope session.TurnScope,
    language LanguageProfile,
    deps toolsetDependencies,
) (*tool.Registry, error)
~~~

每个 Tool 使用独立 arguments struct 严格解码 JSON 并拒绝未知字段，再将捕获的可信 scope 与模型参数组合成 WorkspaceTools request。Tool 不直接操作文件系统、Git 或子进程。Definition 的 Schema 必须与 arguments struct 同步，测试使用有效、缺字段、未知字段和边界值用例验证二者一致。

具体 Tool 类型和构造函数保持 package 私有，因为它们只由 toolset.go 组装；扩展点是 tool.Tool 行为契约，不是把每个实现变成公共 API。

#### 24.6.4 ModelFactory、LanguageResolver 和 CodeNavigator Port

~~~go
type ModelFactory interface {
    NewChatModel(
        ctx context.Context,
        modelRef ModelRef,
    ) (model.ToolCallingChatModel, error)
}

type LanguageResolver interface {
    ResolveLanguage(ctx context.Context, root string) (LanguageProfile, error)
}

type CodeNavigator interface {
    Definition(ctx context.Context, request DefinitionRequest) ([]Location, error)
    References(ctx context.Context, request ReferencesRequest) ([]Location, error)
    Symbols(ctx context.Context, request SymbolsRequest) ([]Symbol, error)
    Diagnostics(ctx context.Context, request DiagnosticsRequest) ([]Diagnostic, error)
    CloseWorktree(ctx context.Context, worktreeID session.WorktreeID) error
}
~~~

ModelFactory 接收中性的 ModelRef，返回 Eino 的 model.ToolCallingChatModel 是有意的：它是 EinoInvoker 的下层 Port，Eino 类型只跨 provider -> agent.EinoInvoker 这一条边界。session 和 agent.AgentInvoker 都只看到中性类型。

CodeNavigator 是 P1 Port。MVP 的 CodingAgentConfig 中该字段为 nil，toolset.go 不注册 definition、references、symbols、diagnostics；不创建返回“未实现”的占位 Adapter。

#### 24.6.5 Factory、CodingAgent 与 EinoInvoker

~~~go
type Factory struct {
    // WorkspaceTools、LanguageResolver、Authorizer、AgentInvokerFactory
}

type Dependencies struct {
    Workspaces  WorkspaceTools
    Languages   LanguageResolver
    Authorizer  session.Authorizer
    Invokers    AgentInvokerFactory
    CodeIntel   CodeNavigator // P1；MVP 为 nil
    Contexts    *contextmanager.Manager
}

func NewFactory(deps Dependencies) (*Factory, error)
func (f *Factory) CreateCodingAgent(
    ctx context.Context,
    config session.CodingAgentConfig,
) (session.CodingAgent, error)

type CodingAgent struct {
    // 不可变 CodingAgentConfig、WorkspaceTools、LanguageResolver、Authorizer、AgentInvoker、Context Manager
}

func (a *CodingAgent) RunTurn(
    ctx context.Context,
    request session.TurnRequest,
    events session.EventSink,
) (session.TurnResult, error)

func (a *CodingAgent) Close() error

type AgentInvokerFactory interface {
    CreateInvoker(ctx context.Context) (AgentInvoker, error)
}

type EinoInvokerDependencies struct {
    Models      ModelFactory
    Checkpoints CheckPointStore
}

func NewEinoInvokerFactory(deps EinoInvokerDependencies) (*EinoInvokerFactory, error)
func (f *EinoInvokerFactory) CreateInvoker(ctx context.Context) (AgentInvoker, error)

type EinoInvoker struct {
    // Eino ChatModelAgent、adk.Runner、ModelFactory 和 CheckPointStore
}

func (i *EinoInvoker) Invoke(
    ctx context.Context,
    input InvocationInput,
    events InvocationEventSink,
) (InvocationResult, error)

func (i *EinoInvoker) Resume(
    ctx context.Context,
    input ResumeInput,
    events InvocationEventSink,
) (InvocationResult, error)

func (i *EinoInvoker) Close() error
~~~

CodingAgent.RunTurn 内部顺序：

1. 将 session.Message 转换为 contextmanager.Message，并标记当前 Turn 的 User Message。
2. 根据 LanguageProfile 组合 system prompt。
3. 调用 buildToolRegistry，以固定顺序注册当前 Turn 可用的 tool.Tool。
4. 调用 Context Manager，按注册顺序应用全部 Strategy，并校验输出仍包含且仅包含一个当前 User Message。
5. 将 Manager 输出转换为 InvocationMessage，创建 InvocationInput，以 TurnID 字符串作为 Invocation ID 和 CheckpointID，并附带只读使用的 Registry。
6. 调用 AgentInvoker.Invoke，并将 InvocationEvent 转换为 CodePilot Event。
7. Invocation 被审批中断时发布 ApprovalRequested，等待 Authorizer Decision，再调用 AgentInvoker.Resume。
8. 将 InvocationResult 转换为 TurnResult；返回事实结果，不自行决定 verified。

EinoInvoker.Invoke / Resume 内部顺序：

1. 校验中性的 InvocationInput / ResumeInput 和非 nil Registry，不接收 Worktree 或 Session 业务对象。
2. 通过 ModelFactory 创建或复用 ToolCallingChatModel。
3. 将 InvocationMessage 转为 Eino schema.Message，并根据 Registry.Definitions 创建 Eino Tool 描述。
4. 创建 ChatModelAgent 和 adk.Runner，执行 Run 或 ResumeWithParams。
5. 收到 ToolCall 时通过 Registry.Lookup 找到 Tool，调用 Tool.Invoke，并将 tool.Result 转换为 Eino ToolResult。
6. 将 Eino AgentEvent 转换为 InvocationEvent。
7. 达到限制、发生错误或 context 取消时停止当前 Runner，并返回中性的终止原因。

Eino 的 Runner 官方负责事件流、Checkpoint 保存和 interrupt recovery；CodePilot 只在 agent 包中封装这些版本相关 API。[Eino Runner](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_extension/)

#### 24.6.6 Approval interrupt 适配

approval_tool.go 的行为：

1. workspace.Service 完成硬性校验并调用 Authorizer.Authorize。
2. allow 直接执行；deny 返回结构化 ToolResult。
3. prompt 返回带 ApprovalRequestID 的 ApprovalRequired 错误。
4. 具体 Tool 将其转换为 tool.ResultInterrupted，Eino Tool Adapter 再转换为 compose.Interrupt；payload 只包含脱敏审批数据。
5. EinoInvoker 返回 interrupted 结果；CodingAgent 发布 ApprovalRequested，并调用 Authorizer.WaitDecision。
6. Session.ResolveApproval 调用 Authorizer.Resolve 唤醒等待者。
7. CodingAgent 将 Decision 转为 InterruptResponse 并调用 AgentInvoker.Resume；EinoInvoker 使用 ResumeWithParams 恢复。工具恢复后重新执行硬性校验和精确 Action 匹配。
8. allow-once 在一次匹配后消费；allow-session 保存 Fingerprint；deny 返回拒绝结果。

恢复时绝不直接调用底层 patch 或 CommandExecutor，必须重新进入 workspace.Service，防止等待期间文件已经变化。Eino 官方 HITL 同样通过 CheckPointID 和 ResumeWithParams 关联中断前后的运行。[Eino HITL](https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_hitl/)

#### 24.6.7 MemoryCheckPointStore

~~~go
type MemoryCheckPointStore struct {
    // sync.RWMutex + map[string][]byte
}

func NewMemoryCheckPointStore() *MemoryCheckPointStore
func (s *MemoryCheckPointStore) Set(ctx context.Context, key string, value []byte) error
func (s *MemoryCheckPointStore) Get(ctx context.Context, key string) ([]byte, bool, error)
func (s *MemoryCheckPointStore) Delete(key string)
func (s *MemoryCheckPointStore) Close() error
~~~

Set 和 Get 必须复制 []byte，不能向调用方暴露内部 map 的切片。Close 清空全部 checkpoint；Delete 是 CodePilot 自己的生命周期方法，不属于 Eino CheckPointStore。

### 24.7 internal/provider

#### 24.7.1 CredentialStore 与 ProviderProfileStore Port

~~~go
package provider

type Secret []byte

type CredentialLocation string

const (
    CredentialInKeyring CredentialLocation = "keyring"
    CredentialInMemory  CredentialLocation = "memory"
)

type CredentialStore interface {
    Get(ctx context.Context, profileID session.ProviderProfileID) (Secret, bool, error)
    Put(ctx context.Context, profileID session.ProviderProfileID, secret Secret) (CredentialLocation, error)
    Delete(ctx context.Context, profileID session.ProviderProfileID) error
}

type ProviderProfileStore interface {
    ListProfiles(ctx context.Context) ([]Profile, error)
    LoadProfile(ctx context.Context, id session.ProviderProfileID) (Profile, error)
    SaveProfile(ctx context.Context, value Profile) error
    DeleteProfile(ctx context.Context, id session.ProviderProfileID) error
}
~~~

CredentialStore 由 credential.FallbackStore 实现，KeyringStore / MemoryStore 是其两个明确后端；ProviderProfileStore 由 config.ProviderFileStore 实现。Secret 不实现 Stringer，不允许用 %v、%s 或结构化日志字段输出。

#### 24.7.2 Provider Adapter

Provider 种类是已经存在的变化轴，因此定义 package 内 Adapter interface：

~~~go
type Adapter interface {
    Kind() Kind
    Defaults() Defaults
    Validate(ctx context.Context, request ValidationRequest) (ValidationResult, error)
    ListModels(ctx context.Context, request ModelListRequest) ([]Model, error)
    NewChatModel(
        ctx context.Context,
        request ChatModelRequest,
    ) (model.ToolCallingChatModel, error)
}
~~~

| 方法 | 作用 |
| --- | --- |
| Kind | 返回稳定 kind，用于 Catalog 查找，不使用显示名称作为 ID |
| Defaults | 返回内置 Base URL、推荐 Model 和是否需要 Key |
| Validate | 验证认证、模型访问和最小 tool-calling；返回结构化失败阶段 |
| ListModels | 获取可选模型；Provider 不支持枚举时返回 Catalog 推荐项并标记来源 |
| NewChatModel | 创建 Eino ToolCallingChatModel，不保存进全局单例 |

OpenAI、DeepSeek、Ollama 和 Compatible adapter 均在 provider package 内通过文件区分。Service 构造时显式注册，禁止 init() 全局注册：

~~~go
func NewService(
    profiles ProviderProfileStore,
    credentials CredentialStore,
    adapters []Adapter,
) (*Service, error)
~~~

Service 实现 session.ModelCatalog 和 agent.ModelFactory。ConfigureProvider 的事务顺序为“验证临时输入 -> 写 Credential -> 写 Profile”；Profile 写入失败时尝试删除刚写入的 Credential，并报告可能需要手动清理。Put 返回 memory 时 Profile 不写 credential_ref，ConfigureProvider 返回需要 UI 展示的“仅当前进程有效”警告。

### 24.8 internal/workspace

workspace.Service 同时实现 session.WorkspaceReader 和 agent.WorkspaceTools。session 使用前者完成激活、切换和 UI Diff；agent 使用后者执行受控 Tool。

#### 24.8.1 ActionAuthorizer 与 CommandExecutor

~~~go
package workspace

type ActionAuthorizer interface {
    Authorize(
        ctx context.Context,
        mode session.PermissionMode,
        action session.Action,
    ) (session.Authorization, error)
}

type CommandExecutor interface {
    Run(ctx context.Context, spec CommandSpec) (CommandResult, error)
}

type Dependencies struct {
    Authorizer ActionAuthorizer
    Executor   CommandExecutor
    Limits     Limits
}
~~~

ActionAuthorizer 是 workspace 消费的窄接口，approval.Service 隐式实现。CommandExecutor 是未来 Sandbox 的替换边界：MVP 注入 LocalCommandExecutor，后续可以注入 ContainerExecutor，而 WorkspaceTools 和 Agent Tool schema 不变。

#### 24.8.2 Service

~~~go
func NewService(deps Dependencies) (*Service, error)

func (s *Service) ResolveWorktree(ctx context.Context, path string) (session.ResolvedWorktree, error)
func (s *Service) ReadWorktreeState(ctx context.Context, root string) (session.WorktreeState, error)
func (s *Service) ReadDiff(ctx context.Context, request session.DiffRequest) (session.DiffResult, error)

func (s *Service) ListFiles(ctx context.Context, request agent.ListFilesRequest) (agent.ListFilesResult, error)
func (s *Service) SearchCode(ctx context.Context, request agent.SearchCodeRequest) (agent.SearchCodeResult, error)
func (s *Service) ReadFile(ctx context.Context, request agent.ReadFileRequest) (agent.ReadFileResult, error)
func (s *Service) GitStatus(ctx context.Context, request agent.GitStatusRequest) (agent.GitStatusResult, error)
func (s *Service) ApplyPatch(ctx context.Context, request agent.ApplyPatchRequest) (agent.ApplyPatchResult, error)
func (s *Service) RunChecks(ctx context.Context, request agent.RunChecksRequest) (agent.RunChecksResult, error)
~~~

每个公开方法都按固定顺序执行：

1. 校验 request 和限制值。
2. 规范化 WorktreeRoot 和目标路径。
3. 执行 Worktree、符号链接、.git 和敏感文件硬规则。
4. 对副作用构建稳定 Action.Fingerprint 并调用 ActionAuthorizer。
5. prompt / deny 时不触碰文件和子进程。
6. allow 后再次检查易变化事实，例如文件 hash。
7. 执行操作并返回有界结构化结果。

ApplyPatch 的额外约束：

- 仅接受 unified diff，不接受“目标文件 + 完整覆盖内容”。
- 先产生 Proposed Diff，再执行 git apply --check 或等价检查。
- 不使用 --reject，不产生 .rej 文件。
- apply 前后计算 sha256；外部修改导致 before hash 变化时返回 drift。
- 成功结果必须包含 session.PatchRecord 所需的 patch、文件和 hash。
- 失败后重新读取 Git Diff；检测到意外部分修改时发布高优先级错误，绝不自动 reset 用户 Worktree。

#### 24.8.3 LocalCommandExecutor

~~~go
type LocalCommandExecutor struct{}

func NewLocalCommandExecutor() *LocalCommandExecutor
func (e *LocalCommandExecutor) Run(ctx context.Context, spec CommandSpec) (CommandResult, error)
~~~

CommandSpec：

~~~go
type CommandSpec struct {
    ID             string
    Program        string
    Args           []string
    Dir            string
    EnvAllowlist   []string
    Timeout        time.Duration
    MaxOutputBytes int
}
~~~

Run 必须使用 exec.CommandContext(ctx, Program, Args...)，不能拼 Shell 字符串。Dir 必须等于已验证 WorktreeRoot 或其允许子目录。环境从最小继承集合构造，不允许工具请求任意环境变量。stdout / stderr 并发读取、统一限制大小并保留截断标记。

### 24.9 internal/language

Language Strategy 是新增语言的唯一主要扩展点：

~~~go
package language

type Strategy interface {
    ID() agent.LanguageID
    Detect(ctx context.Context, root string) (Detection, error)
    BuildProfile(ctx context.Context, root string) (agent.LanguageProfile, error)
}

type Registry struct {
    // ordered strategies
}

func NewRegistry(strategies ...Strategy) (*Registry, error)
func (r *Registry) ResolveLanguage(ctx context.Context, root string) (agent.LanguageProfile, error)
~~~

| 方法 | 作用 |
| --- | --- |
| Detect | 根据 go.mod、pyproject.toml 等事实返回 score 和 evidence |
| BuildProfile | 返回 prompt hint、格式化策略和可选 CheckPlan，不执行命令 |
| ResolveLanguage | 选择最高可信策略；并列或无法识别时返回通用只读 Profile |

Go Strategy 生成 go test、针对性 go test 和可选 go vet 计划；格式化优先使用进程内 go/format。Python Strategy 只使用项目已有 pytest，不安装 black、ruff、pytest 或其他依赖。

CheckPlan 必须预先固定 Program 和参数模板。模型只能选择 plan ID 和被允许的测试目标，不能提交新的 executable。

### 24.10 internal/approval

approval.Service 实现 session.Authorizer；其 Authorize 方法同时满足 workspace.ActionAuthorizer 的窄方法集。

~~~go
package approval

func NewService() *Service

func (s *Service) Authorize(
    ctx context.Context,
    mode session.PermissionMode,
    action session.Action,
) (session.Authorization, error)

func (s *Service) WaitDecision(
    ctx context.Context,
    requestID session.ApprovalRequestID,
) (session.ApprovalDecision, error)

func (s *Service) Resolve(
    ctx context.Context,
    resolution session.ApprovalResolution,
) error

func (s *Service) ClearSession(ctx context.Context, sessionID session.SessionID) error
func (s *Service) Close() error
~~~

内部状态按 SessionID 隔离：

- pending：ApprovalRequestID -> request + one-result channel。
- grants：Action.Fingerprint -> Session 级临时授权。
- once：审批恢复后仅可消费一次的 Fingerprint。

Authorize 先应用硬性结果：workspace 已经判为非法的 Action 根本不会进入该方法。随后按 Permission Mode、grants 和 ActionKind 返回 allow / prompt / deny。Resolve 必须验证 RequestID、SessionID、TurnID 都匹配 pending request，防止旧 UI 事件批准当前 Turn。

Close 和 ClearSession 必须以 cancelled 结果唤醒所有等待者，不能让 Agent goroutine 永久阻塞。

### 24.11 internal/sessionstore

FileStore 同时实现 session.SessionStore 和 session.WorkspaceRegistry：

~~~go
package sessionstore

type FileStore struct {
    // StateDir、per-session locks、已加载索引
}

func NewFileStore(stateDir string) (*FileStore, error)
func (s *FileStore) Close() error

type MemoryStore struct {
    // mutex + maps，保存值的深拷贝
}

func NewMemoryStore() *MemoryStore
func (s *MemoryStore) Reset()

type ProcessLock struct {
    // wraps gofrs/flock exclusive lock
}

func AcquireProcessLock(stateDir string) (*ProcessLock, error)
func (l *ProcessLock) Close() error
~~~

具体实现的方法签名完全来自两个消费方 Port，不再增加 CRUD 风格的公共 API。

实现规则：

- layout.go 是路径映射纯函数，不提供通用文件工具。
- json_file.go 只服务本 package 的版本化 JSON 原子写。
- jsonl.go 只服务 Message、TurnRecord、PatchRecord 三种明确记录。
- MemoryStore 的读写都深拷贝 slice / map，避免测试因共享引用得到错误结论。
- ListSessions 只读 metadata；按 updated_at 倒序，并用 SessionID 作为稳定次级排序。
- 同一个 FileStore 内每个 Session 独立 mutex；registry 和每个 metadata 文件有自己的锁，禁止持锁调用 Session Service。
- FileStore.Close 后所有方法返回 ErrStoreClosed。
- ProcessLock 由 app.foundation 最先获取、最后释放；锁失败返回 ErrStateInUse，不尝试自动删除 .lock 文件。

### 24.12 internal/credential

~~~go
package credential

type KeyringStore struct {
    // service name 固定为 CodePilot
}

func NewKeyringStore() (*KeyringStore, error)
func (s *KeyringStore) Get(ctx context.Context, id session.ProviderProfileID) (provider.Secret, bool, error)
func (s *KeyringStore) Put(ctx context.Context, id session.ProviderProfileID, secret provider.Secret) (provider.CredentialLocation, error)
func (s *KeyringStore) Delete(ctx context.Context, id session.ProviderProfileID) error

type MemoryStore struct {
    // mutex + map，值使用 byte copy
}

func NewMemoryStore() *MemoryStore
func (s *MemoryStore) Get(ctx context.Context, id session.ProviderProfileID) (provider.Secret, bool, error)
func (s *MemoryStore) Put(ctx context.Context, id session.ProviderProfileID, secret provider.Secret) (provider.CredentialLocation, error)
func (s *MemoryStore) Delete(ctx context.Context, id session.ProviderProfileID) error
func (s *MemoryStore) Close() error

type FallbackStore struct {
    // KeyringStore + MemoryStore
}

func NewFallbackStore(keyring *KeyringStore, memory *MemoryStore) *FallbackStore
func (s *FallbackStore) Get(ctx context.Context, id session.ProviderProfileID) (provider.Secret, bool, error)
func (s *FallbackStore) Put(ctx context.Context, id session.ProviderProfileID, secret provider.Secret) (provider.CredentialLocation, error)
func (s *FallbackStore) Delete(ctx context.Context, id session.ProviderProfileID) error
func (s *FallbackStore) Close() error
~~~

FallbackStore 隐式实现 provider.CredentialStore。MemoryStore.Close 尽可能覆盖 map 中已有 byte slice 后删除引用；Go GC 环境下不能承诺物理内存立即擦除，因此产品文案不能宣称“安全清除内存”。

Keyring 不可用与 Key 不存在是不同情况：前者由 FallbackStore 写入 MemoryStore 并返回 CredentialInMemory，后者触发当前 Profile 重新认证。非“Keyring 不可用”类错误不得静默回退，避免用户误以为凭据已持久化。

### 24.13 internal/config

config 使用具体函数和 struct，不定义 Loader / Saver interface：

~~~go
package config

type Paths struct {
    ConfigDir string
    StateDir  string
}

func ResolvePaths() (Paths, error)
func ValidatePaths(paths Paths) error

func Load(path string) (Config, error)
func Save(path string, value Config) error
func Defaults() Config
func Validate(value Config) error

type ProviderFileStore struct {
    path string
}

func NewProviderFileStore(path string) *ProviderFileStore
func (s *ProviderFileStore) ListProfiles(ctx context.Context) ([]provider.Profile, error)
func (s *ProviderFileStore) LoadProfile(ctx context.Context, id session.ProviderProfileID) (provider.Profile, error)
func (s *ProviderFileStore) SaveProfile(ctx context.Context, value provider.Profile) error
func (s *ProviderFileStore) DeleteProfile(ctx context.Context, id session.ProviderProfileID) error
~~~

Load / Save 是确定文件格式的具体能力，没有第二种真实实现，不额外抽象。ProviderFileStore 则实现 provider.ProviderProfileStore，因为 provider 不应知道 YAML 和 ConfigDir。

### 24.14 internal/lsp（P1）

P1 创建 lsp.Navigator，实现 agent.CodeNavigator：

~~~go
package lsp

type Navigator struct {
    // WorktreeID -> language server process/client
}

func NewNavigator(options Options) (*Navigator, error)
func (n *Navigator) Definition(ctx context.Context, request agent.DefinitionRequest) ([]agent.Location, error)
func (n *Navigator) References(ctx context.Context, request agent.ReferencesRequest) ([]agent.Location, error)
func (n *Navigator) Symbols(ctx context.Context, request agent.SymbolsRequest) ([]agent.Symbol, error)
func (n *Navigator) Diagnostics(ctx context.Context, request agent.DiagnosticsRequest) ([]agent.Diagnostic, error)
func (n *Navigator) CloseWorktree(ctx context.Context, id session.WorktreeID) error
func (n *Navigator) Close() error
~~~

实现约束：

- gopls / pyright 按 Worktree 懒加载，不在程序启动时扫描所有 Workspace。
- 启动语言服务器属于外部进程，必须复用 CommandExecutor 和审批语义。
- 所有返回 Location 再经过 Worktree 边界过滤。
- 语言服务器不存在或启动失败时返回可降级错误，Agent 继续使用 search / read。
- LSP JSON-RPC、文档 URI 和进程状态只存在于 lsp package，不进入 Session 文件。

### 24.15 internal/contextmanager

contextmanager 是模型调用前的 provider-neutral 上下文处理边界。当前阶段只定义策略契约、方法依赖的数据结构、顺序组合 Manager 和空的 NopStrategy，不提前实现摘要、token 估算或历史裁剪算法。

~~~go
package contextmanager

type Role string

const (
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
)

type Scope struct {
    SessionID         string
    TurnID            string
    WorktreeRoot      string
    ProviderProfileID string
    ModelID           string
}

type Message struct {
    ID      string
    Role    Role
    Content string
    Current bool
}

type Request struct {
    Scope        Scope
    SystemPrompt string
    Messages     []Message
}

type Result struct {
    SystemPrompt string
    Messages     []Message
}

type Strategy interface {
    Process(ctx context.Context, request Request) (Result, error)
}

type NopStrategy struct{}

func (NopStrategy) Process(ctx context.Context, request Request) (Result, error)

type Manager struct {
    strategies []Strategy
}

func NewManager(strategies ...Strategy) (*Manager, error)
func (m *Manager) Process(ctx context.Context, request Request) (Result, error)
~~~

调用和组合规则：

1. internal/app 在 buildRuntime 中显式创建 `NewManager(NopStrategy{})` 并注入 agent.Factory；CodingAgent 不在内部偷偷选择默认策略。
2. CodingAgent 在 LanguageProfile system prompt 和中性历史准备完成后、构造 InvocationInput 之前，只调用一次 Manager.Process，不直接调用具体 Strategy。
3. Manager 严格按照 NewManager 的参数顺序调用 Strategy。第 N 个 Strategy 收到第 N-1 个 Strategy 返回的 SystemPrompt 和 Messages，因此裁剪、摘要、缓存提示等策略可以组合使用。
4. Scope 在整个策略链中保持不变，只向策略提供 Session、Turn、Worktree、Provider 和 Model 选择依据；策略不能修改可信 TurnScope、Tool Registry、权限或运行限制。
5. 每个策略输入和 Manager 最终输出都复制 Message slice，避免策略保留或修改其他策略、CodingAgent 持有的切片。
6. 任一策略返回错误或 context 取消时立即停止后续策略，不调用 AgentInvoker，也不把部分压缩结果写回持久 Session。
7. CodingAgent 在 Manager 返回后验证 system prompt 非空、消息角色合法，并且原样保留且仅保留一条 `Current=true` 的当前 User Message；策略只能压缩历史上下文，不能改写当前请求。违反约束时当前 Turn 失败，不把不完整请求交给模型。
8. NopStrategy 只做隔离复制，不改变 prompt 或 message。它用于当前默认装配和未来策略全部关闭时的稳定基线。

未来增加策略时，只新增实现 `Strategy` 的具体类型并在组合根决定顺序。例如 `TokenBudgetStrategy -> SummaryStrategy -> RecentTurnsStrategy` 会由同一个 Manager 顺序调用；Session、CodingAgent 和 EinoInvoker 的接口不随策略数量变化。策略输出仍是 provider-neutral Message，不得包含 Eino、OpenAI 或 DeepSeek 私有消息对象。

## 25. 并发、事件与生命周期

### 25.1 Goroutine 所有权

MVP 只保留以下长期并发单元：

| Goroutine | 所有者 | 生命周期 |
| --- | --- | --- |
| Bubble Tea 主循环 | ui | App.Run 到 TUI 退出 |
| EventBridge 等待 Cmd | ui | 每次读取一条 Event 后由 Update 续订 |
| Active Turn | session.Service | StartTurn 到 Turn 收尾完成 |
| Eino event iterator | agent.EinoInvoker | 单个 Invoke / Resume 阶段 |
| 外部检查进程 | workspace CommandExecutor | 单个 run_checks ToolCall |
| LSP 读写循环 | lsp.Navigator，P1 | Active Worktree 使用期间 |

不创建后台 Session worker pool，也不允许多个 Turn 并发。Provider 验证由 UI tea.Cmd 发起并通过 context 取消。

### 25.2 Session Service 锁规则

session.Service 使用一个短临界区 mutex 保护：

- Active Session ID。
- RuntimeState。
- 当前 TurnID 和 cancel function。
- 当前 CodingAgent 引用。

锁规则：

1. 在锁内检查状态并完成 idle -> running 等原子转换。
2. 在锁内复制需要的引用和 TurnScope。
3. 释放锁后调用 Store、CodingAgent、Provider、Workspace、Authorizer 和 EventSink。
4. 外部调用返回后重新加锁，并用 TurnID 校验结果仍属于当前 Turn。
5. EventSink.Publish 期间不得持有 Session mutex。

这些规则防止审批 UI、取消和工具事件形成锁循环。

### 25.3 Event 结构与顺序

~~~go
type Event struct {
    ID        string
    Sequence  uint64
    SessionID SessionID
    TurnID    TurnID
    Kind      EventKind
    Time      time.Time
    Payload   EventPayload
}
~~~

- Sequence 在单个 Turn 内单调递增；Session 级事件使用独立 sequence。
- EventPayload 使用带类型的 struct union，不使用 map[string]any。
- Event ID 用于 UI 忽略重复发送；不作为持久化主键。
- EventBridge 只保证单进程内发布顺序，不提供跨重启重放。
- AssistantDelta 可以合并但不能重排；最终事件必须在所有相关 Diff / Tool Event 之后发布。
- UI 收到非 Active Session / Turn 的过期 Event 时忽略，并保留 debug 级诊断计数。

Session 传给 CodingAgent 的不是裸 UI EventBridge，而是 turnEventSink：

1. 校验 Event 的 SessionID / TurnID。
2. 对 PatchApplied 等领域事件先调用 SessionStore.AppendPatch。
3. 更新本 Turn 的验证证据。
4. 再转发到 UI EventBridge。

turnEventSink 是 session package 私有组件，不增加新的跨模块 Port。

### 25.4 取消

Ctrl+C 调用 CancelTurn：

1. 原子设置 RuntimeState=cancelling。
2. 调用当前 Turn cancel function。
3. Eino Runner 停止模型 stream 和 tool calls。
4. exec.CommandContext 终止检查子进程，并等待 stdout / stderr reader 退出。
5. Authorizer 取消 pending approval。
6. CodingAgent 返回后进入统一 Turn 收尾，再回到 idle。

CancelTurn 在 idle 时返回 ErrNoActiveTurn；连续调用在 cancelling 状态幂等，不重复发布 TurnCancelled。

### 25.5 App 关闭顺序

App.Close 使用 sync.Once，按以下顺序执行：

1. 禁止 UI 提交新的 Command。
2. 取消 Active Turn 并等待有界时间。
3. 保存 Active Session metadata。
4. 关闭 CodingAgent（连带其 EinoInvoker）并清理内存 Checkpoint。
5. 清理 Authorizer pending request / grant。
6. 关闭 LSP，P1。
7. 关闭 SessionStore、Credential MemoryStore 和 EventBridge。
8. 释放 StateDir ProcessLock。

单个 Close 失败不跳过后续清理；最终返回 errors.Join 的结果。OS KeyringStore 通常没有长期资源，不要求 Close。

## 26. 错误模型

跨 UI 边界的错误统一使用 session.AppError：

~~~go
type ErrorCode string

const (
    ErrInvalidInput         ErrorCode = "invalid-input"
    ErrInvalidState         ErrorCode = "invalid-state"
    ErrNotFound             ErrorCode = "not-found"
    ErrConflict             ErrorCode = "conflict"
    ErrWorkspaceUnavailable ErrorCode = "workspace-unavailable"
    ErrProviderUnavailable  ErrorCode = "provider-unavailable"
    ErrPermissionDenied     ErrorCode = "permission-denied"
    ErrApprovalRequired     ErrorCode = "approval-required"
    ErrCancelled            ErrorCode = "cancelled"
    ErrTimeout              ErrorCode = "timeout"
    ErrCorruptedState       ErrorCode = "corrupted-state"
    ErrPersistence          ErrorCode = "persistence"
    ErrInternal             ErrorCode = "internal"
)

type AppError struct {
    Code        ErrorCode
    Operation   string
    UserMessage string
    Cause       error
    Retryable   bool
}

func (e *AppError) Error() string
func (e *AppError) Unwrap() error
~~~

规则：

- UserMessage 可以进入 UI；Cause 只进入脱敏诊断，不直接展示 Provider response body。
- Operation 使用稳定短名，例如 session.start_turn、workspace.apply_patch。
- tool.Result 是给 Agent 的结构化结果，不等同于 AppError；路径拒绝、检查未通过等可预期结果不制造 panic。
- context.Canceled 归一化为 cancelled；DeadlineExceeded 根据所在层归一化为 model、turn 或 command timeout。
- 错误包装使用 %w，业务判断使用 errors.Is / errors.As，不比较字符串。
- API Key、Authorization header、完整环境变量和敏感文件内容在错误构造前脱敏。

tool.Result 必须区分：

| 状态 | 示例 | Agent 是否可继续 |
| --- | --- | --- |
| completed | 读取成功；检查进程非零退出；检查超时并返回证据 | 是 |
| denied | read-only 拒绝 patch；用户拒绝检查 | 是 |
| invalid | Tool 参数错误、路径非法、命令不在 allowlist | 是，由模型修正或结束 |
| failed | 文件读取 I/O 错误、进程无法启动、patch apply 意外失败 | 通常可以，取决于错误 |
| cancelled | 用户 Ctrl+C 或 Turn context 取消 | 否，立即结束 Turn |
| interrupted | Tool 等待审批并已经生成内存 Checkpoint | 否，必须先 Resume |

## 27. 测试设计

### 27.1 编译期 interface 断言

每个 Adapter 在自己的 package 添加断言：

~~~go
// internal/agent/factory.go
var _ session.CodingAgentFactory = (*Factory)(nil)
var _ session.CodingAgent = (*CodingAgent)(nil)
var _ AgentInvokerFactory = (*EinoInvokerFactory)(nil)
var _ AgentInvoker = (*EinoInvoker)(nil)
var _ tool.Tool = (*listFilesTool)(nil)
var _ tool.Tool = (*searchCodeTool)(nil)
var _ tool.Tool = (*readFileTool)(nil)
var _ tool.Tool = (*gitStatusTool)(nil)
var _ tool.Tool = (*gitDiffTool)(nil)
var _ tool.Tool = (*applyPatchTool)(nil)
var _ tool.Tool = (*runChecksTool)(nil)

// internal/sessionstore/file_store.go
var _ session.SessionStore = (*FileStore)(nil)
var _ session.WorkspaceRegistry = (*FileStore)(nil)

// internal/workspace/service.go
var _ session.WorkspaceReader = (*Service)(nil)
var _ agent.WorkspaceTools = (*Service)(nil)

// 其他实现文件遵循相同形式。
~~~

断言放在实现 package，避免消费方导入具体 Adapter。

### 27.2 测试替身

| Port | 测试实现 | 使用位置 |
| --- | --- | --- |
| CodingAgent / CodingAgentFactory | ScriptedCodingAgent / FakeCodingAgentFactory | Session 状态机、取消、事件顺序 |
| AgentInvoker | ScriptedInvoker | CodingAgent 的输入组装、恢复和事件转换 |
| Tool / Registry | stubTool / 真实 Registry | 注册校验、稳定顺序、Lookup 和调用分发 |
| SessionStore / WorkspaceRegistry | MemoryStore | Session 创建、切换、恢复 |
| WorkspaceReader | FakeWorkspaceReader | 路径不可用、Diff 和 drift |
| ModelCatalog | FakeModelCatalog | 首次配置和切换失败 |
| EventSink | RecordingEventSink | Event 顺序和 payload 脱敏 |
| Authorizer | FakeAuthorizer | approval / deny / cancel |
| WorkspaceTools | FakeWorkspaceTools | CodingAgent 工具组装与 Tool 循环 |
| ModelFactory | ScriptedModelFactory | Eino adapter 离线测试 |
| CredentialStore | credential.MemoryStore | Provider 行为测试 |
| CommandExecutor | FakeCommandExecutor | allowlist、timeout 和输出截断 |

Fake 只实现真实 Port，不建设通用 mocking framework。固定脚本模型按预设顺序返回 AssistantDelta、ToolCall、FinalResponse。

### 27.3 必测场景

Session：

- StartTurn 先保存用户消息，再调用 CodingAgent。
- running / awaiting-approval 时拒绝切换 Session 和 Provider。
- Ctrl+C 等待 CodingAgent / AgentInvoker 真正退出后才回到 idle。
- 旧 Turn Event 不影响新 Active Session。
- Provider 切换保留中性消息并重建 CodingAgent / AgentInvoker。

Agent：

- CodingAgent 生成的 InvocationInput 不包含 SessionID、WorktreeRoot、PermissionMode 或 Eino 类型。
- ScriptedInvoker 可以覆盖 final、error、limit、cancelled 和 interrupted / resume 全部分支。
- EinoInvoker 对相同的 tool.Result 产生稳定 InvocationEvent，不泄露 Eino AgentEvent。
- Resume 的 CheckpointID 或 InterruptID 不匹配时拒绝恢复，不调用工具。

Tool：

- Registry 拒绝 nil、空名称、重复名称和非 JSON object Schema，并保持注册顺序。
- List / Definitions 返回副本；Invoke 开始后的并发读取不修改 Registry。
- 七个 MVP Tool 的 Definition 名称和 Schema 与各自 arguments struct 一致。
- arguments 中即使伪造 root、session_id、command、env 或 timeout 字段也会因未知字段被拒绝。
- Tool 实例始终使用构造时捕获的 TurnScope，不能被模型输入覆盖。
- Registry Lookup 不到名称时返回 invalid ToolResult，不执行任何 Workspace 方法。

Workspace / Approval：

- ..、绝对路径、符号链接和大小写差异不能逃逸 Worktree。
- .git、.env、私钥和 credential 文件拒绝访问。
- read-only、ask、auto-edit 决策表完整覆盖。
- Allow once 只能消费一次；Session grant 只匹配精确 Fingerprint。
- 审批等待期间外部修改文件时，恢复后 hash 校验阻止 patch。
- command 从不经 Shell，Dir 和参数不可由模型覆盖。

Store：

- metadata 原子替换失败保留旧文件。
- 最后一行 JSONL 截断可恢复并告警；中间损坏拒绝静默加载。
- 幂等 record ID 不重复追加。
- Archive 不删除历史。
- Session 和 Provider 文件中检索不到测试 API Key。

端到端：

- Go fixture 完成搜索、patch、审批、go test 和 verified。
- Python fixture 完成搜索、patch、审批、pytest 和 verified。
- 用户拒绝检查得到 unverified，而不是 verified。
- 无 Provider 自动打开 Picker；验证失败可以重试或切换。
- 跨 Workspace Session 切换后所有 Tool 使用目标 WorktreeRoot。

所有包含并发状态的 package 在 CI 中运行 go test -race ./...。真实 Provider、Keyring 和 LSP 测试使用单独 build tag 或手动集成测试，不进入默认离线测试。

## 28. 实现顺序

详细设计对应的推荐编码顺序：

1. 定义 session 领域类型、错误和全部 P0 Port，同时定义 tool.Tool、Result 和 Registry。
2. 完成 Registry 单元测试，并实现 sessionstore.MemoryStore、FakeCodingAgent、FakeAuthorizer 和 RecordingEventSink。
3. 完成 Session Service 状态机、Turn 快照、取消和事件顺序测试。
4. 实现 config.Paths、config.yaml、providers.yaml、FileStore 和 CredentialStore。
5. 实现 Provider Catalog、验证和 ModelFactory，再接 Provider Picker。
6. 实现 WorkspaceReader、只读能力、Diff 和安全路径规则。
7. 实现 Approval Policy、ApplyPatch 和 LocalCommandExecutor。
8. 实现七个 MVP tool.Tool、toolset Registry 组装和 ScriptedInvoker 测试。
9. 定义业务无关的 AgentInvoker 协议，接入 EinoInvoker、MemoryCheckPointStore 和 HITL resume。
10. 完成 Go Strategy 和端到端 Bugfix。
11. 在相同 Tool / Language Port 上增加 Python Strategy。
12. 完善脏 Worktree、持久化恢复、窄终端和错误文案。
13. P1 开始时才实现 lsp.Navigator 和对应 Tool。

每一步都必须保持 go test ./... 可通过；在真实 Eino 和文件系统接入前，Session 主流程已经能够用 fake 完整运行。

## 29. 明确不做的抽象

MVP 不引入：

- DI framework 或反射式依赖注入。
- Service Locator、全局 Container 或 init 注册。
- 通用 Repository / Unit of Work。
- 数据库和 ORM。
- 通用 Event Bus package；只使用 Session EventSink 和 UI EventBridge。
- 任意 Shell、通用 HTTP Tool 或 Package Installer。
- 全局 Tool Registry、init 自动注册、反射式 Tool 扫描、动态 Tool 插件和中间件链。
- 多 Agent、Graph / Workflow 编排和后台 Turn。
- 每个 struct 对应一个 interface。
- 为未来语言预留空方法；新增语言只实现当前 Strategy。
- 为未来 Sandbox 改写 Tool schema；只替换 CommandExecutor。
- 未开始实现的 LSP 和文件树占位代码。

判断是否需要新抽象时，至少满足以下之一：

1. 已经出现两个真实实现，例如 FileStore / MemoryStore。
2. 是明确外部边界，例如 CredentialStore、CommandExecutor。
3. 是产品承诺的变化轴，例如 Provider Adapter、Language Strategy。
4. 能显著简化核心模块测试，例如 Tool、CodingAgent、AgentInvoker、Authorizer、WorkspaceTools。

## 30. 详细设计评审项

- [ ] 是否认可 ConfigDir 与 StateDir 分离，以及平台默认位置。
- [ ] 是否认可 config.yaml / providers.yaml 不保存 API Key。
- [ ] 是否认可 Workspace -> Worktree -> Session 的状态目录层级不复制真实仓库。
- [ ] 是否认可 JSON + JSONL，而不是数据库和通用 Repository。
- [ ] 是否认可 SessionStore 保存产品 Session，MemoryCheckPointStore 只保存单 Turn Eino 状态。
- [ ] 是否认可 Session Service 的强类型用例方法和 UI SessionClient 分组。
- [ ] 是否认可 CodingAgent 保留业务语义，AgentInvoker 只暴露强类型 Invoke / Resume 协议。
- [ ] 是否认可业务无关的 tool.Tool、每 Turn Registry，以及七个显式注册的 MVP Tool。
- [ ] 是否认可 WorkspaceRegistry 与 WorkspaceReader 分离。
- [ ] 是否认可模型不能传 WorktreeRoot、任意 command、env 或 timeout。
- [ ] 是否认可审批恢复后重新执行硬性校验，而不是直接执行已等待的动作。
- [ ] 是否认可 CommandExecutor 是后续 Sandbox 的唯一主要替换边界。
- [ ] 是否认可 P1 才创建 lsp package，不保留空实现。
- [ ] 接口数量和方法粒度是否仍符合“简单、明确、可扩展”的目标。

---

至此，CodePilot 的架构设计、逻辑流程和详细设计已经形成可进入实现阶段的基线。
