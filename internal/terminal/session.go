package terminal

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/outputsafe"
	"cyberagent-workbench/internal/toolcontract"
)

const (
	SessionProtocolVersion     = "user_terminal_session.v1"
	SessionPolicyVersion       = "user_terminal_policy.v1"
	DefaultColumns             = 120
	DefaultRows                = 32
	MinColumns                 = 20
	MaxColumns                 = 300
	MinRows                    = 5
	MaxRows                    = 120
	MaxSessions                = 8
	MaxTerminalInputBytes      = 16 * 1024
	MaxTerminalOutputBytes     = 4 * 1024 * 1024
	MaxTerminalOutputReadBytes = 64 * 1024
)

var (
	ErrTerminalBoundary    = errors.New("terminal session boundary is invalid")
	ErrTerminalDenied      = errors.New("terminal session operation is denied")
	ErrTerminalUnavailable = errors.New("terminal backend is unavailable")
	ErrTerminalClosed      = errors.New("terminal session is closed")
)

type SessionState string

const (
	SessionStarting SessionState = "starting"
	SessionRunning  SessionState = "running"
	SessionExited   SessionState = "exited"
	SessionClosed   SessionState = "closed"
	SessionFailed   SessionState = "failed"
)

type SessionScope struct {
	WorkspaceID              string
	RunID                    string
	InteractionSnapshotID    string
	InteractionRevision      int64
	ExecutionProfileRevision int64
	PermissionSnapshotID     string
	PermissionRevision       int64
	PermissionMode           domain.RunExecutionPermissionMode
	Mode                     domain.RunExecutionInteractionMode
}

func (s SessionScope) Validate() error {
	if !domain.ValidAgentID(s.WorkspaceID) ||
		!domain.ValidAgentID(s.RunID) ||
		!domain.ValidAgentID(s.InteractionSnapshotID) ||
		!domain.ValidAgentID(s.PermissionSnapshotID) ||
		s.InteractionRevision <= 0 ||
		s.ExecutionProfileRevision <= 0 ||
		s.PermissionRevision <= 0 ||
		(s.PermissionMode != domain.RunExecutionPermissionFull &&
			s.PermissionMode != domain.RunExecutionPermissionDebug) {
		return ErrTerminalBoundary
	}
	switch s.Mode {
	case domain.RunExecutionInteractionDebug,
		domain.RunExecutionInteractionCyber:
		return nil
	default:
		return ErrTerminalBoundary
	}
}

type StartRequest struct {
	ID                string
	Scope             SessionScope
	WorkspaceRoot     string
	Interaction       domain.RunExecutionInteractionSnapshot
	CurrentProfile    domain.RunExecutionProfileSnapshot
	CurrentPermission domain.RunExecutionPermissionSnapshot
	// Authorizer is injected by the host and re-reads native bindings. It is
	// never serialized, persisted, or supplied by a plugin or renderer.
	Authorizer        *executionauth.PolicyAuthorizer `json:"-"`
	Columns           int
	Rows              int
	RequestedBy       string
	OperatorConfirmed bool
	ReplaceExisting   bool
}

type BackendStartRequest struct {
	SessionID     string
	WorkspaceRoot string
	Columns       int
	Rows          int
}

type ProcessBoundary struct {
	UserOwned             bool
	AgentInputDefault     bool
	JobAssignedAtCreation bool
	KillOnJobClose        bool
	Persistent            bool
}

func (b ProcessBoundary) Validate() error {
	if !b.UserOwned || b.AgentInputDefault || !b.JobAssignedAtCreation ||
		!b.KillOnJobClose || !b.Persistent {
		return ErrTerminalBoundary
	}
	return nil
}

type Process interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	Resize(columns int, rows int) error
	Wait(context.Context) (int, error)
	Close() error
	Boundary() ProcessBoundary
}

type Backend interface {
	Name() string
	Available() bool
	Start(context.Context, BackendStartRequest) (Process, error)
}

type LeaseRevoker interface {
	RevokeTerminal(string) int
	RevokeRun(string) int
	RevokeWorkspace(string) int
	RevokeAll() int
}

