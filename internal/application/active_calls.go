package application

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/redact"
)

const ActiveCallEnvelopeVersion = "v1"

const PublicModelStreamVersion = "model_public_stream.v3"

type PublicModelStreamContentKind string

const (
	PublicModelStreamRootMessage    PublicModelStreamContentKind = "root_message"
	PublicModelStreamToolCommentary PublicModelStreamContentKind = "tool_commentary"
)

const activeCallSubscriberBuffer = 32

type ActiveCallEventType string

const (
	ActiveCallSnapshotEvent        ActiveCallEventType = "model.live_snapshot"
	ActiveCallStartedEvent         ActiveCallEventType = "model.live_started"
	ActiveCallProgressEvent        ActiveCallEventType = "model.live_progress"
	ActiveCallCancelRequestedEvent ActiveCallEventType = "model.live_cancel_requested"
	ActiveCallCompletedEvent       ActiveCallEventType = "model.live_completed"
	ActiveCallFailedEvent          ActiveCallEventType = "model.live_failed"
)

type ActiveCallInfo struct {
	RunID            string    `json:"run_id"`
	SessionID        string    `json:"session_id"`
	AttemptID        string    `json:"attempt_id"`
	ModelAttempt     int       `json:"model_attempt"`
	TransportAttempt int       `json:"transport_attempt"`
	MaxAttempts      int       `json:"max_attempts"`
	ProtocolRepair   int       `json:"protocol_repair"`
	ToolRound        int       `json:"tool_round"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	StartedAt        time.Time `json:"started_at"`
	StreamChunks     int       `json:"stream_chunks"`
	StreamBytes      int       `json:"stream_bytes"`
	CancelRequested  bool      `json:"cancel_requested"`
}

// PublicModelStreamSnapshot is a process-local, provisional view of safe
// user-facing model text. It is never written to the event store.
type PublicModelStreamSnapshot struct {
	Version         string                       `json:"version"`
	Call            ActiveCallInfo               `json:"call"`
	Revision        int64                        `json:"revision"`
	ResponseID      string                       `json:"response_id,omitempty"`
	EventSequence   int                          `json:"event_sequence"`
	Items           []llm.OutputItem             `json:"items"`
	ContentKind     PublicModelStreamContentKind `json:"content_kind"`
	Text            string                       `json:"text"`
	MessageComplete bool                         `json:"message_complete"`
	Provisional     bool                         `json:"provisional"`
	UpdatedAt       time.Time                    `json:"updated_at"`
}

func (s PublicModelStreamSnapshot) Validate() error {
	if s.Version != PublicModelStreamVersion || s.Revision <= 0 || s.UpdatedAt.IsZero() || !s.Provisional {
		return errors.New("public model stream envelope is invalid")
	}
	if s.ContentKind != PublicModelStreamRootMessage && s.ContentKind != PublicModelStreamToolCommentary {
		return errors.New("public model stream content kind is invalid")
	}
	if err := s.Call.Validate(); err != nil {
		return err
	}
	if len(s.Text) > llm.MaxModelOutputBytes || !utf8.ValidString(s.Text) {
		return errors.New("public model stream text is invalid")
	}
	if s.ResponseID == "" {
		if s.EventSequence != 0 || len(s.Items) != 0 {
			return errors.New("public model stream item identity is incomplete")
		}
		return nil
	}
	if err := llm.ValidateStreamID(s.ResponseID); err != nil {
		return errors.New("public model stream response identity is invalid")
	}
	if s.EventSequence <= 0 || s.EventSequence > llm.MaxItemStreamEvents {
		return errors.New("public model stream event sequence is invalid")
	}
	if len(s.Items) > llm.MaxProviderOutputItems {
		return errors.New("public model stream contains too many output items")
	}
	seen := make(map[string]struct{}, len(s.Items))
	for _, item := range s.Items {
		if err := item.Validate(); err != nil || item.ResponseID != s.ResponseID ||
			!item.Provisional || item.Durable {
			return errors.New("public model stream output item is invalid")
		}
		if _, exists := seen[item.ID]; exists {
			return errors.New("public model stream output item ids are not unique")
		}
		seen[item.ID] = struct{}{}
	}
	return nil
}

func (i ActiveCallInfo) Validate() error {
	if strings.TrimSpace(i.RunID) == "" || strings.TrimSpace(i.SessionID) == "" || strings.TrimSpace(i.AttemptID) == "" {
		return errors.New("active call run, session, and attempt ids are required")
	}
	if i.ModelAttempt <= 0 || i.TransportAttempt <= 0 || i.MaxAttempts <= 0 || i.TransportAttempt > i.MaxAttempts {
		return errors.New("active call attempt counters are invalid")
	}
	if i.ProtocolRepair < 0 || i.ProtocolRepair > 1 {
		return errors.New("active call protocol repair number must be zero or one")
	}
	if i.ToolRound < 0 || i.ToolRound > domain.MaxSupervisorExecutionRounds {
		return errors.New("active call tool round is out of range")
	}
	if strings.TrimSpace(i.Provider) == "" || strings.TrimSpace(i.Model) == "" {
		return errors.New("active call provider and model are required")
	}
	if i.StartedAt.IsZero() {
		return errors.New("active call start time is required")
	}
	if i.StreamChunks < 0 || i.StreamBytes < 0 {
		return errors.New("active call stream counters cannot be negative")
	}
	return nil
}

type ActiveCallEvent struct {
	Version    string              `json:"version"`
	Sequence   int64               `json:"sequence"`
	Type       ActiveCallEventType `json:"type"`
	Call       ActiveCallInfo      `json:"call"`
	DeltaBytes int                 `json:"delta_bytes,omitempty"`
	Outcome    llm.Outcome         `json:"outcome,omitempty"`
	CreatedAt  time.Time           `json:"created_at"`
}

func (e ActiveCallEvent) Validate() error {
	if e.Version != ActiveCallEnvelopeVersion || e.Sequence <= 0 || e.CreatedAt.IsZero() {
		return errors.New("active call event envelope is invalid")
	}
	if err := e.Call.Validate(); err != nil {
		return err
	}
	switch e.Type {
	case ActiveCallSnapshotEvent, ActiveCallStartedEvent, ActiveCallCancelRequestedEvent:
		if e.DeltaBytes != 0 || e.Outcome != "" {
			return errors.New("active call state event contains invalid result metadata")
		}
	case ActiveCallProgressEvent:
		if e.DeltaBytes <= 0 || e.Outcome != "" {
			return errors.New("active call progress requires a positive byte delta")
		}
	case ActiveCallCompletedEvent:
		if e.DeltaBytes != 0 || e.Outcome != llm.OutcomeSuccess {
			return errors.New("active call completion requires a success outcome")
		}
	case ActiveCallFailedEvent:
		if e.DeltaBytes != 0 || !e.Outcome.Valid() || e.Outcome == llm.OutcomeSuccess {
			return errors.New("active call failure requires a failure outcome")
		}
	default:
		return errors.New("active call event type is invalid")
	}
	return nil
}

type ActiveCallSubscription struct {
	events     <-chan ActiveCallEvent
	registry   *ActiveCallRegistry
	key        activeCallKey
	subscriber uint64
	dropped    *atomic.Bool
	once       sync.Once
}

func (s *ActiveCallSubscription) Events() <-chan ActiveCallEvent {
	if s == nil {
		return nil
	}
	return s.events
}

func (s *ActiveCallSubscription) Dropped() bool {
	return s != nil && s.dropped != nil && s.dropped.Load()
}

func (s *ActiveCallSubscription) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.registry != nil {
			s.registry.unsubscribe(s.key, s.subscriber)
		}
	})
}

type ActiveCallCancelRequest struct {
	RunID  string
	Reason string
}

type ActiveCallCancelResult struct {
	Found            bool
	AuditRecorded    bool
	Signaled         bool
	AlreadyRequested bool
	Call             ActiveCallInfo
}

type ActiveCallRegistry struct {
	mu               sync.RWMutex
	calls            map[string]*activeCallEntry
	nextSubscriber   uint64
	subscriberBuffer int
}

type activeCallKey struct {
	runID        string
	attemptID    string
	modelAttempt int
}

type activeCallEntry struct {
	key             activeCallKey
	checkpoint      domain.SupervisorCheckpoint
	attempt         llm.ModelAttempt
	info            ActiveCallInfo
	cancel          context.CancelFunc
	started         bool
	sequence        int64
	publicRevision  int64
	publicKind      PublicModelStreamContentKind
	publicText      string
	publicComplete  bool
	publicUpdatedAt time.Time
	responseID      string
	eventSequence   int
	publicItems     map[string]*llm.OutputItem
	publicItemOrder []string
	subscribers     map[uint64]*activeCallSubscriber
}

type activeCallSubscriber struct {
	events  chan ActiveCallEvent
	dropped *atomic.Bool
}

type activeCallLease struct {
	registry *ActiveCallRegistry
	key      activeCallKey
	entry    *activeCallEntry
	ctx      context.Context
	once     sync.Once
}

type activeCallCancelTarget struct {
	key        activeCallKey
	checkpoint domain.SupervisorCheckpoint
	attempt    llm.ModelAttempt
	info       ActiveCallInfo
}

func NewActiveCallRegistry() *ActiveCallRegistry {
	return newActiveCallRegistry(activeCallSubscriberBuffer)
}

func newActiveCallRegistry(subscriberBuffer int) *ActiveCallRegistry {
	if subscriberBuffer <= 0 {
		subscriberBuffer = activeCallSubscriberBuffer
	}
	return &ActiveCallRegistry{calls: map[string]*activeCallEntry{}, subscriberBuffer: subscriberBuffer}
}

func (r *ActiveCallRegistry) Lookup(runID string) (ActiveCallInfo, bool) {
	if r == nil {
		return ActiveCallInfo{}, false
	}
	runID = strings.TrimSpace(runID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.calls[runID]
	if !ok || !entry.started {
		return ActiveCallInfo{}, false
	}
	return entry.info, true
}

func (r *ActiveCallRegistry) LookupPublic(runID string) (PublicModelStreamSnapshot, bool) {
	if r == nil {
		return PublicModelStreamSnapshot{}, false
	}
	runID = strings.TrimSpace(runID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.calls[runID]
	if !ok || !entry.started || entry.publicRevision <= 0 {
		return PublicModelStreamSnapshot{}, false
	}
	return publicModelStreamSnapshot(entry), true
}

func (r *ActiveCallRegistry) LookupSession(sessionID string) (ActiveCallInfo, bool) {
	if r == nil {
		return ActiveCallInfo{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ActiveCallInfo{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, entry := range r.calls {
		if entry.started && entry.info.SessionID == sessionID {
			return entry.info, true
		}
	}
	return ActiveCallInfo{}, false
}

func (r *ActiveCallRegistry) List() []ActiveCallInfo {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	items := make([]ActiveCallInfo, 0, len(r.calls))
	for _, entry := range r.calls {
		if entry.started {
			items = append(items, entry.info)
		}
	}
	r.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		if items[i].StartedAt.Equal(items[j].StartedAt) {
			return items[i].RunID < items[j].RunID
		}
		return items[i].StartedAt.Before(items[j].StartedAt)
	})
	return items
}

func (r *ActiveCallRegistry) Subscribe(runID string) (*ActiveCallSubscription, error) {
	if r == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "active call registry is required")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, apperror.New(apperror.CodeInvalidArgument, "run id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.calls[runID]
	if !ok || !entry.started {
		return nil, apperror.New(apperror.CodeNotFound, "active model call was not found")
	}
	r.nextSubscriber++
	id := r.nextSubscriber
	dropped := &atomic.Bool{}
	channel := make(chan ActiveCallEvent, r.subscriberBuffer)
	entry.subscribers[id] = &activeCallSubscriber{events: channel, dropped: dropped}
	channel <- activeCallEvent(entry, ActiveCallSnapshotEvent, 0, "")
	return &ActiveCallSubscription{
		events: channel, registry: r, key: entry.key, subscriber: id, dropped: dropped,
	}, nil
}

func (r *ActiveCallRegistry) reserve(parent context.Context, checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, sessionID string) (*activeCallLease, error) {
	if r == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "active call registry is required")
	}
	if parent == nil {
		return nil, apperror.New(apperror.CodeInvalidArgument, "active call parent context is required")
	}
	if err := checkpoint.Validate(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument, "invalid active call checkpoint", err)
	}
	if checkpoint.Phase != domain.SupervisorTurnStarted {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "active model call requires a started supervisor turn")
	}
	if attempt.Outcome != "" || strings.TrimSpace(attempt.ErrorText) != "" || attempt.StreamEvents != 0 || attempt.StreamBytes != 0 {
		return nil, apperror.New(apperror.CodeInvalidArgument, "active model call requires the original started attempt")
	}
	if err := attempt.ValidateStarted(); err != nil {
		return nil, apperror.Wrap(apperror.CodeInvalidArgument, "invalid active model attempt", err)
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, apperror.New(apperror.CodeInvalidArgument, "active model call session id is required")
	}
	key := activeCallKey{runID: checkpoint.RunID, attemptID: checkpoint.AttemptID, modelAttempt: attempt.Number}
	callCtx, cancel := context.WithCancel(parent)
	entry := &activeCallEntry{
		key: key, checkpoint: checkpoint, attempt: attempt, cancel: cancel,
		info: ActiveCallInfo{
			RunID: checkpoint.RunID, SessionID: sessionID, AttemptID: checkpoint.AttemptID,
			ModelAttempt: attempt.Number, TransportAttempt: attempt.TransportNumber(), MaxAttempts: attempt.MaxAttempts,
			ProtocolRepair: attempt.ProtocolRepair, ToolRound: attempt.ToolRound,
			Provider: redact.String(strings.TrimSpace(attempt.Provider)),
			Model:    redact.String(strings.TrimSpace(attempt.Model)),
		},
		subscribers: map[uint64]*activeCallSubscriber{},
		publicItems: map[string]*llm.OutputItem{},
	}
	r.mu.Lock()
	if _, exists := r.calls[key.runID]; exists {
		r.mu.Unlock()
		cancel()
		return nil, apperror.New(apperror.CodeConflict, "run already has an active model call")
	}
	r.calls[key.runID] = entry
	r.mu.Unlock()
	return &activeCallLease{registry: r, key: key, entry: entry, ctx: callCtx}, nil
}

func (l *activeCallLease) Context() context.Context {
	if l == nil {
		return nil
	}
	return l.ctx
}

func (l *activeCallLease) Activate() error {
	if l == nil || l.registry == nil || l.entry == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "active call lease is required")
	}
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.calls[l.key.runID]
	if !ok || entry != l.entry || entry.key != l.key {
		return apperror.New(apperror.CodeConflict, "active call reservation changed before activation")
	}
	if entry.started {
		return nil
	}
	entry.started = true
	now := time.Now().UTC()
	entry.info.StartedAt = now
	entry.publicRevision = 1
	entry.publicKind = PublicModelStreamRootMessage
	entry.publicUpdatedAt = now
	entry.sequence++
	r.publishLocked(entry, activeCallEvent(entry, ActiveCallStartedEvent, 0, ""))
	return nil
}

func (l *activeCallLease) PublishPublicPreview(kind PublicModelStreamContentKind,
	text string, complete bool,
) error {
	if l == nil || l.registry == nil || l.entry == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "active call lease is required")
	}
	text = redact.String(text)
	if kind != PublicModelStreamRootMessage && kind != PublicModelStreamToolCommentary {
		return apperror.New(apperror.CodeInvalidArgument, "public model preview kind is invalid")
	}
	if len(text) > llm.MaxModelOutputBytes || !utf8.ValidString(text) {
		return apperror.New(apperror.CodeInvalidArgument, "public model preview is invalid")
	}
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.calls[l.key.runID]
	if !ok || entry != l.entry || entry.key != l.key || !entry.started {
		return apperror.New(apperror.CodeConflict, "active model call is no longer registered")
	}
	if entry.publicKind == kind && entry.publicText == text && entry.publicComplete == complete {
		return nil
	}
	entry.publicKind = kind
	entry.publicText = text
	entry.publicComplete = complete
	entry.publicRevision++
	entry.publicUpdatedAt = time.Now().UTC()
	return nil
}

// PublishItemEvent retains only a content-free live projection. TextDelta,
// ArgumentDelta, completed arguments, usage, raw wire data, and error text are
// deliberately never copied into the public bridge.
func (l *activeCallLease) PublishItemEvent(event llm.StreamEvent) error {
	if l == nil || l.registry == nil || l.entry == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "active call lease is required")
	}
	if event.Version != llm.ItemStreamProtocolVersion || event.Sequence <= 0 ||
		event.Sequence > llm.MaxItemStreamEvents || !event.Type.Valid() ||
		!event.Provisional || event.Durable {
		return apperror.New(apperror.CodeInvalidArgument, "public item stream event is invalid")
	}
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.calls[l.key.runID]
	if !ok || entry != l.entry || entry.key != l.key || !entry.started {
		return apperror.New(apperror.CodeConflict, "active call changed before item publication")
	}
	if event.Sequence != entry.eventSequence+1 {
		return apperror.New(apperror.CodeConflict, "public item stream sequence is inconsistent")
	}
	if entry.responseID == "" {
		if event.Type != llm.StreamResponseStarted || strings.TrimSpace(event.ResponseID) == "" {
			return apperror.New(apperror.CodeInvalidArgument,
				"public item stream omitted its response start")
		}
		entry.responseID = event.ResponseID
	} else if event.ResponseID != entry.responseID {
		return apperror.New(apperror.CodeConflict, "public item stream response identity changed")
	}
	entry.eventSequence = event.Sequence
	switch event.Type {
	case llm.StreamOutputItemStarted:
		if event.ItemType == llm.StreamItemMessage {
			entry.upsertPublicItem(llm.OutputItem{ResponseID: entry.responseID,
				ID: event.ItemID, Type: llm.StreamItemMessage, Status: llm.StreamItemInProgress,
				Provisional: true})
		}
	case llm.StreamToolCallStarted:
		toolName := llm.SafeStreamToolName(event.ToolName)
		entry.upsertPublicItem(llm.OutputItem{ResponseID: entry.responseID,
			ID: event.ItemID, Type: llm.StreamItemToolCall, Status: llm.StreamItemInProgress,
			CallID: event.CallID, ToolName: toolName, Provisional: true})
	case llm.StreamToolArgumentDelta:
		if item := entry.publicItems[event.ItemID]; item != nil &&
			item.Type == llm.StreamItemToolCall && item.CallID == event.CallID &&
			item.ToolName == llm.SafeStreamToolName(event.ToolName) {
			if item.ArgumentBytes > llm.MaxProviderToolPayloadSize-len(event.ArgumentDelta) {
				return apperror.New(apperror.CodeResourceExhausted,
					"public tool argument progress exceeds its limit")
			}
			item.ArgumentBytes += len(event.ArgumentDelta)
		}
	case llm.StreamToolCallCompleted:
		if item := entry.publicItems[event.ItemID]; item != nil && item.CallID == event.CallID {
			item.Status = llm.StreamItemReadyForValidation
		}
	case llm.StreamOutputItemCompleted:
		if item := entry.publicItems[event.ItemID]; item != nil {
			item.Status = llm.StreamItemCompleted
		}
	case llm.StreamResponseFailed:
		entry.failPublicItems(llm.StreamItemFailed)
	case llm.StreamResponseCancelled:
		entry.failPublicItems(llm.StreamItemCancelled)
	}
	entry.publicRevision++
	entry.publicUpdatedAt = time.Now().UTC()
	return nil
}

func (e *activeCallEntry) upsertPublicItem(item llm.OutputItem) bool {
	if e == nil || item.ID == "" || e.publicItems[item.ID] != nil {
		return false
	}
	copy := item
	e.publicItems[item.ID] = &copy
	e.publicItemOrder = append(e.publicItemOrder, item.ID)
	return true
}

func (e *activeCallEntry) failPublicItems(status llm.StreamItemStatus) bool {
	changed := false
	for _, id := range e.publicItemOrder {
		item := e.publicItems[id]
		if item != nil && item.Status != llm.StreamItemCompleted {
			item.Status = status
			changed = true
		}
	}
	return changed
}

func (l *activeCallLease) PublishProgress(deltaBytes int, totalBytes int) error {
	if l == nil || l.registry == nil || l.entry == nil {
		return apperror.New(apperror.CodeFailedPrecondition, "active call lease is required")
	}
	if deltaBytes <= 0 || totalBytes <= 0 {
		return apperror.New(apperror.CodeInvalidArgument, "active call progress bytes must be positive")
	}
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.calls[l.key.runID]
	if !ok || entry != l.entry || entry.key != l.key || !entry.started {
		return apperror.New(apperror.CodeConflict, "active model call is no longer registered")
	}
	if totalBytes != entry.info.StreamBytes+deltaBytes {
		return apperror.New(apperror.CodeConflict, "active call progress is not cumulative")
	}
	entry.info.StreamChunks++
	entry.info.StreamBytes = totalBytes
	entry.sequence++
	r.publishLocked(entry, activeCallEvent(entry, ActiveCallProgressEvent, deltaBytes, ""))
	return nil
}

func (l *activeCallLease) Finish(outcome llm.Outcome) {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.registry != nil {
			l.registry.finish(l.key, l.entry, outcome)
		}
	})
}

func (l *activeCallLease) Abort() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.registry != nil {
			l.registry.abort(l.key, l.entry)
		}
	})
}

func (l *activeCallLease) signalPersistedCancellation() bool {
	if l == nil || l.registry == nil {
		return false
	}
	_, signaled, _ := l.registry.signalCancel(l.key)
	return signaled
}

func (r *ActiveCallRegistry) cancellationTarget(runID string) (activeCallCancelTarget, bool) {
	if r == nil {
		return activeCallCancelTarget{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.calls[strings.TrimSpace(runID)]
	if !ok || !entry.started {
		return activeCallCancelTarget{}, false
	}
	return activeCallCancelTarget{key: entry.key, checkpoint: entry.checkpoint, attempt: entry.attempt, info: entry.info}, true
}

func (r *ActiveCallRegistry) signalCancel(key activeCallKey) (ActiveCallInfo, bool, bool) {
	if r == nil {
		return ActiveCallInfo{}, false, false
	}
	r.mu.Lock()
	entry, ok := r.calls[key.runID]
	if !ok || entry.key != key || !entry.started {
		r.mu.Unlock()
		return ActiveCallInfo{}, false, false
	}
	if entry.info.CancelRequested {
		info := entry.info
		r.mu.Unlock()
		return info, true, true
	}
	entry.info.CancelRequested = true
	entry.sequence++
	r.publishLocked(entry, activeCallEvent(entry, ActiveCallCancelRequestedEvent, 0, ""))
	info := entry.info
	cancel := entry.cancel
	r.mu.Unlock()
	cancel()
	return info, true, false
}

func (r *ActiveCallRegistry) finish(key activeCallKey, expected *activeCallEntry, outcome llm.Outcome) {
	r.mu.Lock()
	entry, ok := r.calls[key.runID]
	if !ok || entry != expected || entry.key != key {
		r.mu.Unlock()
		return
	}
	if !entry.started {
		delete(r.calls, key.runID)
		cancel := entry.cancel
		r.mu.Unlock()
		cancel()
		return
	}
	eventType := ActiveCallFailedEvent
	if outcome == llm.OutcomeSuccess {
		eventType = ActiveCallCompletedEvent
	} else if !outcome.Valid() {
		outcome = llm.OutcomePermanent
	}
	entry.sequence++
	r.publishLocked(entry, activeCallEvent(entry, eventType, 0, outcome))
	for id, subscriber := range entry.subscribers {
		close(subscriber.events)
		delete(entry.subscribers, id)
	}
	delete(r.calls, key.runID)
	cancel := entry.cancel
	r.mu.Unlock()
	cancel()
}

func (r *ActiveCallRegistry) abort(key activeCallKey, expected *activeCallEntry) {
	r.mu.Lock()
	entry, ok := r.calls[key.runID]
	if !ok || entry != expected || entry.key != key {
		r.mu.Unlock()
		return
	}
	for id, subscriber := range entry.subscribers {
		close(subscriber.events)
		delete(entry.subscribers, id)
	}
	delete(r.calls, key.runID)
	cancel := entry.cancel
	r.mu.Unlock()
	cancel()
}

func (r *ActiveCallRegistry) unsubscribe(key activeCallKey, subscriberID uint64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.calls[key.runID]
	if !ok || entry.key != key {
		return
	}
	subscriber, ok := entry.subscribers[subscriberID]
	if !ok {
		return
	}
	delete(entry.subscribers, subscriberID)
	close(subscriber.events)
}

func (r *ActiveCallRegistry) publishLocked(entry *activeCallEntry, event ActiveCallEvent) {
	for id, subscriber := range entry.subscribers {
		select {
		case subscriber.events <- event:
		default:
			subscriber.dropped.Store(true)
			close(subscriber.events)
			delete(entry.subscribers, id)
		}
	}
}

func activeCallEvent(entry *activeCallEntry, eventType ActiveCallEventType, deltaBytes int, outcome llm.Outcome) ActiveCallEvent {
	return ActiveCallEvent{
		Version: ActiveCallEnvelopeVersion, Sequence: entry.sequence, Type: eventType,
		Call: entry.info, DeltaBytes: deltaBytes, Outcome: outcome, CreatedAt: time.Now().UTC(),
	}
}

func publicModelStreamSnapshot(entry *activeCallEntry) PublicModelStreamSnapshot {
	items := make([]llm.OutputItem, 0, len(entry.publicItemOrder))
	for _, id := range entry.publicItemOrder {
		if item := entry.publicItems[id]; item != nil {
			items = append(items, *item)
		}
	}
	return PublicModelStreamSnapshot{
		Version: PublicModelStreamVersion, Call: entry.info,
		Revision: entry.publicRevision, ResponseID: entry.responseID,
		EventSequence: entry.eventSequence, Items: items,
		ContentKind: entry.publicKind, Text: entry.publicText,
		MessageComplete: entry.publicComplete, Provisional: true,
		UpdatedAt: entry.publicUpdatedAt,
	}
}

func sanitizeActiveCallReason(reason string) string {
	reason = redact.String(strings.TrimSpace(reason))
	if reason == "" {
		return "active model call cancellation requested"
	}
	runes := []rune(reason)
	if len(runes) > maxProtocolRepairReasonChars {
		reason = string(runes[:maxProtocolRepairReasonChars])
	}
	return reason
}

func (s *AgentRunner) ActiveCall(runID string) (ActiveCallInfo, bool) {
	if s == nil || s.activeCalls == nil {
		return ActiveCallInfo{}, false
	}
	return s.activeCalls.Lookup(runID)
}

func (s *AgentRunner) PublicModelStream(runID string) (PublicModelStreamSnapshot, bool) {
	if s == nil || s.activeCalls == nil {
		return PublicModelStreamSnapshot{}, false
	}
	return s.activeCalls.LookupPublic(runID)
}

func (s *AgentRunner) ActiveCallForSession(sessionID string) (ActiveCallInfo, bool) {
	if s == nil || s.activeCalls == nil {
		return ActiveCallInfo{}, false
	}
	return s.activeCalls.LookupSession(sessionID)
}

func (s *AgentRunner) ActiveCalls() []ActiveCallInfo {
	if s == nil || s.activeCalls == nil {
		return nil
	}
	return s.activeCalls.List()
}

func (s *AgentRunner) SubscribeActiveCall(runID string) (*ActiveCallSubscription, error) {
	if s == nil || s.activeCalls == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "active call registry is required")
	}
	return s.activeCalls.Subscribe(runID)
}

func (s *AgentRunner) CancelActiveCall(ctx context.Context, request ActiveCallCancelRequest) (ActiveCallCancelResult, error) {
	if s == nil || s.store == nil || s.activeCalls == nil {
		return ActiveCallCancelResult{}, apperror.New(apperror.CodeFailedPrecondition, "active call control dependencies are required")
	}
	if ctx == nil {
		return ActiveCallCancelResult{}, apperror.New(apperror.CodeInvalidArgument, "cancellation context is required")
	}
	runID := strings.TrimSpace(request.RunID)
	if runID == "" {
		return ActiveCallCancelResult{}, apperror.New(apperror.CodeInvalidArgument, "run id is required")
	}
	target, ok := s.activeCalls.cancellationTarget(runID)
	if !ok {
		return ActiveCallCancelResult{Found: false}, nil
	}
	reason := sanitizeActiveCallReason(request.Reason)
	_, err := s.store.RecordSupervisorModelCancelRequested(ctx, target.checkpoint, target.attempt, reason)
	if err != nil {
		return ActiveCallCancelResult{Found: true, Call: target.info}, apperror.Normalize(err)
	}
	info, signalled, alreadyRequested := s.activeCalls.signalCancel(target.key)
	alreadyRequested = alreadyRequested || target.info.CancelRequested
	if !signalled {
		info = target.info
	}
	return ActiveCallCancelResult{
		Found: true, AuditRecorded: true, Signaled: signalled,
		AlreadyRequested: alreadyRequested, Call: info,
	}, nil
}
