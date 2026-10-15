package skills

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/toolgateway"
)

const (
	PackageInstallationProtocolVersion   = "skill_package_installation.v1"
	PackageInstallResultProtocolVersion  = "skill_package_install_result.v1"
	PackageRemovalProtocolVersion        = "skill_package_removal.v1"
	PackageObjectProtocolVersion         = "skill_package_object.v1"
	PackageInventoryProtocolVersion      = "skill_package_inventory.v1"
	PackageRemovalControlProtocolVersion = "skill_package_removal_control.v1"
	PackageRemovalRequestProtocolVersion = "skill_package_removal_request.v1"
	MaxInstalledPackageIdentities        = MaxSkills
	PackageObjectRoot                    = "skill-registry/objects"
)

// PackageInstallation is an immutable, metadata-only intent. Its presence
// never makes an external Skill selectable or executable.
type PackageInstallation struct {
	ID                         string
	ProtocolVersion            string
	Name                       string
	Version                    string
	Surface                    domain.ExecutionSurface
	Manifest                   Manifest
	ArchiveSHA256              string
	PackageFingerprint         string
	ArchiveBytes               int
	UncompressedBytes          int
	EntryCount                 int
	TrustClass                 PackageTrustClass
	RiskCodes                  []PackageRiskCode
	ExecutableAssetCount       int
	InstallHookCount           int
	ImportCommandExecution     bool
	ImportNetworkAccess        bool
	ImportProviderCalls        bool
	ToolCapabilityGrant        bool
	RunSelectionAuthorized     bool
	ContextInjectionAuthorized bool
	OperatorConfirmed          bool
	OperationKeyDigest         string
	RequestFingerprint         string
	InstallationFingerprint    string
	InstalledBy                string
	CreatedAt                  time.Time
}

type PackageInstallOperation struct {
	KeyDigest          string
	RequestFingerprint string
	InstallationID     string
	Name               string
	Version            string
	Surface            domain.ExecutionSurface
	InstalledBy        string
	CreatedAt          time.Time
}

type PackageObjectDescriptor struct {
	ProtocolVersion    string
	ArchiveSHA256      string
	PackageFingerprint string
	ArchiveBytes       int
}

type PackageObjectReceipt struct {
	Descriptor PackageObjectDescriptor
	ObjectKey  string
}

type PackageInstallResult struct {
	ProtocolVersion            string
	InstallationID             string
	InstallationFingerprint    string
	ObjectKey                  string
	ArchiveSHA256              string
	PackageFingerprint         string
	ObjectBytes                int
	ObjectVerified             bool
	RunSelectionAuthorized     bool
	ContextInjectionAuthorized bool
	ToolCapabilityGrant        bool
	ResultFingerprint          string
	CompletedAt                time.Time
}

// PackageRemoval is an append-only tombstone. The content-addressed object is
// deliberately retained for audit and historical recovery.
type PackageRemoval struct {
	ID                          string
	ProtocolVersion             string
	InstallationID              string
	InstallationFingerprint     string
	Name                        string
	Version                     string
	Surface                     domain.ExecutionSurface
	ContentSHA256               string
	ArchiveSHA256               string
	PackageFingerprint          string
	OperationKeyDigest          string
	RequestFingerprint          string
	PackageObjectRetained       bool
	HistoricalRecoveryPreserved bool
	FutureSelectionEnabled      bool
	RunSelectionAuthorized      bool
	ContextInjectionAuthorized  bool
	ToolCapabilityGrant         bool
	RemovalFingerprint          string
	RemovedBy                   string
	CreatedAt                   time.Time
}

type PackageRemoveOperation struct {
	KeyDigest          string
	RequestFingerprint string
	RemovalID          string
	InstallationID     string
	Name               string
	Version            string
	Surface            domain.ExecutionSurface
	RemovedBy          string
	CreatedAt          time.Time
}

type InstalledPackage struct {
	Installation PackageInstallation
	Result       PackageInstallResult
	Removal      *PackageRemoval
	Replayed     bool
}

