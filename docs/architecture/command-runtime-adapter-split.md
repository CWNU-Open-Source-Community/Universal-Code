# Command Runtime adapter split (issue #134)

`command-runtime.v2` remains one adapter-neutral model schema and one shared Job
state machine. The model, repository, Skill, MCP server, persisted Job, and restart
recovery path cannot request an adapter. Go selects at most one installed adapter
from the current Run profile and permission, and advertises it only for a running
root Code/Deliver turn with an active execution lease.

Model results may include `candidate_urls`, extracted from the sanitized output
pages returned by that call. Each entry has `job_id`, `url`,
`source: command_output`, and `verified: false`. The operator service projection
and model result share the same loopback normalization and credential/query
filtering. Candidates are bounded to eight entries and deduplicated per Job;
identical URLs from different Jobs retain their separate identities. A candidate
does not prove readiness, grant browser permission, or trigger a network probe.
`start` need not have output yet; use `read` or `wait` on the original Job.

On Windows Host, `browser_navigate.v2` can supply `service_job_id` with a
candidate URL. The shared runtime checks the live Job and its current authority,
requires the URL in that Job's retained output, and verifies the TCP listener is
in its retained Job Object. A bounded two-second HTTP GET must return 2xx before
navigation; it follows no redirects and uses no proxy or credentials. The browser
must remain on the verified origin. The returned `service` observation and
`navigated` flag distinguish a responding service from an unready or unsupported
backend. This probe grants no permissions and does not verify page behavior.

### Services that coexist with edits

For a persistent development server, `action=start` accepts the optional command
field `workspace_mode: shared_service`. Omission or `exclusive` keeps the ordinary
command behavior: its mutation boundary lasts until exit. `shared_service` is
rejected for foreground `run` batches. The mode is bound into the reviewed command
fingerprint and durable launch intent, and appears in its Job projection.

A shared service retains its existing process, filesystem and network permissions.
It can write files. Its launch checkpoint closes after dispatch; later edits keep
their own checkpoints and the existing file-hash conflict checks. Captures made
while a service can still write are marked as partially attributable, including
services from other Runs using the same physical workspace. Ignored caches retain
the existing capture policy; concurrent changes to tracked or non-ignored files
can still produce a conflict.

Reading, cancelling or recovering an exited service does not replay its launch
checkpoint over later edits. Before restoring or forking a checkpoint, services
using that physical workspace must be terminal with their process trees confirmed
reaped. Readiness remains a separate observation of a live, owned listener: the
mode itself does not prove that the server is ready or that a page works.

Settings > Permissions exposes the current Thread's service preference and live
service state separately. `auto` lets the agent select shared mode for a specific
development or preview server. `exclusive` rejects new shared-service Jobs and
revokes existing ones at their next ownership check. Saving a preference does
not change execution permission or the selected sandbox. The UI reports actual
process cleanup; it does not mark a service stopped merely because the preference
was saved. Preferences survive restart, while process execution authority follows
its existing lifetime. Stale settings writes require reloading the current revision.

## Adapter identities and permissions

Every advertisement, durable Supervisor call, Job, output page, Artifact, and
execution result carries one exact identity:

- `kind`: `sandboxed_workspace`, `host_unsandboxed`, or the non-executable
  historical projection `legacy_unbound`;
- backend family, installed backend identity, and process generation;
- effective isolation grade, network policy, and credential policy.

`sandboxed_workspace` accepts current Ask/Auto under native admission and reports
`workspace_sandbox`, `network=denied`, and `credentials=none`.
`host_unsandboxed` requires modern Full, live process activation and the
danger startup gate, and
truthfully reports that host network and host credentials remain available. The
input fields `network=disabled` and `credentials=none` are intent and Policy facts
for that host adapter, not isolation evidence. A receipt cannot change kind without
also satisfying the complete identity consistency check.

The authority snapshot is issued when the tool is advertised and is stored in the
Supervisor tool-call ledger. Gateway execution decodes that closed JSON object and
requires the same Run, adapter kind, backend identity, and generation. The
Application layer then rechecks Run, Mission, Session, root Agent, Code/Deliver
mode, profile and permission revisions, Drydock/root fingerprint, lease identity
and generation, and process-owned adapter generation before every operation.
Full authority is activated dynamically for the current task and fenced on
permission drift. The Debug interaction retains its explicit terminal lease; it
does not supply another execution permission or restore an old `debug` snapshot.

