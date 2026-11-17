package app

import (
	"errors"
	"fmt"
	"strings"
)

// commandSpec describes one top-level command for completion and help.
// The table is the single source of truth for command discovery:
// unknown-command suggestions, __complete candidates, and per-command
// help all derive from it. New dispatch cases must register here to
// stay discoverable.
type commandSpec struct {
	name        string
	description string
	usage       string
}

// commandTable lists every top-level dispatch case in lexicographic
// order. The hidden plumbing command "__complete" is deliberately
// excluded: it is a completion protocol, not a user-facing command.
var commandTable = []commandSpec{
	{"analyzer", "Execute the analyzer pass over a run", "cyberagent analyzer execute"},
	{"api", "Serve the HTTP API and print the OpenAPI document", "cyberagent api serve|openapi"},
	{"approval", "Review, approve, deny, and grant tool approvals", "cyberagent approval list|show|approve-once|approve-for-thread|deny|grant"},
	{"artifact", "List, show, read, and verify run artifacts", "cyberagent artifact list|show|read|verify"},
	{"code-intel", "Show code intelligence status and qualification", "cyberagent code-intel status|qualify"},
	{"completion", "Emit shell completion scripts for bash or zsh", "cyberagent completion bash|zsh"},
	{"context", "Compact context and manage instructions and memory", "cyberagent context compact|show|instructions|memory"},
	{"ctf", "Scaffold CTF challenge workspaces, analysis worksheets, and writeups", "cyberagent ctf init|analyze|writeup"},
	{"debug", "Run bounded debug queries against a run", "cyberagent debug query --run <run-id> [bounded filters] [--json]"},
	{"doctor", "Diagnose installation, runtime readiness, and run health", "cyberagent doctor portable [--json]"},
	{"drydock", "Manage deterministic run checkpoints, rewind, fork, and cleanup", "cyberagent drydock status|create|use|checkpoint|rewind|undo|fork|deliver|cleanup|reconcile|gc"},
	{"edit", "Propose and review workspace file edits", "cyberagent edit propose|list|show|review-approve|review-deny|approve|deny"},
	{"exec", "Start a one-shot agent run and print its result", `cyberagent exec --workspace <name> "prompt" [--profile code|review|learn|script] [--route <route>] [--instructions <text>] [--timeout 30m] [--json] [--continue <thread-id>]`},
	{"git-advanced", "Preview and run advanced Git operations against a workspace", "cyberagent git-advanced status|discover-hunks|preview|run"},
	{"git-op", "Review and execute typed local Git mutations bound to a run", "cyberagent git-op stage|unstage|commit|create-branch|switch-branch --run <run-id> [--path p]... [--all] [--message m] [--branch b] --operation-key <key> [--confirm]"},
	{"git-remote", "Review and execute network-scoped Git remote and PR operations", "cyberagent git-remote fetch|pull|push-branch|create-pr|update-pr --run <run-id> --remote <https-url> --branch <name> --operation-key <key> [--confirm]"},
	{"github-review", "Configure credentials and drive GitHub review evidence", "cyberagent github-review configure|credential|login|qualify|fetch|evidence|status|write|recover"},
	{"headless", "Stream headless run events", "cyberagent headless events"},
	{"help", "Show the command list or details for one command", "cyberagent help [command]"},
	{"learn", "Ask the learn route a one-off question", "cyberagent learn ask"},
	{"mcp", "Serve, connect, and configure MCP servers and credentials", "cyberagent mcp serve|client|credential"},
	{"model", "List models and set route bindings", "cyberagent model list|set"},
	{"note", "Create and manage workspace notes", "cyberagent note create|list|show|update|archive|restore"},
	{"once-command", "List once-command proposals for a run (legacy)", "cyberagent once-command proposals <run-id>"},
	{"plugin", "Stage, review, trust, and roll back plugins", "cyberagent plugin stage|list|show|review|rollback|trust-publisher|revoke-publisher|stage-mcp"},
	{"project-config", "Show and validate the narrowing-only project configuration", "cyberagent project-config show|validate <config-path>"},
	{"provider", "List, test, and qualify model providers and import pricing", "cyberagent provider list|test|qualify"},
	{"report", "Show reports, findings, and checks", "cyberagent report show|finding|check"},
	{"run", "Create, inspect, and drive execution runs", "cyberagent run create|adapt-task|list|show|mode|phase|execution-profile|execution-interaction|execution-permission|capability-readiness|command-plan|command-execute|host-execute|project-action|events|usage|start|step|execute|checkpoint|graph|lease|finish|fail|pause|resume|cancel|delegations|delegation|plans|plan|delivery|steer|fanouts|fanout|sandbox|wake|schedule"},
	{"sandbox", "Validate sandbox templates and local readiness", "cyberagent sandbox validate|template|local-readiness"},
	{"script", "Create and record local script templates and runs", "cyberagent script new|run"},
	{"session", "Manage run-local sessions, checkpoints, forks, and resumes", "cyberagent session create|list|send|history|tree|checkpoint|fork|resume"},
	{"skill", "Manage packaged skills, candidates, selections, and workspace discovery", "cyberagent skill list|discover|show|validate|package|import|installed|remove|candidate|candidates|select|selection|select-external|external-selection"},
	{"thread", "Create and manage persistent task threads", "cyberagent thread create|list|show|send|history|rename|pin|archive|restore|delete|export"},
	{"todo", "Create and manage run todo items", "cyberagent todo create|list|show|update|start|block|reopen|complete|cancel"},
	{"tool", "Inspect, invoke, approve, and deny gateway tools", "cyberagent tool schema|invoke|list|show|approve|deny"},
	{"tui", "Open the terminal UI for a run or session", "cyberagent tui [--run <run-id> | --session <session-id>] [--print]"},
	{"ui-evidence", "Inspect UI evidence artifacts", "cyberagent ui-evidence list|show|artifact"},
	{"version", "Print the product name and version", "cyberagent version"},
	{"web-evidence", "Inspect collected web evidence for a run", "cyberagent web-evidence list --run <run-id> [--limit <n>]"},
	{"workspace", "Initialize and inspect source workspaces", "cyberagent workspace init|list|show|tree|read|checkpoint"},
}

