# Saved command output follows the interface language

Chrome at 1280 × 900, English selected through Settings. Open the completed command's
execution details, then expand its saved stdout. Before, the saved-output button,
reference-data note, and saved-content caption remain Chinese. After, those controls
and notes use English; the original command output is unchanged.

- `before-desktop.png`: production bundle built from
  `fc0935d6fc03ca7abccbd148ce52d26c2bc154f8`, served by the Go application.
- `after-desktop.png`: production bundle built with the locale prop fix, served by
  Vite preview with `/api` forwarded to that same still-running Go application.
- Both captures read the same persisted command and output artifact through the
  production API. The temporary workbenchjourney provider invokes the real
  `command_runtime` tool; no API responses are intercepted or fabricated.

The disposable command prints three lines, exits with code 0, and saves 87 bytes:

```text
Saved output language check
Build complete: 42 checks passed.
OUTPUT_LOCALE_PROOF_2026
```

Record identity:

- Thread: `thread-run-20261021191529-d5777985f529`
- Activity: `toolu_59d4f909e5af33f545d3f903`
- stdout artifact: `artifact-20261021191530-ac3acb048f8e`
- SHA-256: `140aa5d809213172c2160f53ef69cae6dafc137c1d0ad63b31077351ac61ef10`

Focused regression tests cover English and Chinese, lazy artifact loading, a
failed read followed by retry, and the successful saved-content caption. The
39 activity-detail tests and the production build pass.
