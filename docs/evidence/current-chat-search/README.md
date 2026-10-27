# Current chat search verification

Captured on Windows with Chrome against the production Go API and `web/dist`,
using `cmd/workbenchjourney` with a disposable workspace and its offline provider.
The two sample chats have different titles and bodies containing `请求超时`.
Screenshots use the real search endpoint and saved message navigation.

| Screenshot | Build and observed behavior |
| --- | --- |
| [Before](before-global-search.png) | Base `96ba29bf`, 1440 × 1000. Global search shows both chats' titles and messages: four matches. |
| [Current chat](after-current-search.png) | This change, 1440 × 1000. Current scope shows one matching body; selecting it opens that message's history window. |
| [Narrow window](after-current-search-narrow.png) | This change, 390 × 844. Ctrl+F reopens the hidden sidebar, focuses the retained query and keeps the scope controls within the viewport. |

The baseline UI bundle digest was
`beedd22bc9c451fb4d4a60d914275544d0fbb4a6b134b5eb52bac1b9ff9bc1f4`;
the final bundle digest was
`78d6b88f6c424bb8718cf4779e7a4fc6f3cc24cd7c861580b0471563a14ddea6`.
Ctrl+K returned to all chats and four results. Ctrl+F selected the current chat.
The narrow viewport had no horizontal document overflow.

Focused checks passed:

- Go search, full-message lookup and OpenAPI tests in `internal/httpapi` and
  `internal/store`, including a real branch with an inherited long assistant reply,
  cross-Run compacted history, scoped cursors, lifecycle filters and redaction.
- The affected project-move tests in `internal/application`.
- React search, sidebar, keyboard settings and app tests: 93 tests. The final
  drawer-focus adjustment also passed the 19 search tests.
- `npm run check:api`, `npm run build`, `git diff --check`, and
  `go run ./cmd/protocolregistry -check -baseline 96ba29bff4ae2a6b92e563fe0e5e5dbf93a2840d`.
