# Workflow 取消与暂停到期修复

日期：2026-11-06（Asia/Hong_Kong）。修复分支：`Qiyuanqiii/workflow-lifecycle-fix`，基于复现提交 `9d9cf3bc`。历史复现文档及日志保持原样。

## 行为变化

- Run 已取消/失败/完成时，unfinished workflow 收敛到现有 `failed` 终态，并记录 `Run ended as ...`。取消发生在 workflow 已暂停时也能处理；旧等待指纹不会阻止终态结算。
- `paused` execution 仅在原 deadline 到期或 Run 已终态时进入宿主候选及 claim。未到期暂停继续禁止 worker 调度，原 deadline、累计用量与预算基准不变。
- 新生命周期路径只读取、关闭资源和结算，复用当前 Run lease 与原有 attempt 中断逻辑。它不调用要求 running Run 的 `RecoverSpecialistAttempts`，也不恢复 Run、不放宽普通 dispatch/checkpoint 准入。
- 暂停 Run 的 idle child 可以在持有有效 Run lease 后退役；审批 attempt 保持准确 ID、旧 lease/generation，资源关闭前与事务提交时都验证 workflow claim/revision、父子/Run/session/workspace 归属。步骤、frozen item 与 workflow 在同一事务收尾，释放未用 child 预留。
- 已终态 child 不改写、不重放。已有 native report 在报告型步骤中可结算 completed；`verified_code` 尚待验收的 pending step 结算 failed 并保留报告，避免把模型报告当成验证结果。此前已提交完成的步骤不受影响。
- 对宿主中断后遗留的非审批 running attempt，仍有效的旧 lease 会阻止清理；旧 lease 到期并被替代后，只关闭精确 attempt 资源，以 `interrupted/worker_lost` 记录未知结果并将步骤失败收尾，不重新执行该 attempt。
- 活跃 Run lease 阻塞清理且版本未前进时，宿主继续后续候选，避免最旧的过期任务饿死其他 Run。无 pending child 的到期 execution 仍可直接结算。

## 覆盖与验证

取消与两种暂停的 fault tests 已移除 opt-in，继续使用真实 SQLite、Run/workflow 服务、宿主、审批和工具通道；只有模型回复采用确定性 provider。取消后反复推进、数据库重开与真实 deadline 经过均保留断言；暂停用例验证未批准文件未写入、模型调用及计费 tokens 未增加、未用 child 预留归零。

新增定向测试涵盖：未到期暂停不执行；取消已暂停 workflow；终态 Run 与已消费等待指纹；旧 claim/过期 claim/错误 revision；并发 host 排他；错误 Run lease 与准确 attempt 身份；已有 native completion 的 child 保持终态且无重放；`verified_code` 未验收报告；遗留旧 attempt 的 lease 失效及未知结果结算；同 tick 推进另一个独立 Run。旧 lease 过期测试使用真实 admission、BeginSpecialistAttempt 与 SQLite，仅把持久化时钟推进到已过期状态来表示已丢失宿主，并未启动/杀死真实模型进程。

环境：Windows amd64，Go 1.26.10，`CGO_ENABLED=1`，现有 WinLibs gcc。下面的最终定向执行均退出 0：store `58.902s`，application `155.554s`；额外的已调度 workflow 对照 `1.108s`。`git diff --check` 通过。最终检查针对本提交的实现与测试树；未运行整个项目测试或桌面 UI。

开发中曾有两次有用的失败：原有到期审批对照指出活跃 Run lease 应等待；新增 verified_code 对照的首版 fixture 没有真实 workspace，native worker 在模型前失败。前者补了等待与宿主公平性，后者改为已有真实 workspace/worker runtime fixture。最终执行已覆盖对应修正。

```powershell
go test ./internal/store ./internal/application -run '^(TestWorkflow|Test.*(Lifecycle|RecoverSpecialist|ExecutionLease|RunPause|RunCancel))' -count=1 -timeout=300s
go test ./internal/application -run '^TestScheduledAutomaticWorkflow' -count=1 -timeout=90s
```
