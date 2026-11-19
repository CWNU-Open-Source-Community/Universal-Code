package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func dispatchForOutput(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	a := &App{out: &out}
	err := a.dispatch(context.Background(), args)
	return out.String(), err
}

// dispatchSwitchCommands extracts the literal command names handled by the
// dispatch switch in app.go so the switch and commandTable cannot drift
// apart silently.
func dispatchSwitchCommands(t *testing.T) map[string]bool {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source file")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "app.go"))
	if err != nil {
		t.Fatalf("read app.go: %v", err)
	}
	src := string(raw)
	start := strings.Index(src, "func (a *App) dispatch(")
	if start < 0 {
		t.Fatal("dispatch function not found in app.go")
	}
	end := strings.Index(src[start:], "\nfunc ")
	if end < 0 {
		t.Fatal("dispatch function end not found in app.go")
	}
	body := src[start : start+end]
	commands := make(map[string]bool)
	caseRe := regexp.MustCompile(`(?m)^\tcase (.+):$`)
	nameRe := regexp.MustCompile(`"([^"]+)"`)
	for _, match := range caseRe.FindAllStringSubmatch(body, -1) {
		for _, name := range nameRe.FindAllStringSubmatch(match[1], -1) {
			if strings.HasPrefix(name[1], "-") {
				continue // flag aliases, not command names
			}
			commands[name[1]] = true
		}
	}
	if len(commands) == 0 {
		t.Fatal("no dispatch cases extracted; check the dispatch switch layout")
	}
	return commands
}

func TestDispatchSwitchMatchesCommandTable(t *testing.T) {
	dispatch := dispatchSwitchCommands(t)
	if !dispatch["__complete"] {
		t.Fatal("__complete must stay wired in dispatch")
	}
	delete(dispatch, "__complete") // hidden plumbing, deliberately not in the table
	table := make(map[string]bool, len(commandTable))
	for _, spec := range commandTable {
		table[spec.name] = true
	}
	for name := range dispatch {
		if !table[name] {
			t.Errorf("dispatch handles %q but commandTable omits it; register it to stay discoverable", name)
		}
	}
	for name := range table {
		if !dispatch[name] {
			t.Errorf("commandTable lists %q but dispatch has no case for it", name)
		}
	}
}

func TestCommandTableIntegrity(t *testing.T) {
	seen := make(map[string]bool, len(commandTable))
	for i, spec := range commandTable {
		if spec.name == "" || spec.description == "" || spec.usage == "" {
			t.Fatalf("command table entry %d must set name, description, and usage", i)
		}
		if seen[spec.name] {
			t.Fatalf("duplicate command table entry %q", spec.name)
		}
		seen[spec.name] = true
		if i > 0 && commandTable[i-1].name >= spec.name {
			t.Fatalf("command table must stay sorted: %q before %q", commandTable[i-1].name, spec.name)
		}
	}
	if !seen["completion"] {
		t.Fatal("completion command must be registered for discovery")
	}
	if seen["__complete"] {
		t.Fatal("__complete is hidden plumbing and must not appear in the table")
	}
}

func TestCommandSubcommandsIntegrity(t *testing.T) {
	table := make(map[string]bool, len(commandTable))
	for _, spec := range commandTable {
		table[spec.name] = true
	}
	for name, subcommands := range commandSubcommands {
		parts := strings.Split(name, " ")
		if len(parts) > 1 {
			if !table[parts[0]] {
				t.Fatalf("nested key %q starts from unregistered command %q", name, parts[0])
			}
			for i := 1; i < len(parts); i++ {
				parent := strings.Join(parts[:i], " ")
				if !slices.Contains(commandSubcommands[parent], parts[i]) {
					t.Fatalf("nested key %q: %q is not a verified subcommand of %q", name, parts[i], parent)
				}
			}
		} else if !table[name] {
			t.Fatalf("subcommand map key %q is not a registered command", name)
		}
		if len(subcommands) == 0 {
			t.Fatalf("command %q has an empty subcommand list; remove the entry", name)
		}
		previous := ""
		for _, sub := range subcommands {
			if sub == "" {
				t.Fatalf("command %q has an empty subcommand", name)
			}
			if previous != "" && sub <= previous {
				t.Fatalf("subcommands of %q must stay sorted and unique: %q after %q", name, sub, previous)
			}
			previous = sub
		}
	}
}

