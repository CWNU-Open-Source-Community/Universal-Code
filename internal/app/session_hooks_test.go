package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One invalid hooks.d declaration blocks every lifecycle-protected
// operation (fail-closed by design). The operator must at least be told
// WHICH file to fix: the unavailable error carries the wrapped cause, which
// names the offending declaration file.
func TestSessionCreateNamesInvalidHookDeclaration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CYBERAGENT_HOME", home)
	hooksDir := filepath.Join(home, "hooks.d")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	decl := `{"protocol_version":"hooks-declaration.v1","id":"bad-hook","event":"session_opened","action":"script","failure_policy":"continue","timeout_ms":5000,"script":{"interpreter":"powershell","path":"bad.ps1","sha256":"` + strings.Repeat("a", 64) + `"}}`
	if err := os.WriteFile(filepath.Join(hooksDir, "bad-declaration.json"), []byte(decl), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := executeTestCommand(t, "session", "create", "hook probe")
	if code != 6 || !strings.Contains(stderr, "bad-declaration.json") {
		t.Fatalf("invalid declaration must be named in the unavailable error: stderr=%q code=%d", stderr, code)
	}
}
