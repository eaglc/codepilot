# CodePilot 会话与上下文：工作流程、存储与恢复

> 版本：v1.0　·　日期：2026-08-24　·　依据：当前 `internal/` 源码（非 `_legacy`）
> 关键文件：`internal/codingagent/service.go`、`internal/agent/runtime.go`、`internal/agent/session/{types,recovery}.go`、`internal/contextmanager/{manager,compaction}.go`、`internal/sessionstore/file/repository.go`、`internal/codingstore/file`

本文用「一个具体例子」贯穿，讲清楚三件事：**会话怎么建、上下文怎么管、数据何时以何种格式落盘，以及重启后如何恢复**。

---

## 1. 三层数据模型（先建立心智模型）

CodePilot 把「会话」拆成三层，各归各的包、各存各的文件：

| 层 | 类型 | 归属包 | 存什么 | 存在哪 |
|---|---|---|---|---|
| **产品会话** | `codingagent.Session` | `codingagent` | Workspace/Worktree 绑定、Provider/Model、权限模式、敏感路径、授权记录 | `<state>/coding-sessions/<id>.json` |
| **通用会话** | `agentsession.Snapshot`（Metadata + Entries + Records + Lanes） | `agent/session` | 对话树（Entry）与执行日志（Record），只认识消息/工具/运行，不认识 Git/路径 | `<state>/agent-sessions/<id>/{session.json,journal.jsonl}` |
| **模型消息** | `llm.Message` | `llm` | user/assistant/tool 的标准化消息，含 tool call 与 tool result | 内嵌在 Entry 里，随 journal 落盘 |

关键点：**产品会话与通用会话是一对一的绑定**（`Session.AgentSessionID` 指向一个 `agentsession.ID`）。通用层永远不出现 `WorktreeID`、`PermissionMode`、绝对路径这类产品概念；产品层把通用能力组合成「Coding Agent」。

ID 统一用 `前缀_随机hex` 生成（`crypto/rand` 16 字节）：`coding_…`、`agent_…`、`run_…`、`entry_…`、`record_…`、`event_…`。

---

## 2. 目录与存储格式

`app.New` 会先拿到 **ConfigRoot** 与 **StateRoot** 两个根（可用 `--config-dir/--state-dir` 覆盖），再持有一个**跨进程独占文件锁**（`flock`）后才打开各仓库。落盘布局如下（详见 `docs/architecture/modular-architecture-migration.md` §17.3）：

```text
<config-root>/
└── provider-profiles.json              # 非敏感 Provider profile；不含 API Key

<state-root>/
├── coding-artifacts/<sha256>.blob      # 内容寻址的大 Tool 输出（>64KiB 时外置）
├── agent-archives/<agent-id>/<sha256>.tar.gz   # session.json+journal.jsonl 的不可变冷副本
├── agent-sessions/<agent-id>/
│   ├── session.json                    # version + 通用 Metadata
│   └── journal.jsonl                   # Entry/Record 交错、共享 sequence、append-only
├── context-summaries/<sha256>.json     # provider-neutral 的滚动摘要缓存
├── coding-sessions/<coding-id>.json    # Coding 绑定 + 权限 + secret-free Grant 审计
├── coding-workspaces/<workspace-id>.json
└── coding-worktrees/<worktree-id>.json
```

### 2.1 `session.json`（通用会话元数据）

```json
{ "version": 1,
  "metadata": { "id": "agent_…", "created_at": "…", "updated_at": "…", "archived": false } }
```

### 2.2 `journal.jsonl`（真正的「对话 + 执行日志」）

这是最核心的文件。**Entry 与 Record 交错写入同一份 append-only JSONL，共享同一个单调递增的 `sequence`**。每一行要么是一个 Entry（进入对话树、参与模型上下文），要么是一个 Record（执行事实、供审计与恢复用，不直接进上下文）：

