package httpapi

import (
	"net/http"
	"strings"
	"unicode"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/workspace"
)

const ProjectlessWorkspacePath = "/api/v1/workspaces/projectless"
const ProjectlessDirectoryPath = "/api/v1/settings/projectless-directory"

type ProjectlessWorkspaceView struct {
	Version   string        `json:"version"`
	Workspace WorkspaceView `json:"workspace"`
	Replayed  bool          `json:"replayed"`
}

type ProjectlessWorkspaceRequestView struct {
	Version string `json:"version"`
}
type ProjectlessDirectoryRequestView struct {
	Version       string `json:"version"`
	DirectoryPath string `json:"directory_path"`
	Confirmed     bool   `json:"confirmed"`
}

func (a *API) projectlessManager() (workspace.ProjectlessManager, bool) {
	st, ok := a.store.(workspace.ProjectlessStore)
	return workspace.ProjectlessManager{Store: st}, ok && a.runCreationEnabled && a.sessionMessageEnabled
}

func projectlessEnabled(a *API) bool { _, enabled := a.projectlessManager(); return enabled }

func (a *API) serveProjectlessWorkspace(writer http.ResponseWriter, request *http.Request, requestID string) {
	manager, enabled := a.projectlessManager()
	if !enabled {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "local chat directories are unavailable"), 0)
		return
	}
	if !a.authorizeThreadControl(writer, request, requestID) {
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "chat directory allocation requires POST"), http.StatusMethodNotAllowed)
		return
	}
	key, err := runCreationIdempotencyKey(request.Header)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var body ProjectlessWorkspaceRequestView
	if !a.decodeThreadControlBody(writer, request, requestID, "chat directory allocation", &body) {
		return
	}
	if body.Version != "projectless_workspace.v1" {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "unsupported chat directory version"), 0)
		return
	}
	record, replayed, err := manager.Allocate(request.Context(), key)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccess(writer, requestID, ProjectlessWorkspaceView{Version: body.Version, Workspace: WorkspaceView{ID: record.ID, Name: record.Name, CreatedAt: record.CreatedAt}, Replayed: replayed}, nil)
}

func (a *API) serveProjectlessDirectory(writer http.ResponseWriter, request *http.Request, requestID string) {
	manager, enabled := a.projectlessManager()
	if !enabled {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "local chat directory settings are unavailable"), 0)
		return
	}
	// A root path is operator configuration: a read bearer never receives it.
	if !a.authorizeThreadControl(writer, request, requestID) {
		return
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	switch request.Method {
	case http.MethodGet:
		view, err := manager.Directory(request.Context())
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccess(writer, requestID, view, nil)
	case http.MethodPut:
		if !a.workspaceImportEnabled {
			a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "server directory selection is disabled"), 0)
			return
		}
		var body ProjectlessDirectoryRequestView
		if !a.decodeThreadControlBody(writer, request, requestID, "chat directory settings", &body) {
			return
		}
		if body.Version != "projectless_directory.v1" || !body.Confirmed ||
			strings.TrimSpace(body.DirectoryPath) == "" || len(body.DirectoryPath) > MaxWorkspaceImportDirectoryBytes ||
			strings.IndexFunc(body.DirectoryPath, unicode.IsControl) >= 0 {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "a confirmed absolute existing server directory is required"), 0)
			return
		}
		view, err := manager.SetDirectory(request.Context(), body.DirectoryPath)
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccess(writer, requestID, view, nil)
	default:
		writer.Header().Set("Allow", "GET, PUT")
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "chat directory settings support GET and PUT"), http.StatusMethodNotAllowed)
	}
}
