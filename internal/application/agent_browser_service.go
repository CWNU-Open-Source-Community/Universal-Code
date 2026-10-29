package application

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/toolgateway"
	"os"
	"strings"
)

type AgentBrowserStore interface {
	GetRun(context.Context, string) (domain.Run, error)
	GetMission(context.Context, string) (domain.Mission, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetRootAgent(context.Context, string) (domain.AgentNode, bool, error)
	GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error)
}
type AgentBrowserOptions struct {
	ServiceReadiness AgentBrowserServiceReadiness
	HomePath         string
	Capabilities     domain.ExecutionPermissionRuntimeCapabilities
	Product          browserruntime.BrowserProduct
	Headless         bool
	SessionLifetime  time.Duration
}
type AgentBrowserCapabilities struct {
	ProtocolVersion  string   `json:"protocol_version"`
	Available        bool     `json:"available"`
	CanStart         bool     `json:"can_start"`
	SupportedActions []string `json:"supported_actions"`
	RefusalReason    string   `json:"refusal_reason,omitempty"`
	UnavailableCode  string   `json:"unavailable_code,omitempty"`
}
type AgentBrowserView struct {
	Version          string                              `json:"version"`
	RunID            string                              `json:"run_id"`
	SessionID        string                              `json:"session_id,omitempty"`
	Generation       uint64                              `json:"generation"`
	State            string                              `json:"state"`
	Product          browserruntime.BrowserProduct       `json:"product"`
	Headless         bool                                `json:"headless"`
	CanonicalURL     string                              `json:"canonical_url,omitempty"`
	Title            string                              `json:"title,omitempty"`
	DocumentEpoch    uint64                              `json:"document_epoch"`
	LastAction       string                              `json:"last_action,omitempty"`
	UpdatedAt        time.Time                           `json:"updated_at"`
	ArtifactLocator  string                              `json:"artifact_locator,omitempty"`
	ScreenshotSHA256 string                              `json:"screenshot_sha256,omitempty"`
	ScreenshotBytes  int                                 `json:"screenshot_bytes,omitempty"`
	FailureReason    string                              `json:"failure_reason,omitempty"`
	Cleanup          *browserruntime.AgentBrowserCleanup `json:"cleanup,omitempty"`
	Capabilities     AgentBrowserCapabilities            `json:"capabilities"`
}
type agentBrowserRuntime interface {
	Navigate(context.Context, string) (browserruntime.AgentBrowserNavigation, error)
	Snapshot(context.Context) (browserruntime.AgentBrowserSnapshot, error)
	Click(context.Context, string, string) (browserruntime.AgentBrowserInteraction, error)
	Type(context.Context, string, string, string, bool) (browserruntime.AgentBrowserInteraction, error)
	Screenshot(context.Context) (browserruntime.AgentBrowserScreenshot, error)
	Scroll(context.Context, float64, float64) (browserruntime.AgentBrowserInteraction, error)
	Key(context.Context, string) (browserruntime.AgentBrowserInteraction, error)
	Status() browserruntime.AgentBrowserStatus
	Cancel()
	Close(context.Context) (browserruntime.AgentBrowserCleanup, error)
	Done() <-chan struct{}
}
type agentBrowserViewportNavigator interface {
	NavigateWithViewport(context.Context, string, browserruntime.AgentBrowserViewport) (browserruntime.AgentBrowserNavigation, error)
}

type agentBrowserSnapshotTargetResolver interface {
	ResolveSnapshotTarget(context.Context, string, string, string) (string, error)
}

type agentBrowserSlot struct {
	action    sync.Mutex
	authority toolgateway.AgentBrowserCallAuthority
	// This is a private process-local activation for the runtime contract. The
	// durable authority still records the actual (possibly zero) live generation.
	activation      uint64
	runtime         agentBrowserRuntime
	launchAttempted bool
	view            AgentBrowserView
	closed          bool
	consumed        map[string]bool
	screenshotCalls map[string]string
	screenshotEpoch uint64
	screenshotURL   string
	titleEpoch      uint64
	titleURL        string
	// screencastViewer marks the single operator live-view attachment. It is a
	// process-local visibility guard and grants no execution authority.
	screencastViewer bool
}
type AgentBrowserService struct {
	store     AgentBrowserStore
	drydocks  *RunWorktreeService
	options   AgentBrowserOptions
	mu        sync.Mutex
	boot      string
	sequence  uint64
	slots     map[string]*agentBrowserSlot
	sessions  map[string]*agentBrowserSlot
	closed    bool
	launch    func(context.Context, browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error)
	available bool
}

