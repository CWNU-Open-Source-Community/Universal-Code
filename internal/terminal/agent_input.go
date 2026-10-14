package terminal

import (
	"context"
	"errors"
	"math"
	"time"

	"cyberagent-workbench/internal/executionauth"
)

const AgentInputBridgeProtocolVersion = "terminal_agent_input_bridge.v1"

var ErrAgentInputBridgeDenied = errors.New("terminal Agent input bridge denied the request")

type AgentInputBridge struct {
	manager *Manager
	broker  *executionauth.TerminalInputBroker
}

type IssueAgentInputRequest struct {
	SessionID         string
	RequestedBy       string
	OperatorConfirmed bool
	TTL               time.Duration
}

type IssuedAgentInput struct {
	ProtocolVersion     string
	Lease               executionauth.TerminalInputLease
	Token               string
	WorkspaceRootSHA256 string
}

type AgentWriteRequest struct {
	Token string
	Scope executionauth.TerminalInputScope
	Data  []byte
}

type AgentWriteResult struct {
	ProtocolVersion string
	LeaseID         string
	SessionID       string
	BytesWritten    int
}

type AgentReadRequest struct {
	Token    string
	Scope    executionauth.TerminalInputScope
	Cursor   uint64
	MaxBytes int
	// Fence is a host-only tail operation used when granting a binding or
	// starting a command. Ordinary reads, including liveness checks, omit it.
	Fence bool
}

type AgentReadResult struct {
	ProtocolVersion string
	LeaseID         string
	SessionID       string
	Backend         string
	Page            OutputPage
}

func NewAgentInputBridge(manager *Manager,
	broker *executionauth.TerminalInputBroker,
) (*AgentInputBridge, error) {
	if manager == nil || broker == nil {
		return nil, ErrTerminalBoundary
	}
	return &AgentInputBridge{manager: manager, broker: broker}, nil
}

func (b *AgentInputBridge) Issue(ctx context.Context,
	request IssueAgentInputRequest,
) (IssuedAgentInput, error) {
	if b == nil || b.manager == nil || b.broker == nil ||
		ctx == nil || ctx.Err() != nil {
		return IssuedAgentInput{}, ErrAgentInputBridgeDenied
	}
	session, err := b.manager.Get(request.SessionID)
	if err != nil || session.State != SessionRunning ||
		session.AgentInputDefault || !session.JobAssignedAtCreation ||
		b.manager.recheckAgentInputSession(ctx, session.ID) != nil ||
		!session.KillOnJobClose {
		return IssuedAgentInput{}, ErrAgentInputBridgeDenied
	}
	scope := executionauth.TerminalInputScope{
		WorkspaceID: session.Scope.WorkspaceID, RunID: session.Scope.RunID,
		TerminalSessionID:        session.ID,
		InteractionSnapshotID:    session.Scope.InteractionSnapshotID,
		InteractionRevision:      session.Scope.InteractionRevision,
		ExecutionProfileRevision: session.Scope.ExecutionProfileRevision,
		PermissionSnapshotID:     session.Scope.PermissionSnapshotID,
		PermissionRevision:       session.Scope.PermissionRevision,
		PermissionMode:           session.Scope.PermissionMode,
		Mode:                     session.Scope.Mode,
	}
	issued, err := b.broker.Issue(executionauth.IssueTerminalInputLeaseRequest{
		Scope: scope, RequestedBy: request.RequestedBy,
		OperatorConfirmed: request.OperatorConfirmed, TTL: request.TTL,
	})
	if err != nil {
		return IssuedAgentInput{}, err
	}
	return IssuedAgentInput{
		ProtocolVersion: AgentInputBridgeProtocolVersion,
		Lease:           issued.Lease, Token: issued.Token,
		WorkspaceRootSHA256: session.WorkspaceRootSHA256,
	}, nil
}

