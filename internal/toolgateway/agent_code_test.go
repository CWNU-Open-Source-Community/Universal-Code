package toolgateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
)

func TestAgentCodeCapabilityMatrix(t *testing.T) {
	base := AgentCodeCapabilityContext{RunID: "run-1", MissionID: "mission-1",
		RootAgentID: "agent-1", WorkspaceID: "workspace-1",
		RootFingerprint: strings.Repeat("a", 64), Surface: domain.ExecutionSurfaceCode,
		Phase: domain.ExecutionPhasePlan, Role: domain.AgentRoleRoot,
		Profile: domain.ProfileCode, PermissionMode: domain.RunExecutionPermissionConservative,
		ModeRevision: 1, PermissionRevision: 1}

	plan := AgentCodeCapabilities(base)
	if availableAgentCodeCount(plan) != 6 {
		t.Fatalf("Code/Plan must expose exactly six bounded read tools: %#v", plan)
	}
	for _, tool := range plan.Tools {
		if tool.ReadOnly != tool.Available {
			t.Fatalf("Code/Plan availability mismatch: %#v", tool)
		}
	}

	deliverScope := base
	deliverScope.Phase = domain.ExecutionPhaseDeliver
	deliver := AgentCodeCapabilities(deliverScope)
	if availableAgentCodeCount(deliver) != len(AgentCodeToolDefinitions()) ||
		deliver.Generation == plan.Generation {
		t.Fatalf("Code/Deliver capability mismatch: %#v", deliver)
	}

	reviewScope := deliverScope
	reviewScope.Profile = domain.ProfileReview
	if availableAgentCodeCount(AgentCodeCapabilities(reviewScope)) != 6 {
		t.Fatal("Review profile received workspace mutation tools")
	}
	cyberScope := deliverScope
	cyberScope.Surface = domain.ExecutionSurfaceCyber
	if availableAgentCodeCount(AgentCodeCapabilities(cyberScope)) != 0 {
		t.Fatal("Cyber surface received agent code tools")
	}
	specialistScope := deliverScope
	specialistScope.Role = domain.AgentRoleSpecialist
	if availableAgentCodeCount(AgentCodeCapabilities(specialistScope)) != 0 {
		t.Fatal("Specialist received agent code tools")
	}
}

