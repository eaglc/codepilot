# CodePilot 产品补全路线图

状态：执行中
依据：当前 `internal` 与 `cmd` 源码；`_legacy/internal` 仅用于识别尚未迁移的能力，不作为新运行时依赖
更新规则：一项任务只有在实现、测试、静态依赖检查和必要文档全部通过后，才允许从 `[ ]` 改为 `[x]`

## 1. 当前可用基线

- [x] Provider-neutral LLM 消息、结构化 tool call/result、usage 与 stream 协议。
- [x] Agent run/step/tool loop；一个用户触发对应一个 Turn/Run，一次模型交互对应一个 Step。
- [x] Agent Entry/Record 共享 sequence 的 append-only journal。
- [x] 完整 tool call/result 进入上下文和滚动摘要，摘要独立持久化。
- [x] Coding Workspace、Worktree、Coding Session 与 Agent Session 分层持久化。
- [x] `read_file`、`list_files`、`search_code`、`git_status`、`git_diff`。
- [x] 可审批、可跨重启恢复并带文件漂移校验的 `apply_patch`。
- [x] 单栏命令行式 TUI、折叠 Tool Result、会话内 applied diff、持久失败消息。
- [x] OpenAI、DeepSeek、Ollama adapter 被隔离在 Provider 层；Eino 不进入 Agent、Coding Agent 或 TUI 接口。
- [x] `go test ./...`、`go vet ./...` 和 import 方向测试作为当前回归基线。

## 2. 执行原则

1. 通用 `llm/tool/contextmanager/agent/agent-session` 不认识 Coding、Git、TUI 或具体 Provider。
2. 工具只负责校验和执行；Agent 统一生成 activity、持久化 tool result 和执行记录。
3. TUI 只依赖 `codingagent.Snapshot/Event/Service` 产品协议，不读取 Provider、Agent 或 Store DTO。
4. 任何副作用必须在执行前完成可信范围校验；需要审批的动作必须能在重启后继续验证和执行。
5. 不自动重放结果不确定的工具；恢复策略必须遵守 `ReplayNever/ReplaySafe/ReplayIdempotent`。
6. 不为未来任务创建空包；每个新包必须同时提供可运行实现和测试。
7. `_legacy/internal` 保持只读备份，删除需要单独授权。

## 3. P0：正常使用与安全闭环

### P0-01 持久化 Provider Profile

- [x] 新增版本化 Provider Profile 文件仓库，只保存非敏感配置。
- [x] 支持 Load/List/Save、原子替换、稳定排序、严格字段校验和损坏文件报错。
- [x] App 不再每次仅在内存中重建 Profile；内建 Profile 只负责首次初始化或补齐缺失项。
- [x] 测试覆盖重开恢复、更新、无效 ID、重复/损坏数据和“不落盘 Credential”。

验收：重启后自定义 Base URL、默认模型和 Profile 选择仍存在；配置文件中没有 API Key。

完成记录：2026-08-23；`go test ./...` 与 `go vet ./...` 通过。下一项：P0-02。

### P0-02 安全 Credential Store

- [x] 实现系统 Keyring Credential Store。
- [x] 实现环境变量只读 Store 与有序组合 Store。
- [x] Keyring 不可用时返回可理解错误，不把 secret 写入配置、event、journal 或 UI error。
- [x] 提供保存、覆盖、删除 Credential 的产品用例和测试替身。

验收：API Key 只存在于 Keyring/进程内存；Provider 使用后擦除临时副本。

完成记录：2026-08-23；Keyring 优先、显式环境变量回退已接入 App，`go test ./...` 与 `go vet ./...` 通过。下一项：P0-03。

### P0-03 Provider 预检与产品错误

- [x] 对 Profile、Credential、Base URL 和目标模型执行显式预检。
- [x] Ollama 检查服务与本地模型；远程 Provider 检查认证和模型可用性。
- [x] 统一产品错误码：未配置、凭证缺失、连接失败、认证失败、模型不存在、限流、超时。
- [x] 失败在 CLI/TUI 中持久、可操作，不泄漏 SDK 类型或 secret。

验收：用户在发送 Prompt 前即可知道配置是否可用。

完成记录：2026-08-23；App 启动前执行 10 秒有界预检，运行时 SDK 错误使用同一安全分类，`go test ./...` 与 `go vet ./...` 通过。下一项：P0-04。

### P0-04 TUI Provider/Model Picker

