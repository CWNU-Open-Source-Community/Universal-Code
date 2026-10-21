# macOS 下载与发行边界

Mac 交付包含 Apple Silicon（arm64）和 Intel（amd64）两份 ZIP。文件名绑定完整版本、
架构与源码 revision 前 12 位；manifest 内保存完整 revision。包内应用名为
`TraverseBoard.app`，原 bundle identifier `workbench.prayu.desktop` 和数据目录不变。

## 获取候选包

持续更新入口：[Desktop release 双架构打包记录](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/release-desktop.yml)。也可从 [主干中央 CI](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/ci.yml?query=branch%3Amain) 选择已成功且含 `TraverseBoard-macos-preview-arm64` artifact 的运行，获取 arm64 预览；主干 CI 不提供 Intel 包。下载前核对运行的源码 revision 和包内 metadata。

For newer candidates, use the [Desktop release run list](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/release-desktop.yml) for both architectures, or a successful [main-branch CI run](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/ci.yml?query=branch%3Amain) containing `TraverseBoard-macos-preview-arm64` for arm64 only. Check the run revision against the package metadata.

截至 2026-10-20，[公开 Releases](https://github.com/CWNU-Open-Source-Community/Universal-Code/releases) 尚无 Mac 资产。
[PR #411 的双架构打包检查](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/runs/39306404692)
已完成原生构建与归档复验。以下链接对应 `v1.0.0-pr.411`、源码合并检出
`4158910bc9f478b3511459e0b5babfdc0d4def97`；完整中央 CI 状态见 [PR 检查](https://github.com/CWNU-Open-Source-Community/Universal-Code/pull/411/checks)。
下载需要登录 GitHub；两份 artifact 均保留 30 天，**2026-11-18 19:02 UTC（两种架构）到期**。

Public Releases do not yet contain Mac assets. The following `v1.0.0-pr.411` previews passed native build and archive verification in [Desktop release](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/runs/39306404692), from merge revision `4158910bc9f478b3511459e0b5babfdc0d4def97`. A GitHub login is required. Downloads expire on **2026-11-18 at 19:02 UTC (both architectures)**.

| Mac 类型 / Architecture | 下载 / Download |
| --- | --- |
| Apple Silicon（M 系列 / arm64） | [预览包 / Preview](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/runs/39306404692/artifacts/12214676343) |
| Intel（amd64） | [预览包 / Preview](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/runs/39306404692/artifacts/12214631395) |

在对应候选的 **Desktop release** 检查页面下载两个 `TraverseBoard-macos-<arch>-<revision>`
artifact。artifact 外层是 GitHub 的传输 ZIP；解开后取得产品 ZIP、manifest 和 checksum。
到期后在 [Desktop release 工作流](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/release-desktop.yml)选择较新的成功运行，核对版本、revision 和架构。
CI artifact 不是 GitHub Release。预发布 tag 的流程成功后，同一 publisher
才会把两架构文件发布到同一个 Release 的 Assets 中。

解开产品 ZIP 后打开 `TraverseBoard.app`，无需编译前端或单独启动后端。包内保留历史名称的
`Start-Prayu-Operator-Preview.command` 作为兼容入口，并提供中英文使用说明。
当前包只有 ad-hoc 签名；macOS 可能阻止首次启动。没有公证和真机验收证据时，不称正式 Mac 发行。

Extract the Actions transport ZIP, then the product ZIP inside it, and open `TraverseBoard.app`. The UI and backend are included. The bundle is ad-hoc signed and unnotarized, so macOS may block its first launch.

## 构建与核验

本机源码构建需要 Xcode Command Line Tools、[go.mod](../go.mod) 固定的 Go（当前 1.26.10）、Node.js 24（CI 为 24.22.1）和 CGO。在仓库根目录运行：

```sh
CGO_ENABLED=1 bash scripts/build-desktop-darwin.sh -Version v1.0.0-dev
open build/desktop/TraverseBoard.app
```

该脚本安装前端锁定依赖、执行检查，并以 `desktop,production` 标签和 Mac 所需框架链接参数生成应用。

For a local source build, install Xcode Command Line Tools, the Go version pinned in [go.mod](../go.mod), and Node.js 24, then run the commands above from the repository root. The script builds the UI and app with CGO and the required macOS linker options.

### 发行归档 / Release archive

在干净的 Mac checkout 中先运行 `cd web && npm ci && npm run build`，再回到仓库根目录：

```sh
bash scripts/release-desktop-darwin.sh v1.0.0-rc.1 "$(git rev-parse HEAD)" "$(go env GOARCH)"
```

流水线固定 `macos-15` ARM64 与 `macos-15-intel` Intel runner；检查原生 CPU、Go target、
Mach-O CPU 三者一致。构建两次比较二进制，再 ad-hoc 签名。`CFBundleVersion` 和
`CFBundleShortVersionString` 使用数字版本，完整预发布名称保留在 build metadata。
最低构建目标为 macOS 12；不代表已在所有 macOS 12+ 真机上通过验收。

产物位于 `build/macos-public`。ZIP 包含完整应用、指南、兼容入口、元数据、兼容报告、
SBOM、NOTICE 与 LICENSE。`ditto` 保留 POSIX 权限；新目录解压后再次核对全部文件、
可执行权限、Mach-O、部署目标、Go metadata 和 codesign。
manifest 与 checksum 绑定完整 ZIP。Windows publisher 复验 ZIP 内的实际文件、架构、
版本、revision 和全部哈希，再汇总两架构的精确资产白名单；只有 publisher 可写 Release。

## 正式版尚缺什么

PR 与预发布生成明确标注的 Mac preview。stable prepare 可以生成内部预览候选，
并不等于完成签名请求或正式发布。**stable finalize 当前会明确失败**：尚未实现有证据支持的
Developer ID 签名、公证、staple 验证及绑定最终分发包的真机验收接收流程。
不会因设置 `release_ready=true` 或通过 CI 编译而开放。

后续正式 Mac 接入至少需要：Apple Developer 身份及受保护签名/公证配置；验证实际
签名者、公证票据和最终包；在干净 Mac 验证 Finder 启动、模型配置（含失败提示）、退出与
重启后数据恢复，两架构分别记录。[macOS Keychain 实现](mac-keychain.md) 已在
[main `2bbdab93` 的原生 Mac CI](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/runs/39298548550/job/118217578657)
通过隔离 Keychain 持久化测试，并随上方新候选编译。实际 `.app` 的授权提示、取消、锁定恢复、
重启与升级体验仍需在 Mac 登录会话验收。

Mac 的普通 Agent Browser 当前返回 `platform_unsupported`，原生 Computer Use 与 Local Sandbox
也有独立平台实现缺口。不得通过删除身份校验、明文保存凭据或默认提升权限开放这些能力。

Native macOS CI passed the isolated Keychain persistence test and built the implementation into the newer candidate. Actual application permission prompts, cancellation, locked-keychain recovery, restart, and upgrade still need acceptance in a Mac login session. Ordinary Agent Browser, native Computer Use, and Local Sandbox have separate Mac platform gaps.

现有 Windows 签名、Store、精确 CI revision、Draft evidence 和 attestation 门保持。
Mac job 不预先向 Draft 添加附件，避免破坏 Windows 的精确证据清单。

参考：[GitHub 原生 runner 清单](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)、
[Apple 版本字段](https://developer.apple.com/documentation/bundleresources/information-property-list/cfbundleshortversionstring)、
[Apple 公证流程](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow)。
