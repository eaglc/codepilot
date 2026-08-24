# 上下文管理模块详细设计

> 实施状态说明：本文是最初设计输入，其中 `summaryCache`、固定 byte policy 等片段已被后续持久化和模型预算实现取代。当前实现与验收结论见 `../architecture/modular-architecture-migration.md` 第 17.4 节；本文继续保留设计推导和早期方案对照。
>
> 文档版本：v0.1
> 状态：设计评审稿（待实现）
> 日期：2026-08-21
> 关联：归档的 `../archive/legacy-improvement-plan.md` DEP-5（`contextmanager` 包由 Nop 占位升级为真实实现）

---

## 1. 要解决的问题

一个 coding session 持续对话时，送往模型的上下文（系统提示 + 对话历史 + 工具结果）**单调增长**，而 LLM 的上下文窗口是**有限且随模型而异**的。当前代码对此只有一条「硬拒绝」：`internal/agent/eino_invoker.go` 的 `validateInvocationInput` 里 `maxInvocationHistorySize = 8MB`、`maxInvocationMessages = 2000`、`maxInvocationMessageSize = 1MB`——一旦超过，直接返回错误 `conversation exceeds its total size limit`，**turn 失败**，没有优雅降级。

目标：在触到这根硬线**之前**，用预算驱动的分级策略主动压缩上下文，使：

1. 长会话不因上下文溢出而中断；
2. 压缩尽量保真（优先摘要，其次丢弃最老内容）；
3. 压缩是**瞬态视图**，绝不污染持久化的原始会话；
4. 未来做**消息分支**时不被破坏。

## 2. 现状与既有接缝

以下接缝**已经存在**，本设计是「接线 + 填充」，而非新开模块：

| 接缝 | 位置 | 现状 |
| --- | --- | --- |
| 上下文变换入口 | `internal/contextmanager` | 只有 `NopStrategy`，`Strategy`/`Manager` 接口齐备 |
| 调用时机 | `internal/agent/coding_agent.go:72` | 每 turn 送 LLM 前调用一次 `a.contexts.Process(...)` |
| 装配点 | `internal/app/build.go:146` | `contextmanager.NewManager(contextmanager.NopStrategy{})` |
| 模型创建 | `internal/provider/service.go:232` | `Service.NewChatModel(ctx, ModelRef)` 返回 Eino `ToolCallingChatModel` |
| 工具结果限界 | `internal/agent/tool_adapter.go:58-77` | `jsonResult`/`truncateToolText` 按 `ToolResultMaxBytes` 截断 |
| 工具分页参数 | `tool_read_file.go`(`start_line`/`line_count`)、`tool_search_code.go`/`tool_list_files.go`(`limit`)、`tool_git_diff.go`(`files`) | 已实现 |
| 硬拒绝上限 | `internal/agent/eino_invoker.go:22-31,646-679` | 8MB / 2000 条 / 1MB 单条 |

数据流（已确认，不改变）：

```
sessionstore（原始 message，append-only，永不改写）
  → Service.StartTurn 把 s.active.Messages 全量塞进 TurnRequest.History   (service.go:164,222)
  → CodingAgent.RunTurn 送 LLM 前调用一次 a.contexts.Process(...)          (coding_agent.go:72)
  → invocationMessagesFromContext 转成 InvocationMessage                   (coding_agent.go:76)
  → EinoInvoker.Invoke → runner.Run(messages)                              (eino_invoker.go:142)
```

## 3. 设计决策（已与用户对齐）

1. **要引入 LLM 摘要**，做**分块摘要**，且分块超预算后做**摘要块再摘要**（层级压缩）。
2. **「取全文」复用现有工具分页参数**（`start_line`/`line_count`/`limit`/`files`），不做「按 ID 取被截断结果」的通用缓存工具。
3. **token 计量抽象出 `Tokenizer` 接口**，先实现**字节数**实现，后续可按 provider/model 扩展真 tokenizer。
4. **session store 保持原始 append-only**，压缩是瞬态视图（理由见 §3.1 会话上一轮讨论：不可逆、模型相关、UI 要原文、分支要原文、重算幂等）。

