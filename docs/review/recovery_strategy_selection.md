# CodePilot 恢复策略选择流程详解

## 🎯 核心流程

恢复策略的选择遵循这样的流程：

```
Session Snapshot (会话快照)
        ↓
AnalyzeRecovery()        分析未完成的操作
        ↓
BuildRecoveryPlan()      根据 ReplayPolicy 建立恢复计划
        ↓
RecoveryAction           标记该如何恢复
        ↓
recoveryDecision()       根据用户或自动决策选择最终策略
        ↓
recoverPendingTool()     执行选中的恢复策略
```

---

## 📋 第一步：分析恢复状态 (AnalyzeRecovery)

系统读取持久化日志，找出：
1. **PendingRuns** - 已开始但未完成的运行
2. **PendingTools** - 已启动但未完成的工具
3. **PendingInterrupts** - 等待用户输入但未解决的中断

```go
// 从日志中识别未完成的工具
RecordToolStarted    ✅ 有日志
RecordToolFinished   ❌ 无日志
              ↓
        PendingTool (需要恢复)
```

---

## 🔍 第二步：构建恢复计划 (BuildRecoveryPlan)

根据工具的 **ReplayPolicy** 和状态，自动分类恢复动作：

### **场景1：结果已持久化 (ResultEntryPresent = true)**

```go
if pending.ResultEntryPresent {
    action.Kind = RecoveryReconcileTool  // 和解：结果已存，只补记录
    action.Automatic = true
    action.Reason = "工具结果已持久化，只需补充完成记录"
}
```

**例子：**
```
Tool启动日志: Tool #3 (生成代码) 已启动
工具结果: 已在数据库中找到结果
完成日志: 丢失

→ 恢复策略: ReconcileTool (简单和解)
→ 可自动处理
```

---

### **场景2：工具是安全可重放的 (ReplaySafe)**

```go
if pending.ReplayPolicy == string(llm.ReplaySafe) {
    action.Kind = RecoveryRetryTool
    action.Automatic = true
    action.Reason = "工具声明安全重放"
    action.Decisions = [RecoveryRetry, RecoveryMarkFailed, RecoveryAbandonRun]
}
```

**例子：**
```
工具: read_file (读取文件)
特性: 无副作用，纯读取操作
ReplayPolicy: ReplaySafe

→ 恢复策略: RetryTool (可安全重试)
→ 可自动处理
→ 用户也可选择: 标记失败、放弃运行
```

**ReplaySafe 工具举例：**
- ✅ 读取文件
- ✅ 查询数据库
- ✅ 调用只读 API
- ✅ 代码分析

---

### **场景3：工具是幂等的 (ReplayIdempotent + IdempotencyKey)**

```go
if pending.ReplayPolicy == string(llm.ReplayIdempotent) && 
   pending.IdempotencyKey != "" {
    action.Kind = RecoveryRetryTool
    action.Automatic = true
    action.Reason = "原始幂等性键可用"
    action.Decisions = [RecoveryRetry, RecoveryMarkFailed, RecoveryAbandonRun]
}
```

**例子：**
```
工具: create_pull_request
特性: 有副作用但幂等 (同 key 返回相同结果)
IdempotencyKey: "pr-issue-42-fix-20260824"

重执行时：
  服务器检查 key
  发现已执行过
  直接返回之前的结果
  ↓
  不会创建重复的 PR ✅

→ 恢复策略: RetryTool (幂等重试)
→ 可自动处理
```

**ReplayIdempotent 工具举例：**
- ✅ 创建资源 (带 Key)
- ✅ 更新资源 (幂等操作)
- ✅ 发送通知 (带 MessageID)

---

### **场景4：工具可能有不可逆副作用 (其他情况)**

```go
default:
    action.Kind = RecoveryDecideTool  // 需要用户决策
    action.Automatic = false          // 不可自动处理
    action.Reason = "工具执行可能产生外部副作用"
    action.Decisions = [
        RecoveryConfirmExecuted,      // 用户确认已执行
        RecoveryMarkFailed,           // 标记为失败
        RecoveryRetry,                // 尝试重试 (用户自行决定风险)
        RecoveryAbandonRun            // 放弃整个运行
    ]
}
```

**例子：**
```
工具: delete_branch / send_email / deploy_to_prod
特性: 有不可逆副作用，没有幂等 key
ReplayPolicy: ReplayNever / ReplayDangerous

系统无法确定：
  ❓ 工具是否已成功执行?
  ❓ 重试是否会造成重复操作?

→ 恢复策略: DecideTool (等待用户决策)
→ 必须用户手动介入
→ 用户提供 4 个选项供选择
```

---

## ⚙️ 第三步：应用恢复决策 (recoveryDecision)

根据执行模式和用户选择，应用最终的恢复策略：

### **自动恢复模式 (automatic = true)**

```go
if automatic {
    // 检查该行动是否允许自动执行
    if !action.Automatic {
        return error("不允许自动执行")
    }
    
    // 自动选择决策
    switch action.Kind {
    case RecoveryReconcileTool:
        return RecoveryRetry  // 和解 → 重试
        
    case RecoveryRetryTool:
        return RecoveryRetry  // 重试 → 重试
        
    default:
        return error("不支持此类型自动化")
    }
}
```

**用途：** 系统启动时，自动恢复所有安全的操作（无需用户干预）

---

### **用户交互模式 (automatic = false)**

```go
// 如果用户没有指定决策且是 ReconcileTool，默认重试
if requested == "" && action.Kind == RecoveryReconcileTool {
    return RecoveryRetry
}

// 检查用户的选择是否在允许列表中
for _, allowed := range action.Decisions {
    if requested == allowed {
        return requested  ✅ 允许
    }
}
return error("不允许该决策")  ❌ 拒绝
```

