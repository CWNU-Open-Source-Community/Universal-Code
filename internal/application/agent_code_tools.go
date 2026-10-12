package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/githubreview"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

type AgentCodeToolStore interface {
	FileEditApplyStore
	GetRunMode(context.Context, string) (domain.RunModeSnapshot, error)
	GetRunExecutionPermission(context.Context, string) (
		domain.RunExecutionPermissionSnapshot, error)
	GetAgentNode(context.Context, string) (domain.AgentNode, error)
	GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error)
}

type AgentCodeToolExecutor struct {
	store                 AgentCodeToolStore
	manager               *fileedit.Manager
	apply                 *FileEditApplyService
	revert                *FileEditProposalService
	drydocks              *RunWorktreeService
	executionCapabilities domain.ExecutionPermissionRuntimeCapabilities
}

func (e *AgentCodeToolExecutor) WithExecutionPermissionCapabilities(
	capabilities domain.ExecutionPermissionRuntimeCapabilities,
) *AgentCodeToolExecutor {
	if e != nil {
		e.executionCapabilities = capabilities
		e.apply.WithExecutionPermissionCapabilities(capabilities)
	}
	return e
}

type agentCodeGitHubEvidenceStore interface {
	ListGitHubReviewEvidence(context.Context, string, int) ([]githubreview.EvidenceRecord, error)
	GetGitHubReviewEvidence(context.Context, string) (githubreview.EvidenceRecord, bool, error)
}

func (e *AgentCodeToolExecutor) WithRunWorktree(drydocks *RunWorktreeService) *AgentCodeToolExecutor {
	if e != nil {
		e.drydocks = drydocks
		e.apply.WithRunWorktree(drydocks)
		e.revert.WithRunWorktree(drydocks)
	}
	return e
}

func NewAgentCodeToolExecutor(store AgentCodeToolStore,
	checker policy.Checker,
) *AgentCodeToolExecutor {
	checkpoints := embeddedWorkspaceCheckpointService(store,
		domain.ExecutionPermissionRuntimeCapabilities{})
	return &AgentCodeToolExecutor{store: store, manager: fileedit.NewManager(store),
		apply:  NewFileEditApplyService(store, checker, checkpoints),
		revert: NewFileEditProposalService(store, checker)}
}

