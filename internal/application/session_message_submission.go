package application

import (
	"context"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/session"
)

type SessionMessageSubmissionStore interface {
	GetSession(context.Context, string) (session.Session, error)
	GetRunBySession(context.Context, string) (domain.Run, bool, error)
	GetMission(context.Context, string) (domain.Mission, error)
	EnqueueOperatorSteering(context.Context,
		domain.EnqueueOperatorSteeringRequest) (domain.OperatorSteeringEnqueueResult, error)
}

type SessionMessageSubmissionService struct {
	store          SessionMessageSubmissionStore
	lifecycleHooks *hooks.Engine
}

// WithLifecycleHooks wires the restricted lifecycle hook engine. A nil engine
// leaves every hook boundary a no-op.
func (s *SessionMessageSubmissionService) WithLifecycleHooks(
	engine *hooks.Engine,
) *SessionMessageSubmissionService {
	if s != nil {
		s.lifecycleHooks = engine
	}
	return s
}

type SubmitSessionMessageRequest struct {
	Version      string
	SessionID    string
	Content      string
	OperationKey string
	RequestedBy  string
	DeliveryMode domain.OperatorSteeringDeliveryMode
}

type SubmitSessionMessageResult struct {
	Run      domain.Run
	Session  session.Session
	Message  domain.OperatorSteeringMessage
	Replayed bool
}

func NewSessionMessageSubmissionService(
	store SessionMessageSubmissionStore,
) *SessionMessageSubmissionService {
	return &SessionMessageSubmissionService{store: store}
}

func (s *SessionMessageSubmissionService) Submit(ctx context.Context,
	request SubmitSessionMessageRequest,
) (SubmitSessionMessageResult, error) {
	if s == nil || s.store == nil {
		return SubmitSessionMessageResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "Session message submission store is required")
	}
	if request.Version != domain.SessionMessageSubmissionProtocolVersion {
		return SubmitSessionMessageResult{}, apperror.New(
			apperror.CodeInvalidArgument, "unsupported Session message submission version")
	}
	if request.SessionID != strings.TrimSpace(request.SessionID) ||
		!domain.ValidAgentID(request.SessionID) {
		return SubmitSessionMessageResult{}, apperror.New(
			apperror.CodeInvalidArgument, "Session message submission Session id is invalid")
	}
	content, err := domain.NormalizeOperatorSteeringContent(request.Content)
	if err != nil {
		return SubmitSessionMessageResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, err.Error(), err)
	}
	operationKey, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || containsSpaceOrControl(operationKey) {
		return SubmitSessionMessageResult{}, apperror.New(
			apperror.CodeInvalidArgument, "Session message idempotency key is invalid")
	}
	requestedBy := strings.TrimSpace(request.RequestedBy)
	if requestedBy != request.RequestedBy || !domain.ValidAgentID(requestedBy) {
		return SubmitSessionMessageResult{}, apperror.New(
			apperror.CodeInvalidArgument, "Session message requester is invalid")
	}

	linkedSession, err := s.store.GetSession(ctx, request.SessionID)
	if err != nil {
		return SubmitSessionMessageResult{}, apperror.Normalize(err)
	}
	if linkedSession.ID != request.SessionID {
		return SubmitSessionMessageResult{}, apperror.New(
			apperror.CodeConflict, "Session message lookup identity changed")
	}
	run, found, err := s.store.GetRunBySession(ctx, linkedSession.ID)
	if err != nil {
		return SubmitSessionMessageResult{}, apperror.Normalize(err)
	}
	if !found {
		return SubmitSessionMessageResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "Session is not bound to a Run")
	}
	if run.SessionID != linkedSession.ID {
		return SubmitSessionMessageResult{}, apperror.New(
			apperror.CodeConflict, "Session and Run binding changed")
	}

	// The user_prompt_submit boundary fires before the message is queued; a
	// deny rejects the submission with nothing recorded.
	mission, missionErr := s.store.GetMission(ctx, run.MissionID)
	if missionErr != nil {
		return SubmitSessionMessageResult{}, apperror.Normalize(missionErr)
	}
	if err := executeLifecycleBoundary(ctx, s.lifecycleHooks, hooks.UserPromptSubmit,
		run.ID, mission.WorkspaceID, hooks.BuildUserPromptSubmitPayload(
			hooks.UserPromptSubmitPayload{SessionID: linkedSession.ID, RunID: run.ID,
				Content: content})); err != nil {
		return SubmitSessionMessageResult{}, err
	}
	queued, err := s.store.EnqueueOperatorSteering(ctx,
		domain.EnqueueOperatorSteeringRequest{
			RunID: run.ID, SessionID: linkedSession.ID, Content: content,
			OperationKey: operationKey, RequestedBy: requestedBy,
			DeliveryMode: request.DeliveryMode,
		})
	if err != nil {
		return SubmitSessionMessageResult{}, apperror.Normalize(err)
	}
	if queued.Message.RunID != run.ID || queued.Message.SessionID != linkedSession.ID {
		return SubmitSessionMessageResult{}, apperror.New(
			apperror.CodeConflict, "submitted Session message binding changed")
	}
	return SubmitSessionMessageResult{Run: run, Session: linkedSession,
		Message: queued.Message, Replayed: queued.Replayed}, nil
}
