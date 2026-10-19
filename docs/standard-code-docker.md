# Standard Code Docker backend

The fixed Docker adapter uses current Ask/Auto/Full operation authority and the
existing immutable Docker admission ledger. Full requires live process activation.
The metadata-only, lease-fenced `attach_stdin` lifecycle action is shared with
Command Runtime; execution, log and Drydock state use the established ledgers.

The Docker backend is an explicit, fixed `network=none` Standard Code fallback. It is
available only for a Code Run whose current execution profile is `docker`, current
permission has current operation authority, exact Drydock is ready, per-call approval is
approved, and the current process enables both Workspace Sandbox and Docker execution.
It never falls back to an unsandboxed host command.

## Fixed image

For project dependency preparation, use **Changes and delivery → Checks and
delivery → Prepare project**. Review the detected lockfile and explicitly select
the package origins; the application downloads verified artifacts, then installs
offline in this fixed image. npm/pnpm install scripts have a separate operator choice.
See [Prepare project dependencies](project-preparation.md) for cache reuse,
failure recovery, and installed toolchain requirements.

Set one exact, already-present image digest. The product readiness path only inspects
this digest and never pulls:

```powershell
$env:CYBERAGENT_STANDARD_CODE_DOCKER_IMAGE_DIGEST = "sha256:<64-lowercase-hex>"
```

