Agent is idle (waiting for messages). agent_id: codepilot-project-review, agent_type: explore, status: idle, description: Comprehensive project architecture analysis, elapsed: 440s, total_turns: 1

[Turn 0]
---

│  4. 日志截断 (TruncatedJournalLine)                      │
│     ├─ 原因: 日志写入期间未正确同步                       │
│     ├─ 恢复: 自动修复截断的最后行                        │
│     ├─ 流程:                                              │
│     │  ├─ 检测: 最后一行JSON无效                         │
│     │  ├─ 处理: 舍弃截断行                               │
│     │  ├─ 记录: RecoveryWarning                          │
│     │  └─ 继续: 下次追加前修复                            │
│     │                                                     │
│     └─ 自动恢复，无需用户操作 ✓                           │
│                                                            │
└────────────────────────────────────────────────────────────┘

手动恢复操作 (doctor/repair命令):
┌────────────────────────────────────────────────────────────┐
│  codepilot doctor [--state-dir DIR]                       │
│  ├─ 只读诊断模式                                          │
│  ├─ 检查:                                                 │
│  │  ├─ 会话一致性                                        │
│  │  ├─ 工作树绑定                                        │
│  │  ├─ 日志完整性                                        │
│  │  └─ 权限记录                                          │
│  │                                                        │
│  └─ 输出报告（不修改）                                   │
│                                                            │
│  codepilot repair [--state-dir DIR]                      │
│  ├─ 显式修复模式                                         │
│  ├─ 操作:                                                 │
│  │  ├─ 修复工作树绑定                                    │
│  │  ├─ 清理孤立会话                                      │
│  │  ├─ 验证日志完整性                                    │
│  │  └─ 可撤销归档问题会话                                │
│  │                                                        │
│  └─ 安全：所有修复可撤销                                 │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

---

### 流程4️⃣ : 工具执行的完整流程

