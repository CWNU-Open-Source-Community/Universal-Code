package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
)

const (
	ThreadQueuedMessagesPathTemplate                   = "/api/v1/threads/{thread_id}/queued-messages"
	SessionSteeringRevisionPathTemplate                = "/api/v1/sessions/{session_id}/messages/{message_id}/revise"
	SessionSteeringRevisionObservationPathTemplate     = "/api/v1/sessions/{session_id}/messages/{message_id}/revisions/{operation_key}"
	SessionSteeringCancellationObservationPathTemplate = "/api/v1/sessions/{session_id}/messages/{message_id}/cancellations/{operation_key}"
)

type threadQueuedMessagesStore interface {
	ListThreadQueuedMessages(context.Context, string) (domain.ThreadQueuedMessagesSnapshot, error)
	ReviseOperatorSteering(context.Context, domain.ReviseOperatorSteeringRequest) (domain.ReviseOperatorSteeringResult, error)
	InspectOperatorSteeringRevision(context.Context, string, string, string, string) (domain.OperatorSteeringRevisionInspection, error)
}

type ThreadQueuedMessageView struct {
	ID              string                           `json:"id"`
	Sequence        int64                            `json:"sequence"`
	Status          string                           `json:"status"`
	DeliveryMode    string                           `json:"delivery_mode"`
	Prepared        bool                             `json:"prepared"`
	Content         string                           `json:"content"`
	ContentSHA256   string                           `json:"content_sha256"`
	ContentRedacted bool                             `json:"content_redacted"`
	Revision        int64                            `json:"revision"`
	CreatedAt       time.Time                        `json:"created_at"`
	EditedAt        *time.Time                       `json:"edited_at,omitempty"`
	Images          []domain.WorkspaceImage          `json:"images"`
	Attachments     []domain.WorkspaceFileAttachment `json:"attachments"`
	CanEdit         bool                             `json:"can_edit"`
	CanCancel       bool                             `json:"can_cancel"`
}

type ThreadQueuedMessagesView struct {
	Version          string                    `json:"version"`
	CurrentAttemptID string                    `json:"current_attempt_id,omitempty"`
	ExecutionID      string                    `json:"execution_id,omitempty"`
	ThreadID         string                    `json:"thread_id"`
	RunID            string                    `json:"run_id"`
	SessionID        string                    `json:"session_id"`
	Pending          int                       `json:"pending"`
	Prepared         int                       `json:"prepared"`
	Items            []ThreadQueuedMessageView `json:"items"`
	CapabilityGrant  bool                      `json:"capability_grant"`
}

type SessionSteeringRevisionRequestView struct {
	Version          string  `json:"version"`
	ExpectedRevision *int64  `json:"expected_revision"`
	Content          *string `json:"content"`
}

type SessionSteeringRevisionReceiptView struct {
	ID               string    `json:"id"`
	FromRevision     int64     `json:"from_revision"`
	ToRevision       int64     `json:"to_revision"`
	OldContentSHA256 string    `json:"old_content_sha256"`
	NewContentSHA256 string    `json:"new_content_sha256"`
	CreatedAt        time.Time `json:"created_at"`
}

type QueueRevisionUnchangedView struct {
	Version                 string `json:"version"`
	RunID                   string `json:"run_id"`
	SessionID               string `json:"session_id"`
	MessageID               string `json:"message_id"`
	ExpectedRevision        int64  `json:"expected_revision"`
	OperationKeySHA256      string `json:"operation_key_sha256"`
	RequestContentSHA256    string `json:"request_content_sha256"`
	NormalizedContentSHA256 string `json:"normalized_content_sha256"`
	CurrentContentSHA256    string `json:"current_content_sha256"`
	ExecutionStarted        bool   `json:"execution_started"`
	ModelCalled             bool   `json:"model_called"`
	ToolCalled              bool   `json:"tool_called"`
	CapabilityGrant         bool   `json:"capability_grant"`
}

type SessionSteeringRevisionView struct {
	Version          string                             `json:"version"`
	RunID            string                             `json:"run_id"`
	SessionID        string                             `json:"session_id"`
	MessageID        string                             `json:"message_id"`
	Receipt          SessionSteeringRevisionReceiptView `json:"receipt"`
	Replayed         bool                               `json:"replayed"`
	ExecutionStarted bool                               `json:"execution_started"`
	ModelCalled      bool                               `json:"model_called"`
	ToolCalled       bool                               `json:"tool_called"`
	CapabilityGrant  bool                               `json:"capability_grant"`
}

