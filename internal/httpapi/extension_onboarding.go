package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/plugins"
)

const (
	ExtensionMCPRegistrationPath = "/api/v1/extensions/mcp"
	ExtensionPluginImportPath    = "/api/v1/extensions/plugins/import"
	ExtensionOnboardingProtocol  = "extension-onboarding.v1"
	// Base64 expands the existing 4 MiB archive limit; the small remainder
	// permits the source pin and JSON framing.
	maxExtensionPluginImportBodyBytes = ((plugins.MaxArchiveBytes + 2) / 3 * 4) + 8192
)

// Onboarding is additive. Review-only adapters retain their existing contract
// and advertise no import capability until this interface is implemented.
type ExtensionOnboardingController interface {
	RegisterMCP(context.Context, mcp.ServerDescriptor) (mcp.ServerRecord, bool, error)
	ImportPlugin(context.Context, []byte, string) (plugins.Installation, bool, error)
}

// Source acquisition is additive to ZIP upload; older adapters need not
// advertise remote acquisition.
type ExtensionPluginSourceController interface {
	ImportPluginSource(context.Context, application.ImportPluginSourceRequest) (application.ImportPluginSourceResult, error)
}

type ExtensionScopedInventoryController interface {
	InventoryForScope(context.Context, string, string) (application.ExtensionInventory, error)
}

type ExtensionMCPRegistrationDescriptorView struct {
	ProtocolVersion      string               `json:"protocol_version"`
	ID                   string               `json:"id"`
	Name                 string               `json:"name"`
	Transport            mcp.TransportKind    `json:"transport"`
	Target               string               `json:"target"`
	Arguments            []string             `json:"arguments,omitempty"`
	CredentialRef        string               `json:"credential_ref,omitempty"`
	Cwd                  string               `json:"cwd,omitempty"`
	Env                  map[string]string    `json:"env,omitempty"`
	EnvCredentialRefs    map[string]string    `json:"env_credential_refs,omitempty"`
	Headers              map[string]string    `json:"headers,omitempty"`
	DeclaredCapabilities []mcp.CapabilityKind `json:"declared_capabilities"`
	Scope                mcp.ScopeKind        `json:"scope"`
	RunID                string               `json:"run_id,omitempty"`
	WorkspaceID          string               `json:"workspace_id"`
	CallTimeoutMillis    int64                `json:"call_timeout_ms"`
	MaxResultBytes       int                  `json:"max_result_bytes"`
}

type ExtensionMCPRegistrationRequestView struct {
	Version    string                                 `json:"version"`
	Descriptor ExtensionMCPRegistrationDescriptorView `json:"descriptor"`
}

type ExtensionPluginImportRequestView struct {
	Version       string                           `json:"version"`
	ArchiveBase64 string                           `json:"archive_base64,omitempty"`
	ArchiveSHA256 string                           `json:"archive_sha256,omitempty"`
	Source        *ExtensionPluginImportSourceView `json:"source,omitempty"`
}

type ExtensionPluginImportSourceView struct {
	Kind        string `json:"kind"`
	URL         string `json:"url"`
	Commit      string `json:"commit,omitempty"`
	ArchivePath string `json:"archive_path,omitempty"`
}

type ExtensionMCPRegistrationView struct {
	ProtocolVersion string                 `json:"protocol_version"`
	Replayed        bool                   `json:"replayed"`
	NextStep        string                 `json:"next_step"`
	Server          ExtensionMCPServerView `json:"server"`
}

type ExtensionPluginImportView struct {
	ProtocolVersion string                          `json:"protocol_version"`
	Replayed        bool                            `json:"replayed"`
	NextStep        string                          `json:"next_step"`
	Installation    ExtensionPluginInstallationView `json:"installation"`
	Acquisition     *ExtensionPluginAcquisitionView `json:"acquisition,omitempty"`
}

