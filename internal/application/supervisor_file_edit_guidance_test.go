package application

import (
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/toolgateway"
)

func TestSupervisorFileEditGuidanceReflectsAdvertisedScopeOnly(t *testing.T) {
	for _, tc := range []struct {
		name                                          string
		mode                                          domain.RunExecutionPermissionMode
		active, changeOffered, applyOffered, wantAuto bool
	}{
		{"full live", domain.RunExecutionPermissionFullAccess, true, true, true, true},
		{"full without runtime grant", domain.RunExecutionPermissionFullAccess, false, true, true, false},
		{"operator review", domain.RunExecutionPermissionApproval, false, true, true, false},
		{"ask prepared policy", domain.RunExecutionPermissionAsk, false, true, true, true},
		{"auto prepared policy", domain.RunExecutionPermissionAuto, false, true, true, true},
		{"full prepared policy", domain.RunExecutionPermissionFull, true, true, true, true},
		{"read only offered", domain.RunExecutionPermissionFullAccess, true, false, false, false},
		{"boundary without tools", domain.RunExecutionPermissionFullAccess, true, false, false, false},
		{"apply filtered", domain.RunExecutionPermissionApproval, false, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := toolgateway.AgentCodeCapabilityContext{Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver,
				Role: domain.AgentRoleRoot, Profile: domain.ProfileCode, PermissionMode: tc.mode}
			if tc.active {
				scope.PermissionGeneration, scope.PermissionRuntimeEpoch = 1, "runtime-current"
			}
			request := llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "Read this file only; do not change it."}}}
			if tc.changeOffered {
				request.Tools = append(request.Tools, llm.ToolSpec{Name: "workspace_change"})
			}
			if tc.applyOffered {
				request.Tools = append(request.Tools, llm.ToolSpec{Name: "workspace_apply"})
			}
			if tc.name == "read only offered" {
				request.Tools = append(request.Tools, llm.ToolSpec{Name: "workspace_read"})
			}
			result := supervisorFileEditGuidance(request, toolgateway.AgentCodeCapabilities(scope), 0)
			if !tc.changeOffered {
				if len(result.Messages) != 1 {
					t.Fatal("guidance advertised an unavailable mutation")
				}
				return
			}
			text := result.Messages[0].Content
			if len(result.Messages) != 2 || result.Messages[1].Content != request.Messages[0].Content ||
				!strings.Contains(text, "a newer denial takes precedence") || !strings.Contains(text, "Read-only tasks still require no edits") ||
				!strings.Contains(text, "when review is required, workspace_change records a proposal without writing bytes") || !strings.Contains(text, "establish neither current permission nor revocation") {
				t.Fatal("guidance confused observation with authority", text)
			}
			if strings.Contains(text, "Eligible prepared operations can receive recorded automatic authorization") != tc.wantAuto {
				t.Fatal("incorrect automatic authorization eligibility", text)
			}
			if strings.Contains(text, `"workspace_apply":`) != tc.applyOffered {
				t.Fatal("advertised a filtered apply tool", text)
			}
		})
	}
}
