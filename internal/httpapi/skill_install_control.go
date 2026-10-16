package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/operationreceipt"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/skills"
)

const (
	SkillPackageInstallPath         = "/api/v1/skills/packages/install"
	MaxSkillPackageInstallBodyBytes = 16 * 1024 * 1024
)

type SkillInstallationController interface {
	Import(context.Context, application.ImportSkillPackageRequest) (
		application.ImportSkillPackageResult, error)
	Inventory(context.Context, application.ListInstalledSkillPackagesRequest) (
		application.SkillPackageInventory, error)
	Remove(context.Context, application.RemoveSkillPackageRequest) (
		application.RemoveSkillPackageResult, error)
}

type SkillPackageInstallRequestView struct {
	Snapshot         *plugins.PortableSnapshot  `json:"snapshot,omitempty"`
	Directory        *plugins.UploadedDirectory `json:"directory,omitempty"`
	Version          string                     `json:"version"`
	ArchiveBase64    string                     `json:"archive_base64,omitempty"`
	Surface          string                     `json:"surface"`
	ConfirmUntrusted bool                       `json:"confirm_untrusted"`
}

// PluginSkillInstallView carries a real Plugin installation, without fabricated
// legacy object keys, receipts, profile declarations or context grants.
type PluginSkillInstallView struct {
	ProtocolVersion string                          `json:"protocol_version"`
	Installation    ExtensionPluginInstallationView `json:"installation"`
	Replayed        bool                            `json:"replayed"`
}

type SkillPackageInstallView struct {
	ProtocolVersion            string               `json:"protocol_version"`
	Name                       string               `json:"name"`
	Version                    string               `json:"version"`
	Surface                    string               `json:"surface"`
	Profiles                   []string             `json:"profiles"`
	Surfaces                   []string             `json:"surfaces"`
	Phases                     []string             `json:"phases"`
	Roles                      []string             `json:"roles"`
	UserInvocable              bool                 `json:"user_invocable"`
	ModelInvocable             bool                 `json:"model_invocable"`
	ExplicitOnly               bool                 `json:"explicit_only"`
	TrustClass                 string               `json:"trust_class"`
	ArchiveSHA256              string               `json:"archive_sha256"`
	PackageFingerprint         string               `json:"package_fingerprint"`
	Replayed                   bool                 `json:"replayed"`
	RecoveredPending           bool                 `json:"recovered_pending"`
	ImportCommandExecution     bool                 `json:"import_command_execution"`
	ImportNetworkAccess        bool                 `json:"import_network_access"`
	ImportProviderCalls        bool                 `json:"import_provider_calls"`
	ToolCapabilityGrant        bool                 `json:"tool_capability_grant"`
	RunSelectionAuthorized     bool                 `json:"run_selection_authorized"`
	ContextInjectionAuthorized bool                 `json:"context_injection_authorized"`
	Receipt                    OperationReceiptView `json:"receipt"`
}

func (a *API) serveSkillPackageInstallControl(writer http.ResponseWriter,
	request *http.Request, requestID string,
) {
	const label = "Skill package installation"
	if !a.authorizeRunOperation(writer, request, requestID,
		a.skillInstallationEnabled, label) {
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
	operationKey, err := sessionControlIdempotencyKey(request.Header, label)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	body, err := readBoundedRequestBody(request, MaxSkillPackageInstallBodyBytes)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if err := rejectDuplicateJSONObjectFields(body, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var view SkillPackageInstallRequestView
	if err := decodeStrictRunOperation(body, &view, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if (view.Version != skills.PackageInstallationProtocolVersion && view.Version != plugins.PortableInstallationProtocol) ||
		(view.Version == skills.PackageInstallationProtocolVersion && (view.Snapshot != nil || view.Directory != nil)) ||
		!view.ConfirmUntrusted || strings.TrimSpace(view.ArchiveBase64) != view.ArchiveBase64 ||
		strings.ContainsAny(view.ArchiveBase64, " \t\r\n") {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"Skill package installation confirmation or archive encoding is invalid"), 0)
		return
	}
	var raw []byte
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(body, &fields) // Strict JSON and duplicate fields were checked above.
	if _, directoryPresent := fields["directory"]; directoryPresent {
		_, archivePresent := fields["archive_base64"]
		_, snapshotPresent := fields["snapshot"]
		if view.Directory == nil || archivePresent || snapshotPresent {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				"directory upload cannot be combined with archive or snapshot fields"), 0)
			return
		}
	} else {
		raw, err = base64.StdEncoding.Strict().DecodeString(view.ArchiveBase64)
		if err != nil || len(raw) == 0 || len(raw) > plugins.MaxArchiveBytes ||
			(view.Version == skills.PackageInstallationProtocolVersion && len(raw) > skills.MaxPackageArchiveBytes) {
			a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
				"Skill package archive must be canonical bounded base64"), 0)
			return
		}
	}
	surface, err := domain.ParseExecutionSurface(view.Surface)
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	result, err := a.skillInstallationController.Import(request.Context(),
		application.ImportSkillPackageRequest{Raw: raw, Snapshot: view.Snapshot, Directory: view.Directory, Surface: surface,
			OperationKey: operationKey, InstalledBy: "http_operator",
			ConfirmUntrusted: true})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if result.Installation != nil {
		a.writeSuccessStatus(writer, requestID, PluginSkillInstallView{ProtocolVersion: plugins.PortableInstallationProtocol,
			Installation: ProjectPluginInstallation(*result.Installation), Replayed: result.Replayed}, nil, http.StatusAccepted)
		return
	}
	installation := result.Package.Installation
	profiles := make([]string, len(installation.Manifest.Profiles))
	for index, profile := range installation.Manifest.Profiles {
		profiles[index] = string(profile)
	}
	surfaces := make([]string, len(installation.Manifest.Surfaces))
	for index, surface := range installation.Manifest.Surfaces {
		surfaces[index] = string(surface)
	}
	phases := make([]string, len(installation.Manifest.Phases))
	for index, phase := range installation.Manifest.Phases {
		phases[index] = string(phase)
	}
	roles := make([]string, len(installation.Manifest.Roles))
	for index, role := range installation.Manifest.Roles {
		roles[index] = string(role)
	}
	userInvocable, modelInvocable, explicitOnly := installation.Manifest.InvocationPolicy()
	a.writeSuccessStatus(writer, requestID, SkillPackageInstallView{
		ProtocolVersion: skills.PackageInstallationProtocolVersion,
		Name:            installation.Name, Version: installation.Version,
		Surface: string(installation.Surface), TrustClass: string(installation.TrustClass),
		Profiles: profiles, Surfaces: surfaces, Phases: phases, Roles: roles,
		UserInvocable: userInvocable, ModelInvocable: modelInvocable,
		ExplicitOnly:       explicitOnly,
		ArchiveSHA256:      installation.ArchiveSHA256,
		PackageFingerprint: installation.PackageFingerprint,
		Replayed:           result.Replayed, RecoveredPending: result.RecoveredPending,
		ImportCommandExecution:     installation.ImportCommandExecution,
		ImportNetworkAccess:        installation.ImportNetworkAccess,
		ImportProviderCalls:        installation.ImportProviderCalls,
		ToolCapabilityGrant:        installation.ToolCapabilityGrant,
		RunSelectionAuthorized:     installation.RunSelectionAuthorized,
		ContextInjectionAuthorized: installation.ContextInjectionAuthorized,
		Receipt: operationReceiptView(operationreceipt.Settled(
			operationreceipt.KindSkillPackageInstall, result.Replayed, false)),
	}, nil, http.StatusAccepted)
}
