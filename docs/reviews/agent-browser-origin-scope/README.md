# Agent 浏览器站点级授权（ADR 0195 Phase 1 核心）：OCR delegate 复审

日期：2026-11-05。基线：`9a0b73b0`（origin/main，#535/#536 合入后）。
主题：#491 P1 站点级授权的第一片——ADR 0195 的 Phase 1 核心 + Phase 2 的 authority
校验规则。`AgentBrowserOriginScope`：操作者声明的 1..8 个精确 scheme+host+port
origin，default-deny，指纹绑定，复用 safe-web 的目标语法（公开+loopback 可达、
private 拒绝、metadata/link-local 永拒）；scoped 会话经 `StartRequest.ScopedOrigins`
启动，runtime 在每次分发导航前与落地 URL 上强制 scope（redirect escape 以命名错误
失败）；`AgentBrowserCallAuthority` 增加指纹绑定的 `DeclaredOrigins`（unscoped 指纹
字节不变），Validate 规则 unscoped⇔Full、scoped⇔ask/auto。

本 PR 显式不做（后续 PR，按 ADR 分阶段评审）：subresource 的 Fetch 域拦截——ADR
要求 Phase 1 点名的实现选择，本 PR 选定为**扩展 agent-browser transport 的 Fetch
域**（沿用其事件白名单），而非迁移 restricted transport，避免混合两个产品的执法面；
per-origin 审批追加（Phase 3）；操作者声明表面（Phase 4）。因此当前没有生产路径
创建 scoped authority——本 PR 由测试驱动的管道与执法机制，操作者表面合入前对
现有行为零变化。

OCR 负责文件选择与规则解析（Go 系统包），主 agent 与独立评审子代理实际阅读判断。

## 覆盖范围

纳入审查 14 个生产/测试文件（评审记录不计入）：
- 新增：`internal/browserruntime/agent_browser_scope.go`、`agent_browser_scope_test.go`
- browserruntime：`agent_browser_types.go`、`agent_browser_start.go`、
  `agent_browser_platform_darwin.go`、`agent_browser_runtime.go`、
  `agent_browser_actions.go`、`agent_browser_runtime_test.go`
- toolgateway：`agent_browser_actions.go`、`agent_browser_actions_test.go`
- application：`agent_browser_actions.go`、`agent_browser_service.go`、
  `agent_browser_worker.go`、`agent_browser_download_test.go`

具体明细见 [coverage.json](coverage.json)。

## 发现与修复（评审子代理报告 + 主 agent 复核，全部已处理）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| F1，P1 | ADR Phase 1 要求 resolved-address 执法或 literal-only 二选一，两者皆未落地：公网域名声明后解析到 private/metadata 地址畅行无阻（DNS rebinding），`RevalidateResolvedIPs` 为装饰字段，注释过度声明。 | **实现 dispatch-time 解析复查**（选 ADR 选项 a 与 b 的中间态）：声明 origin 若为公网名（ResolutionCheckRequired），分发前经共享 `classifyAddress` 分类全部解析地址，任一落入 blocked/private 类即以命名错误拒绝（`resolved_address_out_of_scope`/`resolution_failed`），DNS-rebinding 的显式路线关闭；分发解析与浏览器自连之间的 TOCTOU 窗口与既有残留同权注释，per-connection 复查与 Fetch 域扩展同一后续 PR。 |
| F2，P2 | 落地逃逸检出后，逃逸页面保持 live 且 Click/Type 等输入不查 scope（执法仅覆盖导航/重定向）。 | ADR Phase 1 字面范围即导航与重定向；在执法 helper 注释中如实记录该边界与 Phase 2 决策点（错误文案已指引模型先导航回声明集）。 |
| F3，P2 | scoped E2E 测试名不匹配 CI darwin 证据 job 的 `TestAgentBrowserReal.*` 正则，真浏览器执法验证不会在 CI 跑。 | 改名 `TestAgentBrowserRealScopedSessionEnforcesDeclaredOrigins` 纳入矩阵；redirect 目标改为第二本地 server（不同端口=不同 origin），证据 job 不触外网。 |
| F4，P2 | `CheckAgentBrowserService` 与 `currentAuthority` 的 full-only 硬编码是 scoped 会话（含 #491 的 dev-server 预览流）的 Phase 2 接线点。 | 如实列入 Phase 2 接线清单（本 PR 不动——它们同时是"无生产 scoped 路径"的保证）。 |
| F5，P3 | 落地检查把任何 scope 错误一律重标为 `navigation_left_declared_origins`（fragment/超长 URL 会被误报）。 | `errors.As` 后按码透传，仅 `origin_not_declared` 重标为落地逃逸。 |
| F6/F7，P3 | authority 携带原始 origin 串（指纹绑 canonical 集）；`Equal` 对 nil vs 空 slice 敏感。 | Phase 2 构造须 canonical 化写入 Equal 注释；`Equal` 归一化 nil/空。 |
| F8，P3 | slot 键不含 scope——撞 key 时首建 slot 胜出、另一变体被 Equal 拒（fail-closed），Phase 3 原子换权设计上复用同键。 | 在 `agentBrowserSlotKey` 注释中固化该设计意图。 |
| F9，P3 | scope 测试中的 `refingerprinted` 块为死代码。 | 替换为真实断言：扩集重算指纹 ≠ 原指纹。 |
| F10/F11，P3 | >4096 字节 URL 的错误语义噪音；ScopeFingerprint 不绑定语法版本。 | 注释分别说明（模型路径上游已限 4096；语法/上限放宽必须升 `agent_browser_origin_scope.v1`）。 |

评审确认（无阻塞）：unscoped 指纹逐字节不变（omitempty+尾插+测试断言）；pre-check 位于
`begin()/invalidateLocked` 之前（拒绝零状态变更、不 outcome_unknown、会话可继续，E2E
锚定）；落地检查的 `doc.url` 来自 `Page.getFrameTree` 活动帧——302 后取到最终 URL，
重定向确实被捕捉；Validate 无漏网组合（半空/Full+scoped/debug+scoped 全部 fail-closed，
unscoped 错误文案与 main 一致）；接管面（screencast 只读推帧）不经模型执法点，未来操作者
手动导航天然不受限，与 ADR"takeover 交互不变"自洽；语法 profile 与 builtin safe 一致
（public+loopback、拒 private，ceilings 8/4096 精确镜像）。

## 验证

- `go build ./internal/...`、`go vet` 三包无告警。
- `go test ./internal/browserruntime/ ./internal/toolgateway/ ./internal/domain/
  -count=1` 全过；`go test ./internal/application/ -run TestAgentBrowser` 与
  `./internal/app/` 布线/结构化工具测试全过。
- opt-in 真浏览器 scoped 执法 E2E（`TestAgentBrowserRealScopedSession...`，CI
  darwin Chrome 证据 job 执行）：in-scope 导航、越界命名拒绝、redirect escape
  命名拒绝、拒绝后会话继续可用。
- `go test ./internal/application/ -run TestAgentBrowser` 与相邻定向套件全过。application
  全包在本机两次超过 10m/25m 测试超时，挂起签名互不相同（一次 git 进程 spawn、一次
  MCP jsonrpc2 读阻塞），委派复审将前者隔离重跑 6.3s 通过、栈内无任何浏览器符号——判定
  为本机环境 flake 而非本 diff 回归；全量矩阵以 CI 为准。
