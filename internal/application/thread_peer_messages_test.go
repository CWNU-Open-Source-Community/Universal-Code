package application_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
)

type peerMessageProvider struct {
	requests chan llm.ChatRequest
	release  chan struct{}
	calls    atomic.Int32
}

func (p *peerMessageProvider) Name() string { return "peer-message-test" }
func (p *peerMessageProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "model", Provider: p.Name()}}, nil
}
func (p *peerMessageProvider) SupportsTools(string) bool    { return false }
func (p *peerMessageProvider) SupportsVision(string) bool   { return false }
func (p *peerMessageProvider) SupportsJSONMode(string) bool { return true }
func (p *peerMessageProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	response, err := p.Chat(ctx, request)
	if err != nil {
		return nil, err
	}
	chunks := make(chan llm.ChatChunk, 2)
	chunks <- llm.ChatChunk{Text: response.Text}
	chunks <- llm.FinalChatChunk(response)
	close(chunks)
	return chunks, nil
}
func (p *peerMessageProvider) Chat(ctx context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	p.calls.Add(1)
	select {
	case p.requests <- request:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &llm.ChatResponse{Text: rootActionResponse(domain.RootActionFinish, "Peer response recorded.", "done", ""), Provider: p.Name(), Model: "model", Usage: llm.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}}, nil
}

func peerServices(st *store.SQLiteStore, p llm.Provider) (*application.ThreadTurnService, *application.ThreadPeerMessageService) {
	router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
	router.RegisterProvider(p)
	turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st), application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()))
	return turns, application.NewThreadPeerMessageService(st, turns)
}
func createPeerRun(t *testing.T, st *store.SQLiteStore, p llm.Provider, title string) domain.Run {
	t.Helper()
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: title, Profile: "review", ModelRoute: p.Name() + "/model", Interactive: true, Budget: domain.Budget{MaxTurns: 8}})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func peerSourceRequest(t *testing.T, st *store.SQLiteStore, source domain.Run) domain.SendThreadPeerMessageRequest {
	t.Helper()
	if _, err := application.NewRunService(st).Start(t.Context(), source.ID); err != nil {
		t.Fatal(err)
	}
	acquired, err := st.AcquireRunExecutionLease(t.Context(), domain.AcquireRunExecutionLeaseRequest{RunID: source.ID, OwnerID: "peer-source-test", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _, _ = st.ReleaseRunExecutionLease(context.Background(), acquired.Lease) })
	if _, err := st.EnqueueOperatorSteering(t.Context(), domain.EnqueueOperatorSteeringRequest{RunID: source.ID, SessionID: source.SessionID, Content: "Coordinate within this chat's existing task", OperationKey: "peer-source-instruction-" + source.ID, RequestedBy: "test_operator"}); err != nil {
		t.Fatal(err)
	}
	turn, err := st.BeginSupervisorTurn(t.Context(), acquired.Lease, "")
	if err != nil {
		t.Fatal(err)
	}
	return domain.SendThreadPeerMessageRequest{SourceThreadID: domain.InitialThreadID(source.ID), RequestedBy: "run_supervisor", SourceRunID: source.ID, SourceAttemptID: turn.Checkpoint.AttemptID, SourceLeaseID: acquired.Lease.LeaseID, SourceLeaseGeneration: acquired.Lease.Generation}
}
func waitPeerCommitted(t *testing.T, st *store.SQLiteStore, id string) domain.ThreadPeerMessage {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		message, err := st.GetThreadPeerMessage(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		input, err := st.GetOperatorSteering(t.Context(), message.SteeringMessageID)
		if err != nil {
			t.Fatal(err)
		}
		if input.Status == domain.OperatorSteeringCommitted {
			return message
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("peer message was not consumed")
	return domain.ThreadPeerMessage{}
}
func startPeerWorker(t *testing.T, s *application.ThreadPeerMessageService) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("peer worker did not stop")
		}
	})
}

