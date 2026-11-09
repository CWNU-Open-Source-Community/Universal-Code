package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// scriptInterpreterForTest selects a real interpreter that exists on the
// current host: PowerShell on Windows, sh elsewhere. Tests skip when neither
// is available so the suite stays portable.
func scriptInterpreterForTest(t *testing.T) string {
	t.Helper()
	name := "sh"
	if runtime.GOOS == "windows" {
		name = "powershell"
	}
	if _, _, err := resolveInterpreterFn(name); err != nil {
		t.Skipf("interpreter %q is unavailable on this host: %v", name, err)
	}
	return name
}

func writeScriptForTest(t *testing.T, dir, name, body string) ScriptSpec {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	return ScriptSpec{Path: name, SHA256: hex.EncodeToString(digest[:])}
}

func echoScriptBody(t *testing.T, response string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return "$raw = [Console]::In.ReadToEnd()\n" +
			"$null = $raw\n" +
			"Write-Output '" + response + "'\n"
	}
	return "#!/bin/sh\nwhile IFS= read -r line; do :; done\nprintf '%s\\n' '" + response + "'\n"
}

// scriptFileNameForTest returns an extension the platform interpreter accepts:
// PowerShell -File requires .ps1.
func scriptFileNameForTest() string {
	if runtime.GOOS == "windows" {
		return "check.ps1"
	}
	return "check.sh"
}

