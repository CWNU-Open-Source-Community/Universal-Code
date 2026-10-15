package httpapi

import (
	"net/http"
	"strings"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/operationreceipt"
	"cyberagent-workbench/internal/skills"
)

const (
	SkillPackageInventoryPath    = "/api/v1/skills/packages"
	SkillPackageRemovePath       = "/api/v1/skills/packages/remove"
	maxSkillPackageRemoveRequest = 4096
)

type SkillPackageRemovalSummaryView struct {
	PackageObjectRetained       bool   `json:"package_object_retained"`
	HistoricalRecoveryPreserved bool   `json:"historical_recovery_preserved"`
	FutureSelectionEnabled      bool   `json:"future_selection_enabled"`
	RunSelectionAuthorized      bool   `json:"run_selection_authorized"`
	ContextInjectionAuthorized  bool   `json:"context_injection_authorized"`
	ToolCapabilityGrant         bool   `json:"tool_capability_grant"`
	RemovedBy                   string `json:"removed_by"`
	RemovedAt                   string `json:"removed_at"`
}

type SkillPackageItemView struct {
	Skill                      string                          `json:"skill"`
	Surface                    string                          `json:"surface"`
	Profiles                   []string                        `json:"profiles"`
	Surfaces                   []string                        `json:"surfaces"`
	Phases                     []string                        `json:"phases"`
	Roles                      []string                        `json:"roles"`
	UserInvocable              bool                            `json:"user_invocable"`
	ModelInvocable             bool                            `json:"model_invocable"`
	ExplicitOnly               bool                            `json:"explicit_only"`
	ToolDependencies           []string                        `json:"tool_dependencies"`
	ContentSHA256              string                          `json:"content_sha256"`
	ContentBytes               int                             `json:"content_bytes"`
	ContentTokenUpperBound     int                             `json:"content_token_upper_bound"`
	ArchiveSHA256              string                          `json:"archive_sha256"`
	PackageFingerprint         string                          `json:"package_fingerprint"`
	ArchiveBytes               int                             `json:"archive_bytes"`
	UncompressedBytes          int                             `json:"uncompressed_bytes"`
	EntryCount                 int                             `json:"entry_count"`
	TrustClass                 string                          `json:"trust_class"`
	RiskCodes                  []string                        `json:"risk_codes"`
	Status                     string                          `json:"status"`
	ObjectVerified             bool                            `json:"object_verified"`
	OperatorConfirmed          bool                            `json:"operator_confirmed"`
	ImportCommandExecution     bool                            `json:"import_command_execution"`
	ImportNetworkAccess        bool                            `json:"import_network_access"`
	ImportProviderCalls        bool                            `json:"import_provider_calls"`
	RunSelectionAuthorized     bool                            `json:"run_selection_authorized"`
	ContextInjectionAuthorized bool                            `json:"context_injection_authorized"`
	ToolCapabilityGrant        bool                            `json:"tool_capability_grant"`
	ContentBodyExposed         bool                            `json:"content_body_exposed"`
	InstalledBy                string                          `json:"installed_by"`
	CreatedAt                  string                          `json:"created_at"`
	CompletedAt                string                          `json:"completed_at"`
	Removal                    *SkillPackageRemovalSummaryView `json:"removal,omitempty"`
}

type SkillPackageInventoryView struct {
	ProtocolVersion string                 `json:"protocol_version"`
	CapabilityGrant bool                   `json:"capability_grant"`
	Surface         string                 `json:"surface"`
	Profile         string                 `json:"profile"`
	IncludeRemoved  bool                   `json:"include_removed"`
	Items           []SkillPackageItemView `json:"items"`
	ReturnedCount   int                    `json:"returned_count"`
	UnverifiedCount int                    `json:"unverified_count"`
}

type SkillPackageRemoveRequestView struct {
	ProtocolVersion string `json:"protocol_version"`
	Skill           string `json:"skill"`
	ConfirmRemove   bool   `json:"confirm_remove"`
}

type SkillPackageRemovalView struct {
	ProtocolVersion             string               `json:"protocol_version"`
	Skill                       string               `json:"skill"`
	Surface                     string               `json:"surface"`
	Status                      string               `json:"status"`
	CapabilityGrant             bool                 `json:"capability_grant"`
	PackageObjectRetained       bool                 `json:"package_object_retained"`
	HistoricalRecoveryPreserved bool                 `json:"historical_recovery_preserved"`
	FutureSelectionEnabled      bool                 `json:"future_selection_enabled"`
	RunSelectionAuthorized      bool                 `json:"run_selection_authorized"`
	ContextInjectionAuthorized  bool                 `json:"context_injection_authorized"`
	ToolCapabilityGrant         bool                 `json:"tool_capability_grant"`
	RemovedBy                   string               `json:"removed_by"`
	RemovedAt                   string               `json:"removed_at"`
	Replayed                    bool                 `json:"replayed"`
	Receipt                     OperationReceiptView `json:"receipt"`
}

