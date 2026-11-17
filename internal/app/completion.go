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
	{"doctor", "Diagnose installation, runtime readiness, and run health", "cyberagent doctor portable|snapshot|bundle|browser-readiness|browser-network-probe|browser-network-review"},
	{"drydock", "Manage deterministic run checkpoints, rewind, fork, and cleanup", "cyberagent drydock status|create|use|checkpoint|rewind|undo|fork|deliver|cleanup|reconcile|gc"},
	{"edit", "Propose and review workspace file edits", "cyberagent edit propose|list|show|review-approve|review-deny|approve|apply|deny"},
	{"exec", "Start a one-shot agent run and print its result", `cyberagent exec --workspace <name> "prompt" [--profile code|review|learn|script] [--route <route>] [--instructions <text>] [--timeout 30m] [--json] [--continue <thread-id>]`},
	{"git-advanced", "Preview and run advanced Git operations against a workspace", "cyberagent git-advanced status|discover-hunks|preview|run"},
	{"git-op", "Review and execute typed local Git mutations bound to a run", "cyberagent git-op stage|unstage|commit|create-branch|switch-branch --run <run-id> [--path p]... [--all] [--message m] [--branch b] --operation-key <key> [--confirm]"},
	{"git-remote", "Review and execute network-scoped Git remote and PR operations", "cyberagent git-remote fetch|pull|push-branch|create-pr|update-pr --run <run-id> --remote <https-url> --branch <name> [--base <name>] [--title <t>] [--body <b>] [--pr-number <n>] [--credential <name>] [--ttl 30s] --operation-key <key> [--confirm]"},
	{"github-review", "Configure credentials and drive GitHub review evidence", "cyberagent github-review configure|credential|login|qualify|fetch|evidence|status|write|recover"},
	{"headless", "Stream headless run events", "cyberagent headless events"},
	{"help", "Show the command list or details for one command", "cyberagent help [command]"},
	{"learn", "Ask the learn route a one-off question", "cyberagent learn ask"},
	{"mcp", "Serve, connect, and configure MCP servers and credentials", "cyberagent mcp serve|client|credential"},
	{"model", "List models and set route bindings", "cyberagent model list|set"},
	{"note", "Create and manage workspace notes", "cyberagent note create|list|show|get|update|archive|restore"},
	{"once-command", "List once-command proposals for a run (legacy)", "cyberagent once-command proposals <run-id>"},
	{"plugin", "Stage, review, trust, and roll back plugins", "cyberagent plugin stage|import-url|import-git|list|show|review|rollback|trust-publisher|revoke-publisher|stage-mcp"},
	{"project-config", "Show and validate the narrowing-only project configuration", "cyberagent project-config show|validate <path> [--profile <p> --max-turns <n> --max-tool-calls <n>]"},
	{"provider", "List, test, and qualify model providers and import pricing", "cyberagent provider list|test|qualify|price-import|price-list"},
	{"report", "Show reports, findings, and checks", "cyberagent report show|finding|check"},
	{"run", "Create, inspect, and drive execution runs", "cyberagent run create|adapt-task|list|show|mode|phase|execution-profile|execution-interaction|execution-permission|browser-cdp-permission|capability-readiness|command-plan|command-execute|host-execute|project-action|command-proposal|events|usage|start|step|execute|checkpoint|graph|lease|finish|fail|pause|resume|cancel|delegations|delegation|specialist-step|plans|plan|delivery|steer|fanouts|fanout|sandbox|standard-code|wake|schedule"},
	{"sandbox", "Validate sandbox templates and local readiness", "cyberagent sandbox validate|template|local-readiness"},
	{"script", "Create and record local script templates and runs", "cyberagent script new|run"},
	{"session", "Manage run-local sessions, checkpoints, forks, and resumes", "cyberagent session create|list|send|history|tree|checkpoint|fork|resume"},
	{"skill", "Manage packaged skills, candidates, selections, and workspace discovery", "cyberagent skill list|discover|show|validate|package|import|import-url|import-git|import-dir|installed|remove|candidate|candidates|select|selection|select-external|external-selection|catalog"},
	{"thread", "Create and manage persistent task threads", "cyberagent thread create|list|show|send|compact|compactions|history|rename|pin|archive|restore|delete|export"},
	{"todo", "Create and manage run todo items", "cyberagent todo create|list|show|update|start|block|reopen|unblock|complete|cancel"},
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