## Sandboxed backends

The Windows Local adapter compiles the normalized command into the existing
AppContainer/LPAC Local backend. It is installed only after the AppContainer, WFP,
Job Object, ACL, and runtime-generation readiness proof succeeds. A current Run is
advertised only for `local + Ask/Auto + controlled`; execution mounts the
exact Run-owned Drydock at `/workspace` and a read-only executable toolchain root.

The `process` profile accepts native development runtimes such as Node.js and
Python with literal argv, under the same executable hash and Workspace boundary
as other native programs. The read-only toolchain input remains the selected
executable's directory; selecting a shell does not make a separate host runtime
directory visible. Shells, system script hosts, and system/privilege brokers remain
excluded as process targets. Removing the former language-runtime name blacklist
does not replace isolation: already-allowed programs such as `go test` execute
project code. LPAC/WFP, input mounts, Policy, permissions, and leases remain the
actual Local boundary. The shared normalizer also applies to the existing Full
Host adapter, which remains explicitly unsandboxed; this change
neither grants that authority nor supplies a Host fallback.

The adapter uses the existing Local default disk-write budget of 2 GiB per
command. Windows Job accounting counts cumulative writes across the process
tree; the post-exit check also bounds positive workspace growth plus runtime
scratch, including compiler cache and temporary files. This is not a 2 GiB
allocation or a workspace-only growth promise. The previous use of Docker's
16 MiB workspace-growth constant incorrectly included cold compilation in that
small budget. Docker retains its separate fixed workspace and cache limits.

### Known Windows Local runtime compatibility limits

Accepting a native runtime is not a guarantee that its default test runner or
complete SDK works under LPAC. The 2026-09-13 Node 24.19.0 / libuv 1.52.1 probes
separate three limitations: a SYSTEM-owned installation may deny the current
process `WRITE_DAC` needed for the exact toolchain root grant; libuv's older
non-`LOCAL` named-pipe namespace prevents piped child-process startup inside an
AppContainer; and Node's module resolver can require ancestor-directory metadata
outside the mounted workspace. These are separate boundaries, not evidence that
the program should receive Full Access.

