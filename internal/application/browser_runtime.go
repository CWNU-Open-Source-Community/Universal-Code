package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/projectconfig"
	"encoding/json"
)

// BrowserRuntimeStore supplies the durable browser launch preparation, review,
// readiness facts, and runtime lifecycle the operator flow consumes.
type BrowserRuntimeStore interface {
	GetRun(ctx context.Context, id string) (domain.Run, error)
	GetMission(ctx context.Context, id string) (domain.Mission, error)
	GetRunBrowserCDPPermission(ctx context.Context,
		runID string) (domain.RunBrowserCDPPermissionSnapshot, error)
	LoadLatestBrowserNetworkEvidence(ctx context.Context,
		executableIdentityFingerprint string) (browserruntime.BrowserNetworkContainmentEvidence, error)
	LoadBrowserNetworkReview(ctx context.Context,
		evidenceFingerprint string) (browserruntime.BrowserNetworkContainmentReview, error)
	PrepareBrowserLaunch(ctx context.Context, session browserruntime.SessionPlan,
		identity browserruntime.BrowserExecutableIdentity,
		acceptance browserruntime.BrowserAcceptanceCandidate,
		ownership browserruntime.ProfileOwnershipPlan, operationKey string,
		leaseOwnerIdentity string) (browserruntime.BrowserLaunchAttempt,
		browserruntime.BrowserLaunchLease, bool, error)
	RecordBrowserLaunchReview(ctx context.Context, session browserruntime.SessionPlan,
		identity browserruntime.BrowserExecutableIdentity,
		acceptance browserruntime.BrowserAcceptanceCandidate,
		ownership browserruntime.ProfileOwnershipPlan,
		attempt browserruntime.BrowserLaunchAttempt, lease browserruntime.BrowserLaunchLease,
		decision browserruntime.BrowserLaunchReviewDecision, operationKey string,
		reviewerIdentity string) (browserruntime.BrowserLaunchReview, bool, error)
	RecordBrowserRuntimeCheckpoint(context.Context, browserruntime.BrowserRuntimeCheckpoint) error
	RecordBrowserRuntimeReceipt(context.Context, browserruntime.BrowserRuntimeReceipt) error
}

// BrowserRuntimeLaunchRequest is a bounded operator request to start one
// contained Safe Web browser session against an exact loopback target.
type BrowserRuntimeLaunchRequest struct {
	RunID              string
	Target             string
	Identity           browserruntime.BrowserExecutableIdentity
	Acceptance         browserruntime.BrowserAcceptanceCandidate
	ProfileRoot        string
	OperationKey       string
	LeaseOwnerIdentity string
	ReviewerIdentity   string
	ReviewOperationKey string
}

// BrowserRuntimeHandle is the in-memory handle for one running contained
// browser session. Its coordinator owns the bounded close/cleanup flow.
type BrowserRuntimeHandle struct {
	RuntimeID   string
	Coordinator *browserruntime.BrowserRuntimeLifecycleCoordinator
	UIEvidence  *browserruntime.RestrictedBrowserSession
}

// BrowserRuntimeService orchestrates the Safe Web operator flow
// (launch → observe → close) over the existing browser runtime library. It is
// the only application-layer authority that starts a browser; the readiness
// gate, durable review, disposable Profile, and network containment all remain
// mandatory and fail closed.
type BrowserRuntimeService struct {
	store                  BrowserRuntimeStore
	controller             *browserruntime.BrowserProcessController
	runtimeCapabilities    browserruntime.ProductionRuntimeCapabilities
	permissionCapabilities domain.BrowserCDPPermissionRuntimeCapabilities
}

func NewBrowserRuntimeService(store BrowserRuntimeStore,
	controller *browserruntime.BrowserProcessController,
	runtimeCapabilities browserruntime.ProductionRuntimeCapabilities,
	permissionCapabilities domain.BrowserCDPPermissionRuntimeCapabilities,
) *BrowserRuntimeService {
	return &BrowserRuntimeService{store: store, controller: controller,
		runtimeCapabilities:    runtimeCapabilities,
		permissionCapabilities: permissionCapabilities}
}

// Launch runs the full fail-closed operator flow up to process start: durable
// preparation and review, network-containment plan, authorization, disposable
// Profile materialization, and contained process launch. It returns a handle
// whose coordinator performs the bounded close/cleanup. It never returns a
// handle whose process is not yet bound to the restricted Job.
func (s *BrowserRuntimeService) Launch(ctx context.Context,
	request BrowserRuntimeLaunchRequest,
) (*BrowserRuntimeHandle, error) {
	return s.launch(ctx, request, false)
}

