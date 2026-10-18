package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

// This capability is private to the host interpreter. Ordinary Agent receipt
// readers retain their active-attempt and own-actor restrictions.
type workflowVerificationScope struct {
	executionID, stepExecutionID, owner string
	revision                            int64
	final                               bool
}

type workflowProjectVerificationReader interface {
	latestWorkflowVerificationReceipt(context.Context, workflowVerificationScope) (toolgateway.ProjectVerificationReceipt, bool, error)
}

type workflowVerificationStore interface {
	workerExecutionStore
	commandJobActorStore
	ReadWorkflowVerificationClaim(context.Context, string, string, string, int64) (domain.WorkflowExecution, domain.WorkflowStepExecution, error)
	ListWorkflowVerificationSteps(context.Context, string, string, int64) ([]domain.WorkflowStepExecution, error)
	GetWorkflowDefinition(context.Context, string) (domain.WorkflowDefinition, error)
	GetAgentAttempt(context.Context, string) (domain.AgentAttempt, bool, error)
	GetAgentCompletion(context.Context, string) (domain.AgentCompletion, bool, error)
}

func (s *WorkflowService) WithProjectVerification(runtime CommandRuntimeRuntime) *WorkflowService {
	s.verification, _ = runtime.(workflowProjectVerificationReader)
	return s
}

type workflowVerificationEvidence struct {
	store      workflowVerificationStore
	e          domain.WorkflowExecution
	step       domain.WorkflowStepExecution
	spec       domain.WorkflowStep
	sourceSpec domain.WorkflowStep
	calls      []projectTestCallEvidence
	origin     projectTestCallEvidence
	found      bool
}

func (s *CommandRuntimeService) workflowVerificationEvidence(ctx context.Context, scope workflowVerificationScope) (workflowVerificationEvidence, error) {
	var out workflowVerificationEvidence
	st, ok := s.store.(workflowVerificationStore)
	if !ok {
		return out, errors.New("workflow verification ownership store is unavailable")
	}
	out.store = st
	var err error
	out.e, out.step, err = st.ReadWorkflowVerificationClaim(ctx, scope.executionID, scope.stepExecutionID, scope.owner, scope.revision)
	if err != nil {
		return out, err
	}
	d, err := st.GetWorkflowDefinition(ctx, out.e.DefinitionID)
	if err != nil {
		return out, err
	}
	out.spec, ok = d.Spec.Step(out.step.StepID)
	if !ok || !out.spec.RequiresCodeVerification() || out.spec.Task == nil || !out.spec.Task.Worker.AllowsTool("run_tests") {
		return out, errors.New("workflow step has no verified_code completion authority")
	}
	candidates := []domain.WorkflowStepExecution{out.step}
	if scope.final {
		candidates, err = st.ListWorkflowVerificationSteps(ctx, out.e.ID, scope.owner, scope.revision)
		if err != nil {
			return out, err
		}
	}
	// Select the latest invocation of the target, including failed, pending and
	// stale invocations. Filtering to current successful sources hides failures.
	// Final acceptance includes report workers: their later failed tests must
	// not be hidden by an earlier code worker's passing receipt.
	for _, candidate := range candidates {
		sourceSpec, ok := d.Spec.Step(candidate.StepID)
		if !ok || sourceSpec.Task == nil || !sourceSpec.Task.Worker.AllowsTool("run_tests") {
			continue
		}
		calls, err := s.projectTestCalls(ctx, out.e.RunID, candidate.AgentID)
		if err != nil {
			return out, err
		}
		for i := len(calls) - 1; i >= 0; i-- {
			c := calls[i]
			if c.call.Specialist != nil && c.jobID != "" && c.authority.ProjectTestSource != nil && c.authority.ProjectTestSource.Target == out.spec.VerificationTarget() {
				if !out.found || !c.createdAt.Before(out.origin.createdAt) {
					out.origin, out.found, out.step, out.sourceSpec, out.calls = c, true, candidate, sourceSpec, calls
				}
				break
			}
		}
	}
	return out, nil
}

