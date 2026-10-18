package application

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

type projectVerificationStore interface {
	ListProjectVerificationCalls(context.Context, string, string) ([]domain.ProjectVerificationCall, error)
}

type projectTestCallEvidence struct {
	authority commandruntimeadapter.Authority
	input     toolgateway.CommandRuntimeInput
	call      domain.ProjectVerificationCall
	jobID     string
	createdAt time.Time
	status    domain.SupervisorToolCallStatus
}

func (s *CommandRuntimeService) projectTestSource(ctx context.Context, b commandRuntimeBindings, target string) (commandruntimeadapter.ProjectTestSource, error) {
	spec, err := PrepareProjectCommandForAdapter(b.run, "test", target, s.adapter, b.rootPath)
	if err != nil {
		return commandruntimeadapter.ProjectTestSource{}, err
	}
	// Timeout and output paging do not change which registered test is run.
	spec.TimeoutMilliseconds = 1
	raw, err := json.Marshal(spec)
	if err != nil {
		return commandruntimeadapter.ProjectTestSource{}, err
	}
	version, err := projectVerificationWorkspaceVersion(ctx, b.run, b.mission, b.workspace.ID, b.rootPath)
	if err != nil {
		if b.worker != nil {
			err = errors.New("worker verification cannot identify the complete source version")
		}
		return commandruntimeadapter.ProjectTestSource{}, err
	}
	return commandruntimeadapter.ProjectTestSource{WorkspaceVersion: version, Target: target,
		TestFingerprint: runmutation.Fingerprint("project-test-definition.v1", string(raw))}, nil
}

func projectVerificationWorkspaceVersion(ctx context.Context, run domain.Run, mission domain.Mission, workspaceID, root string) (string, error) {
	snapshot, err := workspacecheckpoint.Capture(ctx, workspacecheckpoint.CaptureRequest{ID: "project-hook-version", RunID: run.ID,
		MissionID: mission.ID, SessionID: run.SessionID, WorkspaceID: workspaceID, WorkspaceRoot: root,
		AttemptID: "project-verification", CapabilityGeneration: strings.Repeat("0", 64), Trigger: workspacecheckpoint.TriggerFileTool,
		Phase: workspacecheckpoint.PhaseBefore, TriggerReceiptID: "project-hook-version", CreatedAt: time.Now().UTC()})
	if err != nil {
		return "", err
	}
	return projectHookSnapshotVersion(snapshot)
}

func (s *CommandRuntimeService) projectTestCalls(ctx context.Context, runID, actorID string) ([]projectTestCallEvidence, error) {
	st, ok := s.store.(projectVerificationStore)
	if !ok {
		return nil, errors.New("project verification evidence store is unavailable")
	}
	calls, err := st.ListProjectVerificationCalls(ctx, runID, actorID)
	if err != nil {
		return nil, err
	}
	result := make([]projectTestCallEvidence, 0, len(calls))
	for _, call := range calls {
		var name, payload, authority, operationKey string
		var stamp time.Time
		var status domain.SupervisorToolCallStatus
		if c := call.Supervisor; c != nil {
			name, payload, authority, stamp, status = c.ToolName, c.PayloadJSON, c.AuthorityJSON, c.CreatedAt, c.Status
			operationKey = runmutation.SupervisorToolExecutionKey(c.RunID, c.Turn, c.ToolName, c.PayloadJSON, c.CallID)
		} else if c := call.Specialist; c != nil {
			name, payload, authority, stamp, status = c.Name, c.PayloadJSON, c.AuthorityJSON, c.CreatedAt, c.Status
			operationKey = SpecialistCommandOperationKey(*c)
		} else {
			continue
		}
		a, err := commandruntimeadapter.DecodeAuthority(json.RawMessage(authority))
		if err != nil {
			continue
		}
		input, _, err := toolgateway.CommandToolInput(toolgateway.ToolName(name), json.RawMessage(payload), a)
		if err != nil {
			continue
		}
		value := projectTestCallEvidence{authority: a, input: input, call: call, createdAt: stamp, status: status}
		if name == string(toolgateway.RunTestsTool) && (input.Action == toolgateway.CommandRuntimeActionRun || input.Action == toolgateway.CommandRuntimeActionStart) && len(input.Commands) == 1 {
			if input.Action == toolgateway.CommandRuntimeActionRun {
				operationKey = commandRuntimeBatchOperationKey(operationKey, 0)
			}
			_, value.jobID = runner.CommandRuntimeOperationIdentity(runID, operationKey)
		}
		result = append(result, value)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].createdAt.Before(result[j].createdAt) })
	return result, nil
}

