# Message and code reading sizes

The original conversation and baseline screenshots were captured on Windows Chrome using production frontend bundles and the same saved answer from the production Go API. The baseline bundle was built from `fc0935d6fc03ca7abccbd148ce52d26c2bc154f8`; the original image set is recorded in `8a4221c850158d8d460122def35f402c8862a50d`. The local `workbenchjourney` provider completed a normal turn through `root_lifecycle.v1`, and the normal backend persisted and projected the answer. The thread and run below identify those conversation captures.

- Thread: `thread-run-20261021210809-59fae05150ec`
- Run: `run-20261021210809-59fae05150ec`
- Saved answer marker: `TYPOGRAPHY_SAVED_PROOF_2026`
- Desktop viewport: 1280 × 900; narrow viewport: 480 × 600.
- Product reference: [Codex settings — Appearance](https://learn.chatgpt.com/docs/reference/settings), which exposes UI and code font sizes.

## Verified behavior

The Appearance page exposes native, labelled range controls in Chinese and English. Message size is 12–24 px and code size is 10–24 px, in 1 px steps. Defaults remain 14/13 px. Preferences are stored on this device and applied during appearance initialization.

The message setting changes user and assistant message text, including message tables. The code setting reuses `--prayu-code-size`, so it also reaches existing V2 source previews that use the markdown code component. Monaco and terminal font settings remain independently configured.

| Action | Observed result |
| --- | --- |
| Change message/code controls to 20/18 px using the keyboard | Saved answer updates immediately; composer and Copy code action remain 14 px. |
| Close Chrome and reopen its persistent profile | Appearance controls and saved answer retain 20/18 px. |
| Set both controls to 24 px at 480 × 600 | Message and code computed sizes are 24 px; document width is 480 px. The code block has a 438 px viewport and 674 px scrollable content. |
| Click Reset font sizes | Controls return to 14/13 px; reset becomes disabled; the saved answer returns to its original sizes. |
| Unit checks | 11 tests passed across typography preferences, typography settings, and existing appearance tests. Includes bounds, malformed storage, both locales, initialization, reset, remount, and failed persistence. |
| Production build | TypeScript check and Vite build passed. |
| OCR delegate review | 17 total files; all 6 selected source files reviewed (100% coverage), no remaining findings. The 11 tool exclusions were 2 tests (`default_path`), this README (`unsupported_ext`), and 8 screenshots (`binary`); tests, documentation, and screenshots were checked separately. |

## Settings screenshot refresh — 2026-10-23

`after-reopened-desktop.png` and `after-max-settings-narrow.png` were recaptured from `22d61cf0cac9bfbed184a809bd98d534ebace246`. They show the simplified reading controls: Reset font sizes is beside the heading, the explanatory rows are removed, and the preview uses the current greeting. The current production Vite bundle was served by the production Go API through the unmodified `workbenchjourney` harness, using a disposable database and an isolated persistent Chrome profile. Connection, slider changes, and reset used the application UI.

| Action | Observed result |
| --- | --- |
| Set 20/18 px with the keyboard, close Chrome, reopen the same profile, and reconnect | The 1280 × 900 Appearance page retains 20/18 px; the preview computed sizes are 20/18 px. |
| Set both sliders to their maximum with the keyboard at 480 × 600 | Both controls and preview computed sizes are 24 px; document width is 480 px and the heading-row reset button remains inside the viewport. |
| Click the heading-row Reset font sizes button | Both sliders return to 14/13 px and the reset button becomes disabled. |

The baseline images and saved-answer screenshots retain the original captures above.

## Screenshots

| State | Image |
| --- | --- |
| Baseline Appearance page | [before-settings-desktop.png](before-settings-desktop.png) |
| Baseline saved answer, 14/13 px | [before-chat-desktop.png](before-chat-desktop.png) |
| Saved answer, 20/18 px | [after-chat-desktop.png](after-chat-desktop.png) |
| Appearance after closing and reopening Chrome | [after-reopened-desktop.png](after-reopened-desktop.png) |
| Both controls at maximum, 480 × 600 | [after-max-settings-narrow.png](after-max-settings-narrow.png) |
| Message at maximum, 480 × 600 | [after-max-chat-narrow.png](after-max-chat-narrow.png) |
| Code at maximum, 480 × 600 | [after-max-code-narrow.png](after-max-code-narrow.png) |
| Saved answer after restoring defaults | [after-reset-desktop.png](after-reset-desktop.png) |