func (s *CommandRuntimeService) latestWorkflowVerificationReceipt(ctx context.Context, scope workflowVerificationScope) (toolgateway.ProjectVerificationReceipt, bool, error) {
	evidence, err := s.workflowVerificationEvidence(ctx, scope)
	if err != nil || !evidence.found {
		return toolgateway.ProjectVerificationReceipt{}, false, err
	}
	return s.checkWorkflowVerificationReceipt(ctx, scope, evidence)
}

func (m *CommandRuntimeMultiplexer) latestWorkflowVerificationReceipt(ctx context.Context, scope workflowVerificationScope) (toolgateway.ProjectVerificationReceipt, bool, error) {
	if m == nil || len(m.adapters) == 0 {
		return toolgateway.ProjectVerificationReceipt{}, false, errors.New("workflow project verification runtime is unavailable")
	}
	evidence, err := m.adapters[0].workflowVerificationEvidence(ctx, scope)
	if err != nil || !evidence.found {
		return toolgateway.ProjectVerificationReceipt{}, false, err
	}
	for _, service := range m.adapters {
		if service.adapter.SameBackend(evidence.origin.authority.Adapter) {
			return service.checkWorkflowVerificationReceipt(ctx, scope, evidence)
		}
	}
	return toolgateway.ProjectVerificationReceipt{Version: toolgateway.ProjectVerificationReceiptVersion, RunID: evidence.e.RunID,
		AgentID: evidence.step.AgentID, Target: evidence.spec.VerificationTarget(), JobID: evidence.origin.jobID, State: "stale"}, true, errors.New("workflow project verification backend changed")
}

