package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/sandbox"
)

const (
	DockerContainerLifecycleRecoveryLimit = 64

	// DockerContainerLifecyclePostExitProtocolVersion binds one logical post-exit
	// invocation to the durable intent, exact exit checkpoint, current authority,
	// and current lifecycle request. Implementations must use InvocationKeyDigest
	// as their idempotency key when they persist side effects.
	DockerContainerLifecyclePostExitProtocolVersion = "application_docker_container_lifecycle_post_exit.v1"
	DockerContainerLifecyclePostExitFailureReason   = "post_exit_processing_failed"
	DockerContainerLifecycleRunningProtocolVersion  = "application_docker_container_lifecycle_running.v1"

	DockerContainerLifecycleStartAuthorityProtocolVersion = "application_docker_container_lifecycle_start_authority.v1"
	DockerContainerLifecycleStartAuthorityDeniedReason    = "start_authority_denied"
)

var errDockerContainerLifecycleStartAuthorityDenied = errors.New(
	"Docker lifecycle start authority denied")

var errDockerContainerLifecycleStartAuthorityCancelled = errors.New(
	"Docker lifecycle start authority cancelled")

// DockerContainerLifecycleStore is deliberately separate from the product
// SandboxManifestStore. This slice does not add a CLI, HTTP, Desktop, model, or
// generic Runner execution entry point.
type DockerContainerLifecycleStore interface {
	GetDockerContainerLifecycleByOperation(context.Context,
		string) (sandbox.DockerContainerLifecycleRecord, bool, error)
	BeginDockerContainerLifecycle(context.Context, sandbox.DockerContainerLaunchIntent,
		string, time.Duration) (sandbox.DockerContainerLifecycleRecord, bool, error)
	AcquireDockerContainerLifecycle(context.Context, string, string, string,
		time.Duration) (sandbox.DockerContainerLifecycleRecord, error)
	RenewDockerContainerLifecycleLease(context.Context,
		sandbox.DockerContainerLifecycleLease, time.Duration) (sandbox.DockerContainerLifecycleLease, error)
	ReleaseDockerContainerLifecycleLease(context.Context,
		sandbox.DockerContainerLifecycleLease) (sandbox.DockerContainerLifecycleLease, bool, error)
	FenceDockerContainerLifecycle(context.Context, sandbox.DockerContainerLifecycleLease) error
	PrepareDockerContainerLifecycleAction(context.Context,
		sandbox.DockerContainerLifecyclePreparedAction,
		sandbox.DockerContainerLifecycleLease) (sandbox.DockerContainerLifecycleRecord, bool, error)
	AppendDockerContainerLifecycleTransition(context.Context,
		sandbox.DockerContainerLifecycleTransition,
		sandbox.DockerContainerLifecycleLease) (sandbox.DockerContainerLifecycleRecord, bool, error)
	CompleteDockerContainerLifecycle(context.Context, sandbox.DockerContainerLifecycleReceipt,
		sandbox.DockerContainerLifecycleLease) (sandbox.DockerContainerLifecycleRecord, bool, error)
	GetDockerContainerLifecycle(context.Context, string) (sandbox.DockerContainerLifecycleRecord, error)
	ListRecoverableDockerContainerLifecycles(context.Context,
		int) ([]sandbox.DockerContainerLifecycleRecord, error)
}

// DockerContainerLifecycleAuthority revalidates the current Run, Workspace,
// permission snapshot, process capability, and exact v54 plan. Persistence is
// never treated as fresh authorization. Implementations must reconstruct the
// request from current authority, not from private data in the lifecycle ledger.
type DockerContainerLifecycleAuthority interface {
	RevalidateDockerContainerLifecycle(context.Context,
		sandbox.DockerContainerLaunchIntent) (sandbox.DockerContainerPlan,
		sandbox.DockerContainerWriteRequest, error)
}

// DockerContainerLifecycleStartAuthority rechecks process-local capability and
// permission immediately before a create or start write. It deliberately does
// not authorize wait, termination, delete, or post-exit cleanup: losing start
// authority must never prevent the Supervisor from removing an owned resource.
type DockerContainerLifecycleStartAuthority interface {
	AuthorizeDockerContainerLifecycleStart(context.Context,
		DockerContainerLifecycleStartAuthorityRequest) error
}

// DockerContainerLifecycleStartAuthorityFunc adapts a function to
// DockerContainerLifecycleStartAuthority.
type DockerContainerLifecycleStartAuthorityFunc func(context.Context,
	DockerContainerLifecycleStartAuthorityRequest) error

func (fn DockerContainerLifecycleStartAuthorityFunc) AuthorizeDockerContainerLifecycleStart(
	ctx context.Context, request DockerContainerLifecycleStartAuthorityRequest,
) error {
	if fn == nil {
		return errors.New("Docker lifecycle start authority callback is nil")
	}
	return fn(ctx, request)
}

// DockerContainerLifecycleStartAuthorityRequest is a current, non-persistable
// authorization view bound to one exact create or start action and active lease.
type DockerContainerLifecycleStartAuthorityRequest struct {
	ProtocolVersion string
	Action          sandbox.DockerContainerLifecycleActionKind
	Record          sandbox.DockerContainerLifecycleRecord
	Plan            sandbox.DockerContainerPlan
	WriteRequest    sandbox.DockerContainerWriteRequest
	Lease           sandbox.DockerContainerLifecycleLease
}

func (request DockerContainerLifecycleStartAuthorityRequest) Validate() error {
	if request.ProtocolVersion != DockerContainerLifecycleStartAuthorityProtocolVersion ||
		(request.Action != sandbox.DockerContainerLifecycleActionCreate &&
			request.Action != sandbox.DockerContainerLifecycleActionStart) ||
		request.Record.Validate() != nil || request.Record.Receipt != nil ||
		request.Plan.Validate() != nil || request.WriteRequest.Validate() != nil ||
		request.Lease.Validate() != nil ||
		request.Lease.Status != sandbox.DockerContainerLifecycleLeaseActive ||
		request.Lease.IntentID != request.Record.Intent.ID ||
		request.Lease.LeaseID != request.Record.Lease.LeaseID ||
		request.Lease.OwnerID != request.Record.Lease.OwnerID ||
		request.Lease.Generation != request.Record.Lease.Generation ||
		request.Record.Intent.PlanID != request.Plan.ID ||
		request.Record.Intent.RequestFingerprint != request.WriteRequest.RequestFingerprint {
		return errors.New("Docker lifecycle start authority request is invalid")
	}
	return nil
}

// DockerContainerLifecyclePostExit is the only lifecycle extension point that
// runs while the owned container still exists after its exact exit state has
// been durably checkpointed. The Supervisor revalidates current authority and
// fences the active lease immediately before invoking it. Implementations must
// be idempotent for InvocationKeyDigest because a process crash between the
// callback and the durable cleaning checkpoint can replay the same logical
// invocation during recovery.
type DockerContainerLifecyclePostExit interface {
	HandleDockerContainerLifecyclePostExit(context.Context,
		DockerContainerLifecyclePostExitRequest) error
}

// DockerContainerLifecyclePostExitFunc adapts a function to
// DockerContainerLifecyclePostExit.
type DockerContainerLifecyclePostExitFunc func(context.Context,
	DockerContainerLifecyclePostExitRequest) error

func (fn DockerContainerLifecyclePostExitFunc) HandleDockerContainerLifecyclePostExit(
	ctx context.Context, request DockerContainerLifecyclePostExitRequest,
) error {
	if fn == nil {
		return errors.New("Docker lifecycle post-exit callback is nil")
	}
	return fn(ctx, request)
}

// DockerContainerLifecycleRunning is the process-local extension used only
// for an already-started, ownership-bound container. It cannot create, start,
// adopt, or recover input authority after restart.
type DockerContainerLifecycleRunning interface {
	HandleDockerContainerLifecycleRunning(context.Context,
		DockerContainerLifecycleRunningRequest,
		sandbox.DockerContainerLifecycleFence) error
}