func NewPackageInstallation(id string, parsed *SkillPackage, surface domain.ExecutionSurface,
	operationKeyDigest, installedBy string, createdAt time.Time,
) (PackageInstallation, error) {
	if parsed == nil {
		return PackageInstallation{}, errors.New("validated Skill package is required")
	}
	preview := parsed.Preview()
	preview.Manifest.Description = redact.String(preview.Manifest.Description)
	value := PackageInstallation{
		ID: id, ProtocolVersion: PackageInstallationProtocolVersion,
		Name: preview.Manifest.Name, Version: preview.Manifest.Version,
		Surface: surface, Manifest: preview.Manifest,
		ArchiveSHA256:      preview.ArchiveSHA256,
		PackageFingerprint: preview.PackageFingerprint,
		ArchiveBytes:       preview.ArchiveBytes, UncompressedBytes: preview.UncompressedBytes,
		EntryCount: preview.EntryCount, TrustClass: preview.TrustClass,
		RiskCodes:              slices.Clone(preview.RiskCodes),
		ExecutableAssetCount:   preview.ExecutableAssetCount,
		InstallHookCount:       preview.InstallHookCount,
		ImportCommandExecution: preview.ImportCommandExecution,
		ImportNetworkAccess:    preview.ImportNetworkAccess,
		ImportProviderCalls:    preview.ImportProviderCalls,
		ToolCapabilityGrant:    preview.ToolCapabilityGrant,
		OperationKeyDigest:     operationKeyDigest,
		InstalledBy:            strings.TrimSpace(installedBy), CreatedAt: createdAt.UTC(),
		OperatorConfirmed: true,
	}
	// A valid signature is evidence of provenance, not trust: signed packages
	// install under the same untrusted class. The signature identity is kept
	// by the catalog import ledger, which owns the trust decision.
	if value.TrustClass == PackageTrustSignedUntrusted {
		value.TrustClass = PackageTrustOperatorInstalledUntrusted
	}
	value.RequestFingerprint = PackageInstallationIntentFingerprint(value)
	value.InstallationFingerprint = PackageInstallationFingerprint(value)
	if err := value.Validate(); err != nil {
		return PackageInstallation{}, err
	}
	return ClonePackageInstallation(value), nil
}

func (i PackageInstallation) Validate() error {
	if !validPackageIdentity(i.ID) || i.ProtocolVersion != PackageInstallationProtocolVersion ||
		i.Name != i.Manifest.Name || i.Version != i.Manifest.Version {
		return errors.New("skill package installation identity is invalid")
	}
	if !i.Surface.Valid() || !validPackageActor(i.InstalledBy) || !validUTC(i.CreatedAt) {
		return errors.New("skill package installation surface, operator, or timestamp is invalid")
	}
	if err := validatePackageManifestMetadata(i.Manifest); err != nil {
		return err
	}
	if err := i.Manifest.ValidateInstallationSurface(i.Surface); err != nil {
		return err
	}
	if !validSHA256(i.ArchiveSHA256) || !validSHA256(i.PackageFingerprint) ||
		i.ArchiveBytes <= 0 || i.ArchiveBytes > MaxPackageArchiveBytes ||
		i.UncompressedBytes <= 0 || i.UncompressedBytes > SignedPackageUncompressedBytes ||
		(i.EntryCount != PackageEntryCount && i.EntryCount != SignedPackageEntryCount) ||
		i.TrustClass != PackageTrustOperatorInstalledUntrusted {
		return errors.New("skill package installation archive metadata is invalid")
	}
	wantRisks := []PackageRiskCode{
		PackageRiskUntrustedInstructions,
		PackageRiskDeclaredToolsOnly,
	}
	if !slices.Equal(i.RiskCodes, wantRisks) || i.ExecutableAssetCount != 0 ||
		i.InstallHookCount != 0 || i.ImportCommandExecution || i.ImportNetworkAccess ||
		i.ImportProviderCalls || i.ToolCapabilityGrant || i.RunSelectionAuthorized ||
		i.ContextInjectionAuthorized || !i.OperatorConfirmed {
		return errors.New("skill package installation capability boundary is invalid")
	}
	if !validSHA256(i.OperationKeyDigest) || !validSHA256(i.RequestFingerprint) ||
		i.RequestFingerprint != PackageInstallationIntentFingerprint(i) ||
		!validSHA256(i.InstallationFingerprint) ||
		i.InstallationFingerprint != PackageInstallationFingerprint(i) {
		return errors.New("skill package installation fingerprint is invalid")
	}
	return nil
}