func TestThreadPeerMessagesRestartIdleQueueAndProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	p := &peerMessageProvider{requests: make(chan llm.ChatRequest, 8)}
	source := createPeerRun(t, st, p, "Source chat")
	target := createPeerRun(t, st, p, "Target chat")
	_, peer := peerServices(st, p)
	_, err = st.SaveSessionMessage(t.Context(), session.NewMessage(source.SessionID, "user", "source-only private context"))
	if err != nil {
		t.Fatal(err)
	}
	request := peerSourceRequest(t, st, source)
	request.TargetThreadID, request.Content, request.OperationKey = domain.InitialThreadID(target.ID), "Share the result within your existing permissions.", "peer-message-idle-first-key"
	first, err := peer.Send(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Message.Status != "queued" || first.Message.DeliveredAt != nil {
		t.Fatalf("unexpected receipt: %+v", first)
	}
	replay, err := peer.Send(t.Context(), request)
	if err != nil || !replay.Replayed || replay.Message.ID != first.Message.ID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	request.Content = "Different content"
	if _, err := peer.Send(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed intent accepted: %v", err)
	}
	request.OperationKey = "peer-message-idle-second-key"
	second, err := peer.Send(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, peer = peerServices(st, p)
	startPeerWorker(t, peer)
	for _, id := range []string{first.Message.ID, second.Message.ID} {
		message := waitPeerCommitted(t, st, id)
		if message.Status != "delivered" || message.DeliveredAt == nil {
			t.Fatalf("delivery status: %+v", message)
		}
	}
	for range 2 {
		select {
		case request := <-p.requests:
			var input string
			for _, message := range request.Messages {
				input += message.Content + "\n"
			}
			if !strings.Contains(input, `"source_kind":"peer_message"`) || !strings.Contains(input, `"instruction_authorized":false`) || strings.Contains(input, "source-only private context") {
				t.Fatalf("peer provenance/context isolation lost: %s", input)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("model did not receive peer input")
		}
	}
	messages, err := st.ListSessionMessages(t.Context(), target.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	peers := 0
	for _, message := range messages {
		if message.Provenance.SourceKind == session.SourcePeerMessage {
			peers++
			if message.Role != "tool" || message.Provenance.InstructionAuthorized {
				t.Fatalf("peer input promoted: %+v", message)
			}
		}
	}
	if peers != 2 || p.calls.Load() != 2 {
		t.Fatalf("peer count=%d model calls=%d", peers, p.calls.Load())
	}
}

func TestThreadPeerMessagesBusyTurnConsumesAtBoundary(t *testing.T) {
	p := &peerMessageProvider{requests: make(chan llm.ChatRequest, 8), release: make(chan struct{}, 8)}
	st, turns, targetRequest := threadControlFixture(t, p)
	source := createPeerRun(t, st, p, "Source chat")
	peer := application.NewThreadPeerMessageService(st, turns)
	done := make(chan error, 1)
	go func() { _, err := turns.Execute(t.Context(), targetRequest); done <- err }()
	select {
	case <-p.requests:
	case <-time.After(5 * time.Second):
		t.Fatal("initial turn did not start")
	}
	request := peerSourceRequest(t, st, source)
	request.TargetThreadID, request.Content, request.OperationKey = targetRequest.ThreadID, "Peer input at the next turn boundary", "peer-message-busy-operation"
	receipt, err := peer.Send(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	startPeerWorker(t, peer)
	time.Sleep(600 * time.Millisecond)
	if p.calls.Load() != 1 {
		t.Fatal("peer started a concurrent model turn")
	}
	p.release <- struct{}{}
	select {
	case request := <-p.requests:
		if !strings.Contains(request.Messages[len(request.Messages)-1].Content, `"source_kind":"peer_message"`) {
			t.Fatalf("peer input was not enveloped: %+v", request.Messages[len(request.Messages)-1])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("peer did not reach the next turn")
	}
	p.release <- struct{}{}
	if err := awaitTurnResult(t, done); err != nil {
		t.Fatal(err)
	}
	if message := waitPeerCommitted(t, st, receipt.Message.ID); message.Status != "delivered" {
		t.Fatalf("peer status: %+v", message)
	}
}

func TestThreadPeerMessagesRejectSelfForeignReplyAndStaleModelSource(t *testing.T) {
	p := &peerMessageProvider{requests: make(chan llm.ChatRequest, 8)}
	st, turns, target := threadControlFixture(t, p)
	source := createPeerRun(t, st, p, "Source chat")
	peer := application.NewThreadPeerMessageService(st, turns)
	request := peerSourceRequest(t, st, source)
	request.TargetThreadID, request.Content, request.OperationKey = target.ThreadID, "hello", "peer-message-validation-key"
	self := request
	self.TargetThreadID = self.SourceThreadID
	if _, err := peer.Send(t.Context(), self); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("self accepted: %v", err)
	}
	foreign := request
	foreign.ReplyTo = "peer-message-not-received"
	if _, err := peer.Send(t.Context(), foreign); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("foreign reply accepted: %v", err)
	}
	stale := request
	stale.SourceRunID = source.ID
	stale.SourceAttemptID = "stale-attempt"
	stale.SourceLeaseID = "stale-lease"
	stale.SourceLeaseGeneration = 1
	if _, err := peer.Send(t.Context(), stale); err == nil {
		t.Fatal("stale model source accepted")
	}
	unfenced := request
	unfenced.SourceRunID, unfenced.SourceAttemptID, unfenced.SourceLeaseID, unfenced.SourceLeaseGeneration = "", "", "", 0
	if _, err := peer.Send(t.Context(), unfenced); err == nil {
		t.Fatal("unfenced peer source accepted")
	}
	items, err := st.ListThreadPeerMessages(t.Context(), target.ThreadID, 200)
	if err != nil || len(items) != 0 {
		t.Fatalf("rejected send queued data: %+v %v", items, err)
	}
}

func TestThreadPeerMessagesCannotRestartAStoppedRecipient(t *testing.T) {
	p := &peerMessageProvider{requests: make(chan llm.ChatRequest, 8)}
	st, turns, targetRequest := threadControlFixture(t, p)
	source := createPeerRun(t, st, p, "Source chat")
	target, err := st.GetThread(t.Context(), targetRequest.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(st)
	if _, err := runs.Start(t.Context(), target.ActiveRunID); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Cancel(t.Context(), target.ActiveRunID); err != nil {
		t.Fatal(err)
	}
	peer := application.NewThreadPeerMessageService(st, turns)
	request := peerSourceRequest(t, st, source)
	request.TargetThreadID, request.Content, request.OperationKey = target.ID, "Resume automatically", "peer-message-stopped-recipient"
	if _, err := peer.Send(t.Context(), request); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("peer restarted a stopped recipient: %v", err)
	}
	stored, err := st.GetThread(t.Context(), target.ID)
	if err != nil || stored.ActiveRunID != "" || stored.LastRunID != target.LastRunID {
		t.Fatalf("peer created a successor before admission: %+v %v", stored, err)
	}
}
