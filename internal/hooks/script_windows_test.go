//go:build windows

package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestProcessScriptExecutorRejectsWindowsJunction verifies that a directory
// junction inside the hooks tree is rejected like a symlink: junctions need no
// elevation on Windows and are the realistic link-based escape there.
func TestProcessScriptExecutorRejectsWindowsJunction(t *testing.T) {
	scriptInterpreterForTest(t)
	dir := t.TempDir()
	executor := newExecutorForTest(t, dir)
	spec := writeScriptForTest(t, dir, scriptFileNameForTest(),
		echoScriptBody(t, `{"protocol_version":"restricted-hooks.v1","denied":false}`))

	inside := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "link.source")
	if err := os.Rename(filepath.Join(dir, spec.Path), target); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", inside+"\\junction",
		target).CombinedOutput(); err != nil {
		t.Skipf("junction creation is unavailable: %v (%s)", err, out)
	}
	invocation := ScriptInvocation{HookID: "guard", Event: PreTool,
		RelativePath: "scripts/junction", Interpreter: spec.Interpreter,
		SHA256: spec.SHA256, TimeoutMillis: 5_000}
	if _, err := executor.ExecuteScript(t.Context(), invocation); err == nil {
		t.Fatal("junctioned script path was accepted")
	}
}

// TestLoadOperatorScriptHooksRejectsJunctionDeclaration verifies the loader
// rejects a junctioned declaration file on Windows.
func TestLoadOperatorScriptHooksRejectsJunctionDeclaration(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real.json")
	declaration := `{"protocol_version":"restricted-hooks.v1","id":"a","event":"run_started",
		"action":"script","failure_policy":"deny","timeout_ms":100,
		"script":{"interpreter":"python","path":"s.script","sha256":"` +
		testHookDigest("x") + `"}}`
	if err := os.WriteFile(real, []byte(declaration), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", root+"\\link.json",
		real).CombinedOutput(); err != nil {
		t.Skipf("junction creation is unavailable: %v (%s)", err, out)
	}
	if _, err := LoadOperatorScriptHooks(t.Context(), root); err == nil {
		t.Fatal("junctioned declaration was accepted")
	}
}
