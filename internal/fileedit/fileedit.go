package fileedit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/tools"
)

const (
	MaxContentBytes = 256 * 1024
	// MaxDiffBytes bounds a persisted unified diff generated from two bounded
	// file versions, including per-line prefixes and headers.
	MaxDiffBytes            = 4*MaxContentBytes + 16*1024
	stagingFilePrefix       = ".cyberagent-edit-"
	stagingCleanupScanLimit = 256
	StagingCleanupGrace     = 15 * time.Minute
)

type StagingCleanupResult struct {
	Removed int
	Pending bool
}

const (
	StatusProposed = "proposed"
	StatusApproved = "approved"
	StatusApplied  = "applied"
	StatusDenied   = "denied"
	StatusFailed   = "failed"
)

const (
	OperationReplace = "replace"
	OperationCreate  = "create"
	OperationMove    = "move"
	OperationDelete  = "delete"
)

const missingHash = "missing"

type Edit struct {
	ID                      string
	SessionID               string
	WorkspaceID             string
	Path                    string
	Operation               string
	DestinationPath         string
	Status                  string
	OriginalText            string
	ProposedText            string
	Diff                    string
	OriginalHash            string
	ProposedHash            string
	DestinationOriginalHash string
	DestinationProposedHash string
	Reason                  string
	SecretsRedacted         bool
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// Preview is the read-only FileEdit projection used by operator surfaces.
// It deliberately excludes the original and proposed file bodies.
type Preview struct {
	ID                      string
	SessionID               string
	WorkspaceID             string
	Path                    string
	Operation               string
	DestinationPath         string
	Status                  string
	Diff                    string
	OriginalHash            string
	ProposedHash            string
	DestinationOriginalHash string
	DestinationProposedHash string
	Reason                  string
	SecretsRedacted         bool
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

func ValidStatus(status string) bool {
	switch status {
	case StatusProposed, StatusApproved, StatusApplied, StatusDenied, StatusFailed:
		return true
	default:
		return false
	}
}

type Proposal struct {
	// ID is optional for legacy callers. Interactive boundaries may supply one
	// stable Go-generated ID so an uncertain save can be reconciled safely.
	ID              string
	SessionID       string
	WorkspaceID     string
	WorkspaceRoot   string
	Path            string
	Operation       string
	DestinationPath string
	ProposedText    string
	// ExpectedOriginalHash binds an interactive proposal to the exact file
	// version issued by Go. Legacy agent proposals may leave it empty.
	ExpectedOriginalHash    string
	ExpectedDestinationHash string
}

func NormalizeOperation(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = OperationReplace
	}
	switch value {
	case OperationReplace, OperationCreate, OperationMove, OperationDelete:
		return value, nil
	default:
		return "", fmt.Errorf("unsupported file edit operation %q", value)
	}
}

func ApprovalToolName(edit Edit) string {
	switch edit.Operation {
	case OperationCreate:
		return "create_file"
	case OperationMove:
		return "move_file"
	case OperationDelete:
		return "delete_file"
	default:
		return "replace_file"
	}
}

type ListFilter struct {
	SessionID   string
	WorkspaceID string
	Status      string
}

type Store interface {
	SaveFileEdit(ctx context.Context, edit Edit) (Edit, error)
	GetFileEdit(ctx context.Context, id string) (Edit, error)
	ListFileEdits(ctx context.Context, filter ListFilter) ([]Edit, error)
}

type Manager struct {
	store Store
}

func NewManager(store Store) *Manager {
	return &Manager{store: store}
}

func (m *Manager) Propose(ctx context.Context, proposal Proposal) (Edit, error) {
	edit, err := m.PrepareProposal(ctx, proposal)
	if err != nil {
		return Edit{}, err
	}
	return m.store.SaveFileEdit(ctx, edit)
}

// PrepareProposal reads and verifies the exact current file without persisting
// a proposal or writing the workspace. Interactive callers can then insert the
// prepared edit atomically without resetting a concurrently reviewed proposal.
func (m *Manager) PrepareProposal(ctx context.Context, proposal Proposal) (Edit, error) {
	if m == nil || m.store == nil {
		return Edit{}, errors.New("file edit store is required")
	}
	proposal.WorkspaceID = strings.TrimSpace(proposal.WorkspaceID)
	proposal.WorkspaceRoot = strings.TrimSpace(proposal.WorkspaceRoot)
	if proposal.WorkspaceID == "" {
		return Edit{}, apperror.New(apperror.CodeInvalidArgument, "workspace id is required")
	}
	if proposal.WorkspaceRoot == "" {
		return Edit{}, apperror.New(apperror.CodeInvalidArgument, "workspace root is required")
	}
	operation, err := NormalizeOperation(proposal.Operation)
	if err != nil {
		return Edit{}, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	if len([]byte(proposal.ProposedText)) > MaxContentBytes {
		return Edit{}, apperror.New(apperror.CodeInvalidArgument, fmt.Sprintf("proposed content exceeds %d bytes", MaxContentBytes))
	}
	if !utf8.ValidString(proposal.ProposedText) {
		return Edit{}, apperror.New(apperror.CodeInvalidArgument, "proposed content is not valid UTF-8 text")
	}

	relPath, err := normalizePath(proposal.Path)
	if err != nil {
		return Edit{}, apperror.Wrap(apperror.CodeInvalidArgument, err.Error(), err)
	}
	root, rootedPath, _, err := openWorkspaceRootForFile(proposal.WorkspaceRoot, relPath, operation == OperationCreate)
	if err != nil {
		return Edit{}, err
	}
	defer root.Close()
	original, exists, err := readProposalTextFromRoot(root, rootedPath)
	if err != nil {
		return Edit{}, err
	}
	originalHash := contentHash(original, exists)
	if proposal.ExpectedOriginalHash != "" &&
		proposal.ExpectedOriginalHash != originalHash {
		return Edit{}, apperror.New(apperror.CodeConflict,
			"workspace file changed after the proposal source was issued")
	}
	destinationPath := ""
	destinationOriginalHash := ""
	destinationProposedHash := ""
	proposedRaw := proposal.ProposedText
	switch operation {
	case OperationReplace:
		if !exists {
			// Preserve legacy replace_file behavior while recording a more exact
			// operation for new proposals that explicitly select create.
		}
	case OperationCreate:
		if exists {
			return Edit{}, apperror.New(apperror.CodeConflict, "create requires an absent target")
		}
		if proposal.ExpectedOriginalHash != missingHash {
			return Edit{}, apperror.New(apperror.CodeInvalidArgument, "create requires the missing hash")
		}
	case OperationMove:
		if !exists {
			return Edit{}, apperror.New(apperror.CodeConflict, "move source does not exist")
		}
		destinationPath, err = normalizePath(proposal.DestinationPath)
		if err != nil || destinationPath == relPath {
			return Edit{}, apperror.New(apperror.CodeInvalidArgument, "move destination path is invalid")
		}
		if _, resolveErr := tools.NewWorkspaceFS(proposal.WorkspaceRoot).
			ResolveForWrite(destinationPath); resolveErr != nil {
			return Edit{}, resolveErr
		}
		rootedDestination := filepath.FromSlash(destinationPath)
		current, destinationExists, readErr := readProposalTextFromRoot(root, rootedDestination)
		if readErr != nil {
			return Edit{}, readErr
		}
		destinationOriginalHash = contentHash(current, destinationExists)
		if proposal.ExpectedDestinationHash == "" {
			return Edit{}, apperror.New(apperror.CodeInvalidArgument, "move destination expected hash is required")
		}
		if proposal.ExpectedDestinationHash != destinationOriginalHash {
			return Edit{}, apperror.New(apperror.CodeConflict, "move destination changed after the proposal source was issued")
		}
		if destinationOriginalHash != missingHash {
			return Edit{}, apperror.New(apperror.CodeConflict, "move destination must be absent")
		}
		proposedRaw = ""
		destinationProposedHash = originalHash
	case OperationDelete:
		if !exists {
			return Edit{}, apperror.New(apperror.CodeConflict, "delete target does not exist")
		}
		if proposal.ProposedText != "" || proposal.DestinationPath != "" ||
			proposal.ExpectedDestinationHash != "" {
			return Edit{}, apperror.New(apperror.CodeInvalidArgument, "delete cannot contain replacement or destination data")
		}
		proposedRaw = ""
	}
	proposed := redact.String(proposedRaw)
	secretsRedacted := proposed != proposedRaw
	if operation == OperationReplace && exists && original == proposed {
		return Edit{}, apperror.New(apperror.CodeInvalidArgument, "proposed content does not change the file")
	}

	originalPreview := redact.String(original)
	diff := UnifiedDiff(relPath, originalPreview, proposed)
	proposedHash := contentHash(proposed, true)
	switch operation {
	case OperationMove:
		diff = fmt.Sprintf("move %s -> %s\nsource_sha256 %s\ndestination_expected %s\n",
			relPath, destinationPath, originalHash, destinationOriginalHash)
		proposedHash = missingHash
	case OperationDelete:
		diff = fmt.Sprintf("delete %s\nexpected_sha256 %s\nsize_bytes %d\n",
			relPath, originalHash, len([]byte(original)))
		proposedHash = missingHash
	case OperationReplace:
		if exists && original != proposed && originalPreview == proposed {
			diff = redactedChangeDiff(relPath)
		}
	}
	editID := strings.TrimSpace(proposal.ID)
	if editID == "" {
		editID = newID("edit")
	} else if editID != proposal.ID || !validProposedEditID(editID) {
		return Edit{}, apperror.New(apperror.CodeInvalidArgument, "file edit proposal id is invalid")
	}
	now := time.Now().UTC()
	edit := Edit{
		ID:                      editID,
		SessionID:               strings.TrimSpace(proposal.SessionID),
		WorkspaceID:             proposal.WorkspaceID,
		Path:                    relPath,
		Operation:               operation,
		DestinationPath:         destinationPath,
		Status:                  StatusProposed,
		OriginalText:            originalPreview,
		ProposedText:            proposed,
		Diff:                    diff,
		OriginalHash:            originalHash,
		ProposedHash:            proposedHash,
		DestinationOriginalHash: destinationOriginalHash,
		DestinationProposedHash: destinationProposedHash,
		SecretsRedacted:         secretsRedacted || originalPreview != original,
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	return edit, nil
}

func (m *Manager) Approve(ctx context.Context, id string, workspaceRoot string) (Edit, error) {
	return m.ApproveWithPreWriteCheck(ctx, id, workspaceRoot, nil)
}

// ApproveWithPreWriteCheck rechecks a live automatic authorization before the
// first workspace mutation and again immediately before atomic publication.
// An error leaves the existing durable approval available for safe recovery.
func (m *Manager) ApproveWithPreWriteCheck(ctx context.Context, id string,
	workspaceRoot string, preWrite func() error,
) (Edit, error) {
	edit, err := m.store.GetFileEdit(ctx, strings.TrimSpace(id))
	if err != nil {
		return Edit{}, err
	}
	if edit.Status == StatusApplied {
		return edit, nil
	}
	if edit.Status != StatusProposed && edit.Status != StatusApproved {
		return Edit{}, fmt.Errorf("file edit %s is %s, not %s", edit.ID, edit.Status, StatusProposed)
	}
	if strings.TrimSpace(workspaceRoot) == "" {
		return Edit{}, errors.New("workspace root is required")
	}
	operation, err := NormalizeOperation(edit.Operation)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	edit.Operation = operation
	switch operation {
	case OperationMove:
		return m.approveMove(ctx, edit, workspaceRoot, preWrite)
	case OperationDelete:
		return m.approveDelete(ctx, edit, workspaceRoot, preWrite)
	}
	if contentHash(edit.ProposedText, true) != edit.ProposedHash {
		return m.fail(ctx, edit, errors.New("stored proposed content failed integrity validation"))
	}

	root, rootedPath, target, err := openWorkspaceRootForFile(workspaceRoot, edit.Path, operation == OperationCreate)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	defer root.Close()
	current, exists, err := readCurrentTextFromRoot(root, rootedPath)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	currentHash := contentHash(current, exists)
	if currentHash == edit.ProposedHash {
		edit.Status = StatusApplied
		edit.Reason = ""
		edit.UpdatedAt = time.Now().UTC()
		return m.store.SaveFileEdit(ctx, edit)
	}
	if currentHash != edit.OriginalHash {
		return m.fail(ctx, edit, errors.New("workspace file changed after the proposal; refusing to overwrite"))
	}

	edit.Status = StatusApproved
	edit.Reason = ""
	edit.UpdatedAt = time.Now().UTC()
	edit, err = m.store.SaveFileEdit(ctx, edit)
	if err != nil {
		return Edit{}, err
	}
	if preWrite != nil {
		if err := preWrite(); err != nil {
			return Edit{}, err
		}
	}
	if operation == OperationCreate {
		if createErr := prepareCreateDirectories(root, workspaceRoot, rootedPath); createErr != nil {
			return m.fail(ctx, edit, createErr)
		}
	}
	writeTarget, err := tools.NewWorkspaceFS(workspaceRoot).ResolveForWrite(edit.Path)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	if writeTarget != target {
		return m.fail(ctx, edit, errors.New("workspace path changed during approval; refusing to write"))
	}
	latest, latestExists, err := readCurrentTextFromRoot(root, rootedPath)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	if contentHash(latest, latestExists) != edit.OriginalHash {
		return m.fail(ctx, edit, errors.New("workspace file changed during approval; refusing to overwrite"))
	}
	target = writeTarget
	mode := os.FileMode(0o644)
	if info, statErr := root.Stat(rootedPath); statErr == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(statErr) {
		return m.fail(ctx, edit, statErr)
	}
	stagedPath, err := stageAtomicReplacement(root, rootedPath, edit.ProposedText, mode)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	defer func() {
		if stagedPath != "" {
			_ = root.Remove(stagedPath)
		}
	}()
	finalTarget, err := tools.NewWorkspaceFS(workspaceRoot).ResolveForWrite(edit.Path)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	if finalTarget != target {
		return m.fail(ctx, edit, errors.New(
			"workspace path changed before atomic replacement; refusing to write"))
	}
	latest, latestExists, err = readCurrentTextFromRoot(root, rootedPath)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	if contentHash(latest, latestExists) != edit.OriginalHash {
		return m.fail(ctx, edit, errors.New(
			"workspace file changed before atomic replacement; refusing to overwrite"))
	}
	if preWrite != nil {
		if err := preWrite(); err != nil {
			return Edit{}, err
		}
	}
	// A live authorization check may block on a database or policy adapter.
	// Re-read the target after it returns, just as the move path does.
	finalTarget, err = tools.NewWorkspaceFS(workspaceRoot).ResolveForWrite(edit.Path)
	if err != nil || finalTarget != target {
		return m.fail(ctx, edit, errors.New("workspace path changed after authorization check"))
	}
	latest, latestExists, err = readCurrentTextFromRoot(root, rootedPath)
	if err != nil || contentHash(latest, latestExists) != edit.OriginalHash {
		return m.fail(ctx, edit, errors.New("workspace file changed after authorization check"))
	}
	if operation == OperationCreate || edit.OriginalHash == missingHash {
		// Linking the completed staging inode into an absent target is an
		// atomic no-clobber publish. A concurrent creator therefore wins and
		// this proposal fails closed instead of overwriting its file.
		if err := root.Link(stagedPath, rootedPath); err != nil {
			return m.fail(ctx, edit, err)
		}
		if err := root.Remove(stagedPath); err != nil {
			return m.fail(ctx, edit, err)
		}
	} else if err := root.Rename(stagedPath, rootedPath); err != nil {
		return m.fail(ctx, edit, err)
	}
	stagedPath = ""
	syncRootParentDirectory(root, rootedPath)

	written, writtenExists, err := readCurrentTextFromRoot(root, rootedPath)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	if contentHash(written, writtenExists) != edit.ProposedHash {
		return m.fail(ctx, edit, errors.New("written file failed integrity validation"))
	}
	edit.Status = StatusApplied
	edit.Reason = ""
	edit.UpdatedAt = time.Now().UTC()
	return m.store.SaveFileEdit(ctx, edit)
}

func (m *Manager) approveMove(ctx context.Context, edit Edit,
	workspaceRoot string, preWrite func() error,
) (Edit, error) {
	if edit.DestinationPath == "" || edit.DestinationPath == edit.Path ||
		edit.ProposedHash != missingHash || edit.DestinationOriginalHash != missingHash ||
		!validDigest(edit.OriginalHash) || edit.DestinationProposedHash != edit.OriginalHash {
		return m.fail(ctx, edit, errors.New("stored move proposal failed integrity validation"))
	}
	root, rootedSource, source, err := openWorkspaceRootForFile(workspaceRoot, edit.Path)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	defer root.Close()
	fs := tools.NewWorkspaceFS(workspaceRoot)
	destination, err := fs.ResolveForWrite(edit.DestinationPath)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	rootedDestination := filepath.FromSlash(edit.DestinationPath)
	sourceHash, err := currentHashFromRoot(root, rootedSource)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	destinationHash, err := currentHashFromRoot(root, rootedDestination)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	if sourceHash == missingHash && destinationHash == edit.DestinationProposedHash {
		edit.Status = StatusApplied
		edit.Reason = ""
		edit.UpdatedAt = time.Now().UTC()
		return m.store.SaveFileEdit(ctx, edit)
	}
	linkedRecovery := sourceHash == edit.OriginalHash &&
		destinationHash == edit.DestinationProposedHash
	if linkedRecovery {
		same, sameErr := sameRootFile(root, rootedSource, rootedDestination)
		if sameErr != nil || !same {
			return m.fail(ctx, edit, errors.New(
				"workspace move destination is not the recoverable source link"))
		}
	} else if sourceHash != edit.OriginalHash ||
		destinationHash != edit.DestinationOriginalHash {
		return m.fail(ctx, edit, errors.New(
			"workspace move source or destination changed; refusing to rename"))
	}
	edit.Status = StatusApproved
	edit.Reason = ""
	edit.UpdatedAt = time.Now().UTC()
	edit, err = m.store.SaveFileEdit(ctx, edit)
	if err != nil {
		return Edit{}, err
	}
	latestSource, err := fs.ResolveForWrite(edit.Path)
	if err != nil || latestSource != source {
		return m.fail(ctx, edit, errors.New("workspace move source changed during approval"))
	}
	latestDestination, err := fs.ResolveForWrite(edit.DestinationPath)
	if err != nil || latestDestination != destination {
		return m.fail(ctx, edit, errors.New("workspace move destination changed during approval"))
	}
	if !linkedRecovery {
		latestSourceHash, sourceErr := currentHashFromRoot(root, rootedSource)
		latestDestinationHash, destinationErr := currentHashFromRoot(root, rootedDestination)
		if sourceErr != nil || destinationErr != nil || latestSourceHash != edit.OriginalHash ||
			latestDestinationHash != edit.DestinationOriginalHash {
			return m.fail(ctx, edit, errors.New(
				"workspace move changed before no-clobber publish; refusing to continue"))
		}
		if preWrite != nil {
			if err := preWrite(); err != nil {
				return Edit{}, err
			}
		}
		latestSource, err = fs.ResolveForWrite(edit.Path)
		if err != nil || latestSource != source {
			return m.fail(ctx, edit, errors.New(
				"workspace move source changed after authorization check"))
		}
		latestDestination, err = fs.ResolveForWrite(edit.DestinationPath)
		if err != nil || latestDestination != destination {
			return m.fail(ctx, edit, errors.New(
				"workspace move destination changed after authorization check"))
		}
		latestSourceHash, sourceErr = currentHashFromRoot(root, rootedSource)
		latestDestinationHash, destinationErr = currentHashFromRoot(root, rootedDestination)
		if sourceErr != nil || destinationErr != nil || latestSourceHash != edit.OriginalHash ||
			latestDestinationHash != edit.DestinationOriginalHash {
			return m.fail(ctx, edit, errors.New(
				"workspace move changed after authorization check; refusing to publish"))
		}
		// A hard-link publish is atomic and cannot replace a destination that
		// appeared after the hash check. Removing the source completes the move;
		// a crash between these steps leaves a recognizable, recoverable pair.
		if err := root.Link(rootedSource, rootedDestination); err != nil {
			return m.fail(ctx, edit, err)
		}
		same, sameErr := sameRootFile(root, rootedSource, rootedDestination)
		linkedSourceHash, sourceErr := currentHashFromRoot(root, rootedSource)
		linkedDestinationHash, destinationErr := currentHashFromRoot(root, rootedDestination)
		if sameErr != nil || !same || sourceErr != nil || destinationErr != nil ||
			linkedSourceHash != edit.OriginalHash ||
			linkedDestinationHash != edit.DestinationProposedHash {
			return m.fail(ctx, edit, errors.New(
				"workspace move changed during no-clobber publish"))
		}
	}
	// Moving is a two-step publish. Recheck both the live authorization and
	// the recoverable hard-link identity immediately before deleting the
	// source. This check also covers a restart that found both linked paths.
	if preWrite != nil {
		if err := preWrite(); err != nil {
			return Edit{}, err
		}
	}
	latestSource, err = fs.ResolveForWrite(edit.Path)
	if err != nil || latestSource != source {
		return m.fail(ctx, edit, errors.New("workspace move source changed before source removal"))
	}
	latestDestination, err = fs.ResolveForWrite(edit.DestinationPath)
	if err != nil || latestDestination != destination {
		return m.fail(ctx, edit, errors.New("workspace move destination changed before source removal"))
	}
	finalSourceBeforeRemove, sourceErr := currentHashFromRoot(root, rootedSource)
	finalDestinationBeforeRemove, destinationErr := currentHashFromRoot(root, rootedDestination)
	same, sameErr := sameRootFile(root, rootedSource, rootedDestination)
	if sourceErr != nil || destinationErr != nil || sameErr != nil || !same ||
		finalSourceBeforeRemove != edit.OriginalHash ||
		finalDestinationBeforeRemove != edit.DestinationProposedHash {
		return m.fail(ctx, edit, errors.New(
			"workspace move changed before source removal; refusing to continue"))
	}
	if err := root.Remove(rootedSource); err != nil {
		if sourceHashAfter, hashErr := currentHashFromRoot(root, rootedSource); hashErr != nil || sourceHashAfter != missingHash {
			return m.fail(ctx, edit, err)
		}
	}
	syncRootParentDirectory(root, rootedSource)
	if filepath.Dir(destination) != filepath.Dir(source) {
		syncRootParentDirectory(root, rootedDestination)
	}
	finalSourceHash, sourceErr := currentHashFromRoot(root, rootedSource)
	finalDestinationHash, destinationErr := currentHashFromRoot(root, rootedDestination)
	if sourceErr != nil || destinationErr != nil || finalSourceHash != missingHash ||
		finalDestinationHash != edit.DestinationProposedHash {
		return m.fail(ctx, edit, errors.New("workspace move failed final hash verification"))
	}
	edit.Status = StatusApplied
	edit.UpdatedAt = time.Now().UTC()
	return m.store.SaveFileEdit(ctx, edit)
}

func (m *Manager) approveDelete(ctx context.Context, edit Edit,
	workspaceRoot string, preWrite func() error,
) (Edit, error) {
	if edit.DestinationPath != "" || edit.DestinationOriginalHash != "" ||
		edit.DestinationProposedHash != "" || edit.ProposedHash != missingHash ||
		!validDigest(edit.OriginalHash) {
		return m.fail(ctx, edit, errors.New("stored delete proposal failed integrity validation"))
	}
	root, rootedPath, target, err := openWorkspaceRootForFile(workspaceRoot, edit.Path)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	defer root.Close()
	currentHash, err := currentHashFromRoot(root, rootedPath)
	if err != nil {
		return m.fail(ctx, edit, err)
	}
	if currentHash == missingHash {
		edit.Status = StatusApplied
		edit.Reason = ""
		edit.UpdatedAt = time.Now().UTC()
		return m.store.SaveFileEdit(ctx, edit)
	}
	if currentHash != edit.OriginalHash {
		return m.fail(ctx, edit, errors.New(
			"workspace delete target changed; refusing to remove"))
	}
	edit.Status = StatusApproved
	edit.Reason = ""
	edit.UpdatedAt = time.Now().UTC()
	edit, err = m.store.SaveFileEdit(ctx, edit)
	if err != nil {
		return Edit{}, err
	}
	latest, err := tools.NewWorkspaceFS(workspaceRoot).ResolveForWrite(edit.Path)
	if err != nil || latest != target {
		return m.fail(ctx, edit, errors.New("workspace delete target changed during approval"))
	}
	latestHash, err := currentHashFromRoot(root, rootedPath)
	if err != nil || latestHash != edit.OriginalHash {
		return m.fail(ctx, edit, errors.New(
			"workspace delete target changed before removal; refusing to continue"))
	}
	if preWrite != nil {
		if err := preWrite(); err != nil {
			return Edit{}, err
		}
	}
	latest, err = tools.NewWorkspaceFS(workspaceRoot).ResolveForWrite(edit.Path)
	if err != nil || latest != target {
		return m.fail(ctx, edit, errors.New("workspace delete target changed after authorization check"))
	}
	latestHash, err = currentHashFromRoot(root, rootedPath)
	if err != nil || latestHash != edit.OriginalHash {
		return m.fail(ctx, edit, errors.New("workspace delete content changed after authorization check"))
	}
	if err := root.Remove(rootedPath); err != nil {
		return m.fail(ctx, edit, err)
	}
	syncRootParentDirectory(root, rootedPath)
	finalHash, err := currentHashFromRoot(root, rootedPath)
	if err != nil || finalHash != missingHash {
		return m.fail(ctx, edit, errors.New("workspace delete failed final hash verification"))
	}
	edit.Status = StatusApplied
	edit.UpdatedAt = time.Now().UTC()
	return m.store.SaveFileEdit(ctx, edit)
}

func stageAtomicReplacement(root *os.Root, target string, content string,
	mode os.FileMode,
) (string, error) {
	if root == nil {
		return "", errors.New("workspace root handle is required")
	}
	var file *os.File
	var path string
	for attempt := 0; attempt < 32; attempt++ {
		path = filepath.Join(filepath.Dir(target), stagingFilePrefix+newID("stage"))
		var err error
		file, err = root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return "", err
		}
	}
	if file == nil {
		return "", errors.New("could not allocate a unique workspace staging file")
	}
	complete := false
	defer func() {
		if !complete {
			_ = file.Close()
			_ = root.Remove(path)
		}
	}()
	if _, err := io.WriteString(file, content); err != nil {
		return "", err
	}
	if err := file.Chmod(mode); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	complete = true
	return path, nil
}

// CleanupStaleStaging removes only old regular internal staging files whose
// exact bytes match this approved proposal. Fresh files are left alone so a
// concurrent retry cannot lose an in-progress atomic replacement.
func CleanupStaleStaging(workspaceRoot string, path string, proposedHash string,
	now time.Time,
) (StagingCleanupResult, error) {
	if !validDigest(proposedHash) || now.IsZero() {
		return StagingCleanupResult{}, errors.New("staging cleanup digest and time are required")
	}
	root, rootedTarget, _, err := openWorkspaceRootForFile(workspaceRoot, path)
	if err != nil {
		return StagingCleanupResult{}, err
	}
	defer root.Close()
	directoryPath := filepath.Dir(rootedTarget)
	directory, err := root.Open(directoryPath)
	if err != nil {
		return StagingCleanupResult{}, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(stagingCleanupScanLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return StagingCleanupResult{}, err
	}
	result := StagingCleanupResult{Pending: len(entries) > stagingCleanupScanLimit}
	if len(entries) > stagingCleanupScanLimit {
		entries = entries[:stagingCleanupScanLimit]
	}
	cutoff := now.UTC().Add(-StagingCleanupGrace)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), stagingFilePrefix) {
			continue
		}
		candidate := filepath.Join(directoryPath, entry.Name())
		info, infoErr := root.Lstat(candidate)
		if infoErr != nil {
			if !os.IsNotExist(infoErr) {
				result.Pending = true
			}
			continue
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > MaxContentBytes ||
			!info.ModTime().Before(cutoff) {
			result.Pending = true
			continue
		}
		matches, matchErr := stagingFileMatches(root, candidate, info, proposedHash)
		if matchErr != nil {
			result.Pending = true
			continue
		}
		if !matches {
			continue
		}
		latest, latestErr := root.Lstat(candidate)
		if latestErr != nil {
			if !os.IsNotExist(latestErr) {
				result.Pending = true
			}
			continue
		}
		if !os.SameFile(info, latest) || !latest.Mode().IsRegular() {
			result.Pending = true
			continue
		}
		if removeErr := root.Remove(candidate); removeErr != nil {
			result.Pending = true
			continue
		}
		result.Removed++
	}
	return result, nil
}

// InspectStaging reports whether cleanup may still be required without
// deleting or modifying any directory entry.
func InspectStaging(workspaceRoot string, path string, proposedHash string,
	now time.Time,
) (StagingCleanupResult, error) {
	if !validDigest(proposedHash) || now.IsZero() {
		return StagingCleanupResult{}, errors.New("staging inspection digest and time are required")
	}
	root, rootedTarget, _, err := openWorkspaceRootForFile(workspaceRoot, path)
	if err != nil {
		return StagingCleanupResult{}, err
	}
	defer root.Close()
	directoryPath := filepath.Dir(rootedTarget)
	directory, err := root.Open(directoryPath)
	if err != nil {
		return StagingCleanupResult{}, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(stagingCleanupScanLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return StagingCleanupResult{}, err
	}
	result := StagingCleanupResult{Pending: len(entries) > stagingCleanupScanLimit}
	if len(entries) > stagingCleanupScanLimit {
		entries = entries[:stagingCleanupScanLimit]
	}
	cutoff := now.UTC().Add(-StagingCleanupGrace)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), stagingFilePrefix) {
			continue
		}
		candidate := filepath.Join(directoryPath, entry.Name())
		info, infoErr := root.Lstat(candidate)
		if infoErr != nil || !info.Mode().IsRegular() || info.Size() < 0 ||
			info.Size() > MaxContentBytes || !info.ModTime().Before(cutoff) {
			result.Pending = true
			continue
		}
		matches, matchErr := stagingFileMatches(root, candidate, info, proposedHash)
		if matchErr != nil {
			result.Pending = true
			continue
		}
		if matches {
			result.Pending = true
		}
	}
	return result, nil
}