type DockerContainerLifecycleRunningRequest struct {
	ProtocolVersion    string
	Record             sandbox.DockerContainerLifecycleRecord
	Plan               sandbox.DockerContainerPlan
	WriteRequest       sandbox.DockerContainerWriteRequest
	LifecycleRequest   sandbox.DockerContainerLifecycleRequest
	RunningObservation sandbox.DockerContainerLifecycleObservation
	Lease              sandbox.DockerContainerLifecycleLease
}

func (request DockerContainerLifecycleRunningRequest) Validate() error {
	started := latestLifecycleTransition(request.Record,
		sandbox.DockerContainerLifecycleTransitionStarted)
	if request.ProtocolVersion != DockerContainerLifecycleRunningProtocolVersion ||
		request.Record.Validate() != nil || request.Record.Receipt != nil ||
		request.Plan.Validate() != nil || request.WriteRequest.Validate() != nil ||
		request.LifecycleRequest.Validate() != nil || request.Lease.Validate() != nil ||
		request.Lease.Status != sandbox.DockerContainerLifecycleLeaseActive ||
		request.Lease.IntentID != request.Record.Intent.ID ||
		request.Lease.LeaseID != request.Record.Lease.LeaseID ||
		request.Lease.OwnerID != request.Record.Lease.OwnerID ||
		request.Lease.Generation != request.Record.Lease.Generation ||
		request.LifecycleRequest.LeaseGeneration != request.Lease.Generation ||
		request.LifecycleRequest.WriteRequest.RequestFingerprint !=
			request.WriteRequest.RequestFingerprint ||
		request.Record.Intent.PlanID != request.Plan.ID ||
		request.Record.Intent.RequestFingerprint != request.WriteRequest.RequestFingerprint ||
		request.RunningObservation.State != sandbox.DockerContainerLifecycleStateRunning ||
		!request.RunningObservation.Running ||
		!request.RunningObservation.ContainerPresent ||
		request.RunningObservation.RequestFingerprint !=
			request.LifecycleRequest.RequestFingerprint ||
		started == nil || started.ContainerIDFingerprint !=
		request.RunningObservation.ContainerIDFingerprint {
		return errors.New("Docker lifecycle running request is invalid")
	}
	return nil
}

// DockerContainerLifecyclePostExitRequest is a non-authorizing snapshot. Its
// lease and authority are current only for the callback context supplied by the
// Supervisor; callers must not retain or reuse them after the callback returns.
type DockerContainerLifecyclePostExitRequest struct {
	ProtocolVersion     string
	InvocationKeyDigest string
	Record              sandbox.DockerContainerLifecycleRecord
	Plan                sandbox.DockerContainerPlan
	WriteRequest        sandbox.DockerContainerWriteRequest
	LifecycleRequest    sandbox.DockerContainerLifecycleRequest
	ExitObservation     sandbox.DockerContainerLifecycleObservation
	Lease               sandbox.DockerContainerLifecycleLease
}

func (request DockerContainerLifecyclePostExitRequest) Validate() error {
	exited := latestLifecycleTransition(request.Record,
		sandbox.DockerContainerLifecycleTransitionExited)
	if request.ProtocolVersion != DockerContainerLifecyclePostExitProtocolVersion ||
		request.Record.Validate() != nil || request.Record.Receipt != nil ||
		request.Plan.Validate() != nil || request.WriteRequest.Validate() != nil ||
		request.LifecycleRequest.Validate() != nil || request.Lease.Validate() != nil ||
		request.Lease.Status != sandbox.DockerContainerLifecycleLeaseActive ||
		request.Lease.IntentID != request.Record.Intent.ID ||
		request.Lease.LeaseID != request.Record.Lease.LeaseID ||
		request.Lease.OwnerID != request.Record.Lease.OwnerID ||
		request.Lease.Generation != request.Record.Lease.Generation ||
		request.LifecycleRequest.LeaseGeneration != request.Lease.Generation ||
		request.LifecycleRequest.WriteRequest.RequestFingerprint !=
			request.WriteRequest.RequestFingerprint ||
		request.LifecycleRequest.RequestFingerprint !=
			request.ExitObservation.RequestFingerprint ||
		request.Record.Intent.PlanID != request.Plan.ID ||
		request.Record.Intent.RequestFingerprint != request.WriteRequest.RequestFingerprint ||
		request.ExitObservation.State != sandbox.DockerContainerLifecycleStateExited ||
		request.ExitObservation.Running || !request.ExitObservation.ContainerPresent ||
		exited == nil || exited.ExitCode == nil ||
		*exited.ExitCode != request.ExitObservation.ExitCode ||
		exited.ContainerIDFingerprint != request.ExitObservation.ContainerIDFingerprint ||
		request.InvocationKeyDigest != dockerContainerLifecyclePostExitInvocationKey(request) {
		return errors.New("Docker lifecycle post-exit request is invalid")
	}
	return nil
}

func dockerContainerLifecyclePostExitInvocationKey(
	request DockerContainerLifecyclePostExitRequest,
) string {
	exited := latestLifecycleTransition(request.Record,
		sandbox.DockerContainerLifecycleTransitionExited)
	exitedFingerprint := ""
	if exited != nil {
		exitedFingerprint = exited.TransitionFingerprint
	}
	return runmutation.Fingerprint(DockerContainerLifecyclePostExitProtocolVersion,
		request.Record.Intent.IntentFingerprint, exitedFingerprint,
		request.Plan.AuthorityFingerprint, request.LifecycleRequest.RequestFingerprint)
}

type DockerContainerLifecycleSupervisor struct {
	store     DockerContainerLifecycleStore
	transport sandbox.DockerContainerLifecycleTransport
	authority DockerContainerLifecycleAuthority
	startAuth DockerContainerLifecycleStartAuthority
	running   DockerContainerLifecycleRunning
	postExit  DockerContainerLifecyclePostExit
	ownerID   string
	leaseTTL  time.Duration
	now       func() time.Time

	mu    sync.Mutex
	lease sandbox.DockerContainerLifecycleLease
}

func NewDockerContainerLifecycleSupervisor(store DockerContainerLifecycleStore,
	transport sandbox.DockerContainerLifecycleTransport,
	authority DockerContainerLifecycleAuthority, ownerID string,
	leaseTTL time.Duration,
) (*DockerContainerLifecycleSupervisor, error) {
	ownerID = strings.TrimSpace(ownerID)
	if store == nil || transport == nil || authority == nil || ownerID == "" ||
		sandbox.ValidateDockerContainerLifecycleLeaseTTL(leaseTTL) != nil ||
		transport.Endpoint().Validate() != nil {
		return nil, errors.New("Docker lifecycle Supervisor dependencies are invalid")
	}
	return &DockerContainerLifecycleSupervisor{store: store, transport: transport,
		authority: authority, ownerID: ownerID, leaseTTL: leaseTTL,
		now: func() time.Time { return time.Now().UTC() }}, nil
}

// WithDockerContainerLifecyclePostExit installs the optional exit hook before
// the Supervisor is used. A nil hook leaves the existing lifecycle unchanged.
func (s *DockerContainerLifecycleSupervisor) WithDockerContainerLifecyclePostExit(
	postExit DockerContainerLifecyclePostExit,
) *DockerContainerLifecycleSupervisor {
	if s != nil && postExit != nil {
		s.postExit = postExit
	}
	return s
}

func (s *DockerContainerLifecycleSupervisor) WithDockerContainerLifecycleRunning(
	running DockerContainerLifecycleRunning,
) *DockerContainerLifecycleSupervisor {
	if s != nil && running != nil {
		s.running = running
	}
	return s
}