func (s *CommandRuntimeService) projectTestJobSource(ctx context.Context, job runner.CommandRuntimeJob, actorID string) (projectTestCallEvidence, error) {
	calls, err := s.projectTestCalls(ctx, job.RunID, actorID)
	if err != nil {
		return projectTestCallEvidence{}, err
	}
	for i := len(calls) - 1; i >= 0; i-- {
		c := calls[i]
		if c.jobID == job.ID && len(c.authority.CommandFingerprints) == 1 && c.authority.CommandFingerprints[0] == job.SpecFingerprint && c.authority.Adapter.SameBackend(job.Adapter) {
			return c, nil
		}
	}
	return projectTestCallEvidence{}, apperror.New(apperror.CodeNotFound, "Job has no owned registered-test source")
}

func projectVerificationResult(raw string, worker bool) (*toolgateway.ProjectVerificationReceipt, bool) {
	var projection struct {
		VerificationReceipt *toolgateway.ProjectVerificationReceipt `json:"verification_receipt"`
	}
	if worker {
		var envelope struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal([]byte(raw), &envelope) != nil || json.Unmarshal(envelope.Result, &projection) != nil {
			return nil, false
		}
	} else {
		var envelope supervisorToolResultEnvelope
		if json.Unmarshal([]byte(raw), &envelope) != nil || json.Unmarshal([]byte(envelope.Stdout), &projection) != nil {
			return nil, false
		}
	}
	return projection.VerificationReceipt, projection.VerificationReceipt != nil
}

func projectVerificationReceiptFromCalls(calls []projectTestCallEvidence, jobID string) *toolgateway.ProjectVerificationReceipt {
	for i := len(calls) - 1; i >= 0; i-- {
		c := calls[i]
		if c.status != domain.SupervisorToolCompleted {
			continue
		}
		var raw string
		worker := false
		if c.call.Supervisor != nil {
			raw = c.call.Supervisor.ResultJSON
		} else {
			raw = c.call.Specialist.ResultJSON
			worker = true
		}
		if receipt, ok := projectVerificationResult(raw, worker); ok && receipt.JobID == jobID {
			return receipt
		}
	}
	return nil
}

// ReadProjectVerificationReceipt rechecks an existing receipt under the caller's
// current native authority. Workers can read only their own resource scope.
// It never starts, adopts, stops or waits on a Job.
func (s *CommandRuntimeService) ReadProjectVerificationReceipt(ctx context.Context, authority commandruntimeadapter.Authority, jobID string) (toolgateway.ProjectVerificationReceipt, error) {
	_, b, err := s.commandRuntimeAuthorityBindingsForTest(ctx, authority, true)
	if err != nil {
		return toolgateway.ProjectVerificationReceipt{}, err
	}
	job, err := s.authorizeReadableJob(ctx, jobID, b)
	if err != nil {
		return toolgateway.ProjectVerificationReceipt{}, err
	}
	origin, err := s.projectTestJobSource(ctx, job, b.actor.ID)
	if err != nil {
		return toolgateway.ProjectVerificationReceipt{}, err
	}
	calls, err := s.projectTestCalls(ctx, b.run.ID, b.actor.ID)
	if err != nil {
		return toolgateway.ProjectVerificationReceipt{}, err
	}
	receipt, err := s.checkProjectVerificationReceipt(ctx, b, job, origin, projectVerificationReceiptFromCalls(calls, jobID))
	if err != nil {
		return receipt, err
	}
	if origin.authority.ProjectTestSource != nil {
		for i := len(calls) - 1; i >= 0; i-- {
			c := calls[i]
			if c.jobID != "" && c.authority.ProjectTestSource != nil && *c.authority.ProjectTestSource == *origin.authority.ProjectTestSource && c.authority.Adapter.SameBackend(job.Adapter) {
				if c.jobID != job.ID {
					receipt.State, receipt.Reusable = "superseded", false
				}
				break
			}
		}
	}
	return receipt, nil
}

// LatestProjectVerificationReceipt selects the latest attempt for this exact
// source/test pair, including failed attempts. An older pass cannot replace it.
func (s *CommandRuntimeService) LatestProjectVerificationReceipt(ctx context.Context, authority commandruntimeadapter.Authority, target string) (toolgateway.ProjectVerificationReceipt, bool, error) {
	_, b, err := s.commandRuntimeAuthorityBindingsForTest(ctx, authority, true)
	if err != nil {
		return toolgateway.ProjectVerificationReceipt{}, false, err
	}
	current, err := s.projectTestSource(ctx, b, target)
	if err != nil {
		return toolgateway.ProjectVerificationReceipt{}, false, err
	}
	calls, err := s.projectTestCalls(ctx, b.run.ID, b.actor.ID)
	if err != nil {
		return toolgateway.ProjectVerificationReceipt{}, false, err
	}
	for i := len(calls) - 1; i >= 0; i-- {
		c := calls[i]
		if c.jobID == "" || c.authority.ProjectTestSource == nil || *c.authority.ProjectTestSource != current || !c.authority.Adapter.SameBackend(s.adapter) || !commandRuntimeAuthorityCurrent(s.capabilities, c.authority, b.permission) {
			continue
		}
		job, err := s.authorizeReadableJob(ctx, c.jobID, b)
		if err != nil {
			if c.status == domain.SupervisorToolFailed || c.status == domain.SupervisorToolDenied {
				return toolgateway.ProjectVerificationReceipt{Version: toolgateway.ProjectVerificationReceiptVersion, RunID: b.run.ID, AgentID: b.actor.ID,
					Target: target, SourceVersion: current.WorkspaceVersion, TestFingerprint: current.TestFingerprint, JobID: c.jobID, Adapter: s.adapter, State: "failed"}, true, nil
			}
			return toolgateway.ProjectVerificationReceipt{}, false, err
		}
		receipt, err := s.checkProjectVerificationReceipt(ctx, b, job, c, projectVerificationReceiptFromCalls(calls, job.ID))
		return receipt, true, err
	}
	return toolgateway.ProjectVerificationReceipt{}, false, nil
}

