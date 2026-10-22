# Windows 下载与发行说明 / Windows distribution

[首页](../README.md) | [English home](../README.en.md) | [Mac 下载](macos-release.md)

## 下载与启动 / Download and launch

截至 2026-10-20，最新公开 Release 为 [v0.1.0-pr195-preview](https://github.com/CWNU-Open-Source-Community/Universal-Code/releases/tag/v0.1.0-pr195-preview)，发布于 2026-09-05、源码为 `8df0df9ad48a9d53c03fdb63ac78d5c7199219b0`。其中的 `TraverseBoard.exe` 是旧预览，不能用于验收当前主线功能。v1.0.0 进度见 [发布说明](releases/v1.0.0.md)。

As of 2026-10-20, the newest public Release is the older [v0.1.0-pr195-preview](https://github.com/CWNU-Open-Source-Community/Universal-Code/releases/tag/v0.1.0-pr195-preview), published on 2026-09-05 from revision `8df0df9ad48a9d53c03fdb63ac78d5c7199219b0`. Its `TraverseBoard.exe` predates current main-branch features.

### 当前测试候选 / Current test candidate

长期入口为 [Desktop release 打包记录](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/release-desktop.yml)，成功运行中的 Windows artifact 可用于测试。当前中央 CI 主干运行不上传 Windows 应用包。

The [Desktop release run list](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/release-desktop.yml) remains the ongoing Windows candidate entry point. Main-branch central CI does not currently upload a Windows application package.

[PR #411 的 Desktop release 打包检查](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/runs/39306404692)已通过。以下候选版本为 `v1.0.0-pr.411`，源码绑定 PR 合并检出 `4158910bc9f478b3511459e0b5babfdc0d4def97`；它与 PR 分支头提交不同。完整中央 CI 状态见 [PR 检查](https://github.com/CWNU-Open-Source-Community/Universal-Code/pull/411/checks)。

The [PR #411 Desktop release packaging check](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/runs/39306404692) passed. The candidate is `v1.0.0-pr.411`, built from the PR merge checkout `4158910bc9f478b3511459e0b5babfdc0d4def97`. See the [PR checks](https://github.com/CWNU-Open-Source-Community/Universal-Code/pull/411/checks) for the full central CI status.

1. 登录 GitHub，下载 [Windows 测试候选](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/runs/39306404692/artifacts/12215261616)（`TraverseBoard-windows-release-v1.0.0-pr.411`）。该 artifact 保留 14 天，**2026-11-02 19:13 UTC 到期**。
2. 解开 GitHub Actions 的传输 ZIP，双击根目录的 **`TraverseBoard.exe`**。Windows 10/11 需要已安装的 Edge WebView2 Evergreen Runtime。
3. 这是未签名测试候选，可能出现 SmartScreen 提示。窗口显示 Universal-Code，文件仍沿用历史名。测试说明见[本机测试指南](../packaging/windows/LOCAL-TEST-GUIDE.txt)。

Sign in to GitHub, download the [Windows test candidate](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/runs/39306404692/artifacts/12215261616), and extract the Actions transport ZIP. It expires on **2026-11-02 at 19:13 UTC**. Double-click the root `TraverseBoard.exe` on Windows 10/11 with Edge WebView2 Evergreen Runtime installed. This unsigned candidate may trigger SmartScreen; the application displays Universal-Code while retaining the historical filename.

候选到期后，在 [Desktop release 工作流](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/release-desktop.yml)选择较新的成功运行，核对版本、源码 revision 和 `TraverseBoard-windows-release-*` artifact。Actions 测试包与公开 Release 的直发 EXE 分发方式不同。只读启动入口为 `TraverseBoard.exe --safe-view`。

If this artifact has expired, select a newer successful [Desktop release run](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/workflows/release-desktop.yml), check its version and source revision, and download its `TraverseBoard-windows-release-*` artifact. `TraverseBoard.exe --safe-view` starts the read-only view.

### 从源码构建与启动 / Build and launch from source

在仓库根目录使用 PowerShell、[go.mod](../go.mod) 固定的 Go（当前 1.26.10）与 Node.js 24（CI 为 24.22.1）：

```powershell
.\scripts\build-desktop.ps1 -Version v1.0.0-dev
.\build\desktop\TraverseBoard.exe
```

构建脚本包含 `npm ci`、前端检查与构建、Go 桌面检查，以及 `desktop,production,wv2runtime.error` 标签；生成的 EXE 内含界面和后端。若前端已单独构建，可用 `-SkipFrontend`。仅运行不带标签的 `go run ./cmd/cyberagent-desktop` 会因构建约束而失败。

Run the commands above in PowerShell from the repository root using the Go version pinned in [go.mod](../go.mod) and Node.js 24. The script checks and builds the frontend and desktop application with the required tags. Use `-SkipFrontend` only when `web/dist` has already been built. The resulting EXE includes the UI and backend.

## 成品与发布合同 / Deliverables and release contract

Windows 面向普通用户只提供两个成品；某个版本实际提供哪一项，必须以该版本的 GitHub
Release 和 Microsoft Store 页面为准：

1. **Microsoft Store 包**：从 Store 安装和更新。提交候选必须使用 Partner Center
   分配的精确 identity 并声明真实处理器架构；只有 Partner Center 接收且认证通过后
   才能称为 Store 成品。仓库生成的 `PrayuDesktop.msix`、Actions artifact 或本地旁加载
   包都不能证明 Store 已通过，也不能证明正式签名已经取得。
2. **唯一的直发入口 `TraverseBoard.exe`**：从 GitHub Release 直接下载并双击，无需
   ZIP、CMD、参数或单独后端。稳定直发版本需要可信 Authenticode 签名；未签名构建
   只能明确标记为预发布候选，并可能出现 SmartScreen“未知发布者”提示。

There are exactly two user-facing Windows deliverables. Availability for a particular version
must be read from that version's GitHub Release and Microsoft Store listing:

1. **Microsoft Store package**, installed and updated through Store. Its submission candidate
   must use the exact Partner Center identity and real processor architecture. A repository-built
   `PrayuDesktop.msix`, Actions artifact, or sideload package does not prove Store certification
   or production signing.
2. **The sole direct entry, `TraverseBoard.exe`**, downloaded from GitHub Release and started by
   double-click, with no ZIP, CMD, flags, or separate backend. A stable direct release requires
   trusted Authenticode signing; an unsigned build remains an explicitly labelled prerelease
   candidate and may trigger SmartScreen.

两者使用同一版本、源码提交、内嵌前端和发布门证据；Authenticode 签名导致字节变化时，
证据同时保留 Store 内可复现 payload hash 与公开签名 EXE 的 artifact hash，并由 GitHub
provenance/SBOM attestation 绑定最终对象。MSIX 的设计合同将安装文件与外部用户数据目录
分开；在称为 Store 成品前，仍必须用 Windows 10/11 实机矩阵验证
升级、卸载与重装时的 Workspace、凭证及 SQLite 行为。WebView2 缺失或版本过旧时，
应用只显示有界本机指导，不隐式安装依赖。

便携 ZIP 仅供 CI、兼容性、packaged E2E 与可复现性验证，不是第三种产品，也不能上传
到 Store。`SHA256SUMS`、SBOM、NOTICE、release metadata、manifest 与 Standard Code
报告属于校验/来源 sidecar，不是可启动产品。稳定版还会公开签名 request、handoff 与
signing evidence sidecar；签名服务返回的 `TraverseBoard-signed.exe` 只用于受控接收，
不会作为第二个 EXE 发布。源码与新 Release 均不再提供
`Start-Prayu-Operator-Preview.cmd` 或其他 Start 脚本；历史 Release 的旧 ZIP、旧启动器
名称和校验和保持不变。

The portable ZIP is retained only for CI, compatibility, packaged E2E, and reproducibility
checks; it is neither a third product nor a Store upload. Checksums, SBOM, NOTICE, metadata,
manifests, and Standard Code reports are verification/provenance sidecars. A stable release also
publishes the signing request, handoff, and signing evidence as public sidecars; the signer-returned
`TraverseBoard-signed.exe` is intake-only and is not published as a second executable. Source and
new releases provide neither `Start-Prayu-Operator-Preview.cmd` nor another Start script, and
historical Release assets remain immutable.

稳定版自动化会消费受保护的外部签名 handoff，复核允许的 signer 与 RFC 3161 时间戳，
记录签名前后 hash，并由独立最小权限 job 生成 GitHub provenance/SBOM attestation。Store
完成态还要求实际安装包为 `SignatureKind=Store`，identity/version/architecture/payload 精确
匹配，并具备四条 Windows 10/11 × 100%/200% DPI × 中文 IME 生命周期 row。Partner Center
export、截图和操作者生命周期记录仍是 reviewer-attested 外部证据；hash 只能发现后续漂移，
不能把它们变成加密证明。因此
[Issue #123](https://github.com/CWNU-Open-Source-Community/Traverse-Board/issues/123) 会保持开启，
直到同一真实候选补齐并通过全部证据门。

Stable automation consumes a protected external signing handoff, verifies the approved signer and
RFC 3161 timestamp, records pre/post-sign hashes, and generates permission-scoped GitHub
provenance/SBOM attestations. Store completion additionally requires a real installed package with
`SignatureKind=Store`, the exact identity/version/architecture/payload, and four hash-bound Windows
10/11 × 100%/200% DPI × Chinese-IME lifecycle rows. Partner Center exports, screenshots, and
operator lifecycle records remain reviewer-attested external evidence: hashing them detects later
drift but does not turn them into cryptographic proof. [Issue #123](https://github.com/CWNU-Open-Source-Community/Traverse-Board/issues/123)
therefore remains open until one real candidate supplies and passes all of this evidence.

具体发布边界见 [ADR 0145](adr/0145-windows-two-deliverable-release-contract.md)。
The exact release boundary is defined by [ADR 0145](adr/0145-windows-two-deliverable-release-contract.md).
Partner Center 的 identity、版本、上传与 `runFullTrust` 认证步骤见
[Microsoft Store submission runbook](../packaging/windows/STORE-SUBMISSION.md)。

## CI evidence and data compatibility

The `Desktop release` workflow keeps pull-request evidence honest: its packaged bootstrap remains
`needs_full_matrix`. A non-PR candidate must obtain the independently produced #182 product report
and #181 security report from the matching Draft Release, bind both reports to the current EXE,
internal verification ZIP, source revision, fixture, and attack matrix in
`standard_code_release_gate.v1`, and reverify that aggregate before publication. Missing,
tampered, replayed, skipped, or waived evidence fails closed. Prerelease versions remain explicitly
marked. A stable release additionally consumes a protected external Authenticode handoff, verifies
the approved signer and RFC 3161 timestamp, records pre-sign payload and post-sign artifact hashes,
and requires complete Partner Center identity/version configuration. The stable public Release
retains the signing request, handoff, and verified signing result as public sidecars; the returned
`TraverseBoard-signed.exe` is intake-only. A separate permission-scoped job creates GitHub
provenance and SBOM attestations. Store certification stays external until
`finalize-windows-release.ps1` verifies a real installed package with `SignatureKind=Store`, the
exact identity/version/architecture/payload, and the four Windows/DPI/IME lifecycle rows. See the
[Beta release gate](standard-code-release-gate.md),
[ADR 0144](adr/0144-standard-code-release-gate-aggregation.md), and
[ADR 0145](adr/0145-windows-two-deliverable-release-contract.md).

> [!NOTE]
> Portable means install-free application files, not directory-local data. The default data home remains `%USERPROFILE%\.cyberagent-workbench`, so extracting another ZIP reuses the same database. `v0.1.0-rc.2` cannot upgrade one exact pre-final Windows preview v97 history; schema v125 in later builds accepts that exact history and transactionally restores the canonical trigger without deleting or falsifying old data. See [ADR 0126](adr/0126-legacy-v97-docker-trigger-compatibility.md).
