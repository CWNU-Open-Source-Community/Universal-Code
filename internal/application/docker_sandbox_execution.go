package application

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
)

const (
	dockerSandboxStartOperationProtocol  = "docker_sandbox_start_operation.v1"
	dockerSandboxStartRequestProtocol    = "docker_sandbox_start_request.v1"
	dockerSandboxCancelOperationProtocol = "docker_sandbox_cancel_operation.v1"
	dockerSandboxOutputCommitProtocol    = "docker_sandbox_output_commit_operation.v1"
	standardCodeGitMetadataMaskFile      = "standard-code-git-metadata-mask.v1"
)

type DockerSandboxStartRequest struct {
	AdmissionID  string
	OperationKey string
	RequestedBy  string
}

type DockerSandboxStartResult struct {
	Record   domain.DockerSandboxRecord
	Replayed bool
}

type DockerSandboxCancelRequest struct {
	AdmissionID  string
	OperationKey string
	RequestedBy  string
}

type DockerSandboxCancelResult struct {
	Cancellation domain.DockerSandboxCancellation
	Record       domain.DockerSandboxRecord
	Replayed     bool
}

// Start executes one already-authorized admission through the durable v97
// lifecycle. Execution outcomes are returned as terminal product receipts;
// infrastructure errors are returned only while recovery work remains.
func (s *DockerSandboxService) Start(ctx context.Context,
	request DockerSandboxStartRequest,
) (DockerSandboxStartResult, error) {
	if err := s.requireExecutionConfigured(); err != nil {
		noteDockerCommandRuntimeDispatchPossible(ctx)
		return DockerSandboxStartResult{}, err
	}
	normalized, err := normalizeDockerSandboxStartRequest(request)
	if err != nil {
		noteDockerCommandRuntimeDispatchPossible(ctx)
		return DockerSandboxStartResult{}, apperror.Wrap(apperror.CodeInvalidArgument,
			"Docker Sandbox start request is invalid", err)
	}
	// The process-local admission gate covers the read and Start WAL as well as
	// execution. Another Start or admission-only Cancel cannot dispatch between
	// observing no previous Start/Launch and a definite pre-lifecycle refusal.
	activeCtx, cancel, err := s.registerActive(normalized.AdmissionID, ctx)
	if err != nil {
		noteDockerCommandRuntimeDispatchPossible(ctx)
		return DockerSandboxStartResult{}, err
	}
	defer s.unregisterActive(normalized.AdmissionID)
	defer cancel()
	record, err := s.store.GetDockerSandboxRecord(activeCtx, normalized.AdmissionID)
	if err != nil {
		noteDockerCommandRuntimeDispatchPossible(ctx)
		return DockerSandboxStartResult{}, apperror.Normalize(err)
	}
	if record.Start != nil || record.Launch != nil {
		noteDockerCommandRuntimeDispatchPossible(ctx)
	}
	if normalized.RequestedBy != record.Admission.RequestedBy {
		return DockerSandboxStartResult{}, apperror.New(apperror.CodePolicyDenied,
			"Docker Sandbox start requester does not own the admission")
	}
	operationDigest := runmutation.Fingerprint(dockerSandboxStartOperationProtocol,
		record.Admission.ID, record.Admission.RunID, normalized.OperationKey)
	requestFingerprint := runmutation.Fingerprint(dockerSandboxStartRequestProtocol,
		record.Admission.ID, record.Admission.RunID, normalized.RequestedBy,
		record.Admission.AdmissionFingerprint,
		record.Admission.RuntimeEpochFingerprint)
	if record.Start != nil {
		if record.Start.OperationKeyDigest != operationDigest ||
			record.Start.RequestFingerprint != requestFingerprint ||
			record.Start.RequestedBy != normalized.RequestedBy {
			return DockerSandboxStartResult{}, apperror.New(apperror.CodeConflict,
				"Docker Sandbox admission already has a different start request")
		}
		if record.Start.RuntimeEpochFingerprint != s.runtimeEpochFingerprint {
			return DockerSandboxStartResult{}, apperror.New(
				apperror.CodeFailedPrecondition,
				"Docker Sandbox start authority is unavailable after restart")
		}
	} else {
		if record.Admission.RuntimeEpochFingerprint != s.runtimeEpochFingerprint {
			return DockerSandboxStartResult{}, apperror.New(
				apperror.CodeFailedPrecondition,
				"Docker Sandbox admission belongs to a previous runtime epoch")
		}
		now := s.now().UTC()
		if !now.Before(record.Admission.ReadinessExpiresAt) {
			return DockerSandboxStartResult{}, apperror.New(
				apperror.CodeFailedPrecondition,
				"Docker Sandbox readiness expired before its first start")
		}
		start := domain.DockerSandboxStartIntent{
			AdmissionID:             record.Admission.ID,
			ProtocolVersion:         domain.DockerSandboxStartProtocolVersion,
			OperationKeyDigest:      operationDigest,
			RequestFingerprint:      requestFingerprint,
			RuntimeEpochFingerprint: s.runtimeEpochFingerprint,
			RunID:                   record.Admission.RunID, RequestedBy: normalized.RequestedBy,
			CreatedAt: now,
		}
		start.StartFingerprint = domain.DockerSandboxStartFingerprint(start)
		stored, replayed, beginErr := s.store.BeginDockerSandboxStart(activeCtx, start)
		if beginErr != nil {
			// A read or commit failure cannot prove that a Start WAL was absent
			// or rolled back; preserve recovery until durable cleanup is known.
			noteDockerCommandRuntimeDispatchPossible(ctx)
			return DockerSandboxStartResult{}, apperror.Normalize(beginErr)
		}
		if replayed {
			noteDockerCommandRuntimeDispatchPossible(ctx)
		}
		record.Start = &stored
	}
	if record.Receipt != nil {
		record.Replayed = true
		return DockerSandboxStartResult{Record: record, Replayed: true}, nil
	}
	result, err := s.executeActiveAdmission(activeCtx, record)
	return DockerSandboxStartResult{Record: result, Replayed: result.Replayed}, err
}

