package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"cyberagent-workbench/internal/hooks"
)

func scriptHookDeclaration() hooks.Declaration {
	digest := sha256.Sum256([]byte("operator script"))
	return hooks.Declaration{ProtocolVersion: hooks.ProtocolVersion, ID: "run-script",
		Event: hooks.PreTool, Action: hooks.ActionScript, FailurePolicy: hooks.FailureDeny,
		TimeoutMillis: 2_000,
		Script: &hooks.ScriptSpec{Interpreter: "python", Path: "check.py",
			SHA256: hex.EncodeToString(digest[:])}}
}

func declarativeManifest(t *testing.T, declaration hooks.Declaration) Manifest {
	t.Helper()
	return Manifest{ProtocolVersion: ProtocolVersion, ID: "guard-plugin", Name: "Guard",
		Version: "1.0.0", Publisher: "publisher", Description: "declarative guard",
		Capabilities: []Capability{CapabilityHooks},
		Files:        []FileEntry{{Path: "docs/guard.md", Bytes: 1, SHA256: strings.Repeat("a", 64)}},
		Hooks:        []hooks.Declaration{declaration}}
}

func TestPluginManifestRejectsScriptHookDeclarations(t *testing.T) {
	manifest := declarativeManifest(t, scriptHookDeclaration())
	if err := manifest.Validate(); err == nil ||
		err.Error() != "plugin hooks cannot execute scripts" {
		t.Fatalf("plugin manifest accepted a script hook: err=%v", err)
	}
}

func TestPluginManifestRejectsScriptSpecificationOnDeclarativeAction(t *testing.T) {
	declaration := scriptHookDeclaration()
	declaration.Action = hooks.ActionRecord
	declaration.Message = "record only"
	manifest := declarativeManifest(t, declaration)
	if err := manifest.Validate(); err == nil ||
		err.Error() != "plugin hooks cannot declare a script specification" {
		t.Fatalf("plugin manifest accepted a script specification: err=%v", err)
	}
}

func TestPluginManifestAcceptsDeclarativeHooks(t *testing.T) {
	declaration := hooks.Declaration{ProtocolVersion: hooks.ProtocolVersion,
		ID: "deny-shell", Event: hooks.PreTool, Action: hooks.ActionDeny,
		FailurePolicy: hooks.FailureDeny, TimeoutMillis: 1_000, ToolNames: []string{"shell"},
		Message: "shell disabled by reviewed plugin"}
	manifest := declarativeManifest(t, declaration)
	if err := manifest.Validate(); err != nil {
		t.Fatalf("declarative plugin hook was rejected: %v", err)
	}
}
