package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
)

const (
	FullCDPSessionProtocolVersion      = "full_cdp_session.v1"
	FullCDPSessionCloseProtocolVersion = "full_cdp_session_close.v1"
	maxActiveFullCDPSessions           = 4
	fullCDPPermissionPollInterval      = 500 * time.Millisecond
	fullCDPCleanupRetryInterval        = 250 * time.Millisecond
	fullCDPAuditRetryInterval          = 250 * time.Millisecond
	fullCDPTerminalRecordRetention     = 30 * time.Minute
	maxRetainedFullCDPRunSessions      = 256
	maxRetainedFullCDPOpenOperations   = 512
	maxRetainedFullCDPCloseOperations  = 512
)

var errFullCDPCloseAuditDetached = errors.New(
	"full CDP close audit store is detached")

type FullCDPSessionState string

const (
	FullCDPSessionNone     FullCDPSessionState = "none"
	FullCDPSessionStarting FullCDPSessionState = "starting"
	FullCDPSessionReady    FullCDPSessionState = "ready"
	FullCDPSessionClosing  FullCDPSessionState = "closing"
	FullCDPSessionClosed   FullCDPSessionState = "closed"
	FullCDPSessionFailed   FullCDPSessionState = "failed"
)

const (
	FullCDPCloseOperator          = "operator_closed"
	FullCDPCloseExpired           = "expired"
	FullCDPClosePermissionRevoked = "permission_revoked"
	FullCDPCloseProcessExited     = "process_exited"
	FullCDPCloseRunTerminal       = "run_terminal"
	FullCDPCloseDesktopShutdown   = "desktop_shutdown"
	FullCDPCloseOpenFailed        = "open_failed"
)

type FullCDPBrowserSelection struct {
	Product string `json:"product"`
	Channel string `json:"channel"`
}

// FullCDPSessionView is the deliberately redacted public session state. It
// never includes a PID, executable/Profile path, DevTools endpoint, token,
// permission snapshot identity, runtime fence, or authorization fingerprint.
type FullCDPSessionView struct {
	Version              string                  `json:"version"`
	SessionID            string                  `json:"session_id,omitempty"`
	RunID                string                  `json:"run_id"`
	State                FullCDPSessionState     `json:"state"`
	Browser              FullCDPBrowserSelection `json:"browser"`
	TargetOrigin         string                  `json:"target_origin,omitempty"`
	RuntimeAvailable     bool                    `json:"runtime_available"`
	StartedAt            *time.Time              `json:"started_at,omitempty"`
	ExpiresAt            *time.Time              `json:"expires_at,omitempty"`
	CompletedAt          *time.Time              `json:"completed_at,omitempty"`
	CloseReason          string                  `json:"close_reason,omitempty"`
	CDPClosed            bool                    `json:"cdp_closed"`
	ProcessTreeQuiescent bool                    `json:"process_tree_quiescent"`
	ProfileReleased      bool                    `json:"profile_released"`
	ProfileCleaned       bool                    `json:"profile_cleaned"`
	FailureCode          string                  `json:"failure_code,omitempty"`
}

type FullCDPSessionResult struct {
	Session  FullCDPSessionView `json:"session"`
	Replayed bool               `json:"replayed"`
}

type OpenFullCDPSessionRequest struct {
	RunID                                string
	Target                               string
	Browser                              FullCDPBrowserSelection
	ExpectedExecutionPermissionRevision  int64
	ExpectedBrowserCDPPermissionRevision int64
	ConfirmFullCDP                       bool
	Reason                               string
	OperationKey                         string
}

type CloseFullCDPSessionRequest struct {
	RunID             string
	ExpectedSessionID string
	OperationKey      string
	Reason            string
}

type FullCDPSessionController interface {
	GetFullCDPSession(context.Context, string) (FullCDPSessionView, error)
	OpenFullCDPSession(context.Context, OpenFullCDPSessionRequest) (
		FullCDPSessionResult, error)
	CloseFullCDPSession(context.Context, CloseFullCDPSessionRequest) (
		FullCDPSessionResult, error)
}

type FullCDPProductionStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetRunBrowserCDPPermission(context.Context, string) (
		domain.RunBrowserCDPPermissionSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (
		domain.RunExecutionPermissionSnapshot, error)
	PrepareBrowserLaunch(context.Context, browserruntime.SessionPlan,
		browserruntime.BrowserExecutableIdentity,
		browserruntime.BrowserAcceptanceCandidate,
		browserruntime.ProfileOwnershipPlan, string, string) (
		browserruntime.BrowserLaunchAttempt, browserruntime.BrowserLaunchLease,
		bool, error)
	RecordBrowserLaunchReview(context.Context, browserruntime.SessionPlan,
		browserruntime.BrowserExecutableIdentity,
		browserruntime.BrowserAcceptanceCandidate,
		browserruntime.ProfileOwnershipPlan, browserruntime.BrowserLaunchAttempt,
		browserruntime.BrowserLaunchLease, browserruntime.BrowserLaunchReviewDecision,
		string, string) (browserruntime.BrowserLaunchReview, bool, error)
	RecordFullCDPSessionOpened(context.Context, string, string, string, string,
		string, string, string, time.Time, time.Time) error
	RecordFullCDPSessionClosed(context.Context, string, string, string, string, string,
		browserruntime.FullCDPRuntimeReceipt) error
}

type managedFullCDPRuntime interface {
	Close(context.Context, string) (browserruntime.FullCDPRuntimeReceipt, error)
	Done() <-chan struct{}
	ExpiresAt() time.Time
}

type fullCDPRuntimeLauncher func(context.Context,
	browserruntime.FullCDPManagedLaunchRequest) (managedFullCDPRuntime, error)

type fullCDPBrowserDiscovery func() ([]browserruntime.BrowserExecutableIdentity, error)
type fullCDPBrowserAcceptance func(browserruntime.BrowserExecutableIdentity) (
	browserruntime.BrowserAcceptanceCandidate, error)

type fullCDPSessionEntry struct {
	view                        FullCDPSessionView
	runtime                     managedFullCDPRuntime
	openRequestFingerprint      string
	openOperationDigest         string
	openDone                    chan struct{}
	openFinished                bool
	openErr                     error
	openAudited                 bool
	closeDone                   chan struct{}
	closeFinished               bool
	closeErr                    error
	resourcesReleased           bool
	terminalSequence            uint64
	executionPermissionID       string
	executionPermissionRevision int64
	browserPermissionID         string
	browserPermissionRevision   int64
	executionFence              uint64
	runtimeID                   string
	runSessionID                string
	preview                     *FullCDPPreviewCapture
	previewBusy                 bool
}

