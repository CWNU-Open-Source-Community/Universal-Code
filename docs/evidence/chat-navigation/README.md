# Previous and next chat shortcuts

The default shortcuts are `Ctrl/⌘+Shift+[` and `Ctrl/⌘+Shift+]`. Both are listed in Settings → Shortcuts and can be changed or cleared. An existing custom binding takes priority over a newly introduced default.

Navigation follows the loaded chat rows rendered by the sidebar: pinned chats, expanded project groups, and local chats, with the current execution filter applied. It stops at either edge or when the selected chat is outside that list. The sidebar reports its rendered order directly; navigation does not repeat its grouping or sorting logic.

Explicit keyboard navigation uses the same accepted-navigation callback as a sidebar click. Cancelling the unsubmitted-file-edits dialog preserves the destination chat's unread marker. Drafts continue through the existing per-chat storage.

## Captures

All images are real Windows Chromium captures at 1280 × 900. The production renderer was served by `cmd/workbenchjourney`, using the production API with its offline provider and disposable projects. The replies shown came from actual `workspace_read` calls and persisted assistant messages. No API success responses or page content were substituted.

- Original baseline: `52c7da77fd83251b61d1696735a3d98f521ce37f`, built before editing. Bundle digest: `78d6b88f6c424bb8718cf4779e7a4fc6f3cc24cd7c861580b0471563a14ddea6`.
- Final captures: this change on `e790ec4b7856a5eafea4d2ea7d441f05ec677963`, including the unread navigation-acceptance fix and sidebar resizing. Bundle digest: `2d86a8dfd8717c392176ac2b92e7b39482f26816da2d69647f3e7c47572c94e4`.

| Image | Observed result |
| --- | --- |
| [Before, Chinese](before-shortcuts-zh.png) / [English](before-shortcuts-en.png) | Original six shortcuts; adjacent-chat actions are absent. |
| [After, Chinese](after-shortcuts-zh.png) / [English](after-shortcuts-en.png) | Previous/next chat, default keys and sidebar-order explanation are visible. |
| [Before navigation](before-navigation.png) | From B, `Ctrl+Shift+]` leaves B selected despite A being the next visible chat. The unsent draft remains. |
| [Next chat](after-next-a.png) | The same physical key opens A; A has its own rich-text draft. |
| [Previous chat](after-previous-b.png) | `Ctrl+Shift+[` returns to B and restores its unsent plain-text draft. |

## Verification

```text
npm test -- src/v2/app.test.tsx src/v2/components/sidebar.test.tsx src/v2/components/keyboard-settings.test.tsx src/v2/navigation-acceptance.test.ts
4 files, 85 tests passed

npm run build
Passed (existing large-chunk warning)

git diff --check
Passed
```

The browser journey verified both edges without wrapping, A/B round trips, plain/rich composer draft restoration, settings/search/menu protection, collapsed projects, execution filters, and navigation after expanding tool details. Pinning A changed the actual sidebar order to A → B; the shortcuts followed that order. A manually unread chat became read after an accepted keyboard switch.

Component tests additionally cover Ctrl/Meta physical `BracketLeft`/`BracketRight` events, remapping persistence, pre-existing custom bindings, IME/repeated keys, editors/terminals, popovers, new-chat/inspector contexts, and cancelling/accepting the actual file-leave confirmation provider. macOS was not tested on a native device; Meta bindings were covered in component tests.
