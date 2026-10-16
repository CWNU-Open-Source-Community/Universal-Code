# Bounded project tests

`run_tests` is the root Supervisor's project test entry point in Code Plan and
Deliver. Review is a workflow state, not an execution phase: the domain has
only `plan` and `deliver` phases.

The request contains `version: "run-tests.v1"`, a relative directory `target`,
and `timeout_milliseconds` from 1 through 25000. The host resolves the pinned
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
Job/output receipts remain authoritative. A test runs as one synchronous Job;
failed or timed-out Jobs are returned as test evidence. A completed call is
replayed without starting another Job. Specialist calls retain their existing
bounded validation tools and child budget; `run_tests` grants no child access.

General `command_runtime` remains Deliver-only. Plan Job insertion additionally
requires a pending `run_tests` call with matching actor, command fingerprint,
prepared command, permission snapshot and backend identity. Passing a Plan
test supplies evidence without advancing the workflow to Deliver or marking
an unedited workspace as a verified mutation.
