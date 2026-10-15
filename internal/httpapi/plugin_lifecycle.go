package httpapi

import (
	"context"
	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/plugins"
	"net/http"
	"strings"
	"time"
)

const (
	ExtensionPluginHistoryPath             = "/api/v1/extensions/plugins/{installation_id}/history"
	ExtensionPluginRollbackPath            = "/api/v1/extensions/plugins/{installation_id}/rollback"
	ExtensionPluginPublisherRevocationPath = "/api/v1/extensions/plugins/{installation_id}/publisher-revocation"
	ExtensionPluginPublisherTrustPath      = "/api/v1/extensions/plugins/{installation_id}/publisher-trust"
	ExtensionHookDiagnosticsPath           = "/api/v1/extensions/hooks"
	PluginLifecycleProtocol                = "plugin-lifecycle.v1"
	HookDiagnosticsProtocol                = "hook-diagnostics.v1"
)

type PluginLifecycleController interface {
	PluginHistory(context.Context, string) (plugins.History, error)
	RollbackPlugin(context.Context, string, string, plugins.RollbackRequest) (plugins.Installation, plugins.Installation, error)
	RevokePluginPublisher(context.Context, string, string, int64, string) (plugins.PublisherTrust, error)
	TrustPluginPublisher(context.Context, string, string, int64, string) (plugins.PublisherTrust, error)
	HookDiagnostics(context.Context, string, string) (application.HookDiagnostics, error)
}

type PluginPublisherTrustView struct {
	Fingerprint string `json:"fingerprint"`
	Publisher   string `json:"publisher"`
	State       string `json:"state"`
	Generation  int64  `json:"generation"`
	ReviewedAt  string `json:"reviewed_at"`
}
type PluginHistoryView struct {
	ProtocolVersion             string                            `json:"protocol_version"`
	InstallationID              string                            `json:"installation_id"`
	PackageID                   string                            `json:"package_id"`
	Installations               []ExtensionPluginInstallationView `json:"installations"`
	Publisher                   *PluginPublisherTrustView         `json:"publisher,omitempty"`
	PublisherInstallationIDs    []string                          `json:"publisher_installation_ids"`
	TotalVersions               int                               `json:"total_versions"`
	TotalPublisherInstallations int                               `json:"total_publisher_installations"`
}
type PluginRollbackRequestView struct {
	Version                    string               `json:"version"`
	TargetInstallationID       string               `json:"target_installation_id"`
	ExpectedCurrentFingerprint string               `json:"expected_current_fingerprint"`
	ExpectedCurrentGeneration  int64                `json:"expected_current_generation"`
	ExpectedTargetFingerprint  string               `json:"expected_target_fingerprint"`
	ExpectedTargetGeneration   int64                `json:"expected_target_generation"`
	Capabilities               []plugins.Capability `json:"capabilities"`
	ConfirmUntrusted           bool                 `json:"confirm_untrusted"`
}
type PluginRollbackView struct {
	ProtocolVersion string                          `json:"protocol_version"`
	Current         ExtensionPluginInstallationView `json:"current"`
	Target          ExtensionPluginInstallationView `json:"target"`
}
type PluginPublisherRevocationRequestView struct {
	Version                      string `json:"version"`
	ExpectedPublisherFingerprint string `json:"expected_publisher_fingerprint"`
	ExpectedPublisherGeneration  int64  `json:"expected_publisher_generation"`
	Confirm                      bool   `json:"confirm"`
}
type PluginPublisherRevocationView struct {
	ProtocolVersion string                   `json:"protocol_version"`
	InstallationID  string                   `json:"installation_id"`
	Publisher       PluginPublisherTrustView `json:"publisher"`
}

// PluginPublisherTrustRequestView binds a grant to the publisher record the
// operator actually observed. The generation is required even for a first
// trust, where the reviewed value is zero.
type PluginPublisherTrustRequestView struct {
	Version                      string `json:"version"`
	ExpectedPublisherFingerprint string `json:"expected_publisher_fingerprint"`
	ExpectedPublisherGeneration  int64  `json:"expected_publisher_generation"`
	Confirm                      bool   `json:"confirm"`
}

