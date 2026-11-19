package app

import (
	"errors"
	"fmt"
	"io"
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
	{"exec", "Start a one-shot agent run and print its result", `cyberagent exec --workspace <name> "prompt" [--profile code|review|learn|script] [--route <route>] [--instructions <text>] [--timeout 30m] [--json] [--continue <thread-id>]`},
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
	candidates, directive := completionCandidates(context, prefix)
	for _, candidate := range candidates {
		fmt.Fprintln(a.out, candidate)
	}
	fmt.Fprintf(a.out, ":%d\n", directive)
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
	"report finding": {"accept", "attach", "fix", "reject", "remediation", "validate", "verify"},
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
	"context instructions":    {"scaffold"},
	"context memory":          {"create", "delete", "disable", "edit", "enable", "export", "list", "show"},
	"run delivery":            {"checkpoint", "list", "record", "report", "show"},
	"run delivery checkpoint": {"list", "record"},
	"run schedule":            {"cancel", "create", "pause", "resume"},
	"workspace checkpoint":    {"capture", "fork", "preview", "redo", "rewind", "timeline", "undo"},
	"run fanout":              {"execute", "execution", "plan", "report", "show"},
	"run plan":                {"choose", "selection", "show"},
	"run delegation":          {"apply", "approve", "continue", "reject", "schedule", "show"},
	"run sandbox":             {"docker-admit", "docker-cancel", "docker-readiness", "docker-start", "docker-status"},
	"run standard-code":       {"docker-cancel", "docker-execute", "docker-prepare", "docker-readiness", "docker-recover", "pause-and-configure", "preset"},
	"skill catalog":           {"audit", "disable", "enable", "list", "pin", "revoke", "trust"},
}

