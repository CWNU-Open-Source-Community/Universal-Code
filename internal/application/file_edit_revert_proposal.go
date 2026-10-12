package application

import (
	"context"
	"fmt"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/tools"
	"cyberagent-workbench/internal/workspace"
)

type CreateFileEditRevertProposalRequest struct {
	Version      string
	RunID        string
	SourceEditID string
	OperationKey string
	// Explicit source coordinates are used by the current Run's model tool.
	// Older same-Run HTTP callers omit all three fields.
	SourceRunID    string
	Path           string
	ExpectedSHA256 string
}

type fileEditRevertProposalStore interface {
	CreateFileEditIfAbsent(context.Context, fileedit.Edit) (fileedit.Edit, bool, error)
	GetApprovalByProposal(context.Context, string) (approval.Record, error)
}

type preparedFileEditRevertWriter func(context.Context, fileedit.Edit) (fileedit.Edit, bool, error)

// ProposeRevert derives a new pending edit from an exact applied source. It
// changes neither the source receipt nor the workspace; ordinary review/apply
// remain separate, freshly authorized operations.
func (s *FileEditProposalService) ProposeRevert(ctx context.Context,
	request CreateFileEditRevertProposalRequest,
) (CreateFileEditProposalResult, error) {
	if s == nil || s.store == nil {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"file edit revert proposal dependencies are required")
	}
	store, ok := s.store.(fileEditRevertProposalStore)
	if !ok {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"atomic file edit proposal storage is required")
	}
	return s.proposeRevertWithWriter(ctx, request, store.CreateFileEditIfAbsent)
}

