# Changelog

CodePilot 的重要变更记录在这里。发布版本遵循 Semantic Versioning；只有带日期的版本小节才允许由 Tag 发布流程对外发布。

## [Unreleased]

### Added

- Provider-neutral LLM、Agent Runtime、Coding Agent、Context Manager、Tool 与分层 Store 架构。
- OpenAI、DeepSeek、Ollama 和自定义 OpenAI-compatible Provider 配置、凭证与模型探测流程。
- 持久 Session、结构化 Tool Call/Result、滚动摘要、恢复计划、跨进程 writer lease 与一致性修复命令。
- Go、Python、Node 项目工具、受限检查、Patch 审批、LSP 导航和单栏命令行式 TUI。
- 三平台 CI、真实临时仓库 E2E、可选 Provider 探测和 Tag 发布门禁。

### Changed

- Tool 执行事件先投影为稳定的 Agent/Coding 事件，再由 TUI 消费，不再向产品界面泄漏 Provider 流式事件。
- Tool 结果默认折叠，文件修改以内联 Diff 显示在会话时间线中。
- `--version` 同时报告版本、提交和构建时间。

### Fixed

- 启动、Provider 预检和 Agent 运行错误会保留为可读的持久状态，不再只短暂闪现于 TUI 底部。
