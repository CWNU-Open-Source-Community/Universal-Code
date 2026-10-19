package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/session"
)

const ProjectlessDirectorySetting = "workspace.projectless_directory.v1"

type ProjectlessStore interface {
	GetWorkspaceByID(context.Context, string) (session.WorkspaceRecord, error)
	GetProviderSetting(context.Context, string) (string, bool, error)
	SetProviderSetting(context.Context, string, string) error
	ReserveProjectlessWorkspace(context.Context, string, session.WorkspaceRecord) (session.WorkspaceRecord, bool, error)
	CompleteProjectlessWorkspace(context.Context, string, session.WorkspaceRecord) error
}

type ProjectlessDirectory struct {
	Version              string `json:"version"`
	DirectoryPath        string `json:"directory_path"`
	DefaultDirectoryPath string `json:"default_directory_path"`
}

type ProjectlessManager struct{ Store ProjectlessStore }

func (m ProjectlessManager) Directory(ctx context.Context) (ProjectlessDirectory, error) {
	defaultRoot, err := DefaultProjectlessDirectory()
	if err != nil {
		return ProjectlessDirectory{}, apperror.Wrap(apperror.CodeUnavailable, "user documents directory is unavailable", err)
	}
	root, found, err := m.Store.GetProviderSetting(ctx, ProjectlessDirectorySetting)
	if err != nil {
		return ProjectlessDirectory{}, err
	}
	if !found {
		root = defaultRoot
	}
	if !filepath.IsAbs(root) {
		return ProjectlessDirectory{}, apperror.New(apperror.CodeFailedPrecondition, "saved chat directory must be an absolute path")
	}
	return ProjectlessDirectory{Version: "projectless_directory.v1", DirectoryPath: root, DefaultDirectoryPath: defaultRoot}, nil
}

// SetDirectory accepts only a directory selected by a native picker or already
// imported through the separately enabled server-directory control capability.
func (m ProjectlessManager) SetDirectory(ctx context.Context, selectedPath string) (ProjectlessDirectory, error) {
	root, err := canonicalImportRoot(selectedPath)
	if err != nil {
		return ProjectlessDirectory{}, apperror.Wrap(apperror.CodeInvalidArgument, "selected chat directory is unavailable", err)
	}
	if err := m.Store.SetProviderSetting(ctx, ProjectlessDirectorySetting, root); err != nil {
		return ProjectlessDirectory{}, err
	}
	return m.Directory(ctx)
}

func (m ProjectlessManager) Allocate(ctx context.Context, operationKey string) (session.WorkspaceRecord, bool, error) {
	if operationKey == "" {
		return session.WorkspaceRecord{}, false, apperror.New(apperror.CodeInvalidArgument, "chat directory operation key is required")
	}
	directory, err := m.Directory(ctx)
	if err != nil {
		return session.WorkspaceRecord{}, false, err
	}
	digest := sha256.Sum256([]byte("projectless_workspace.v1\x00" + operationKey))
	key := hex.EncodeToString(digest[:])
	id := "ws-chat-" + key[:32]
	record := session.WorkspaceRecord{ID: id, Name: "chat-" + key[:32], RootPath: filepath.Join(directory.DirectoryPath, "chat-"+key[:32]), CreatedAt: time.Now().UTC()}
	// Persist the original directory before touching disk. A failed attempt and
	// a later retry retain this exact destination even after settings change.
	record, replayed, err := m.Store.ReserveProjectlessWorkspace(ctx, key, record)
	if err != nil {
		return session.WorkspaceRecord{}, false, err
	}
	if err = os.MkdirAll(record.RootPath, 0o700); err != nil {
		return session.WorkspaceRecord{}, false, apperror.Wrap(apperror.CodeUnavailable, "chat working directory could not be created", err)
	}
	resolved, err := canonicalImportRoot(record.RootPath)
	if err != nil {
		return session.WorkspaceRecord{}, false, apperror.Wrap(apperror.CodeUnavailable, "chat working directory could not be opened", err)
	}
	if !sameWorkspaceRoot(resolved, record.RootPath) {
		return session.WorkspaceRecord{}, false, apperror.New(apperror.CodeFailedPrecondition, "chat working directory no longer resolves to its reserved path")
	}
	if err := m.Store.CompleteProjectlessWorkspace(ctx, key, record); err != nil {
		return session.WorkspaceRecord{}, false, err
	}
	return record, replayed, nil
}

func DefaultProjectlessDirectory() (string, error) {
	documents, err := userDocumentsDirectory()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(documents) {
		return "", errors.New("documents directory must be absolute")
	}
	return filepath.Join(documents, "Universal-Code"), nil
}
