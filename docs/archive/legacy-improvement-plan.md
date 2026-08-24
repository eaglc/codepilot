# CodePilot 改进建议与修复计划

> 文档版本：v0.2
> 状态：P0 已全部修复（UI-1 / UI-2 / NAM-1 已实现）；P1 已全部修复（UI-3 / UI-4 / DOC-1 / DOC-2 / DOC-3 已实现）；P2 依赖项已修复（DEP-1 / DEP-2 已实现）
> 更新日期：2026-08-21
> 来源：MVP 架构与代码评审（模块划分 / 依赖关系 / 可维护性 / 代码风格 / UI 交互）

## 1. 文档目的

本文档记录 CodePilot MVP 代码评审发现的问题，并给出统一的修复优先级、修复方案和修复后的验证方法，作为后续迭代的实施依据。

评审结论一句话：**架构与代码质量处于优秀水平，主要短板集中在终端交互可用性（滚动 / 换行 / 输入）与一个命名错误，而非结构设计本身。**

## 2. 问题总览

优先级定义：

- **P0**：阻塞级，影响正常使用或违反语言硬性规范，必须先修。
- **P1**：重要，影响体验或一致性，应在下一迭代修复。
- **P2**：结构 / 健壮性改进，降低长期维护风险。
- **P3**：增强项，可选。

| ID | 优先级 | 类别 | 问题 | 状态 | 位置 |
| --- | --- | --- | --- | --- | --- |
| UI-1 | P0 | UI 交互 | 对话与 Diff 完全无滚动，无法回看历史输出 | ✅ 已修复（§3.4） | `view.go` / `update.go` |
| UI-2 | P0 | UI 交互 | 对话无自动换行，长行被静默截断 | ✅ 已修复（§4.4） | `conversation.go` |
| NAM-1 | P0 | 命名 | `CheckPoint` 单词内大写，违反 Go 惯例 | ✅ 已修复（§5.4） | `agent/checkpoint.go` |
| UI-3 | P1 | UI 交互 | 输入框无光标移动 / 历史补全 / 词删除 | ✅ 已修复（§6.4） | `update.go` / `composer_edit.go` |
| UI-4 | P1 | UI 交互 | Picker 列表无滚动窗口，光标移出屏幕 | ✅ 已修复（§7.4） | `picker_window.go` |
| DOC-1 | P1 | 文档 | 13 个包缺 `// Package` 注释 | ✅ 已修复（§8.4） | 全 `internal/` |
| DOC-2 | P1 | 文档 | 4 个 provider 适配器 20 个导出方法无注释 | ✅ 已修复（§9.4） | `openai.go` 等 |
| DOC-3 | P1 | 文档 | `ProviderPicker*` 9 个常量、`ErrStoreClosed` 无注释 | ✅ 已修复（§10.4） | `provider_picker.go:19-27` |
| DEP-1 | P2 | 依赖 | `LanguageID` / `LanguageProfile` 放错包，致 `language` 反向依赖 `agent` | ✅ 已修复（§11.4） | `agent/ports.go` |
| DEP-2 | P2 | 依赖 | `ModelRef` 放错包，致 `provider` 反向依赖 `agent` | ✅ 已修复（§12.4） | `agent/ports.go:302-307` |
| ROB-1 | P2 | 健壮性 | 5 处真实错误被 `_ =` 吞掉 | ✅ 已修复（§13.4） | `session/service.go:727,746` 等 |
| DEP-3 | P2 | 结构 | `session.Service` 编排过大（~1247 行） | ✅ 已修复（§14.4） | `service.go` → `service_turn.go` / `service_clone.go` |
| DEP-4 | P2 | 结构 | `ListWorkspaceFiles` 靠类型断言访问可选能力，脆弱 | ✅ 已修复（§15.4） | `ports.go` / `service.go` |
| UI-5 | P3 | UI 增强 | 无浅色主题 / `NO_COLOR` 支持，硬编码深色背景 | 待修复 | `view.go:107` |
| UI-6 | P3 | UI 增强 | 宽屏下焦点指示近乎无意义 | 待修复 | `view.go:119,127` |
| DEP-5 | P3 | 结构 | `contextmanager` 包在 MVP 仅为 Nop 占位 | 待修复 | `build.go:146` |

---

# 第一部分：P0（必须修复）

## 3. UI-1 对话与 Diff 无滚动

### 3.1 问题与影响

