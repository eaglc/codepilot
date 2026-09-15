# CodePilot 下一阶段：产品体验与扩展能力路线图

状态：待执行
形成日期：2026-09-15
适用基线：`main` 分支当前模块化架构，以及已经落地的 Product Turn、Plan、Workflow、子 Agent、上下文管理、权限、恢复、LSP、Artifact 和 TUI 能力
前置文档：[产品补全路线图](product-completion-roadmap.md)、[Plan、Workflow 与多 Agent 可持续交付方案](plan-workflow-multi-agent-delivery-plan.md)、[模块化架构基线与迁移计划](../architecture/modular-architecture-migration.md)

## 1. 路线图结论

CodePilot 已经完成“理解仓库、规划任务、修改代码、执行可信检查、恢复中断和协调子 Agent”的核心闭环。下一阶段不再以增加更多 Agent 调度策略为主，而是把已有能力组织成一个长期可用、容易理解、可以安全扩展的 Coding Agent 产品。

后续建设分为四个阶段：

1. **P3：产品可见性与基础交互。** 让用户看见当前指令、上下文、任务状态、权限和执行进度，并补齐多行输入等高频体验。
2. **P4：Skills 最小可用系统。** 建立可发现、渐进加载、可审计并受现有 Tool/Permission 边界约束的复用能力层。
3. **P5：任务工作台。** 将会话、计划、Agent、Diff、检查和仓库结构组织为任务中心式工作流。
4. **P6：受控扩展生态。** 在证明前述边界稳定后，再增加 MCP、受控 Git 写操作、附件和有来源的项目记忆。

核心概念必须保持分离：

```text
AGENTS.md = 这个项目及目录范围内应该怎样工作
Skill     = 遇到某类任务时建议采用怎样的方法
Tool      = 当前运行实际能够执行什么操作
Agent     = 基于用户目标、上下文和能力决定下一步
```

Instruction 和 Skill 都不能授权 Tool，Plan 不能替代 Permission，仓库内容不能改变 Provider、恢复、持久化或安全策略。

## 2. 当前基线与主要缺口

### 2.1 已有能力

- Product Turn、Plan 版本、Workflow DAG、串行和并行子 Agent 已持久化并可恢复。
- 并行写入使用独立 Git worktree 和内容寻址 ChangeSet，集成前验证精确目标和 drift。
- Context Manager 已支持预算、摘要、当前 Turn 原子保护、大结果 Artifact 外置和事实一致性验证。
- `AGENTS.md` 已支持根到叶作用域、敏感路径过滤、大小限制、来源摘要和 Prompt Injection 隔离。
- Tool Registry、Permission Boundary、RecoveryPlan 和 proposed/applied diff 已形成可信执行边界。
- Go、Python、Node/TypeScript 语言识别及 Definition、References、Diagnostics、Document Symbols 已接入。
- TUI 已支持流式 Markdown、Tool 折叠、Diff 侧栏、Picker、命令补全、鼠标选择和复制。

### 2.2 下一阶段缺口

- 用户看不到当前加载了哪些项目指令、为何生效或为何被忽略。
- UI 只显示聚合 Context 数值，无法解释预算由哪些内容构成。
- Composer 仍是单行视口，多行内容仅以换行标记压平显示。
- Plan、Workflow、Child Agent、Diff 和检查虽有产品 DTO，但仍主要混排在聊天时间线中。
- 没有 `SKILL.md` 的目录、索引、触发、渐进加载、依赖验证、审计和管理 UI。
- Session 数据完整，但缺少以任务结果为中心的搜索、摘要和历史入口。
- 仓库索引与 LSP 已有基础，尚未形成面向用户和 Agent 的轻量 Repository Map。
- 主题、快捷键和窄终端适配主要是硬编码配置。
- 外部工具、网络能力和 Git 写操作尚无统一扩展协议；这是当前的安全边界，不应以任意 Shell 绕过。

## 3. 执行原则