// Cancel persists a sticky cancellation before touching an active context or
// taking over an expired lifecycle lease. A restarted process can therefore
// finish cleanup without recovering start authority.
func (s *DockerSandboxService) Cancel(ctx context.Context,
	request DockerSandboxCancelRequest,
) (DockerSandboxCancelResult, error) {
	if err := s.requireCleanupConfigured(); err != nil {
		return DockerSandboxCancelResult{}, err
	}
	request.AdmissionID = strings.TrimSpace(request.AdmissionID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	if !validDockerSandboxIdentity(request.AdmissionID) ||
		!validDockerSandboxIdentity(request.RequestedBy) ||
		!validDockerSandboxOperationKey(request.OperationKey) {
		return DockerSandboxCancelResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Docker Sandbox cancel request is invalid")
	}
	record, err := s.store.GetDockerSandboxRecord(ctx, request.AdmissionID)
	if err != nil {
		return DockerSandboxCancelResult{}, apperror.Normalize(err)
	}
	if request.RequestedBy != record.Admission.RequestedBy {
		return DockerSandboxCancelResult{}, apperror.New(apperror.CodePolicyDenied,
			"Docker Sandbox cancel requester does not own the admission")
	}
	digest := runmutation.Fingerprint(dockerSandboxCancelOperationProtocol,
		record.Admission.ID, record.Admission.RunID, request.OperationKey)
	if record.Receipt != nil {
		if existing, found, lookupErr := s.store.GetDockerSandboxCancellation(ctx,
			record.Admission.ID); lookupErr != nil {
			return DockerSandboxCancelResult{}, apperror.Normalize(lookupErr)
		} else if found && record.Receipt.Outcome ==
			domain.DockerSandboxOutcomeCancelled {
			if existing.OperationKeyDigest != digest || existing.RequestedBy != request.RequestedBy {
				return DockerSandboxCancelResult{}, apperror.New(apperror.CodeConflict,
					"Docker Sandbox admission already has a different cancellation request")
			}
			record.Replayed = true
			return DockerSandboxCancelResult{Cancellation: existing, Record: record,
				Replayed: true}, nil
		}
		return DockerSandboxCancelResult{}, apperror.New(
			apperror.CodeFailedPrecondition,
			"Docker Sandbox attempt is already terminal")
	}
	now := s.now().UTC()
	// An active cancellation can be durable before cleanup has a receipt. Reuse
	// its server-owned timestamp so an identical retry keeps the original exact
	// fingerprint; the existing store transaction still checks every binding.
	if existing, found, lookupErr := s.store.GetDockerSandboxCancellation(ctx,
		record.Admission.ID); lookupErr != nil {
		return DockerSandboxCancelResult{}, apperror.Normalize(lookupErr)
	} else if found && existing.OperationKeyDigest == digest &&
		existing.RequestedBy == request.RequestedBy {
		now = existing.RequestedAt
	}
	cancellation := domain.DockerSandboxCancellation{
		ID:              "docker-sandbox-cancel-" + digest[:24],
		AdmissionID:     record.Admission.ID,
		ProtocolVersion: domain.DockerSandboxCancellationProtocolVersion,
		RunID:           record.Admission.RunID, RequestedBy: request.RequestedBy,
		OperationKeyDigest: digest, ReasonCode: domain.DockerSandboxReasonCancelled,
		RequestedAt: now,
	}
	cancellation.CancellationFingerprint =
		domain.DockerSandboxCancellationFingerprint(cancellation)
	stored, replayed, err := s.store.RequestDockerSandboxCancellation(ctx, cancellation)
	if apperror.CodeOf(err) == apperror.CodeConflict {
		// Two first requests can both observe absence and choose different
		// timestamps. One bounded reread/retry converges only the same exact
		// request, while the store remains the atomic identity boundary.
		existing, found, lookupErr := s.store.GetDockerSandboxCancellation(ctx,
			record.Admission.ID)
		if lookupErr != nil {
			return DockerSandboxCancelResult{}, apperror.Normalize(lookupErr)
		}
		if found && existing.Validate() == nil && existing.ID == cancellation.ID &&
			existing.AdmissionID == cancellation.AdmissionID && existing.RunID == cancellation.RunID &&
			existing.RequestedBy == cancellation.RequestedBy &&
			existing.OperationKeyDigest == cancellation.OperationKeyDigest &&
			existing.ReasonCode == cancellation.ReasonCode {
			cancellation.RequestedAt = existing.RequestedAt
			cancellation.CancellationFingerprint = domain.DockerSandboxCancellationFingerprint(cancellation)
			stored, replayed, err = s.store.RequestDockerSandboxCancellation(ctx, cancellation)
		}
	}
	if err != nil {
		return DockerSandboxCancelResult{}, apperror.Normalize(err)
	}
	if s.cancelActive(record.Admission.ID) {
		return DockerSandboxCancelResult{Cancellation: stored, Record: record,
			Replayed: replayed}, nil
	}
	result, executeErr := s.cancelInactiveAdmission(ctx, record)
	return DockerSandboxCancelResult{Cancellation: stored, Record: result,
		Replayed: replayed || result.Replayed}, executeErr
}

// RecoverStartup converges only admissions that already crossed the durable
// lifecycle-launch boundary. Admission-only records are audit evidence and are
// deliberately not turned into new execution attempts after restart.
func (s *DockerSandboxService) RecoverStartup(ctx context.Context) (
	[]domain.DockerSandboxRecord, error,
) {
	if err := s.requireCleanupConfigured(); err != nil {
		return nil, err
	}
	values, err := s.store.ListRecoverableDockerSandboxes(ctx,
		DockerSandboxRecoveryLimit)
	if err != nil {
		return nil, apperror.Normalize(err)
	}
	results := make([]domain.DockerSandboxRecord, 0, len(values))
	for _, value := range values {
		if value.Launch == nil {
			results = append(results, value)
			continue
		}
		result, recoverErr := s.recoverAdmission(ctx, value)
		if recoverErr != nil {
			return results, recoverErr
		}
		results = append(results, result)
	}
	return results, nil
}

func (s *DockerSandboxService) executeAdmission(ctx context.Context,
	record domain.DockerSandboxRecord,
) (domain.DockerSandboxRecord, error) {
	activeCtx, cancel, err := s.registerActive(record.Admission.ID, ctx)
	if err != nil {
		noteDockerCommandRuntimeDispatchPossible(ctx)
		return domain.DockerSandboxRecord{}, err
	}
	defer s.unregisterActive(record.Admission.ID)
	defer cancel()
	return s.executeActiveAdmission(activeCtx, record)
}

// executeActiveAdmission requires the caller to hold this admission's active
// gate. A freshly committed, non-replayed Start has no daemon effects until
// the lifecycle boundary below; earlier validation failures need no reaping.
func (s *DockerSandboxService) executeActiveAdmission(ctx context.Context,
	record domain.DockerSandboxRecord,
) (domain.DockerSandboxRecord, error) {
	if record.Start == nil {
		if _, cancelled, err := s.store.GetDockerSandboxCancellation(ctx,
			record.Admission.ID); err != nil || !cancelled {
			if err != nil {
				return domain.DockerSandboxRecord{}, apperror.Normalize(err)
			}
			return domain.DockerSandboxRecord{}, apperror.New(
				apperror.CodeFailedPrecondition,
				"Docker Sandbox has no durable start or cancellation request")
		}
	}
	executionCtx, timeoutCancel := context.WithTimeout(ctx,
		time.Duration(record.Admission.WallClockSeconds)*time.Second)
	defer timeoutCancel()
	plan, writeRequest, err := s.reconstructDockerSandboxWriteRequest(executionCtx,
		record.Admission)
	if err != nil {
		return domain.DockerSandboxRecord{}, err
	}
	supervisor, err := s.newLifecycleSupervisor()
	if err != nil {
		return domain.DockerSandboxRecord{}, err
	}
	// Lifecycle lookup/WAL failures, replay and every subsequent daemon write
	// can have an unknown outcome. Only its durable cleanup receipt can clear
	// this provenance; no error text or missing return value establishes it.
	noteDockerCommandRuntimeDispatchPossible(ctx)
	lifecycle, lifecycleErr := supervisor.BeginAndRun(executionCtx, plan, writeRequest,
		record.Admission.RequestedBy,
		dockerSandboxLifecycleKey(record.Admission.OperationKeyDigest))
	if lifecycle.Intent.ID != "" && lifecycle.Receipt == nil && lifecycle.Replayed &&
		lifecycleErr == nil {
		lifecycle, lifecycleErr = supervisor.RecoverOne(executionCtx, lifecycle.Intent.ID)
	}
	if lifecycle.Receipt == nil {
		return record, lifecycleErr
	}
	if errors.Is(executionCtx.Err(), context.DeadlineExceeded) &&
		lifecycle.Receipt.Outcome == sandbox.DockerContainerLifecycleOutcomeCancelled {
		lifecycleErr = context.DeadlineExceeded
	}
	finalizeCtx, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx),
		15*time.Second)
	defer finalizeCancel()
	completed, completeErr := s.completeDockerSandbox(finalizeCtx, record.Admission,
		lifecycle, lifecycleErr)
	if completeErr != nil {
		return completed, completeErr
	}
	return completed, nil
}

