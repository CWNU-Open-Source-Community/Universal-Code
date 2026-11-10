# Workflow process restart experiments

2026-11-07, Windows amd64. Base: `cfeee05a8724a23c673b0ed9971f2ef6e8de9674`.

The new tests add actual process-exit coverage to the existing workflow recovery
tests. They pass on the unchanged production implementation. This commit changes
tests and this report only.

Each subprocess calls the real workflow scheduler and SQLite store, then exits
with code 87 immediately after one committed operation. `Close`, test cleanup,
workflow claim release and Run lease release do not run. The parent opens the same
database with a second service instance.

| Committed boundary before process exit | Result after ownership expires |
| --- | --- |
| `PrepareWorkflowStep`, before child admission | Original step/child/session identities complete; one attempt, turn 1, one model call, 5 tokens. |
| Run execution lease acquired, before attempt start | Second worker takes the next lease generation, completes the original prepared work once, releases the lease; the old lease cannot renew. |
| `BeginSpecialistAttempt`, before model call | Original attempt becomes `worker_lost`; workflow/step fail with zero model calls and zero tokens. |
| Lease acquired, then workflow paused and database reopened again | Workflow remains paused, step pending, no attempt or model call. |
| Lease acquired, then Run cancelled and database reopened again | Workflow and step settle failed, no attempt or model call. |

All cases also verify that immediate reopening cannot steal the live workflow
claim. Frozen step inputs and execution identities are created through production
APIs; no status or result is seeded into SQLite. The provider is deterministic,
so these tests do not contact a remote model or exercise external side effects.

## Time boundary

The process termination and SQLite reopen are real. Lease expiry is simulated by
shifting workflow `created_at` and `claim_until`, plus any Run lease's acquired,
renewed and expiry times, back together by 16 minutes. The test workflow has an
overall one-hour budget. It does not wait 16 wall-clock minutes or claim to prove
recovery before the existing claim expires.

Production currently grants a fixed 15-minute workflow claim without renewal.
A restarted host waits for that claim even after the Run lease expires. A workflow
whose remaining overall timeout is shorter than that wait reaches timeout cleanup
instead of resuming its prepared work. This report records that existing timing
boundary; this commit does not change the claim policy.

## Verification

```powershell
go test ./internal/application -run '^(TestWorkflowRestartProcess|TestWorkflowHostContinuesPausedCheckpointAfterDatabaseReopen|TestWorkflowHostDoesNotReplayUnfinishedAttemptAfterDatabaseReopen|TestScheduledAutomaticCrashBeforeReceiptIdentityRecoversWithOneAttempt|TestScheduledAutomaticWorkflowLostResponseObservesOriginalAcrossRestart)' -count=1 -v -timeout=90s
```

Passed in 6.159s. The child-only entry point skips during direct enumeration;
the parent test actually executes it five times and checks each exit code. Full
output: [test-output.txt](test-output.txt). Existing scheduled execution recovery
tests in this command also retain the original occurrence and attempt.
