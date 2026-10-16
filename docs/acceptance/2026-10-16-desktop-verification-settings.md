# 桌面验证偏好与重启恢复验收

日期：2026-10-16。关联 [#340](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/340)；设计见 [ADR 0191](../adr/0191-desktop-verification-preferences.md)。

## 构建与环境

在 Windows 上，以主线 `5b9d2e9bf06fe327bcfbd42fe987d978b1e43172` 加本 PR 改动构建 Wails 生产模式 EXE。所有人工启动均无命令行参数，通过进程级 `CYBERAGENT_HOME` 指定独立测试数据目录；未使用用户的模型凭据或工作数据。

```powershell
go build -tags desktop,production,wv2runtime.error -trimpath `
  -ldflags '-s -w -H=windowsgui -X=cyberagent-workbench/internal/buildinfo.Version=v0.1.0-issue340-local -X=cyberagent-workbench/internal/buildinfo.Modified=true' `
  -o output/playwright/issue340-native/package/TraverseBoard-issue340.exe ./cmd/cyberagent-desktop
```

受管重启及专用草稿恢复使用的 EXE SHA-256：`c2a792a2e7ac39babba05b8a8a0eafcae73c2a101825c8efea9cc4bed4953fa7`。其后的最终构建只增加空 mailbox 引用列表的 HTTP 投影修复，SHA-256：`4cd5d3696ba8c9efda5de6db1d7975718fc7b4346b729913abe82e5f67019471`。

测试 Run `run-20261016044654-3f7d56938a34` 由既有 application/store 服务在应用关闭时创建，使用独立 Git 项目、已准入的合成提案和 Git-only 准备计划。它保持暂停，模型调用、Tokens、验证命令均为 0。此数据用于验证真实桌面读写与恢复。

## 实际桌面操作

使用 Computer Use 驱动真实 EXE，没有写入 WebView localStorage、替换 DOM 或伪造原生桥返回。

| 操作 | 观察结果 |
| --- | --- |
| 初始无参数启动，打开设置 → 执行环境 | 浏览器验证和批量验证均未启用；可分别选择 |
| 勾选两项，保存时按 ESC | 原生框关闭，显示已取消保存；偏好文件不存在，原进程继续运行 |
| 再次保存，在原生框选择“是” | 文件 revision 为 2，两项为 true；界面显示需重启，原进程继续运行 |
| 点击重启，在原生框取消 | 原进程保留，已保存的 revision 2 设置保留 |
| 再次普通启动同一数据目录 | 两项均显示当前进程已启用；此前新对话输入草稿也恢复 |
| 在合成 Run 的 UI 取证表单填写名称与页面地址 | 保存名称 `Issue 340 专用浏览器草稿 — 重启后保留` 和 `http://127.0.0.1:43991/issue340-draft`；未提交启动命令 |
| 在批量交付表单填写多行负责路径 | 保存 `owned.txt` 与 `issue340-重启草稿.txt`；未提交准备或执行 |
| 在设置中只关闭批量验证并原生确认保存 | revision 为 3，浏览器为 true、批量为 false；当前进程仍显示原启用状态 |
| 原生确认受管重启 | 父进程 23816 退出，子进程 36412 显示新的可见 Wails 窗口，回到同一 Thread |
| 回到 UI 取证表单 | 名称与地址完全恢复；执行确认框没有恢复为已勾选 |
| 回到批量交付表单 | 提案选择和两行负责路径恢复；页面显示当前仅可检查 Git 差异，Go/npm 验证需启用，符合独立关闭偏好 |
| 使用最终 EXE 再次无参数启动并查看同一计划 | 计划、子任务与 DISPATCH 消息完整显示，原 mailbox 解析警报消失；多行草稿仍保留，执行确认框未勾选 |

本地原始记录位于忽略目录 `output/playwright/issue340-native/`：`desktop-final-process.json`、`managed-restart-visible-process.json`、`ui-evidence-draft-before-restart.txt` / `ui-evidence-draft-after-restart.txt`、`batch-draft-before-restart.txt` / `batch-draft-after-restart.txt`、`synthetic-seed-result.json`。

最终计划详情读回见同目录 `desktop-mailbox-fixed-process.json` 与 `batch-mailbox-final.txt`。

## 本轮发现与修复

- Wails 2.14 Windows 警告框忽略自定义按钮，导致原生保存无法确认。两处确认改为带默认取消的“是／否／取消”；真实界面验证了保存与取消路径，回归覆盖否、ESC/关闭映射、API 错误和上下文取消。
- Windows 重启 helper 的 `HideWindow` 覆盖新应用首次显示，形成“父进程退出、子进程存在但无窗口”。改用 `CREATE_NO_WINDOW` 后，真实 Wails 重启显示窗口。独立 Win32 子进程回归复现旧代码失败，并验证修复后首窗可见、没有控制台。
- 原生窗口查询改为复用唯一 callback，避免反复确认耗尽 Go 永久回调槽；回归连续执行 2,048 次真实窗口查询。
- 超限草稿现在进入共享未保存状态，切换面板后仍阻止重启；修正保存或明确丢弃才解除。旧 Batch 请求的 409 调整流程先记录已结算，再清理原请求，避免重新导入旧请求。
- 正常 Batch dispatch 消息没有 evidence 引用时，HTTP 曾返回 `null`，触发前端严格数组校验。投影改为返回 `[]`，保持已有前端合同。

## 自动回归

主线集成后运行的相关前端测试共 9 文件、349 项通过，覆盖原生桥、设置、readiness、两项验证表单、恢复存储及真实 Provider 与设置面板的重启阻断组合。类型检查和生产前端构建通过。

Go 定向检查通过：

```text
go test ./internal/application -run 'TestDesktopVerificationPreferences' -count=1
go test ./internal/desktop -run 'TestVerificationSettings' -count=1
go test ./internal/httpapi -run 'TestOpenAPIGoldenDocumentMatchesGoDTOs|TestRunCapabilityReadinessHTTPCurrentFixtureMatchesGoProjection|TestRunCapabilityReadinessVerification|TestControllerContracts' -count=1
go test -tags desktop,wv2runtime.error ./cmd/cyberagent-desktop -run 'TestWindowsRiskRestart|TestRiskRestart|TestNativeRiskRestarter|TestDesktopRiskRestartNativeDialog|TestWindowsRiskConfirmation' -count=1 -v
go test ./internal/httpapi -run '^(TestBatchDelivery|TestBatchWorkbench)' -count=1
go run ./cmd/protocolregistry -check -baseline 5b9d2e9bf06fe327bcfbd42fe987d978b1e43172
go run ./cmd/surfacecheck
```

HTTP mailbox 回归通过 stub controller 提供空引用 dispatch 和非空引用消息，再走真实 HTTP handler 序列化，断言空数组及非空引用均保持。旧投影已用同一测试复现失败。

原请求 unknown、原 operation key 重试、旧 sessionStorage 迁移、跨 Run/数据存储隔离与存储失败由组件测试覆盖。尝试为真 EXE 注入网络中断时，带 WebView2 远程调试端口的启动被工具自动审批拒绝，仅返回 `blocked by policy`，没有具体原因；没有执行该注入，也没有将组件结果记为真 EXE 网络故障验收。

## 尚待发布验收

上述 EXE 是本地源码构建。#340 保持开放，待包含此变更的实际发布资产完成无参数启动、能力组合、失败恢复、原请求保留和新进程重新授权的复验。macOS 本轮没有桌面实测。既有 Docker/SBX 执行验收见 [项目交付记录](2026-10-16-project-preparation-delivery.md)。
