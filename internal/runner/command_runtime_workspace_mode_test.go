package runner

import (
	"encoding/json"
	"testing"
)

func TestCommandRuntimeWorkspaceModeIsBoundToImmutableIntent(t *testing.T) {
	spec := CommandRuntimeResolvedSpec{Spec: CommandRuntimeSpec{Version: CommandRuntimeProtocolVersion}}
	legacyIntent, legacyFingerprint := commandRuntimeIntentJSON(spec), CommandRuntimeSpecFingerprint(spec)
	var legacy map[string]any
	if err := json.Unmarshal([]byte(legacyIntent), &legacy); err != nil {
		t.Fatal(err)
	}
	if _, exists := legacy["workspace_mode"]; exists {
		t.Fatal("default mode changed historical intent encoding")
	}
	spec.Spec.WorkspaceMode = CommandRuntimeWorkspaceSharedService
	intent := commandRuntimeIntentJSON(spec)
	if legacyFingerprint == CommandRuntimeSpecFingerprint(spec) {
		t.Fatal("service mode did not change the reviewed command fingerprint")
	}
	job := CommandRuntimeJob{IntentJSON: intent}
	if CommandRuntimeJobWorkspaceMode(job) != CommandRuntimeWorkspaceSharedService ||
		ProjectCommandRuntimeJob(job).WorkspaceMode != CommandRuntimeWorkspaceSharedService {
		t.Fatal("service mode was lost in the persisted intent or projection")
	}
	for _, raw := range []string{legacyIntent, `{`, `{"workspace_mode":"unrecognized"}`} {
		if CommandRuntimeJobWorkspaceMode(CommandRuntimeJob{IntentJSON: raw}) != "" {
			t.Fatalf("historical or invalid intent acquired service behavior: %s", raw)
		}
	}
}
