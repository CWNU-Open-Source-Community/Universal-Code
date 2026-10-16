package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/toolcontract"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

var errStaleCatalog = apperror.New(apperror.CodeConflict, "MCP catalog changed; obtain the current catalog and prepare a new invocation")

func staleCatalogError() error { return errStaleCatalog }

type catalogSessionKey struct{ run, workspace, server string }
type catalogRefreshFailure struct{ cause error }
type catalogStoreFailure struct{ cause error }

func (e *catalogStoreFailure) Error() string { return e.cause.Error() }
func (e *catalogStoreFailure) Unwrap() error { return e.cause }
func catalogStoreError(err error) error {
	if err == nil || apperror.CodeOf(err) == apperror.CodeConflict {
		return err
	}
	return &catalogStoreFailure{cause: err}
}

func (e *catalogRefreshFailure) Error() string { return e.cause.Error() }
func (e *catalogRefreshFailure) Unwrap() error { return e.cause }

type catalogFault struct {
	client *Client
	cause  error
}

// Each task/server owns one serial SDK connection. The lease covers discovery
// and invocation so guards and input handlers never cross operations.
type catalogSession struct {
	lease       chan struct{}
	key         []byte
	client      *Client // accessed only while holding lease
	fingerprint string
	lifetime    context.Context
	stop        context.CancelFunc
	dirty       chan struct{}
	fault       chan catalogFault
	checkMu     sync.RWMutex
	check       func(context.Context) error
	revoked     <-chan struct{}
	changed     chan struct{}
	done        chan struct{}
}

func (m *Manager) session(key catalogSessionKey) (*catalogSession, error) {
	m.sessionsMu.Lock()
	defer m.sessionsMu.Unlock()
	if m.closed {
		return nil, errors.New("MCP manager is closed")
	}
	if s := m.sessions[key]; s != nil && s.lifetime.Err() == nil {
		return s, nil
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &catalogSession{lease: make(chan struct{}, 1), key: secret, lifetime: ctx, stop: cancel, dirty: make(chan struct{}, 1), fault: make(chan catalogFault, 1), changed: make(chan struct{}, 1), done: make(chan struct{})}
	s.lease <- struct{}{}
	m.sessions[key] = s
	go m.watchCatalog(key, s)
	return s, nil
}

func (s *catalogSession) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.lifetime.Done():
		return errors.New("MCP task session is closed")
	case <-s.lease:
		if s.lifetime.Err() != nil {
			s.lease <- struct{}{}
			return errors.New("MCP task session is closed")
		}
		return nil
	}
}
func (s *catalogSession) release() { s.lease <- struct{}{} }
func (s *catalogSession) signal() {
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}
func (s *catalogSession) boundTaskCheck() func(context.Context) error {
	s.checkMu.RLock()
	check := s.check
	s.checkMu.RUnlock()
	return func(ctx context.Context) error {
		if check == nil {
			return errors.New("MCP catalog has no current task authority")
		}
		if err := s.lifetime.Err(); err != nil {
			return err
		}
		return check(ctx)
	}
}

