# #339 诊断、测试与浏览器反馈验收

2026-10-16，在 Windows Host 上用已有 DeepSeek 配置完成两次真实模型任务，
覆盖诊断修复、浏览器发现并修复页面问题、开发服务发现与复验三条链路。
浏览器修复与开发服务复验属于同一次任务。运行结果与最终文件已独立核对，
原比例截图已实际查看；摘要见[结构化记录](../evidence/agent-verification/issue339-model-results.json)。

## 执行方式

验收驱动调用生产 `desktop.OpenControlPlane`、HTTP handler、Supervisor、
工具网关和 SQLite，使用独立应用目录与工作区。配置模型为
`deepseek-v4-flash`，响应报告 `deepseek-flash`；gopls 为真实的 v0.21.1 进程。
操作者预先选择 Local 执行配置与 Full 权限、配置并初始化 gopls、提交任务。
此后工具选择、修改、停止与重启开发服务、页面点击由模型完成。
这次通过生产服务链路验收，没有启动正式桌面安装包。

## 三条链路

| 链路 | 实际结果 |
| --- | --- |
| 诊断与修复 | `diagnostics-06` 中模型读取 `sample.go`，将 `Updated` 的整数改为字符串。第一次 apply 返回 added=1、persisting=1；模型再恢复整数，第二次返回 resolved=1、persisting=1。旧的 `Existing` 类型错误始终保留，最终文件与原文一致，模型正常 finish。 |
| 浏览器发现问题并修复 | `browser-05` 首次页面快照确认按钮 disabled。模型读取 HTML，通过 workspace 工具只删除 `disabled` 属性；修复后真实点击按钮，后续两次快照均显示“计数：1”，按钮可用，模型正常 finish。 |
| 开发服务发现与复验 | 同次浏览器任务由模型启动 Node 服务，从原 Job 的结构化候选取得动态地址，导航前返回 HTTP 200、state=ready、verified=true。服务占用写边界时编辑被拒绝；模型取消并等待原 Job 终止，完成编辑后启动新 Job，再通过新 Job 的就绪检查、导航和点击复验。 |

诊断任务共 12 次 Provider 请求，浏览器任务共 30 次，均包含 2 次资格检查。
浏览器任务中有 1 次携带图片的模型请求；最终计数为 1 的截图由浏览器工具保存，
主审阅者另行查看。任务结束后关闭验收控制面，两个服务 Job 分别为
`cancelled` 和 `interrupted`，均记录 `tree_reaped=true`。

![点击后计数为 1，原始 1258 × 802 截图](../evidence/agent-verification/counter-after-click.png)

调试过程保留在本地 `output/issue339-acceptance/`。早期运行暴露并修正了
LSP 工具 schema 缺少顶层对象类型、read/wait 参数说明不一致、运行中命令
阻止编辑时缺少可执行反馈，以及编辑意图保存过早的问题。另有配置准备失败、
模型结束格式失败和上下文超限的尝试；它们没有记为通过。

## 有界测试与界面

`run_tests` 在 Code Plan、Deliver 复用现有项目测试动作、选定后端、
Full/Ask/Auto、Job、预算和 lease。项目测试可能生成缓存或文件，已按用户选择
沿用现有执行权限。Review 是工作流状态，实际执行阶段仍为 Plan 或 Deliver。
接口与权限说明见[有界项目测试](../architecture/bounded-project-tests.md)。

针对性集成测试通过 Plan/Deliver × Full/Ask/Auto 的真实 Host 执行、批准与拒绝、
清单变化、过期 lease、预算和重放；Plan Docker 测试使用受控传输，确认走所选
后端且不回退 Host。本次没有把该受控传输记录为真实 Docker 验收。

浏览器状态组件已在实际页面按 1280px 和 360px 查看，展示平台、权限、运行时
或会话不可用的具体原因，并说明后台浏览器的显示方式。macOS/Linux 采用明确
降级提示；真实模型浏览器链路本轮只在 Windows Host 验收。

## 审阅范围

OCR delegate 提供文件选择和规则，主 agent 与子 agent 分批阅读诊断、测试、
浏览器及集成改动。生产文件按差异和相关调用链审阅，新增测试补读；生成 SQL、
OpenAPI 和协议注册表由生成器及对应检查验证，不记为全文源码审阅。
各批具体覆盖与发现保留在本地 `output/reviews/`，不把本轮结果扩展为全仓审阅。
