# Usage CSV Export — Acceptance Record (2026-11-01)

## Scope

Settings → Usage adds an "Export CSV" action next to the refresh button. The export
mirrors the currently loaded `GET /api/v1/usage` projection (totals, per-day, per-model,
and recent-session rows plus the global cost ledger) into one BOM-prefixed UTF-8 CSV.
No API, protocol, or Surface changes; the export is frontend-only over data the page
already loads.

## Before / After

Left: `main` baseline. Right: this change. Both sides ran the same real Go API
(`workbenchjourney`), the production web bundle, and headless Chromium on
Windows; the usage numbers come from one real journey conversation settled
through the bundled offline provider (64 tokens / 2 model calls in both runs).

| Before | After |
| --- | --- |
| ![Before: refresh button only](before-usage.png) | ![After: export button next to refresh](after-usage-export.png) |

The only visible difference is the export (download) button beside the refresh
button in the period controls. Disabled states (no data, empty period) are covered
by unit tests instead of screenshots.

## Environment

- Windows 11, Go 1.26 (workbenchjourney harness), Node 24, production `vite build`.
- Headless Chromium 1228 via Playwright, 1280×1000, zh-CN locale.
- Usage ledger content is the journey provider's real settlement (input 32 /
  output 32, no cache details, unpriced cost), identical on both sides.

**Local-harness note:** capturing these screenshots required a temporary local-only
tweak to `workbenchjourney`'s offline provider: the composer now ships underscores
Markdown-escaped, so the journey trigger `JOURNEY_READ` reaches the provider as
`JOURNEY\_READ` and the bundled journey state machine no longer recognizes it on
`main` (the e2e journey fails the same way without the tweak). The temporary tweak
is not part of this PR; the recorded usage, the pages, and the export behavior are
real runs.

## Validation

- `npm run typecheck` — pass.
- `npm test` — 2877/2877 pass (includes 4 new export cases: full CSV layout by
  column index, RFC4180 escaping of comma/quote/CRLF titles, disabled without
  loaded data, disabled on an empty period).
- `npm run build` — pass.
- Manual: real download produced `universal-code-usage-30d-<start>_<end>.csv`
  opening cleanly in a spreadsheet with 20 aligned columns.