1. **先做产品投影，再增加新的运行时复杂度。** 已持久化的数据优先通过 `codingagent.Snapshot/Event/Service` 暴露，UI 不读取底层 Store DTO。
2. **渐进披露。** Skill、Repository Map、Tool Result 和历史记录默认只提供有界摘要，用户或 Agent 明确选择后再加载正文。
3. **不可见即不可控。** Instruction、Skill、Context、Permission、Agent 状态和 ChangeSet 必须能在 UI 中解释来源和作用域。
4. **不可变审批。** 修改已提交 Plan 或已展示 ChangeSet 时必须创建新版本并重新计算 digest，不在原批准对象上静默删改步骤或 hunk。
5. **无任意 Shell。** 新检查、Skill 脚本和 Git 操作必须编译为可展示、可审批、可恢复的固定执行计划。
6. **模型不能授予能力。** Skill 声明的工具只是依赖，实际可用工具由可信产品策略、角色和 Permission 的交集决定。
7. **事实必须有来源。** Repository Map 和未来 Project Memory 必须记录文件、符号、commit 或用户确认来源，模型推测不能成为长期权威数据。
8. **不重复建设。** 不重新实现 Context Manager、LSP、Plan 或 Permission；本阶段在现有边界上增加产品能力。
9. **每个里程碑独立可交付。** 一项任务只有在实现、测试、架构检查和文档同步完成后才能标记为完成。

## 4. P3：产品可见性与基础交互

P3 的目标是降低日常使用中的不确定感。完成后，用户不需要阅读日志或猜测模型行为，就能回答“当前依据了什么、正在做什么、改了什么、还在等什么”。

### P3-01 项目指令标准化与可见性

- [ ] 仓库根开发规范使用大小写精确的 `AGENTS.md` 文件名，并增加回归检查防止重新出现 `AGENT.md` 等误拼。
- [ ] 将项目指令发现结果投影为稳定的产品 DTO，至少包含 `source`、`scope`、`sha256`、状态和有界诊断。
- [ ] 增加 `/instructions` 页面，展示已加载、未命中、被忽略和加载失败的指令来源。
- [ ] 在当前 Turn 或选中文件的上下文中标明实际生效的根到叶规则链。
- [ ] 对常见误拼只给出修复提示，不静默加载非标准文件。
- [ ] Prompt Builder 与 UI 使用同一个可信发现结果，避免展示内容与模型实际收到的内容不一致。

验收：针对同一 worktree 和 scope，UI 展示的指令集合、顺序和 digest 与 Agent Run 使用的集合完全一致；切换 worktree、敏感路径或文件 scope 后结果正确刷新；仓库文本仍无法改变权限和安全策略。

### P3-02 Context 可观测性

- [ ] 在 Context Manager 输出稳定的分类统计：System、Task、Instructions、Skills、History、Tool Results、Artifacts、Reserved Output。
- [ ] 区分 Provider 精确计数和本地估算，未知时明确显示 `estimated`，不伪造精度。
- [ ] 增加 `/context` 页面，展示当前输入预算、保留输出、压缩阈值、最近压缩结果和降级原因。
- [ ] Context 统计只暴露产品安全的大小和来源摘要，不回显 secret、敏感文件正文或内部 system prompt。
- [ ] 增加指令或 Skill 后，在发起模型请求前重新执行预算检查。

验收：用户能够解释当前 Context 的主要占用来源；摘要、Artifact 外置或安全裁剪发生后，页面与真实下一次模型输入保持一致。

### P3-03 多行 Composer

- [ ] 将输入区改为 2–8 行自适应高度的真实多行编辑器。
- [ ] 保持 Enter 发送、Alt+Enter 插入换行的默认语义，并允许后续 Keymap 配置覆盖。
- [ ] 支持 CJK、emoji、组合字符、软换行、长行定位和大段粘贴。
- [ ] 草稿按 Session 保存或显式丢弃；草稿和已发送输入历史使用不同的数据模型。
- [ ] Provider Credential 输入继续使用独立的敏感缓冲区，不复用普通 Composer。
- [ ] 窄终端下优先保证输入内容和确认操作可见。

验收：多行编辑、历史切换、Session 切换、取消、粘贴和窗口 resize 不丢失或错位；敏感输入仍不进入历史、草稿和日志。

### P3-04 Task/Plan/Agent 状态层级

