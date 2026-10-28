# macOS 下载与发行边界

[首页](../README.md) | [English home](../README.en.md) | [Windows 下载](windows-release.md)

## 下载与启动 / Download and launch

<!-- public-downloads:start -->
当前公开预览版 / Current public preview: [**v1.0.0-preview.20261020**](https://github.com/CWNU-Open-Source-Community/Universal-Code/releases/tag/v1.0.0-preview.20261020)，发布于 / published on **2026-10-20**，已提供两种架构的 DMG。

| Mac 类型 / Architecture | 下载 / Download |
| --- | --- |
| Apple Silicon（M 系列 / arm64） | 在 [Release Assets](https://github.com/CWNU-Open-Source-Community/Universal-Code/releases/tag/v1.0.0-preview.20261020) 选择名称含 `macos-arm64` 的 `.dmg` |
| Intel（amd64） | 在 [Release Assets](https://github.com/CWNU-Open-Source-Community/Universal-Code/releases/tag/v1.0.0-preview.20261020) 选择名称含 `macos-amd64` 的 `.dmg` |

1. 按处理器架构下载 DMG，用 `shasum -a 256 /path/to/download.dmg` 与同一 Release 的 [SHA256SUMS](https://github.com/CWNU-Open-Source-Community/Universal-Code/releases/download/v1.0.0-preview.20261020/SHA256SUMS) 核对。
2. 打开 DMG，将 **`TraverseBoard.app`** 拖到 **Applications**，复制完成后弹出映像，再从 Applications 打开应用。前端和后端均已内置。
3. 包只有 **ad-hoc 签名，未经 Apple 公证**，首次打开可能被 Gatekeeper 阻止。最低构建目标为 macOS 12；该版本的原生启动检查在 macOS 15 完成，完整交互、真实模型调用和升级恢复仍需验收。具体证据与限制见 Release 说明。

Choose the DMG matching your processor from Release Assets, verify it against the same release's `SHA256SUMS`, then drag `TraverseBoard.app` into Applications and eject the image. The UI and backend are included. These **ad-hoc signed, unnotarized previews** may be blocked by Gatekeeper. The minimum build target is macOS 12; native startup was checked on macOS 15. Full interaction, real-model workflows and upgrade recovery remain unverified.

公开 Release 下载无需登录 GitHub，不受 Actions artifact 保留期限限制。
Public Release downloads require no GitHub login and are not subject to Actions artifact retention limits.
<!-- public-downloads:end -->

## 更新的测试候选 / Newer test candidates

[Desktop release 双架构打包记录](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/release-desktop.yml)提供两种架构的测试候选。选择成功运行并核对版本、源码 revision 和架构；下载需要 GitHub 登录且会过期。
`TraverseBoard-macos-dmg-<arch>-<revision>` artifact 内含 DMG 与 checksum；
`TraverseBoard-macos-<arch>-<revision>` artifact 内含产品 ZIP、manifest 与 checksum。
先解开 Actions 的传输 ZIP；若选择产品 ZIP，再解开内层 ZIP 后打开 `TraverseBoard.app`。

For newer candidates, use a successful Desktop release run for the matching architecture and source revision. Actions downloads require login and expire. The DMG artifact contains an image and checksum; the archive artifact contains a product ZIP, manifest and checksum. Unpack the outer Actions transport ZIP first.

应用名仍为 `TraverseBoard.app`，bundle identifier `workbench.prayu.desktop` 和数据目录不变。
发行文件名绑定版本、架构与源码 revision 前 12 位；ZIP manifest 保存完整 revision。

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
manifest 与 checksum 绑定完整 ZIP。DMG 来自该核验后的 ZIP，原生 Mac job 会挂载映像
核对包内字节、权限与签名。Windows publisher 复验 ZIP 内的实际文件、架构、版本、
revision 和全部哈希，并下载同次运行的 DMG、核对其独立 checksum，再汇总两架构的
ZIP、DMG 与校验文件。公开 `SHA256SUMS` 同时包含 Windows EXE 和两份 DMG；
只有 publisher 可写 Release，公开前须通过四份下载文档与实际资产的一致性检查。

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

Mac 浏览器、原生 Computer Use 与 Local Sandbox 的可用能力须以当前构建的运行时能力报告
为准。浏览器启动检查不等于完整用户工作流验收；不得通过删除身份校验、明文保存凭据
或默认提升权限开放尚不可用的能力。

Native macOS CI passed the isolated Keychain persistence test. Actual application permission prompts, cancellation, locked-keychain recovery, restart, and upgrade still need acceptance in a Mac login session. Check the current build's runtime capability report for browser, native Computer Use and Local Sandbox availability; browser startup checks do not establish complete workflow acceptance.

现有 Windows 签名、Store、精确 CI revision、Draft evidence 和 attestation 门保持。
Mac job 不预先向 Draft 添加附件，避免破坏 Windows 的精确证据清单。

参考：[GitHub 原生 runner 清单](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)、
[Apple 版本字段](https://developer.apple.com/documentation/bundleresources/information-property-list/cfbundleshortversionstring)、
[Apple 公证流程](https://developer.apple.com/documentation/security/customizing-the-notarization-workflow)。
