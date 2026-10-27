# ADR 0194: Complete line statistics and paged repository diff previews

- Status: Accepted
- Date: 2026-10-27
- Scope: `repository_diff.v1` (current source-project changes)
- Updates: the repository-diff limits in ADR 0048

## Problem

A one-character edit in a 65,537-byte file previously returned `size_limited`.
Two distant edits in a 100-line file could be represented as an 81-line replacement.
Line counts were calculated from the truncated patch, and changes after the first
50 files were not reachable from the review UI.

## Decision

Read complete regular files up to 4 MiB per side for repository review, independently
of the unchanged 64 KiB explorer preview. Reuse canonical relative-path validation,
reject linked paths, confine the open with `os.Root`, and check the opened file's
identity. Both sides are UTF-8 checked and secret-redacted before comparison.
Ordinary read failures and detected identity/size/mtime changes return `unavailable`,
without a patch or complete statistics. A path missing after a status scan is a
deletion only if the scan also reported it deleted; a reappearing deletion is
likewise unavailable until refreshed. These checks detect inconsistencies rather
than providing a filesystem snapshot.

Use a line-based Myers edit script with common-prefix/suffix trimming and a linear
disjoint-line-set fast path. Bound each side to 100,000 lines, search distance to
1,024 edits, and search work to 2,000,000 steps. On exhaustion return
`computation_limited`, an empty patch, and `statistics_complete=false`; do not
silently substitute a coarse replacement. Empty additions/deletions and completely
disjoint replacements do not require the bounded search.

Count edits before rendering. Emit separate unified hunks with three context lines
and proper missing-final-newline markers. Keep only 256 KiB of preview per file,
including its truncation marker. `statistics_complete=true` means the counts cover
the whole unredacted edit script, even when the preview is truncated. Redacted files
do not claim complete original-content counts; the UI labels them accordingly.
When redaction leaves both sides identical, return an empty patch and show
“Sensitive changes hidden”, rather than manufacturing hunk positions.

Return at most 50 items and 2 MiB of patch text per page. A cooperative three-second
page computation budget starts after repository status/HEAD inspection; it is not
a hard wall-clock guarantee for filesystem IO or redaction. When a later file runs
out of this page's time, leave it for the next page with a fresh budget. A single
file that cannot finish is explicitly computation-limited.

`after` / `next_after` use the last canonical relative path as a keyset cursor.
`omitted_count` counts files remaining after this page; pagination alone no longer
sets `truncated`. Status inspection still has its existing 10,000-entry safety cap,
but the separate 200-item repository-state preview does not cap diff pagination.
Pages read the live worktree, not an immutable snapshot: refresh from the first page
after concurrent edits/renames. The UI replaces pages, discards inactive patch caches,
supports back/next navigation, and resets the cursor when changing workspace.

This projection remains read-only and starts no process or Git hook. The existing
file-edit diff implementation and the task Git mutation-preview limits remain
unchanged; task Git receives explicit constants to avoid accidentally inheriting
the larger review limits.

## Verification

Tests cover minimal line edits against an independent small LCS oracle, round-trip
reconstruction, `git apply --check` for emitted hunks, a 2.15 MB sparse edit, complete
counts despite truncated output, 205-file pagination, aggregate-byte continuation,
computation limits, cancellation, redaction beyond the old 64 KiB boundary, and
canonical/linked-path rejection. HTTP/OpenAPI/client tests cover the new fields and
page parameter; UI tests cover navigation, failure recovery, workspace changes,
and complete versus unavailable statistics.
Deterministic filesystem tests also cover removal, rename, replacement and
reappearance between inspection and reading, insertion on either side of a page
cursor, deletion/rename between pages, and recovery by refreshing from page one.

See [real before/after captures and measurements](../acceptance/2026-10-27-repository-diff-scale/README.md).