**用途：** 用户通过 UI 或 API 选择恢复策略

---

## 📊 决策树

```
发现未完成的工具
        ↓
╔═══════════════════════════════════╗
║ 检查: 结果是否已持久化?            ║
╚═══════════════════════════════════╝
        ↓         ↓
       YES       NO
        ↓         ↓
   ReconcileTool  继续检查
   (自动)          ↓
              ╔═════════════════════╗
              ║ ReplayPolicy 是什么? ║
              ╚═════════════════════╝
                  ↓        ↓        ↓
               Safe   Idempotent   Other
                ↓        ↓         ↓
             RetryTool RetryTool  DecideTool
            (自动)   (自动)    (需用户)
                ↓        ↓         ↓
         可选: Retry  可选: Retry  必须选:
         MarkFailed MarkFailed  ConfirmExecuted
         AbandonRun AbandonRun   MarkFailed
                                Retry
                                AbandonRun
```

---

## 🔄 恢复策略执行 (recoverPendingTool)

一旦决策确定，就执行对应的恢复策略：

### **RecoveryRetry - 重新执行工具**

```go
case agentsession.RecoveryRetry:
    // 1. 查找工具
    executable := request.Tools.Lookup(pending.ToolName)
    
    // 2. 验证 ReplayPolicy 未变更
    if executable.ReplayPolicy() != pending.ReplayPolicy {
        return error("replay policy changed!")
    }
    
    // 3. 重新执行
    result = request.Tools.Execute(ctx, tool.Call{
        ID: pending.ToolCallID,
        Name: pending.ToolName,
        Arguments: pending.EffectiveArgs,      // 原始参数
        IdempotencyKey: pending.IdempotencyKey, // 幂等性密钥
    })
    
    // 4. 如果中断，返回中断信息
    if result.Status == tool.ResultInterrupted {
        return result.Interrupt
    }
```

---

### **RecoveryConfirmExecuted - 用户确认已执行**

```go
case agentsession.RecoveryConfirmExecuted:
    // 用户说: "我确认工具已经成功执行了"
    result = tool.Result{
        Status: tool.ResultCompleted,
        Content: []llm.Content{{
            Type: llm.ContentText,
            Text: "用户确认该工具在重启前已完成执行"
        }}
    }
```

---

### **RecoveryMarkFailed - 标记为失败**

```go
case agentsession.RecoveryMarkFailed:
    // 标记工具执行失败
    result = tool.Result{
        Status: tool.ResultFailed,
        Content: []llm.Content{{
            Type: llm.ContentText,
            Text: "工具执行在崩溃恢复中被标记为失败"
        }}
    }
```

---

### **RecoveryAbandonRun - 放弃整个运行**

```go
// 中止所有未完成的工具
for _, pending := range state.PendingTools {
    if pending.RunID != request.RunID {
        continue
    }
    
    if pending.ResultEntryPresent {
        // 结果已存，补记录
        r.persistRecoveredToolFinish(...)
    } else {
        // 结果不存，标记为已取消
        r.persistToolResult(..., 
            tool.ResultCancelled,
            "运行被放弃")
    }
}

return RunResult{
    Status: RunAborted,
    Reason: "abandoned_during_recovery"
}
```

---

## 📈 完整流程示例

### **场景：Tool #3 (生成 PR) 在 ReplayDangerous 状态崩溃**

```
时间线：
─────────────────────────────────────
1. 初始状态
   ├─ Tool #1: ✅ 完成
   ├─ Tool #2: ✅ 完成
   └─ Tool #3: ⏳ 未完成 (ReplayPolicy=Dangerous)
       └─ IdempotencyKey: "" (无幂等性)

2. AnalyzeRecovery()
   └─ PendingTools = [Tool #3]

3. BuildRecoveryPlan()
   └─ 检查 Tool #3
      ├─ ResultEntryPresent? NO
      ├─ ReplayPolicy=Dangerous? YES
      ├─ IdempotencyKey? ""
      └─ 决定: RecoveryDecideTool (需用户决策)

4. 用户收到提示：
   ┌─────────────────────────────────┐
   │ Tool: create_pull_request       │
   │ 状态: 未完成                     │
   │ 原因: 系统崩溃                   │
   │                                 │
   │ 请选择恢复策略:                  │
   │ A) 确认已执行 (confirm)          │
   │ B) 标记失败   (mark_failed)     │
   │ C) 重试执行   (retry)            │ ← 高风险
   │ D) 放弃运行   (abandon)          │
   └─────────────────────────────────┘

5. 用户选择: A (确认已执行)
   └─ recoveryDecision() = RecoveryConfirmExecuted

6. recoverPendingTool()
   └─ result = {
        Status: Completed,
        Content: "用户确认工具已执行"
      }

7. 继续执行
   └─ Tool #4, #5 继续正常流程
```

---

## 🎓 总结：策略选择的原则

| 条件 | 恢复策略 | 自动 | 说明 |
|------|--------|------|------|
| 结果已存 | ReconcileTool | ✅ | 只补记录，无风险 |
| ReplaySafe | RecoveryRetry | ✅ | 无副作用，安全重执行 |
| Idempotent + Key | RecoveryRetry | ✅ | 幂等保证，重执行安全 |
| Idempotent 但无 Key | DecideTool | ❌ | 无法保证幂等，需用户 |
| Dangerous/Never | DecideTool | ❌ | 可能有副作用，需用户 |

**核心原则：**
1. **安全优先** - 自动处理所有安全的恢复
2. **幂等保证** - 使用 IdempotencyKey 防止副作用
3. **用户决策** - 高风险操作由用户选择
4. **数据一致** - 通过持久化和幂等保证最终一致性
