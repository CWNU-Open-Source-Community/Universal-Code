# ADR 0176: Project dependency preparation in the selected sandbox

Status: implementation

## User flow

The project preparation panel detects an exact project directory, displays its
toolchain, lock files, installation and test commands, and the public package
origins required by the lock. The operator authorizes those origins for that
preparation. An empty selection uses verified cached packages only. The web
access preference remains independent.

The first supported package formats are npm package-lock v2/v3 and Go modules
with go.sum. Other detected package managers receive a specific preparation
instruction. The product uses installed toolchains and the selected Local,
Docker Engine, or Docker Sandboxes runtime. Host execution is not selected by
this flow.

npm workspaces use the root package-lock v2/v3 and bind every discovered member
manifest along with root configuration. Selecting a member prepares the whole
workspace at its shared root, then runs tests in the selected member. Only
lock-declared internal links to those bound members are accepted. npm-installed
symlinks and Windows junctions are checked during finalization; command wrappers
refer directly to the bound package files. Local dependency links outside the
declared workspace remain unavailable.

## Downloads and execution

Go fetches the exact lock-bound artifacts through the existing public HTTPS,
DNS-pinned transport. Every redirect needs an authorized origin. Downloads have
per-artifact and aggregate bounds and never attach credentials. npm SRI and Go
module h1 checksums verify downloads and cache hits. The shared application
cache contains download bytes; per-project copies are writable by the sandbox.

Installation runs offline through the existing Command Runtime. Package source
approval grants these downloads; it does not give arbitrary subprocesses a
network connection. An optional, separate operator choice runs dependency
installation scripts inside the same sandbox after materializing dependencies.
The chosen toolchain, current permission and exact Run execution lease remain
required. No model or repository field can supply operator consent.

## Persistence and recovery

Schema v193 adds immutable preparation intents and bounded progress/results.
The intent binds the Run, target, inspected file hashes, backend, approved
origins, script selection and operator. Reusing a request key returns the same
operation; a different intent conflicts. Downloads and command dispatch run
under the existing Run lease. Browser disconnection leaves the owned operation
running. Cancellation is durable and stops the owned work.

Existing Command Runtime Jobs retain execution, output and cleanup facts.
Interrupted operations are displayed for explicit retry; reopening the page or
replaying the request does not launch another install. A fresh retry rechecks
the project, authorization and cached content.

## Acceptance

Verification covers missing dependencies, denied origins and redirects, corrupt
cache entries, changed lock files, real offline installation and testing,
nonzero tests followed by repair, cancellation/reopening, and final delivery.
The acceptance report records the actual runtime and model used, completed
cases and manual interventions. Unsupported or unexecuted combinations remain
separate from successful cases.
