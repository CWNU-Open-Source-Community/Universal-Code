package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/workspace"
)

const BatchDeliveryWorkspaceToolVersion = "batch-delivery-workspace-tools.v1"

type BatchDeliveryToolAuthority struct {
	PlanID       string
	Ordinal      int
	Generation   int64
	OwnerToken   string
	OperationKey string
}

type BatchDeliveryListRequest struct {
	Authority BatchDeliveryToolAuthority
	Path      string
	Cursor    string
	Limit     int
}

type BatchDeliveryReadRequest struct {
	Authority BatchDeliveryToolAuthority
	Path      string
	StartLine int
	EndLine   int
}

type BatchDeliveryGlobRequest struct {
	Authority BatchDeliveryToolAuthority
	Pattern   string
	Cursor    string
	Limit     int
}

type BatchDeliveryGrepRequest struct {
	Authority     BatchDeliveryToolAuthority
	Query         string
	Pattern       string
	Cursor        string
	Limit         int
	CaseSensitive bool
}

type BatchDeliveryChangeRequest struct {
	Authority      BatchDeliveryToolAuthority
	Action         string
	Path           string
	ExpectedSHA256 string
	Content        string
	Replacements   []toolgateway.WorkspaceReplacement
}

type BatchDeliveryApplyRequest struct {
	Authority              BatchDeliveryToolAuthority
	EditID                 string
	ExpectedOriginalSHA256 string
	ExpectedProposedSHA256 string
}

type BatchDeliveryGitRequest struct {
	Authority BatchDeliveryToolAuthority
	Message   string
}

type BatchDeliveryToolResult[T any] struct {
	ProtocolVersion string
	Workspace       domain.BatchDeliveryWorkspace
	Value           T
	Replayed        bool
}

type batchDeliveryToolStore interface {
	BatchDeliveryStore
	fileedit.Store
	GetAgentNode(context.Context, string) (domain.AgentNode, error)
	GetApprovalByProposal(context.Context, string) (approval.Record, error)
	DecideApproval(context.Context, approval.DecisionRequest) (approval.DecisionResult, error)
}

type batchDeliveryToolBinding struct {
	store        batchDeliveryToolStore
	plan         domain.BatchDeliveryPlan
	workspace    domain.BatchDeliveryWorkspace
	task         domain.BatchDeliveryTaskSpec
	tokenDigest  string
	operationKey string
	pathPolicy   workspace.AgentCodePathPolicy
}

func (s *BatchDeliveryService) bindBatchDeliveryTool(ctx context.Context,
	authority BatchDeliveryToolAuthority,
) (batchDeliveryToolBinding, error) {
	var binding batchDeliveryToolBinding
	if s == nil || s.store == nil {
		return binding, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery workspace tool store is unavailable")
	}
	store, ok := s.store.(batchDeliveryToolStore)
	if !ok {
		return binding, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery workspace tool store is unavailable")
	}
	operationKey, err := domain.NormalizeAgentOperationKey(
		strings.TrimSpace(authority.OperationKey))
	if err != nil {
		return binding, apperror.New(apperror.CodeInvalidArgument,
			"batch delivery tool operation key is invalid")
	}
	tokenDigest, err := batchDeliveryOwnerTokenDigest(authority.OwnerToken)
	if err != nil {
		return binding, apperror.Wrap(apperror.CodePolicyDenied,
			"batch delivery owner token was rejected", err)
	}
	plan, child, task, err := s.loadBatchTask(ctx, authority.PlanID, authority.Ordinal)
	if err != nil {
		return binding, err
	}
	if child.Generation != authority.Generation || child.OwnerTokenDigest != tokenDigest {
		return binding, apperror.New(apperror.CodePolicyDenied,
			"batch delivery tool authority is stale or does not match")
	}
	if child.Status != domain.BatchWorkspaceAcknowledged &&
		child.Status != domain.BatchWorkspaceWorking &&
		child.Status != domain.BatchWorkspaceQuestion {
		return binding, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery tool requires an acknowledged active child")
	}
	if !s.now().UTC().Before(child.LeaseExpiresAt) {
		return binding, apperror.New(apperror.CodeDeadlineExceeded,
			"batch delivery child tool lease expired")
	}
	if plan.Status.Terminal() || plan.Status == domain.BatchDeliveryMerging {
		return binding, apperror.New(apperror.CodeFailedPrecondition,
			"batch delivery plan no longer accepts child tools")
	}
	if err := child.ToolProfile.Validate(); err != nil ||
		child.ToolProfileFingerprint != child.ToolProfile.Fingerprint() {
		return binding, apperror.New(apperror.CodePolicyDenied,
			"batch delivery child tool profile changed")
	}
	if err := s.requireBatchDependencies(ctx, plan, task); err != nil {
		return binding, err
	}
	run, err := s.store.GetRun(ctx, plan.RunID)
	if err != nil {
		return binding, err
	}
	pathPolicy, err := projectconfig.PathPolicy(run.Config)
	if err != nil {
		return binding, err
	}
	return batchDeliveryToolBinding{pathPolicy: pathPolicy, store: store, plan: plan, workspace: child,
		task: task, tokenDigest: tokenDigest, operationKey: operationKey}, nil
}