// RefreshCapabilities is the host's once-per-model-turn catalog entry point.
// check must re-read current task authority, and must not capture a one-shot tool
// approval. Searches can use Capabilities without reconnecting every page.
func (m *Manager) RefreshCapabilities(ctx context.Context, runID, workspaceID string, check func(context.Context) error, revoked <-chan struct{}) (ScopedCapabilities, error) {
	if !validClientIdentity(runID) || !validClientIdentity(workspaceID) || check == nil {
		return ScopedCapabilities{}, errors.New("MCP catalog task scope is invalid")
	}
	if err := check(ctx); err != nil {
		m.CloseRun(runID)
		return ScopedCapabilities{}, err
	}
	if err := m.EnsureComputerUse(ctx, workspaceID); err != nil {
		return ScopedCapabilities{}, err
	}
	records, err := m.store.ListMCPClientServers(ctx, runID, workspaceID, MaxClientServers)
	if err != nil {
		return ScopedCapabilities{}, err
	}
	for _, record := range records {
		if IsComputerUseServer(record.Descriptor) && m.computerUseExecutable == "" {
			continue
		}
		if record.State != TrustEnabled || record.Health == HealthConnecting || !scopeMatches(record.Descriptor, runID, workspaceID) {
			continue
		}
		key := catalogSessionKey{runID, workspaceID, record.Descriptor.ID}
		s, err := m.session(key)
		if err != nil {
			return ScopedCapabilities{}, err
		}
		s.checkMu.RLock()
		oldCheck := s.check
		oldRevoked := s.revoked
		s.checkMu.RUnlock()
		// A completed execution lease is not a task revocation. Ask approval
		// resumes with a new lease; preserve the peer's window/observation state
		// only while the same live runtime fence owns this task. The new check
		// passed above, and every dispatch still installs fresh operation guards.
		sameFence := oldRevoked != nil && oldRevoked == revoked
		closedFence := false
		if oldRevoked != nil {
			select {
			case <-oldRevoked:
				closedFence = true
			default:
			}
		}
		if oldCheck != nil && (oldRevoked != revoked || closedFence || (!sameFence && oldCheck(ctx) != nil)) {
			m.closeSessions(func(k catalogSessionKey) bool { return k == key })
			s, err = m.session(key)
			if err != nil {
				return ScopedCapabilities{}, err
			}
		}
		s.checkMu.Lock()
		s.check = check
		s.revoked = revoked
		s.checkMu.Unlock()
		select {
		case s.changed <- struct{}{}:
		default:
		}
		if err := m.refreshTaskCatalog(ctx, key, s); err != nil {
			var serverFailure *catalogRefreshFailure
			if !errors.As(err, &serverFailure) {
				return ScopedCapabilities{}, err
			}
		}
	}
	if err := check(ctx); err != nil {
		m.CloseRun(runID)
		return ScopedCapabilities{}, err
	}
	return m.Capabilities(ctx, runID, workspaceID)
}

func (m *Manager) refreshTaskCatalog(ctx context.Context, key catalogSessionKey, s *catalogSession) error {
	check := s.boundTaskCheck()
	if err := check(ctx); err != nil {
		s.stop()
		return err
	}
	record, err := m.store.GetMCPClientServer(ctx, key.server)
	if err != nil {
		return err
	}
	if record.State != TrustEnabled || record.Health == HealthConnecting || !scopeMatches(record.Descriptor, key.run, key.workspace) {
		s.stop()
		return &catalogRefreshFailure{cause: apperror.New(apperror.CodePolicyDenied, "MCP server is no longer enabled for this task")}
	}
	_, err = m.invokeResolved(ctx, InvokeRequest{RunID: key.run, WorkspaceID: key.workspace, ServerID: key.server,
		CapabilityFingerprint: record.Capabilities.Fingerprint, OperationID: idgen.New("mcp-catalog"), catalogCheck: check}, record)
	if err != nil {
		if checkErr := check(ctx); checkErr != nil {
			s.stop()
			return checkErr
		}
		var storeFailure *catalogStoreFailure
		if errors.As(err, &storeFailure) {
			return err
		}
		current, readErr := m.store.GetMCPClientServer(ctx, key.server)
		if readErr != nil {
			return readErr
		}
		var authority *runtimeAuthorityError
		if !errors.As(err, &authority) && !errors.Is(err, errStaleCatalog) && current.State == TrustEnabled && current.Generation == record.Generation {
			previous := current.Generation
			current.Generation++
			current.Health, current.HealthMessage = HealthUnavailable, "catalog refresh failed"
			current.UpdatedAt = m.now().UTC()
			if _, updateErr := m.store.UpdateMCPClientServer(ctx, current, previous); updateErr != nil {
				return updateErr
			}
		}
		return &catalogRefreshFailure{cause: err}
	}
	return err
}

func (m *Manager) watchCatalog(key catalogSessionKey, s *catalogSession) {
	defer close(s.done)
	defer func() {
		<-s.lease
		if s.client != nil {
			_ = s.client.Close()
			s.client = nil
		}
		s.release()
	}()
	for {
		s.checkMu.RLock()
		revoked := s.revoked
		s.checkMu.RUnlock()
		select {
		case <-s.changed:
			continue
		case <-revoked:
			s.stop()
			return
		case <-s.lifetime.Done():
			return
		case fault := <-s.fault:
			if s.acquire(s.lifetime) != nil {
				return
			}
			if s.client != fault.client {
				s.release()
				continue
			}
			if s.client != nil {
				_ = s.client.Close()
				s.client = nil
			}
			record, err := m.store.GetMCPClientServer(s.lifetime, key.server)
			if err == nil && record.State == TrustEnabled {
				_ = m.markInvocationUnavailable(s.lifetime, record, "catalog notification stream disconnected", fault.cause)
			}
			s.release()
		case <-s.dirty:
			s.checkMu.RLock()
			active := s.check != nil
			s.checkMu.RUnlock()
			if !active {
				continue
			}
			// The bounded discovery persists failures in HealthMessage and retains the
			// old snapshot. Another model turn may reconnect; this worker never retries.
			_ = m.refreshTaskCatalog(s.lifetime, key, s)
		}
	}
}