func stagingFileMatches(root *os.Root, path string, expected os.FileInfo,
	proposedHash string,
) (bool, error) {
	file, err := root.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !os.SameFile(expected, opened) || !opened.Mode().IsRegular() {
		return false, nil
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxContentBytes+1))
	if err != nil {
		return false, err
	}
	if len(data) > MaxContentBytes {
		return false, nil
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == proposedHash, nil
}

func syncRootParentDirectory(root *os.Root, path string) {
	if root == nil {
		return
	}
	directory, err := root.Open(filepath.Dir(path))
	if err != nil {
		return
	}
	_ = directory.Sync()
	_ = directory.Close()
}

// ApproveIntent records an operator review without touching the workspace.
// Applying the approved proposal remains a separate operation that must call
// Approve with an independently authorized workspace root.
func (m *Manager) ApproveIntent(ctx context.Context, id string) (Edit, error) {
	if m == nil || m.store == nil {
		return Edit{}, errors.New("file edit store is required")
	}
	edit, err := m.store.GetFileEdit(ctx, strings.TrimSpace(id))
	if err != nil {
		return Edit{}, err
	}
	if edit.Status == StatusApproved {
		return edit, nil
	}
	if edit.Status != StatusProposed {
		return Edit{}, fmt.Errorf("file edit %s is %s, not %s", edit.ID, edit.Status, StatusProposed)
	}
	operation, err := NormalizeOperation(edit.Operation)
	if err != nil {
		return Edit{}, err
	}
	edit.Operation = operation
	if operation != OperationMove && operation != OperationDelete &&
		contentHash(edit.ProposedText, true) != edit.ProposedHash {
		return Edit{}, errors.New("stored proposed content failed integrity validation")
	}
	if operation == OperationMove && (edit.ProposedHash != missingHash ||
		edit.DestinationProposedHash != edit.OriginalHash) {
		return Edit{}, errors.New("stored move proposal failed integrity validation")
	}
	if operation == OperationDelete && edit.ProposedHash != missingHash {
		return Edit{}, errors.New("stored delete proposal failed integrity validation")
	}
	edit.Status = StatusApproved
	edit.Reason = ""
	edit.UpdatedAt = time.Now().UTC()
	return m.store.SaveFileEdit(ctx, edit)
}