// commandSubcommands lists the first-level dispatch vocabulary for commands
// that take a static subcommand, verified against each command's dispatch
// switch (several entries are absent from the help text). Entries are
// sorted; deeper levels are not completed yet.
var commandSubcommands = map[string][]string{
	"analyzer":       {"execute"},
	"api":            {"openapi", "serve"},
	"approval":       {"approve-for-thread", "approve-once", "deny", "grant", "list", "show"},
	"artifact":       {"list", "read", "show", "verify"},
	"code-intel":     {"qualify", "status"},
	"completion":     {"bash", "zsh"},
	"context":        {"compact", "instructions", "memory", "show"},
	"ctf":            {"analyze", "init", "writeup"},
	"debug":          {"query"},
	"doctor":         {"browser-network-probe", "browser-network-review", "browser-readiness", "bundle", "portable", "snapshot"},
	"drydock":        {"checkpoint", "cleanup", "create", "deliver", "fork", "gc", "reconcile", "rewind", "status", "undo", "use"},
	"edit":           {"apply", "approve", "deny", "list", "propose", "review-approve", "review-deny", "show"},
	"git-advanced":   {"discover-hunks", "preview", "run", "status"},
	"git-op":         {"commit", "create-branch", "stage", "switch-branch", "unstage"},
	"git-remote":     {"create-pr", "fetch", "pull", "push-branch", "update-pr"},
	"github-review":  {"configure", "credential", "evidence", "fetch", "login", "qualify", "recover", "status", "write"},
	"headless":       {"events"},
	"learn":          {"ask"},
	"mcp":            {"client", "credential", "serve"},
	"model":          {"list", "set"},
	"note":           {"archive", "create", "get", "list", "restore", "show", "update"},
	"once-command":   {"proposals"},
	"plugin":         {"import-git", "import-url", "list", "review", "revoke-publisher", "rollback", "show", "stage", "stage-mcp", "trust-publisher"},
	"project-config": {"show", "validate"},
	"provider":       {"list", "price-import", "price-list", "qualify", "test"},
	"report":         {"check", "finding", "show"},
	"run":            {"adapt-task", "browser-cdp-permission", "cancel", "capability-readiness", "checkpoint", "command-execute", "command-plan", "command-proposal", "create", "delegation", "delegations", "delivery", "events", "execute", "execution-interaction", "execution-permission", "execution-profile", "fail", "fanout", "fanouts", "finish", "graph", "host-execute", "lease", "list", "mode", "pause", "phase", "plan", "plans", "project-action", "resume", "sandbox", "schedule", "show", "specialist-step", "standard-code", "start", "steer", "step", "usage", "wake"},
	"sandbox":        {"local-readiness", "template", "validate"},
	"script":         {"new", "run"},
	"session":        {"checkpoint", "create", "fork", "history", "list", "resume", "send", "tree"},
	"skill":          {"candidate", "candidates", "catalog", "discover", "external-selection", "import", "import-dir", "import-git", "import-url", "installed", "list", "package", "remove", "select", "select-external", "selection", "show", "validate"},
	"thread":         {"archive", "compact", "compactions", "create", "delete", "export", "history", "list", "pin", "rename", "restore", "send", "show"},
	"todo":           {"block", "cancel", "complete", "create", "list", "reopen", "show", "start", "unblock", "update"},
	"tool":           {"approve", "deny", "invoke", "list", "schema", "show"},
	"ui-evidence":    {"artifact", "list", "show"},
	"web-evidence":   {"list"},
	"workspace":      {"checkpoint", "init", "list", "read", "show", "tree"},
}

// completionCandidates resolves candidate names for the current position.
// The top-level command position and the argument of 'help' complete
// command names; the first argument of a command with a known static
// subcommand vocabulary completes those subcommands; deeper positions
// return no candidates so the shell never offers a word the CLI would
// reject.
func completionCandidates(context []string, prefix string) []string {
	if len(context) == 0 || (len(context) == 1 && context[0] == "help") {
		return matchedNames(commandNames(), prefix)
	}
	if len(context) == 1 {
		if spec, ok := findCommandSpec(context[0]); ok {
			return matchedNames(commandSubcommands[spec.name], prefix)
		}
	}
	return nil
}

func matchedNames(pool []string, prefix string) []string {
	var matched []string
	for _, name := range pool {
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
  candidates=(${(f)"$(command cyberagent __complete "${(@)words[2,CURRENT]}" 2>/dev/null)"})
  if ((${#candidates[@]})); then
    _describe -t cyberagent-commands 'cyberagent command' candidates
  fi
}

_cyberagent "$@"
`
