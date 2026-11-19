package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/hooks"
)

func seedScriptHook(t *testing.T, id string) {
	t.Helper()
	home := os.Getenv("CYBERAGENT_HOME")
	hooksDir := filepath.Join(home, "hooks.d")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(hooksDir, id+".py")
	content := []byte("print(1)\n")
	if err := os.WriteFile(script, content, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	declaration := map[string]any{
		"protocol_version": "restricted-hooks.v1",
		"id":               id,
		"event":            "pre_tool",
		"action":           "script",
		"failure_policy":   "continue",
		"timeout_ms":       5000,
		"script": map[string]any{
			"interpreter": "python",
			"path":        id + ".py",
			"sha256":      hex.EncodeToString(digest[:]),
		},
	}
	raw, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, id+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHooksListReportsOperatorScriptHooks(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	seedScriptHook(t, "smoke-hook")
	stdout, stderr, code := executeTestCommand(t, "hooks", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("hooks list failed: stdout=%q stderr=%q code=%d", stdout, stderr, code)
	}
	if !strings.Contains(stdout, "registered: 1") ||
		!strings.Contains(stdout, "plugin=operator\thook=smoke-hook\tevent=pre_tool\taction=script") {
		t.Fatalf("operator hook missing from output: %q", stdout)
	}
}

func TestHooksListJSONRoundTrips(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	seedScriptHook(t, "smoke-hook")
	stdout, stderr, code := executeTestCommand(t, "hooks", "list", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("hooks list failed: stdout=%q stderr=%q code=%d", stdout, stderr, code)
	}
	var registrations []hooks.Registration
	if err := json.Unmarshal([]byte(stdout), &registrations); err != nil {
		t.Fatalf("hooks list output is not registration JSON: %v\n%s", err, stdout)
	}
	if len(registrations) != 1 || registrations[0].Declaration.ID != "smoke-hook" {
		t.Fatalf("unexpected registrations: %#v", registrations)
	}
}

func TestHooksListEmptyConfiguration(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	stdout, stderr, code := executeTestCommand(t, "hooks", "list")
	if code != 0 || stderr != "" {
		t.Fatalf("hooks list failed: stderr=%q code=%d", stderr, code)
	}
	if !strings.Contains(stdout, "registered: 0") {
		t.Fatalf("empty configuration must report zero registrations: %q", stdout)
	}
}

func TestHooksListRejectsUnknownSubcommand(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	_, stderr, code := executeTestCommand(t, "hooks")
	if code == 0 || !strings.Contains(stderr, "hooks subcommand is required") {
		t.Fatalf("bare hooks must return the subcommand requirement: stderr=%q code=%d", stderr, code)
	}
	_, stderr, code = executeTestCommand(t, "hooks", "replay")
	if code == 0 || !strings.Contains(stderr, "unknown hooks subcommand") {
		t.Fatalf("unknown subcommand must be rejected: stderr=%q code=%d", stderr, code)
	}
}

func TestHooksValidateReportsValidConfiguration(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	seedScriptHook(t, "validate-hook")
	stdout, stderr, code := executeTestCommand(t, "hooks", "validate")
	if code != 0 || stderr != "" {
		t.Fatalf("hooks validate failed: stdout=%q stderr=%q code=%d", stdout, stderr, code)
	}
	if !strings.Contains(stdout, "hooks configuration valid") ||
		!strings.Contains(stdout, "hook=validate-hook\tevent=pre_tool") {
		t.Fatalf("valid configuration must list the declarations: %q", stdout)
	}
}

func TestHooksValidateRejectsDriftedScript(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	seedScriptHook(t, "drift-hook")
	home := os.Getenv("CYBERAGENT_HOME")
	script := filepath.Join(home, "hooks.d", "drift-hook.py")
	if err := os.WriteFile(script, []byte("print(2)\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := executeTestCommand(t, "hooks", "validate")
	if code == 0 || !strings.Contains(stderr, "hooks configuration is invalid") {
		t.Fatalf("drifted script must fail validation: stderr=%q code=%d", stderr, code)
	}
}

func TestHooksValidateRejectsMalformedDeclaration(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	hooksDir := filepath.Join(os.Getenv("CYBERAGENT_HOME"), "hooks.d")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := executeTestCommand(t, "hooks", "validate")
	if code == 0 || !strings.Contains(stderr, "declaration is not valid JSON") {
		t.Fatalf("malformed declaration must be diagnosed: stderr=%q code=%d", stderr, code)
	}
}

func TestHooksValidateMissingDirectoryIsValid(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	stdout, stderr, code := executeTestCommand(t, "hooks", "validate")
	if code != 0 || stderr != "" {
		t.Fatalf("missing hooks.d must be valid: stderr=%q code=%d", stderr, code)
	}
	if !strings.Contains(stdout, "declarations: 0") {
		t.Fatalf("missing directory must report zero declarations: %q", stdout)
	}
}
