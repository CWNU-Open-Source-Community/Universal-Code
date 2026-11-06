# #527 frontend dependency verification

The images render the production `V2Composer`, `MarkdownInput`, styles, and Lucide icons in Microsoft Edge. A local React harness supplies draft state and a local submit callback; no API server or model is invoked. Clipboard checks use the browser's system clipboard through `navigator.clipboard.writeText` and actual Ctrl+V / Ctrl+Shift+V keyboard gestures.

| Stage | Product revision | Dependencies |
| --- | --- | --- |
| Before | `964ce97ce640610dab79f5d339f7f14c9e6dea8a` (current main) | Tiptap 3.32.0, React Query 5.104.5, Lucide 1.61.0 |
| After | `19a01a39` (#527 + main + shared CI repair) | Tiptap 3.32.1, React Query 5.105.0, Lucide 1.64.0 |

The production composer/editor files are identical in both checkouts. Each checkout uses its own `npm ci` dependency installation. The upgrade also changes `@types/node` 26.6.5 → 26.6.6; React remains 19.3.1 and jsdom remains 29.1.1.

## Images and interactions

Compare `before-desktop-rich-composer.png` with `after-desktop-rich-composer.png` for the initial Markdown table, local image, and icons. Compare `*-desktop-pasted-composer.png` for the real clipboard paste and edited table cell. `*-narrow-rich-composer.png` shows the same source at a 390px window width; the mode switch is accessed from the attachment menu at that width, matching the production layout. `*-desktop-dark-*` repeats the workflow in dark theme.

`*-viewport.png` images are unaltered full-window captures (1024×600 desktop, 390×800 narrow). `*-composer.png` images are unscaled crops of the full composer at its native width (738px desktop, 358px narrow). Device scale is 1. Every before/after pair uses the same theme, content, and window size.

`metrics.json` records six completed workflows: initial image decode, Markdown/source round trip without rewriting the source, system clipboard Markdown paste with bold/code/table/image, typing into a table cell, local submission preserving serialized Markdown, and Ctrl+Shift+V preserving literal punctuation. The layouts have no horizontal overflow; the add/send icons remain visible. At 390px the mode icon is intentionally inside the attachment menu.

## Existing image-paste issue

An image that forms the final, standalone Markdown paragraph is dropped during paste in both old and upgraded dependencies. An image preceded by text in the same paragraph is retained in both. Restoring a draft containing a standalone image renders it correctly in both. `pasted-image-check.json` and `check-pasted-image.js` retain the four-case reproduction; this existing issue was reported separately and no editor source change was added to this dependency PR.

## Checks

Windows, Node 24.22.1 (official SHA256-verified toolchain), npm 11.19.1:

```powershell
cd web
npm ci --no-audit --no-fund
npm test -- --maxWorkers=2
npm run build
git diff --check
```

All 288 test files and 2,932 tests passed. The production build (including TypeScript checking) and whitespace check exited successfully. Raw test/build logs are included. The logs contain jsdom canvas/navigation warnings and the existing large-bundle warning.

## Reproduce

Copy the HTML/TSX/SVG fixture files into `web/.tmp/` in both checkouts. Start their Vite servers at ports 5270 (before) and 5271 (after), then open the chosen fixture URL with Playwright CLI using Edge. Run `capture-editor.js` with `playwright-cli run-code --filename`; its `phase=before` or `phase=after` URL selects the checkout. Run `check-pasted-image.js` to repeat the independent image-paste comparison.