func (e *AgentCodeToolExecutor) ExecuteAgentCode(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, name toolgateway.ToolName,
	payload json.RawMessage,
) (result toolgateway.AgentCodeExecutionResult, returnErr error) {
	if e == nil || e.store == nil || e.manager == nil || e.apply == nil {
		return toolgateway.AgentCodeExecutionResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "agent code tool dependencies are unavailable")
	}
	if err := e.validateScope(ctx, scope, name); err != nil {
		return toolgateway.AgentCodeExecutionResult{}, err
	}
	run, err := e.store.GetRun(ctx, scope.RunID)
	if err != nil {
		return toolgateway.AgentCodeExecutionResult{}, apperror.Normalize(err)
	}
	pathPolicy, err := projectconfig.PathPolicy(run.Config)
	if err != nil {
		return toolgateway.AgentCodeExecutionResult{}, err
	}
	if err := e.checkProjectPaths(ctx, pathPolicy, name, payload); err != nil {
		return toolgateway.AgentCodeExecutionResult{}, err
	}
	payload = append(json.RawMessage(nil), payload...)
	if definition, found := toolgateway.AgentCodeToolDefinition(name); found && definition.Class == toolgateway.ClassWorkspaceRead {
		recheck, err := e.authorizeAgentCodeRead(ctx, scope, name, payload)
		if err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		defer func() {
			if returnErr == nil {
				if err := recheck(); err != nil {
					result, returnErr = toolgateway.AgentCodeExecutionResult{}, err
				}
			}
		}()
	}
	var value any
	var metadata = map[string]string{"workspace_id": scope.WorkspaceID}
	switch name {
	case toolgateway.WorkspaceListTool:
		var input toolgateway.WorkspaceListPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		result, err := workspace.AgentCodeListDirectory(scope.WorkspaceRoot,
			scope.WorkspaceID, input.Path, input.Cursor, input.Limit, false, pathPolicy)
		if err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		value = result
		metadata["result_count"] = fmt.Sprint(len(result.Items))
		metadata["truncated"] = fmt.Sprint(result.Truncated)
	case toolgateway.WorkspaceReadTool:
		var input toolgateway.WorkspaceReadPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		result, err := workspace.AgentCodeReadFile(scope.WorkspaceRoot,
			scope.WorkspaceID, input.Path, input.StartLine, input.EndLine, false, pathPolicy)
		if err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		value = result
		metadata["path"] = result.Path
		metadata["content_sha256"] = result.ContentSHA256
		metadata["truncated"] = fmt.Sprint(result.Truncated)
	case toolgateway.WorkspaceGlobTool:
		var input toolgateway.WorkspaceGlobPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		result, err := workspace.AgentCodeGlobFiles(scope.WorkspaceRoot,
			scope.WorkspaceID, input.Pattern, input.Cursor, input.Limit, false, pathPolicy)
		if err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		value = result
		metadata["result_count"] = fmt.Sprint(len(result.Paths))
		metadata["truncated"] = fmt.Sprint(result.Truncated)
	case toolgateway.WorkspaceGrepTool:
		var input toolgateway.WorkspaceGrepPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		result, err := workspace.AgentCodeGrepFiles(scope.WorkspaceRoot,
			scope.WorkspaceID, input.Query, input.Pattern, input.Cursor, input.Limit,
			input.CaseSensitive, false, pathPolicy)
		if err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		value = result
		metadata["result_count"] = fmt.Sprint(len(result.Matches))
		metadata["truncated"] = fmt.Sprint(result.Truncated)
	case toolgateway.GitHubEvidenceListTool:
		metadata["workspace_id"] = scope.ControlWorkspaceID()
		var input toolgateway.GitHubEvidenceListPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		githubStore, ok := e.store.(agentCodeGitHubEvidenceStore)
		if !ok {
			return toolgateway.AgentCodeExecutionResult{}, apperror.New(
				apperror.CodeFailedPrecondition, "GitHub review evidence store is unavailable")
		}
		result, err := githubStore.ListGitHubReviewEvidence(ctx, scope.RunID, input.Limit)
		if err != nil {
			return toolgateway.AgentCodeExecutionResult{}, apperror.Normalize(err)
		}
		value = result
		metadata["result_count"] = fmt.Sprint(len(result))
		metadata["trust"] = "untrusted_remote_data"
	case toolgateway.GitHubEvidenceReadTool:
		metadata["workspace_id"] = scope.ControlWorkspaceID()
		var input toolgateway.GitHubEvidenceReadPayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		githubStore, ok := e.store.(agentCodeGitHubEvidenceStore)
		if !ok {
			return toolgateway.AgentCodeExecutionResult{}, apperror.New(
				apperror.CodeFailedPrecondition, "GitHub review evidence store is unavailable")
		}
		result, found, err := githubStore.GetGitHubReviewEvidence(ctx, input.EvidenceID)
		if err != nil {
			return toolgateway.AgentCodeExecutionResult{}, apperror.Normalize(err)
		}
		if !found || result.RunID != scope.RunID || result.WorkspaceID != scope.ControlWorkspaceID() {
			return toolgateway.AgentCodeExecutionResult{}, apperror.New(
				apperror.CodeNotFound, "GitHub review evidence was not found for this Run")
		}
		value = result
		metadata["evidence_id"] = result.ID
		metadata["trust"] = "untrusted_remote_data"
	case toolgateway.WorkspaceChangeTool:
		canonical, err := toolgateway.NormalizeAgentCodePayload(name, payload)
		if err != nil {
			return toolgateway.AgentCodeExecutionResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "invalid workspace change arguments", err)
		}
		var input toolgateway.WorkspaceChangePayload
		if err := json.Unmarshal(canonical, &input); err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		result, replayed, err := e.propose(ctx, scope, input)
		if err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		value = e.agentCodeEditResult(ctx, scope, result,
			result.Operation == fileedit.OperationDelete)
		metadata["edit_id"] = result.ID
		metadata["status"] = result.Status
		metadata["operation"] = result.Operation
		encoded, err := json.Marshal(value)
		return toolgateway.AgentCodeExecutionResult{JSON: string(encoded),
			Metadata: metadata, Replayed: replayed}, err
	case toolgateway.WorkspaceApplyTool:
		canonical, err := toolgateway.NormalizeAgentCodePayload(name, payload)
		if err != nil {
			return toolgateway.AgentCodeExecutionResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "invalid workspace apply arguments", err)
		}
		var input toolgateway.WorkspaceApplyPayload
		if err := json.Unmarshal(canonical, &input); err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		result, err := e.applyChange(ctx, scope, input)
		if err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		value = agentCodeApplyResult(result)
		metadata["edit_id"] = result.Edit.ID
		metadata["status"] = result.Edit.Status
		metadata["operation"] = result.Edit.Operation
		encoded, err := json.Marshal(value)
		return toolgateway.AgentCodeExecutionResult{JSON: string(encoded),
			Metadata: metadata, Replayed: result.Replayed}, err
	case toolgateway.WorkspaceDeleteTool:
		var input toolgateway.WorkspaceDeletePayload
		if err := json.Unmarshal(payload, &input); err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		if input.Action == "propose" {
			result, replayed, err := e.proposeDelete(ctx, scope, input)
			if err != nil {
				return toolgateway.AgentCodeExecutionResult{}, err
			}
			value = e.agentCodeEditResult(ctx, scope, result, true)
			metadata["edit_id"] = result.ID
			metadata["status"] = result.Status
			metadata["operation"] = result.Operation
			encoded, err := json.Marshal(value)
			return toolgateway.AgentCodeExecutionResult{JSON: string(encoded),
				Metadata: metadata, Replayed: replayed}, err
		}
		result, err := e.applyDelete(ctx, scope, input)
		if err != nil {
			return toolgateway.AgentCodeExecutionResult{}, err
		}
		value = agentCodeApplyResult(result)
		metadata["edit_id"] = result.Edit.ID
		metadata["status"] = result.Edit.Status
		metadata["operation"] = result.Edit.Operation
		encoded, err := json.Marshal(value)
		return toolgateway.AgentCodeExecutionResult{JSON: string(encoded),
			Metadata: metadata, Replayed: result.Replayed}, err
	default:
		return toolgateway.AgentCodeExecutionResult{}, apperror.New(
			apperror.CodeInvalidArgument, "unsupported agent code tool")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return toolgateway.AgentCodeExecutionResult{}, err
	}
	return toolgateway.AgentCodeExecutionResult{JSON: string(encoded), Metadata: metadata}, nil
}