对话面板和 Diff 面板都没有任何滚动能力（`grep -r 'scroll|PageUp|PageDown|KeyUp|KeyDown' internal/ui` 仅命中 picker 光标移动）。`tailBoundedLines`（`view.go:453-466`）始终把 `lines[0]`（第一条消息）钉在顶部，然后只显示末尾 `height-1` 行，结果既不是真尾部也不是真顶部：**早期 AI 输出完全无法查看**。

### 3.2 修复方案

1. 在 `ui.Model` 中为对话与 Diff 各自维护一个滚动偏移量（`conversationScroll int`、`diffScroll int`），并在 `model.go` 的会话/事件刷新路径上做越界钳制（内容变短时回卷到末尾）。
2. 新增按键：`↑/↓` 逐行、`PgUp/PgDn` 翻页、`Home/End` 到顶/到底；仅在对应面板获得焦点时生效。
3. 将 `tailBoundedLines` 改为「基于偏移量切片 + 底部提示」：当前不在末尾时显示 `(↑ more)` 之类的指示，滚动到底后恢复自动跟随新输出。
4. 有长行时先依赖 UI-2 的换行再按「行」滚动，避免滚动单位不一致。

### 3.3 验证方法

- 单测：为新的滚动状态机写表驱动测试，覆盖「内容增长时保持在尾部」「上翻后新内容不打断阅读」「内容缩短时偏移量回卷」三组用例。
- 手动：`go build -o codepilot.exe ./cmd/codepilot`，发起一个输出超过一屏的 turn，验证 `↑/↓/PgUp/PgDn` 能回看完整输出，滚到底后新 token 自动跟随。

### 3.4 实现记录（已修复）

- `Model` 新增 `conversationScroll int` / `diffScroll int` 两个字段，以 `scrollFollowBottom = -1` 哨兵值表示「跟随最新内容」，`NewModel` 初始化为该值（流式输出自动滚到底）。
- 抽取 `Model.layout()`（`view.go`），统一计算扣除 footer 后的 `BodyHeight`，供渲染与滚动处理共用，避免宽度/高度计算不一致。
- 抽出 `conversationPanel` / `diffPanel` 与 `conversationContentWidth`（`view.go`），使面板内容宽度、滚动偏移量在渲染与按键两条路径上一致。
- `renderPanel` 增加 `offset int` 参数，改用 `windowLines`（按偏移量切片）替代并删除了原 `tailBoundedLines`；新增纯函数 `resolveScroll` / `windowLines` / `clipLines` / `clampInt`。
- `handleScrollKey`（`update.go`）处理 `↑/↓`（逐行）、`PgUp/PgDn`（翻页，10 行）、`Home/End`（到顶/到底）；放置在 `inputBusy` 判断**之前**（流式输出时仍可回看历史），并以 `!m.completion.active()` 门控（补全菜单打开时方向键仍用于选择）。
- 会话切换（`sessionLoadedMsg` 检测 `Session.ID` 变化）与切换 diff 类型（`executeDiffCommand`）时重置滚动到跟随末尾。
- **本次未落地**：原方案 §3.2 第 3 条的「不在末尾时显示 `(↑ more)` 可视指示」未加；「滚动到底后恢复自动跟随」已由哨兵值实现（内容缩短时偏移量也由 `resolveScroll` 钳制到有效范围）。可视的历史提示保留为后续增强项。

验证：新增 `internal/ui/scroll_test.go`，覆盖 `resolveScroll`/`windowLines` 边界、流式状态下对话滚动、Diff 面板滚动、会话切换重置滚动。`go vet ./...` 与 `go test ./... -count=1 -timeout=120s` 全绿。

## 4. UI-2 对话无自动换行

### 4.1 问题与影响

`prefixedLines`（`conversation.go:28-42`）只按 `\n` 切分，`clipLine`（`view.go:474-479`）直接截断超宽内容。AI 输出的 prose/code 长行被静默砍掉，右侧内容丢失。

### 4.2 修复方案

1. 新增按显示宽度换行的函数（复用已有的 `ansi.StringWidth` 保证 CJK/emoji 正确）：先按 `\n` 拆段，再对每段按面板内容宽度折行，前缀（`You: ` / `Assistant: `）只加在首行，续行按前缀宽度缩进。
2. 折行只作用于「对话面板」，Diff 面板保持截断（对齐 diff 语义）。
3. 把 `conversation.go:13-17` 的角色前缀抽为共享常量，消除 `view.go:410-413` 里靠字符串前缀二次匹配的漂移隐患。

### 4.3 验证方法

- 单测：给折行函数喂包含 CJK、emoji、超长无空格 token 的字符串，断言折行结果每行显示宽度 ≤ 内容宽度，且续行缩进正确。
- 手动：窄终端（<100 列）发起包含长 URL / 长代码行的 turn，确认右侧不再丢字。

