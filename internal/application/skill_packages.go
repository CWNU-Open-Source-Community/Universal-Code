package application

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/redact"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/toolcontract"
)

type SkillPackageRegistryStore interface {
	CompletePackageInstallation(context.Context, skills.PackageInstallResult) (
		skills.InstalledPackage, bool, error)
	GetPackageInstallOperation(context.Context, string) (
		skills.PackageInstallOperation, bool, error)
	GetPackageInstallation(context.Context, string) (skills.PackageInstallation, error)
	GetInstalledPackageByRef(context.Context, string, string) (
		skills.InstalledPackage, bool, error)
	ListInstalledPackages(context.Context, domain.ExecutionSurface, domain.Profile, bool) (
		[]skills.InstalledPackage, error)
	CreatePackageRemoval(context.Context, skills.PackageRemoval,
		skills.PackageRemoveOperation) (skills.PackageRemoval, bool, error)
	GetPackageRemoveOperation(context.Context, string) (
		skills.PackageRemoveOperation, bool, error)
}

type SkillPackageRegistryService struct {
	store    SkillPackageRegistryStore
	objects  skills.PackageObjectStore
	builtins *skills.Registry
}

type ImportSkillPackageRequest struct {
	Raw              []byte
	Snapshot         *plugins.PortableSnapshot
	Source           plugins.InstallSource
	EnableSkills     bool
	Surface          domain.ExecutionSurface
	OperationKey     string
	InstalledBy      string
	ConfirmUntrusted bool
}

type ImportSkillPackageResult struct {
	Installation     *plugins.Installation
	Package          skills.InstalledPackage
	Replayed         bool
	RecoveredPending bool
}

type ListInstalledSkillPackagesRequest struct {
	Surface        domain.ExecutionSurface
	Profile        domain.Profile
	IncludeRemoved bool
}

type RemoveSkillPackageRequest struct {
	Name          string
	Version       string
	OperationKey  string
	RemovedBy     string
	ConfirmRemove bool
}

type RemoveSkillPackageResult struct {
	Removal  skills.PackageRemoval
	Replayed bool
}

func NewSkillPackageRegistryService(store SkillPackageRegistryStore,
	objects skills.PackageObjectStore, builtins *skills.Registry,
) *SkillPackageRegistryService {
	return &SkillPackageRegistryService{store: store, objects: objects, builtins: builtins}
}

