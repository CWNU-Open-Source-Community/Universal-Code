package app

import (
	"errors"
	"fmt"
	"io"
	"os"
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
	{"completion", "Emit shell completion scripts for bash, zsh, or fish", "cyberagent completion bash|zsh|fish"},
	{"context", "Compact context and manage instructions and memory", "cyberagent context compact|show|instructions|memory"},
	{"ctf", "Scaffold CTF challenge workspaces, analysis worksheets, and writeups", "cyberagent ctf init|analyze|writeup"},
	{"debug", "Run bounded debug queries against a run", "cyberagent debug query --run <run-id> [bounded filters] [--json]"},
	{"doctor", "Diagnose installation, runtime readiness, and run health", "cyberagent doctor portable|snapshot|bundle|browser-readiness|browser-network-probe|browser-network-review"},
	{"drydock", "Manage deterministic run checkpoints, rewind, fork, and cleanup", "cyberagent drydock status|create|use|checkpoint|rewind|undo|fork|deliver|cleanup|reconcile|gc"},
	{"edit", "Propose and review workspace file edits", "cyberagent edit propose|list|show|review-approve|review-deny|approve|apply|deny"},
	{"exec", "Start a one-shot agent run and print its result", execUsage},
	{"git-advanced", "Preview and run advanced Git operations against a workspace", "cyberagent git-advanced status|discover-hunks|preview|run"},
	{"git-op", "Review and execute typed local Git mutations bound to a run", "cyberagent git-op stage|unstage|commit|create-branch|switch-branch --run <run-id> [--path p]... [--all] [--message m] [--branch b] --operation-key <key> [--confirm]"},
	{"git-remote", "Review and execute network-scoped Git remote and PR operations", "cyberagent git-remote fetch|pull|push-branch|create-pr|update-pr --run <run-id> --remote <https-url> --branch <name> [--base <name>] [--title <t>] [--body <b>] [--pr-number <n>] [--credential <name>] [--ttl 30s] --operation-key <key> [--confirm]"},
	{"github-review", "Configure credentials and drive GitHub review evidence", "cyberagent github-review configure|credential|login|qualify|fetch|evidence|status|write|recover"},
	{"headless", "Stream headless run events", "cyberagent headless events"},
	{"help", "Show the command list or details for one command", "cyberagent help [command]"},
	{"hooks", "Inspect registered lifecycle hooks", "cyberagent hooks list|validate"},
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

// Completion directives mirror cobra's values so generic tooling can
// interop with the protocol: 0 lets the shell add its default completion
// (file paths at positions the CLI does not complete) and 4 suppresses it
// wherever the CLI already provided candidates.
const (
	completionDirectiveDefault    = 0
	completionDirectiveNoFileComp = 4
)

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

// completeProtocolEnvironment is the handshake shell scripts use to declare
// they understand the trailing ":<directive>" line. Consumers generated
// before the directive protocol — including scripts already installed by
// earlier releases — receive plain candidate lines only, so upgrading the
// binary alone never leaks protocol markers like ":4" into the offered
// candidates.
const completeProtocolEnvironment = "CYBERAGENT_COMPLETE_PROTOCOL"

// completeCommand implements the hidden "__complete" protocol consumed by
// the generated shell scripts: every argument is a word from the command
// line and the last one is the word being completed. Candidates print one
// per line; an empty result means no completion. The trailing directive
// line is only emitted when the caller opted into the protocol.
func (a *App) completeCommand(args []string) error {
	var context []string
	var prefix string
	if len(args) > 0 {
		context = args[:len(args)-1]
		prefix = args[len(args)-1]
	}
	candidates, directive := completionCandidates(context, prefix)
	for _, candidate := range candidates {
		fmt.Fprintln(a.out, candidate)
	}
	if os.Getenv(completeProtocolEnvironment) == "4" {
		fmt.Fprintf(a.out, ":%d\n", directive)
	}
	return nil
}

// commandSubcommands lists the verified dispatch vocabulary: first-level
// subcommands under their command name, and second-level group arguments
// under "command group" keys, verified against each nested dispatch switch
// (several entries are absent from the help text). Entries are sorted;
// deeper levels are not completed yet.
var commandSubcommands = map[string][]string{
	"analyzer":       {"execute"},
	"api":            {"openapi", "serve"},
	"approval":       {"approve-for-thread", "approve-once", "deny", "grant", "list", "show"},
	"artifact":       {"list", "read", "show", "verify"},
	"code-intel":     {"qualify", "status"},
	"completion":     {"bash", "fish", "zsh"},
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
	"hooks":          {"list", "validate"},
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

	// Two-level groups: "command group" keys complete the first argument
	// after the group, mirroring the nested dispatch switches.
	"context instructions": {"scaffold"},
	"context memory":       {"create", "delete", "disable", "edit", "enable", "export", "list", "show"},
	"run delivery":         {"checkpoint", "list", "record", "report", "show"},
	"run fanout":           {"execute", "execution", "plan", "report", "show"},
	"run plan":             {"choose", "selection", "show"},
	"run sandbox": {
		"begin", "cancel", "candidate", "candidate-show",
		"candidates", "cleanup", "docker-admit", "docker-attempt-resume",
		"docker-attempt-show", "docker-attempts", "docker-cancel", "docker-host-input-handoff-show",
		"docker-host-input-handoffs", "docker-host-input-show", "docker-host-inputs", "docker-plan",
		"docker-plan-show", "docker-plans", "docker-production-evidence-attempt-resume", "docker-production-evidence-attempt-show",
		"docker-production-evidence-attempts", "docker-production-evidence-capture", "docker-production-evidence-captures", "docker-production-evidence-review",
		"docker-production-evidence-review-show", "docker-production-evidence-reviews", "docker-production-evidence-show", "docker-readiness",
		"docker-rehearsal-show", "docker-rehearsals", "docker-rehearse", "docker-runtime-input-application-show",
		"docker-runtime-input-applications", "docker-runtime-input-apply", "docker-runtime-input-apply-resume", "docker-runtime-input-plan",
		"docker-runtime-input-plan-show", "docker-runtime-input-plans", "docker-runtime-input-resource-cleanup", "docker-runtime-input-resource-cleanup-resume",
		"docker-runtime-input-resource-cleanup-show", "docker-runtime-input-resource-cleanups", "docker-runtime-input-resource-inspect", "docker-runtime-input-resource-inspection-show",
		"docker-runtime-input-resource-inspections", "docker-start", "docker-start-gate-review", "docker-start-gate-review-show",
		"docker-start-gate-reviews", "docker-status", "evidence", "evidence-show",
		"evidences", "execution-show", "executions", "list",
		"observation-show", "observations", "observe", "output-simulate",
		"output-simulation-show", "output-simulations", "preflight", "preflight-show",
		"preflights", "prepare", "request", "review",
		"show",
	},
	"run standard-code": {"docker-cancel", "docker-execute", "docker-prepare", "docker-readiness", "docker-recover", "pause-and-configure", "preset"},
	"skill catalog":     {"audit", "disable", "enable", "list", "pin", "revoke", "trust"},
}

// completionCandidates resolves candidate names for the current position.
// The top-level command position and the argument of 'help' complete
// command names; the first argument of a command with a known static
// subcommand vocabulary completes those subcommands; deeper positions
// return no candidates so the shell never offers a word the CLI would
// reject.
// completionCandidates resolves the candidates and the shell directive for
// the current position: positions with candidates suppress the shell's
// file completion, and empty positions hand control back to the shell so
// path-shaped arguments (skill import's package, workspace read's file)
// still complete, cobra-style.
func completionCandidates(context []string, prefix string) ([]string, int) {
	var pool []string
	switch {
	case len(context) == 0 || (len(context) == 1 && context[0] == "help"):
		pool = commandNames()
	case len(context) == 1:
		if spec, ok := findCommandSpec(context[0]); ok {
			pool = commandSubcommands[spec.name]
		}
	case len(context) == 2:
		pool = commandSubcommands[context[0]+" "+context[1]]
	}
	matched := matchedNames(pool, prefix)
	if len(matched) > 0 {
		return matched, completionDirectiveNoFileComp
	}
	return nil, completionDirectiveDefault
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
		return errors.New("usage: cyberagent completion bash|zsh|fish")
	}
	switch args[0] {
	case "bash":
		fmt.Fprint(a.out, bashCompletionScript)
	case "zsh":
		fmt.Fprint(a.out, zshCompletionScript)
	case "fish":
		if _, err := io.WriteString(a.out, fishCompletionScript); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown shell %q; supported shells: bash, zsh, fish", args[0])
	}
	return nil
}