type Session struct {
	ID                       string
	ProtocolVersion          string
	PolicyVersion            string
	Scope                    SessionScope
	WorkspaceRootSHA256      string
	Backend                  string
	State                    SessionState
	Columns                  int
	Rows                     int
	CreatedAt                time.Time
	ExitedAt                 time.Time
	ExitCode                 int
	OutputBaseCursor         uint64
	OutputNextCursor         uint64
	UserOwned                bool
	AgentInputDefault        bool
	JobAssignedAtCreation    bool
	KillOnJobClose           bool
	Persistent               bool
	ProcessLocal             bool
	RawOutputPersisted       bool
	EnvironmentPersisted     bool
	ProcessIdentityPublished bool
}

func (s Session) Validate() error {
	if !domain.ValidAgentID(s.ID) ||
		s.ProtocolVersion != SessionProtocolVersion ||
		s.PolicyVersion != SessionPolicyVersion ||
		s.Scope.Validate() != nil || !validTerminalBackendName(s.Backend) ||
		!validTerminalState(s.State) ||
		s.Columns < MinColumns || s.Columns > MaxColumns ||
		s.Rows < MinRows || s.Rows > MaxRows || s.CreatedAt.IsZero() ||
		!validWorkspaceRootSHA256(s.WorkspaceRootSHA256) ||
		s.OutputNextCursor < s.OutputBaseCursor ||
		!s.UserOwned || s.AgentInputDefault || !s.JobAssignedAtCreation ||
		!s.KillOnJobClose || !s.Persistent || !s.ProcessLocal ||
		s.RawOutputPersisted || s.EnvironmentPersisted ||
		s.ProcessIdentityPublished {
		return ErrTerminalBoundary
	}
	if s.State == SessionExited && s.ExitedAt.IsZero() {
		return ErrTerminalBoundary
	}
	return nil
}

type OutputPage struct {
	SessionID  string
	BaseCursor uint64
	NextCursor uint64
	Data       []byte
	Dropped    bool
	State      SessionState
}

type UserInputRequest struct {
	SessionID     string
	Data          []byte
	RequestedBy   string
	UserConfirmed bool
}

type sessionEntry struct {
	mu               sync.Mutex
	value            Session
	process          Process
	cancel           context.CancelFunc
	output           outputRing
	agentOutput      outputRing
	agentSanitizer   outputsafe.RedactingStream
	authorizer       *executionauth.PolicyAuthorizer
	subject          executionauth.SubjectRef
	startOperation   toolcontract.Operation
	authorizationRef string
}

type Manager struct {
	mu           sync.RWMutex
	backend      Backend
	revoker      LeaseRevoker
	sessions     map[string]*sessionEntry
	byRun        map[string]string
	now          func() time.Time
	operationKey [32]byte
}

func NewManager(backend Backend, revoker LeaseRevoker) (*Manager, error) {
	if backend == nil || !validTerminalBackendName(backend.Name()) {
		return nil, ErrTerminalBoundary
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, err
	}
	return &Manager{
		backend: backend, revoker: revoker, operationKey: key,
		sessions: make(map[string]*sessionEntry),
		byRun:    make(map[string]string), now: time.Now,
	}, nil
}

func NewPlatformManager(revoker LeaseRevoker) (*Manager, error) {
	return NewManager(newPlatformBackend(), revoker)
}

func (m *Manager) Available() bool {
	return m != nil && m.backend != nil && m.backend.Available()
}