// WithDockerContainerLifecycleStartAuthority installs the optional process-local
// create/start gate before the Supervisor is used. Cleanup remains independent.
func (s *DockerContainerLifecycleSupervisor) WithDockerContainerLifecycleStartAuthority(
	startAuthority DockerContainerLifecycleStartAuthority,
) *DockerContainerLifecycleSupervisor {
	if s != nil && startAuthority != nil {
		s.startAuth = startAuthority
	}
	return s
}

// BeginAndRun commits launch intent before StageOwned is allowed to issue a
// create. It remains an internal, explicitly-called engineering surface.
func (s *DockerContainerLifecycleSupervisor) BeginAndRun(ctx context.Context,
	plan sandbox.DockerContainerPlan, writeRequest sandbox.DockerContainerWriteRequest,
	requestedBy, operationKey string,
) (sandbox.DockerContainerLifecycleRecord, error) {
	if ctx == nil || plan.Validate() != nil || writeRequest.Validate() != nil ||
		requestedBy != plan.RequestedBy || strings.TrimSpace(operationKey) == "" {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.New(
			apperror.CodeInvalidArgument, "Docker lifecycle request is invalid")
	}
	endpoint := s.transport.Endpoint()
	now := s.now()
	operationKeyDigest := runmutation.Fingerprint("sandbox_docker_lifecycle_operation.v1",
		plan.ID, operationKey)
	existing, found, err := s.store.GetDockerContainerLifecycleByOperation(ctx,
		operationKeyDigest)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Normalize(err)
	}
	if found {
		if existing.Intent.OperationKeyDigest != operationKeyDigest ||
			existing.Intent.RequestedBy != requestedBy {
			return sandbox.DockerContainerLifecycleRecord{}, apperror.New(
				apperror.CodeConflict, "Docker lifecycle operation replay does not match its intent")
		}
		if err := validateDockerLifecycleAuthority(existing.Intent, plan, writeRequest,
			endpoint); err != nil {
			return sandbox.DockerContainerLifecycleRecord{}, err
		}
		existing.Replayed = true
		return existing, nil
	}
	intent, err := sandbox.NewDockerContainerLaunchIntent(
		idgen.New("sandbox-docker-lifecycle"), idgen.New("sandbox-docker-lifecycle-attempt"),
		operationKeyDigest, plan, writeRequest, endpoint, requestedBy, 1, now)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "Docker lifecycle intent assembly failed", err)
	}
	record, replayed, err := s.store.BeginDockerContainerLifecycle(ctx, intent, s.ownerID, s.leaseTTL)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Normalize(err)
	}
	if replayed {
		// Preparation replay is metadata-only. Resuming daemon work requires
		// RecoverOne so the caller must revalidate authority and acquire a
		// current generation instead of borrowing the existing owner's lease.
		return record, nil
	}
	currentPlan, currentRequest, err := s.authority.RevalidateDockerContainerLifecycle(ctx,
		record.Intent)
	if err != nil {
		return record, apperror.Wrap(apperror.CodeFailedPrecondition,
			"Docker lifecycle authority revalidation failed", err)
	}
	if err := validateDockerLifecycleAuthority(record.Intent, currentPlan, currentRequest,
		endpoint); err != nil {
		return record, err
	}
	return s.run(ctx, record, currentPlan, currentRequest)
}

// RecoverOne takes over an expired generation, revalidates current authority,
// and converges exact daemon state without enumerating containers.
func (s *DockerContainerLifecycleSupervisor) RecoverOne(ctx context.Context,
	intentID string,
) (sandbox.DockerContainerLifecycleRecord, error) {
	stored, err := s.store.GetDockerContainerLifecycle(ctx, strings.TrimSpace(intentID))
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Normalize(err)
	}
	if stored.Receipt != nil {
		stored.Replayed = true
		return stored, nil
	}
	plan, request, err := s.authority.RevalidateDockerContainerLifecycle(ctx, stored.Intent)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Wrap(
			apperror.CodeFailedPrecondition, "Docker lifecycle authority revalidation failed", err)
	}
	if err := validateDockerLifecycleAuthority(stored.Intent, plan, request,
		s.transport.Endpoint()); err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	record, err := s.store.AcquireDockerContainerLifecycle(ctx, stored.Intent.ID,
		stored.Intent.RequestedBy, s.ownerID, s.leaseTTL)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Normalize(err)
	}
	if record.Receipt != nil {
		return record, nil
	}
	return s.run(ctx, record, plan, request)
}

// CancelOne acquires an unowned/expired lifecycle generation, persists the
// cancelled cleaning checkpoint, and then converges exact daemon state without
// granting create/start authority. A live owner remains authoritative and makes
// Acquire return conflict; the product layer must durably request cancellation
// before cancelling that owner's process context.
func (s *DockerContainerLifecycleSupervisor) CancelOne(ctx context.Context,
	intentID string,
) (sandbox.DockerContainerLifecycleRecord, error) {
	if ctx == nil || strings.TrimSpace(intentID) == "" {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.New(
			apperror.CodeInvalidArgument, "Docker lifecycle cancellation request is invalid")
	}
	entryCtx, entryCancel := context.WithTimeout(ctx, s.leaseTTL)
	defer entryCancel()
	stored, err := s.store.GetDockerContainerLifecycle(entryCtx, strings.TrimSpace(intentID))
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Normalize(err)
	}
	if stored.Receipt != nil {
		stored.Replayed = true
		return stored, nil
	}
	// Revalidation is used only to reconstruct the exact sealed write request
	// needed for Observe/Terminate/Cleanup. It does not grant start authority.
	plan, request, err := s.authority.RevalidateDockerContainerLifecycle(entryCtx, stored.Intent)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Wrap(
			apperror.CodeFailedPrecondition,
			"Docker lifecycle cancellation reconstruction failed", err)
	}
	if err := validateDockerLifecycleAuthority(stored.Intent, plan, request,
		s.transport.Endpoint()); err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	record, err := s.store.AcquireDockerContainerLifecycle(entryCtx, stored.Intent.ID,
		stored.Intent.RequestedBy, s.ownerID, s.leaseTTL)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Normalize(err)
	}
	if record.Receipt != nil {
		record.Replayed = true
		return record, nil
	}
	s.setLease(record.Lease)
	cancelCtx, cancel := s.leaseBoundContext(context.WithoutCancel(ctx))
	defer cancel()
	record, err = s.ensureCleaning(cancelCtx, record,
		sandbox.DockerContainerLifecycleReasonCancelled)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	return s.run(cancelCtx, record, plan, request)
}

func (s *DockerContainerLifecycleSupervisor) RecoverStartup(ctx context.Context) ([]sandbox.DockerContainerLifecycleRecord, error) {
	values, err := s.store.ListRecoverableDockerContainerLifecycles(ctx,
		DockerContainerLifecycleRecoveryLimit)
	if err != nil {
		return nil, apperror.Normalize(err)
	}
	results := make([]sandbox.DockerContainerLifecycleRecord, 0, len(values))
	for _, value := range values {
		result, recoverErr := s.RecoverOne(ctx, value.Intent.ID)
		if recoverErr != nil {
			return results, recoverErr
		}
		results = append(results, result)
	}
	return results, nil
}

