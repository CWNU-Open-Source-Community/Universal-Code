package desktop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/workspace"
)

type WorkspaceDirectorySelection struct {
	Handle        string `json:"handle"`
	DirectoryPath string `json:"directory_path"`
	Name          string `json:"name"`
}
type WorkspaceDirectorySelectionResult struct {
	Status    string                       `json:"status"`
	Selection *WorkspaceDirectorySelection `json:"selection"`
}
type CreateWorkspaceRequest struct {
	Handle string `json:"handle"`
	Name   string `json:"name"`
}
type pendingWorkspaceDirectory struct {
	selection WorkspaceDirectorySelection
	info      os.FileInfo
	expires   time.Time
	result    *WorkspaceImportSummary
	name      string
}
type namedWorkspaceRegistrar interface {
	RegisterNamedWorkspaceDirectory(context.Context, string, string) (WorkspaceImportSummary, error)
}

// Selection grants one bounded native handle; it never registers a workspace.
func (b *DesktopBridge) SelectWorkspaceDirectory() (WorkspaceDirectorySelectionResult, error) {
	if b == nil || !b.bootstrap.WorkspaceImportEnabled || b.workspaceDirectoryPicker == nil {
		return WorkspaceDirectorySelectionResult{}, apperror.New(apperror.CodeNotFound, "Native project selection is unavailable")
	}
	if !b.dialogActive.CompareAndSwap(false, true) {
		return WorkspaceDirectorySelectionResult{}, apperror.New(apperror.CodeResourceExhausted, "A native dialog is already active")
	}
	defer b.dialogActive.Store(false)
	ctx, err := b.lifecycleContext()
	if err != nil {
		return WorkspaceDirectorySelectionResult{}, err
	}
	path, err := b.workspaceDirectoryPicker.OpenWorkspaceDirectory(ctx)
	if err != nil {
		return WorkspaceDirectorySelectionResult{}, apperror.New(apperror.CodeUnavailable, "Native directory selection failed")
	}
	if err = ctx.Err(); err != nil {
		return WorkspaceDirectorySelectionResult{}, err
	}
	if path == "" {
		return WorkspaceDirectorySelectionResult{Status: "cancelled"}, nil
	}
	path, err = workspace.ResolveImportDirectory(path)
	if err != nil {
		return WorkspaceDirectorySelectionResult{}, apperror.New(apperror.CodeInvalidArgument, "Selected directory is unavailable")
	}
	info, err := workspaceDirectoryIdentity(path)
	if err != nil {
		return WorkspaceDirectorySelectionResult{}, apperror.New(apperror.CodeInvalidArgument, "Selected directory is unavailable")
	}
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return WorkspaceDirectorySelectionResult{}, err
	}
	selection := WorkspaceDirectorySelection{Handle: hex.EncodeToString(random[:]), DirectoryPath: path, Name: workspace.ImportDisplayName(filepath.Base(path))}
	b.workspaceSelectionMu.Lock()
	b.workspaceSelection = &pendingWorkspaceDirectory{selection: selection, info: info, expires: time.Now().Add(20 * time.Minute)}
	b.workspaceSelectionMu.Unlock()
	return WorkspaceDirectorySelectionResult{Status: "selected", Selection: &selection}, nil
}
func (b *DesktopBridge) CreateWorkspace(request CreateWorkspaceRequest) (WorkspaceImportSummary, error) {
	if b == nil || !b.bootstrap.WorkspaceImportEnabled {
		return WorkspaceImportSummary{}, apperror.New(apperror.CodeNotFound, "Native project creation is unavailable")
	}
	registrar, ok := b.workspaceRegistrar.(namedWorkspaceRegistrar)
	if !ok {
		return WorkspaceImportSummary{}, apperror.New(apperror.CodeNotFound, "Native project creation is unavailable")
	}
	name, err := domain.NormalizeWorkspaceName(request.Name)
	if err != nil {
		return WorkspaceImportSummary{}, err
	}
	ctx, err := b.lifecycleContext()
	if err != nil {
		return WorkspaceImportSummary{}, err
	}
	b.workspaceSelectionMu.Lock()
	defer b.workspaceSelectionMu.Unlock()
	pending := b.workspaceSelection
	if pending == nil || len(request.Handle) != 64 || request.Handle != pending.selection.Handle || time.Now().After(pending.expires) {
		return WorkspaceImportSummary{}, apperror.New(apperror.CodeFailedPrecondition, "Select the project folder again")
	}
	if pending.result != nil {
		if pending.name != name {
			return WorkspaceImportSummary{}, apperror.New(apperror.CodeConflict, "This selection already created a project with another name")
		}
		return *pending.result, nil
	}
	info, err := workspaceDirectoryIdentity(pending.selection.DirectoryPath)
	if err != nil || !info.IsDir() || !os.SameFile(pending.info, info) {
		return WorkspaceImportSummary{}, apperror.New(apperror.CodeFailedPrecondition, "Selected directory changed; select it again")
	}
	result, err := registrar.RegisterNamedWorkspaceDirectory(ctx, pending.selection.DirectoryPath, name)
	if err != nil {
		return WorkspaceImportSummary{}, err
	}
	pending.result = &result
	pending.name = name
	return result, nil
}
func (c *ControlPlane) RegisterNamedWorkspaceDirectory(ctx context.Context, path, name string) (WorkspaceImportSummary, error) {
	if c == nil || c.workspaceManager == nil {
		return WorkspaceImportSummary{}, apperror.New(apperror.CodeUnavailable, "Project registration is unavailable")
	}
	c.workspaceImportMu.Lock()
	defer c.workspaceImportMu.Unlock()
	record, err := c.workspaceManager.ImportNamed(ctx, path, name)
	if err != nil {
		code := apperror.CodeOf(apperror.Normalize(err))
		if code != apperror.CodeConflict && code != apperror.CodeInvalidArgument {
			code = apperror.CodeUnavailable
		}
		return WorkspaceImportSummary{}, apperror.New(code, "Project registration failed; check its name and selected directory")
	}
	return WorkspaceImportSummary{ID: record.ID, Name: workspace.ImportDisplayName(record.Name), CreatedAt: record.CreatedAt}, nil
}

// File.Stat pins Windows volume/file IDs immediately; os.Stat may resolve them
// lazily by pathname after a selected directory has already been replaced.
func workspaceDirectoryIdentity(path string) (os.FileInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Stat()
}
