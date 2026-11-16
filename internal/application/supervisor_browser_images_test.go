package application

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/imageattachment"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
)

type screenshotEvidenceStore struct {
	*fakeFullCDPBrowserActionStore
	rounds []domain.SupervisorToolRound
}

func (s *screenshotEvidenceStore) ListSupervisorToolRounds(context.Context, domain.SupervisorCheckpoint) ([]domain.SupervisorToolRound, error) {
	return s.rounds, nil
}

type screenshotVisionProvider struct {
	llm.MockProvider
	state llm.VisionSupport
}

func (s screenshotVisionProvider) Name() string { return "screenshot-vision" }
func (s screenshotVisionProvider) DescribeVision(string) llm.VisionCapability {
	return llm.VisionCapability{State: s.state, Source: "operator_declared"}
}

// Exercise the actual screenshot executor, PNG artifact persistence and native
// call/result projection. Only CDP and the durable read store are test doubles.
func newScreenshotEvidenceFixture(t *testing.T, legacy bool) (*FullCDPProductionService, *screenshotEvidenceStore, domain.SupervisorCheckpoint, domain.SupervisorToolCall) {
	t.Helper()
	service, base, _, _ := newFullCDPPreviewFixture(t)
	st := &screenshotEvidenceStore{fakeFullCDPBrowserActionStore: &fakeFullCDPBrowserActionStore{fakeFullCDPProductionStore: base, workspace: session.WorkspaceInfo{ID: base.mission.WorkspaceID, RootPath: t.TempDir(), Name: "browser screenshots"}}}
	service.store = st
	binding, ok, err := service.browserActionBinding(t.Context(), base.run.ID)
	if err != nil || !ok {
		t.Fatalf("binding %t %v", ok, err)
	}
	call := domain.SupervisorToolCall{RunID: base.run.ID, Turn: 1, AttemptID: "attempt-screen", AgentID: "agent-browser-root", AgentAttemptID: "attempt-screen", AgentAttribution: domain.AgentAttributionSupervisorRoot, Round: 1, Position: 1, ModelAttempt: 1, CallID: "call-screen-one", ToolName: string(toolgateway.BrowserScreenshotTool), PayloadJSON: `{"version":"browser_screenshot.v1"}`, CreatedAt: time.Now().UTC()}
	key := supervisorBrowserScreenshotOperationKey(call)
	if legacy {
		key = supervisorToolOperationKey(call.RunID, call.Turn, toolgateway.BrowserScreenshotTool, json.RawMessage(call.PayloadJSON))
	}
	scope := browserActionTestScope(binding, key)
	authority, err := toolgateway.NewBrowserActionCallAuthority(toolgateway.BrowserActionCapabilityContext{RunID: scope.RunID, MissionID: scope.MissionID, SessionID: scope.SessionID, RootAgentID: scope.RootAgentID, WorkspaceID: scope.WorkspaceID, Surface: scope.Surface, Phase: scope.Phase, Role: scope.Role, Profile: scope.Profile, PermissionMode: scope.PermissionMode, ModeRevision: scope.ModeRevision, PermissionSnapshotID: scope.PermissionSnapshotID, PermissionRevision: scope.PermissionRevision, PermissionActivation: scope.PermissionActivation, RunAuthorizationFence: scope.RunAuthorizationFence, FullCDPSessionID: scope.FullCDPSessionID, BrowserPermissionSnapshotID: scope.BrowserPermissionSnapshotID, BrowserPermissionRevision: scope.BrowserPermissionRevision, TargetOrigin: binding.view.TargetOrigin, Ready: true, RuntimeAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := toolgateway.EncodeBrowserActionCallAuthority(authority)
	if err != nil {
		t.Fatal(err)
	}
	call.AuthorityJSON = string(encoded)
	result, err := service.ExecuteBrowserAction(t.Context(), scope, toolgateway.BrowserScreenshotTool, json.RawMessage(call.PayloadJSON))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := marshalSupervisorToolResultEnvelope(supervisorToolResultEnvelope{Version: supervisorToolResultVersion, Tool: call.ToolName, Status: "completed", Stdout: result.Content, Metadata: result.Metadata})
	if err != nil {
		t.Fatal(err)
	}
	completed := time.Now().UTC()
	call.ResultJSON = string(envelope)
	call.Status = domain.SupervisorToolCompleted
	call.CompletedAt = &completed
	if err := call.Validate(); err != nil {
		t.Fatal(err)
	}
	st.rounds = []domain.SupervisorToolRound{{RunID: call.RunID, Turn: call.Turn, AttemptID: call.AttemptID, Round: 1, ModelAttempt: 1, CreatedAt: call.CreatedAt, CompletedAt: call.CompletedAt, Calls: []domain.SupervisorToolCall{call}}}
	checkpoint := domain.SupervisorCheckpoint{RunID: call.RunID, NextTurn: 1, AttemptID: call.AttemptID, Phase: domain.SupervisorTurnStarted, LeaseID: "lease-screen", LeaseGeneration: 1, UpdatedAt: completed}
	if err := checkpoint.Validate(); err != nil {
		t.Fatal(err)
	}
	return service, st, checkpoint, call
}

func TestSupervisorBrowserImagesKeepExactNativePairAndVisionBoundary(t *testing.T) {
	service, st, checkpoint, call := newScreenshotEvidenceFixture(t, false)
	request, err := supervisorRequestWithToolRounds(llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "inspect this app"}}}, st.rounds)
	if err != nil {
		t.Fatal(err)
	}
	ref := llm.ModelRef{Provider: "screenshot-vision", Model: "vision-test"}
	for _, support := range []llm.VisionSupport{llm.VisionSupported, llm.VisionUnknown, llm.VisionUnsupported} {
		t.Run(string(support), func(t *testing.T) {
			router := llm.NewRouter(ref)
			router.RegisterProvider(screenshotVisionProvider{state: support})
			supervisor := &AgentRunner{router: router, browserActions: service}
			actual, err := supervisor.supervisorBrowserImages(t.Context(), checkpoint, ref, request, st.rounds)
			if err != nil {
				t.Fatal(err)
			}
			message := actual.Messages[2]
			if message.Role != "user" || len(message.ToolResults) != 1 || message.ToolResults[0].ToolCallID != call.CallID || message.ToolResults[0].Content != call.ResultJSON || len(actual.Messages[1].ToolCalls) != 1 || actual.Messages[1].ToolCalls[0].ID != call.CallID {
				t.Fatal("native call/result pair changed")
			}
			if !strings.Contains(message.Content, `"instruction_authorized":false`) || !strings.Contains(message.Content, call.CallID) {
				t.Fatalf("source missing %s", message.Content)
			}
			wantPixelNote := `"pixels_supplied":false`
			if support == llm.VisionSupported {
				wantPixelNote = `"pixels_supplied":true`
			}
			if !strings.Contains(message.Content, wantPixelNote) {
				t.Fatalf("incorrect pixel coverage note: %s", message.Content)
			}
			if support == llm.VisionSupported {
				if len(message.Images) != 1 || llm.ValidateMessageImages(message) != nil || message.Images[0].Width != 2 || message.Images[0].Height != 3 || llm.EstimateImageTokens(message.Images[0]) <= 0 {
					t.Fatal("actual image/budget missing")
				}
			} else if len(message.Images) != 0 || !strings.Contains(message.Content, "Do not claim visual inspection") {
				t.Fatal("unestablished vision was silently treated as supported")
			}
			if len(request.Messages[2].Images) != 0 || request.Messages[2].Content != "" {
				t.Fatal("base request mutated across retries")
			}
		})
	}
	other := call
	other.CallID = "call-screen-two"
	other.Round = 2
	if supervisorBrowserScreenshotOperationKey(call) == supervisorBrowserScreenshotOperationKey(other) {
		t.Fatal("distinct same-turn screenshot calls alias")
	}
}