- [x] 列出、新建、编辑 Provider Profile。
- [x] Provider 采用两段式配置：缺少 Credential 时回车只录入 API Key，保存成功后再加载模型；已配置时直接进入模型列表，并可用 `k` 单独替换 API Key。
- [x] 安全录入 Credential；输入内容不回显、不进入历史和日志。
- [x] 模型列表合并远端发现结果、Profile 默认模型和当前 Session 模型；目录请求失败时仍展示已保存模型。
- [x] 选择模型后先执行认证、接口与目标模型权限预检，通过后才持久化到 Coding Session。
- [x] 未配置 Provider 时自动进入 Picker，而不是静默选择不可用 Ollama。

验收：不退出 TUI 即可完成 Provider 配置、验证和模型切换。

完成记录：2026-08-23；支持启动自动进入与 `/provider`、`/model` 手动进入，15 秒有界模型发现，两段式 API Key 流程、已配置模型恢复展示和选择前权限检测均已覆盖测试。下一项：P0-05。

### P0-05 审批前 Proposed Diff

- [x] `PendingInterrupt` 增加产品安全的 typed proposed change，而不是暴露原始 interrupt payload。
- [x] TUI 审批区展示文件列表、增删行和完整可滚动 Diff。
- [x] 审批后仍执行 digest、原始参数和 worktree drift 复核。
- [x] 支持 allow/deny/cancel，决定与最终 applied diff 都进入 durable journal。

验收：用户在任何写入发生前能看清准确修改内容。

完成记录：2026-08-23；`coding_patch_approval_v1` 只向产品层投影白名单字段，hash/digest/原始参数不穿透，`go test ./...` 与 `go vet ./...` 通过。下一项：P0-06。

### P0-06 可信 Run Checks

- [x] 识别 Go、Python、Node 项目并生成可信 `plan_id`。
- [x] Tool 只接受 plan ID，不接受模型拼接 shell 命令。
- [x] 固定 cwd、环境变量白名单、超时、输出上限和子进程取消。
- [x] 首批支持 Go Test/Vet/Build；随后支持 Python/Node 对应检查。
- [x] Tool output 默认折叠；失败摘要和详细日志可展开，超大输出进入 Artifact Store。

验收：Agent 修改代码后能够运行受控检查并把结果反馈给模型和用户。

完成记录：2026-08-23；所有计划执行前均显示固定命令并审批，Windows/Unix 进程组取消均有实现，`go test ./...` 与 `go vet ./...` 通过。P0 全部完成；下一项：P1-01。

## 4. P1：恢复、会话与长期稳定性

### P1-01 RecoveryPlan 与 RecoveryCoordinator

- [x] 将 Pending Run/Tool/Interrupt 转成 typed RecoveryPlan。
- [x] `ReplaySafe` 重新校验后可重试；`ReplayIdempotent` 必须复用原 key；`ReplayNever` 不自动重放。
- [x] 支持“确认已执行、标记失败、人工重试、放弃 Turn”等产品决策。
- [x] App 启动时运行 RecoveryCoordinator；TUI 提供恢复界面。
- [x] 覆盖每个 Entry/Record 写入间隙的崩溃注入测试。

验收：启动协调器只自动处理可证明安全的 Tool 边界，并在继续模型对话前停止等待用户；所有恢复决定可重复构建、可审计且不向产品层泄漏 Tool 参数或幂等键。

完成记录：2026-08-24；覆盖 operation/user/step/assistant/tool/interrupt/result/finish/terminal 写入边界、真实文件 journal 启动恢复、产品投影和 TUI 决策测试，`go test ./...` 与 `go vet ./...` 通过。下一项：P1-02。

### P1-02 跨进程 Writer Lease

- [x] State root 或 Session 获取跨进程文件锁。
- [x] 锁包含 owner、时间和安全诊断；禁止两个 writer 同时追加 journal。
- [x] 异常退出后可安全重新获取，不通过删除未知锁文件强行恢复。

验收：App 组合根在打开任何可写 Repository 前持有 State root 独占 OS 文件锁；冲突错误只暴露有界 owner、PID、主机和获取时间诊断。正常关闭与进程被终止均可重新获取，锁文件和 owner 元数据不会以删除未知文件的方式“抢锁”。

完成记录：2026-08-24；覆盖同进程双实例、App 双实例、锁 owner 诊断、幂等关闭、子进程异常退出后的重新获取，`go test ./...` 与 `go vet ./...` 通过。下一项：P1-03。

### P1-03 跨仓库一致性与修复

- [x] Coding Session 与 Agent Session 创建失败时形成可恢复事务意图。
- [x] 检测并列出 orphan Agent Session、悬空 Coding Session 和缺失 Worktree。
- [x] 提供只读诊断与显式 repair 命令。