```jsonl
{"version":1,"sequence":1,"record":{"id":"record_…","type":"operation_started","run_id":"run_…","operation":{"intent":"run"}}}
{"version":1,"sequence":2,"entry":{"id":"entry_…","type":"message","run_id":"run_…","message":{"role":"user","content":[{"type":"text","text":"修复登录 bug"}]}}}
```

两个不变量，加载时被强制校验（`buildSnapshot`）：
1. `sequence` 从 1 开始**连续无空洞**；任何跳跃/重复直接拒绝加载。
2. Entry 通过 `parent_id` 串成树；`lane` 的叶子指针必须与父链一致，否则视为损坏。

### 2.3 `coding-sessions/<id>.json`（产品会话）

```json
{ "version":1,
  "session": { "id":"coding_…", "agent_session_id":"agent_…",
    "workspace_id":"…", "worktree_id":"…",
    "provider_profile_id":"…", "model_id":"…",
    "permission_mode":"ask", "active_lane":"main",
    "sensitive_paths":["…"], "permission_grants":[ … ] } }
```

注意：`permission_grants` 只存**稳定的授权事实**（tool/action/精确相对路径/来源/创建·过期·撤销时间），**绝不存** patch 内容、命令输出、Tool 参数或 Credential。

---

## 3. 会话创建：两阶段意图（防「半截创建」）

`Service.CreateSession` 不能简单地「写两个文件」，因为进程可能在写了一半时崩溃。它用了一个 **durable intent** 两阶段协议：

```text
1. 校验 worktree 存在、规范化 sensitive paths、默认 permission=ask、lane=main
2. 生成 coding_id + agent_id
3. BeginSessionCreation  →  写 SessionCreationIntent{ status: pending }
4. reconcileSessionCreation:
   a. 写 agent-sessions/<agent_id>/session.json      (通用元数据)
   b. 写 coding-sessions/<coding_id>.json           (产品绑定)
   c. 把 intent 标记为 completed
5. 返回 Session；内存状态置为 Idle
```

重启后，`ConsistencyManager.Diagnose/Repair`（`codepilot doctor` / `repair`）会扫描 intent：
- intent 还是 `pending` → 补齐缺失的一侧（例如只写了通用侧就崩了，则补产品侧）。
- 无法安全补齐的 orphan/dangling 绑定 → 只做**可逆归档**，不删任何 journal。

这就是「数据所有权唯一 + 崩溃可重建」的落地方式。

---

## 4. 一次 turn 的完整流程（贯穿例子）

例子：用户在 UI 输入「修复登录 bug」，模型先 `read_file` 看代码，再 `apply_patch` 改代码（apply_patch 需要审批）。

### 4.1 产品层入口 `StartTurn`（`codingagent/service.go`）

```text
1. operationLock(sessionID)          —— 每会话一把互斥锁，串行化 turn
2. LoadSession + Load AgentSession   —— 读产品 + 读通用
3. AnalyzeRecovery(durable)
      ├─ 有 PendingRuns/PendingInterrupts/PendingTools → 直接报错「必须先恢复」
      └─ 干净 → 继续
4. LoadWorktree → 构造 ToolScope（权限模式、授权快照、敏感路径）
5. CreateTools(scope)               —— 生成本轮可用的工具集（只读工具 + apply_patch + checks + LSP）
6. buildPromptContext               —— systemPrompt + untrustedContext（AGENTS.md）
7. NewAgentEventAdapter             —— 把通用 Event 转成产品 Event
8. setState(Running); beginActiveTurn（可取消）
9. deps.Agent.Run(...)              —— 进入通用运行时（下面 §4.2）
10. setState(Idle / AwaitingApproval)
11. touchSession                    —— 只更新产品会话 UpdatedAt
12. 返回 TurnResult
```

### 4.2 通用运行时 `Agent.Run`（`agent/runtime.go`）—— 存储时机就在这里

`Run` 的写盘顺序是**「先写事实、后做副作用」**，这是恢复机制成立的前提。一次 run 会依次追加：

