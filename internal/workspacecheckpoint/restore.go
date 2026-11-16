package workspacecheckpoint

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"cyberagent-workbench/internal/rootfs"
	"cyberagent-workbench/internal/workspaceidentity"
)

type ChangeKind string

const (
	ChangeCreate   ChangeKind = "create"
	ChangeModify   ChangeKind = "modify"
	ChangeDelete   ChangeKind = "delete"
	ChangeRename   ChangeKind = "rename"
	ChangeMetadata ChangeKind = "metadata"
)

type ConflictKind string

const (
	ConflictRootDrift         ConflictKind = "root_drift"
	ConflictCommitDrift       ConflictKind = "commit_drift"
	ConflictBranchDrift       ConflictKind = "branch_drift"
	ConflictIndexDrift        ConflictKind = "dirty_index"
	ConflictExternalChange    ConflictKind = "external_change"
	ConflictUnrecoverable     ConflictKind = "unrecoverable"
	ConflictRedirectedPath    ConflictKind = "redirected_path"
	ConflictCheckpointBinding ConflictKind = "checkpoint_binding"
)

type Change struct {
	Kind         ChangeKind `json:"kind"`
	Path         string     `json:"path"`
	PreviousPath string     `json:"previous_path,omitempty"`
	FromSHA256   string     `json:"from_sha256"`
	ToSHA256     string     `json:"to_sha256"`
	Binary       bool       `json:"binary"`
	Recoverable  bool       `json:"recoverable"`
	Reason       string     `json:"reason,omitempty"`
}

type Conflict struct {
	Kind           ConflictKind `json:"kind"`
	Path           string       `json:"path,omitempty"`
	ExpectedSHA256 string       `json:"expected_sha256,omitempty"`
	CurrentSHA256  string       `json:"current_sha256,omitempty"`
	TargetSHA256   string       `json:"target_sha256,omitempty"`
	Reason         string       `json:"reason"`
}

func (c Conflict) Validate() error {
	if !c.Kind.Valid() || (c.Path != "" && !validPath(c.Path)) ||
		!validConflictValue(c.ExpectedSHA256) ||
		!validConflictValue(c.CurrentSHA256) ||
		!validConflictValue(c.TargetSHA256) || !validReason(c.Reason) ||
		c.Reason == "" {
		return errors.New("workspace checkpoint conflict is invalid")
	}
	return nil
}

func (k ConflictKind) Valid() bool {
	switch k {
	case ConflictRootDrift, ConflictCommitDrift, ConflictBranchDrift,
		ConflictIndexDrift, ConflictExternalChange, ConflictUnrecoverable,
		ConflictRedirectedPath, ConflictCheckpointBinding:
		return true
	default:
		return false
	}
}

type Preview struct {
	ProtocolVersion             string        `json:"protocol_version"`
	ExpectedCurrentCheckpointID string        `json:"expected_current_checkpoint_id"`
	TargetCheckpointID          string        `json:"target_checkpoint_id"`
	ObservedCheckpointID        string        `json:"observed_checkpoint_id"`
	RecoveryLevel               RecoveryLevel `json:"recovery_level"`
	IndexChanged                bool          `json:"index_changed"`
	Changes                     []Change      `json:"changes"`
	Conflicts                   []Conflict    `json:"conflicts"`
	Truncated                   bool          `json:"truncated"`
}

type RestoreResult struct {
	Preview       Preview  `json:"preview"`
	AppliedPaths  []string `json:"applied_paths"`
	DeletedPaths  []string `json:"deleted_paths"`
	IndexRestored bool     `json:"index_restored"`
}

type ConflictError struct {
	Conflicts []Conflict
}

func (e *ConflictError) Error() string {
	if e == nil || len(e.Conflicts) == 0 {
		return "workspace restore conflict"
	}
	return fmt.Sprintf("workspace restore stopped with %d conflict(s)", len(e.Conflicts))
}

