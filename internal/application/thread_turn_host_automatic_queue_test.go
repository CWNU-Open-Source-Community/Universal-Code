package application_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

type automaticQueueOptions struct {
	attachment      bool
	cancelAtCommit  bool
	blockSettlement bool
	failOrdinary    bool
}

type automaticQueueFixture struct {
	state             *store.SQLiteStore
	turns             *application.ThreadTurnService
	provider          *boundaryJourneyProvider
	queued            []application.ExecuteThreadTurnRequest
	accepted          []application.ExecuteThreadTurnResult
	automaticDone     <-chan error
	releaseAutomatic  func()
	cancelAutomatic   context.CancelFunc
	cancelOrdinary    context.CancelFunc
	ordinaryStarted   <-chan struct{}
	releaseOrdinary   func()
	settlementStarted <-chan struct{}
	releaseSettlement func()
	executionID       string
	attachmentID      string
}

func newAutomaticQueueFixture(t *testing.T, options automaticQueueOptions) *automaticQueueFixture {
	t.Helper()
	started, ordinaryStarted := make(chan struct{}), make(chan struct{})
	automaticRelease, ordinaryRelease := make(chan struct{}), make(chan struct{})
	f := &automaticQueueFixture{ordinaryStarted: ordinaryStarted,
		releaseAutomatic: sync.OnceFunc(func() { close(automaticRelease) }),
		releaseOrdinary:  sync.OnceFunc(func() { close(ordinaryRelease) }), releaseSettlement: func() {}}
	f.provider = &boundaryJourneyProvider{}
	f.provider.respond = func(ctx context.Context, _ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		switch index {
		case 1:
			close(started)
			select {
			case <-automaticRelease:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		case 2:
			close(ordinaryStarted)
			if options.failOrdinary {
				return nil, apperror.New(apperror.CodeFailedPrecondition, "injected ordinary provider failure")
			}
			select {
			case <-ordinaryRelease:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return textResponse(rootActionResponse(domain.RootActionFinish, "Accepted user input completed", "done", "")), nil
	}
	state, _, automatic, _, _ := threadFilesFixture(t, f.provider)
	automatic.Files, automatic.RequestedBy = nil, "pr_followup"
	f.state = state
	caller, cancel := context.WithCancel(t.Context())
	f.cancelAutomatic = cancel
	ordinaryCaller, cancelOrdinary := context.WithCancel(t.Context())
	f.cancelOrdinary = cancelOrdinary
	first := automatic
	first.Content, first.OperationKey, first.RequestedBy = "FIRST explicit queued input", "host-automatic-queued-ordinary-0002", "test_operator"
	second := first
	second.Content, second.OperationKey = "SECOND explicit queued input", "host-automatic-queued-ordinary-0003"
	if options.attachment {
		file, err := state.SaveWorkspaceFileAttachment(t.Context(), "ws-thread-files", "automatic-queue-upload", "text/plain", "note.txt", []byte("FIRST_INPUT_ATTACHMENT_OBSERVATION"))
		if err != nil {
			t.Fatal(err)
		}
		f.attachmentID = file.ID
		first.Attachments = []domain.FileAttachmentReference{{ID: file.ID, WorkspaceID: file.WorkspaceID, SHA256: file.SHA256, ByteSize: file.ByteSize}}
	}
	f.queued = []application.ExecuteThreadTurnRequest{first, second}
	var threadStore application.ThreadStore = state
	if options.cancelAtCommit {
		// SQLite really commits; only the automatic caller's lifetime is injected.
		threadStore = &queuedCommitCancelStore{state, cancel, first.OperationKey}
	}
	if options.blockSettlement {
		fault := &stalledThreadTurnSettlement{SQLiteStore: state, started: make(chan struct{}), release: make(chan struct{})}
		threadStore = fault
		f.settlementStarted = fault.started
		f.releaseSettlement = sync.OnceFunc(func() { close(fault.release) })
	}
	router := llm.NewRouter(llm.ModelRef{Provider: f.provider.Name(), Model: "model"})
	router.RegisterProvider(f.provider)
	f.turns = application.NewThreadTurnService(threadStore, application.NewRunLifecycleControlService(state),
		application.NewRunExecutionHandoffService(state, router, policy.NewDefaultChecker())).WithHostLifetime(t.Context())
	t.Cleanup(func() {
		cancel()
		cancelOrdinary()
		f.releaseOrdinary()
		f.releaseSettlement()
		ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := f.turns.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	automaticDone := make(chan error, 1)
	f.automaticDone = automaticDone
	go func() { _, err := f.turns.ExecuteAutomatic(caller, automatic); automaticDone <- err }()
	awaitTurnSignal(t, started)
	live, err := f.turns.ExecutionState(t.Context(), automatic.ThreadID)
	if err != nil || live.ExecutionID == "" {
		t.Fatalf("automatic owner missing: %+v %v", live, err)
	}
	f.executionID = live.ExecutionID
	for _, request := range f.queued {
		accepted, err := f.turns.Execute(ordinaryCaller, request)
		if err != nil || accepted.ExecutionStarted || accepted.Submission.Message.Status != domain.OperatorSteeringPending {
			t.Fatalf("ordinary message was not durably accepted: %+v %v", accepted, err)
		}
		f.accepted = append(f.accepted, accepted)
	}
	// Both ordinary POST callers may disappear immediately after acceptance.
	cancelOrdinary()
	return f
}

func awaitAutomaticQueueIdle(t *testing.T, f *automaticQueueFixture) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		live, err := f.turns.ExecutionState(t.Context(), f.queued[0].ThreadID)
		if err != nil {
			t.Fatal(err)
		}
		if live.State == "idle" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued owner did not drain: %+v", live)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func awaitAutomaticQueueCancellation(t *testing.T, f *automaticQueueFixture) {
	t.Helper()
	err := awaitTurnResult(t, f.automaticDone)
	if apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeCancelled {
		t.Fatalf("automatic method did not return its original cancellation: %v", err)
	}
}

func TestHostOwnedQueuedOrdinaryTurnSurvivesAutomaticCallerCancel(t *testing.T) {
	for _, attachment := range []bool{false, true} {
		for _, atCommit := range []bool{false, true} {
			name := "text"
			if attachment {
				name = "attachment"
			}
			if atCommit {
				name += "/cancel_at_commit"
			}
			t.Run(name, func(t *testing.T) {
				f := newAutomaticQueueFixture(t, automaticQueueOptions{attachment: attachment, cancelAtCommit: atCommit})
				f.cancelAutomatic()
				// The automatic method returns its cancellation even while the
				// accepted ordinary turn is held inside a separate host drain.
				awaitAutomaticQueueCancellation(t, f)
				awaitTurnSignal(t, f.ordinaryStarted)
				live, err := f.turns.ExecutionState(t.Context(), f.queued[0].ThreadID)
				if err != nil || live.ExecutionID != f.executionID || live.State != "running" {
					t.Fatalf("handoff replaced or stopped the existing owner: %+v %v", live, err)
				}
				f.releaseOrdinary()
				awaitAutomaticQueueIdle(t, f)
				requests := f.provider.Requests()
				if len(requests) != 3 {
					t.Fatalf("accepted inputs did not each execute exactly once: %d model calls", len(requests))
				}
				for i, request := range f.queued {
					observation, err := f.state.InspectThreadTurnRequest(t.Context(), request.ThreadID, request.OperationKey, request.RequestedBy)
					if err != nil || observation.State != "completed" || !observation.Settled || observation.MessageID != f.accepted[i].Submission.Message.ID || observation.RunID != f.accepted[i].Submission.Run.ID {
						t.Fatalf("accepted input did not complete its original receipt: %+v %v", observation, err)
					}
					lastUser := ""
					for _, message := range requests[i+1].Messages {
						if message.Role == "user" && (strings.Contains(message.Content, f.queued[0].Content) || strings.Contains(message.Content, f.queued[1].Content)) {
							lastUser = message.Content
						}
					}
					if !strings.Contains(lastUser, request.Content) {
						t.Fatalf("queued inputs changed FIFO: model %d last user=%q", i+2, lastUser)
					}
					files, err := f.state.ListOperatorMessageAttachments(t.Context(), f.accepted[i].Submission.Run.ID, observation.MessageID)
					if err != nil || len(files) != map[bool]int{false: 0, true: 1}[attachment && i == 0] || len(files) == 1 && files[0].ID != f.attachmentID {
						t.Fatalf("first input's attachment association changed: %+v %v", files, err)
					}
					if _, err := f.turns.Execute(t.Context(), request); err != nil || len(f.provider.Requests()) != 3 {
						t.Fatalf("same-key replay repeated an accepted input: %v", err)
					}
				}
				if attachment {
					var wire strings.Builder
					for _, message := range requests[1].Messages {
						wire.WriteString(message.Content)
					}
					if !strings.Contains(wire.String(), "FIRST_INPUT_ATTACHMENT_OBSERVATION") {
						t.Fatal("first ordinary model call lost the actual uploaded evidence")
					}
				}
			})
		}
	}
}

func TestHostOwnedAutomaticSuccessHandsQueuedOrdinaryToHost(t *testing.T) {
	f := newAutomaticQueueFixture(t, automaticQueueOptions{})
	f.releaseAutomatic()
	if err := awaitTurnResult(t, f.automaticDone); err != nil {
		t.Fatalf("successful automatic boundary changed: %v", err)
	}
	awaitTurnSignal(t, f.ordinaryStarted)
	// Losing the already-finished automatic caller cannot cancel the next input.
	f.cancelAutomatic()
	f.releaseOrdinary()
	awaitAutomaticQueueIdle(t, f)
	for _, request := range f.queued {
		observation, err := f.state.InspectThreadTurnRequest(t.Context(), request.ThreadID, request.OperationKey, request.RequestedBy)
		if err != nil || observation.State != "completed" || !observation.Settled {
			t.Fatalf("next ordinary turn inherited the completed automatic caller: %+v %v", observation, err)
		}
	}
	if len(f.provider.Requests()) != 3 {
		t.Fatal("successful automatic boundary duplicated or dropped queued input")
	}
}

func TestHostOwnedAutomaticQueueStopAndShutdownFenceHandoff(t *testing.T) {
	for _, before := range []bool{true, false} {
		for _, shutdown := range []bool{false, true} {
			name := map[bool]string{false: "after_handoff", true: "before_handoff"}[before] + "/" + map[bool]string{false: "stop", true: "shutdown"}[shutdown]
			t.Run(name, func(t *testing.T) {
				f := newAutomaticQueueFixture(t, automaticQueueOptions{blockSettlement: before})
				f.cancelAutomatic()
				if before {
					awaitTurnSignal(t, f.settlementStarted)
				} else {
					awaitAutomaticQueueCancellation(t, f)
					awaitTurnSignal(t, f.ordinaryStarted)
				}
				if shutdown {
					grace := 5 * time.Second
					if before {
						grace = 100 * time.Millisecond
					}
					ctx, cancel := context.WithTimeout(t.Context(), grace)
					err := f.turns.Shutdown(ctx)
					cancel()
					if before && !errors.Is(err, context.DeadlineExceeded) || !before && err != nil {
						t.Fatalf("shutdown lost its bounded drain: %v", err)
					}
				} else {
					stopping, err := f.turns.Interrupt(t.Context(), f.queued[0].ThreadID, f.executionID)
					if err != nil || stopping.ExecutionID != f.executionID || stopping.State != "stopping" {
						t.Fatalf("same execution Stop failed across handoff: %+v %v", stopping, err)
					}
				}
				f.releaseSettlement()
				if before {
					awaitAutomaticQueueCancellation(t, f)
				}
				awaitAutomaticQueueIdle(t, f)
				wantCalls := 2
				if before {
					wantCalls = 1
				}
				if len(f.provider.Requests()) != wantCalls {
					t.Fatalf("stopped host resumed queued work: %d model calls", len(f.provider.Requests()))
				}
				for i, request := range f.queued {
					observation, err := f.state.InspectThreadTurnRequest(t.Context(), request.ThreadID, request.OperationKey, request.RequestedBy)
					want := "received"
					if !before && i == 0 {
						want = "failed"
					}
					if err != nil || observation.State != want || observation.Settled != (want == "failed") {
						t.Fatalf("stopped input lost its pending/failed receipt: %+v %v", observation, err)
					}
				}
			})
		}
	}
}

func TestHostOwnedAutomaticQueuePreservesOrdinaryFailure(t *testing.T) {
	f := newAutomaticQueueFixture(t, automaticQueueOptions{failOrdinary: true})
	f.cancelAutomatic()
	awaitAutomaticQueueCancellation(t, f)
	awaitAutomaticQueueIdle(t, f)
	for i, request := range f.queued {
		observation, err := f.state.InspectThreadTurnRequest(t.Context(), request.ThreadID, request.OperationKey, request.RequestedBy)
		want := "received"
		if i == 0 {
			want = "failed"
		}
		if err != nil || observation.State != want || observation.Settled != (want == "failed") {
			t.Fatalf("background drain lost its failure or unconsumed input: %+v %v", observation, err)
		}
		if i == 0 && (observation.Failure == nil || observation.Failure.ErrorCode != string(apperror.CodeFailedPrecondition)) {
			t.Fatalf("ordinary provider failure was swallowed: %+v", observation)
		}
	}
	if len(f.provider.Requests()) != 2 {
		t.Fatal("ordinary model failure repeated or consumed the next queued input")
	}
}