func (s *DockerSandboxService) cancelInactiveAdmission(ctx context.Context,
	record domain.DockerSandboxRecord,
) (domain.DockerSandboxRecord, error) {
	if record.Launch == nil {
		// Build the durable lifecycle intent; the start callback sees the sticky
		// cancellation and closes it without issuing create/start.
		return s.executeAdmission(ctx, record)
	}
	supervisor, err := s.newLifecycleSupervisor()
	if err != nil {
		return record, err
	}
	lifecycle, err := supervisor.CancelOne(ctx, record.Launch.LifecycleIntentID)
	if lifecycle.Receipt == nil {
		return record, err
	}
	return s.completeDockerSandbox(ctx, record.Admission, lifecycle, err)
}

func (s *DockerSandboxService) recoverAdmission(ctx context.Context,
	record domain.DockerSandboxRecord,
) (domain.DockerSandboxRecord, error) {
	lifecycle, err := s.store.GetDockerContainerLifecycle(ctx,
		record.Launch.LifecycleIntentID)
	if err != nil {
		return record, apperror.Normalize(err)
	}
	if lifecycle.Receipt == nil {
		supervisor, supervisorErr := s.newLifecycleSupervisor()
		if supervisorErr != nil {
			return record, supervisorErr
		}
		if _, cancelled, cancelErr := s.store.GetDockerSandboxCancellation(ctx,
			record.Admission.ID); cancelErr != nil {
			return record, apperror.Normalize(cancelErr)
		} else if cancelled {
			lifecycle, err = supervisor.CancelOne(ctx, lifecycle.Intent.ID)
		} else {
			lifecycle, err = supervisor.RecoverOne(ctx, lifecycle.Intent.ID)
		}
	}
	if lifecycle.Receipt == nil {
		return record, err
	}
	return s.completeDockerSandbox(ctx, record.Admission, lifecycle, err)
}

func (s *DockerSandboxService) newLifecycleSupervisor() (
	*DockerContainerLifecycleSupervisor, error,
) {
	supervisor, err := NewDockerContainerLifecycleSupervisor(s.store,
		s.lifecycleTransport, s, idgen.New("docker-sandbox-owner"), s.leaseTTL)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeFailedPrecondition,
			"Docker Sandbox lifecycle Supervisor is unavailable", err)
	}
	return supervisor.WithDockerContainerLifecycleStartAuthority(s).
		WithDockerContainerLifecycleRunning(s).
		WithDockerContainerLifecyclePostExit(s), nil
}

func (s *DockerSandboxService) commandRuntimeStdinAvailable() bool {
	return s != nil && s.stdinTransport != nil && s.lifecycleTransport != nil &&
		s.stdinTransport.SupportsOwnedStdin() &&
		s.stdinTransport.Endpoint() == s.lifecycleTransport.Endpoint()
}

func (s *DockerSandboxService) registerCommandRuntimeStdin(admissionID string,
	stdin io.ReadCloser,
) (func(), error) {
	admissionID = strings.TrimSpace(admissionID)
	if !s.commandRuntimeStdinAvailable() || stdin == nil ||
		!validDockerSandboxIdentity(admissionID) {
		if stdin != nil {
			_ = stdin.Close()
		}
		return nil, errors.New("Docker Command Runtime stdin transport is unavailable")
	}
	s.stdinMu.Lock()
	if _, exists := s.activeStdin[admissionID]; exists {
		s.stdinMu.Unlock()
		_ = stdin.Close()
		return nil, errors.New("Docker Command Runtime stdin is already registered")
	}
	s.activeStdin[admissionID] = stdin
	s.stdinMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.stdinMu.Lock()
			current, exists := s.activeStdin[admissionID]
			if exists {
				delete(s.activeStdin, admissionID)
			}
			s.stdinMu.Unlock()
			if exists {
				_ = current.Close()
			}
		})
	}, nil
}

