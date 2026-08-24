# 真实 Provider 可选集成测试

普通 `go test ./...` 必须离线、可重复且不消耗模型额度。真实 OpenAI、DeepSeek、Ollama 探测只有在对应 `CODEPILOT_LIVE_*` 开关被显式设为 `1/true/yes/on` 时才执行；仅存在 API Key、`OPENAI_BASE_URL` 或本机 Ollama 不会触发网络请求。

每个已启用 Provider 依次验证：

1. 真实模型目录可以访问，且目标模型可见；
2. 当前 adapter 能创建 provider-neutral `llm.ChatModel`；
3. 一次非流式请求返回合法 assistant message；
4. 一次流式请求能经标准事件协议收敛为合法 assistant message。

因此每个远程 Provider 会产生两次很小的模型请求，可能计费。测试不打印 Credential，也不把 Credential 写入 profile、journal 或 fixture；测试结束会清零自己的可变 Credential 副本。

## 环境变量

| Provider | 显式开关 | Credential | 可选 endpoint | 可选目标模型 |
|---|---|---|---|---|
| OpenAI | `CODEPILOT_LIVE_OPENAI=1` | `OPENAI_API_KEY` | `CODEPILOT_LIVE_OPENAI_BASE_URL` | `CODEPILOT_LIVE_OPENAI_MODEL` |
| DeepSeek | `CODEPILOT_LIVE_DEEPSEEK=1` | `DEEPSEEK_API_KEY` | `CODEPILOT_LIVE_DEEPSEEK_BASE_URL` | `CODEPILOT_LIVE_DEEPSEEK_MODEL` |
| Ollama | `CODEPILOT_LIVE_OLLAMA=1` | 无 | `CODEPILOT_LIVE_OLLAMA_URL` | `CODEPILOT_LIVE_OLLAMA_MODEL` |

未指定模型时使用对应 adapter 的当前默认模型。Ollama 必须已经安装目标模型；远程账户必须有权列出并调用目标模型。

PowerShell 示例：

```powershell
$env:CODEPILOT_LIVE_OPENAI = "1"
$env:OPENAI_API_KEY = Read-Host -MaskInput "OpenAI API Key"
$env:CODEPILOT_LIVE_OPENAI_MODEL = "your-accessible-model"
go test ./internal/provider -run TestLiveProviderCatalogCompleteAndStream -count=1 -v
Remove-Item Env:OPENAI_API_KEY, Env:CODEPILOT_LIVE_OPENAI
```

POSIX shell 示例：

```bash
read -rs OPENAI_API_KEY && export OPENAI_API_KEY
export CODEPILOT_LIVE_OPENAI=1
export CODEPILOT_LIVE_OPENAI_MODEL=your-accessible-model
go test ./internal/provider -run TestLiveProviderCatalogCompleteAndStream -count=1 -v
unset OPENAI_API_KEY CODEPILOT_LIVE_OPENAI
```

可以同时启用多个开关；未启用的 Provider 会完全跳过。CI 的常规 job 不设置这些开关。发布负责人可在受保护、显式授权且带 secrets 的独立环境中运行，不应在来自外部贡献者的代码上暴露真实凭证。