// NewHomeFullCDPProductionService shares the same private-profile setup between
// the desktop host and the standalone HTTP host. It grants no Run authority.
func NewHomeFullCDPProductionService(store FullCDPProductionStore,
	controller *browserruntime.BrowserProcessController,
	runtimeCapabilities browserruntime.FullCDPRuntimeCapabilities,
	permissionCapabilities domain.BrowserCDPPermissionRuntimeCapabilities,
	executionCapabilities domain.ExecutionPermissionRuntimeCapabilities, home string,
) (*FullCDPProductionService, error) {
	profileRoot, err := browserruntime.PrepareFullCDPProfileRuntimeRoot(home)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeUnavailable,
			"Full CDP private Profile runtime could not be prepared", err)
	}
	return NewFullCDPProductionService(store, controller, runtimeCapabilities,
		permissionCapabilities, executionCapabilities, profileRoot)
}

type fullCDPOperationRecord struct {
	requestFingerprint string
	entry              *fullCDPSessionEntry
	acceptedAt         time.Time
	acceptedSequence   uint64
}

type fullCDPCachePolicy struct {
	terminalRetention   time.Duration
	latestByRunLimit    int
	openOperationLimit  int
	closeOperationLimit int
}

// FullCDPProductionService owns all process-local Full CDP handles. A restart
// restores no authority and adopts no browser PID or Profile.
type FullCDPProductionService struct {
	store                  FullCDPProductionStore
	controller             *browserruntime.BrowserProcessController
	runtimeCapabilities    browserruntime.FullCDPRuntimeCapabilities
	permissionCapabilities domain.BrowserCDPPermissionRuntimeCapabilities
	executionCapabilities  domain.ExecutionPermissionRuntimeCapabilities
	profileRoot            string
	discover               fullCDPBrowserDiscovery
	accept                 fullCDPBrowserAcceptance
	launch                 fullCDPRuntimeLauncher

	mu          sync.Mutex
	latestByRun map[string]*fullCDPSessionEntry
	// Keep only target selection, separately from evictable replay records.
	// Forgetting a closed session must not select another browser in this host.
	selectedBrowserRuns map[string]struct{}
	openOperations      map[string]fullCDPOperationRecord
	closeOperations     map[string]fullCDPOperationRecord
	cachePolicy         fullCDPCachePolicy
	cacheSequence       uint64
	active              int
	closed              bool
	openContext         context.Context
	cancelOpens         context.CancelFunc

	closeAuditMu       sync.RWMutex
	closeAuditDetached bool
}

func NewFullCDPProductionService(store FullCDPProductionStore,
	controller *browserruntime.BrowserProcessController,
	runtimeCapabilities browserruntime.FullCDPRuntimeCapabilities,
	permissionCapabilities domain.BrowserCDPPermissionRuntimeCapabilities,
	executionCapabilities domain.ExecutionPermissionRuntimeCapabilities,
	profileRoot string,
) (*FullCDPProductionService, error) {
	if store == nil || controller == nil || !controller.FullCDPAvailable() {
		return nil, apperror.New(apperror.CodeUnavailable,
			"full CDP production runtime is unavailable")
	}
	if err := runtimeCapabilities.Validate(); err != nil ||
		!runtimeCapabilities.StartEnabled ||
		!runtimeCapabilities.DisposableProfileEnabled ||
		!runtimeCapabilities.TransportEnabled {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"full CDP production runtime gates are not enabled")
	}
	if err := permissionCapabilities.Validate(); err != nil ||
		!permissionCapabilities.ControlEnabled ||
		!permissionCapabilities.FullDebugEnabled {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"full CDP permission gates are not enabled")
	}
	if err := executionCapabilities.Validate(); err != nil ||
		!executionCapabilities.DangerFullAccessEnabled ||
		executionCapabilities.RuntimeAuthority == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"full CDP execution authority is unavailable")
	}
	profileRoot = filepath.Clean(strings.TrimSpace(profileRoot))
	if profileRoot == "." || !filepath.IsAbs(profileRoot) ||
		filepath.Dir(profileRoot) == profileRoot ||
		filepath.Base(profileRoot) != browserruntime.ProfileRuntimeRootName {
		return nil, apperror.New(apperror.CodeInvalidArgument,
			"full CDP Profile root is invalid")
	}
	service := &FullCDPProductionService{
		store: store, controller: controller,
		runtimeCapabilities:    runtimeCapabilities,
		permissionCapabilities: permissionCapabilities,
		executionCapabilities:  executionCapabilities, profileRoot: profileRoot,
		discover:        browserruntime.DiscoverInstalledBrowsers,
		accept:          browserruntime.BuildBrowserAcceptanceCandidate,
		latestByRun:     make(map[string]*fullCDPSessionEntry),
		openOperations:  make(map[string]fullCDPOperationRecord),
		closeOperations: make(map[string]fullCDPOperationRecord),
		cachePolicy:     defaultFullCDPCachePolicy(),
	}
	service.openContext, service.cancelOpens = context.WithCancel(context.Background())
	service.launch = func(ctx context.Context,
		request browserruntime.FullCDPManagedLaunchRequest,
	) (managedFullCDPRuntime, error) {
		runtime, err := browserruntime.LaunchManagedFullCDP(ctx, controller, request)
		return managedFullCDPLaunchResult(runtime, err)
	}
	return service, nil
}

// managedFullCDPLaunchResult prevents a nil concrete runtime from becoming a
// non-nil interface value. Launch failures that already released every owned
// resource intentionally return (nil, err); the Application layer must not
// retain capacity or start an impossible cleanup loop for that result.
func managedFullCDPLaunchResult(runtime *browserruntime.FullCDPManagedRuntime,
	err error,
) (managedFullCDPRuntime, error) {
	if runtime == nil {
		return nil, err
	}
	return runtime, err
}