func (s *CommandRuntimeService) checkWorkflowVerificationReceipt(ctx context.Context, scope workflowVerificationScope, ev workflowVerificationEvidence) (toolgateway.ProjectVerificationReceipt, bool, error) {
	// Keep the actual native reference even when current authority invalidates
	// reuse, so the workflow can explain which invocation it rejected.
	receipt := toolgateway.ProjectVerificationReceipt{Version: toolgateway.ProjectVerificationReceiptVersion, RunID: ev.e.RunID,
		AgentID: ev.step.AgentID, JobID: ev.origin.jobID, Target: ev.spec.VerificationTarget(), State: "stale"}
	fail := func(reason string) (toolgateway.ProjectVerificationReceipt, bool, error) {
		return receipt, true, errors.New(reason)
	}
	if !s.commandRuntimeAdapterCurrent() || !s.adapter.SameBackend(ev.origin.authority.Adapter) {
		return fail("workflow project verification backend changed")
	}
	b, err := s.loadCommandRuntimeBindings(ctx, ev.e.RunID)
	if err != nil {
		return receipt, true, err
	}
	if err := workflowVerificationRunStatus(b.run); err != nil {
		return receipt, true, err
	}
	child, err := ev.store.GetAgentNode(ctx, ev.step.AgentID)
	if err != nil {
		return receipt, true, err
	}
	completion, found, err := ev.store.GetAgentCompletion(ctx, child.ID)
	if err != nil {
		return receipt, true, err
	}
	if !found || child.Status != domain.AgentCompleted || child.ActiveAttemptID != "" || child.RunID != ev.e.RunID || child.SessionID != ev.step.SessionID || child.ParentID != b.root.ID ||
		completion.RunID != ev.e.RunID || completion.AgentID != child.ID || completion.ParentAgentID != child.ParentID || !domain.EqualWorkerProfiles(child.Worker, ev.sourceSpec.Task.Worker) {
		return fail("workflow verification requires its completed child and native completion")
	}
	origin := ev.origin.call.Specialist
	a := ev.origin.authority
	if origin == nil || a.Worker == nil || origin.Ref.RunID != ev.e.RunID || origin.Ref.AgentID != child.ID || a.Worker.AgentID != child.ID || a.Worker.SessionID != child.SessionID || a.Worker.AttemptID != origin.Ref.AttemptID ||
		origin.ChargeID == "" || origin.StartedAt == nil || origin.Status != domain.SupervisorToolCompleted {
		return fail("workflow test has no completed, charged native child invocation")
	}
	// The originating Job may be from an earlier continued turn. The final
	// Finish attempt is checked separately and must never replace its identity.
	for _, ref := range []domain.AgentAttemptRef{origin.Ref, {RunID: ev.e.RunID, AgentID: child.ID, AttemptID: completion.AttemptID}} {
		attempt, found, err := ev.store.GetAgentAttempt(ctx, ref.AttemptID)
		if err != nil {
			return receipt, true, err
		}
		if !found || attempt.RunID != ref.RunID || attempt.AgentID != child.ID || attempt.ParentAgentID != child.ParentID ||
			(attempt.Status != domain.AgentAttemptContinued && attempt.Status != domain.AgentAttemptFinished) || (ref.AttemptID == completion.AttemptID && attempt.Status != domain.AgentAttemptFinished) {
			return fail("workflow test originating attempt or native Finish ownership changed")
		}
	}
	binding, err := ev.store.GetSpecialistToolBinding(ctx, origin.Ref)
	if err != nil {
		return receipt, true, err
	}
	profile, _ := json.Marshal(child.Worker)
	actorSession, err := ev.store.GetSession(ctx, child.SessionID)
	if err != nil {
		return receipt, true, err
	}
	if binding.Ref != origin.Ref || binding.Mode != "worker" || binding.SessionID != child.SessionID || binding.WorkspaceID != b.workspace.ID || binding.WorkerProfileJSON != string(profile) || binding.BatchPlanID != "" || actorSession.WorkspaceID != b.workspace.ID {
		return fail("workflow verification child workspace or capability profile changed")
	}
	files, err := ResolveRunFileWorkspace(ctx, ev.store, b.run, b.mission, s.drydocks)
	if err != nil {
		return receipt, true, err
	}
	fingerprint, err := workspace.AgentCodeRootFingerprint(files.Workspace.RootPath)
	if err != nil || fingerprint != binding.RootFingerprint {
		return fail("workflow verification physical workspace changed")
	}
	b.actor, b.worker, b.rootPath = child, &binding, files.Workspace.RootPath
	b.rootSHA256, err = runner.CommandRuntimeWorkspaceRootSHA256(b.rootPath)
	if err != nil {
		return receipt, true, err
	}
	if !b.rootFound || b.mode.Surface != domain.ExecutionSurfaceCode || !projectTestPhaseAllowed(b.mode.Phase) ||
		b.profile.Profile != commandRuntimeExecutionProfile(s.adapter) || b.profile.NetworkScope != domain.ExecutionNetworkDisabled ||
		!s.adapter.AllowsPermission(b.permission.Mode) || !s.capabilities.AllowsSnapshot(b.permission) || !commandRuntimeAuthorityCurrent(s.capabilities, a, b.permission) {
		return fail("workflow verification current Run permission, mode or backend changed")
	}
	job, err := s.store.GetCommandRuntimeJob(ctx, ev.origin.jobID)
	if err != nil {
		return receipt, true, err
	}
	actor, err := ev.store.GetCommandRuntimeJobAttribution(ctx, job.ID)
	if err != nil {
		return receipt, true, err
	}
	if actor.Source != domain.AgentAttributionRecorded || actor.AgentID != child.ID || actor.AgentAttemptID != origin.Ref.AttemptID ||
		job.RunID != ev.e.RunID || job.MissionID != b.mission.ID || job.RootAgentID != b.root.ID || job.SessionID != child.SessionID || job.WorkspaceID != binding.WorkspaceID ||
		len(a.CommandFingerprints) != 1 || a.CommandFingerprints[0] != job.SpecFingerprint || !job.Adapter.SameBackend(a.Adapter) {
		return fail("workflow verification Job does not belong to this child, attempt and Run")
	}
	receipt, err = s.checkProjectVerificationReceipt(ctx, b, job, ev.origin, projectVerificationReceiptFromCalls(ev.calls, job.ID))
	if err != nil {
		return receipt, true, err
	}
	if job.State != runner.CommandRuntimeJobCompleted || job.ExitCode == nil || *job.ExitCode != 0 || !job.TreeReaped || job.TruncationReason != "" || receipt.Cursor != job.OutputCursor {
		receipt.Reusable = false
		if job.State.Terminal() {
			receipt.State = "failed"
		} else {
			receipt.State = "pending"
		}
	}
	// Source hashing can take time. Recheck the live claim before returning any
	// evidence to the interpreter; a pause or another host invalidates this read.
	if _, _, err = ev.store.ReadWorkflowVerificationClaim(ctx, scope.executionID, ev.step.ID, scope.owner, scope.revision); err != nil {
		return receipt, true, err
	}
	currentRun, err := ev.store.GetRun(ctx, ev.e.RunID)
	if err != nil {
		return receipt, true, err
	}
	if err := workflowVerificationRunStatus(currentRun); err != nil {
		receipt.Reusable = false
		return receipt, true, err
	}
	return receipt, true, nil
}