```
工具执行的7层防御:
┌─────────────────────────────────────────────────────────────┐
│  Layer 1: 数据清理                                          │
│  ├─ 去除敏感路径前缀                                       │
│  ├─ 检测和清除秘密:                                         │
│  │  ├─ Private Keys (-----BEGIN...PRIVATE KEY-----)        │
│  │  ├─ API Tokens (sk-*, gh_*, xox*)                      │
│  │  ├─ Bearer Tokens (Bearer <token>)                      │
│  │  ├─ 赋值语句 (api_key=, password=等)                   │
│  │  └─ Credential URLs (user:pass@host)                    │
│  │                                                         │
│  └─ 函数: DataPolicy.SanitizeToolArguments()               │
│                                                             │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────────┐
│  Layer 2: 权限检查                                          │
│  ├─ 检查活跃权限授予:                                       │
│  │  ├─ PermissionGrant.Scope == session?                   │
│  │  ├─ PermissionGrant.ToolName == requested ToolName?    │
│  │  ├─ PermissionGrant.Action == requested Action?        │
│  │  ├─ PermissionGrant.RevokedAt.IsZero()? (未撤销)       │
│  │  ├─ now >= CreatedAt && now < ExpiresAt? (TTL有效)     │
│  │  └─ 请求路径 ⊆ 授予路径? (路径覆盖)                   │
│  │                                                         │
│  ├─ 如果权限不足:                                          │
│  │  ├─ 权限模式 = read_only → 拒绝并返回错误               │
│  │  ├─ 权限模式 = ask → 创建中断，等待用户批准             │
│  │  └─ 权限模式 = auto_edit → 自动批准                    │
│  │                                                         │
│  └─ 函数: PermissionGranted()                              │
│                                                             │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────────┐
│  Layer 3: 工件边界                                          │
│  ├─ 文件大小检查:                                           │
│  │  ├─ read_file: file_size < MaxFileBytes?               │
│  │  ├─ edit_file: final_size < MaxAutoEditBytes?          │
│  │  └─ apply_patch: patch_size < MaxPatchBytes?           │
│  │                                                         │
│  ├─ 集合检查:                                              │
│  │  ├─ 文件计数 < MaxFiles?                               │
│  │  ├─ 补丁文件计数 < MaxPatchFiles?                      │
│  │  └─ 行数 < MaxAutoEditLines?                           │
│  │                                                         │
│  └─ 函数: ArtifactBoundary.Check()                         │
│                                                             │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────────┐
│  Layer 4: 安全边界                                          │
│  ├─ 敏感路径检查:                                           │
│  │  ├─ 匹配配置的敏感路径前缀                              │
│  │  ├─ 示例: ["node_modules", ".env", "secrets"]          │
│  │  └─ 如果匹配 → 拒绝                                     │
│  │                                                         │
│  ├─ 路径规范化验证:                                        │
│  │  ├─ 无绝对路径 (/)                                     │
│  │  ├─ 无父目录遍历 (..)                                  │
│  │  ├─ 无规范化符号                                       │
│  │  └─ 如果违反 → 拒绝                                     │
│  │                                                         │
│  ├─ 工作树边界:                                            │
│  │  ├─ 确保所有路径在工作树内                              │
│  │  ├─ path.Join(worktreeRoot, relativePath)             │
│  │  └─ 如果超出边界 → 拒绝                                │
│  │                                                         │
│  └─ 函数: SecurityBoundary.Check()                         │
│                                                             │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────────┐
│  Layer 5: 工具执行                                          │
│  ├─ 工具特定的业务逻辑:                                    │
│  │  ├─ read_file: 读取文件内容                            │
│  │  ├─ search_repository: 运行代码搜索                     │
│  │  ├─ edit_file: 修改文件行                              │
│  │  ├─ apply_patch: 应用统一补丁                          │
│  │  ├─ git_read: 执行只读Git命令                          │
│  │  └─ run_checks: 运行项目检查                            │
│  │                                                         │
│  ├─ 进度报告:                                              │
│  │  ├─ FOR EACH 进度更新:                                 │
│  │  │  ├─ progressSink.PublishToolProgress()              │
│  │  │  └─ 实时流式到UI                                    │
│  │  │                                                     │
│  │  └─ 例如: 逐行编辑、搜索进度等                         │
│  │                                                         │
│  └─ 函数: Tool.Execute()                                   │
│                                                             │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────────┐
│  Layer 6: 结果消毒                                          │
│  ├─ 清除秘密:                                              │
│  │  └─ 应用相同的秘密正则表达式                            │
│  │                                                         │
│  ├─ 截断输出:                                            │
│  │  ├─ 工具输出 < MaxOutput?                              │
│  │  └─ 搜索结果 < MaxMatches?                             │
│  │                                                         │
│  ├─ 清除敏感路径:                                          │
│  │  └─ 从输出中移除敏感路径前缀匹配                        │
│  │                                                         │
│  └─ 函数: DataPolicy.SanitizeToolResult()                  │
│                                                             │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────────┐
│  Layer 7: 中断处理                                          │
│  ├─ 检查: result.Status == ResultInterrupted?              │
│  │                                                         │
│  ├─ 是的→ 创建Interrupt:                                  │
│  │  ├─ InterruptID: uuid                                  │
│  │  ├─ InterruptKind: "user_approval"等                   │
│  │  ├─ 显示给UI                                           │
│  │  └─ 等待用户决策                                       │
│  │                                                         │
│  │  用户决策:                                              │
│  │  ├─ Approve:                                           │
│  │  │  └─ 创建PermissionGrant                             │
│  │  │     ├─ Scope: session                               │
│  │  │     ├─ ExpiresAt: session end                       │
│  │  │     └─ 调用Resume()继续                             │
│  │  │                                                     │
│  │  ├─ Reject:                                            │
│  │  │  └─ 返回错误结果                                    │
│  │  │                                                     │
│  │  └─ Modify:                                            │
│  │     └─ 手动修改后继续                                  │
│  │                                                        │
│  └─ 函数: tool.Result.Interrupt处理                       │
│                                                             │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
创建ToolResult消息并返回给模型
```

---

## 架构特点

### 1️⃣ 防御性编程设计

**多层验证策略**:
```
输入 → 消毒 → 权限 → 安全 → 执行 → 消毒 → 日志
```

每个边界都应用一致的数据策略:
- **DataPolicy接口** - 统一的消毒规则
- **SanitizeText()** - 清除秘密
- **SanitizeToolArguments()** - 参数清理
- **SanitizeToolResult()** - 结果清理
- **SanitizeMessage()** - 消息清理

**关键代码示例**:
```go
// 秘密检测正则表达式
var (
    privateKeyPattern = regexp.MustCompile(`(?is)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
    tokenPattern = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{16,}|gh[pousr]_[a-z0-9]{20,}|...)\b`)
    bearerPattern = regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9._~+/=-]{12,}`)
    assignmentPattern = regexp.MustCompile(`(?im)((?:api[_-]?key|password|secret|token|credential)\s*(?:=|:)\s*["']?)([^"'\s,;]{4,})`)
)

