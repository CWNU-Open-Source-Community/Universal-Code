package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/plugins"
)

const (
	ExtensionInventoryPath       = "/api/v1/extensions"
	ExtensionControlProtocol     = "extension-control.v1"
	ExtensionMCPReviewPath       = "/api/v1/extensions/mcp/{server_id}/review"
	ExtensionMCPRefreshPath      = "/api/v1/extensions/mcp/{server_id}/refresh"
	ExtensionPluginReviewPath    = "/api/v1/extensions/plugins/{installation_id}/review"
	maxExtensionControlBodyBytes = 64 * 1024
)

type ExtensionController interface {
	Inventory(context.Context, string) (application.ExtensionInventory, error)
	ReviewMCP(context.Context, string, mcp.ReviewRequest) (mcp.ServerRecord, error)
	RefreshMCP(context.Context, string) (mcp.ServerRecord, error)
	ReviewPlugin(context.Context, string, plugins.ReviewRequest) (plugins.Installation, error)
}

type ExtensionInventoryView struct {
	ProtocolVersion string                               `json:"protocol_version"`
	RunID           string                               `json:"run_id,omitempty"`
	WorkspaceID     string                               `json:"workspace_id,omitempty"`
	MCPServers      []ExtensionMCPServerView             `json:"mcp_servers"`
	MCPCalls        []ExtensionMCPCallAuditView          `json:"mcp_calls"`
	Plugins         []ExtensionPluginInstallationView    `json:"plugins"`
	Onboarding      *ExtensionOnboardingCapabilitiesView `json:"onboarding,omitempty"`
}

// These process-local capabilities describe available operator entry points.
// They never grant extension execution authority or imply a successful call.
type ExtensionOnboardingCapabilitiesView struct {
	MCPRegistration  bool `json:"mcp_registration"`
	MCPCredentials   bool `json:"mcp_credentials,omitempty"`
	PluginImport     bool `json:"plugin_import"`
	LSPConfiguration bool `json:"lsp_configuration"`
	PluginLifecycle  bool `json:"plugin_lifecycle,omitempty"`
	HookDiagnostics  bool `json:"hook_diagnostics,omitempty"`
}

