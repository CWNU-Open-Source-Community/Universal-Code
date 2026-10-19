package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

type WorkspaceProjectView struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	CreatedAt         time.Time `json:"created_at"`
	DirectoryPath     string    `json:"directory_path"`
	ThreadCount       int64     `json:"thread_count"`
	ActiveThreadCount int64     `json:"active_thread_count"`
}
type WorkspaceNameRequestView struct {
	Name string `json:"name"`
}
type workspaceProjectStore interface {
	GetWorkspaceByID(context.Context, string) (session.WorkspaceRecord, error)
	RenameWorkspace(context.Context, string, string) (session.WorkspaceRecord, error)
	WorkspaceThreadCounts(context.Context, string) (int64, int64, error)
	IsProjectlessWorkspace(context.Context, string) (bool, error)
}

func matchWorkspaceProjectPath(path string) (string, string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/workspaces/"), "/")
	if !strings.HasPrefix(path, "/api/v1/workspaces/") || len(parts) != 2 || !domain.ValidAgentID(parts[0]) || (parts[1] != "project" && parts[1] != "name") {
		return "", "", false
	}
	return parts[0], parts[1], true
}
func (a *API) serveWorkspaceProject(w http.ResponseWriter, r *http.Request, requestID, id, action string) {
	if !a.authorizeThreadControl(w, r, requestID) {
		return
	}
	method := http.MethodGet
	if action == "name" {
		method = http.MethodPut
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		a.writeError(w, requestID, apperror.New(apperror.CodeInvalidArgument, "unsupported project method"), http.StatusMethodNotAllowed)
		return
	}
	if err := rejectQuery(r.URL.Query()); err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	st, ok := a.store.(workspaceProjectStore)
	if !ok {
		a.writeError(w, requestID, apperror.New(apperror.CodeUnavailable, "Project metadata is unavailable"), 0)
		return
	}
	local, err := st.IsProjectlessWorkspace(r.Context(), id)
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if local {
		a.writeError(w, requestID, apperror.New(apperror.CodeNotFound, "Project was not found"), 0)
		return
	}
	record, err := st.GetWorkspaceByID(r.Context(), id)
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	if action == "name" {
		var body WorkspaceNameRequestView
		if !a.decodeThreadControlBody(w, r, requestID, "project name", &body) {
			return
		}
		record, err = st.RenameWorkspace(r.Context(), id, body.Name)
		if err != nil {
			a.writeError(w, requestID, err, 0)
			return
		}
	}
	total, active, err := st.WorkspaceThreadCounts(r.Context(), id)
	if err != nil {
		a.writeError(w, requestID, err, 0)
		return
	}
	a.writeSuccess(w, requestID, WorkspaceProjectView{ID: record.ID, Name: record.Name, CreatedAt: record.CreatedAt, DirectoryPath: record.RootPath, ThreadCount: total, ActiveThreadCount: active}, nil)
}