func (s *FullCDPProductionService) GetFullCDPSession(ctx context.Context,
	runID string,
) (FullCDPSessionView, error) {
	runID = strings.TrimSpace(runID)
	if s == nil || s.store == nil {
		return FullCDPSessionView{}, apperror.New(apperror.CodeUnavailable,
			"full CDP session service is unavailable")
	}
	if !domain.ValidAgentID(runID) {
		return FullCDPSessionView{}, apperror.New(apperror.CodeInvalidArgument,
			"full CDP Run id is invalid")
	}
	if _, err := s.store.GetRun(ctx, runID); err != nil {
		return FullCDPSessionView{}, apperror.Normalize(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneTerminalCachesLocked(time.Now().UTC())
	if entry := s.latestByRun[runID]; entry != nil {
		return entry.view, nil
	}
	return FullCDPSessionView{Version: FullCDPSessionProtocolVersion,
		RunID: runID, State: FullCDPSessionNone,
		RuntimeAvailable: !s.closed}, nil
}

func (s *FullCDPProductionService) OpenFullCDPSession(ctx context.Context,
	request OpenFullCDPSessionRequest,
) (FullCDPSessionResult, error) {
	canonical, fingerprint, operationDigest, err := s.validateOpenRequest(request)
	if err != nil {
		return FullCDPSessionResult{}, err
	}
	request = canonical

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return FullCDPSessionResult{}, apperror.New(apperror.CodeUnavailable,
			"full CDP session service is shutting down")
	}
	s.ensureOpenContextLocked()
	serviceOpenContext := s.openContext
	now := time.Now().UTC()
	s.pruneTerminalCachesLocked(now)
	if prior, ok := s.openOperations[operationDigest]; ok {
		if prior.requestFingerprint != fingerprint {
			s.mu.Unlock()
			return FullCDPSessionResult{}, apperror.New(apperror.CodeConflict,
				"full CDP Idempotency-Key was reused with another open intent")
		}
		entry := prior.entry
		done := entry.openDone
		s.mu.Unlock()
		if err := waitFullCDPOperation(ctx, done); err != nil {
			return FullCDPSessionResult{}, err
		}
		s.mu.Lock()
		view, openErr := entry.view, entry.openErr
		s.mu.Unlock()
		return FullCDPSessionResult{Session: view, Replayed: true}, openErr
	}
	if current := s.latestByRun[request.RunID]; current != nil &&
		(current.runtime != nil || current.view.State == FullCDPSessionStarting ||
			current.view.State == FullCDPSessionReady ||
			current.view.State == FullCDPSessionClosing) {
		s.mu.Unlock()
		return FullCDPSessionResult{}, apperror.New(apperror.CodeConflict,
			"Run already has an active full CDP session")
	}
	if s.active >= maxActiveFullCDPSessions {
		s.mu.Unlock()
		return FullCDPSessionResult{}, apperror.New(apperror.CodeResourceExhausted,
			"full CDP session runtime is at capacity")
	}
	if err := s.reserveOpenCacheCapacityLocked(request.RunID); err != nil {
		s.mu.Unlock()
		return FullCDPSessionResult{}, err
	}
	entry := &fullCDPSessionEntry{
		view: FullCDPSessionView{Version: FullCDPSessionProtocolVersion,
			SessionID: idgen.New("full_cdp_session"), RunID: request.RunID,
			State: FullCDPSessionStarting, Browser: request.Browser,
			RuntimeAvailable: true},
		openRequestFingerprint: fingerprint, openOperationDigest: operationDigest,
		openDone: make(chan struct{}),
	}
	s.latestByRun[request.RunID] = entry
	if s.selectedBrowserRuns == nil {
		s.selectedBrowserRuns = make(map[string]struct{})
	}
	s.selectedBrowserRuns[request.RunID] = struct{}{}
	s.openOperations[operationDigest] = fullCDPOperationRecord{
		requestFingerprint: fingerprint, entry: entry, acceptedAt: now,
		acceptedSequence: s.nextCacheSequenceLocked()}
	s.active++
	s.mu.Unlock()

	openContext, cancelOpen := bindFullCDPOpenContext(ctx, serviceOpenContext)
	defer cancelOpen()
	openErr := s.openReservedEntry(openContext, entry, request, operationDigest)
	recovering := false
	recoveryReason := ""
	var recoveryRuntime managedFullCDPRuntime
	s.mu.Lock()
	entry.openErr = openErr
	if openErr != nil {
		if entry.view.State == FullCDPSessionClosing && entry.runtime != nil {
			recovering = true
			recoveryReason = entry.view.CloseReason
			recoveryRuntime = entry.runtime
		} else {
			now := time.Now().UTC()
			entry.view.State = FullCDPSessionFailed
			entry.view.FailureCode = "open_failed"
			s.markEntryTerminalLocked(entry, now)
			s.active--
		}
	}
	entry.openFinished = true
	close(entry.openDone)
	view := entry.view
	s.mu.Unlock()
	if openErr != nil {
		if recovering {
			go func() {
				_, _ = s.finalizeClosingEntry(entry, recoveryRuntime, recoveryReason)
			}()
		}
		return FullCDPSessionResult{Session: view}, openErr
	}
	go s.monitor(entry)
	return FullCDPSessionResult{Session: view}, nil
}

func (s *FullCDPProductionService) openReservedEntry(ctx context.Context,
	entry *fullCDPSessionEntry, request OpenFullCDPSessionRequest,
	operationDigest string,
) error {
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if !domain.CanChangeRunBrowserCDPPermission(run.Status) {
		return apperror.New(apperror.CodeFailedPrecondition,
			"full CDP requires a non-terminal Run")
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return apperror.Normalize(err)
	}
	browserPermission, err := s.store.GetRunBrowserCDPPermission(ctx, run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	executionPermission, err := s.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if browserPermission.Revision != request.ExpectedBrowserCDPPermissionRevision ||
		executionPermission.Revision != request.ExpectedExecutionPermissionRevision {
		return apperror.New(apperror.CodeConflict,
			"full CDP permission revision changed before open")
	}
	if browserPermission.Mode != domain.RunBrowserCDPPermissionFullDebug ||
		!browserPermission.OperatorConfirmed ||
		executionPermission.Mode != domain.RunExecutionPermissionFull ||
		!s.executionCapabilities.AllowsSnapshot(executionPermission) {
		return apperror.New(apperror.CodePolicyDenied,
			"full CDP requires live Full and its enabled sub-permission")
	}
	identity, acceptance, err := s.selectBrowser(request.Browser)
	if err != nil {
		return err
	}
	session, err := browserruntime.BuildSessionPlan(browserruntime.NewSessionPlanRequest{
		SessionID: run.SessionID, RunID: run.ID, WorkspaceID: mission.WorkspaceID,
		ProfileID: browserruntime.ProfileCTFLab, Targets: []string{request.Target},
		LimitsOverride: browserLimitsOverrideForRun(run),
		Features: browserruntime.FeatureRequest{InterceptRequests: true,
			ModifyRequests: true, ReplayRequests: true, EditCookies: true},
	})
	if err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"full CDP target is invalid", err)
	}
	if err := browserruntime.ValidateFullCDPSessionPlan(session); err != nil {
		return apperror.Wrap(apperror.CodeInvalidArgument,
			"full CDP target must be one literal loopback origin", err)
	}
	ownership, err := browserruntime.BuildProfileOwnershipPlan(session, identity,
		s.profileRoot)
	if err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"full CDP disposable Profile could not be prepared", err)
	}
	prepareKey := "full-cdp-open-" + operationDigest[:32] + "-prepare"
	reviewKey := "full-cdp-open-" + operationDigest[:32] + "-review"
	attempt, launchLease, _, err := s.store.PrepareBrowserLaunch(ctx, session,
		identity, acceptance, ownership, prepareKey, "full_cdp_runtime")
	if err != nil {
		return apperror.Normalize(err)
	}
	review, _, err := s.store.RecordBrowserLaunchReview(ctx, session, identity,
		acceptance, ownership, attempt, launchLease,
		browserruntime.BrowserLaunchReviewAcceptCandidate, reviewKey,
		"desktop_operator")
	if err != nil {
		return apperror.Normalize(err)
	}
	latestBrowserPermission, err := s.store.GetRunBrowserCDPPermission(ctx, run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	latestExecutionPermission, err := s.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if latestBrowserPermission.ID != browserPermission.ID ||
		latestBrowserPermission.Revision != browserPermission.Revision ||
		latestBrowserPermission.Mode != browserPermission.Mode ||
		latestExecutionPermission.ID != executionPermission.ID ||
		latestExecutionPermission.Revision != executionPermission.Revision ||
		latestExecutionPermission.Mode != executionPermission.Mode ||
		!s.executionCapabilities.AllowsSnapshot(latestExecutionPermission) {
		return apperror.New(apperror.CodeConflict,
			"full CDP permission changed during browser launch preparation")
	}
	// One fence is issued after every durable/confirm boundary and is shared by
	// both the process-start and CDP-session authorizations.
	executionFence, err := s.executionCapabilities.RuntimeAuthority.
		IssueRunAuthorizationFence(run.ID)
	if err != nil {
		return apperror.Wrap(apperror.CodePolicyDenied,
			"full CDP execution authority is unavailable", err)
	}
	runtimeID := idgen.New("full_cdp_runtime")
	s.mu.Lock()
	entry.runtimeID = runtimeID
	entry.runSessionID = run.SessionID
	s.mu.Unlock()
	managed, err := s.launch(ctx, browserruntime.FullCDPManagedLaunchRequest{
		RuntimeID: runtimeID, InitialURL: request.Target, Session: session, Identity: identity,
		Acceptance: acceptance, Ownership: ownership, Attempt: attempt,
		LaunchLease: launchLease, Review: review, Permission: browserPermission,
		ExecutionPermission:    executionPermission,
		RuntimeCapabilities:    s.runtimeCapabilities,
		PermissionCapabilities: s.permissionCapabilities,
		ExecutionCapabilities:  s.executionCapabilities,
		ExecutionFence:         executionFence, Confirmed: request.ConfirmFullCDP,
		Now: time.Now().UTC(),
	})
	if err != nil {
		if managed != nil {
			s.mu.Lock()
			entry.runtime = managed
			entry.view.State = FullCDPSessionClosing
			entry.view.CloseReason = FullCDPCloseOpenFailed
			entry.view.FailureCode = "open_failed"
			if entry.closeDone == nil {
				entry.closeDone = make(chan struct{})
			}
			s.mu.Unlock()
		}
		return apperror.Wrap(apperror.CodeUnavailable,
			"full CDP browser session could not be opened", err)
	}
	s.mu.Lock()
	entry.runtime = managed
	shuttingDown := s.closed
	if shuttingDown {
		entry.view.State = FullCDPSessionClosing
		entry.view.CloseReason = FullCDPCloseDesktopShutdown
		entry.view.FailureCode = "open_failed"
		if entry.closeDone == nil {
			entry.closeDone = make(chan struct{})
		}
		s.mu.Unlock()
		return apperror.New(apperror.CodeUnavailable,
			"full CDP session service is shutting down")
	}
	s.mu.Unlock()
	startedAt := time.Now().UTC()
	expiresAt := managed.ExpiresAt().UTC()
	targetOrigin := session.Scope.Origins[0].String()
	if err := s.store.RecordFullCDPSessionOpened(ctx, run.ID, runtimeID,
		entry.view.SessionID, run.SessionID, request.Browser.Product, request.Browser.Channel,
		targetOrigin, startedAt, expiresAt); err != nil {
		s.mu.Lock()
		entry.view.State = FullCDPSessionClosing
		entry.view.CloseReason = FullCDPCloseOpenFailed
		entry.view.FailureCode = "open_audit_failed"
		if entry.closeDone == nil {
			entry.closeDone = make(chan struct{})
		}
		s.mu.Unlock()
		return apperror.Wrap(apperror.CodeInternal,
			"full CDP open audit could not be recorded", err)
	}
	s.mu.Lock()
	entry.openAudited = true
	entry.view.State = FullCDPSessionReady
	entry.view.TargetOrigin = targetOrigin
	entry.view.StartedAt = &startedAt
	entry.view.ExpiresAt = &expiresAt
	entry.executionPermissionID = executionPermission.ID
	entry.executionPermissionRevision = executionPermission.Revision
	entry.browserPermissionID = browserPermission.ID
	entry.browserPermissionRevision = browserPermission.Revision
	entry.executionFence = executionFence
	s.mu.Unlock()
	return nil
}

