package application

import (
	"context"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/executionauth"
	terminalruntime "cyberagent-workbench/internal/terminal"
)

type TerminalSessionInventory interface {
	ActiveSessions() []terminalruntime.Session
}

// TerminalPreferencesController applies native consent to the shared terminal.
// Automatic consent renews bounded leases; the underlying controller continues
// to validate the current Full permission, Run, workspace and live session.
type TerminalPreferencesController struct {
	mu         sync.Mutex
	base       DebugTerminalAgentInputController
	settings   DesktopTerminalSettingsStore
	sessions   TerminalSessionInventory
	current    *DesktopTerminalSettings
	suppressed map[string]string
	recent     map[string][]DebugTerminalAgentInputBinding
	writes     map[string]int
	stopped    bool
	now        func() time.Time
}

func NewTerminalPreferencesController(base DebugTerminalAgentInputController, settings DesktopTerminalSettingsStore, sessions TerminalSessionInventory) *TerminalPreferencesController {
	return &TerminalPreferencesController{base: base, settings: settings, sessions: sessions, suppressed: make(map[string]string), recent: make(map[string][]DebugTerminalAgentInputBinding), writes: make(map[string]int), now: time.Now}
}

func (p *TerminalPreferencesController) load(ctx context.Context) (DesktopTerminalSettings, error) {
	if p.stopped {
		return DesktopTerminalSettings{}, apperror.New(apperror.CodeUnavailable, "terminal control has shut down")
	}
	snapshot, err := p.settings.Load(ctx)
	if err != nil {
		p.base.Shutdown(ctx)
		return DesktopTerminalSettings{}, err
	}
	p.apply(ctx, snapshot.Settings)
	return snapshot.Settings, nil
}

func (p *TerminalPreferencesController) apply(ctx context.Context, settings DesktopTerminalSettings) {
	if p.current != nil && p.current.AgentControl != settings.AgentControl {
		p.base.Shutdown(ctx)
		clear(p.suppressed)
		clear(p.recent)
		clear(p.writes)
	}
	p.current = &settings
}

// Saving consent and dispatching terminal input share the same lock, so once a
// save returns, no further input can use the previous authorization preference.
func (p *TerminalPreferencesController) SaveSettings(ctx context.Context, revision int64, settings DesktopTerminalSettings) (DesktopTerminalSettingsSnapshot, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot, replayed, err := p.settings.Save(ctx, revision, settings)
	if err == nil {
		p.apply(ctx, snapshot.Settings)
	}
	return snapshot, replayed, err
}

func (p *TerminalPreferencesController) remember(binding DebugTerminalAgentInputBinding) {
	recent := p.recent[binding.RunID]
	if len(recent) > 0 && recent[len(recent)-1].ID == binding.ID {
		return
	}
	recent = append(recent, binding)
	if len(recent) > 2 {
		delete(p.writes, recent[0].ID)
		recent = recent[len(recent)-2:]
	}
	p.recent[binding.RunID] = recent
}

func (p *TerminalPreferencesController) Grant(ctx context.Context, request GrantDebugTerminalAgentInputRequest) (DebugTerminalAgentInputBinding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	settings, err := p.load(ctx)
	if err != nil {
		return DebugTerminalAgentInputBinding{}, err
	}
	if settings.AgentControl == "disabled" {
		return DebugTerminalAgentInputBinding{}, terminalPreferenceDenied()
	}
	request.TTL = time.Duration(settings.ManualTTLSeconds) * time.Second
	if settings.AgentControl == "full" {
		request.TTL = executionauth.MaxTerminalInputLeaseTTL
	}
	binding, err := p.base.Grant(ctx, request)
	if err == nil {
		delete(p.suppressed, request.RunID)
		p.remember(binding)
	}
	return binding, err
}