### 4.4 实现记录（已修复）

- `prefixedLines`（`conversation.go`）签名改为 `prefixedLines(prefix, value, width)`：先按 `\n` 拆段，再对每段用 `ansi.Hardwrap(paragraph, contentWidth, true)` 按显示宽度折行（grapheme 感知、保留 ANSI 转义与 CJK/emoji 宽度），前缀只加在首行，续行按 `ansi.StringWidth(prefix)` 缩进对齐。
- `conversationView` 去掉 `height` 参数，返回完整折行后的行（不再依赖 `tailBoundedLines` 截断）。
- `diffView` 精简为 `diffView(result)`，Diff 面板保持按宽度截断（符合 diff 语义，避免破坏 diff 行格式）。
- **本次未落地**：原方案 §4.2 第 3 条「把角色前缀抽为共享常量，消除 `view.go:410-413` 靠字符串前缀二次匹配的漂移隐患」未做，保留为后续一致性改进项。

验证：`internal/ui/scroll_test.go` 中 `TestPrefixedLinesWrapsLongContent` / `TestPrefixedLinesKeepsExplicitNewlinesIndented` / `TestConversationViewWrapsMessagesWithinWidth` 覆盖折行宽度与续行缩进；全量测试通过。

## 5. NAM-1 `CheckPoint` 命名错误

### 5.1 问题与影响

单字「checkpoint」被写成 `CheckPoint`，违反 Go 不在词中间大写首字母的惯例，且会通过公共 API 扩散。根因是沿用了上游 `adk.CheckPointStore` 的拼写，但项目自己的导出 API 不应继承。

### 5.2 修复方案

改名（含约 30 处调用点与测试）：

- `CheckPointStore` → `CheckpointStore`
- `MemoryCheckPointStore` → `MemoryCheckpointStore`
- `NewMemoryCheckPointStore` → `NewMemoryCheckpointStore`
- `ErrCheckPointStoreClosed` → `ErrCheckpointStoreClosed`

涉及 `internal/agent/checkpoint.go`、`internal/app/build.go:48,127` 及 `internal/agent/*_test.go`。用 IDE 重命名或全局替换后逐个确认。

### 5.3 验证方法

- `grep -rn "CheckPoint" internal/ cmd/` 中**项目自有标识符**（`CheckPointStore` / `MemoryCheckPointStore` / `NewMemoryCheckPointStore` / `ErrCheckPointStoreClosed`）归零；仅剩上游 Eino `adk.CheckPointStore` / `adk.CheckPointDeleter` / `adk.WithCheckPointID` 与 `RunnerConfig.CheckPointStore` 字段等外部 API 名称（不可改）。
- `go build ./... && go test ./... -count=1 -timeout=120s` 全部通过。
- 若外部有引用（暂无），对外破坏性变更记录在提交说明中。

### 5.4 实现记录（已修复）

- 按 §5.2 将四个自有标识符整体改名：`CheckPointStore` → `CheckpointStore`、`MemoryCheckPointStore` → `MemoryCheckpointStore`、`NewMemoryCheckPointStore` → `NewMemoryCheckpointStore`、`ErrCheckPointStoreClosed` → `ErrCheckpointStoreClosed`。
- 涉及 `internal/agent/checkpoint.go`（定义）、`internal/agent/eino_invoker.go`（接口使用与 `deleteCheckpoint` 参数）、`internal/app/build.go`（依赖组装）及 `internal/agent/{eino_invoker,checkpoint,bugfix_e2e,python_bugfix_e2e}_test.go`。
- **刻意保留**：`eino_invoker.go:37/38` 的 `adk.CheckPointStore` / `adk.CheckPointDeleter`（实现的上游接口）、`:139` 的 `RunnerConfig.CheckPointStore` 字段、`:142` 的 `adk.WithCheckPointID`，以及 `checkpoint.go:75` 注释中引用的 `CheckPointDeleter` 契约，均属 Eino 上游 API，改名会破坏实现断言。

验证：`go vet ./...` 无输出，`go test ./... -count=1 -timeout=120s` 全绿；`grep -rn "CheckPoint" internal/ cmd/ --include=*.go` 仅命中上述上游 API 名称。

---

# 第二部分：P1（重要）

## 6. UI-3 输入框交互简陋

### 6.1 问题与影响

`update.go:211-237` 只支持尾部追加、尾部退格、`Alt+Enter` 换行。没有左右光标、`Home/End`、词删除、上下历史；多行输入只能从尾部改，修改多行 prompt 很痛苦。