type SessionSteeringRevisionObservationView struct {
	Version         string                                  `json:"version"`
	SessionID       string                                  `json:"session_id"`
	MessageID       string                                  `json:"message_id"`
	State           string                                  `json:"state"`
	Revision        *SessionSteeringRevisionView            `json:"revision,omitempty"`
	Message         *OperatorSteeringObservationMessageView `json:"message,omitempty"`
	CapabilityGrant bool                                    `json:"capability_grant"`
}

type OperatorSteeringObservationMessageView struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	SessionID string `json:"session_id"`
	Revision  int64  `json:"revision"`
	Status    string `json:"status"`
}

type SessionSteeringCancellationObservedReceiptView struct {
	CancellationID string    `json:"cancellation_id"`
	RunID          string    `json:"run_id"`
	SessionID      string    `json:"session_id"`
	MessageID      string    `json:"message_id"`
	Kind           string    `json:"kind"`
	CreatedAt      time.Time `json:"created_at"`
}

type SessionSteeringCancellationObservationView struct {
	Version         string                                          `json:"version"`
	SessionID       string                                          `json:"session_id"`
	MessageID       string                                          `json:"message_id"`
	State           string                                          `json:"state"`
	Message         *OperatorSteeringObservationMessageView         `json:"message,omitempty"`
	Receipt         *SessionSteeringCancellationObservedReceiptView `json:"receipt,omitempty"`
	CapabilityGrant bool                                            `json:"capability_grant"`
}

func steeringObservationMessage(message *domain.OperatorSteeringObservationMessage, sessionID, messageID string) (*OperatorSteeringObservationMessageView, error) {
	if message == nil || message.SessionID != sessionID || message.ID != messageID || !domain.ValidAgentID(message.RunID) || message.Revision < 0 ||
		(message.Status != domain.OperatorSteeringPending && message.Status != domain.OperatorSteeringCommitted && message.Status != domain.OperatorSteeringCancelled) {
		return nil, apperror.New(apperror.CodeConflict, "queue observation message binding differs")
	}
	return &OperatorSteeringObservationMessageView{ID: message.ID, RunID: message.RunID, SessionID: message.SessionID, Revision: message.Revision, Status: string(message.Status)}, nil
}

func (a *API) threadQueuedMessages(request *http.Request, threadID string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	reader, ok := a.store.(threadQueuedMessagesStore)
	if !ok {
		return nil, nil, apperror.New(apperror.CodeFailedPrecondition, "persistent queue reader is unavailable")
	}
	snapshot, err := reader.ListThreadQueuedMessages(request.Context(), threadID)
	if err != nil {
		return nil, nil, err
	}
	if snapshot.ThreadID != threadID || snapshot.ProtocolVersion != domain.ThreadQueuedMessagesProtocolVersion || len(snapshot.Messages) > domain.MaxPendingOperatorSteering {
		return nil, nil, apperror.New(apperror.CodeConflict, "persistent queue binding differs")
	}
	view := ThreadQueuedMessagesView{Version: snapshot.ProtocolVersion, ThreadID: snapshot.ThreadID,
		RunID: snapshot.RunID, SessionID: snapshot.SessionID, Items: []ThreadQueuedMessageView{}}
	if snapshot.CurrentAttemptID != "" && a.sessionSteeringControlEnabled && a.runExecutionEnabled {
		if controller, ok := a.threadTurnController.(ThreadExecutionController); ok {
			state, stateErr := controller.ExecutionState(request.Context(), threadID)
			if stateErr == nil && state.ThreadID == threadID && state.State == "running" && domain.ValidAgentID(state.ExecutionID) {
				view.CurrentAttemptID, view.ExecutionID = snapshot.CurrentAttemptID, state.ExecutionID
			}
		}
	}
	var sequence int64
	for _, queued := range snapshot.Messages {
		message := queued.Message
		if message.RunID != snapshot.RunID || message.SessionID != snapshot.SessionID || message.Status != domain.OperatorSteeringPending ||
			message.Sequence <= sequence || message.Validate() != nil || len(queued.Images) != message.ImageCount || len(queued.Attachments) != message.AttachmentCount {
			return nil, nil, apperror.New(apperror.CodeConflict, "persistent queue message binding differs")
		}
		sequence = message.Sequence
		content := redact.String(message.Content)
		changeable := a.sessionSteeringControlEnabled && !message.Prepared &&
			(snapshot.RunStatus == domain.RunRunning || snapshot.RunStatus == domain.RunPaused)
		peer := false
		if reader, ok := a.store.(interface {
			ThreadPeerMessageForSteering(context.Context, string) (domain.ThreadPeerMessage, bool, error)
		}); ok {
			_, peer, err = reader.ThreadPeerMessageForSteering(request.Context(), message.ID)
			if err != nil {
				return nil, nil, err
			}
		}
		view.Items = append(view.Items, ThreadQueuedMessageView{ID: message.ID, Sequence: message.Sequence,
			Status: string(message.Status), DeliveryMode: string(message.DeliveryMode), Prepared: message.Prepared, Content: content,
			ContentSHA256: message.ContentSHA256, ContentRedacted: content != message.Content,
			Revision: message.Revision, CreatedAt: message.CreatedAt, EditedAt: message.EditedAt,
			Images: append([]domain.WorkspaceImage{}, queued.Images...), Attachments: append([]domain.WorkspaceFileAttachment{}, queued.Attachments...),
			CanEdit: changeable && !peer, CanCancel: changeable})
		if message.Prepared {
			view.Prepared++
		} else {
			view.Pending++
		}
	}
	return view, nil, nil
}