```text
① record operation_started          (run 开始标记)
② entry  user message               (用户消息进对话树)
③ (step 循环，最多 MaxSteps=32 步)
   ├─ record step_started           (attempt=N)
   ├─ buildContext → contextmanager.Process → 可能生成/复用 summary
   │     └─ persistSummaries        (若有新 summary → 追加 entry compaction)
   ├─ 流式请求模型 → 逐字发布 text_delta / thinking 状态
   ├─ entry  assistant message      (含 tool_call)
   ├─ record usage                  (token/cost)
   ├─ record step_finished
   ├─ 若无 tool call → finishRun(completed) 结束
   └─ 对每个 tool call：
        ├─ record tool_started      ★ 副作用之前落盘
        ├─ 发布 tool_started 事件
        ├─ 执行工具（真实副作用）
        ├─ 若 ResultInterrupted（要审批）→ record interrupt_requested → 返回 RunInterrupted
        └─ 否则 entry tool result + record tool_finished
④ record operation_finished          (用 context.WithoutCancel 保证取消时也落盘)
```

对应我们的例子，`journal.jsonl` 会是这样（sequence 已标注）：

| seq | 类型 | 内容 | 说明 |
|---|---|---|---|
| 1 | record `operation_started` | intent=run | run 开始 |
| 2 | entry message | user「修复登录 bug」 | 用户消息 |
| 3 | record `step_started` | attempt=1 | 第 1 步 |
| 4 | entry message | assistant（含 `read_file` tool_call） | 模型要读文件 |
| 5 | record `usage` | tokens | |
| 6 | record `step_finished` | assistant_entry=seq4 | |
| 7 | record `tool_started` | `read_file` | ★ 副作用前 |
| 8 | entry message | role=tool（read_file 结果） | 工具结果 |
| 9 | record `tool_finished` | completed | |
| 10 | record `step_started` | attempt=2 | 第 2 步 |
| 11 | entry message | assistant（含 `apply_patch` tool_call） | |
| 12 | record `usage` | | |
| 13 | record `step_finished` | | |
| 14 | record `tool_started` | `apply_patch` | ★ 副作用前 |
| 15 | record `interrupt_requested` | approval | ★ 要审批，工具**未执行**，也没有 tool result |

到这里 `Run` 返回 `RunInterrupted`，产品层把状态置为 `AwaitingApproval`。注意：**seq 14/15 已经落盘，但既没有 tool result entry，也没有 `tool_finished`**——这正是恢复机制要识别的「半截」状态。

### 4.3 审批后 `ResumeTurn`（继续同一个 run）

用户按 `y`（allow once）。产品层把「批准」转成一个 `tool.Result`（`productResolution`），交给 `Agent.Resume`：

```text
16. record interrupt_resolved        (decision=approved)
17. entry tool result                (apply_patch 的真实结果)
18. record tool_finished             (completed)
19. record step_started (attempt=3)
20. entry assistant message          (模型最后回复「已修复」)
21. record usage
22. record step_finished
23. record operation_finished        (outcome=completed)
```

关键：`Resume` **不新增用户消息、不新开 operation**，而是从 seq 14 的 `tool_started` 与 seq 15 的 `interrupt_requested` 中取回原始参数与 payload，把「被审批中断的工具」补完，然后继续后续 step。

### 4.4 快照 `Snapshot`（UI 的权威状态）

UI 不直接读 journal，而是通过 `Service.Snapshot` 拿到**投影后的产品视图**：

```text
Snapshot = ProjectSnapshot(product, durable, lane, runtimeState, revision)
         ├─ Transcript   —— 按对话树顺序投影成 user/assistant/tool 文本、折叠的工具活动、内联 diff
         ├─ PendingInterrupts —— 当前待审批项（白名单字段，不含原始 payload）
         ├─ RecoveryActions   —— 当前可执行的恢复动作
         ├─ Metrics     —— Step/Context tokens/成本/耗时（只来自 durable usage/operation/step 事实）
         └─ Revision    —— 最后一个 Log sequence（UI 用它检测事件跳号，跳号就重读快照）
```

