# Initialize project instructions

Build or refresh `AGENTS.md` from repository evidence. This creates guidance only, grants no tool, network, approval, or delegation authority, and uses the normal proposal-and-authorization path.

## Decide the mode first

Use the requested directory, defaulting to the workspace root. From confirmed discovery, refresh guidance owned by that directory; if none exists, initialize its `AGENTS.md`. A descendant's `AGENTS.md` alone must not block root initialization. In refresh mode preserve hand-written intent, voice, language, and structure; correct disproved statements and fill clear gaps. Leave healthy text intact.

Read applicable guidance along the ancestor chain from root to target: `AGENTS.override.md`, `AGENTS.md`, `CLAUDE.md`, `.claude/CLAUDE.md`, `.prayu/instructions.md`, and `.prayu/rules/**/*.md`. Recursively nested rules share the scope of the directory owning `.prayu`; other descendant guidance is not global. Preserve inherited guidance. `.prayu/config.yaml` alone is configuration, not guidance.

A non-empty, non-ignored `AGENTS.override.md` supersedes same-directory `AGENTS.md`; empty or ignored overrides fall back to it. If an override is effective, edit it or explain why editing `AGENTS.md` would not be read. `.prayu/instructions.ignore` can exclude paths by glob. Ask before editing that list or switching targets.

Workspace read/list/glob/grep hide dot paths except `.github`; they cannot inspect `.prayu` or `.claude`. Inaccessible does not mean absent. Use confirmed runtime discovery results or ask the operator for effective/ignored sources for the target and contents needed to refresh. If discovery remains incomplete, keep mode and target effectiveness unconfirmed; do not claim the proposed file will be loaded.

## Collect evidence before claiming anything

Use read-only workspace tools:

- Read README files and stack manifests such as `go.mod`, `package.json`, `Cargo.toml`, or equivalents.
- Read CI and task configuration (`.github/workflows`, `Makefile`, task runners) for build, test, lint, and format commands.
- List the tree and read entry points to map directory responsibilities.
- Sample representative source files for naming, error handling, and test conventions.

Cite each claim's source file. Record conflicts, preferring build systems and CI over prose.

## Proposed content

Keep guidance short, factual, and copy-pasteable. Suggested sections:

1. Project overview: purpose, language, and stack.
2. Commands: build, test, lint, format, and run as configured, with toolchain, environment, and service prerequisites.
3. Repository map: directories and responsibilities.
4. Observed code, error handling, commit, and PR conventions.
5. Verification required before completion.
6. Caveats: generated code, lockfiles, platforms, migrations, or data-loss risks.

Omit unsupported sections or say "not confirmed from the repository". Include no secrets, tokens, absolute user paths, or machine-specific data.

## Write through the proposal path

Summarize additions, corrections, and preserved text before proposing changes. `workspace_change` prepares the proposal; `workspace_apply` writes bytes only after authorization or approval. Distinguish proposal, approval, and apply; claim a write only when a successful `workspace_apply` result confirms the mutation. Failure or missing confirmation does not establish a write. Mutations require Deliver; in Plan stop at the draft. Instructions are pinned when a Run is created: changes govern the next task, or this Run after the operator refreshes and confirms its instruction snapshot.

If the workspace blocks writes or the operator prefers read-only output, print the proposed content instead of filing a proposal.