func (s *DockerContainerLifecycleSupervisor) run(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord, plan sandbox.DockerContainerPlan,
	writeRequest sandbox.DockerContainerWriteRequest,
) (sandbox.DockerContainerLifecycleRecord, error) {
	if err := validateDockerLifecycleAuthority(record.Intent, plan, writeRequest,
		s.transport.Endpoint()); err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	s.setLease(record.Lease)
	stopHeartbeat := s.startHeartbeat(ctx, record.Intent.ID)
	defer stopHeartbeat()
	ownership, err := record.Intent.LifecycleOwnership()
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}

	resourceID := lifecycleContainerIDFingerprint(record)
	if resourceID == "" && latestLifecycleTransition(record,
		sandbox.DockerContainerLifecycleTransitionCleaning) != nil {
		return s.finishAbsentRecovery(context.WithoutCancel(ctx), record)
	}
	if resourceID != "" {
		recovered, recoverErr := sandbox.NewRecoveredDockerContainerLifecycleRequest(
			record.Intent.AttemptID, record.Lease.Generation, writeRequest, resourceID,
			ownership, sandbox.DockerContainerLifecycleConfirmation)
		if recoverErr != nil {
			return sandbox.DockerContainerLifecycleRecord{}, recoverErr
		}
		observation, observeErr := s.transport.Observe(ctx, recovered)
		if observeErr != nil {
			return s.fail(ctx, record, lifecycleFailureReason(observeErr))
		}
		if observation.State == sandbox.DockerContainerLifecycleStateAbsent {
			if latestLifecycleTransition(record,
				sandbox.DockerContainerLifecycleTransitionCleaning) == nil {
				if denied := s.authorizeStart(ctx, record.Intent.ID,
					sandbox.DockerContainerLifecycleActionStart); denied != nil &&
					isDockerContainerLifecycleStartAuthorityDenied(denied) {
					return s.finishStartDeniedAbsent(context.WithoutCancel(ctx), record, denied)
				}
				return s.fail(ctx, record,
					sandbox.DockerContainerLifecycleFailureConfigMismatch)
			}
			return s.finishAbsentRecovery(context.WithoutCancel(ctx), record)
		}
		if observation.State == sandbox.DockerContainerLifecycleStateRunning &&
			latestLifecycleTransition(record,
				sandbox.DockerContainerLifecycleTransitionCleaning) == nil {
			if denied := s.authorizeStart(ctx, record.Intent.ID,
				sandbox.DockerContainerLifecycleActionStart); denied != nil {
				if !isDockerContainerLifecycleStartAuthorityDenied(denied) {
					return sandbox.DockerContainerLifecycleRecord{}, denied
				}
				record, err = s.ensureCleaning(context.WithoutCancel(ctx), record,
					sandbox.DockerContainerLifecycleReasonRestartRecovery)
				if err != nil {
					return sandbox.DockerContainerLifecycleRecord{}, err
				}
				cleaned, cleanupErr := s.runObserved(ctx, record, recovered, observation)
				if cleanupErr != nil {
					return cleaned, cleanupErr
				}
				return cleaned, denied
			}
		}
		return s.runObserved(ctx, record, recovered, observation)
	}

	// StageOwned can reconcile an uncertain create by exact name/config/labels.
	// Its create fence prepares and commits the action before the HTTP request.
	stage, err := s.stageOwned(ctx, record, writeRequest, ownership)
	if err != nil {
		if isDockerContainerLifecycleStartAuthorityDenied(err) {
			return s.finishStartDeniedAbsent(context.WithoutCancel(ctx), record, err)
		}
		return s.fail(ctx, record, lifecycleFailureReason(err))
	}
	request, err := sandbox.NewOwnedDockerContainerLifecycleRequest(record.Intent.AttemptID,
		record.Lease.Generation, writeRequest, stage, ownership,
		sandbox.DockerContainerLifecycleConfirmation)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}

	observation, err := s.transport.Observe(ctx, request)
	if err != nil {
		return s.fail(ctx, record, lifecycleFailureReason(err))
	}
	record, err = s.checkpointObservation(ctx, record, observation)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	return s.runObserved(ctx, record, request, observation)
}

func (s *DockerContainerLifecycleSupervisor) startHeartbeat(ctx context.Context,
	intentID string,
) context.CancelFunc {
	heartbeatCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	interval := s.leaseTTL / 3
	if interval > 30*time.Second {
		interval = 30 * time.Second
	}
	if interval < 250*time.Millisecond {
		interval = 250 * time.Millisecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				lease := s.currentLease()
				if lease.IntentID != intentID {
					return
				}
				renewed, err := s.store.RenewDockerContainerLifecycleLease(heartbeatCtx,
					lease, s.leaseTTL)
				if err != nil {
					// Retain the old fencing token. Every later daemon write must
					// pass the Store fence and therefore fails closed at expiry.
					return
				}
				s.setLease(renewed)
			}
		}
	}()
	return cancel
}

