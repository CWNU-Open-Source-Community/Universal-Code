# ADR 0173: Configured project tests and formatting through the selected runtime

Status: accepted

## Behavior

The ordinary Changes and delivery panel can inspect and run the Run-pinned
`test.go`, `test.node`, `test.npm`, and `format.go` presets. Tests accept an
optional relative directory; formatting accepts one exact relative Go file.
The current runtime and permission determine availability. A click supplies
the existing exact operator consent without changing the Run's preferences.

`GET /api/v1/runs/{run_id}/project-actions` returns two action slots, their
configured IDs, target kinds, backend and actionable unavailability reasons.
`POST .../project-actions/{slot}` accepts the target and an Idempotency-Key and
returns the persisted Command Runtime Job promptly. `GET
.../project-actions/jobs/{job_id}` reads status and bounded real output. Live
output and stopping reuse the existing Job-owner bridges. Docker output retains
its current terminal capture behavior.

The accepted invocation keeps its lease and execution context until the Job
ends, including after the initiating HTTP request disconnects. Saved intent
binds the Run's pinned project configuration, slot, target and operator. A retry
with the same key reads that Job; a changed target conflicts. No new Job is
created to reconstruct missing output or recover an interrupted execution.

## Runtime boundaries

Host and Local resolve the installed native toolchain. Local binds the Go
distribution read-only so compilers and the standard library remain available,
and includes its bin directory on the isolated PATH. An npm package installed
outside the Node distribution is a separate exact read-only input with a
virtual launcher path. Local does not mount its user-profile ancestors.

Docker uses fixed guest executables and image-generation/path digests. Host
executable or npm launcher paths are never sent to the guest. The fixed image
adds the npm package/launcher and gofmt. An exact image metadata marker gates
these new tools; older images receive a rebuild reason. The existing fixed
runner protocol and historical Job readers remain available. See
[image rebuild instructions](../standard-code-docker.md#fixed-image).

Formatting checks read-only settings and the actual target's pinned path
policy. Availability checks only action-level settings and toolchains; a
synthetic filename does not determine whether formatting is available.
Execution retains the existing CommandRuntimeOperator, current permission
authority, exact Run lease, host policy and ordinary Job accounting.

## Validation

Focused tests cover real Go formatting, live Node output after HTTP-context
disconnect, stopping the admitted Job, nonzero test output, cold replay,
changed-key intent, excluded targets, host-policy denial, Local Go/npm path
translation and fixed Docker guest toolchains. The Docker image marker is
inspected without execution. Container execution tests use the existing
controlled Docker transports; these tests do not claim a rebuilt image ran on
this host.