func NewAgentBrowserService(st AgentBrowserStore, options AgentBrowserOptions) *AgentBrowserService {
	if filepath.IsAbs(options.HomePath) {
		options.HomePath = filepath.Clean(options.HomePath)
	}
	if options.SessionLifetime <= 0 || options.SessionLifetime > 24*time.Hour {
		options.SessionLifetime = 30 * time.Minute
	}
	s := &AgentBrowserService{store: st, options: options, boot: idgen.New("browser-boot"), slots: map[string]*agentBrowserSlot{}, sessions: map[string]*agentBrowserSlot{}}
	s.launch = func(ctx context.Context, r browserruntime.AgentBrowserStartRequest) (agentBrowserRuntime, error) {
		value, err := browserruntime.LaunchAgentBrowser(ctx, r)
		if err != nil {
			return nil, err
		}
		return value, nil
	}
	s.available = browserruntime.AgentBrowserPlatformAvailable() && filepath.IsAbs(options.HomePath) && options.Capabilities.Validate() == nil
	return s
}
func agentBrowserUnavailable(message string) error {
	return apperror.New(apperror.CodeFailedPrecondition, message)
}
func (s *AgentBrowserService) currentAuthority(ctx context.Context, runID string) (toolgateway.AgentBrowserCallAuthority, error) {
	var a toolgateway.AgentBrowserCallAuthority
	if s == nil || s.store == nil || !s.available {
		return a, agentBrowserUnavailable("Agent browser adapter is unavailable")
	}
	run, e := s.store.GetRun(ctx, runID)
	if e != nil {
		return a, e
	}
	if run.Terminal() || run.Status == domain.RunPaused {
		return a, agentBrowserUnavailable("Agent browser requires an active Run")
	}
	mission, e := s.store.GetMission(ctx, run.MissionID)
	if e != nil {
		return a, e
	}
	mode, e := s.store.GetRunMode(ctx, runID)
	if e != nil {
		return a, e
	}
	root, found, e := s.store.GetRootAgent(ctx, runID)
	if e != nil {
		return a, e
	}
	if !found || root.Role != domain.AgentRoleRoot {
		return a, agentBrowserUnavailable("Agent browser requires the Run root Agent")
	}
	permission, e := s.store.GetRunExecutionPermission(ctx, runID)
	if e != nil {
		return a, e
	}
	generation, live := s.options.Capabilities.FullAccessGeneration(permission)
	if permission.Mode != domain.RunExecutionPermissionFull || !live || s.options.Capabilities.RuntimeAuthority == nil {
		return a, agentBrowserUnavailable("Agent browser requires live Full activation")
	}
	a = toolgateway.AgentBrowserCallAuthority{ProtocolVersion: toolgateway.AgentBrowserAuthorityVersion, RunID: run.ID, MissionID: mission.ID, SessionID: run.SessionID, RootAgentID: root.ID, WorkspaceID: mission.WorkspaceID, Surface: mode.Surface, Phase: mode.Phase, Role: root.Role, Profile: mode.Profile, ModeRevision: mode.Revision, PermissionMode: permission.Mode, PermissionSnapshotID: permission.ID, PermissionRevision: permission.Revision, PermissionActivation: generation}
	return a, nil
}
func (s *AgentBrowserService) authority(ctx context.Context, runID string) (toolgateway.AgentBrowserCallAuthority, error) {
	a, e := s.currentAuthority(ctx, runID)
	if e != nil {
		return a, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authorityLocked(a, false)
}
func (s *AgentBrowserService) authorityLocked(a toolgateway.AgentBrowserCallAuthority, rotate bool) (toolgateway.AgentBrowserCallAuthority, error) {
	runID := a.RunID
	key := agentBrowserSlotKey(a)
	if s.closed {
		return a, agentBrowserUnavailable("Agent browser manager is shut down")
	}
	slot := s.slots[key]
	if rotate && agentBrowserUnusedColdSlot(slot) {
		candidate := agentBrowserAuthorityForSlot(a, slot)
		if !agentBrowserSameResource(candidate, slot.authority) || !s.options.Capabilities.RuntimeAuthority.AllowsRunAuthorizationFence(runID, candidate.RunAuthorizationFence) {
			// A read-only status query may have allocated this session before a
			// quiescent mode/permission change. No launch ever began, so there
			// are no owned resources to reap. Retain the old immutable session;
			// only a fresh provider advertisement may allocate its successor.
			slot.closed = true
			slot.view.State = "closed"
			cleanup := browserruntime.AgentBrowserNoProcessCleanup(slot.authority.BrowserSessionID)
			slot.view.Cleanup = &cleanup
			slot.view.UpdatedAt = time.Now().UTC()
		}
	}
	if slot == nil || (rotate && agentBrowserCleanupComplete(slot)) {
		fence, e := s.options.Capabilities.RuntimeAuthority.IssueRunAuthorizationFence(runID)
		if e != nil {
			return a, e
		}
		s.sequence++
		a.ManagerBootID = s.boot
		a.BrowserSessionID = idgen.New("agent-browser")
		a.SessionGeneration = s.sequence
		a.RunAuthorizationFence = fence
		a.Generation = a.Fingerprint()
		slot = &agentBrowserSlot{authority: a, activation: s.sequence, consumed: map[string]bool{}, view: AgentBrowserView{Version: "agent_browser_status.v1", RunID: runID, SessionID: a.BrowserSessionID, Generation: a.SessionGeneration, State: "idle", Product: s.options.Product, Headless: s.options.Headless, UpdatedAt: time.Now().UTC()}}
		s.slots[key] = slot
		s.sessions[a.BrowserSessionID] = slot
	}
	a = agentBrowserAuthorityForSlot(a, slot)
	if slot.closed || agentBrowserRuntimeEnded(slot.runtime) || !agentBrowserSameResource(a, slot.authority) || !s.options.Capabilities.RuntimeAuthority.AllowsRunAuthorizationFence(runID, a.RunAuthorizationFence) {
		return a, agentBrowserUnavailable("Agent browser session authority has expired")
	}
	return a, a.Validate()
}

func agentBrowserAuthorityForSlot(a toolgateway.AgentBrowserCallAuthority, slot *agentBrowserSlot) toolgateway.AgentBrowserCallAuthority {
	a.ManagerBootID = slot.authority.ManagerBootID
	a.BrowserSessionID = slot.authority.BrowserSessionID
	a.SessionGeneration = slot.authority.SessionGeneration
	a.RunAuthorizationFence = slot.authority.RunAuthorizationFence
	a.Generation = a.Fingerprint()
	return a
}
func (s *AgentBrowserService) check(ctx context.Context, a toolgateway.AgentBrowserCallAuthority) error {
	var current toolgateway.AgentBrowserCallAuthority
	var e error
	if a.Role == domain.AgentRoleSpecialist {
		current, e = s.workerAuthority(ctx, domain.AgentAttemptRef{RunID: a.RunID, AgentID: a.AgentID, AttemptID: a.AgentAttemptID}, false)
	} else {
		current, e = s.authority(ctx, a.RunID)
	}
	if e != nil {
		return e
	}
	if current != a {
		return agentBrowserUnavailable("Agent browser authority changed")
	}
	return nil
}
func agentBrowserRuntimeEnded(r agentBrowserRuntime) bool {
	if r == nil {
		return false
	}
	select {
	case <-r.Done():
		return true
	default:
	}
	state := r.Status().State
	return state == "closed" || state == "closing" || state == "cleanup_pending"
}
func agentBrowserCleanupComplete(slot *agentBrowserSlot) bool {
	return slot.closed && slot.view.Cleanup != nil && slot.view.Cleanup.ProcessesStopped() && slot.view.Cleanup.ProfileRemoved && !slot.view.Cleanup.CleanupPending
}

func agentBrowserUnusedColdSlot(slot *agentBrowserSlot) bool {
	return slot != nil && slot.runtime == nil && !slot.launchAttempted && !slot.closed
}

// Only preparation for a new provider request may rotate a cleaned session.
// Existing durable calls always keep their original immutable authority.
func (s *AgentBrowserService) authorityForAdvertisement(ctx context.Context, runID string) (toolgateway.AgentBrowserCallAuthority, error) {
	a, e := s.currentAuthority(ctx, runID)
	if e != nil {
		return a, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authorityLocked(a, true)
}
func (s *AgentBrowserService) Capabilities(ctx context.Context, runID string) (AgentBrowserCapabilities, error) {
	c := AgentBrowserCapabilities{ProtocolVersion: toolgateway.AgentBrowserAuthorityVersion, SupportedActions: []string{"browser_status", "browser_navigate", "browser_snapshot", "browser_click", "browser_type", "browser_screenshot", "browser_scroll", "browser_key"}}
	a, e := s.currentAuthority(ctx, runID)
	if e != nil {
		c.RefusalReason = e.Error()
		c.UnavailableCode = s.unavailableCode(ctx, runID)
		return c, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, e = s.authorityLocked(a, false)
	slot := s.slots[runID]
	if e == nil && slot != nil {
		c.Available = true
		c.CanStart = slot.runtime == nil
		return c, nil
	}
	if !s.closed && slot != nil && (agentBrowserCleanupComplete(slot) || agentBrowserUnusedColdSlot(slot)) {
		c.Available = true
		c.CanStart = true
		return c, nil
	}
	if e != nil {
		c.RefusalReason = e.Error()
		c.UnavailableCode = "session_unavailable"
	}
	return c, nil
}

func (s *AgentBrowserService) unavailableCode(ctx context.Context, runID string) string {
	if !browserruntime.AgentBrowserPlatformAvailable() {
		return "platform_unsupported"
	}
	if s == nil || s.store == nil || !s.available {
		return "runtime_unavailable"
	}
	permission, err := s.store.GetRunExecutionPermission(ctx, runID)
	if err == nil && permission.Mode != domain.RunExecutionPermissionFull {
		return "full_access_required"
	}
	return "run_unavailable"
}
func (s *AgentBrowserService) projectSlotLocked(slot *agentBrowserSlot) AgentBrowserView {
	v := slot.view
	if slot.runtime != nil {
		r := slot.runtime.Status()
		if !slot.closed {
			v.State = r.State
		}
		v.CanonicalURL = r.CanonicalURL
		v.DocumentEpoch = r.DocumentEpoch
		v.Product = r.Product
	}
	if v.DocumentEpoch != slot.titleEpoch || v.CanonicalURL != slot.titleURL {
		v.Title = ""
	}
	if v.DocumentEpoch != slot.screenshotEpoch || v.CanonicalURL != slot.screenshotURL {
		v.ArtifactLocator = ""
		v.ScreenshotSHA256 = ""
		v.ScreenshotBytes = 0
	}
	return v
}
func (s *AgentBrowserService) observeRuntimeDone(slot *agentBrowserSlot, r agentBrowserRuntime) {
	<-r.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cleanup, e := r.Close(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	slot.closed = true
	slot.view.Cleanup = &cleanup
	slot.view.State = "closed"
	slot.view.UpdatedAt = time.Now().UTC()
	if e != nil || cleanup.CleanupPending {
		slot.view.State = "cleanup_pending"
	}
}
func (s *AgentBrowserService) GetStatus(ctx context.Context, runID string) (AgentBrowserView, error) {
	if s == nil || s.store == nil {
		return AgentBrowserView{}, agentBrowserUnavailable("Agent browser unavailable")
	}
	if _, e := s.store.GetRun(ctx, runID); e != nil {
		return AgentBrowserView{}, e
	}
	c, e := s.Capabilities(ctx, runID)
	if e != nil {
		return AgentBrowserView{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v := AgentBrowserView{Version: "agent_browser_status.v1", RunID: runID, State: "unavailable", Headless: s.options.Headless}
	if slot := s.slots[runID]; slot != nil {
		v = s.projectSlotLocked(slot)
	}
	v.Capabilities = c
	return v, nil
}
func (s *AgentBrowserService) Close(ctx context.Context, runID, sessionID string) (AgentBrowserView, error) {
	if s == nil {
		return AgentBrowserView{}, agentBrowserUnavailable("Agent browser unavailable")
	}
	s.mu.Lock()
	slot := s.sessions[sessionID]
	if slot == nil || slot.authority.RunID != runID {
		s.mu.Unlock()
		return AgentBrowserView{}, agentBrowserUnavailable("Agent browser close requires the exact session")
	}
	slot.closed = true
	slot.view.State = "closing"
	r := slot.runtime
	s.mu.Unlock()
	if r != nil {
		r.Cancel()
	}
	slot.action.Lock()
	defer slot.action.Unlock()
	s.mu.Lock()
	r = slot.runtime
	launchAttempted := slot.launchAttempted
	var launchCleanup *browserruntime.AgentBrowserCleanup
	if slot.view.Cleanup != nil {
		receipt := *slot.view.Cleanup
		launchCleanup = &receipt
	}
	s.mu.Unlock()
	cleanup := browserruntime.AgentBrowserNoProcessCleanup(sessionID)
	var e error
	if r == nil && launchAttempted {
		cleanup.TreeReaped = false
		cleanup.ProcessScopeVerified = false
		cleanup.ProfileRemoved = false
		cleanup.CleanupPending = true
		if launchCleanup != nil && launchCleanup.SessionID == sessionID {
			cleanup = *launchCleanup
		}
		cleanup.CleanupPending = cleanup.CleanupPending || !cleanup.ProcessesStopped() || !cleanup.ProfileRemoved
		if cleanup.CleanupPending {
			e = agentBrowserUnavailable("browser launch failed without complete cleanup evidence; session cannot be reopened")
		}
	}
	if r != nil {
		cleanup, e = r.Close(ctx)
	}
	s.mu.Lock()
	slot.view.Cleanup = &cleanup
	slot.view.State = "closed"
	if cleanup.CleanupPending || e != nil {
		slot.view.State = "cleanup_pending"
	}
	slot.view.UpdatedAt = time.Now().UTC()
	v := s.projectSlotLocked(slot)
	s.mu.Unlock()
	return v, e
}
func (s *AgentBrowserService) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	all := make([]toolgateway.AgentBrowserCallAuthority, 0, len(s.sessions))
	for _, slot := range s.sessions {
		all = append(all, slot.authority)
	}
	s.mu.Unlock()
	var e error
	for _, a := range all {
		_, x := s.Close(ctx, a.RunID, a.BrowserSessionID)
		e = errors.Join(e, x)
	}
	return e
}
func (s *AgentRunner) WithAgentBrowser(service *AgentBrowserService) *AgentRunner {
	if service != nil {
		service.WithRunWorktree(s.drydocks)
	}
	if s != nil {
		s.agentBrowser = service
		if s.tools != nil {
			s.tools.WithAgentBrowserExecutor(service)
		}
	}
	return s
}
func (s *RunExecutionHandoffService) WithAgentBrowser(service *AgentBrowserService) *RunExecutionHandoffService {
	if s != nil && s.supervisor != nil {
		s.supervisor.WithAgentBrowser(service)
	}
	return s
}
func (s *AgentRunner) agentBrowserCapabilities(ctx context.Context, turn domain.SupervisorTurn) (toolgateway.BrowserActionCapabilities, json.RawMessage, error) {
	a, e := s.agentBrowser.authorityForAdvertisement(ctx, turn.Run.ID)
	if e != nil {
		return toolgateway.BrowserActionCapabilities{}, nil, nil
	}
	if a.RootAgentID != turn.Agent.ID || a.ModeRevision != turn.Mode.Revision {
		return toolgateway.BrowserActionCapabilities{}, nil, nil
	}
	b, e := json.Marshal(a)
	return toolgateway.BrowserActionCapabilities{ProtocolVersion: toolgateway.AgentBrowserAuthorityVersion, Generation: a.Generation, Available: true}, b, e
}

func refreshBrowserModelTools(existing []llm.ToolSpec, c toolgateway.BrowserActionCapabilities) []llm.ToolSpec {
	out := make([]llm.ToolSpec, 0, len(existing)+8)
	for _, tool := range existing {
		if !toolgateway.IsBrowserActionTool(toolgateway.ToolName(tool.Name)) {
			out = append(out, tool)
		}
	}
	if c.Available {
		names := toolgateway.BrowserActionToolNames()
		definition := toolgateway.BrowserActionToolDefinition
		if c.ProtocolVersion == toolgateway.AgentBrowserAuthorityVersion {
			names = append(names, toolgateway.BrowserScrollTool, toolgateway.BrowserKeyTool)
			definition = toolgateway.AgentBrowserToolDefinition
		}
		for _, name := range names {
			d, _ := definition(name)
			out = append(out, llm.ToolSpec{Name: string(name), Description: d.Description, Parameters: d.InputSchema})
		}
	}
	return out
}

// BrowserHeadedFromEnvironment reports whether the operator requested the
// takeover mode: the agent browser runs as a visible window on the operator's
// desktop so a human can log in or handle waiting_user states manually. The
// window shares the same disposable session profile, so login state persists
// for the session and dies with the profile at cleanup; it grants no
// additional authority to the agent. Opt-in via
// CYBERAGENT_BROWSER_HEADED=1|true at the composition root.
func BrowserHeadedFromEnvironment() bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("CYBERAGENT_BROWSER_HEADED")))
	return value == "1" || value == "true"
}