func (s *FullCDPProductionService) CloseFullCDPSession(ctx context.Context,
	request CloseFullCDPSessionRequest,
) (FullCDPSessionResult, error) {
	canonical, fingerprint, operationDigest, err := s.validateCloseRequest(request)
	if err != nil {
		return FullCDPSessionResult{}, err
	}
	request = canonical
	s.mu.Lock()
	now := time.Now().UTC()
	s.pruneTerminalCachesLocked(now)
	if prior, ok := s.closeOperations[operationDigest]; ok {
		if prior.requestFingerprint != fingerprint {
			s.mu.Unlock()
			return FullCDPSessionResult{}, apperror.New(apperror.CodeConflict,
				"full CDP Idempotency-Key was reused with another close intent")
		}
		entry := prior.entry
		done := entry.closeDone
		s.mu.Unlock()
		if err := waitFullCDPOperation(ctx, done); err != nil {
			return FullCDPSessionResult{}, err
		}
		s.mu.Lock()
		view, closeErr := entry.view, entry.closeErr
		s.mu.Unlock()
		return FullCDPSessionResult{Session: view, Replayed: true}, closeErr
	}
	entry := s.latestByRun[request.RunID]
	if entry == nil {
		s.mu.Unlock()
		return FullCDPSessionResult{}, apperror.New(apperror.CodeNotFound,
			"full CDP session was not found")
	}
	if entry.view.SessionID != request.ExpectedSessionID {
		s.mu.Unlock()
		return FullCDPSessionResult{}, apperror.New(apperror.CodeConflict,
			"full CDP session changed before close")
	}
	if err := s.reserveCloseCacheCapacityLocked(); err != nil {
		s.mu.Unlock()
		return FullCDPSessionResult{}, err
	}
	if entry.closeDone == nil {
		// Publish the completion channel before the idempotency record so a
		// concurrent replay can never observe a half-created close operation.
		entry.closeDone = make(chan struct{})
	}
	done := entry.closeDone
	s.closeOperations[operationDigest] = fullCDPOperationRecord{
		requestFingerprint: fingerprint, entry: entry, acceptedAt: now,
		acceptedSequence: s.nextCacheSequenceLocked()}
	s.mu.Unlock()
	// Once an exact cleanup intent is accepted, client cancellation must not
	// cancel or strand it. The request context only bounds how long this caller
	// waits; the process/Profile owner completes cleanup independently.
	go func() {
		_, _ = s.closeEntry(context.Background(), entry, FullCDPCloseOperator)
	}()
	if err := waitFullCDPOperation(ctx, done); err != nil {
		return FullCDPSessionResult{}, err
	}
	s.mu.Lock()
	view, closeErr := entry.view, entry.closeErr
	s.mu.Unlock()
	return FullCDPSessionResult{Session: view}, closeErr
}

