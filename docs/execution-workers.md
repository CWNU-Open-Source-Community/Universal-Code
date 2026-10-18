# Execution workers and saved workflows

An admitted Specialist with `worker: {}` uses the execution worker profile.
The host intersects the parent's currently available workspace, command, browser,
MCP, Skill, history and language-server tools with the delegation's optional `worker.tools` list. Omitting
`worker` retains the existing analysis/scoped-read behavior. An omitted or null
`tools` list inherits; `tools: []` grants no native tools. A list such as
`["workspace_read", "workspace_grep", "command_runtime"]` narrows the profile.

Tools keep the Run's Ask / Auto / Full mode, policy checks, permission activation,
workspace and budgets. The command adapter must already be enabled for the Run's
execution profile. Browser tools retain their platform and Full-mode requirements.
Delegation does not activate a runtime or authorize a new MCP server.

`skill_read` discovers and reads currently available native Skills and exact
installed package versions during execution. A successful main-file read can
activate that guide for this worker; bundled resources stay reference data.
Removing, disabling or changing the selected installation invalidates it before
the next model request, including retries. This is independent of preloaded task
guidance. Read-only children keep their existing tool profile.

Workers receive the configured language server's supported read tools, including
definitions, references, symbols, diagnostics and call hierarchy. Queries use the
worker's physical workspace and retain the current permission and path checks.

`history_search` and `history_read` default to the child's own records and task
material explicitly delivered by its parent. Set `worker.history_scope` to
`parent_snapshot` to additionally capture the parent's visible history during
admission. This choice is immutable for that child. Later parent messages and
other children’s private records are excluded. Each source and page is rechecked;
a changed or no-longer-visible source cannot be read using an old result.

Every native call records its real child Agent, attempt and Session. A required
approval suspends the attempt and releases the foreground Run lease. Repeating
the execution request while review is pending returns `waiting_approval` without
another model call. After review, execution resumes the same attempt and exact
stored intent. A dispatched call whose outcome was lost is not automatically
sent again. Terminal tool results remain available to subsequent model rounds.

Commands, browser sessions and MCP connections belong to their child worker.
They remain available when that same worker continues into another attempt;
each new call still records and checks the current attempt. Another child cannot
read, write to, or cancel those resources. MCP roots resolve through the host's
pinned execution workspace. Finishing or closing the worker releases its
resources. An approval wait retains the same attempt and does not repeat a model
request until the decision is available. A host restart can recover durable
records, but does not restore live browser or MCP sessions.

Ordinary workers use the Run's workspace, including its owned Drydock when one
exists. Batch workers use isolated branches and worktrees, with owned file paths,
declared verification, independent review and integration. They can also use
their delegated command, browser, MCP, Skill, history and language tools. Batch
file edits and Git operations keep the existing owned-path executor; a general
command remains governed by the selected runtime and current task permissions.

## Using the interface

Open the Run's **Agents** page, expand **Create workflow**, enter a name, select
**Share current workspace**, enter the task, and keep **Execution worker**
selected. Leave delegated tools empty to inherit,
or enter a comma-separated list to narrow them. New execution workers default to
**Code delivery: verified completion**. Choose **Analysis report** for research or
inspection without a code-delivery test requirement. A code-delivery step needs
`run_tests` in its delegated tools. Its optional **Test directory** is a relative
project directory; leaving it empty uses the project root. **Include current parent history**
is off by default; enable it only when that task needs the parent's current
history. Save the definition, then start
it. The JSON editor exposes the task and workflow budgets and includes branch,
loop and dynamic parallel review templates. The execution list shows persisted
outputs and per-item outcomes, with controls for continue, pause, resume, failed-item
retry and rerun. These actions submit control requests; the
running Desktop or API host advances the saved workflow independently of the page.
The page observes its persisted state, so refreshing or closing it does not stop
the next worker turn. The host needs model control enabled; a read-only host does
not execute saved workflows.

Choose **Isolated delivery (Batch)** to use a delivery plan already approved and
prepared in Batch, and provide its `plan_id` in launch inputs. The workflow runs
the children, waits for independent review and produces an integration branch.
Tool approval resumes the existing attempt. Unknown execution outcomes require
inspection and owner recovery. Completion or cancellation closes the Batch
workers, releasing their unused budget and child slots. Unconfirmed resource
cleanup preserves the worktrees and can be retried.

Rerun creates a new execution with the same saved definition and inputs. It checks
current Run authority; it does not restore previous execution permission. Pause
takes effect between worker turns. Stop the active Run to cancel in-flight work.
An approval stops the original worker until its decision is recorded. The host
then continues that same worker through the existing approval and permission checks.
An entire completed workflow sends one durable summary notification to the main
conversation, with output treated as untrusted report data.

## Workflow contract