## 4. 总体架构：两条独立机制

「历史压缩」与「工具结果限界」操作对象不同、时机不同、归属接缝不同，**不能做成单点级联**：

```
┌──────────────────────────────────────────────────────────────────┐
│ 机制 A：对话历史压缩（contextmanager 包）                          │
│   时机：每 turn 一次，送 LLM 前                                      │
│   预算阶梯：不超→放行；超 t2→分块摘要；摘要块超预算→递归合并；        │
│            仍超 t3→强裁丢最老轮次                                    │
└──────────────────────────────────────────────────────────────────┘
┌──────────────────────────────────────────────────────────────────┐
│ 机制 B：工具结果限界（tool / agent / workspace 层）                 │
│   时机：agentic loop 里每次工具执行后                                │
│   手段：字节预算截断（已有）+ 分页参数取全量（已有）+ 截断信号（补） │
└──────────────────────────────────────────────────────────────────┘
```

本文主体实现机制 A；机制 B 大部分已存在，只补「截断信号标准化」（§7）。

## 5. 机制 A：对话历史压缩

### 5.1 新增抽象（`internal/contextmanager`）

**5.1.1 `Tokenizer`——token 计量抽象（决策 3）**

```go
// Tokenizer estimates the size of neutral text in token-proxy units.
type Tokenizer interface {
    CountTokens(text string) int
}

// ByteTokenizer counts UTF-8 bytes as a provider-neutral token proxy.
type ByteTokenizer struct{}

func (ByteTokenizer) CountTokens(text string) int { return len(text) }
```

- 现在用字节数；后续新增 `ModelTokenizer` 可按 `(provider, model)` 选真 tokenizer（tiktoken 等）。
- **代价说明**：字节数不是真 token。英文 token 约 4 字节、CJK 约 3 字节/字。所以字节阈值是**预算代理**而非精确窗口算术，阈值要保守。`Scope` 已携带 `ProviderProfileID`/`ModelID`，为未来 per-model 计量预留了入口。

**5.1.2 `Policy`——预算阶梯配置**

```go
// Policy defines the compaction budget ladder.
type Policy struct {
    RecentTurns         int // 保留最近 N 轮原始对话（不被摘要）
    SummarizeThreshold  int // t2：总尺寸超过则开始分块摘要
    BlockTurns          int // 每个摘要块折叠的轮次目标
    SummaryBudget       int // 摘要区尺寸预算；超过则递归合并摘要块
    HardLimit           int // t3：最终安全上限，超过则丢最老轮次
}
```

约束（构造时校验）：`0 < RecentTurns`，`SummarizeThreshold < HardLimit ≤ 8MB`（对齐 eino 硬限），`BlockTurns ≥ 1`，`SummaryBudget < HardLimit`。

**5.1.3 `Summarizer`——LLM 摘要接口**

```go
// Summarizer produces a shorter neutral digest of one turn block.
type Summarizer interface {
    Summarize(ctx context.Context, scope Scope, turns []Message) (string, error)
}
```

- 接口定义在 `contextmanager`，保持包**纯 provider-neutral**（不 import provider/Eino）。
- 具体实现由装配层提供（§6），内部调用 `provider.Service.GenerateText`。

**5.1.4 `Message` 增加 `TurnID`**

当前 `contextmanager.Message` 只有 `ID/Role/Content/Current`，而 `contextRequestFromTurn`（coding_agent.go:241）在转换时**丢弃了 `session.Message.TurnID`**。分块摘要需要按「轮」分组，而每条 user 消息与其 assistant 回复共享同一 `TurnID`（service.go:186,686），因此给 `Message` 增加 `TurnID string` 字段（向后兼容的纯新增，零值不影响现有测试），并在 `contextRequestFromTurn` 里填充它。

### 5.2 `CompactionStrategy` 核心算法

`CompactionStrategy` 实现现有 `Strategy` 接口，`Process(ctx, Request) (Result, error)`。持有 `Policy`、`Tokenizer`、`Summarizer`、`summaryCache`。