### 6.2 修复方案

1. 把输入文本从 `string` 改为带光标位置的轻量缓冲结构（`text []rune` + `cursor int`），支持 `←/→`、`Home/End`、`Ctrl+←/→` 词跳、`Delete`、`Ctrl+K/U` 行操作。
2. 新增 `↑/↓` 历史补全（保存本 session 的输入历史，`↑` 回退、`↓` 前进，正在编辑的内容暂存）。
3. 渲染时在光标位置画光标（窄终端下用反色块，避免零宽光标不可见）。

### 6.3 验证方法

- 单测：对文本缓冲的插入 / 删除 / 词跳 / 行操作写表驱动测试。
- 手动：输入一段多行文本，验证可回到中间修改；`↑` 能召回上一条输入。

### 6.4 实现记录（已修复）

- `Model` 新增 `composer []rune` / `composerCursor int` / `history []string` / `historyIndex int` / `historyStash []rune` 字段；`composer` 由原先的 `string` 改为 `[]rune`（结合 `composerCursor` 形成轻量文本缓冲），历史在 `submitComposer` 中经 `recordHistory` 记录（跳过空串与连续重复项）。
- 新增 `internal/ui/composer_edit.go`，承载全部编辑原语：`insertRunes` / `deleteRunes`（删除时对释放的尾部 rune 清零，避免敏感输入残留）、`prevWordBoundary` / `nextWordBoundary`（按 `unicode.IsSpace` 分词）、`lineStart` / `lineEnd`、以及 `handleComposerEditKey` / `handleHistoryKey` / `historyBack` / `historyForward` / `restoreHistoryEntry`。
- `handleComposerEditKey` 处理 `←/→`（配合 `Ctrl` 做词跳）、`Home/End`（行首/行尾）、`Backspace`/`Delete`（光标处删 rune）、`Ctrl+K`（删到行尾）、`Ctrl+U`（删到行首）；文本变更返回 `refreshCompletion()`，纯光标移动不刷新补全。
- 历史补全：空闲（非 `inputBusy`、无补全/picker/overlay）时 `↑/↓` 召回历史；首次 `↑` 用 `historyStash` 暂存正在编辑的内容，越过最新项后 `↓` 恢复暂存内容。会话切换（`sessionLoadedMsg`）时 `resetHistory()` 清空历史。
- 渲染：`composerView` 在光标显示位置插入反色块光标（`theme.go` 新增 `cursor` 样式），`composerDisplayIndex` 负责把 `\n`→`" ↵ "` 的 3-rune 展开映射回显示索引；空输入时显示占位符 + 光标。
- 按键分派顺序（`update.go`）：`inputBusy` → 补全键 → 历史键 → 编辑键 → 翻页 → 面板切换 → 回车（`Alt+Enter` 换行，普通回车提交）→ 文本插入。即「可编辑时方向键归编辑/历史，流式输出时方向键归面板滚动」。

验证：新增 `internal/ui/composer_edit_test.go`，覆盖 `insertRunes`/`deleteRunes` 边界、词跳/行边界、`composerDisplayIndex` 换行映射、`composerView` 光标渲染（`ansi.Strip` 断言）、光标移动/插入、`Home/End`、删除/退格、`Ctrl+←/→`、`Ctrl+K/U`、历史召回与提交去重。`go vet ./...` 无输出，`go test ./... -count=1 -timeout=180s` 全绿。

## 7. UI-4 Picker 列表无滚动窗口

### 7.1 问题与影响

Provider（`provider_picker.go:307-364`）与 Session（`session_picker.go:184-199`）全量渲染所有条目后由 `renderDialog` 尾部裁剪，条目超过对话框高度后，高亮光标会移出屏幕，用户盲选。而 `completion.go:177-189` 已正确实现了 keep-cursor-visible，却没有复用到 picker。

### 7.2 修复方案

1. 抽取 `completion.go` 的「保持光标可见」逻辑为共享帮助函数，或让 picker 渲染前按光标位置计算滚动窗口（`start = clamp(cursor - visible/2, 0, len - visible)`）。
2. 列表上下各显示 `…` 指示还有更多条目。

### 7.3 验证方法

- 单测：构造超过可视高度的条目集，断言任意光标位置都落在可见窗口内。
- 手动：配置 >10 个 provider / 会话，`↑/↓` 全量遍历，确认光标始终可见。

### 7.4 实现记录（已修复）