func matchSessionSteeringRevisionPath(path string) (string, string, bool) {
	const prefix = "/api/v1/sessions/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 4 || parts[1] != "messages" || parts[3] != "revise" {
		return "", "", false
	}
	return parts[0], parts[2], true
}

func (a *API) serveSessionSteeringRevision(writer http.ResponseWriter, request *http.Request, requestID, sessionID, messageID string) {
	if !a.sessionSteeringControlEnabled {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "HTTP API endpoint was not found"), http.StatusNotFound)
		return
	}
	if !a.authorized(request, a.controlTokenHash) {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent Control API"`)
		a.writeError(writer, requestID, apperror.New(apperror.CodePolicyDenied, "valid control bearer authorization is required"), http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "Session steering revision endpoint only supports POST"), http.StatusMethodNotAllowed)
		return
	}
	for _, id := range []string{sessionID, messageID} {
		if err := validatePathIdentity(id); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	key, err := sessionControlIdempotencyKey(request.Header, "Session steering revision")
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	body, err := readBoundedRequestBody(request, 128*1024)
	if err != nil {
		a.writeError(writer, requestID, err, http.StatusRequestEntityTooLarge)
		return
	}
	if !utf8.Valid(body) {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "revision body must be valid UTF-8 JSON"), 0)
		return
	}
	if err := rejectDuplicateJSONObjectFields(body, "Session steering revision"); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var view SessionSteeringRevisionRequestView
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&view); err != nil {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "revision body must be one JSON object"), 0)
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if view.Version != domain.SessionSteeringRevisionProtocolVersion || view.ExpectedRevision == nil || *view.ExpectedRevision < 0 || view.Content == nil {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "revision version, expected_revision and content are required"), 0)
		return
	}
	controller, ok := a.store.(threadQueuedMessagesStore)
	if !ok {
		a.writeError(writer, requestID, apperror.New(apperror.CodeFailedPrecondition, "queue revision store is unavailable"), 0)
		return
	}
	result, err := controller.ReviseOperatorSteering(request.Context(), domain.ReviseOperatorSteeringRequest{
		SessionID: sessionID, MessageID: messageID, ExpectedRevision: *view.ExpectedRevision, Content: *view.Content,
		OperationKey: key, RequestedBy: "http_session_operator"})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if result.Receipt.SessionID != sessionID || result.Receipt.MessageID != messageID || result.Receipt.Validate() != nil ||
		result.Receipt.RequestedBy != "http_session_operator" || result.Receipt.FromRevision != *view.ExpectedRevision ||
		result.Message.ID != messageID || result.Message.RunID != result.Receipt.RunID || result.Message.SessionID != sessionID {
		a.writeError(writer, requestID, apperror.New(apperror.CodeConflict, "revision receipt binding differs"), 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, sessionSteeringRevisionView(result.Receipt, result.Replayed), nil, http.StatusAccepted)
}

