package application

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/durableoperation"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

type ControlledRunCreationStore interface {
	GetMission(context.Context, string) (domain.Mission, error)
	GetRun(context.Context, string) (domain.Run, error)
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetSession(context.Context, string) (session.Session, error)
	GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error)
	GetRunCreationOperation(context.Context, string) (domain.RunCreationOperation, bool, error)
	CreateMissionRunWithOperation(context.Context, domain.Mission, domain.Run,
		domain.RunModeSnapshot, session.Session, []events.Event,
		domain.RunCreationOperation, domain.InitialThreadModelRoutePin) (
		domain.RunCreationOperation, bool, error)
}

type ControlledRunCreationService struct {
	store          ControlledRunCreationStore
	lifecycleHooks *hooks.Engine
}

type ControlledRunCreationRequest struct {
	Version           string
	Goal              string
	WorkspaceID       string
	Profile           string
	Surface           string
	Phase             string
	NetworkMode       string
	AllowedTargets    []string
	WebAccessMode     string
	WebAllowedTargets []string
	ModelRoute        string
	Budget            *domain.TaskBudgetSettings
	// CustomModelProvider and ExpectedProviderDefinitionRevision are internal
	// admission facts supplied by the model Registry. They are deliberately not
	// copied from the renderer's JSON request.
	CustomModelProvider                bool
	ExpectedProviderDefinitionRevision uint64
	OperationKey                       string
	RequestedBy                        string
}

type ControlledRunCreationResult struct {
	Mission  domain.Mission
	Run      domain.Run
	Session  session.Session
	Mode     domain.RunModeSnapshot
	Replayed bool
}

type normalizedControlledRunCreationRequest struct {
	Goal                 string
	WorkspaceID          string
	Profile              domain.Profile
	Surface              domain.ExecutionSurface
	Phase                domain.ExecutionPhase
	NetworkMode          string
	AllowedTargets       []string
	WebAccessMode        domain.WebAccessMode
	WebAllowedTargets    []string
	ModelRoute           string
	InitialModelRoutePin domain.InitialThreadModelRoutePin
	OperationKey         string
	RequestedBy          string
	Budget               domain.Budget
}

func NewControlledRunCreationService(store ControlledRunCreationStore) *ControlledRunCreationService {
	return &ControlledRunCreationService{store: store}
}

func (s *ControlledRunCreationService) WithLifecycleHooks(
	engine *hooks.Engine,
) *ControlledRunCreationService {
	if s != nil {
		s.lifecycleHooks = engine
	}
	return s
}