// LaunchUIEvidence runs the same reviewed Safe Web launch path and then opens
// the separately authorized fixed-method UI evidence CDP session. It never
// adopts a pre-existing browser or user Profile.
func (s *BrowserRuntimeService) LaunchUIEvidence(ctx context.Context,
	request BrowserRuntimeLaunchRequest,
) (*BrowserRuntimeHandle, error) {
	return s.launch(ctx, request, true)
}

// browserLimitsOverrideForRun decodes the run's pinned project config and
// returns the operator-narrowed browser limits, if any. The stored snapshot is
// the marshaled projectconfig.Effective; a missing, malformed, or absent
// browser_limits section yields nil (keep the builtin profile limits).
func browserLimitsOverrideForRun(run domain.Run) *browserruntime.ProfileLimits {
	if len(run.Config.ProjectConfig) == 0 {
		return nil
	}
	var effective projectconfig.Effective
	if json.Unmarshal(run.Config.ProjectConfig, &effective) != nil || effective.BrowserLimits == nil {
		return nil
	}
	limits := effective.BrowserLimits
	return &browserruntime.ProfileLimits{
		MaxOrigins:       limits.MaxOrigins,
		MaxURLBytes:      limits.MaxURLBytes,
		MaxRequests:      limits.MaxRequests,
		MaxResponseBytes: limits.MaxResponseBytes,
		MaxDownloadBytes: limits.MaxDownloadBytes,
		TimeoutMS:        limits.TimeoutMS,
	}
}

