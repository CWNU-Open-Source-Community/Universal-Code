# #348：扩展与手动 Skill 选择验收

日期：2026-10-15。分支：`Qiyuanqiii/issue-334-extension-surfaces`，
补齐前提交：`2b160ae53116323fca3b03d578f83e8294237c77`。

## 范围与环境

本次覆盖版本化 Skill 新安装的选择链路、原生 `SKILL.md` 手动选择、历史 Registry
移除，以及签名 Plugin 的发布者信任界面。原生 Skill 自动发现和按需读取继续保留。

Windows、Go 1.26.9、生产 `web/dist`、Microsoft Edge / Playwright CLI。
服务使用独立 HOME、SQLite 和临时 Git 工作区。离线 Anthropic 协议测试服务只在
检查实际请求内的选中正文、实际 `skill_read` 返回和工作区文件读取结果后才返回完成。
本记录验证本地产品链路；未调用外部付费模型。

原生目录使用生产 `plugin import-dir` 导入并审查启用；版本化包和签名 Plugin
均由浏览器实际上传。历史包通过原 Registry 写入接口构造非空的兼容数据，未将它
冒充为新安装流程。签名包由 `plugins.SignPackage` 生成并由正式解析器确认签名有效。

## 浏览器结果

| 流程 | 结果 |
| --- | --- |
| 新版本化包 | 上传 `pr348-new-legacy-skill@1.0.0`，审查并启用 skills；任务配置显示候选，确认后选择成功。 |
| 原生组件 | `pr348-native-skill` 显示真实名称、无虚构版本；手动勾选、确认、发送后精确绑定安装/组件/revision/代次。 |
| 实际使用 | 两次任务均收到原始正文和一致 SHA-256；执行 `skill_read` 后返回同一正文，再读取 `notes.txt` 得到 `JOURNEY_CHILD_PROOF_2026`。 |
| 历史 Registry | 列表显示已登记记录；未确认时按钮禁用；确认移除后默认隐藏，勾选“显示已移除记录”可见 tombstone 与对象保留说明。 |
| 发布者信任 | 签名有效的 1.0.0 包完成首次信任、撤销、重新信任；旧安装保持撤销。同发布者 1.1.0 新包可重新审查并启用 skills。 |
| 请求和界面 | 候选 GET、两次选择 POST 均 200；安装、移除、信任、撤销和审查控制返回 202；控制台 0 error / 0 warning。截图已人工检查。 |

任务记录：`run-20261015142526-e637a5ed0c9d`（native）、
`run-20261015142712-e3521302e153`（versioned Plugin）。测试服务记录的
`selected_guidance_received`、`skill_read_body_received` 与
`skill_workflow_completed` 均来自实际请求与工具响应内容比对。

本地原始记录保存于 `output/playwright/pr348/`，包含页面快照、截图、请求摘要、
正文比对结果与 OCR delegate 清单。临时访问令牌不写入本记录或 PR。

## 自动验证

- Native 集成测试：精确正文进入 root 上下文、实际 `skill_read`、空作者版本、
  同 Plugin 双组件、数据库重开、停用和重新启用导致的代次失效。
- v196 → v197：旧 Registry 与 Plugin 选择全行、rowid、指纹、收据和历史迁移账本保持；
  SQL 拒绝重复组件以及名称、版本、组件、代次和内容绑定篡改。
- 候选与 API：新上传包/原生混选、重放、错误 pin、模式/目录资格、对象损坏、容量、
  read/control bearer 分界、严格 query/body、完整 OpenAPI 路由可达性。
- 前端：草稿恢复、确认、切换工作区/Profile/phase/client 后隐藏旧候选与忽略迟到响应；
  信任请求响应丢失后只在成功重读并重新确认后恢复提交。
- `internal/skills`、`internal/plugins` 整包及相关 application/httpapi/store 测试通过；
  `typecheck`、生产构建、API 生成一致性、protocolregistry、surfacecheck、相关包 vet、
  releasegate 与 producte2e 通过。

补齐前提交的 Windows 失败经本机重复执行与原 CI 失败任务重跑恢复，全套 CI 已绿。
本次未修改 Windows 运行时、超时或权限检查；后续提交的 CI 单独核验。

本次补齐的首个提交 `cdf0e094` 在完整 CI 中暴露初始化产物遗漏：schema 已到 v197，
`clean_install_baseline_generated.go` 仍为 v196。空库因此回退执行全部历史迁移，
使新库基线断言失败、并发打开出现锁超时，并拖慢 Windows 测试至其原有整包时限。
后续修复仅使用现有 `go generate ./internal/store` 同步初始化 SQL 与元数据；
历史迁移和超时设置保持原值。初始化、历史结构/迁移账本等价、回滚恢复、并发打开和
v197 绑定验证全部通过；空库初始化约 0.20 秒，并发打开约 0.52 秒。完整 CI 以 PR
最新提交为准。

## #334 后续范围

URL/Git GUI 导入、旧 Skill Catalog 发布者信任 GUI、原生目录 Web 导入仍未交付。
本次原生目录由 CLI 导入后可在任务界面手动选择。#334 保留为后续跟踪，#348 不自动关闭它。
