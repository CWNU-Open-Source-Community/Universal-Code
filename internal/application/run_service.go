package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/webevidence"
)

type RunStore interface {
	CreateMissionRun(ctx context.Context, mission domain.Mission, run domain.Run,
		mode domain.RunModeSnapshot, linkedSession session.Session, createSession bool,
		initialEvents []events.Event) error
	GetMission(ctx context.Context, id string) (domain.Mission, error)
	GetRun(ctx context.Context, id string) (domain.Run, error)
	GetRunMode(ctx context.Context, runID string) (domain.RunModeSnapshot, error)
	GetRunModeSnapshot(ctx context.Context, id string) (domain.RunModeSnapshot, error)
	GetRunModeOperation(ctx context.Context, keyDigest string) (domain.RunModeOperation, bool, error)
	GetSession(ctx context.Context, id string) (session.Session, error)
	ListRuns(ctx context.Context, filter domain.RunFilter) ([]domain.Run, error)
	TransitionRun(ctx context.Context, run domain.Run, expected domain.RunStatus, event events.Event) error
	TransitionRunPhase(ctx context.Context, snapshot domain.RunModeSnapshot,
		operation domain.RunModeOperation, event events.Event) (domain.RunModeSnapshot, bool, error)
	ListRunEvents(ctx context.Context, runID string) ([]events.Event, error)
}

type RunService struct {
	store            RunStore
	lifecycleHooks   *hooks.Engine
	runtimeAuthority *domain.ExecutionPermissionRuntimeAuthority
}

type CreateRunRequest struct {
	Goal              string
	Profile           string
	Surface           string
	Phase             string
	WorkspaceID       string
	SessionID         string
	ModelRoute        string
	Interactive       bool
	NetworkMode       string
	AllowedTargets    []string
	WebAccessMode     string
	WebAllowedTargets []string
	Budget            domain.Budget
	RequestedBudget   *domain.Budget
	RequestedBy       string
	// ProjectConfig is the validated, narrowed .prayu snapshot. It can only
	// reduce the requested budget and profiles; any widening was already
	// rejected by the caller, and the Run pins this exact view at creation.
	ProjectConfig *projectconfig.Effective
	// ProjectInstructions is discovered inside the Workspace boundary and pins
	// workflow-only guidance for deterministic Run context.
	ProjectInstructions *projectconfig.InstructionSnapshot
	// ContinuityContext is an explicitly selected immutable restore point. It
	// is injected as untrusted historical data and carries no authorization.
	ContinuityContext *contextmgr.ContinuitySnapshot
}

func NewRunService(store RunStore) *RunService {
	return &RunService{store: store}
}

func (s *RunService) WithLifecycleHooks(engine *hooks.Engine) *RunService {
	if s != nil {
		s.lifecycleHooks = engine
	}
	return s
}

// WithExecutionPermissionRuntimeAuthority installs the process-local
// revocation authority used by child runtimes such as Full CDP. Terminal Run
// transitions revoke it synchronously before the durable status commit so no
// new privileged child operation can race a terminal Run into existence.
func (s *RunService) WithExecutionPermissionRuntimeAuthority(
	authority *domain.ExecutionPermissionRuntimeAuthority,
) *RunService {
	if s != nil {
		s.runtimeAuthority = authority
	}
	return s
}

type preparedRun struct {
	Mission       domain.Mission
	Run           domain.Run
	Mode          domain.RunModeSnapshot
	Session       session.Session
	CreateSession bool
	InitialEvents []events.Event
}