func (m *Manager) Start(ctx context.Context, request StartRequest) (Session, error) {
	if m == nil || m.backend == nil || !m.backend.Available() {
		return Session{}, ErrTerminalUnavailable
	}
	if ctx == nil {
		return Session{}, ErrTerminalBoundary
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	request.Columns, request.Rows = normalizeSize(request.Columns, request.Rows)
	if err := validateStartRequest(request); err != nil {
		return Session{}, err
	}
	workspaceRootSHA256, err := WorkspaceRootSHA256(request.WorkspaceRoot)
	if err != nil {
		return Session{}, err
	}
	subject := executionauth.SubjectRef{RunID: request.Scope.RunID, ActorID: request.RequestedBy}
	operation, err := m.operation(request.ID, request.Scope, workspaceRootSHA256,
		"start", request.Columns, request.Rows, nil)
	if err != nil {
		return Session{}, err
	}
	decision, err := request.Authorizer.Authorize(ctx, subject, operation, "")
	if err != nil || decision.Outcome != "allow" || decision.Validate() != nil {
		return Session{}, fmt.Errorf("%w: start authorization failed: %v", ErrTerminalDenied, err)
	}
	if existingID := m.runSessionID(request.Scope.RunID); existingID != "" {
		if !request.ReplaceExisting {
			return Session{}, fmt.Errorf("%w: Run already has a terminal",
				ErrTerminalDenied)
		}
		if err := m.Close(existingID, request.RequestedBy, true); err != nil {
			return Session{}, err
		}
	}
	m.mu.RLock()
	sessionCount := m.activeSessionCountLocked()
	m.mu.RUnlock()
	if sessionCount >= MaxSessions {
		return Session{}, fmt.Errorf("%w: terminal session limit reached",
			ErrTerminalDenied)
	}
	fingerprint, err := toolcontract.FingerprintOperation(operation)
	if err != nil {
		return Session{}, err
	}
	if err := decision.BeforeDispatch(ctx, fingerprint); err != nil {
		return Session{}, fmt.Errorf("%w: %v", ErrTerminalDenied, err)
	}
	process, err := m.backend.Start(ctx, BackendStartRequest{
		SessionID: request.ID, WorkspaceRoot: request.WorkspaceRoot,
		Columns: request.Columns, Rows: request.Rows,
	})
	if err != nil {
		return Session{}, err
	}
	// Revoke a process admitted concurrently with binding loss before publishing
	// the session. An already-started effect is not described as rolled back.
	if err := request.Authorizer.Recheck(ctx, subject, operation, "", decision.AuthorizationRef); err != nil {
		_ = process.Close()
		return Session{}, fmt.Errorf("%w: %v", ErrTerminalDenied, err)
	}
	boundary := process.Boundary()
	if err := boundary.Validate(); err != nil {
		_ = process.Close()
		return Session{}, err
	}
	processContext, cancel := context.WithCancel(context.Background())
	value := Session{
		ID: request.ID, ProtocolVersion: SessionProtocolVersion,
		PolicyVersion: SessionPolicyVersion, Scope: request.Scope,
		WorkspaceRootSHA256: workspaceRootSHA256,
		Backend:             m.backend.Name(), State: SessionRunning,
		Columns: request.Columns, Rows: request.Rows,
		CreatedAt: m.now().UTC(), UserOwned: boundary.UserOwned,
		AgentInputDefault:     boundary.AgentInputDefault,
		JobAssignedAtCreation: boundary.JobAssignedAtCreation,
		KillOnJobClose:        boundary.KillOnJobClose, Persistent: boundary.Persistent,
		ProcessLocal: true,
	}
	entry := &sessionEntry{value: value, process: process, cancel: cancel,
		authorizer: request.Authorizer, subject: subject,
		startOperation: operation, authorizationRef: decision.AuthorizationRef,
	}
	if err := entry.value.Validate(); err != nil {
		cancel()
		_ = process.Close()
		return Session{}, err
	}
	m.mu.Lock()
	if m.activeSessionCountLocked() >= MaxSessions ||
		m.byRun[request.Scope.RunID] != "" {
		m.mu.Unlock()
		cancel()
		_ = process.Close()
		return Session{}, ErrTerminalDenied
	}
	m.sessions[value.ID] = entry
	m.byRun[value.Scope.RunID] = value.ID
	m.mu.Unlock()
	go m.readLoop(entry)
	go m.waitLoop(processContext, entry)
	return value, nil
}

// WorkspaceRootSHA256 binds process-local terminal state to the canonical
// root without exposing the host path to renderer or model projections.
func WorkspaceRootSHA256(root string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		strings.ContainsRune(root, 0) || !utf8.ValidString(root) {
		return "", ErrTerminalBoundary
	}
	digest := sha256.Sum256([]byte(root))
	return hex.EncodeToString(digest[:]), nil
}

func validWorkspaceRootSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func (m *Manager) Get(sessionID string) (Session, error) {
	entry := m.entry(sessionID)
	if entry == nil {
		return Session{}, ErrTerminalClosed
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	value := entry.value
	value.OutputBaseCursor = entry.output.base
	value.OutputNextCursor = entry.output.next()
	return value, value.Validate()
}

func (m *Manager) ActiveSessions() []Session {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	entries := make([]*sessionEntry, 0, len(m.sessions))
	for _, entry := range m.sessions {
		entries = append(entries, entry)
	}
	m.mu.RUnlock()
	sessions := make([]Session, 0, len(entries))
	for _, entry := range entries {
		entry.mu.Lock()
		value := entry.value
		value.OutputBaseCursor = entry.output.base
		value.OutputNextCursor = entry.output.next()
		entry.mu.Unlock()
		if value.State == SessionRunning && value.Validate() == nil {
			sessions = append(sessions, value)
		}
	}
	return sessions
}

func (m *Manager) Read(sessionID string, cursor uint64,
	maxBytes int,
) (OutputPage, error) {
	entry := m.entry(sessionID)
	if entry == nil {
		return OutputPage{}, ErrTerminalClosed
	}
	if maxBytes == 0 {
		maxBytes = MaxTerminalOutputReadBytes
	}
	if maxBytes < 1 || maxBytes > MaxTerminalOutputReadBytes {
		return OutputPage{}, ErrTerminalBoundary
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	data, base, next, dropped := entry.output.read(cursor, maxBytes)
	return OutputPage{
		SessionID: sessionID, BaseCursor: base, NextCursor: next,
		Data: data, Dropped: dropped, State: entry.value.State,
	}, nil
}

// Agent cursors address a separate, already redacted byte stream. Only an
// explicit grant/command fence suppresses pending output; tail reads used to
// check a lease's liveness leave the parser and its pending line untouched.
func (m *Manager) readAgentOutput(sessionID string, cursor uint64,
	maxBytes int, fence bool,
) (OutputPage, error) {
	entry := m.entry(sessionID)
	if entry == nil {
		return OutputPage{}, ErrTerminalClosed
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if fence {
		entry.agentSanitizer.DiscardPending()
	}
	data, base, next, dropped := entry.agentOutput.read(cursor, maxBytes)
	return OutputPage{
		SessionID: sessionID, BaseCursor: base, NextCursor: next,
		Data: data, Dropped: dropped, State: entry.value.State,
	}, nil
}

func (m *Manager) WriteUser(ctx context.Context,
	request UserInputRequest,
) (int, error) {
	if ctx == nil || ctx.Err() != nil || !request.UserConfirmed ||
		!validTerminalOperator(request.RequestedBy) {
		return 0, ErrTerminalDenied
	}
	if err := validateTerminalInput(request.Data); err != nil {
		return 0, err
	}
	return m.write(ctx, request.SessionID, request.Data)
}

func (m *Manager) Resize(sessionID string, columns int, rows int,
	requestedBy string, userConfirmed bool,
) error {
	if !userConfirmed || !validTerminalOperator(requestedBy) ||
		columns < MinColumns || columns > MaxColumns ||
		rows < MinRows || rows > MaxRows {
		return ErrTerminalDenied
	}
	entry := m.entry(sessionID)
	if entry == nil {
		return ErrTerminalClosed
	}
	entry.mu.Lock()
	if entry.value.State != SessionRunning {
		entry.mu.Unlock()
		return ErrTerminalClosed
	}
	process := entry.process
	entry.mu.Unlock()
	if err := m.authorizeMutation(context.Background(), entry, "resize", columns, rows, nil); err != nil {
		_ = m.CloseForBindingInvalidation(sessionID)
		return err
	}
	if err := process.Resize(columns, rows); err != nil {
		return err
	}
	entry.mu.Lock()
	entry.value.Columns = columns
	entry.value.Rows = rows
	entry.mu.Unlock()
	return nil
}

func (m *Manager) Close(sessionID string, requestedBy string,
	userConfirmed bool,
) error {
	if !userConfirmed || !validTerminalOperator(requestedBy) {
		return ErrTerminalDenied
	}
	return m.closeSession(sessionID)
}

func (m *Manager) closeSession(sessionID string) error {
	entry := m.entry(sessionID)
	if entry == nil {
		return ErrTerminalClosed
	}
	m.mu.Lock()
	delete(m.sessions, sessionID)
	if m.byRun[entry.value.Scope.RunID] == sessionID {
		delete(m.byRun, entry.value.Scope.RunID)
	}
	m.mu.Unlock()
	entry.mu.Lock()
	if entry.value.State != SessionClosed {
		entry.value.State = SessionClosed
	}
	cancel := entry.cancel
	process := entry.process
	entry.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if m.revoker != nil {
		m.revoker.RevokeTerminal(sessionID)
	}
	return process.Close()
}

func (m *Manager) CloseRun(runID string, requestedBy string,
	userConfirmed bool,
) error {
	if !userConfirmed || !validTerminalOperator(requestedBy) {
		return ErrTerminalDenied
	}
	sessionID := m.runSessionID(strings.TrimSpace(runID))
	if m.revoker != nil {
		m.revoker.RevokeRun(runID)
	}
	if sessionID == "" {
		return nil
	}
	return m.closeSession(sessionID)
}

// CloseForBindingInvalidation is the non-user path used when the durable Run
// binding that authorized a terminal is no longer current. It cannot start,
// retarget, resize, or write to a terminal.
func (m *Manager) CloseForBindingInvalidation(sessionID string) error {
	if m == nil || !domain.ValidAgentID(strings.TrimSpace(sessionID)) {
		return ErrTerminalBoundary
	}
	err := m.closeSession(sessionID)
	if errors.Is(err, ErrTerminalClosed) {
		return nil
	}
	return err
}

// CloseForRunTermination revokes every input lease for the Run before closing
// its user-owned terminal. It is deliberately separate from user confirmation.
func (m *Manager) CloseForRunTermination(runID string) error {
	runID = strings.TrimSpace(runID)
	if m == nil || !domain.ValidAgentID(runID) {
		return ErrTerminalBoundary
	}
	if m.revoker != nil {
		m.revoker.RevokeRun(runID)
	}
	sessionID := m.runSessionID(runID)
	if sessionID == "" {
		return nil
	}
	err := m.closeSession(sessionID)
	if errors.Is(err, ErrTerminalClosed) {
		return nil
	}
	return err
}

func (m *Manager) RevokeForWorkspaceSwitch(previousWorkspaceID string) int {
	if m == nil || m.revoker == nil {
		return 0
	}
	return m.revoker.RevokeWorkspace(previousWorkspaceID)
}

func (m *Manager) RevokeForLockOrSleep() int {
	if m == nil || m.revoker == nil {
		return 0
	}
	return m.revoker.RevokeAll()
}

func (m *Manager) Shutdown() error {
	if m == nil {
		return nil
	}
	if m.revoker != nil {
		m.revoker.RevokeAll()
	}
	m.mu.RLock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	var result error
	for _, id := range ids {
		result = errors.Join(result, m.Close(id, "system_shutdown", true))
	}
	return result
}

func (m *Manager) writeAuthorized(ctx context.Context, sessionID string, data []byte) (int, error) {
	if err := validateTerminalInput(data); err != nil {
		return 0, err
	}
	return m.write(ctx, sessionID, data)
}

func (m *Manager) write(ctx context.Context, sessionID string, data []byte) (int, error) {
	// Keep authorization and dispatch bound to the same immutable bytes.
	data = append([]byte(nil), data...)
	entry := m.entry(sessionID)
	if entry == nil {
		return 0, ErrTerminalClosed
	}
	entry.mu.Lock()
	if entry.value.State != SessionRunning {
		entry.mu.Unlock()
		return 0, ErrTerminalClosed
	}
	process := entry.process
	entry.mu.Unlock()
	if err := m.authorizeMutation(ctx, entry, "write", 0, 0, data); err != nil {
		// Cancelling one input request is not revocation of the user-owned
		// session. Live binding loss is also checked by reconciliation.
		if ctx.Err() == nil {
			_ = m.CloseForBindingInvalidation(sessionID)
		}
		return 0, err
	}
	return process.Write(data)
}

// ReconcileBindings closes sessions whose original live host authorization is
// no longer current, including runtime generation changes with identical data.
// It cannot mint replacement authority from a durable snapshot.
func (m *Manager) ReconcileBindings(ctx context.Context) int {
	if m == nil || ctx == nil || ctx.Err() != nil {
		return 0
	}
	closed := 0
	for _, session := range m.ActiveSessions() {
		if ctx.Err() != nil {
			break
		}
		entry := m.entry(session.ID)
		if entry == nil {
			continue
		}
		if entry.authorizer == nil ||
			entry.authorizer.Recheck(ctx, entry.subject, entry.startOperation, "", entry.authorizationRef) != nil {
			if ctx.Err() != nil {
				break
			}
			_ = m.CloseForBindingInvalidation(session.ID)
			closed++
		}
	}
	return closed
}

func (m *Manager) authorizeMutation(ctx context.Context, entry *sessionEntry,
	action string, columns, rows int, data []byte,
) error {
	if ctx == nil || entry.authorizer == nil {
		return ErrTerminalDenied
	}
	if err := entry.authorizer.Recheck(ctx, entry.subject, entry.startOperation, "", entry.authorizationRef); err != nil {
		return fmt.Errorf("%w: %v", ErrTerminalDenied, err)
	}
	operation, err := m.operation(entry.value.ID, entry.value.Scope,
		entry.value.WorkspaceRootSHA256, action, columns, rows, data)
	if err != nil {
		return err
	}
	decision, err := entry.authorizer.Authorize(ctx, entry.subject, operation, "")
	if err != nil || decision.Outcome != "allow" || decision.Validate() != nil {
		return fmt.Errorf("%w: terminal operation authorization failed: %v", ErrTerminalDenied, err)
	}
	fingerprint, err := toolcontract.FingerprintOperation(operation)
	if err != nil {
		return err
	}
	if err := decision.BeforeDispatch(ctx, fingerprint); err != nil {
		return fmt.Errorf("%w: %v", ErrTerminalDenied, err)
	}
	return nil
}

func (m *Manager) operation(sessionID string, scope SessionScope, rootSHA256,
	action string, columns, rows int, data []byte,
) (toolcontract.Operation, error) {
	raw, err := json.Marshal(struct {
		SessionID     string
		Scope         SessionScope
		RootSHA256    string
		Action        string
		Columns, Rows int
		Data          []byte
	}{sessionID, scope, rootSHA256, action, columns, rows, data})
	if err != nil {
		return toolcontract.Operation{}, err
	}
	// Terminal bytes can contain secrets. Only a process-keyed HMAC enters the
	// common operation contract; neither bytes nor the key are persisted.
	mac := hmac.New(sha256.New, m.operationKey[:])
	_, _ = mac.Write(raw)
	operation := toolcontract.Operation{
		ID: sessionID, Kind: toolcontract.OperationProcess, ToolID: "user_terminal",
		Component: toolcontract.ComponentRef{PackageID: "builtin", ComponentID: "user_terminal"},
		AdapterID: m.backend.Name(), AdapterRevision: SessionPolicyVersion,
		InputFingerprint: hex.EncodeToString(mac.Sum(nil)),
		Targets: []toolcontract.Target{{Kind: "process", Locator: sessionID},
			{Kind: "directory", Locator: "workspace:" + scope.WorkspaceID + ":" + rootSHA256}},
		Effects: []toolcontract.Effect{toolcontract.EffectProcess, toolcontract.EffectOutside, toolcontract.EffectUnknown},
	}
	return operation, operation.Validate()
}

func (m *Manager) entry(sessionID string) *sessionEntry {
	if m == nil || !domain.ValidAgentID(strings.TrimSpace(sessionID)) {
		return nil
	}
	m.mu.RLock()
	entry := m.sessions[sessionID]
	m.mu.RUnlock()
	return entry
}

func (m *Manager) runSessionID(runID string) string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	id := m.byRun[runID]
	m.mu.RUnlock()
	return id
}

func (m *Manager) activeSessionCountLocked() int {
	count := 0
	for _, entry := range m.sessions {
		entry.mu.Lock()
		if entry.value.State == SessionRunning {
			count++
		}
		entry.mu.Unlock()
	}
	return count
}

func (m *Manager) readLoop(entry *sessionEntry) {
	buffer := make([]byte, 32*1024)
	for {
		count, err := entry.process.Read(buffer)
		if count > 0 {
			entry.mu.Lock()
			entry.output.append(buffer[:count])
			entry.agentOutput.append([]byte(entry.agentSanitizer.Feed(buffer[:count])))
			entry.mu.Unlock()
		}
		if err != nil {
			entry.mu.Lock()
			entry.agentOutput.append([]byte(entry.agentSanitizer.Flush()))
			entry.mu.Unlock()
			return
		}
	}
}

func (m *Manager) waitLoop(ctx context.Context, entry *sessionEntry) {
	exitCode, err := entry.process.Wait(ctx)
	entry.mu.Lock()
	if entry.value.State == SessionRunning {
		if err == nil {
			entry.value.State = SessionExited
			entry.value.ExitCode = exitCode
			entry.value.ExitedAt = m.now().UTC()
		} else if !errors.Is(err, context.Canceled) {
			entry.value.State = SessionFailed
		}
	}
	sessionID := entry.value.ID
	entry.mu.Unlock()
	if m.revoker != nil {
		m.revoker.RevokeTerminal(sessionID)
	}
	_ = entry.process.Close()
}

type outputRing struct {
	base uint64
	data []byte
}

func (r *outputRing) append(data []byte) {
	if len(data) == 0 {
		return
	}
	if len(data) >= MaxTerminalOutputBytes {
		r.base += uint64(len(r.data) + len(data) - MaxTerminalOutputBytes)
		r.data = append(r.data[:0],
			data[len(data)-MaxTerminalOutputBytes:]...)
		return
	}
	overflow := len(r.data) + len(data) - MaxTerminalOutputBytes
	if overflow > 0 {
		copy(r.data, r.data[overflow:])
		r.data = r.data[:len(r.data)-overflow]
		r.base += uint64(overflow)
	}
	r.data = append(r.data, data...)
}

func (r *outputRing) next() uint64 {
	return r.base + uint64(len(r.data))
}

func (r *outputRing) read(cursor uint64, maxBytes int) (
	[]byte, uint64, uint64, bool,
) {
	next := r.next()
	if cursor == 0 {
		cursor = r.base
	}
	dropped := cursor < r.base
	if dropped {
		cursor = r.base
	}
	if cursor > next {
		cursor = next
	}
	offset := int(cursor - r.base)
	end := offset + maxBytes
	if end > len(r.data) {
		end = len(r.data)
	}
	data := append([]byte(nil), r.data[offset:end]...)
	return data, r.base, cursor + uint64(len(data)), dropped
}

func validateStartRequest(request StartRequest) error {
	if !domain.ValidAgentID(request.ID) || request.Scope.Validate() != nil ||
		!filepath.IsAbs(request.WorkspaceRoot) ||
		filepath.Clean(request.WorkspaceRoot) != request.WorkspaceRoot ||
		strings.ContainsRune(request.WorkspaceRoot, 0) ||
		!request.OperatorConfirmed || request.Authorizer == nil ||
		!validTerminalOperator(request.RequestedBy) ||
		request.Columns < MinColumns || request.Columns > MaxColumns ||
		request.Rows < MinRows || request.Rows > MaxRows ||
		request.Interaction.Validate() != nil ||
		request.CurrentProfile.Validate() != nil ||
		request.CurrentPermission.Validate() != nil {
		return ErrTerminalBoundary
	}
	if request.Scope.RunID != request.Interaction.RunID ||
		request.Scope.InteractionSnapshotID != request.Interaction.ID ||
		request.Scope.InteractionRevision != request.Interaction.Revision ||
		request.Scope.ExecutionProfileRevision != request.CurrentProfile.Revision ||
		request.Scope.PermissionSnapshotID != request.CurrentPermission.ID ||
		request.Scope.PermissionRevision != request.CurrentPermission.Revision ||
		request.Scope.PermissionMode != request.CurrentPermission.Mode ||
		request.Scope.Mode != request.Interaction.Mode ||
		request.Interaction.WorkspaceTrust != domain.WorkspaceTrustTrusted ||
		request.Interaction.AgentInputDefault ||
		request.Interaction.ProcessEnabled ||
		request.Interaction.ExecutionAuthorized ||
		request.Interaction.CapabilityGrant ||
		request.CurrentProfile.RunID != request.Interaction.RunID ||
		request.CurrentProfile.MissionID != request.Interaction.MissionID ||
		request.CurrentProfile.Revision !=
			request.Interaction.ExecutionProfileRevision ||
		request.CurrentPermission.RunID != request.Interaction.RunID ||
		request.CurrentPermission.MissionID != request.Interaction.MissionID ||
		request.CurrentPermission.ProtocolVersion != domain.RunApprovalPermissionProtocolVersion ||
		request.CurrentPermission.Mode != domain.RunExecutionPermissionFull {
		return ErrTerminalBoundary
	}
	switch request.Scope.Mode {
	case domain.RunExecutionInteractionDebug:
		if request.Interaction.Surface != domain.ExecutionSurfaceCode ||
			request.Interaction.ExecutionProfile != domain.RunExecutionProfileLocal ||
			request.Interaction.CommandForm != domain.ExecutionCommandUserConPTY ||
			!request.Interaction.PersistentTerminal ||
			request.CurrentProfile.Profile != domain.RunExecutionProfileLocal {
			return ErrTerminalBoundary
		}
	case domain.RunExecutionInteractionCyber:
		if request.Interaction.Surface != domain.ExecutionSurfaceCyber ||
			request.Interaction.ExecutionProfile != domain.RunExecutionProfileDocker ||
			request.Interaction.CommandForm != domain.ExecutionCommandContainerPTY ||
			!request.Interaction.PersistentTerminal ||
			request.CurrentProfile.Profile != domain.RunExecutionProfileDocker {
			return ErrTerminalBoundary
		}
		return fmt.Errorf("%w: Cyber terminal requires the Docker backend",
			ErrTerminalUnavailable)
	default:
		return ErrTerminalBoundary
	}
	return nil
}

func validateTerminalInput(data []byte) error {
	if len(data) == 0 || len(data) > MaxTerminalInputBytes ||
		!utf8.Valid(data) {
		return ErrTerminalBoundary
	}
	return nil
}

func normalizeSize(columns int, rows int) (int, int) {
	if columns == 0 {
		columns = DefaultColumns
	}
	if rows == 0 {
		rows = DefaultRows
	}
	return columns, rows
}

func validTerminalState(state SessionState) bool {
	switch state {
	case SessionStarting, SessionRunning, SessionExited, SessionClosed,
		SessionFailed:
		return true
	default:
		return false
	}
}

func validTerminalBackendName(value string) bool {
	return value != "" && value == strings.TrimSpace(value) &&
		utf8.ValidString(value) && !strings.ContainsRune(value, 0) &&
		len([]rune(value)) <= 128
}

func validTerminalOperator(value string) bool {
	value = strings.TrimSpace(value)
	if !domain.ValidAgentID(value) {
		return false
	}
	switch strings.ToLower(value) {
	case "agent", "llm", "model", "repository", "repo", "skill":
		return false
	default:
		return true
	}
}
