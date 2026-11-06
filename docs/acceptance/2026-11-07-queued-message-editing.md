# Queued-message editing — 2026-11-07

Base: `cfeee05a8724a23c673b0ed9971f2ef6e8de9674`.

Opening three queued-message editors made the recovery area grow without a
height limit. At 1280 × 800, the transcript shrank to zero height and the ordinary
composer started at y=871.8, below the viewport. Reloading restored the editors
and the same obstruction.

The editors and unconfirmed queue operations now share a scroll area bounded by
220px or 26% of the viewport height. The existing message list, editor controls,
collapsed-list recovery access and stored draft identities are preserved.

In the reproduced paused-task scenario, the patched 1280 × 800 layout keeps
105px of transcript visible and the composer ends at y=787. At 390 × 844 the
composer ends at y=834, with no horizontal page overflow. All three local edits
remain available by scrolling, and survive reload alongside the ordinary draft.

## Official product references

The [Claude Code Desktop prompt-box reference](https://code.claude.com/docs/en/desktop#use-the-prompt-box)
documents stopping immediately and sending corrections while an action runs.
The [Codex long-running-work reference](https://learn.chatgpt.com/docs/long-running-work)
documents follow-up messages that adjust an ongoing task. These established
workflows motivated checking whether queued corrections remain usable here.
This report makes no claim that either product lacks an undocumented feature.

## Real browser captures

These are unmodified viewport screenshots from Windows Chromium: desktop
1280 × 800 and narrow 390 × 844, using the application's Light and Dark settings.
The base bundle was served by the real Go journey API. The patched production
bundle was served by Vite preview, proxying that same API. Editors were opened
and filled through the UI in each origin. No HTTP responses or rendered content
were mocked. The existing unavailable-browser notice was dismissed in both
versions. The separate automated journey below serves the patched bundle
directly through Go.

| Theme / viewport | Before | After |
| --- | --- | --- |
| Light / desktop | [Before](../images/queued-message-editing/before-light-1280.png) | [After](../images/queued-message-editing/after-light-1280.png) |
| Dark / desktop | [Before](../images/queued-message-editing/before-dark-1280.png) | [After](../images/queued-message-editing/after-dark-1280.png) |
| Light / narrow | [Before](../images/queued-message-editing/before-light-390.png) | [After](../images/queued-message-editing/after-light-390.png) |
| Dark / narrow | [Before](../images/queued-message-editing/before-dark-390.png) | [After](../images/queued-message-editing/after-dark-390.png) |

## Verification

- `npm run build`: passed strict TypeScript and the production bundle.
- `npm test -- --maxWorkers=2 src/v2/components/queued-messages.test.tsx src/v2/queue-recovery.test.ts`:
  39 tests passed.
- `WORKBENCH_JOURNEY_SKIP_BUILD=1 npm run test:journeys -- --grep "queued message edits"`:
  one production Go/Chromium journey passed. It queues five messages, stops the
  current turn, edits three messages, reloads, checks both viewport sizes, focuses
  every retained editor, and saves one edit while the message list is collapsed.
  The ordinary composer draft stays intact.
- `git diff --check`: passed.

The journey uses the repository's offline model provider to hold an actual
runtime turn. No external model service was called. Initial runs encountered a
changing active-turn layout and the existing browser notice reappearing after
reconnect. The final journey stops the turn and waits for and closes that notice
before measuring the queue. It retains the original transcript-height assertion.

OCR delegate selected both production files and excluded the browser spec by
its default path rule. CSS was read in full; the TSX and browser spec were read
partially, covering the changed QueuePanel/QueueEditor paths, new journey and
surrounding helpers. No finding remains in this patch. Unrelated application
files were read only as needed for the flow.