const RedactedValue = "[REDACTED]"

// 应用多层消毒
func (p *SecurityPolicy) SanitizeText(text string) string {
    result := text
    // Layer 1: 私钥
    result = privateKeyPattern.ReplaceAllString(result, "[REDACTED]")
    // Layer 2: Token
    result = tokenPattern.ReplaceAllString(result, "[REDACTED]")
    // Layer 3: Bearer
    result = bearerPattern.ReplaceAllString(result, "${1}[REDACTED]")
    // Layer 4: 赋值
    result = assignmentPattern.ReplaceAllString(result, "${1}[REDACTED]")
    // Layer 5: 敏感路径
    for _, path := range p.customPaths {
        result = removePathOccurrences(result, path)
    }
    return result
}
```

### 2️⃣ 接口隔离原则

**分层接口设计** - 防止下层污染上层:

```go
// Layer 1: Agent运行时 (通用，不知道编码)
type Runtime interface {
    Run(ctx context.Context, request RunRequest, sink EventSink) (RunResult, error)
    Resume(ctx context.Context, request ResumeRequest, sink EventSink) (RunResult, error)
    Recover(ctx context.Context, request RecoverRequest, sink EventSink) (RunResult, error)
}

// Layer 2: 编码代理 (知道工作区，但不知道特定工具)
type Service interface {
    StartTurn(ctx context.Context, request TurnRequest) (TurnResult, error)
    ResumeTurn(ctx context.Context, request ResumeTurnRequest) (TurnResult, error)
    RecoverTurn(ctx context.Context, request RecoverTurnRequest) (TurnResult, error)
}

// Layer 3: 工具系统 (工作区特定)
type ToolFactory interface {
    CreateTools(ctx context.Context, scope ToolScope) (*tool.Registry, error)
}

// 每层只看到它需要的接口，不会泄露实现细节
```

**关键特性**:
- ✅ 通用Agent不知道编码
- ✅ 编码Agent不知道具体工具实现
- ✅ 工具不知道UI如何显示
- ✅ UI不直接调用工具

### 3️⃣ 模块间通信模式

**事件驱动**:
```go
// 单向事件流
Agent → EventSink → CodingAgent → EventAdapter → UI

type EventSink interface {
    PublishAgentEvent(ctx context.Context, event Event) error
}

// Agent发布通用事件
type Event struct {
    ID         string
    Sequence   uint64
    SessionID  agentsession.ID
    RunID      agentsession.RunID
    Kind       EventKind  // run_started, step_started, tool_started等
    Assistant  *AssistantEvent
    Step       *StepEvent
    Tool       *ToolEvent
    Terminal   *TerminalEvent
}

// CodingAgent适配为产品事件
type AgentEventAdapter struct {
    // 映射关系
}

func (a *AgentEventAdapter) Adapt(agentEvent agent.Event) codingagent.Event {
    // 转换为产品事件
}
```

**依赖注入**:
```go
// Service接收所有依赖
type Service struct {
    deps Dependencies
}

type Dependencies struct {
    Sessions      SessionRepository
    AgentSessions agentsession.Repository
    Worktrees     WorktreeReader
    Workspaces    WorkspaceController
    Agent         AgentRunner
    Tools         ToolFactory
    Prompts       PromptBuilder
    Events        EventSink
    Providers     ProviderManager
    Limits        agent.RunLimits
}

// 便于测试，可以注入mock
func NewService(deps Dependencies) (*Service, error) {
    if deps.Sessions == nil || deps.Agent == nil || ... {
        return nil, errors.New("incomplete dependencies")
    }
    return &Service{deps: deps}, nil
}
```

### 4️⃣ 持久化优先设计

**Journal优先原则** - 所有重要操作先记录再执行:

```go
// Bad: 先执行后记录 ✗
tool.Execute()
journal.AppendEntry()  // 如果中间崩溃→数据丢失

// Good: 先记录后执行 ✓
journal.AppendRecord(RecordOperationStarted)
tool.Execute()
journal.AppendRecord(RecordOperationFinished)
// 即使中间崩溃，也能从RecordOperationStarted恢复
```

**两相式操作**:
```go
// Phase 1: 记录意图
r.appendRecord(ctx, request, Record{
    Type: RecordOperationStarted,
    Operation: &OperationData{Intent: OperationRun}
})