func (e *ConflictError) ConflictJSON() string {
	if e == nil {
		return "[]"
	}
	value, err := json.Marshal(e.Conflicts)
	if err != nil {
		return "[]"
	}
	return string(value)
}

// PreviewRestore performs a three-way comparison. expected is the immutable
// state the caller believes is live, target is the requested historical state,
// and observed is a fresh read-only capture. Any fourth state fails closed.
func PreviewRestore(expected, target, observed Snapshot) (Preview, error) {
	return previewRestore(expected, target, observed, false)
}

// PreviewWorkspaceRestore retains each snapshot's original execution identity.
// The caller must first authorize all three Runs against the same immutable
// physical workspace binding. Root, workspace, branch, index and content checks
// remain identical to an ordinary restore.
func PreviewWorkspaceRestore(expected, target, observed Snapshot) (Preview, error) {
	return previewRestore(expected, target, observed, true)
}

func previewRestore(expected, target, observed Snapshot, sameWorkspace bool) (Preview, error) {
	for _, snapshot := range []Snapshot{expected, target, observed} {
		if err := snapshot.Validate(); err != nil {
			return Preview{}, err
		}
	}
	preview := Preview{ProtocolVersion: ProtocolVersion,
		ExpectedCurrentCheckpointID: expected.Checkpoint.ID,
		TargetCheckpointID:          target.Checkpoint.ID,
		ObservedCheckpointID:        observed.Checkpoint.ID,
		RecoveryLevel: weakestRecovery(expected.Checkpoint.RecoveryLevel,
			target.Checkpoint.RecoveryLevel, observed.Checkpoint.RecoveryLevel),
		IndexChanged: !expected.Checkpoint.SameGitIndex(target.Checkpoint),
		Changes:      []Change{}, Conflicts: []Conflict{}}

	if (!sameWorkspace && (expected.Checkpoint.RunID != target.Checkpoint.RunID ||
		observed.Checkpoint.RunID != expected.Checkpoint.RunID)) ||
		expected.Checkpoint.WorkspaceID != target.Checkpoint.WorkspaceID ||
		observed.Checkpoint.WorkspaceID != expected.Checkpoint.WorkspaceID {
		appendConflict(&preview, Conflict{Kind: ConflictCheckpointBinding,
			Reason: "checkpoint Run or Workspace binding does not match"})
	}
	if expected.Checkpoint.RootFingerprint != target.Checkpoint.RootFingerprint ||
		expected.Checkpoint.RootPathSHA256 != target.Checkpoint.RootPathSHA256 ||
		observed.Checkpoint.RootFingerprint != expected.Checkpoint.RootFingerprint ||
		observed.Checkpoint.RootPathSHA256 != expected.Checkpoint.RootPathSHA256 {
		appendConflict(&preview, Conflict{Kind: ConflictRootDrift,
			Reason: "workspace root identity changed"})
	}
	if target.Checkpoint.BaseCommit != expected.Checkpoint.BaseCommit ||
		observed.Checkpoint.BaseCommit != expected.Checkpoint.BaseCommit {
		appendConflict(&preview, Conflict{Kind: ConflictCommitDrift,
			ExpectedSHA256: expected.Checkpoint.BaseCommit,
			CurrentSHA256:  observed.Checkpoint.BaseCommit,
			TargetSHA256:   target.Checkpoint.BaseCommit,
			Reason:         "Git HEAD changed; restore cannot rewrite commit history"})
	}
	if target.Checkpoint.Branch != expected.Checkpoint.Branch ||
		observed.Checkpoint.Branch != expected.Checkpoint.Branch {
		appendConflict(&preview, Conflict{Kind: ConflictBranchDrift,
			Reason: "Git branch identity changed"})
	}
	if !observed.Checkpoint.SameGitIndex(expected.Checkpoint) &&
		!observed.Checkpoint.SameGitIndex(target.Checkpoint) {
		appendConflict(&preview, Conflict{Kind: ConflictIndexDrift,
			ExpectedSHA256: expected.Checkpoint.IndexSHA256,
			CurrentSHA256:  observed.Checkpoint.IndexSHA256,
			TargetSHA256:   target.Checkpoint.IndexSHA256,
			Reason:         "Git index differs from both expected and target checkpoints"})
	}
	for _, reason := range []string{incompleteManifestReason, caseCollisionReason, indexPathCasingReason} {
		if slices.Contains(expected.Checkpoint.IncompleteReasons, reason) ||
			slices.Contains(target.Checkpoint.IncompleteReasons, reason) ||
			slices.Contains(observed.Checkpoint.IncompleteReasons, reason) {
			appendConflict(&preview, Conflict{Kind: ConflictUnrecoverable, Reason: reason})
		}
	}
	if preview.IndexChanged && target.Checkpoint.IndexBlobSHA256 == "" && target.Checkpoint.gitIndexPresent() {
		appendConflict(&preview, Conflict{Kind: ConflictUnrecoverable,
			Reason: "target Git index blob is unavailable"})
	}

	expectedEntries := entryMap(expected.Entries)
	targetEntries := entryMap(target.Entries)
	observedEntries := entryMap(observed.Entries)
	paths := unionPaths(expectedEntries, targetEntries, observedEntries)
	changes := make([]Change, 0, len(paths))
	for _, currentPath := range paths {
		from, fromOK := expectedEntries[currentPath]
		to, toOK := targetEntries[currentPath]
		live, liveOK := observedEntries[currentPath]
		if !sameWorktreeEntry(from, fromOK, to, toOK) {
			change := describeChange(currentPath, from, fromOK, to, toOK)
			if !change.Recoverable {
				appendConflict(&preview, Conflict{Kind: ConflictUnrecoverable,
					Path: currentPath, ExpectedSHA256: entryStateDigest(from, fromOK),
					CurrentSHA256: entryStateDigest(live, liveOK),
					TargetSHA256:  entryStateDigest(to, toOK),
					Reason:        change.Reason})
			}
			changes = append(changes, change)
		}
		if sameWorktreeEntry(live, liveOK, from, fromOK) ||
			sameWorktreeEntry(live, liveOK, to, toOK) {
			continue
		}
		appendConflict(&preview, Conflict{Kind: ConflictExternalChange, Path: currentPath,
			ExpectedSHA256: entryStateDigest(from, fromOK),
			CurrentSHA256:  entryStateDigest(live, liveOK),
			TargetSHA256:   entryStateDigest(to, toOK),
			Reason:         "live Workspace content differs from both expected and target checkpoints"})
	}
	preview.Changes = foldRenames(changes)
	if len(preview.Changes) > MaxPreviewChanges {
		preview.Changes = preview.Changes[:MaxPreviewChanges]
		preview.Truncated = true
		appendConflict(&preview, Conflict{Kind: ConflictUnrecoverable,
			Reason: "workspace restore exceeds the preview change limit"})
	}
	if len(preview.Conflicts) != 0 {
		preview.RecoveryLevel = RecoveryUnavailable
	}
	return preview, nil
}