- [ ] Header 使用紧凑状态 Badge 展示 workspace、branch/dirty、Direct/Plan/Workflow、Permission、Context 占用和 Provider/Model。
- [ ] 将 Workflow 和 Child Agent 投影为可折叠树，明确 `queued/running/waiting/blocked/failed/completed`。
- [ ] 每个节点显示角色、目标摘要、文件 scope、耗时、重试和 ChangeSet/检查结果入口。
- [ ] 将需要用户输入、审批或 replan 的节点置于视觉优先位置。
- [ ] 普通模型过程文本保持可访问，但不与任务状态竞争主要视觉层级。
- [ ] 为小终端定义明确降级：状态树单独页面、Diff 全屏切换、Header 自动收缩。

验收：用户无需展开完整聊天即可判断任务阶段、并行度、失败位置、等待原因和下一步操作；状态全部来自 durable source of truth，而非临时文本推断。

### P3 退出门槛

- [ ] P3-01 至 P3-04 的行为测试和 UI snapshot/golden 测试通过。
- [ ] Windows、Linux、macOS 原生 TUI 关键测试实际成功，关闭上一阶段遗留的跨平台验证项。
- [ ] `go test ./...`、`go vet ./...`、架构依赖检查和 CLI 构建通过。
- [ ] README、TUI Help 和文档索引与新命令保持一致。

## 5. P4：Skills 最小可用系统

P4 的目标不是建立市场或执行任意脚本，而是增加一个位于项目指令和工具之间的可复用方法层，并证明它能在安全、Context 和恢复边界内稳定工作。

### P4-01 Skill 领域模型和目录

- [ ] 定义版本化 `SkillDescriptor`、`SkillSource`、`SkillRevision`、`SkillActivation` 和诊断模型。
- [ ] 首版支持内置、用户级和项目级 `.codepilot/skills/<name>/SKILL.md` 三个来源。
- [ ] 明确定义名称、描述、触发提示、tags、适用任务、工具依赖和可选资源元数据。
- [ ] 定义稳定的优先级和同名冲突规则；项目 Skill 不得静默覆盖内置安全 Skill。
- [ ] Skill 文件采用内容 digest 标识版本，正在运行的 Turn 绑定精确 revision。
- [ ] 不把 Skill 激活状态或仓库 Skill 正文写入 system prompt。

验收：相同输入目录得到确定、有界、跨平台一致的 Skill Index；来源和冲突可诊断；Skill 更新不会改变已经开始的 Run。

### P4-02 安全发现与渐进加载

- [ ] Skill Index 平时只向模型提供 name、description、trigger、tags 和有界能力摘要。
- [ ] 通过可信 `use_skill` 产品边界或用户 `$skill-name` 显式选择后再加载完整 `SKILL.md`。
- [ ] 限制 Skill 数量、单文件大小、总字节、资源文件数量和 Context 预算。
- [ ] 不跟随 symlink，排除敏感路径、产品状态目录、依赖目录和构建产物。
- [ ] Skill 内容按低信任 user-role context 注入，JSON/结构化包络防止逃逸。
- [ ] 模型自动选择 Skill 时只允许一次有界选择批次，避免递归激活和无进展循环。

验收：100 个 Skill 不会将 100 份正文加入 Context；未选择 Skill 的正文不可见；恶意 Skill 不能改变 Tool Registry、Permission、Provider、恢复或持久化策略。

### P4-03 Tool、Role 和 Permission 组合

- [ ] Skill 的 `required_tools` 和 `optional_tools` 只作为依赖声明，不创建或授权工具。
- [ ] 实际工具集为 Feature Flags、Agent Role、Tool Factory、工作区能力、Permission 和 Skill 依赖的可信交集。
- [ ] 依赖缺失时返回可操作诊断，允许选择降级 Skill 或继续不用 Skill。
- [ ] Skill 不能扩大 Plan scope、子 Agent 文件 scope 或 ChangeSet 集成范围。
- [ ] 脚本资源首版只作为参考文件；任何未来脚本执行必须先转换为固定 Check Plan。
- [ ] 子 Agent 默认只继承计划编译时显式绑定的 Skill revision，不继承主 Agent 的全部用户级 Skills。