func (s *FullCDPProductionService) closeEntry(ctx context.Context,
	entry *fullCDPSessionEntry, reason string,
) (FullCDPSessionView, error) {
	for {
		s.mu.Lock()
		if entry.view.State == FullCDPSessionClosed ||
			entry.view.State == FullCDPSessionFailed {
			if entry.closeDone != nil && !entry.closeFinished {
				close(entry.closeDone)
				entry.closeFinished = true
			}
			view, err := entry.view, entry.closeErr
			s.mu.Unlock()
			return view, err
		}
		if entry.view.State == FullCDPSessionStarting {
			done := entry.openDone
			s.mu.Unlock()
			if err := waitFullCDPOperation(ctx, done); err != nil {
				return FullCDPSessionView{}, err
			}
			continue
		}
		if entry.view.State == FullCDPSessionClosing {
			done := entry.closeDone
			s.mu.Unlock()
			if err := waitFullCDPOperation(ctx, done); err != nil {
				return FullCDPSessionView{}, err
			}
			s.mu.Lock()
			view, err := entry.view, entry.closeErr
			s.mu.Unlock()
			return view, err
		}
		entry.view.State = FullCDPSessionClosing
		entry.view.CloseReason = reason
		entry.preview = nil
		if entry.closeDone == nil {
			entry.closeDone = make(chan struct{})
		}
		runtime := entry.runtime
		s.mu.Unlock()
		if runtime == nil {
			closeErr := apperror.New(apperror.CodeFailedPrecondition,
				"full CDP session has no live runtime")
			now := time.Now().UTC()
			s.mu.Lock()
			entry.view.State = FullCDPSessionFailed
			s.markEntryTerminalLocked(entry, now)
			entry.view.CloseReason = reason
			entry.view.FailureCode = "runtime_missing"
			entry.closeErr = closeErr
			if !entry.resourcesReleased {
				entry.resourcesReleased = true
				if s.active > 0 {
					s.active--
				}
			}
			if !entry.closeFinished {
				close(entry.closeDone)
				entry.closeFinished = true
			}
			view := entry.view
			s.mu.Unlock()
			return view, closeErr
		}
		return s.finalizeClosingEntry(entry, runtime, reason)
	}
}

// finalizeClosingEntry is called only by the goroutine that moved entry into
// Closing. A cleanup receipt is terminal only after its complete protocol and
// owner binding validate, the process tree is quiescent, and the disposable
// Profile has been removed. Until then the entry continues to consume capacity.
// Once resources are safe, capacity is released exactly once, but closeDone
// remains open until the idempotent close audit has been delivered or Desktop
// shutdown has explicitly detached the store.
func (s *FullCDPProductionService) finalizeClosingEntry(
	entry *fullCDPSessionEntry, runtime managedFullCDPRuntime, reason string,
) (FullCDPSessionView, error) {
	var receipt browserruntime.FullCDPRuntimeReceipt
	var cleanupErr error
	for {
		receipt, cleanupErr = runtime.Close(context.Background(), reason)
		if receiptErr := validateFullCDPCloseReceipt(entry, receipt); receiptErr != nil {
			closeErr := apperror.Wrap(apperror.CodeInternal,
				"full CDP cleanup returned an invalid receipt", receiptErr)
			now := time.Now().UTC()
			s.mu.Lock()
			entry.view.State = FullCDPSessionFailed
			entry.view.CloseReason = reason
			entry.view.FailureCode = "receipt_validation_failed"
			s.markEntryTerminalLocked(entry, now)
			entry.closeErr = closeErr
			// A malformed proof cannot authorize releasing the runtime owner or
			// its capacity. Keep both retained, but finish the close operation as
			// an explicit internal failure instead of retrying a store write with
			// a receipt the store must reject forever.
			if !entry.closeFinished {
				close(entry.closeDone)
				entry.closeFinished = true
			}
			view := entry.view
			s.mu.Unlock()
			return view, closeErr
		}
		resourcesSafe := receipt.ProcessTreeQuiescent && receipt.ProfileCleaned
		if resourcesSafe {
			break
		}
		failureCode := receipt.FailureCode
		if failureCode == "" {
			failureCode = "cleanup_recovery_required"
		}
		s.mu.Lock()
		entry.view.CloseReason = reason
		entry.view.CDPClosed = receipt.CDPClosed
		entry.view.ProcessTreeQuiescent = receipt.ProcessTreeQuiescent
		entry.view.ProfileReleased = receipt.ProfileReleased
		entry.view.ProfileCleaned = receipt.ProfileCleaned
		entry.view.FailureCode = failureCode
		// This error is diagnostic while cleanup is still owned by the
		// service. It is not observable through CloseFullCDPSession until
		// closeDone is closed after a resource-safe receipt and durable audit.
		entry.closeErr = apperror.Wrap(apperror.CodeInternal,
			"full CDP session cleanup requires recovery", cleanupErr)
		s.mu.Unlock()

		timer := time.NewTimer(fullCDPCleanupRetryInterval)
		<-timer.C
	}

	s.mu.Lock()
	openAudited := entry.openAudited
	entry.view.CloseReason = reason
	entry.view.CDPClosed = receipt.CDPClosed
	entry.view.ProcessTreeQuiescent = receipt.ProcessTreeQuiescent
	entry.view.ProfileReleased = receipt.ProfileReleased
	entry.view.ProfileCleaned = receipt.ProfileCleaned
	if receipt.FailureCode != "" {
		entry.view.FailureCode = receipt.FailureCode
	} else if openAudited && receipt.Succeeded && cleanupErr == nil {
		// Clear a diagnostic recovery code from an earlier unsafe receipt.
		entry.view.FailureCode = ""
	}
	entry.runtime = nil
	if !entry.resourcesReleased {
		entry.resourcesReleased = true
		if s.active > 0 {
			s.active--
		}
	}
	s.mu.Unlock()

	// Audit every resource-safe outcome, including a launch that failed before
	// its open event was committed. Store event IDs are deterministic, so a
	// commit-acknowledgement failure can be retried without duplicating facts.
	auditDelivered := false
	var auditDeliveryErr error
	for {
		auditContext, auditCancel := context.WithTimeout(context.Background(), 2*time.Second)
		auditErr := s.recordFullCDPSessionClosed(auditContext,
			entry.view.RunID, receipt.RuntimeID, entry.view.SessionID, receipt.SessionID,
			reason, receipt)
		auditCancel()
		if auditErr == nil {
			auditDelivered = true
			break
		}
		if errors.Is(auditErr, errFullCDPCloseAuditDetached) {
			auditDeliveryErr = auditErr
			break
		}
		s.mu.Lock()
		entry.closeErr = apperror.Wrap(apperror.CodeUnavailable,
			"full CDP close audit delivery is pending", auditErr)
		s.mu.Unlock()
		timer := time.NewTimer(fullCDPAuditRetryInterval)
		<-timer.C
	}

	var closeErr error
	if cleanupErr != nil {
		closeErr = apperror.Wrap(apperror.CodeInternal,
			"full CDP session cleanup did not complete", cleanupErr)
	}
	if auditDeliveryErr != nil {
		closeErr = errors.Join(closeErr, apperror.Wrap(apperror.CodeUnavailable,
			"full CDP close audit was not delivered before store shutdown",
			auditDeliveryErr))
	}
	now := receipt.CompletedAt.UTC()
	s.mu.Lock()
	s.markEntryTerminalLocked(entry, now)
	if openAudited && auditDelivered && closeErr == nil && receipt.Succeeded {
		entry.view.State = FullCDPSessionClosed
		entry.view.FailureCode = ""
	} else {
		entry.view.State = FullCDPSessionFailed
		if entry.view.FailureCode == "" {
			entry.view.FailureCode = "cleanup_failed"
		}
	}
	entry.closeErr = closeErr
	if !entry.closeFinished {
		close(entry.closeDone)
		entry.closeFinished = true
	}
	view := entry.view
	s.mu.Unlock()
	return view, closeErr
}