func workflowVerificationRunStatus(run domain.Run) error {
	if run.Status == domain.RunPaused {
		return apperror.New(apperror.CodeConflict, "workflow verification yields while its Run is paused")
	}
	if run.Status != domain.RunRunning {
		return apperror.New(apperror.CodeFailedPrecondition, "workflow verification requires a running Run; current status="+string(run.Status))
	}
	return nil
}

func (s *WorkflowService) checkWorkerVerification(ctx context.Context, e *domain.WorkflowExecution, job domain.WorkflowStepExecution, step domain.WorkflowStep, owner string, final bool) error {
	if !step.RequiresCodeVerification() {
		return nil
	}
	proof := domain.WorkflowVerification{StepExecutionID: job.ID, StepID: step.ID, AgentID: job.AgentID, Target: step.VerificationTarget(), State: "missing", CheckedAt: time.Now().UTC()}
	var err error
	if s.verification == nil {
		err = errors.New("workflow project verification runtime is unavailable")
	} else {
		var r toolgateway.ProjectVerificationReceipt
		var found bool
		r, found, err = s.verification.latestWorkflowVerificationReceipt(ctx, workflowVerificationScope{executionID: e.ID, stepExecutionID: job.ID, owner: owner, revision: e.Version, final: final})
		if found {
			proof.JobID, proof.State, proof.Reusable = r.JobID, r.State, r.Reusable
			if r.AgentID != "" {
				proof.AgentID = r.AgentID
			}
			proof.SourceVersion, proof.TestFingerprint, proof.ExitCode, proof.TreeReaped = r.SourceVersion, r.TestFingerprint, r.ExitCode, r.TreeReaped
			if proof.State == "" {
				proof.State = "unavailable"
			}
		}
		if err == nil && (!found || !r.Reusable || r.State != "passed") {
			err = fmt.Errorf("project test directory %q requires a current passed native receipt (state=%s, job=%s)", proof.Target, proof.State, proof.JobID)
		}
	}
	if err != nil {
		proof.Reusable = false
		proof.Reason = err.Error()
	}
	if e.State.Verifications == nil {
		e.State.Verifications = map[string]domain.WorkflowVerification{}
	}
	e.State.Verifications[proof.Target] = proof
	if err != nil {
		return fmt.Errorf("workflow step %s verification: %w", step.ID, err)
	}
	return nil
}

func (s *WorkflowService) recheckWorkflowVerification(ctx context.Context, e *domain.WorkflowExecution, spec domain.WorkflowSpec, owner string) error {
	for target, proof := range e.State.Verifications {
		step, found := spec.Step(proof.StepID)
		if !found || !step.RequiresCodeVerification() || step.VerificationTarget() != target {
			return apperror.New(apperror.CodeFailedPrecondition, "workflow verification checkpoint does not match its definition")
		}
		job, found, err := s.store.GetWorkflowStep(ctx, proof.StepExecutionID)
		if err != nil {
			return err
		}
		if !found || job.ExecutionID != e.ID || job.StepID != step.ID || job.Status != "completed" {
			return errors.New("workflow verification checkpoint has no completed owned step")
		}
		if err = s.checkWorkerVerification(ctx, e, job, step, owner, true); err != nil {
			return err
		}
	}
	return nil
}
