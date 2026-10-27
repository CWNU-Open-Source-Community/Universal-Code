# Escape to stop from the message input

Captured on Windows in Chrome at 1440 × 1000 with the built production UI,
production API and SQLite store. The offline journey provider waits on its real
request context when the submitted message contains `JOURNEY_STOP:`. The UI
therefore cancels a real running turn; interrupt and execution responses were
not mocked, and the page was not edited to produce the screenshots.

- Before UI: `333a4451`, digest
  `c7801ca8cbe28986fd1b513fdd3a9010096e928b2b80f8d527a11f2741a9f22b`.
- After UI: this change on `e790ec4b`, digest
  `0c787bae41a07b05786f5d23d8e0f55addb987ec8a40084a1fcbbd13d0bebafa`.

| Image | Observed behavior |
| --- | --- |
| [Before](before-escape.png) | Escape in the focused Markdown input leaves the turn running. A subsequent execution GET confirmed `state: running`. |
| [Shortcut hint](after-shortcut-hint.png) | The existing stop button shows Esc. Escape in the attachment menu closed that menu and the execution GET still returned `running`. |
| [After Escape](after-escape-stopped.png) | Escape in the message input stops the turn. The stopped notice is visible and the unsent draft is unchanged. |

The final Markdown check sent one real `POST /threads/{thread_id}/interrupt`
for execution `thread-execution-20261021195553-fc4c0222ada7`. It used the existing
operation key `v2-thread-stop-thread-execution-20261021195553-fc4c0222ada7` and
received HTTP 202. The following execution GET returned `state: idle` and
`last_turn_interrupted: true`. The draft remained
`停止后继续修改的草稿，不要清除。`.

The same workflow also passed in the plain-text input. Escape in the model menu
closed the menu; Escape on a toolbar button left the execution running. Escape
in the input then received HTTP 202, reached the same confirmed interrupted
state, and retained `纯文本停止后保留的草稿。`. Each cancelled turn's pending
submission returned the expected HTTP 499; Chrome logged those two resource
responses and no other console errors or warnings in the final run.

Focused React tests passed: 94 tests across composer shortcuts, composer,
conversation, and execution controls. They cover composition, modifier and
repeat keys, native editor Escape handling, overlay priority, absent execution
permission, and pending/stopping deduplication. `npm run build`, the tagged
journey build, `git diff --check`, and the protocol registry check against
`e790ec4b7856a5eafea4d2ea7d441f05ec677963` passed. The production build retained
the existing large-chunk warning.

The journey provider branch is excluded without the `workbenchjourney` build
tag. Its normal response paths and provider timeout configuration are unchanged.
