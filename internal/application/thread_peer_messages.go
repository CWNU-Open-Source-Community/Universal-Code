package application

import (
	"context"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

type threadPeerStore interface {
	ThreadStore
	FindThreadPeerMessage(context.Context, domain.SendThreadPeerMessageRequest) (domain.ThreadPeerMessage, bool, error)
	EnqueueThreadPeerMessage(context.Context, domain.SendThreadPeerMessageRequest, string) (domain.ThreadPeerMessageReceipt, error)
	GetThreadPeerMessage(context.Context, string) (domain.ThreadPeerMessage, error)
	ListThreadPeerMessages(context.Context, string, int) ([]domain.ThreadPeerMessage, error)
	ListThreadPeerTargets(context.Context, string) (domain.ThreadPeerTargets, error)
	ListPendingThreadPeerMessages(context.Context) ([]domain.ThreadPeerMessage, error)
	FailThreadPeerMessage(context.Context, string, string) error
	GetOperatorSteering(context.Context, string) (domain.OperatorSteeringMessage, error)
}

type ThreadPeerMessageService struct {
	store threadPeerStore
	turns *ThreadTurnService
}

func NewThreadPeerMessageService(store ThreadStore, turns *ThreadTurnService) *ThreadPeerMessageService {
	peer, _ := store.(threadPeerStore)
	return &ThreadPeerMessageService{store: peer, turns: turns}
}

func (s *ThreadPeerMessageService) Targets(ctx context.Context, sourceID string) (domain.ThreadPeerTargets, error) {
	if s == nil || s.store == nil {
		return domain.ThreadPeerTargets{}, apperror.New(apperror.CodeFailedPrecondition, "Peer messaging is unavailable")
	}
	if !domain.ValidAgentID(sourceID) {
		return domain.ThreadPeerTargets{}, apperror.New(apperror.CodeInvalidArgument, "Source chat identity is invalid")
	}
	return s.store.ListThreadPeerTargets(ctx, sourceID)
}

func (s *ThreadPeerMessageService) Messages(ctx context.Context, threadID string) ([]domain.ThreadPeerMessage, error) {
	if s == nil || s.store == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "Peer messaging is unavailable")
	}
	if _, err := s.store.GetThread(ctx, threadID); err != nil {
		return nil, err
	}
	return s.store.ListThreadPeerMessages(ctx, threadID, 200)
}