验收：Skill 声明 `run_checks`、写工具或网络能力都不会绕过原有审批；只读 Role 无法因 Skill 变成写 Role。

### P4-04 Skills UI 与命令入口

- [ ] 增加 `/skills`，支持搜索、来源筛选、查看详情、启用/禁用和诊断。
- [ ] Composer 支持 `$skill-name` 补全和显式调用。
- [ ] Command Palette 可将 `/review` 等产品别名映射到稳定 Skill ID，但内建管理命令优先且不可覆盖。
- [ ] Timeline 显示本 Turn 使用的 Skill、来源、revision、触发方式和加载状态。
- [ ] 明确区分“已安装”“当前项目可见”“当前 Session 启用”“本 Turn 已使用”。
- [ ] Skill 文件变化时提示下一 Turn 生效，不热替换正在运行的 Run。

验收：用户可以在不编辑配置文件的情况下发现和显式使用 Skill，并能解释它为何被自动选择或未被使用。

### P4-05 持久化、恢复与质量门槛

- [ ] 持久化 Skill Activation 的 ID、revision、来源摘要和结果，不复制不必要的正文。
- [ ] Resume/Recovery 重新校验 Skill revision；发生 drift 时暂停并让用户选择固定旧版本、采用新版本或取消。
- [ ] Context Summary 保留影响后续工作的 Skill 决策，不把 Skill 文本提升为可信事实。
- [ ] 建立发现、冲突、注入、预算、权限、恢复、跨平台路径和恶意 fixture 测试。
- [ ] 提供至少三个内置示例 Skill，验证修复测试、代码评审和生成测试等不同工作流。

验收：崩溃恢复不会在未知 Skill 版本上继续执行；Skill 使用全程可审计且不泄漏本机绝对路径或敏感内容。

### P4 退出门槛

- [ ] 至少在真实 Go 和 Python 仓库中完成显式调用、自动选择、依赖缺失和恢复 E2E。
- [ ] 比较启用/禁用 Skill 的 token、步骤、成功率和耗时，确认渐进加载没有显著 Context 回归。
- [ ] Skills 不引入任意命令执行入口，安全和架构测试锁定此边界。
- [ ] 文档包含 Skill 作者指南、项目接入指南和故障诊断。

## 6. P5：任务工作台

P5 将已有的持久化能力重组为以任务结果为中心的产品界面。它不要求把 CodePilot 改造成完整 IDE；终端 UI 继续以 Task、Plan、Changes 和 Checks 为一级信息，聊天作为持续干预入口。

### P5-01 Task History 与搜索

- [ ] 从现有 Product Turn、Plan、Workflow、Tool、Diff 和 Usage 数据生成稳定 Task Summary。
- [ ] 增加 `/tasks`，按时间、workspace、状态、模型和标签筛选。
- [ ] 支持搜索用户目标、最终结果、修改文件和检查名称，不默认索引敏感 Tool 正文。
- [ ] 列表显示完成/失败/取消/等待状态、改动统计、检查结果、耗时和成本。
- [ ] 支持从任务继续、打开关联 Session、查看 Plan/ChangeSet，以及安全导出 Markdown/JSON。

验收：不新增重复的任务日志数据源；任何 Task Summary 都可由 durable journal 和产品 Store 重建。

### P5-02 Command Palette 和设置中心

- [ ] 增加 `Ctrl+K` Command Palette，支持模糊搜索、分类、最近使用和快捷键提示。
- [ ] 命令参数提供内联帮助和补全，Skills 可贡献受限命令别名。
- [ ] 增加 `/settings`，统一展示全局、项目和 Session 配置及最终生效来源。
- [ ] 支持 Keymap、主题、Markdown、默认 Permission、执行偏好和 Agent 上限等非敏感配置。
- [ ] Provider Credential 仍由专用安全界面和 Credential Store 管理，不进入普通设置文件。

验收：CLI flag、配置文件和 Session override 的优先级确定且可解释；未知或旧版本配置不会静默改变安全行为。

### P5-03 Repository Map