func TestCommandSubcommandsDocumented(t *testing.T) {
	var out bytes.Buffer
	(&App{out: &out}).printHelp()
	help := out.String()
	for _, spec := range commandTable {
		for _, sub := range commandSubcommands[spec.name] {
			if !strings.Contains(spec.usage, sub) {
				t.Errorf("subcommand %q of %q is missing from commandTable usage %q", sub, spec.name, spec.usage)
			}
			if !usageNotInHelp[spec.name] && !strings.Contains(help, sub) {
				t.Errorf("subcommand %q of %q is missing from printHelp output", sub, spec.name)
			}
		}
	}
	for name, subcommands := range commandSubcommands {
		if !strings.Contains(name, " ") {
			continue
		}
		for _, sub := range subcommands {
			if !strings.Contains(help, sub) {
				t.Errorf("group subcommand %q of %q is missing from printHelp output; document it or verify it against the nested dispatch switch", sub, name)
			}
		}
	}
}

// commandFunctionBody locates the source body of the dispatch function for
// one command by matching its normalized name, so the completion vocabulary
// cannot drift from the dispatch switch without failing a test.
func commandFunctionBody(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test source file")
	}
	lookup := name
	if override, ok := nestedDispatchFunctions[lookup]; ok {
		lookup = override
	}
	dir := filepath.Dir(thisFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read app package sources: %v", err)
	}
	want := strings.ToLower(strings.ReplaceAll(lookup, "-", ""))
	if !strings.Contains(name, " ") {
		want += "command"
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		src := string(raw)
		marker := "func (a *App) "
		for searchFrom := 0; ; searchFrom++ {
			relative := strings.Index(src[searchFrom:], marker)
			if relative < 0 {
				break
			}
			position := searchFrom + relative
			searchFrom = position
			offset := position + len(marker)
			end := strings.Index(src[offset:], "(")
			if end < 0 {
				continue
			}
			function := src[offset : offset+end]
			if strings.ToLower(strings.ReplaceAll(function, "-", "")) != want {
				continue
			}
			bodyStart := offset + end
			if next := strings.Index(src[bodyStart:], "\nfunc "); next >= 0 {
				return src[bodyStart : bodyStart+next]
			}
			return src[bodyStart:]
		}
	}
	t.Fatalf("dispatch function for command %q not found in package sources", name)
	return ""
}

// nestedDispatchFunctions maps two-level group keys to the dispatch
// functions that hold their nested switches; the normalized-name lookup
// only covers top-level command functions.
var nestedDispatchFunctions = map[string]string{
	"context instructions": "contextCommand",
	"context memory":       "contextMemory",
	"run delivery":         "runDeliveryCheckpoint",
	"run fanout":           "runFanout",
	"run plan":             "runPlanDelivery",
	"run sandbox":          "runSandboxManifest",
	"run standard-code":    "runStandardCode",
	"skill catalog":        "skillCatalogCommand",
}

func TestCommandSubcommandsMatchDispatch(t *testing.T) {
	for name, subcommands := range commandSubcommands {
		body := commandFunctionBody(t, name)
		for _, sub := range subcommands {
			if !strings.Contains(body, sub) {
				t.Errorf("subcommand %q of %q does not appear in its dispatch source; the vocabulary drifted", sub, name)
			}
		}
	}
}

// usageNotInHelp lists commands whose canonical usage line is not printed
// by printHelp: their usage lives in the command's own error output.
var usageNotInHelp = map[string]bool{
	"help": true, "completion": true,
}