输入 `Request.Messages` 的顺序恒为：`[u1,a1, u2,a2, …, uk,ak, u_{k+1}]`，其中 `u_{k+1}` 是 `Current==true` 的当前用户消息，`u1..ak` 是 k 个已完成轮次。

**算法（伪代码）**：

```
Process(request):
  1. 拆分：
     current   = 唯一 Current==true 的消息（必须存在且是最后一条）
     history   = 其余消息（已完成轮次，按 TurnID 分组）
  2. 计量 total = tokenizer(systemPrompt + history + current)
     if total ≤ SummarizeThreshold:
         return 原样（仅深拷贝）          // 未超预算，放行
  3. 划 tail：
     tail     = history 中最近 RecentTurns 个轮次（按 TurnID 计数）
     oldPrefix= history 中 tail 之前的部分
  4. 分块 oldPrefix：从左到右按 BlockTurns 个轮次切块（左对齐，见 §5.3）
     for each block:
         digest = cache.Get(key(block)) 或 Summarizer.Summarize(block)
         cache.Put(key(block), digest)
     summaries = [digest...]
  5. 递归合并（决策 1 的「摘要块再摘要」，独立于总阈值）：
         while size(summaries) > SummaryBudget:
             summaries = [ Summarizer.Summarize(把 summaries 当作「已有摘要块」合并) ]
  6. 强裁兜底：若 tokenizer(systemPrompt + summaries + tail + current) > HardLimit:
         从 tail 最老一端丢轮次，直到 ≤ HardLimit（绝不丢 current）
  7. 组装 Result：
     SystemPrompt = "[Conversation summary]…\n\n" + 原 systemPrompt   // §5.5
     Messages     = tail + current
     return
```

**关键不变量**（写进代码注释 + 单测）：

1. `current` 消息**逐字保留、恒为最后一条**（`invocationMessagesFromContext` 也会二次校验，coding_agent.go:281-294）。
2. 摘要**只覆盖 oldPrefix**，绝不涉及 tail 与 current。
3. tail 与 current 的消息 `ID`/`TurnID` 原样保留（缓存命中与未来分支依赖此点）。
4. `Process` 对每次调用**无残留状态**；跨 turn 状态只在 `summaryCache` 里，且按 key 隔离（§5.3）。
5. **摘要失败不使 turn 失败**：`Summarize` 出错时降级为丢弃该块（或截断），交给第 6 步强裁兜底，只记录 warning，不向上返回 error（`context.Canceled` 除外）。

### 5.3 分块与摘要缓存（上一轮「复用摘要」问题的落点）

**块必须左对齐、按轮固定粒度切分**，否则块边界每轮漂移、缓存永不命中：

- `oldPrefix` 从左（最老）到右按 `BlockTurns` 轮切块。
- **满块**（恰好 `BlockTurns` 轮）一旦形成，其 `[firstTurnID, lastTurnID]` 区间**冻结不变**（store append-only，前缀不可变）。
- **最右块**可能不满 `BlockTurns` 轮，每新增一轮其 `lastTurnID` 变化，该块每次重摘要（块小，成本低）；补满后冻结。

**缓存 key**（确定性、可跨 turn 复用）：

```
key = hash(sessionID, modelID, firstTurnID, lastTurnID, strategyVersion)
```

- 换 model：`modelID` 参与 key，天然隔离，无需显式失效（旧 entry 残留，v1 接受，后续可加 LRU）。
- 换策略版本：`strategyVersion` 参与 key，摘要提示模板或算法变化时递增。
- 进程重启：缓存清空，每个块重新摘要一次（一次 LLM 调用），可接受。

**并发**：`Manager`/`CompactionStrategy` 在 `buildRuntime` 只构建一次、被所有 agent 共享（`agent.Factory.deps.Contexts`）。因此 `summaryCache` 必须 `sync.RWMutex` 保护，且 key 含 sessionID 隔离不同会话。（当前同一时刻至多一个活跃 CodingAgent 跑一个 turn，但不同 session 会依次复用同一 strategy。）

### 5.4 摘要块再摘要（层级压缩）

第 5 步的「递归合并」即决策 1 的「分块超出后摘要块再摘要」。规则：