func (o PackageInstallOperation) Validate() error {
	if !validSHA256(o.KeyDigest) || !validSHA256(o.RequestFingerprint) ||
		!validPackageIdentity(o.InstallationID) || !validName(o.Name) ||
		!validCoreVersion(o.Version) || !o.Surface.Valid() ||
		!validPackageActor(o.InstalledBy) || !validUTC(o.CreatedAt) {
		return errors.New("skill package installation operation is invalid")
	}
	return nil
}

func PackageInstallationIntentFingerprint(i PackageInstallation) string {
	if i.Manifest.HasModeMetadata() {
		return runmutation.Fingerprint("skill_package_installation_intent.v2",
			i.Name, i.Version, string(i.Surface), i.Manifest.Protocol,
			i.Manifest.Description, strings.Join(profileStrings(i.Manifest.Profiles), ","),
			strings.Join(surfaceStrings(i.Manifest.Surfaces), ","),
			strings.Join(phaseStrings(i.Manifest.Phases), ","),
			strings.Join(roleStrings(i.Manifest.Roles), ","),
			"user_invocable="+strconv.FormatBool(i.Manifest.UserInvocable),
			"model_invocable="+strconv.FormatBool(i.Manifest.ModelInvocable),
			"explicit_only="+strconv.FormatBool(i.Manifest.ExplicitOnly),
			strings.Join(toolStrings(i.Manifest.ToolDependencies), ","), i.Manifest.ContentPath,
			i.Manifest.ContentSHA256, strconv.Itoa(i.Manifest.ContentBytes),
			strconv.Itoa(i.Manifest.ContentTokenUpperBound), i.ArchiveSHA256,
			i.PackageFingerprint, strconv.Itoa(i.ArchiveBytes),
			strconv.Itoa(i.UncompressedBytes), strconv.Itoa(i.EntryCount), string(i.TrustClass),
			strings.Join(riskStrings(i.RiskCodes), ","), i.InstalledBy,
			"command=false", "network=false", "provider=false", "tool_grant=false",
			"run_selection=false", "context_injection=false")
	}
	return runmutation.Fingerprint("skill_package_installation_intent.v1",
		i.Name, i.Version, string(i.Surface), i.Manifest.Protocol,
		i.Manifest.Description, strings.Join(profileStrings(i.Manifest.Profiles), ","),
		strings.Join(toolStrings(i.Manifest.ToolDependencies), ","), i.Manifest.ContentPath,
		i.Manifest.ContentSHA256, strconv.Itoa(i.Manifest.ContentBytes),
		strconv.Itoa(i.Manifest.ContentTokenUpperBound), i.ArchiveSHA256,
		i.PackageFingerprint, strconv.Itoa(i.ArchiveBytes),
		strconv.Itoa(i.UncompressedBytes), strconv.Itoa(i.EntryCount), string(i.TrustClass),
		strings.Join(riskStrings(i.RiskCodes), ","), i.InstalledBy,
		"command=false", "network=false", "provider=false", "tool_grant=false",
		"run_selection=false", "context_injection=false")
}

func PackageInstallationFingerprint(i PackageInstallation) string {
	return runmutation.Fingerprint("skill_package_installation.v1", i.ID,
		i.OperationKeyDigest, i.RequestFingerprint, i.CreatedAt.UTC().Format(time.RFC3339Nano))
}

func DescriptorForInstallation(i PackageInstallation) PackageObjectDescriptor {
	return PackageObjectDescriptor{
		ProtocolVersion: PackageObjectProtocolVersion, ArchiveSHA256: i.ArchiveSHA256,
		PackageFingerprint: i.PackageFingerprint, ArchiveBytes: i.ArchiveBytes,
	}
}