func sessionSteeringRevisionView(receipt domain.OperatorSteeringRevisionReceipt, replayed bool) SessionSteeringRevisionView {
	return SessionSteeringRevisionView{Version: domain.SessionSteeringRevisionProtocolVersion, RunID: receipt.RunID,
		SessionID: receipt.SessionID, MessageID: receipt.MessageID, Replayed: replayed,
		Receipt: SessionSteeringRevisionReceiptView{ID: receipt.ID, FromRevision: receipt.FromRevision, ToRevision: receipt.ToRevision,
			OldContentSHA256: receipt.OldContentSHA256, NewContentSHA256: receipt.NewContentSHA256, CreatedAt: receipt.CreatedAt}}
}

func (a *API) sessionSteeringRevisionObservation(request *http.Request, sessionID, messageID, key string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	reader, ok := a.store.(threadQueuedMessagesStore)
	if !ok {
		return nil, nil, apperror.New(apperror.CodeFailedPrecondition, "queue revision reader is unavailable")
	}
	inspection, err := reader.InspectOperatorSteeringRevision(request.Context(), sessionID, messageID, key, "http_session_operator")
	if err != nil {
		return nil, nil, err
	}
	view := SessionSteeringRevisionObservationView{Version: domain.SessionSteeringRevisionProtocolVersion,
		SessionID: sessionID, MessageID: messageID, State: string(inspection.State)}
	if inspection.State == domain.OperatorSteeringRevisionSealed {
		if inspection.Receipt == nil || inspection.Receipt.SessionID != sessionID || inspection.Receipt.MessageID != messageID || inspection.Receipt.Validate() != nil ||
			inspection.Receipt.RequestedBy != "http_session_operator" {
			return nil, nil, apperror.New(apperror.CodeConflict, "revision observation binding differs")
		}
		sealed := sessionSteeringRevisionView(*inspection.Receipt, true)
		view.Revision = &sealed
	} else if inspection.State != domain.OperatorSteeringRevisionAbsent || inspection.Receipt != nil {
		return nil, nil, apperror.New(apperror.CodeConflict, "revision observation state is invalid")
	} else {
		view.Message, err = steeringObservationMessage(inspection.Message, sessionID, messageID)
		if err != nil {
			return nil, nil, err
		}
	}
	return view, nil, nil
}

func (a *API) sessionSteeringCancellationObservation(request *http.Request, sessionID, messageID, key string) (any, *Page, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, nil, err
	}
	reader, ok := a.store.(interface {
		InspectOperatorSteeringCancellation(context.Context, string, string, string, string) (domain.OperatorSteeringCancellationInspection, error)
	})
	if !ok {
		return nil, nil, apperror.New(apperror.CodeFailedPrecondition, "queue cancellation reader is unavailable")
	}
	inspection, err := reader.InspectOperatorSteeringCancellation(request.Context(), sessionID, messageID, key, "http_session_operator")
	if err != nil {
		return nil, nil, err
	}
	view := SessionSteeringCancellationObservationView{Version: domain.SessionSteeringCancellationProtocolVersion,
		SessionID: sessionID, MessageID: messageID, State: string(inspection.State)}
	switch string(inspection.State) {
	case "sealed":
		r := inspection.Receipt
		if r == nil || r.MessageID != messageID || r.SessionID != sessionID || !domain.ValidAgentID(r.RunID) ||
			!domain.ValidAgentID(r.CancellationID) || r.RequestedBy != "http_session_operator" || r.Kind != domain.OperatorSteeringCancellationOperator || r.CreatedAt.IsZero() {
			return nil, nil, apperror.New(apperror.CodeConflict, "cancellation observation receipt binding differs")
		}
		view.Receipt = &SessionSteeringCancellationObservedReceiptView{CancellationID: r.CancellationID, RunID: r.RunID,
			SessionID: r.SessionID, MessageID: r.MessageID, Kind: string(r.Kind), CreatedAt: r.CreatedAt}
	case "absent":
		if inspection.Receipt != nil {
			return nil, nil, apperror.New(apperror.CodeConflict, "cancellation observation state differs")
		}
		view.Message, err = steeringObservationMessage(inspection.Message, sessionID, messageID)
		if err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, apperror.New(apperror.CodeConflict, "cancellation observation state is invalid")
	}
	return view, nil, nil
}
