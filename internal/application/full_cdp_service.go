package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
)

// FullCDPStore supplies the durable Run, Mission, and browser CDP permission
// facts the Full CDP operator flow consumes.
type FullCDPStore interface {
	GetRun(ctx context.Context, id string) (domain.Run, error)
	GetMission(ctx context.Context, id string) (domain.Mission, error)
	GetRunBrowserCDPPermission(ctx context.Context,
		runID string) (domain.RunBrowserCDPPermissionSnapshot, error)
}

type fullCDPExecutionPermissionStore interface {
	GetRunExecutionPermission(ctx context.Context,
		runID string) (domain.RunExecutionPermissionSnapshot, error)
}

// FullCDPService opens the highly-sensitive Full CDP channel. It is independent
// from Safe Web: it requires an exact live Full execution
// snapshot, the separately confirmed Full CDP sub-permission, an exact per-call
// confirmation, and a dedicated contained browser process.
type FullCDPService struct {
	store                  FullCDPStore
	runtimeCapabilities    browserruntime.FullCDPRuntimeCapabilities
	permissionCapabilities domain.BrowserCDPPermissionRuntimeCapabilities
	executionCapabilities  domain.ExecutionPermissionRuntimeCapabilities
	requireExecutionGrant  bool
}

func NewFullCDPServiceWithExecutionCapabilities(store FullCDPStore,
	runtimeCapabilities browserruntime.FullCDPRuntimeCapabilities,
	permissionCapabilities domain.BrowserCDPPermissionRuntimeCapabilities,
	executionCapabilities domain.ExecutionPermissionRuntimeCapabilities,
) *FullCDPService {
	return &FullCDPService{store: store, runtimeCapabilities: runtimeCapabilities,
		permissionCapabilities: permissionCapabilities,
		executionCapabilities:  executionCapabilities, requireExecutionGrant: true}
}

func NewFullCDPService(store FullCDPStore,
	runtimeCapabilities browserruntime.FullCDPRuntimeCapabilities,
	permissionCapabilities domain.BrowserCDPPermissionRuntimeCapabilities,
) *FullCDPService {
	// Kept for source compatibility only. Without explicit execution
	// capabilities this constructor remains fail-closed at Open.
	return &FullCDPService{store: store, runtimeCapabilities: runtimeCapabilities,
		permissionCapabilities: permissionCapabilities, requireExecutionGrant: true}
}

// FullCDPOpenRequest is a bounded request to open the Full CDP debug session
// over an already launched, contained browser process.
type FullCDPOpenRequest struct {
	RunID        string
	Target       string
	Identity     browserruntime.BrowserExecutableIdentity
	Acceptance   browserruntime.BrowserAcceptanceCandidate
	Ownership    browserruntime.ProfileOwnershipPlan
	Confirmed    bool
	ProfileLease browserruntime.ProfileRuntimeLease
	Process      *browserruntime.BrowserProcess
}

// Open authorizes and dials the Full CDP session. It fails closed unless the
// Run uses live Full permission, the caller supplies an exact
// per-call confirmation, and the dedicated browser process is live.
func (s *FullCDPService) Open(ctx context.Context,
	request FullCDPOpenRequest,
) (*browserruntime.FullCDPSession, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("full CDP service is not fully configured")
	}
	if err := s.runtimeCapabilities.Validate(); err != nil {
		return nil, err
	}
	if err := s.permissionCapabilities.Validate(); err != nil {
		return nil, err
	}
	if err := browserruntime.ValidateBrowserExecutableIdentity(request.Identity); err != nil {
		return nil, err
	}
	if err := browserruntime.ValidateBrowserAcceptanceCandidate(
		request.Acceptance, request.Identity); err != nil {
		return nil, err
	}
	request.RunID = strings.TrimSpace(request.RunID)
	if request.RunID == "" || !domain.ValidAgentID(request.RunID) {
		return nil, errors.New("full CDP Run id is invalid")
	}
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return nil, err
	}
	if !domain.CanChangeRunBrowserCDPPermission(run.Status) {
		return nil, errors.New("full CDP requires a non-terminal Run")
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return nil, err
	}
	permission, err := s.store.GetRunBrowserCDPPermission(ctx, request.RunID)
	if err != nil {
		return nil, err
	}
	var executionPermission domain.RunExecutionPermissionSnapshot
	var executionFence uint64
	if s.requireExecutionGrant {
		if err := s.executionCapabilities.Validate(); err != nil {
			return nil, err
		}
		reader, ok := s.store.(fullCDPExecutionPermissionStore)
		if !ok {
			return nil, errors.New("full CDP execution permission reader is required")
		}
		executionPermission, err = reader.GetRunExecutionPermission(ctx, request.RunID)
		if err != nil {
			return nil, err
		}
		if executionPermission.Mode != domain.RunExecutionPermissionFull {
			return nil, errors.New(
				"full CDP requires the current Full execution preference")
		}
		if !s.executionCapabilities.AllowsSnapshot(executionPermission) ||
			s.executionCapabilities.RuntimeAuthority == nil {
			return nil, errors.New(
				"full CDP requires the current Run's live execution authority")
		}
		// Do not rotate the current Run's child-authority fence for a request
		// that has not passed the explicit high-risk confirmation boundary.
		if !request.Confirmed {
			return nil, errors.New(
				"full CDP requires exact per-call operator confirmation")
		}
		executionFence, err = s.executionCapabilities.RuntimeAuthority.
			IssueRunAuthorizationFence(request.RunID)
		if err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	session, err := browserruntime.BuildSessionPlan(browserruntime.NewSessionPlanRequest{
		SessionID: run.SessionID, RunID: run.ID, WorkspaceID: mission.WorkspaceID,
		ProfileID: browserruntime.ProfileCTFLab, Targets: []string{request.Target},
		LimitsOverride: browserLimitsOverrideForRun(run),
		Features: browserruntime.FeatureRequest{
			InterceptRequests: true, ModifyRequests: true,
			ReplayRequests: true, EditCookies: true,
		},
	})
	if err != nil {
		return nil, err
	}
	authorization, err := browserruntime.AuthorizeFullCDP(session, request.Identity,
		request.Acceptance, permission, executionPermission, s.runtimeCapabilities,
		s.permissionCapabilities, s.executionCapabilities, executionFence,
		request.Confirmed, now)
	if err != nil {
		return nil, err
	}
	if s.requireExecutionGrant {
		return browserruntime.OpenFullCDPSession(ctx,
			authorization, session, request.Identity, request.Acceptance,
			request.Ownership, permission, executionPermission,
			s.executionCapabilities, executionFence, request.ProfileLease,
			request.Process)
	}
	return nil, errors.New("full CDP execution authority is required")
}