// Phase 2: 执行操作
runSteps(ctx, request)

// Phase 3: 记录完成
r.appendRecord(ctx, request, Record{
    Type: RecordOperationFinished,
    Operation: &OperationData{Intent: OperationRun, Status: RunCompleted}
})
```

### 5️⃣ 上下文持久化

**三阶段会话存储** - 确保崩溃安全:

```
┌──────────────────────────────────────────┐
│  Session Metadata (session.json)         │
│  ├─ ID                                  │
│  ├─ Name                                │
│  ├─ Archived                            │
│  ├─ CreatedAt / UpdatedAt               │
│  └─ 大小: ~100B                         │
└──────────────────────────────────────────┘

┌──────────────────────────────────────────┐
│  Append-Only Journal (journal.jsonl)     │
│  ├─ Entry序列                           │
│  ├─ Record序列                          │
│  ├─ 共享全局Sequence                    │
│  ├─ 原子追加 (temp → rename)            │
│  └─ 大小: 可增长到GB级                  │
└──────────────────────────────────────────┘

┌──────────────────────────────────────────┐
│  Content-Addressed Archives              │
│  ├─ journal复制 (SHA256命名)            │
│  ├─ 确定性打包 (gzip/tar)               │
│  ├─ 审计追踪                             │
│  └─ 可选历史归档                         │
└──────────────────────────────────────────┘

崩溃恢复流程:
1. 读取session.json元数据 ✓
2. 读取journal.jsonl所有条目 ✓
3. 检查最后一行是否完整 ?
   ├─ 是: 正常重放 ✓
   └─ 否: 舍弃并记录警告 ⚠
4. 构建Snapshot (全状态重建) ✓
5. 检查PendingRecovery条件 ?
   ├─ 无待恢复: 正常启动 ✓
   └─ 有待恢复: 显示恢复菜单 ⏸
```

### 6️⃣ 令牌预算管理

**分层预算策略**:
```
总预算
├─ 系统消息 (固定)
├─ 上下文消息
│  ├─ 压缩摘要 (Variable)
│  ├─ 最近完整轮次 (保留)
│  └─ 硬限制修剪
├─ 当前用户消息 (固定)
└─ 模型输出 (MaxOutputTokens)

例如 (GPT-4):
TotalBudget: 8000
├─ SystemPrompt: 500
├─ PreviousContext: 5000
│  ├─ Summaries: 2000
│  ├─ RecentMessages: 3000
│  └─ HardLimit: 1500
├─ CurrentUserMessage: 500
└─ OutputAllowance: 2000
```

**实现方式**:
```go
type CompactionStrategy struct {
    // 滚动摘要 + 尾部保留 + 硬限制
}

