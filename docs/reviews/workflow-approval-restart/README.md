# Atomic file review before workflow continuation

Windows amd64, 2026-11-07. Base integration commit: `e59557ff`.

## Reproduction

The workflow reaches a real file proposal and waits for its `workspace_apply`
approval. The review store wrapper calls the production SQLite approval writer,
then cancels the request context immediately after its commit returns. The test
closes and reopens the same database and invokes the public workflow scheduler.
It does not modify stored statuses or timestamps.

Before the fix, `FileEditReviewService.Review` committed `DecideApproval` and
subsequently called the file manager to save the edit state in another
transaction. Request cancellation between them left an approved/denied approval
and a proposed edit. The scheduler observed the approval's new version and
continued the original tool call.

The approve case durably recorded that tool as failed:

```
FAILED_PRECONDITION: new FileEdit apply requires a running Run, active Session, and approved edit
```

The file remained absent. The deny case correctly withheld the write, but its
edit projection remained `proposed`. Both retained one original attempt and
charged only the next five-token model response, while losing the accepted
intent's application/projection.

The two original cases fail in 1.38s (application package 1.500s):
[before output](before.txt).

## Change

`CommitFileEditReview` rereads the immutable edit and its exact approval inside
one SQLite transaction. It checks the approval identity, Run/Session/workspace,
fingerprint and observed version, then commits the approval operation, decision
event, proposed-to-reviewed edit transition, and original review snapshot
together. It reuses the existing decision/event functions and the file manager's
extracted pure review validation. The original current-workspace checks remain
in `projectFileEditTx`.

Same-result replay returns the recorded review snapshot while preserving a
later applied file state. Reverse decisions and changed proposal/approval
observations conflict. Existing callers can still explicitly retry the same
review to complete an older approval-only commit; that compatibility uses the
same atomic entry point.

The regression intercepts both the old and new persistence methods and counts
successful commits. Its passing path requires exactly one new atomic commit
and a cancelled caller context, so removing the old hook cannot hide the fault.
The SQLite database, file writes, public application services and reopen are
real. The provider is deterministic; cancellation is injected at the committed
store boundary rather than through a TCP socket. Reopen retains the fixture's
permission runtime authority to isolate this transaction boundary. A fresh
process permission epoch is a separate investigation.

## Verification

The focused store tests inject a real SQLite trigger failure after the decision
write and verify rollback of the approval version, decision operation and all
Run events. They also exercise real file application followed by review replay,
reverse decisions, changed proposal/version/approval identity, and explicit
repair of a previously committed approval-only decision.

The six application race cases check approve and deny through ordinary reopen,
workflow pause followed by explicit resume, and Run cancellation. Paused work
keeps its original pending attempt and token usage until resume. Cancelled work
keeps two model calls and ten tokens and settles failed, with one interrupted
attempt and no file write. Ordinary/resumed work uses the original attempt and
three model calls/fifteen tokens.

```powershell
go test ./internal/store -run '^TestCommitFileEditReview' -count=1 -v -timeout=60s
go test -race ./internal/application -run '^(TestWorkflowReviewDecisionCommitDisconnectBeforeEditStateSurvivesReopen|TestFileEditReview)' -count=1 -v -timeout=90s
go test ./internal/application -run '^(TestFileEditReview|TestApprovalContinuation|TestWorkflowHostResumesReviewedFileIntentAfterDatabaseReopen|TestWorkflowMapApprovalCapacityAndSiblingProgressSurviveRestart|TestHostOwnedApproval)' -count=1 -timeout=180s
go test ./internal/fileedit -count=1 -timeout=60s
go test ./... -run '^$' -count=1 -timeout=60s
go vet ./internal/application ./internal/store ./internal/fileedit
```

Store checks pass in 1.964s. The initial two-case application group passes in
1.717s, and the pure manager approval test passes in 0.687s. Store/fileedit race
checks pass in 12.035s/1.575s. The six-case application race passes in 29.999s;
the full file manager package passes in 1.238s and vet exits zero.
All Go consumers compile successfully, including
HTTP, Desktop and CLI. Only the existing in-memory file review test store needs
an interface adapter.

A wider consumer run passes HTTP in 39.331s and CLI/App in 13.779s. Its
application package hits the overall 180-second timeout while initializing an
existing map-approval fixture. That timeout has not been reproduced on the base
revision and remains unattributed. The directly affected application group is
green in 42.164s and recorded separately.

After the shared build disks filled during linking, the affected application
race and consumer commands were rerun with process-local `GOCACHE`, `GOTMPDIR`,
`TEMP` and `TMP` under `C:/Users/22977/.codex/tmp/overnight-20261107/`.

The application race and consumer regression logs are recorded separately:
[application race](atomic-application-race.txt),
[consumer regressions](consumer-regressions.txt),
[focused application regressions](application-regressions.txt),
[all-consumer compilation](compile-all.txt).