func (s *DockerContainerLifecycleSupervisor) runObserved(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord, request sandbox.DockerContainerLifecycleRequest,
	observation sandbox.DockerContainerLifecycleObservation,
) (sandbox.DockerContainerLifecycleRecord, error) {
	writeRequest := request.WriteRequest
	exitCheckpointedBeforeRun := latestLifecycleTransition(record,
		sandbox.DockerContainerLifecycleTransitionExited) != nil
	cleaningCheckpointedBeforeRun := latestLifecycleTransition(record,
		sandbox.DockerContainerLifecycleTransitionCleaning) != nil
	var err error
	if observation.State == sandbox.DockerContainerLifecycleStateCreated {
		createdObservation := observation
		if cleaningCheckpointedBeforeRun {
			return s.finishCreatedCleanup(context.WithoutCancel(ctx), record, request,
				createdObservation)
		}
		startCtx, startCancel := s.leaseBoundContext(ctx)
		observation, _, err = s.transport.Start(startCtx, request, s.fence(record.Intent.ID))
		startCancel()
		if err != nil {
			if isDockerContainerLifecycleStartAuthorityDenied(err) {
				return s.finishStartDeniedCreated(context.WithoutCancel(ctx), record,
					request, createdObservation, err)
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return s.finishInterruptedStart(ctx, record, request, err)
			}
			return s.fail(ctx, record, lifecycleFailureReason(err))
		}
		if err := ctx.Err(); err != nil {
			return s.finishInterruptedStart(ctx, record, request, err)
		}
		// Start returns the pre-start observation. Exact observe resolves the
		// response/commit crash window even when the caller cancels after its
		// daemon write. Only confirmation detaches; Wait retains the caller ctx.
		confirmationCtx, confirmationCancel := s.cleanupContext(ctx)
		observation, err = s.transport.Observe(confirmationCtx, request)
		if err != nil {
			failed, failureErr := s.fail(confirmationCtx, record, lifecycleFailureReason(err))
			confirmationCancel()
			return failed, failureErr
		}
		record, err = s.checkpointObservation(confirmationCtx, record, observation)
		confirmationCancel()
		if err != nil {
			return sandbox.DockerContainerLifecycleRecord{}, err
		}
	}

	if observation.State == sandbox.DockerContainerLifecycleStateRunning {
		cleaning := latestLifecycleTransition(record,
			sandbox.DockerContainerLifecycleTransitionCleaning)
		if cleaning == nil && writeRequest.Spec.StdinPipe {
			inputErr := s.handleRunning(ctx, record, request, observation)
			if inputErr != nil {
				err = inputErr
				reason := sandbox.DockerContainerLifecycleReasonCleanupStarted
				if errors.Is(inputErr, context.DeadlineExceeded) {
					reason = sandbox.DockerContainerLifecycleReasonTimeout
				} else if errors.Is(inputErr, context.Canceled) || ctx.Err() != nil {
					reason = sandbox.DockerContainerLifecycleReasonCancelled
				}
				record, inputErr = s.ensureCleaning(context.WithoutCancel(ctx), record, reason)
				if inputErr != nil {
					return sandbox.DockerContainerLifecycleRecord{}, inputErr
				}
				cleaning = latestLifecycleTransition(record,
					sandbox.DockerContainerLifecycleTransitionCleaning)
			}
		}
		if cleaning == nil {
			waitCtx, cancel := context.WithTimeout(ctx,
				time.Duration(writeRequest.Spec.Termination.TimeoutSeconds)*time.Second)
			observation, err = s.transport.Wait(waitCtx, request, s.fence(record.Intent.ID))
			cancel()
			if err != nil && !errors.Is(err, context.DeadlineExceeded) &&
				!errors.Is(err, context.Canceled) {
				return s.fail(ctx, record, lifecycleFailureReason(err))
			}
			if err == nil {
				confirmationCtx, confirmationCancel := s.cleanupContext(ctx)
				record, err = s.checkpointObservation(confirmationCtx, record, observation)
				confirmationCancel()
				if err != nil {
					return sandbox.DockerContainerLifecycleRecord{}, err
				}
			}
		}
		if cleaning != nil || err != nil {
			reason := sandbox.DockerContainerLifecycleReasonTimeout
			if cleaning != nil {
				reason = cleaning.ReasonCode
			} else if ctx.Err() != nil {
				reason = sandbox.DockerContainerLifecycleReasonCancelled
			}
			record, err = s.ensureCleaning(context.WithoutCancel(ctx), record, reason)
			if err != nil {
				return sandbox.DockerContainerLifecycleRecord{}, err
			}
			cleanupCtx, cleanupCancel := s.cleanupContext(ctx)
			termination, terminateErr := s.transport.Terminate(cleanupCtx, request,
				s.fence(record.Intent.ID))
			cleanupCancel()
			if terminateErr != nil {
				return s.fail(context.WithoutCancel(ctx), record, lifecycleFailureReason(terminateErr))
			}
			exitObservation := termination.Observation
			exitObservation.State = sandbox.DockerContainerLifecycleStateExited
			exitObservation.Running = false
			exitObservation.ExitCode = termination.ExitCode
			// Terminate's aggregate observation is intentionally not reused as a
			// transport receipt; observe exact final state before checkpoint.
			exitObservation, err = s.transport.Observe(context.WithoutCancel(ctx), request)
			if err != nil {
				return s.fail(context.WithoutCancel(ctx), record, lifecycleFailureReason(err))
			}
			record, err = s.checkpointObservation(context.WithoutCancel(ctx), record, exitObservation)
			if err != nil {
				return sandbox.DockerContainerLifecycleRecord{}, err
			}
			observation = exitObservation
		}
	}

	if observation.State != sandbox.DockerContainerLifecycleStateExited {
		return s.fail(context.WithoutCancel(ctx), record,
			sandbox.DockerContainerLifecycleReasonWaitFailed)
	}
	// An exited daemon observation discovered during restart recovery must be
	// checkpointed before the callback sees it. The ordinary wait/terminate
	// paths already checkpoint above, so this is idempotent.
	record, err = s.checkpointObservation(context.WithoutCancel(ctx), record, observation)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	var postExitErr error
	// Exited+cleaning on entry normally means a previous owner crossed the
	// post-exit boundary before attempting delete. CancelOne or an interrupted
	// Start may append a cancellation/timeout marker after a checkpointed exit,
	// so they use the same deterministic logical key and rely on callback
	// idempotency across the unavoidable callback/cleaning crash window.
	cleaning := latestLifecycleTransition(record,
		sandbox.DockerContainerLifecycleTransitionCleaning)
	if !exitCheckpointedBeforeRun || !cleaningCheckpointedBeforeRun ||
		(cleaning != nil && (cleaning.ReasonCode == sandbox.DockerContainerLifecycleReasonCancelled ||
			cleaning.ReasonCode == sandbox.DockerContainerLifecycleReasonTimeout)) {
		postExitErr = s.handlePostExit(context.WithoutCancel(ctx), record, request,
			observation)
	}
	reason := sandbox.DockerContainerLifecycleReasonNaturalExit
	if cleaning != nil {
		reason = cleaning.ReasonCode
	} else if ctx.Err() != nil {
		reason = sandbox.DockerContainerLifecycleReasonCancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			reason = sandbox.DockerContainerLifecycleReasonTimeout
		}
	}
	record, err = s.ensureCleaning(context.WithoutCancel(ctx), record, reason)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	cleanupCtx, cleanupCancel := s.cleanupContext(ctx)
	cleanup, err := s.transport.Cleanup(cleanupCtx, request, s.fence(record.Intent.ID))
	cleanupCancel()
	if err != nil {
		return s.fail(context.WithoutCancel(ctx), record, lifecycleFailureReason(err))
	}
	finished, finishErr := s.finish(context.WithoutCancel(ctx), record, observation, cleanup, reason)
	if finishErr != nil {
		return sandbox.DockerContainerLifecycleRecord{}, finishErr
	}
	if postExitErr != nil {
		return finished, postExitErr
	}
	return finished, nil
}

// Start may reach the daemon before cancellation interrupts its response or
// confirmation. The existing owned request and lease authorize cleanup only;
// the sticky cleaning transition prevents another create/start or Wait.
func (s *DockerContainerLifecycleSupervisor) finishInterruptedStart(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord, request sandbox.DockerContainerLifecycleRequest,
	interrupted error,
) (sandbox.DockerContainerLifecycleRecord, error) {
	cleanupCtx, cancel := s.cleanupContext(ctx)
	defer cancel()
	reason := sandbox.DockerContainerLifecycleReasonCancelled
	if errors.Is(interrupted, context.DeadlineExceeded) {
		reason = sandbox.DockerContainerLifecycleReasonTimeout
	}
	observation, err := s.transport.Observe(cleanupCtx, request)
	if err != nil {
		return s.fail(cleanupCtx, record, lifecycleFailureReason(err))
	}
	// SQLite requires the observed start before a cleaning marker or TERM WAL.
	// An interrupted response must not skip that durable side-effect receipt.
	record, err = s.checkpointObservation(cleanupCtx, record, observation)
	if err != nil {
		return record, err
	}
	record, err = s.ensureCleaning(cleanupCtx, record, reason)
	if err != nil {
		return record, err
	}
	if observation.State == sandbox.DockerContainerLifecycleStateAbsent {
		return s.finishAbsentRecovery(cleanupCtx, record)
	}
	return s.runObserved(cleanupCtx, record, request, observation)
}

func (s *DockerContainerLifecycleSupervisor) handleRunning(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord,
	lifecycleRequest sandbox.DockerContainerLifecycleRequest,
	observation sandbox.DockerContainerLifecycleObservation,
) error {
	if s.running == nil {
		return errors.New("Docker lifecycle stdin authority is unavailable")
	}
	plan, writeRequest, err := s.authority.RevalidateDockerContainerLifecycle(ctx,
		record.Intent)
	if err != nil || validateDockerLifecycleAuthority(record.Intent, plan, writeRequest,
		s.transport.Endpoint()) != nil ||
		writeRequest.RequestFingerprint != lifecycleRequest.WriteRequest.RequestFingerprint {
		return errors.New("Docker lifecycle running authority changed")
	}
	lease := s.currentLease()
	if err := s.store.FenceDockerContainerLifecycle(ctx, lease); err != nil {
		return err
	}
	record.Lease = lease
	request := DockerContainerLifecycleRunningRequest{
		ProtocolVersion: DockerContainerLifecycleRunningProtocolVersion,
		Record:          record, Plan: plan, WriteRequest: writeRequest,
		LifecycleRequest: lifecycleRequest, RunningObservation: observation,
		Lease: lease,
	}
	if request.Validate() != nil {
		return errors.New("Docker lifecycle running request is invalid")
	}
	return s.running.HandleDockerContainerLifecycleRunning(ctx, request,
		s.fence(record.Intent.ID))
}

func (s *DockerContainerLifecycleSupervisor) finishStartDeniedAbsent(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord, denied error,
) (sandbox.DockerContainerLifecycleRecord, error) {
	reason := sandbox.DockerContainerLifecycleReasonCleanupStarted
	if isDockerContainerLifecycleStartAuthorityCancelled(denied) {
		reason = sandbox.DockerContainerLifecycleReasonCancelled
	}
	record, err := s.ensureCleaning(ctx, record, reason)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	finished, err := s.finishAbsentRecovery(ctx, record)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	return finished, denied
}

func (s *DockerContainerLifecycleSupervisor) finishStartDeniedCreated(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord,
	request sandbox.DockerContainerLifecycleRequest,
	observation sandbox.DockerContainerLifecycleObservation, denied error,
) (sandbox.DockerContainerLifecycleRecord, error) {
	reason := sandbox.DockerContainerLifecycleReasonCleanupStarted
	if isDockerContainerLifecycleStartAuthorityCancelled(denied) {
		reason = sandbox.DockerContainerLifecycleReasonCancelled
	}
	record, err := s.ensureCleaning(ctx, record, reason)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	finished, err := s.finishCreatedCleanup(ctx, record, request, observation)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	return finished, denied
}

