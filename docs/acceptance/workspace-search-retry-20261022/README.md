# Workspace search retry

Baseline: `b6e89e8c481bb38293d584a9204b36916528c834` (#448).

Both PNGs are unmodified 1440 × 1000 Chrome captures of the production renderer and API served by `cmd/workbenchjourney`. The harness uses a disposable workspace and an offline model; `retry-notes.md` is a temporary document containing `SEARCH_RECOVERY_PROOF`.

For each capture, only the browser's workspace-search requests were temporarily aborted. After the error appeared, the interruption was removed and a direct request confirmed that the real API returned HTTP 200 with the expected file. No API response was replaced.

- `before-retry-stuck.png`: clicking Search again with the unchanged keyword sends no request and leaves the error visible.
- `after-retry-recovers.png`: the same action sends a request, receives HTTP 200, and displays the result without editing the keyword.

The button and Enter regression cases both fail against the baseline and pass with the fix. The Explorer and Markdown preview suites pass together (21 tests); TypeScript and the production build pass. OCR completed the changed production component with zero findings; its default path rules exclude test files.
