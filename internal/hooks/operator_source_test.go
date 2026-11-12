package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeOperatorScript(t *testing.T, dir, relative, body string) string {
	t.Helper()
	absolute := filepath.Join(dir, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(absolute)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func operatorDeclarationJSON(t *testing.T, id, event, scriptRelative string) string {
	t.Helper()
	script := filepath.Join("hooks-root", scriptRelative)
	digest := writeOperatorScript(t, t.TempDir(), script, "# operator script\n")
	return fmt.Sprintf(`{"protocol_version":%q,"id":%q,"event":%q,"action":"script",
		"failure_policy":"deny","timeout_ms":2000,
		"script":{"interpreter":"python","path":%q,"sha256":%q}}`,
		ProtocolVersion, id, event, scriptRelative, digest)
}

func TestLoadOperatorScriptHooksReadsPinnedDeclarations(t *testing.T) {
	root := t.TempDir()
	digest := writeOperatorScript(t, root, "scripts/check.py", "# operator script\n")
	declaration := fmt.Sprintf(`{"protocol_version":%q,"id":"guard-shell","event":"pre_tool",
		"action":"script","failure_policy":"deny","timeout_ms":2000,
		"tool_names":["shell"],
		"script":{"interpreter":"python","path":"scripts/check.py","sha256":%q,"args":["--strict"]}}`,
		ProtocolVersion, digest)
	if err := os.WriteFile(filepath.Join(root, "guard.json"), []byte(declaration), 0o644); err != nil {
		t.Fatal(err)
	}
	registrations, err := LoadOperatorScriptHooks(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(registrations) != 1 {
		t.Fatalf("unexpected registration count: %d", len(registrations))
	}
	registration := registrations[0]
	if registration.PluginID != OperatorPluginID || registration.PluginFingerprint != digest {
		t.Fatalf("operator binding is wrong: %#v", registration.PluginID)
	}
	if registration.Declaration.ID != "guard-shell" || registration.Declaration.Event != PreTool ||
		registration.Declaration.Action != ActionScript ||
		registration.Declaration.Script == nil ||
		registration.Declaration.Script.Interpreter != "python" ||
		len(registration.Declaration.Script.Args) != 1 {
		t.Fatalf("declaration did not round-trip: %#v", registration.Declaration)
	}
	if err := registration.Validate(); err != nil {
		t.Fatalf("loaded registration is invalid: %v", err)
	}
}

func TestLoadOperatorScriptHooksTreatsMissingDirectoryAsUnconfigured(t *testing.T) {
	registrations, err := LoadOperatorScriptHooks(t.Context(),
		filepath.Join(t.TempDir(), "missing"))
	if err != nil || registrations != nil {
		t.Fatalf("missing hooks directory must mean no script hooks: %#v err=%v", registrations, err)
	}
	if registrations, err := LoadOperatorScriptHooks(t.Context(), ""); err != nil || registrations != nil {
		t.Fatalf("empty hooks directory must mean no script hooks: %#v err=%v", registrations, err)
	}
}

func TestLoadOperatorScriptHooksFailsClosedOnAnomalies(t *testing.T) {
	validBody := echoScriptBody(t, `{"protocol_version":"restricted-hooks.v1","denied":false}`)
	cases := []struct {
		name    string
		file    string
		content string
	}{
		{"unknown declaration fields", "unknown.json",
			`{"protocol_version":"restricted-hooks.v1","id":"a","event":"run_started",
			"action":"script","failure_policy":"deny","timeout_ms":100,"extra":1,
			"script":{"interpreter":"python","path":"s.script","sha256":"` + testHookDigest("x") + `"}}`},
		{"trailing declaration JSON", "trailing.json",
			`{"protocol_version":"restricted-hooks.v1","id":"a","event":"run_started",
			"action":"script","failure_policy":"deny","timeout_ms":100,
			"script":{"interpreter":"python","path":"s.script","sha256":"` + testHookDigest("x") + `"}}` + "\n{}"},
		{"declarative action from operator file", "declarative.json",
			`{"protocol_version":"restricted-hooks.v1","id":"a","event":"run_started",
			"action":"record","failure_policy":"continue","timeout_ms":100}`},
		{"script path escape", "escape.json",
			`{"protocol_version":"restricted-hooks.v1","id":"a","event":"run_started",
			"action":"script","failure_policy":"deny","timeout_ms":100,
			"script":{"interpreter":"python","path":"../outside.script","sha256":"` + testHookDigest("x") + `"}}`},
		{"absolute script path", "absolute.json",
			`{"protocol_version":"restricted-hooks.v1","id":"a","event":"run_started",
			"action":"script","failure_policy":"deny","timeout_ms":100,
			"script":{"interpreter":"python","path":"/etc/passwd","sha256":"` + testHookDigest("x") + `"}}`},
		{"missing script digest", "digest.json",
			`{"protocol_version":"restricted-hooks.v1","id":"a","event":"run_started",
			"action":"script","failure_policy":"deny","timeout_ms":100,
			"script":{"interpreter":"python","path":"missing.script","sha256":"` + testHookDigest("x") + `"}}`},
		{"script on post_tool", "post.json",
			`{"protocol_version":"restricted-hooks.v1","id":"a","event":"post_tool",
			"action":"script","failure_policy":"continue","timeout_ms":100,
			"script":{"interpreter":"python","path":"s.script","sha256":"` + testHookDigest("x") + `"}}`},
	}
	for _, testCase := range cases {
		root := t.TempDir()
		writeOperatorScript(t, root, "s.script", validBody)
		if err := os.WriteFile(filepath.Join(root, testCase.file),
			[]byte(testCase.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOperatorScriptHooks(t.Context(), root); err == nil {
			t.Fatalf("%s: anomalous operator declaration was accepted", testCase.name)
		}
	}
}

func TestLoadOperatorScriptHooksRejectsOversizedAndDriftedContent(t *testing.T) {
	root := t.TempDir()
	oversized := make([]byte, MaxDeclarationBytes+1)
	for index := range oversized {
		oversized[index] = ' '
	}
	if err := os.WriteFile(filepath.Join(root, "big.json"), oversized, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOperatorScriptHooks(t.Context(), root); err == nil {
		t.Fatal("oversized declaration was accepted")
	}

	drifted := t.TempDir()
	digest := writeOperatorScript(t, drifted, "s.script", validBodyPlaceholder)
	declaration := fmt.Sprintf(`{"protocol_version":%q,"id":"a","event":"run_started",
		"action":"script","failure_policy":"deny","timeout_ms":100,
		"script":{"interpreter":"python","path":"s.script","sha256":%q}}`,
		ProtocolVersion, digest)
	if err := os.WriteFile(filepath.Join(drifted, "a.json"), []byte(declaration), 0o644); err != nil {
		t.Fatal(err)
	}
	writeOperatorScript(t, drifted, "s.script", "# drifted content\n")
	if _, err := LoadOperatorScriptHooks(t.Context(), drifted); err == nil {
		t.Fatal("drifted script content was accepted")
	}
}

func TestLoadOperatorScriptHooksBoundsDeclarationCount(t *testing.T) {
	root := t.TempDir()
	writeOperatorScript(t, root, "s.script", validBodyPlaceholder)
	digest := scriptDigest([]byte(validBodyPlaceholder))
	for index := 0; index <= MaxScriptDeclarations; index++ {
		declaration := fmt.Sprintf(`{"protocol_version":%q,"id":"h-%d","event":"run_started",
			"action":"script","failure_policy":"deny","timeout_ms":100,
			"script":{"interpreter":"python","path":"s.script","sha256":%q}}`,
			ProtocolVersion, index, digest)
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("h%03d.json", index)),
			[]byte(declaration), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := LoadOperatorScriptHooks(t.Context(), root); err == nil {
		t.Fatal("hooks directory above the declaration ceiling was accepted")
	}
}

func TestLoadOperatorScriptHooksIgnoresDirectoriesAndNonJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	registrations, err := LoadOperatorScriptHooks(t.Context(), root)
	if err != nil || len(registrations) != 0 {
		t.Fatalf("directories and non-JSON files must be ignored: %#v err=%v", registrations, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(root, filepath.Join(root, "link.json")); err != nil {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		if _, err := LoadOperatorScriptHooks(t.Context(), root); err == nil {
			t.Fatal("symlinked declaration was accepted")
		}
	}
}

const validBodyPlaceholder = `{"protocol_version":"restricted-hooks.v1","denied":false}`

func TestScriptRegistrationJoinsPluginRegistryGeneration(t *testing.T) {
	engine := NewEngine(nil)
	spec := ScriptSpec{Interpreter: "python", Path: "check.py", SHA256: testHookDigest("script")}
	operator := Registration{PluginID: OperatorPluginID,
		PluginFingerprint: spec.SHA256,
		Declaration: Declaration{ProtocolVersion: ProtocolVersion, ID: "guard",
			Event: RunStarted, Action: ActionScript, FailurePolicy: FailureDeny,
			TimeoutMillis: 100, Script: &spec}}
	plugin := Registration{PluginID: "plugin-a", PluginFingerprint: testHookDigest("plugin"),
		Declaration: Declaration{ProtocolVersion: ProtocolVersion, ID: "annotate",
			Event: RunStarted, Action: ActionAnnotate, FailurePolicy: FailureDeny,
			TimeoutMillis: 100, Message: "reviewed"}}
	if err := engine.Replace([]Registration{operator, plugin}); err != nil {
		t.Fatal(err)
	}
	generation := engine.Generation()
	if err := engine.Replace([]Registration{plugin, operator}); err != nil {
		t.Fatal(err)
	}
	if engine.Generation() != generation {
		t.Fatal("registration order changed the registry generation")
	}
	spec.SHA256 = testHookDigest("script-v2")
	if err := engine.Replace([]Registration{operator, plugin}); err != nil {
		t.Fatal(err)
	}
	if engine.Generation() == generation {
		t.Fatal("script digest change did not advance the registry generation")
	}
	if _, err := json.Marshal(operator); err != nil {
		t.Fatal(err)
	}
}
