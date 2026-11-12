# Browser navigation origin label review

These images show the production `V2AgentBrowser` React component and its production styles in Microsoft Edge, driven by a local API-client fixture. The fixture replaces the status API with a ready, headed browser status containing declared origins. It does not start a browser session, execute server actions, or supply screenshots or live frames. The surrounding page is a component review harness, not the full workbench.

| Stage | Source revision | Meaning |
| --- | --- | --- |
| `base` | `bab65dca83986f105b16e45e59dfb6676ec16b33` | #542 base; no origin note |
| `current` | `d4f12cd7c08519aeeb7b269847266c155fd246ce` | #542 original head; longer origin claim |
| `after` | `3bb1837a0aee62e18366e0ca815d4c5c67a0494d` | Concise navigation-origin label |

For the base capture, the production component file was loaded byte-for-byte from the base commit. The base and original head have identical rendered imports/styles; their other web differences are the companion type, schema, and tests. `metrics.json` records the component blobs and measured dimensions.

## Suggested PR images

Use `base-desktop-card.png` and `after-desktop-card.png` for the PR base-to-head comparison. Use `current-desktop-card.png` and `after-desktop-card.png` to review the wording refinement.

Use `current-narrow-long-origins-card.png` and `after-narrow-long-origins-card.png` for the 390px window with three long origins. The origins wrap without horizontal scrolling; both header controls and the latest-status footer remain visible.

`*-viewport.png` files are unaltered full-window captures (1024×600 desktop; 390×800 narrow). `*-card.png` files are unscaled element crops of the complete card at its native width (738px desktop; 358px narrow). Both comparison stages use light theme, Chinese locale, and device scale 1. `after-desktop-dark-*` and `after-desktop-english-*` are supplementary checks.

## Verification

On Windows with Node 24.22.1:

```powershell
npm test -- src/v2/components/agent-browser.test.tsx
npm run typecheck
git diff --check
```

The component test file passed all 25 tests; type checking and the whitespace check exited successfully. The existing jsdom canvas warning appeared during the live-view test. These screenshots establish the label/layout behavior for the supplied component status, and do not establish backend network enforcement.

## Reproduce the component capture

Copy `pr542-browser-evidence.html`, `pr542-browser-evidence.tsx`, `capture-stage.js`, and `select-stage.mjs` into `web/.tmp/`. Save the original `d4f12cd7` component as `web/.tmp/agent-browser-original.tsx`. Start Vite in `web` on loopback port 5420, then open the fixture URL with Playwright CLI in the `pr542` session.

For each `base`, `current`, and `after` stage, run `node web/.tmp/select-stage.mjs STAGE`, navigate to `http://127.0.0.1:5420/.tmp/pr542-browser-evidence.html?phase=STAGE`, then run `playwright-cli --session pr542 run-code --filename web/.tmp/capture-stage.js`. Restore `after` when finished. The capture checks the expected label, horizontal overflow, and header-control placement before returning measurements.