func (d PackageObjectDescriptor) Validate() error {
	if d.ProtocolVersion != PackageObjectProtocolVersion || !validSHA256(d.ArchiveSHA256) ||
		!validSHA256(d.PackageFingerprint) || d.ArchiveBytes <= 0 ||
		d.ArchiveBytes > MaxPackageArchiveBytes {
		return errors.New("skill package object descriptor is invalid")
	}
	return nil
}

func PackageObjectKey(archiveSHA256 string) (string, error) {
	if !validSHA256(archiveSHA256) {
		return "", errors.New("skill package object digest is invalid")
	}
	return "sha256/" + archiveSHA256[:2] + "/" + archiveSHA256 + ".zip", nil
}

func ValidatePackageObjectReceipt(expected PackageObjectDescriptor,
	receipt PackageObjectReceipt,
) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	if err := receipt.Descriptor.Validate(); err != nil {
		return err
	}
	wantKey, _ := PackageObjectKey(expected.ArchiveSHA256)
	if receipt.Descriptor != expected || receipt.ObjectKey != wantKey {
		return errors.New("skill package object receipt does not match its descriptor")
	}
	return nil
}

func NewPackageInstallResult(installation PackageInstallation, receipt PackageObjectReceipt,
	completedAt time.Time,
) (PackageInstallResult, error) {
	if err := installation.Validate(); err != nil {
		return PackageInstallResult{}, err
	}
	if err := ValidatePackageObjectReceipt(DescriptorForInstallation(installation), receipt); err != nil {
		return PackageInstallResult{}, err
	}
	result := PackageInstallResult{
		ProtocolVersion:         PackageInstallResultProtocolVersion,
		InstallationID:          installation.ID,
		InstallationFingerprint: installation.InstallationFingerprint,
		ObjectKey:               receipt.ObjectKey, ArchiveSHA256: receipt.Descriptor.ArchiveSHA256,
		PackageFingerprint: receipt.Descriptor.PackageFingerprint,
		ObjectBytes:        receipt.Descriptor.ArchiveBytes, ObjectVerified: true,
		CompletedAt: completedAt.UTC(),
	}
	result.ResultFingerprint = PackageInstallResultFingerprint(result)
	if err := result.Validate(); err != nil {
		return PackageInstallResult{}, err
	}
	return result, nil
}

func (r PackageInstallResult) Validate() error {
	wantKey, err := PackageObjectKey(r.ArchiveSHA256)
	if err != nil || r.ProtocolVersion != PackageInstallResultProtocolVersion ||
		!validPackageIdentity(r.InstallationID) || !validSHA256(r.InstallationFingerprint) ||
		r.ObjectKey != wantKey || !validSHA256(r.PackageFingerprint) ||
		r.ObjectBytes <= 0 || r.ObjectBytes > MaxPackageArchiveBytes || !r.ObjectVerified ||
		r.RunSelectionAuthorized || r.ContextInjectionAuthorized || r.ToolCapabilityGrant ||
		!validUTC(r.CompletedAt) || !validSHA256(r.ResultFingerprint) ||
		r.ResultFingerprint != PackageInstallResultFingerprint(r) {
		return errors.New("skill package installation result is invalid")
	}
	return nil
}

func PackageInstallResultFingerprint(r PackageInstallResult) string {
	return runmutation.Fingerprint("skill_package_install_result.v1", r.InstallationID,
		r.InstallationFingerprint, r.ObjectKey, r.ArchiveSHA256, r.PackageFingerprint,
		strconv.Itoa(r.ObjectBytes), r.CompletedAt.UTC().Format(time.RFC3339Nano),
		"verified=true", "run_selection=false", "context_injection=false", "tool_grant=false")
}

