# 恢复后 Agent Loop 的流程详解

## 🎯 核心问题

**场景：** 
- Tool #3 开始执行
- 后续步骤不确定（需要 Tool #3 完成后 Agent 决定）
- 系统崩溃
- 恢复后执行完 Tool #3，**然后呢？**

**答案：继续进入 Agent Loop！**

---

## 📊 完整流程图

```
User Request
    ↓
Run() → runSteps(step=1)  (Agent Loop)
    │
    ├─ Step 1: LLM 调用
    │  └─ 返回: Tool #1 Call
    │
    ├─ 执行 Tool #1 ✅
    │
    ├─ Step 2: LLM 调用
    │  └─ 返回: Tool #2 Call
    │
    ├─ 执行 Tool #2 ✅
    │
    ├─ Step 3: LLM 调用
    │  └─ 返回: Tool #3 Call
    │
    ├─ 执行 Tool #3 ⏳
    │
    ❌ CRASH! 系统崩溃
    │
    ━━━━━━━━━━━━━━━━━━━━━━━━━━
    
    恢复过程 (Recover)
    │
    ├─ 加载快照
    ├─ 检查待恢复工具: Tool #3
    ├─ 构建恢复计划
    ├─ 用户/系统选择恢复策略 (Retry)
    │
    ├─ 执行恢复: Tool #3 重新执行 ✅
    │
    ├─ ⚡ 关键: 调用 continueAfterAssistant()
    │
    ├─ Step 3 (续): 执行 Tool #3 的其他调用（如果有）
    │
    ├─ Step 3 完成后... ✨
    │
    ├─ runSteps(step=4)  ← 继续 Agent Loop!
    │
    ├─ Step 4: LLM 调用  ← 再次询问 LLM
    │  ├─ 上下文包含: Tool #1,#2,#3 的结果
    │  ├─ LLM 决定: 继续执行 Tool #4? 或结束?
    │  └─ 返回: Tool #4 Call (或没有 calls → 结束)
    │
    ├─ [如果有 Tool #4]
    │  └─ 执行 Tool #4
    │     └─ 继续循环...
    │
    └─ [如果没有 Tool]
       └─ RunCompleted ✅
```

---

## 🔍 代码流程详解

### **第一步：执行恢复的工具**

```go
func (r *Runtime) Recover(ctx context.Context, recover RecoverRequest, sink EventSink) (RunResult, error) {
    // ... 加载快照，验证恢复计划 ...
    
    if action.Tool != nil {
        // 执行恢复工具 (Tool #3)
        interrupt, err := r.recoverPendingTool(
            runCtx, request, dispatcher, snapshot, 
            *action.Tool, action.Kind, decision, recover.Automatic)
        
        if !recover.ContinueRun {
            // ❌ 停止，返回检查点
            return RunResult{
                Status: RunInterrupted, 
                Reason: "recovery_checkpoint"
            }, nil
        }
    }
    
    // ✅ 继续恢复后的运行
    return r.continueRecoveredRun(runCtx, request, dispatcher)
}
```

**关键：** `recover.ContinueRun` 标志决定是否继续

---

### **第二步：进入 continueRecoveredRun()**

这个函数会计算应该从哪个 step 继续执行：

```go
func (r *Runtime) continueRecoveredRun(ctx context.Context, request RunRequest, 
    dispatcher *eventDispatcher) (RunResult, error) {
    
    snapshot, err := r.sessions.Load(ctx, request.SessionID)
    
    // 查找最后完成的步骤
    latestStartedAttempt := 0      // 最后启动的步骤
    latestFinishedAttempt := 0     // 最后完成的步骤
    var latestFinishedEntry agentsession.EntryID
    
    for _, record := range snapshot.Records {
        if record.RunID != request.RunID || record.Step == nil {
            continue
        }
        switch record.Type {
        case agentsession.RecordStepStarted:
            // 更新最后启动的步骤
            if record.Sequence >= latestStartedSequence {
                latestStartedAttempt = record.Step.Attempt
                latestStartedSequence = record.Sequence
            }
        case agentsession.RecordStepFinished:
            // 更新最后完成的步骤
            if record.Step.Attempt >= latestFinishedAttempt {
                latestFinishedAttempt = record.Step.Attempt
                latestFinishedEntry = record.Step.AssistantEntryID
            }
        }
    }
    
    // 情况1: 有未完成的步骤
    if latestStartedAttempt > latestFinishedAttempt {
        if assistantEntry, found := unfinishedAssistantEntry(snapshot, ...); found {
            // 补充完成记录
            ...
            // 继续该步骤的工具执行
            return r.continueAfterAssistant(
                ctx, request, dispatcher, snapshot, 
                assistantEntry.ID, latestStartedAttempt)
        }
        model, err := r.models.CreateModel(ctx, request.Model)
        // 或者从该步骤重新开始
        return r.runSteps(ctx, request, dispatcher, model, latestStartedAttempt)
    }
    
    // 情况2: 最后完成的步骤 > 0
    if latestFinishedAttempt > 0 {
        return r.continueAfterAssistant(
            ctx, request, dispatcher, snapshot, 
            latestFinishedEntry, latestFinishedAttempt)
    }
    
    // 情况3: 从头开始
    model, err := r.models.CreateModel(ctx, request.Model)
    return r.runSteps(ctx, request, dispatcher, model, 1)
}
```