func (m *Manager) Deny(ctx context.Context, id string, reason string) (Edit, error) {
	edit, err := m.store.GetFileEdit(ctx, strings.TrimSpace(id))
	if err != nil {
		return Edit{}, err
	}
	if edit.Status == StatusDenied {
		return edit, nil
	}
	if edit.Status != StatusProposed {
		return Edit{}, fmt.Errorf("file edit %s is %s, not %s", edit.ID, edit.Status, StatusProposed)
	}
	edit.Status = StatusDenied
	edit.Reason = redact.String(strings.TrimSpace(reason))
	edit.UpdatedAt = time.Now().UTC()
	return m.store.SaveFileEdit(ctx, edit)
}

func (m *Manager) Get(ctx context.Context, id string) (Edit, error) {
	return m.store.GetFileEdit(ctx, strings.TrimSpace(id))
}

func (m *Manager) List(ctx context.Context, filter ListFilter) ([]Edit, error) {
	return m.store.ListFileEdits(ctx, filter)
}

func (m *Manager) fail(ctx context.Context, edit Edit, cause error) (Edit, error) {
	if edit.Operation == OperationCreate {
		cause = fmt.Errorf("%w; newly created parent directories, if any, are retained", cause)
	}
	edit.Status = StatusFailed
	edit.Reason = redact.String(cause.Error())
	edit.UpdatedAt = time.Now().UTC()
	saved, saveErr := m.store.SaveFileEdit(ctx, edit)
	if saveErr != nil {
		return Edit{}, errors.Join(cause, saveErr)
	}
	return saved, cause
}

