# 计划任务与结构化诊断 / Scheduled Jobs and Structured Diagnostics

Schema v122 adds a durable, bounded monitor for an explicit Run and three metadata-only
diagnostic protocols. It does not add an indefinite autonomous agent, arbitrary cron
Shell, OS service, autostart entry, or remote control plane.

Schema v122 为显式指定的 Run 增加持久化、有边界的监控器，以及三种仅元数据诊断协议；
它不增加无限自治 Agent、任意 cron Shell、操作系统服务、开机启动项或远程控制平面。

## Safety defaults / 安全默认值

Schema v167 adds immutable per-job consent for ordinary desktop observation. It does not
backfill existing jobs or change their original specification hashes.

- The owner and target are an exact existing Run and its root Agent.
- `read_only` is the default; its zero model-call budget disables model calls.
  Explicit `automatic` targets instead use the selected task's existing model/tool budget.
- Deadline, maximum rounds, maximum elapsed time, retry attempts, and backoff are mandatory.
- The target reaching a terminal state stops the monitor by default.
- Concurrency is fixed at one. Unchanged metadata records an `unchanged` round without a
  model or tool call.
- The worker is process-local and startup-only. HTTP/Desktop cannot enable it at runtime.
- The schedule projection includes the operator-configured execution target. Raw observed
  events, terminal input, lease owners and fence tokens remain withheld; diagnostic exports
  retain their existing metadata-only contract.

## CLI

Create a one-shot read-only monitor:

```powershell
cyberagent run schedule create <run-id> `
  --at 2026-08-20T12:00:00Z `
  --deadline 2026-08-20T13:00:00Z `
  --max-rounds 1 --max-model-calls 0 --max-elapsed-seconds 3600 `
  --max-attempts 3 --initial-backoff-seconds 5 --max-backoff-seconds 60 `
  --notify on_failure --operation-key <stable-key>
```

Add `--every 15m --timezone Asia/Hong_Kong` for a periodic monitor. The elapsed interval
is anchored in UTC; the IANA timezone is retained for display/audit. `--misfire run_once`
catches up one overdue occurrence after restart or sleep; `--misfire skip` records and
advances past it.

```powershell
cyberagent run schedule list <run-id> --limit 50
cyberagent run schedule show <job-id> --rounds 20 --notifications 20
cyberagent run schedule pause  <run-id> <job-id> --revision <n> --operation-key <key>
cyberagent run schedule resume <run-id> <job-id> --revision <n> --operation-key <key>
cyberagent run schedule cancel <run-id> <job-id> --revision <n> --operation-key <key>
cyberagent run schedule tick --owner cli_scheduled_worker
```

`tick` performs one foreground due-job step. It does not start a daemon or an unbounded
loop. Reuse the same operation key only to replay the exact same intent; changing the
body under an existing key is rejected.

## Worker startup / Worker 启动

The API worker runs only when both control auth and the explicit startup flag are present:

```powershell
$env:CYBERAGENT_API_TOKEN = "<read-token>"
$env:CYBERAGENT_API_CONTROL_TOKEN = "<control-token>"
cyberagent api serve --enable-scheduled-job-worker
```

Ordinary Desktop startup selects confirmed jobs before reconciliation and claiming:
`read_only` with observation consent version 1 and zero model-call budget, or `automatic`
with separate execution consent version 1. Existing unconfirmed observations require an
exact-job confirmation. Restarting preserves that confirmation but still rechecks current
execution permissions. The explicit `--enable-scheduled-jobs --enable-scheduled-job-worker`
launch and operator preview retain their existing whole-job worker behavior.

Closing API/Desktop drains and stops the worker. Capability output always reports
`runtime_enable_supported=false`, `persistent_service=false`, and
`authority_escalation=false`.

## PR 持续跟进

在任务的 **改动与交付 → PR 状态 → 持续跟进 PR** 中，选择已连接的仓库和当前分支的
PR，设置检查间隔、截止时间和自动修复次数。默认每 5 分钟检查一次，持续 24 小时，最多
续跑 3 轮；可选 1–60 分钟、未来 7 天以内及 1–20 轮。默认只处理启用后出现的评论；
启用时已有的 CI 失败也会处理。“包含已有评论”可将当前评论一并纳入第一轮。

此入口要求 GitHub review、任务执行和 scheduled-job worker 三项启动能力均已启用。
API 在提供控制凭据后，使用 `--enable-github-review --enable-permission-control
--enable-scheduled-job-worker`；任务执行由现有控制配置提供。旧定时观察任务继续使用
原来的只读观察流程。

Worker 读取当前 PR 提交的 CI、审阅、行内及普通评论；资料不完整、任务正在运行或没有
新反馈时不启动模型。新反馈进入原 Thread，并使用该任务已绑定的工作目录和分支，
复用 Full / Ask / Auto、工具审批和原任务预算。需要隔离工作区时，应先将任务绑定到
已有 worktree，再启用跟进；跟进过程中不会自动更换工作区或创建新分支。

续跑正文及操作标识先持久化，再交给现有 Thread 执行器。重启后恢复同一轮次；审批等待
期间只观察原执行结果。未知工具结果、失效的权限激活、预算不足或工作区与 PR 不一致时
显示“需要处理”，由用户处理后继续。新建轮次才扣减自动修复次数，轮询和恢复不重复计数。

一轮结束后重新检查 CI，显示待复核结果；点击“继续跟进”后继续等待新的反馈。
“暂停”或“停止”会取消当前实例正在执行的对应跟进轮次，并阻止之后的调度；其他进程
在租约续期时发现授权状态改变并停止。应用关闭时停止 worker，下次启动按保存状态继续，
期限和次数上限仍然有效。PR 关闭或合并后结束跟进。

提交和推送沿用原任务已有授权；合并 PR、向 GitHub 发布评论均需用户另行明确授权。
PR 的远端文字作为待分析资料传入，不改变任务权限。此功能的持久化记录和 API 使用
`pr-followup.v1`；读取 `GET /api/v1/threads/{thread_id}/pr-followups`，创建和
`pause|resume|cancel` 使用控制凭据，创建还需要稳定的 `Idempotency-Key`。

## Structured diagnostics / 结构化诊断

```powershell
cyberagent doctor snapshot --run <run-id> --json
cyberagent debug query --run <run-id> --after 0 --limit 100 --json
cyberagent debug query --run <run-id> --from <RFC3339> --to <RFC3339> `
  --correlation-kind attempt --correlation-id <attempt-id> --json
cyberagent doctor bundle --run <run-id> --after 0 --limit 100
```

