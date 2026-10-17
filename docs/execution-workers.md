# Execution workers and saved workflows

An admitted Specialist with `worker: {}` uses the execution worker profile.
The host intersects the parent's currently available workspace, command, browser
and MCP capabilities with the delegation's optional `worker.tools` list. Omitting
`worker` retains the existing analysis/scoped-read behavior. An omitted or null
`tools` list inherits; `tools: []` grants no native tools. A list such as
`["workspace_read", "workspace_grep", "command_runtime"]` narrows the profile.

Tools keep the Run's Ask / Auto / Full mode, policy checks, permission activation,
workspace and budgets. The command adapter must already be enabled for the Run's
execution profile. Browser tools retain their platform and Full-mode requirements.
Delegation does not activate a runtime or authorize a new MCP server.

Every native call records its real child Agent, attempt and Session. A required
approval suspends the attempt and releases the foreground Run lease. Repeating
the execution request while review is pending returns `waiting_approval` without
another model call. After review, execution resumes the same attempt and exact
stored intent. A dispatched call whose outcome was lost is not automatically
sent again. Terminal tool results remain available to subsequent model rounds.

Command jobs belong to their creating child attempt. Another child cannot read,
write to, or cancel them. Browser sessions and MCP connections have separate
worker identities; MCP roots resolve through the host's pinned execution
workspace. Completing an attempt closes its resources. Waiting for approval keeps
the attempt's live jobs until completion, cancellation or authority revocation.

Ordinary workers use the Run's workspace, including its owned Drydock when one
exists. The existing Batch delivery path continues to provide isolated branches,
worktrees, owned edit paths, declared verification, review and integration.

## Using the interface

Open the Run's **Agents** page, expand **Create workflow**, enter a name and task,
and keep **Execution worker** selected. Leave delegated tools empty to inherit,
or enter a comma-separated list to narrow them. Save the definition, then start
it. The JSON editor exposes the task and workflow budgets and includes a branch
and loop template. The execution list shows persisted outputs and offers continue,
pause, resume and rerun. Start, continue and resume submit control requests; the
running Desktop or API host advances the saved workflow independently of the page.
The page observes its persisted state, so refreshing or closing it does not stop
the next worker turn. The host needs model control enabled; a read-only host does
not execute saved workflows.

Rerun creates a new execution with the same saved definition and inputs. It checks
current Run authority; it does not restore previous execution permission. Pause
takes effect between worker turns. Stop the active Run to cancel in-flight work.
An approval stops the original worker until its decision is recorded. The host
then continues that same worker through the existing approval and permission checks.
An entire completed workflow sends one durable summary notification to the main
conversation, with output treated as untrusted report data.

## Workflow contract

`workflow.v1` is an immutable JSON definition stored by content digest. Its step
kinds are `worker`, `if`, and `repeat`. Worker steps contain a `child_task` contract;
their committed output is the `CompletionReport` (`outcome`, `summary`, work-item
IDs and note IDs). Inputs can refer to `$input` or a completed step using a JSON
pointer. Conditions compare the referenced JSON value with `equals`. Repeat checks
its condition after each iteration and fails explicitly if `max_iterations` is
exhausted without success.

Each definition bounds steps, jobs, tokens, turns and elapsed time. A workflow can
start at most 64 jobs. The core keeps at most two active direct children and a
bounded history of 256 children per Run. Finished children retain actual usage
without reserving their unused allocation forever. A workflow advances one worker
turn at a time; independent workflows remain subject to the same shared admission
and Run limits. State, revision, outputs and worker identity survive database reopen.

The host uses the existing scheduler and advances one worker turn per tick. Each
turn holds the existing database claim, preventing another host or a manual API
request from executing the same workflow concurrently. Paused executions stay
paused; waiting executions are revisited when their dependencies change. A
deadline can still expire while paused or waiting, and resume never resets the
saved time, token or turn limits.

After the host restarts, it can continue from committed checkpoints and approval
stops. A clean shutdown releases the workflow claim; after an abnormal exit, the
next host waits for the existing claim's remaining lease (at most 15 minutes).
If the process ended during a model/tool attempt whose result is unknown,
the existing recovery marks that attempt interrupted and the workflow reports the
failure rather than repeating its possible side effects. A fresh rerun remains an
explicit operator action. The computer and host must be running to make progress.

API entry points:

- `GET/POST /api/v1/workflows`: list or save definitions.
- `GET /api/v1/workflows/{definition_id}`: inspect a definition.
- `GET/POST /api/v1/runs/{run_id}/workflows`: list or start executions.
- `GET /api/v1/runs/{run_id}/workflows/{execution_id}`: read current state.
- `POST .../{advance|pause|resume|rerun}`: control an execution. Advance, pause
  and resume include the observed revision; start and rerun use stable request keys.
  Resume also retries an execution waiting for a dependency. The single-turn
  `advance` endpoint remains available for API clients; normal UI execution does
  not use a browser loop to call it.

The existing `run specialist-step` CLI accepts execution workers as well as legacy
scoped readers. Runtime configuration and invocation grants use the ordinary Run
execution flags. The old `--tools` flag continues to select only scoped reads for
legacy children.

Implementation follows the permission inheritance described by
[Codex](https://learn.chatgpt.com/docs/agent-configuration/subagents), tool narrowing
described by [Claude Code](https://code.claude.com/docs/en/sub-agents), and explicit
reusable orchestration described by [Claude Code workflows](https://code.claude.com/docs/en/workflows).
Universal-Code executes its own bounded JSON program through the existing Go
worker, approval, session and usage services.

Host-owned continuation follows the separation of control and observation in
[Codex App Server](https://learn.chatgpt.com/docs/app-server) and the hosted-session
lifecycle described by [Claude Code](https://code.claude.com/docs/en/agent-view#how-background-sessions-are-hosted).
The implementation reuses Universal-Code's scheduler and workflow interpreter.