func (s *ThreadPeerMessageService) Send(ctx context.Context, request domain.SendThreadPeerMessageRequest) (domain.ThreadPeerMessageReceipt, error) {
	if s == nil || s.store == nil || s.turns == nil {
		return domain.ThreadPeerMessageReceipt{}, apperror.New(apperror.CodeFailedPrecondition, "Peer messaging is unavailable")
	}
	if !domain.ValidAgentID(request.SourceThreadID) || !domain.ValidAgentID(request.TargetThreadID) || request.SourceThreadID == request.TargetThreadID || request.ReplyTo != "" && !domain.ValidAgentID(request.ReplyTo) {
		return domain.ThreadPeerMessageReceipt{}, apperror.New(apperror.CodeInvalidArgument, "Peer source, target or reply identity is invalid")
	}
	var err error
	request.Content, err = domain.NormalizeOperatorSteeringContent(request.Content)
	if err != nil {
		return domain.ThreadPeerMessageReceipt{}, apperror.New(apperror.CodeInvalidArgument, err.Error())
	}
	request.OperationKey, err = domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || strings.TrimSpace(request.RequestedBy) == "" {
		return domain.ThreadPeerMessageReceipt{}, apperror.New(apperror.CodeInvalidArgument, "Peer operation identity is invalid")
	}
	if message, found, err := s.store.FindThreadPeerMessage(ctx, request); found || err != nil {
		return domain.ThreadPeerMessageReceipt{Version: domain.ThreadPeerMessageVersion, Message: message, Replayed: found}, err
	}
	source, err := s.store.GetThread(ctx, request.SourceThreadID)
	if err != nil {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	target, err := s.store.GetThread(ctx, request.TargetThreadID)
	if err != nil {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	if !source.CanAcceptMessages() || !target.CanAcceptMessages() || source.WorkspaceID != target.WorkspaceID {
		return domain.ThreadPeerMessageReceipt{}, apperror.New(apperror.CodeFailedPrecondition, "Peer chats must be active and in the same workspace")
	}
	// The recipient's existing successor creation copies its own configuration,
	// Plan phase and permission preference. Sender settings never enter it.
	prepared, err := s.turns.threads.prepareRun(ctx, SubmitThreadMessageRequest{peer: true, Version: domain.ThreadMessageProtocolVersion, ThreadID: target.ID, Content: request.Content, OperationKey: request.OperationKey, RequestedBy: "peer:" + source.ID}, domain.ThreadMessageIntent{})
	if err != nil {
		return domain.ThreadPeerMessageReceipt{}, err
	}
	return s.store.EnqueueThreadPeerMessage(ctx, request, prepared.Run.ID)
}

// Run owns all detached deliveries and waits for them at shutdown. Pending
// messages survive a restart; stopped/approval-bound recipients stay queued.
func (s *ThreadPeerMessageService) Run(ctx context.Context) error {
	if s == nil || s.store == nil || s.turns == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "Peer delivery dependencies are unavailable")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var group sync.WaitGroup
	var mu sync.Mutex
	active := map[string]bool{}
	defer func() { cancel(); group.Wait() }()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		messages, err := s.store.ListPendingThreadPeerMessages(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		for _, message := range messages {
			mu.Lock()
			if active[message.TargetThreadID] || len(active) >= 4 {
				mu.Unlock()
				continue
			}
			active[message.TargetThreadID] = true
			mu.Unlock()
			group.Add(1)
			go func(message domain.ThreadPeerMessage) {
				defer group.Done()
				defer func() { mu.Lock(); delete(active, message.TargetThreadID); mu.Unlock() }()
				err := s.deliver(ctx, message)
				if err != nil && ctx.Err() == nil && apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeFailedPrecondition && apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeConflict {
					_ = s.store.FailThreadPeerMessage(ctx, message.ID, err.Error())
				}
			}(message)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *ThreadPeerMessageService) deliver(ctx context.Context, message domain.ThreadPeerMessage) error {
	thread, err := s.store.GetThread(ctx, message.TargetThreadID)
	if err != nil {
		return err
	}
	if !thread.CanAcceptMessages() {
		return s.store.FailThreadPeerMessage(ctx, message.ID, "Recipient chat was archived or deleted before delivery")
	}
	run, err := s.store.GetRun(ctx, message.RunID)
	if err != nil {
		return err
	}
	input, err := s.store.GetOperatorSteering(ctx, message.SteeringMessageID)
	if err != nil {
		return err
	}
	if input.Status != domain.OperatorSteeringPending {
		return nil
	}
	if run.Terminal() || thread.ActiveRunID != run.ID {
		return s.store.FailThreadPeerMessage(ctx, message.ID, "Recipient execution ended before delivery")
	}
	linkedSession, err := s.store.GetSession(ctx, run.SessionID)
	if err != nil {
		return err
	}
	if linkedSession.Status != session.StatusActive {
		return apperror.New(apperror.CodeFailedPrecondition, "Recipient session is not active")
	}
	_, err = s.turns.Execute(ctx, ExecuteThreadTurnRequest{queued: &SubmitThreadMessageResult{Thread: thread, Run: run, Session: linkedSession, Message: input}, automatic: true, Version: domain.ThreadMessageProtocolVersion, ThreadID: thread.ID, Content: input.Content, OperationKey: "peer-delivery-" + message.ID, RequestedBy: "peer:" + message.SourceThreadID})
	return err
}