func (s *DockerContainerLifecycleSupervisor) finishCreatedCleanup(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord,
	request sandbox.DockerContainerLifecycleRequest,
	observation sandbox.DockerContainerLifecycleObservation,
) (sandbox.DockerContainerLifecycleRecord, error) {
	cleanupCtx, cleanupCancel := s.cleanupContext(ctx)
	cleanup, err := s.transport.Cleanup(cleanupCtx, request, s.fence(record.Intent.ID))
	cleanupCancel()
	if err != nil {
		return s.fail(ctx, record, lifecycleFailureReason(err))
	}
	finished, err := s.finishCleanupWithoutExit(ctx, record,
		observation.ContainerIDFingerprint, cleanup)
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	return finished, nil
}

func (s *DockerContainerLifecycleSupervisor) handlePostExit(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord,
	lifecycleRequest sandbox.DockerContainerLifecycleRequest,
	exitObservation sandbox.DockerContainerLifecycleObservation,
) error {
	if s.postExit == nil {
		return nil
	}
	hookCtx, cancel := s.leaseBoundContext(ctx)
	defer cancel()
	plan, writeRequest, err := s.authority.RevalidateDockerContainerLifecycle(hookCtx,
		record.Intent)
	if err != nil || validateDockerLifecycleAuthority(record.Intent, plan, writeRequest,
		s.transport.Endpoint()) != nil ||
		writeRequest.RequestFingerprint != lifecycleRequest.WriteRequest.RequestFingerprint {
		return dockerContainerLifecyclePostExitError()
	}
	lease := s.currentLease()
	if err := s.store.FenceDockerContainerLifecycle(hookCtx, lease); err != nil {
		return dockerContainerLifecyclePostExitError()
	}
	// The Store may have renewed the lease since the last transition snapshot.
	// Project that exact current lease into the callback's non-authorizing view.
	record.Lease = lease
	request := DockerContainerLifecyclePostExitRequest{
		ProtocolVersion:  DockerContainerLifecyclePostExitProtocolVersion,
		Record:           record,
		Plan:             plan,
		WriteRequest:     writeRequest,
		LifecycleRequest: lifecycleRequest,
		ExitObservation:  exitObservation,
		Lease:            lease,
	}
	request.InvocationKeyDigest = dockerContainerLifecyclePostExitInvocationKey(request)
	if request.Validate() != nil {
		return dockerContainerLifecyclePostExitError()
	}
	if err := s.postExit.HandleDockerContainerLifecyclePostExit(hookCtx, request); err != nil {
		return dockerContainerLifecyclePostExitError()
	}
	return nil
}

func dockerContainerLifecyclePostExitError() error {
	return apperror.New(apperror.CodeFailedPrecondition,
		"Docker lifecycle failed closed: "+DockerContainerLifecyclePostExitFailureReason)
}

func (s *DockerContainerLifecycleSupervisor) stageOwned(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord, request sandbox.DockerContainerWriteRequest,
	ownership sandbox.DockerContainerLifecycleOwnership,
) (sandbox.DockerContainerStageResult, error) {
	writeCtx, cancel := s.leaseBoundContext(ctx)
	defer cancel()
	return s.transport.StageOwned(writeCtx, request, ownership, s.fence(record.Intent.ID))
}

func (s *DockerContainerLifecycleSupervisor) fence(intentID string) sandbox.DockerContainerLifecycleFence {
	return func(ctx context.Context, action sandbox.DockerContainerLifecycleActionKind) error {
		lease := s.currentLease()
		if lease.IntentID != intentID {
			return apperror.New(apperror.CodeConflict, "Docker lifecycle fence intent changed")
		}
		// wait is a daemon read despite using POST. It is fenced, but does not
		// enter the external-write WAL table.
		if action != sandbox.DockerContainerLifecycleActionWait &&
			action != sandbox.DockerContainerLifecycleActionAttachStdin {
			deadline, ok := ctx.Deadline()
			if !ok || deadline.After(lease.ExpiresAt) {
				return apperror.New(apperror.CodeConflict,
					"Docker lifecycle daemon request is not bounded by the active lease")
			}
		}
		if action != sandbox.DockerContainerLifecycleActionWait {
			record, err := s.store.GetDockerContainerLifecycle(ctx, intentID)
			if err != nil {
				return err
			}
			prepared := false
			for _, existing := range record.Actions {
				if existing.LeaseGeneration == lease.Generation &&
					existing.Verb == string(action) {
					prepared = true
					break
				}
			}
			if !prepared {
				actionIntent, err := sandbox.NewDockerContainerLifecyclePreparedAction(intentID,
					len(record.Actions)+1, lease, string(action), s.now())
				if err != nil {
					return err
				}
				if _, _, err := s.store.PrepareDockerContainerLifecycleAction(ctx,
					actionIntent, lease); err != nil {
					return err
				}
			}
			// WAL intent is durable before the process-local gate. The gate is then
			// the final authority check before the lease fence and daemon mutation.
			if action == sandbox.DockerContainerLifecycleActionCreate ||
				action == sandbox.DockerContainerLifecycleActionStart {
				if err := s.authorizeStart(ctx, intentID, action); err != nil {
					return err
				}
			}
		}
		return s.store.FenceDockerContainerLifecycle(ctx, lease)
	}
}

func (s *DockerContainerLifecycleSupervisor) authorizeStart(ctx context.Context,
	intentID string, action sandbox.DockerContainerLifecycleActionKind,
) error {
	if s.startAuth == nil {
		return nil
	}
	authorityCtx, cancel := s.leaseBoundContext(ctx)
	defer cancel()
	if authorityCtx.Err() != nil {
		return dockerContainerLifecycleStartAuthorityError()
	}
	record, err := s.store.GetDockerContainerLifecycle(authorityCtx, intentID)
	if err != nil || record.Receipt != nil {
		return dockerContainerLifecycleStartAuthorityError()
	}
	plan, writeRequest, err := s.authority.RevalidateDockerContainerLifecycle(authorityCtx,
		record.Intent)
	if err != nil || validateDockerLifecycleAuthority(record.Intent, plan, writeRequest,
		s.transport.Endpoint()) != nil {
		return dockerContainerLifecycleStartAuthorityError()
	}
	lease := s.currentLease()
	if err := s.store.FenceDockerContainerLifecycle(authorityCtx, lease); err != nil {
		return dockerContainerLifecycleStartAuthorityError()
	}
	record.Lease = lease
	request := DockerContainerLifecycleStartAuthorityRequest{
		ProtocolVersion: DockerContainerLifecycleStartAuthorityProtocolVersion,
		Action:          action,
		Record:          record,
		Plan:            plan,
		WriteRequest:    writeRequest,
		Lease:           lease,
	}
	if request.Validate() != nil || authorityCtx.Err() != nil {
		return dockerContainerLifecycleStartAuthorityError()
	}
	if authErr := s.startAuth.AuthorizeDockerContainerLifecycleStart(authorityCtx,
		request); authErr != nil {
		if isDockerContainerLifecycleStartAuthorityCancelled(authErr) {
			return dockerContainerLifecycleStartAuthorityCancelledError()
		}
		return dockerContainerLifecycleStartAuthorityError()
	}
	if authorityCtx.Err() != nil {
		return dockerContainerLifecycleStartAuthorityError()
	}
	return nil
}

func dockerContainerLifecycleStartAuthorityError() error {
	return apperror.Wrap(apperror.CodePolicyDenied,
		"Docker lifecycle failed closed: "+DockerContainerLifecycleStartAuthorityDeniedReason,
		errDockerContainerLifecycleStartAuthorityDenied)
}

