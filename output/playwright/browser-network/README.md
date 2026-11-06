# Browser internet access: UI and verification

Date: 2026-11-06. Platform: Windows 10.0.26200; UI automation: Microsoft Edge.

## Screenshots

The screenshots show the production Vite frontend and production HTTP application.
Only the model provider is replaced with the repository's controlled offline journey provider.
No screenshot was composited, stretched or resized.

| Files | Viewport / theme | Content |
| --- | --- | --- |
| `before-card.png`, `after-card.png` | 1280 × 900 / light | Unscaled element crops of the conversation's Agent browser card. |
| `before-permissions.png`, `after-permissions-enabled.png` | 1280 × 900 / light | Settings before, then internet enabled while execution mode remains Ask. |
| `after-advanced.png` | 1280 × 900 / light | Optional site restriction saved through the real API. |
| `after-narrow-advanced.png` | 390 × 844 / light | Optional site editor; document width measured 390 px, with no horizontal overflow. |
| `after-dark-advanced.png` | 1280 × 900 / dark | Saved site restriction reopened in the dark theme. |

Before: main `cfeee05a8724a23c673b0ed9971f2ef6e8de9674`.
After screenshots: backend `0b33648d` plus the journey setup wording now committed
in `1fd9cd88`; frontend tree is unchanged between that capture and `1fd9cd88`.
Later backend fixes cover terminal continuation and pending-request cleanup and
were tested separately. The settings fixture has no desktop sandbox service;
its `/api/v1/sandbox/environment` 404 is unrelated to the browser setting.

## Verification

- Frontend `43307dcb` (integrated as `1fb7641f`): `npm run typecheck` and five focused
  Vitest files passed, 61 tests. Files: `src/api/browser-network.test.ts`,
  `src/v2/components/{browser-network-control,agent-browser,settings,permission-control}.test.tsx`.
- Production frontend build passed. Existing chunk-size warnings remain.
- Backend `7f8b3c6a` (integrated as `1c577359`): focused application, HTTP API,
  store, gateway and CLI tests passed, including completed-chat continuation,
  stale candidate/successor transaction ordering, worker availability and schema
  v223/history consistency. Follow-up `79b0458e` (integrated as `b8161836`) passed
  targeted restart persistence, pending-request revocation and OpenAPI tests.
- Integration `b8161836` plus the exact pending source changes committed as
  `1fd9cd88`: `go test ./internal/httpapi -run 'TestOpenAPI|TestBrowserNetworkAccess' -count=1 -timeout=3m`
  passed; the final native browser journey passed (one test, 14.7 s including setup).
  `journey-result.json` preserves the original run metadata. The journey toggles
  access in Ask, reloads and reconnects, continues the conversation, operates the
  local test page, captures its image and closes the owned browser.
- `go vet ./internal/browserruntime ./internal/application ./internal/httpapi ./internal/store ./internal/toolgateway`
  passed. Protocol registry tests and `go run ./cmd/surfacecheck` passed.
- At `1fd9cd88`, `npm run check:api` passed. Generated OpenAPI changes consist of
  two added paths, four new schemas and browser status fields; no prior paths changed.
- Runtime `35bcf7b` (integrated as `5cfe8cba`, unchanged since): runtime tests and
  native cross-origin/resource, restriction and HTTP-write-approval tests passed.
  A separate real Edge request in Ask + Internet with zero declared origins
  loaded `https://example.com/` and returned title `Example Domain` with nonempty
  text. See `external-example-result.json`.

These runs cover Windows. macOS was not exercised locally. The model-driven
journey uses an offline provider; the external Edge check uses the actual internet.
Chromium Fetch does not intercept WebSocket handshakes/messages; this existing
limitation also reproduces with the previous restricted mode and is documented
in ADR 0196. HTTP-write approvals do not claim to cover WebSocket traffic.

## Review

OCR Delegate selected files and supplied rules; the coordinator and an independent
runtime agent read and judged the implementation. `review-coverage.json` separates
full-file reading, diff/context reading, generated-source exclusions and OCR's
default exclusions. It does not claim all-file full-source coverage.

The review fixed lost restrictions on off/on, editing restrictions while offline,
terminal-chat selection, successor transaction ordering, the old Full-only worker
catalogue gate, stale browser approval cards and the new live OpenAPI fixtures.