func (s *CompactionStrategy) Apply(ctx, messages, budget) Result {
    // 1. 识别"完整轮次"边界
    completeRounds := identifyRounds(messages)
    
    // 2. 对于每个完整轮次：
    for _, round := range completeRounds[:-1] {
        // 生成摘要
        summary := s.summarizer.Summarize(round)
        // 替换为摘要消息
    }
    
    // 3. 保留最近完整轮次
    // (最后一个round不压缩)
    
    // 4. 硬限制修剪
    if totalTokens > budget.Total {
        result = safeTrim(result, budget.Total)
    }
    
    return result
}
```

---

## 完整思维导图

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                          CodePilot 架构全景图                               │
└─────────────────────────────────────────────────────────────────────────────┘

                            ┌─────────────────┐
                            │  用户 (Terminal) │
                            └────────┬─────────┘
                                     │
                    ┌────────────────▼───────────────────┐
                    │    UI Model (BubbleTea)            │
                    │  ├─ 对话展示                       │
                    │  ├─ 命令处理 (/provider等)        │
                    │  ├─ 会话选择                       │
                    │  └─ 批准对话框                     │
                    └────────────────┬───────────────────┘
                                     │
                    ┌────────────────▼───────────────────┐
                    │   Event Bridge                      │
                    │  ├─ Agent Event → Product Event    │
                    │  └─ 事件适配                        │
                    └────────────────┬───────────────────┘
                                     │
        ┌────────────────────────────▼───────────────────────────┐
        │         CodingAgent.Service (产品层)                   │
        │  ┌────────────────────────────────────────────────┐   │
        │  │ Session Lifecycle                              │   │
        │  │ ├─ CreateSession()                            │   │
        │  │ ├─ SwitchSession()                            │   │
        │  │ ├─ RenameSession()                            │   │
        │  │ └─ ArchiveSession()                           │   │
        │  └────────────────────────────────────────────────┘   │
        │                                                        │
        │  ┌────────────────────────────────────────────────┐   │
        │  │ Turn Management                                │   │
        │  │ ├─ StartTurn()                                │   │
        │  │ ├─ ResumeTurn() (中断恢复)                    │   │
        │  │ ├─ RecoverTurn() (崩溃恢复)                   │   │
        │  │ └─ CancelTurn()                               │   │
        │  └────────────────────────────────────────────────┘   │
        │                                                        │
        │  ┌────────────────────────────────────────────────┐   │
        │  │ Security & Permissions                         │   │
        │  │ ├─ SecurityPolicy (秘密检测)                  │   │
        │  │ ├─ PermissionGrant (权限授予)                │   │
        │  │ ├─ SetPermissionMode() (ask/read-only/auto)  │   │
        │  │ └─ ValidatePath() (路径检查)                 │   │
        │  └────────────────────────────────────────────────┘   │
        │                                                        │
        │  ┌────────────────────────────────────────────────┐   │
        │  │ Workspace Management                           │   │
        │  │ ├─ WorkspaceManager                           │   │
        │  │ ├─ WorktreeReader                             │   │
        │  │ ├─ ConsistencyManager                         │   │
        │  │ └─ RelocateWorktree()                         │   │
        │  └────────────────────────────────────────────────┘   │
        │                                                        │
        │  ┌────────────────────────────────────────────────┐   │
        │  │ State Projection                               │   │
        │  │ ├─ Snapshot() (秘密隐藏视图)                  │   │
        │  │ ├─ ProjectSnapshot() (消毒)                   │   │
        │  │ ├─ TranscriptItem[] (消息序列)                │   │
        │  │ └─ Artifact (代码工件)                        │   │
        │  └────────────────────────────────────────────────┘   │
        │                                                        │
        └────────┬────────────────────────────────────────────┘
                 │
    ┌────────────┼─────────────┬──────────────┬─────────────┐
    │            │             │              │             │
    ▼            ▼             ▼              ▼             ▼
┌──────┐  ┌─────────┐  ┌────────────┐ ┌──────────┐ ┌──────────┐
│Agent │  │PromptBld│ │ToolFactory │ │EvtSink   │ │Provider  │
│Runr  │  │─────────│ │────────────┤ │──────────│ │Mgr       │
└──┬───┘  │SystemPr │ │ReadFile    │ │EventAptr │ │Profile   │
   │      │UntrustedC│ │SearchRepo  │ │          │ │Credential│
   │      │────────── │EditFile    │ │          │ │Model Sel │
   │      │           │ApplyPatch  │ │          │ │          │
   │      │           │GitRead     │ │          │ │          │
   │      │           │RunChecks   │ │          │ │          │
   │      │           │────────────│ │          │ │          │
   │      │           │Boundaries: │ │          │ │          │
   │      │           │ ├─ Data    │ │          │ │          │
   │      │           │ ├─ Permis  │ │          │ │          │
   │      │           │ ├─ Artifact│ │          │ │          │
   │      │           │ └─ Security│ │          │ │          │
   │      └───────────┴────────────┘ └──────────┘ └──────────┘
   │
   ▼
┌──────────────────────────────────────────────────────────────┐
│              Agent.Runtime (通用层)                          │
│  ┌─────────────────────────────────────────────────────┐   │
│  │ Run / Resume / Recover                              │   │
│  ├─ 步骤循环 (model → tools → repeat)                 │   │
│  ├─ 预算管理 (MaxSteps, MaxDuration等)                │   │
│  ├─ 中断处理 (工具中断/用户中止)                      │   │
│  ├─ 崩溃恢复 (RecoveryPlan)                           │   │
│  └─ 事件发布 (run_started, step_finished等)          │   │
│  └─────────────────────────────────────────────────────┘   │
└──────┬──────────────────────────────────────────────────────┘
       │
    ┌──┴───┬──────────┬─────────────┐
    │      │          │             │
    ▼      ▼          ▼             ▼
┌──────────────┐ ┌─────────────┐ ┌──────────────┐
