# CodePilot 功能规划与待实现功能

> 文档版本：v0.1
> 状态：待评审
> 更新日期：2026-08-21
> 来源：基于 prd.md §3.1/§3.4/§14 与 MVP 代码评审梳理
> 关联文档：prd.md（目标与范围）、improvement-plan.md（代码质量问题修复计划）

## 1. 文档目的

回答一个问题：**当前 MVP 作为 coding agent 功能是否完善，还差哪些功能需要实现。**

结论一句话：**MVP 的「Bugfix 闭环」是完整的**（定位 → 补丁 → 受控检查 → 验证 → Diff 展示 → 审批，全链路已通），但作为 PRD §3.1 所定义的「本地优先、安全、可验证、可扩展的通用 Coding Agent」，还差 7 大类功能。

本文档与两份已有文档的分工：

| 文档 | 关注点 |
| --- | --- |
| prd.md | 「为什么做」「MVP 边界」「非目标」 |
| improvement-plan.md | 已实现代码的**质量问题**（滚动、命名、依赖方向等） |
| **feature-roadmap.md（本文）** | 尚未实现的**功能缺口**（任务类型、上下文、隔离、集成等） |

## 2. 功能缺口总览

优先级定义（与 improvement-plan.md 一致）：

- **P1**：近期（下 1–2 个迭代），对「可用性」或「真实 agent 能力」影响最大。
- **P2**：中期，扩展任务类型与安全边界。
- **P3**：远期，平台化与规模化能力。

| ID | 优先级 | 类别 | 待实现功能 | PRD 出处 |
| --- | --- | --- | --- | --- |
| CTX-1 | P1 | 上下文 | 上下文窗口管理：摘要压缩、历史裁剪、token 统计 | §14.1 |
| LSP-1 | P1 | 代码理解 | 完善 LSP：hover / rename / 实时诊断（当前仅导航） | §14.1 |
| UI-F1 | P1 | 交互 | 文件树、文件预览、side-by-side Diff | §14.1 |
| UI-F2 | P1 | 交互 | 对话滚动 / 换行 / 输入框增强（✅ UI-1/2/3/4 已实现） | improvement-plan UI-1/2/3/4 |
| TASK-1 | P2 | 任务类型 | 代码审查（Review） | §3.1/§14 |
| TASK-2 | P2 | 任务类型 | 测试生成与补全 | §14.1 |
| TASK-3 | P2 | 任务类型 | 代码理解与问答 | §3.1 |
| TASK-4 | P2 | 任务类型 | 小范围重构 | §3.1 |
| TASK-5 | P2 | 任务类型 | 文档与变更说明生成 | §3.1 |
| ISO-1 | P2 | 隔离安全 | 独立 Git Worktree per Session（隔离修改） | §14.1 |
| ISO-2 | P2 | 隔离安全 | Sandbox / 容器执行（替换 CommandExecutor） | §14.2 |
| INT-1 | P3 | 集成 | 远程 issue 拉取（GitHub / GitLab） | §14.2 |
| INT-2 | P3 | 集成 | 自动 branch / commit / push / PR 流程 | §14.2 |
| INT-3 | P3 | 集成 | MCP / 插件协议 | §14.2 |
| RUN-1 | P3 | 运行时 | 持久 Eino Checkpoint（跨重启恢复） | §14.2 |
| RUN-2 | P3 | 运行时 | 多 Agent / 子 Agent / 任务规划 | §3.4 |
| RUN-3 | P3 | 运行时 | Session fork 与跨 Worktree 复制上下文 | §14.2 |
| RUN-4 | P3 | 运行时 | 后台 / 并发 Agent | §3.4 |
| CLI-1 | P3 | 入口 | 非交互式入口（codepilot fix / exec） | §3.4 |
| PLAT-1 | P3 | 平台 | Web UI / 桌面 UI / IDE 插件 | §3.4 |

---

# 第一部分：P1（近期）

## 3. CTX-1 上下文窗口管理

### 3.1 现状

`contextmanager` 目前只接入 `NopStrategy`（`build.go:146`），不压缩、不裁剪、不统计 token。上下文随对话线性增长，长会话必然撞到模型上下文上限，且用户无感知。

### 3.2 为什么需要

这是「通用 coding agent」与「一次性 bugfix 脚本」的分水岭。没有上下文管理，持续多轮对话（PRD §3.2 第 2 条）在真实仓库里跑几次就会退化或报错。

### 3.3 实现路径

