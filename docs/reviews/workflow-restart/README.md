# Workflow process restart recovery

2026-11-07, Windows amd64. Base: `cfeee05a8724a23c673b0ed9971f2ef6e8de9674`.

The original experiments were added in test-only commit `efc99242`. The follow-up
fix gives newly acquired workflow claims a 30-second lifetime and renews them every
10 seconds while the existing interpreter runs. This lets prepared short workflows
recover after a process crash while retaining their original execution identity.

## Before and after

The same regression starts a workflow with a two-minute overall budget, exits the
actual subprocess immediately after `PrepareWorkflowStep` commits, reopens the
same SQLite database, and polls the production scheduler with the real clock.
No timestamps are changed in this regression.

| Version | Observed result |
| --- | --- |
| Before: fixed 15-minute claim | After 40 seconds: `running`, zero jobs completed, zero model calls. The regression fails. |
| After: 30-second claim, renewed every 10 seconds | Recovers in 30.253 seconds with the original step/child/session, turn 1, one model call and 5 tokens. |

Logs: [before](short-workflow-before.txt), [after](short-workflow-after.txt).

Renewal changes only `claim_until`, preserving checkpoint version and `updated_at`.
It requires the same owner and an unexpired claim. A renewal failure cancels the
current interpreter context; exit stops and joins the renewer before releasing
the original owner's claim. Renewal and release errors are returned. Claim,
renewal and release store calls use two-second context deadlines.
The claim renewer and interpreter share the same ten-minute deadline, applied
before the first wake read. Caller cancellation or that deadline stops both
renewal and worker execution. The deadline regression observes the context at
the first claimed read; it does not wait ten wall-clock minutes. A shorter
caller deadline remains in force.

The independent Run execution lease fences worker execution and side effects
while live. Cancellation stops renewal; an old worker that drains beyond the
lease expiry can lose ownership before returning. A replacement interpreter
settles the already-begun unknown attempt as `crashed` / `worker_lost` and does
not replay its model call. The old owner cannot overwrite that checkpoint when
it eventually exits. A paused accepted wave retains ownership
while settling; renewal neither resumes it nor permits a new dispatch. Claims
already persisted by older versions retain their original expiry until released
or renewed by their owner.

## Process exit boundaries

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

## Simulation boundaries

In the five boundary cases above, process termination and SQLite reopen are real.
Lease expiry is simulated by
shifting workflow `created_at` and `claim_until`, plus any Run lease's acquired,
renewed and expiry times, back together by 16 minutes. The test workflow has an
overall one-hour budget. It does not wait 16 wall-clock minutes or claim to prove
recovery before the existing claim expires.

The short-workflow regression and the 35-second long-worker test use actual
elapsed time. The long-worker test observes three successful renewals and proves
that a second SQLite owner cannot steal the claim past its original expiry.
Renewal storage failure, owner replacement and renewal-call timeout each cancel
the model fixture. An immediate drain-gate check verifies that a second worker
cannot acquire the still-live Run lease. Owner replacement uses one explicit
expiry-time injection; replacement, fencing and release use the production store.
An additional independent case keeps the cancelled provider draining for 35 real
seconds, past the Run lease expiry, and calls a second real WorkflowService on
the same database. It verifies zero replacement model calls, one original
`crashed` / `worker_lost` attempt, and the same replacement checkpoint after the
old interpreter finally drains. This models delayed provider cleanup after
cancellation.

## Verification

```powershell
go test ./internal/application -run '^TestWorkflowRestartProcessRecoversShortWorkflowBeforeDeadline$' -count=1 -v -timeout=65s
go test ./internal/application -run '^TestWorkflowClaim' -count=1 -v -timeout=120s
go test ./internal/application -run '^TestWorkflowClaimExpiryDuringLongDrainDoesNotReplayAttempt$' -count=1 -v -timeout=90s
go test -race ./internal/application ./internal/store -run '^TestWorkflowClaim' -count=1 -timeout=120s
go test ./internal/store -run '^TestWorkflowClaimRenewal' -count=1 -v -timeout=30s
go test ./internal/application -run '^(TestWorkflowRestartProcess|TestWorkflowHostContinuesPausedCheckpointAfterDatabaseReopen|TestWorkflowHostDoesNotReplayUnfinishedAttemptAfterDatabaseReopen|TestScheduledAutomaticCrashBeforeReceiptIdentityRecoversWithOneAttempt|TestScheduledAutomaticWorkflowLostResponseObservesOriginalAcrossRestart)' -skip '^TestWorkflowRestartProcessRecoversShortWorkflowBeforeDeadline$' -count=1 -v -timeout=90s
go test ./internal/application ./internal/store -run '^TestWorkflow' -skip '^TestWorkflow(Claim|RestartProcess)' -count=1 -timeout=240s
go test ./internal/httpapi ./internal/desktop -run Workflow -count=1 -timeout=120s
go vet ./internal/application ./internal/store ./internal/httpapi ./internal/desktop
```

The short recovery test passes in 31.060s; claim lifetime tests in 49.213s; store
fencing test in 0.460s; process-boundary/scheduled recovery group in 6.586s. HTTP
workflow tests pass in 7.574s and Desktop in 0.802s; `go vet` exits zero.
Existing workflow regressions pass in 129.057s for application and 5.328s for store.

Independent review of the short-claim patch found no runtime finding in the
renew/cancel/join path or the owner/checkpoint fences. The additional long-drain
regression passes in 45.898s, including 35.348s after cancellation. The full
claim-focused race run, including that new case, passes in 66.176s for application
and 5.480s for store. Production code is unchanged by this review follow-up.

Logs: [claim lifetime](claim-lifetime-output.txt), [process boundaries](test-output.txt),
[existing workflows](workflow-regression-output.txt), [host/API](host-api-regression-output.txt).
Independent review logs: [long drain](long-drain-review-output.txt),
[claim race](claim-race-review-output.txt).
The child-only entry point skips during
direct enumeration; the parent test actually executes it five times and checks
each exit code. Unknown attempts still fail as `worker_lost` without model replay.