func (s *BatchDeliveryService) BatchList(ctx context.Context,
	request BatchDeliveryListRequest,
) (BatchDeliveryToolResult[workspace.AgentCodeList], error) {
	var result BatchDeliveryToolResult[workspace.AgentCodeList]
	binding, err := s.bindBatchDeliveryTool(ctx, request.Authority)
	if err != nil {
		return result, err
	}
	if !binding.workspace.ToolProfile.WorkspaceList ||
		!batchDeliveryListScopeAllows(binding.task.OwnershipHints, request.Path) {
		return result, apperror.New(apperror.CodePolicyDenied,
			"batch child cannot list outside its owned directory scope")
	}
	value, err := workspace.AgentCodeListDirectory(binding.workspace.WorktreeRoot,
		batchChildWorkspaceID(binding), request.Path, request.Cursor, request.Limit, false, binding.pathPolicy)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	value.Items = slices.DeleteFunc(value.Items, func(item workspace.AgentCodeEntry) bool {
		return !domain.BatchOwnershipAllows(binding.task.OwnershipHints, item.Path) &&
			!batchDeliveryListScopeAllows(binding.task.OwnershipHints, item.Path)
	})
	updated, replayed, err := s.auditBatchDeliveryTool(ctx, binding, "workspace_list",
		[]string{"root:" + value.RootFingerprint})
	return BatchDeliveryToolResult[workspace.AgentCodeList]{ProtocolVersion: BatchDeliveryWorkspaceToolVersion,
		Workspace: updated, Value: value, Replayed: replayed}, err
}

func (s *BatchDeliveryService) BatchRead(ctx context.Context,
	request BatchDeliveryReadRequest,
) (BatchDeliveryToolResult[workspace.AgentCodeRead], error) {
	var result BatchDeliveryToolResult[workspace.AgentCodeRead]
	binding, err := s.bindBatchDeliveryTool(ctx, request.Authority)
	if err != nil {
		return result, err
	}
	if !binding.workspace.ToolProfile.WorkspaceRead ||
		!domain.BatchOwnershipAllows(binding.task.OwnershipHints, request.Path) {
		return result, apperror.New(apperror.CodePolicyDenied,
			"batch child cannot read outside its owned scope")
	}
	value, err := workspace.AgentCodeReadFile(binding.workspace.WorktreeRoot,
		batchChildWorkspaceID(binding), request.Path, request.StartLine, request.EndLine, false, binding.pathPolicy)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	updated, replayed, err := s.auditBatchDeliveryTool(ctx, binding, "workspace_read",
		[]string{"sha256:" + value.ContentSHA256})
	return BatchDeliveryToolResult[workspace.AgentCodeRead]{ProtocolVersion: BatchDeliveryWorkspaceToolVersion,
		Workspace: updated, Value: value, Replayed: replayed}, err
}

