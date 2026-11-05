# Run 取消后的 workflow 终态传播复现

基线：`21f56e6b390abf1c2a7de86f9ae8af68d34edb91`，分支 `Qiyuanqiii/workflow-repro-cancel`。Windows amd64、Go 1.26.10、`CGO_ENABLED=1`、现有 WinLibs gcc。完整实跑输出见 [cancel-output.md](cancel-output.md)。产品源码未修改。

## 命令与替代层

```powershell
# 默认只验证可编译，测试应 Skip；本次 exit 0。
go test ./internal/application -run '^TestWorkflowFaultCancelRunConverges$' -count=1 -timeout=60s -v

# 实际复现；本次 exit 1。
$env:UC_WORKFLOW_FAULT_REPRO = '1'
go test ./internal/application -run '^TestWorkflowFaultCancelRunConverges$' -count=1 -timeout=60s -v
```

唯一替代层为既有 `specialistTestProvider`：依次提供合法的 `continue` 和成功 `finish` 响应，每次计 5 tokens。测试使用真实 SQLite、RunService、SpecialistExecutionService、WorkflowService 和宿主 `RunDue`。故障通过首轮返回后调用真实 `RunService.Cancel` 触发。workflow 设置 3 秒期限，使用有界 timer 等待真实时间跨过期限；没有篡改数据库或传入未来时钟。每个子测试上下文上限 15 秒，测试进程上限 60 秒。

## 预期与实际

预期：取消 Run 后，未完成的 workflow 和 step 均收敛为取消或失败终态；宿主重复推进、数据库重开和超时均不应重新调用模型或持续报错。测试断言这一正确行为，复现缺陷时保持失败。

| 场景 | 实际结果 |
| --- | --- |
| 正常对照：continue → finish | PASS，0.61 秒；workflow/step/child 完成。2 次模型调用、10 tokens。重开数据库后连续 3 次推进均不重放。 |
| 首轮 continue 后取消 | Run/child 均 cancelled；已提交 attempt 保持 continued，active attempt 为空。workflow 此时仍 running，step pending。 |
| 取消后连续 3 次宿主推进 | 第一次将 workflow 写为 waiting，reason=`Run is not running`、revision=4；随后两次 `ran=false`。step 持续 pending。 |
| 第一次数据库重开后连续 3 次推进 | 均 `ran=false`；仍为 waiting/pending、revision=4。 |
| 真实期限经过后连续 3 次推进 | 均 `ran=true` 并返回 `INTERNAL: workflow budget cleanup is incomplete`；底层为 `FAILED_PRECONDITION: run ... is terminal as cancelled`。状态及 revision 均未改变。 |
| 第二次数据库重开后再次推进 | 同样报错；仍为 waiting/pending、revision=4。 |

启用测试总计 4.14 秒，其中取消子测试 FAIL（3.53 秒）。最终取消场景仍为 1 次模型调用、5 tokens；执行租约为 released，active attempt 为空，command runtime jobs 为 0。此次任务未请求工具，结果证明持久化 workflow/step 未收敛；未观察到真实进程残留。

## 根因与修复边界

`internal/application/workflow.go:233` 将 cancelled Run 与可恢复的暂停统一写成 waiting，并保存依赖指纹。后续 `RunDue` 因指纹不变而跳过，数据库重开保留同一等待状态。

期限经过后，`workflow.go` 的过期路径调用 `workflow_round.go:224` 的 `expireWorkflowPending`。它发现 pending step，进入 `workflow_round.go:207` 的 `rejectWorkflowBudget`，尝试获取 Run 执行租约。`internal/store/execution_leases.go:45` 正确拒绝 terminal Run；上层将此包装为 INTERNAL，未完成 step 和 workflow 的终态提交。后续宿主轮询重复相同错误。

修复应区分终态 Run 与可恢复等待，并提供在 Run 已终态、child 已停止时仍可完成 workflow/step 结算的受约束路径。保持终态 Run 禁止重新执行的租约检查；取消收敛不应依赖再次取得 worker 执行权限。此次提交仅包含复现测试、日志和说明，未实现修复。