func (s *SkillPackageRegistryService) Import(ctx context.Context,
	request ImportSkillPackageRequest,
) (ImportSkillPackageResult, error) {
	if s == nil || s.store == nil || s.objects == nil || s.builtins == nil {
		return ImportSkillPackageResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Skill package Registry store, object store, and built-in Registry are required")
	}
	if !request.ConfirmUntrusted {
		return ImportSkillPackageResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"untrusted Skill package installation requires explicit operator confirmation")
	}
	surface, err := domain.ParseExecutionSurface(string(request.Surface))
	if err != nil || surface != request.Surface {
		return ImportSkillPackageResult{}, apperror.New(apperror.CodeInvalidArgument,
			"Skill package installation surface is invalid")
	}
	operationKey := request.OperationKey
	if request.Snapshot != nil {
		// Existing native-directory operations accepted 1..512 bytes. Keep the
		// exact key/digest stable when their writer moves into this service.
		if operationKey == "" || len(operationKey) > 512 || strings.TrimSpace(operationKey) != operationKey || !utf8.ValidString(operationKey) {
			return ImportSkillPackageResult{}, apperror.New(apperror.CodeInvalidArgument, "portable import operation key is invalid")
		}
	} else if operationKey, err = normalizeSkillPackageOperationKey(operationKey); err != nil {
		return ImportSkillPackageResult{}, err
	}
	installedBy, err := normalizeSkillPackageActor(request.InstalledBy, "cli_operator")
	if err != nil {
		return ImportSkillPackageResult{}, err
	}
	keyDigest := runmutation.Fingerprint("skill_package_install_operation.v1", operationKey)
	existing, found, err := s.store.GetPackageInstallOperation(ctx, keyDigest)
	if err != nil {
		return ImportSkillPackageResult{}, apperror.Normalize(err)
	}
	if !found {
		return s.importPlugin(ctx, request, operationKey, installedBy)
	}
	if request.Snapshot != nil {
		return ImportSkillPackageResult{}, apperror.New(apperror.CodeConflict, "operation key belongs to a legacy installation")
	}
	// Only an already durable legacy intent can reach the old object/result
	// recovery path. New imports never create a legacy installation or intent.
	unsigned, err := skills.UnsignedForm(request.Raw)
	if err != nil {
		return ImportSkillPackageResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "legacy recovery archive is invalid", err)
	}
	parsed, err := skills.ParsePackageAny(unsigned)
	if err != nil {
		return ImportSkillPackageResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "legacy recovery archive is invalid", err)
	}
	prepared, err := s.store.GetPackageInstallation(ctx, existing.InstallationID)
	if err != nil {
		return ImportSkillPackageResult{}, apperror.Normalize(err)
	}
	candidate, err := skills.NewPackageInstallation(prepared.ID, parsed.Package(), surface, keyDigest, installedBy, prepared.CreatedAt)
	// Historical installation rows require exactly two entries. Catalog
	// signatures were verified before storing this unsigned object.
	request.Raw = unsigned
	if err != nil || candidate.RequestFingerprint != existing.RequestFingerprint ||
		candidate.Name != existing.Name || candidate.Version != existing.Version ||
		candidate.Surface != existing.Surface || candidate.InstalledBy != existing.InstalledBy {
		return ImportSkillPackageResult{}, apperror.New(apperror.CodeConflict, "legacy recovery operation does not match its durable intent")
	}
	descriptor := skills.DescriptorForInstallation(prepared)
	stored, completed, err := s.store.GetInstalledPackageByRef(ctx, prepared.Name, prepared.Version)
	if err != nil {
		return ImportSkillPackageResult{}, apperror.Normalize(err)
	}
	if completed {
		if stored.Installation.ID != prepared.ID {
			return ImportSkillPackageResult{}, apperror.New(apperror.CodeInternal, "stored Skill package installation result binding is invalid")
		}
		if stored.Removal != nil {
			return ImportSkillPackageResult{}, apperror.New(apperror.CodeConflict, "removed Skill package cannot be reinstalled without an explicit restore protocol")
		}
		receipt, err := s.objects.Verify(ctx, descriptor)
		if err != nil || skills.ValidatePackageObjectReceipt(descriptor, receipt) != nil || receipt.ObjectKey != stored.Result.ObjectKey {
			return ImportSkillPackageResult{}, apperror.New(apperror.CodeFailedPrecondition, "legacy installation object failed readback verification")
		}
		stored.Replayed = true
		if err := stored.Validate(); err != nil {
			return ImportSkillPackageResult{}, apperror.Normalize(err)
		}
		return ImportSkillPackageResult{Package: stored, Replayed: true}, nil
	}
	receipt, err := s.objects.Put(ctx, request.Raw, descriptor)
	if err != nil {
		return ImportSkillPackageResult{}, apperror.Wrap(apperror.CodeFailedPrecondition, "legacy installation object could not be recovered", err)
	}
	result, err := skills.NewPackageInstallResult(prepared, receipt, time.Now().UTC())
	if err != nil {
		return ImportSkillPackageResult{}, apperror.Normalize(err)
	}
	installed, completedReplay, err := s.store.CompletePackageInstallation(ctx, result)
	if err != nil {
		return ImportSkillPackageResult{}, apperror.Normalize(err)
	}
	installed.Replayed = true
	return ImportSkillPackageResult{Package: installed, Replayed: true, RecoveredPending: !completedReplay}, nil
}