// proposeRevertWithWriter keeps source validation and inverse derivation in the
// proposal service while allowing the trusted Agent Code boundary to persist a
// freshly prepared non-delete inverse with its current Full Access authority.
// The writer is invocation-local so authority can never leak between callers.
func (s *FileEditProposalService) proposeRevertWithWriter(ctx context.Context,
	request CreateFileEditRevertProposalRequest, writer preparedFileEditRevertWriter,
) (CreateFileEditProposalResult, error) {
	if s == nil || s.store == nil || s.manager == nil || s.checker == nil {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"file edit revert proposal dependencies are required")
	}
	store, ok := s.store.(fileEditRevertProposalStore)
	if !ok {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"atomic file edit proposal storage is required")
	}
	if writer == nil {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"file edit revert proposal writer is required")
	}
	if request.Version != FileEditProposalProtocolVersion ||
		!validControlIdentity(request.RunID) || !validControlIdentity(request.SourceEditID) {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeInvalidArgument,
			"file edit revert proposal request is invalid")
	}
	if request.SourceRunID != "" || request.Path != "" || request.ExpectedSHA256 != "" {
		if !validControlIdentity(request.SourceRunID) || !validProposalSourcePath(request.Path) ||
			(request.ExpectedSHA256 != "missing" && !validSHA256Digest(request.ExpectedSHA256)) {
			return CreateFileEditProposalResult{}, apperror.New(apperror.CodeInvalidArgument,
				"file edit revert source coordinates are invalid")
		}
	}
	sourceRunID := request.SourceRunID
	if sourceRunID == "" {
		sourceRunID = request.RunID
	}
	if _, err := domain.NormalizeAgentOperationKey(request.OperationKey); err != nil ||
		strings.ContainsAny(request.OperationKey, " \t\r\n") {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeInvalidArgument,
			"file edit revert proposal operation key is invalid")
	}
	run, err := s.store.GetRun(ctx, request.RunID)
	if err != nil {
		return CreateFileEditProposalResult{}, apperror.Normalize(err)
	}
	mission, err := s.store.GetMission(ctx, run.MissionID)
	if err != nil {
		return CreateFileEditProposalResult{}, apperror.Normalize(err)
	}
	sourceRun := run
	sourceMission := mission
	if sourceRunID != run.ID {
		sourceRun, err = s.store.GetRun(ctx, sourceRunID)
		if err != nil {
			return CreateFileEditProposalResult{}, apperror.Normalize(err)
		}
		sourceMission, err = s.store.GetMission(ctx, sourceRun.MissionID)
		if err != nil {
			return CreateFileEditProposalResult{}, apperror.Normalize(err)
		}
	}
	source, err := s.store.GetFileEdit(ctx, request.SourceEditID)
	if err != nil {
		return CreateFileEditProposalResult{}, apperror.Normalize(err)
	}
	if err := checkRunProjectPaths(run, source.Path, source.DestinationPath); err != nil {
		return CreateFileEditProposalResult{}, err
	}
	record, err := store.GetApprovalByProposal(ctx, source.ID)
	if err != nil {
		return CreateFileEditProposalResult{}, apperror.Normalize(err)
	}
	belongs, err := fileEditWorkspaceBelongsToRun(ctx, s.store, sourceRun, sourceMission,
		source.SessionID, source.WorkspaceID)
	if err != nil {
		return CreateFileEditProposalResult{}, apperror.Normalize(err)
	}
	if !belongs || source.ID != request.SourceEditID || record.RunID != sourceRun.ID ||
		record.SessionID != sourceRun.SessionID || record.WorkspaceID != source.WorkspaceID ||
		record.ProposalID != source.ID || record.ToolName != fileedit.ApprovalToolName(source) ||
		record.ActionClass != "workspace_write" || record.Status != approval.StatusApproved {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"source file edit does not belong to this Run and Workspace")
	}
	if request.SourceRunID != "" && (source.Path != request.Path || source.ProposedHash != request.ExpectedSHA256) {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeConflict,
			"file edit revert expectations do not match the source receipt")
	}
	if sourceRun.ID != run.ID {
		if err := s.requireRevertContinuation(ctx, sourceRun, run, source.WorkspaceID); err != nil {
			return CreateFileEditProposalResult{}, err
		}
	}
	// The key is scoped to this Run and source receipt, not to mutable file
	// contents. A changed source can never evade an existing attempt's binding.
	identityDigest := runmutation.Fingerprint("file_edit_revert_proposal.v1",
		request.RunID, request.SourceEditID, request.OperationKey)
	editID := "edit-revert-" + identityDigest[:48]
	expected, err := inverseFileEdit(source, editID)
	if err != nil {
		return CreateFileEditProposalResult{}, err
	}
	// The source proves historical content only. The new edit and its current
	// authorization belong to this execution Session, never to the old grant.
	expected.SessionID = run.SessionID
	if existing, getErr := s.store.GetFileEdit(ctx, editID); getErr == nil {
		if !fileedit.SameProposalContent(existing, expected) {
			return CreateFileEditProposalResult{}, apperror.New(apperror.CodeConflict,
				"file edit revert proposal no longer matches its source")
		}
		return CreateFileEditProposalResult{Edit: existing, Replayed: true, RunTerminal: run.Terminal()}, nil
	} else if apperror.CodeOf(apperror.Normalize(getErr)) != apperror.CodeNotFound {
		return CreateFileEditProposalResult{}, apperror.Normalize(getErr)
	}
	binding, err := s.loadBinding(ctx, request.RunID)
	if err != nil {
		return CreateFileEditProposalResult{}, err
	}
	if binding.session.ID != expected.SessionID || binding.workspace.ID != expected.WorkspaceID {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeConflict,
			"file edit revert proposal binding changed")
	}
	if request.SourceRunID != "" {
		if _, _, err := workspace.AgentCodeResolveWritePath(binding.workspace.RootPath,
			expected.Path, expected.OriginalHash == "missing"); err != nil {
			return CreateFileEditProposalResult{}, apperror.Normalize(err)
		}
	}
	decision := s.checker.CheckToolCall(tools.Call{Name: fileedit.ApprovalToolName(expected),
		Args: map[string]string{"path": expected.Path, "content": expected.ProposedText,
			"original_hash": expected.OriginalHash, "proposed_hash": expected.ProposedHash},
		WorkingDir: binding.workspace.RootPath})
	if !decision.Allowed {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodePolicyDenied,
			"file edit revert proposal was denied by current Policy")
	}
	currentHash, err := fileedit.CurrentHash(binding.workspace.RootPath, expected.Path)
	if err != nil || currentHash != expected.OriginalHash {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeConflict,
			"file changed after the source edit; review the current file before reverting")
	}
	prepared, err := s.manager.PrepareProposal(ctx, fileedit.Proposal{ID: expected.ID,
		SessionID: expected.SessionID, WorkspaceID: expected.WorkspaceID,
		WorkspaceRoot: binding.workspace.RootPath, Path: expected.Path,
		Operation: expected.Operation, ProposedText: expected.ProposedText,
		ExpectedOriginalHash: expected.OriginalHash})
	if err != nil {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeConflict,
			"file could not be verified for this revert proposal")
	}
	if !fileedit.SameProposalContent(prepared, expected) {
		return CreateFileEditProposalResult{}, apperror.New(apperror.CodeConflict,
			"file edit revert proposal content failed exact verification")
	}
	edit, replayed, err := writer(ctx, prepared)
	if err != nil {
		return CreateFileEditProposalResult{}, apperror.Normalize(err)
	}
	return CreateFileEditProposalResult{Edit: edit, Replayed: replayed}, nil
}

