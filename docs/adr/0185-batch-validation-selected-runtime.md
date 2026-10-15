# ADR 0185: Batch validation through the selected command runtime

Status: accepted

## Behavior and target

Batch `go_test` and `npm_test` validations use the same installed adapter selector
and Run-owned Command Runtime Job manager as ordinary project commands. Local
uses its current LPAC or explicitly enabled host route; Docker Engine and SBX
use their fixed guest toolchains. Missing readiness or a missing adapter is an
error. The validation declaration cannot supply an executable, argv, backend,
mount, image, template, environment, network grant, or credential grant.

Batch worktrees are separate from the Run Drydock. A private native invocation
binds the stored plan/child generation or merge queue, repository, branch, head,
root identity and operation key. The existing adapter keeps its Run/Drydock
ownership checks before accepting this additional target. Docker binds the
target into the fixed command's purpose digest, uses a dedicated requester
marker, and refuses new dispatch without the live private target proof. Its
terminal validation receipt does not checkpoint the unrelated Run Drydock.

Reusing a different cwd without this binding, temporarily changing the Run
Drydock, and issuing another Run with copied Full authority were rejected.

## Authority, output and recovery

The explicit Batch validation startup switch, current modern Full permission,
live runtime generation and native `native-batch-validator` authorizer remain
required. Jobs additionally pin the current Run lease and execution profile.
An idle Run obtains and renews a lease through the existing lease service for
the foreground validation. A native specialist may borrow only its already
validated ToolCall parent lease ID/generation; an expired parent is rejected.
Other active owners cause a conflict before dispatch, so concurrent HTTP
validation cannot borrow an operation that may release its lease first.
Dispatch rechecks the exact worktree; Job ownership renewal checks permission,
Run state, profile, Batch generation/lease and Run lease. Loss of authority
terminates the owned Job through the existing cancellation path.

The same native operation key reads a terminal Job or refuses an uncertain
result; it never restarts an interrupted execution. Author verification,
submission, independent review and integration validation have distinct keys.
Active Job reconciliation uses the same process-local target proof while
retaining ordinary Run/profile/permission/fence checks. Cold recovery cannot
recreate that proof or execution grant. Docker's immutable requester stores a
bounded ledger reference, generation and directory identity for cleanup-only
mount reconstruction. Full request fingerprints, labels and ownership checks
still apply; create/start requires live proof. Backend cleanup precedes Batch
startup worktree removal. Docker recovery does not turn a Batch terminal record
into an unrelated Drydock checkpoint.

Cancellation first persists the existing `aborted` plan state. A short native
lock serializes this fence with Job publication. It then stops/waits for owned
Jobs before deleting their mounts; durable active Job/root facts and Run leases
reject deletion when another process still owns execution. Unconfirmed Jobs
retain their local cleanup proof. An aborted plan with unfinished cleanup can
be cancelled again, including through the UI; startup cannot resume its work.
Public Standard Code cancellation rejects Batch admissions before side effects,
and its finalizer rejects them before any parent Drydock checkpoint.

Digest-only Jobs retain byte counts and full-stream hashes without retaining
stdout/stderr bodies or output frames. Test receipts add optional actual
backend identity/generation, isolation/network/credential policy, readiness
observation, workspace digest, Job, image/template and process cleanup evidence.
Historical receipts remain readable. A non-diff specialist receipt without
execution evidence or with another current adapter is not reused.

## Verification scope

Focused tests use the production selector, authorizer, Job manager and SQLite
records with controlled Docker/SBX transports, plus native Local host checks.
They cover the recorded child target, no host fallback, offline fixed commands,
independent review, old receipt reuse, authority loss and output-body omission.
These tests do not constitute a new live Docker, LPAC or SBX acceptance record.
Advanced Git backend expansion remains tracked separately within issue #335.
