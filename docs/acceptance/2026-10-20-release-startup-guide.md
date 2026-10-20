# 发行下载与源码启动说明验收

日期：2026-10-20（Asia/Hong_Kong）。文档修改基于 main
`2bbdab93ec244426b431cbc2f0595917e802931e`。本批修正中英文 README、Windows/Mac
下载说明和源码启动命令；程序代码及发行门未修改。

## 文档前后截图

先渲染原文件并截图，再编辑源码、用相同渲染器、浏览器与视口配置截取修改结果。
渲染使用 Python `markdown-it-py 4.2.0` 的 CommonMark、HTML 与表格支持；浏览器为
Chrome `155.0.0.0`，Playwright CLI `0.1.22`，视口 1440×1200。
截图为浏览器实际渲染，未拼接或改图。完整页面截图高度随真实内容变化。

| 实际源文档范围 | 修改前 | 修改后 |
| --- | --- | --- |
| 中文 README 的“快速开始”，截至“模型配置与连接”之前 | [截图](assets/2026-10-20-release-startup-guide/before-readme-zh.png) | [截图](assets/2026-10-20-release-startup-guide/after-readme-zh.png) |
| English README “Quick Start”，截至 “Connecting Models” 之前 | [截图](assets/2026-10-20-release-startup-guide/before-readme-en.png) | [截图](assets/2026-10-20-release-startup-guide/after-readme-en.png) |
| Windows 说明开头，截至“成品与发布合同”之前 | [截图](assets/2026-10-20-release-startup-guide/before-windows.png) | [截图](assets/2026-10-20-release-startup-guide/after-windows.png) |
| Mac 说明全文 | [截图](assets/2026-10-20-release-startup-guide/before-macos.png) | [截图](assets/2026-10-20-release-startup-guide/after-macos.png) |

原文件、修改后文件与实际渲染片段的 SHA256 见
[source-manifest.json](assets/2026-10-20-release-startup-guide/source-manifest.json)。
原图保留在同目录。

## Windows 源码构建与实际启动

本机为 Windows 11 `10.0.26200` amd64，Go `1.26.9`，Node `24.14.0`，npm `11.9.0`，
`CGO_ENABLED=1`。源码版本为上述 main 加本批文档及证据，metadata 如实记录
`modified=true`。本地开发包版本 `v1.0.0-dev`，EXE 大小 120,064,512 bytes，SHA256：

```text
0c6834a0fef3295dffbe0fcc8b117623ea5519be10dc9cc818004eed53809a1a
```

| 已运行命令 | 实际结果 |
| --- | --- |
| `npm ci`、`npm run build`（web 目录） | 成功，生成生产界面 |
| `scripts/build-desktop.ps1 -Version v1.0.0-dev` | 在前端测试处失败：253 文件中 251 通过；2478 测试中 3 个达到 5 秒超时，尚未执行该次 Go 打包 |
| `npm test -- --maxWorkers=2 src/components/plan-delivery-work-items.test.tsx src/v2/components/provider-settings.test.tsx` | 两个失败文件定向复测通过，66 个测试通过；未修改测试或超时时间 |
| `scripts/build-desktop.ps1 -Version v1.0.0-dev -SkipFrontend` | 成功，桌面 Go 检查通过，生成上述 EXE；兼容检查 `automated_checks_passed=true`、`release_ready=false` |
| `scripts/smoke-desktop-operator-preview.ps1 -StartupTimeoutSeconds 30` | 成功：隔离 SQLite 与真实原生窗口均创建；正常关闭，退出码 0，`forced=false`，脚本清理其数据目录 |
| `go test ./internal/releasegate -run 'TestDirectEXESigningHandoffIsFailClosed\|TestWindowsCompletionConsumesExternalFactsWithoutRewritingCandidate' -count=1` | 成功 |
| `python -B -m unittest discover -s scripts -p test_macos_release.py -v` | 成功，16 个测试通过 |
| `git diff --check` | 通过 |

完整构建命令的超时发生于 `plan-delivery-work-items.test.tsx` 中的最终交付证据测试，
以及 `provider-settings.test.tsx` 中的模型数量上限、一次性 API Key 编辑测试。
上表分别记录原始失败、低并发复测和跳过已构建前端后的打包结果。

另从同一 EXE 重开新的隔离 `CYBERAGENT_HOME`，未传启动参数。Computer Use 读到
原生窗口中的 `http://wails.localhost/#/new`、中文新对话界面和未配置模型状态。
重开窗口的自有进程 PID 41888 通过 Computer Use 的 `Alt+F4` 关闭后，已确认退出，
未强制终止；本次退出码未单独采集。未连接模型或发送任务。

## Mac 与发行状态

本机未运行 Mac 应用。Mac 原生构建及隔离 Keychain 持久化结果引用
[main `2bbdab93` 的成功 CI](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/runs/39298548550/job/118217578657)。
双架构候选的精确版本、源码 revision、下载和到期时间记录于
[Mac 说明](../macos-release.md)，Windows 候选见 [Windows 说明](../windows-release.md)。
工作流列表作为持续更新入口；仓库没有当前版本的 `latest-preview` 标签，主干 CI 仅上传
Mac arm64 应用包。

2026-10-20 只读检查得到仓库级 Actions Secrets 数量为 0，Environments 数量为 0；未读取
凭据值。Mac 正式发行仍需外部 Apple Developer 签名/公证配置，以及最终包的实际应用验收。
已有原生 Keychain 隔离测试不覆盖 `.app` 授权提示、取消、锁定恢复或升级体验。
本批未发布 Release、触发签名或改变 `release_ready`。