- [ ] 复用 Git 文件索引、Language Registry 和 LSP Symbols 构建有界 Repository Map。
- [ ] 首版包含目录、语言、package/module、主要类型/函数和最近相关文件，不引入向量数据库。
- [ ] Map 绑定 worktree identity、commit 和 dirty 状态，增量失效而不是长期缓存陈旧事实。
- [ ] Agent 默认只看到摘要和相关片段，需要时再通过工具展开。
- [ ] 增加 `/map` 页面，并允许从 Task/Agent 节点跳到相关文件和符号。

验收：大型仓库不会因启动时全量 LSP 扫描阻塞 UI；Map 中的每个符号和关系都有文件或 LSP 来源。

### P5-04 Diff 审核工作流

- [ ] Changes 成为一级入口，按文件展示 proposed、approved、applied、user-edited 和 drift 状态。
- [ ] 支持全屏 Diff、文件筛选、跳转、复制和与检查结果联动。
- [ ] 设计 hunk/file 接受、拒绝和编辑语义；任何调整都生成新的 ChangeSet revision 和 digest。
- [ ] 用户直接编辑导致 drift 时不沿用旧批准，明确提供重新计算、重新规划或取消。
- [ ] 并行 Agent 的 ChangeSet 继续串行集成并保留来源节点。

验收：UI 不存在“显示一个 Diff、实际应用另一个 Diff”的路径；部分接受后的验证针对真实最终工作树执行。

### P5-05 主题、响应式和可访问性

- [ ] 将硬编码颜色抽取为版本化 Theme，并支持 dark、light、16/256 色和 no-color。
- [ ] 提供色盲友好状态表达，状态不能只依赖红绿颜色。
- [ ] 为窄终端、低高度、非 true-color 和不支持鼠标的终端建立降级测试。
- [ ] 快捷键可发现、可配置且冲突可诊断；所有核心流程只用键盘即可完成。
- [ ] 为中文、CJK 宽字符和 emoji 保持布局回归测试；国际化文本资源作为独立后续切片，不与主题重构耦合。

验收：核心任务流程在无鼠标、无颜色和窄终端环境中仍可完成；主题变化不影响选择、Diff 和审批含义。

### P5 退出门槛

- [ ] 任务创建、执行、审批、恢复、继续、搜索和导出的端到端路径通过。
- [ ] 对常见终端尺寸和三平台执行视觉及交互回归。
- [ ] 用户无需阅读完整聊天即可判断任务、变更和检查状态。

## 7. P6：受控扩展生态

P6 必须在 P4 的 Skill 安全模型和 P5 的可见性成熟后启动。该阶段不追求扩展数量，优先证明外部能力不会破坏现有可信边界。

### P6-01 MCP 与外部 Tool

- [ ] 定义外部 Tool 的来源、版本、输入 schema、数据访问类型、读写/网络/进程风险和恢复策略。
- [ ] MCP 连接按用户配置和 workspace/session 显式启用，不因仓库文本自动连接。
- [ ] `/tools` 展示所有当前实际可用工具、来源、Permission、健康状态和被禁用原因。
- [ ] 外部 Tool Result 继续通过 secret sanitizer、Artifact Boundary、Context 预算和产品安全投影。
- [ ] 覆盖断线、超时、重连、重复调用、结果不确定和崩溃恢复。

验收：未连接或未授权的 MCP Tool 不出现在模型工具集中；网络和写操作不能借 Skill 自动授权。

### P6-02 可扩展可信检查与 Git 写操作

- [ ] 允许项目或 Skill 声明候选检查，但由可信编译器生成固定 executable/args/cwd/env 和稳定 plan ID。
- [ ] 增加 stage、commit、create branch 等独立 Git Tool；每种操作有精确目标、审批、幂等和 drift 策略。
- [ ] 不提供任意 Git 子命令或 Shell 字符串入口。
- [ ] commit message 可由模型建议，但最终内容在执行前展示并进入 durable record。
- [ ] 删除文件、移动文件和大范围格式化采用独立的高风险策略。

验收：所有新副作用均可在执行前完整展示，重启后不会猜测结果并自动重放。