func (s *DockerSandboxService) HandleDockerContainerLifecycleRunning(ctx context.Context,
	request DockerContainerLifecycleRunningRequest,
	fence sandbox.DockerContainerLifecycleFence,
) error {
	if request.Validate() != nil || !request.WriteRequest.Spec.StdinPipe ||
		fence == nil || !s.commandRuntimeStdinAvailable() {
		return errors.New("Docker Command Runtime running input request is invalid")
	}
	record, found, err := s.store.GetDockerSandboxRecordByLifecycleIntent(ctx,
		request.Record.Intent.ID)
	if err != nil || !found || record.Launch == nil ||
		record.Launch.LifecycleIntentID != request.Record.Intent.ID {
		return errors.Join(err,
			errors.New("Docker Command Runtime input has no product owner"))
	}
	s.stdinMu.Lock()
	stdin := s.activeStdin[record.Admission.ID]
	s.stdinMu.Unlock()
	if stdin == nil {
		return errors.New("Docker Command Runtime input authority did not survive restart")
	}
	return s.stdinTransport.AttachOwnedStdin(ctx, request.LifecycleRequest,
		stdin, fence)
}

// RevalidateDockerContainerLifecycle implements cleanup-safe exact authority
// reconstruction. It intentionally does not require live start permission;
// that permission is checked separately immediately before create/start.
func (s *DockerSandboxService) RevalidateDockerContainerLifecycle(ctx context.Context,
	intent sandbox.DockerContainerLaunchIntent,
) (sandbox.DockerContainerPlan, sandbox.DockerContainerWriteRequest, error) {
	if s == nil || s.store == nil || intent.Validate() != nil {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{},
			errors.New("Docker Sandbox lifecycle intent is invalid")
	}
	admission, found, err := s.store.GetDockerSandboxAdmissionByLifecycleOperation(ctx,
		intent.OperationKeyDigest)
	if err != nil || !found {
		if err == nil {
			err = errors.New("Docker Sandbox lifecycle has no product admission")
		}
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{}, err
	}
	plan, request, err := s.reconstructDockerSandboxWriteRequest(ctx, admission)
	if err != nil {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{}, err
	}
	if intent.PlanID != admission.PlanID || intent.RunID != admission.RunID ||
		intent.MissionID != admission.MissionID || intent.WorkspaceID != admission.WorkspaceID ||
		intent.RequestedBy != admission.RequestedBy ||
		intent.SpecFingerprint != admission.SpecFingerprint ||
		intent.PlanFingerprint != admission.PlanFingerprint ||
		intent.AuthorityFingerprint != admission.AuthorityFingerprint ||
		intent.RequestFingerprint != request.RequestFingerprint ||
		intent.EndpointClass != s.lifecycleTransport.Endpoint().Class ||
		intent.EndpointFingerprint != s.lifecycleTransport.Endpoint().Fingerprint {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{},
			errors.New("Docker Sandbox lifecycle intent no longer binds product authority")
	}
	product, productErr := s.store.GetDockerSandboxRecord(ctx, admission.ID)
	if productErr != nil {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{},
			productErr
	}
	startOperationKeyDigest := ""
	if product.Start != nil {
		startOperationKeyDigest = product.Start.OperationKeyDigest
	} else if _, cancelled, cancelErr := s.store.GetDockerSandboxCancellation(ctx,
		admission.ID); cancelErr != nil || !cancelled {
		if cancelErr == nil {
			cancelErr = errors.New("Docker Sandbox lifecycle has no start or cancellation WAL")
		}
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{},
			cancelErr
	}
	launch := domain.DockerSandboxLaunch{
		AdmissionID:                 admission.ID,
		ProtocolVersion:             domain.DockerSandboxLaunchProtocolVersion,
		StartOperationKeyDigest:     startOperationKeyDigest,
		LifecycleIntentID:           intent.ID,
		LifecycleRequestFingerprint: intent.RequestFingerprint,
		AttemptID:                   intent.AttemptID, RunID: intent.RunID, CreatedAt: intent.CreatedAt,
	}
	launch.LaunchFingerprint = domain.DockerSandboxLaunchFingerprint(launch)
	if _, _, err := s.store.BindDockerSandboxLaunch(ctx, launch); err != nil {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{}, err
	}
	return plan, request, nil
}

// AuthorizeDockerContainerLifecycleStart is the final process-local start
// check. A persisted admission or Full Access setting alone can never pass it.
func (s *DockerSandboxService) AuthorizeDockerContainerLifecycleStart(ctx context.Context,
	request DockerContainerLifecycleStartAuthorityRequest,
) error {
	if request.Validate() != nil || s.requireExecutionConfigured() != nil {
		return errors.New("Docker Sandbox start authority request is invalid")
	}
	record, found, err := s.store.GetDockerSandboxRecordByLifecycleIntent(ctx,
		request.Record.Intent.ID)
	if err != nil || !found || record.Launch == nil ||
		record.Launch.LifecycleRequestFingerprint != request.WriteRequest.RequestFingerprint {
		return errors.New("Docker Sandbox lifecycle launch is not product-bound")
	}
	if _, cancelled, cancelErr := s.store.GetDockerSandboxCancellation(ctx,
		record.Admission.ID); cancelErr != nil {
		return errors.New("Docker Sandbox cancellation state is unavailable")
	} else if cancelled {
		return errDockerContainerLifecycleStartAuthorityCancelled
	}
	if record.Start == nil {
		return errors.New("Docker Sandbox lifecycle launch has no durable start request")
	}
	return s.requireCurrentDockerSandboxStartAuthority(ctx, record.Admission,
		request.Plan, request.WriteRequest)
}

func (s *DockerSandboxService) requireCurrentDockerSandboxStartAuthority(ctx context.Context,
	admission domain.DockerSandboxAdmission, plan sandbox.DockerContainerPlan,
	writeRequest sandbox.DockerContainerWriteRequest,
) error {
	if admission.Validate() != nil || admission.RuntimeEpochFingerprint !=
		s.runtimeEpochFingerprint || !s.dockerCapabilities.Enabled ||
		!admission.ReadinessExpiresAt.After(s.now().UTC()) || plan.ID != admission.PlanID ||
		writeRequest.RequestFingerprint == "" {
		return errors.New("Docker Sandbox process-local authority is unavailable")
	}
	manifest, err := sandbox.DecodeManifest([]byte(admission.ManifestJSON))
	if err != nil {
		return err
	}
	authority, err := s.loadCurrentDockerSandboxAuthority(ctx, admission.PlanID,
		manifest, admission.RequestedBy)
	if err != nil {
		return err
	}
	readiness, err := s.readiness.Check(ctx, s.dockerCapabilities, manifest,
		authority.Plan.ImageDigest)
	if err != nil || !readiness.ReadyAt(s.now().UTC()) ||
		readiness.EndpointClass != s.lifecycleTransport.Endpoint().Class ||
		readiness.EndpointFingerprint != s.lifecycleTransport.Endpoint().Fingerprint {
		return errors.New("Docker Sandbox readiness changed before start")
	}
	if _, denied := s.evaluateCurrentDockerSandboxGates(authority, readiness); denied ||
		!dockerSandboxAdmissionMatchesCurrent(admission, authority) ||
		authority.Plan.PlanFingerprint != plan.PlanFingerprint ||
		writeRequest.RequestFingerprint !=
			s.mustDockerSandboxWriteRequestFingerprint(ctx, authority) {
		return errors.New("Docker Sandbox authority changed before start")
	}
	return nil
}