func (s *BrowserRuntimeService) launch(ctx context.Context,
	request BrowserRuntimeLaunchRequest, uiEvidence bool,
) (*BrowserRuntimeHandle, error) {
	if s == nil || s.store == nil || s.controller == nil {
		return nil, errors.New("browser runtime service is not fully configured")
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
		return nil, errors.New("browser runtime Run id is invalid")
	}
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return nil, err
	}
	if !domain.CanChangeRunBrowserCDPPermission(run.Status) {
		return nil, errors.New("browser runtime launch requires a non-terminal Run")
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return nil, err
	}
	permission, err := s.store.GetRunBrowserCDPPermission(ctx, request.RunID)
	if err != nil {
		return nil, err
	}
	if permission.Mode != domain.RunBrowserCDPPermissionRestricted {
		return nil, errors.New("browser runtime launch requires the restricted CDP permission")
	}
	now := time.Now().UTC()
	session, err := browserruntime.BuildSessionPlan(browserruntime.NewSessionPlanRequest{
		SessionID: run.SessionID, RunID: run.ID, WorkspaceID: mission.WorkspaceID,
		ProfileID: browserruntime.ProfileSafeWeb, Targets: []string{request.Target},
		LimitsOverride: browserLimitsOverrideForRun(run),
	})
	if err != nil {
		return nil, err
	}
	evidence, err := s.store.LoadLatestBrowserNetworkEvidence(ctx, request.Identity.Fingerprint)
	if err != nil {
		return nil, err
	}
	review := browserruntime.BrowserNetworkContainmentReview{}
	if evidence.Fingerprint != "" {
		review, err = s.store.LoadBrowserNetworkReview(ctx, evidence.Fingerprint)
		if err != nil {
			return nil, err
		}
	}
	readiness, err := browserruntime.BuildBrowserSafeWebReadiness(evidence, review,
		request.Identity, request.Acceptance, now)
	if err != nil {
		return nil, err
	}
	if !readiness.Ready {
		return nil, errors.New("browser runtime launch refused: safe web is not ready (" +
			readiness.BlockingReason + ")")
	}
	ownership, err := browserruntime.BuildProfileOwnershipPlan(session, request.Identity,
		request.ProfileRoot)
	if err != nil {
		return nil, err
	}
	launchAttempt, launchLease, _, err := s.store.PrepareBrowserLaunch(ctx, session,
		request.Identity, request.Acceptance, ownership, request.OperationKey,
		request.LeaseOwnerIdentity)
	if err != nil {
		return nil, err
	}
	launchReview, _, err := s.store.RecordBrowserLaunchReview(ctx, session,
		request.Identity, request.Acceptance, ownership, launchAttempt, launchLease,
		browserruntime.BrowserLaunchReviewAcceptCandidate, request.ReviewOperationKey,
		request.ReviewerIdentity)
	if err != nil {
		return nil, err
	}
	networkPlan, err := browserruntime.BuildBrowserNetworkContainmentPlan(session,
		request.Identity, request.Acceptance, evidence, review, now, evidence.ExpiresAt)
	if err != nil {
		return nil, err
	}
	authorization, err := browserruntime.AuthorizeSafeWebStart(session, request.Identity,
		request.Acceptance, ownership, launchAttempt, launchLease, launchReview,
		evidence, review, networkPlan, permission, s.permissionCapabilities,
		s.runtimeCapabilities, now)
	if err != nil {
		return nil, err
	}
	profileLease, err := browserruntime.MaterializeDisposableProfile(authorization,
		session, request.Identity, request.Acceptance, ownership, launchAttempt,
		launchLease, launchReview, evidence, review, networkPlan, permission, now)
	if err != nil {
		return nil, err
	}
	process, err := s.controller.Start(ctx, authorization, session, request.Identity,
		request.Acceptance, ownership, launchAttempt, launchLease, launchReview,
		evidence, review, networkPlan, permission, profileLease, now)
	if err != nil {
		return nil, err
	}
	runtimeID := idgen.New("browser_runtime")
	var restricted *browserruntime.RestrictedBrowserSession
	if uiEvidence {
		cdpAuthorization, authorizeErr := browserruntime.AuthorizeUIEvidenceCDP(
			authorization, session, request.Identity, request.Acceptance, ownership,
			launchAttempt, launchLease, launchReview, evidence, review, networkPlan,
			permission, s.runtimeCapabilities, time.Now().UTC())
		if authorizeErr == nil {
			restricted, authorizeErr = browserruntime.OpenRestrictedBrowserSession(ctx,
				cdpAuthorization, authorization, session, request.Identity,
				request.Acceptance, ownership, launchAttempt, launchLease, launchReview,
				evidence, review, networkPlan, permission, profileLease, process)
		}
		if authorizeErr != nil {
			cleanupErr := s.cleanupFailedBrowserLaunch(runtimeID, launchAttempt,
				authorization, ownership, profileLease, process, now)
			return nil, errors.Join(authorizeErr, cleanupErr)
		}
	}
	coordinator, err := browserruntime.NewBrowserRuntimeLifecycleCoordinator(runtimeID,
		launchAttempt, authorization, ownership, profileLease, process, restricted, s.store, now)
	if err != nil {
		if restricted != nil {
			_ = restricted.Close(context.Background())
		}
		cleanupErr := s.cleanupFailedBrowserLaunch(runtimeID, launchAttempt,
			authorization, ownership, profileLease, process, now)
		return nil, errors.Join(err, cleanupErr)
	}
	return &BrowserRuntimeHandle{RuntimeID: runtimeID, Coordinator: coordinator,
		UIEvidence: restricted}, nil
}

func (s *BrowserRuntimeService) cleanupFailedBrowserLaunch(runtimeID string,
	attempt browserruntime.BrowserLaunchAttempt,
	authorization browserruntime.BrowserStartAuthorization,
	ownership browserruntime.ProfileOwnershipPlan,
	profileLease browserruntime.ProfileRuntimeLease,
	process *browserruntime.BrowserProcess, startedAt time.Time,
) error {
	coordinator, err := browserruntime.NewBrowserRuntimeLifecycleCoordinator(runtimeID,
		attempt, authorization, ownership, profileLease, process, nil, s.store, startedAt)
	if err != nil {
		_ = process.Stop(context.Background())
		return err
	}
	_, err = coordinator.Finalize(context.Background())
	return err
}

// Close finalizes a running browser session: it stops the process tree, verifies
// network-containment cleanup, releases and cleans the disposable Profile, and
// records the terminal receipt. Caller cancellation never skips bounded cleanup.
func (s *BrowserRuntimeService) Close(ctx context.Context,
	handle *BrowserRuntimeHandle,
) (browserruntime.BrowserRuntimeReceipt, error) {
	if s == nil {
		return browserruntime.BrowserRuntimeReceipt{}, errors.New("browser runtime service is required")
	}
	if handle == nil || handle.Coordinator == nil {
		return browserruntime.BrowserRuntimeReceipt{}, errors.New("browser runtime handle is required")
	}
	return handle.Coordinator.Finalize(ctx)
}
