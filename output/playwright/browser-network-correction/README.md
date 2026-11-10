# Browser UI correction and CI repairs

Date: 2026-11-07 (Asia/Hong_Kong). PR #546 remains open for frontend review.

The conversation no longer mounts the Agent browser card or network settings.
Network choices live in Settings → Permissions → Browser. The existing App
preview exposes an explicit “查看浏览器” action. Closing that view stops its
polling/stream; “停止浏览器” separately stops the owned browser process.

## Screenshots

All images are real production Vite UI captures against the production HTTP
application on Windows, automated in Microsoft Edge. The journey replaces only
the model provider with the repository's controlled offline provider. Images
are full viewport captures, without compositing, cropping or resizing.

| File | Viewport / theme | Content |
| --- | --- | --- |
| `before-chat.png` | 1280 × 900 / light | Earlier PR UI with permanent browser card and network control. |
| `after-chat.png` | 1280 × 900 / light | Ordinary chat without that card or control. |
| `before-settings.png`, `after-settings.png` | 1280 × 900 / light | The retained settings location, internet enabled under Ask. |
| `after-preview.png` | 1280 × 900 / light | Browser image opened explicitly in App preview. |
| `after-preview-dark.png` | 1280 × 900 / dark | The same explicit view in system dark mode. |
| `after-preview-narrow.png` | 390 × 844 / light | Existing narrow App preview layout; document width is 390 px. |

Before images are copied from `../browser-network/after-chat.png` and
`../browser-network/after-permissions-enabled.png`. Their backend was `0b33648d`
plus journey wording committed in `1fd9cd88`; their frontend matches the original
PR head `c17ffaa2`. See that directory's README for the original capture record.

After chat/settings/preview images and `journey-result.json` came from product
`8ac3e78d` with the original-image-viewer assertion subsequently committed as
`ce455959`. That journey passed (1 test, 12.6 seconds including setup). Dark and
narrow captures used product `9938f0d2`, whose frontend is identical to `8ac3e78d`.
The real browser operated by the Agent was installed Google Chrome. The native
journey verified screenshot image decoding, then process/profile cleanup after
“停止浏览器”. It did not use a real model provider.

## Fixes and verification

- UI `f3b11075` (integrated as `778a088c`): typecheck and five focused component
  test files passed (69 tests). They cover settings, ordinary chat, explicit
  opening/closing, polling lifetime and browser process ownership.
- Frame-display follow-up `06c2ba37` (integrated as `8ac3e78d`): typecheck and
  agent-browser/application-preview tests passed (44 tests); production build
  passed. A live canvas is shown only after a frame actually decodes and draws.
- Store `a2cb6e31` (integrated as `847b2af0`): all 12 tests reported failing by
  the original eight CI shards passed locally. Historical fixtures now use the
  existing historical browser-preference adapter instead of querying the v223
  table before it exists. Migration SQL and test assertions are unchanged.
  Related historical-fixture/browser-preference/v223 tests also passed.
- Native/CLI `365a9a5b` (integrated as `d604050f`): CLI entrypoint, wake,
  approval-continuation, API and Desktop wiring checks passed. Ask/Auto Internet
  no longer require the unrelated Full process flag. The real HTTP product test
  passed with Ask, explicit Internet, separate paused POST approval, exactly one
  fixture write, actual image decoding and native cleanup.
- Coexisting managers `b6d4555d` (integrated as `9938f0d2`): starting an unrelated
  CLI no longer invalidates a live owner's paused browser request. A native
  browser reproduction and a deterministic two-SQLite-handle regression passed;
  expiry/restart and CLI/API/Desktop checks passed. The coordinator repeated the
  deterministic regression on the integrated checkout (0.818 seconds, exit 0).
  Desktop/API retain their default startup recovery.
- Desktop recovery fixture `38be0ea2`: the final original Go job exposed one
  manually built approval missing its Run's network mode/revision. Adding those
  two fields preserved all recovery assertions. The failing test was reproduced,
  then passed with adjacent Desktop startup and Store expiry/restart tests. The
  coordinator repeated the repaired test after integration (0.549 seconds).
- `go vet ./internal/app ./internal/httpapi ./internal/desktop` passed before the
  constructor-option follow-up; `go vet ./internal/app ./internal/application`
  also passed on the integrated `d8007234` tree after that follow-up.

The local broader Windows command test has an unrelated Git installation-path
failure (Git is installed on D:, outside its approved candidates). Local Sandbox
host ACL preparation also failed. Neither result is reported as a passing full
Windows gate. Native macOS and full platform CI results belong to the PR's GitHub
checks, not these Windows checks.

## Review scope

OCR Delegate selected the `c17ffaa2..9938f0d2` delta and supplied rules. The primary
agent reviewed every changed source/test line with its affected call paths. Full
files read include `internal/app/run_runtime.go`, the browser view and its CSS,
the historical controlled-run seed helper, and `web/e2e/agent-browser.spec.ts`.
The remaining files were reviewed as diffs with adjacent implementation/callers;
this is not full-file coverage of every file. The application constructor and
its CLI/Desktop/API callers were read to reproduce and fix cross-manager
approval invalidation. Tests excluded by OCR's default selection were reviewed
manually. No generated source changed in this correction.
The later Desktop recovery fixture correction was read in full separately.

The original PR's broader implementation review and external-internet check
remain recorded in `../browser-network/README.md`. Its old permanent-card
screenshots are historical and are superseded by this directory.