// ApplyRestore applies a previously previewed three-way restore. Each path is
// checked again immediately before mutation, so a concurrent writer creates a
// conflict instead of being overwritten. A partially applied restore is safe
// to replay because target values are accepted alongside expected values.
func ApplyRestore(ctx context.Context, workspaceRoot string, expected, target,
	observed Snapshot,
) (RestoreResult, error) {
	return applyRestore(ctx, workspaceRoot, expected, target, observed, false)
}

// ApplyWorkspaceRestore is the mutation counterpart of PreviewWorkspaceRestore;
// its caller must hold the durable physical workspace mutation reservation.
func ApplyWorkspaceRestore(ctx context.Context, workspaceRoot string, expected, target,
	observed Snapshot,
) (RestoreResult, error) {
	return applyRestore(ctx, workspaceRoot, expected, target, observed, true)
}

func applyRestore(ctx context.Context, workspaceRoot string, expected, target,
	observed Snapshot, sameWorkspace bool,
) (RestoreResult, error) {
	preview, err := previewRestore(expected, target, observed, sameWorkspace)
	result := RestoreResult{Preview: preview, AppliedPaths: []string{}, DeletedPaths: []string{}}
	if err != nil {
		return result, err
	}
	if len(preview.Conflicts) != 0 {
		return result, &ConflictError{Conflicts: preview.Conflicts}
	}
	rootPath, err := canonicalRoot(workspaceRoot)
	if err != nil {
		return result, err
	}
	rootFingerprint, err := workspaceRootFingerprint(rootPath)
	if err != nil {
		return result, err
	}
	if rootFingerprint != expected.Checkpoint.RootFingerprint {
		conflict := Conflict{Kind: ConflictRootDrift,
			Reason: "workspace root identity changed before restore"}
		return result, &ConflictError{Conflicts: []Conflict{conflict}}
	}
	root, err := rootfs.OpenExactRoot(rootPath)
	if err != nil {
		return result, err
	}
	defer root.Close()

	var restoreIndex func() error
	if preview.IndexChanged {
		var releaseIndex func()
		restoreIndex, releaseIndex, err = prepareGitIndexRestore(ctx, rootPath, expected, target)
		if err != nil {
			return result, err
		}
		defer releaseIndex()
	}

	expectedEntries := entryMap(expected.Entries)
	targetEntries := entryMap(target.Entries)
	blobs := blobMap(target.Blobs)
	paths := unionPaths(expectedEntries, targetEntries, nil)
	for _, currentPath := range paths {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		from, fromOK := expectedEntries[currentPath]
		to, toOK := targetEntries[currentPath]
		if sameWorktreeEntry(from, fromOK, to, toOK) {
			continue
		}
		live, liveOK, inspectErr := inspectLiveEntry(ctx, root, currentPath)
		if inspectErr != nil {
			return result, inspectErr
		}
		if sameWorktreeEntry(live, liveOK, to, toOK) {
			continue
		}
		if !sameWorktreeEntry(live, liveOK, from, fromOK) {
			conflict := Conflict{Kind: ConflictExternalChange, Path: currentPath,
				ExpectedSHA256: entryStateDigest(from, fromOK),
				CurrentSHA256:  entryStateDigest(live, liveOK),
				TargetSHA256:   entryStateDigest(to, toOK),
				Reason:         "Workspace path changed after restore preview"}
			return result, &ConflictError{Conflicts: []Conflict{conflict}}
		}
		if restoreEntryAbsent(to, toOK) {
			if !entryCanDelete(from, fromOK) {
				conflict := Conflict{Kind: ConflictUnrecoverable, Path: currentPath,
					Reason: "checkpoint does not authorize deletion of this path"}
				return result, &ConflictError{Conflicts: []Conflict{conflict}}
			}
			if err := removeRootFile(root, currentPath); err != nil {
				return result, err
			}
			result.DeletedPaths = append(result.DeletedPaths, currentPath)
			continue
		}
		if !to.Recoverable || to.StoragePolicy != StorageStored || to.BlobSHA256 == "" {
			conflict := Conflict{Kind: ConflictUnrecoverable, Path: currentPath,
				Reason: "target checkpoint content is not available for restore"}
			return result, &ConflictError{Conflicts: []Conflict{conflict}}
		}
		blob, ok := blobs[to.BlobSHA256]
		if !ok {
			return result, errors.New("target checkpoint blob is missing")
		}
		if err := atomicRootWrite(root, currentPath, blob.Content, os.FileMode(to.Mode)); err != nil {
			return result, err
		}
		result.AppliedPaths = append(result.AppliedPaths, currentPath)
	}
	if restoreIndex != nil {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := restoreIndex(); err != nil {
			return result, err
		}
		result.IndexRestored = true
	}
	return result, nil
}