func (s *ControlledRunCreationService) Create(ctx context.Context,
	request ControlledRunCreationRequest,
) (ControlledRunCreationResult, error) {
	if s == nil || s.store == nil {
		return ControlledRunCreationResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "controlled Run creation store is required")
	}
	normalized, err := normalizeControlledRunCreationRequest(request)
	if err != nil {
		return ControlledRunCreationResult{}, err
	}
	keyDigest := runmutation.RunCreationOperationDigest(normalized.OperationKey)
	requestFingerprint := domain.ControlledCreationFingerprint(normalized.Goal,
		normalized.WorkspaceID, string(normalized.Profile), string(normalized.Surface),
		string(normalized.Phase), normalized.NetworkMode, normalized.AllowedTargets,
		normalized.ModelRoute, normalized.RequestedBy, normalized.Budget, domain.Scope{WebAccessMode: normalized.WebAccessMode, WebAllowedTargets: normalized.WebAllowedTargets})

	if existing, found, err := s.store.GetRunCreationOperation(ctx, keyDigest); err != nil {
		return ControlledRunCreationResult{}, apperror.Normalize(err)
	} else if found {
		if err := validateControlledRunCreationIntent(existing, keyDigest, requestFingerprint,
			normalized.WorkspaceID, normalized.RequestedBy); err != nil {
			return ControlledRunCreationResult{}, err
		}
		return s.loadResult(ctx, existing, true)
	}

	workspace, err := s.store.GetWorkspaceInfo(ctx, normalized.WorkspaceID)
	if err != nil {
		return ControlledRunCreationResult{}, apperror.Normalize(err)
	}
	if workspace.ID != normalized.WorkspaceID || strings.TrimSpace(workspace.Name) == "" ||
		strings.TrimSpace(workspace.RootPath) == "" {
		return ControlledRunCreationResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "controlled Run workspace record is invalid")
	}
	project, rejected, err := projectconfig.ResolveWorkspace(ctx, workspace.RootPath, normalized.Profile, normalized.Budget, toolgateway.TypedActionIDs())
	if err != nil {
		return ControlledRunCreationResult{}, apperror.New(apperror.CodeFailedPrecondition, "project configuration could not be safely loaded; fix .prayu/config.yaml")
	}
	if len(rejected) != 0 {
		return ControlledRunCreationResult{}, apperror.New(apperror.CodeFailedPrecondition, "project configuration rejected: "+rejected[0].Field+": "+rejected[0].Reason)
	}
	instructions, err := projectconfig.DiscoverInstructions(ctx, workspace.RootPath, ".")
	if err != nil {
		return ControlledRunCreationResult{}, apperror.New(apperror.CodeFailedPrecondition, "project instruction discovery failed closed")
	}

	prepared, err := prepareRun(ctx, CreateRunRequest{
		Goal: normalized.Goal, Profile: string(normalized.Profile),
		Surface: string(normalized.Surface), Phase: string(normalized.Phase),
		WorkspaceID: normalized.WorkspaceID, ModelRoute: normalized.ModelRoute,
		NetworkMode:   normalized.NetworkMode,
		WebAccessMode: string(normalized.WebAccessMode), WebAllowedTargets: normalized.WebAllowedTargets,
		AllowedTargets: append([]string(nil), normalized.AllowedTargets...),
		Interactive:    true, Budget: normalized.Budget, RequestedBudget: &normalized.Budget, RequestedBy: normalized.RequestedBy,
		ProjectConfig:       project,
		ProjectInstructions: &instructions,
	}, nil)
	if err != nil {
		return ControlledRunCreationResult{}, apperror.Normalize(err)
	}
	operation := domain.RunCreationOperation{
		ProtocolVersion: domain.RunCreationProtocolVersion,
		KeyDigest:       keyDigest, RequestFingerprint: requestFingerprint,
		MissionID: prepared.Mission.ID, RunID: prepared.Run.ID,
		SessionID: prepared.Session.ID, WorkspaceID: normalized.WorkspaceID,
		RequestedBy: normalized.RequestedBy, CreatedAt: prepared.Run.CreatedAt,
	}
	if err := executeLifecycleBoundary(ctx, s.lifecycleHooks, hooks.SessionOpened,
		prepared.Run.ID, prepared.Mission.WorkspaceID, map[string]any{
			"session_id": prepared.Session.ID, "source": "controlled_run_create",
		}); err != nil {
		return ControlledRunCreationResult{}, err
	}
	stored, replayed, err := s.store.CreateMissionRunWithOperation(ctx,
		prepared.Mission, prepared.Run, prepared.Mode, prepared.Session,
		prepared.InitialEvents, operation, normalized.InitialModelRoutePin)
	if err != nil {
		return ControlledRunCreationResult{}, apperror.Normalize(err)
	}
	if err := validateControlledRunCreationIntent(stored, keyDigest, requestFingerprint,
		normalized.WorkspaceID, normalized.RequestedBy); err != nil {
		return ControlledRunCreationResult{}, err
	}
	if replayed {
		return s.loadResult(ctx, stored, true)
	}
	if stored.MissionID != prepared.Mission.ID || stored.RunID != prepared.Run.ID ||
		stored.SessionID != prepared.Session.ID {
		return ControlledRunCreationResult{}, apperror.New(
			apperror.CodeConflict, "controlled Run creation identity changed during commit")
	}
	return ControlledRunCreationResult{Mission: prepared.Mission, Run: prepared.Run,
		Session: prepared.Session, Mode: prepared.Mode}, nil
}