// bashCompletionScript emits a bash completer that delegates candidate
// generation to 'cyberagent __complete' under the CYBERAGENT_COMPLETE_
// PROTOCOL=4 handshake so the binary emits the trailing directive line.
// The ${context_words[@]+...} guard keeps the unset-array expansion safe
// under 'set -u' on older bash.
const bashCompletionScript = `# bash completion for cyberagent
#
# Delegates candidate generation to 'cyberagent __complete'; the binary
# filters by the current word, so results are appended verbatim instead of
# being re-split through compgen -W, which would cut names at spaces.

_cyberagent_completions() {
  local cur
  local -a context_words
  local i
  COMPREPLY=()
  cur="${COMP_WORDS[COMP_CWORD]}"
  for ((i = 1; i < COMP_CWORD; i++)); do
    context_words+=("${COMP_WORDS[i]}")
  done
  local raw candidates directive line
  raw="$(CYBERAGENT_COMPLETE_PROTOCOL=4 command cyberagent __complete ${context_words[@]+"${context_words[@]}"} "$cur" 2>/dev/null)" || return 0
  candidates=""
  directive="4"
  while IFS= read -r line; do
    case "$line" in
      :*) directive="${line#:}";;
      "") ;;
      *) candidates+="$line"$'\n';;
    esac
  done <<<"$raw"
  candidates="${candidates%$'\n'}"
  if [ -n "$candidates" ]; then
    while IFS= read -r line; do
      COMPREPLY+=("$line")
    done <<<"$candidates"
  elif [ "$directive" = 0 ]; then
    # Bash 3.2 enables Readline's filename mode when compgen runs in this shell.
    compopt -o filenames 2>/dev/null || compgen -f /dev/null >/dev/null
    while IFS= read -r line; do
      COMPREPLY+=("$line")
    done < <(compgen -f -- "$cur" 2>/dev/null)
  fi
}

complete -F _cyberagent_completions cyberagent
`