验收：Session 创建先持久化版本化、确定性事务意图，再依次确保 Agent/Coding 两侧存在并标记完成；`codepilot doctor` 在排除 writer 后只读对账，`codepilot repair` 只完成可证明的创建意图或做可逆归档，不删除 journal、Session 目录和 Worktree 记录。

完成记录：2026-08-24；覆盖意图后、Agent 写入后、Coding 写入后的中断恢复，真实文件重开，orphan/悬空/缺失 Worktree 诊断，未知 journal 拒绝接管和 CLI repair；`go test ./...`、`go vet ./...`、`git diff --check -- internal cmd docs` 通过。下一项：P1-04。

### P1-04 Session 产品管理

- [x] Coding Service 支持 Create/List/Switch/Rename/Archive。
- [x] TUI Session Picker、快捷键和 `/new`、`/session` 命令。
- [x] 支持从历史 Entry fork 新 lane，并投影正确分支 transcript。
- [x] 会话切换不会串用 live delta、approval 或 tool activity。

验收：Coding Service 返回产品 `Session/Snapshot` 并发布层级隔离后的 session activated/updated 事件；TUI 可用 `/session` 或 Ctrl+S 打开同 Worktree Picker、用 `/new [title]` 创建并切换。`/fork` 打开当前分支的历史消息选择器，由 TUI 将所选消息映射到来源 Entry，并将确定性 `ActiveLane` 持久化，后续 Turn 继续写入该 lane。切换时 TUI 增加 generation 并清空 live delta、approval、tool activity、输入历史等瞬态状态，旧 generation 或旧 Session 事件无法覆盖新 Snapshot。

完成记录：2026-08-24；覆盖 Create/List/Switch/Rename/Archive 生命周期、活动 Session 归档保护、历史 Entry fork 与分支继续、Picker `/new` `/session`、Ctrl+S 及旧 snapshot/turn/event 隔离；`go test ./...`、`go vet ./...`、`git diff --check -- internal cmd docs` 通过。下一项：P1-05。

### P1-05 Context 与 Artifact Store

- [x] 模型元数据提供真实 context window 与 tokenizer。
- [x] 大 Tool Result、命令输出和 Diff 外置为 content-addressed Artifact，并向模型提供有界引用。
- [x] 单个当前 Turn 超限时提供明确降级策略。
- [x] 摘要失败可回退到安全裁剪或备用摘要模型。
- [x] 增加摘要事实一致性、版本升级和 golden eval。
- [x] 原始 journal 归档/冷存储策略不破坏审计与重新摘要。

验收：`llm.Model` 携带 context window、max output 和带来源的 tokenizer 身份；OpenAI/DeepSeek 使用版本化公开能力目录，Ollama 从 `/api/show` 读取本机模型信息，未知模型明确回退而不伪造精确能力。Context Manager 使用模型输入预算、计入 Tool schema，并把同一活动 Turn 作为不可拆分保护块；活动 Turn 自身超限时返回 `CurrentTurnTooLargeError`。Coding ToolFactory 统一外置超限文本 Result，不要求具体 Tool 写 Artifact 逻辑；完整规范化 Result 以 SHA-256 私有保存，journal 和模型只收到有界预览及无路径引用。摘要对 tool、artifact 和 changed-file 事实做一致性校验，失败时只裁最老完整 Turn；P1-09 已进一步将格式升级为 v4 信任隔离。Session Archive 创建确定性 `tar+gzip` 冷副本但不旋转或删除在线 journal。

计量边界：Provider/版本目录给出真实模型上限和 tokenizer 身份；当前本地 `ByteTokenizer` 仍是保守估算，并额外预留输出与 5% framing margin。没有精确计数接口的 Provider 不会被标记成“精确 token 数”。这不影响结构安全；未来可在同一能力边界增加 Provider-side exact counter。

完成记录：2026-08-24；覆盖模型能力发现与缓存、动态预算与 Tool schema、当前 Turn 原子超限、大 Result Execute/Resume 外置、Artifact 摘要校验读取、摘要失败安全裁剪、关键事实与 golden eval、版本化缓存隔离、不可变 journal 冷归档和归档后继续恢复。`go test ./...`、`go vet ./...`、`git diff --check -- internal cmd docs` 和两个 CLI 入口构建均通过。下一项：P1-06。

### P1-06 Agent 重试与预算