func (s *DockerSandboxService) HandleDockerContainerLifecyclePostExit(ctx context.Context,
	request DockerContainerLifecyclePostExitRequest,
) error {
	if request.Validate() != nil || s.ioService == nil {
		return errors.New("Docker Sandbox post-exit request is invalid")
	}
	record, found, err := s.store.GetDockerSandboxRecordByLifecycleIntent(ctx,
		request.Record.Intent.ID)
	if err != nil || !found || record.Launch == nil || record.Receipt != nil {
		return errors.New("Docker Sandbox post-exit lifecycle is not product-bound")
	}
	admission := record.Admission
	duration := min(admission.WallClockSeconds, 30)
	logPlan, err := sandbox.NewDockerLogCapturePlan(request.Record.Intent.AttemptID,
		request.Record.Intent.ResourceGeneration, admission.RunID,
		request.ExitObservation.ContainerIDFingerprint, admission.LogBytes,
		admission.LogLines, duration)
	if err != nil {
		return err
	}
	var deliver func(sandbox.DockerLogCaptureReceipt, sandbox.DockerLogOutput) error
	if output := dockerCommandRuntimeOutputFromContext(ctx); output != nil {
		if !output.matches(admission.RunID, admission.ID) {
			return errors.New("Docker Command Runtime output does not own the exited admission")
		}
		deliver = output.accept
	}
	if _, _, err = s.ioService.captureOwnedLogs(ctx, request.LifecycleRequest,
		logPlan, deliver); err != nil {
		return err
	}
	if request.ExitObservation.ExitCode != 0 ||
		dockerSandboxLifecycleWasInterrupted(request.Record) {
		return nil
	}
	manifest, decodeErr := sandbox.DecodeManifest([]byte(admission.ManifestJSON))
	if decodeErr != nil {
		return decodeErr
	}
	// Standard Code writes only to its exact Drydock mount. Its durable file
	// result is the Drydock Workspace Checkpoint assembled by the higher-level
	// adapter, so exporting the complete repository as a bounded Artifact would
	// be both incomplete and misleading. Logs still use the shared capture
	// contract above; ordinary Docker Sandbox output mounts keep the existing
	// staging and Artifact commit path below.
	if _, standardCode := sandbox.ParseDockerStandardCodeManifest(manifest); standardCode {
		return nil
	}
	// Artifact commit has its own current-authority check. Log capture and
	// cleanup remain possible when this check fails, but no output is committed.
	if err := s.requireCurrentDockerSandboxArtifactAuthority(ctx, admission,
		request.Plan, request.WriteRequest); err != nil {
		return err
	}
	outputTarget, ok := dockerSandboxDedicatedOutputTarget(request.WriteRequest.Spec)
	if !ok {
		return errors.New("Docker Sandbox dedicated output mount is missing")
	}
	exportPlan, err := sandbox.NewDockerOutputExportPlan(request.Record.Intent.AttemptID,
		request.Record.Intent.ResourceGeneration, admission.RunID,
		request.ExitObservation.ContainerIDFingerprint, outputTarget,
		sandbox.MaxDockerOutputFiles, min(admission.DiskBytes,
			sandbox.MaxDockerOutputFileBytes), admission.DiskBytes)
	if err != nil {
		return err
	}
	stagingRoot, err := s.dockerSandboxAttemptStagingRoot(admission)
	if err != nil {
		return err
	}
	staging, _, err := s.ioService.StageOwnedOutputs(ctx, request.LifecycleRequest,
		exportPlan, stagingRoot)
	if err != nil {
		return err
	}
	if staging.Status != sandbox.DockerOutputStagingStatusCompleted ||
		len(staging.Entries) == 0 {
		return nil
	}
	accepted := make([]sandbox.DockerOutputCommitEntry, len(staging.Entries))
	for index, entry := range staging.Entries {
		accepted[index] = sandbox.DockerOutputCommitEntry{Path: entry.Path,
			SHA256: entry.SHA256, SizeBytes: entry.SizeBytes, MediaType: entry.MediaType}
	}
	operation := runmutation.Fingerprint(dockerSandboxOutputCommitProtocol,
		admission.AdmissionFingerprint, request.InvocationKeyDigest)
	commitRequest, err := sandbox.NewDockerOutputCommitRequest(
		request.Record.Intent.AttemptID, request.Record.Intent.ResourceGeneration,
		admission.RunID, admission.WorkspaceID, staging.ID, operation, accepted)
	if err != nil {
		return err
	}
	_, _, err = s.ioService.CommitOutputs(ctx, commitRequest, staging, stagingRoot)
	return err
}

func (s *DockerSandboxService) requireCurrentDockerSandboxArtifactAuthority(
	ctx context.Context, admission domain.DockerSandboxAdmission,
	plan sandbox.DockerContainerPlan, writeRequest sandbox.DockerContainerWriteRequest,
) error {
	if admission.RuntimeEpochFingerprint != s.runtimeEpochFingerprint ||
		!admission.ArtifactCommitAuthorized || !s.dockerCapabilities.Enabled {
		return errors.New("Docker Sandbox artifact authority is unavailable")
	}
	manifest, err := sandbox.DecodeManifest([]byte(admission.ManifestJSON))
	if err != nil {
		return err
	}
	authority, err := s.loadCurrentDockerSandboxAuthority(ctx, admission.PlanID,
		manifest, admission.RequestedBy)
	if err != nil {
		return err
	}
	if authority.Run.Terminal() || authority.Profile.Profile !=
		domain.RunExecutionProfileDocker ||
		!s.permissionCapabilities.AllowsSnapshot(authority.Permission) ||
		!dockerSandboxAdmissionMatchesCurrent(admission, authority) ||
		plan.PlanFingerprint != admission.PlanFingerprint ||
		s.mustDockerSandboxWriteRequestFingerprint(ctx, authority) !=
			writeRequest.RequestFingerprint {
		return errors.New("Docker Sandbox artifact authority changed")
	}
	return nil
}

