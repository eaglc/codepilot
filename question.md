
  下面按「现状 → 纠正 → 逐条回答 → 你要拍板的点」来捋。

  ---

  一、现状：数据流和接缝

  sessionstore（原始 message，append-only，永不改写）
    → Service.StartTurn 把 s.active.Messages 全量塞进 TurnRequest.History   (service.go:164,222)
    → CodingAgent.RunTurn 送 LLM 前调用一次 a.contexts.Process(...)          (coding_agent.go:72)
    → invocationMessagesFromContext 转成 InvocationMessage                   (coding_agent.go:76)
    → EinoInvoker.Invoke → runner.Run(messages)                              (eino_invoker.go:142)

  contextmanager.Strategy 接口就是你要的接缝：

  Process(ctx, Request{Scope, SystemPrompt, Messages}) (Result{SystemPrompt, Messages}, error)

  它在每轮 turn 开始时被调用一次，输入是「系统提示 + 全部历史 + 当前用户消息」。压缩的产物是瞬态的，跑完就丢，不落库。

  ---

  二、最关键的纠正：策略 1 和策略 2/3 不是同一个轴

  你的「三个阈值一个级联，一轮可能都触发」的直觉对了一半，但把两件事混在了一起：

  ┌─────────────────────────┬──────────────────────────────────────────┬────────────────────────────────┬──────────────────────────────┐
  │          策略           │                 操作对象                 │              时机              │          归属的接缝          │
  ├─────────────────────────┼──────────────────────────────────────────┼────────────────────────────────┼──────────────────────────────┤
  │ ② 摘要                  │ 历史对话（过去轮次 user/assistant 消息） │ turn 开始、送 LLM 前，每轮一次 │ contextmanager               │
  ├─────────────────────────┼──────────────────────────────────────────┼────────────────────────────────┼──────────────────────────────┤
  │ ③ 强裁最近 N 轮         │ 历史对话                                 │ 同上（②跑完还不够小才触发）    │ contextmanager               │
  ├─────────────────────────┼──────────────────────────────────────────┼────────────────────────────────┼──────────────────────────────┤
  │ ① 截断工具结果 + 取全文 │ turn 内部的工具调用结果                  │ agentic loop 里每次工具执行后  │ tool 层，不在 contextmanager │
  └─────────────────────────┴──────────────────────────────────────────┴────────────────────────────────┴──────────────────────────────┘

  关键点：策略 1 的「工具结果」在 turn 开始时根本不存在——它们是 EinoInvoker 跑起来之后才一条条产生的（eino_invoker.go:142 → consumeEinoEvents → einoRegistryTool.complete）。所以「阈值 t1
  触发策略1」这个动作，没法在一个 turn 开始时的单点检查里发生。

  结论：你的级联其实是两个独立机制：

  1. 历史压缩器（pre-turn，每轮一次）：预算阶梯 → t2 摘要、t3 强裁。
  2. 工具结果限界器（intra-turn，每次工具调用）：t1 截断 + 取全文方法。

  「一轮三个都触发」是成立的，只是发生在不同的阶段：先跑历史压缩（可能触发 ②③），loop 里再跑工具截断（触发 ①）。

  而且策略 1 的接缝已经有雏形：RunLimits.ToolResultMaxBytes / CommandOutputMaxBytes（turn.go:27-28），每个工具结果经 encodeToolResult 编码（eino_invoker.go:487）、boundedInvocationText
  限长（:460-464）。所以「截断工具结果」要在 tool 层做，不是塞进 contextmanager。

  ---

  三、逐条回答你的三个问题

  Q1：session store 要不要改成存压缩后的上下文？——不要。

  store 保持 append-only 原始消息，压缩是「每次从原始重算的瞬态视图」。理由：

  1. 压缩不可逆。摘要/截断丢信息，一旦把原始历史覆盖成摘要，原文就永远没了。而摘要是「带观点的、依赖模型的」产物——换了模型（上下文窗口不同）摘要就失效，又没法重新摘要。
  2. 不同模型窗口不同。压缩阈值是 per-model 的，原始消息是 model-agnostic 的。存压缩产物 = 把 model 绑死进 store。
  3. UI 要显示原文。对话面板渲染的是 s.active.Messages，必须是真实文本，不是摘要。
  4. 分支需要原文（见 Q3）。原文没了，分支就无从谈起。
  5. 重算廉价且幂等。摘要本身只是一次 LLM 调用的成本，从原始重算永远是确定性的；存快照反而带来「又加了几轮要不要重新摘要、用哪个窗口」的一致性泥潭。

  顺带：现在的 SessionStore 接口（AppendMessage / CommitTurn / AppendPatch）本来就是 append-only，SaveSession 只存元数据不存消息。现状就已经是对的，你不需要改 store。

  Q2：上下文管理什么时候调用？——你的直觉对一半。

  - 历史压缩：每 turn 一次，在送 LLM 之前，不在 loop 里重复调。因为历史在 turn 期间不变（变的只有工具结果），重复压缩只会反复搅动摘要让模型困惑。
  - 工具结果截断：loop 里每次工具调用时。
  - 「LLM 完成一轮后再保存一次」：这里要纠正——turn 完成后保存的是原始 assistant 消息 + turn 记录（runTurn 里的 CommitTurn，service.go:713 已经在做），不是保存压缩上下文。不需要额外的「存压缩产物」步骤。

  （唯一的例外：如果你想缓存摘要以加速未来 turn——避免每次都对同一批旧轮次重复摘要——那是一个纯优化，key 要带「消息区间 + model」。我建议先不做，YAGNI，摘要相对 LLM 调用本身就便宜。）

  Q3：消息分支会和这个设计冲突吗？——不冲突，反而是这个设计天然兼容分支。

  分支（编辑/重生成/走另一条 agent 路径）的本质是「从某个点选一条不同的 history 前缀」去喂模型。只有「原始存储 + 瞬态压缩」这个设计做得到：

  - 分支 = 选一个不同的前缀作为 TurnRequest.History，压缩从所选前缀现场重算。不同分支各自得到自己的摘要。
  - 反之，如果你把摘要写回 store（Q1 的错误方向），原文没了，分支就不可能。

  唯一要注意的一点：现在 contextmanager 里有个约束——它校验「恰好一条 Current 消息，且必须原样保留」（coding_agent.go:281-294）。这个约束和分支不冲突（Current =
  发起本轮的那条消息），但如果你将来要做「改写历史消息再重放」的分支，这条「当前消息必须原样」的校验要放宽成「允许替换当前消息内容」。

  另外有一个硬约束现在就在：eino_invoker.go 里有 maxInvocationHistorySize = 8MB 和 maxInvocationMessages = 2000 的硬拒绝（超了直接报错 conversation exceeds its total size
  limit，不是优雅截断）。上下文管理的职责就是在触到这根硬线之前主动收缩，策略 3 本质上就是把这条硬报错换成「优雅丢到最近 N 轮」。

  ---

  四、需要你拍板的三个点

  在往下设计前，有三个决策会决定整个模块的形状：

  1. 摘要（策略②）要不要 LLM？ 这是最大的分叉。现在 contextmanager 是纯 provider-neutral、没有 model 访问权的。做真正的「摘要」意味着要给 Strategy 注入一个 model 接口（打破它的纯净性，或把摘要挪到 agent
     层做）。我建议：第一版只做确定性的「截断 + 保留 N 轮 + 强裁」，先不做 LLM 摘要——因为摘要不是正确性必需，且引入 model 依赖会显著扩大改动面。
  2. 策略①「取全文」的机制：是复用现有工具自己的分页参数（tool_read_file 的 offset/limit 这类，最干净），还是做一个通用的「按 ID 取被截断结果」工具（需要结果缓存 + ID + 生命周期管理，复杂度高）？我建议前者。
  3. 「token 占用」怎么计量：代码里现在全是字节（maxInvocationHistorySize、ToolResultMaxBytes），没有 tokenizer。用字节/字符做代理最省、最确定、和现状一致；真 token 计数要引入 provider 相关的
     tokenizer。我建议字节代理。

  ---

  这三个点里，第 1 个最关键——它决定 contextmanager 要不要引入 model 依赖，也就是这个模块到底长成「纯函数变换」还是「带 LLM 能力的子系统」。

  你想先从哪个往下钻？或者你对上面哪一条的判断跟我不同，我们先把它对齐。