func workspaceRootFingerprint(root string) (string, error) {
	// Use the same device/path identity primitive as Capture; path text alone
	// is insufficient after a directory replacement.
	return workspaceidentity.Fingerprint(root)
}

func weakestRecovery(values ...RecoveryLevel) RecoveryLevel {
	result := RecoveryComplete
	for _, value := range values {
		if value == RecoveryUnavailable {
			return RecoveryUnavailable
		}
		if value == RecoveryPartial {
			result = RecoveryPartial
		}
	}
	return result
}

func entryMap(entries []Entry) map[string]Entry {
	result := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		result[entry.Path] = entry
	}
	return result
}

func blobMap(blobs []Blob) map[string]Blob {
	result := make(map[string]Blob, len(blobs))
	for _, blob := range blobs {
		result[blob.SHA256] = blob
	}
	return result
}

func unionPaths(first, second, third map[string]Entry) []string {
	seen := make(map[string]struct{}, len(first)+len(second)+len(third))
	for key := range first {
		seen[key] = struct{}{}
	}
	for key := range second {
		seen[key] = struct{}{}
	}
	for key := range third {
		seen[key] = struct{}{}
	}
	values := make([]string, 0, len(seen))
	for key := range seen {
		values = append(values, key)
	}
	sort.Strings(values)
	return values
}

