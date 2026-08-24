# 发布、安装、升级与回滚

## 1. 当前状态与边界

仓库已经具备 Tag 驱动的发布流水线，但当前文档不表示已经创建过正式 GitHub Release。正式发布必须以 `v<SemVer>` Tag 触发 `.github/workflows/release.yml`，并通过全部门禁后才会上传制品。

每个版本提供六个分发归档：Linux、Windows、macOS 分别覆盖 `amd64` 与 `arm64`。Windows 使用 ZIP，Linux/macOS 使用 `tar.gz`；归档包含 `codepilot` 可执行文件、README、LICENSE、CHANGELOG 和本说明。发布页同时包含 `checksums.txt` 与每个归档的 SPDX JSON SBOM。当前没有 MSI、DMG、系统包管理器仓库、代码签名或自动更新器，不应把 ZIP/tar 归档描述为经过平台签名的安装器。

## 2. 版本与制品身份

版本使用 Semantic Versioning。Git Tag 带 `v`，程序内版本不带 `v`，例如 Tag `v1.2.3` 对应：

```text
codepilot 1.2.3 (commit 0123456789abcdef..., built 2026-08-24T12:30:00Z)
```

版本、完整 Git commit 和 commit timestamp 由发布构建统一注入。`--version` 只读取编译期常量，不打开 Workspace、Provider 或 Session，因此也可用于安装后的无副作用核验。

## 3. 发布前验收

发布维护者按以下顺序操作：

1. 将 `CHANGELOG.md` 中准备发布的内容从 `[Unreleased]` 移入 `## [X.Y.Z] - YYYY-MM-DD`；日期必须与最终发布 commit 的日期一致。
2. 确认目标 commit 的常规 CI 已在 Windows、Linux、macOS 原生 runner 全部成功；可选真实 Provider 探测只在受保护环境中人工启用，不允许 Tag 工作流自行读取或消费 Provider 凭证。
3. 确认工作树干净，创建指向该 commit 的 `vX.Y.Z` Tag。验收器会拒绝非 SemVer、Tag/commit 不一致、commit 时间不一致、脏工作树或缺少精确 CHANGELOG 标题。
4. Tag 工作流重新执行完整离线测试、Python E2E 和 `go vet`，然后用同一组版本元数据在两个独立目录分别构建六个目标并逐字节比较 SHA-256。
5. 只有验收 job 成功后，发布 job 才可获得 `contents: write` 权限。它使用锁定版本的 GoReleaser 与 Syft 生成归档、SHA-256 清单和归档级 SBOM。
6. 发布完成后，在至少一个 Windows、Linux、macOS 目标上下载归档，校验 checksum，执行 `codepilot --version`，再对隔离的 StateDir 执行一次启动/退出冒烟测试。没有实际通过的目标不得在路线图中标记为已验证。

本地可对尚未提交的实现验证二进制可复现性；省略 `--require-clean` 和 `--require-changelog` 不等价于正式发布门禁：

```powershell
$commit = git rev-parse HEAD
$date = git show -s --format=%cI $commit
go run ./cmd/releasecheck --version 0.0.0-dev --commit $commit --date $date
```

正式工作流额外传入 `--require-clean --require-changelog`。

## 4. 可复现构建原则

“可复现”在这里指：固定相同源码 commit、Go toolchain、模块依赖和版本元数据时，每个目标的裸二进制应逐字节一致。实现依赖以下约束：

- `CGO_ENABLED=0`，避免宿主 C toolchain 与动态库进入结果；
- 固定 `GOOS/GOARCH` 六目标矩阵；
- `-trimpath` 移除构建机绝对路径，`-buildvcs=false` 避免 Go 自动注入另一套 VCS 状态，`-buildid=` 移除变化的 linker build ID；
- 构建时间使用 Git commit timestamp，不使用流水线当前时间；
- GoReleaser 的 binary 与归档中文件 mtime 都使用 commit 时间；
- `go.mod/go.sum` 由常规 CI 的 tidy-diff 门禁锁定。

`cmd/releasecheck` 会真正构建两轮并比较裸二进制，不是只检查 YAML 字段。该结论仍限定于同一受控 toolchain；若要声称独立第三方可复现，还需在另一台干净机器上用相同 Go 版本重新构建并比对发布 digest。

## 5. 安装与校验

从对应 GitHub Release 下载与你的操作系统和 CPU 匹配的归档、`checksums.txt` 和同名 `.sbom.json`。在解压或执行前核对 SHA-256。

Windows PowerShell：

```powershell
Get-FileHash .\codepilot_1.2.3_windows_amd64.zip -Algorithm SHA256
Select-String -Path .\checksums.txt -Pattern "codepilot_1.2.3_windows_amd64.zip"
Expand-Archive .\codepilot_1.2.3_windows_amd64.zip -DestinationPath .\codepilot-1.2.3
.\codepilot-1.2.3\codepilot_1.2.3_windows_amd64\codepilot.exe --version
```

Linux：

```bash
grep 'codepilot_1.2.3_linux_amd64.tar.gz' checksums.txt | sha256sum --check
tar -xzf codepilot_1.2.3_linux_amd64.tar.gz
./codepilot_1.2.3_linux_amd64/codepilot --version
```

macOS：

```bash
grep 'codepilot_1.2.3_darwin_arm64.tar.gz' checksums.txt | shasum -a 256 --check
tar -xzf codepilot_1.2.3_darwin_arm64.tar.gz
./codepilot_1.2.3_darwin_arm64/codepilot --version
```

确认输出中的版本、commit 与发布页一致后，再把二进制复制到用户自己的 PATH 目录。不要从聊天记录、临时网盘或未经校验的镜像安装。

## 6. 升级

CodePilot 不做静默自更新。升级由用户显式替换单个二进制：

1. 结束所有 CodePilot 进程；同一 StateDir 不能同时由新旧版本打开。
2. 备份当前二进制、ConfigDir 和 StateDir。显式传入过 `--config-dir/--state-dir` 时以该路径为准；否则配置位于 Go `os.UserConfigDir()/CodePilot`，状态在 Windows 为 `%LOCALAPPDATA%/CodePilot/State`，其他平台为 `os.UserCacheDir()/codepilot/state`。
3. 下载、校验并解压新归档，在原位置旁保留带版本号的目录；先执行 `--version`，再切换 PATH 或启动脚本指向新二进制。
4. 使用 `codepilot doctor` 对实际 StateDir 做只读一致性检查。只有明确看到问题且理解修复动作时才执行 `codepilot repair`。
5. 打开一个非关键 Git worktree 做会话恢复和 Provider 预检；确认后再继续日常使用。

系统 Keyring 中的 API Key 不在 ConfigDir/StateDir 备份内，升级不会复制或导出它；Provider profile 只保存 credential reference。不要为了备份而把 Key 写入普通文件。

## 7. 回滚

如果新版本尚未写入状态，结束进程并把 PATH 切回已保留且校验过的旧二进制即可。如果新版本已经打开或修改过 Session：

1. 先保存当前 ConfigDir/StateDir 的故障副本，不能直接覆盖唯一现场。
2. 恢复升级前的 ConfigDir/StateDir 备份，再切回旧二进制。
3. 运行旧版本支持的只读诊断；不要手工截断 JSONL、删除 writer lock 或拼接 journal。

旧版本若不认识新格式，应停止启动并恢复成套备份，不能只恢复单个 metadata 或 journal 文件。Keyring credential 通常可继续由同一 credential reference 使用；如果 Profile 也被回滚，需确认引用仍一致，而不是导出 secret。
