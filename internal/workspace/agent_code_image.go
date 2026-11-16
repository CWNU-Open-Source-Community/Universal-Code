package workspace

import (
	"io"
	"net/http"
	"os"
	"path/filepath"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/imageattachment"
)

// AgentCodeReadImage applies the same exclusions and redirect checks as text
// reads, then opens through os.Root so a path swap cannot escape the workspace.
func AgentCodeReadImage(root, requested string, policy AgentCodePathPolicy) (domain.WorkspaceImage, []byte, error) {
	w, err := openAgentWorkspace(root, policy)
	if err != nil {
		return domain.WorkspaceImage{}, nil, err
	}
	relative, err := normalizeAgentPath(requested, false)
	if err != nil {
		return domain.WorkspaceImage{}, nil, err
	}
	if w.blocked(relative, false, false) {
		return domain.WorkspaceImage{}, nil, apperror.New(apperror.CodePolicyDenied, "workspace image is hidden, ignored, or excluded")
	}
	_, expected, err := w.resolve(relative, false)
	if err != nil {
		return domain.WorkspaceImage{}, nil, err
	}
	if !expected.Mode().IsRegular() || expected.Size() > imageattachment.MaxBytes {
		return domain.WorkspaceImage{}, nil, apperror.New(apperror.CodeInvalidArgument, "workspace image must be a regular file of at most 5 MiB")
	}
	openedRoot, err := os.OpenRoot(w.root)
	if err != nil {
		return domain.WorkspaceImage{}, nil, err
	}
	defer openedRoot.Close()
	file, err := openedRoot.Open(filepath.FromSlash(relative))
	if err != nil {
		return domain.WorkspaceImage{}, nil, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || !os.SameFile(expected, before) {
		return domain.WorkspaceImage{}, nil, apperror.New(apperror.CodeConflict, "workspace image changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, imageattachment.MaxBytes+1))
	if err != nil {
		return domain.WorkspaceImage{}, nil, err
	}
	after, err := file.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return domain.WorkspaceImage{}, nil, apperror.New(apperror.CodeConflict, "workspace image changed while reading")
	}
	mimeType := http.DetectContentType(data)
	if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/webp" {
		return domain.WorkspaceImage{}, nil, apperror.New(apperror.CodeInvalidArgument, "workspace_view_image supports PNG, JPEG, or WebP; render SVG/HTML with browser tools")
	}
	metadata, err := imageattachment.Validate(data, mimeType, filepath.Base(relative))
	return metadata, data, err
}
