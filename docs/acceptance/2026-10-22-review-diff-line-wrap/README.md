# Review Diff long-line wrapping

The current compact file-header controls and preserved horizontal scrolling are
documented in the [2026-10-26 follow-up](../2026-10-26-review-diff-toolbar/README.md).
The captures below record the original implementation.

The review Diff now has a Chinese/English **Wrap long lines** toggle. It starts off and changes only the display; source text and old/new line references retain their original values.

Screenshots were captured in Microsoft Edge using the production UI and the production Go API from `cmd/workbenchjourney`. A disposable Git repository and a pending `settings.ts` edit were created through the real workspace, thread, lifecycle and file-proposal APIs. Both captures use the same persisted edit, original/proposed hashes and raw patch. The before bundle was built from `fc0935d6fc03ca7abccbd148ce52d26c2bc154f8`; the after bundle includes this change. The provider is the existing offline journey provider; this flow does not request model inference.

| Viewport | Review-body scroll offset | Before | After, wrapping enabled |
| --- | ---: | --- | --- |
| 1280 × 900 | 260 px | [Screenshot](before-desktop.png) | [Screenshot](after-desktop.png) |
| 390 × 844 | 650 px | [Screenshot](before-narrow.png) | [Screenshot](after-narrow.png) |

Observed browser behavior:

- Desktop Diff content width changes from 1596 px inside a 588 px viewport to 588 px when wrapped. Turning wrapping off with Enter restores horizontal scrolling.
- At 390 px viewport width, wrapped Diff content and viewport widths are both 323 px; the document remains 390 px wide.
- Space enables the focused toggle. Its `aria-pressed` state follows the display mode.
- Diff source `textContent` is unchanged by wrapping, including the tab and trailing spaces in the long URL line. Clicking the wrapped new line 2 prepares the same original text and source line in the conversation's plain-text draft.
- The Git preview caller is covered by focused component tests. This browser harness does not enable the task Git API, so its Git preview was not exercised live.

Validation passed: 44 focused tests in `review-diff.test.tsx`, `task-overview.test.tsx`, and `task-git.test.tsx`; TypeScript build checking; production Vite build; `go test -count=1 -tags desktop,wv2runtime.error ./cmd/cyberagent-desktop ./web`; and a Windows executable build with those tags. Commands used `scripts/ui-test.ps1` with isolated D-drive runtime directories and the repository-pinned Go 1.26.10 toolchain.

[Desktop pixel metrics](desktop-metrics.json) and [narrow pixel metrics](narrow-metrics.json) report the intentional visual change at zero pixel tolerance. Their `passed: false` means the before and after images differ, as expected for the new toolbar and wrapped rows; it is not a behavioral assertion. The generated diff images and executable remain in the ignored `output/playwright/review-diff/` directory.