- [x] 针对限流、瞬时网络错误和服务端错误实现有界退避重试。
- [x] 真正产生现有 `retry_scheduled` 事件。
- [x] 增加 token、费用、tool call 数、重复调用和总输出预算。
- [x] 检测无进展循环并生成明确终止原因。
- [x] Coding Service 提供显式 Cancel API，不只依赖 TUI 本地 Context。

阶段记录：模型调用使用总 Turn timeout 内的有界指数退避，默认最多 3 次；只重试尚未产生可见 delta 的标准 transient error，不重试 Tool 或部分输出流。`retry_scheduled` 经 Agent→Coding 显式事件映射提供稳定 reason/attempt/delay。单元测试覆盖两次失败后成功、delay cap、逻辑 Step 不重复记账，以及部分流失败零重试。

验收：token/output token/cost 来自 durable Usage record，总 assistant output bytes 来自 message Entry，Tool 总数和规范化 `(name,args)` 重复数来自 ToolStarted record；Run/Resume/Recovery 共享同一 journal 额度。超预算 assistant Tool Call 会写 cancelled Tool Result 后以稳定 reason 结束，保证消息结构与 RecoveryPlan 完整。连续完整 Step 指纹相同或连续全 Tool error 达阈值时分别以 `no_progress_repeated_step` / `no_progress_tool_errors` 结束。Coding Service 持有 active-turn cancel handle，`CancelTurn` 幂等触发，Agent 持久化 aborted terminal，产品层发布 `turn_cancelled`；TUI Ctrl+C 调用该 API。

完成记录：2026-08-24；聚焦及全仓测试、`go vet ./...`、`git diff --check -- internal cmd docs` 与两个 CLI 入口构建均通过。下一项：P1-07。

## 5. P1：安全策略

### P1-07 细粒度 Permission/Approval

- [x] 支持 allow once、allow session、按 tool/path/action 授权。
- [x] Grant 有明确作用域、过期时间和 durable audit，不存 secret。
- [x] `auto_edit` 仍受禁止目录、文件类型、大小和变更范围约束。

授权语义：`allow once` 只形成当前 Agent interrupt 的 durable resolution，不产生可复用授权；`allow session` 只能从匹配 Turn/Interrupt/ToolCall 的未决 journal 事实派生。Session Grant 只保存稳定 ID、`tool/action/paths`、来源 Turn/Interrupt、创建/过期/撤销时间，不保存 Tool 参数、patch、命令输出、Result 或 Credential；默认 8 小时，最大 24 小时。`apply_patch` 按精确 worktree-relative path 集合匹配，`run_checks` 进一步按固定 plan ID 匹配；过期、撤销、tool/action/path 不匹配均重新审批。

`auto_edit` 和 session grant 都只对自动策略内的 patch 生效：默认不超过 256 KiB、20 个文件、2000 行变更和 1 MiB 单文件，并排除 Git/CodePilot 元数据、CI workflow、依赖/构建目录和二进制/归档/媒体文件类型；超出自动范围会回到一次性审批，仍不绕过统一 diff、Git dry-run、worktree/symlink、hard size/file count 与 before-state 漂移检查。

完成记录：2026-08-24；精确授权、过期/撤销、session grant journal 派生与同 Turn 复用、文件/内存后端持久化隔离、TUI `[y] allow once` / `[s] allow session`、check plan 隔离和 auto-edit 禁止目标测试通过。全仓质量门见本阶段最终验收。下一项：P1-08。

### P1-08 Secret 与敏感文件策略

- [x] 识别 `.env`、私钥、Credential 文件和用户配置的敏感路径。
- [x] 读取前阻止或审批；输出与错误做 secret redaction。
- [x] 明确 Tool 参数和 Result Details 的持久化敏感度策略。
- [x] System Prompt 默认不发送不必要的本地绝对路径。

敏感路径由 Coding 层统一判断，默认覆盖 `.env*`（示例模板除外）、私钥/证书密钥、常见 Credential/Auth 文件以及 `.ssh/.aws/.kube/.azure` 等目录；用户可重复传入 `--sensitive-path`，规范化后的 worktree-relative 文件或目录前缀持久化到 Coding Session。目录和搜索默认跳过敏感内容，显式 `read_file`/`git_diff` 读取必须逐次审批且不提供 session grant；批准后仍对结果脱敏。敏感文件写入以及 patch 中可识别的 secret 直接拒绝，不生成可绕过的审批。

