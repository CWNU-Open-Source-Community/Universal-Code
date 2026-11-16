package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	saveKeys map[string]bool
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
	if workspaceID != s.metadata.WorkspaceID || !s.saveKeys[key] {
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
	return &AgentRunner{store: &workspaceImageProjectionStore{metadata: metadata, pixels: encoded.Bytes(), saveKeys: map[string]bool{key: true}}}, turn, call
}

func TestSupervisorWorkspaceImagesBoundsVisualHistoryAcrossRounds(t *testing.T) {
	for _, existingImage := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing_image_%t", existingImage), func(t *testing.T) {
			runner, turn, original := workspaceImageProjectionFixture(t)
			st := runner.store.(*workspaceImageProjectionStore)
			// PNG readers accept bytes after IEND; padding makes each valid image
			// reach the byte bound without making a large decoded bitmap.
			st.pixels = append(st.pixels, make([]byte, llm.MaxImageBytes-len(st.pixels))...)
			metadata, err := imageattachment.Validate(st.pixels, st.metadata.MIMEType, st.metadata.Name)
			if err != nil {
				t.Fatal(err)
			}
			metadata.ID, metadata.WorkspaceID = st.metadata.ID, st.metadata.WorkspaceID
			st.metadata = metadata
			var envelope supervisorToolResultEnvelope
			var output workspaceImageResult
			_ = json.Unmarshal([]byte(original.ResultJSON), &envelope)
			_ = json.Unmarshal([]byte(envelope.Stdout), &output)
			output.Image = metadata
			encoded, _ := json.Marshal(output)
			envelope.Stdout = string(encoded)
			encoded, _ = json.Marshal(envelope)
			original.ResultJSON = string(encoded)
			rounds := []domain.SupervisorToolRound{{}, {}}
			for i := range rounds {
				rounds[i] = domain.SupervisorToolRound{RunID: original.RunID, Turn: original.Turn,
					AttemptID: original.AttemptID, Round: i + 1, ModelAttempt: original.ModelAttempt,
					CreatedAt: original.CreatedAt, CompletedAt: original.CompletedAt}
			}
			for i := range 5 {
				call := original
				call.CallID = fmt.Sprintf("image-call-%d", i)
				round := i / 3
				call.Round, call.Position = round+1, i%3+1
				st.saveKeys[workspaceImageSaveKey(call.RunID, call.CallID, supervisorWorkspaceImageOperationKey(call), json.RawMessage(call.PayloadJSON))] = true
				rounds[round].Calls = append(rounds[round].Calls, call)
			}
			request := llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "inspect images"}}}
			wantToolImages := 4
			if existingImage {
				request.Messages[0].Images = []llm.ImagePart{{MediaType: metadata.MIMEType, Data: st.pixels, SHA256: metadata.SHA256, Width: metadata.Width, Height: metadata.Height}}
				wantToolImages--
			}
			request, err = supervisorRequestWithToolRounds(request, rounds)
			if err != nil {
				t.Fatal(err)
			}
			projected, err := runner.supervisorWorkspaceImages(t.Context(), turn, request, rounds, true)
			if err != nil {
				t.Fatal(err)
			}
			totalBytes, toolImages, suppliedNotes, omittedNotes := 0, 0, 0, 0
			for i, message := range projected.Messages {
				if err := llm.ValidateMessageImages(message); err != nil {
					t.Fatal(err)
				}
				for _, part := range message.Images {
					totalBytes += len(part.Data)
				}
				if len(message.ToolResults) == 0 {
					continue
				}
				toolImages += len(message.Images)
				suppliedNotes += strings.Count(message.Content, `"pixels_supplied":true`)
				omittedNotes += strings.Count(message.Content, `"pixels_supplied":false`)
				if len(message.ToolResults) != len(request.Messages[i].ToolResults) || len(request.Messages[i].Images) != 0 || request.Messages[i].Content != "" {
					t.Fatal("tool results or original request mutated")
				}
			}
			if totalBytes != llm.MaxRequestImageBytes || toolImages != wantToolImages || suppliedNotes != wantToolImages || omittedNotes != 5-wantToolImages {
				t.Fatalf("visual coverage: bytes=%d images=%d supplied=%d omitted=%d", totalBytes, toolImages, suppliedNotes, omittedNotes)
			}
			if !strings.Contains(projected.Messages[2].Content, `"source_ref":"image-call-0"`) || !strings.Contains(projected.Messages[2].Content, "omitted") || len(projected.Messages[4].Images) != 2 {
				t.Fatal("newest pixels and identified omission missing")
			}
		})
	}
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