// applyProjectNarrowing enforces the fail-closed project rules inside the Run
// service: the project snapshot may only reduce the requested budget and
// restrict profiles, and a read-only project forbids write-capable profiles.
// It returns the canonical snapshot bytes pinned into the Run config.
func applyProjectNarrowing(project *projectconfig.Effective, profile domain.Profile, budget *domain.Budget) ([]byte, error) {
	if project == nil {
		return nil, nil
	}
	if len(project.AllowedProfiles) > 0 {
		allowed := false
		for _, value := range project.AllowedProfiles {
			if value == string(profile) {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("project config allowed_profiles does not include the requested profile %q", profile)
		}
	}
	if project.ReadOnly && profile != domain.ProfileReview && profile != domain.ProfileLearn {
		return nil, fmt.Errorf("project config read_only forbids write-capable profile %q", profile)
	}
	if project.MaxTurns > 0 && project.MaxTurns < budget.MaxTurns {
		budget.MaxTurns = project.MaxTurns
	}
	if project.MaxToolCalls > 0 && int64(project.MaxToolCalls) < budget.MaxToolCalls {
		budget.MaxToolCalls = int64(project.MaxToolCalls)
	}
	raw, err := json.Marshal(project)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func projectFingerprintOf(project *projectconfig.Effective) string {
	if project == nil {
		return ""
	}
	return project.Fingerprint()
}

func projectInstructionSnapshotOf(snapshot *projectconfig.InstructionSnapshot) ([]byte, string, error) {
	if snapshot == nil {
		return nil, "", nil
	}
	if err := snapshot.Validate(); err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, "", err
	}
	return raw, snapshot.Fingerprint, nil
}

func continuitySnapshotOf(snapshot *contextmgr.ContinuitySnapshot) ([]byte, string, error) {
	if snapshot == nil {
		return nil, "", nil
	}
	if err := snapshot.Validate(); err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, "", err
	}
	return raw, snapshot.Fingerprint, nil
}

func (s *RunService) Create(ctx context.Context, req CreateRunRequest) (domain.Mission, domain.Run, error) {
	prepared, err := s.prepare(ctx, req)
	if err != nil {
		return domain.Mission{}, domain.Run{}, err
	}
	if prepared.CreateSession {
		if err := executeLifecycleBoundary(ctx, s.lifecycleHooks, hooks.SessionOpened,
			prepared.Run.ID, prepared.Mission.WorkspaceID, map[string]any{
				"session_id": prepared.Session.ID, "source": "run_create",
			}); err != nil {
			return domain.Mission{}, domain.Run{}, err
		}
	}
	if err := s.store.CreateMissionRun(ctx, prepared.Mission, prepared.Run, prepared.Mode, prepared.Session,
		prepared.CreateSession, prepared.InitialEvents); err != nil {
		return domain.Mission{}, domain.Run{}, err
	}
	return prepared.Mission, prepared.Run, nil
}

func (s *RunService) prepare(ctx context.Context, req CreateRunRequest) (preparedRun, error) {
	if s == nil || s.store == nil {
		return preparedRun{}, errors.New("run store is required")
	}
	return prepareRun(ctx, req, s.store.GetSession)
}