func TestSupervisorBrowserImagesHonorsExistingRequestImageBudget(t *testing.T) {
	service, st, checkpoint, _ := newScreenshotEvidenceFixture(t, false)
	workspaceRunner, _, _ := workspaceImageProjectionFixture(t)
	imageStore := workspaceRunner.store.(*workspaceImageProjectionStore)
	data := append(imageStore.pixels, make([]byte, llm.MaxImageBytes-len(imageStore.pixels))...)
	metadata, err := imageattachment.Validate(data, "image/png", "")
	if err != nil {
		t.Fatal(err)
	}
	part := llm.ImagePart{MediaType: metadata.MIMEType, Data: data, SHA256: metadata.SHA256, Width: metadata.Width, Height: metadata.Height}
	request, err := supervisorRequestWithToolRounds(llm.ChatRequest{Messages: []llm.Message{{Role: "user",
		Images: []llm.ImagePart{part, part, part, part}}}}, st.rounds)
	if err != nil {
		t.Fatal(err)
	}
	ref := llm.ModelRef{Provider: "screenshot-vision", Model: "vision-test"}
	router := llm.NewRouter(ref)
	router.RegisterProvider(screenshotVisionProvider{state: llm.VisionSupported})
	runner := &AgentRunner{router: router, browserActions: service}
	projected, err := runner.supervisorBrowserImages(t.Context(), checkpoint, ref, request, st.rounds)
	if err != nil {
		t.Fatal(err)
	}
	result := projected.Messages[2]
	if len(result.Images) != 0 || !strings.Contains(result.Content, `"pixels_supplied":false`) ||
		!strings.Contains(result.Content, "request image bound") || result.ToolResults[0] != request.Messages[2].ToolResults[0] {
		t.Fatalf("over-budget screenshot was not explicitly omitted: %+v", result)
	}
}