事件（Event）只是**增量提示、可丢失**；Snapshot 才是权威。`Event.Sequence` 只在一次 adapter 生命周期内有序，不能作为跨重启游标。

---

## 5. 上下文如何管理（`contextmanager`）

### 5.1 每步构建一次（`buildContext`）

每个 model step 之前，`Runtime.buildContext` 会：

1. `Load` 快照 → `BranchEntries(lane)` 沿当前分支回溯，取出全部 `EntryMessage`。
2. 最后一条标记为 `Current`（本轮用户消息，必须原样保留）。
3. 若本轮有 `UntrustedContext`（AGENTS.md 项目指引），插入到 Current 之前，作为 **user-role 的低优先级数据**。
4. 若能拿到模型能力（`ModelCatalog.DescribeModel`），用 `BudgetForModel` 把「context window − max output − 5% 安全边距」换算成输入预算；摘要水位 = 预算的 80%。
5. 交给 `contextmanager.Process`（策略管线）。

### 5.2 滚动摘要 `CompactionStrategy`（`rolling-summary/v4`）

```text
如果 总 token ≤ SummarizeThreshold   → 原样返回（不压缩）
否则：
  把历史按 TurnID 分组（groupTurns）
  保留最近 RecentTurns 个完整 turn 作为「尾巴」（tail）
  更早的 turn 归为 old：
    digest = sourceDigest(old)                       ← 摘要缓存键的一部分
    key = hash(sessionID, digest, "rolling-summary", "v4")
    若缓存命中 → 复用（即使主模型已切换，因为键不含模型）
    否则 → 调用 Summarizer（主模型或独立摘要模型）生成摘要
            └─ 摘要先过 sanitizer 脱敏，再校验「事实一致性」（ValidateSummaryFacts）
               └─ 失败 → 降级为 safeTrim（安全裁剪最老 turn，不写摘要）
    生成 summary message（Role=user，标记为「不可信派生上下文」）
  最终上下文 = [summary message] + [tail] + [current]
  再走 fitHardLimit 兜底硬上限
```

几个要点：

- **摘要绝不写进 system prompt**。它以 `role=user` 的「不可信派生上下文」身份加入历史，system prompt 保持不变——这是防 prompt injection 的关键（仓库内容/摘要不能改变工具、权限、模型或策略）。
- **摘要缓存是 provider-neutral 的**：键由 `session + source digest + strategy + version` 组成，不含当前模型，所以从模型 A 切到 B 时同一段历史复用同一份摘要；`Summary.Model` 只记录「当时由哪个模型生成」供审计。
- 摘要生成后**双写**：`SummaryStore`（`context-summaries/<sha256>.json`，做缓存）+ journal 里的 `entry compaction`（做 durable 权威）。缓存丢失不致命，下次重新摘要即可。
- **硬上限不拆散 tool call/result**：`fitHardLimit` 以完整 Turn 为单位丢弃最老的 turn；若当前 turn 本身就超限，返回 `CurrentTurnTooLargeError`，由既有 operation 失败路径投影到会话，而不是发一个孤立的 tool result。

### 5.3 工具结果外置（Artifact，另一层「上下文」约束）

超过 64 KiB 的纯文本工具结果（命令输出、大 diff）不会整块进 journal/模型上下文，而是：
- 完整 `{version, tool_name, call_id, result}` 存成 `<state>/coding-artifacts/<sha256>.blob`（最大 32 MiB）。
- 模型与 journal 只保留 ≤16 KiB 预览 + 类型化 detail/diff + 无路径的 artifact 引用。

---

## 6. 重启后的恢复（`agent/session` + `agent.Recover`）

恢复**不依赖最终消息**，而是从 durable 的 Entry/Record 重建。启动时 `app` 会跑一个**有界协调器** `RecoverAutomatically`，只处理「可自动」的动作，随后停在需要人工决策的边界。

