# Workflow pause deadline reproduction

本文是修复前证据；当前行为与检查结果见[修复验收](fix-verification.md)。

实际复现了暂停期间的两处到期清理阻塞。未暂停对照通过；暂停 workflow 后，宿主完全不接纳该 execution，审批 attempt 和未使用预算一直保留到恢复。仅暂停 Run 时，宿主接纳 execution，但清理报错，也要恢复 Run 后才能完成。

## 版本与运行方式

- 产品基线：`main`，`21f56e6b390abf1c2a7de86f9ae8af68d34edb91`。
- 独立分支：`Qiyuanqiii/workflow-repro-pause`；工作树：`D:/CodexProjects/worktrees/workflow-repro-pause`。
- 环境：Windows amd64，Go 1.26.10，`CGO_ENABLED=1`，现有 WinLibs gcc；未修改依赖或产品源码。
- 测试：`internal/application/workflow_fault_pause_test.go`；完整输出：[pause-output.md](pause-output.md)。

```powershell
$env:UC_WORKFLOW_FAULT_REPRO='1'
go test ./internal/application -run '^TestWorkflowFaultPauseDeadlineCleansPendingApproval$' -count=1 -v -timeout=2m
```

最终记录：测试执行 16.388 秒，退出码 **1**。`unpaused` 通过，`run_pause` 和 `workflow_pause` 的正确行为断言失败。测试断言到期应清理，而非断言缺陷必须存在。

默认不启用复现：

```powershell
Remove-Item Env:UC_WORKFLOW_FAULT_REPRO -ErrorAction SilentlyContinue
go test ./internal/application -run '^TestWorkflowFaultPauseDeadlineCleansPendingApproval$' -count=1 -v -timeout=2m
```

实际结果：`SKIP`，退出码 **0**。每组操作使用 20 秒 context，Go 测试设 2 分钟 timeout。只运行了上述目标测试，没有运行完整套件。

## 注入层与预期

使用真实临时 SQLite、workspace、RunService、WorkflowService、worker runtime、tool gateway、审批记录、workflow claim 和 Run lease。确定性测试 provider 只替代模型回复，不调用真实模型：第一条回复调用 `workspace_change` 创建文件提议，第二条回复调用 `workspace_apply`，默认权限加精确文件审阅策略使其停在待审批状态。测试没有批准该提议。

三组各自通过 `WorkflowService.Start` 保存 **5 秒**固定期限，先由宿主 `RunDue` 建立真实审批停点，再分别保持运行、调用 `RunService.Pause`、调用 `WorkflowService.Pause`。测试实际等待 `CreatedAt + 5s + 25ms`，然后驱动三次宿主 `RunDue`。没有修改 SQL、持久化时间或系统时钟，也没有用未来的 `RunDue(now)` 冒充真实到期。到期后的直接 claim 仅用于诊断，取得时立即释放，三个场景都执行同一诊断。

依据 `docs/execution-workers.md:193` 的“deadline can still expire while paused or waiting”约定，预期宿主到期后将 workflow 和 pending step 置为失败，终止仍悬挂的审批 attempt、退役 child，并释放未使用额度；恢复应保留原期限，不再执行模型或文件写入。

## 实际状态

各组审批停点相同：workflow `waiting_approval`，step `pending`，child/attempt `running`，有效审批等待为 `true`。Run 上限 128 tokens，child 分配 64 tokens，已计费 10 tokens，未使用预留为 54 tokens；已使用 1 turn，child 上限 2 turns。

| 场景 | 到期候选 / claim | 到期后宿主结果 | workflow / step | child / attempt | 有效审批等待 | 未使用预留 tokens |
| --- | --- | --- | --- | --- | --- | --- |
| 未暂停 | 1 / 成功 | 首次 `ran=true`，后两次 `false`，无错误 | `failed` / `failed` | `cancelled` / `interrupted` | false | 0 |
| 仅 Run 暂停 | 1 / 成功 | 三次均 `ran=true`，均报清理错误 | `waiting_approval` / `pending` | `waiting` / `interrupted` | false | 54 |
| workflow 暂停 | 0 / 失败 | 三次均 `ran=false`，无错误 | `paused` / `pending` | `running` / `running` | true | 54 |