func isDockerContainerLifecycleStartAuthorityDenied(err error) bool {
	return errors.Is(err, errDockerContainerLifecycleStartAuthorityDenied) ||
		isDockerContainerLifecycleStartAuthorityCancelled(err)
}

func dockerContainerLifecycleStartAuthorityCancelledError() error {
	return apperror.Wrap(apperror.CodePolicyDenied,
		"Docker lifecycle failed closed: "+DockerContainerLifecycleStartAuthorityDeniedReason,
		errDockerContainerLifecycleStartAuthorityCancelled)
}

func isDockerContainerLifecycleStartAuthorityCancelled(err error) bool {
	return errors.Is(err, errDockerContainerLifecycleStartAuthorityCancelled)
}

func (s *DockerContainerLifecycleSupervisor) checkpointObservation(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord,
	observation sandbox.DockerContainerLifecycleObservation,
) (sandbox.DockerContainerLifecycleRecord, error) {
	var state, reason string
	var exitCode *int
	switch observation.State {
	case sandbox.DockerContainerLifecycleStateCreated:
		state, reason = sandbox.DockerContainerLifecycleTransitionCreated,
			sandbox.DockerContainerLifecycleReasonCreated
	case sandbox.DockerContainerLifecycleStateRunning:
		state, reason = sandbox.DockerContainerLifecycleTransitionStarted,
			sandbox.DockerContainerLifecycleReasonStarted
	case sandbox.DockerContainerLifecycleStateExited:
		if latestLifecycleTransition(record,
			sandbox.DockerContainerLifecycleTransitionStarted) == nil {
			var startErr error
			record, startErr = s.appendTransition(ctx, record,
				sandbox.DockerContainerLifecycleTransitionStarted,
				sandbox.DockerContainerLifecycleReasonRestartRecovery, nil,
				observation.ContainerIDFingerprint)
			if startErr != nil {
				return sandbox.DockerContainerLifecycleRecord{}, startErr
			}
		}
		state = sandbox.DockerContainerLifecycleTransitionExited
		reason = sandbox.DockerContainerLifecycleReasonNaturalExit
		if cleaning := latestLifecycleTransition(record,
			sandbox.DockerContainerLifecycleTransitionCleaning); cleaning != nil {
			reason = cleaning.ReasonCode
		}
		value := observation.ExitCode
		exitCode = &value
	case sandbox.DockerContainerLifecycleStateAbsent:
		return record, nil
	default:
		return sandbox.DockerContainerLifecycleRecord{}, apperror.New(
			apperror.CodeFailedPrecondition, "Docker lifecycle observation is inconsistent")
	}
	if record.TookOver && (state == sandbox.DockerContainerLifecycleTransitionCreated ||
		state == sandbox.DockerContainerLifecycleTransitionStarted) {
		reason = sandbox.DockerContainerLifecycleReasonRestartRecovery
	}
	if latestLifecycleTransition(record, state) != nil {
		return record, nil
	}
	return s.appendTransition(ctx, record, state, reason, exitCode,
		observation.ContainerIDFingerprint)
}

func (s *DockerContainerLifecycleSupervisor) ensureCleaning(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord, reason string,
) (sandbox.DockerContainerLifecycleRecord, error) {
	if latestLifecycleTransition(record, sandbox.DockerContainerLifecycleTransitionCleaning) != nil {
		return record, nil
	}
	return s.appendTransition(ctx, record, sandbox.DockerContainerLifecycleTransitionCleaning,
		reason, nil, "")
}

func (s *DockerContainerLifecycleSupervisor) appendTransition(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord, state, reason string, exitCode *int,
	containerIDFingerprint string,
) (sandbox.DockerContainerLifecycleRecord, error) {
	previous := ""
	if len(record.Transitions) > 0 {
		previous = record.Transitions[len(record.Transitions)-1].TransitionFingerprint
	}
	transition, err := sandbox.NewDockerContainerLifecycleTransition(record.Intent.ID,
		len(record.Transitions)+1, s.currentLease(), state, reason, exitCode,
		containerIDFingerprint, previous, s.now())
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	stored, _, err := s.store.AppendDockerContainerLifecycleTransition(ctx, transition,
		s.currentLease())
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Normalize(err)
	}
	return stored, nil
}

func (s *DockerContainerLifecycleSupervisor) fail(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord, reason string,
) (sandbox.DockerContainerLifecycleRecord, error) {
	if reason == "" {
		reason = sandbox.DockerContainerLifecycleReasonWaitFailed
	}
	failed, appendErr := s.appendTransition(ctx, record,
		sandbox.DockerContainerLifecycleTransitionFailed, reason, nil, "")
	if appendErr != nil {
		return record, appendErr
	}
	return failed, apperror.New(dockerLifecycleApplicationCode(reason),
		"Docker lifecycle failed closed: "+reason)
}

func (s *DockerContainerLifecycleSupervisor) finish(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord,
	exit sandbox.DockerContainerLifecycleObservation,
	cleanup sandbox.DockerContainerLifecycleCleanupResult, reason string,
) (sandbox.DockerContainerLifecycleRecord, error) {
	cleaned, err := s.appendTransition(ctx, record,
		sandbox.DockerContainerLifecycleTransitionCleaned,
		sandbox.DockerContainerLifecycleReasonCleanupCompleted, nil, "")
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	outcome := sandbox.DockerContainerLifecycleOutcomeNaturalExit
	switch reason {
	case sandbox.DockerContainerLifecycleReasonTimeout:
		outcome = sandbox.DockerContainerLifecycleOutcomeTimedOut
	case sandbox.DockerContainerLifecycleReasonCancelled:
		outcome = sandbox.DockerContainerLifecycleOutcomeCancelled
	case sandbox.DockerContainerLifecycleReasonCleanupStarted,
		sandbox.DockerContainerLifecycleReasonRestartRecovery:
		outcome = sandbox.DockerContainerLifecycleOutcomeFailed
	}
	exitCode := exit.ExitCode
	final := cleaned.Transitions[len(cleaned.Transitions)-1]
	receipt, err := sandbox.NewDockerContainerLifecycleReceipt(cleaned.Intent.ID,
		s.currentLease(), final, exit.ContainerIDFingerprint, outcome, &exitCode,
		cleanup.ContainerRemoved, cleanup.AlreadyAbsent, s.now())
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	stored, _, err := s.store.CompleteDockerContainerLifecycle(ctx, receipt, s.currentLease())
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Normalize(err)
	}
	if released, _, releaseErr := s.store.ReleaseDockerContainerLifecycleLease(ctx,
		s.currentLease()); releaseErr == nil {
		s.setLease(released)
		stored.Lease = released
	} else {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Normalize(releaseErr)
	}
	return stored, nil
}

func (s *DockerContainerLifecycleSupervisor) finishCleanupWithoutExit(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord, containerIDFingerprint string,
	cleanup sandbox.DockerContainerLifecycleCleanupResult,
) (sandbox.DockerContainerLifecycleRecord, error) {
	cleaned, err := s.appendTransition(ctx, record,
		sandbox.DockerContainerLifecycleTransitionCleaned,
		sandbox.DockerContainerLifecycleReasonCleanupCompleted, nil, "")
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	outcome := sandbox.DockerContainerLifecycleOutcomeFailed
	if cleaning := latestLifecycleTransition(cleaned,
		sandbox.DockerContainerLifecycleTransitionCleaning); cleaning != nil {
		switch cleaning.ReasonCode {
		case sandbox.DockerContainerLifecycleReasonNaturalExit:
			outcome = sandbox.DockerContainerLifecycleOutcomeNaturalExit
		case sandbox.DockerContainerLifecycleReasonTimeout:
			outcome = sandbox.DockerContainerLifecycleOutcomeTimedOut
		case sandbox.DockerContainerLifecycleReasonCancelled:
			outcome = sandbox.DockerContainerLifecycleOutcomeCancelled
		}
	}
	final := cleaned.Transitions[len(cleaned.Transitions)-1]
	receipt, err := sandbox.NewDockerContainerLifecycleReceipt(cleaned.Intent.ID,
		s.currentLease(), final, containerIDFingerprint,
		outcome, nil,
		cleanup.ContainerRemoved, cleanup.AlreadyAbsent, s.now())
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	stored, _, err := s.store.CompleteDockerContainerLifecycle(ctx, receipt, s.currentLease())
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Normalize(err)
	}
	released, _, err := s.store.ReleaseDockerContainerLifecycleLease(ctx, s.currentLease())
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Normalize(err)
	}
	s.setLease(released)
	stored.Lease = released
	return stored, nil
}

