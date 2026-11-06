package application_test

import (
	"context"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

type queuedCommitCancelStore struct {
	*store.SQLiteStore
	cancel context.CancelFunc
	key    string
}

func (s *queuedCommitCancelStore) CommitThreadMessage(ctx context.Context, request domain.ThreadMessageIntentRequest, runID string, prepared []session.PreparedEvidenceAttachment) (domain.OperatorSteeringEnqueueResult, error) {
	result, err := s.SQLiteStore.CommitThreadMessage(ctx, request, runID, prepared)
	if err == nil && request.OperationKey == s.key {
		s.cancel()
	}
	return result, err
}

func TestIndependentHostQueuedDisconnectAtDurableCommit(t *testing.T) {
	for _, attachment := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "attachment"}[attachment], func(t *testing.T) {
			provider := &gatedThreadProvider{started: make(chan struct{}), release: make(chan struct{}), lifecycleProvider: lifecycleProvider{responses: []string{rootActionResponse(domain.RootActionFinish, "first done", "done", ""), rootActionResponse(domain.RootActionFinish, "second done", "done", "")}}}
			state, _, input, _, _ := threadFilesFixture(t, provider)
			input.Files = nil
			caller, cancel := context.WithCancel(t.Context())
			defer cancel()
			queued := input
			queued.Content = "accepted second input"
			queued.OperationKey = "independent-queued-after-commit"
			if attachment {
				thread, err := state.GetThread(t.Context(), input.ThreadID)
				if err != nil {
					t.Fatal(err)
				}
				run, err := state.GetRun(t.Context(), thread.ActiveRunID)
				if err != nil {
					t.Fatal(err)
				}
				linked, err := state.GetSession(t.Context(), run.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				file, err := state.SaveWorkspaceFileAttachment(t.Context(), linked.WorkspaceID, "queued-disconnect-upload", "text/plain", "note.txt", []byte("Queued attachment preserved across caller disconnect"))
				if err != nil {
					t.Fatal(err)
				}
				queued.Attachments = []domain.FileAttachmentReference{{ID: file.ID, WorkspaceID: file.WorkspaceID, SHA256: file.SHA256, ByteSize: file.ByteSize}}
			}
			fault := &queuedCommitCancelStore{state, cancel, queued.OperationKey}
			router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
			router.RegisterProvider(provider)
			turns := application.NewThreadTurnService(fault, application.NewRunLifecycleControlService(state), application.NewRunExecutionHandoffService(state, router, policy.NewDefaultChecker())).WithHostLifetime(t.Context())
			done := make(chan error, 1)
			go func() { _, err := turns.Execute(t.Context(), input); done <- err }()
			awaitTurnSignal(t, provider.started)
			_, submitErr := turns.Execute(caller, queued)
			close(provider.release)
			if err := awaitTurnResult(t, done); err != nil {
				t.Fatal(err)
			}
			observation, err := state.InspectThreadTurnRequest(t.Context(), queued.ThreadID, queued.OperationKey, queued.RequestedBy)
			if submitErr != nil || err != nil || observation.State != "completed" || provider.calls != 2 {
				t.Fatalf("durably queued input was lost after commit disconnect: state=%s status=%s calls=%d submit=%v read=%v", observation.State, observation.MessageStatus, provider.calls, submitErr, err)
			}
		})
	}
}