- workflow 暂停组到期时间为 `2026-11-05T16:15:21.246013Z`，检查时为 `16:15:21.2712829Z`。版本仍为 5，原因仍为 `operator pause`。
- 仅 Run 暂停组在暂停动作时已把原 attempt 置为 `interrupted`，失败码 `run_paused`，child 变为 `waiting`；因此它已无有效审批等待，但 child 仍非终态，未使用分配仍被预留。到期 tick 返回 `INTERNAL: workflow budget cleanup is incomplete`，内部原因为 `Specialist recovery requires a running Run`。
- 两个暂停组各自恢复后，仅需 **一个**宿主 tick，就变为 `failed` / `workflow deadline exceeded`，step `failed`，child `cancelled`，预留降为 0。原 attempt 身份未更换；workflow 暂停组被置为 `interrupted/workflow_failed`，Run 暂停组保留原先的 `interrupted/run_paused`。
- 三组始终只调用模型 provider 两次，Run 已计费 tokens 始终为 10，目标文件始终不存在。恢复没有重置时间或重新执行模型。

`UnusedReservedTokens` 是依据真实 child 状态、上限与已用量，按 `internal/store/coordinator_admission.go:329` 的预留策略计算的未用额度；Run 的实际计费量来自 `GetRunAgentUsage`。终态 child 只保留已用量的预算占用。

清理后审批表原记录在三组中都仍为历史 `pending`；日志原样保存此值。这里用 `SpecialistToolApprovalState` 的结果和原 attempt 状态判断是否仍有有效审批等待，不能把历史记录的 `pending` 当成仍可调度的审批。本实验未尝试决定已退役 intent，不据此推断它还能执行。

## 根因与修复范围

**Workflow 暂停：实际验证了候选与 claim 排除 `paused` 的假设。**

1. `internal/store/workflow_worker.go:18` 的 `ListWorkflowCandidates` 只查询 `running/waiting/waiting_approval`。实测到期后候选为 0，三个宿主 tick 均不运行。
2. `internal/store/workflow.go:137` 的 `ClaimWorkflowExecution` 使用同样的状态集合。实测对该到期 execution 直接 claim 也返回 false。
3. 因而无法进入 `internal/application/workflow.go:216` 的到期分支及 `workflow_round.go:224` 的 `expireWorkflowPending`。恢复只改变控制状态，再调用同一路径立即成功清理，支持“入口状态过滤阻塞清理”的解释。
4. 修复不能只放宽候选：`CheckpointWorkflowExecution`、`ListWorkflowVerificationSteps`、`completeWorkflowStep` 和 `validateWorkflowSteps` 也限制状态。需要专门接纳**已到期**暂停 execution 的清理路径，保留 claim、revision、Run lease 及精确 attempt 所有权校验，同时继续阻止未到期暂停 execution 执行 worker。

**仅 Run 暂停：实际首个阻塞为清理复用了运行态恢复入口。**

`expireWorkflowPending → rejectWorkflowBudget` 在清理前无条件调用 `RecoverSpecialistAttempts`（`internal/application/workflow_round.go:210`）。该函数在 `internal/store/coordinator_attempts.go:427` 要求 Run 为 `running`，即使原 attempt 已因 Run 暂停而中断，也先返回失败。日志捕获了该内部原因；恢复 Run 后同一个 execution 立即成功清理。

后续 `failWorkflowRoundBudget` 还调用 `ValidateWorkflowDispatch`（`workflow_round.go:162`），其 `workflow_retry.go:37` 同样要求 `r.status='running'`。这是源码确认的下一道限制；本次 Run 暂停的实际错误首先发生在 recovery，尚未执行到该检查。修复应让到期清理使用允许非运行态 Run 的资源退役校验，继续持有有效 Run lease、workflow claim 和 revision，避免把暂停 Run 的普通调度权限一并放开。

当前产物仅新增复现测试与证据，未实施以上修复。
