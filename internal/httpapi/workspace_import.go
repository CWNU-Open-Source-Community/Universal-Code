package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/workspace"
)

const (
	WorkspaceImportProtocolVersion     = "workspace_import.v1"
	WorkspaceImportPath                = "/api/v1/workspaces/import"
	MaxWorkspaceImportDirectoryBytes   = 4096
	MaxWorkspaceImportRequestBodyBytes = 32 * 1024
)

type WorkspaceImporter interface {
	Import(context.Context, string) (session.WorkspaceRecord, error)
}

type WorkspaceImportRequestView struct {
	Name          *string `json:"name,omitempty"`
	Version       string  `json:"version"`
	DirectoryPath string  `json:"directory_path"`
	Confirmed     bool    `json:"confirmed"`
}

type WorkspaceImportView struct {
	ProtocolVersion          string        `json:"protocol_version"`
	Workspace                WorkspaceView `json:"workspace"`
	DirectoryContentModified bool          `json:"directory_content_modified"`
	AgentAuthorityGranted    bool          `json:"agent_authority_granted"`
}

// This optional HTTP capability is composed only by api serve. Desktop keeps its
// pathless native picker; enabling Run creation does not enable path input.
func (a *API) serveWorkspaceImport(writer http.ResponseWriter, request *http.Request,
	requestID string) {
	if !a.workspaceImportEnabled || a.workspaceImporter == nil {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound,
			"HTTP API endpoint was not found"), http.StatusNotFound)
		return
	}
	if !a.authorized(request, a.controlTokenHash) {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent Control API"`)
		a.writeError(writer, requestID, apperror.New(apperror.CodePolicyDenied,
			"valid control bearer authorization is required"), http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"workspace import only supports POST"), http.StatusMethodNotAllowed)
		return
	}
	if err := rejectQuery(request.URL.Query()); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if err := validateJSONContentType(request.Header); err != nil {
		a.writeError(writer, requestID, err, http.StatusUnsupportedMediaType)
		return
	}
	body, err := readBoundedRequestBody(request, MaxWorkspaceImportRequestBodyBytes)
	if err != nil {
		status := 0
		if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeResourceExhausted {
			status = http.StatusRequestEntityTooLarge
		}
		a.writeError(writer, requestID, err, status)
		return
	}
	invalid := func() {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"workspace import requires a confirmed absolute existing directory on the server computer"), 0)
	}
	if !utf8.Valid(body) || rejectDuplicateJSONObjectFields(body, "workspace import") != nil {
		invalid()
		return
	}
	var view WorkspaceImportRequestView
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&view) != nil || ensureJSONEOF(decoder) != nil ||
		view.Version != WorkspaceImportProtocolVersion || !view.Confirmed ||
		strings.TrimSpace(view.DirectoryPath) == "" ||
		len(view.DirectoryPath) > MaxWorkspaceImportDirectoryBytes ||
		strings.IndexFunc(view.DirectoryPath, unicode.IsControl) >= 0 {
		invalid()
		return
	}
	var record session.WorkspaceRecord
	if view.Name != nil {
		name, nameErr := domain.NormalizeWorkspaceName(*view.Name)
		if nameErr != nil {
			a.writeError(writer, requestID, nameErr, 0)
			return
		}
		named, ok := a.workspaceImporter.(interface {
			ImportNamed(context.Context, string, string) (session.WorkspaceRecord, error)
		})
		if !ok {
			a.writeError(writer, requestID, apperror.New(apperror.CodeUnavailable, "Named project import is unavailable"), 0)
			return
		}
		record, err = named.ImportNamed(request.Context(), view.DirectoryPath, name)
	} else {
		record, err = a.workspaceImporter.Import(request.Context(), view.DirectoryPath)
	}
	if apperror.CodeOf(apperror.Normalize(err)) == apperror.CodeConflict {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if errors.Is(err, workspace.ErrInvalidImportDirectory) {
		invalid()
		return
	}
	if err != nil {
		// Neither database diagnostics nor an operator-entered host path belongs
		// in a public error response. A retry reuses the canonical directory ID.
		a.writeError(writer, requestID, apperror.New(apperror.CodeUnavailable,
			"workspace directory registration failed; retry the same directory"), 0)
		return
	}
	a.writeSuccess(writer, requestID, WorkspaceImportView{
		ProtocolVersion:          WorkspaceImportProtocolVersion,
		Workspace:                WorkspaceView{ID: record.ID, Name: workspace.ImportDisplayName(record.Name), CreatedAt: record.CreatedAt},
		DirectoryContentModified: false, AgentAuthorityGranted: false,
	}, nil)
}