func restoreEntryAbsent(entry Entry, ok bool) bool {
	return !ok || entry.State == StateMissing || entry.StoragePolicy == StorageMissing
}

func entryCanDelete(entry Entry, ok bool) bool {
	return !restoreEntryAbsent(entry, ok) && entry.Kind == EntryFile && entry.Recoverable
}

func sameWorktreeEntry(left Entry, leftOK bool, right Entry, rightOK bool) bool {
	leftAbsent := restoreEntryAbsent(left, leftOK)
	rightAbsent := restoreEntryAbsent(right, rightOK)
	if leftAbsent || rightAbsent {
		return leftAbsent == rightAbsent
	}
	if left.Kind != right.Kind || left.State != right.State ||
		left.WorktreeSHA256 != right.WorktreeSHA256 {
		return false
	}
	if left.Kind == EntryFile && left.Mode != right.Mode {
		return false
	}
	return true
}

func entryStateDigest(entry Entry, ok bool) string {
	if restoreEntryAbsent(entry, ok) {
		return "missing"
	}
	return entry.WorktreeSHA256
}

func validConflictValue(value string) bool {
	if value == "" || value == "missing" || value == "non-git" || value == "unborn" {
		return true
	}
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func describeChange(currentPath string, from Entry, fromOK bool, to Entry,
	toOK bool,
) Change {
	change := Change{Path: currentPath, FromSHA256: entryStateDigest(from, fromOK),
		ToSHA256: entryStateDigest(to, toOK), Binary: from.Binary || to.Binary,
		Recoverable: true}
	switch {
	case restoreEntryAbsent(from, fromOK) && !restoreEntryAbsent(to, toOK):
		change.Kind = ChangeCreate
		change.Recoverable = to.Recoverable && to.StoragePolicy == StorageStored
		change.Reason = to.Reason
	case !restoreEntryAbsent(from, fromOK) && restoreEntryAbsent(to, toOK):
		change.Kind = ChangeDelete
		change.Recoverable = entryCanDelete(from, fromOK)
		change.Reason = from.Reason
	case from.WorktreeSHA256 == to.WorktreeSHA256:
		change.Kind = ChangeMetadata
		change.Recoverable = to.Recoverable && to.StoragePolicy == StorageStored
		change.Reason = to.Reason
	default:
		change.Kind = ChangeModify
		change.Recoverable = to.Recoverable && to.StoragePolicy == StorageStored
		change.Reason = to.Reason
	}
	if !change.Recoverable && change.Reason == "" {
		change.Reason = "target checkpoint content is not recoverable"
	}
	return change
}

func foldRenames(changes []Change) []Change {
	deletes := make(map[string][]int)
	creates := make(map[string][]int)
	for index, change := range changes {
		switch change.Kind {
		case ChangeDelete:
			if change.FromSHA256 != "missing" {
				deletes[change.FromSHA256] = append(deletes[change.FromSHA256], index)
			}
		case ChangeCreate:
			if change.ToSHA256 != "missing" {
				creates[change.ToSHA256] = append(creates[change.ToSHA256], index)
			}
		}
	}
	removed := make(map[int]struct{})
	for digest, deleteIndexes := range deletes {
		createIndexes := creates[digest]
		if len(deleteIndexes) != 1 || len(createIndexes) != 1 {
			continue
		}
		deleteIndex, createIndex := deleteIndexes[0], createIndexes[0]
		created := changes[createIndex]
		deleted := changes[deleteIndex]
		created.Kind = ChangeRename
		created.PreviousPath = deleted.Path
		created.FromSHA256 = deleted.FromSHA256
		created.Recoverable = created.Recoverable && deleted.Recoverable
		changes[createIndex] = created
		removed[deleteIndex] = struct{}{}
	}
	result := make([]Change, 0, len(changes)-len(removed))
	for index, change := range changes {
		if _, ok := removed[index]; !ok {
			result = append(result, change)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path == result[j].Path {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Path < result[j].Path
	})
	return result
}

func appendConflict(preview *Preview, conflict Conflict) {
	if preview == nil || len(preview.Conflicts) >= MaxConflictItems {
		if preview != nil {
			preview.Truncated = true
		}
		return
	}
	preview.Conflicts = append(preview.Conflicts, conflict)
}

func inspectLiveEntry(ctx context.Context, root *rootfs.ExactRoot, relative string) (Entry, bool, error) {
	if err := inspectExactRootPath(root, relative, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Entry{}, false, nil
		}
		return Entry{}, false, err
	}
	info, err := root.Lstat(filepath.FromSlash(relative))
	if errors.Is(err, os.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, err
	}
	entry := Entry{Path: relative, Mode: uint32(info.Mode().Perm()), Size: info.Size(),
		State: StatePresent}
	if !info.Mode().IsRegular() {
		return Entry{}, false, &ConflictError{Conflicts: []Conflict{{
			Kind: ConflictRedirectedPath, Path: relative,
			Reason: "workspace path is not a regular file"}}}
	}
	if info.Size() > MaxStoredFileBytes {
		return Entry{}, false, &ConflictError{Conflicts: []Conflict{{
			Kind: ConflictExternalChange, Path: relative,
			Reason: "live Workspace file exceeds the checkpoint limit"}}}
	}
	digest, _, err := hashCaptureFile(ctx, root, relative, info, false)
	if err != nil {
		return Entry{}, false, err
	}
	entry.Kind = EntryFile
	entry.WorktreeSHA256 = digest
	return entry, true, nil
}

func inspectExactRootPath(root *rootfs.ExactRoot, relative string, createParents bool) error {
	components := strings.Split(filepath.ToSlash(relative), "/")
	current := "."
	for index, component := range components {
		directory, err := root.OpenReadOnly(current)
		if err != nil {
			return err
		}
		dirInfo, err := directory.Stat()
		if err != nil || !dirInfo.IsDir() {
			_ = directory.Close()
			return errors.Join(errors.New("workspace path parent is no longer a directory"), err)
		}
		entries, err := directory.ReadDir(-1)
		closeErr := directory.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		var exact os.DirEntry
		alias := false
		for _, entry := range entries {
			if entry.Name() == component {
				exact = entry
				break
			}
			if strings.EqualFold(entry.Name(), component) {
				alias = true
			}
		}
		last := index == len(components)-1
		current = filepath.Join(current, component)
		if exact == nil {
			if alias {
				return &ConflictError{Conflicts: []Conflict{{Kind: ConflictRedirectedPath,
					Path: relative, Reason: "workspace path casing changed"}}}
			}
			if createParents && !last {
				if err := root.Mkdir(current, 0o755); err != nil {
					return err
				}
				continue
			}
			return os.ErrNotExist
		}
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if (exact.Type()|info.Mode())&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return &ConflictError{Conflicts: []Conflict{{Kind: ConflictRedirectedPath,
				Path: relative, Reason: "workspace path contains a symlink or junction"}}}
		}
		if !last && !info.IsDir() {
			return &ConflictError{Conflicts: []Conflict{{Kind: ConflictRedirectedPath,
				Path: relative, Reason: "workspace path parent is not a directory"}}}
		}
	}
	return nil
}

