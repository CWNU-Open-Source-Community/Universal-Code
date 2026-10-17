# Context compaction and fixed requirements acceptance

Issue: [#366](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/366).
Base: `ec5e772aed7300cac52a1fbf1adbc645eb8b567f`. Local verification: Windows, 2026-10-17.

## Delivered behavior

- Recent committed user originals remain available independently of a lossy generated summary, within a bounded budget. The latest complete messages are fitted after mandatory context; older originals remain readable from stored history.
- Users can add, edit and remove Thread-scoped fixed requirements. The model can propose one, then must wait for the user to select pin, ordinary discussion or a custom reply. Custom replies are pinned only with the explicit checkbox.
- Required pins reach the Supervisor and scoped child requests even after optional history and large completed tool results are reduced. Impossible mandatory context fails before another provider call.
- The idle Thread UI and `thread compact` CLI generate and atomically install a summary. Original history, actual usage and source identity remain recorded. Summary input already over the window is reduced with an explicit omitted-message count.
- Children can page their own completed tool result after receipt projection, with lease, attempt, source hash and permission checks. A provider context rejection allows one strictly smaller retry without discarding global usage.
- Kimi native reasoning history survives the old message-count threshold; genuine overflow has a context-window failure classification.

## Verification performed

Application regressions cover recent original retention, confirmed pins, all proposal choices, fourth-tool-round wait enforcement, manual compression and Kimi native-history paths. The final combined application run passed in 22.407s. The pending-input case verifies that compaction is rejected without dispatching a summary call even when no execution lease is active.

The child regression executes a real 30 KB workspace read, compresses its tool receipt, and checks the next outbound request and audit. It covers both Specialist and a Worker restricted to `workspace_read`; optional history is also forced over budget. Separate tests cover scoped paging, revoked permission, DB reopen, approval resume, one smaller retry and mandatory-pin overflow.

Passed local commands include:

```text
go test ./internal/application -run 'Test(ThreadContextPin|ManualThreadCompact|RecentOperator|SupervisorKimi|SpecialistThreadPins)' -count=1
go test ./internal/application -run TestManualThreadCompactPendingInputWithoutActiveLeaseIsBusy -count=1
go test ./internal/application -run '^TestSpecialistThreadPins|^TestSpecialistReceiptCapacity|^TestExecutionWorkerReadsWritesAndResumesExactFileApproval$' -count=1
go test ./internal/httpapi ./internal/app ./internal/store ./internal/toolgateway ./internal/contextmgr -run 'ThreadContext|ThreadCompact|OpenAPI|CleanInstallBaselineMatches|RecentOperatorContext|SpecialistResult|Generated' -count=1
go vet ./internal/application ./internal/store ./internal/domain ./internal/httpapi ./internal/desktop ./internal/contextmgr ./internal/toolgateway ./internal/app
go run ./cmd/protocolregistry -check -baseline ec5e772aed7300cac52a1fbf1adbc645eb8b567f
go run ./cmd/surfacecheck
go test ./internal/releasegate ./internal/producte2e -count=1
npm run build
npm test -- src/v2/components/thread-context-controls.test.tsx src/v2/components/thread-context.test.tsx src/v2/components/conversation-recovery.test.tsx
```

The three Web files passed 43 tests. After the modal layout correction, its nine interaction tests and the production Web build passed again. OpenAPI, TypeScript DTOs, the schema v206 clean-install baseline and protocol inventory were regenerated; representative old-schema upgrades match the baseline.

The production Web bundle was operated through a browser against the real Go API, disposable SQLite/workspace and an offline provider. Observed: automatic proposal dialog; custom reply without a pin; direct add/edit; manual compaction with a focus; reload preserving pin and summary; successful subsequent conversation; custom reply with an explicit pin. The latest modal was inspected at the ambient narrow viewport, at 1280 × 720, and at 390 × 560. At the last size it measured 358 × 528 at (16,16), with no horizontal overflow; only its body scrolled and the footer remained visible. The earlier global-dialog width and missing-padding defect was corrected.

## OCR delegate review

OCR performed deterministic file selection and rule resolution. Independent agents reviewed the Worker, pins, manual-compaction and UI/recent-original/Kimi changes; the integrating agent reviewed shared-file resolutions, the browser fixture, final layout and documentation. New source/test files were read fully; existing files were reviewed as diffs with relevant callers. This is scoped patch review, not a whole-repository audit.

The final runtime snapshot contains 84 changed files: 79 reviewed, 5 generated files skipped with generator/check evidence. OCR selected 62 of those files, of which 58 were reviewed and 4 were generated; its default exclusions include the additionally reviewed tests and documentation. This acceptance document is one further reviewed documentation file. The five skipped artifacts are `docs/openapi.json`, `web/src/api/schema.d.ts`, `internal/store/clean_install_baseline.sql`, `internal/store/clean_install_baseline_generated.go`, and `docs/convergence/protocol-registry.md`.

Review corrections: keep the latest fitting raw user message; refresh known CAS conflicts while retaining unknown-response identities; let a remotely resolved dialog close; enforce `wait` at the fourth tool round; reject pending user input during manual compaction. Corrected paths and regressions were reviewed again. The responsive layout was verified visually after the user's report.

## Operational boundaries

This run used controlled local providers to inspect actual requests, state changes and UI behavior; it does not measure a hosted model's adherence rate. A process exit with an unconfirmed prepared/started manual summary operation retains that pending operation and refuses to resend it automatically. There is currently no operator reconciliation UI for clearing that slot; ordinary Thread continuation remains available, subject to the retained usage/budget. Kimi's required native replay remains intact and cannot always be compacted into a generic summary.