func (s *SkillPackageRegistryService) importPlugin(ctx context.Context, request ImportSkillPackageRequest,
	operationKey, actor string,
) (ImportSkillPackageResult, error) {
	pluginStore, ok := s.store.(plugins.Store)
	if !ok {
		return ImportSkillPackageResult{}, apperror.New(apperror.CodeFailedPrecondition, "Plugin installation store is unavailable")
	}
	service, err := plugins.NewService(pluginStore)
	if err != nil {
		return ImportSkillPackageResult{}, err
	}
	source := request.Source
	if source.Kind == "" {
		source = plugins.InstallSource{Kind: "upload", URI: "sha256:" + portableIdentity(string(request.Raw))}
	}
	source.Surface = string(request.Surface)
	source.OperationKeyDigest = portableIdentity("skill-directory-import\x00" + actor + "\x00" + operationKey)
	var snapshot plugins.PortableSnapshot
	if request.Snapshot != nil {
		snapshot = *request.Snapshot
		if source.Kind == "upload" {
			snapshot.Source = toolcontract.SourceRef{URI: source.URI, SHA256: portableIdentity(string(request.Raw))}
		}
	} else {
		parsed, err := skills.ParsePackageAny(request.Raw)
		if err != nil {
			return ImportSkillPackageResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "Skill package failed strict validation", err)
		}
		manifest := parsed.Preview().Manifest
		if _, reserved := s.builtins.Get(manifest.Name); reserved {
			return ImportSkillPackageResult{}, apperror.New(apperror.CodeConflict, "external Skills cannot use a built-in Skill name")
		}
		pkg, err := plugins.CaptureLegacySkill(ctx, request.Raw, "legacy-skill-"+portableIdentity(manifest.Name+"@"+manifest.Version),
			toolcontract.SourceRef{URI: source.URI, Revision: source.Commit, SHA256: portableIdentity(string(request.Raw))})
		if err != nil {
			return ImportSkillPackageResult{}, apperror.Wrap(apperror.CodeInvalidArgument, "Skill package acquisition failed", err)
		}
		snapshot = pkg.Snapshot
	}
	installed, replayed, err := service.StageSnapshot(ctx, snapshot, request.Raw, source, "", actor, "")
	if err != nil {
		return ImportSkillPackageResult{}, apperror.Normalize(err)
	}
	// Only the explicit native-instruction import path requests activation.
	// A legacy archive or approved candidate is staged for Plugin review.
	if request.EnableSkills && snapshot.Legacy == nil {
		for _, action := range []plugins.ReviewAction{plugins.ReviewApprove, plugins.ReviewEnable} {
			if (action == plugins.ReviewApprove && installed.State != plugins.StateStaged) ||
				(action == plugins.ReviewEnable && installed.State != plugins.StateApproved) ||
				!slices.Contains(installed.Capabilities(), plugins.CapabilitySkills) {
				continue
			}
			installed, err = service.Review(ctx, installed.ID, plugins.ReviewRequest{Action: action,
				ExpectedPackageFingerprint: installed.PackageFingerprint, ExpectedGeneration: installed.Generation,
				Capabilities: []plugins.Capability{plugins.CapabilitySkills}, ConfirmUntrusted: true, ReviewedBy: actor})
			if err != nil {
				return ImportSkillPackageResult{}, err
			}
		}
	}
	return ImportSkillPackageResult{Installation: &installed, Replayed: replayed}, nil
}

// SkillPackageInventory is an operator-facing projection. Unlike List, one
// unreadable retained object is reported per installation instead of hiding the
// whole Registry, so the operator can still inspect and remove that package.
// Unverified entries carry the installation identity, which the table does not
// guarantee to be unique per name@version, so the projection cannot key by ref.
type SkillPackageInventory struct {
	Items      []skills.InstalledPackage
	Unverified []string
}

func (s *SkillPackageRegistryService) Inventory(ctx context.Context,
	request ListInstalledSkillPackagesRequest,
) (SkillPackageInventory, error) {
	result := SkillPackageInventory{Items: []skills.InstalledPackage{}, Unverified: []string{}}
	if s == nil || s.store == nil || s.objects == nil {
		return result, apperror.New(apperror.CodeFailedPrecondition,
			"Skill package Registry store and object store are required")
	}
	values, err := s.store.ListInstalledPackages(ctx, request.Surface, request.Profile,
		request.IncludeRemoved)
	if err != nil {
		return result, apperror.Normalize(err)
	}
	for index := range values {
		value := skills.CloneInstalledPackage(values[index])
		descriptor := skills.DescriptorForInstallation(value.Installation)
		receipt, verifyErr := s.objects.Verify(ctx, descriptor)
		if verifyErr != nil ||
			skills.ValidatePackageObjectReceipt(descriptor, receipt) != nil ||
			receipt.ObjectKey != value.Result.ObjectKey {
			result.Unverified = append(result.Unverified, value.Installation.ID)
		}
		result.Items = append(result.Items, value)
	}
	return result, nil
}