// commandFlags lists the verified flag vocabulary for a curated set of
// commands and groups, read from their flag declarations; values carry
// their leading dashes so shell prefix matching works verbatim. The ""
// key covers the top-level aliases accepted by dispatch.
var commandFlags = map[string][]string{
	"":                                {"--help", "--version", "-h", "-v"},
	"analyzer execute":                {"--confirm", "--file", "--json", "--media-type", "--run", "--text"},
	"api openapi":                     {"--output"},
	"api serve":                       {"--browser-headed", "--code-intel-config", "--enable-batch-validation-execution", "--enable-browser-cdp-control", "--enable-danger-full-access", "--enable-docker-execution", "--enable-file-edit-proposals", "--enable-full-cdp-debug", "--enable-git-advanced", "--enable-github-review", "--enable-permission-control", "--enable-provider-credentials", "--enable-scheduled-job-worker", "--enable-wake-worker", "--enable-workspace-import", "--enable-workspace-sandbox", "--git-worktree-root", "--listen", "--ui-dir"},
	"approval grant":                  {"--by", "--idempotency-key", "--reason", "--session", "--tool", "--workspace"},
	"approval list":                   {"--limit", "--run", "--session", "--status", "--tool"},
	"artifact list":                   {"--limit", "--run", "--source", "--stream"},
	"artifact read":                   {"--max-bytes"},
	"code-intel status":               {"--config", "--json", "--start", "--workspace"},
	"context compact":                 {"--message", "--task", "--workspace"},
	"context instructions":            {"--confirm", "--expected-fingerprint", "--expected-live-fingerprint", "--json", "--operator", "--run", "--target", "--workspace"},
	"context instructions scaffold":   {"--json", "--print", "--target", "--workspace"},
	"context memory create":           {"--content", "--operator", "--redact-sensitive", "--reference", "--retention", "--scope", "--source-ref", "--title", "--workspace"},
	"context memory delete":           {"--operator", "--version"},
	"context memory disable":          {"--operator", "--version"},
	"context memory edit":             {"--content", "--operator", "--redact-sensitive", "--reference", "--retention", "--source-ref", "--title", "--version"},
	"context memory enable":           {"--operator", "--version"},
	"context memory list":             {"--all", "--json", "--scope", "--workspace"},
	"context show":                    {"--task"},
	"ctf init":                        {"--category", "--scope"},
	"debug query":                     {"--after", "--correlation-id", "--correlation-kind", "--from", "--json", "--limit", "--run", "--source-prefix", "--to", "--type-prefix"},
	"doctor browser-network-probe":    {"--collector", "--confirm", "--json", "--product"},
	"doctor browser-network-review":   {"--accept", "--operation-key", "--product", "--reject", "--reviewer"},
	"doctor browser-readiness":        {"--json", "--product"},
	"doctor bundle":                   {"--after", "--from", "--limit", "--run", "--source-prefix", "--to", "--type-prefix"},
	"doctor portable":                 {"--json"},
	"doctor snapshot":                 {"--json", "--run"},
	"edit apply":                      {"--operation-key", "--operator"},
	"edit deny":                       {"--reason"},
	"edit list":                       {"--session", "--status", "--workspace"},
	"edit propose":                    {"--content", "--content-file", "--path", "--session", "--workspace"},
	"edit review-approve":             {"--confirm-full", "--enable-danger-full-access", "--enable-permission-control"},
	"edit review-deny":                {"--confirm-full", "--enable-danger-full-access", "--enable-permission-control"},
	"exec":                            {"--attach", "--confirm-full", "--continue", "--enable-danger-full-access", "--enable-permission-control", "--image", "--instructions", "--json", "--max-tokens", "--max-tool-calls", "--max-turns", "--output-last-message", "--profile", "--route", "--stdin", "--stream", "--timeout", "--workspace"},
	"headless events":                 {"--after-sequence", "--follow", "--max-events", "--poll-interval", "--timeout"},
	"hooks list":                      {"--json"},
	"mcp client":                      {"--file", "--limit", "--run", "--workspace"},
	"mcp credential":                  {"--confirm", "--from-env"},
	"mcp serve":                       {"--call-timeout", "--max-concurrent", "--run", "--session-ttl", "--workspace"},
	"note create":                     {"--category", "--content", "--content-file", "--evidence", "--owner", "--owner-agent", "--pin", "--source", "--tag", "--visibility"},
	"note list":                       {"--category", "--limit", "--owner", "--owner-agent", "--pinned", "--status", "--tag", "--visibility"},
	"note update":                     {"--category", "--clear-evidence", "--clear-sources", "--clear-tags", "--content", "--content-file", "--evidence", "--owner", "--owner-agent", "--pin", "--source", "--tag", "--title", "--unpin", "--version", "--visibility"},
	"plugin import-git":               {"--archive", "--by", "--commit", "--repo", "--supersedes"},
	"plugin import-url":               {"--by", "--sha256", "--supersedes", "--url"},
	"plugin review":                   {"--action", "--by", "--capabilities", "--confirm-untrusted", "--fingerprint", "--generation"},
	"plugin rollback":                 {"--by", "--capabilities", "--confirm-untrusted", "--current-fingerprint", "--current-generation", "--target", "--target-fingerprint", "--target-generation"},
	"plugin stage":                    {"--by", "--file", "--supersedes"},
	"plugin stage-mcp":                {"--run", "--scope", "--workspace"},
	"project-config show":             {"--max-tool-calls", "--max-turns", "--profile"},
	"provider price-import":           {"--file"},
	"report check":                    {"--fail-status", "--format", "--min-severity"},
	"report finding accept":           {"--operation-key", "--operator", "--reason"},
	"report finding attach":           {"--note", "--operation-key", "--operator"},
	"report finding fix":              {"--operation-key", "--operator", "--reason"},
	"report show":                     {"--format"},
	"run create":                      {"--allow-target", "--browser-network", "--browser-origin", "--ignore-project-config", "--ignore-project-instructions", "--instruction-target", "--interactive", "--max-tokens", "--max-tool-calls", "--max-turns", "--network", "--phase", "--profile", "--route", "--session", "--surface", "--timeout", "--workspace"},
	"run delegation apply":            {"--operation-key", "--operator"},
	"run delegation schedule":         {"--agent", "--max-rounds", "--operation-key", "--operator", "--tools"},
	"run delegations":                 {"--limit"},
	"run delivery checkpoint list":    {"--limit"},
	"run delivery checkpoint record":  {"--diff-audit", "--focused", "--functional", "--handoff", "--operation-key", "--operator", "--robustness", "--security-audit"},
	"run execute":                     {"--confirm-full", "--enable-danger-full-access", "--enable-permission-control", "--finish", "--max-steps", "--summary"},
	"run execution-interaction":       {"--confirm-container-boundary", "--confirm-debug-boundary", "--confirm-workspace-trust", "--operation-key", "--operator", "--reason", "--trust"},
	"run execution-permission":        {"--confirm-full", "--enable-danger-full-access", "--enable-permission-control", "--operation-key", "--operator", "--reason"},
	"run execution-profile":           {"--operation-key", "--operator", "--reason"},
	"run fanout execute":              {"--max-output-tokens", "--operation-key", "--operator"},
	"run fanout plan":                 {"--operation-key", "--operator", "--path", "--tier"},
	"run fanout report":               {"--format"},
	"run host-execute":                {"--arg", "--confirm-execution", "--confirm-full", "--confirm-non-sandboxed-host-execution", "--cwd", "--enable-danger-full-access", "--enable-permission-control", "--executable", "--operation-key", "--operator", "--purpose", "--timeout"},
	"run list":                        {"--limit", "--mission", "--status"},
	"run phase":                       {"--operation-key", "--operator", "--reason"},
	"run plan choose":                 {"--manual-acceptance", "--operation-key", "--operator"},
	"run plans":                       {"--limit"},
	"run project-action":              {"--confirm-execution", "--confirm-full", "--confirm-non-sandboxed-host-execution", "--enable-danger-full-access", "--enable-docker-execution", "--enable-permission-control", "--enable-workspace-sandbox", "--operation-key", "--operator", "--path", "--prepare"},
	"run sandbox docker-admit":        {"--confirm-full", "--manifest-file", "--operation-key", "--operator"},
	"run sandbox docker-cancel":       {"--operation-key", "--operator"},
	"run sandbox docker-readiness":    {"--manifest-file"},
	"run sandbox docker-start":        {"--admission-operation-key", "--confirm-full", "--manifest-file", "--operation-key", "--operator"},
	"run schedule":                    {"--limit", "--notifications", "--owner", "--rounds"},
	"run schedule cancel":             {"--operation-key", "--operator", "--revision"},
	"run schedule create":             {"--at", "--confirm-repair", "--deadline", "--every", "--initial-backoff-seconds", "--max-attempts", "--max-backoff-seconds", "--max-elapsed-seconds", "--max-model-calls", "--max-rounds", "--misfire", "--mode", "--notify", "--operation-key", "--operator", "--stop-on-target-terminal", "--timezone"},
	"run schedule pause":              {"--operation-key", "--operator", "--revision"},
	"run schedule resume":             {"--operation-key", "--operator", "--revision"},
	"run specialist-step":             {"--agent", "--confirm-full", "--enable-danger-full-access", "--enable-permission-control", "--operation-key", "--tools"},
	"run standard-code docker-cancel": {"--operation-key", "--operator"},
	"run standard-code preset":        {"--backend", "--confirm-workspace-trust", "--enable-docker-execution", "--enable-permission-control", "--enable-workspace-sandbox", "--expected-trust-digest", "--goal", "--json", "--operation-key", "--operator", "--workspace"},
	"run steer":                       {"--limit", "--max-steps", "--operation-key", "--operator", "--reason"},
	"run step":                        {"--confirm-full", "--enable-danger-full-access", "--enable-permission-control"},
	"run wake":                        {"--base-backoff-seconds", "--confirm-full", "--enable-danger-full-access", "--enable-permission-control", "--initial-delay-seconds", "--max-attempts", "--max-backoff-seconds", "--max-elapsed-seconds", "--max-steps", "--operation-key", "--operator"},
	"script new":                      {"--language", "--workspace"},
	"script run":                      {"--idempotency-key", "--local", "--workspace"},
	"session create":                  {"--route", "--title", "--workspace"},
	"session history":                 {"--all"},
	"session send":                    {"--operation-key"},
	"session tree":                    {"--json"},
	"skill candidate":                 {"--candidate-fingerprint", "--confirm-untrusted-skill", "--operation-key", "--operator", "--reason", "--run", "--show-content"},
	"skill catalog":                   {"--operator", "--surface", "--team"},
	"skill import":                    {"--confirm-untrusted-skill", "--operation-key", "--operator", "--surface"},
	"skill list":                      {"--profile"},
	"thread create":                   {"--json", "--phase", "--profile", "--route", "--surface", "--workspace"},
	"thread history":                  {"--all", "--json"},
	"thread list":                     {"--include-deleted", "--status"},
	"thread send":                     {"--json", "--operation-key"},
	"todo create":                     {"--acceptance", "--depends-on", "--description", "--owner", "--owner-agent", "--priority"},
	"todo list":                       {"--limit", "--owner", "--owner-agent", "--status"},
	"tool list":                       {"--session", "--status"},
	"tui":                             {"--print", "--route", "--run", "--session", "--title", "--workspace"},
	"ui-evidence artifact":            {"--output"},
	"ui-evidence list":                {"--limit", "--run", "--status"},
	"web-evidence list":               {"--limit", "--run"},
	"workspace checkpoint capture":    {"--operation-key", "--operator", "--run", "--title"},
	"workspace checkpoint fork":       {"--branch", "--checkpoint", "--expected-current", "--goal", "--operation-key", "--run", "--workspace-name", "--workspace-root"},
	"workspace checkpoint preview":    {"--checkpoint", "--expected-current", "--run"},
	"workspace checkpoint rewind":     {"--checkpoint", "--expected-current", "--operation-key", "--run"},
	"workspace checkpoint timeline":   {"--limit", "--run"},
	"workspace checkpoint undo":       {"--expected-current", "--operation-key", "--run"},
	"workspace checkpoint redo":       {"--expected-current", "--operation-key", "--run"},
	"workspace read":                  {"--max-bytes"},
	"workspace tree":                  {"--depth"},
	"tool deny":                       {"--reason"},
	"tool invoke":                     {"--operation-key", "--payload", "--payload-file", "--run"},
}