- 一级：原始轮次块 → 摘要块。
- 二级及更高：当摘要块的尺寸合计超过 `SummaryBudget` 时，把「摘要块列表」当作新输入，再 `Summarize` 成**一个**更粗的摘要块；仍超则继续，直到单块或预算内（收敛性由「每层把 N 块折成 1 块」保证，层数 O(log N)）。
- **损失叠加只发生在真溢出时**：正常情况每段原始数据只被摘要一次；只有摘要区本身也塞不下时才摘要摘要，此时最老信息本来最不相关，可接受。

### 5.5 摘要的表示方式：系统提示前缀

摘要块**不**作为新 `Role` 塞进 `Messages`，而是**前缀到 `SystemPrompt`**：

```
SystemPrompt = "[Conversation summary of earlier turns]\n"
             + "<摘要块1>\n---\n<摘要块2>\n---\n…\n"
             + "\n\n" + 原 systemPrompt
```

理由：

1. `Result.Messages` 保持纯 user/assistant，「恰好一条 Current 且逐字保留」的不变量不受影响；
2. `invocationMessagesFromContext` 与 Eino `invocationMessages`（eino_invoker.go:633）**零改动**（它们只处理 user/assistant）；
3. 系统提示是「注入的背景上下文」的语义正确归宿。

（被否决的替代：新增 `RoleSystem`/`RoleSummary`——需改 `Role` 枚举、`invocationMessagesFromContext` 校验、Eino 消息映射，改动面大且无收益。）

## 6. LLM 摘要的实现与装配

### 6.1 `provider.Service.GenerateText`（通用文本生成原语）

在 `internal/provider/service.go` 增加一个无工具的单轮文本生成方法：

```go
// GenerateText calls the validated model once and returns its reply text.
func (s *Service) GenerateText(ctx context.Context, ref ModelRef, systemPrompt, userText string) (string, error)
```

实现复用 `NewChatModel` + Eino `ChatModel.Generate(ctx, []*schema.Message{SystemMessage, UserMessage})`，返回 `out.Content`。这是 provider「如何与模型对话」的职责范围内，**不**包含摘要语义（摘要提示模板在 `contextmanager`）。

### 6.2 装配（`internal/app/build.go`）

替换 `buildRuntime` 中的 Nop 装配（build.go:146）：

```go
policy := contextPolicyFromConfig(base.configuration.Agent)   // 读 config 字段
summarizer := newModelSummarizer(caps.providers)             // app 内适配器：GenerateText → contextmanager.Summarizer
compaction, err := contextmanager.NewCompactionStrategy(policy, contextmanager.ByteTokenizer{}, summarizer)
contexts, err := contextmanager.NewManager(compaction)        // 替代 NopStrategy
```

`newModelSummarizer` 是把 `provider.Service.GenerateText` 适配成 `contextmanager.Summarizer` 的小型闭包/类型，属于装配层胶水，不放进 `contextmanager`（保持其 provider-neutral）。摘要提示模板（`Summarize` 的 system prompt）作为 `contextmanager` 包内常量，描述「保留目标/文件改动/决策/未决项」等要点。

## 7. 机制 B：工具结果截断信号标准化（决策 2）

**现状已足够**：每个工具按 `ToolResultMaxBytes` 截断（`jsonResult`），并暴露分页参数（`read_file.start_line/line_count`、`search_code.limit`、`list_files.limit`、`git_diff.files`），模型可通过分页参数取全量。

**要补的一小步**：截断信号目前只是 `truncateToolText` 追加 `"..."`，模型读到 `Content` 时不知道「还能分页取更多」。做法：

- 在 `truncateToolText` 截断处追加统一、可辨识的提示（例如 `…[truncated]`）；
- 各工具的 `Definition().Description` 已含分页说明，保持与 schema 一致即可。

这是一处**小改动 + 一致性整理**，不是新机制；若时间紧可作为独立小迭代。

## 8. 配置扩展（`internal/config/config.go`）

`AgentConfig` 增加上下文预算字段（平铺，与现有 `max_steps` 等风格一致）：