持久化策略采用“执行数据与可持久化数据分流”：Tool 在当前调用内获得模型生成的原始参数以完成执行，但 Agent 注入的 `DataPolicy` 会在 assistant tool-call、`tool_started.effective_args`、tool-result message、Result Details、progress、错误和事件进入 journal/context/UI 前递归脱敏；未发生变化的安全 JSON 保留原始字节，继续满足 digest/recovery 语义。interrupt payload 为恢复完整性保留精确字节，因此 Coding Tool 必须在创建 interrupt 前拒绝含 secret 的写请求，产品投影也永不暴露原 payload/digest。检查命令的大输出会先脱敏再保存 Artifact。模型文本分片在终止边界合并后再脱敏，避免一个密钥跨多个 delta 泄漏。

System Prompt 只发送稳定 Workspace/Worktree ID 和“路径必须相对当前 worktree”的规则，不再嵌入本机绝对路径；仓库内容同样被要求不得用于请求或泄露 Credential。

完成记录：2026-08-24；默认/自定义敏感路径、读取审批、搜索排除、敏感 patch 拒绝、Tool/Agent/Artifact/事件全链路脱敏、跨 delta secret、文件/内存持久化隔离、TUI 提示和 CLI 配置测试通过。下一项：P1-09。

### P1-09 仓库指令与 Prompt Injection 边界

- [x] 将仓库内容视为不可信数据，而不是系统指令。
- [x] 项目级 agent instruction 有明确文件名、层级、作用域和展示来源。
- [x] 外部内容不能修改 Permission、Provider、恢复和持久化策略。

信任通道被结构化分离：Coding system prompt 只包含产品写死的安全规则和已注册 Tool 名称，不包含任何仓库文件内容；普通源码、注释、依赖、生成文本、Web 内容和 Tool Result 均被声明为不可信数据。项目 guidance 只认大小写精确的 `AGENTS.md`，根文件作用于整个 worktree，嵌套文件只作用于其目录后代，按根到叶顺序携带 `source/scope/sha256/content` JSON 来源。更深层只可覆盖其作用域内的编码、构建和测试惯例，不能增加用户意图或授权。

加载器不跟随 symlink，跳过 Git/CodePilot 元数据、依赖、虚拟环境和构建产物目录，也跳过内建/用户敏感路径；最多扫描 50,000 个目录项、接受 32 个文件、单文件 16 KiB、总内容 64 KiB，超限或非 UTF-8 明确失败而不是静默截断。JSON 编码阻止仓库文本逃逸数据包络，secret redaction 在进入上下文前执行。本机绝对路径不出现在 prompt 或 guidance 来源中。

`AGENTS.md` 内容通过 Agent 的 `UntrustedContext` 以 user role 临时插入当前 Run 上下文，位于真实当前用户消息之前，参与模型预算和 DataPolicy，但不写 Agent journal、摘要缓存或 Coding Session；Resume/Recovery 重新从当前可信 Worktree 边界加载。Agent 强制该通道只能使用 user role，Provider adapter 无法把它转换成 system message。

滚动摘要同步升级为 `rolling-summary/v4`：摘要模型收到带 `trust=untrusted_conversation_data` 的结构化 JSON，而不是可伪造的行协议；摘要输出经 secret sanitizer 后才进入 cache/journal。使用时，它以 `trust=untrusted_derived_context` 的 user-role 历史数据加入上下文，原始 Coding system prompt 保持逐字不变；v3 cache 不会复用。Tool Registry、`ToolScope`、Permission Grant、Provider/model 选择、ReplayPolicy、RecoveryPlan 和存储 Repository 都由模型上下文之外的可信产品/Agent 代码决定，因此仓库文本没有可调用的策略修改通道。

完成记录：2026-08-24；覆盖 `AGENTS.md` 根/嵌套层级、来源与作用域、普通文件/排除目录/敏感目录忽略、大小限制、JSON delimiter 注入、system prompt 隔离、低信任上下文不落盘且脱敏、摘要输入/输出信任隔离、v3→v4 缓存隔离及恢复装配。下一项：P2-01。

## 6. P2：Coding 深度与 TUI 体验

### P2-01 Language 与 LSP

- [x] Go/Python/Node language strategy。
- [x] Definition、References、Diagnostics、Document Symbols。
- [x] LSP 生命周期、超时、崩溃重启与 worktree 隔离。

Language Registry 只用有界、只读的 worktree 根标记和源码文件识别 Go、Python、Node/TypeScript；多语言结果按置信度和稳定 ID 排序，文件扩展名再选择对应 profile。每个 profile 固定扩展名、LSP `languageId` 与白名单 server 命令：`gopls serve`、`pyright-langserver --stdio`、`typescript-language-server --stdio`。仓库内容不能构造 executable、参数或环境变量，CodePilot 也不自动安装 server。