// flagValueVocabulary completes enum-style flag values: the key is
// "<command group> <flag>" and the values are the documented choices,
// verified against each flag's help string in the dispatch source.
var flagValueVocabulary = map[string][]string{
	"context memory create --scope": {"project", "user"},
	"exec --profile":                {"code", "learn", "review", "script"},
	"note create --category":        {"decision", "hypothesis", "observation", "reference", "summary"},
	"note create --visibility":      {"owner", "root", "run"},
	"skill import --surface":        {"code", "cyber"},
	"skill list --profile":          {"code", "learn", "review", "script"},
	"thread create --phase":         {"deliver", "plan"},
	"thread create --surface":       {"code", "cyber"},
	"todo create --priority":        {"critical", "high", "low", "normal"},
}

// completionCandidates resolves the candidates and the shell directive for
// the current position: positions with candidates suppress the shell's
// file completion, and empty positions hand control back to the shell so
// path-shaped arguments (skill import's package, workspace read's file)
// still complete, cobra-style.
func completionCandidates(context []string, prefix string) ([]string, int) {
	if strings.HasPrefix(prefix, "-") {
		key := ""
		if len(context) >= 1 && len(context) <= 4 {
			key = strings.Join(context, " ")
		}
		return matchedNames(commandFlags[key], prefix), completionDirectiveNoFileComp
	}
	if len(context) >= 2 {
		last := context[len(context)-1]
		if strings.HasPrefix(last, "-") {
			key := strings.Join(context[:len(context)-1], " ") + " " + last
			return matchedNames(flagValueVocabulary[key], prefix), completionDirectiveNoFileComp
		}
	}
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
  local raw candidates directive line
  raw="$(command cyberagent __complete ${context_words[@]+"${context_words[@]}"} "$cur" 2>/dev/null)" || return 0
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
    COMPREPLY=($(compgen -W "$candidates" -- "$cur"))
  elif [ "$directive" = 0 ]; then
    COMPREPLY=($(compgen -f -- "$cur" 2>/dev/null))
  fi
}

complete -F _cyberagent_completions cyberagent
`

// fishCompletionScript emits a fish completer that delegates candidate
// generation to 'cyberagent __complete' and parses the trailing directive:
// candidates are offered as-is, and an empty result with the default
// directive falls back to fish's path completion.
const fishCompletionScript = `# fish completion for cyberagent
# Delegates candidate generation to 'cyberagent __complete'. Install under
# ~/.config/fish/completions/cyberagent.fish.

function __cyberagent_complete
    set -l tokens (commandline -opc) (commandline -ct)
    set -l candidates
    set -l directive 4
    for line in (command cyberagent __complete $tokens[2..-1] 2>/dev/null)
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
// generation to 'cyberagent __complete'.
const zshCompletionScript = `#compdef cyberagent
# Delegates candidate generation to 'cyberagent __complete'.

_cyberagent() {
  local -a candidates lines
  local directive=4 line
  lines=(${(f)"$(command cyberagent __complete "${(@)words[2,CURRENT]}" 2>/dev/null)"})
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