func newExecutorForTest(t *testing.T, dir string) *ProcessScriptExecutor {
	t.Helper()
	executor, err := NewProcessScriptExecutor(dir)
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func TestProcessScriptExecutorRunsPinnedScriptWithBoundedIO(t *testing.T) {
	interpreter := scriptInterpreterForTest(t)
	dir := t.TempDir()
	response := `{"protocol_version":"restricted-hooks.v1","denied":true,"reason":"script said no","annotations":[{"message":"checked"}]}`
	spec := writeScriptForTest(t, dir, scriptFileNameForTest(), echoScriptBody(t, response))
	spec.Interpreter = interpreter
	executor := newExecutorForTest(t, dir)
	invocation := ScriptInvocation{HookID: "guard", Event: PreTool, RunID: "run-1",
		WorkspaceID: "ws-1", ToolName: "shell", Payload: json.RawMessage(`{"path":"a"}`),
		RelativePath: spec.Path, Interpreter: spec.Interpreter, SHA256: spec.SHA256,
		TimeoutMillis: 5_000}
	output, err := executor.ExecuteScript(t.Context(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if !output.Denied || output.Reason != "script said no" || len(output.Annotations) != 1 {
		t.Fatalf("unexpected script output: %#v", output)
	}
}

func TestProcessScriptExecutorReceivesStdinJSONAndMinimalEnvironment(t *testing.T) {
	interpreter := scriptInterpreterForTest(t)
	dir := t.TempDir()
	t.Setenv("HOOK_TEST_SECRET", "must-not-leak")
	var body string
	if runtime.GOOS == "windows" {
		body = "$raw = [Console]::In.ReadToEnd()\n" +
			"$in = $raw | ConvertFrom-Json\n" +
			"$ok = ($in.event -eq 'pre_tool') -and ($in.payload.path -eq 'a')\n" +
			"if ($env:HOOK_TEST_SECRET) { $ok = $false }\n" +
			"$decision = 'restricted-hooks.v1'\n" +
			"if ($ok) { Write-Output '{\"protocol_version\":\"restricted-hooks.v1\",\"denied\":false}' }\n" +
			"else { Write-Output '{\"protocol_version\":\"restricted-hooks.v1\",\"denied\":true,\"reason\":\"leak\"}' }\n"
	} else {
		body = "#!/bin/sh\n" +
			"input=$(cat)\n" +
			"case \"$input\" in\n" +
			"  *'\"event\":\"pre_tool\"'*'\"payload\":{\"path\":\"a\"}'*) ok=1 ;;\n" +
			"  *) ok=0 ;;\n" +
			"esac\n" +
			"if [ -n \"$HOOK_TEST_SECRET\" ]; then ok=0; fi\n" +
			"if [ \"$ok\" = \"1\" ]; then\n" +
			"  printf '%s\\n' '{\"protocol_version\":\"restricted-hooks.v1\",\"denied\":false}'\n" +
			"else\n" +
			"  printf '%s\\n' '{\"protocol_version\":\"restricted-hooks.v1\",\"denied\":true,\"reason\":\"leak\"}'\n" +
			"fi\n"
	}
	spec := writeScriptForTest(t, dir, scriptFileNameForTest(), body)
	spec.Interpreter = interpreter
	executor := newExecutorForTest(t, dir)
	invocation := ScriptInvocation{HookID: "guard", Event: PreTool, RunID: "run-1",
		WorkspaceID: "ws-1", ToolName: "shell", Payload: json.RawMessage(`{"path":"a"}`),
		RelativePath: spec.Path, Interpreter: spec.Interpreter, SHA256: spec.SHA256,
		TimeoutMillis: 5_000}
	output, err := executor.ExecuteScript(t.Context(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if output.Denied {
		t.Fatalf("stdin JSON or environment boundary failed: %#v", output)
	}
}

func TestProcessScriptExecutorRejectsDriftedDigestAndLinks(t *testing.T) {
	interpreter := scriptInterpreterForTest(t)
	dir := t.TempDir()
	spec := writeScriptForTest(t, dir, scriptFileNameForTest(), echoScriptBody(t,
		`{"protocol_version":"restricted-hooks.v1","denied":false}`))
	spec.Interpreter = interpreter
	executor := newExecutorForTest(t, dir)
	invocation := ScriptInvocation{HookID: "guard", Event: PreTool,
		RelativePath: spec.Path, Interpreter: spec.Interpreter,
		SHA256: testHookDigest("different"), TimeoutMillis: 5_000}
	if _, err := executor.ExecuteScript(t.Context(), invocation); err == nil {
		t.Fatal("executor accepted content that does not match its pinned digest")
	}
	if err := os.WriteFile(filepath.Join(dir, scriptFileNameForTest()),
		[]byte(echoScriptBody(t, `{"protocol_version":"restricted-hooks.v1","denied":false}`)+"\n# drifted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	invocation.SHA256 = spec.SHA256
	if _, err := executor.ExecuteScript(t.Context(), invocation); err == nil {
		t.Fatal("executor accepted drifted script content")
	}
	if runtime.GOOS != "windows" {
		linkedDir := filepath.Join(dir, "linked")
		if err := os.MkdirAll(linkedDir, 0o755); err != nil {
			t.Fatal(err)
		}
		linkName := scriptFileNameForTest()
		if err := os.Symlink(filepath.Join("..", linkName),
			filepath.Join(linkedDir, linkName)); err != nil {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		invocation.RelativePath = "linked/" + linkName
		if _, linkErr := executor.ExecuteScript(t.Context(), invocation); linkErr == nil ||
			!strings.Contains(linkErr.Error(), "not a regular file") {
			t.Fatalf("executor did not reject a symlinked script path: %v", linkErr)
		}
	}
}

func TestProcessScriptExecutorRejectsInvalidOutputAndFailures(t *testing.T) {
	interpreter := scriptInterpreterForTest(t)
	dir := t.TempDir()
	executor := newExecutorForTest(t, dir)
	cases := []struct {
		name string
		body string
	}{
		{"non-JSON output", echoScriptBody(t, "not json at all")},
		{"foreign protocol version", echoScriptBody(t,
			`{"protocol_version":"something-else","denied":false}`)},
		{"unknown output fields", echoScriptBody(t,
			`{"protocol_version":"restricted-hooks.v1","denied":false,"extra":1}`)},
		{"trailing JSON", echoScriptBody(t,
			`{"protocol_version":"restricted-hooks.v1"}`+"\n"+`{"protocol_version":"restricted-hooks.v1"}`)},
		{"empty output", echoScriptBody(t, "")},
	}
	for _, testCase := range cases {
		spec := writeScriptForTest(t, dir, scriptFileNameForTest(), testCase.body)
		invocation := ScriptInvocation{HookID: "guard", Event: PreTool,
			RelativePath: spec.Path, Interpreter: interpreter, SHA256: spec.SHA256,
			TimeoutMillis: 5_000}
		if _, err := executor.ExecuteScript(t.Context(), invocation); err == nil {
			t.Fatalf("%s: invalid script output was accepted", testCase.name)
		}
	}
	var overflow string
	if runtime.GOOS == "windows" {
		overflow = "$bytes = New-Object byte[] 20000\n" +
			"[Console]::Out.Write([System.Text.Encoding]::ASCII.GetString($bytes))\n"
	} else {
		overflow = "#!/bin/sh\nhead -c 20000 /dev/zero | tr '\\0' 'a'\n"
	}
	oversized := writeScriptForTest(t, dir, scriptFileNameForTest(), overflow)
	invocation := ScriptInvocation{HookID: "guard", Event: PreTool,
		RelativePath: oversized.Path, Interpreter: interpreter, SHA256: oversized.SHA256,
		TimeoutMillis: 5_000}
	if _, err := executor.ExecuteScript(t.Context(), invocation); err == nil {
		t.Fatal("stdout beyond the output limit was accepted")
	}
	var failing ScriptSpec
	if runtime.GOOS == "windows" {
		failing = writeScriptForTest(t, dir, scriptFileNameForTest(), "exit 3\n")
	} else {
		failing = writeScriptForTest(t, dir, scriptFileNameForTest(), "#!/bin/sh\nexit 3\n")
	}
	invocation = ScriptInvocation{HookID: "guard", Event: PreTool,
		RelativePath: failing.Path, Interpreter: interpreter, SHA256: failing.SHA256,
		TimeoutMillis: 5_000}
	if _, err := executor.ExecuteScript(t.Context(), invocation); err == nil {
		t.Fatal("failing script exit code was accepted")
	}
}

func TestProcessScriptExecutorKillsScriptAtDeclaredTimeout(t *testing.T) {
	interpreter := scriptInterpreterForTest(t)
	dir := t.TempDir()
	var spec ScriptSpec
	if runtime.GOOS == "windows" {
		spec = writeScriptForTest(t, dir, scriptFileNameForTest(), "Start-Sleep -Seconds 5\n")
	} else {
		spec = writeScriptForTest(t, dir, scriptFileNameForTest(), "#!/bin/sh\nsleep 5\n")
	}
	spec.Interpreter = interpreter
	executor := newExecutorForTest(t, dir)
	invocation := ScriptInvocation{HookID: "guard", Event: PreTool,
		RelativePath: spec.Path, Interpreter: spec.Interpreter, SHA256: spec.SHA256,
		TimeoutMillis: 400}
	started := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	if _, err := executor.ExecuteScript(ctx, invocation); err == nil {
		t.Fatal("script outlived its declared timeout without an error")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("timeout enforcement took too long: %v", elapsed)
	}
}

func TestProcessScriptExecutorRejectsPathEscapes(t *testing.T) {
	scriptInterpreterForTest(t)
	dir := t.TempDir()
	executor := newExecutorForTest(t, dir)
	invocation := ScriptInvocation{HookID: "guard", Event: PreTool,
		RelativePath: "../outside.script", Interpreter: "python",
		SHA256: testHookDigest("escape"), TimeoutMillis: 1_000}
	if _, err := executor.ExecuteScript(t.Context(), invocation); err == nil {
		t.Fatal("script path escaping the hooks directory was accepted")
	}
}

func TestProcessScriptExecutorTruncatesOversizedStderrWithoutFailing(t *testing.T) {
	interpreter := scriptInterpreterForTest(t)
	dir := t.TempDir()
	var body string
	if runtime.GOOS == "windows" {
		body = "$bytes = New-Object byte[] 40000\n" +
			"[Console]::Error.Write([System.Text.Encoding]::ASCII.GetString($bytes))\n" +
			"Write-Output '{\"protocol_version\":\"restricted-hooks.v1\",\"denied\":false}'\n"
	} else {
		body = "#!/bin/sh\nhead -c 40000 /dev/zero | tr '\\0' 'w' >&2\n" +
			"printf '%s\\n' '{\"protocol_version\":\"restricted-hooks.v1\",\"denied\":false}'\n"
	}
	spec := writeScriptForTest(t, dir, scriptFileNameForTest(), body)
	spec.Interpreter = interpreter
	executor := newExecutorForTest(t, dir)
	invocation := ScriptInvocation{HookID: "guard", Event: PreTool,
		RelativePath: spec.Path, Interpreter: spec.Interpreter, SHA256: spec.SHA256,
		TimeoutMillis: 5_000}
	output, err := executor.ExecuteScript(t.Context(), invocation)
	if err != nil {
		t.Fatalf("oversized stderr failed the execution: %v", err)
	}
	if output.Denied {
		t.Fatalf("unexpected denial: %#v", output)
	}
}

func TestProcessScriptExecutorNodeResolvesRelativeModulesInHookDirectory(t *testing.T) {
	if _, _, err := resolveInterpreterFn("node"); err != nil {
		t.Skip("node interpreter is not available")
	}
	dir := t.TempDir()
	helperPath := filepath.Join(dir, "helper.js")
	if err := os.WriteFile(helperPath, []byte(`module.exports = { denied: true, reason: "denied from hook dir helper" };`), 0o644); err != nil {
		t.Fatal(err)
	}

	fakeHelperPath := filepath.Join(os.TempDir(), "helper.js")
	_ = os.WriteFile(fakeHelperPath, []byte(`module.exports = { denied: false, reason: "fake helper" };`), 0o644)
	t.Cleanup(func() { _ = os.Remove(fakeHelperPath) })

	hookBody := `const helper = require('./helper');
console.log(JSON.stringify({ protocol_version: "restricted-hooks.v1", denied: helper.denied, reason: helper.reason }));`
	spec := writeScriptForTest(t, dir, "hook.js", hookBody)
	spec.Interpreter = "node"

	executor := newExecutorForTest(t, dir)
	invocation := ScriptInvocation{
		HookID: "node-guard", Event: PreTool,
		RelativePath: spec.Path, Interpreter: "node", SHA256: spec.SHA256,
		TimeoutMillis: 5_000,
	}
	output, err := executor.ExecuteScript(t.Context(), invocation)
	if err != nil {
		t.Fatalf("ExecuteScript failed: %v", err)
	}
	if !output.Denied || output.Reason != "denied from hook dir helper" {
		t.Fatalf("expected denial from hook directory helper, got %#v", output)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".operator-hook-") {
			t.Fatalf("temporary snapshot file was not removed: %s", entry.Name())
		}
	}
}