func (s *CommandRuntimeService) checkProjectVerificationReceipt(ctx context.Context, b commandRuntimeBindings, job runner.CommandRuntimeJob, origin projectTestCallEvidence, persisted *toolgateway.ProjectVerificationReceipt) (toolgateway.ProjectVerificationReceipt, error) {
	r := toolgateway.ProjectVerificationReceipt{Version: toolgateway.ProjectVerificationReceiptVersion, RunID: job.RunID, AgentID: b.actor.ID,
		WorkspaceID: job.WorkspaceID, Adapter: job.Adapter, JobID: job.ID, JobRecordVersion: job.Version, State: "pending", Cursor: job.OutputBaseCursor,
		ExitCode: job.ExitCode, TreeReaped: job.TreeReaped, CompletedAt: job.CompletedAt}
	if origin.call.Supervisor != nil {
		r.AgentAttemptID = origin.call.Supervisor.AgentAttemptID
	} else {
		r.AgentAttemptID = origin.call.Specialist.Ref.AttemptID
	}
	if source := origin.authority.ProjectTestSource; source != nil {
		r.SourceVersion, r.TestFingerprint, r.Target = source.WorkspaceVersion, source.TestFingerprint, source.Target
	} else {
		r.State = "stale"
		return r, nil
	}
	if persisted != nil && persisted.Version == r.Version && persisted.RunID == r.RunID && persisted.AgentID == r.AgentID && persisted.AgentAttemptID == r.AgentAttemptID &&
		persisted.WorkspaceID == r.WorkspaceID && persisted.SourceVersion == r.SourceVersion && persisted.TestFingerprint == r.TestFingerprint && persisted.Target == r.Target && persisted.Adapter.SameBackend(r.Adapter) && persisted.JobID == r.JobID {
		r.Cursor = persisted.Cursor
		r.CompletedSourceVersion = persisted.CompletedSourceVersion
		if persisted.State == "stale" && r.CompletedSourceVersion != "" {
			r.State = "stale"
		}
		if persisted.State == "passed" && persisted.Reusable && persisted.JobRecordVersion == job.Version && r.CompletedSourceVersion == r.SourceVersion {
			r.State = "passed"
			r.Reusable = true
		}
	}
	if job.State.Terminal() && (job.State != runner.CommandRuntimeJobCompleted || job.ExitCode == nil || *job.ExitCode != 0 || !job.TreeReaped || job.TruncationReason != "") {
		r.State, r.Reusable = "failed", false
	}
	if r.CompletedSourceVersion != "" && r.CompletedSourceVersion != r.SourceVersion {
		r.State, r.Reusable = "stale", false
	}
	if !job.Adapter.SameBackend(s.adapter) || job.WorkspaceRootSHA256 != b.rootSHA256 ||
		job.ModeSnapshotID != b.mode.ID || job.ModeRevision != b.mode.Revision ||
		job.ProfileSnapshotID != b.profile.ID || job.ProfileRevision != b.profile.Revision ||
		job.PermissionSnapshotID != b.permission.ID || job.PermissionRevision != b.permission.Revision ||
		job.PermissionMode != b.permission.Mode || !commandRuntimeJobGrantMatches(s.capabilities, b.permission, job) {
		r.State, r.Reusable = "stale", false
	}
	current, err := s.projectTestSource(ctx, b, r.Target)
	if err != nil {
		return r, err
	}
	if current.WorkspaceVersion != r.SourceVersion || current.TestFingerprint != r.TestFingerprint {
		r.State, r.Reusable = "stale", false
	}
	return r, nil
}