func normalizePath(path string) (string, error) {
	if path != strings.TrimSpace(path) {
		return "", errors.New("file path cannot contain surrounding whitespace")
	}
	if path == "" || path == "." {
		return "", errors.New("file path is required")
	}
	if filepath.IsAbs(path) {
		return "", errors.New("path must be relative to the workspace")
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace: %s", path)
	}
	return filepath.ToSlash(clean), nil
}

func openWorkspaceRootForFile(workspaceRoot string, path string, createParents ...bool) (
	*os.Root, string, string, error,
) {
	relPath, err := normalizePath(path)
	if err != nil {
		return nil, "", "", err
	}
	fs := tools.NewWorkspaceFS(workspaceRoot)
	resolve := fs.ResolveForWrite
	if len(createParents) != 0 && createParents[0] {
		resolve = fs.ResolveForCreate
	}
	target, err := resolve(relPath)
	if err != nil {
		return nil, "", "", err
	}
	canonicalRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, "", "", err
	}
	canonicalRoot, err = filepath.EvalSymlinks(canonicalRoot)
	if err != nil {
		return nil, "", "", err
	}
	root, err := os.OpenRoot(canonicalRoot)
	if err != nil {
		return nil, "", "", err
	}
	fail := func(cause error) (*os.Root, string, string, error) {
		_ = root.Close()
		return nil, "", "", cause
	}
	latestTarget, err := resolve(relPath)
	if err != nil || latestTarget != target {
		return fail(errors.New("workspace path changed while opening its root handle"))
	}
	openedInfo, openedErr := root.Stat(".")
	currentInfo, currentErr := os.Lstat(canonicalRoot)
	if openedErr != nil || currentErr != nil || !currentInfo.IsDir() ||
		currentInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, currentInfo) {
		return fail(errors.New("workspace root changed while opening its root handle"))
	}
	return root, filepath.FromSlash(relPath), target, nil
}

