# Workflow 完成通知丢失：实际故障复现

本文是修复前证据；当前行为与检查结果见[修复验收](fix-verification.md)。

结论：workflow 写入 `completed` 后、总结通知入队前发生一次存储错误或进程退出，已完成的 worker 结果会保留，但宿主不会自动补发主会话总结。数据库重开后仍复现；显式调用 `Advance` 可以补发，重复调用没有重复通知。

基线：`main` 的 `21f56e6b390abf1c2a7de86f9ae8af68d34edb91`。独占分支：`Qiyuanqiii/workflow-repro-completion`。仅新增测试与本说明、日志，没有修改产品源码。环境为 Windows amd64、Go 1.26.10、`CGO_ENABLED=1`、现有 WinLibs GCC。完整输出和真实退出码见 [completion-output.md](completion-output.md)。

## 运行命令

在仓库根目录的 PowerShell 中执行：

```powershell
$env:UC_WORKFLOW_FAULT_REPRO = '1'
go test ./internal/application -run '^TestWorkflowFaultCompletion' -count=1 -v
# 实际 exit 1；正常对照通过，三个故障案例在“宿主自动补发”断言失败。

Remove-Item Env:UC_WORKFLOW_FAULT_REPRO -ErrorAction SilentlyContinue
go test ./internal/application -run '^TestWorkflowFaultCompletion' -count=1 -v
# 实际 exit 0；四个顶层测试默认全部 Skip。
```

只运行以上完成通知相关测试，没有运行全套测试。

## 复现方式与实际结果

每例先通过真实 `WorkflowService.RunDue` 执行一个 worker，保存其 `Finish` 报告与 workflow output。此时 `Jobs=1`、worker token 用量为 5、通知和主会话总结均为 0。worker 与主会话模型响应由仓库现有 `specialistTestProvider` 提供；SQLite、workflow interpreter、子代理执行、通知入队、wake 消费、主会话 handoff 均走实际实现。

| 案例 | 精确故障位置 | 实际结果 |
| --- | --- | --- |
| 正常完成对照 | 不注入故障 | 自动产生一个已提交通知及一个主会话总结；重复 `Advance`、关闭/重开 SQLite 后再次 `Advance`，仍各一个，模型总调用两次。通过。 |
| `get-run-once` | `completed` checkpoint 成功后，完成总结读取 `GetRun` 时仅一次返回 `UNAVAILABLE` | `RunDue` 报 `workflow completed; main-conversation summary is pending: UNAVAILABLE`；数据库重开后，健康宿主也不补发。自动恢复断言失败。 |
| `enqueue-once` | `completed` checkpoint 成功后，仅该 execution 总结 key 的 `EnqueueOperatorSteering` 一次返回 `UNAVAILABLE` | 与上一例相同；SQLite 中没有该完成通知。自动恢复断言失败。 |
| 真实子进程退出 | 子进程内真实 SQLite 完成 checkpoint 的 UPDATE 和读取均返回之后、回到总结函数之前调用 `os.Exit(86)` | 子进程实际以 86 退出；未执行 SQLite `Close`、claim release 或测试清理。父进程重开相同数据库后结果仍是 `completed`、一个已完成 job、无通知。自动恢复断言失败。 |

两个存储失败属于测试包装器的方法级一次性注入，没有模拟 SQLite 自身报错。包装器嵌入真实 `SQLiteStore`，只在完成 checkpoint 后启用该故障；正常 worker 阶段无注入。重开时换回无故障包装器与正常总结 handoff。

进程退出案例实际启动当前 Go 测试二进制，在该完成点退出；不是用关闭/重开数据库代替进程退出。退出由测试包装器内 `os.Exit` 触发，没有执行外部强杀，也没有模拟断电。

三个故障案例重开后的结果一致：

- `Status=completed, Jobs=1, WorkerTokens=5, Notifications=0, Summaries=0`；已保存 output 仍在。
- 分别以当前时间、当前时间加 16 分钟、加 24 小时调用 `ListWorkflowCandidates` 和宿主 `RunDue`：候选始终为 0，`ran=false`，没有新的模型调用。时间偏移仅是传入的 scheduler 时间，未实际等待；16 分钟已超过 workflow claim 的 15 分钟有效期。
- 接着显式 `Advance`，实际通知数、已提交通知数、主会话总结数都变成 1，模型累计调用由 1 增到 2；主会话模型请求含正确 workflow ID 与已保存的 worker 输出。再次 `Advance` 后计数不变，worker token 用量一直为 5。

测试对“健康宿主自动补发一次总结”的正确行为使用 `t.Errorf` 断言，因此目前明确失败，同时继续记录显式恢复与重复调用的结果。默认不设置 opt-in 环境变量时跳过。

## 根因与修复范围

`internal/application/workflow.go:352` 先将状态设为 `completed`，checkpoint 成功后才在第 355 行调用 `summarizeWorkflow`。`internal/application/specialist_continuation.go:194` 的 `GetRun` 和第 214 行的 `EnqueueOperatorSteering` 因而均位于持久化完成之后。此处错误只返回“summary is pending”，没有为宿主留下可调度的待通知状态。

`internal/store/workflow_worker.go:18` 的候选查询只包含 `running / waiting / waiting_approval`。重启宿主不会再选择 `completed`；即使过去 claim 已过期也是如此。与此对应，`internal/application/workflow.go:163` 的显式 `Advance` 在未取得 claim、读到 `completed` 时会直接调用总结，因此手动恢复有效。`internal/application/workflow_summary.go:30` 使用 execution ID 派生的稳定总结 key，实际重复调用证明当前通知去重有效。

修复需要在完成 checkpoint 同一事务持久化待通知记录，并让宿主选择和重试尚未交付的完成通知；成功后持久化交付状态，沿用 execution ID 的稳定 key。该恢复只需处理总结通知，不能重新执行已完成 worker。仅把所有 `completed` 加入现有候选查询并不充分：现有 claim 也只允许非终态，而且已总结的 workflow 不应被永久重新选择。

本次没有实现修复。已验证的范围是本地 SQLite 与应用服务恢复、实际测试子进程退出；没有使用真实模型或桌面 UI，也没有覆盖通知已入队后的崩溃位置。
