package application

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
)

type fileAttachmentInputStore interface {
	ListSupervisorFileAttachmentInputs(context.Context, domain.SupervisorCheckpoint) (domain.FileAttachmentInputSet, error)
	GetSupervisorCheckpoint(context.Context, string) (domain.SupervisorCheckpoint, bool, error)
	GetWorkspaceFileAttachment(context.Context, string, string) (domain.WorkspaceFileAttachment, []byte, error)
	FileAttachmentInputDirectory() string
}

func supportsOriginalAttachmentInputs(adapter commandruntimeadapter.Identity) bool {
	return adapter.Executable() && (adapter.Kind == commandruntimeadapter.KindHostUnsandboxed ||
		adapter.Backend == CommandRuntimeLocalSandboxBackend)
}

// Binding runs only after loadAuthorizedBindings, and never upgrades a profile,
// grants a lease, or copies anything into either source or execution Workspace.
func (s *CommandRuntimeService) bindOriginalAttachmentInputs(ctx context.Context, bindings commandRuntimeBindings, resolved runner.CommandRuntimeResolvedSpec) (runner.CommandRuntimeResolvedSpec, error) {
	store, ok := s.store.(fileAttachmentInputStore)
	if !ok {
		return resolved, nil
	}
	cp, found, err := store.GetSupervisorCheckpoint(ctx, bindings.run.ID)
	if err != nil {
		return resolved, err
	}
	if !found {
		return resolved, nil
	}
	set, err := store.ListSupervisorFileAttachmentInputs(ctx, cp)
	if err != nil || len(set.Files) == 0 {
		return resolved, err
	}
	if cp.LeaseID != bindings.lease.LeaseID || cp.LeaseGeneration != bindings.lease.Generation {
		return resolved, apperror.New(apperror.CodeConflict, "Attachment input lease changed before command execution")
	}
	if !supportsOriginalAttachmentInputs(s.adapter) {
		// Other sandbox adapters do not mount this host directory. Do not inject
		// a false path; their existing tools can still run without attachment use.
		return resolved, nil
	}
	if set.RunID != bindings.run.ID || set.SessionID != bindings.run.SessionID || set.WorkspaceID != bindings.workspace.ID {
		return resolved, apperror.New(apperror.CodeConflict, "Attachment input task scope changed")
	}
	input, err := materializeOriginalAttachmentInputs(ctx, store, set, bindings.workspace.RootPath, bindings.rootPath)
	if err != nil {
		return resolved, err
	}
	return runner.BindCommandRuntimeAttachmentInput(resolved, input)
}

// The directory is an immutable cache of exact sent bytes. It can be reused
// across a process restart, but every execution rechecks the database receipt
// and every cached byte; a changed/extra file fails closed and is never repaired
// behind an approved command's back. The database remains the original ledger.
func materializeOriginalAttachmentInputs(ctx context.Context, store fileAttachmentInputStore, set domain.FileAttachmentInputSet, workspaceRoots ...string) (runner.CommandRuntimeAttachmentInput, error) {
	var input runner.CommandRuntimeAttachmentInput
	if len(set.Files) == 0 || len(set.Files) > domain.MaxRunFileAttachmentInputs || !domain.ValidAgentID(set.RunID) {
		return input, apperror.New(apperror.CodeInvalidArgument, "Attachment input manifest is invalid")
	}
	body, digest := set.Manifest()
	base := filepath.Clean(store.FileAttachmentInputDirectory())
	if !filepath.IsAbs(base) {
		return input, runner.ErrCommandRuntimeBoundary
	}
	for _, workspace := range workspaceRoots {
		root, err := filepath.Abs(workspace)
		if err != nil || attachmentPathsOverlap(base, root) {
			return input, runner.ErrCommandRuntimeBoundary
		}
	}
	// Check the already-existing private home before creating any descendants.
	if err := requirePlainAttachmentDirectory(filepath.Dir(base)); err != nil {
		return input, err
	}
	root := filepath.Join(base, set.RunID, digest)
	for _, directory := range []string{base, filepath.Dir(root), root} {
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return input, err
		}
		if err := requirePlainAttachmentDirectory(directory); err != nil {
			return input, err
		}
	}
	expected := map[string]bool{".": true, "manifest.json": true}
	for _, file := range set.Files {
		if err := ctx.Err(); err != nil {
			return input, err
		}
		metadata, raw, err := store.GetWorkspaceFileAttachment(ctx, file.WorkspaceID, file.ID)
		if err != nil {
			return input, err
		}
		if domain.NewFileAttachmentInput(metadata) != file {
			return input, apperror.New(apperror.CodeConflict, "Original attachment identity changed before execution")
		}
		relative := filepath.FromSlash(file.RelativePath)
		if !filepath.IsLocal(relative) || filepath.Dir(relative) != file.ID || !domain.ValidAgentID(file.ID) {
			return input, runner.ErrCommandRuntimeBoundary
		}
		directory := filepath.Join(root, file.ID)
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return input, err
		}
		if err := requirePlainAttachmentDirectory(directory); err != nil {
			return input, err
		}
		if err := writeOrVerifyAttachmentInput(filepath.Join(root, relative), raw); err != nil {
			return input, err
		}
		expected[file.ID], expected[relative] = true, true
	}
	if err := writeOrVerifyAttachmentInput(filepath.Join(root, "manifest.json"), body); err != nil {
		return input, err
	}
	if err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, name)
		if err != nil || !expected[relative] || entry.Type()&os.ModeSymlink != 0 {
			return apperror.New(apperror.CodeConflict, "Attachment input cache contains unexpected paths")
		}
		return nil
	}); err != nil {
		return input, err
	}
	return runner.CommandRuntimeAttachmentInput{Root: root, ManifestSHA256: digest}, nil
}

func requirePlainAttachmentDirectory(name string) error {
	actual, err := filepath.EvalSymlinks(name)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(actual), filepath.Clean(name)) {
		return runner.ErrCommandRuntimeBoundary
	}
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return runner.ErrCommandRuntimeBoundary
	}
	return nil
}

func attachmentPathsOverlap(a, b string) bool {
	if left, right := filepath.VolumeName(a), filepath.VolumeName(b); left != "" && right != "" && !strings.EqualFold(left, right) {
		return false
	}
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

func writeOrVerifyAttachmentInput(name string, raw []byte) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err == nil {
		_, writeErr := file.Write(raw)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return errors.Join(writeErr, closeErr)
		}
	} else if !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(raw)) {
		return apperror.New(apperror.CodeConflict, "Original attachment cache integrity failed")
	}
	actual, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, raw) {
		return apperror.New(apperror.CodeConflict, "Original attachment cache bytes changed")
	}
	return nil
}