func (s *FileEditProposalService) requireRevertContinuation(ctx context.Context,
	sourceRun, targetRun domain.Run, workspaceID string,
) error {
	reader, ok := s.store.(interface {
		GetThreadByRun(context.Context, string) (domain.Thread, error)
	})
	if !ok {
		return apperror.New(apperror.CodeFailedPrecondition, "file edit source continuation is unavailable")
	}
	sourceThread, err := reader.GetThreadByRun(ctx, sourceRun.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	targetThread, err := reader.GetThreadByRun(ctx, targetRun.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if sourceThread.ID != targetThread.ID || sourceRun.MissionID != targetRun.MissionID {
		return apperror.New(apperror.CodeFailedPrecondition, "file edit source belongs to another Thread")
	}
	sourceFiles, sourceOwned, err := readRunFileDrydock(ctx, s.store, sourceRun.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	targetFiles, targetOwned, err := readRunFileDrydock(ctx, s.store, targetRun.ID)
	if err != nil {
		return apperror.Normalize(err)
	}
	if sourceOwned != targetOwned || (sourceOwned && (sourceFiles.ID != targetFiles.ID ||
		sourceFiles.WorkspaceID != workspaceID || targetFiles.WorkspaceID != workspaceID)) ||
		(!sourceOwned && (sourceThread.WorkspaceID != workspaceID || targetThread.WorkspaceID != workspaceID)) {
		return apperror.New(apperror.CodeFailedPrecondition,
			"file edit source does not belong to this Thread working directory")
	}
	return nil
}

func inverseFileEdit(source fileedit.Edit, id string) (fileedit.Edit, error) {
	if source.Status != fileedit.StatusApplied || source.SecretsRedacted ||
		!validProposalSourcePath(source.Path) || !validProposedText(source.OriginalText) ||
		!validProposedText(source.ProposedText) || strings.ContainsRune(source.OriginalText, 0) ||
		strings.ContainsRune(source.ProposedText, 0) ||
		redact.String(source.OriginalText) != source.OriginalText ||
		redact.String(source.ProposedText) != source.ProposedText ||
		!validPersistedOriginal(source.OriginalHash, source.OriginalText) ||
		!validPersistedOriginal(source.ProposedHash, source.ProposedText) ||
		source.DestinationPath != "" || source.DestinationOriginalHash != "" ||
		source.DestinationProposedHash != "" {
		return fileedit.Edit{}, apperror.New(apperror.CodeFailedPrecondition,
			"revert requires a complete unredacted applied text edit")
	}
	operation := fileedit.OperationReplace
	switch source.Operation {
	case "", fileedit.OperationReplace:
		if source.ProposedHash == "missing" || source.OriginalHash == source.ProposedHash {
			return fileedit.Edit{}, apperror.New(apperror.CodeFailedPrecondition, "source replacement is invalid")
		}
		if source.OriginalHash == "missing" {
			operation = fileedit.OperationDelete
		}
	case fileedit.OperationCreate:
		if source.OriginalHash != "missing" || source.ProposedHash == "missing" {
			return fileedit.Edit{}, apperror.New(apperror.CodeFailedPrecondition, "source creation is invalid")
		}
		operation = fileedit.OperationDelete
	case fileedit.OperationDelete:
		if source.ProposedHash != "missing" || source.OriginalHash == "missing" {
			return fileedit.Edit{}, apperror.New(apperror.CodeFailedPrecondition, "source deletion is invalid")
		}
		operation = fileedit.OperationCreate
	default:
		return fileedit.Edit{}, apperror.New(apperror.CodeFailedPrecondition,
			"revert supports replacement, creation, and deletion only")
	}
	edit := fileedit.Edit{ID: id, SessionID: source.SessionID, WorkspaceID: source.WorkspaceID,
		Path: source.Path, Operation: operation, Status: fileedit.StatusProposed,
		OriginalText: source.ProposedText, ProposedText: source.OriginalText,
		OriginalHash: source.ProposedHash, ProposedHash: source.OriginalHash}
	edit.Diff = fileedit.UnifiedDiff(edit.Path, edit.OriginalText, edit.ProposedText)
	if edit.Operation == fileedit.OperationDelete {
		edit.Diff = fmt.Sprintf("delete %s\nexpected_sha256 %s\nsize_bytes %d\n",
			edit.Path, edit.OriginalHash, len(edit.OriginalText))
	}
	return edit, nil
}
