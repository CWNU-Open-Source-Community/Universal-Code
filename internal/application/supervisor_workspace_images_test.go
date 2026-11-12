package application

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/imageattachment"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/toolgateway"
)

type workspaceImageProjectionStore struct {
	AgentRunnerStore
	metadata domain.WorkspaceImage
	pixels   []byte
	saveKey  string
}

const otherSavedWorkspaceImageID = "image-from-another-call"

func (s *workspaceImageProjectionStore) GetWorkspaceImage(_ context.Context, workspaceID, imageID string) (domain.WorkspaceImage, []byte, error) {
	if workspaceID == s.metadata.WorkspaceID && imageID == otherSavedWorkspaceImageID {
		metadata := s.metadata
		metadata.ID = otherSavedWorkspaceImageID
		return metadata, append([]byte(nil), s.pixels...), nil
	}
	if workspaceID != s.metadata.WorkspaceID || imageID != s.metadata.ID {
		return domain.WorkspaceImage{}, nil, apperror.New(apperror.CodeNotFound, "image not found")
	}
	return s.metadata, append([]byte(nil), s.pixels...), nil
}

func (s *workspaceImageProjectionStore) GetWorkspaceImageByOperationKey(_ context.Context, workspaceID, key string) (domain.WorkspaceImage, bool, error) {
	if workspaceID != s.metadata.WorkspaceID || key != s.saveKey {
		return domain.WorkspaceImage{}, false, nil
	}
	return s.metadata, true, nil
}

func workspaceImageProjectionFixture(t *testing.T) (*AgentRunner, domain.SupervisorTurn, domain.SupervisorToolCall) {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	metadata, err := imageattachment.Validate(encoded.Bytes(), "image/png", "preview.png")
	if err != nil {
		t.Fatal(err)
	}
	metadata.ID, metadata.WorkspaceID = "image-original", "workspace-image"
	scope := toolgateway.AgentCodeCapabilityContext{RunID: "run-image", MissionID: "mission-image",
		RootAgentID: "agent-image", WorkspaceID: metadata.WorkspaceID, RootFingerprint: strings.Repeat("a", 64),
		Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Role: domain.AgentRoleRoot,
		Profile: domain.ProfileCode, PermissionMode: domain.RunExecutionPermissionConservative,
		ModeRevision: 1, PermissionRevision: 1}
	authority, err := toolgateway.NewAgentCodeCallAuthority(scope, "session-image")
	if err != nil {
		t.Fatal(err)
	}
	rawAuthority, err := toolgateway.EncodeAgentCodeCallAuthority(authority)
	if err != nil {
		t.Fatal(err)
	}
	turn := domain.SupervisorTurn{Run: domain.Run{ID: scope.RunID, MissionID: scope.MissionID, SessionID: authority.SessionID},
		Mission:    domain.Mission{ID: scope.MissionID, WorkspaceID: metadata.WorkspaceID},
		Agent:      domain.AgentNode{ID: scope.RootAgentID},
		Checkpoint: domain.SupervisorCheckpoint{RunID: scope.RunID, AttemptID: "attempt-image", NextTurn: 1}}
	completed := time.Now().UTC()
	call := domain.SupervisorToolCall{RunID: scope.RunID, Turn: 1, AttemptID: turn.Checkpoint.AttemptID,
		AgentID: scope.RootAgentID, AgentAttemptID: turn.Checkpoint.AttemptID, AgentAttribution: domain.AgentAttributionRecorded,
		Round: 1, Position: 1, ModelAttempt: 1, CallID: "call-view-image", ToolName: string(toolgateway.WorkspaceViewImageTool),
		PayloadJSON: `{"version":"agent-code-tools.v1","path":"preview.png"}`, AuthorityJSON: string(rawAuthority),
		Status: domain.SupervisorToolCompleted, CreatedAt: completed, CompletedAt: &completed}
	output, err := json.Marshal(workspaceImageResult{Version: "workspace_image_result.v1", Path: "preview.png",
		FileWorkspaceID: metadata.WorkspaceID, RootFingerprint: scope.RootFingerprint, Image: metadata})
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(supervisorToolResultEnvelope{Version: supervisorToolResultVersion,
		Tool: call.ToolName, Status: "completed", Stdout: string(output),
		Metadata: map[string]string{"workspace_id": metadata.WorkspaceID, "root_fingerprint": scope.RootFingerprint}})
	if err != nil {
		t.Fatal(err)
	}
	call.ResultJSON = string(result)
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
	key := workspaceImageSaveKey(call.RunID, call.CallID, supervisorWorkspaceImageOperationKey(call), json.RawMessage(call.PayloadJSON))
	return &AgentRunner{store: &workspaceImageProjectionStore{metadata: metadata, pixels: encoded.Bytes(), saveKey: key}}, turn, call
}