func (s *DockerSandboxService) reconstructDockerSandboxWriteRequest(ctx context.Context,
	admission domain.DockerSandboxAdmission,
) (sandbox.DockerContainerPlan, sandbox.DockerContainerWriteRequest, error) {
	if admission.Validate() != nil || admission.Decision !=
		domain.DockerSandboxAdmissionAuthorized {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{},
			errors.New("Docker Sandbox admission is invalid")
	}
	manifest, err := sandbox.DecodeManifest([]byte(admission.ManifestJSON))
	if err != nil {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{}, err
	}
	plan, err := s.store.GetDockerContainerPlan(ctx, admission.PlanID)
	if err != nil {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{}, err
	}
	if err := requireDockerSandboxManifestPlan(manifest, plan); err != nil ||
		plan.RunID != admission.RunID || plan.MissionID != admission.MissionID ||
		plan.WorkspaceID != admission.WorkspaceID ||
		plan.CandidateID != admission.CandidateID ||
		plan.PreparationID != admission.PreparationID ||
		plan.ManifestFingerprint != admission.ManifestFingerprint ||
		plan.PlanFingerprint != admission.PlanFingerprint ||
		plan.SpecFingerprint != admission.SpecFingerprint ||
		plan.AuthorityFingerprint != admission.AuthorityFingerprint ||
		plan.RequestedBy != admission.RequestedBy {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{},
			errors.New("Docker Sandbox compiled plan changed")
	}
	// Cleanup reconstructs the immutable mount without borrowing the foreground
	// proof, which may have expired. Create/start still requires that live proof.
	workspaceCtx := ctx
	if isBatchValidationDockerRequester(admission.RequestedBy) {
		workspaceCtx = context.WithValue(ctx, batchValidationRuntimeKey{}, (*batchValidationRuntimeProof)(nil))
	}
	if isGitBisectDockerRequester(admission.RequestedBy) {
		source, err := s.gitBisectCleanupWorkspace(ctx, admission, sandbox.WorkspaceBinding{ID: admission.WorkspaceID})
		if err != nil {
			return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{}, err
		}
		workspaceCtx = context.WithValue(workspaceCtx, gitBisectRuntimeKey{}, (*gitBisectRuntimeProof)(nil))
		workspaceCtx = context.WithValue(workspaceCtx, gitBisectCleanupRootKey{}, source)
	}
	workspace, err := s.resolveDockerSandboxWorkspace(workspaceCtx, admission.RunID,
		admission.WorkspaceID, manifest, false)
	if err != nil {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{}, err
	}
	if isBatchValidationDockerRequester(admission.RequestedBy) {
		workspace, err = s.batchValidationCleanupWorkspace(ctx, admission, workspace)
		if err != nil {
			return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{}, err
		}
	}
	if isGitBisectDockerRequester(admission.RequestedBy) {
		workspace, err = s.gitBisectCleanupWorkspace(ctx, admission, workspace)
		if err != nil {
			return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{}, err
		}
	}
	root, err := validateSandboxWorkspaceBinding(workspace)
	if err != nil {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{}, err
	}
	observation, err := s.store.GetDockerObservation(ctx, plan.ObservationID)
	if err != nil {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{}, err
	}
	spec, err := sandbox.CompileDockerContainerSpec(ctx, observation, manifest)
	if err != nil || sandbox.DockerContainerPlanMatchesSpec(plan, spec) != nil {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{},
			errors.New("Docker Sandbox specification cannot be reconstructed")
	}
	writeRequest, err := s.newDockerSandboxWriteRequest(workspaceCtx, root, manifest, spec)
	if err != nil {
		return sandbox.DockerContainerPlan{}, sandbox.DockerContainerWriteRequest{}, err
	}
	return plan, writeRequest, nil
}

func (s *DockerSandboxService) completeDockerSandbox(ctx context.Context,
	admission domain.DockerSandboxAdmission,
	lifecycle sandbox.DockerContainerLifecycleRecord, lifecycleErr error,
) (domain.DockerSandboxRecord, error) {
	product, found, err := s.store.GetDockerSandboxRecordByLifecycleIntent(ctx,
		lifecycle.Intent.ID)
	if err != nil || !found || product.Launch == nil || lifecycle.Receipt == nil {
		if err == nil {
			err = errors.New("Docker Sandbox terminal lifecycle is not product-bound")
		}
		return domain.DockerSandboxRecord{}, apperror.Normalize(err)
	}
	if product.Receipt != nil {
		product.Replayed = true
		return product, nil
	}
	outcome, reason := dockerSandboxProductOutcome(lifecycle, lifecycleErr)
	if cancellation, cancelled, cancelErr := s.store.GetDockerSandboxCancellation(ctx,
		admission.ID); cancelErr != nil {
		return product, apperror.Normalize(cancelErr)
	} else if cancelled && !cancellation.RequestedAt.After(
		lifecycle.Receipt.CompletedAt) {
		outcome, reason = domain.DockerSandboxOutcomeCancelled,
			domain.DockerSandboxReasonCancelled
	}
	logReceipt, hasLog, logErr := s.store.GetDockerLogCaptureReceiptByAttempt(ctx,
		lifecycle.Intent.AttemptID)
	if logErr != nil {
		return product, apperror.Normalize(logErr)
	}
	staging, hasStaging, stagingErr := s.store.GetDockerOutputStagingReceiptByAttempt(ctx,
		lifecycle.Intent.AttemptID)
	if stagingErr != nil {
		return product, apperror.Normalize(stagingErr)
	}
	commit, hasCommit, commitErr := s.store.GetDockerOutputCommitReceiptByAttempt(ctx,
		lifecycle.Intent.AttemptID)
	if commitErr != nil {
		return product, apperror.Normalize(commitErr)
	}
	exitCode := lifecycle.Receipt.ExitCode
	_, standardCode := sandbox.ParseDockerStandardCodeManifest(manifestFromAdmission(admission))
	if lifecycle.Receipt.Outcome == sandbox.DockerContainerLifecycleOutcomeNaturalExit &&
		exitCode != nil {
		if !hasLog || logReceipt.Validate() != nil {
			outcome, reason = domain.DockerSandboxOutcomeFailed,
				domain.DockerSandboxReasonIOFailed
		}
		if *exitCode == 0 && !standardCode && (!hasStaging || staging.Validate() != nil ||
			(staging.FileCount > 0 && (!hasCommit || commit.Validate() != nil))) {
			outcome, reason = domain.DockerSandboxOutcomeFailed,
				domain.DockerSandboxReasonIOFailed
		}
	}
	receipt := domain.DockerSandboxReceipt{
		ID:                "docker-sandbox-receipt-" + admission.AdmissionFingerprint[:24],
		AdmissionID:       admission.ID,
		ProtocolVersion:   domain.DockerSandboxReceiptProtocolVersion,
		LifecycleIntentID: lifecycle.Intent.ID, AttemptID: lifecycle.Intent.AttemptID,
		RunID: admission.RunID, WorkspaceID: admission.WorkspaceID,
		Outcome: outcome, ReasonCode: reason, ExitCode: exitCode,
		CleanupComplete:          true,
		ArtifactCommitAuthorized: admission.ArtifactCommitAuthorized,
		CompletedAt:              lifecycle.Receipt.CompletedAt,
	}
	if hasLog {
		receipt.LogReceiptID = logReceipt.ID
	}
	if hasStaging {
		receipt.OutputStagingReceiptID = staging.ID
	}
	if hasCommit && outcome == domain.DockerSandboxOutcomeSucceeded {
		receipt.OutputCommitReceiptID = commit.ID
		receipt.ArtifactCount = commit.CommittedCount
	}
	receipt.ReceiptFingerprint = domain.DockerSandboxReceiptFingerprint(receipt)
	stored, replayed, err := s.store.CompleteDockerSandbox(ctx, receipt)
	if err != nil {
		return product, apperror.Normalize(err)
	}
	stored.Replayed = replayed
	return stored, nil
}