// fishCompletionScript emits a fish completer that delegates candidate
// generation to 'cyberagent __complete' under the CYBERAGENT_COMPLETE_
// PROTOCOL=4 handshake and parses the trailing directive: candidates are
// offered as-is, and an empty result with the default directive falls back
// to fish's path completion.
const fishCompletionScript = `# fish completion for cyberagent
# Delegates candidate generation to 'cyberagent __complete'. Install under
# ~/.config/fish/completions/cyberagent.fish.

function __cyberagent_complete
    set -l tokens (commandline -opc) (commandline -ct)
    set -l candidates
    set -l directive 4
    for line in (env CYBERAGENT_COMPLETE_PROTOCOL=4 cyberagent __complete $tokens[2..-1] 2>/dev/null)
        if string match -q ':*' $line
            set directive (string replace -r '^:' '' $line)
        else
            set -a candidates $line
        end
    end
    if test (count $candidates) -gt 0
        printf '%s\n' $candidates
    else if test "$directive" = 0
        __fish_complete_path (commandline -ct)
    end
end

complete -c cyberagent -f -a '(__cyberagent_complete)'
`

// zshCompletionScript emits a zsh completer that delegates candidate
// generation to 'cyberagent __complete' under the CYBERAGENT_COMPLETE_
// PROTOCOL=4 handshake.
const zshCompletionScript = `#compdef cyberagent
# Delegates candidate generation to 'cyberagent __complete'.

_cyberagent() {
  local -a candidates lines
  local directive=4 line
  lines=(${(f)"$(env CYBERAGENT_COMPLETE_PROTOCOL=4 cyberagent __complete "${(@)words[2,CURRENT]}" 2>/dev/null)"})
  for line in $lines; do
    if [[ $line == :* ]]; then
      directive=${line#:}
    else
      candidates+=($line)
    fi
  done
  if ((${#candidates[@]})); then
    _describe -t cyberagent-commands 'cyberagent command' candidates
  elif [[ $directive == 0 ]]; then
    _files
  fi
}

_cyberagent "$@"
`
