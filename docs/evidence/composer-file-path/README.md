# Composer project path completion

Captured on Windows with Chrome 155 on 2026-10-22, using the production Go API,
`web/dist`, and `cmd/workbenchjourney` with its offline provider. The provider ran
against a disposable real workspace and saved conversations. File listings,
file snapshots, message submission and replies used the existing endpoints.

## Before and after

The baseline is `bf34da021df886b6cc29f7ff212ddd24c4019def` (#458). Each pair shows
the same project, empty conversation, `检查 @` draft and viewport. The baseline
bundle was copied before editing and served by the same Go API used for the
changed renderer.

| Viewport | Before | After |
| --- | --- | --- |
| 1440 × 960 | [Before](before-wide.png) | [Root directory candidates](after-wide.png) |
| 390 × 844 | [Before](before-narrow.png) | [Root directory candidates](after-narrow.png) |

## Real selection and submission

The real file `资料 目录/说明 文.md` contains the offline provider's `JOURNEY_READ`
trigger and `JOURNEY_FILE_PROOF_2026`. In rich mode, typing `@`, choosing the
`资料 目录/` directory and selecting the file by keyboard produced a quoted path
and the normal file-reference chip. Sending a normal Chinese prompt started the
provider and produced the saved response `Read README.md: JOURNEY_FILE_PROOF_2026`.

- [Rich mode: selected file](after-attached-rich.png)
- [Rich mode: saved message and reply](after-sent-rich.png)
- [Plain mode: selected file with Tab](after-attached-plain.png)
- [Plain mode: second saved message and reply](after-sent-plain.png)

The second submission returned HTTP 202. Its recorded request included:

```json
{
  "version": "thread_message_submission.v1",
  "content": "再次检查 @\"资料 目录/说明 文.md\"",
  "files": [{
    "source_kind": "workspace_file",
    "path": "资料 目录/说明 文.md",
    "expected_sha256": "821620b4cfddfb8a756b23183c9ddb2712ca79228d7c74ba7849d7485fe42485"
  }]
}
```

The earlier sidebar chat with a `JOURNEY_READ` title is a fixture probe: Markdown
escaped the trigger in its body. The successful rich-mode run above instead
supplied the trigger in the actual referenced file.

For the failure check, `资料 目录/失败 示例.md` was created on disk, listed by the
real directory endpoint, then deleted before selection. Reading it returned HTTP
404. The [failure screenshot](after-read-failure.png) shows the inline error;
the draft remained exactly `保留这段草稿 @"资料 目录/失` and no file reference was added.

## Keyboard, language and boundaries

- [English, 1440 × 960](after-english.png) and
  [English, 390 × 844](after-english-narrow.png) show the real Chinese path under
  English controls. After Escape, the listbox count was zero and the draft was
  unchanged, including after the browser's following selection event.
- The document client/scroll widths were 1440/1440 and 390/390. The candidate
  bounds were x=501.5…1238.2 and x=12.7…377.3. The existing conversation scroll
  area in the narrow sent-chat screenshot still has a horizontal scrollbar
  (client=380, scroll=521); this base does not include the separate #441 tooltip
  fix (`03dc2611`).
- Candidates come from `workspaceExplore` for the current parent directory only.
  The existing 200-entry limit and truncation notice remain. A filename outside
  that returned directory page is not discoverable through this completion.
  `workspaceSearch` and file-body scans are not used.
- Completion acts at the end of an active path token, including immediately
  before its closing quote. Moving into the middle of an existing path leaves
  normal editing available. File selection reuses the existing snapshot,
  digest, partial/redacted flags, four-file limit and submission permission checks.

Focused checks passed: 60 tests across the five files below, followed by
`npm run build` (TypeScript and production bundle; existing bundle-size warning).

```sh
cd web
npx vitest run src/v2/components/file-path-mention.test.ts src/v2/components/file-path-suggestions.test.tsx src/v2/components/markdown-input.test.tsx src/v2/components/file-context.test.tsx src/v2/components/composer-stop-shortcut.test.tsx
npm run build
```

The tests cover both editor modes, local replacement and rich formatting,
directory-only requests, spaces and Chinese paths, email/code/link protection,
IME, Enter/Tab/Escape, read failure, closing while reading, late responses after
scope/mode/manual edits, restored drafts and the existing stop shortcut.
Browser coverage is Windows Chrome. Native Tauri and macOS were not exercised.
#440 prompt history is not in this base; its `historyKey` call must remain after
candidate navigation when the branches are combined.