- 复用 `contextmanager.Manager` 的顺序策略机制，新增 `TokenBudgetStrategy`（估算 token、超预算裁剪）与 `SummaryStrategy`（调用模型对早期消息做摘要），NopStrategy 保留为关闭基线。
- 补充 token 估算（provider-neutral，不依赖具体模型）与「剩余上下文」展示到 UI。

### 3.4 验证

- 单测：固定 token 预算下，超长历史被正确裁剪/摘要，且不影响最新一轮请求。
- 手动：构造长对话，确认 UI 显示 token 用量、超过阈值时自动压缩且用户可见提示。

## 4. LSP-1 完善 LSP 能力

### 4.1 现状

LSP 已实现导航子集：`Definition` / `References` / `Symbols` / `Diagnostics`（`agent/ports.go` 的 `CodeNavigator`）。缺 hover、rename、以及「编辑后实时诊断」。

### 4.2 为什么需要

导航只回答「在哪」，hover/rename/诊断才能支撑「理解 + 安全重构」，是代码审查与重构类任务的前置。

### 4.3 实现路径

- 在 `CodeNavigator` 接口按需增加 `Hover`、`Rename`（均需审批），沿用现有 `lsp.Navigator` 与协议层。
- 诊断从「按需查询」升级为「编辑后触发 + 结果进入 Diff 面板」。

### 4.4 验证

- 单测：协议 mock 返回 hover/rename，断言走 PathGuard 与审批。
- 手动：Go/Python 仓库内触发 hover 与 rename，确认不越过 Worktree 边界。

## 5. UI-F1 文件树与文件预览

### 5.1 现状

仅支持 `@` 提及补全（最多 500 个 Git 可见文件）。无树形导航、无「点击打开预览」、Diff 为单栏非 side-by-side。

### 5.2 为什么需要

大仓库里靠 `@` 补全无法建立空间感；文件树 + 预览是用户理解「Agent 在看/改哪里」的核心信息界面，也是 PRD §14.1 的 P1 项。

### 5.3 实现路径

- 新增 `ui/files.go`，消费现有 Workspace 文件事件（PRD §14 扩展表已预留此接缝），不新增底层能力。
- Diff 面板升级为 side-by-side（复用 `ansi.StringWidth` 处理对齐）。

### 5.4 验证

- 手动：打开大型仓库，树形导航 + 文件预览 + side-by-side diff 均可用，窄终端退化正常。

## 6. UI-F2 对话滚动 / 换行 / 输入框增强

### 6.1 说明

这三项本质是「缺失的基础交互能力」，已在 `improvement-plan.md` 中作为 UI-1 / UI-2 / UI-3 详细展开（含修复方案与验证），此处不重复，仅登记为功能缺口。它们是 P1 的交互基线，应先于文件树等「锦上添花」能力落地。

> 进度：UI-1（对话与 Diff 滚动）、UI-2（对话换行）、UI-3（输入框光标移动 / 历史补全 / 词删除）与 UI-4（Picker 列表滚动窗口）已于 2026-08-21 实现并随单测落地，见 `improvement-plan.md` §3.4 / §4.4 / §6.4 / §7.4。

---

# 第二部分：P2（中期）

## 7. 任务类型扩展（TASK-1 ~ TASK-5）

| ID | 功能 | 实现路径（复用现有 seam） | 验证 |
| --- | --- | --- | --- |
| TASK-1 | 代码审查 | 复用 prompt / tool policy，或独立 AgentFactory 策略（PRD §14「Review」行） | 对同一改动输出结构化 review，含逐条 issue |
| TASK-2 | 测试生成 | 复用 patch + language + check 工具，新增测试框架策略 | 生成的测试可执行且覆盖目标函数 |
| TASK-3 | 代码理解问答 | 复用搜索/读取/LSP，不改基础设施 | 能回答跨文件「这段代码为什么这样」 |
| TASK-4 | 小范围重构 | 复用 patch + 审批 + check，约束范围上限 | 重构后原测试集仍通过 |
| TASK-5 | 文档/变更说明生成 | 复用 Diff + git 状态，新增纯文本生成 prompt | 从 Session Diff 生成变更说明 |

**共性约束**：均共享 Session / Provider / 权限 / Workspace / Agent 运行时 / 工具 / UI（PRD §3.1「不应为每一种任务重写基础设施」）。优先级按 PRD §14.1：Review 与 Test generation 在前。

## 8. ISO-1 独立 Git Worktree per Session

### 8.1 现状

所有 Session 直接操作真实 Worktree 文件（PRD §4.4 明确「Session A 改文件后 Session B 会看到」）。改动未隔离，误操作不可回滚（除依赖用户自己 commit）。

### 8.2 为什么需要