Coding ToolFactory 仅在当前 worktree 检测到支持语言且 Registry/Navigator 成对存在时注册 `find_definition`、`find_references`、`get_diagnostics`、`document_symbols`。输入路径必须是当前 worktree 内的普通 UTF-8 文件，不能是 symlink、敏感路径或不匹配该语言的扩展名；位置、文档大小、协议帧和结果数都有上限。定义/引用结果会丢弃 worktree 外 URI 并去重，错误只返回可降级提示，Agent 可继续使用 search/read。

LSP Manager 按精确 `worktree ID + 规范化 root + language + server program/args` 绑定并复用进程，不把进程、文档或 JSON-RPC 状态写入 Session。请求和初始化分别有超时，进程在一次查询中意外退出时最多自动重启一次；不同 worktree 不共享进程，binding 漂移直接拒绝，提供按 worktree 关闭和应用退出全量关闭。启动外部 server 即使在 `auto_edit` 下也先产生可恢复审批；`read_only` 不启动进程，session grant 只覆盖精确 `execute:lsp:<language>`。TUI 只看到白名单后的语言服务器审批信息，不接触 JSON-RPC 或底层流事件。

完成记录：2026-08-24；覆盖多语言检测、四类导航、协议初始化与文档同步、请求取消、超时、单次崩溃重启、进程复用、worktree/server binding 隔离、环境白名单、审批恢复与投影、App 生命周期关闭。下一项：P2-02。

### P2-02 Workspace 与 Git 工作流

- [x] Workspace Picker、重定位和不可用 Worktree 修复。
- [x] Git log/branch/commit 等只读能力；副作用操作必须单独审批。
- [x] 文件遍历尊重 `.gitignore`，避免无界扫描 vendor/node_modules。

Workspace/Worktree 继续使用稳定产品 ID 绑定 Session，但 Workspace 额外保存 `git-anchor-v1:<object-format>:<root-commit>` 锚点。锚点不包含 remote URL、凭证或源码；后续提交、分支和合并不会改变它，候选目录必须用 `git cat-file` 证明仍含该提交。空仓库没有可验证历史，因此不允许自动/显式迁移。每次加载和切换都会重新验证 root、Git dir、common dir 与锚点；状态只动态投影为 `available/unavailable/identity_changed`，不把瞬时错误写进 Session。

普通 `SaveWorktree` 仍禁止修改路径；只有显式 `RelocateWorktree` 能以 expected-root CAS 更新 root/Git dir/common dir。目标必须是未占用的有效 Git worktree、原绑定必须已不可用、历史锚点必须匹配，写入可幂等重跑；重定位和跨 worktree 切换前先关闭对应 LSP。启动时若只发现一个匹配的不可用绑定，CLI 展示旧/新路径并询问是否恢复原 Session，也提供 `--relocate-worktree`/`--skip-relocation`；TUI `/workspace` 列出全部状态，键盘选择或 `r` 输入新路径修复。UI 只调用 Coding 产品 DTO/API，不接触 Store 或 Git adapter。

新增 `git_log`、`git_branches`、`git_show_commit`。历史条数最多 50，分支工具只读取 refs，提交查询只接受完整 40/64 位十六进制对象 ID且使用 `--no-patch`；没有 checkout、branch 创建/删除、stage、commit 创建入口。未来任何 Git 写操作仍必须作为独立可恢复 Tool 定义审批、漂移和幂等策略，不能借这些只读工具扩权。

统一 `workspace.IndexFiles` 使用 `git ls-files --cached --others --exclude-standard -z` 解析根/嵌套 `.gitignore`、`.git/info/exclude` 和 Git exclude；只接受有界 UTF-8 相对普通文件，不跟随 symlink。`vendor/node_modules`、虚拟环境、缓存、构建产物与产品元数据目录即使已跟踪也默认跳过；20 秒、100,000 文件和 4,096 字节路径上限防止无界扫描。`list_files`、`search_code`、源码语言识别和 `AGENTS.md` 发现共用该索引，敏感路径过滤发生在计数之前。

完成记录：2026-08-24；覆盖启动单候选恢复、Picker 修复/切换、路径删除/移动/Git 身份替换、不同历史拒绝、空仓库拒绝、CAS/idempotent 持久化、LSP 关闭、Git 只读参数约束、`.gitignore`、tracked vendor/node_modules、symlink、过滤后限额和架构依赖方向。下一项：P2-03。

### P2-03 TUI 完整体验