func NewPackageRemoval(id string, installation PackageInstallation, operationKeyDigest,
	removedBy string, createdAt time.Time,
) (PackageRemoval, error) {
	value := PackageRemoval{
		ID: id, ProtocolVersion: PackageRemovalProtocolVersion,
		InstallationID:          installation.ID,
		InstallationFingerprint: installation.InstallationFingerprint,
		Name:                    installation.Name, Version: installation.Version, Surface: installation.Surface,
		ContentSHA256:         installation.Manifest.ContentSHA256,
		ArchiveSHA256:         installation.ArchiveSHA256,
		PackageFingerprint:    installation.PackageFingerprint,
		OperationKeyDigest:    operationKeyDigest,
		PackageObjectRetained: true, HistoricalRecoveryPreserved: true,
		RemovedBy: strings.TrimSpace(removedBy), CreatedAt: createdAt.UTC(),
	}
	value.RequestFingerprint = PackageRemovalIntentFingerprint(value)
	value.RemovalFingerprint = PackageRemovalFingerprint(value)
	if err := value.Validate(); err != nil {
		return PackageRemoval{}, err
	}
	return value, nil
}

func (r PackageRemoval) Validate() error {
	if !validPackageIdentity(r.ID) || r.ProtocolVersion != PackageRemovalProtocolVersion ||
		!validPackageIdentity(r.InstallationID) || !validSHA256(r.InstallationFingerprint) ||
		!validName(r.Name) || !validCoreVersion(r.Version) || !r.Surface.Valid() ||
		!validSHA256(r.ContentSHA256) || !validSHA256(r.ArchiveSHA256) ||
		!validSHA256(r.PackageFingerprint) || !validSHA256(r.OperationKeyDigest) ||
		!validPackageActor(r.RemovedBy) || !validUTC(r.CreatedAt) ||
		!r.PackageObjectRetained || !r.HistoricalRecoveryPreserved ||
		r.FutureSelectionEnabled || r.RunSelectionAuthorized ||
		r.ContextInjectionAuthorized || r.ToolCapabilityGrant ||
		!validSHA256(r.RequestFingerprint) ||
		r.RequestFingerprint != PackageRemovalIntentFingerprint(r) ||
		!validSHA256(r.RemovalFingerprint) ||
		r.RemovalFingerprint != PackageRemovalFingerprint(r) {
		return errors.New("skill package removal tombstone is invalid")
	}
	return nil
}

func PackageRemovalIntentFingerprint(r PackageRemoval) string {
	return runmutation.Fingerprint("skill_package_removal_intent.v1", r.InstallationID,
		r.InstallationFingerprint, r.Name, r.Version, string(r.Surface), r.ContentSHA256,
		r.ArchiveSHA256, r.PackageFingerprint, r.RemovedBy,
		"object_retained=true", "historical_recovery=true", "future_selection=false",
		"run_selection=false", "context_injection=false", "tool_grant=false")
}

func PackageRemovalFingerprint(r PackageRemoval) string {
	return runmutation.Fingerprint("skill_package_removal.v1", r.ID,
		r.OperationKeyDigest, r.RequestFingerprint, r.CreatedAt.UTC().Format(time.RFC3339Nano))
}

func (o PackageRemoveOperation) Validate() error {
	if !validSHA256(o.KeyDigest) || !validSHA256(o.RequestFingerprint) ||
		!validPackageIdentity(o.RemovalID) || !validPackageIdentity(o.InstallationID) ||
		!validName(o.Name) || !validCoreVersion(o.Version) || !o.Surface.Valid() ||
		!validPackageActor(o.RemovedBy) || !validUTC(o.CreatedAt) {
		return errors.New("skill package removal operation is invalid")
	}
	return nil
}