- 新增 `internal/ui/picker_window.go`，抽出共享的 `pickerWindow(cursor, count, visible int) (int, int)`（窗口常量 `maxVisiblePickerItems = 8`）：列表不长时返回 `(0, count)` 全量渲染；超长时按 `start = clamp(cursor - visible/2, 0, count - visible)` 计算起始行，保证高亮光标始终落在可见窗口内。
- `completion.go` 的 `completionWindow` 改为委托 `pickerWindow`，消除原先 keep-cursor-visible 逻辑与 picker 的重复实现。
- `session_picker.go` 的 `SessionPickerChoosing` 视图改用 `pickerWindow` 分窗渲染，窗口上下各显示 `"  …"` 指示还有更多条目。
- `provider_picker.go` 的 `ChooseModel` 对 `p.models`、`ChooseProvider` 对 `profiles + providerChoices` 合并列表分别按 `pickerWindow` 分窗，并在各段上下显示 `…` 指示。

验证：新增 `internal/ui/picker_window_test.go`，覆盖 `pickerWindow` 钳制（顶部/底部/居中/零可视高度）、SessionPicker 20 个会话、ProviderPicker 20 个模型、20 个 provider profile + 内建选项下「任意光标位置都可见」。`go vet ./...` 无输出，`go test ./... -count=1 -timeout=180s` 全绿。

## 8. DOC-1 补齐 package 文档注释

### 8.1 问题与影响

14 个 `internal/` 包中只有 `contextmanager` 有 `// Package` 注释，其余 13 个（agent / app / approval / config / credential / language / lsp / provider / session / sessionstore / tool / ui / workspace）均缺。

### 8.2 修复方案

每个包挑一个主文件，在 `package` 声明上方加一行 `// Package xxx ...`，说明职责，例如：

```go
// Package workspace provides the bounded, secret-free filesystem and Git
// operations exposed to the coding agent, enforcing read and diff limits.
package workspace
```

### 8.3 验证方法

- `go doc ./internal/...` 每个包都能看到首行简介。
- 如启用 `golangci-lint`，`revive`/`golint` 的 package-comments 规则不再报错。

### 8.4 实现记录（已修复）

- 为 13 个缺失 `// Package` 注释的包各新增 `doc.go`，说明职责：`agent` / `app` / `approval` / `config` / `credential` / `language` / `lsp` / `provider` / `session` / `sessionstore` / `tool` / `ui` / `workspace`（`contextmanager` 原有注释保留）。
- 注释统一采用「Package xxx 说明职责」的两行形式，与 `contextmanager.go` 现有风格一致。

验证：`go doc ./internal/...` 每个包可见首行简介；`go vet ./...` 无输出。

## 9. DOC-2 适配器导出方法注释

### 9.1 问题与影响

4 个 provider 适配器共 20 个导出方法（`Kind` / `Defaults` / `Validate` / `ListModels` / `NewChatModel`）无文档注释，与全项目 276/277 的覆盖水平不匹配。集中在 `openai.go`、`deepseek.go:26,30,39,53,64`、`ollama.go:31,35,43,54,111`、`compatible.go:36,40,47,59,75`。

### 9.2 修复方案

逐个补一行以方法名开头的注释（这些方法实现 `Adapter` 契约）。可选：在 `Adapter` 接口上写一句「实现需满足 Adapter 契约」，从而豁免对接口实现方法的逐行要求。

### 9.3 验证方法

- `go vet ./...` 通过（无副作用确认）；人工 `go doc` 抽查每个适配器。

### 9.4 实现记录（已修复）

- 为 4 个 provider 适配器共 20 个导出方法补注释：`Kind` / `Defaults` / `Validate` / `ListModels` / `NewChatModel`，分布在 `openai.go` / `deepseek.go` / `ollama.go` / `compatible.go`。
- 注释以方法名开头，说明各方法在 `Adapter` 契约下的职责（例如 `ListModels` 说明「列出可用模型并标记推荐项」）。

验证：`go vet ./...` 无输出；`go doc` 抽查每个适配器可见方法注释。

## 10. DOC-3 常量注释与 `ErrStoreClosed`

### 10.1 问题与影响

`ui/provider_picker.go:19-27` 的 9 个 `ProviderPicker*` 常量无注释，与其兄弟 `session_picker.go:16-30`（每个都写注释）不一致；`credential/memory_store.go:12` 的 `ErrStoreClosed` 是 277 个顶层符号中唯一缺注释的。

### 10.2 修复方案

- 为 9 个常量补 `// ProviderPickerX ...` 注释（对齐 `session_picker.go` 写法）。
- 为 `ErrStoreClosed` 补注释；同时可考虑改名 `ErrClosed`（`Store` 略显冗余，需同步 `credential_test.go`）。

### 10.3 验证方法