---

### **第三步：执行 continueAfterAssistant()**

这是恢复后继续执行的关键函数：

```go
func (r *Runtime) continueAfterAssistant(ctx context.Context, request RunRequest, 
    dispatcher *eventDispatcher, snapshot agentsession.Snapshot, 
    assistantEntryID agentsession.EntryID, step int) (RunResult, error) {
    
    // 加载 Step N 的 Assistant 消息 (含工具调用列表)
    assistant, err := assistantMessage(snapshot, assistantEntryID)
    
    // 获取该步骤中的所有工具调用
    calls := assistant.ToolCalls()
    
    // 例如，Step 3 中 LLM 调用了: [Tool #3]
    
    // 如果没有工具调用，运行已完成
    if len(calls) == 0 {
        if err := r.finishRun(ctx, request, dispatcher, step, 
            RunCompleted, "recovered_model_completed"); err != nil {
            return RunResult{}, err
        }
        message := assistant.Clone()
        return RunResult{
            RunID: request.RunID,
            Status: RunCompleted,
            FinalMessage: &message,
            Steps: step,
            Reason: "recovered_model_completed"
        }, nil
    }
    
    // 获取已完成/未完成的工具事实
    started, finished := runToolFacts(snapshot, request.RunID)
    
    // 执行所有未完成的工具
    for index, call := range calls {
        if finished[call.ID] {
            continue  // 已完成，跳过
        }
        if started[call.ID] {
            // 已启动但未完成，需要另外的恢复动作
            return RunResult{}, fmt.Errorf(
                "Tool %q: another recovery action is required", call.Name)
        }
        
        // 执行该工具
        _, interrupted, err := r.executeTool(
            ctx, request, dispatcher, assistantEntryID, index, call)
        
        if err != nil {
            return r.failRun(ctx, request, dispatcher, step, 
                "execute_tool_during_recovery", err)
        }
        if interrupted != nil {
            return RunResult{
                Status: RunInterrupted,
                Reason: "tool_interrupted",
                Interrupt: interrupted
            }, nil
        }
    }
    
    // ✅ 所有工具执行完成
    // 检查是否达到步数限制
    if step >= request.Limits.MaxSteps {
        if err := r.finishRun(ctx, request, dispatcher, step, 
            RunLimitReached, "max_steps"); err != nil {
            return RunResult{}, err
        }
        return RunResult{
            Status: RunLimitReached,
            Steps: step,
            Reason: "max_steps"
        }, nil
    }
    
    // ✨ 关键: 继续 Agent Loop!
    // 创建新的模型实例
    model, err := r.models.CreateModel(ctx, request.Model)
    if err != nil {
        return r.failRun(ctx, request, dispatcher, step, 
            "create_model_after_recovery", err)
    }
    
    // 进入 Agent Loop 的下一个步骤
    // 这会调用 LLM，LLM 看到所有之前的对话和工具结果
    return r.runSteps(ctx, request, dispatcher, model, step+1)
}
```

---

## 📌 关键点详解

### **1. 工具恢复后会继续进入 Agent Loop**

```
Tool #3 恢复执行 ✅
    ↓
执行 Step 3 中的其他工具 (如果有)
    ↓
Step 3 完成 ✅
    ↓
runSteps(step=4)  ← 进入新的 Agent Loop!
    ↓
LLM 再次调用 (包含完整上下文)
```