func TestAgentCodeAuthorityRoundTripAndGenerationBinding(t *testing.T) {
	scope := AgentCodeCapabilityContext{RunID: "run-1", MissionID: "mission-1",
		RootAgentID: "agent-1", WorkspaceID: "workspace-1",
		RootFingerprint: strings.Repeat("b", 64), Surface: domain.ExecutionSurfaceCode,
		Phase: domain.ExecutionPhaseDeliver, Role: domain.AgentRoleRoot,
		Profile: domain.ProfileCode, PermissionMode: domain.RunExecutionPermissionApproval,
		ModeRevision: 3, PermissionRevision: 4}
	authority, err := NewAgentCodeCallAuthority(scope, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeAgentCodeCallAuthority(authority)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeAgentCodeCallAuthority(encoded)
	if err != nil || decoded != authority {
		t.Fatalf("authority round trip=%#v err=%v", decoded, err)
	}
	var tampered map[string]any
	if err := json.Unmarshal(encoded, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered["permission_revision"] = float64(5)
	tamperedJSON, _ := json.Marshal(tampered)
	if _, err := DecodeAgentCodeCallAuthority(tamperedJSON); err == nil {
		t.Fatal("tampered authority generation was accepted")
	}
}

func TestAgentCodePayloadsAreStrictAndBounded(t *testing.T) {
	if _, err := NormalizeAgentCodePayload(WorkspaceListTool,
		json.RawMessage(`{"version":"agent-code-tools.v1","path":".","limit":10,"include_hidden":true}`)); err == nil {
		t.Fatal("model-controlled hidden-file authority was accepted")
	}
	large := `{"version":"agent-code-tools.v1","action":"create","path":"large.txt","expected_sha256":"missing","content":"` +
		strings.Repeat("x", MaxAgentCodeCreateBytes+1) + `"}`
	if _, err := NormalizeAgentCodePayload(WorkspaceChangeTool, json.RawMessage(large)); err == nil {
		t.Fatal("oversized create payload was accepted")
	}
	move := json.RawMessage(`{"version":"agent-code-tools.v1","action":"move","path":"a.txt","expected_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","destination_path":"b.txt","destination_expected_sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)
	if _, err := NormalizeAgentCodePayload(WorkspaceChangeTool, move); err == nil {
		t.Fatal("move accepted an existing destination hash")
	}
	moveApply := json.RawMessage(`{"version":"agent-code-tools.v1","edit_id":"edit-move-1","expected_action":"move","expected_original_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expected_proposed_sha256":"missing"}`)
	if _, err := NormalizeAgentCodePayload(WorkspaceApplyTool, moveApply); err != nil {
		t.Fatalf("valid move apply sentinel was rejected: %v", err)
	}
	secretCreate := json.RawMessage(`{"version":"agent-code-tools.v1","action":"create","path":"secret.txt","expected_sha256":"missing","content":"API_TOKEN=not-a-real-secret-value"}`)
	if _, err := NormalizeAgentCodePayload(WorkspaceChangeTool, secretCreate); err == nil {
		t.Fatal("secret-shaped create content was accepted for durable persistence")
	}
	secretPatch := json.RawMessage(`{"version":"agent-code-tools.v1","action":"propose_patch","path":"config.txt","expected_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","replacements":[{"old_text":"mode=dev","new_text":"PASSWORD=not-a-real-password","expected_occurrences":1}]}`)
	if _, err := NormalizeAgentCodePayload(WorkspaceChangeTool, secretPatch); err == nil {
		t.Fatal("secret-shaped patch content was accepted for durable persistence")
	}
	secretQuery := `{"version":"agent-code-tools.v1","query":"sk-` +
		strings.Repeat("a", 24) + `","pattern":"**","limit":10,"case_sensitive":true}`
	if _, err := NormalizeAgentCodePayload(WorkspaceGrepTool,
		json.RawMessage(secretQuery)); err == nil {
		t.Fatal("secret-shaped read argument was accepted for durable persistence")
	}
	if _, err := NormalizeAgentCodePayload(GitHubEvidenceListTool,
		json.RawMessage(`{"version":"agent-code-tools.v1","limit":51}`)); err == nil {
		t.Fatal("oversized GitHub evidence list was accepted")
	}
	if _, err := NormalizeAgentCodePayload(GitHubEvidenceReadTool,
		json.RawMessage(`{"version":"agent-code-tools.v1","evidence_id":"ghg-1","token":"secret"}`)); err == nil {
		t.Fatal("unknown GitHub evidence argument was accepted")
	}
}

func TestAgentCodePayloadPreservesWhitespaceInWorkspaceCoordinates(t *testing.T) {
	const version = AgentCodeRegistryVersion
	const exact = " note.txt "
	for _, test := range []struct {
		name    ToolName
		field   string
		payload any
	}{
		{WorkspaceListTool, "path", WorkspaceListPayload{Version: version, Path: " src ", Limit: 10}},
		{WorkspaceReadTool, "path", WorkspaceReadPayload{Version: version, Path: exact, StartLine: 1, EndLine: 2}},
		{WorkspaceGlobTool, "pattern", WorkspaceGlobPayload{Version: version, Pattern: exact, Limit: 10}},
		{WorkspaceGrepTool, "pattern", WorkspaceGrepPayload{Version: version, Query: "needle", Pattern: exact, Limit: 10}},
		{WorkspaceChangeTool, "path", WorkspaceChangePayload{Version: version, Action: "create", Path: exact, ExpectedSHA256: "missing", Content: "draft"}},
		{WorkspaceChangeTool, "destination_path", WorkspaceChangePayload{Version: version, Action: "move", Path: "note.txt", ExpectedSHA256: strings.Repeat("a", 64), DestinationPath: exact, DestinationExpectedSHA256: "missing"}},
		{WorkspaceDeleteTool, "confirm_path", WorkspaceDeletePayload{Version: version, Action: "propose", Path: exact, ConfirmPath: exact, ExpectedSHA256: strings.Repeat("a", 64)}},
	} {
		t.Run(string(test.name)+"/"+test.field, func(t *testing.T) {
			raw, err := json.Marshal(test.payload)
			if err != nil {
				t.Fatal(err)
			}
			normalized, err := NormalizeAgentCodePayload(test.name, raw)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, normalized) {
				t.Fatalf("workspace coordinate changed from %s to %s", raw, normalized)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			fields[test.field] = json.RawMessage("\"note\\n.txt\"")
			invalid, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NormalizeAgentCodePayload(test.name, invalid); err == nil {
				t.Fatal("workspace coordinate containing a control character was accepted")
			}
		})
	}
}

func TestAgentCodeDefinitionsDescribeAutomaticOrdinaryChanges(t *testing.T) {
	change, found := AgentCodeToolDefinition(WorkspaceChangeTool)
	if !found || !strings.Contains(change.Description,
		"create, replace, non-overwriting move, or reversal that resolves to create/replace") ||
		!strings.Contains(change.Description,
			"Direct deletes and reversals that resolve to delete require operator review") {
		t.Fatalf("workspace_change Full Access description=%q", change.Description)
	}
	apply, found := AgentCodeToolDefinition(WorkspaceApplyTool)
	if !found || !strings.Contains(apply.Description, "create/replace/move") {
		t.Fatalf("workspace_apply Full Access description=%q", apply.Description)
	}
}

func availableAgentCodeCount(snapshot AgentCodeCapabilitySnapshot) int {
	count := 0
	for _, tool := range snapshot.Tools {
		if tool.Available {
			count++
		}
	}
	return count
}

func FuzzAgentCodePayloadNormalizationIsBoundedAndIdempotent(f *testing.F) {
	f.Add(string(WorkspaceReadTool),
		`{"version":"agent-code-tools.v1","path":"README.md","start_line":1,"end_line":20}`)
	f.Add(string(WorkspaceChangeTool),
		`{"version":"agent-code-tools.v1","action":"create","path":"note.txt","expected_sha256":"missing","content":"safe text\n"}`)
	f.Add(string(WorkspaceDeleteTool), `{}`)
	f.Fuzz(func(t *testing.T, rawName string, rawPayload string) {
		if len(rawName) > 128 || len(rawPayload) > MaxAgentCodePayloadBytes+1024 {
			return
		}
		normalized, err := NormalizeAgentCodePayload(ToolName(rawName),
			json.RawMessage(rawPayload))
		if err != nil {
			return
		}
		if len(normalized) == 0 || len(normalized) > MaxAgentCodePayloadBytes ||
			!json.Valid(normalized) {
			t.Fatalf("accepted payload escaped canonical bounds: %q", normalized)
		}
		repeated, err := NormalizeAgentCodePayload(ToolName(rawName), normalized)
		if err != nil || !bytes.Equal(normalized, repeated) {
			t.Fatalf("accepted payload was not idempotent: first=%q second=%q err=%v",
				normalized, repeated, err)
		}
	})
}