func prepareRun(ctx context.Context, req CreateRunRequest,
	getSession func(context.Context, string) (session.Session, error),
) (preparedRun, error) {
	goal := redact.String(strings.TrimSpace(req.Goal))
	if goal == "" {
		return preparedRun{}, errors.New("mission goal is required")
	}
	requestedBy := redact.String(strings.TrimSpace(req.RequestedBy))
	if requestedBy == "" {
		requestedBy = "run_service"
	}
	if !domain.ValidAgentID(requestedBy) || strings.ContainsRune(requestedBy, 0) {
		return preparedRun{}, errors.New("run requester must be normalized and bounded UTF-8")
	}
	profileValue := strings.TrimSpace(req.Profile)
	if profileValue == "" {
		profileValue = string(domain.ProfileCode)
	}
	profile, err := domain.ParseProfile(profileValue)
	if err != nil {
		return preparedRun{}, err
	}
	surfaceValue := strings.TrimSpace(req.Surface)
	if surfaceValue == "" {
		surfaceValue = string(domain.ExecutionSurfaceCode)
	}
	surface, err := domain.ParseExecutionSurface(surfaceValue)
	if err != nil {
		return preparedRun{}, err
	}
	phaseValue := strings.TrimSpace(req.Phase)
	if phaseValue == "" {
		phaseValue = string(domain.ExecutionPhaseDeliver)
	}
	phase, err := domain.ParseExecutionPhase(phaseValue)
	if err != nil {
		return preparedRun{}, err
	}
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	networkMode := strings.ToLower(strings.TrimSpace(req.NetworkMode))
	if networkMode == "" {
		networkMode = "disabled"
	}
	var allowedTargets []string
	switch networkMode {
	case "disabled":
		if len(req.AllowedTargets) != 0 {
			return preparedRun{}, errors.New(
				"disabled Run network mode cannot retain allowed targets")
		}
	case "allowlist":
		allowedTargets, err = webevidence.NormalizeExactAuthorityTargets(req.AllowedTargets)
		if err != nil {
			return preparedRun{}, fmt.Errorf("Run network scope: %w", err)
		}
	default:
		return preparedRun{}, fmt.Errorf("Run network scope: unsupported mode %q", networkMode)
	}
	requestedSessionID := strings.TrimSpace(req.SessionID)
	var linkedSession session.Session
	createSession := requestedSessionID == ""
	if !createSession {
		if getSession == nil {
			return preparedRun{}, errors.New("run session lookup is required")
		}
		linkedSession, err = getSession(ctx, requestedSessionID)
		if err != nil {
			return preparedRun{}, err
		}
		if linkedSession.Status != session.StatusActive {
			return preparedRun{}, errors.New("run session must be active")
		}
		if workspaceID != "" && linkedSession.WorkspaceID != "" && workspaceID != linkedSession.WorkspaceID {
			return preparedRun{}, errors.New("session and requested workspace do not match")
		}
		if workspaceID == "" {
			workspaceID = linkedSession.WorkspaceID
		}
	}
	route := strings.TrimSpace(req.ModelRoute)
	if route == "" {
		if !createSession {
			route = linkedSession.Route
		} else {
			route = string(profile)
		}
	}
	budget := req.Budget
	if budget.MaxTurns == 0 {
		budget.MaxTurns = domain.DefaultBudget().MaxTurns
	}
	if err := budget.Validate(); err != nil {
		return preparedRun{}, err
	}
	projectSnapshot, err := applyProjectNarrowing(req.ProjectConfig, profile, &budget)
	if err != nil {
		return preparedRun{}, err
	}
	projectInstructions, projectInstructionsFingerprint, err :=
		projectInstructionSnapshotOf(req.ProjectInstructions)
	if err != nil {
		return preparedRun{}, fmt.Errorf("project instruction snapshot: %w", err)
	}
	continuityContext, continuityContextFingerprint, err := continuitySnapshotOf(req.ContinuityContext)
	if err != nil {
		return preparedRun{}, fmt.Errorf("continuity context snapshot: %w", err)
	}
	now := time.Now().UTC()
	if createSession {
		linkedSession = session.New(workspaceID, goal, route)
		linkedSession.CreatedAt = now
	} else {
		linkedSession.WorkspaceID = workspaceID
		linkedSession.Route = route
	}
	linkedSession.UpdatedAt = now
	if err := linkedSession.Validate(); err != nil {
		return preparedRun{}, err
	}
	webMode, webTargets, err := normalizeWebAccess(req.WebAccessMode, req.WebAllowedTargets, true)
	if err != nil {
		return preparedRun{}, err
	}
	mission := domain.Mission{
		ID:          idgen.New("mission"),
		Goal:        goal,
		Profile:     profile,
		WorkspaceID: workspaceID,
		Scope: domain.Scope{WebAccessMode: webMode, WebAllowedTargets: webTargets, WorkspaceID: workspaceID, NetworkMode: networkMode,
			AllowedTargets: append([]string(nil), allowedTargets...)},
		CreatedAt: now,
		UpdatedAt: now,
	}
	run := domain.Run{
		ID:        idgen.New("run"),
		MissionID: mission.ID,
		SessionID: linkedSession.ID,
		Status:    domain.RunCreated,
		Config: domain.RunConfig{
			ModelRoute:                     route,
			Interactive:                    req.Interactive,
			RequestedBudget:                req.RequestedBudget,
			ProjectConfig:                  projectSnapshot,
			ProjectConfigFingerprint:       projectFingerprintOf(req.ProjectConfig),
			ProjectInstructions:            projectInstructions,
			ProjectInstructionsFingerprint: projectInstructionsFingerprint,
			ContinuityContext:              continuityContext,
			ContinuityContextFingerprint:   continuityContextFingerprint,
		},
		Budget:    budget,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := mission.Validate(); err != nil {
		return preparedRun{}, err
	}
	if err := run.Validate(); err != nil {
		return preparedRun{}, err
	}
	mode, err := domain.NewInitialRunModeSnapshot(idgen.New("run-mode"), run, mission,
		surface, phase, requestedBy, "initial Run mode", now)
	if err != nil {
		return preparedRun{}, err
	}
	createdEvent, err := events.New(run.ID, mission.ID, events.RunCreatedEvent, "run_service", run.ID, map[string]any{
		"status":       run.Status,
		"profile":      mission.Profile,
		"surface":      mode.Surface,
		"phase":        mode.Phase,
		"network_mode": mission.Scope.NetworkMode,
		"session_id":   run.SessionID,
	})
	if err != nil {
		return preparedRun{}, err
	}
	attachedEvent, err := events.New(run.ID, mission.ID, events.SessionAttachedEvent, "run_service", linkedSession.ID, map[string]any{
		"created":      createSession,
		"route":        linkedSession.Route,
		"workspace_id": linkedSession.WorkspaceID,
	})
	if err != nil {
		return preparedRun{}, err
	}
	return preparedRun{
		Mission: mission, Run: run, Mode: mode, Session: linkedSession, CreateSession: createSession,
		InitialEvents: []events.Event{createdEvent, attachedEvent},
	}, nil
}

func (s *RunService) Start(ctx context.Context, id string) (domain.Run, error) {
	run, err := s.store.GetRun(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Run{}, err
	}
	if run.Status == domain.RunRunning {
		return run, nil
	}
	if run.Status == domain.RunCreated {
		mission, missionErr := s.store.GetMission(ctx, run.MissionID)
		if missionErr != nil {
			return domain.Run{}, missionErr
		}
		if hookErr := executeLifecycleBoundary(ctx, s.lifecycleHooks, hooks.RunStarted,
			run.ID, mission.WorkspaceID, map[string]any{
				"session_id": run.SessionID, "from": run.Status,
				"to": domain.RunRunning, "source": "run_service",
			}); hookErr != nil {
			return domain.Run{}, hookErr
		}
		run, err = s.transition(ctx, run, domain.RunPreparing, "start requested")
		if err != nil {
			return domain.Run{}, err
		}
	}
	return s.transition(ctx, run, domain.RunRunning, "run prepared")
}

func (s *RunService) Pause(ctx context.Context, id string) (domain.Run, error) {
	run, err := s.store.GetRun(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Run{}, err
	}
	if run.Status == domain.RunPaused {
		return run, nil
	}
	return s.transition(ctx, run, domain.RunPaused, "pause requested")
}

func (s *RunService) Resume(ctx context.Context, id string) (domain.Run, error) {
	run, err := s.store.GetRun(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Run{}, err
	}
	if run.Status == domain.RunRunning {
		return run, nil
	}
	return s.transition(ctx, run, domain.RunRunning, "resume requested")
}

func (s *RunService) Cancel(ctx context.Context, id string) (domain.Run, error) {
	run, err := s.store.GetRun(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Run{}, err
	}
	if run.Status == domain.RunCancelled {
		return run, nil
	}
	return s.transition(ctx, run, domain.RunCancelled, "cancel requested")
}

func (s *RunService) Complete(ctx context.Context, id string) (domain.Run, error) {
	run, err := s.store.GetRun(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Run{}, err
	}
	return s.transition(ctx, run, domain.RunCompleted, "run completed")
}

func (s *RunService) Fail(ctx context.Context, id string, reason string) (domain.Run, error) {
	run, err := s.store.GetRun(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Run{}, err
	}
	return s.transition(ctx, run, domain.RunFailed, redact.String(strings.TrimSpace(reason)))
}

func (s *RunService) Get(ctx context.Context, id string) (domain.Mission, domain.Run, error) {
	run, err := s.store.GetRun(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Mission{}, domain.Run{}, err
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return domain.Mission{}, domain.Run{}, err
	}
	return mission, run, nil
}

func (s *RunService) List(ctx context.Context, filter domain.RunFilter) ([]domain.Run, error) {
	return s.store.ListRuns(ctx, filter)
}

func (s *RunService) Events(ctx context.Context, runID string) ([]events.Event, error) {
	runID = strings.TrimSpace(runID)
	if _, err := s.store.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	return s.store.ListRunEvents(ctx, runID)
}

func (s *RunService) transition(ctx context.Context, run domain.Run, target domain.RunStatus, reason string) (domain.Run, error) {
	expected := run.Status
	if expected == target {
		if run.Terminal() && s.runtimeAuthority != nil {
			s.runtimeAuthority.RevokeRun(run.ID)
		}
		return run, nil
	}
	if target == domain.RunCancelled || target == domain.RunCompleted ||
		target == domain.RunFailed {
		mission, err := s.store.GetMission(ctx, run.MissionID)
		if err != nil {
			return domain.Run{}, err
		}
		if err := executeLifecycleBoundary(ctx, s.lifecycleHooks, hooks.RunCompleted,
			run.ID, mission.WorkspaceID, map[string]any{
				"session_id": run.SessionID, "from": expected, "to": target,
				"source": "run_service",
			}); err != nil {
			return domain.Run{}, err
		}
	}
	if err := run.Transition(target, time.Now().UTC()); err != nil {
		return domain.Run{}, err
	}
	event, err := events.New(run.ID, run.MissionID, events.RunStatusChangedEvent, "run_service", run.ID, map[string]any{
		"from":   expected,
		"to":     target,
		"reason": redact.String(reason),
	})
	if err != nil {
		return domain.Run{}, err
	}
	if run.Terminal() && s.runtimeAuthority != nil {
		// Revocation deliberately precedes the durable transition. If the store
		// commit fails, fail closed and require an explicit activation again.
		s.runtimeAuthority.RevokeRun(run.ID)
	}
	if err := s.store.TransitionRun(ctx, run, expected, event); err != nil {
		return domain.Run{}, err
	}
	if target == domain.RunCancelled || target == domain.RunCompleted ||
		target == domain.RunFailed {
		// Best-effort: terminal runs release every open monetary
		// reservation so the aggregate can never leak capacity.
		if releaser, ok := s.store.(interface {
			ReleaseOpenMonetaryReservations(context.Context, string) (int, error)
		}); ok {
			_, _ = releaser.ReleaseOpenMonetaryReservations(ctx, run.ID)
		}
		// Best-effort: a terminal run cancels every open dependency wait
		// (parent-cancel fan-down) with exactly-once wake receipts.
		if reconciler, ok := s.store.(interface {
			ReconcileDependencyEdges(context.Context, string) ([]domain.DependencyWake, error)
		}); ok {
			_, _ = reconciler.ReconcileDependencyEdges(ctx, run.ID)
		}
	}
	return run, nil
}