type PluginPublisherTrustResponseView struct {
	ProtocolVersion string                   `json:"protocol_version"`
	InstallationID  string                   `json:"installation_id"`
	Publisher       PluginPublisherTrustView `json:"publisher"`
}
type HookDeclarationView struct {
	InstallationID     string   `json:"installation_id"`
	PluginID           string   `json:"plugin_id"`
	PackageFingerprint string   `json:"package_fingerprint"`
	InstallationState  string   `json:"installation_state"`
	Active             bool     `json:"active"`
	Scope              string   `json:"scope"`
	HookID             string   `json:"hook_id"`
	Event              string   `json:"event"`
	Action             string   `json:"action"`
	FailurePolicy      string   `json:"failure_policy"`
	TimeoutMillis      int      `json:"timeout_ms"`
	ToolNames          []string `json:"tool_names"`
	RemoveFields       []string `json:"remove_fields"`
}
type HookObservationView struct {
	ID                 string `json:"id"`
	PluginID           string `json:"plugin_id"`
	HookID             string `json:"hook_id"`
	PackageFingerprint string `json:"package_fingerprint,omitempty"`
	Event              string `json:"event"`
	Action             string `json:"action,omitempty"`
	RunID              string `json:"run_id,omitempty"`
	WorkspaceID        string `json:"workspace_id,omitempty"`
	ToolName           string `json:"tool_name,omitempty"`
	Outcome            string `json:"outcome"`
	Decision           string `json:"decision"`
	CreatedAt          string `json:"created_at"`
}
type HookDiagnosticsView struct {
	ProtocolVersion     string                `json:"protocol_version"`
	RunID               string                `json:"run_id,omitempty"`
	WorkspaceID         string                `json:"workspace_id,omitempty"`
	Declarations        []HookDeclarationView `json:"declarations"`
	OmittedDeclarations int                   `json:"omitted_declarations"`
	Observations        []HookObservationView `json:"observations"`
}

func matchPluginLifecyclePath(path string) (string, string, bool) {
	const prefix = "/api/v1/extensions/plugins/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	for _, action := range []string{"history", "rollback", "publisher-revocation", "publisher-trust"} {
		if strings.HasSuffix(path, "/"+action) {
			id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/"+action)
			return id, action, id != "" && !strings.Contains(id, "/")
		}
	}
	return "", "", false
}