func (s *BatchDeliveryService) BatchGlob(ctx context.Context,
	request BatchDeliveryGlobRequest,
) (BatchDeliveryToolResult[workspace.AgentCodeGlob], error) {
	var result BatchDeliveryToolResult[workspace.AgentCodeGlob]
	binding, err := s.bindBatchDeliveryTool(ctx, request.Authority)
	if err != nil {
		return result, err
	}
	if !binding.workspace.ToolProfile.WorkspaceSearch {
		return result, apperror.New(apperror.CodePolicyDenied,
			"batch child search capability is unavailable")
	}
	value, err := workspace.AgentCodeScopedGlobFiles(binding.workspace.WorktreeRoot,
		batchChildWorkspaceID(binding), request.Pattern, request.Cursor, request.Limit,
		batchDeliverySearchScopes(binding.task.OwnershipHints), binding.pathPolicy)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	updated, replayed, err := s.auditBatchDeliveryTool(ctx, binding, "workspace_glob",
		[]string{fmt.Sprintf("matches:%d", len(value.Paths))})
	return BatchDeliveryToolResult[workspace.AgentCodeGlob]{ProtocolVersion: BatchDeliveryWorkspaceToolVersion,
		Workspace: updated, Value: value, Replayed: replayed}, err
}

func (s *BatchDeliveryService) BatchGrep(ctx context.Context,
	request BatchDeliveryGrepRequest,
) (BatchDeliveryToolResult[workspace.AgentCodeGrep], error) {
	var result BatchDeliveryToolResult[workspace.AgentCodeGrep]
	binding, err := s.bindBatchDeliveryTool(ctx, request.Authority)
	if err != nil {
		return result, err
	}
	if !binding.workspace.ToolProfile.WorkspaceSearch {
		return result, apperror.New(apperror.CodePolicyDenied,
			"batch child search capability is unavailable")
	}
	value, err := workspace.AgentCodeScopedGrepFiles(binding.workspace.WorktreeRoot,
		batchChildWorkspaceID(binding), request.Query, request.Pattern, request.Cursor,
		request.Limit, request.CaseSensitive,
		batchDeliverySearchScopes(binding.task.OwnershipHints), binding.pathPolicy)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	updated, replayed, err := s.auditBatchDeliveryTool(ctx, binding, "workspace_grep",
		[]string{fmt.Sprintf("matches:%d", len(value.Matches))})
	return BatchDeliveryToolResult[workspace.AgentCodeGrep]{ProtocolVersion: BatchDeliveryWorkspaceToolVersion,
		Workspace: updated, Value: value, Replayed: replayed}, err
}