func manifestFromAdmission(admission domain.DockerSandboxAdmission) sandbox.Manifest {
	manifest, _ := sandbox.DecodeManifest([]byte(admission.ManifestJSON))
	return manifest
}

func dockerSandboxProductOutcome(record sandbox.DockerContainerLifecycleRecord,
	lifecycleErr error,
) (string, string) {
	if record.Receipt == nil {
		return domain.DockerSandboxOutcomeFailed, domain.DockerSandboxReasonCleanupFailed
	}
	if errors.Is(lifecycleErr, context.DeadlineExceeded) {
		return domain.DockerSandboxOutcomeTimedOut, domain.DockerSandboxReasonTimedOut
	}
	switch record.Receipt.Outcome {
	case sandbox.DockerContainerLifecycleOutcomeNaturalExit:
		if record.Receipt.ExitCode != nil && *record.Receipt.ExitCode == 0 &&
			lifecycleErr == nil {
			return domain.DockerSandboxOutcomeSucceeded, domain.DockerSandboxReasonCompleted
		}
		if lifecycleErr != nil && strings.Contains(lifecycleErr.Error(),
			DockerContainerLifecyclePostExitFailureReason) {
			return domain.DockerSandboxOutcomeFailed, domain.DockerSandboxReasonIOFailed
		}
		return domain.DockerSandboxOutcomeFailed, domain.DockerSandboxReasonProcessFailed
	case sandbox.DockerContainerLifecycleOutcomeTimedOut:
		return domain.DockerSandboxOutcomeTimedOut, domain.DockerSandboxReasonTimedOut
	case sandbox.DockerContainerLifecycleOutcomeCancelled:
		return domain.DockerSandboxOutcomeCancelled, domain.DockerSandboxReasonCancelled
	default:
		for index := len(record.Transitions) - 1; index >= 0; index-- {
			if record.Transitions[index].State !=
				sandbox.DockerContainerLifecycleTransitionFailed {
				continue
			}
			switch record.Transitions[index].ReasonCode {
			case sandbox.DockerContainerLifecycleFailureConnection,
				sandbox.DockerContainerLifecycleFailureInvalidResponse:
				return domain.DockerSandboxOutcomeFailed,
					domain.DockerSandboxReasonDaemonDisconnected
			case sandbox.DockerContainerLifecycleReasonCleanupFailed:
				return domain.DockerSandboxOutcomeFailed,
					domain.DockerSandboxReasonCleanupFailed
			}
		}
		return domain.DockerSandboxOutcomeFailed, domain.DockerSandboxReasonAuthorityChanged
	}
}

func dockerSandboxLifecycleWasInterrupted(record sandbox.DockerContainerLifecycleRecord) bool {
	cleaning := latestLifecycleTransition(record,
		sandbox.DockerContainerLifecycleTransitionCleaning)
	return cleaning != nil && cleaning.ReasonCode !=
		sandbox.DockerContainerLifecycleReasonNaturalExit
}

func dockerSandboxDedicatedOutputTarget(spec sandbox.DockerContainerSpec) (string, bool) {
	for _, mount := range spec.Mounts {
		if mount.DedicatedOutput {
			return mount.Target, true
		}
	}
	return "", false
}