func (e *AgentCodeToolExecutor) validateScope(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, name toolgateway.ToolName,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := scope.Validate(); err != nil {
		return apperror.Wrap(apperror.CodeFailedPrecondition,
			"agent code tool scope is invalid", err)
	}
	run, err := e.store.GetRun(ctx, scope.RunID)
	if err != nil {
		return apperror.Normalize(err)
	}
	mission, err := e.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return apperror.Normalize(err)
	}
	linkedSession, err := e.store.GetSession(ctx, run.SessionID)
	if err != nil {
		return apperror.Normalize(err)
	}
	files, err := ResolveRunFileWorkspace(ctx, e.store, run, mission, e.drydocks)
	if err != nil {
		return apperror.Normalize(err)
	}
	mode, err := e.store.GetRunMode(ctx, run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	permission, err := e.store.GetRunExecutionPermission(ctx, run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	agent, err := e.store.GetAgentNode(ctx, scope.RootAgentID)
	if err != nil {
		return apperror.Normalize(err)
	}
	lease, found, err := e.store.GetRunExecutionLease(ctx, run.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	registered := files.Workspace
	sourceWorkspaceID := scope.ControlWorkspaceID()
	rootFingerprint, err := workspace.AgentCodeRootFingerprint(registered.RootPath)
	if err != nil {
		return apperror.Normalize(err)
	}
	if run.Status != domain.RunRunning || linkedSession.Status != session.StatusActive ||
		run.MissionID != scope.MissionID || run.SessionID != scope.SessionID ||
		mission.ID != scope.MissionID || mission.WorkspaceID != sourceWorkspaceID ||
		linkedSession.WorkspaceID != sourceWorkspaceID || registered.ID != scope.WorkspaceID ||
		registered.RootPath != scope.WorkspaceRoot || rootFingerprint != scope.RootFingerprint ||
		mode.Revision != scope.ModeRevision || mode.Surface != scope.Surface ||
		mode.Phase != scope.Phase || mode.Profile != scope.Profile ||
		permission.Revision != scope.PermissionRevision || permission.Mode != scope.PermissionMode ||
		agent.RunID != run.ID || agent.SessionID != run.SessionID ||
		agent.Role != domain.AgentRoleRoot || agent.Profile != scope.Profile ||
		!found || !lease.ActiveAt(scopeNow()) || lease.LeaseID != scope.LeaseID ||
		lease.Generation != scope.LeaseGeneration {
		return apperror.New(apperror.CodeFailedPrecondition,
			"agent code tool authority binding changed before execution")
	}
	if !agentCodeRuntimeCurrent(e.executionCapabilities, permission, scope.PermissionSnapshotID, scope.PermissionGeneration, scope.PermissionRuntimeEpoch, scope.RunAuthorizationFence) {
		return apperror.New(apperror.CodePolicyDenied, "agent code tool requires its current permission and runtime fence")
	}
	capabilities := toolgateway.AgentCodeCapabilities(toolgateway.AgentCodeCapabilityContext{
		RunID: run.ID, MissionID: mission.ID, RootAgentID: agent.ID,
		WorkspaceID:     sourceWorkspaceID,
		RootFingerprint: rootFingerprint, Surface: mode.Surface, Phase: mode.Phase,
		Role: agent.Role, Profile: agent.Profile, PermissionMode: permission.Mode,
		PermissionSnapshotID:   scope.PermissionSnapshotID,
		PermissionGeneration:   scope.PermissionGeneration,
		PermissionRuntimeEpoch: scope.PermissionRuntimeEpoch,
		RunAuthorizationFence:  scope.RunAuthorizationFence,
		ModeRevision:           mode.Revision, PermissionRevision: permission.Revision})
	if capabilities.Generation != scope.CapabilityGeneration {
		return apperror.New(apperror.CodeConflict,
			"agent code capability generation changed before execution")
	}
	// code-intel tools deliberately reuse the exact Agent Code authority and
	// fencing checks above. Their reviewed server generation and capability
	// fingerprint are independently validated by the Go-owned LSP manager.
	if toolgateway.IsCodeIntelTool(name) {
		if available, reason := toolgateway.CodeIntelScopeEligibility(
			toolgateway.AgentCodeCapabilityContext{RunID: run.ID, MissionID: mission.ID,
				RootAgentID: agent.ID, WorkspaceID: sourceWorkspaceID,
				RootFingerprint: rootFingerprint, Surface: mode.Surface, Phase: mode.Phase,
				Role: agent.Role, Profile: agent.Profile, PermissionMode: permission.Mode,
				ModeRevision: mode.Revision, PermissionRevision: permission.Revision}); !available {
			return apperror.New(apperror.CodePolicyDenied, reason)
		}
		return nil
	}
	for _, item := range capabilities.Tools {
		if item.Name == name {
			if !item.Available {
				return apperror.New(apperror.CodePolicyDenied, item.Refusal)
			}
			return nil
		}
	}
	return apperror.New(apperror.CodePolicyDenied,
		"agent code tool is absent from the current capability registry")
}

func (e *AgentCodeToolExecutor) propose(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, input toolgateway.WorkspaceChangePayload,
) (fileedit.Edit, bool, error) {
	if input.Action == "propose_revert" {
		result, err := e.revert.proposeRevertWithWriter(ctx, CreateFileEditRevertProposalRequest{
			Version: FileEditProposalProtocolVersion, RunID: scope.RunID,
			SourceRunID: input.SourceRunID, SourceEditID: input.SourceEditID,
			Path: input.Path, ExpectedSHA256: input.ExpectedSHA256,
			OperationKey: scope.OperationKey}, func(ctx context.Context,
			prepared fileedit.Edit,
		) (fileedit.Edit, bool, error) {
			automatic, err := e.automaticallyAuthorizePreparedFileEdit(ctx, scope, prepared)
			if err != nil {
				return fileedit.Edit{}, false, err
			}
			if automatic {
				return e.saveAutomaticallyAuthorizedPreparedProposal(ctx, scope, prepared)
			}
			writer, ok := e.store.(interface {
				CreateFileEditIfAbsent(context.Context, fileedit.Edit) (fileedit.Edit, bool, error)
			})
			if !ok {
				return fileedit.Edit{}, false, apperror.New(apperror.CodeFailedPrecondition,
					"atomic file edit proposal storage is required")
			}
			return writer.CreateFileEditIfAbsent(ctx, prepared)
		})
		return result.Edit, result.Replayed, err
	}
	operation := fileedit.OperationReplace
	proposedText := ""
	destination := ""
	destinationHash := ""
	switch input.Action {
	case "propose_patch":
		text, observedHash, err := workspace.AgentCodeReadMutationSource(
			scope.WorkspaceRoot, input.Path)
		if err != nil {
			return fileedit.Edit{}, false, err
		}
		if observedHash != input.ExpectedSHA256 {
			return fileedit.Edit{}, false, apperror.New(apperror.CodeConflict,
				"workspace patch source changed after it was read")
		}
		proposedText = text
		for _, replacement := range input.Replacements {
			if count := strings.Count(proposedText, replacement.OldText); count != replacement.ExpectedOccurrences {
				return fileedit.Edit{}, false, apperror.New(apperror.CodeConflict,
					fmt.Sprintf("workspace patch expected %d occurrence(s), found %d",
						replacement.ExpectedOccurrences, count))
			}
			proposedText = strings.ReplaceAll(proposedText, replacement.OldText,
				replacement.NewText)
		}
	case "replace":
		if _, _, err := workspace.AgentCodeResolveWritePath(scope.WorkspaceRoot, input.Path, false); err != nil {
			return fileedit.Edit{}, false, err
		}
		// saveProposal replays an existing exact proposal before reading current
		// bytes. A new proposal still binds Manager.PrepareProposal to the exact
		// original hash, and apply performs the ordinary second CAS check.
		proposedText = input.Content
	case "create":
		operation = fileedit.OperationCreate
		if _, _, err := workspace.AgentCodeResolveCreatePath(scope.WorkspaceRoot,
			input.Path); err != nil {
			return fileedit.Edit{}, false, err
		}
		proposedText = input.Content
	case "move":
		operation = fileedit.OperationMove
		if _, _, err := workspace.AgentCodeResolveWritePath(scope.WorkspaceRoot,
			input.Path, false); err != nil {
			return fileedit.Edit{}, false, err
		}
		if _, _, err := workspace.AgentCodeResolveWritePath(scope.WorkspaceRoot,
			input.DestinationPath, true); err != nil {
			return fileedit.Edit{}, false, err
		}
		destination = input.DestinationPath
		destinationHash = input.DestinationExpectedSHA256
	default:
		return fileedit.Edit{}, false, apperror.New(apperror.CodeInvalidArgument,
			"workspace change action is invalid")
	}
	if redact.String(proposedText) != proposedText {
		return fileedit.Edit{}, false, apperror.New(apperror.CodePolicyDenied,
			"workspace mutation content contains secret-like material")
	}
	proposal := fileedit.Proposal{ID: agentCodeEditID(scope.OperationKey),
		SessionID: scope.SessionID, WorkspaceID: scope.WorkspaceID,
		WorkspaceRoot: scope.WorkspaceRoot, Path: input.Path, Operation: operation,
		DestinationPath: destination, ProposedText: proposedText,
		ExpectedOriginalHash:    input.ExpectedSHA256,
		ExpectedDestinationHash: destinationHash}
	return e.saveProposal(ctx, scope, proposal)
}

func (e *AgentCodeToolExecutor) proposeDelete(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, input toolgateway.WorkspaceDeletePayload,
) (fileedit.Edit, bool, error) {
	if _, _, err := workspace.AgentCodeResolveWritePath(scope.WorkspaceRoot,
		input.Path, false); err != nil {
		return fileedit.Edit{}, false, err
	}
	return e.saveProposal(ctx, scope, fileedit.Proposal{ID: agentCodeEditID(scope.OperationKey),
		SessionID: scope.SessionID, WorkspaceID: scope.WorkspaceID,
		WorkspaceRoot: scope.WorkspaceRoot, Path: input.Path,
		Operation: fileedit.OperationDelete, ExpectedOriginalHash: input.ExpectedSHA256})
}

func (e *AgentCodeToolExecutor) saveProposal(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, proposal fileedit.Proposal,
) (fileedit.Edit, bool, error) {
	if existing, err := e.store.GetFileEdit(ctx, proposal.ID); err == nil {
		if !sameAgentCodeProposal(existing, proposal) {
			return fileedit.Edit{}, false, apperror.New(apperror.CodeConflict,
				"agent code operation key was already used for a different change")
		}
		return existing, true, nil
	} else if apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeNotFound {
		return fileedit.Edit{}, false, apperror.Normalize(err)
	}
	edit, err := e.manager.PrepareProposal(ctx, proposal)
	if err != nil {
		return fileedit.Edit{}, false, apperror.Normalize(err)
	}
	automatic, err := e.automaticallyAuthorizePreparedFileEdit(ctx, scope, edit)
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	if automatic {
		return e.saveAutomaticallyAuthorizedPreparedProposal(ctx, scope, edit)
	}
	writer, ok := e.store.(interface {
		CreateFileEditIfAbsent(context.Context, fileedit.Edit) (fileedit.Edit, bool, error)
	})
	if !ok {
		return fileedit.Edit{}, false, apperror.New(apperror.CodeFailedPrecondition, "atomic file proposal storage unavailable")
	}
	return writer.CreateFileEditIfAbsent(ctx, edit)
}

func (e *AgentCodeToolExecutor) saveAutomaticallyAuthorizedPreparedProposal(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, edit fileedit.Edit,
) (fileedit.Edit, bool, error) {
	automatic, err := e.automaticallyAuthorizePreparedFileEdit(ctx, scope, edit)
	if err != nil {
		return fileedit.Edit{}, false, err
	}
	if !automatic {
		return fileedit.Edit{}, false, apperror.New(apperror.CodePolicyDenied,
			"automatic FileEdit authorization is unavailable for this operation")
	}
	writer, ok := e.store.(interface {
		CreateAutomaticallyAuthorizedFileEditIfAbsent(context.Context,
			fileedit.Edit, fileedit.AutoAuthorization) (fileedit.Edit, bool, error)
	})
	if !ok {
		return fileedit.Edit{}, false, apperror.New(apperror.CodeFailedPrecondition,
			"automatic FileEdit authorization store is unavailable")
	}
	// Recheck the exact live grant, lease, permission revision, and capability
	// after reading and preparing the file but before the atomic durable insert.
	if err := e.validateScope(ctx, scope, toolgateway.WorkspaceChangeTool); err != nil {
		return fileedit.Edit{}, false, err
	}
	auth := fileedit.AutoAuthorization{
		RunID: scope.RunID, SessionID: scope.SessionID,
		WorkspaceID: scope.WorkspaceID, AgentID: scope.RootAgentID,
		OperationKeyDigest: runmutation.Fingerprint("agent_code_file_edit_source.v1",
			scope.RunID, scope.OperationKey),
		ProposalFingerprint: runmutation.Fingerprint("agent_code_file_edit_proposal.v1",
			edit.ID, edit.SessionID, edit.WorkspaceID, edit.Operation, edit.Path,
			edit.DestinationPath, edit.OriginalHash, edit.ProposedHash,
			edit.DestinationOriginalHash, edit.DestinationProposedHash),
		PermissionSnapshotID:    scope.PermissionSnapshotID,
		DestinationPath:         edit.DestinationPath,
		DestinationOriginalHash: edit.DestinationOriginalHash,
		DestinationProposedHash: edit.DestinationProposedHash,
		PermissionRevision:      scope.PermissionRevision,
		ModeRevision:            scope.ModeRevision,
		RuntimeEpoch:            scope.PermissionRuntimeEpoch,
		RuntimeGeneration:       scope.PermissionGeneration,
		RunAuthorizationFence:   scope.RunAuthorizationFence,
		CapabilityGeneration:    scope.CapabilityGeneration,
		LeaseID:                 scope.LeaseID, LeaseGeneration: scope.LeaseGeneration,
	}
	return writer.CreateAutomaticallyAuthorizedFileEditIfAbsent(ctx, edit, auth)
}

func sameAgentCodeProposal(edit fileedit.Edit, proposal fileedit.Proposal) bool {
	operation, err := fileedit.NormalizeOperation(proposal.Operation)
	return err == nil && edit.ID == proposal.ID && edit.SessionID == proposal.SessionID &&
		edit.WorkspaceID == proposal.WorkspaceID && edit.Path == proposal.Path &&
		edit.Operation == operation && edit.DestinationPath == proposal.DestinationPath &&
		edit.OriginalHash == proposal.ExpectedOriginalHash &&
		edit.DestinationOriginalHash == proposal.ExpectedDestinationHash &&
		(edit.Operation == fileedit.OperationMove || edit.Operation == fileedit.OperationDelete ||
			fileedit.HashText(proposal.ProposedText) == edit.ProposedHash)
}

func (e *AgentCodeToolExecutor) applyChange(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, input toolgateway.WorkspaceApplyPayload,
) (ApplyFileEditResult, error) {
	edit, err := e.store.GetFileEdit(ctx, input.EditID)
	if err != nil {
		return ApplyFileEditResult{}, apperror.Normalize(err)
	}
	expectedOperation := fileedit.OperationReplace
	if input.ExpectedAction == "create" {
		expectedOperation = fileedit.OperationCreate
	} else if input.ExpectedAction == "move" {
		expectedOperation = fileedit.OperationMove
	}
	if agentCodeEditBindingMismatch(edit, scope) || edit.Operation != expectedOperation ||
		edit.OriginalHash != input.ExpectedOriginalSHA256 ||
		edit.ProposedHash != input.ExpectedProposedSHA256 ||
		edit.Operation == fileedit.OperationDelete {
		return ApplyFileEditResult{}, apperror.New(apperror.CodeConflict,
			"workspace apply expectations do not match the approved proposal")
	}
	operationKey := scope.OperationKey
	if reader, ok := e.store.(interface {
		GetFailedFileApplyOperationKey(context.Context, string, string, string) (string, bool, error)
	}); ok {
		key, found, lookupErr := reader.GetFailedFileApplyOperationKey(ctx, scope.RunID, edit.ID, scope.RootAgentID)
		if lookupErr != nil {
			return ApplyFileEditResult{}, apperror.Normalize(lookupErr)
		}
		if found {
			operationKey = key
		}
	}
	return e.apply.Apply(ctx, ApplyFileEditRequest{Version: fileedit.FileEditApplyProtocolVersion,
		RunID: scope.RunID, EditID: edit.ID, OperationKey: operationKey,
		AppliedBy: scope.RootAgentID, InvocationID: scope.CheckpointInvocationID(),
		CapabilityGeneration: scope.CapabilityGeneration, LeaseID: scope.LeaseID,
		LeaseGeneration:        scope.LeaseGeneration,
		PermissionSnapshotID:   scope.PermissionSnapshotID,
		PermissionGeneration:   scope.PermissionGeneration,
		PermissionRuntimeEpoch: scope.PermissionRuntimeEpoch, RunAuthorizationFence: scope.RunAuthorizationFence})
}

func (e *AgentCodeToolExecutor) applyDelete(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, input toolgateway.WorkspaceDeletePayload,
) (ApplyFileEditResult, error) {
	edit, err := e.store.GetFileEdit(ctx, input.EditID)
	if err != nil {
		return ApplyFileEditResult{}, apperror.Normalize(err)
	}
	if agentCodeEditBindingMismatch(edit, scope) || edit.Operation != fileedit.OperationDelete ||
		edit.Path != input.Path || edit.Path != input.ConfirmPath ||
		edit.OriginalHash != input.ExpectedSHA256 {
		return ApplyFileEditResult{}, apperror.New(apperror.CodeConflict,
			"workspace delete expectations do not match the approved proposal")
	}
	return e.apply.Apply(ctx, ApplyFileEditRequest{Version: fileedit.FileEditApplyProtocolVersion,
		RunID: scope.RunID, EditID: edit.ID, OperationKey: scope.OperationKey,
		AppliedBy: scope.RootAgentID, InvocationID: scope.CheckpointInvocationID(),
		CapabilityGeneration: scope.CapabilityGeneration, LeaseID: scope.LeaseID,
		LeaseGeneration: scope.LeaseGeneration, PermissionSnapshotID: scope.PermissionSnapshotID,
		PermissionGeneration: scope.PermissionGeneration, PermissionRuntimeEpoch: scope.PermissionRuntimeEpoch,
		RunAuthorizationFence: scope.RunAuthorizationFence})
}

func agentCodeEditID(operationKey string) string {
	sum := sha256.Sum256([]byte("agent-code-edit.v1\x00" + operationKey))
	return "edit-" + hex.EncodeToString(sum[:16])
}

func (e *AgentCodeToolExecutor) agentCodeEditResult(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, edit fileedit.Edit,
	deleteConfirmation bool,
) map[string]any {
	automatic := false
	authorized := false
	if reader, ok := e.store.(interface {
		GetFileEditAutoAuthorization(context.Context, string) (fileedit.AutoAuthorization, bool, error)
	}); ok {
		if source, found, err := reader.GetFileEditAutoAuthorization(ctx, edit.ID); err == nil {
			if found {
				automatic = true
				authorized = edit.Status == fileedit.StatusApproved &&
					source.RunID == scope.RunID &&
					source.PermissionSnapshotID == scope.PermissionSnapshotID &&
					source.RuntimeEpoch == scope.PermissionRuntimeEpoch &&
					source.RuntimeGeneration == scope.PermissionGeneration &&
					source.RunAuthorizationFence == scope.RunAuthorizationFence &&
					source.CapabilityGeneration == scope.CapabilityGeneration
			} else {
				// ExecuteAgentCode verified the current execution scope. Only a
				// confirmed absence of automatic authority permits the approved
				// manual projection; failed reads and expired automatic sources do not.
				authorized = edit.Status == fileedit.StatusApproved && !agentCodeEditBindingMismatch(edit, scope)
			}
		}
	}
	result := map[string]any{"version": toolgateway.AgentCodeRegistryVersion,
		"edit_id": edit.ID, "operation": edit.Operation, "path": edit.Path,
		"destination_path": edit.DestinationPath, "status": edit.Status,
		"diff": edit.Diff, "original_sha256": edit.OriginalHash,
		"proposed_sha256":             edit.ProposedHash,
		"destination_original_sha256": edit.DestinationOriginalHash,
		"destination_proposed_sha256": edit.DestinationProposedHash,
		"review_required":             !automatic && !authorized, "apply_authorized": authorized,
		"authorization_source": func() string {
			if automatic {
				return "operation_policy_automatic"
			}
			return "operator_review"
		}(),
		"delete_confirmation_required": deleteConfirmation}
	if edit.Operation == fileedit.OperationCreate || edit.Operation == fileedit.OperationReplace || edit.Operation == fileedit.OperationMove {
		result["apply_arguments"] = toolgateway.WorkspaceApplyPayload{Version: toolgateway.AgentCodeRegistryVersion,
			EditID: edit.ID, ExpectedAction: edit.Operation, ExpectedOriginalSHA256: edit.OriginalHash, ExpectedProposedSHA256: edit.ProposedHash}
	}
	return result
}

func agentCodeApplyResult(result ApplyFileEditResult) map[string]any {
	return map[string]any{"version": toolgateway.AgentCodeRegistryVersion,
		"edit_id": result.Edit.ID, "operation": result.Edit.Operation,
		"path": result.Edit.Path, "destination_path": result.Edit.DestinationPath,
		"status": result.Edit.Status, "original_sha256": result.Edit.OriginalHash,
		"proposed_sha256":             result.Edit.ProposedHash,
		"destination_proposed_sha256": result.Edit.DestinationProposedHash,
		"file_written":                result.FileWritten, "replayed": result.Replayed}
}

func agentCodeEditBindingMismatch(edit fileedit.Edit,
	scope toolgateway.AgentCodeExecutionScope,
) bool {
	return edit.SessionID != scope.SessionID || edit.WorkspaceID != scope.WorkspaceID
}

var scopeNow = func() time.Time { return time.Now().UTC() }

// Check stored edits as well as proposed paths: an edit ID is not an exemption
// from the current task's pinned exclusions.
func (e *AgentCodeToolExecutor) checkProjectPaths(ctx context.Context, policy workspace.AgentCodePathPolicy,
	name toolgateway.ToolName, payload json.RawMessage,
) error {
	if name != toolgateway.WorkspaceChangeTool && name != toolgateway.WorkspaceApplyTool && name != toolgateway.WorkspaceDeleteTool {
		return nil
	}
	if len(policy.ExcludePaths) == 0 {
		return nil
	}
	var input struct {
		Path            string `json:"path"`
		DestinationPath string `json:"destination_path"`
		EditID          string `json:"edit_id"`
		SourceEditID    string `json:"source_edit_id"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return err
	}
	for _, path := range []string{input.Path, input.DestinationPath} {
		if path != "" {
			if err := policy.Check(path); err != nil {
				return err
			}
		}
	}
	for _, id := range []string{input.EditID, input.SourceEditID} {
		if id == "" {
			continue
		}
		edit, err := e.store.GetFileEdit(ctx, id)
		if err != nil {
			return apperror.Normalize(err)
		}
		for _, path := range []string{edit.Path, edit.DestinationPath} {
			if path != "" {
				if err := policy.Check(path); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