func (s *FullCDPProductionService) monitor(entry *fullCDPSessionEntry) {
	s.mu.Lock()
	runtime := entry.runtime
	s.mu.Unlock()
	if runtime == nil {
		return
	}
	delay := time.Until(runtime.ExpiresAt())
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	ticker := time.NewTicker(fullCDPPermissionPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-runtime.Done():
			_, _ = s.closeEntry(context.Background(), entry,
				FullCDPCloseProcessExited)
			return
		case <-timer.C:
			_, _ = s.closeEntry(context.Background(), entry, FullCDPCloseExpired)
			return
		case <-ticker.C:
			s.mu.Lock()
			shuttingDown := s.closed
			s.mu.Unlock()
			if shuttingDown {
				_, _ = s.closeEntry(context.Background(), entry,
					FullCDPCloseDesktopShutdown)
				return
			}
			reason := s.revocationReason(entry)
			if reason != "" {
				_, _ = s.closeEntry(context.Background(), entry, reason)
				return
			}
		}
	}
}

func (s *FullCDPProductionService) revocationReason(
	entry *fullCDPSessionEntry,
) string {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	run, err := s.store.GetRun(ctx, entry.view.RunID)
	if err != nil {
		// Failure to re-prove current authority is fail-closed, but it is not
		// evidence that the durable Run actually reached a terminal state.
		return FullCDPClosePermissionRevoked
	}
	if !domain.CanChangeRunBrowserCDPPermission(run.Status) {
		return FullCDPCloseRunTerminal
	}
	browserPermission, err := s.store.GetRunBrowserCDPPermission(ctx, run.ID)
	if err != nil || browserPermission.ID != entry.browserPermissionID ||
		browserPermission.Revision != entry.browserPermissionRevision ||
		browserPermission.Mode != domain.RunBrowserCDPPermissionFullDebug ||
		!browserPermission.OperatorConfirmed {
		return FullCDPClosePermissionRevoked
	}
	executionPermission, err := s.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil || executionPermission.ID != entry.executionPermissionID ||
		executionPermission.Revision != entry.executionPermissionRevision ||
		!s.executionCapabilities.AllowsSnapshot(executionPermission) ||
		s.executionCapabilities.RuntimeAuthority == nil ||
		!s.executionCapabilities.RuntimeAuthority.AllowsRunAuthorizationFence(
			run.ID, entry.executionFence) {
		return FullCDPClosePermissionRevoked
	}
	return ""
}

func (s *FullCDPProductionService) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.ensureOpenContextLocked()
	s.closed = true
	cancelOpens := s.cancelOpens
	type cleanupWait struct {
		entry *fullCDPSessionEntry
		done  <-chan struct{}
	}
	waits := make([]cleanupWait, 0, len(s.latestByRun))
	for _, entry := range s.latestByRun {
		if entry != nil && (entry.view.State == FullCDPSessionStarting ||
			entry.view.State == FullCDPSessionReady ||
			entry.view.State == FullCDPSessionClosing) {
			if entry.closeDone == nil {
				entry.closeDone = make(chan struct{})
			}
			waits = append(waits, cleanupWait{entry: entry, done: entry.closeDone})
		}
	}
	s.mu.Unlock()
	cancelOpens()
	// Shutdown bounds how long its caller waits, not the resource cleanup.
	// Every accepted cleanup continues in a background owner even if ctx expires,
	// but the timeout path detaches and drains close-audit store access before it
	// returns to the Desktop owner.
	for _, wait := range waits {
		entry := wait.entry
		go func() {
			_, _ = s.closeEntry(context.Background(), entry,
				FullCDPCloseDesktopShutdown)
		}()
	}
	var joined error
	for _, wait := range waits {
		if err := waitFullCDPOperation(ctx, wait.done); err != nil {
			// Stop all future close-audit writes before returning control to the
			// Desktop owner that will close SQLite. Taking the exclusive lock also
			// drains an audit call that was already in flight.
			s.detachFullCDPCloseAuditStore()
			return errors.Join(joined, err)
		}
		s.mu.Lock()
		closeErr := wait.entry.closeErr
		s.mu.Unlock()
		joined = errors.Join(joined, closeErr)
	}
	return joined
}

func validateFullCDPCloseReceipt(entry *fullCDPSessionEntry,
	receipt browserruntime.FullCDPRuntimeReceipt,
) error {
	if entry == nil {
		return errors.New("full CDP close receipt has no session owner")
	}
	if err := browserruntime.ValidateFullCDPRuntimeReceipt(receipt); err != nil {
		return err
	}
	if entry.runtimeID == "" || entry.runSessionID == "" ||
		receipt.RuntimeID != entry.runtimeID ||
		receipt.RunID != entry.view.RunID ||
		receipt.SessionID != entry.runSessionID {
		return errors.New("full CDP close receipt is not bound to its runtime owner")
	}
	return nil
}

func (s *FullCDPProductionService) ensureOpenContextLocked() {
	if s.openContext == nil || s.cancelOpens == nil {
		s.openContext, s.cancelOpens = context.WithCancel(context.Background())
	}
}

func bindFullCDPOpenContext(ctx, serviceContext context.Context) (
	context.Context, context.CancelFunc,
) {
	if ctx == nil {
		ctx = context.Background()
	}
	if serviceContext == nil {
		serviceContext = context.Background()
	}
	bound, cancel := context.WithCancel(ctx)
	stopServiceCancel := context.AfterFunc(serviceContext, cancel)
	return bound, func() {
		stopServiceCancel()
		cancel()
	}
}

func (s *FullCDPProductionService) recordFullCDPSessionClosed(ctx context.Context,
	runID, runtimeID, fullCDPSessionID, runSessionID, reason string,
	receipt browserruntime.FullCDPRuntimeReceipt,
) error {
	s.closeAuditMu.RLock()
	defer s.closeAuditMu.RUnlock()
	if s.closeAuditDetached {
		return errFullCDPCloseAuditDetached
	}
	return s.store.RecordFullCDPSessionClosed(ctx, runID, runtimeID,
		fullCDPSessionID, runSessionID, reason, receipt)
}

func (s *FullCDPProductionService) detachFullCDPCloseAuditStore() {
	s.closeAuditMu.Lock()
	s.closeAuditDetached = true
	s.closeAuditMu.Unlock()
}