func TestReadModelScreenshotRejectsWrongScopeTamperAndPreservesLegacy(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacy], func(t *testing.T) {
			service, st, checkpoint, call := newScreenshotEvidenceFixture(t, legacy)
			before := call.ResultJSON
			got, err := service.ReadModelScreenshot(t.Context(), checkpoint, call.CallID)
			if err != nil || got.RunID != call.RunID || got.SessionID != st.run.SessionID || len(got.Image.Data) == 0 {
				t.Fatalf("read %v", err)
			}
			for _, mutate := range []func(*domain.SupervisorCheckpoint){func(cp *domain.SupervisorCheckpoint) { cp.RunID = "wrong-run" }, func(cp *domain.SupervisorCheckpoint) { cp.AttemptID = "wrong-attempt" }, func(cp *domain.SupervisorCheckpoint) { cp.NextTurn++ }} {
				wrong := checkpoint
				mutate(&wrong)
				if _, err := service.ReadModelScreenshot(t.Context(), wrong, call.CallID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
					t.Fatalf("wrong scope accepted: %v", err)
				}
			}
			if _, err := service.ReadModelScreenshot(t.Context(), checkpoint, "other-call"); err == nil {
				t.Fatal("wrong call accepted")
			}
			key := supervisorBrowserScreenshotOperationKey(call)
			if legacy {
				key = supervisorToolOperationKey(call.RunID, call.Turn, toolgateway.BrowserScreenshotTool, json.RawMessage(call.PayloadJSON))
			}
			if err := os.WriteFile(filepath.Join(st.workspace.RootPath, fullCDPScreenshotRelativePath(call.RunID, key)), []byte("tampered"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := service.ReadModelScreenshot(t.Context(), checkpoint, call.CallID); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
				t.Fatalf("tampered image accepted %v", err)
			}
			if st.rounds[0].Calls[0].ResultJSON != before {
				t.Fatal("sealed receipt changed")
			}
		})
	}
}
