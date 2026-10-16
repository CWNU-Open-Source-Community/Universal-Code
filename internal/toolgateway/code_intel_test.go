package toolgateway

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
)

func TestCodeIntelCapabilityMatrixReusesAgentCodeReadAuthority(t *testing.T) {
	base := AgentCodeCapabilityContext{RunID: "run-1", MissionID: "mission-1",
		RootAgentID: "agent-1", WorkspaceID: "workspace-1",
		RootFingerprint: strings.Repeat("a", 64), Surface: domain.ExecutionSurfaceCode,
		Phase: domain.ExecutionPhasePlan, Role: domain.AgentRoleRoot,
		Profile: domain.ProfileReview, PermissionMode: domain.RunExecutionPermissionConservative,
		ModeRevision: 1, PermissionRevision: 1}
	for _, phase := range []domain.ExecutionPhase{
		domain.ExecutionPhasePlan, domain.ExecutionPhaseDeliver,
	} {
		for _, profile := range []domain.Profile{
			domain.ProfileCode, domain.ProfileReview, domain.ProfileScript,
		} {
			scope := base
			scope.Phase = phase
			scope.Profile = profile
			if available, reason := CodeIntelScopeEligibility(scope); !available || reason != "" {
				t.Fatalf("Code Root %s/%s refused semantic reads: %q", phase, profile, reason)
			}
		}
	}
	cyber := base
	cyber.Surface = domain.ExecutionSurfaceCyber
	if available, _ := CodeIntelScopeEligibility(cyber); available {
		t.Fatal("Cyber surface received Code Intel tools")
	}
	specialist := base
	specialist.Role = domain.AgentRoleSpecialist
	if available, _ := CodeIntelScopeEligibility(specialist); available {
		t.Fatal("Specialist received Code Intel tools")
	}

	definitions := CodeIntelToolDefinitions()
	if len(definitions) != 10 {
		t.Fatalf("semantic tool count=%d", len(definitions))
	}
	seen := make(map[ToolName]struct{}, len(definitions))
	for _, definition := range definitions {
		if definition.Class != ClassWorkspaceRead || definition.Approval != ApprovalAutomatic ||
			len(definition.InputSchema) == 0 || !json.Valid(definition.InputSchema) {
			t.Fatalf("semantic tool widened authority: %#v", definition)
		}
		if _, exists := seen[definition.Name]; exists {
			t.Fatalf("duplicate semantic tool: %s", definition.Name)
		}
		seen[definition.Name] = struct{}{}
	}
	for _, forbidden := range []ToolName{"code_rename", "code_action", "code_format"} {
		if IsCodeIntelTool(forbidden) {
			t.Fatalf("write-capable LSP tool was registered: %s", forbidden)
		}
	}
}

func TestCodeIntelPayloadsAreStrictBoundedAndCanonical(t *testing.T) {
	digestA := strings.Repeat("a", 64)
	digestB := strings.Repeat("b", 64)
	raw := json.RawMessage(`{"version":"code-intel-lsp.v1","server_id":"gopls","server_generation":"` +
		digestA + `","capability_fingerprint":"` + digestB +
		`","path":"main.go","line":1,"character":2,"include_declaration":true,"limit":20}`)
	value, canonical, err := NormalizeCodeIntelPayload(CodeReferencesTool, raw)
	if err != nil || value.ServerID != "gopls" || !json.Valid(canonical) {
		t.Fatalf("valid semantic payload failed: value=%#v raw=%s err=%v", value, canonical, err)
	}
	_, repeated, err := NormalizeCodeIntelPayload(CodeReferencesTool, canonical)
	if err != nil || !bytes.Equal(canonical, repeated) {
		t.Fatalf("semantic normalization was not idempotent: %s %s %v", canonical, repeated, err)
	}
	invalid := []struct {
		name ToolName
		raw  string
	}{
		{name: CodeDefinitionTool, raw: strings.Replace(string(raw),
			`,"include_declaration":true`, `,"write":true`, 1)},
		{name: CodeReferencesTool, raw: strings.Replace(string(raw), "gopls",
			"token=super-secret-value", 1)},
		{name: CodeWorkspaceSymbolsTool, raw: string(raw)},
		{name: CodeCallHierarchyTool, raw: strings.Replace(string(raw),
			`"include_declaration":true`, `"direction":"delete"`, 1)},
	}
	for _, current := range invalid {
		if _, _, err := NormalizeCodeIntelPayload(current.name,
			json.RawMessage(current.raw)); err == nil {
			t.Fatalf("invalid semantic payload was accepted for %s: %s", current.name, current.raw)
		}
	}
}

func TestCodeIntelVisibleDefinitionsPinExactServerGeneration(t *testing.T) {
	snapshot := CodeIntelCapabilitySnapshot{ProtocolVersion: CodeIntelProtocolVersion,
		Servers: []CodeIntelServerCapability{{ServerID: "gopls", ServerName: "gopls",
			Languages: []string{"go"}, Generation: strings.Repeat("a", 64),
			CapabilityFingerprint: strings.Repeat("b", 64),
			Tools:                 []ToolName{CodeDefinitionTool, CodeHoverTool}}}}
	definitions := snapshot.VisibleDefinitions()
	if len(definitions) != 2 {
		t.Fatalf("visible definitions=%#v", definitions)
	}
	for _, definition := range definitions {
		var schema map[string]any
		if json.Unmarshal(definition.InputSchema, &schema) != nil {
			t.Fatalf("invalid semantic schema: %s", definition.InputSchema)
		}
		if schema["type"] != "object" {
			t.Fatalf("provider function schemas require an object root: %s", definition.InputSchema)
		}
		raw := string(definition.InputSchema)
		for _, binding := range []string{"gopls", strings.Repeat("a", 64),
			strings.Repeat("b", 64)} {
			if !strings.Contains(raw, binding) {
				t.Fatalf("schema omitted binding %q: %s", binding, raw)
			}
		}
	}
}

func FuzzCodeIntelPayloadNormalizationIsBoundedAndIdempotent(f *testing.F) {
	f.Add(string(CodeDefinitionTool), `{"version":"code-intel-lsp.v1"}`)
	f.Add(string(CodeWorkspaceSymbolsTool), `{}`)
	f.Fuzz(func(t *testing.T, rawName, rawPayload string) {
		if len(rawName) > 128 || len(rawPayload) > MaxAgentCodePayloadBytes+1024 {
			return
		}
		_, normalized, err := NormalizeCodeIntelPayload(ToolName(rawName),
			json.RawMessage(rawPayload))
		if err != nil {
			return
		}
		if len(normalized) == 0 || len(normalized) > MaxAgentCodePayloadBytes ||
			!json.Valid(normalized) {
			t.Fatalf("accepted semantic payload escaped bounds: %q", normalized)
		}
		_, repeated, err := NormalizeCodeIntelPayload(ToolName(rawName), normalized)
		if err != nil || !bytes.Equal(normalized, repeated) {
			t.Fatalf("semantic payload was not idempotent: %q %q %v",
				normalized, repeated, err)
		}
	})
}
