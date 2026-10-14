# Web access configuration acceptance — 2026-10-14

Tracks [Issue #313](https://github.com/CWNU-Open-Source-Community/Universal-Code/issues/313)
and [ADR 0175](../adr/0175-explicit-web-access-modes.md).

## Actual browser verification

Windows Edge 155.0.4283.45 loaded the production Vite bundle from the real Go API.
The disposable SQLite database and project were created using the production
Store and RunService. The visible conversation is labelled as a UI acceptance
sample; the isolated server uses the mock model configuration.

Verified through normal page controls:

- Settings → 配置 presents 禁止搜索, 规则搜索 and 完全访问 as selectable values.
  缓存搜索 and 索引搜索 are disabled and labelled 待支持.
- Changing the new-conversation default to 完全访问 survives a full page reload
  and reconnect. The existing task keeps its separate 规则搜索 selection.
- Changing that task from 规则搜索 to 完全访问, then 禁止搜索, receives real HTTP
  202 responses with mode revisions 2 and 3. Both responses retain command
  `network_mode: disabled`. Reopening its panel reads the saved 禁止搜索 value.
- 禁止搜索 hides the live connection check and domain editor. Previously saved
  webpage reading remains covered by executor regression tests.
- The new conversation composer displays the stored 完全访问 default. Sending
  from this isolated server opens model setup, as no qualified external model is
  configured. Creation payloads and returned scope binding are covered by the
  application/HTTP and frontend integration tests.
- Light and dark configuration pages retain the neutral palette. The 390 × 844
  layout has no horizontal document overflow. Browser console reported no errors.

## Screenshot artifacts

The original, unedited browser screenshots are retained under the worktree's
ignored `output/playwright/` directory. They were captured with Playwright's
`page.screenshot`; the screenshots are supplied directly in the task reply.

| Image | Content | SHA-256 |
| --- | --- | --- |
| `08-configuration-menu.png` | All five menu labels | `b2072687555255b0b7b73f097158a646a1fb90c7341008b9c0787db14a56f438` |
| `05-current-disabled.png` | Persisted current-task selection | `3481451672614a1b353f54af47376f4644914ab698a05103b6ae39485029bd00` |
| `06-configuration-dark.png` | Dark theme | `983cdd29d0a2733aaba9dbee0ad2dc14a7c6135854cb5922a0d740d075f03f58` |
| `07-configuration-narrow.png` | Narrow layout | `73ff9a96209905f075809ea575f947d7c597fff9ce50363c607458348da1eafb` |

The same local directory contains the capture scripts, sanitized mode-change
receipts, the full screenshot hash manifest and test logs. Temporary connection
credentials and the disposable database stay outside version control.

## Automated verification

- Frontend full suite: 202 files, 2,012 tests passed. The subsequent busy-search
  diagnostic message has an additional focused regression test.
- TypeScript checking and production frontend build passed.
- Generated API type checks, protocol and surface registry checks passed.
- `go vet -p 4 ./...` and `go mod verify` passed. Focused race tests passed for
  web-access transitions, search diagnostics, grant revocation and migration 192.
- Focused backend tests exercise full public fetch without domain approval,
  disabled search/fetch, saved snapshot reading, exact-host rules, public URL
  transport restrictions, stale mode rejection, replay/reopen, inheritance,
  diagnostics cancellation and schema migration.
- Broad checks exposed missing schema-history documentation and two fixtures
  requiring updates: the live-route catalog needed a valid web-access request,
  and the v166 historical fixture needed an empty current selection ledger while
  invoking current application code. All three cases passed after correction,
  along with migration, grant revocation and approval-resumption regressions.

The broad local Windows run also reports machine-specific native-test failures:
the installed fixed Go executable is 1.26.5, Git is installed under `D:\Git`
rather than the fixed adapter's supported installation directories, and the
attachment shell test requires a configured PowerShell 7. Deep repository-local
temporary paths produce Git's `$GIT_DIR too big` error; the application package
reached its 20-minute timeout. Moving the fixed-command test to an external
temporary directory allowed its Go execution and replay to pass, leaving its
fixed Git prerequisite. Logs retain these failures; the PR CI runs use their
configured build hosts.

Public-network provider availability and account-specific search behavior remain
dependent on the configured provider; the browser capture validates the real
settings/control workflow against the isolated local service.