### 6.1 `AnalyzeRecovery`：找出未完成的工作

逐条扫 Record，得到三类未完成状态：

| 状态 | 判定 | 含义 |
|---|---|---|
| `PendingRuns` | 有 `operation_started` 无 `operation_finished` | 一个 run 没跑完 |
| `PendingTools` | 有 `tool_started` 无 `tool_finished` | 工具副作用可能已经发生但没记账 |
| `PendingInterrupts` | 有 `interrupt_requested` 无 `interrupt_resolved` | 有未决审批 |

### 6.2 `BuildRecoveryPlan`：按 replay policy 给出下一条动作

对每个未完成 run **只生成一条**类型化动作（一次处理一个边界，处理完重建计划——这是「重启安全」的关键）：

```text
有未决中断 → resolve_interrupt（人工：批准/拒绝/取消/放弃）
否则有未完成工具：
   result 已落盘但 finish 缺失  → reconcile_tool（自动补齐 tool_finished，不重跑）
   ReplayPolicy == safe          → retry_tool（自动：工具声明「校验后可安全重放」）
   ReplayPolicy == idempotent 且 key 存在 → retry_tool（自动：用原幂等键重试）
   否则                          → decide_tool（人工：确认已执行/标失败/重试/放弃）
否则 → continue_run（有上下文：继续/放弃） 或  decide_run（用户消息都没落盘就崩了：标失败/放弃）
```

三种 replay policy（工具自己声明，见 `tool.ReplayPolicy`）：
- `never`：永不自动重放，必须用户确认（有副作用的写工具默认如此，如 apply_patch）。
- `safe`：校验前置条件后允许自动重放。
- `idempotent`：只有 journal 保存了原始幂等键时才自动重放。

### 6.3 一个崩溃场景还原

进程在 seq 14（`tool_started: apply_patch`）之后、seq 15 之前崩溃。重启后：

1. `AnalyzeRecovery` 发现 `PendingTools`（apply_patch 有 started 无 finished）。
2. `BuildRecoveryPlan` 看它的 `ReplayPolicy == never` → 生成 `decide_tool` 动作，决策集 = `{confirm_executed, mark_failed, retry, abandon_run}`。
3. UI 渲染「Crash recovery required • apply_patch」，用户按 `x` 确认已执行（或 `f` 标失败 / `r` 重试 / `a` 放弃整个 turn）。
4. `RecoverTurn` 把这个决策落成 durable record，然后继续或关闭该 run。

若崩溃点换成「tool result entry 已写（seq 8）但 `tool_finished`（seq 9）没写」，恢复器识别 `ResultEntryPresent=true`，只**补写 finish 记录，绝不重跑工具**。

---

## 7. 边界情况一览（按关注点）

### 7.1 崩溃一致性

| 场景 | 处理 |
|---|---|
| journal 尾部截断（最后一行不完整） | 加载时忽略并写入 `RecoveryWarning{truncated_journal_tail}`；下次 `appendJournalLine` 前 `prepareJournal` 补齐或截掉残缺行 |
| 追加后、touchMetadata 前崩溃 | journal 数据安全；仅 `updated_at` 可能滞后（数据安全、元数据滞后，可接受） |
| 会话创建任一步崩溃 | 由 `SessionCreationIntent` + `doctor/repair` 补齐或可逆归档 |
| 取消/超时 | `finishRun`/`failRun` 用 `context.WithoutCancel` 写 `operation_finished`，保证取消也留下终态 |

### 7.2 原子性与并发

- 所有 JSON 写走「临时文件 + `chmod 0600` + `Sync` + `Rename`」原子替换；journal 追加走 `O_APPEND` + `Sync`。
- 跨进程：`flock` 独占写锁，锁文件永不删除、也不当作所有权证明（异常退出靠 OS 释放）。
- 进程内：每会话一把 `sessionLock`，`Service` 另有每会话 `operationLock` 串行化 turn。
- 重复 ID：`AppendEntry/AppendRecord` 都会查重拒绝。