To build the repository fixture, first make the four exact base references named by
`CYBERAGENT_STANDARD_CODE_{GO,NODE,PYTHON,RUST}_IMAGE_DIGEST` available in the
fixed local Engine. Each value must be `name@sha256:<64-hex>`. Also download the
[pnpm 10.28.1 archive](https://registry.npmjs.org/pnpm/-/pnpm-10.28.1.tgz) and set
`CYBERAGENT_STANDARD_CODE_PNPM_TARBALL` to that local file. The build verifies its
fixed SHA-512 integrity before copying it; it does not download package managers.
Then run:

```powershell
powershell -ExecutionPolicy Bypass -File internal/sandbox/testdata/standard-code-docker/build-fixture.ps1
```

The script refuses missing/unpinned bases, disables build-step networking and pulling,
chooses a random tag it owns, strips inherited environment, volume, and label config,
and prints the exact digest for both product and opt-in test vars. After removing
inherited labels, it sets `org.traverse-board.project-actions=1`,
`org.traverse-board.project-preparation=1`, and `org.traverse-board.pnpm-version=10.28.1`.
The last label enables pnpm preparation; older images retain their existing
npm/Go capabilities and request a rebuild for pnpm.

The current fixture contains `standard-code-docker-runner.v2`; rebuild it when
upgrading from a v1 image. Durable v1 manifests remain readable as closed-stdin
recovery evidence, but new execution always compiles the v2 contract.

Project action support adds the installed npm package and its `npm` launcher to
the fixed Node toolchain, and exposes the Go distribution's `gofmt` as a fixed
toolchain. Rebuild the fixture with the command above, replace
`CYBERAGENT_STANDARD_CODE_DOCKER_IMAGE_DIGEST` with the newly printed digest,
and restart the application. Project actions inspect the exact image metadata;
an older image without the fixed label keeps Go/Node test support but reports
npm/gofmt unavailable with a rebuild instruction. This inspection starts no
container and never pulls an image. The actual Job still determines whether
the command succeeded.

Command Runtime uses `docker-standard-code-guest.v1` executable identities for
new Docker Jobs. Executable digests bind the installed image generation and
fixed guest path rather than a host executable. Historical
`docker-standard-code.v1` Job receipts retain their original digest semantics.

## Readiness and execution

After creating the Drydock and selecting the required Run profile/permission, probe
the current exact generation and Checkpoint:

```powershell
cyberagent run standard-code docker-readiness <run-id> `
  --generation <n> --checkpoint <checkpoint-id> `
  --toolchain go --arg test --arg ./... --purpose "offline tests" `
  --enable-permission-control --enable-workspace-sandbox `
  --enable-docker-execution
```

The JSON response is `standard-code-backend-readiness.v1` and includes stable
`blocked_by` and `remediation`. A missing daemon or image is an expected blocked result,
not host fallback or an image pull.

The following standalone CLI workflow is for Ask/Auto; these commands do not
activate Full. Preparation creates the existing exact sandbox approval proposal:

```powershell
cyberagent run standard-code docker-prepare <run-id> `
  --generation <n> --checkpoint <checkpoint-id> `
  --toolchain go --arg test --arg ./... --purpose "offline tests" `
  --operation-key <prepare-key> `
  --enable-permission-control --enable-workspace-sandbox `
  --enable-docker-execution
```

Review the returned preparation with the existing `run sandbox request|review` flow.
Then resupply the identical command and exact approved identities:

```powershell
cyberagent run standard-code docker-execute <run-id> `
  --generation <n> --checkpoint <checkpoint-id> `
  --preparation <preparation-id> --approval <approval-id> `
  --toolchain go --arg test --arg ./... --purpose "offline tests" `
  --operation-key <execute-key> `
  --enable-permission-control --enable-workspace-sandbox `
  --enable-docker-execution
```

The result uses `standard-code-command-result.v1`: the file result is the exact new
Drydock Checkpoint and logs are bounded receipt metadata. Raw logs are not persisted.
Use `docker-cancel` for an exact admission and `docker-recover` after restart; both
reuse the existing ownership ledger and never start a new attempt during recovery.

For an active `command-runtime.v2` request, the same owned log attachment also
delivers bounded, redacted stdout and stderr to the existing Job manager. The
content is delivered only after its capture receipt is saved and its stream
digests match. This is terminal output delivery, not live streaming; the ordinary
Standard Code response and its durable log receipt remain metadata-only. The Job
manager continues to own its existing output artifacts, cursors, and hashes.

The adapter preserves the actual terminal exit code, including a nonzero test
exit. A saved failed, cancelled, or timed-out outcome remains a failure even if
its process exit code was zero and replay has no new transport error. Capture
limits, invalid/incomplete frames, and the combined Job artifact
byte limit are reported explicitly in stderr. The combined limit includes these
diagnostics and clipping preserves UTF-8 boundaries. A replay with only a Docker
receipt reports that its output is unavailable; it does not attach again or rerun
the command to reconstruct discarded text. A previously saved terminal Job keeps
the output already persisted by the Job manager. Inspect the Job state and exit
code to determine test success, rather than the successful status of a Job query.

The ordinary operator `standard-code` commands keep stdin closed. When the same
backend is selected by `command-runtime.v2`, `stdin_policy=pipe` enables Docker's
non-TTY stdin flags and one input-only attachment to the exact owned running
container. Initial and later `write_stdin` bytes stay in memory, are bounded by the
shared Job manager and backend transport, and are never written to SQLite. The
attachment action is recorded before daemon mutation; timeout, cancellation,
authority drift, process exit, or restart closes the pipe and follows the existing
exact cleanup path.

## Boundary

The only writable host projection is the exact current Drydock root at `/workspace`.
A fixed application-owned regular file is mounted read-only over `/workspace/.git`, so
the linked-worktree control path is neither disclosed nor usable in the container; the
host `.git` file is never replaced. The transport rejects a directory, symlink,
unexpected content, or any generic Standard Code request that omits this mask.

Container root filesystem/toolchains are read-only; user is `65532:65532`;
capabilities are dropped; network is none; environment and credential inputs are
absent; Docker socket, HOME, SSH/Git credentials, browser profiles, devices, ports,
and undeclared mounts are not accepted. The Supervisor command has no fields for any
of them. Toolchain cache/temp data uses one fixed tmpfs outside the host projection:
512 MiB for Go compilation and module preparation, 128 MiB for the other toolchains,
with 16,384 inodes and the same 2 GiB total container memory limit. The runner requires 2 GiB/1,000,000-inode host headroom and
enforces at most 16 MiB and 4,096 entries of aggregate Workspace growth, plus the
16 MiB process file-size ceiling needed by the offline toolchains.

Drydock and its Git worktree are not a process/network/filesystem security sandbox.
They establish ownership, attribution, Checkpoint, and recovery. The fixed Docker
container supplies isolation. Cleanup only touches exact lifecycle-owned containers;
unknown host files/directories and foreign/ambiguous containers are preserved.