func commandNames() []string {
	names := make([]string, 0, len(commandTable))
	for _, spec := range commandTable {
		names = append(names, spec.name)
	}
	return names
}

func findCommandSpec(name string) (commandSpec, bool) {
	for _, spec := range commandTable {
		if spec.name == name {
			return spec, true
		}
	}
	return commandSpec{}, false
}

// completeCommand implements the hidden "__complete" protocol consumed by
// the generated shell scripts: every argument is a word from the command
// line and the last one is the word being completed. Candidates print one
// per line; an empty result means no completion.
func (a *App) completeCommand(args []string) error {
	var context []string
	var prefix string
	if len(args) > 0 {
		context = args[:len(args)-1]
		prefix = args[len(args)-1]
	}
	for _, candidate := range completionCandidates(context, prefix) {
		fmt.Fprintln(a.out, candidate)
	}
	return nil
}

// completionCandidates resolves candidate command names for the current
// position. v1 completes the top-level command position and the argument
// of 'help'; deeper positions return no candidates so the shell never
// offers a word the CLI would reject.
func completionCandidates(context []string, prefix string) []string {
	if len(context) > 1 || (len(context) == 1 && context[0] != "help") {
		return nil
	}
	var matched []string
	for _, name := range commandNames() {
		if strings.HasPrefix(name, prefix) {
			matched = append(matched, name)
		}
	}
	return matched
}

// printCommandHelp renders the table entry for one command; unknown names
// fall through to the same suggestion error as the dispatch default.
func (a *App) printCommandHelp(name string) error {
	spec, ok := findCommandSpec(name)
	if !ok {
		return a.unknownCommandError(name)
	}
	fmt.Fprintf(a.out, "%s - %s\n", spec.name, spec.description)
	fmt.Fprintf(a.out, "usage: %s\n", spec.usage)
	fmt.Fprintln(a.out, "Run 'cyberagent help' for the full command list.")
	return nil
}

func (a *App) completionCommand(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: cyberagent completion bash|zsh")
	}
	switch args[0] {
	case "bash":
		fmt.Fprint(a.out, bashCompletionScript)
	case "zsh":
		fmt.Fprint(a.out, zshCompletionScript)
	default:
		return fmt.Errorf("unknown shell %q; supported shells: bash, zsh", args[0])
	}
	return nil
}

// bashCompletionScript emits a bash completer that delegates candidate
// generation to 'cyberagent __complete'. The ${context_words[@]+...} guard
// keeps the unset-array expansion safe under 'set -u' on older bash.
const bashCompletionScript = `# bash completion for cyberagent
#
# Delegates candidate generation to 'cyberagent __complete'; the binary
# filters by the current word and compgen filters again locally.

_cyberagent_completions() {
  local cur
  local -a context_words
  local i
  COMPREPLY=()
  cur="${COMP_WORDS[COMP_CWORD]}"
  for ((i = 1; i < COMP_CWORD; i++)); do
    context_words+=("${COMP_WORDS[i]}")
  done
  local candidates
  candidates="$(command cyberagent __complete ${context_words[@]+"${context_words[@]}"} "$cur" 2>/dev/null)" || return 0
  COMPREPLY=($(compgen -W "$candidates" -- "$cur"))
}

complete -F _cyberagent_completions cyberagent
`

// zshCompletionScript emits a zsh completer that delegates candidate
// generation to 'cyberagent __complete'.
const zshCompletionScript = `#compdef cyberagent
# Delegates candidate generation to 'cyberagent __complete'.

_cyberagent() {
  local -a candidates
  candidates=(${(f)"$(command cyberagent __complete "${(@)words[2,-1]}" 2>/dev/null)"})
  if ((${#candidates[@]})); then
    _describe -t cyberagent-commands 'cyberagent command' candidates
  fi
}

_cyberagent "$@"
`