### P6-03 附件和模型能力协商

- [ ] 支持文本、图片和结构化附件的安全导入、大小限制、类型验证和 Artifact 存储。
- [ ] Provider/Model Picker 展示工具调用、图片、上下文、结构化输出和成本能力。
- [ ] 当前模型不支持附件时提供明确降级或切换建议，不静默丢弃内容。
- [ ] 附件来源和处理结果进入 Task Summary，但本机绝对路径不进入模型上下文。

验收：附件从选择、持久化、Provider 请求到 Transcript 投影的能力和隐私边界一致。

### P6-04 有来源的 Project Memory 实验

- [ ] 首版只保存候选事实，不自动写入权威项目规则。
- [ ] 每条事实包含来源文件/符号/commit、scope、置信状态、用户确认和最后验证时间。
- [ ] 明确区分代码事实、用户决定、编码规范和模型推测。
- [ ] 分支或 worktree 改变后重新验证；来源失效时标记陈旧而不是继续注入。
- [ ] 用户可以查看、确认、编辑、禁用和删除记忆。
- [ ] 与 `AGENTS.md` 冲突时只提示，不自动覆盖显式项目规则。

验收：未经来源或用户确认的模型推测不会进入长期 Context；删除记忆不会删除原始 Session 审计记录。

## 8. 明确暂不实施

- 不继续增加新的多 Agent 编排类型，直到真实任务指标证明现有策略的收益和主要缺口。
- 不实现多个 Agent 在同一 worktree 并发写入。
- 不允许 Skill、MCP 或项目文件注册任意 Shell 命令并自动执行。
- 不默认读取 `CLAUDE.md`、`.cursor/rules`、Copilot 指令等其他生态文件；未来只能显式导入并显示来源。
- 不把 Skill 正文、Project Memory 或 Repository Map 放入 system-role 信任通道。
- 不为了 Repository Map 首版引入向量数据库、远程索引服务或后台全仓库 embedding。
- 不在原地修改已经批准的 Plan 或 ChangeSet。
- 不把完整 IDE、编辑器插件、云端协作和 Skill 市场作为本路线图的前置条件。

## 9. 度量与决策门槛

每个阶段应使用真实任务记录以下聚合指标，且不得包含源码、Prompt 正文或 secret：

- 任务完成、失败、取消、恢复和 replan 比例。
- 首次有效修改前的步骤数和耗时。
- Tool error、重复调用和无进展终止数量。
- Context 使用、压缩次数、Artifact 读取和 Skill 加载开销。
- Direct、单 Workflow、串行多 Agent、并行只读和隔离写入策略的耗时、token、成本和成功率。
- 用户审批次数、拒绝原因和因信息不足返回修改的次数。
- Skill 自动选择被用户接受、替换或禁用的比例。
- Diff revision、drift、冲突和检查失败的比例。

进入下一阶段前至少回答：

1. 新能力是否减少了用户解释成本或重复操作？
2. 是否保持或加强了 Tool、Permission、Recovery 和 Prompt Injection 边界？
3. 是否造成不可接受的启动时间、Context 或持久化开销？
4. 是否有真实任务证据，而不只是演示路径通过？

## 10. 推荐实施切片

为控制风险，建议按以下垂直切片交付，而不是一次创建所有空接口和包：

1. `AGENTS.md` 标准化 + 指令产品 DTO + `/instructions`。
2. Context 分类统计 + `/context`。
3. 多行 Composer。
4. durable Workflow/Child Agent 树形投影。
5. 一个只包含内置 Skill 的端到端 `use_skill` 切片。
6. 项目级和用户级 Skill Catalog + `$skill-name` + `/skills`。
7. Skill 恢复、权限和恶意输入测试。
8. `/tasks` + Task Summary 搜索。
9. Repository Map 摘要 + `/map`。
10. Changes 一级页面 + ChangeSet revision。
11. Command Palette、设置和主题系统。
12. 单个只读 MCP Server 的受控试点，再决定是否扩展外部生态。

每个切片都必须包含可运行实现、产品行为测试、安全测试、架构依赖检查和用户文档；不得提前创建只为未来阶段占位的空包。