func atomicRootWrite(root *rootfs.ExactRoot, relative string, content []byte, mode os.FileMode) error {
	parent := path.Dir(relative)
	if err := inspectExactRootPath(root, relative, true); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	for attempt := 0; attempt < 8; attempt++ {
		random := make([]byte, 12)
		if _, err := rand.Read(random); err != nil {
			return err
		}
		temporary := path.Join(parent, ".cyberagent-checkpoint-"+hex.EncodeToString(random))
		local := filepath.FromSlash(temporary)
		file, err := root.CreateExclusive(local, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		writeErr := writeAndSync(file, content)
		if writeErr == nil {
			writeErr = file.Chmod(mode.Perm())
		}
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			_ = root.Remove(local)
			return errors.Join(writeErr, closeErr)
		}
		if err := root.Rename(local, filepath.FromSlash(relative)); err != nil {
			_ = root.Remove(local)
			return err
		}
		return nil
	}
	return errors.New("workspace checkpoint temporary file allocation failed")
}

func removeRootFile(root *rootfs.ExactRoot, relative string) error {
	if err := inspectExactRootPath(root, relative, false); err != nil {
		return err
	}
	info, err := root.Lstat(filepath.FromSlash(relative))
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return &ConflictError{Conflicts: []Conflict{{Kind: ConflictRedirectedPath,
			Path: relative, Reason: "restore deletes regular files only"}}}
	}
	return root.Remove(filepath.FromSlash(relative))
}

