# CodePilot 文档索引

本文档是 CodePilot 文档的入口。除 `archive/` 外，目录中的文档应当能够指导当前代码开发、测试、发布或后续产品演进。

## 当前有效文档

### 架构

- [模块化架构基线与迁移计划](architecture/modular-architecture-migration.md)：当前 `internal` 分层、依赖方向、迁移边界和已落地能力的权威说明。

### 路线图

- [产品补全路线图](roadmap/product-completion-roadmap.md)：当前产品实现状态、已完成阶段和发布前剩余事项。
- [Plan 模式与多 Agent 演进规划](roadmap/plan-mode-and-multi-agent-roadmap.md)：P3/P4 及后续发展方向，包含设计方案、执行步骤和验收标准。

### 设计

- [会话与上下文工作流](design/session-and-context-workflow.md)：Session、Context、持久化、压缩和恢复的工作流程。
- [上下文管理设计](design/context-management-design.md)：上下文预算、摘要和压缩策略的设计推导与实现边界。

### 工程指南

- [真实 Provider 可选集成测试](guides/live-provider-integration-tests.md)：OpenAI、DeepSeek、Ollama 的显式联网测试指南。
- [发布、安装、升级与回滚](release-and-upgrade.md)：版本、制品、安装、升级、回滚和发布检查。该文件保留在 `docs/` 根目录，因为 README、GoReleaser 和架构测试会引用它。

## 文档状态约定

- 当前有效文档必须与代码、测试和配置保持一致；如果实现发生变化，应在同一个变更中更新文档。
- 路线图记录未来工作，不把模型猜测、未实现设计或历史缺陷清单写成当前事实。
- 设计文档可以记录取舍，但必须标明适用的代码基线和实施状态。
- `archive/` 只保存历史 PRD、旧设计、评审快照和迁移前分析，不作为新功能开发依据。
- 一个主题只保留一份权威说明；其他文档通过链接引用，不复制整段内容。

## 历史归档

`archive/` 中的文件用于追溯决策和迁移背景，包括旧版 PRD、单体架构详细设计、早期功能路线、改进清单、架构评审报告和迁移前上下文分析。归档文件可以帮助理解历史，但不代表当前实现。