type ExtensionPluginAcquisitionView struct {
	Kind        string `json:"kind"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	Commit      string `json:"commit,omitempty"`
	ArchivePath string `json:"archive_path,omitempty"`
}

func (a *API) serveExtensionOnboarding(writer http.ResponseWriter, request *http.Request, requestID string) {
	const label = "Extension onboarding"
	if !a.authorizeRunOperation(writer, request, requestID, a.extensionControlEnabled, label) {
		return
	}
	controller, ok := a.extensionController.(ExtensionOnboardingController)
	if !ok || controller == nil {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound,
			"extension onboarding is unavailable"), http.StatusNotFound)
		return
	}
	limit := int64(maxExtensionControlBodyBytes)
	if request.URL.Path == ExtensionPluginImportPath {
		limit = maxExtensionPluginImportBodyBytes
	}
	body, err := readExtensionOnboardingBody(request, limit)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if request.URL.Path == ExtensionMCPRegistrationPath {
		var view ExtensionMCPRegistrationRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		if view.Version != ExtensionControlProtocol {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				"extension onboarding version is invalid"), 0)
			return
		}
		d := view.Descriptor
		value, replayed, err := controller.RegisterMCP(request.Context(), mcp.ServerDescriptor{
			ProtocolVersion: d.ProtocolVersion, ID: d.ID, Name: d.Name, Transport: d.Transport,
			Target: d.Target, Arguments: d.Arguments, CredentialRef: d.CredentialRef,
			Cwd: d.Cwd, Env: d.Env, EnvCredentialRefs: d.EnvCredentialRefs, Headers: d.Headers,
			DeclaredCapabilities: d.DeclaredCapabilities, Scope: d.Scope, RunID: d.RunID,
			WorkspaceID: d.WorkspaceID, CallTimeoutMillis: d.CallTimeoutMillis,
			MaxResultBytes: d.MaxResultBytes})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, ExtensionMCPRegistrationView{
			ProtocolVersion: ExtensionOnboardingProtocol, Replayed: replayed,
			NextStep: extensionMCPNextStep(value), Server: extensionMCPServerView(value)}, nil,
			http.StatusAccepted)
		return
	}
	var view ExtensionPluginImportRequestView
	if err := decodeStrictRunOperation(body, &view, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if view.Version != ExtensionControlProtocol {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"extension onboarding version is invalid"), 0)
		return
	}
	if view.Source != nil {
		if view.ArchiveBase64 != "" {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				"choose an uploaded archive or a pinned source"), 0)
			return
		}
		sourceController, ok := a.extensionController.(ExtensionPluginSourceController)
		if !ok || sourceController == nil {
			a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound,
				"plugin source acquisition is unavailable"), http.StatusNotFound)
			return
		}
		deadline := time.Now().Add(plugins.MaxPluginImportDuration + 30*time.Second)
		if err := http.NewResponseController(writer).SetWriteDeadline(deadline); err != nil &&
			!errors.Is(err, http.ErrNotSupported) {
			a.writeError(writer, requestID, apperror.Wrap(apperror.CodeUnavailable,
				"plugin import response deadline could not be configured", err), 0)
			return
		}
		result, err := sourceController.ImportPluginSource(request.Context(), application.ImportPluginSourceRequest{
			Kind: view.Source.Kind, URL: view.Source.URL, SHA256: view.ArchiveSHA256,
			Commit: view.Source.Commit, ArchivePath: view.Source.ArchivePath})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, ExtensionPluginImportView{
			ProtocolVersion: ExtensionOnboardingProtocol, Replayed: result.Replayed,
			NextStep: extensionPluginNextStep(result.Installation.State), Installation: extensionImportedPluginView(result.Installation),
			Acquisition: &ExtensionPluginAcquisitionView{Kind: result.Acquisition.Kind, URL: result.Acquisition.URL,
				SHA256: result.Acquisition.SHA256, Commit: result.Acquisition.Commit, ArchivePath: result.Acquisition.ArchivePath}}, nil,
			http.StatusAccepted)
		return
	}
	if len(view.ArchiveBase64) > (plugins.MaxArchiveBytes+2)/3*4 {
		a.writeError(writer, requestID, apperror.New(apperror.CodeResourceExhausted,
			"plugin archive exceeds its limit"), http.StatusRequestEntityTooLarge)
		return
	}
	archive, err := base64.StdEncoding.Strict().DecodeString(view.ArchiveBase64)
	if err != nil || len(archive) == 0 {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"plugin archive must be valid nonempty base64"), 0)
		return
	}
	if len(archive) > plugins.MaxArchiveBytes {
		a.writeError(writer, requestID, apperror.New(apperror.CodeResourceExhausted,
			"plugin archive exceeds its limit"), http.StatusRequestEntityTooLarge)
		return
	}
	value, replayed, err := controller.ImportPlugin(request.Context(), archive, view.ArchiveSHA256)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, ExtensionPluginImportView{
		ProtocolVersion: ExtensionOnboardingProtocol, Replayed: replayed,
		NextStep: extensionPluginNextStep(value.State), Installation: extensionImportedPluginView(value)}, nil,
		http.StatusAccepted)
}

func extensionImportedPluginView(value plugins.Installation) ExtensionPluginInstallationView {
	view := ProjectPluginInstallation(value)
	// An identical package may already have been staged by the CLI. Keep its
	// actual provenance kind while returning a digest locator instead of the
	// CLI's private host path in this upload reply.
	if value.Source.Kind == "local_file" || value.Source.Kind == "local_directory" {
		view.Source.URI = "sha256:" + value.Source.SHA256
	}
	return view
}

func readExtensionOnboardingBody(request *http.Request, limit int64) ([]byte, error) {
	if err := rejectQuery(request.URL.Query()); err != nil {
		return nil, err
	}
	if err := validateJSONContentType(request.Header); err != nil {
		return nil, err
	}
	body, err := readBoundedRequestBody(request, limit)
	if err != nil {
		return nil, err
	}
	if err := rejectDuplicateJSONObjectFields(body, "Extension onboarding"); err != nil {
		return nil, err
	}
	return body, nil
}

func extensionMCPNextStep(server mcp.ServerRecord) string {
	switch server.State {
	case mcp.TrustStaged, mcp.TrustDisabled:
		return "approve_discovery"
	case mcp.TrustDiscoveryApproved:
		return "refresh"
	case mcp.TrustCapabilitiesPending:
		return "enable_capabilities"
	case mcp.TrustEnabled:
		if server.Health != mcp.HealthHealthy {
			return "refresh"
		}
		return "request_execution"
	default:
		return "inspect_state"
	}
}

func extensionPluginNextStep(state plugins.State) string {
	switch state {
	case plugins.StateStaged, plugins.StateQuarantined:
		return "approve"
	case plugins.StateApproved, plugins.StateDisabled:
		return "enable"
	case plugins.StateEnabled:
		return "select_contributions"
	default:
		return "inspect_state"
	}
}