func writeAndSync(file *os.File, content []byte) error {
	if file == nil {
		return errors.New("workspace checkpoint staging handle is absent")
	}
	if _, err := file.Write(content); err != nil {
		return err
	}
	return file.Sync()
}

func prepareGitIndexRestore(ctx context.Context, root string, expected, target Snapshot,
) (commit func() error, release func(), err error) {
	targetPresent := target.Checkpoint.gitIndexPresent()
	targetContent := []byte(nil)
	if target.Checkpoint.IndexBlobSHA256 != "" {
		targetBlob, ok := blobMap(target.Blobs)[target.Checkpoint.IndexBlobSHA256]
		if !ok {
			return nil, nil, errors.New("target Git index blob is missing")
		}
		targetContent = targetBlob.Content
	} else if targetPresent {
		return nil, nil, &ConflictError{Conflicts: []Conflict{{Kind: ConflictUnrecoverable,
			Reason: "target Git index blob is unavailable"}}}
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return nil, nil, err
	}
	top, err := captureGitOutput(ctx, gitPath, root, "rev-parse", "--show-toplevel")
	if err != nil || !sameCapturePath(strings.TrimRight(string(top), "\r\n"), root) {
		return nil, nil, errors.Join(errors.New("Git worktree identity changed before index restore"), err)
	}
	indexPath, err := gitIndexPath(ctx, gitPath, root)
	if err != nil {
		return nil, nil, err
	}
	return stageGitIndexRestore(indexPath, expected.Checkpoint.IndexSHA256,
		expected.Checkpoint.gitIndexPresent(), target.Checkpoint.IndexSHA256, targetPresent, targetContent)
}

