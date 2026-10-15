package skills

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolcontract"
)

const (
	ExternalSelectionProtocolVersion       = "external_skill_selection.v1"
	PluginExternalSelectionProtocolVersion = "external_skill_selection.v2"
	DefaultExternalSelectionTokenBudget    = 2048
	MaxExternalSelectionTokenBudget        = 4096
	MaxExternalSelectionItems              = 4
)

// PluginSkillBinding pins a real installation component. It is not an install
// receipt and grants no execution capability.
type PluginSkillBinding struct {
	Native      bool   `json:"native,omitempty"`
	PackageID   string `json:"package_id"`
	ComponentID string `json:"component_id"`
	Revision    string `json:"revision"`
	Generation  int64  `json:"generation"`
}

func (p PluginSkillBinding) Validate() error {
	if (toolcontract.ComponentRef{PackageID: p.PackageID, ComponentID: p.ComponentID}).Validate() != nil ||
		!validSHA256(p.Revision) || p.Generation < 1 {
		return errors.New("selected Plugin component binding is invalid")
	}
	return nil
}

// ExternalSelectionSource adapts verified metadata to the single selection
// resolver. Plugin sources leave legacy result fingerprints and object keys empty.
type ExternalSelectionSource struct {
	Item     ExternalSelectionItem
	Manifest Manifest
}

// ExternalSelectionItem pins either a historical object or a real Plugin component.
// Declared tools remain metadata and never become capabilities.
type ExternalSelectionItem struct {
	Plugin                   *PluginSkillBinding `json:",omitempty"`
	SelectionID              string
	Ordinal                  int
	InstallationID           string
	InstallationFingerprint  string
	InstallResultFingerprint string
	Name                     string
	Version                  string
	Surface                  domain.ExecutionSurface
	ContentSHA256            string
	ContentBytes             int
	TokenUpperBound          int
	ArchiveSHA256            string
	ArchiveBytes             int
	PackageFingerprint       string
	ObjectKey                string
	TrustClass               PackageTrustClass
	ToolDependencyCount      int
	SpecialistEligible       bool
}

type ExternalSelection struct {
	ID                        string
	RunID                     string
	MissionID                 string
	ModeSnapshotID            string
	ModeRevision              int64
	ProtocolVersion           string
	Surface                   domain.ExecutionSurface
	Profile                   domain.Profile
	TokenBudget               int
	TokenUpperBound           int
	ItemCount                 int
	Fingerprint               string
	RequestedBy               string
	OperatorConfirmed         bool
	ContextDeliveryAuthorized bool
	ToolCapabilityGrant       bool
	Items                     []ExternalSelectionItem
	CreatedAt                 time.Time
}

type ExternalSelectionOperation struct {
	KeyDigest          string
	RequestFingerprint string
	SelectionID        string
	RunID              string
	RequestedBy        string
	CreatedAt          time.Time
}

type ResolveExternalSelectionRequest struct {
	SelectionID    string
	RunID          string
	MissionID      string
	ModeSnapshotID string
	ModeRevision   int64
	Surface        domain.ExecutionSurface
	Phase          domain.ExecutionPhase
	Profile        domain.Profile
	Packages       []InstalledPackage
	Sources        []ExternalSelectionSource
	SpecialistRef  string
	TokenBudget    int
	RequestedBy    string
	Confirmed      bool
	CreatedAt      time.Time
}