- 全局扫描「无文档注释的导出符号」归零（`golint`/`revive` 的 exported 规则）。

### 10.4 实现记录（已修复）

- 为 `ui/provider_picker.go` 的 9 个 `ProviderPicker*` 阶段常量补齐注释，与 `session_picker.go` 的逐条注释风格对齐。
- 为 `credential/memory_store.go` 的 `ErrStoreClosed` 补注释（保留原名称，未做冗余改名）。

验证：`go vet ./...` 无输出；导出符号无文档注释扫描归零。

---

# 第三部分：P2（结构 / 健壮性）

## 11. DEP-1 `LanguageID` / `LanguageProfile` 下移到 `language`

### 11.1 问题与影响

`LanguageID`（`agent/ports.go:130-161`）与 `LanguageProfile` 是语言领域概念，却定义在 `agent` 包，导致 `language`（策略实现包）反向 `import agent`（`language/strategy.go:6`）。这是最无理由的一处依赖倒置。

### 11.2 修复方案

1. 把 `LanguageID` 常量、`LanguageProfile`、`CheckCommand`、`CheckPlan` 迁到 `language` 包。
2. `agent` 改为 `import language` 使用这些类型；`agent.LanguageResolver` 接口保留在 agent（端口归消费者），其方法签名引用 `language.LanguageProfile`。
3. 更新 `session` 中若引用了 `LanguageID` 的地方。

### 11.3 验证方法

- `go build ./... && go test ./...` 通过。
- `grep -rn '"github.com/eaglc/codepilot/internal/agent"' internal/language/` 不再命中（language 不再依赖 agent）。

### 11.4 实现记录（已修复）

- 将 `LanguageID`（含 `LanguageGo` / `LanguagePython` / `LanguageGeneric` 常量）、`LanguageProfile`、`CheckCommand`、`CheckPlan` 迁到 `language` 包（新建 `internal/language/types.go`）。
- `agent.LanguageResolver` 保留在 agent（端口归消费者），方法签名改为返回 `language.LanguageProfile`；`agent.NavigationScope.Language` 与 `agent.RunChecksRequest.Command` 改为引用 `language.*`。
- 删除 `language/registry.go` 中的 `var _ agent.LanguageResolver` 断言与 `agent` 导入，接口一致性由 `app/build.go` 的组装处由编译器保证。
- 更新所有消费方：`agent`（prompt / toolset / tool_run_checks）、`lsp/navigator.go`、`workspace/command_test.go` 及 `language` / `agent` / `lsp` 相关测试。

验证：`go build ./... && go test ./... -count=1 -timeout=180s` 全绿；`grep -rn 'internal/agent"' internal/language/` 不再命中。

## 12. DEP-2 `ModelRef` 下移到 `provider`

### 12.1 问题与影响

`ModelRef`（`agent/ports.go:302-307`，仅 Provider + Model 两个字符串）是 provider 概念，却定义在 agent，导致 `provider` 仅为实现 `agent.ModelFactory` 就 import 了 agent（`provider/service.go:24`）。

### 12.2 修复方案

1. 把 `ModelRef` 迁到 `provider` 包。
2. `agent.ModelFactory` 接口改为引用 `provider.ModelRef`（agent import provider）。
3. 评估 `ModelFactory` 接口本身是否也应随之一并归 provider（由 `session` 消费），可留作后续。

### 12.3 验证方法

- `go build ./... && go test ./...` 通过。
- `grep -rn 'internal/agent"' internal/provider/` 不再命中（provider 不再依赖 agent）。

### 12.4 实现记录（已修复）

- 将 `ModelRef` 迁到 `provider` 包（新建 `internal/provider/model_ref.go`）。
- `agent.ModelFactory` 接口改为引用 `provider.ModelRef`（agent import provider）；删除 `provider/service.go` 的 `var _ agent.ModelFactory` 断言与 `agent` 导入，接口一致性由 `app/build.go` 的组装处由编译器保证。
- 更新所有消费方：`agent`（coding_agent / invocation / ports）、`provider/service.go` 及 `agent` / `provider` 相关测试。
- `ModelFactory` 接口本身暂留 agent（§12.2 第 3 点），留作后续评估。

验证：`go build ./... && go test ./... -count=1 -timeout=180s` 全绿；`grep -rn 'internal/agent"' internal/provider/` 不再命中。

## 13. ROB-1 处理被吞掉的真实错误

### 13.1 问题与影响

以下 `_ =` 丢弃的是有用户可见后果或诊断价值的错误，而非无害清理：