func (s *DockerSandboxService) dockerSandboxAttemptStagingRoot(
	admission domain.DockerSandboxAdmission,
) (string, error) {
	if admission.Validate() != nil || s.stagingRoot == "" {
		return "", errors.New("Docker Sandbox staging authority is invalid")
	}
	target := filepath.Join(s.stagingRoot, admission.AdmissionFingerprint)
	if err := os.MkdirAll(target, 0o700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	resolved = filepath.Clean(resolved)
	if filepath.Dir(resolved) != s.stagingRoot {
		return "", errors.New("Docker Sandbox staging path escaped its trusted root")
	}
	return resolved, nil
}

func dockerSandboxAdmissionMatchesCurrent(admission domain.DockerSandboxAdmission,
	authority dockerSandboxAuthority,
) bool {
	return admission.RunID == authority.Run.ID &&
		admission.MissionID == authority.Mission.ID &&
		admission.WorkspaceID == authority.Workspace.ID &&
		admission.PlanID == authority.Plan.ID &&
		admission.CandidateID == authority.Candidate.Candidate.ID &&
		admission.PreparationID == authority.Intent.Preparation.ID &&
		admission.ManifestFingerprint == authority.Plan.ManifestFingerprint &&
		admission.PlanFingerprint == authority.Plan.PlanFingerprint &&
		admission.SpecFingerprint == authority.Plan.SpecFingerprint &&
		admission.AuthorityFingerprint == authority.Plan.AuthorityFingerprint &&
		admission.ProfileSnapshotID == authority.Profile.ID &&
		admission.ProfileRevision == authority.Profile.Revision &&
		admission.PermissionSnapshotID == authority.Permission.ID &&
		admission.PermissionRevision == authority.Permission.Revision &&
		admission.PermissionMode == authority.Permission.Mode &&
		admission.ApprovalID == authority.Approval.ID &&
		admission.ApprovalVersion == authority.Approval.Version &&
		admission.PolicyFingerprint == authority.Plan.PolicyFingerprint
}

func (s *DockerSandboxService) mustDockerSandboxWriteRequestFingerprint(ctx context.Context,
	authority dockerSandboxAuthority,
) string {
	spec, err := sandbox.CompileDockerContainerSpec(ctx, authority.Observation,
		authority.Manifest)
	if err != nil || sandbox.DockerContainerPlanMatchesSpec(authority.Plan, spec) != nil {
		return ""
	}
	request, err := s.newDockerSandboxWriteRequest(ctx, authority.RootPath,
		authority.Manifest, spec)
	if err != nil {
		return ""
	}
	return request.RequestFingerprint
}

func (s *DockerSandboxService) newDockerSandboxWriteRequest(ctx context.Context,
	root string, manifest sandbox.Manifest, spec sandbox.DockerContainerSpec,
) (sandbox.DockerContainerWriteRequest, error) {
	if _, standardCode := sandbox.ParseDockerStandardCodeManifest(manifest); !standardCode {
		return sandbox.NewDockerContainerWriteRequest(ctx, root, spec)
	}
	if proof := gitBisectRuntimeFromContext(ctx); proof != nil {
		if proof.root != root || proof.check == nil || proof.check(ctx) != nil {
			return sandbox.DockerContainerWriteRequest{}, runner.ErrCommandRuntimeBoundary
		}
		if info, err := os.Lstat(filepath.Join(root, ".git")); err == nil && info.IsDir() {
			mask, err := s.nativeGitMetadataMaskDirectory()
			if err != nil {
				return sandbox.DockerContainerWriteRequest{}, err
			}
			return sandbox.NewDockerNativeGitContainerWriteRequest(ctx, root, mask, spec)
		}
	}
	if source, ok := ctx.Value(gitBisectCleanupRootKey{}).(sandbox.WorkspaceBinding); ok && source.RootPath == root {
		if info, err := os.Lstat(filepath.Join(root, ".git")); err == nil && info.IsDir() {
			mask, err := s.nativeGitMetadataMaskDirectory()
			if err != nil {
				return sandbox.DockerContainerWriteRequest{}, err
			}
			return sandbox.NewDockerNativeGitContainerWriteRequest(ctx, root, mask, spec)
		}
	}
	mask, err := s.standardCodeGitMetadataMaskPath()
	if err != nil {
		return sandbox.DockerContainerWriteRequest{}, err
	}
	return sandbox.NewDockerStandardCodeContainerWriteRequest(ctx, root, mask, spec)
}

func (s *DockerSandboxService) standardCodeGitMetadataMaskPath() (string, error) {
	if s == nil || s.stagingRoot == "" {
		return "", errors.New("Standard Code Git metadata mask root is unavailable")
	}
	s.standardCodeMaskMu.Lock()
	defer s.standardCodeMaskMu.Unlock()
	target := filepath.Join(s.stagingRoot, standardCodeGitMetadataMaskFile)
	if filepath.Dir(target) != s.stagingRoot {
		return "", errors.New("Standard Code Git metadata mask escaped its trusted root")
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return target, nil
		}
		return "", err
	}
	created := true
	defer func() {
		if created {
			_ = os.Remove(target)
		}
	}()
	if _, err = file.WriteString(sandbox.DockerStandardCodeGitMetadataMask); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Chmod(target, 0o444); err != nil {
		return "", err
	}
	created = false
	return target, nil
}

func normalizeDockerSandboxStartRequest(request DockerSandboxStartRequest) (
	DockerSandboxStartRequest, error,
) {
	request.AdmissionID = strings.TrimSpace(request.AdmissionID)
	request.OperationKey = strings.TrimSpace(request.OperationKey)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	if !validDockerSandboxIdentity(request.AdmissionID) ||
		!validDockerSandboxIdentity(request.RequestedBy) ||
		!validDockerSandboxOperationKey(request.OperationKey) {
		return DockerSandboxStartRequest{}, errors.New("invalid start identity")
	}
	return request, nil
}

func (s *DockerSandboxService) requireExecutionConfigured() error {
	if err := s.requireCleanupConfigured(); err != nil ||
		!s.dockerCapabilities.Enabled || s.dockerCapabilities.Validate() != nil {
		return apperror.New(apperror.CodeFailedPrecondition,
			"Docker Sandbox execution is disabled")
	}
	return nil
}

func (s *DockerSandboxService) requireCleanupConfigured() error {
	if s == nil || s.store == nil || s.lifecycleTransport == nil ||
		s.ioService == nil || s.stagingRoot == "" {
		return apperror.New(apperror.CodeFailedPrecondition,
			"Docker Sandbox cleanup transport is unavailable")
	}
	return nil
}

func (s *DockerSandboxService) registerActive(admissionID string,
	parent context.Context,
) (context.Context, context.CancelFunc, error) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if _, exists := s.active[admissionID]; exists {
		return nil, nil, apperror.New(apperror.CodeConflict,
			"Docker Sandbox admission is already active")
	}
	activeCtx, cancel := context.WithCancel(parent)
	s.active[admissionID] = cancel
	return activeCtx, cancel, nil
}

func (s *DockerSandboxService) unregisterActive(admissionID string) {
	s.activeMu.Lock()
	delete(s.active, admissionID)
	s.activeMu.Unlock()
}

func (s *DockerSandboxService) cancelActive(admissionID string) bool {
	s.activeMu.Lock()
	cancel, found := s.active[admissionID]
	s.activeMu.Unlock()
	if found {
		cancel()
	}
	return found
}

func (s *DockerSandboxService) nativeGitMetadataMaskDirectory() (string, error) {
	if s == nil || s.stagingRoot == "" {
		return "", errors.New("native Git metadata mask root unavailable")
	}
	s.standardCodeMaskMu.Lock()
	defer s.standardCodeMaskMu.Unlock()
	target := filepath.Join(s.stagingRoot, "native-git-metadata-mask")
	if err := os.Mkdir(target, 0555); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	return target, nil
}
