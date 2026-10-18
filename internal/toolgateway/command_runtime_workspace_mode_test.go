package toolgateway

import (
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/runner"
)

func TestCommandRuntimeSharedServiceRequiresExplicitStart(t *testing.T) {
	input, _, err := normalizeCommandRuntimePayload(commandRuntimeValidPayload("Write-Output ready"))
	if err != nil {
		t.Fatal(err)
	}
	input.Commands[0].WorkspaceMode = runner.CommandRuntimeWorkspaceSharedService
	if err := input.Validate(); err == nil {
		t.Fatal("foreground service accepted")
	}
	input.Action, input.FailurePolicy, input.MaxBytes = CommandRuntimeActionStart, "", nil
	raw, _ := json.Marshal(input)
	got, canonical, err := normalizeCommandRuntimePayload(raw)
	if err != nil || got.Commands[0].WorkspaceMode != runner.CommandRuntimeWorkspaceSharedService || !strings.Contains(string(canonical), `"workspace_mode":"shared_service"`) {
		t.Fatalf("service start lost its intent: %s %v", canonical, err)
	}
	for _, value := range []string{`null`, `""`, `"read_only"`, `true`} {
		invalid := strings.Replace(string(raw), `"workspace_mode":"shared_service"`, `"workspace_mode":`+value, 1)
		if _, _, err := normalizeCommandRuntimePayload([]byte(invalid)); err == nil {
			t.Fatalf("accepted invalid workspace mode %s", value)
		}
	}
}