func (s *ControlledRunCreationService) loadResult(ctx context.Context,
	operation domain.RunCreationOperation, replayed bool,
) (ControlledRunCreationResult, error) {
	mission, err := s.store.GetMission(ctx, operation.MissionID)
	if err != nil {
		return ControlledRunCreationResult{}, apperror.Normalize(err)
	}
	run, err := s.store.GetRun(ctx, operation.RunID)
	if err != nil {
		return ControlledRunCreationResult{}, apperror.Normalize(err)
	}
	linkedSession, err := s.store.GetSession(ctx, operation.SessionID)
	if err != nil {
		return ControlledRunCreationResult{}, apperror.Normalize(err)
	}
	mode, err := s.store.GetRunMode(ctx, operation.RunID)
	if err != nil {
		return ControlledRunCreationResult{}, apperror.Normalize(err)
	}
	if run.MissionID != mission.ID || run.SessionID != linkedSession.ID ||
		mission.ID != operation.MissionID || mission.WorkspaceID != operation.WorkspaceID ||
		linkedSession.WorkspaceID != operation.WorkspaceID || mode.RunID != run.ID ||
		mode.MissionID != mission.ID || mode.Profile != mission.Profile ||
		mode.Revision != 1 || mode.RequestedBy != operation.RequestedBy ||
		mode.Scope.WorkspaceID != operation.WorkspaceID ||
		!sameControlledRunScope(mode.Scope, mission.Scope) ||
		!validControlledRunNetworkScope(mission.Scope) ||
		mission.Scope.WorkspaceID != operation.WorkspaceID ||
		run.Status != domain.RunCreated || run.StartedAt != nil || run.FinishedAt != nil ||
		linkedSession.Status != session.StatusActive || linkedSession.Title != mission.Goal ||
		!run.Config.Interactive || run.Config.ModelRoute == "" ||
		linkedSession.Route != run.Config.ModelRoute ||
		!mission.CreatedAt.Equal(operation.CreatedAt) ||
		!mission.UpdatedAt.Equal(operation.CreatedAt) ||
		!run.CreatedAt.Equal(operation.CreatedAt) || !run.UpdatedAt.Equal(operation.CreatedAt) ||
		!linkedSession.CreatedAt.Equal(operation.CreatedAt) ||
		!linkedSession.UpdatedAt.Equal(operation.CreatedAt) ||
		!mode.CreatedAt.Equal(operation.CreatedAt) ||
		operation.RequestFingerprint != domain.ControlledCreationFingerprint(
			mission.Goal, mission.WorkspaceID, string(mission.Profile), string(mode.Surface),
			string(mode.Phase), mission.Scope.NetworkMode, mission.Scope.AllowedTargets,
			run.Config.ModelRoute, operation.RequestedBy, run.Config.CreationBudget(), mission.Scope) {
		return ControlledRunCreationResult{}, apperror.New(
			apperror.CodeConflict, "stored controlled Run creation binding is inconsistent")
	}
	if run.Config.RequestedBudget != nil {
		if _, err := PinnedTaskConfiguration(run, mission.WorkspaceID, mission.Profile); err != nil {
			return ControlledRunCreationResult{}, err
		}
	} else if run.Budget != domain.DefaultBudget() {
		return ControlledRunCreationResult{}, apperror.New(apperror.CodeConflict, "legacy controlled budget binding is invalid")
	}
	return ControlledRunCreationResult{Mission: mission, Run: run,
		Session: linkedSession, Mode: mode, Replayed: replayed}, nil
}