`doctor-snapshot.v1` distinguishes `ready`, `degraded`, `not_configured`, and
`not_probed`; absence of an active browser/sandbox/plugin probe is not reported as success.
`debug-query.v1` returns at most 100 timeline items after an exclusive sequence cursor and
scans at most 500 persisted events inside a maximum seven-day window. Correlation kinds are
exactly `run`, `attempt`, `tool`, `process`, and `request`. Continue with
`next_after_sequence` when `has_more=true`.

Timeline timestamps are monotonic for display. If persisted timestamps regress, the item
retains `occurred_at`, advances `observed_at`, and sets `timestamp_adjusted=true`. Every item
has `evidence=persisted_event` and `payload_state=withheld`; the classification is one of
`model`, `tool`, `policy`, `application`, or `infrastructure`.

## HTTP/OpenAPI

Read bearer routes:

- `GET /api/v1/scheduled-jobs?run_id=<run>&limit=<1..100>&cursor=<opaque>`
- `GET /api/v1/runs/{run_id}/scheduled-jobs?limit=<1..100>&cursor=<opaque>`
- `GET /api/v1/scheduled-jobs/{job_id}?round_limit=<1..100>&notification_limit=<1..100>`
- `GET /api/v1/doctor?run_id=<run>`
- `GET /api/v1/debug?run_id=<run>&after_sequence=<n>&limit=<1..100>...`
- `GET /api/v1/diagnostic-bundle?run_id=<run>&after_sequence=<n>&limit=<1..100>...`

Schedule lists return `data.items` and a top-level `page` containing `limit` and,
when older records remain, `next_cursor`. Omit `cursor` for the first page; pass
the returned cursor unchanged with the same route and Run filter for the next.
Creation time and ID define stable descending order, so status changes and newer
inserts do not shift continuation pages. Each read returns at most 100 jobs; the
shared browsing bound is 100,000, after which `page.truncated` is explicit.
The Automations workspace offers **Load more schedules** and retries the same
continuation after a read failure, keeping older active plans available for pause
and cancellation. The TypeScript client's existing three-argument
`listScheduledJobs(runID, limit, signal)` call remains valid; an optional fourth
`cursor` argument reads the next page and the returned value includes `page`.

Control bearer plus `Idempotency-Key` routes:

- `POST /api/v1/runs/{run_id}/scheduled-jobs`
- `POST /api/v1/runs/{run_id}/scheduled-jobs/{job_id}/pause|resume|cancel`
- `POST /api/v1/runs/{run_id}/scheduled-jobs/{job_id}/enable-observation`