| 位置 | 丢弃的错误 |
| --- | --- |
| `session/service.go:727` | 最终 turn 保存失败事件投递失败 |
| `session/service.go:746` | 最终 turn 完成事件投递失败 |
| `lsp/client.go:282` | 回写语言服务器响应失败 |
| `agent/eino_invoker.go:410` | 工具失败上报失败 |
| `agent/coding_agent.go:404` | snapshot 错误（`patches, _ := state.snapshot()`） |

（`lsp/client.go:261` 的 JSON 解码失败属无害回退，可保留但建议注释说明。）

### 13.2 修复方案

- 对 `session/service.go` 两处：投递失败至少落一条结构化日志（或塞入 `session` 的恢复警告），让「保存失败」的信号不丢失。
- 对 `lsp/client.go:282`：写失败记录到客户端日志并考虑关闭连接。
- 对 `agent/eino_invoker.go:410` / `coding_agent.go:404`：把错误 join 到当前返回的 error，或记录日志。

### 13.3 验证方法

- 单测：注入会失败的 EventSink / 写连接，断言错误被记录或向上传递，不再静默。
- 静态：`grep -rn '_ = ' internal/` 复查，仅剩无害清理类（Close / 临时文件 / 响应体 drain）。

### 13.4 实现记录（已修复）

- `session/service.go`：`runTurn` 两处最终事件投递失败不再 `_ =` 丢弃，改为调用 `recordRecoveryWarning` 写入一条 `RecoveryTurnUnrecorded` 警告到 `active.RecoveryWarnings`（新增 `RecoveryTurnUnrecorded` 码，`view.go` 已渲染 `RecoveryWarnings`）。
- `lsp/client.go:282`：回写语言服务器响应失败时调用 `c.finish` 关闭连接，错误经 `failure()` 向上暴露。
- `agent/eino_invoker.go:410`：工具失败上报失败改为 `errors.Join(err, reportErr)`，两条错误都不再丢失。
- `agent/coding_agent.go:404`：经核实 `state.snapshot()` 返回 `([]PatchRecord, CheckSummary)`，`_` 丢弃的是未使用的 CheckSummary 而非错误——原问题描述不成立，已补注释说明，未改行为。
- `lsp/client.go:261`：JSON 解码失败属无害回退，已补注释说明。
- 单测：`TestService_RunTurnRecordsWarningWhenFinalEventDeliveryFails` / `...WhenSaveFailureEventDeliveryFails`、`TestClientWriteFailureClosesConnection`、`TestEinoRegistryToolJoinsToolFailureWithReportFailure`。
- 静态复查：`grep -rn '_ = ' internal/` 仅剩 Close / 临时文件 / 响应体 drain / 哈希与缓冲区写入等无害清理。

## 14. DEP-3 `session.Service` 编排过大

### 14.1 问题与影响

两个层面：

1. **文件 / 编排过大**：`session/service.go`（约 1247 行）同时承担领域状态持有与「session + turn + 模型切换 + 权限切换 + diff + 文件补全 + 审批转发」编排，后续会持续膨胀，锁粒度（`operations` 与 `mu` 双层锁）也更难追。
2. **依赖层面的 god package 风险**：`session` 是唯一零内部依赖的叶子包，领域模型（`Session`/`Message`/`Turn`/`Event`/`AppError` 等）和编排 `Service` 挤在同一个包里，而所有其他包（agent / workspace / provider / lsp / language / ui / approval / sessionstore / config / credential）都 import 它。这使 `session` 天然成为共享类型的「倾倒场」——任何需要跨包共享的类型都倾向于塞进 `session`（`AppError`、`PermissionMode`、各种 DTO 已经如此）。当前尚可控，但若不主动拆，后续每次新增共享类型都会加重它。

### 14.2 修复方案

按「先提取纯函数、再拆服务」的渐进方式：

1. 先把 `codingAgentConfig` / `classifyTurnStatus` / `finalTextForStatus` / `clone*` 等纯函数与领域类型抽到独立文件（`session/service_turn.go` 等），降低单文件行数，不改行为。
2. 若继续演进，把「turn 运行状态机」抽为独立的 `turnRunner`，`Service` 只保留 session 生命周期与对外 API。

### 14.3 验证方法

- 每步拆分后 `go test ./internal/session/...` 全绿，保证纯重构不改行为。

### 14.4 实现记录（已修复）

