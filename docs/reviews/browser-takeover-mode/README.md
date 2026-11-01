# Agent 浏览器接管模式与会话级登录：OCR delegate 复审

日期：2026-10-30。基线：`7edf02b3ec217060b4e2a01ba640c978d84ef22a`（Qiyuanqiii/browser-download-release）。
主题：#491 P2 的 headed 交接与会话级登录 v1——agent 浏览器可由操作者选择以**可见窗口**
启动（接管模式）：人工在同一一次性会话 profile 内登录/完成 2FA/OAuth/处理
`waiting_user`，登录态存续于会话、随 cleanup 擦除。只改变进程可见性：origin scope、
请求级拦截、下载隔离与审批规则全部不变，agent 不获得额外权限。

OCR 负责文件选择与规则解析（Rule Group 1 system Go），主 agent 与独立评审子代理
实际阅读判断。评审子代理全文核验 4 个生产文件、2 个测试文件与全量 diff，并核对了
Headless 的全部汇点、指纹链与 spec 参数校验。

第二轮（本轮）由本 agent 用 OCR delegate 模式复核本 PR 相对 #523 分支尖端的独有 diff，
发现并整理 F4，并更正 F2 的时效性措辞。

## 覆盖范围

纳入审查 8 个（PR 相对 base 分支的独有 diff 共 12 个文件；两份评审记录本身与
`docs/reviews/browser-session-lifetime/*` 那组记录不计入）：
- 生产代码（5 个，OCR 检出）：`internal/application/agent_browser_service.go`
  （BrowserHeadedFromEnvironment）、`internal/app/api_command.go`（--browser-headed +
  banner 回显 + Headless 选择）、`internal/app/run_runtime.go`（前台 CLI 组合根接线）、
  `internal/desktop/control_plane.go`（桌面组合根接线）、
  `web/src/v2/components/agent-browser.tsx`（waiting_user 文案）。
- 测试（2 个，人工补入，OCR 按 `default_path` 排除后人工全文核验）：
  `internal/application/agent_browser_service_test.go`（两条 env 助手用例，本轮自
  `agent_browser_download_test.go` 移来，见 F4）、
  `web/src/v2/components/agent-browser.test.tsx`（文案断言同步）。
- 文档（1 个）：`docs/usage.md` 新增双语 "Browser takeover and session login" 小节。

具体明细见 [coverage.json](coverage.json)。

## 发现与修复（评审子代理报告，全部已修复并复测）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| F1，P3 | 第三个构造点 `newCLIExecutionRuntime`（前台 CLI run/approval-continuation/git-advanced/wake）仍硬编码 `Headless: true` 且不读 env——前台 CLI 设 env 静默不生效，文档未限定适用组合根。 | `run_runtime.go` 同样接入 `!application.BrowserHeadedFromEnvironment()`，接管模式在全部三个组合根语义一致。 |
| F2，P3 | 文档称登录态"整个会话存续有效"，但组合根未设 `SessionLifetime`：浏览器会话默认 30 分钟 RuntimeDeadline 到期即擦 profile、下次导航开新 profile，登录态随之丢失——未文档化的硬边界。 | 文档明确"浏览器会话有 RuntimeDeadline（默认 30 分钟），到期 profile 被擦除、下次导航开新 profile，人工登录请在会话存续期内完成"。提高接管模式时长留作后续（需组合根显式选择）。更正：同分支随后的 `584a41d9`（feat(browser): operator-selectable session lifetime for long takeover logins）已经把这件事做掉了——`CYBERAGENT_BROWSER_SESSION_LIFETIME_MINUTES`（5..1440）在三个组合根都接入，`docs/usage.md` 与 `docs/reviews/browser-session-lifetime/` 记录了它的取值边界与回落语义，本行的"留作后续"只对当时的提交成立，不再是当前状态。 |
| F3，P3 | `--browser-headed` 在无 control token 时被静默接受但无效（服务仅在 controlToken != "" 时构造）；启动 banner 不回显接管模式，操作员无法确认生效。 | banner 增加 `agent_browser_headed: <bool>` 行（与其它能力开关同格式，含旗标与 env 的合成值）。 |
| F4，P3（本轮 delegate 复审） | 两个新导出的 env 助手落在 `agent_browser_service.go` 末尾，但 `"os"`/`"strings"` 被追加到第三个 import 组（项目包那一组），stdlib 组里其余成员都在同一组内按字母序；同时两条 env 测试被放进 `agent_browser_download_test.go`——那是下载隔离的用例文件，与本 PR 改的服务级组合根无关，`gofmt` 和 `go vet` 都不会报这两件事。 | `os`/`strings` 回到 stdlib 组并保持组内字典序；两条测试移到 `agent_browser_service_test.go`（同包，无需新增 import）。纯整理，行为与断言强度不变；移动后 `go test -count=1 -run "TestBrowser|AgentBrowserDownload" ./internal/application/ ./internal/browserruntime/` 仍全通过。 |


评审确认（No blocking findings）：env 解析 fail-closed（仅 1/true，大小写/空白容忍，
其余值 headless）且有测试；Headless 只有两个汇点（状态展示 + 启动参数），
`validateAgentBrowserProcessSpec` 本就接受 headed 变体；ProfileToken/Generation 指纹
与可见性无关，切换模式无跨代漂移；origin scope/拦截/隔离/审批代码路径不读 Headless；
waiting_user 新文案无旧文案残留，headed 下 UI 正确隐藏实时画面。

## 验证

- `gofmt -l` 无输出；`go vet ./internal/app/ ./internal/desktop/ ./internal/application/` 无告警；
  `go build ./...` 通过。
- `go test ./internal/application/ -run TestBrowserHeadedFromEnvironment` 通过（含拒绝值矩阵）。
- 评审子代理独立实跑 `./internal/desktop` 全量 ok；web 侧静态核对（文案/断言一致、
  locale 参数配对、headed 下实时画面正确隐藏）。
- 本轮（F4）实测：`gofmt -l internal/application/agent_browser_service.go internal/application/agent_browser_service_test.go internal/application/agent_browser_download_test.go` 无输出；`go vet ./internal/application/ ./internal/browserruntime/ ./internal/httpapi/` 无告警；`go test -count=1 -run "TestBrowser|AgentBrowserDownload" ./internal/application/ ./internal/browserruntime/` 通过（application 0.168s、browserruntime 0.941s）。本轮把 #520/#523 的新提交并入本分支（merge commit），所以隔离与释放两轮的回归也随本分支一起跑过。
- web 侧本轮未改：`web/src/v2/components/agent-browser.test.tsx` 的文案断言仍由 CI 执行（本工作树没有 `web/node_modules`，未做本地 vitest 实跑，不做超出实际的声明）。
- 已知边界（如实记录）：env 在进程启动时求值一次，运行中更改需重启；web vitest 因
  node_modules 未装未本地实跑，由 CI 执行。
