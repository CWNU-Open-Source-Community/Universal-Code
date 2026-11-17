package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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

// usageNotInHelp lists commands whose canonical usage line is not printed
// by printHelp: their usage lives in the command's own error output.
var usageNotInHelp = map[string]bool{
	"help": true, "completion": true, "exec": true,
	"project-config": true, "once-command": true,
	"git-op": true, "git-remote": true,
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

func TestCompleteDeeperPositionsOfferNothing(t *testing.T) {
	for _, args := range [][]string{
		{"__complete", "workspace", ""},
		{"__complete", "help", "workspace", ""},
		{"__complete", "not-a-command", ""},
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
