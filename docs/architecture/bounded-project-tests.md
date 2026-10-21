# Bounded project tests

`run_tests` is the project test entry point in Code Plan and Deliver for the root
Supervisor and recorded `specialist_runner` workers with matching WorkerAuthority.
Review is a workflow state, not an execution phase: the domain has
only `plan` and `deliver` phases.

The request contains `version: "run-tests.v1"`. `action: "run"` (the default when
omitted) takes a relative directory `target` and `timeout_milliseconds` from 1
through 25000. Longer tests use `action: "start"` with a timeout up to 1800000 ms,
then `action: "wait"` with the original `job_id`, returned `cursor`, `max_bytes`
(4–32768), and `wait_milliseconds` (1–5000). Wait observes the existing Job without
a new target or timeout; repeating start is not a polling operation.

The host resolves the pinned
project test ID or detects a supported project through the existing project
action registry. It chooses the executable, literal arguments, environment,
closed stdin, disabled network intent, output bounds and empty credentials.
The model cannot supply shell text, executable paths or network settings.

Tests execute project code and can write caches, generated files or fixtures.
They use the currently selected backend and Full/Ask/Auto approval preference
in either phase. Host execution can affect the source workspace and retains
the existing host effects and policy checks. A selected Local/Docker/SBX
sandbox uses its owned Run workspace; unavailable sandbox execution does not
fall back to the host. Dependency installation retains its sandbox-only gate.

The prepared command is pinned in the durable tool authority and regenerated
before approval and dispatch. The existing command authorizer, Run lease,
root attempt, gateway tool budget, command budget, timeout, cancellation and
Job/output receipts remain authoritative. Each run/start creates one Job; wait
returns bounded output and status for that Job, retaining its original actor and
authority. Failed or timed-out Jobs are returned as test evidence. A completed
call is replayed without starting another Job. Worker execution retains its
recorded assignment, WorkerAuthority, and child budget checks.

General `command_runtime` remains Deliver-only. Plan Job insertion additionally
requires a pending `run_tests` call with matching actor, command fingerprint,
prepared command, permission snapshot and backend identity. Passing a Plan
test supplies evidence without advancing the workflow to Deliver or marking
an unedited workspace as a verified mutation.
