## macOS downloads / macOS 下载

{{MACOS_VERSION}} 的 Mac 包是**未公证试用版**，分别提供 Apple Silicon（arm64）和 Intel（amd64）ZIP。
在本页 Assets 中选择名称含对应架构的 `TraverseBoard-…-macos-…-preview.zip`，解压后打开
`TraverseBoard.app`。每个包都有同名 `.sha256` 和 `.manifest.json` 校验文件；启动脚本、
本地使用说明、版本元数据、SBOM 和许可证也在 ZIP 内。

The Mac downloads are **unnotarized previews** for Apple Silicon (arm64) and Intel (amd64).
Download the matching `TraverseBoard-…-macos-…-preview.zip` from Assets, extract it, and open
`TraverseBoard.app`. Matching `.sha256` and `.manifest.json` sidecars bind the complete archive.
The ZIP includes the launcher, local guide, build metadata, SBOM and licenses.

Mac 信任状态 / Mac trust state: **ad-hoc signed; not notarized; release_ready=false**.
尚未通过干净 Mac 的首次启动、模型配置和退出恢复验收；macOS 可能阻止首次打开。
请先阅读包内 `LOCAL-TEST-GUIDE.txt`。系统 Keychain 凭据存储已实现；实际应用中的
锁定、授权取消和构建升级体验仍待验收。可启动独立 Terminal.app；内嵌 Debug 用户终端
尚缺 macOS 锁屏/休眠监控，Local Sandbox、受限浏览器与完整 CDP 仍不可用。
Clean-machine launch, model setup and restart recovery remain unverified. macOS may block the
first launch. Read `LOCAL-TEST-GUIDE.txt` before testing. System Keychain storage is implemented;
locking, cancelled access and upgrades still need application testing. The separate Terminal.app
can be launched; the embedded Debug terminal lacks macOS lock/sleep monitoring. Local Sandbox,
the restricted browser and Full CDP remain unavailable.