// Hold Git's index lock across the worktree writes. Any existing lock, index
// drift, or staging failure must stop restore before it changes files.
func stageGitIndexRestore(target, expectedDigest string, expectedPresent bool,
	targetDigest string, targetPresent bool, content []byte,
) (commit func() error, release func(), err error) {
	clean := filepath.Clean(target)
	parent := filepath.Dir(clean)
	name := filepath.Base(clean)
	if name == "." || name == string(filepath.Separator) ||
		!sameCapturePath(filepath.Join(parent, name), clean) {
		return nil, nil, errors.New("git index path is invalid")
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.Join(errors.New("git index parent is redirected or unavailable"), err)
	}
	resolved, err := rootfs.EvalExactSymlinks(parent)
	if err != nil || !sameCapturePath(parent, resolved) {
		return nil, nil, errors.Join(errors.New("git index parent is redirected"), err)
	}
	directory, err := rootfs.OpenExactRoot(parent)
	if err != nil {
		return nil, nil, err
	}
	var lock *os.File
	lockName := name + ".lock"
	lockOwned := false
	cleanup := func() {
		if lock != nil {
			_ = lock.Close()
		}
		if lockOwned {
			_ = directory.Remove(lockName)
		}
		_ = directory.Close()
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	openedInfo, err := directory.Stat(".")
	if err != nil || !os.SameFile(parentInfo, openedInfo) {
		return nil, nil, errors.Join(errors.New("git index parent changed during restore"), err)
	}
	if err := inspectExactRootPath(directory, name, false); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	mode := os.FileMode(0o600)
	if info, statErr := directory.Lstat(name); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, errors.New("git index is not a regular file")
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, nil, statErr
	}
	lock, err = directory.CreateExclusive(lockName, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, nil, &ConflictError{Conflicts: []Conflict{{Kind: ConflictIndexDrift,
			ExpectedSHA256: expectedDigest, TargetSHA256: targetDigest,
			Reason: "Git index is locked by another operation"}}}
	}
	if err != nil {
		return nil, nil, err
	}
	lockOwned = true
	current, currentPresent, err := readGitIndexUnderLock(directory, name)
	if err != nil {
		return nil, nil, err
	}
	currentSHA := sha256.Sum256(current)
	currentDigest := hex.EncodeToString(currentSHA[:])
	if currentDigest == targetDigest && currentPresent == targetPresent {
		return func() error { return nil }, cleanup, nil
	}
	if currentDigest != expectedDigest || currentPresent != expectedPresent {
		return nil, nil, &ConflictError{Conflicts: []Conflict{{Kind: ConflictIndexDrift,
			ExpectedSHA256: expectedDigest, CurrentSHA256: currentDigest,
			TargetSHA256: targetDigest,
			Reason:       "Git index changed after restore preview"}}}
	}
	if !targetPresent {
		return func() error { return directory.Remove(name) }, cleanup, nil
	}
	if err := writeAndSync(lock, content); err != nil {
		return nil, nil, err
	}
	if err := lock.Chmod(mode); err != nil {
		return nil, nil, err
	}
	if err := lock.Close(); err != nil {
		return nil, nil, err
	}
	lock = nil
	return func() error {
		if err := directory.Rename(lockName, name); err != nil {
			return err
		}
		lockOwned = false
		return nil
	}, cleanup, nil
}

func readGitIndexUnderLock(root *rootfs.ExactRoot, name string) ([]byte, bool, error) {
	file, err := root.OpenReadOnly(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, MaxStoredIndexBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(content) > MaxStoredIndexBytes {
		return nil, false, &ConflictError{Conflicts: []Conflict{{Kind: ConflictIndexDrift,
			Reason: "live Git index exceeds the checkpoint limit"}}}
	}
	return content, true, nil
}