### **2. LLM 能看到什么**

在 Step 4，LLM 的上下文包括：

```
=== Agent 对话历史 ===

用户消息: "请完成这个任务"

[Step 1]
Assistant: 我会执行 Tool #1 来获取数据
Tool #1 Result: (返回结果)

[Step 2]
Assistant: 现在执行 Tool #2 来分析
Tool #2 Result: (返回结果)

[Step 3]
Assistant: 现在执行 Tool #3 来生成修复
Tool #3 Result: (返回结果)  ← 这是恢复后得到的!

[Step 4] ← 新的步骤
Assistant: 我看到 Tool #3 已完成...
           现在我需要:
           - 继续执行 Tool #4?
           - 还是结束?
           ← LLM 自己决定
```

### **3. LLM 做出新的决策**

基于前面的上下文，LLM 会：

```
a) 返回新的工具调用
   → Tool #4 Call
   → 进入 Step 4 工具执行循环
   → 完成后继续 Step 5...

b) 返回没有工具调用
   → StopReason = "end_turn"
   → 运行结束 (RunCompleted)
```

---

## 🔄 完整的恢复+Agent Loop 过程

### **场景：Tool #3 因系统崩溃中断**

```
初始状态:
  Step 1: ✅ Tool #1 完成
  Step 2: ✅ Tool #2 完成
  Step 3: ⏳ Tool #3 执行中... 系统崩溃 ❌

恢复流程:
  1. 检测: Step 3 有未完成的工具
  2. 恢复: Tool #3 重新执行 ✅
  3. 继续: Step 3 的其他工具 (如果有)
  4. 完成: Step 3 工具全部完成 ✅

Agent Loop 继续:
  5. 进入 Step 4 (新的 Agent Loop)
  6. LLM 调用: 给 LLM 前面 Step 1-3 的完整对话
  7. LLM 响应: 
     - 如果需要 Tool #4 → 执行 Tool #4
     - 如果不需要   → 返回最终答案 (结束)

用户视角:
  ✅ 从恢复点继续执行 (节省时间)
  ✅ 数据一致性 (只有一份 Tool #3 结果)
  ✅ Agent 逻辑不变 (对 LLM 透明)
```

---

## 💡 为什么要继续进入 Agent Loop?

### **原因 1: 后续步骤不确定性**

```
Tool #3 是什么?
  ├─ 如果是: 生成代码
  │  └─ 后续可能: 创建 PR (Tool #4)
  │     或者: 直接返回代码
  │     取决于 LLM 的判断
  │
  ├─ 如果是: 查询数据库
  │  └─ 后续可能: 生成报告 (Tool #5)
  │     或者: 等待用户反馈
  │
  └─ LLM 需要看到 Tool #3 的结果后再决定!

系统无法预先知道!
  ↓
必须让 LLM 继续执行
```

### **原因 2: 保持 Agent 逻辑一致**

```
正常执行:          vs    恢复后执行:
┌──────────────┐        ┌──────────────┐
│ Tool #1      │ ✅      │ Tool #1 ✅   │
│ Tool #2      │ ✅      │ Tool #2 ✅   │
│ Tool #3      │ ✅      │ Tool #3 ✅   │
│              │         │              │
│ LLM 决策     │ ← ✨    │ LLM 决策  ← ✨│
│ Tool #4?     │         │ Tool #4?     │
│              │         │              │
│ ...继续...   │         │ ...继续...   │
└──────────────┘        └──────────────┘

两个流程完全相同！
恢复是透明的，对 Agent 逻辑没有影响
```

---

## 🎯 总结

| 方面 | 说明 |
|------|------|
| **Tool #3 恢复后** | 执行完成 ✅ |
| **然后进入什么** | Agent Loop (Step N+1) |
| **谁决定后续步骤** | LLM (基于前面的完整上下文) |
| **如何决定** | LLM 看到 Tool #3 结果，自行判断 |
| **对用户影响** | 恢复对 Agent 逻辑透明 |
| **代码体现** | `runSteps(ctx, ..., model, step+1)` |

**核心原则：** 恢复只是跳过了已完成的步骤，之后的所有逻辑与正常执行完全一样。恢复的目的是**提高效率**，而不是改变 Agent 的行为。
