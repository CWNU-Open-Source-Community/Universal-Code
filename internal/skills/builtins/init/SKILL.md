# Initialize project instructions

Build or refresh the workspace `AGENTS.md` so every future task starts from accurate, evidence-backed project instructions. This workflow creates guidance only: it grants no tool, network, approval, or delegation authority, and every write travels through the normal file-edit proposal and operator approval path.

## Decide the mode first

Read before writing. Check, in order, `AGENTS.override.md`, `AGENTS.md`, `CLAUDE.md`, `.claude/CLAUDE.md`, and `.prayu/config.yaml` plus any `.prayu` guidance file. If an instruction file already exists, work in refresh mode: preserve hand-written intent, fix only statements that current evidence disproves, fill clearly missing sections, and keep the existing voice, language, and structure. Rewriting a healthy file is a defect. If none exists, work in initialize mode and propose one new `AGENTS.md`.

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

Deliver the draft as a `replace_file` proposal for operator review. Summarize what will change first, especially in refresh mode: which sections are new, corrected, or untouched, and which existing lines are preserved verbatim. Never claim the file has been written; the proposal applies only after approval. Project instructions are captured when a Run is created, so an approved `AGENTS.md` takes effect from the next task, not retroactively for the current one.

If the workspace blocks writes or the operator prefers read-only output, print the proposed `AGENTS.md` content instead of filing a proposal.
