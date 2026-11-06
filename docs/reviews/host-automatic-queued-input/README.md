# Automatic owner 与已接受普通输入的交接验证

基底：`dac396f56757399d3c3f2048528536d603d464ff`。本次生产改动仅涉及
`internal/application/thread_turn_control.go`；不改变消息协议、权限、预算、Run 绑定或持久队列顺序。

原问题使用真实 SQLite 与 ThreadTurnService 复现，仅模型 provider 使用协调 channel 的测试实现：
automatic turn 已进入模型调用时，普通输入成功返回 pending；随后取消 automatic caller。
原实现返回 automatic 的取消，但清除了 live owner，普通输入停在 received/pending，模型调用总数为 1。
最小回归在原代码上失败（2.46s）。

修复保留独立的宿主 Stop context。automatic 的准备与模型执行仍使用 caller context；
已接受的普通输入在真实 commit 后使用宿主 context。automatic 到达边界后，交接同一个
active owner、execution ID 和完整 FIFO，先登记宿主 operation，再异步使用原队列收尾逻辑。
automatic 原调用返回自己的结果或取消。Stop 与 host shutdown 可以在交接前后取消同一个 owner。
模型失败仍写入原 handoff/message 失败回执，未消费的后续输入保持 pending。

本地验证均使用真实 SQLite；模型响应、caller 取消和 SQL 返回时点由测试 channel 控制，
没有调用外部模型。已验证两条普通输入各执行一次、原 operation key 重放不增模型调用、
FIFO、原 Run 绑定、首条文本或真实上传附件的关联、普通 caller 断开、commit 后读回、
Stop/shutdown 交接前后、宿主有界 drain，以及普通模型失败后的失败封存和后条保留。

生产 diff 稳定后，新取消专项 race 重复三次通过（59.950s）：

```powershell
go test -race ./internal/application -run '^(TestHostOwnedQueuedOrdinaryTurnSurvivesAutomaticCallerCancel|TestHostOwnedAutomaticQueue.*)$' -count=3 -timeout=90s
```

补充 automatic 成功后的交接与原 Run 断言后，最终 focused race 通过（96.962s），覆盖新专项、
既有 automatic caller 取消、预算/权限门禁、普通 host turn、commit/reject/drain、Stop 和 peer 队列：

```powershell
go test -race ./internal/application -run '^(TestHostOwnedQueuedOrdinaryTurnSurvivesAutomaticCallerCancel|TestHostOwnedAutomatic.*|TestHostLifetimeDoesNotDetachAutomaticThreadTurn|TestHostOwnedThreadTurnSurvivesHTTPDisconnect|TestHostOwnedThreadShutdownDeadlineLeavesDrainObservable|TestIndependentHost.*|TestThreadTurn(InterruptFencesExecutionAndPreservesDurableQueue|ConsumesAcceptedQueueOnceAtNextBoundary|StopDoesNotDependOnCancellingAcceptedInputs)|TestAutomaticThreadTurn.*|TestThreadPeerMessages.*)$' -count=1 -timeout=180s
go vet ./internal/application
git diff --check
```

另用临时 commit gate 核对了 automatic 自身已入库、模型尚未开始时的取消窗口：

- gate 内原 automatic message 为 received/pending，模型调用数为 0。
- ordinary 已被接受后取消 automatic caller，原调用返回 `context canceled`，
  `ExecutionStarted=false`、`ModelCalled=false`；原 automatic handoff 尚未创建。
- ordinary 的新宿主 handoff 以 `requested_by=test_operator` 依次消费两个 pending input。
  只读 SQL 显示同一个新 handoff 的 ordinal 1 是原 automatic message、turn 1；
  ordinal 2 是 ordinary message、turn 2；两个 delivery 均 committed，模型调用数为 2。
- 这是后续显式 ordinary handoff 首次推进已接受的 durable pending FIFO。
  原 automatic 调用已取消，原 automatic handoff 没有重启。未删除、取消或重写已接受消息。
  `checkAutomaticTurn` 保留自己的单 pending input 恢复约定；failed seal 仍只封存已有失败
  handoff 对应的 prepared input。

OCR Delegate 选择了 1 个生产文件并解析 Go 规则。生产文件阅读覆盖变更区域、完整
execute/drain/Stop 路径及 host operation/drain 锁序；automatic、peer delivery、失败封存和
提交 context 选择按相关调用路径部分阅读。新增回归文件完整阅读；没有扩大为全应用审计。

## 已保存审批续行的相同交接窗口

在 `afda73eeb61113c4f482c3cc362906345730cd29` 上另行复现：automatic 已完成精确
`root_wait`，Run 已暂停、执行 lease 已释放，末次 SQLite handoff 读回被 channel 延迟。
公共 FileEditReview `approve_intent` 已保存；公共 ResumeApproval 返回 queued。
取消 automatic caller 后，原调用返回取消、owner 归零，模型仍停在 proposal/wait 两次调用。
再次显式 ResumeApproval 同一个审批立即完成并正常 apply，达到四次模型调用，证明原审批具备
续行资格。原回归失败 0.72s。

补充修复只把 `active.approvals` 纳入已有宿主交接条件，保留同 owner/id、FIFO、Stop 和
host operation drain。资格、精确 proposal、wait、scope、新输入、lease 和既有 handoff 回执
继续由 `resumeApprovalWithOwner` 的原路径检查。

真实 SQLite、公共 Review/Resume API 和实际本地文件 apply 的专项 race 三次通过（45.371s）：
完成时 apply 一次；交接前 Stop/shutdown 阻止本次续行；交接后取消 continuation，后续调用
不重复执行；不同 proposal 不启动模型。只有模型 provider 与 handoff 读回时点由测试控制。

```powershell
go test -race ./internal/application -run '^TestHostOwnedQueuedApprovalSurvivesAutomaticCallerCancel$' -count=3 -v -timeout=90s
go test -race ./internal/application -run '^(TestHostOwnedQueuedApprovalSurvivesAutomaticCallerCancel|TestHostOwnedApproval.*|TestApprovalContinuation.*|TestHostLifetimeDoesNotDetachAutomaticThreadTurn|TestHostOwnedQueuedOrdinaryTurnSurvivesAutomaticCallerCancel|TestHostOwnedAutomatic.*|TestThreadPeerMessages.*)$' -count=1 -timeout=180s
```

最终 focused race 通过 103.478s，覆盖既有审批 scope/新输入/晚到重放保护、真实 HTTP 断开、
ordinary 队列、automatic caller 取消和 peer；`go vet ./internal/application` 与 diff check 通过。