func (s *CommandRuntimeService) checkProjectTestDispatchSource(ctx context.Context, scope toolgateway.CommandRuntimeContext, b commandRuntimeBindings) error {
	if !scope.ProjectTest {
		return nil
	}
	st, ok := s.store.(commandApprovalStore)
	if !ok {
		return errors.New("project test dispatch source is unavailable")
	}
	id := scope.SupervisorToolCallID
	if scope.RequestedBy == "specialist_runner" {
		id = domain.SpecialistToolApprovalID(domain.AgentAttemptRef{RunID: scope.RunID, AgentID: scope.AgentID, AttemptID: scope.AgentAttemptID}, scope.SpecialistToolCallID)
	}
	source, err := readCommandSource(ctx, st, scope.RunID, id, true)
	if err != nil {
		return err
	}
	if source.authority.ProjectTestSource == nil {
		return nil
	}
	current, err := s.projectTestSource(ctx, b, source.authority.ProjectTestSource.Target)
	if err != nil {
		return err
	}
	if current != *source.authority.ProjectTestSource {
		return errors.New("project test source changed before native launch")
	}
	return nil
}

func (s *CommandRuntimeService) attachProjectVerificationResult(ctx context.Context, scope toolgateway.CommandRuntimeContext, b commandRuntimeBindings, result toolgateway.CommandRuntimeExecutionResult) (toolgateway.CommandRuntimeExecutionResult, error) {
	if len(result.Jobs) != 1 {
		return result, nil
	}
	if !scope.ProjectTest {
		if _, ok := s.store.(projectVerificationStore); !ok {
			return result, nil
		}
	}
	job, err := s.store.GetCommandRuntimeJob(ctx, result.Jobs[0].ID)
	if err != nil {
		return result, err
	}
	if job.State.Terminal() && job.TreeReaped {
		s.projectTestJobs.Delete(job.ID)
	}
	origin, err := s.projectTestJobSource(ctx, job, b.actor.ID)
	if err != nil {
		if !scope.ProjectTest && apperror.CodeOf(err) == apperror.CodeNotFound {
			return result, nil
		}
		return result, err
	}
	if origin.authority.ProjectTestSource == nil {
		return result, nil
	}
	calls, err := s.projectTestCalls(ctx, b.run.ID, b.actor.ID)
	if err != nil {
		return result, err
	}
	r, err := s.checkProjectVerificationReceipt(ctx, b, job, origin, projectVerificationReceiptFromCalls(calls, job.ID))
	if err != nil {
		return result, err
	}
	for _, page := range result.Pages {
		if page.JobID == job.ID {
			r.Cursor = page.NextCursor
		}
	}
	if job.State.Terminal() {
		// The first completed source identity is durable evidence. Later waits
		// may drain output, but cannot replace a mismatching terminal version.
		if r.CompletedSourceVersion == "" {
			current, err := s.projectTestSource(ctx, b, r.Target)
			if err != nil {
				return result, err
			}
			r.CompletedSourceVersion = current.WorkspaceVersion
			if r.CompletedSourceVersion != r.SourceVersion || current.TestFingerprint != r.TestFingerprint {
				r.State, r.Reusable = "stale", false
			}
		}
		complete := true
		for _, reason := range result.IncompleteReasons {
			if !projectVerificationHostLimitation(reason) {
				complete = false
			}
		}
		if r.State != "stale" && r.State != "failed" && complete && r.Cursor == job.OutputCursor && job.State == runner.CommandRuntimeJobCompleted && job.ExitCode != nil && *job.ExitCode == 0 && job.TreeReaped && job.TruncationReason == "" {
			r.State, r.Reusable = "passed", true
		}
	}
	result.VerificationReceipt = &r
	return result, nil
}

func projectVerificationHostLimitation(reason string) bool {
	return reason == "host_unsandboxed cannot prove host credentials are unavailable to the child process" || reason == "host_unsandboxed cannot enforce the command network=disabled intent"
}

func (m *CommandRuntimeMultiplexer) ReadProjectVerificationReceipt(ctx context.Context, a commandruntimeadapter.Authority, jobID string) (toolgateway.ProjectVerificationReceipt, error) {
	for _, s := range m.adapters {
		if s.adapter.SameBackend(a.Adapter) {
			return s.ReadProjectVerificationReceipt(ctx, a, jobID)
		}
	}
	return toolgateway.ProjectVerificationReceipt{}, apperror.New(apperror.CodeConflict, "project verification backend changed")
}

func (m *CommandRuntimeMultiplexer) LatestProjectVerificationReceipt(ctx context.Context, a commandruntimeadapter.Authority, target string) (toolgateway.ProjectVerificationReceipt, bool, error) {
	for _, s := range m.adapters {
		if s.adapter.SameBackend(a.Adapter) {
			return s.LatestProjectVerificationReceipt(ctx, a, target)
		}
	}
	return toolgateway.ProjectVerificationReceipt{}, false, apperror.New(apperror.CodeConflict, "project verification backend changed")
}