func normalizeControlledRunCreationRequest(request ControlledRunCreationRequest) (
	normalizedControlledRunCreationRequest, error,
) {
	if request.Version != domain.RunCreationProtocolVersion {
		return normalizedControlledRunCreationRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "unsupported controlled Run creation version")
	}
	if !utf8.ValidString(request.Goal) || strings.ContainsRune(request.Goal, 0) {
		return normalizedControlledRunCreationRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "Run goal must be valid UTF-8 without NUL bytes")
	}
	rawGoal := strings.TrimSpace(request.Goal)
	if rawGoal == "" || len([]byte(rawGoal)) > domain.MaxRunCreationGoalBytes {
		return normalizedControlledRunCreationRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "Run goal must contain between 1 and 4096 bytes")
	}
	goal := redact.String(rawGoal)
	workspaceID := strings.TrimSpace(request.WorkspaceID)
	if workspaceID != request.WorkspaceID || !domain.ValidAgentID(workspaceID) ||
		strings.ContainsRune(workspaceID, 0) {
		return normalizedControlledRunCreationRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "Run workspace id is invalid")
	}
	profileValue := strings.TrimSpace(request.Profile)
	if profileValue == "" {
		profileValue = string(domain.ProfileCode)
	}
	profile, err := domain.ParseProfile(profileValue)
	if err != nil {
		return normalizedControlledRunCreationRequest{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "Run profile is invalid", err)
	}
	if request.Profile != "" && request.Profile != string(profile) {
		return normalizedControlledRunCreationRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "Run profile must use its canonical value")
	}
	modelRoute := strings.TrimSpace(request.ModelRoute)
	initialModelRoutePin := domain.InitialThreadModelRoutePin{}
	if modelRoute == "" {
		modelRoute = string(profile)
	}
	if request.ModelRoute != "" && (request.ModelRoute != modelRoute ||
		!validControlledModelRoute(modelRoute)) {
		return normalizedControlledRunCreationRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "Run model route is invalid")
	}
	if request.ModelRoute == "" {
		if request.CustomModelProvider || request.ExpectedProviderDefinitionRevision != 0 {
			return normalizedControlledRunCreationRequest{}, apperror.New(
				apperror.CodeInvalidArgument,
				"default Run model route cannot carry Provider admission metadata")
		}
	} else {
		parts := strings.SplitN(modelRoute, "/", 2)
		initialModelRoutePin = domain.InitialThreadModelRoutePin{
			Provider: parts[0], Model: parts[1],
			CustomProvider:                     request.CustomModelProvider,
			ExpectedProviderDefinitionRevision: request.ExpectedProviderDefinitionRevision,
		}
		if err := initialModelRoutePin.Validate(); err != nil {
			return normalizedControlledRunCreationRequest{}, apperror.Wrap(
				apperror.CodeInvalidArgument, "Run model route admission is invalid", err)
		}
	}
	surfaceValue := strings.TrimSpace(request.Surface)
	if surfaceValue == "" {
		surfaceValue = string(domain.ExecutionSurfaceCode)
	}
	surface, err := domain.ParseExecutionSurface(surfaceValue)
	if err != nil {
		return normalizedControlledRunCreationRequest{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "Run surface is invalid", err)
	}
	if request.Surface != "" && request.Surface != string(surface) {
		return normalizedControlledRunCreationRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "Run surface must use its canonical value")
	}
	phaseValue := strings.TrimSpace(request.Phase)
	if phaseValue == "" {
		phaseValue = string(domain.ExecutionPhaseDeliver)
	}
	phase, err := domain.ParseExecutionPhase(phaseValue)
	if err != nil {
		return normalizedControlledRunCreationRequest{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "Run phase is invalid", err)
	}
	if request.Phase != "" && request.Phase != string(phase) {
		return normalizedControlledRunCreationRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "Run phase must use its canonical value")
	}
	networkMode := strings.TrimSpace(request.NetworkMode)
	if networkMode == "" {
		networkMode = "disabled"
	}
	if request.NetworkMode != "" && request.NetworkMode != networkMode {
		return normalizedControlledRunCreationRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "Run network mode must use its canonical value")
	}
	var allowedTargets []string
	switch networkMode {
	case "disabled":
		if len(request.AllowedTargets) != 0 {
			return normalizedControlledRunCreationRequest{}, apperror.New(
				apperror.CodeInvalidArgument,
				"disabled Run network mode cannot retain allowed targets")
		}
	case "allowlist":
		allowedTargets, err = webevidence.NormalizeExactAuthorityTargets(request.AllowedTargets)
		if err != nil {
			return normalizedControlledRunCreationRequest{}, apperror.Wrap(
				apperror.CodeInvalidArgument, "Run network allowlist is invalid", err)
		}
	default:
		return normalizedControlledRunCreationRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "Run network mode must be disabled or allowlist")
	}
	webMode, webTargets, err := normalizeWebAccess(request.WebAccessMode, request.WebAllowedTargets, true)
	if err != nil {
		return normalizedControlledRunCreationRequest{}, err
	}
	operationKey, err := domain.NormalizeAgentOperationKey(request.OperationKey)
	if err != nil || containsSpaceOrControl(operationKey) {
		return normalizedControlledRunCreationRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "Run creation idempotency key is invalid")
	}
	requestedBy := redact.String(strings.TrimSpace(request.RequestedBy))
	if requestedBy == "" {
		requestedBy = "http_control"
	}
	if !domain.ValidAgentID(requestedBy) || strings.ContainsRune(requestedBy, 0) {
		return normalizedControlledRunCreationRequest{}, apperror.New(
			apperror.CodeInvalidArgument, "Run creation requester is invalid")
	}
	budget, err := request.Budget.Normalize()
	if err != nil {
		return normalizedControlledRunCreationRequest{}, apperror.New(apperror.CodeInvalidArgument, err.Error())
	}
	return normalizedControlledRunCreationRequest{Budget: budget, Goal: goal, WorkspaceID: workspaceID,
		Profile: profile, Surface: surface, Phase: phase, NetworkMode: networkMode,
		WebAccessMode: webMode, WebAllowedTargets: webTargets,
		AllowedTargets: append([]string(nil), allowedTargets...), ModelRoute: modelRoute,
		InitialModelRoutePin: initialModelRoutePin,
		OperationKey:         operationKey,
		RequestedBy:          requestedBy}, nil
}

