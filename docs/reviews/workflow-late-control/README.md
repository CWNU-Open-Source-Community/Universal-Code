# Workflow completion after Run control

2026-11-07. Reproduction base:
`a8e02652966b13fab2be429a77914f10c893b53a` (overnight integration).

A Run cancellation or pause could commit after the interpreter's final checks
but before its `completed` checkpoint. That checkpoint checked workflow revision
and ownership, but did not check the Run's current status. The old interpreter
then marked the workflow completed and created its completion notification.
Reopening the database left it completed, so the scheduler never applied the
cancelled or paused Run state to that execution.

The completion UPDATE now requires its stored parent Run to remain running in
the same SQLite statement. A rejected write returns the existing Conflict error.
Waiting and failure checkpoints keep their existing conditions. The next host
tick can therefore settle cancellation or wait for an explicitly resumed Run.

## Actual reproduction and result

The test first completes one real workflow child with a deterministic model
provider. At the final checkpoint boundary, a store wrapper calls the public
`RunService.Cancel` or `RunService.Pause`, waits for its real SQLite commit, then
lets the old interpreter call the real checkpoint method. It closes SQLite,
reopens the same file with fresh services, and calls the public scheduler entry.

| Control committed before old completed checkpoint | Before | After |
| --- | --- | --- |
| Cancel | Old Advance succeeds; reopened workflow remains completed. | Old completed checkpoint conflicts; next RunDue settles failed with `Run ended as cancelled`. |
| Pause | Old Advance succeeds; reopened workflow remains completed. | Old completed checkpoint conflicts; reopened workflow waits. Explicit Start completes it using the saved result. |

Both controls retain the same native completion report and attempt ID, the
completed step output, one model call and five charged tokens. The rejected
completion creates no notification. Store tests also prove that rejection leaves
the checkpoint/version unchanged, while failure after cancellation and waiting
after pause remain writable.

These are real SQLite/service executions with a controlled method-boundary
interleaving. Model responses are fixtures. Host reconstruction uses database
close/reopen and new service instances within the test process.

## Verification

```powershell
# Before production change: both controls fail the regression (0.98s).
go test ./internal/application -run '^TestWorkflowLateCompletion' -count=1 -v -timeout=30s

# After: application 1.255s; store 0.804s.
go test ./internal/application ./internal/store -run '^(TestWorkflowLateCompletion|TestWorkflowCompletedCheckpointHonorsRunControl)' -count=1 -v -timeout=30s

go test ./internal/application ./internal/store -run '^(TestWorkflow(CancelledPause|FaultCancel|FaultPause|FaultCompletion|HostContinuesPaused|Lifecycle)|TestWorkflowClaimsPauseAndStableStartProtectDispatch)' -count=1 -timeout=120s
go vet ./internal/application ./internal/store
git diff --check
```

Logs: [before](before.txt), [after](after.txt), [existing regressions](regressions.txt).
Existing cancellation, pause, completion and lifecycle regressions pass in 25.425s
for application and 2.238s for store. `go vet` and `git diff --check` exit zero.
