# Agent 浏览器会话时长运维项：OCR delegate 复审

日期：2026-10-30。基线：`aa83acb0`（Qiyuanqiii/browser-takeover-mode，即 #524）。
主题：接管模式评审 F2 的收口——浏览器会话 RuntimeDeadline（默认 30 分钟）到期即擦
profile（含人工登录态），对"人工登录"场景是硬边界。新增运维项
`CYBERAGENT_BROWSER_SESSION_LIFETIME_MINUTES`（5..1440 分钟，畸形/越界回落默认），
在同一组三个组合根生效；隔离区、审批与清理回执语义不变。

OCR 负责文件选择与规则解析（Rule Group 1 system Go），主 agent 与独立评审子代理
实际阅读判断。评审子代理全文核验 5 个生产/文档文件与 1 个测试文件，并对 24h 双守卫
衔接（helper 上限 1440min 恰好不触发构造器 `>24h` 回落）做了确定性推理。

## 覆盖范围

纳入审查 6 个（评审记录不计入）：
- 生产代码：`internal/application/agent_browser_service.go`（BrowserSessionLifetimeFromEnvironment）、
  `internal/app/api_command.go`（接线 + banner 回显）、`internal/app/run_runtime.go`、
  `internal/desktop/control_plane.go`（组合根接线）、`docs/usage.md`（运维项文档）。
- 测试（1 个）：`internal/application/agent_browser_download_test.go`
  （TestBrowserSessionLifetimeFromEnvironment）。

具体明细见 [coverage.json](coverage.json)。

## 发现与修复（评审子代理报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| F1，P3 | 启动 banner 回显 `agent_browser_headed` 但不回显 session lifetime：env 拼写错误（如 `90m`）会静默回落 30 分钟，接管登录中途 profile 被擦且无可观测信号。 | banner 增加 `agent_browser_session_lifetime_minutes: <N|default>` 行。 |
| F2，P3 | 测试断言措辞称 "unset"，但 `t.Setenv(k,"")` 是设为空值；真 unset 路径未覆盖。 | 补 `os.Unsetenv` 真 unset 用例（行为与空值等价，均回落默认）。 |
| F3，P3 | 拒绝矩阵未测**接受的边界** "5" 与 "1440"；1440→24h 恰好不触发构造器 `>24h` 回落是关键契约，无测试锚定。 | 矩阵补 "5"/"1440" 接受用例（1440 若守卫改成 `>=` 会静默退回 30 分钟，现被锚定）。 |

评审确认（No blocking findings）：解析 fail-closed 无绕过（TrimSpace+Atoi，空/非数字/
越界/溢出全走回落）；env 全仓仅此一处读取、组合根构造时求值一次；1440min=24h 与下游
`validateAgentBrowserRequest` 的 deadline 校验确定性衔接；三个组合根接线一致；文档与
实现逐项一致。

## 验证

- `gofmt -l` 无输出；`go vet` 三包无告警；`go build ./internal/...` 通过。
- `go test -race ./internal/application/ -run "TestBrowserSessionLifetime|TestBrowserHeaded"` 通过
  （边界接受矩阵 + 拒绝矩阵 + 真 unset 用例）。