func validControlledModelRoute(value string) bool {
	parts := strings.SplitN(value, "/", 2)
	return len(parts) == 2 && domain.ValidAgentID(parts[0]) && domain.ValidAgentID(parts[1]) &&
		parts[0] == strings.TrimSpace(parts[0]) && parts[1] == strings.TrimSpace(parts[1])
}

func validControlledRunNetworkScope(scope domain.Scope) bool {
	switch scope.NetworkMode {
	case "disabled":
		return len(scope.AllowedTargets) == 0
	case "allowlist":
		normalized, err := webevidence.NormalizeExactAuthorityTargets(scope.AllowedTargets)
		return err == nil && sameStringValues(normalized, scope.AllowedTargets)
	default:
		return false
	}
}

func sameControlledRunScope(first domain.Scope, second domain.Scope) bool {
	return first.SameWebAccess(second) && first.WorkspaceID == second.WorkspaceID &&
		first.NetworkMode == second.NetworkMode &&
		sameStringValues(first.AllowedTargets, second.AllowedTargets)
}

func sameStringValues(first []string, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func validateControlledRunCreationIntent(operation domain.RunCreationOperation,
	keyDigest string, requestFingerprint string, workspaceID string, requestedBy string,
) error {
	if err := operation.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeConflict,
			"stored Run creation operation is invalid", err)
	}
	storedIdentity, storedErr := operation.ReplayIdentity()
	requestedIdentity, requestedErr := (domain.RunCreationOperation{
		ProtocolVersion: domain.RunCreationProtocolVersion,
		KeyDigest:       keyDigest, RequestFingerprint: requestFingerprint,
	}).ReplayIdentity()
	decision, decisionErr := durableoperation.Decide(storedIdentity, requestedIdentity)
	if storedErr != nil || requestedErr != nil || decisionErr != nil ||
		decision != durableoperation.DecisionReplay ||
		operation.WorkspaceID != workspaceID || operation.RequestedBy != requestedBy {
		return apperror.New(apperror.CodeConflict,
			"Run creation idempotency key was already used for a different request")
	}
	return nil
}

func containsSpaceOrControl(value string) bool {
	for _, current := range value {
		if unicode.IsSpace(current) || unicode.IsControl(current) {
			return true
		}
	}
	return false
}