func (s *SkillPackageRegistryService) List(ctx context.Context,
	request ListInstalledSkillPackagesRequest,
) ([]skills.InstalledPackage, error) {
	if s == nil || s.store == nil || s.objects == nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition,
			"Skill package Registry store and object store are required")
	}
	values, err := s.store.ListInstalledPackages(ctx, request.Surface, request.Profile,
		request.IncludeRemoved)
	if err != nil {
		return nil, apperror.Normalize(err)
	}
	for index := range values {
		descriptor := skills.DescriptorForInstallation(values[index].Installation)
		receipt, verifyErr := s.objects.Verify(ctx, descriptor)
		if verifyErr != nil {
			return nil, apperror.Wrap(apperror.CodeFailedPrecondition,
				"installed Skill package object failed readback verification", verifyErr)
		}
		if receiptErr := skills.ValidatePackageObjectReceipt(descriptor, receipt); receiptErr != nil ||
			receipt.ObjectKey != values[index].Result.ObjectKey {
			return nil, apperror.Wrap(apperror.CodeFailedPrecondition,
				"installed Skill package object receipt binding is invalid", receiptErr)
		}
		values[index] = skills.CloneInstalledPackage(values[index])
	}
	return values, nil
}

func (s *SkillPackageRegistryService) Get(ctx context.Context, name, version string,
) (skills.InstalledPackage, error) {
	if s == nil || s.store == nil || s.objects == nil {
		return skills.InstalledPackage{}, apperror.New(apperror.CodeFailedPrecondition,
			"Skill package Registry store and object store are required")
	}
	name, version = strings.TrimSpace(name), strings.TrimSpace(version)
	value, found, err := s.store.GetInstalledPackageByRef(ctx, name, version)
	if err != nil {
		return skills.InstalledPackage{}, apperror.Normalize(err)
	}
	if !found {
		return skills.InstalledPackage{}, apperror.New(
			apperror.CodeNotFound, "installed Skill package was not found")
	}
	descriptor := skills.DescriptorForInstallation(value.Installation)
	receipt, err := s.objects.Verify(ctx, descriptor)
	if err != nil {
		return skills.InstalledPackage{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"installed Skill package object failed readback verification", err)
	}
	if receiptErr := skills.ValidatePackageObjectReceipt(descriptor, receipt); receiptErr != nil ||
		receipt.ObjectKey != value.Result.ObjectKey {
		return skills.InstalledPackage{}, apperror.Wrap(apperror.CodeFailedPrecondition,
			"installed Skill package object receipt binding is invalid", receiptErr)
	}
	return skills.CloneInstalledPackage(value), nil
}

