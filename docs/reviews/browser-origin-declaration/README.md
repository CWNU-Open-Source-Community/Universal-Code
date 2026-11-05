# Scoped agent browser 操作者声明与接线（ADR 0195 Phase 4 第一片）：OCR delegate 复审

日期：2026-11-06。基线：`dc740504`（PR #538 分支，Phase 1 + 协议注册）。
主题：#491 P1 的 Phase 4 第一片（CLI-only）——`RunConfig.BrowserOrigins` 创建时
钉死的操作者声明（trusted operator input，与不可信 `.prayu` 快照严格分离）；
`currentAuthority` 的 scoped ask/auto 分支（拒绝面与 Phase 1 逐字节一致）；
`CheckAgentBrowserService` scoped 分支（service URL 必须命中声明 origin，
`MatchesOrigin` 纯成员判断不解析）；`Capabilities.DeclaredOrigins` 暴露；
CLI `--browser-origin` 重复旗标（reorderFlags 登记）与 fail-closed 错误语义；
声明继承矩阵（thread 后继继承、fork/resume/branch 不继承、项目移动跟随）。

显式不做：Desktop 声明面与 live view 投影（后续片）、worker 链（保持 Full-only，
既有设计）、origin-add（Phase 3，设计见 #540）。

## 覆盖范围

纳入审查 11 个文件（评审记录不计入）：
- `internal/domain/run.go`、`internal/application/run_service.go`、
  `agent_browser_service.go`、`agent_browser_readiness.go`、
  `thread_project_move.go`、`internal/browserruntime/agent_browser_scope.go`
- `internal/app/run_command.go`（旗标 + reorderFlags 登记）
- 测试：`agent_browser_service_test.go`（scoped ask 端到端/门控/Full 忽略/
  规范化）、`thread_service_test.go`（后继继承）、`run_command_test.go`（CLI）
- `docs/usage.md`

具体明细见 [coverage.json](coverage.json)。

## 发现与修复（评审子代理报告，全部已处理）

| 发现 | 具体问题 | 最终处理 |
| --- | --- | --- |
| F1，P1 | `thread_project_move.go` 的 origins 跟随未暂存、未测试、与 web `AllowedTargets` 不复制形成不对称。 | 纳入本切片：保留继承（项目移动是操作者发起，声明应跟随重建的 Run），normalize 幂等保证 canonical 串直通；继承矩阵写入 usage.md 与 domain 注释。 |
| F2，P2 | `prepareSuccessor` 整体拷贝 Config 使后继静默继承声明——必要行为但零测试零文档，与 fork/resume/branch 的不继承并存成未成文不对称。 | 补回归测试 `TestThreadSuccessorInheritsBrowserOrigins`（后继继承 canonical 串）；继承矩阵写入 usage.md 与 RunConfig 注释。 |
| F3，P3 | `Capabilities.DeclaredOrigins` 注释过度声明（httpapi live view 未投影）。 | 注释改为"为后续 live view 展示预留，httpapi 投影随 Desktop 表面落地"。 |
| F4，P3 | 非 Full 不可用码恒为 `full_access_required`（Phase 1 既有行为）。 | 如实记录，留 Phase 4 后续（RefusalReason 文案已正确）。 |
| F5，P3 | 调试探针文件遗留。 | 已删除。 |
| F6，P3 | `TestNormalizeBrowserOrigins` nil 分支死断言。 | 改为真实断言（nil 输入返回 nil 集且无错误）。 |
| F7，P3 | Full 忽略声明测试未断言能力层；三类低成本回归缺口。 | 能力层断言留待后续（应用类型经 service.Capabilities 已在端到端测试覆盖）；mode 切换场景记录为已知测试缺口。 |

评审确认（无阻塞）：currentAuthority 拆分后所有非 ask/auto/full 模式的拒绝面与
Phase 1 逐字节一致；scoped 权威在 cold slot/advertisement/dispatch/rotation 全链
Equal 稳定（PermissionActivation=0 链自洽）；声明三层独立重建并比对指纹
（currentAuthority/toolgateway/runtime）任何非 canonical 输入 fail-closed；
readiness 预检后 navigate 双重执法必然兜底；1..8 上限在 raw 输入长度上强制
（比去重后计数更保守）；fork/resume/branch 不继承与既有 NotInherited 语义对称。

## 验证

- `go build ./...`、`go vet` 四包；`go test ./internal/browserruntime/
  ./internal/toolgateway/ ./internal/domain/ -count=1` 全绿。
- `go test ./internal/application/ -run 'TestAgentBrowserScoped|TestAgentBrowserAskRun|
  TestAgentBrowserFullRun|TestNormalizeBrowserOrigins|TestThreadSuccessorInherits|
  TestThreadProjectMove|TestAgentBrowser' -count=1` 全绿（application 全包在本机有
  环境性超时——干净 main 对照实验已证明与本 diff 无关，见 #538 评审记录）。
- CLI：创建成功/私网 fail-closed/超 8 上限拒绝三断言实测通过。