func (a *API) servePluginLifecycle(writer http.ResponseWriter, request *http.Request, requestID, id, action string) {
	const label = "Plugin lifecycle"
	read := action == "history" || action == "hooks"
	if read {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "diagnostics require GET"), http.StatusMethodNotAllowed)
			return
		}
		if !a.authorized(request, a.tokenHash) {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="CyberAgent API"`)
			a.writeError(writer, requestID, apperror.New(apperror.CodePolicyDenied, "valid bearer authorization is required"), http.StatusUnauthorized)
			return
		}
		if request.ContentLength != 0 || len(request.TransferEncoding) != 0 {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "diagnostics cannot contain a body"), 0)
			return
		}
	} else if !a.authorizeRunOperation(writer, request, requestID, a.extensionControlEnabled, label) {
		return
	}
	controller, ok := a.extensionController.(PluginLifecycleController)
	if !ok || controller == nil {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound, "plugin lifecycle diagnostics are unavailable"), http.StatusNotFound)
		return
	}
	if action != "hooks" {
		if err := validatePathIdentity(id); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
	}
	if action == "history" {
		if err := rejectQuery(request.URL.Query()); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		value, err := controller.PluginHistory(request.Context(), id)
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		view := PluginHistoryView{ProtocolVersion: PluginLifecycleProtocol, InstallationID: value.InstallationID, PackageID: value.PackageID, Installations: []ExtensionPluginInstallationView{}, PublisherInstallationIDs: value.PublisherInstallationIDs, TotalVersions: value.TotalVersions, TotalPublisherInstallations: value.TotalPublisherInstallations}
		for _, item := range value.Installations {
			view.Installations = append(view.Installations, ProjectPluginInstallation(item))
		}
		if value.Publisher != nil {
			projected := projectPluginPublisher(*value.Publisher)
			view.Publisher = &projected
		}
		a.writeSuccess(writer, requestID, view, nil)
		return
	}
	if action == "hooks" {
		query := request.URL.Query()
		if err := validateSingleQueryValues(query, "run_id", "workspace_id"); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		for _, values := range query {
			if len(values) != 1 || values[0] == "" {
				a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "hook scope requires one nonempty identity"), 0)
				return
			}
			if err := validatePathIdentity(values[0]); err != nil {
				a.writeError(writer, requestID, err, 0)
				return
			}
		}
		value, err := controller.HookDiagnostics(request.Context(), query.Get("run_id"), query.Get("workspace_id"))
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		view := HookDiagnosticsView{ProtocolVersion: HookDiagnosticsProtocol, RunID: value.RunID, WorkspaceID: value.WorkspaceID, Declarations: []HookDeclarationView{}, OmittedDeclarations: value.OmittedDeclarations, Observations: []HookObservationView{}}
		for _, item := range value.Declarations {
			d := item.Declaration
			view.Declarations = append(view.Declarations, HookDeclarationView{InstallationID: item.InstallationID, PluginID: item.PluginID, PackageFingerprint: item.PackageFingerprint, InstallationState: string(item.InstallationState), Active: item.Active, Scope: "local", HookID: d.ID, Event: string(d.Event), Action: string(d.Action), FailurePolicy: string(d.FailurePolicy), TimeoutMillis: d.TimeoutMillis, ToolNames: append([]string{}, d.ToolNames...), RemoveFields: append([]string{}, d.RemoveFields...)})
		}
		for _, item := range value.Observations {
			decision := "unknown"
			if item.Rejected != nil {
				decision = "continued"
				if *item.Rejected {
					decision = "rejected"
				}
			} else if item.Outcome == "failed_closed" {
				decision = "rejected"
			}
			view.Observations = append(view.Observations, HookObservationView{ID: item.ID, PluginID: item.PluginID, HookID: item.HookID, PackageFingerprint: item.PluginFingerprint, Event: string(item.Event), Action: string(item.Action), RunID: item.RunID, WorkspaceID: item.WorkspaceID, ToolName: item.ToolName, Outcome: item.Outcome, Decision: decision, CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano)})
		}
		a.writeSuccess(writer, requestID, view, nil)
		return
	}
	body, err := readExtensionOnboardingBody(request, maxExtensionControlBodyBytes)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if action == "publisher-trust" {
		var view PluginPublisherTrustRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		if view.Version != PluginLifecycleProtocol || !view.Confirm {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "publisher trust requires the lifecycle version and confirmation"), 0)
			return
		}
		value, err := controller.TrustPluginPublisher(request.Context(), id, view.ExpectedPublisherFingerprint, view.ExpectedPublisherGeneration, "http_extension_operator")
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, PluginPublisherTrustResponseView{ProtocolVersion: PluginLifecycleProtocol, InstallationID: id, Publisher: projectPluginPublisher(value)}, nil, http.StatusAccepted)
		return
	}
	if action == "rollback" {
		var view PluginRollbackRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		if view.Version != PluginLifecycleProtocol {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "plugin lifecycle version is invalid"), 0)
			return
		}
		if err := validatePathIdentity(view.TargetInstallationID); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		current, target, err := controller.RollbackPlugin(request.Context(), id, view.TargetInstallationID, plugins.RollbackRequest{ExpectedCurrentFingerprint: view.ExpectedCurrentFingerprint, ExpectedCurrentGeneration: view.ExpectedCurrentGeneration, ExpectedTargetFingerprint: view.ExpectedTargetFingerprint, ExpectedTargetGeneration: view.ExpectedTargetGeneration, Capabilities: view.Capabilities, ConfirmUntrusted: view.ConfirmUntrusted, ReviewedBy: "http_extension_operator"})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, PluginRollbackView{ProtocolVersion: PluginLifecycleProtocol, Current: ProjectPluginInstallation(current), Target: ProjectPluginInstallation(target)}, nil, http.StatusAccepted)
		return
	}
	var view PluginPublisherRevocationRequestView
	if err := decodeStrictRunOperation(body, &view, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if view.Version != PluginLifecycleProtocol || !view.Confirm {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument, "publisher revocation requires the lifecycle version and confirmation"), 0)
		return
	}
	value, err := controller.RevokePluginPublisher(request.Context(), id, view.ExpectedPublisherFingerprint, view.ExpectedPublisherGeneration, "http_extension_operator")
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, PluginPublisherRevocationView{ProtocolVersion: PluginLifecycleProtocol, InstallationID: id, Publisher: projectPluginPublisher(value)}, nil, http.StatusAccepted)
}

func projectPluginPublisher(value plugins.PublisherTrust) PluginPublisherTrustView {
	return PluginPublisherTrustView{Fingerprint: value.Fingerprint, Publisher: value.Publisher, State: string(value.State), Generation: value.Generation, ReviewedAt: value.ReviewedAt.UTC().Format(time.RFC3339Nano)}
}
