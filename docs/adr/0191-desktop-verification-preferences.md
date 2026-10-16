# ADR 0191: 桌面验证偏好与重启恢复

Date: 2026-10-16
Status: Proposed for [Issue #340](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/340).

## 行为

桌面设置提供独立的“浏览器验证”和“批量验证”开关。用户点击保存后，Go 原生对话框列出本次两项选择；确认才按当前 revision 写入应用目录中的 `desktop-verification.json`。保存和重启是两个操作。界面同时显示已保存偏好与当前进程的启用状态，关闭某项也在下一次启动生效。

普通无参数启动和所有受管重启读取同一份偏好。明确传入细粒度 CLI 参数的启动继续按参数组装，不继承桌面偏好。验证设置重启只选择 Go 内部的 `verification_settings` 或 `verification_settings_debug`，保留当前终端启用状态；渲染器不能传入程序路径、额外参数或任意 profile。

Windows 的 Wails 2.14 `WarningDialog` 忽略自定义按钮，只返回系统确定按钮，无法完成保存或重启确认。因此这两个操作使用绑定当前应用窗口的 Win32“是／否／取消”对话框，默认取消；只有 `IDYES` 且没有 API 错误或上下文取消才通过。macOS 保持原生自定义按钮。按钮文本及返回值映射需要桌面实测与平台测试共同覆盖。

Windows 重启子进程最终成为新的应用窗口。启动它时使用 `CREATE_NO_WINDOW` 抑制控制台，并保留独立进程组和 ready 管道；不设置会覆盖 Wails 首次显示的 `STARTF_USESHOWWINDOW/SW_HIDE`。否则父进程已退出，新应用却会保持隐藏。

## 持久化与权限

`desktop_verification_settings.v1` 属于 `internal-durable`，文件只包含版本、revision 和两个布尔偏好。它复用受限应用目录、跨进程发布锁和原子替换。旧 revision 只在与已提交的下一 revision 及设置完全一致时作为丢响应重试返回；其它冲突要求重新读取。

偏好控制服务装配。Run 的 Ask／Auto／Full、审批、当前执行状态、环境就绪与进程内激活仍由现有 Go 执行路径检查。每次进程启动重新创建执行授权管理器，偏好文件没有 Run、令牌、租约或授权记录。

HTTP `capability-readiness` 增加只读的 `ui_evidence` 与 `batch_validation`。`installed` 表示当前服务已装配；`blocked_by` 和 `remediation` 区分需启用、已保存待重启及需连接桌面设置。HTTP 没有保存验证偏好的入口。

## 失败和恢复

- 原生取消、保存失败不退出当前应用。保存结果不明时，界面要求先重新读取，避免把未知结果显示为成功。
- 重启失败保留已保存的偏好，并允许再次重启；固定重启握手成功前不退出旧进程。
- 浏览器验证与批量验证草稿、原请求保存在既有 `v2_recovery.v1` 存储中，按页面来源、API 地址、数据存储身份与 Run 隔离。待确认请求保留原操作键，恢复后只提供手动核对，不自动重发。
- 旧 `universal-code.batch-workbench.v1` sessionStorage reader 保留。当前服务回读确认 Run／计划绑定后才复制原请求；旧值不删除，新的作用域内结算标记防止已处理的旧请求再次导入。
- 恢复记录格式异常、存储失败或草稿超限均显示原因；未保存的草稿阻止重启。修正后成功保存或明确丢弃可以解除暂存失败状态。

## 兼容与回退

保留 v1 偏好 reader、原始 revision 和旧 Batch reader。后续版本变更需要迁移或保留记录；不能以新的默认值覆盖无法理解的文件。回退到本变更前的应用会忽略新增偏好文件；用户数据和旧验证记录保留，重新升级时继续读取。

正式发布包的验收需引用实际发布资产并验证无参数启动、原生保存／取消／重启和同一数据目录恢复。本地构建的测试包及单元测试分别记录，不能替代该发布验收。