- 新增 `session/service_turn.go`：抽走 `validateRunLimits` / `validPermissionMode` / `classifyTurnStatus` / `finalEventKind` / `finalTextForStatus` / `codingAgentConfig` / `sessionSummaryFromSession` / `worktreeSummaryFromRecord` / `titleFromMessage` 等纯函数。
- 新增 `session/service_clone.go`：抽走 `cloneSessionSnapshot` / `cloneMessages` / `clonePatchRecords` / `clonePatchRecord` / `containsPatchRecord`。
- `service.go` 由 1263 行降至 1119 行；`newEntityID` / `applicationError` / `hasApplicationErrorCode` 留在 `service.go`（错误与 ID 基础设施）。
- 纯重构不改行为：`go test ./internal/session/...` 全绿。

## 15. DEP-4 `ListWorkspaceFiles` 类型断言脆弱

### 15.1 问题与影响

`session/service.go:488-504` 用本地私有接口 `fileLister` + 类型断言访问可选能力。注释已说明是为了保持 `WorkspaceReader` 端口聚焦，但换实现会静默降级为 `ErrInternal`。

### 15.2 修复方案

两种取向任选：

- 若该能力稳定，直接在 `WorkspaceReader` 接口上增加 `ListWorkspaceFiles` 方法（接受端口变大）。
- 若想保持可选，把能力检测从「运行时类型断言」改为「构造时显式传入可选依赖」（在 `session.Dependencies` 里加一个可空字段），让能力缺失在组装期暴露，而非运行期。

### 15.3 验证方法

- `go build ./... && go test ./...` 通过；构造期缺能力时在 `NewService` 即报错或明确降级。

### 15.4 实现记录（已修复）

- 选择方案 (a)：把 `ListWorkspaceFiles` 加入 `WorkspaceReader` 接口（`ports.go`），移除 `service.go` 中的本地 `fileLister` 类型断言与 `ErrInternal` 降级分支。
- `workspace.Service` 与测试 `fakeWorkspaceReader` 均已实现该方法，`go build ./... && go test ./...` 通过；能力缺失现在在编译期暴露，而非运行期静默降级。

---

# 第四部分：P3（增强，可选）

## 16. UI-5 浅色主题 / `NO_COLOR` 支持

- **现状**：`view.go:107` 硬编码深色背景，无 `NO_COLOR` / 256 色 / 浅色主题处理。
- **方案**：从 `theme.go` 抽出「浅色 / 深色」两套调色板，依据 `NO_COLOR`、`colorprofile` 环境或启动 flag 选择；颜色能力检测可复用 lipgloss v2 的 profile。
- **验证**：`NO_COLOR=1` 下运行确认无 ANSI 颜色码；浅色终端下可读。

## 17. UI-6 宽屏焦点指示弱

- **现状**：宽屏两面板常显，`Tab` 仅切换边框色（`view.go:119,127`），焦点是纯装饰，`Tab` 提示还在宽屏被隐藏（`view.go:226`）。
- **方案**：待 UI-1 滚动落地后，焦点才有实际语义（决定 `↑/↓` 滚哪个面板）；届时保留可见的焦点高亮并显示切换提示。
- **验证**：宽屏下 `Tab` 切换后滚动作用于正确的面板。

## 18. DEP-5 `contextmanager` 过早抽象

- **现状**：MVP 中仅接入 `contextmanager.NopStrategy{}`（`build.go:146`），整包为占位。
- **方案**：若上下文压缩不是下个迭代的明确目标，可先删包、在需要时再引入（YAGNI）；若近期要做则保留接缝并补充策略实现。
- **验证**：决策记录到本文档「决策记录」一节，避免反复摇摆。

---

## 19. 通用验证基线

所有修复提交前，按 `AGENT.md` §14 执行：

```bash
gofmt -l .            # 应为空
go vet ./...          # 应无输出
go test ./... -count=1 -timeout=120s   # 全绿
```

涉及 UI 交互的改动，额外执行一次手动冒烟：

```powershell
go build -o codepilot.exe ./cmd/codepilot
.\codepilot.exe --workspace H:\path\to\repo
```

重点回归：发起长输出 turn、审批 Y/S/N、`Tab` 切换面板、窄终端（<100 列）布局、宽字符（CJK/emoji）渲染。

## 20. 建议迭代节奏

| 迭代 | 范围 | 目标 |
| --- | --- | --- |
| 迭代 1 | UI-1 / UI-2 / NAM-1（已完成） | 消除 P0，恢复可用性基线 |
| 迭代 2 | UI-3、UI-4（已完成）、DOC-1/2/3 | 补齐交互与文档一致性 |
| 迭代 3 | DEP-1/2、ROB-1、DEP-3/4 | 理顺依赖方向与健壮性 |
| 迭代 4 | UI-5/6、DEP-5 | 增强项，按需取舍 |