func (s *DockerContainerLifecycleSupervisor) finishAbsentRecovery(ctx context.Context,
	record sandbox.DockerContainerLifecycleRecord,
) (sandbox.DockerContainerLifecycleRecord, error) {
	if latestLifecycleTransition(record, sandbox.DockerContainerLifecycleTransitionCleaned) == nil {
		var err error
		record, err = s.appendTransition(ctx, record,
			sandbox.DockerContainerLifecycleTransitionCleaned,
			sandbox.DockerContainerLifecycleReasonRestartRecovery, nil, "")
		if err != nil {
			return sandbox.DockerContainerLifecycleRecord{}, err
		}
	}
	reason := sandbox.DockerContainerLifecycleReasonRestartRecovery
	if cleaning := latestLifecycleTransition(record,
		sandbox.DockerContainerLifecycleTransitionCleaning); cleaning != nil {
		reason = cleaning.ReasonCode
	}
	outcome := sandbox.DockerContainerLifecycleOutcomeFailed
	switch reason {
	case sandbox.DockerContainerLifecycleReasonNaturalExit:
		outcome = sandbox.DockerContainerLifecycleOutcomeNaturalExit
	case sandbox.DockerContainerLifecycleReasonTimeout:
		outcome = sandbox.DockerContainerLifecycleOutcomeTimedOut
	case sandbox.DockerContainerLifecycleReasonCancelled:
		outcome = sandbox.DockerContainerLifecycleOutcomeCancelled
	}
	var exitCode *int
	if exited := latestLifecycleTransition(record,
		sandbox.DockerContainerLifecycleTransitionExited); exited != nil {
		exitCode = exited.ExitCode
	}
	final := record.Transitions[len(record.Transitions)-1]
	receipt, err := sandbox.NewDockerContainerLifecycleReceipt(record.Intent.ID,
		s.currentLease(), final, lifecycleContainerIDFingerprint(record), outcome,
		exitCode, false, true, s.now())
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, err
	}
	stored, _, err := s.store.CompleteDockerContainerLifecycle(ctx, receipt, s.currentLease())
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Normalize(err)
	}
	released, _, err := s.store.ReleaseDockerContainerLifecycleLease(ctx, s.currentLease())
	if err != nil {
		return sandbox.DockerContainerLifecycleRecord{}, apperror.Normalize(err)
	}
	s.setLease(released)
	stored.Lease = released
	return stored, nil
}

func (s *DockerContainerLifecycleSupervisor) cleanupContext(parent context.Context) (context.Context,
	context.CancelFunc,
) {
	base := context.WithoutCancel(parent)
	lease := s.currentLease()
	remaining := time.Until(lease.ExpiresAt)
	if remaining <= 0 {
		remaining = time.Millisecond
	}
	if remaining > 15*time.Second {
		remaining = 15 * time.Second
	}
	return context.WithTimeout(base, remaining)
}

func (s *DockerContainerLifecycleSupervisor) leaseBoundContext(parent context.Context) (
	context.Context, context.CancelFunc,
) {
	lease := s.currentLease()
	deadline := lease.ExpiresAt
	if parentDeadline, ok := parent.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	return context.WithDeadline(parent, deadline)
}

func (s *DockerContainerLifecycleSupervisor) setLease(lease sandbox.DockerContainerLifecycleLease) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lease = lease
}

func (s *DockerContainerLifecycleSupervisor) currentLease() sandbox.DockerContainerLifecycleLease {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lease
}

func validateDockerLifecycleAuthority(intent sandbox.DockerContainerLaunchIntent,
	plan sandbox.DockerContainerPlan, request sandbox.DockerContainerWriteRequest,
	endpoint sandbox.DockerObservationEndpoint,
) error {
	if intent.Validate() != nil || plan.Validate() != nil || request.Validate() != nil ||
		endpoint.Validate() != nil || intent.PlanID != plan.ID || intent.RunID != plan.RunID ||
		intent.MissionID != plan.MissionID || intent.WorkspaceID != plan.WorkspaceID ||
		intent.RequestedBy != plan.RequestedBy || intent.RequestFingerprint != request.RequestFingerprint ||
		intent.SpecFingerprint != plan.SpecFingerprint || intent.PlanFingerprint != plan.PlanFingerprint ||
		intent.AuthorityFingerprint != plan.AuthorityFingerprint ||
		intent.BaseLabelPlanFingerprint != plan.LabelPlanFingerprint ||
		intent.ContainerNameFingerprint != plan.ContainerNameFingerprint ||
		intent.EndpointClass != endpoint.Class || intent.EndpointFingerprint != endpoint.Fingerprint ||
		sandbox.DockerContainerPlanMatchesSpec(plan, request.Spec) != nil ||
		plan.NetworkMode != "disabled" || plan.NetworkTargetCount != 0 ||
		plan.EnvironmentCount != 0 || plan.SecretReferenceCount != 0 ||
		!plan.SimulationOnly || plan.ProductionSubmitted || plan.ProductionVerified ||
		plan.BackendAvailable || plan.BackendEnabled || plan.ExecutionAuthorized ||
		plan.ArtifactCommitAuthorized {
		return apperror.New(apperror.CodeFailedPrecondition,
			"Docker lifecycle current authority does not exactly match durable intent")
	}
	return nil
}

func latestLifecycleTransition(record sandbox.DockerContainerLifecycleRecord,
	state string,
) *sandbox.DockerContainerLifecycleTransition {
	for index := len(record.Transitions) - 1; index >= 0; index-- {
		if record.Transitions[index].State == state {
			value := record.Transitions[index]
			return &value
		}
	}
	return nil
}

func lifecycleContainerIDFingerprint(record sandbox.DockerContainerLifecycleRecord) string {
	for index := len(record.Transitions) - 1; index >= 0; index-- {
		if record.Transitions[index].ContainerIDFingerprint != "" {
			return record.Transitions[index].ContainerIDFingerprint
		}
	}
	return ""
}

func lifecycleFailureReason(err error) string {
	if code := sandbox.DockerContainerLifecycleErrorCode(err); code != "" {
		return code
	}
	switch {
	case errors.Is(err, context.Canceled):
		return sandbox.DockerContainerLifecycleReasonCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return sandbox.DockerContainerLifecycleReasonTimeout
	default:
		return sandbox.DockerContainerLifecycleReasonWaitFailed
	}
}

func dockerLifecycleApplicationCode(reason string) apperror.Code {
	switch reason {
	case sandbox.DockerContainerLifecycleFailureDisabled,
		sandbox.DockerContainerLifecycleFailureUnsupported,
		sandbox.DockerContainerLifecycleFailureConnection:
		return apperror.CodeUnavailable
	case sandbox.DockerContainerLifecycleReasonCancelled:
		return apperror.CodeCancelled
	case sandbox.DockerContainerLifecycleReasonTimeout:
		return apperror.CodeDeadlineExceeded
	case sandbox.DockerContainerLifecycleFailureConfigMismatch,
		sandbox.DockerContainerLifecycleFailureUnsafeExisting,
		sandbox.DockerContainerLifecycleFailureInvalidResponse:
		return apperror.CodeFailedPrecondition
	default:
		return apperror.CodeFailedPrecondition
	}
}