### 7.3 安全边界（脱敏时机）

`agent.DataPolicy`（产品注入的是 `codingagent.SecurityPolicy`）在**每次跨越边界前**统一脱敏：
- 工具参数：`SanitizeToolArguments` → 落盘的是 `effective_args`（已脱敏）；原始参数只在当次调用栈内存活。
- 工具结果：`SanitizeToolResult` → 写进 journal 的 tool result 已是脱敏版本。
- 模型文本 / 错误：`SanitizeText` / `safeError`。
- 敏感文件（`.env*`、私钥等）：`read_file` 命中则走 `coding_sensitive_read_approval_v1` 逐次审批，批准后仍脱敏、且**不能形成 session grant**。

关键后果：**journal、摘要输入、后续模型上下文读的是同一份已脱敏结构**；原始 secret 不会落到磁盘。

### 7.4 分支（fork）

`ForkLane` 在 MainLane 上追加一条 `record lane_forked`，指向历史某个 `from_entry_id` 作为新 lane 的根。之后 `buildContext` 走 `BranchEntries(新lane)`，从该分支点回溯，得到一个不同的历史前缀 → 天然支持「从某个历史消息重新续写」。摘要从所选前缀现场重算（因为 store 只存原始消息，不存压缩产物）。

### 7.5 会话授权（session grant）

「allow session」由 `ResumeTurn` 在批准时从 journal 的 Turn/Interrupt/ToolCall 与白名单 payload **派生**，不接受 UI 自报。Grant 是 append-only 产品审计，只含稳定 ID、精确 tool/action/worktree 相对路径、来源与创建/过期/撤销时间，默认 8h、上限 24h。`ToolFactory` 每次只拿到当前会话授权快照的 defensive copy，工具自身不写 Grant。

### 7.6 模型/Provider 切换

切换主模型只改 `coding-sessions` 里的 `model_id`（先预检再写，并发布 `session_updated`）。因为摘要缓存键不含模型，切换后同一段历史仍复用旧摘要，不会强制重算。

---

## 8. 一句话总结设计权衡

1. **「先写事实、后做副作用」**（tool_started 在副作用前落盘）是恢复正确性的根基。
2. **Entry（对话树）与 Record（执行日志）共享 sequence、同文件交错**：用最少的文件表达「什么进入了上下文 + 什么发生了」两套事实，加载时用连续性/父链/查重三重校验兜底。
3. **事件可丢、记录不可丢、快照是权威**：UI 永远以 Snapshot 为准，事件跳号就重读。
4. **原始消息 append-only、摘要/裁剪是瞬态投影**：store 不存压缩产物，因此可换模型重算摘要、可分支、可显示原文。
5. **通用层与产品层彻底分离**：通用 `agent`/`agent/session` 不认识 Git/路径/权限，产品事实只在 `codingagent` 与 `codingstore`。

---

## 附：术语速查

| 术语 | 含义 |
|---|---|
| Session（产品） | 一次绑定到 workspace/worktree/model/permission 的 Coding 会话 |
| Agent Session（通用） | 与之 1:1 的对话树 + 执行日志 |
| Turn / Run | 一次用户输入触发的完整运行（`RunID`） |
| Step | Turn 内的一次模型请求/响应（`attempt=N`） |
| Entry | 对话树节点：message / compaction / model_change 等 |
| Record | 执行事实：operation/step/tool/interrupt/usage/lane_fork 等 |
| Lane | 对话树的可变叶子指针，`main` 为主干，fork 生成分支 |
| Interrupt | 需要外部输入（审批）的 durable 边界 |
| Compaction / Summary | 把旧 turn 压成 provider-neutral 摘要并回注上下文 |
| ReplayPolicy | 崩溃恢复时的重放策略：never / safe / idempotent |
| Snapshot | 投影到 UI 的权威产品状态 |