// Full CDP idempotency records have a deliberately finite replay window. A
// record remains non-evictable while its open is running, while it owns a live
// runtime, or while cleanup is incomplete. Once the entry is resource-safe and
// terminal, exact replay/conflict semantics are retained for this window and
// then the oldest terminal records may be reused to admit new intents.
func defaultFullCDPCachePolicy() fullCDPCachePolicy {
	return fullCDPCachePolicy{
		terminalRetention:   fullCDPTerminalRecordRetention,
		latestByRunLimit:    maxRetainedFullCDPRunSessions,
		openOperationLimit:  maxRetainedFullCDPOpenOperations,
		closeOperationLimit: maxRetainedFullCDPCloseOperations,
	}
}

func (s *FullCDPProductionService) effectiveCachePolicyLocked() fullCDPCachePolicy {
	policy := s.cachePolicy
	defaults := defaultFullCDPCachePolicy()
	if policy.terminalRetention <= 0 {
		policy.terminalRetention = defaults.terminalRetention
	}
	if policy.latestByRunLimit <= 0 {
		policy.latestByRunLimit = defaults.latestByRunLimit
	}
	if policy.openOperationLimit <= 0 {
		policy.openOperationLimit = defaults.openOperationLimit
	}
	if policy.closeOperationLimit <= 0 {
		policy.closeOperationLimit = defaults.closeOperationLimit
	}
	return policy
}

func (s *FullCDPProductionService) nextCacheSequenceLocked() uint64 {
	s.cacheSequence++
	if s.cacheSequence == 0 {
		// A wrapped zero would be indistinguishable from a legacy/test record.
		s.cacheSequence++
	}
	return s.cacheSequence
}

func (s *FullCDPProductionService) markEntryTerminalLocked(
	entry *fullCDPSessionEntry, completedAt time.Time,
) {
	completedAt = completedAt.UTC()
	entry.view.CompletedAt = &completedAt
	if entry.terminalSequence == 0 {
		entry.terminalSequence = s.nextCacheSequenceLocked()
	}
}

func (s *FullCDPProductionService) pruneTerminalCachesLocked(now time.Time) {
	policy := s.effectiveCachePolicyLocked()
	cutoff := now.Add(-policy.terminalRetention)
	for digest, record := range s.openOperations {
		if fullCDPOperationEvictable(record, false) &&
			!fullCDPTerminalTime(record.entry).After(cutoff) {
			delete(s.openOperations, digest)
		}
	}
	for digest, record := range s.closeOperations {
		if fullCDPOperationEvictable(record, true) &&
			!fullCDPTerminalTime(record.entry).After(cutoff) {
			delete(s.closeOperations, digest)
		}
	}
	for runID, entry := range s.latestByRun {
		if fullCDPEntryEvictable(entry) &&
			!fullCDPTerminalTime(entry).After(cutoff) {
			delete(s.latestByRun, runID)
		}
	}
}

func (s *FullCDPProductionService) reserveOpenCacheCapacityLocked(
	runID string,
) error {
	policy := s.effectiveCachePolicyLocked()
	openNeeded := len(s.openOperations) + 1 - policy.openOperationLimit
	latestAddition := 1
	if _, exists := s.latestByRun[runID]; exists {
		latestAddition = 0
	}
	latestNeeded := len(s.latestByRun) + latestAddition - policy.latestByRunLimit
	openVictims, openOK := fullCDPOperationEvictionCandidates(
		s.openOperations, openNeeded, false)
	latestVictims, latestOK := fullCDPLatestEvictionCandidates(
		s.latestByRun, latestNeeded)
	if !openOK || !latestOK {
		return apperror.New(apperror.CodeResourceExhausted,
			"full CDP session replay cache is at capacity")
	}
	for _, digest := range openVictims {
		delete(s.openOperations, digest)
	}
	for _, victimRunID := range latestVictims {
		delete(s.latestByRun, victimRunID)
	}
	return nil
}

func (s *FullCDPProductionService) reserveCloseCacheCapacityLocked() error {
	policy := s.effectiveCachePolicyLocked()
	needed := len(s.closeOperations) + 1 - policy.closeOperationLimit
	victims, ok := fullCDPOperationEvictionCandidates(
		s.closeOperations, needed, true)
	if !ok {
		return apperror.New(apperror.CodeResourceExhausted,
			"full CDP close replay cache is at capacity")
	}
	for _, digest := range victims {
		delete(s.closeOperations, digest)
	}
	return nil
}

func fullCDPEntryEvictable(entry *fullCDPSessionEntry) bool {
	if entry == nil || !entry.openFinished || entry.runtime != nil ||
		entry.view.CompletedAt == nil {
		return false
	}
	if entry.view.State != FullCDPSessionClosed &&
		entry.view.State != FullCDPSessionFailed {
		return false
	}
	return entry.closeDone == nil || entry.closeFinished
}

func fullCDPOperationEvictable(record fullCDPOperationRecord, closeRecord bool) bool {
	if !fullCDPEntryEvictable(record.entry) {
		return false
	}
	return !closeRecord ||
		(record.entry.closeDone != nil && record.entry.closeFinished)
}

func fullCDPTerminalTime(entry *fullCDPSessionEntry) time.Time {
	if entry == nil || entry.view.CompletedAt == nil {
		return time.Time{}
	}
	return entry.view.CompletedAt.UTC()
}

type fullCDPCacheEvictionCandidate struct {
	key              string
	terminalAt       time.Time
	acceptedAt       time.Time
	terminalSequence uint64
	acceptedSequence uint64
}

func fullCDPOperationEvictionCandidates(
	records map[string]fullCDPOperationRecord, needed int, closeRecord bool,
) ([]string, bool) {
	if needed <= 0 {
		return nil, true
	}
	candidates := make([]fullCDPCacheEvictionCandidate, 0, len(records))
	for digest, record := range records {
		if fullCDPOperationEvictable(record, closeRecord) {
			candidates = append(candidates, fullCDPCacheEvictionCandidate{
				key: digest, terminalAt: fullCDPTerminalTime(record.entry),
				acceptedAt:       record.acceptedAt,
				terminalSequence: record.entry.terminalSequence,
				acceptedSequence: record.acceptedSequence,
			})
		}
	}
	fullCDPSortEvictionCandidates(candidates)
	if len(candidates) < needed {
		return nil, false
	}
	victims := make([]string, needed)
	for i := range victims {
		victims[i] = candidates[i].key
	}
	return victims, true
}

func fullCDPLatestEvictionCandidates(
	entries map[string]*fullCDPSessionEntry, needed int,
) ([]string, bool) {
	if needed <= 0 {
		return nil, true
	}
	candidates := make([]fullCDPCacheEvictionCandidate, 0, len(entries))
	for runID, entry := range entries {
		if fullCDPEntryEvictable(entry) {
			candidates = append(candidates, fullCDPCacheEvictionCandidate{
				key: runID, terminalAt: fullCDPTerminalTime(entry),
				terminalSequence: entry.terminalSequence,
			})
		}
	}
	fullCDPSortEvictionCandidates(candidates)
	if len(candidates) < needed {
		return nil, false
	}
	victims := make([]string, needed)
	for i := range victims {
		victims[i] = candidates[i].key
	}
	return victims, true
}

