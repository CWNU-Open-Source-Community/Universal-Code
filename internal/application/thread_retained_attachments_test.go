package application_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/modelregistry"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

func TestThreadSuccessorRetainsQueuedAttachments(t *testing.T) {
	for _, tc := range []struct {
		name, content        string
		files, image, budget bool
	}{
		{name: "text_and_files_model_switch", content: "Use the retained attached note", files: true},
		{name: "files_only_model_switch", files: true},
		{name: "image_only_model_switch", image: true},
		{name: "mixed_budget_transition", files: true, image: true, budget: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			dbPath := filepath.Join(t.TempDir(), "retained-attachments.db")
			st, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			const workspaceID = "workspace-retained-attachments"
			if err := st.SaveWorkspace(ctx, store.WorkspaceRecord{ID: workspaceID, Name: "retained attachments", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			provider := &imageLifecycleProvider{lifecycleProvider{responses: []string{
				rootActionResponse(domain.RootActionContinue, "Continue with the accepted inputs", "", ""),
				rootActionResponse(domain.RootActionContinue, "Continue with the accepted inputs", "", ""),
			}}}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			maxTurns := 4
			if tc.budget {
				maxTurns = 1
			}
			runs := application.NewRunService(st)
			_, created, err := runs.Create(ctx, application.CreateRunRequest{
				Goal: "Read the queued attachments", WorkspaceID: workspaceID, Profile: "review",
				ModelRoute: provider.Name() + "/model", Interactive: true, Budget: domain.Budget{MaxTurns: maxTurns},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runs.Start(ctx, created.ID); err != nil {
				t.Fatal(err)
			}
			if tc.budget {
				if _, err := application.NewAgentRunner(st, router, policy.NewDefaultChecker()).StepWithInput(ctx, created.ID, "Use the initial execution turn"); err != nil {
					t.Fatal(err)
				}
			}
			input := domain.EnqueueOperatorSteeringRequest{RunID: created.ID, SessionID: created.SessionID,
				Content: tc.content, OperationKey: "queued-retained-attachments", RequestedBy: "operator"}
			var files []domain.WorkspaceFileAttachment
			if tc.files {
				for _, fileInput := range []struct{ name, mime, body string }{
					{"note.txt", "text/plain", "RETAINED_ATTACHMENT_BODY"},
					{"original.zip", "application/zip", "PK\x03\x04retained-original"},
				} {
					file, err := st.SaveWorkspaceFileAttachment(ctx, workspaceID, "upload-retained-"+fileInput.name, fileInput.mime, fileInput.name, []byte(fileInput.body))
					if err != nil {
						t.Fatal(err)
					}
					files = append(files, file)
					input.Attachments = append(input.Attachments, domain.FileAttachmentReference{ID: file.ID, WorkspaceID: file.WorkspaceID, SHA256: file.SHA256, ByteSize: file.ByteSize})
				}
			}
			var image domain.WorkspaceImage
			var pixels []byte
			if tc.image {
				pixels = threadImagePNG(t, 64)
				image, err = st.SaveWorkspaceImage(ctx, workspaceID, "upload-retained-image", "image/png", "queued.png", pixels)
				if err != nil {
					t.Fatal(err)
				}
				input.Images = []domain.ImageReference{{ID: image.ID, SHA256: image.SHA256}}
			}
			queued, err := st.EnqueueOperatorSteering(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			// Explicitly withdrawn input must not become eligible merely because
			// this Run is later retired for a model or budget change.
			withdrawnFile, err := st.SaveWorkspaceFileAttachment(ctx, workspaceID, "upload-withdrawn-file", "text/plain", "withdrawn.txt", []byte("WITHDRAWN_ATTACHMENT_BODY"))
			if err != nil {
				t.Fatal(err)
			}
			withdrawn, err := st.EnqueueOperatorSteering(ctx, domain.EnqueueOperatorSteeringRequest{
				RunID: created.ID, SessionID: created.SessionID, OperationKey: "queued-withdrawn-file", RequestedBy: "operator",
				Attachments: []domain.FileAttachmentReference{{ID: withdrawnFile.ID, WorkspaceID: workspaceID, SHA256: withdrawnFile.SHA256, ByteSize: withdrawnFile.ByteSize}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.CancelOperatorSteering(ctx, domain.CancelOperatorSteeringRequest{MessageID: withdrawn.Message.ID, OperationKey: "withdraw-queued-file", RequestedBy: "operator", Reason: "Withdraw this input"}); err != nil {
				t.Fatal(err)
			}
			threadID := domain.InitialThreadID(created.ID)
			registry := &mutableThreadModelRouteRegistry{router: router, snapshot: modelregistry.Snapshot{
				ProtocolVersion: modelregistry.ProtocolVersion, Generation: 1,
				Providers: []modelregistry.ProviderAvailability{{Name: provider.Name(), Enabled: true,
					Kind: modelregistry.ProviderKindOpenAICompatible, Status: modelregistry.ProviderAvailable,
					Models: []string{"model", "model-next"}, CredentialSource: "test",
					Harnesses: []modelregistry.HarnessAvailability{
						{Model: "model", RootEligible: true, LatestQualificationStatus: modelregistry.QualificationStatusAvailable},
						{Model: "model-next", RootEligible: true, LatestQualificationStatus: modelregistry.QualificationStatusAvailable},
					}}},
			}}
			if !tc.budget {
				if _, err := application.NewThreadModelRouteService(st, registry).Change(ctx, application.ChangeThreadModelRouteRequest{
					Version: domain.ThreadModelRouteControlProtocolVersion, ThreadID: threadID, Action: domain.ThreadModelRouteSelect,
					Provider: provider.Name(), Model: "model-next", OperationKey: "select-successor-model", RequestedBy: "operator",
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st),
				application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker())).WithModelRouteRegistry(registry)
			request := application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: threadID,
				Content: "Continue with the accepted input", OperationKey: "execute-retained-input", RequestedBy: "operator"}
			result, err := turns.Execute(ctx, request)
			if err != nil || !result.Submission.SuccessorCreated || result.Submission.PredecessorRunID != created.ID {
				t.Fatalf("successor did not execute: %+v err=%v", result, err)
			}
			actual := provider.requests[len(provider.requests)-1]
			var wire strings.Builder
			for _, message := range actual.Messages {
				wire.WriteString(message.Content)
			}
			for _, required := range []string{tc.content, queued.Message.ID, "accepted but never prepared or executed"} {
				if !strings.Contains(wire.String(), required) {
					t.Fatalf("retained input lost %q", required)
				}
			}
			if strings.Contains(wire.String(), withdrawnFile.ID) || strings.Contains(wire.String(), "WITHDRAWN_ATTACHMENT_BODY") {
				t.Fatal("explicitly withdrawn input was carried into successor")
			}
			if tc.files && !strings.Contains(wire.String(), "RETAINED_ATTACHMENT_BODY") {
				t.Fatal("retained file text did not reach the model")
			}
			for _, file := range files {
				if !strings.Contains(wire.String(), domain.NewFileAttachmentInput(file).RelativePath) {
					t.Fatalf("original-file inventory lost %s", file.ID)
				}
			}
			if tc.image {
				included := 0
				for _, message := range actual.Messages {
					for _, part := range message.Images {
						if part.SHA256 == image.SHA256 && bytes.Equal(part.Data, pixels) && message.Role == "user" && message.Content == tc.content {
							included++
						}
					}
				}
				if included != 1 || !strings.Contains(wire.String(), image.ID) {
					t.Fatalf("retained image pixels or source descriptor missing: images=%d", included)
				}
			}
			history, err := st.ListSessionMessages(ctx, result.Submission.Run.SessionID, true)
			if err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				found := false
				for _, message := range history {
					if message.Provenance.SourceKind == session.SourceUploadedFile && message.Provenance.SourceRef == file.ID && !message.Provenance.InstructionAuthorized && strings.Contains(message.Content, queued.Message.ID) {
						found = true
					}
				}
				if !found {
					t.Fatalf("retained file evidence has no durable source binding: %s", file.ID)
				}
			}
			old, err := st.GetOperatorSteering(ctx, queued.Message.ID)
			if err != nil || old.Status != domain.OperatorSteeringCancelled || old.SessionMessageID != 0 || old.ImageCount != len(input.Images) || old.AttachmentCount != len(input.Attachments) {
				t.Fatalf("retention changed the old queue's execution facts: %+v err=%v", old, err)
			}
			calls := provider.calls
			replay, err := turns.Execute(ctx, request)
			if err != nil || !replay.Replayed || provider.calls != calls {
				t.Fatalf("successor retry repeated execution: %+v err=%v", replay, err)
			}
			after, err := st.ListSessionMessages(ctx, result.Submission.Run.SessionID, true)
			if err != nil || len(after) != len(history) {
				t.Fatalf("successor retry duplicated retained history: %d -> %d err=%v", len(history), len(after), err)
			}
		})
	}
}