- [x] `/help`、`/model`、`/permissions`、`/new`、`/session`、`/clear`。
- [x] Markdown、代码块和语法高亮。
- [x] Tool 折叠、审批和 Picker 全部支持键盘导航。
- [x] Token、费用、耗时、Step、Context 占用展示。
- [x] 输入历史持久化和安全清理。
- [x] Event Bridge 不在持锁状态阻塞发送；支持 delta 合并、关闭和背压测试。

命令在 TUI 内先由白名单路由，未知 `/...` 只显示持久错误，绝不作为 prompt 进入 Agent。`/permissions` 通过 Coding Service 修改当前 Session；模式变化会持久化并撤销仍有效的 session grant，避免切回旧模式时静默复用授权。`/clear` 不删除 journal，而是创建并切换到同 Workspace/Worktree/Provider/Model/Permission 的空白 Session，因此原会话仍可在 `/session` 中恢复。

assistant 的 durable Markdown 使用 GFM 终端渲染和 Chroma 围栏代码语法高亮；渲染按消息、内容和终端宽度有界缓存，失败时回退到普通安全换行。Tool Result 仍默认折叠，applied diff 保持时间线内直接可见；鼠标点击和 `Tab` 选择、`Enter/Space` 切换、左右方向键展开/折叠均可用。审批支持 `y/s/n/c/a` 决策和 PageUp/PageDown 滚动，Workspace、Session、Provider、Permission Picker 均可全键盘完成。

`codingagent.Snapshot.Metrics` 从 active lane 的 durable Entry/Record 投影：累计 Token/费用来自 Usage record 与摘要 usage，Context 是最近一次模型调用的 input token，Step 和耗时来自最近 Turn 的 operation/step 时间线；其他分支的 usage 不计入当前视图。Provider 未报告费用时显示 `Cost n/a`，不会伪造零费用。输入历史不另建重复明文文件，而从当前 Session 的 durable user transcript 恢复，最多 100 条、单条 32 Ki rune；assistant、tool、slash command 与 API Key 不进入历史。切换或 `/clear` 从目标 Snapshot 重建历史，composer 和 Provider credential 的可变缓冲区在离开时主动清零。

Event Bridge 使用有界内部队列和单独 delivery 协程。发布方只在锁内检查关闭状态、合并相邻同 Session/Turn assistant delta 或入队，实际 channel send 永远发生在锁外；满队列形成可取消背压，关闭信号同时唤醒 delivery 和所有等待发布方。单个合并 delta 最多 64 KiB，防止慢消费者导致无界字符串增长；关闭幂等且不会被无消费者或背压发布者锁死。

完成记录：2026-08-24；覆盖命令白名单、权限持久化与授权撤销、clean-session 语义、durable 历史恢复和内存清零、Markdown/代码高亮、Tool 键盘折叠、usage/step/timing 分支投影、Event Bridge delta/顺序/背压/关闭。`go test ./...`、`go vet ./...`、差异检查与两个 CLI 构建通过。下一项：P2-05。

### P2-05 发布与质量

- [x] OpenAI、DeepSeek、Ollama 可选集成测试。
- [x] Go/Python 真实 Coding E2E、崩溃注入和多进程测试。
- [ ] Windows/Linux/macOS TUI 与存储测试。
- [x] 版本、变更日志、可复现构建、安装包和升级策略。

实施拆分：

- [x] P2-05A：增加显式环境开关的真实 Provider catalog/complete/stream 探测；标准测试永不消耗 API 或依赖本机 Ollama。
- [x] P2-05B：以真实临时 Git 仓库、真实 Coding Tools/Store 和确定性模型完成 Go/Python 修复 E2E；在进程边界注入崩溃并验证恢复、lease 与多 writer。
- [x] P2-05C：建立 Windows/Linux/macOS CI matrix，分别执行全测试、静态检查、CLI 构建和关键 TUI/文件存储测试；增加 race/跨编译检查但不伪装成未实际运行的平台测试。
- [x] P2-05D：统一版本注入与 `--version` 产物，增加变更日志、发布配置、checksums/SBOM、可复现构建说明、安装/升级/回滚策略和发布前自动验收。

P2-05A 使用 `CODEPILOT_LIVE_OPENAI/DEEPSEEK/OLLAMA` 三个独立显式开关；只存在 Credential、endpoint 或本机服务不会触发网络。每个启用项依次经过真实 catalog、adapter 创建、complete 和 stream 收敛，远程请求次数与计费风险已单独记录在 [真实 Provider 可选集成测试](../guides/live-provider-integration-tests.md)。常规测试验证“零开关即零网络”，真实请求只允许在受保护的发布环境中按需执行。