```yaml
agent:
  context_recent_turns: 4            # 保留最近 N 轮原始对话
  context_summarize_threshold: 32768 # t2：字节预算，超则开始摘要
  context_block_turns: 10            # 每个摘要块折叠的轮次
  context_summary_budget: 8192       # 摘要区预算，超则递归合并
  context_hard_limit: 1048576        # t3：安全上限，必须 < eino 的 8MB
```

配套：`Defaults()` 给安全默认值；`Validate()` 校验正数、`summarize_threshold < hard_limit`、`hard_limit ≤ maxInvocationHistorySize`（`8 << 20`）、`block_turns ≥ 1`、`recent_turns ≥ 1`。`config` 不 import `contextmanager`，`Policy` 由 `app` 装配时构造。

## 9. 调用时机与生命周期（回答「何时调用 / 是否保存」）

- **历史压缩**：每 turn **一次**，在 `CodingAgent.RunTurn` 送 LLM 前（现有 seam）。不在 agentic loop 内重复调——turn 期间历史不变，重复压缩会反复搅动摘要。
- **工具结果截断**：loop 内每次工具调用时（现有行为）。
- **turn 完成后**：保存**原始** assistant 消息 + turn 记录（`runTurn` 的 `CommitTurn`，service.go:713 已有）。**不保存压缩产物**。
- **摘要缓存的持久化**：v1 仅内存；进程重启后每块重摘要一次，可接受。若未来频繁重启成为痛点，再考虑把缓存落盘为派生 artifact（key 含消息区间 + model），但**永不写进原始消息表**。

## 10. 与消息分支的兼容性

- 原始 append-only 存储 + 瞬态压缩，天然兼容分支：分支 = 选不同 history 前缀作为 `TurnRequest.History`，压缩现场重算，各分支各自得到自己的摘要。
- 缓存 key 含 `(firstTurnID, lastTurnID)`，不同分支的相同前缀命中同一缓存、不同后缀各自 miss，互不污染。
- 当前 `invocationMessagesFromContext` 要求「当前消息逐字保留」。若未来做「改写历史消息再重放」分支，需把这条校验放宽为「允许替换当前消息内容」——**本次不改**，仅在文档标注为未来分叉点。

## 11. 边界情况与错误处理

| 场景 | 行为 |
| --- | --- |
| 单条历史消息本身巨大（用户粘贴大文件） | 第 6 步强裁仍超时，可对**最大的一条非 current 消息**做头尾截断（保留头部 + `…[truncated]`）；current 永不截断 |
| 摘要调用失败（provider 错误/超时） | 降级为丢弃该块 + 记 warning，交给强裁兜底；**不让 turn 失败** |
| 上下文被取消 | 尊重 `ctx.Err()`，向上返回（这是唯一向上返回 error 的情况） |
| 总尺寸本就 ≤ 阈值 | 直接放行，零开销 |
| 换 model | 缓存 key 含 modelID，自动隔离；阈值仍用同一 Policy（per-model 阈值留待后续） |
| 并发跨 session | `summaryCache` 用 `sync.RWMutex`；key 含 sessionID |

## 12. 测试策略

- **纯函数单元测试**（不依赖 LLM）：`Tokenizer`/`ByteTokenizer`、`Policy` 校验、分块逻辑（左对齐、冻结边界）、缓存 key 稳定性、强裁不丢 current、`TurnID` 分组。
- **`CompactionStrategy` 用 fake `Summarizer`**：验证「不超不摘要 / 超则分块 / 块冻结后不重复调 Summarizer / 递归合并 / 强裁」；fake Summarizer 记录调用次数，断言「同一满块只调一次」。
- **`provider.GenerateText`**：用 fake `ToolCallingChatModel` 验证消息组装与返回。
- **装配**：`buildRuntime` 用临时 config 验证 `CompactionStrategy` 正确接线（可断言非 Nop）。
- **端到端**：构造一个超长会话，验证 turn 不因 `maxInvocationHistorySize` 硬拒绝而失败，且 UI 仍显示原始对话。
- 全量回归：`gofmt -l .`、`go vet ./...`、`go test ./... -count=1 -timeout=180s`（对齐 AGENT.md §14）。

