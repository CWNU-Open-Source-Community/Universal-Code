# PR follow-up live acceptance

The opt-in checks exercise authenticated GitHub reads and an isolated PR repair
with the existing configured model. Ordinary `go test` runs skip both live tests.
They require `gh`, Git, Go, an active GitHub login with repository/workflow access,
and an already configured DeepSeek provider in the local model registry and OS
credential store. The checks do not start a model login or save credentials.

For authenticated pagination without mutations:

```powershell
$env:TRAVERSE_GITHUB_LIVE = '1'
$env:TRAVERSE_GITHUB_LIVE_REPORT = "$PWD/output/github-live/pagination.json"
go test ./internal/githubreview -run '^TestLiveFollowupAuthenticatedPagination$' -count=1 -v
```

This reads `golang/go#51679` twice through the production HTTPS client. It requires
more than one real page of issue comments, complete resource pagination, and a
stable snapshot fingerprint. It makes no model requests and writes no GitHub
content. A concurrent change causes the check to fail so the operator can inspect
the current state.

For a repair, run each phase explicitly:

```powershell
pwsh -File scripts/github-followup-live.ps1 -Action Prepare -EvidenceRoot output/github-live
pwsh -File scripts/github-followup-live.ps1 -Action Run -EvidenceRoot output/github-live
pwsh -File scripts/github-followup-live.ps1 -Action Close -EvidenceRoot output/github-live
```

`Prepare` creates isolated base/head branches containing a Go microproject and one
push workflow restricted to the acceptance head branch pattern. The draft PR
targets that isolated base. Its failing `Sum` implementation is intentional. The
test file and workflow remain fixed acceptance oracles. Each run creates only a
single small CI job per push; it does not use the main project's CI configuration.
The command prints the PR URL and saves its identity in `fixture.json`. If a
preparation step fails after a remote write, inspect the existing branches/PR
before retrying.

`Run` reads the existing model settings database with SQLite `mode=ro` and loads
credentials through the OS credential store. Defaults are
`~/.cyberagent-workbench/cyberagent.db` and `deepseek/deepseek-v4-flash`;
`TRAVERSE_LIVE_MODEL_SETTINGS` and `TRAVERSE_LIVE_MODEL_ROUTE` select existing
alternatives. A missing model qualification runs the ordinary qualification
routine and records its calls separately from the repair.

The acceptance uses the production GitHub client, PR follow-up worker, Thread
executor, FileEdit boundary, project hook trust, and command runtime. It reads the
real failing workflow, allows the exact observed signed-log host, and submits the
feedback to the model. The model must edit only `sum.go`. Trusted format/test hooks
run real local processes. The harness verifies their receipts and the unchanged
oracle files, runs `go test ./...`, then commits and pushes the repair. A fresh
GitHub read must confirm successful CI for the new head before readiness passes.
Another unchanged poll must make zero additional model calls.

The repair uses a four-turn, sixteen-tool, 300,000-token application budget.
Token accounting includes each repeated input and the host hook continuations.
The harness checks successful `format` and `test` receipts with project-hook
origin, completed process receipts, and an applied FileEdit for `sum.go` before
allowing its commit/push.

Evidence includes `repair.json`, preserved prior reports, and a new local runtime
database per attempt. Reports contain commit IDs, workflow IDs, model/command
counts, and completion state. They omit credentials, signed log URLs, private
model reasoning, and remote comment bodies. A failed attempt retains its evidence
and workspace; inspect those before another run. `Close` closes the disposable PR
without adding a comment or merging. The isolated branches remain available for
verification.

Authentication rejection, rate limiting, missing pages and identity drift are
covered by deterministic protocol tests in `internal/githubreview/followup_test.go`.
Those tests use a loopback server and must be described as simulated boundaries.
The live checks never deliberately exhaust GitHub rate limits.

## Recorded execution: 2026-10-18

The [saved evidence](evidence/github-followup-20261018/) records a completed live
acceptance on Windows with `deepseek/deepseek-v4-flash` and the existing OS-stored
credentials. Authenticated production reads collected 384 issue comments across
four pages from `golang/go#51679`; the second read had the same fingerprint.

The isolated [draft PR #375](https://github.com/CWNU-Open-Source-Community/Universal-Code/pull/375)
started at `690ba4094f03e3501eae208e19ca082b2b2fa6db` with failing
[workflow 39091821353](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/runs/39091821353).
The successful attempt made eight repair model requests, plus two separate model
qualification requests, and produced one applied FileEdit. The host verified both
project-hook slots and three completed command jobs with exit code zero and the
process tree reaped. The fixed oracle files and project hook config were unchanged.
The host committed and pushed `8edaf094f2c461b595e16860afe966be67cccca4`;
production reads confirmed its
[workflow 39094121778](https://github.com/CWNU-Open-Source-Community/Universal-Code/actions/runs/39094121778)
succeeded. Readiness and a resumed unchanged poll added zero model requests.
The PR was closed without merging at `2026-10-18T03:35:54Z`.

`attempts.json` preserves the earlier harness failures: an undersized operation
key, an unshared process-local Full Access authority, and the original 100,000-token
budget. Their raw reports and runtime databases remain in the local output
directory. These failures were corrected in the harness; this acceptance added
no product-code changes. `repair.json`, `repair.log`, `pagination.json` and
`closure.json` contain the final observations. Runtime databases are local only.