P2-05B 的 Go/Python E2E 不 mock Tool 或 Store：测试先提交一个真实失败仓库，确定性模型通过真实 Agent Runtime 和 Coding Service 调用真实 `apply_patch`，第二个模型 step 必须收到持久化 Tool Result；修复后分别执行 `go test ./...` 与 Python `unittest`，并重开 Agent file store 验证完整 Tool started/result/finished 与空 RecoveryPlan。Python 不存在时本地可跳过，跨平台 CI 使用 `CODEPILOT_REQUIRE_PYTHON_E2E=1` 把缺失运行时变成失败。

崩溃测试由独立子进程写入半条 journal JSON、同步后 `os.Exit(73)`，父进程验证重开只忽略尾残片，下一次 append 先截断残片再从正确 sequence 继续。既有子进程持有 StateDir lease 后被强制终止的测试同时证明：活跃第二 writer 被拒绝、异常退出由 OS 释放锁、恢复方不删除锁文件或猜测 stale owner。跨 Repository 创建事务的各写入边界和 App 启动 Recovery Coordinator 继续由既有注入测试覆盖。P2-05B 完成记录：2026-08-24；聚焦 E2E/子进程测试、`go test ./...`、`go vet ./...` 和差异检查通过。

P2-05C 已新增 `.github/workflows/ci.yml`。三个原生 runner 都安装 Go（从 `go.mod` 解析）和固定 Python 3.13，强制 Python E2E，执行非缓存全测试、TUI/SessionStore 三轮稳定性测试、`go vet` 与本机 CLI 构建；Linux 另跑 stateful core 的 race detector。独立 job 对 Linux/Windows/macOS 的 amd64/arm64 六个目标执行 `CGO_ENABLED=0` 交叉链接。架构测试解析 workflow YAML 并锁定三平台、E2E、race、TUI/Store、vet/build 和“常规 CI 不启用真实 Provider”门禁。

本机 Windows 已按 CI 参数完成全测试（含强制 Python E2E）和三轮 TUI/Store 测试；六目标交叉链接也通过。但这两项不能证明 Linux/macOS 原生语义，所以上方“Windows/Linux/macOS TUI 与存储测试”主项保持未勾选，必须等 workflow 在 GitHub-hosted 三平台 runner 上实际成功后再完成。P2-05C 这里的勾选仅表示 CI 实现与本机可验证部分完成，不伪造远端执行结果。

P2-05D 将 CLI identity 统一为 version、commit、commit timestamp 三段编译期元数据；`--version` 不启动 App，并对异常控制字符和超长值做有界处理。`.goreleaser.yml` 只构建 `CGO_ENABLED=0` 的 Linux/Windows/macOS amd64/arm64 六目标，以 `-trimpath -buildvcs=false -buildid=` 和 commit 时间消除宿主路径、自动 VCS 状态与当前时钟，输出 Windows ZIP、其他平台 tar.gz、SHA-256 清单及归档级 SPDX JSON SBOM。归档内 binary、README、LICENSE、CHANGELOG 和升级说明统一使用 commit mtime。

`.github/workflows/release.yml` 只响应 `v*` Tag；verify job 只有只读权限，先跑完整离线测试、Python E2E、vet，再由 `cmd/releasecheck` 校验严格 SemVer、完整 commit、commit 时间、干净工作树和带日期 CHANGELOG，最后在两个独立目录真正构建两轮六目标并逐字节比较。只有 verify 成功后，release job 才取得 `contents: write`，使用锁定的 GoReleaser v2.17.1 与 Syft v1.51.0 发布。Tag 流程不会打开真实 Provider 开关。

安装、checksum 核验、显式升级、ConfigDir/StateDir 备份、`doctor` 验证和成套状态回滚见 [发布、安装、升级与回滚](../release-and-upgrade.md)；变更格式见仓库根 `CHANGELOG.md`。本机已用两轮共十二次真实构建确认六目标裸二进制分别一致，并用官方 GoReleaser v2.17.1 二进制通过配置 schema 检查。P2-05D 完成记录：2026-08-24；聚焦与全量测试、`go vet ./...`、只读 tidy diff、差异检查、两个 CLI 构建和版本输出均通过。没有创建 Tag、推送代码或声称已经发布制品。

## 7. 当前执行位置

P0-01 至 P0-06、P1-01 至 P1-09、P2-01 至 P2-03、P2-05A/B/C 的实现以及 P2-05D 已完成。P2-05 唯一未关闭项是 GitHub-hosted Windows/Linux/macOS 原生 runner 的实际成功记录；在未推送并实际运行前保持未勾选。
