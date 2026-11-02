# Initialize project instructions

Build or refresh the workspace `AGENTS.md` so every future task starts from accurate, evidence-backed project instructions. This workflow creates guidance only: it grants no tool, network, approval, or delegation authority, and every write travels through the normal file-edit proposal and authorization path.

## Decide the mode first

Read before writing. The guidance files the runtime actually loads are `AGENTS.override.md`, `AGENTS.md`, `CLAUDE.md`, `.claude/CLAUDE.md`, `.prayu/instructions.md`, and `.prayu/rules/*.md`. `.prayu/config.yaml` is project configuration, not guidance: its presence alone must not switch you to refresh mode. If a guidance file already exists, work in refresh mode: preserve hand-written intent, fix only statements that current evidence disproves, fill clearly missing sections, and keep the existing voice, language, and structure. Rewriting a healthy file is a defect. If none exists, work in initialize mode and propose one new `AGENTS.md`.

A non-empty `AGENTS.override.md` supersedes the `AGENTS.md` in the same directory, and an empty override falls back to it. When a non-empty override is in force, either edit the override or state plainly that a change to `AGENTS.md` would not be read.

## Collect evidence before claiming anything

Use read-only workspace tools to ground every statement:

- Read the top-level README files and the primary manifest for the stack: `go.mod`, `package.json`, `Cargo.toml`, `pyproject.toml`, `requirements.txt`, `pom.xml`, `build.gradle*`, `composer.json`, or their equivalents.
- Read CI or task configuration (`.github/workflows`, `Makefile`, `justfile`, task runners) for the canonical build, test, lint, and format commands.
- List the workspace tree and read key entry points to map directory responsibilities.
- Sample a few representative source files to infer conventions such as naming, error handling, and test layout.

Cite the file each claim comes from. When evidence conflicts, prefer the build system and CI over prose, and record the conflict instead of papering over it.

## Proposed content

Keep `AGENTS.md` short, factual, and copy-pasteable. Suggested sections:

1. One-paragraph project overview: what it is, primary language and stack.
2. Commands: build, test, lint, format, and run, exactly as CI runs them, with any prerequisites (toolchain versions, environment variables, services).
3. Repository map: core directories and what belongs in each.
4. Conventions: code style, error handling, and commit or PR expectations actually observed in the repository.
5. Verification requirements: what must pass before a change is considered complete.
6. Caveats: generated code, lockfiles, platform differences, migration or data-loss risks.

Omit a section when the repository provides no evidence for it, or write "not confirmed from the repository" instead of guessing. The file must not contain secrets, tokens, absolute user paths, or machine-specific data.

## Write through the proposal path

Deliver the draft through the normal file-edit flow: `workspace_change` prepares the proposal, and `workspace_apply` writes bytes only once that proposal is authorized or approved. Proposal, approval, and apply are three states; never claim the file is written before the apply ran. Workspace mutations need the Deliver phase, so in a Plan-phase turn stop at the draft and say so. Summarize what will change first, especially in refresh mode: which sections are new, corrected, or untouched, and which existing lines are preserved verbatim. Project instructions are pinned when a Run is created, so an approved `AGENTS.md` does not change the current Run by itself: it governs the next task, or this one only after the operator refreshes and confirms the Run's instruction snapshot.

If the workspace blocks writes or the operator prefers read-only output, print the proposed `AGENTS.md` content instead of filing a proposal.