func (a *API) skillPackageInventory(request *http.Request,
	segments []string,
) (any, *Page, error) {
	if a.skillInstallationController == nil {
		return nil, nil, apperror.New(apperror.CodeFailedPrecondition,
			"Skill package Registry control is unavailable")
	}
	query := request.URL.Query()
	if err := validateSingleQueryValues(query, "surface", "profile", "include_removed"); err != nil {
		return nil, nil, err
	}
	// The shared validator only pins repetition for pagination identities. This
	// inventory has no legitimate multi-value form, so a duplicated filter must
	// fail instead of silently taking the first value.
	for _, key := range []string{"surface", "profile", "include_removed"} {
		if len(query[key]) > 1 {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"Skill package inventory query parameter must appear exactly once")
		}
	}
	var surface domain.ExecutionSurface
	if raw := query.Get("surface"); raw != "" {
		parsed, err := domain.ParseExecutionSurface(raw)
		if err != nil {
			return nil, nil, err
		}
		surface = parsed
	}
	var profile domain.Profile
	if raw := query.Get("profile"); raw != "" {
		parsed, err := domain.ParseProfile(raw)
		if err != nil || parsed != domain.Profile(raw) {
			return nil, nil, apperror.New(apperror.CodeInvalidArgument,
				"installed Skill package Profile is invalid")
		}
		profile = parsed
	}
	includeRemoved, err := parseOptionalBool(query.Get("include_removed"))
	if err != nil {
		return nil, nil, err
	}
	inventory, err := a.skillInstallationController.Inventory(request.Context(),
		application.ListInstalledSkillPackagesRequest{Surface: surface,
			Profile: profile, IncludeRemoved: includeRemoved})
	if err != nil {
		return nil, nil, err
	}
	return ProjectSkillPackageInventory(string(surface), string(profile),
		includeRemoved, inventory), nil, nil
}

// ProjectSkillPackageInventory is the single owner of the installed-package
// projection. Desktop and HTTP share it so one unverifiable object cannot be
// hidden on one surface and shown on the other.
func ProjectSkillPackageInventory(surface, profile string, includeRemoved bool,
	inventory application.SkillPackageInventory,
) SkillPackageInventoryView {
	unverified := make(map[string]bool, len(inventory.Unverified))
	for _, ref := range inventory.Unverified {
		unverified[ref] = true
	}
	items := make([]SkillPackageItemView, len(inventory.Items))
	for index, value := range inventory.Items {
		items[index] = skillPackageItemView(value, !unverified[value.Installation.ID])
	}
	return SkillPackageInventoryView{
		ProtocolVersion: skills.PackageInventoryProtocolVersion,
		CapabilityGrant: false, Surface: surface, Profile: profile,
		IncludeRemoved: includeRemoved, Items: items, ReturnedCount: len(items),
		UnverifiedCount: len(inventory.Unverified),
	}
}

