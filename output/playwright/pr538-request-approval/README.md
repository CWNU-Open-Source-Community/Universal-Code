# #538 page-request approval UI

These captures render production `V2ApprovalCards`, `V2Composer`, and their production styles in Edge. A local API-client fixture supplies the queue and preview; no browser request, backend approval, or approval decision is executed by this harness.

The **before** fixture has an empty approval queue, representing the original code path that did not create a page-request proposal. The **after** fixture adds one `browser-network-*` proposal with the preview fields proposed by #538. Both use the same approval region, composer, theme, and window size. An older browser-tool approval is not used as the before state.

The production component revision is `3620b5f521e5dd2a27bd444af3727b1668c79e77`, based on #538 revision `0d57dd73d9c31aca4a8331ab6988df17b1c08bda`. The only frontend code change adds existing-layout labels for request body size, request body SHA-256, and expiry in Chinese and English. The mock preview is derived from the concurrent backend implementation in `internal/httpapi/approval_preview.go`, not from an executed API response.

## Review images

- `before-desktop-viewport.png` → `after-desktop-viewport.png`: same 1024×720 light-theme window.
- `before-narrow-viewport.png` → `after-narrow-viewport.png`: same 390×800 light-theme window.
- `after-desktop-card.png` / `after-narrow-card.png`: unscaled crops of the full approval card (738px / 358px wide).
- `after-desktop-english-*`: supplementary English-label verification.

Full-window images are unaltered, and card crops retain native size; device scale is 1. The blank approval area in the before image is deliberate: the queue contains no approval for this request.

## Fixture

Tool: `agent_browser_sensitive`; action class: `browser_external_write`; mode: `per_call`; effect: `browser_sensitive_action`. Allowed actions: `approve_once`, `deny`.

| Field | Value |
| --- | --- |
| operation | `POST` |
| url | `http://127.0.0.1:51234/write` |
| body_bytes | `19` |
| body_sha256 | `91d28b1e660fca8e5a54e5f00e2756024be094bc718ce7fd660d1415f1a3922c8` |
| expires_at | `2026-11-06T14:32:00Z` |

`metrics.json` records all five layouts and exact displayed values. At 390px, the 64-character digest wraps into two lines with no horizontal scrolling. Both decision buttons fit within the viewport, and the card does not overlap the composer.

## Validation and reproduction

Windows, Edge 156.0.0.0, Node 24.22.1. The 22 existing tests in `src/v2/components/approval-cards.test.tsx` passed, and `git diff --check` passed.

Copy the HTML/TSX files into `web/.tmp/`, start Vite from `web` at loopback port 5381, and open the fixture using a new Playwright CLI session from this repository. Run `capture-approvals.js` through `playwright-cli run-code --filename`. The script checks the localized labels, all preview values, wrapping, button bounds, and composer placement before saving each capture. It does not click an approval control.