隔离是「安全、可验证」定位的硬件基础：每个 Session 在自己的 Worktree 上跑，互不污染，失败可整体丢弃。

### 8.3 实现路径

- 新增 `WorktreeManager`（PRD §14 扩展表），Session 仍绑定 `WorktreeRoot`，数据模型与 UI 不变。
- 复用 `workspace` 的 git 能力创建/删除临时 Worktree。

### 8.4 验证

- 单测：两个 Session 并行改同一文件互不影响；删除 Session 可清理其 Worktree。
- 手动：隔离 Worktree 中 `go test` / `python -m pytest` 结果不与主 Worktree 冲突。

## 9. ISO-2 Sandbox / 容器执行

### 9.1 现状

检查命令通过 `LocalCommandExecutor` 直接在本机运行（仅靠命令策略 + 审批约束）。README 也明确「MVP 不宣称隔离」。

### 9.2 为什么需要

这是从「约束式安全」到「隔离式安全」的升级，是执行测试/生成代码等有副作用任务的真正安全边界。

### 9.3 实现路径

- 实现 `ContainerExecutor`（或 OS 级 Sandbox），替换 `CommandExecutor`（PRD §14 扩展表明确「只替换 CommandExecutor，不改 Agent/Session/UI」）。
- 保持相同 `CommandSpec` / `CommandResult` 语义。

### 9.4 验证

- 单测：危险命令在沙箱内被隔离，退出码/输出语义与本地执行一致。

---

# 第三部分：P3（远期）

## 10. 协作与集成（INT-1 ~ INT-3）

| ID | 功能 | 实现路径 | 说明 |
| --- | --- | --- | --- |
| INT-1 | 远程 issue 拉取 | 新增 IssueSource，转换为 Session 用户消息 | GitHub/GitLab，不改 Agent 工具 |
| INT-2 | 自动 branch/commit/PR | 独立 publish workflow + 新授权 | 与「不自动 commit」默认行为隔离，用户显式触发 |
| INT-3 | MCP / 插件协议 | 按 PRD §14.2 引入，避免自研协议 | 先消费官方 MCP SDK，不造插件市场 |

## 11. Agent 运行时增强（RUN-1 ~ RUN-4）

| ID | 功能 | 说明 |
| --- | --- | --- |
| RUN-1 | 持久 Eino Checkpoint | 替换内存 `CheckpointStore` 实现，跨重启恢复中断 turn；产品 SessionStore 不变 |
| RUN-2 | 多 Agent / 子 Agent / 任务规划 | 明确非目标后再进入，需先有 Sandbox 与隔离 Worktree 支撑 |
| RUN-3 | Session fork / 跨 Worktree 复制上下文 | 依赖 ISO-1 落地后的 WorktreeManager |
| RUN-4 | 后台 / 并发 Agent | 突破「单进程单 Active Session」约束，需重构 Session 并发模型 |

## 12. 入口与平台（CLI-1、PLAT-1）

| ID | 功能 | 说明 |
| --- | --- | --- |
| CLI-1 | 非交互式入口（codepilot fix / exec） | 面向 CI / 脚本，复用 Session + Agent，去掉 TUI |
| PLAT-1 | Web / 桌面 / IDE 插件 | 将 `session.Service` 与 `agent` 从 TUI 解耦后，作为后端复用 |

---

## 13. 建议实施顺序

```
迭代 1（P0/P1 质量）   improvement-plan：UI-1/2/3/4（已完成）、NAM-1
迭代 2（P1 能力）       CTX-1（上下文管理）→ UI-F2 → UI-F1（文件树）
迭代 3（P2 能力）       ISO-1（隔离 Worktree）→ LSP-1 → TASK-1/2（Review/测试生成）
迭代 4（P2 能力）       ISO-2（Sandbox）→ TASK-3/4/5
迭代 5（P3 平台化）      INT-1/2 → RUN-1 → CLI-1 → RUN-2/3/4 → INT-3 / PLAT-1
```

排序依据：**先让「已实现的 bugfix 闭环」好用（质量），再补「上下文管理」这个真实 agent 的命脉，再上「隔离」这个安全升级，最后才做平台化**。多 Agent（RUN-2）和 Sandbox（ISO-2）都依赖更基础的能力先落地，不应提前。

## 14. 决策记录

- `contextmanager` 是否保留占位：若 CTX-1 在迭代 2 落地，则保留并补齐策略；否则按 improvement-plan DEP-5 处理。
- LSP 与「搜索/读取/Diff」的边界：PRD §13 明确「LSP 不替代搜索、读取、Diff 和测试」，后续 LSP 扩展保持辅助定位，不变成主交互。