func (s *SkillPackageRegistryService) Remove(ctx context.Context,
	request RemoveSkillPackageRequest,
) (RemoveSkillPackageResult, error) {
	if s == nil || s.store == nil {
		return RemoveSkillPackageResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Skill package Registry store is required")
	}
	if !request.ConfirmRemove {
		return RemoveSkillPackageResult{}, apperror.New(apperror.CodeFailedPrecondition,
			"Skill package removal requires explicit operator confirmation")
	}
	request.Name, request.Version = strings.TrimSpace(request.Name), strings.TrimSpace(request.Version)
	operationKey, err := normalizeSkillPackageOperationKey(request.OperationKey)
	if err != nil {
		return RemoveSkillPackageResult{}, err
	}
	removedBy, err := normalizeSkillPackageActor(request.RemovedBy, "cli_operator")
	if err != nil {
		return RemoveSkillPackageResult{}, err
	}
	installed, found, err := s.store.GetInstalledPackageByRef(ctx, request.Name, request.Version)
	if err != nil {
		return RemoveSkillPackageResult{}, apperror.Normalize(err)
	}
	if !found {
		return RemoveSkillPackageResult{}, apperror.New(
			apperror.CodeNotFound, "installed Skill package was not found")
	}
	keyDigest := runmutation.Fingerprint("skill_package_remove_operation.v1", operationKey)
	candidate, err := skills.NewPackageRemoval(idgen.New("skill-remove"),
		installed.Installation, keyDigest, removedBy, time.Now().UTC())
	if err != nil {
		return RemoveSkillPackageResult{}, apperror.Wrap(
			apperror.CodeInvalidArgument, "Skill package removal intent is invalid", err)
	}
	if existing, operationFound, lookupErr := s.store.GetPackageRemoveOperation(ctx,
		keyDigest); lookupErr != nil {
		return RemoveSkillPackageResult{}, apperror.Normalize(lookupErr)
	} else if operationFound {
		if existing.RequestFingerprint != candidate.RequestFingerprint ||
			existing.InstallationID != installed.Installation.ID ||
			existing.Name != candidate.Name || existing.Version != candidate.Version ||
			existing.Surface != candidate.Surface || existing.RemovedBy != candidate.RemovedBy {
			return RemoveSkillPackageResult{}, apperror.New(apperror.CodeConflict,
				"Skill package removal operation key was already used for different intent")
		}
		// The installation read above can precede a concurrent transaction that
		// atomically commits this operation and its tombstone. Refresh after the
		// operation becomes visible instead of rejecting the stale snapshot.
		installed, found, lookupErr = s.store.GetInstalledPackageByRef(ctx,
			request.Name, request.Version)
		if lookupErr != nil {
			return RemoveSkillPackageResult{}, apperror.Normalize(lookupErr)
		}
		if !found || installed.Removal == nil || installed.Removal.ID != existing.RemovalID {
			return RemoveSkillPackageResult{}, apperror.New(apperror.CodeInternal,
				"stored Skill package removal operation binding is invalid")
		}
		return RemoveSkillPackageResult{Removal: *installed.Removal, Replayed: true}, nil
	}
	if installed.Removal != nil {
		return RemoveSkillPackageResult{}, apperror.New(
			apperror.CodeConflict, "Skill package already has an immutable removal tombstone")
	}
	operation := skills.PackageRemoveOperation{
		KeyDigest: keyDigest, RequestFingerprint: candidate.RequestFingerprint,
		RemovalID: candidate.ID, InstallationID: candidate.InstallationID,
		Name: candidate.Name, Version: candidate.Version, Surface: candidate.Surface,
		RemovedBy: candidate.RemovedBy, CreatedAt: candidate.CreatedAt,
	}
	removal, replayed, err := s.store.CreatePackageRemoval(ctx, candidate, operation)
	return RemoveSkillPackageResult{Removal: removal, Replayed: replayed},
		apperror.Normalize(err)
}

func normalizeSkillPackageOperationKey(value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) ||
		len(value) < 16 || len(value) > 256 {
		return "", apperror.New(apperror.CodeInvalidArgument,
			"Skill package operation key must contain between 16 and 256 normalized UTF-8 bytes")
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return "", apperror.New(apperror.CodeInvalidArgument,
				"Skill package operation key contains a forbidden control character")
		}
	}
	return value, nil
}

func normalizeSkillPackageActor(value, fallback string) (string, error) {
	value = strings.TrimSpace(redact.String(value))
	if value == "" {
		value = fallback
	}
	if !utf8.ValidString(value) || len([]rune(value)) > 256 || strings.ContainsRune(value, 0) {
		return "", apperror.New(apperror.CodeInvalidArgument,
			"Skill package operator identity is invalid")
	}
	for _, current := range value {
		if unicode.IsControl(current) {
			return "", apperror.New(apperror.CodeInvalidArgument,
				"Skill package operator identity contains a forbidden control character")
		}
	}
	return value, nil
}