// workspaceReadError records a proven read limitation without making it
// recoverable in approval/apply paths, where a write may already have happened.
type workspaceReadError struct {
	message string
	changed bool
}

func (e *workspaceReadError) Error() string { return e.message }

func readProposalTextFromRoot(root *os.Root, path string) (string, bool, error) {
	content, exists, err := readCurrentTextFromRoot(root, path)
	if known, ok := err.(*workspaceReadError); ok {
		code := apperror.CodeFailedPrecondition
		if known.changed {
			code = apperror.CodeConflict
		}
		return content, exists, apperror.Wrap(code, known.Error(), known)
	}
	return content, exists, err
}

func readCurrentTextFromRoot(root *os.Root, path string) (string, bool, error) {
	if root == nil {
		return "", false, errors.New("workspace root handle is required")
	}
	expected, err := root.Lstat(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !expected.Mode().IsRegular() || expected.Mode()&os.ModeSymlink != 0 {
		return "", false, &workspaceReadError{message: fmt.Sprintf("%s is not a regular workspace file", path)}
	}
	file, err := root.Open(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return "", false, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return "", false, &workspaceReadError{message: "workspace file changed while it was opened", changed: true}
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxContentBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(data) > MaxContentBytes {
		return "", false, &workspaceReadError{message: fmt.Sprintf("file exceeds %d bytes", MaxContentBytes)}
	}
	if !utf8.Valid(data) {
		return "", false, &workspaceReadError{message: "file is not valid UTF-8 text"}
	}
	return string(data), true, nil
}

func currentHashFromRoot(root *os.Root, path string) (string, error) {
	current, exists, err := readCurrentTextFromRoot(root, path)
	if err != nil {
		return "", err
	}
	return contentHash(current, exists), nil
}

func sameRootFile(root *os.Root, left string, right string) (bool, error) {
	leftInfo, err := root.Stat(left)
	if err != nil {
		return false, err
	}
	rightInfo, err := root.Stat(right)
	if err != nil {
		return false, err
	}
	return leftInfo.Mode().IsRegular() && rightInfo.Mode().IsRegular() &&
		os.SameFile(leftInfo, rightInfo), nil
}

func contentHash(content string, exists bool) string {
	if !exists {
		return missingHash
	}
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func HashText(content string) string {
	return contentHash(content, true)
}

// CurrentHash resolves a persisted workspace-relative path through the same
// workspace boundary used for writes and returns either its SHA-256 or the
// stable "missing" sentinel.
func CurrentHash(workspaceRoot string, path string) (string, error) {
	return currentHash(workspaceRoot, path, false)
}

// CurrentCreateHash is read-only and treats an absent parent suffix as an
// absent create target. Existing-path integrity checks are unchanged.
func CurrentCreateHash(workspaceRoot string, path string) (string, error) {
	return currentHash(workspaceRoot, path, true)
}

func currentHash(workspaceRoot string, path string, createParents bool) (string, error) {
	if strings.TrimSpace(workspaceRoot) == "" {
		return "", errors.New("workspace root is required")
	}
	relPath, err := normalizePath(path)
	if err != nil {
		return "", err
	}
	root, rootedPath, _, err := openWorkspaceRootForFile(workspaceRoot, relPath, createParents)
	if err != nil {
		return "", err
	}
	defer root.Close()
	return currentHashFromRoot(root, rootedPath)
}

func newID(prefix string) string {
	return idgen.New(prefix)
}

func validProposedEditID(value string) bool {
	if len(value) < len("edit-")+1 || len(value) > 64 || !strings.HasPrefix(value, "edit-") {
		return false
	}
	for _, current := range value {
		if !((current >= 'a' && current <= 'z') || (current >= '0' && current <= '9') ||
			current == '-') {
			return false
		}
	}
	return true
}
