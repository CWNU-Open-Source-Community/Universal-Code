# Worker tools: OCR delegate review

Reviewed on 2026-10-18 against `de318deb8f52ebcf33d29f414d0f3e39ee94bd76`.
This slice adds on-demand Skills, scoped history recall, and the configured
language server's read tools to execution workers. Batch integration and resource
continuation are tracked separately in #374.

OCR selected files and resolved review rules; the host agent and delegated
reviewers read and assessed the implementation. No OCR model endpoint was used.
The final source tree was preserved by the application's startup snapshot
`b6f3ade888276865ceddbde2fd16b40fa7479045`; the recovered source matches that tree.

## Coverage

The source preview contained 46 paths: 36 reviewable and 10 selector exclusions.
Of the 36 reviewable paths, 32 were manually reviewed: 7 new files in full and
25 existing files by changed lines and relevant callers. Four generated files
were regenerated and their changes checked, but excluded from full manual source
review. Manual coverage of OCR's reviewable set is 88.89%.

The [per-file checklist](coverage.json) records every previewed path, review scope
and exclusion reason. The audit report and checklist themselves are not counted
as implementation review coverage. There are no outstanding confirmed findings
in this slice.

Review focused on current permission checks, child/attempt authority, immutable
parent-history admission scope, revalidation of history source visibility,
Skill installation changes during retries, exact Skill reads after compaction,
and preserving the existing read-only child profile.

## Verification

The integrated focused run passed in `internal/application`, `internal/store`,
`internal/toolgateway`, `internal/domain`, and `internal/httpapi`:

```text
go test -p 1 ./internal/application ./internal/store ./internal/toolgateway ./internal/domain ./internal/httpapi -run '^(TestWorkerSkill|TestWorkerHistory|TestWorkerCodeIntel|TestWorkerCatalogue|TestExecutionWorkerReads|TestExecutionWorkerNarrowed|TestHistoryRecall|TestCleanInstallBaseline|TestWorkerProfile|TestOpenAPI)' -count=1 -timeout 180s
```

This run exercises real files and SQLite through controlled model responses.
The optional real-gopls test skips when its binary environment variable is absent;
that skip is not counted as a real language-server acceptance result here.
The retained local log is `tools-integrated-tests.log` in this task's evidence
directory.

OpenAPI generation, clean-install baseline generation, frontend type checking,
and 16 focused WorkflowPanel/SpecialistExecution UI tests passed. The actual
WorkflowPanel component was viewed in Edge at 700 px and 390 px widths, with
static API responses: the parent-history option was readable and the 390 px
document had no horizontal overflow. This was a component-layout check, not a
full backend task journey or an autonomous real-model acceptance run.
