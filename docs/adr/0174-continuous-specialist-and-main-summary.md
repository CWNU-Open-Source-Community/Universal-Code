# ADR 0174: Authorized continuous child execution and main-conversation summary

- Status: Accepted
- Date: 2026-10-12

## Problem

The ordinary child execution button dispatched one Specialist turn. A child that
returned `continue` required another click, and its completion report reached the
root inbox without causing a main-conversation reply.

## Decision

The ordinary button calls `POST /api/v1/runs/{run_id}/agents/{agent_id}/continue`
with `specialist_continuation.v1`, a stable operation key and explicit execution
confirmation. The visible button and explanation authorize both continuous child
execution and the completion summary. The shared Go application service invokes
the existing `SpecialistScheduler` for at most 32 rounds. Admission, direct root
parentage, Run leases, child/Run turn and token limits, monetary budgets, model
cancellation and the server-owned read/search profile remain authoritative.

Migration 191 adds an immutable optional continuation digest and lookup index to
`specialist_schedules`. The schedule itself records the result. A retry joins a
live schedule or replays its terminal result; stale attempts use the scheduler's
existing fenced recovery. There is no second child execution queue. `/execute`
and the existing CLI retain their single-turn behavior.

An actual completion report queues an attributed main-conversation request with
`requested_by=specialist_completion`. It identifies itself as an authorized child
completion notification. The existing Run wake control and consumer execute a
parent turn and save its assistant reply in the main Session. One-step wakes
process earlier messages in FIFO order until the summary notification is reached,
using stable batch identities. The notification retains the inbox's bounded,
redacted report projection so an earlier turn cannot drain its evidence. Wake
consumption selects the exact intent so replaying this child cannot consume a
later operator wake. The child's budget and the parent's summary usage retain
their separate Agent identities.

The response exposes schedule status, stop reason, attempts, child token/time
usage and summary status. The UI supports receipts from each child turn, refreshes
the main Thread queries and links to the main conversation. Execution clears the
ordinary 30-second HTTP write deadline and restores it before returning the final
response; request cancellation and execution budgets continue to bound work.

## Refusal and recovery

Read credentials, disabled model control, missing confirmation, unknown request
fields, unadmitted or foreign children, and non-root parents cannot dispatch this
operation. New execution requires a running Run. A paused task must be explicitly
resumed using the existing task controls; cancellation is retained. Waiting or
budget-exhausted children without a completion report do not trigger a summary.

When the parent is busy, the request waits for its execution lease to be released
and then delivers the summary. Existing wake retries retain their backoff and
attempt bounds. A pause or cancellation stops delivery. The UI retains the original
key after a lost response or failed summary so the operator can resume the task and
retry without repeating child work. Summary failure is shown separately from child
completion. The parent uses its normal budget and runtime controls for the reply.

The new presentation storage namespace prevents an earlier single-turn key from
authorizing continuous execution. Old presentation values remain untouched and
the original `/execute` replay is retained. Rollback restores the single-turn
component and leaves schedules, attempts, completion reports and wake receipts
available. Migration 191 only adds an optional column and index.

## Alternatives

- Repeated renderer calls to `/execute`: rejected because the browser would own
  continuation and recovery, and navigation would interrupt its decision loop.
- A new background child scheduler: rejected because the existing Go scheduler
  already provides the required execution, budget and cancellation behavior.
- A synthetic summary made directly from child text: rejected because the parent
  should consume the durable completion report and generate the main reply through
  its existing Session and Run accounting.

## Validation

Focused application tests cover two child turns, real completion delivery into
the parent model context and a stored main-Session assistant summary; exact replay
without another model call; cancellation; a spent child turn budget; explicit Run
pause; FIFO delivery behind earlier messages; a busy parent's frozen handoff;
wake retry; and preserving a later wake when the earlier child request replays. HTTP
tests cover confirmation and capability gating. UI tests cover lost-response
retry, a queued summary retry, the main-conversation link, per-turn tool receipts
and paused-task feedback. The v190 upgrade test preserves existing schedules and
checks the new digest's immutable replay behavior.