func (p *TerminalPreferencesController) active(ctx context.Context, runID string, settings DesktopTerminalSettings) (DebugTerminalAgentInputBinding, bool, error) {
	if settings.AgentControl == "disabled" {
		return DebugTerminalAgentInputBinding{}, false, nil
	}
	binding, found, err := p.base.Active(ctx, runID)
	if err != nil || settings.AgentControl != "full" {
		return binding, found, err
	}
	if found && binding.ExpiresAt.Sub(p.now()) > 30*time.Second && p.writes[binding.ID] < 200 {
		p.remember(binding)
		return binding, true, nil
	}
	if found {
		err = p.base.Revoke(ctx, RevokeDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, BindingID: binding.ID, RequestedBy: "desktop_operator", OperatorConfirmed: true})
		if err != nil {
			return DebugTerminalAgentInputBinding{}, false, err
		}
	}
	sessionID := ""
	for _, session := range p.sessions.ActiveSessions() {
		if session.Scope.RunID != runID || session.State != terminalruntime.SessionRunning {
			continue
		}
		if sessionID != "" {
			return DebugTerminalAgentInputBinding{}, false, apperror.New(apperror.CodeConflict, "multiple user terminals belong to this Run")
		}
		sessionID = session.ID
	}
	if sessionID == "" || p.suppressed[runID] == sessionID {
		return DebugTerminalAgentInputBinding{}, false, nil
	}
	binding, err = p.base.Grant(ctx, GrantDebugTerminalAgentInputRequest{ProtocolVersion: DebugTerminalAgentInputProtocolVersion, RunID: runID, TerminalSessionID: sessionID,
		RequestedBy: "desktop_operator", TTL: executionauth.MaxTerminalInputLeaseTTL, ConfirmFullAccess: true, ConfirmAgentTerminalInput: true})
	if err != nil {
		return DebugTerminalAgentInputBinding{}, false, err
	}
	p.remember(binding)
	return binding, true, nil
}

func (p *TerminalPreferencesController) Active(ctx context.Context, runID string) (DebugTerminalAgentInputBinding, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	settings, err := p.load(ctx)
	if err != nil {
		return DebugTerminalAgentInputBinding{}, false, err
	}
	return p.active(ctx, runID, settings)
}

func (p *TerminalPreferencesController) Write(ctx context.Context, request WriteDebugTerminalAgentInputRequest) (DebugTerminalAgentInputWriteResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	settings, err := p.load(ctx)
	if err != nil {
		return DebugTerminalAgentInputWriteResult{}, err
	}
	if settings.AgentControl == "disabled" {
		return DebugTerminalAgentInputWriteResult{}, terminalPreferenceDenied()
	}
	result, err := p.base.Write(ctx, request)
	if err == nil {
		p.writes[request.BindingID]++
	}
	return result, err
}

func (p *TerminalPreferencesController) Read(ctx context.Context, request ReadDebugTerminalAgentOutputRequest) (DebugTerminalAgentOutputResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	settings, err := p.load(ctx)
	if err != nil {
		return DebugTerminalAgentOutputResult{}, err
	}
	if settings.AgentControl == "disabled" {
		return DebugTerminalAgentOutputResult{}, terminalPreferenceDenied()
	}
	return p.base.Read(ctx, request)
}

func (p *TerminalPreferencesController) Revoke(ctx context.Context, request RevokeDebugTerminalAgentInputRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if request.ProtocolVersion != DebugTerminalAgentInputProtocolVersion || !request.OperatorConfirmed || !validDebugTerminalOperator(request.RequestedBy) {
		return terminalPreferenceDenied()
	}
	for runID, recent := range p.recent {
		for _, binding := range recent {
			if binding.ID == request.BindingID {
				p.suppressed[runID] = binding.TerminalSessionID
				current, found, err := p.base.Active(ctx, runID)
				if err != nil {
					return err
				}
				if !found || current.TerminalSessionID != binding.TerminalSessionID {
					return nil
				}
				request.BindingID = current.ID
				return p.base.Revoke(ctx, request)
			}
		}
	}
	return p.base.Revoke(ctx, request)
}

func (p *TerminalPreferencesController) Reconcile(ctx context.Context) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	settings, err := p.load(ctx)
	if err != nil {
		return p.base.Reconcile(ctx)
	}
	revoked := p.base.Reconcile(ctx)
	live := make(map[string]bool)
	for _, session := range p.sessions.ActiveSessions() {
		live[session.Scope.RunID] = true
		if settings.AgentControl == "full" {
			_, _, _ = p.active(ctx, session.Scope.RunID, settings)
		}
	}
	for runID, recent := range p.recent {
		if !live[runID] {
			for _, binding := range recent {
				delete(p.writes, binding.ID)
			}
			delete(p.recent, runID)
			delete(p.suppressed, runID)
		}
	}
	return revoked
}

func (p *TerminalPreferencesController) Shutdown(ctx context.Context) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped = true
	clear(p.recent)
	clear(p.suppressed)
	clear(p.writes)
	return p.base.Shutdown(ctx)
}

func terminalPreferenceDenied() error {
	return apperror.New(apperror.CodePolicyDenied, "Agent access to the user terminal is disabled in terminal settings")
}