## 13. 实施步骤（每步独立可测、可交付）

按「先确定性、后 LLM、再接装配」的顺序，每步落地一个可编译可测的增量：

1. **基础类型**：`Tokenizer` + `ByteTokenizer` + `Policy` + `Message.TurnID`（`contextmanager`），纯新增，测试覆盖。
2. **确定性压缩**：`CompactionStrategy` 的「未超放行 + 强裁兜底」路径（此时 `Summarizer` 可传 nil，只走保留 N 轮 + 丢最老），fakenil。交付价值：**turn 不再因超限硬失败**。
3. **模型原语**：`provider.Service.GenerateText` + 测试（fake chat model）。
4. **摘要实现**：`contextmanager.Summarizer` 接口 + 摘要提示模板 + `app` 内适配器（`GenerateText → Summarizer`）。
5. **完整压缩**：`CompactionStrategy` 的「分块 + 缓存 + 摘要 + 递归合并」全路径，用 fake `Summarizer` 测试。
6. **配置 + 装配**：`config` 字段/默认值/校验 + `build.go` 接线（替换 Nop）。
7. **截断信号标准化**：机制 B 的小改动（可独立小迭代）。
8. **端到端验证**：长会话冒烟 + 全量回归。

## 14. 实现原则

除遵循 `AGENT.md` 全部规则外，本模块额外强调：

1. **`contextmanager` 保持 provider-neutral**：只 import stdlib，不 import provider/Eino/session。模型能力经 `Summarizer` 接口注入，装配在 `app` 完成。
2. **接口由消费者定义、尽量小**：`Summarizer`/`Tokenizer` 都只有一个方法。
3. **摘要永不污染原文**：原始消息表 append-only；摘要缓存是派生 artifact，落盘也不进消息表。
4. **压缩可降级、不可失败**：摘要错误 → 丢块/截断兜底，绝不因压缩失败而让 turn 失败。
5. **每步 TDD**：先写测试再写实现；每步之后跑全量回归。
6. **不变量进注释与断言**：§5.2 的五条不变量要写进代码注释，并由单测锁死。
7. **YAGNI**：摘要缓存 v1 仅内存；per-model 阈值、LRU 失效、缓存落盘都留作后续。

## 15. 风险与开放问题

| 项 | 说明 | 处置 |
| --- | --- | --- |
| 字节 ≠ token | 字节阈值是代理，不同语言/模型偏差不同 | 阈值保守 + 可调；`Tokenizer` 已留扩展点 |
| 摘要质量影响后续能力 | 摘要丢失关键上下文会导致后续 turn 答非所问 | 摘要提示强调「保留目标/改动/未决项」；后续可评估在 tail 保留更多原始轮次 |
| 摘要增加首轮延迟 | 超阈值时，送主模型前多一次 LLM 调用 | 仅超阈值时发生；缓存命中后为 0；可接受 |
| 缓存内存增长 | 满块缓存随 session×块数增长 | v1 接受；后续 LRU/落盘 |
| per-model 阈值 | 不同模型窗口不同，当前单一 Policy | 用 `Scope.ModelID` 预留；后续按模型查表 |

---

## 附：改动文件清单

| 文件 | 动作 |
| --- | --- |
| `internal/contextmanager/tokenizer.go` | 新增 `Tokenizer`/`ByteTokenizer` |
| `internal/contextmanager/summarizer.go` | 新增 `Summarizer`/`Policy`/摘要提示 |
| `internal/contextmanager/compaction.go` | 新增 `CompactionStrategy`/`summaryCache` |
| `internal/contextmanager/contextmanager.go` | `Message` 加 `TurnID`；保留 `Manager`/`NopStrategy` |
| `internal/contextmanager/*_test.go` | 新增测试 |
| `internal/agent/coding_agent.go` | `contextRequestFromTurn` 填充 `TurnID` |
| `internal/provider/service.go` | 新增 `GenerateText` |
| `internal/app/build.go` | 装配 `CompactionStrategy`（替换 Nop） |
| `internal/config/config.go` | 新增 5 个上下文预算字段 + 默认值 + 校验 |