func (s *BatchDeliveryService) BatchProposeChange(ctx context.Context,
	request BatchDeliveryChangeRequest,
) (BatchDeliveryToolResult[fileedit.Preview], error) {
	var result BatchDeliveryToolResult[fileedit.Preview]
	binding, err := s.bindBatchDeliveryTool(ctx, request.Authority)
	if err != nil {
		return result, err
	}
	if err := binding.pathPolicy.Check(request.Path); err != nil {
		return result, err
	}
	if !binding.workspace.ToolProfile.WorkspaceChange ||
		!domain.BatchOwnershipAllows(binding.task.OwnershipHints, request.Path) {
		return result, apperror.New(apperror.CodePolicyDenied,
			"batch child cannot change outside its owned scope")
	}
	if redact.String(request.Content) != request.Content {
		return result, apperror.New(apperror.CodePolicyDenied,
			"batch child change contains secret-like material")
	}
	for _, replacement := range request.Replacements {
		if redact.String(replacement.OldText) != replacement.OldText ||
			redact.String(replacement.NewText) != replacement.NewText {
			return result, apperror.New(apperror.CodePolicyDenied,
				"batch child patch contains secret-like material")
		}
	}
	agent, err := binding.store.GetAgentNode(ctx, binding.workspace.AgentID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	proposal := fileedit.Proposal{ID: batchDeliveryEditID(binding),
		SessionID: agent.SessionID, WorkspaceID: binding.plan.WorkspaceID,
		WorkspaceRoot: binding.workspace.WorktreeRoot, Path: request.Path,
		ExpectedOriginalHash: request.ExpectedSHA256}
	switch strings.TrimSpace(request.Action) {
	case "create":
		if request.ExpectedSHA256 != "missing" || len(request.Replacements) != 0 {
			return result, apperror.New(apperror.CodeInvalidArgument,
				"batch child create request is invalid")
		}
		proposal.Operation, proposal.ProposedText = fileedit.OperationCreate, request.Content
	case "patch":
		if request.Content != "" || request.ExpectedSHA256 == "missing" ||
			len(request.Replacements) == 0 || len(request.Replacements) > 64 {
			return result, apperror.New(apperror.CodeInvalidArgument,
				"batch child patch request is invalid")
		}
		current, observed, readErr := workspace.AgentCodeReadMutationSource(
			binding.workspace.WorktreeRoot, request.Path, binding.pathPolicy)
		if readErr != nil {
			return result, apperror.Normalize(readErr)
		}
		if observed != request.ExpectedSHA256 {
			return result, apperror.New(apperror.CodeConflict,
				"batch child patch source changed after read")
		}
		for _, replacement := range request.Replacements {
			if replacement.OldText == "" || replacement.ExpectedOccurrences <= 0 ||
				strings.Count(current, replacement.OldText) != replacement.ExpectedOccurrences {
				return result, apperror.New(apperror.CodeConflict,
					"batch child patch occurrence binding changed")
			}
			current = strings.ReplaceAll(current, replacement.OldText, replacement.NewText)
		}
		proposal.Operation, proposal.ProposedText = fileedit.OperationReplace, current
	default:
		return result, apperror.New(apperror.CodeInvalidArgument,
			"batch child supports only patch or create changes")
	}
	manager := fileedit.NewManager(binding.store)
	edit, replayed, err := proposeBatchDeliveryEdit(ctx, binding.store, manager, proposal)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	updated, mailboxReplayed, err := s.auditBatchDeliveryTool(ctx, binding,
		"workspace_change", []string{"edit:" + edit.ID})
	return BatchDeliveryToolResult[fileedit.Preview]{ProtocolVersion: BatchDeliveryWorkspaceToolVersion,
		Workspace: updated, Value: batchDeliveryEditPreview(edit),
		Replayed: replayed || mailboxReplayed}, err
}

func (s *BatchDeliveryService) BatchApplyChange(ctx context.Context,
	request BatchDeliveryApplyRequest,
) (BatchDeliveryToolResult[fileedit.Preview], error) {
	var result BatchDeliveryToolResult[fileedit.Preview]
	binding, err := s.bindBatchDeliveryTool(ctx, request.Authority)
	if err != nil {
		return result, err
	}
	if !binding.workspace.ToolProfile.WorkspaceApply {
		return result, apperror.New(apperror.CodePolicyDenied,
			"batch child apply capability is unavailable")
	}
	edit, err := binding.store.GetFileEdit(ctx, strings.TrimSpace(request.EditID))
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if err := binding.pathPolicy.Check(edit.Path); err != nil {
		return result, err
	}
	agent, err := binding.store.GetAgentNode(ctx, binding.workspace.AgentID)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	if edit.SessionID != agent.SessionID || edit.WorkspaceID != binding.plan.WorkspaceID ||
		!domain.BatchOwnershipAllows(binding.task.OwnershipHints, edit.Path) ||
		(edit.Operation != fileedit.OperationReplace && edit.Operation != fileedit.OperationCreate) ||
		edit.OriginalHash != request.ExpectedOriginalSHA256 ||
		edit.ProposedHash != request.ExpectedProposedSHA256 {
		return result, apperror.New(apperror.CodeConflict,
			"batch child apply binding does not match its owned proposal")
	}
	alreadyApplied := edit.Status == fileedit.StatusApplied
	manager := fileedit.NewManager(binding.store)
	if edit.Status == fileedit.StatusProposed {
		record, approvalErr := binding.store.GetApprovalByProposal(ctx, edit.ID)
		if approvalErr != nil || record.ProposalID != edit.ID ||
			record.SessionID != edit.SessionID || record.WorkspaceID != edit.WorkspaceID ||
			record.ToolName != fileedit.ApprovalToolName(edit) ||
			record.ActionClass != "workspace_write" || record.Mode != "per_call" {
			return result, apperror.New(apperror.CodeFailedPrecondition,
				"batch child edit approval binding is invalid")
		}
		if record.Status == approval.StatusPending {
			decision, decisionErr := binding.store.DecideApproval(ctx,
				approval.DecisionRequest{ProposalID: edit.ID,
					IdempotencyKey: approval.ReviewIdempotencyKey(
						fileedit.ApprovalToolName(edit), edit.ID, approval.ActionApprove),
					Action:     approval.ActionApprove,
					ReviewedBy: "batch_delivery_control"})
			if decisionErr != nil {
				return result, apperror.Normalize(decisionErr)
			}
			if decision.Approval.Status != approval.StatusApproved {
				return result, apperror.New(apperror.CodeInternal,
					"batch child edit approval did not commit")
			}
		} else if record.Status != approval.StatusApproved {
			return result, apperror.New(apperror.CodeConflict,
				"batch child edit approval was denied")
		}
		edit, err = manager.ApproveIntent(ctx, edit.ID)
		if err != nil {
			return result, apperror.Normalize(err)
		}
	}
	edit, err = manager.Approve(ctx, edit.ID,
		binding.workspace.WorktreeRoot)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	updated, mailboxReplayed, err := s.auditBatchDeliveryTool(ctx, binding,
		"workspace_apply", []string{"edit:" + edit.ID, "sha256:" + edit.ProposedHash})
	return BatchDeliveryToolResult[fileedit.Preview]{ProtocolVersion: BatchDeliveryWorkspaceToolVersion,
		Workspace: updated, Value: batchDeliveryEditPreview(edit),
		Replayed: alreadyApplied || mailboxReplayed}, err
}

func (s *BatchDeliveryService) BatchGitStatus(ctx context.Context,
	request BatchDeliveryGitRequest,
) (BatchDeliveryToolResult[repository.BatchWorkspaceChanges], error) {
	return s.batchGitInspection(ctx, request, "git_status")
}

func (s *BatchDeliveryService) BatchGitDiff(ctx context.Context,
	request BatchDeliveryGitRequest,
) (BatchDeliveryToolResult[repository.BatchWorkspaceChanges], error) {
	return s.batchGitInspection(ctx, request, "git_diff")
}

func (s *BatchDeliveryService) batchGitInspection(ctx context.Context,
	request BatchDeliveryGitRequest, action string,
) (BatchDeliveryToolResult[repository.BatchWorkspaceChanges], error) {
	var result BatchDeliveryToolResult[repository.BatchWorkspaceChanges]
	binding, err := s.bindBatchDeliveryTool(ctx, request.Authority)
	if err != nil {
		return result, err
	}
	if action == "git_status" && !binding.workspace.ToolProfile.GitStatus ||
		action == "git_diff" && !binding.workspace.ToolProfile.GitDiff {
		return result, apperror.New(apperror.CodePolicyDenied,
			"batch child Git inspection capability is unavailable")
	}
	value, err := repository.InspectBatchWorkspaceChanges(ctx,
		binding.workspace.WorktreeRoot, binding.workspace.Branch,
		binding.workspace.BaseCommit, binding.plan.Spec.Contract.MaxChangedFiles,
		repository.MaxBatchWorkspaceDiffPreviewBytes)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	updated, replayed, err := s.auditBatchDeliveryTool(ctx, binding, action,
		[]string{"sha256:" + value.DiffSHA256})
	return BatchDeliveryToolResult[repository.BatchWorkspaceChanges]{ProtocolVersion: BatchDeliveryWorkspaceToolVersion,
		Workspace: updated, Value: value, Replayed: replayed}, err
}

func (s *BatchDeliveryService) BatchGitCommit(ctx context.Context,
	request BatchDeliveryGitRequest,
) (BatchDeliveryToolResult[repository.BatchWorkspaceChanges], error) {
	var result BatchDeliveryToolResult[repository.BatchWorkspaceChanges]
	binding, err := s.bindBatchDeliveryTool(ctx, request.Authority)
	if err != nil {
		return result, err
	}
	if !binding.workspace.ToolProfile.GitCommit {
		return result, apperror.New(apperror.CodePolicyDenied,
			"batch child Git commit capability is unavailable")
	}
	messageText := strings.TrimSpace(request.Message)
	if messageText == "" || len([]rune(messageText)) > 256 ||
		strings.ContainsAny(messageText, "\r\n\x00") {
		return result, apperror.New(apperror.CodeInvalidArgument,
			"batch child Git commit message is invalid")
	}
	messageHash := sha256.Sum256([]byte(messageText))
	messageDigest := hex.EncodeToString(messageHash[:])
	auditDigest := runmutation.OperationKeyDigest(domain.BatchDeliveryMailboxVersion,
		binding.plan.ID, binding.operationKey+"-git_commit")
	if message, exists, loadErr := binding.store.GetBatchDeliveryMailboxByOperationDigest(
		ctx, auditDigest); loadErr != nil {
		return result, apperror.Normalize(loadErr)
	} else if exists {
		head := batchEvidenceValue(message.EvidenceRefs, "commit:")
		if message.PlanID != binding.plan.ID || message.Ordinal != binding.workspace.Ordinal ||
			message.Generation != binding.workspace.Generation ||
			message.Actor != binding.workspace.AgentID ||
			message.Summary != "narrowed child tool completed: git_commit" || head == "" ||
			batchEvidenceValue(message.EvidenceRefs, "message-sha256:") != messageDigest {
			return result, apperror.New(apperror.CodeConflict,
				"batch child Git commit replay binding changed")
		}
		current, inspectErr := repository.InspectBatchWorkspaceChanges(ctx,
			binding.workspace.WorktreeRoot, binding.workspace.Branch,
			binding.workspace.BaseCommit, binding.plan.Spec.Contract.MaxChangedFiles,
			repository.MaxBatchWorkspaceDiffPreviewBytes)
		if inspectErr != nil || !current.Clean || current.HeadCommit != head {
			return result, apperror.New(apperror.CodeConflict,
				"batch child Git commit replay no longer matches the clean worktree")
		}
		binding.workspace.HeadCommit = head
		return BatchDeliveryToolResult[repository.BatchWorkspaceChanges]{
			ProtocolVersion: BatchDeliveryWorkspaceToolVersion,
			Workspace:       binding.workspace, Value: current, Replayed: true}, nil
	}
	intentDigest := runmutation.OperationKeyDigest(domain.BatchDeliveryMailboxVersion,
		binding.plan.ID, binding.operationKey+"-git_commit_intent")
	intent, hasIntent, err := binding.store.GetBatchDeliveryMailboxByOperationDigest(
		ctx, intentDigest)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	priorHead := ""
	if hasIntent {
		priorHead = batchEvidenceValue(intent.EvidenceRefs, "prior-head:")
		if intent.PlanID != binding.plan.ID || intent.Ordinal != binding.workspace.Ordinal ||
			intent.Generation != binding.workspace.Generation ||
			intent.Actor != binding.workspace.AgentID ||
			intent.Summary != "narrowed child tool intent: git_commit" ||
			batchEvidenceValue(intent.EvidenceRefs, "message-sha256:") != messageDigest ||
			priorHead == "" {
			return result, apperror.New(apperror.CodeConflict,
				"batch child Git commit intent binding changed")
		}
		current, inspectErr := repository.InspectBatchWorkspaceChanges(ctx,
			binding.workspace.WorktreeRoot, binding.workspace.Branch,
			binding.workspace.BaseCommit, binding.plan.Spec.Contract.MaxChangedFiles,
			repository.MaxBatchWorkspaceDiffPreviewBytes)
		if inspectErr != nil {
			return result, apperror.Normalize(inspectErr)
		}
		if !current.Clean && current.HeadCommit != priorHead {
			return result, apperror.New(apperror.CodeConflict,
				"batch child Git commit advanced and then became dirty during recovery")
		}
		if current.Clean && current.HeadCommit != priorHead {
			inspection, verifyErr := repository.VerifyBatchWorkspaceCommittedIntent(ctx,
				binding.workspace.WorktreeRoot, binding.workspace.Branch,
				binding.workspace.BaseCommit, priorHead, messageText,
				binding.plan.Spec.Contract.MaxChangedFiles,
				binding.plan.Spec.Contract.MaxDiffBytes, func(path string) bool {
					return domain.BatchOwnershipAllows(binding.task.OwnershipHints, path)
				})
			if verifyErr != nil {
				return result, apperror.Wrap(apperror.CodeConflict,
					"batch child Git commit recovery was rejected", verifyErr)
			}
			if binding.workspace.HeadCommit != inspection.HeadCommit {
				if err := s.store.SetBatchDeliveryWorkspaceStatus(ctx, binding.plan.ID,
					binding.workspace.Ordinal, binding.workspace.Generation,
					binding.workspace.Status, binding.workspace.Status,
					inspection.HeadCommit, s.now().UTC()); err != nil {
					return result, apperror.Normalize(err)
				}
			}
			binding.workspace.HeadCommit = inspection.HeadCommit
			updated, _, auditErr := s.auditBatchDeliveryTool(ctx, binding, "git_commit",
				[]string{"commit:" + inspection.HeadCommit,
					"sha256:" + inspection.DiffSHA256,
					"message-sha256:" + messageDigest})
			value := repository.BatchWorkspaceChanges{Branch: inspection.Branch,
				HeadCommit: inspection.HeadCommit, ChangedFiles: inspection.ChangedFiles,
				DiffSHA256: inspection.DiffSHA256, DiffBytes: inspection.DiffBytes, Clean: true}
			return BatchDeliveryToolResult[repository.BatchWorkspaceChanges]{
				ProtocolVersion: BatchDeliveryWorkspaceToolVersion,
				Workspace:       updated, Value: value, Replayed: true}, auditErr
		}
	}
	if !hasIntent {
		before, inspectErr := repository.InspectBatchWorkspaceChanges(ctx,
			binding.workspace.WorktreeRoot, binding.workspace.Branch,
			binding.workspace.BaseCommit, binding.plan.Spec.Contract.MaxChangedFiles,
			repository.MaxBatchWorkspaceDiffPreviewBytes)
		if inspectErr != nil {
			return result, apperror.Normalize(inspectErr)
		}
		if before.Clean || len(before.ChangedFiles) == 0 || len(before.DeletedFiles) != 0 {
			return result, apperror.New(apperror.CodeFailedPrecondition,
				"batch child Git commit requires owned non-deleting changes")
		}
		for _, changed := range before.ChangedFiles {
			if !domain.BatchOwnershipAllows(binding.task.OwnershipHints, changed) {
				return result, apperror.New(apperror.CodePolicyDenied,
					"batch child Git commit includes an unowned path")
			}
		}
		priorHead = before.HeadCommit
		intent = batchDeliveryMessage(binding.plan.ID, binding.workspace.Ordinal,
			binding.workspace.Generation, domain.BatchMailboxEvidence,
			binding.workspace.AgentID, "narrowed child tool intent: git_commit",
			[]string{"message-sha256:" + messageDigest, "prior-head:" + priorHead},
			binding.operationKey+"-git_commit_intent", s.now().UTC())
		if _, _, _, appendErr := binding.store.AppendBatchDeliveryMailbox(ctx, intent,
			binding.tokenDigest, binding.workspace.LeaseExpiresAt); appendErr != nil {
			return result, apperror.Normalize(appendErr)
		}
	}
	head, before, err := repository.CommitBatchWorkspace(ctx,
		binding.workspace.WorktreeRoot, binding.workspace.Branch,
		binding.workspace.BaseCommit, messageText,
		binding.plan.Spec.Contract.MaxChangedFiles, func(path string) bool {
			return domain.BatchOwnershipAllows(binding.task.OwnershipHints, path)
		})
	if err != nil {
		return result, apperror.Wrap(apperror.CodeFailedPrecondition,
			"batch child Git commit was rejected", err)
	}
	if err := s.store.SetBatchDeliveryWorkspaceStatus(ctx, binding.plan.ID,
		binding.workspace.Ordinal, binding.workspace.Generation, binding.workspace.Status,
		binding.workspace.Status, head, s.now().UTC()); err != nil {
		return result, apperror.Normalize(err)
	}
	binding.workspace.HeadCommit = head
	inspection, inspectErr := repository.InspectBatchDelivery(ctx,
		binding.workspace.WorktreeRoot, binding.workspace.Branch,
		binding.workspace.BaseCommit, binding.plan.Spec.Contract.MaxChangedFiles,
		binding.plan.Spec.Contract.MaxDiffBytes)
	if inspectErr != nil || inspection.HeadCommit != head {
		return result, apperror.Wrap(apperror.CodeInternal,
			"batch child Git commit readback failed", inspectErr)
	}
	updated, replayed, err := s.auditBatchDeliveryTool(ctx, binding, "git_commit",
		[]string{"commit:" + head, "sha256:" + inspection.DiffSHA256,
			"message-sha256:" + messageDigest})
	before.HeadCommit, before.Clean = head, true
	return BatchDeliveryToolResult[repository.BatchWorkspaceChanges]{ProtocolVersion: BatchDeliveryWorkspaceToolVersion,
		Workspace: updated, Value: before, Replayed: replayed}, err
}

func (s *BatchDeliveryService) auditBatchDeliveryTool(ctx context.Context,
	binding batchDeliveryToolBinding, action string, evidence []string,
) (domain.BatchDeliveryWorkspace, bool, error) {
	now := s.now().UTC()
	message := batchDeliveryMessage(binding.plan.ID, binding.workspace.Ordinal,
		binding.workspace.Generation, domain.BatchMailboxEvidence,
		binding.workspace.AgentID, "narrowed child tool completed: "+action, evidence,
		binding.operationKey+"-"+action, now)
	updated, _, replayed, err := binding.store.AppendBatchDeliveryMailbox(ctx, message,
		binding.tokenDigest, binding.workspace.LeaseExpiresAt)
	return updated, replayed, apperror.Normalize(err)
}

func batchDeliverySearchScopes(hints []domain.BatchDeliveryOwnershipHint) []workspace.AgentCodeScopePath {
	out := make([]workspace.AgentCodeScopePath, len(hints))
	for index, hint := range hints {
		out[index] = workspace.AgentCodeScopePath{Path: hint.Path,
			Directory: hint.Kind == domain.BatchDeliveryOwnershipDirectory}
	}
	return out
}

func batchDeliveryListScopeAllows(hints []domain.BatchDeliveryOwnershipHint,
	requested string,
) bool {
	requested = strings.TrimSuffix(strings.ReplaceAll(strings.TrimSpace(requested), "\\", "/"), "/")
	for _, hint := range hints {
		if hint.Kind == domain.BatchDeliveryOwnershipDirectory &&
			(requested == hint.Path || strings.HasPrefix(requested, hint.Path+"/")) {
			return true
		}
	}
	return false
}

func batchChildWorkspaceID(binding batchDeliveryToolBinding) string {
	return fmt.Sprintf("%s-task-%d-g%d", binding.plan.WorkspaceID,
		binding.workspace.Ordinal, binding.workspace.Generation)
}

func batchDeliveryEditID(binding batchDeliveryToolBinding) string {
	digest := sha256.Sum256([]byte("batch-delivery-edit.v1\x00" + binding.plan.ID + "\x00" +
		binding.operationKey))
	return "edit-batch-" + hex.EncodeToString(digest[:16])
}

func proposeBatchDeliveryEdit(ctx context.Context, store batchDeliveryToolStore,
	manager *fileedit.Manager, proposal fileedit.Proposal,
) (fileedit.Edit, bool, error) {
	if existing, err := store.GetFileEdit(ctx, proposal.ID); err == nil {
		operation, normalizeErr := fileedit.NormalizeOperation(proposal.Operation)
		if normalizeErr != nil || existing.ID != proposal.ID ||
			existing.SessionID != proposal.SessionID || existing.WorkspaceID != proposal.WorkspaceID ||
			existing.Path != proposal.Path || existing.Operation != operation ||
			existing.OriginalHash != proposal.ExpectedOriginalHash ||
			(existing.Operation != fileedit.OperationDelete &&
				existing.ProposedHash != fileedit.HashText(proposal.ProposedText)) {
			return fileedit.Edit{}, false, apperror.New(apperror.CodeConflict,
				"batch child tool operation key was reused for a different edit")
		}
		return existing, true, nil
	} else if apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeNotFound {
		return fileedit.Edit{}, false, err
	}
	edit, err := manager.Propose(ctx, proposal)
	return edit, false, err
}

func batchDeliveryEditPreview(edit fileedit.Edit) fileedit.Preview {
	return fileedit.Preview{ID: edit.ID, SessionID: edit.SessionID,
		WorkspaceID: edit.WorkspaceID, Path: edit.Path, Operation: edit.Operation,
		DestinationPath: edit.DestinationPath, Status: edit.Status, Diff: edit.Diff,
		OriginalHash: edit.OriginalHash, ProposedHash: edit.ProposedHash,
		DestinationOriginalHash: edit.DestinationOriginalHash,
		DestinationProposedHash: edit.DestinationProposedHash, Reason: edit.Reason,
		SecretsRedacted: edit.SecretsRedacted, CreatedAt: edit.CreatedAt,
		UpdatedAt: edit.UpdatedAt}
}

func batchEvidenceValue(values []string, prefix string) string {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) && len(value) > len(prefix) {
			return strings.TrimPrefix(value, prefix)
		}
	}
	return ""
}
