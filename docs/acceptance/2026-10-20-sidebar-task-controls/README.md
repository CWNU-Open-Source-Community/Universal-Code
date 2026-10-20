# Sidebar conversation controls — 2026-10-20

Conversation rows now offer rename and pin actions from their options menu. Pins
are saved in SQLite and listed independently of the ordinary history page. The
sidebar can show running conversations or conversations awaiting approval across
saved history.

Migration 218 adds the pin column and its creation-order index. Rename uses the
existing title field and its 4096-character input bound. Both metadata writes
require the control token. Execution filters reuse the existing live and durable
status projection.

The filtered endpoint scans at most 1000 saved candidates per request. When that
budget is reached, its continuation cursor advances past the scanned records,
including when the current page contains no matches. The sidebar retains the
load-more action. Changing a filter starts a new query; a cursor from another
filter is rejected.

## Browser exercise

Windows Chromium, 1280 × 900, production renderer and real Go HTTP API on an
isolated loopback port. The fixture uses its own SQLite database and local offline
provider. The API was restarted after replacing production assets.

- `before.png`: main `2bbdab93`, captured before changing product UI.
- `after.png`: the same conversation renamed and pinned above the project groups.
- `rename.png`: the actual rename dialog.
- `chat-menu.png`: pin, rename and archive actions, including keyboard focus.
- `awaiting-approval.png`: a saved approval-waiting conversation found beyond the
  first unfiltered page.
- `running.png`: an older pinned conversation selected by Running while its real
  request is held in the offline provider.

Rename and pin were exercised through the UI, then confirmed after restarting
the backend and reconnecting. Escape closes the options menu and returns focus
to its button. The selected Thread URL remained the same.

For pagination, the isolated fixture added 105 newer conversations. A real API
read returned 100 records and excluded both older target conversations. The
separate pinned list still showed the older pin. The approval fixture uses a
persisted Run transition to `waiting_approval`; the running fixture uses the
normal message-submit path and a blocked local provider response. No browser
responses or DOM content were replaced for these captures.

## Checks

Passed:

- Focused metadata, execution-filter, migration 218 and OpenAPI tests in
  `internal/httpapi` and `internal/store`.
- Clean-install baseline tests after regenerating the schema artifact.
- `web/src/v2/app.test.tsx`: 44 tests.
- Sidebar, chat actions and metadata client tests: 30 tests.
- `npm run typecheck`, `npm run build`, and `npm run check:api`.

CI follow-up: the migration history now includes v218. The unfiltered list keeps
its existing query-cache key, and search/recovery fixtures return a separate
empty pinned page instead of treating pinned requests as history pagination.
All 59 tests across task search, recovery navigation, sidebar and chat actions,
plus the schema-history check, passed after this correction.

The tests cover authorization and malformed writes, repeated absolute PUTs,
retained Mission/Session identity, pin persistence through upgrade and restart,
older matching records, empty scan pages with continuation, and filter-scoped
cursors. Native desktop composition was not exercised in this browser check.