func ResolveExternalSelection(request ResolveExternalSelectionRequest) (ExternalSelection, error) {
	request.SelectionID = strings.TrimSpace(request.SelectionID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.MissionID = strings.TrimSpace(request.MissionID)
	request.ModeSnapshotID = strings.TrimSpace(request.ModeSnapshotID)
	request.RequestedBy = strings.TrimSpace(request.RequestedBy)
	request.SpecialistRef = strings.TrimSpace(request.SpecialistRef)
	request.CreatedAt = request.CreatedAt.UTC()
	if request.Phase == "" {
		request.Phase = domain.ExecutionPhaseDeliver
	}
	if !request.Confirmed {
		return ExternalSelection{}, errors.New("external Skill context selection requires explicit operator confirmation")
	}
	if count := len(request.Packages) + len(request.Sources); count == 0 || count > MaxExternalSelectionItems {
		return ExternalSelection{}, fmt.Errorf("external Skill selection requires between 1 and %d packages", MaxExternalSelectionItems)
	}
	if request.TokenBudget <= 0 || request.TokenBudget > MaxExternalSelectionTokenBudget {
		return ExternalSelection{}, fmt.Errorf("external Skill selection token budget must be between 1 and %d", MaxExternalSelectionTokenBudget)
	}
	if !request.Surface.Valid() || !request.Phase.Valid() || request.ModeRevision <= 0 {
		return ExternalSelection{}, errors.New("external Skill selection mode is invalid")
	}
	profile, err := domain.ParseProfile(string(request.Profile))
	if err != nil || profile != request.Profile {
		return ExternalSelection{}, fmt.Errorf("invalid external Skill selection profile %q", request.Profile)
	}
	if request.Surface == domain.ExecutionSurfaceCyber && request.Profile != domain.ProfileScript {
		return ExternalSelection{}, errors.New("cyber external Skills are restricted to the script Profile")
	}

	sources := append([]ExternalSelectionSource(nil), request.Sources...)
	for _, installed := range request.Packages {
		if err := installed.Validate(); err != nil {
			return ExternalSelection{}, fmt.Errorf("selected external Skill package is invalid: %w", err)
		}
		if installed.Removal != nil {
			return ExternalSelection{}, fmt.Errorf("selected external Skill %q has been removed", FormatInstalledPackageRef(installed.Installation.Name, installed.Installation.Version))
		}
		i, r := installed.Installation, installed.Result
		sources = append(sources, ExternalSelectionSource{Manifest: i.Manifest, Item: ExternalSelectionItem{
			InstallationID: i.ID, InstallationFingerprint: i.InstallationFingerprint, InstallResultFingerprint: r.ResultFingerprint,
			Name: i.Name, Version: i.Version, Surface: i.Surface, ContentSHA256: i.Manifest.ContentSHA256,
			ContentBytes: i.Manifest.ContentBytes, TokenUpperBound: i.Manifest.ContentTokenUpperBound,
			ArchiveSHA256: i.ArchiveSHA256, ArchiveBytes: i.ArchiveBytes, PackageFingerprint: i.PackageFingerprint,
			ObjectKey: r.ObjectKey, TrustClass: i.TrustClass, ToolDependencyCount: len(i.Manifest.ToolDependencies),
		}})
	}
	sort.Slice(sources, func(a, b int) bool {
		return ExternalSelectionItemRef(sources[a].Item) < ExternalSelectionItemRef(sources[b].Item)
	})
	items := make([]ExternalSelectionItem, 0, len(sources))
	tokens := 0
	protocol := ExternalSelectionProtocolVersion
	specialistFound := request.SpecialistRef == ""
	previousRef := ""
	for index, source := range sources {
		item, manifest := source.Item, source.Manifest
		ref := ExternalSelectionItemRef(item)
		if ref == previousRef {
			return ExternalSelection{}, fmt.Errorf("selected external Skill %q is duplicated", ref)
		}
		if item.Surface != request.Surface || !containsProfile(manifest.Profiles, request.Profile) {
			return ExternalSelection{}, fmt.Errorf("selected external Skill %q is incompatible with %s/%s", ref, request.Surface, request.Profile)
		}
		specialist := request.SpecialistRef != "" && ref == request.SpecialistRef
		if manifest.HasModeMetadata() {
			supports := func(role domain.AgentRole) bool {
				if item.Plugin != nil {
					return manifest.SupportsContext(ExecutionContext{Surface: request.Surface, Phase: request.Phase, Profile: request.Profile, Role: role})
				}
				return supportsExternalSelectionAcrossPhases(manifest, request.Surface, request.Profile, role)
			}
			if !manifest.AllowsInvocation(InvocationSourceUser, true) || !supports(domain.AgentRoleRoot) {
				if item.Plugin == nil {
					return ExternalSelection{}, fmt.Errorf("selected external Skill %q must support root delivery in both Plan and Deliver because the external selection is immutable", ref)
				}
				return ExternalSelection{}, fmt.Errorf("selected external Skill %q does not support explicit Root delivery in its selected mode", ref)
			}
			if specialist && !supports(domain.AgentRoleSpecialist) {
				if item.Plugin == nil {
					return ExternalSelection{}, fmt.Errorf("selected external Skill %q must support the Specialist role in both phases", ref)
				}
				return ExternalSelection{}, fmt.Errorf("selected external Skill %q does not support explicit Specialist delivery", ref)
			}
		}
		if item.Plugin != nil {
			protocol = PluginExternalSelectionProtocolVersion
		}
		if specialist {
			specialistFound = true
		}
		item.SelectionID, item.Ordinal, item.SpecialistEligible = request.SelectionID, index+1, specialist
		if specialist && item.TokenUpperBound > MaxExternalSpecialistTokenBudget {
			return ExternalSelection{}, fmt.Errorf(
				"specialist external Skill %q exceeds the %d token hard limit",
				ref, MaxExternalSpecialistTokenBudget)
		}
		items = append(items, item)
		tokens += item.TokenUpperBound
		previousRef = ref
	}
	if !specialistFound {
		return ExternalSelection{}, fmt.Errorf("specialist external Skill %q is not selected", request.SpecialistRef)
	}
	if tokens > request.TokenBudget {
		return ExternalSelection{}, fmt.Errorf("external Skills require token upper bound %d, budget is %d", tokens, request.TokenBudget)
	}
	selection := ExternalSelection{
		ID: request.SelectionID, RunID: request.RunID, MissionID: request.MissionID,
		ModeSnapshotID: request.ModeSnapshotID, ModeRevision: request.ModeRevision,
		ProtocolVersion: protocol, Surface: request.Surface,
		Profile: request.Profile, TokenBudget: request.TokenBudget,
		TokenUpperBound: tokens, ItemCount: len(items), RequestedBy: request.RequestedBy,
		OperatorConfirmed: true, ContextDeliveryAuthorized: true,
		Items: items, CreatedAt: request.CreatedAt,
	}
	selection.Fingerprint = ExternalSelectionFingerprint(selection)
	if err := selection.Validate(); err != nil {
		return ExternalSelection{}, err
	}
	return CloneExternalSelection(selection), nil
}

func supportsExternalSelectionAcrossPhases(manifest Manifest,
	surface domain.ExecutionSurface, profile domain.Profile, role domain.AgentRole,
) bool {
	for _, phase := range []domain.ExecutionPhase{
		domain.ExecutionPhasePlan, domain.ExecutionPhaseDeliver,
	} {
		if !manifest.SupportsContext(ExecutionContext{
			Surface: surface, Phase: phase, Profile: profile, Role: role,
		}) {
			return false
		}
	}
	return true
}

func (s ExternalSelection) Validate() error {
	for _, value := range []string{s.ID, s.RunID, s.MissionID, s.ModeSnapshotID, s.RequestedBy} {
		if !validSelectionIdentity(value) {
			return errors.New("external Skill selection identities are invalid")
		}
	}
	if (s.ProtocolVersion != ExternalSelectionProtocolVersion && s.ProtocolVersion != PluginExternalSelectionProtocolVersion) || s.ModeRevision <= 0 ||
		!s.Surface.Valid() || !s.OperatorConfirmed || !s.ContextDeliveryAuthorized ||
		s.ToolCapabilityGrant || !validUTC(s.CreatedAt) {
		return errors.New("external Skill selection protocol or capability boundary is invalid")
	}
	profile, err := domain.ParseProfile(string(s.Profile))
	if err != nil || profile != s.Profile ||
		(s.Surface == domain.ExecutionSurfaceCyber && s.Profile != domain.ProfileScript) {
		return errors.New("external Skill selection surface or Profile is invalid")
	}
	if s.TokenBudget <= 0 || s.TokenBudget > MaxExternalSelectionTokenBudget ||
		s.TokenUpperBound <= 0 || s.TokenUpperBound > s.TokenBudget ||
		len(s.Items) == 0 || len(s.Items) > MaxExternalSelectionItems ||
		s.ItemCount != len(s.Items) {
		return errors.New("external Skill selection bounds are invalid")
	}
	total := 0
	previousRef := ""
	specialistCount := 0
	pluginCount := 0
	for index, item := range s.Items {
		ref := ExternalSelectionItemRef(item)
		maxArchiveBytes := MaxPackageArchiveBytes
		if nativeSelectionBinding(item.Plugin) {
			maxArchiveBytes = 4 * 1024 * 1024 // Portable Plugin acquisition limit.
		}
		wantKey, keyErr := PackageObjectKey(item.ArchiveSHA256)
		objectValid := validSHA256(item.InstallResultFingerprint) && keyErr == nil && item.ObjectKey == wantKey
		if item.Plugin != nil {
			pluginCount++
			objectValid = item.Plugin.Validate() == nil && item.Plugin.Revision == item.ArchiveSHA256 && item.InstallResultFingerprint == "" && item.ObjectKey == ""
		}
		if item.SelectionID != s.ID || item.Ordinal != index+1 ||
			!validPackageIdentity(item.InstallationID) ||
			!validSHA256(item.InstallationFingerprint) ||
			!objectValid || !validExternalSelectionIdentity(item.Name, item.Version, nativeSelectionBinding(item.Plugin)) || item.Surface != s.Surface ||
			!validSHA256(item.ContentSHA256) || item.ContentBytes <= 0 ||
			item.ContentBytes > MaxContentBytes || item.TokenUpperBound != item.ContentBytes ||
			item.TokenUpperBound > MaxContentTokenUpperBound ||
			item.ArchiveBytes <= 0 || item.ArchiveBytes > maxArchiveBytes ||
			!validSHA256(item.PackageFingerprint) ||
			item.TrustClass != PackageTrustOperatorInstalledUntrusted ||
			item.ToolDependencyCount < 0 || item.ToolDependencyCount > MaxToolDependencies ||
			(previousRef != "" && previousRef >= ref) {
			return fmt.Errorf("external Skill selection item %d is invalid", index+1)
		}
		if item.SpecialistEligible {
			if item.TokenUpperBound > MaxExternalSpecialistTokenBudget {
				return fmt.Errorf("external Skill selection item %d exceeds the Specialist hard limit", index+1)
			}
			specialistCount++
		}
		total += item.TokenUpperBound
		previousRef = ref
	}
	if (s.ProtocolVersion == PluginExternalSelectionProtocolVersion) != (pluginCount > 0) || total != s.TokenUpperBound || specialistCount > 1 ||
		!validSHA256(s.Fingerprint) || s.Fingerprint != ExternalSelectionFingerprint(s) {
		return errors.New("external Skill selection accounting or fingerprint is invalid")
	}
	return nil
}

func (o ExternalSelectionOperation) Validate() error {
	if !validSHA256(o.KeyDigest) || !validSHA256(o.RequestFingerprint) ||
		!validSelectionIdentity(o.SelectionID) || !validSelectionIdentity(o.RunID) ||
		!validSelectionIdentity(o.RequestedBy) || !validUTC(o.CreatedAt) {
		return errors.New("external Skill selection operation is invalid")
	}
	return nil
}

func ExternalSelectionFingerprint(s ExternalSelection) string {
	parts := []string{s.ProtocolVersion, s.ID, s.RunID, s.MissionID,
		s.ModeSnapshotID, strconv.FormatInt(s.ModeRevision, 10), string(s.Surface),
		string(s.Profile), strconv.Itoa(s.TokenBudget), strconv.Itoa(len(s.Items)),
		s.RequestedBy, s.CreatedAt.UTC().Format(time.RFC3339Nano),
		"operator_confirmed=true", "context_delivery=true", "tool_grant=false"}
	for _, item := range s.Items {
		parts = append(parts, strconv.Itoa(item.Ordinal), item.InstallationID,
			item.InstallationFingerprint, item.InstallResultFingerprint, item.Name,
			item.Version, string(item.Surface), item.ContentSHA256,
			strconv.Itoa(item.ContentBytes), strconv.Itoa(item.TokenUpperBound),
			item.ArchiveSHA256, strconv.Itoa(item.ArchiveBytes), item.PackageFingerprint,
			item.ObjectKey, string(item.TrustClass), strconv.Itoa(item.ToolDependencyCount),
			strconv.FormatBool(item.SpecialistEligible))
		parts = append(parts, pluginSelectionFingerprintParts(item.Plugin)...)
	}
	return runmutation.Fingerprint(parts...)
}

func ExternalSelectionRequestFingerprint(s ExternalSelection) string {
	intentProtocol := "external_skill_selection_intent.v1"
	if s.ProtocolVersion == PluginExternalSelectionProtocolVersion {
		intentProtocol = "external_skill_selection_intent.v2"
	}
	parts := []string{intentProtocol, s.RunID, s.MissionID,
		s.ModeSnapshotID, strconv.FormatInt(s.ModeRevision, 10), string(s.Surface),
		string(s.Profile), strconv.Itoa(s.TokenBudget), s.RequestedBy,
		"operator_confirmed=true", "context_delivery=true", "tool_grant=false"}
	for _, item := range s.Items {
		parts = append(parts, item.InstallationID, item.InstallationFingerprint,
			item.InstallResultFingerprint, item.Name, item.Version, item.ContentSHA256,
			item.ArchiveSHA256, item.PackageFingerprint, item.ObjectKey,
			strconv.FormatBool(item.SpecialistEligible))
		parts = append(parts, pluginSelectionFingerprintParts(item.Plugin)...)
	}
	return runmutation.Fingerprint(parts...)
}

func CloneExternalSelection(value ExternalSelection) ExternalSelection {
	value.Items = append([]ExternalSelectionItem(nil), value.Items...)
	for i := range value.Items {
		value.Items[i].Plugin = clonePluginSkillBinding(value.Items[i].Plugin)
	}
	return value
}

func ExternalSpecialistItem(selection ExternalSelection) (ExternalSelectionItem, bool) {
	for _, item := range selection.Items {
		if item.SpecialistEligible {
			return item, true
		}
	}
	return ExternalSelectionItem{}, false
}

func pluginSelectionFingerprintParts(p *PluginSkillBinding) []string {
	if p == nil {
		return nil
	}
	parts := []string{"plugin-component", p.PackageID, p.ComponentID, p.Revision, strconv.FormatInt(p.Generation, 10)}
	if p.Native {
		parts = append(parts, "native=true")
	}
	return parts
}

func clonePluginSkillBinding(value *PluginSkillBinding) *PluginSkillBinding {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