func (p InstalledPackage) Validate() error {
	if err := p.Installation.Validate(); err != nil {
		return err
	}
	if err := p.Result.Validate(); err != nil {
		return err
	}
	if p.Result.InstallationID != p.Installation.ID ||
		p.Result.InstallationFingerprint != p.Installation.InstallationFingerprint ||
		p.Result.ArchiveSHA256 != p.Installation.ArchiveSHA256 ||
		p.Result.PackageFingerprint != p.Installation.PackageFingerprint ||
		p.Result.ObjectBytes != p.Installation.ArchiveBytes ||
		p.Result.CompletedAt.Before(p.Installation.CreatedAt) {
		return errors.New("installed Skill package result binding is invalid")
	}
	if p.Removal != nil {
		if err := p.Removal.Validate(); err != nil {
			return err
		}
		if p.Removal.InstallationID != p.Installation.ID ||
			p.Removal.InstallationFingerprint != p.Installation.InstallationFingerprint ||
			p.Removal.Name != p.Installation.Name || p.Removal.Version != p.Installation.Version ||
			p.Removal.Surface != p.Installation.Surface ||
			p.Removal.ContentSHA256 != p.Installation.Manifest.ContentSHA256 ||
			p.Removal.ArchiveSHA256 != p.Installation.ArchiveSHA256 ||
			p.Removal.PackageFingerprint != p.Installation.PackageFingerprint ||
			p.Removal.CreatedAt.Before(p.Result.CompletedAt) {
			return errors.New("installed Skill package removal binding is invalid")
		}
	}
	return nil
}

func ClonePackageInstallation(value PackageInstallation) PackageInstallation {
	value.Manifest = cloneManifest(value.Manifest)
	value.RiskCodes = slices.Clone(value.RiskCodes)
	return value
}

func CloneInstalledPackage(value InstalledPackage) InstalledPackage {
	value.Installation = ClonePackageInstallation(value.Installation)
	if value.Removal != nil {
		removal := *value.Removal
		value.Removal = &removal
	}
	return value
}

func validatePackageManifestMetadata(manifest Manifest) error {
	if manifest.Protocol != ProtocolVersion || !validName(manifest.Name) ||
		!validCoreVersion(manifest.Version) {
		return errors.New("installed Skill manifest identity is invalid")
	}
	if err := validateDescription(manifest.Description); err != nil {
		return err
	}
	if err := validateProfiles(manifest.Profiles); err != nil {
		return err
	}
	if err := validateModeMetadata(manifest); err != nil {
		return err
	}
	if err := validateToolDependencies(manifest.Profiles, manifest.ToolDependencies); err != nil {
		return err
	}
	if err := validateContentPath(manifest.ContentPath); err != nil {
		return err
	}
	if !validSHA256(manifest.ContentSHA256) || manifest.ContentBytes <= 0 ||
		manifest.ContentBytes > MaxContentBytes || manifest.ContentTokenUpperBound <= 0 ||
		manifest.ContentTokenUpperBound > MaxContentTokenUpperBound {
		return errors.New("installed Skill manifest content metadata is invalid")
	}
	return nil
}

func validPackageIdentity(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value &&
		utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validPackageActor(value string) bool {
	if !validPackageIdentity(value) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func validUTC(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}

func profileStrings(values []domain.Profile) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func surfaceStrings(values []domain.ExecutionSurface) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func phaseStrings(values []domain.ExecutionPhase) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func roleStrings(values []domain.AgentRole) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func toolStrings(values []toolgateway.ToolName) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func riskStrings(values []PackageRiskCode) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func FormatInstalledPackageRef(name, version string) string {
	return fmt.Sprintf("%s@%s", name, version)
}

func ParseInstalledPackageRef(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if strings.Count(value, "@") != 1 {
		return "", "", errors.New("installed Skill package reference must be name@version")
	}
	name, version, _ := strings.Cut(value, "@")
	if !validName(name) || !validCoreVersion(version) {
		return "", "", errors.New("installed Skill package reference is invalid")
	}
	return name, version, nil
}

// ValidateInstallationSurface retains the legacy manifest scope for both
// historical installations and new Plugin installations of legacy archives.
func (m Manifest) ValidateInstallationSurface(surface domain.ExecutionSurface) error {
	if !surface.Valid() || (m.HasModeMetadata() && !slices.Contains(m.Surfaces, surface)) {
		return errors.New("installed Skill manifest does not support its installation surface")
	}
	if surface == domain.ExecutionSurfaceCyber && !slices.Equal(m.Profiles, []domain.Profile{domain.ProfileScript}) {
		return errors.New("cyber Skill packages must declare only the script Profile")
	}
	return nil
}
