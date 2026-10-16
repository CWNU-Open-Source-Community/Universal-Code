package application

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"time"

	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspacecheckpoint"
)

type workspaceApplyReceiptStore interface {
	RunFileWorkspaceStore
	GetFileEdit(context.Context, string) (fileedit.Edit, error)
	GetFileEditApplyOperation(context.Context, string) (fileedit.ApplyOperation, *fileedit.ApplyResult, bool, error)
	GetWorkspaceCheckpointTransactionByOperation(context.Context, string) (workspacecheckpoint.Transaction, bool, error)
	GetWorkspaceCheckpoint(context.Context, string) (workspacecheckpoint.Checkpoint, error)
}

// Reconcile only a successful workspace_apply whose optional feedback or
// artifact capture crossed the tool deadline. This never retries a mutation or
// infers success from current file bytes. The original fenced operation and its
// exact completed checkpoint must already be durable.
func (s *AgentRunner) reconcileWorkspaceApplyDeadline(ctx context.Context,
	turn domain.SupervisorTurn, call domain.SupervisorToolCall, outcome toolgateway.Outcome,
) (toolgateway.Outcome, bool, error) {
	reader, ok := s.store.(workspaceApplyReceiptStore)
	if !ok || call.ToolName != string(toolgateway.WorkspaceApplyTool) ||
		outcome.Result == nil || outcome.Result.Status != toolgateway.StatusCompleted ||
		!outcome.Decision.Allowed || outcome.Execution == nil ||
		outcome.Execution.Backend != "agent_code_workspace" || outcome.Validate() != nil ||
		outcome.Call.Name != toolgateway.WorkspaceApplyTool || outcome.Call.InvocationID == "" ||
		outcome.Call.RunID != call.RunID || outcome.Call.SessionID != turn.Run.SessionID ||
		outcome.Call.WorkspaceID != turn.Mission.WorkspaceID ||
		outcome.Call.AgentID != turn.Agent.ID || outcome.Call.AgentAttemptID != call.AttemptID ||
		outcome.Call.SupervisorTurn != call.Turn || outcome.Call.SupervisorToolCallID != call.CallID {
		return outcome, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	payload, err := toolgateway.NormalizeAgentCodePayload(toolgateway.WorkspaceApplyTool, json.RawMessage(call.PayloadJSON))
	if err != nil || string(payload) != call.PayloadJSON {
		return outcome, false, nil
	}
	var input toolgateway.WorkspaceApplyPayload
	if json.Unmarshal(payload, &input) != nil {
		return outcome, false, nil
	}
	authority, err := toolgateway.DecodeAgentCodeCallAuthority(json.RawMessage(call.AuthorityJSON))
	key := supervisorToolOperationKey(call.RunID, call.Turn, toolgateway.WorkspaceApplyTool, payload)
	callID, keyErr := runmutation.SupervisorToolCallID(key, call.Round)
	if err != nil || keyErr != nil || callID != call.CallID ||
		call.RunID != turn.Run.ID || call.AttemptID != turn.Checkpoint.AttemptID ||
		call.AgentID != turn.Agent.ID || call.AgentAttemptID != call.AttemptID ||
		authority.RunID != call.RunID || authority.MissionID != turn.Mission.ID ||
		authority.SessionID != turn.Run.SessionID || authority.RootAgentID != turn.Agent.ID ||
		authority.WorkspaceID != turn.Mission.WorkspaceID ||
		outcome.Call.CapabilityGeneration != authority.CapabilityGeneration ||
		outcome.Call.RootFingerprint != authority.RootFingerprint ||
		outcome.Result.Metadata["capability_generation"] != authority.CapabilityGeneration {
		return outcome, false, nil
	}
	// An explicit retry of a sealed pre-boundary failure can reuse its prepared
	// intent. Its NEW checkpoint must still belong to this exact invocation.
	if recovery, ok := s.store.(interface {
		GetFailedFileApplyOperationKey(context.Context, string, string, string) (string, bool, error)
	}); ok {
		oldKey, found, lookupErr := recovery.GetFailedFileApplyOperationKey(ctx, call.RunID, input.EditID, turn.Agent.ID)
		if lookupErr != nil {
			return outcome, false, lookupErr
		}
		if found {
			key = oldKey
		}
	}
	digest := runmutation.FileEditApplyOperationDigest(call.RunID, input.EditID, key)
	operation, result, found, err := reader.GetFileEditApplyOperation(ctx, digest)
	if err != nil || !found || result == nil {
		return outcome, false, err
	}
	kind := map[string]string{"create": fileedit.OperationCreate, "propose_patch": fileedit.OperationReplace,
		"replace": fileedit.OperationReplace, "move": fileedit.OperationMove}[input.ExpectedAction]
	if operation.ProtocolVersion != fileedit.FileEditApplyProtocolVersion || operation.KeyDigest != digest ||
		operation.RequestFingerprint != runmutation.FileEditApplyRequestFingerprint(call.RunID, input.EditID, turn.Agent.ID) ||
		operation.RunID != call.RunID || operation.SessionID != turn.Run.SessionID ||
		operation.AppliedBy != turn.Agent.ID || operation.EditID != input.EditID || operation.Operation != kind ||
		operation.OriginalHash != input.ExpectedOriginalSHA256 || operation.ProposedHash != input.ExpectedProposedSHA256 ||
		result.OperationKeyDigest != digest || result.Status != fileedit.ApplyCompleted || result.ReasonCode != "" {
		return outcome, false, nil
	}
	// Resolve only durable ownership here. A historical receipt does not grant
	// new execution, and needs no further native Git inspection of either root.
	files, err := resolveRunFileWorkspaceControl(ctx, reader, turn.Run, turn.Mission, s.drydocks)
	if err != nil {
		return outcome, false, err
	}
	edit, err := reader.GetFileEdit(ctx, input.EditID)
	if err != nil {
		return outcome, false, err
	}
	if edit.ID != input.EditID || operation.WorkspaceID != files.Workspace.ID || edit.WorkspaceID != operation.WorkspaceID ||
		edit.SessionID != operation.SessionID || edit.Status != fileedit.StatusApplied ||
		edit.Operation != operation.Operation || edit.Path != operation.Path ||
		edit.DestinationPath != operation.DestinationPath || edit.OriginalHash != operation.OriginalHash ||
		edit.ProposedHash != operation.ProposedHash || edit.DestinationOriginalHash != operation.DestinationOriginalHash ||
		edit.DestinationProposedHash != operation.DestinationProposedHash {
		return outcome, false, nil
	}
	boundaryDigest := workspaceBoundaryOperationDigest(call.RunID, workspacecheckpoint.TransactionFileTool, digest)
	transaction, found, err := reader.GetWorkspaceCheckpointTransactionByOperation(ctx, boundaryDigest)
	if err != nil || !found {
		return outcome, false, err
	}
	fingerprint := runmutation.Fingerprint("workspace_mutation_boundary_request.v1", call.RunID,
		turn.Mission.ID, turn.Run.SessionID, operation.WorkspaceID,
		string(workspacecheckpoint.TransactionFileTool), input.EditID, call.CallID)
	if transaction.OperationKeyDigest != boundaryDigest || transaction.RequestFingerprint != fingerprint ||
		transaction.RunID != call.RunID || transaction.WorkspaceID != operation.WorkspaceID ||
		transaction.Kind != workspacecheckpoint.TransactionFileTool || transaction.TriggerReceiptID != input.EditID ||
		transaction.Status != workspacecheckpoint.TransactionCompleted || transaction.ErrorCode != "" ||
		transaction.AfterCheckpointID == "" || transaction.CompletedAt == nil {
		return outcome, false, nil
	}
	for _, phase := range []workspacecheckpoint.Phase{workspacecheckpoint.PhaseBefore, workspacecheckpoint.PhaseAfter} {
		id := transaction.BeforeCheckpointID
		if phase == workspacecheckpoint.PhaseAfter {
			id = transaction.AfterCheckpointID
		}
		checkpoint, err := reader.GetWorkspaceCheckpoint(ctx, id)
		if err != nil {
			return outcome, false, err
		}
		if checkpoint.RunID != call.RunID || checkpoint.MissionID != turn.Mission.ID ||
			checkpoint.SessionID != turn.Run.SessionID || checkpoint.WorkspaceID != operation.WorkspaceID ||
			checkpoint.AttemptID != call.AttemptID || checkpoint.CapabilityGeneration != authority.CapabilityGeneration ||
			checkpoint.Trigger != workspacecheckpoint.TriggerFileTool || checkpoint.TriggerReceiptID != input.EditID ||
			checkpoint.Phase != phase || (phase == workspacecheckpoint.PhaseAfter &&
			checkpoint.ParentCheckpointID != transaction.BeforeCheckpointID) {
			return outcome, false, nil
		}
	}
	// Decode through the same public field names as the ordinary apply result.
	var projection map[string]json.RawMessage
	if json.Unmarshal([]byte(outcome.Result.Stdout), &projection) != nil {
		return outcome, false, nil
	}
	for field, expected := range map[string]string{"version": toolgateway.AgentCodeRegistryVersion,
		"edit_id": edit.ID, "operation": edit.Operation, "path": edit.Path,
		"destination_path": edit.DestinationPath, "status": fileedit.StatusApplied,
		"original_sha256": edit.OriginalHash, "proposed_sha256": edit.ProposedHash,
		"destination_proposed_sha256": edit.DestinationProposedHash} {
		var actual string
		if json.Unmarshal(projection[field], &actual) != nil || actual != expected {
			return outcome, false, nil
		}
	}
	if outcome.Result.Metadata["status"] != fileedit.StatusApplied ||
		outcome.Result.Metadata["edit_id"] != edit.ID || outcome.Result.Metadata["operation"] != edit.Operation ||
		outcome.Result.Metadata["workspace_id"] != operation.WorkspaceID {
		return outcome, false, nil
	}
	// Artifact capture may be the only operation canceled by the tool deadline.
	// Retry its identical bytes under the live parent, using the original budget
	// invocation. The artifact store enforces identity and content on replay.
	descriptors, err := s.tools.Artifacts().Capture(ctx, artifact.CaptureRequest{
		RunID: call.RunID, SessionID: turn.Run.SessionID, WorkspaceID: turn.Mission.WorkspaceID,
		SourceID: outcome.Call.InvocationID, ToolName: call.ToolName,
		Outputs: []artifact.Output{{Stream: artifact.StreamStdout, MIME: "application/json", Content: outcome.Result.Stdout}},
	})
	if err != nil {
		return outcome, false, fmt.Errorf("archive committed workspace_apply receipt: %w", err)
	}
	resultCopy := *outcome.Result
	resultCopy.Metadata = maps.Clone(resultCopy.Metadata)
	resultCopy.Metadata["artifact_count"] = strconv.Itoa(len(descriptors))
	for _, descriptor := range descriptors {
		prefix := "artifact_" + string(descriptor.Stream)
		resultCopy.Metadata[prefix+"_id"] = descriptor.ID
		resultCopy.Metadata[prefix+"_sha256"] = descriptor.SHA256
		resultCopy.Metadata[prefix+"_bytes"] = strconv.FormatInt(descriptor.SizeBytes, 10)
	}
	outcome.Result = &resultCopy
	return outcome, true, nil
}