func (m *Manager) closeSessions(match func(catalogSessionKey) bool) {
	m.sessionsMu.Lock()
	var closing []*catalogSession
	for key, s := range m.sessions {
		if match(key) {
			s.stop()
			delete(m.sessions, key)
			closing = append(closing, s)
		}
	}
	m.sessionsMu.Unlock()
	for _, s := range closing {
		<-s.done
	}
}
func (m *Manager) CloseRun(runID string) {
	m.closeSessions(func(k catalogSessionKey) bool { return k.run == runID })
}
func (m *Manager) Close() error {
	m.sessionsMu.Lock()
	m.closed = true
	m.sessionsMu.Unlock()
	m.closeSessions(func(catalogSessionKey) bool { return true })
	return nil
}

// No operation authority survives its serial lease. The connection and passive
// list-change subscription remain; server-initiated input is denied while idle.
func clearSessionOperation(c *Client) {
	c.resolved.guards = toolcontract.ExecutionGuards{}
	c.resolved.credential = nil
	if c.remote != nil {
		c.remote.resolveBearer = nil
	}
	c.inputMu.Lock()
	c.inputHandler = nil
	c.inputRecheck = nil
	c.inputMu.Unlock()
}

func (c *Client) invalidateCatalog() {
	c.catalogRevision.Add(1)
	if c.resolved != nil {
		c.resolved.mu.Lock()
		c.resolved.capability = ""
		c.resolved.mu.Unlock()
	}
	if c.catalogChanged != nil {
		c.catalogChanged()
	}
}

type catalogSubscriptionKey struct{}
type catalogSubscription struct {
	mu        sync.Mutex
	discovery *resolvedDiscovery
	wireID    json.RawMessage
	cancelled bool
}

func (s *catalogSubscription) listen(ctx context.Context, fingerprint string, frame *jsonrpc.Request, size int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.wireID) != 0 || s.discovery == nil || s.discovery.guard == nil {
		return errors.New("MCP catalog subscription has no bounded discovery authority")
	}
	// Only the SDK's catalog notification subscription is permitted. Resource
	// subscriptions and arbitrary filters would expand this passive channel.
	var params map[string]json.RawMessage
	if json.Unmarshal(frame.Params, &params) != nil {
		return errors.New("invalid MCP catalog subscription")
	}
	for key := range params {
		if key != "notifications" && key != "_meta" {
			return errors.New("unsupported MCP subscription parameter")
		}
	}
	var notifications map[string]bool
	if json.Unmarshal(params["notifications"], &notifications) != nil || len(notifications) == 0 {
		return errors.New("empty MCP catalog subscription")
	}
	for key, enabled := range notifications {
		if !enabled || (key != "toolsListChanged" && key != "resourcesListChanged" && key != "promptsListChanged") {
			return errors.New("unsupported MCP subscription notification")
		}
	}
	if err := s.discovery.guard(ctx, toolcontract.DiscoverySend{ConnectionFingerprint: fingerprint, Method: frame.Method, Bytes: size}); err != nil {
		return err
	}
	s.wireID, _ = json.Marshal(frame.ID.Raw())
	s.discovery = nil
	return nil
}
func (s *catalogSubscription) cancel(frame *jsonrpc.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if s.cancelled || len(s.wireID) == 0 || json.Unmarshal(frame.Params, &params) != nil || !bytes.Equal(bytes.TrimSpace(params.RequestID), s.wireID) {
		return errors.New("unbound MCP catalog cancellation")
	}
	s.cancelled = true
	return nil
}

func (m *Manager) releaseSession(key catalogSessionKey, s *catalogSession) {
	s.checkMu.RLock()
	persistent := s.check != nil
	s.checkMu.RUnlock()
	s.release()
	if !persistent {
		m.sessionsMu.Lock()
		if m.sessions[key] == s {
			delete(m.sessions, key)
			s.stop()
		}
		m.sessionsMu.Unlock()
	}
}
