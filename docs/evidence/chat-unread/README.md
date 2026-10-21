# Conversation unread markers

Captured on Windows in Chrome with the production `cmd/workbenchjourney` API,
SQLite store, offline model provider, and a built `web/dist`. Replies came from
real workspace-read executions. No browser API mocks or DOM edits were used.

- Before: commit `52c7da77fd83251b61d1696735a3d98f521ce37f`, UI digest
  `78d6b88f6c424bb8718cf4779e7a4fc6f3cc24cd7c861580b0471563a14ddea6`.
- After: this change, UI digest
  `ee1de139b321c223b4c6d8e17ecb6d5cb80a11b39b3bf516516b76d57f9ec07b`.
- Desktop viewport: 1440 × 1000. Narrow viewport: 390 × 844.

| Image | Observed behavior |
| --- | --- |
| [Before](before-unread.png) | A background conversation has a saved reply but no unread marker. |
| [Background reply](after-background-reply.png) | A new saved reply in the other conversation adds a dot before its title. The existing execution-state dot stays at the right. |
| [Saved-message history](after-history-retains-unread.png) | Opening an earlier saved message retains the unread marker. Returning to latest then clears it. |
| [Manual unread](after-manual-unread.png) | Marking the current conversation unread keeps the dot and offers “Mark as read” in its menu. |
| [Narrow sidebar](after-narrow-sidebar.png) | A new reply in the current conversation stays unread while the sidebar covers its content. Opening that conversation clears it after the latest view is available. |

The two background replies had identical text but distinct saved message IDs:
`evt-20261021191411-57d288cf6c25` and
`evt-20261021191519-4bf5a1b6b80c`. The second reply still produced an unread marker.
Manual unread also survived page reload and reconnect; clicking the current
sidebar row again cleared it. Explicit “Mark as read” cleared the marker.
Both production pages reported zero browser console errors and warnings.

Focused React checks passed for the unread store, reading confirmation,
transcript refresh, sidebar, conversation menu, app integration, and conversation
behavior (173 tests, followed by all 7 unread-store tests after adding the
malformed-record case). `npm run typecheck`, `npm run build`, `npm run check:api`,
`git diff --check`, and the protocol registry check against the backend base
passed. The build retained the existing large-chunk warning.

Tests cover failed fresh-head reads, replies arriving during a read, old pending
identities outside the latest window, empty feed continuation pages, persistence
failure, and data-store changes. Focus/visibility cancellation is covered by
these tests. Headless Chrome kept both tabs focused and visible, so tab switching
was not used as evidence for actual window-focus changes.