func (b *AgentInputBridge) Write(ctx context.Context,
	request AgentWriteRequest,
) (AgentWriteResult, error) {
	if b == nil || b.manager == nil || b.broker == nil ||
		ctx == nil || ctx.Err() != nil ||
		validateTerminalInput(request.Data) != nil {
		return AgentWriteResult{}, ErrAgentInputBridgeDenied
	}
	lease, err := b.broker.Authorize(request.Token, request.Scope)
	if err != nil {
		return AgentWriteResult{}, err
	}
	session, err := b.manager.Get(request.Scope.TerminalSessionID)
	if err != nil || !agentSessionMatchesScope(session, request.Scope) ||
		session.ID != lease.Scope.TerminalSessionID {
		return AgentWriteResult{}, ErrAgentInputBridgeDenied
	}
	count, err := b.manager.writeAuthorized(ctx, session.ID, request.Data)
	if err != nil {
		return AgentWriteResult{}, err
	}
	return AgentWriteResult{
		ProtocolVersion: AgentInputBridgeProtocolVersion,
		LeaseID:         lease.ID, SessionID: session.ID, BytesWritten: count,
	}, nil
}

// Read returns a bounded page of the continuously redacted Agent projection.
// Its byte cursors are independent of the user's raw terminal ring. Partial
// logical lines wait for LF/EOF; reading never consumes the user's output.
func (b *AgentInputBridge) Read(ctx context.Context,
	request AgentReadRequest,
) (AgentReadResult, error) {
	if b == nil || b.manager == nil || b.broker == nil ||
		ctx == nil || ctx.Err() != nil || request.MaxBytes < 1 ||
		request.MaxBytes > MaxTerminalOutputReadBytes ||
		(request.Fence && request.Cursor != math.MaxUint64) {
		return AgentReadResult{}, ErrAgentInputBridgeDenied
	}
	lease, err := b.broker.Authorize(request.Token, request.Scope)
	if err != nil {
		return AgentReadResult{}, err
	}
	session, err := b.manager.Get(request.Scope.TerminalSessionID)
	if err != nil || !agentSessionMatchesScope(session, request.Scope) ||
		session.ID != lease.Scope.TerminalSessionID {
		return AgentReadResult{}, ErrAgentInputBridgeDenied
	}
	if err := b.manager.recheckAgentInputSession(ctx, session.ID); err != nil {
		return AgentReadResult{}, ErrAgentInputBridgeDenied
	}
	page, err := b.manager.readAgentOutput(session.ID, request.Cursor, request.MaxBytes, request.Fence)
	if err != nil {
		return AgentReadResult{}, err
	}
	return AgentReadResult{
		ProtocolVersion: AgentInputBridgeProtocolVersion,
		LeaseID:         lease.ID, SessionID: session.ID, Backend: session.Backend,
		Page: page,
	}, nil
}

func agentSessionMatchesScope(session Session,
	scope executionauth.TerminalInputScope,
) bool {
	return session.State == SessionRunning &&
		session.Scope.WorkspaceID == scope.WorkspaceID &&
		session.Scope.RunID == scope.RunID &&
		session.Scope.InteractionSnapshotID == scope.InteractionSnapshotID &&
		session.Scope.InteractionRevision == scope.InteractionRevision &&
		session.Scope.ExecutionProfileRevision == scope.ExecutionProfileRevision &&
		session.Scope.PermissionSnapshotID == scope.PermissionSnapshotID &&
		session.Scope.PermissionRevision == scope.PermissionRevision &&
		session.Scope.PermissionMode == scope.PermissionMode &&
		session.Scope.Mode == scope.Mode
}

func (b *AgentInputBridge) Revoke(leaseID string, requestedBy string,
	operatorConfirmed bool,
) (executionauth.TerminalInputLease, error) {
	if b == nil || b.broker == nil {
		return executionauth.TerminalInputLease{}, ErrAgentInputBridgeDenied
	}
	return b.broker.Revoke(leaseID, requestedBy, operatorConfirmed)
}

// The bridge cannot attach a new activation to a terminal from an old one.
// Issue and Read use the same original authorization ref as native writes.
func (m *Manager) recheckAgentInputSession(ctx context.Context, sessionID string) error {
	entry := m.entry(sessionID)
	if ctx == nil || entry == nil || entry.authorizer == nil {
		return ErrAgentInputBridgeDenied
	}
	return entry.authorizer.Recheck(ctx, entry.subject, entry.startOperation, "", entry.authorizationRef)
}