func skillPackageItemView(value skills.InstalledPackage, objectVerified bool) SkillPackageItemView {
	installation := value.Installation
	manifest := installation.Manifest
	userInvocable, modelInvocable, explicitOnly := manifest.InvocationPolicy()
	item := SkillPackageItemView{
		Skill:         skills.FormatInstalledPackageRef(installation.Name, installation.Version),
		Surface:       string(installation.Surface),
		Profiles:      skillPackageStringList(manifest.Profiles),
		Surfaces:      skillPackageStringList(manifest.Surfaces),
		Phases:        skillPackageStringList(manifest.Phases),
		Roles:         skillPackageStringList(manifest.Roles),
		UserInvocable: userInvocable, ModelInvocable: modelInvocable,
		ExplicitOnly:               explicitOnly,
		ToolDependencies:           skillPackageStringList(manifest.ToolDependencies),
		ContentSHA256:              manifest.ContentSHA256,
		ContentBytes:               manifest.ContentBytes,
		ContentTokenUpperBound:     manifest.ContentTokenUpperBound,
		ArchiveSHA256:              installation.ArchiveSHA256,
		PackageFingerprint:         installation.PackageFingerprint,
		ArchiveBytes:               installation.ArchiveBytes,
		UncompressedBytes:          installation.UncompressedBytes,
		EntryCount:                 installation.EntryCount,
		TrustClass:                 string(installation.TrustClass),
		RiskCodes:                  skillPackageStringList(installation.RiskCodes),
		Status:                     "installed",
		ObjectVerified:             objectVerified,
		OperatorConfirmed:          installation.OperatorConfirmed,
		ImportCommandExecution:     installation.ImportCommandExecution,
		ImportNetworkAccess:        installation.ImportNetworkAccess,
		ImportProviderCalls:        installation.ImportProviderCalls,
		RunSelectionAuthorized:     installation.RunSelectionAuthorized,
		ContextInjectionAuthorized: installation.ContextInjectionAuthorized,
		ToolCapabilityGrant:        installation.ToolCapabilityGrant,
		ContentBodyExposed:         false,
		InstalledBy:                installation.InstalledBy,
		CreatedAt:                  installation.CreatedAt.UTC().Format(time.RFC3339Nano),
		CompletedAt:                value.Result.CompletedAt.UTC().Format(time.RFC3339Nano),
	}
	if value.Removal != nil {
		removal := value.Removal
		item.Status = "removed"
		item.Removal = &SkillPackageRemovalSummaryView{
			PackageObjectRetained:       removal.PackageObjectRetained,
			HistoricalRecoveryPreserved: removal.HistoricalRecoveryPreserved,
			FutureSelectionEnabled:      removal.FutureSelectionEnabled,
			RunSelectionAuthorized:      removal.RunSelectionAuthorized,
			ContextInjectionAuthorized:  removal.ContextInjectionAuthorized,
			ToolCapabilityGrant:         removal.ToolCapabilityGrant,
			RemovedBy:                   removal.RemovedBy,
			RemovedAt:                   removal.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
	}
	return item
}

func skillPackageStringList[T ~string](values []T) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func (a *API) serveSkillPackageRemoveControl(writer http.ResponseWriter,
	request *http.Request, requestID string,
) {
	const label = "Skill package removal"
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
	body, err := readBoundedRequestBody(request, maxSkillPackageRemoveRequest)
	if err != nil {
		a.writeError(writer, requestID, err, runOperationErrorStatus(err))
		return
	}
	if err := rejectDuplicateJSONObjectFields(body, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	var requestView SkillPackageRemoveRequestView
	if err := decodeStrictRunOperation(body, &requestView, label); err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	if requestView.ProtocolVersion != skills.PackageRemovalRequestProtocolVersion ||
		!requestView.ConfirmRemove ||
		strings.TrimSpace(requestView.Skill) != requestView.Skill {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"Skill package removal confirmation or protocol is invalid"), 0)
		return
	}
	name, version, err := skills.ParseInstalledPackageRef(requestView.Skill)
	if err != nil {
		a.writeError(writer, requestID, apperror.New(apperror.CodeInvalidArgument,
			"installed Skill package reference is invalid"), 0)
		return
	}
	result, err := a.skillInstallationController.Remove(request.Context(),
		application.RemoveSkillPackageRequest{Name: name, Version: version,
			OperationKey: operationKey, RemovedBy: "http_operator", ConfirmRemove: true})
	if err != nil {
		a.writeError(writer, requestID, err, 0)
		return
	}
	a.writeSuccessStatus(writer, requestID, ProjectSkillPackageRemoval(result),
		nil, http.StatusAccepted)
}

// ProjectSkillPackageRemoval is the shared tombstone projection for HTTP and
// Desktop. It reports the retained object and the stopped authorisations
// without exposing removal identity or operation digests.
func ProjectSkillPackageRemoval(
	result application.RemoveSkillPackageResult,
) SkillPackageRemovalView {
	removal := result.Removal
	return SkillPackageRemovalView{
		ProtocolVersion: skills.PackageRemovalControlProtocolVersion,
		Skill:           skills.FormatInstalledPackageRef(removal.Name, removal.Version),
		Surface:         string(removal.Surface), Status: "removed", CapabilityGrant: false,
		PackageObjectRetained:       removal.PackageObjectRetained,
		HistoricalRecoveryPreserved: removal.HistoricalRecoveryPreserved,
		FutureSelectionEnabled:      removal.FutureSelectionEnabled,
		RunSelectionAuthorized:      removal.RunSelectionAuthorized,
		ContextInjectionAuthorized:  removal.ContextInjectionAuthorized,
		ToolCapabilityGrant:         removal.ToolCapabilityGrant,
		RemovedBy:                   removal.RemovedBy,
		RemovedAt:                   removal.CreatedAt.UTC().Format(time.RFC3339Nano),
		Replayed:                    result.Replayed,
		Receipt: operationReceiptView(operationreceipt.Settled(
			operationreceipt.KindSkillPackageRemove, result.Replayed, false)),
	}
}