With an existing user-owned Node installation, `--version`, a builtin `-e`
script, and child processes using inherited/ignored stdio succeeded. Default
`node --test` and a piped child timed out; `--test-isolation=none` instead reached
an `EPERM` on ancestor `lstat('D:\\')`, so it is not a verified workaround.
The [upstream libuv fix](https://github.com/libuv/libuv/pull/5181) is merged but
was not in a released libuv version at this audit; checked Node tags 24.19.0,
24.21.0 and 26.8.0 still contained the old pipe implementation. This audit adds
no implicit root grants, IPC bridge, runtime staging, permission changes or Host
fallback. Exact versions, result receipts and remaining limits are recorded in
the [Node LPAC compatibility evidence](../../output/playwright/ux-context-reliability/node-lpac-compatibility.md).

The Docker adapter compiles only the fixed Go, Node, Python, or Rust Standard Code
toolchains into the existing fixed-image `network=none` backend. Image, endpoint,
user, mounts, environment, resource limits, and Docker flags remain Go-owned. It
uses a persisted v2 sandbox execution candidate bound to the exact active Run lease;
candidate validation, lifecycle creation, preflight, evidence, admission, and the
final pre-start check all reject lease ID, owner, generation, status, or expiry
drift. The ordinary operator flow continues to use a quiescent candidate, so this
bridge does not weaken the existing Sandbox transaction.

Both backends reuse foreground/background Job creation, cursor output, timeout,
cancel/kill, owner heartbeat, bounded Artifact capture, and restart reconciliation.
Sandbox Jobs deliberately persist no host PID or process group. Backend cancellation
must return only after its complete process/container tree is reaped. The shared Job
manager owns a bounded stdin pipe for `stdin_policy=pipe`, serializes initial and
interactive writes, and closes it on EOF, timeout, cancel, kill, or owner shutdown.
Windows Local copies that pipe into the AppContainer child's sole inherited stdin
handle. Docker binds the policy into `standard-code-docker-runner.v2`, opens only the
exact owned running container's input attachment, and fences the metadata-only
`attach_stdin` action in schema v132's lifecycle WAL. Input bytes and handles are
never persisted, so restart cannot adopt or replay them and instead converges through
normal container cleanup.

Local commands use the shared Workspace Checkpoint boundary. Docker Standard Code
already owns the exact Drydock checkpoint transaction, so the outer Command Runtime
does not create a second competing checkpoint.

## Restart and legacy data

Schema v131 adds the complete adapter identity to the v116 Job ledger and permits
PID-less sandbox Job states. All pre-v131 rows are migrated to `legacy_unbound`.
They remain listable/readable evidence but cannot count as active capacity, acquire
process ownership, execute, accept stdin, or be adopted after restart. New active
Jobs also are never adopted from a persisted PID: owner loss converges them to
`interrupted` through the existing reconciliation rules. Schema v132 changes no Job
payload and stores no input; it only admits the fenced Docker lifecycle attach verb.

## Product projections

CLI, HTTP, and Desktop expose separate facts:

- the `command-runtime.v2` protocol is compiled into the product;
- one or more exact adapters are installed and their backends are ready;
- the current Run is actually granted one adapter under its present durable
  snapshots and active lease.

Global runtime capabilities list installed adapter receipts without granting them.
`run_capability_readiness.v1` adds the current-Run projection. Persisted readiness,
an installed backend, or a selected permission never becomes execution authority.

## Universal Code process dispatch increment

New application `command_runtime` starts and stdin writes now use the common
`executionauth.PolicyAuthorizer`. The host binds the final resolved executable,
argv, environment, cwd, input, adapter generation and native scope to a transient
operation. Secret-bearing inputs use a fresh private HMAC key, which is not
persisted. Plugin declarations and a host working directory do not attest
isolation: an unsandboxed process retains unknown effects. A sandbox is considered
bounded only through the already installed, validated adapter and Drydock path.

The Job manager checks the operation after durable preparation. Windows and POSIX
host starters check again before creating the process, then revalidate executable
bytes and the launch directory. Local and Docker sandbox bridges check before
calling their existing execution boundary; their lower-level admission and lease
checks remain in force. These are live checks, not an atomic lock shared with an
OS syscall or container service. Revocation after dispatch can encounter an
in-flight effect; existing process ownership, reconciliation and tree cleanup
remain responsible for stopping owned work, without claiming to undo its effects.

Background process lifetime may outlive a request. Before process dispatch, the
original request's cancellation remains enforced even through a detached context.
For Docker, Windows Local, and SBX, `start` waits for the adapter's final dispatch
admission before returning its running Job. The manager first installs the Job's
output collectors, ownership heartbeat, timeout and initial-stdin workers, so
waiting for admission does not block backend output or input. A process-local
callback confirms admission while the originating model attempt is still active;
SBX confirms only after its final VM identity check, immediately before exec.
Preparation failure, cancellation or timeout ends that wait and retains the owned
Job's cleanup and terminal evidence. Model payloads and persisted receipts cannot
supply this callback or authorize a later dispatch under an old attempt.
After the manager has durably committed the running Job and started its ownership,
wait and timeout workers, asynchronous initial stdin uses the Job lifetime. A
completed HTTP request no longer cancels those already-bound initial bytes. This
handoff detaches request cancellation only: the same operation's authority check
still re-reads current permission, activation and lease state before writing.
Initial stdin and subsequent writes recheck authority; later writes do so after
acquiring the per-Job input gate. Existing exact Job/input replays return their original receipt
without consuming another dispatch grant. Failed or uncertain effects are not
automatically retried. Cleanup-only cancellation remains usable after revocation.

Retired in this increment: the application-only, pre-`Start` authorization checks
in foreground and background launch paths, and the application's direct unguarded
`WriteStdin` call. They are replaced by operation-bound checks at the effect
boundary. The runner's compatibility methods remain for existing internal callers
and tests; they do not decode grants from persisted state or model input.

Public ask/auto/full writers and production MCP stdio/HTTP operation checks are
now wired. The shared policy classifies effects verified by the host: Ask permits
verified workspace reads, reversible writes, and bounded workspace processes;
Auto additionally permits verified public-network effects. Auto does not invoke
a model to approve operations. Full still requires current runtime activation and
preserves native guards and existing exact-consent decisions; it does not select
or create an OS sandbox. These names do not claim equivalence with modes in other
agent products.

Legacy Host/Risk/Controlled/Once execution paths are retired; historical rows
and saved-outcome readers remain. See [ADR 0165](../adr/0165-retire-legacy-command-execution.md).
Existing Code/Deliver/root and native admission checks remain in force. This
change creates no new Run, Session, Job or approval database.