func TestSupervisorWorkspaceImagesEnforcesExactCallAndOwner(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.SupervisorTurn, *domain.SupervisorToolCall, *llm.ChatRequest)
	}{
		{"other run", func(turn *domain.SupervisorTurn, _ *domain.SupervisorToolCall, _ *llm.ChatRequest) {
			turn.Run.ID = "other-run"
		}},
		{"other session", func(turn *domain.SupervisorTurn, _ *domain.SupervisorToolCall, _ *llm.ChatRequest) {
			turn.Run.SessionID = "other-session"
		}},
		{"other root agent", func(turn *domain.SupervisorTurn, _ *domain.SupervisorToolCall, _ *llm.ChatRequest) {
			turn.Agent.ID = "other-root"
		}},
		{"legacy actor attribution", func(_ *domain.SupervisorTurn, call *domain.SupervisorToolCall, _ *llm.ChatRequest) {
			call.AgentAttribution = domain.AgentAttributionLegacyRoot
		}},
		{"operator attribution", func(_ *domain.SupervisorTurn, call *domain.SupervisorToolCall, _ *llm.ChatRequest) {
			call.AgentAttribution = domain.AgentAttributionOperatorRoot
		}},
		{"different result", func(_ *domain.SupervisorTurn, _ *domain.SupervisorToolCall, request *llm.ChatRequest) {
			request.Messages[0].ToolResults[0].Content = "{}"
		}},
		{"different durable call", func(_ *domain.SupervisorTurn, call *domain.SupervisorToolCall, request *llm.ChatRequest) {
			call.CallID = "other-image-call"
			request.Messages[0].ToolResults[0].ToolCallID = call.CallID
		}},
		{"different image ID", func(_ *domain.SupervisorTurn, call *domain.SupervisorToolCall, request *llm.ChatRequest) {
			var envelope supervisorToolResultEnvelope
			var output workspaceImageResult
			_ = json.Unmarshal([]byte(call.ResultJSON), &envelope)
			_ = json.Unmarshal([]byte(envelope.Stdout), &output)
			output.Image.ID = "other-image"
			raw, _ := json.Marshal(output)
			envelope.Stdout = string(raw)
			raw, _ = json.Marshal(envelope)
			call.ResultJSON = string(raw)
			request.Messages[0].ToolResults[0].Content = call.ResultJSON
		}},
		{"valid image receipt from another call in the same workspace", func(_ *domain.SupervisorTurn, call *domain.SupervisorToolCall, request *llm.ChatRequest) {
			var envelope supervisorToolResultEnvelope
			var output workspaceImageResult
			_ = json.Unmarshal([]byte(call.ResultJSON), &envelope)
			_ = json.Unmarshal([]byte(envelope.Stdout), &output)
			output.Image.ID = otherSavedWorkspaceImageID
			raw, _ := json.Marshal(output)
			envelope.Stdout = string(raw)
			raw, _ = json.Marshal(envelope)
			call.ResultJSON = string(raw)
			request.Messages[0].ToolResults[0].Content = call.ResultJSON
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, turn, call := workspaceImageProjectionFixture(t)
			request := llm.ChatRequest{Messages: []llm.Message{{Role: "user", ToolResults: []llm.ToolResult{{ToolCallID: call.CallID, Content: call.ResultJSON}}}}}
			valid, err := runner.supervisorWorkspaceImages(t.Context(), turn, request, []domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{call}}}, true)
			if err != nil || len(valid.Messages[0].Images) != 1 {
				t.Fatalf("valid fixture not projected: %v", err)
			}
			test.mutate(&turn, &call, &request)
			if _, err := runner.supervisorWorkspaceImages(t.Context(), turn, request, []domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{call}}}, true); err == nil {
				t.Fatal("mismatched call/image owner was accepted")
			}
		})
	}
}

func TestSupervisorWorkspaceImagesAcceptsProvenRootAttributionSources(t *testing.T) {
	for _, source := range []domain.AgentAttributionSource{domain.AgentAttributionRecorded, domain.AgentAttributionSupervisorRoot} {
		t.Run(string(source), func(t *testing.T) {
			runner, turn, call := workspaceImageProjectionFixture(t)
			call.AgentAttribution = source
			request := llm.ChatRequest{Messages: []llm.Message{{Role: "user", ToolResults: []llm.ToolResult{{ToolCallID: call.CallID, Content: call.ResultJSON}}}}}
			projected, err := runner.supervisorWorkspaceImages(t.Context(), turn, request, []domain.SupervisorToolRound{{Calls: []domain.SupervisorToolCall{call}}}, true)
			if err != nil || len(projected.Messages[0].Images) != 1 {
				t.Fatalf("valid %s root image rejected: %v", source, err)
			}
		})
	}
}