func fullCDPSortEvictionCandidates(candidates []fullCDPCacheEvictionCandidate) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].terminalSequence != candidates[j].terminalSequence {
			if candidates[i].terminalSequence == 0 ||
				candidates[j].terminalSequence == 0 {
				return candidates[i].terminalSequence == 0
			}
			return candidates[i].terminalSequence < candidates[j].terminalSequence
		}
		if !candidates[i].terminalAt.Equal(candidates[j].terminalAt) {
			return candidates[i].terminalAt.Before(candidates[j].terminalAt)
		}
		if candidates[i].acceptedSequence != candidates[j].acceptedSequence {
			if candidates[i].acceptedSequence == 0 ||
				candidates[j].acceptedSequence == 0 {
				return candidates[i].acceptedSequence == 0
			}
			return candidates[i].acceptedSequence < candidates[j].acceptedSequence
		}
		if !candidates[i].acceptedAt.Equal(candidates[j].acceptedAt) {
			return candidates[i].acceptedAt.Before(candidates[j].acceptedAt)
		}
		return candidates[i].key < candidates[j].key
	})
}

func (s *FullCDPProductionService) selectBrowser(selection FullCDPBrowserSelection) (
	browserruntime.BrowserExecutableIdentity,
	browserruntime.BrowserAcceptanceCandidate, error,
) {
	identities, err := s.discover()
	if err != nil {
		return browserruntime.BrowserExecutableIdentity{},
			browserruntime.BrowserAcceptanceCandidate{},
			apperror.Wrap(apperror.CodeUnavailable,
				"installed browser discovery failed", err)
	}
	product := browserruntime.BrowserProduct(selection.Product)
	channel := browserruntime.BrowserChannel(selection.Channel)
	var lastErr error
	for _, identity := range identities {
		if identity.Product != product || identity.Channel != channel {
			continue
		}
		acceptance, acceptErr := s.accept(identity)
		if acceptErr != nil || !acceptance.ReviewEligible {
			lastErr = errors.Join(acceptErr,
				errors.New("browser publisher is not eligible"))
			continue
		}
		return identity, acceptance, nil
	}
	return browserruntime.BrowserExecutableIdentity{},
		browserruntime.BrowserAcceptanceCandidate{},
		apperror.Wrap(apperror.CodeUnavailable,
			"selected browser is not installed in a trusted fixed location", lastErr)
}

func (s *FullCDPProductionService) validateOpenRequest(
	request OpenFullCDPSessionRequest,
) (OpenFullCDPSessionRequest, string, string, error) {
	if s == nil || s.store == nil || s.launch == nil {
		return request, "", "", apperror.New(apperror.CodeUnavailable,
			"full CDP session service is unavailable")
	}
	request.RunID = strings.TrimSpace(request.RunID)
	request.Target = strings.TrimSpace(request.Target)
	request.Browser.Product = strings.ToLower(strings.TrimSpace(request.Browser.Product))
	request.Browser.Channel = strings.ToLower(strings.TrimSpace(request.Browser.Channel))
	request.Reason = strings.TrimSpace(request.Reason)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	if !domain.ValidAgentID(request.RunID) || request.Target == "" ||
		len(request.Target) > 2048 || !utf8.ValidString(request.Target) ||
		!validFullCDPBrowserSelection(request.Browser) ||
		request.ExpectedExecutionPermissionRevision <= 0 ||
		request.ExpectedBrowserCDPPermissionRevision <= 0 ||
		!request.ConfirmFullCDP || !validFullCDPOperationKey(request.OperationKey) ||
		len(request.Reason) > 512 || !utf8.ValidString(request.Reason) {
		return request, "", "", apperror.New(apperror.CodeInvalidArgument,
			"full CDP open request is invalid or unconfirmed")
	}
	fingerprint := fullCDPRequestFingerprint(struct {
		RunID             string                  `json:"run_id"`
		Target            string                  `json:"target"`
		Browser           FullCDPBrowserSelection `json:"browser"`
		ExecutionRevision int64                   `json:"execution_revision"`
		BrowserRevision   int64                   `json:"browser_revision"`
		Confirmed         bool                    `json:"confirmed"`
		Reason            string                  `json:"reason"`
	}{request.RunID, request.Target, request.Browser,
		request.ExpectedExecutionPermissionRevision,
		request.ExpectedBrowserCDPPermissionRevision, request.ConfirmFullCDP,
		request.Reason})
	return request, fingerprint, fullCDPOperationDigest(request.OperationKey), nil
}

func (s *FullCDPProductionService) validateCloseRequest(
	request CloseFullCDPSessionRequest,
) (CloseFullCDPSessionRequest, string, string, error) {
	if s == nil || s.store == nil {
		return request, "", "", apperror.New(apperror.CodeUnavailable,
			"full CDP session service is unavailable")
	}
	request.RunID = strings.TrimSpace(request.RunID)
	request.ExpectedSessionID = strings.TrimSpace(request.ExpectedSessionID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.Reason = strings.TrimSpace(request.Reason)
	if !domain.ValidAgentID(request.RunID) ||
		!domain.ValidAgentID(request.ExpectedSessionID) ||
		!validFullCDPOperationKey(request.OperationKey) ||
		len(request.Reason) > 512 || !utf8.ValidString(request.Reason) {
		return request, "", "", apperror.New(apperror.CodeInvalidArgument,
			"full CDP close request is invalid or unconfirmed")
	}
	fingerprint := fullCDPRequestFingerprint(struct {
		RunID     string `json:"run_id"`
		SessionID string `json:"session_id"`
		Reason    string `json:"reason"`
	}{request.RunID, request.ExpectedSessionID, request.Reason})
	return request, fingerprint, fullCDPOperationDigest(request.OperationKey), nil
}

func validFullCDPBrowserSelection(value FullCDPBrowserSelection) bool {
	product := browserruntime.BrowserProduct(value.Product)
	if product != browserruntime.BrowserProductChrome &&
		product != browserruntime.BrowserProductEdge {
		return false
	}
	channel := browserruntime.BrowserChannel(value.Channel)
	return channel == browserruntime.BrowserChannelStable ||
		channel == browserruntime.BrowserChannelBeta ||
		channel == browserruntime.BrowserChannelDev ||
		channel == browserruntime.BrowserChannelCanary
}

func validFullCDPOperationKey(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func fullCDPRequestFingerprint(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func fullCDPOperationDigest(value string) string {
	sum := sha256.Sum256([]byte("full-cdp-session-operation.v1\x00" + value))
	return hex.EncodeToString(sum[:])
}

func waitFullCDPOperation(ctx context.Context, done <-chan struct{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return apperror.Normalize(ctx.Err())
	case <-done:
		return nil
	}
}

var _ FullCDPSessionController = (*FullCDPProductionService)(nil)

func (view FullCDPSessionView) String() string {
	return fmt.Sprintf("%s/%s", view.RunID, view.State)
}