Creation accepts optional `observation_consent_version: 1` to save the job and its consent
atomically. Omission preserves legacy creation and idempotency behavior. The separate
confirmation body is `{"version":"scheduled-job-control.v1","expected_revision":<n>,
"observation_consent_version":1}`. Confirmation never resumes a paused job. The receipt is
immutable; pause/cancel control whether an already confirmed job can be observed.

Unknown/duplicate/blank query parameters and unknown JSON fields fail closed. Scheduled
control never returns a fence token or starts execution inside the HTTP request.

## Desktop

Open **自动化 / Automations** from the current conversation's Inspector tools. Select an
existing task and choose a timed task instruction, a saved workflow, PR/CI follow-up, or
read-only observation. The selected task supplies its workspace and current permissions.
The host must remain running; closing the renderer does not stop the worker.

Read-only schedules still require their own observation confirmation and never invoke a
model or tool. The ordinary desktop worker selects only explicitly confirmed observations
or automatic executions (`confirmed_jobs`). Legacy schedules are not silently enabled.
A paused schedule remains paused after restart.

The page reads actual worker health instead of presenting a startup capability as running.
Select a job to inspect next wake, deadline, round
budget, latest result, and notifications; active jobs can be paused/cancelled and paused
jobs resumed with exact revision CAS. **Export diagnostics** downloads the same redacted
`diagnostic-bundle.v1` JSON after the renderer validates its redaction contract.

## Automatic task and workflow execution / 自动执行

The existing create endpoint accepts `execution_mode: "automatic"`,
`execution_consent_version: 1`, and one `execution_target`:

```json
{"kind":"thread","thread_id":"thread-…","prompt":"Run the project tests and investigate failures."}
```

```json
{"kind":"workflow","workflow_definition_id":"workflow-…","inputs":{}}
```

The target Run belongs to the selected task. A workflow requires a running Run; each
occurrence starts the selected immutable definition through `WorkflowService.Start`, then
the existing host worker advances it. Thread instructions use `ExecuteAutomatic` and the
ordinary Full/Ask/Auto permission, workspace, approval, and remaining Run budget checks.
Restarting the host does not reactivate process-local Full Access. Pending approvals remain
attached to the original attempt and must be resolved through the existing task controls.

For automatic jobs, `stop_on_target_terminal` is false and `max_model_calls` is zero:
`max_rounds` limits scheduled executions, while actual model/tool/cost limits remain the
Run's budget. The legacy model-call counter is not a count of a multi-turn task's requests.
Execution consent is separate from observation consent and binds the selected workspace
and Git branch, when present. A changed binding requires a new schedule.

Each occurrence keeps its submission key and execution receipt across retries/restarts.
The detail response includes `snapshot.executions`, with the original Run/message or
workflow execution identity and its latest result. Waiting or running occurrences are
observed without submitting a second task. Pause/cancel stop future dispatches; an already
started workflow is controlled through the existing workflow/task controls. Notifications
are saved in the same in-product schedule history.

PR/CI event triggers reuse the existing PR follow-up service and its exact remote-head,
workspace, feedback cursor, and execution checks. They retain their existing repair-turn
budget and result review; the general entry does not create a second GitHub polling engine.

## Repair mode / 修复模式

`approved_repair` is not a shortcut around normal tools. Creation requires Code/Deliver,
root, explicit `--confirm-repair`, and operator-confirmed modern Full with current
process activation. Historical permission modes cannot authorize repair.
The authorization stores exact snapshot identities/revisions and expiry. Every execution
rechecks them; phase, permission, expiry, cancellation, or revision drift fails closed.
Only separately configured repair capabilities may be used, and their normal Policy and
approval checks still apply. Monitoring itself cannot create or renew authorization.
The current production worker intentionally installs no implicit repair executor; a changed
`approved_repair` round therefore fails closed unless a future ordinary command-lifecycle
adapter is explicitly configured and independently enforces those same checks.
当前生产 Worker 有意不安装隐式修复执行器；在未来显式接入、且独立执行同一组普通命令
生命周期检查的适配器之前，发生变化的 `approved_repair` 轮次会失败关闭。

## Restart and failure semantics / 重启与失败语义

- An active unexpired lease remains owned; another worker cannot claim it.
- Exactly expired claims reconcile to `retry_wait` with a durable retry event.
- Completion requires the exact occurrence, attempt, generation, owner digest, and fence.
- `run_once` and `skip` give deterministic suspend/restart catch-up behavior.
- Exhausted retries, deadline, elapsed/round/model budgets, stale authorization, target
  termination, and cancellation are terminal stop reasons.
- Notifications are durable in-product records (`change`, `failure`, `recovery`,
  `completed`), not email or push delivery.

The design decision and rejected alternatives are recorded in
[ADR 0121](adr/0121-durable-scheduled-monitoring-and-structured-diagnostics.md).