func TestCommandTableUsageMatchesHelp(t *testing.T) {
	var out bytes.Buffer
	(&App{out: &out}).printHelp()
	help := out.String()
	for _, spec := range commandTable {
		if usageNotInHelp[spec.name] {
			continue
		}
		if !strings.Contains(help, spec.usage) {
			t.Errorf("usage %q for command %q is missing from printHelp; update both together", spec.usage, spec.name)
		}
	}
}

func TestCompleteListsAllCommandsForEmptyPrefix(t *testing.T) {
	out, err := dispatchForOutput(t, "__complete", "")
	if err != nil {
		t.Fatalf("dispatch __complete: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	want := commandNames()
	if len(got) != len(want) {
		t.Fatalf("got %d candidates, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCompleteFiltersByPrefix(t *testing.T) {
	out, err := dispatchForOutput(t, "__complete", "git-")
	if err != nil {
		t.Fatalf("dispatch __complete: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	want := []string{"git-advanced", "git-op", "git-remote"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCompleteCompletesHelpArgument(t *testing.T) {
	out, err := dispatchForOutput(t, "__complete", "help", "wo")
	if err != nil {
		t.Fatalf("dispatch __complete: %v", err)
	}
	if got := strings.Fields(out); len(got) != 1 || got[0] != "workspace" {
		t.Fatalf("'help wo' must complete workspace only, got %q", out)
	}
}

func TestCompleteSecondLevelSubcommands(t *testing.T) {
	out, err := dispatchForOutput(t, "__complete", "workspace", "")
	if err != nil {
		t.Fatalf("dispatch __complete: %v", err)
	}
	if got := strings.Split(strings.TrimSuffix(out, "\n"), "\n"); len(got) != len(commandSubcommands["workspace"]) {
		t.Fatalf("workspace must complete its full subcommand set, got %v", got)
	}
	out, err = dispatchForOutput(t, "__complete", "run", "st")
	if err != nil {
		t.Fatalf("dispatch __complete: %v", err)
	}
	if got := strings.Fields(out); len(got) != 4 || got[0] != "standard-code" || got[1] != "start" || got[2] != "steer" || got[3] != "step" {
		t.Fatalf("'run st' must complete the four st-subcommands sorted, got %v", got)
	}
	out, err = dispatchForOutput(t, "__complete", "completion", "")
	if err != nil {
		t.Fatalf("dispatch __complete: %v", err)
	}
	if got := strings.Fields(out); len(got) != 2 || got[0] != "bash" || got[1] != "zsh" {
		t.Fatalf("'completion ' must complete bash and zsh, got %v", got)
	}
}

func TestCompleteDeeperPositionsOfferNothing(t *testing.T) {
	for _, args := range [][]string{
		{"__complete", "workspace", "init", ""},
		{"__complete", "help", "workspace", ""},
		{"__complete", "not-a-command", ""},
		{"__complete", "exec", ""}, // exec takes a prompt, not a subcommand
	} {
		out, err := dispatchForOutput(t, args...)
		if err != nil {
			t.Fatalf("dispatch %v: %v", args, err)
		}
		if out != "" {
			t.Fatalf("deep positions must offer no candidates for %v, got %q", args, out)
		}
	}
}

func TestPrintCommandHelpKnownCommand(t *testing.T) {
	out, err := dispatchForOutput(t, "help", "workspace")
	if err != nil {
		t.Fatalf("dispatch help workspace: %v", err)
	}
	if !strings.HasPrefix(out, "workspace - Initialize and inspect source workspaces\n") {
		t.Fatalf("help output missing description line: %q", out)
	}
	if !strings.Contains(out, "usage: cyberagent workspace init|list|show|tree|read|checkpoint\n") {
		t.Fatalf("help output missing usage line: %q", out)
	}
}

func TestPrintCommandHelpUnknownCommandSuggests(t *testing.T) {
	_, err := dispatchForOutput(t, "help", "wrkspace")
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("help with an unknown command must suggest, got %v", err)
	}
}

func TestHelpFlagAliasesKeepFullHelp(t *testing.T) {
	for _, args := range [][]string{{"help", "-h"}, {"help", "--help"}, {"-h"}} {
		out, err := dispatchForOutput(t, args...)
		if err != nil {
			t.Fatalf("dispatch %v: %v", args, err)
		}
		if !strings.Contains(out, "Usage:") {
			t.Fatalf("dispatch %v must render the full help, got %q", args, out)
		}
	}
}

func TestCompletionCommandEmitsBashScript(t *testing.T) {
	out, err := dispatchForOutput(t, "completion", "bash")
	if err != nil {
		t.Fatalf("dispatch completion bash: %v", err)
	}
	if !strings.Contains(out, "complete -F _cyberagent_completions cyberagent") {
		t.Fatalf("bash script missing complete registration: %q", out)
	}
	if !strings.Contains(out, "__complete") {
		t.Fatalf("bash script must delegate to __complete: %q", out)
	}
}

func TestCompletionCommandEmitsZshScript(t *testing.T) {
	out, err := dispatchForOutput(t, "completion", "zsh")
	if err != nil {
		t.Fatalf("dispatch completion zsh: %v", err)
	}
	if !strings.HasPrefix(out, "#compdef cyberagent") {
		t.Fatalf("zsh script must start with #compdef: %q", out)
	}
	if !strings.Contains(out, "__complete") {
		t.Fatalf("zsh script must delegate to __complete: %q", out)
	}
	if !strings.Contains(out, `"${(@)words[2,CURRENT]}"`) || strings.Contains(out, "words[2,-1]") {
		t.Fatalf("zsh script must slice words up to CURRENT so trailing arguments do not suppress mid-line completion: %q", out)
	}
	// Simulates Zsh passing ${(@)words[2,CURRENT]} when the cursor sits after
	// "wo" in `cyberagent wo list` (CURRENT=2) and at end-of-line `cyberagent workspace in` (CURRENT=3).
	midLine, err := dispatchForOutput(t, "__complete", "wo")
	if err != nil || strings.TrimSpace(midLine) != "workspace" {
		t.Fatalf("mid-line cursor slice must complete workspace, got %q (err=%v)", midLine, err)
	}
	endOfLine, err := dispatchForOutput(t, "__complete", "workspace", "in")
	if err != nil || strings.TrimSpace(endOfLine) != "init" {
		t.Fatalf("end-of-line cursor slice must complete init, got %q (err=%v)", endOfLine, err)
	}
}

func TestPrintCommandHelpListsFullDoctorAndRunVocabularies(t *testing.T) {
	doctorOut, err := dispatchForOutput(t, "help", "doctor")
	if err != nil {
		t.Fatalf("dispatch help doctor: %v", err)
	}
	for _, sub := range commandSubcommands["doctor"] {
		if !strings.Contains(doctorOut, sub) {
			t.Fatalf("help doctor missing subcommand %q: %q", sub, doctorOut)
		}
	}
	runOut, err := dispatchForOutput(t, "help", "run")
	if err != nil {
		t.Fatalf("dispatch help run: %v", err)
	}
	if !strings.Contains(runOut, "standard-code") {
		t.Fatalf("help run missing standard-code: %q", runOut)
	}
}

func TestCompletionCommandRejectsUnknownShell(t *testing.T) {
	_, err := dispatchForOutput(t, "completion", "fish")
	if err == nil || !strings.Contains(err.Error(), "bash, zsh") {
		t.Fatalf("unknown shell must list supported shells, got %v", err)
	}
}

func TestCompletionCommandRequiresShell(t *testing.T) {
	for _, args := range [][]string{{"completion"}, {"completion", "bash", "zsh"}} {
		if _, err := dispatchForOutput(t, args...); err == nil {
			t.Fatalf("dispatch %v must require exactly one shell", args)
		}
	}
}