type ExtensionSourceView struct {
	Kind        string `json:"kind"`
	URI         string `json:"uri"`
	Version     string `json:"version,omitempty"`
	Commit      string `json:"commit,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type ExtensionMCPCapabilityView struct {
	ServerName        string   `json:"server_name,omitempty"`
	ServerVersion     string   `json:"server_version,omitempty"`
	Negotiated        []string `json:"negotiated"`
	Tools             []string `json:"tools"`
	Resources         []string `json:"resources"`
	ResourceTemplates []string `json:"resource_templates,omitempty"`
	Prompts           []string `json:"prompts"`
	Fingerprint       string   `json:"fingerprint,omitempty"`
	DiscoveredAt      string   `json:"discovered_at,omitempty"`
}

type ExtensionMCPServerView struct {
	ProtocolVersion               string                        `json:"protocol_version"`
	ID                            string                        `json:"id"`
	Name                          string                        `json:"name"`
	Transport                     string                        `json:"transport"`
	Target                        string                        `json:"target"`
	NativeSource                  *ExtensionMCPNativeSourceView `json:"native_source,omitempty"`
	CredentialRef                 string                        `json:"credential_ref,omitempty"`
	DeclaredCapabilities          []string                      `json:"declared_capabilities"`
	Scope                         string                        `json:"scope"`
	RunID                         string                        `json:"run_id,omitempty"`
	WorkspaceID                   string                        `json:"workspace_id"`
	Source                        ExtensionSourceView           `json:"source"`
	DescriptorFingerprint         string                        `json:"descriptor_fingerprint"`
	State                         string                        `json:"state"`
	Capabilities                  ExtensionMCPCapabilityView    `json:"capabilities"`
	ApprovedCapabilityFingerprint string                        `json:"approved_capability_fingerprint,omitempty"`
	Health                        string                        `json:"health"`
	HealthMessage                 string                        `json:"health_message,omitempty"`
	Generation                    int64                         `json:"generation"`
	ReviewedBy                    string                        `json:"reviewed_by,omitempty"`
	ReviewedAt                    string                        `json:"reviewed_at,omitempty"`
	CreatedAt                     string                        `json:"created_at"`
	UpdatedAt                     string                        `json:"updated_at"`
}

// Native registrations have no legacy target. Expose only the pinned host
// source identity needed for review; original launch fields stay in the object.
type ExtensionMCPNativeSourceView struct {
	InstallationID         string `json:"installation_id"`
	PackageID              string `json:"package_id"`
	ComponentID            string `json:"component_id"`
	Revision               string `json:"revision"`
	InstallationGeneration int64  `json:"installation_generation"`
	Surface                string `json:"surface"`
}

type ExtensionMCPCallAuditView struct {
	ID                    string `json:"id"`
	RunID                 string `json:"run_id"`
	WorkspaceID           string `json:"workspace_id"`
	ServerID              string `json:"server_id"`
	ToolName              string `json:"tool_name"`
	CapabilityFingerprint string `json:"capability_fingerprint"`
	ArgumentsSHA256       string `json:"arguments_sha256"`
	Status                string `json:"status"`
	ErrorCode             string `json:"error_code,omitempty"`
	ResultBytes           int    `json:"result_bytes"`
	Truncated             bool   `json:"truncated"`
	StartedAt             string `json:"started_at"`
	CompletedAt           string `json:"completed_at"`
}

type ExtensionPluginManifestView struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Publisher    string   `json:"publisher"`
	Description  string   `json:"description"`
	Capabilities []string `json:"capabilities"`
}

type ExtensionPluginInstallationView struct {
	ProtocolVersion      string                         `json:"protocol_version"`
	ID                   string                         `json:"id"`
	Manifest             ExtensionPluginManifestView    `json:"manifest"`
	Snapshot             *ExtensionPortableSnapshotView `json:"snapshot,omitempty"`
	Source               ExtensionSourceView            `json:"source"`
	ArchiveSHA256        string                         `json:"archive_sha256"`
	PackageFingerprint   string                         `json:"package_fingerprint"`
	SignaturePresent     bool                           `json:"signature_present"`
	SignatureValid       bool                           `json:"signature_valid"`
	PublisherFingerprint string                         `json:"publisher_fingerprint,omitempty"`
	State                string                         `json:"state"`
	EnabledCapabilities  []string                       `json:"enabled_capabilities"`
	Generation           int64                          `json:"generation"`
	StagedBy             string                         `json:"staged_by"`
	ReviewedBy           string                         `json:"reviewed_by,omitempty"`
	ReviewedAt           string                         `json:"reviewed_at,omitempty"`
	CreatedAt            string                         `json:"created_at"`
	UpdatedAt            string                         `json:"updated_at"`
}

// Portable author version is optional. The acquired revision is a separate
// host identity; it must never be represented as an invented author version.
type ExtensionPortableSnapshotView struct {
	Format   string `json:"format"`
	Revision string `json:"revision"`
	Surface  string `json:"surface"`
}

type ExtensionMCPReviewRequestView struct {
	Version                       string           `json:"version"`
	Action                        mcp.ReviewAction `json:"action"`
	ExpectedDescriptorFingerprint string           `json:"expected_descriptor_fingerprint"`
	ExpectedCapabilityFingerprint string           `json:"expected_capability_fingerprint,omitempty"`
}

type ExtensionRefreshRequestView struct {
	Version string `json:"version"`
}

type ExtensionPluginReviewRequestView struct {
	Version                    string               `json:"version"`
	Action                     plugins.ReviewAction `json:"action"`
	ExpectedPackageFingerprint string               `json:"expected_package_fingerprint"`
	ExpectedGeneration         int64                `json:"expected_generation"`
	Capabilities               []plugins.Capability `json:"capabilities,omitempty"`
	ConfirmUntrusted           bool                 `json:"confirm_untrusted"`
}

type extensionMutationKind int

const (
	extensionMCPReview extensionMutationKind = iota + 1
	extensionMCPRefresh
	extensionPluginReview
)

func matchExtensionMutationPath(requestPath string) (string, extensionMutationKind, bool) {
	for _, candidate := range []struct {
		prefix string
		suffix string
		kind   extensionMutationKind
	}{
		{"/api/v1/extensions/mcp/", "/review", extensionMCPReview},
		{"/api/v1/extensions/mcp/", "/refresh", extensionMCPRefresh},
		{"/api/v1/extensions/plugins/", "/review", extensionPluginReview},
	} {
		if strings.HasPrefix(requestPath, candidate.prefix) &&
			strings.HasSuffix(requestPath, candidate.suffix) {
			identity := strings.TrimSuffix(strings.TrimPrefix(requestPath, candidate.prefix),
				candidate.suffix)
			if identity != "" && !strings.Contains(identity, "/") {
				return identity, candidate.kind, true
			}
		}
	}
	return "", 0, false
}

func (a *API) extensionInventory(request *http.Request) (any, *Page, error) {
	if a.extensionController == nil {
		return nil, nil, apperror.New(apperror.CodeNotFound,
			"extension inventory is unavailable")
	}
	if err := validateSingleQueryValues(request.URL.Query(), "run_id", "workspace_id"); err != nil {
		return nil, nil, err
	}
	runID, runSpecified := singleQueryValue(request.URL.Query(), "run_id")
	workspaceID, workspaceSpecified := singleQueryValue(request.URL.Query(), "workspace_id")
	if (runSpecified && runID == "") || (workspaceSpecified && workspaceID == "") {
		return nil, nil, apperror.New(apperror.CodeInvalidArgument,
			"run_id and workspace_id must each appear at most once with a value")
	}
	for _, identity := range []string{runID, workspaceID} {
		if identity != "" {
			if err := validatePathIdentity(identity); err != nil {
				return nil, nil, err
			}
		}
	}
	var value application.ExtensionInventory
	var err error
	if scoped, ok := a.extensionController.(ExtensionScopedInventoryController); ok {
		value, err = scoped.InventoryForScope(request.Context(), runID, workspaceID)
	} else if workspaceID != "" {
		return nil, nil, apperror.New(apperror.CodeUnavailable,
			"workspace-scoped extension inventory is unavailable; select a Run or update the backend")
	} else {
		value, err = a.extensionController.Inventory(request.Context(), runID)
	}
	if err != nil {
		return nil, nil, err
	}
	result := ExtensionInventoryView{ProtocolVersion: value.ProtocolVersion,
		RunID: value.RunID, WorkspaceID: value.WorkspaceID,
		Onboarding: a.extensionOnboardingCapabilities(),
		MCPServers: make([]ExtensionMCPServerView, 0, len(value.MCPServers)),
		MCPCalls:   make([]ExtensionMCPCallAuditView, 0, len(value.MCPCalls)),
		Plugins:    make([]ExtensionPluginInstallationView, 0, len(value.Plugins))}
	for _, server := range value.MCPServers {
		result.MCPServers = append(result.MCPServers, extensionMCPServerView(server))
	}
	for _, call := range value.MCPCalls {
		result.MCPCalls = append(result.MCPCalls, extensionMCPCallAuditView(call))
	}
	for _, installation := range value.Plugins {
		result.Plugins = append(result.Plugins, ProjectPluginInstallation(installation))
	}
	return result, nil, nil
}

func (a *API) extensionOnboardingCapabilities() *ExtensionOnboardingCapabilitiesView {
	_, onboarding := a.extensionController.(ExtensionOnboardingController)
	_, lifecycle := a.extensionController.(PluginLifecycleController)
	credentials, credentialsPresent := a.extensionController.(MCPCredentialController)
	return &ExtensionOnboardingCapabilitiesView{
		MCPRegistration:  a.extensionControlEnabled && onboarding,
		MCPCredentials:   credentialsPresent && credentials.HasMCPCredentialControl(),
		PluginImport:     a.extensionControlEnabled && onboarding,
		LSPConfiguration: a.extensionControlEnabled && a.codeIntelController != nil,
		PluginLifecycle:  lifecycle,
		HookDiagnostics:  lifecycle,
	}
}

func (a *API) serveExtensionMutation(writer http.ResponseWriter, request *http.Request,
	requestID, identity string, kind extensionMutationKind,
) {
	const label = "Extension control"
	if !a.authorizeRunOperation(writer, request, requestID,
		a.extensionControlEnabled, label) {
		return
	}
	if a.extensionController == nil {
		a.writeError(writer, requestID, apperror.New(apperror.CodeNotFound,
			"extension control is unavailable"), http.StatusNotFound)
		return
	}
	if err := validatePathIdentity(identity); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	body, err := readStrictControlBody(request, label)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if len(body) > maxExtensionControlBodyBytes {
		a.writeError(writer, requestID, apperror.New(apperror.CodeResourceExhausted,
			label+" request body exceeds its limit"), http.StatusRequestEntityTooLarge)
		return
	}
	switch kind {
	case extensionMCPReview:
		var view ExtensionMCPReviewRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		if view.Version != ExtensionControlProtocol {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				label+" version is invalid"), 0)
			return
		}
		value, err := a.extensionController.ReviewMCP(request.Context(), identity,
			mcp.ReviewRequest{Action: view.Action,
				ExpectedDescriptorFingerprint: view.ExpectedDescriptorFingerprint,
				ExpectedCapabilityFingerprint: view.ExpectedCapabilityFingerprint,
				ReviewedBy:                    "http_extension_operator"})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, extensionMCPServerView(value), nil,
			http.StatusAccepted)
	case extensionMCPRefresh:
		var view ExtensionRefreshRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		if view.Version != ExtensionControlProtocol {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				label+" version is invalid"), 0)
			return
		}
		value, err := a.extensionController.RefreshMCP(request.Context(), identity)
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, extensionMCPServerView(value), nil,
			http.StatusAccepted)
	case extensionPluginReview:
		var view ExtensionPluginReviewRequestView
		if err := decodeStrictRunOperation(body, &view, label); err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		if view.Version != ExtensionControlProtocol {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				label+" version is invalid"), 0)
			return
		}
		value, err := a.extensionController.ReviewPlugin(request.Context(), identity,
			plugins.ReviewRequest{Action: view.Action,
				ExpectedPackageFingerprint: view.ExpectedPackageFingerprint,
				ExpectedGeneration:         view.ExpectedGeneration, Capabilities: view.Capabilities,
				ConfirmUntrusted: view.ConfirmUntrusted, ReviewedBy: "http_extension_operator"})
		if err != nil {
			a.writeError(writer, requestID, err, 0)
			return
		}
		a.writeSuccessStatus(writer, requestID, ProjectPluginInstallation(value), nil,
			http.StatusAccepted)
	}
}

func extensionMCPServerView(value mcp.ServerRecord) ExtensionMCPServerView {
	capability := ExtensionMCPCapabilityView{ServerName: value.Capabilities.ServerName,
		ServerVersion: value.Capabilities.ServerVersion,
		Negotiated:    stringsFromMCPCapabilities(value.Capabilities.Negotiated),
		Tools:         make([]string, 0, len(value.Capabilities.Tools)),
		Resources:     make([]string, 0, len(value.Capabilities.Resources)),
		Prompts:       make([]string, 0, len(value.Capabilities.Prompts)),
		Fingerprint:   value.Capabilities.Fingerprint}
	if !value.Capabilities.DiscoveredAt.IsZero() {
		capability.DiscoveredAt = value.Capabilities.DiscoveredAt.UTC().Format(time.RFC3339Nano)
	}
	for _, tool := range value.Capabilities.Tools {
		capability.Tools = append(capability.Tools, tool.Name)
	}
	for _, resource := range value.Capabilities.Resources {
		capability.Resources = append(capability.Resources, resource.URI)
	}
	for _, template := range value.Capabilities.ResourceTemplates {
		capability.ResourceTemplates = append(capability.ResourceTemplates, template.URITemplate)
	}
	for _, prompt := range value.Capabilities.Prompts {
		capability.Prompts = append(capability.Prompts, prompt.Name)
	}
	result := ExtensionMCPServerView{ProtocolVersion: value.ProtocolVersion,
		ID: value.Descriptor.ID, Name: value.Descriptor.Name,
		Transport: string(value.Descriptor.Transport), Target: value.Descriptor.Target,
		CredentialRef:        value.Descriptor.CredentialRef,
		DeclaredCapabilities: stringsFromMCPCapabilities(value.Descriptor.DeclaredCapabilities),
		Scope:                string(value.Descriptor.Scope), RunID: value.Descriptor.RunID,
		WorkspaceID: value.Descriptor.WorkspaceID,
		Source: ExtensionSourceView{Kind: value.Descriptor.Source.Kind,
			URI: value.Descriptor.Source.URI, Version: value.Descriptor.Source.Version,
			Commit: value.Descriptor.Source.Commit, SHA256: value.Descriptor.Source.SHA256,
			Publisher:   value.Descriptor.Source.Publisher,
			Fingerprint: value.Descriptor.Source.Fingerprint},
		DescriptorFingerprint: value.DescriptorFingerprint, State: string(value.State),
		Capabilities:                  capability,
		ApprovedCapabilityFingerprint: value.ApprovedCapabilityFingerprint,
		Health:                        string(value.Health), HealthMessage: value.HealthMessage,
		Generation: value.Generation, ReviewedBy: value.ReviewedBy,
		CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	if source := value.Descriptor.NativeSource; source != nil {
		result.NativeSource = &ExtensionMCPNativeSourceView{InstallationID: source.InstallationID,
			PackageID: source.Component.PackageID, ComponentID: source.Component.ComponentID,
			Revision: source.Revision, InstallationGeneration: source.InstallationGeneration, Surface: source.Surface}
	}
	if value.ReviewedAt != nil {
		result.ReviewedAt = value.ReviewedAt.UTC().Format(time.RFC3339Nano)
	}
	return result
}

func extensionMCPCallAuditView(value mcp.CallAudit) ExtensionMCPCallAuditView {
	return ExtensionMCPCallAuditView{ID: value.ID, RunID: value.RunID,
		WorkspaceID: value.WorkspaceID, ServerID: value.ServerID,
		ToolName: value.ToolName, CapabilityFingerprint: value.CapabilityFingerprint,
		ArgumentsSHA256: value.ArgumentsSHA256, Status: value.Status,
		ErrorCode: value.ErrorCode, ResultBytes: value.ResultBytes,
		Truncated:   value.Truncated,
		StartedAt:   value.StartedAt.UTC().Format(time.RFC3339Nano),
		CompletedAt: value.CompletedAt.UTC().Format(time.RFC3339Nano)}
}

// ProjectPluginInstallation exposes persisted installation metadata without package content.
func ProjectPluginInstallation(value plugins.Installation) ExtensionPluginInstallationView {
	capabilities := make([]string, 0, len(value.Capabilities()))
	for _, capability := range value.Capabilities() {
		capabilities = append(capabilities, string(capability))
	}
	enabled := make([]string, 0, len(value.EnabledCapabilities))
	for _, capability := range value.EnabledCapabilities {
		enabled = append(enabled, string(capability))
	}
	result := ExtensionPluginInstallationView{ProtocolVersion: value.ProtocolVersion,
		ID: value.ID, Manifest: ExtensionPluginManifestView{ID: value.PackageID(),
			Name: value.DisplayName(), Version: value.Manifest.Version,
			Publisher: value.Manifest.Publisher, Description: value.Manifest.Description,
			Capabilities: capabilities},
		Source: ExtensionSourceView{Kind: value.Source.Kind, URI: value.Source.URI,
			Commit: value.Source.Commit, SHA256: value.Source.SHA256},
		ArchiveSHA256:      value.ArchiveSHA256,
		PackageFingerprint: value.PackageFingerprint,
		SignaturePresent:   value.SignaturePresent, SignatureValid: value.SignatureValid,
		PublisherFingerprint: value.PublisherFingerprint, State: string(value.State),
		EnabledCapabilities: enabled, Generation: value.Generation,
		StagedBy: value.StagedBy, ReviewedBy: value.ReviewedBy,
		CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano)}
	if value.Snapshot != nil {
		result.Manifest.Version = value.Snapshot.AuthorVersion
		result.Snapshot = &ExtensionPortableSnapshotView{Format: value.Snapshot.Format, Revision: value.Revision(), Surface: value.Source.Surface}
	}
	if value.ReviewedAt != nil {
		result.ReviewedAt = value.ReviewedAt.UTC().Format(time.RFC3339Nano)
	}
	return result
}

func stringsFromMCPCapabilities(values []mcp.CapabilityKind) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, string(value))
	}
	return result
}