`workflow.v1` is an immutable JSON definition stored by content digest. Its step
kinds are `worker`, `map`, `batch`, `if`, and `repeat`. Worker steps contain a `child_task` contract;
their committed output is the `CompletionReport` (`outcome`, `summary`, work-item
IDs and note IDs). Inputs can refer to `$input` or a completed step using a JSON
pointer. Conditions compare the referenced JSON value with `equals`. Repeat checks
its condition after each iteration and fails explicitly if `max_iterations` is
exhausted without success.

### Result-driven parallel tasks

A completion report may include a `data` JSON object (at most 4 KiB and eight
levels deep). The existing redaction rules apply before persistence. This is
report data for subsequent inputs and conditions; it cannot supply tool grants
or replace native verification evidence.

A `map` step uses an `items` reference to a saved JSON array, for example
`{"step":"plan","path":"/data/items"}`. Its fixed `task`, `completion` and
reading scope apply to every item. The host adds `item` and zero-based `index`
to each child's inputs. `max_items` is required (1–64); `parallelism` defaults
to one and can be two. The source array and attempt identities are saved before
any child starts. An empty array completes without a child; a non-array or an
array over the limit fails without silently dropping entries.

Up to two children run under one existing Run execution lease. A sibling's failure
does not cancel a valid in-flight completion. Once all items finish, the map
publishes an ordered `items` array containing each item's index, attempt identity,
outcome, summary and optional data. A downstream step can read this array to join
the results. A partial report remains a result for conditions and loops; a failed
execution is handled through the retry control below. Per-item code delivery uses
the same native verification requirements as an ordinary worker.

### Retry failed items

After every item in the current group has a durable completion or failure, a
retryable failure offers **Retry failed items only**. The confirmed request carries
the observed revision and a stable operation key. Successful items keep their
original result and child; only failed items get a new attempt. Replaying the same
request does not create another attempt. Workers awaiting approval keep their
existing identity and resume through the approval flow.

`max_attempts` on worker and map steps defaults to three and accepts 1–3, including
the initial attempt. Retries keep the original deadline, inputs and cumulative
job, token and turn usage. They require current task authority and remaining
budget. **Rerun original inputs** creates a separate execution from the beginning.
`state.maps` records each group's frozen inputs and latest item outcomes;
`state.allocated_jobs` also counts failed attempts, while `state.jobs` counts
completed jobs.

### Code-delivery completion

A worker step can declare `completion: {"mode":"verified_code","target":"."}`.
The target is a relative directory passed to the existing `run_tests` tool, not a
shell command. Omitted `completion`, or `completion: {"mode":"report"}`, retains
report-based completion for existing definitions and analysis steps. Read-only
workers cannot declare `verified_code`.

For code delivery, the host checks the native project-test Job before accepting
the child's successful report. It uses the persisted workflow step and child
identity to read the Job, including a test started in an earlier continued
attempt of that same child. A prose claim or Job ID in the model's report is not
verification evidence. The receipt must be passed and reusable for the current
source, test configuration, permissions, workspace and runtime; the actual Job
must have exited successfully and finished process-tree cleanup.

Before completing the entire workflow, the host checks the required test
directories again. Later source changes invalidate earlier results. A more
recent test failure or unfinished invocation in this execution takes precedence
over an earlier pass for that directory. A later successful verification step
can provide the current result after subsequent edits.

`state.verifications` records the last host check for each required directory,
including its time, Job, source version and rejection reason. The interface calls
this **Last verification checks**: viewing a completed execution does not run
tests or certify later edits. A failed check prevents workflow completion and is
shown with its reason. Rerunning remains an explicit action using current
permissions. Receipt inspection is internal to the host's claimed workflow;
ordinary worker tool access keeps its existing resource ownership rules.

A Batch step uses `batch_plan: {"step":"$input","path":"/plan_id"}`. Its output
contains accepted receipt and review IDs, the integration branch and commit, and
`source_updated: false`. A later shared step can consume this output but still
uses the Run's original workspace; integration does not silently switch or edit
that workspace. The existing Batch review and merge controls remain responsible
for accepting changes.

Each definition bounds steps, jobs, tokens, turns and elapsed time. A workflow can
start at most 64 jobs. The core keeps at most two active direct children and a
bounded history of 256 children per Run. Finished children retain actual usage
without reserving their unused allocation forever. A workflow advances one round
at a time, with up to two map workers; independent workflows remain subject to the same shared admission
and Run limits. State, revision, outputs and worker identity survive database reopen.

The host uses the existing scheduler and advances one worker round per tick. Each
round holds the existing database claim, preventing another host or a manual API
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
- `POST .../{advance|pause|resume|retry_failed|rerun}`: control an execution. Advance, pause
  and resume include the observed revision; start and rerun use stable request keys.
  Failed-item retry includes the observed revision, `confirm_start: true` and an
  idempotency key, retaining the same execution and cumulative limits.
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
