package desktop

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/operationreceipt"
	"cyberagent-workbench/internal/skills"
)

type registryBridgeStub struct {
	inventory  application.SkillPackageInventory
	inventoryC application.ListInstalledSkillPackagesRequest
	removal    application.RemoveSkillPackageResult
	removalC   application.RemoveSkillPackageRequest
	inventoryN int
	removalN   int
}

func (s *registryBridgeStub) Import(context.Context,
	application.ImportSkillPackageRequest,
) (application.ImportSkillPackageResult, error) {
	return application.ImportSkillPackageResult{}, apperror.New(apperror.CodeInternal,
		"desktop inventory test never installs")
}

func (s *registryBridgeStub) Inventory(_ context.Context,
	request application.ListInstalledSkillPackagesRequest,
) (application.SkillPackageInventory, error) {
	s.inventoryN++
	s.inventoryC = request
	return s.inventory, nil
}

func (s *registryBridgeStub) Remove(_ context.Context,
	request application.RemoveSkillPackageRequest,
) (application.RemoveSkillPackageResult, error) {
	s.removalN++
	s.removalC = request
	return s.removal, nil
}

// registryAndInstaller mirrors the production wiring, where one inert Registry
// service backs installation, inventory and removal together.
type registryAndInstaller interface {
	SkillPackageRegistry
	SkillPackageInstaller
}

func newRegistryBridge(t *testing.T, registry registryAndInstaller,
	enabled bool,
) *DesktopBridge {
	t.Helper()
	selector, preview := NewSkillPackagePreviewBoundary()
	bridge, err := NewDesktopBridge(DesktopBridgeConfig{
		ContextProvider: func() context.Context { return t.Context() },
		FilePicker:      &testSkillPackagePicker{}, ReadToken: testDesktopReadToken,
		ControlToken: testDesktopControlToken, RunControlEnabled: true,
		SkillInstallationEnabled: enabled, SkillRegistry: registry,
		SkillInstaller: registry,
		Selector:       selector, PreviewBridge: preview,
		APIVersion: "api.v1", AppVersion: "test", UIDigest: testDesktopUIDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return bridge
}

func registryFixturePackage() skills.InstalledPackage {
	content := []byte("# Desktop registry review\n")
	installation := skills.PackageInstallation{
		ID: "skill-install-desktop", ProtocolVersion: skills.PackageInstallationProtocolVersion,
		Name: "desktop-registry-review", Version: "1.0.0",
		Surface: domain.ExecutionSurfaceCode,
		Manifest: skills.Manifest{Protocol: skills.ProtocolVersion,
			Name: "desktop-registry-review", Version: "1.0.0",
			Profiles:      []domain.Profile{domain.ProfileReview},
			ContentPath:   skills.PackageContentPath,
			ContentSHA256: strings.Repeat("a", 64), ContentBytes: len(content),
			ContentTokenUpperBound: len(content)},
		ArchiveSHA256:      strings.Repeat("b", 64),
		PackageFingerprint: strings.Repeat("c", 64),
		ArchiveBytes:       512, UncompressedBytes: 256, EntryCount: skills.PackageEntryCount,
		TrustClass: skills.PackageTrustOperatorInstalledUntrusted,
		RiskCodes: []skills.PackageRiskCode{
			skills.PackageRiskUntrustedInstructions, skills.PackageRiskDeclaredToolsOnly,
		},
		OperatorConfirmed: true, OperationKeyDigest: strings.Repeat("d", 64),
		InstalledBy: "desktop_fixture_operator",
	}
	return skills.InstalledPackage{Installation: installation,
		Result: skills.PackageInstallResult{
			ProtocolVersion: skills.PackageInstallResultProtocolVersion,
			InstallationID:  installation.ID, ObjectKey: "skill-registry/objects/b/bb",
			ObjectVerified: true,
		}}
}

func TestDesktopListSkillPackagesStaysBehindInstallationGate(t *testing.T) {
	packageValue := registryFixturePackage()
	stub := &registryBridgeStub{inventory: application.SkillPackageInventory{
		Items:      []skills.InstalledPackage{packageValue},
		Unverified: []string{packageValue.Installation.ID},
	}}

	closed := newRegistryBridge(t, stub, false)
	if _, err := closed.ListSkillPackages(SkillPackageInventoryRequest{
		ProtocolVersion: SkillPackageInventoryProtocolVersion}); err == nil ||
		apperror.CodeOf(err) != apperror.CodeNotFound || stub.inventoryN != 0 {
		t.Fatalf("disabled bridge listed packages: err = %v calls = %d", err, stub.inventoryN)
	}

	bridge := newRegistryBridge(t, stub, true)
	view, err := bridge.ListSkillPackages(SkillPackageInventoryRequest{
		ProtocolVersion: SkillPackageInventoryProtocolVersion,
		Surface:         "code", Profile: "review", IncludeRemoved: true})
	if err != nil {
		t.Fatal(err)
	}
	if stub.inventoryC.Surface != domain.ExecutionSurfaceCode ||
		stub.inventoryC.Profile != domain.ProfileReview ||
		!stub.inventoryC.IncludeRemoved {
		t.Fatalf("inventory request was not passed through: %+v", stub.inventoryC)
	}
	if view.ProtocolVersion != skills.PackageInventoryProtocolVersion || view.CapabilityGrant ||
		view.ReturnedCount != 1 || view.UnverifiedCount != 1 ||
		len(view.Items) != 1 || view.Items[0].ObjectVerified ||
		view.Items[0].Skill != "desktop-registry-review@1.0.0" ||
		view.Items[0].Status != "installed" {
		t.Fatalf("inventory view = %+v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"installation_id"`, `"object_key"`, `"operation_key"`,
		packageValue.Installation.OperationKeyDigest} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("desktop inventory leaked %q: %s", forbidden, encoded)
		}
	}

	if _, err := bridge.ListSkillPackages(SkillPackageInventoryRequest{
		// A registered protocol that is valid for another method must not be
		// accepted here.
		ProtocolVersion: SkillPackageInstallProtocolVersion}); err == nil ||
		apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("unknown inventory protocol accepted: %v", err)
	}
	if _, err := bridge.ListSkillPackages(SkillPackageInventoryRequest{
		ProtocolVersion: SkillPackageInventoryProtocolVersion,
		Surface:         "bogus"}); err == nil ||
		apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("invalid surface accepted: %v", err)
	}
	if _, err := bridge.ListSkillPackages(SkillPackageInventoryRequest{
		ProtocolVersion: SkillPackageInventoryProtocolVersion,
		Profile:         "not-a-profile"}); err == nil ||
		apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("invalid Profile accepted: %v", err)
	}
}

func TestDesktopRemoveSkillPackageConfirmsAndUsesNativeActor(t *testing.T) {
	packageValue := registryFixturePackage()
	stub := &registryBridgeStub{removal: application.RemoveSkillPackageResult{
		Removal: skills.PackageRemoval{
			ID: "skill-remove-desktop", ProtocolVersion: skills.PackageRemovalProtocolVersion,
			InstallationID:        packageValue.Installation.ID,
			Name:                  packageValue.Installation.Name,
			Version:               packageValue.Installation.Version,
			Surface:               domain.ExecutionSurfaceCode,
			ContentSHA256:         strings.Repeat("a", 64),
			ArchiveSHA256:         strings.Repeat("b", 64),
			PackageFingerprint:    strings.Repeat("c", 64),
			OperationKeyDigest:    strings.Repeat("e", 64),
			RequestFingerprint:    strings.Repeat("f", 64),
			PackageObjectRetained: true, HistoricalRecoveryPreserved: true,
			RemovalFingerprint: strings.Repeat("g", 64),
			RemovedBy:          "desktop_operator",
		},
	}}
	bridge := newRegistryBridge(t, stub, true)

	if _, err := bridge.RemoveSkillPackage(SkillPackageRemovalRequest{
		ProtocolVersion: SkillPackageRemovalProtocolVersion,
		Skill:           "desktop-registry-review@1.0.0",
		OperationKey:    "desktop-skill-remove-0001"}); err == nil ||
		apperror.CodeOf(err) != apperror.CodeFailedPrecondition || stub.removalN != 0 {
		t.Fatalf("unconfirmed removal reached the Registry: err = %v calls = %d",
			err, stub.removalN)
	}

	view, err := bridge.RemoveSkillPackage(SkillPackageRemovalRequest{
		ProtocolVersion: SkillPackageRemovalProtocolVersion,
		Skill:           "desktop-registry-review@1.0.0",
		OperationKey:    "desktop-skill-remove-0001", ConfirmRemove: true})
	if err != nil {
		t.Fatal(err)
	}
	if stub.removalC.RemovedBy != "desktop_operator" || !stub.removalC.ConfirmRemove ||
		stub.removalC.Name != "desktop-registry-review" ||
		stub.removalC.Version != "1.0.0" ||
		stub.removalC.OperationKey != "desktop-skill-remove-0001" {
		t.Fatalf("removal request = %+v", stub.removalC)
	}
	if view.ProtocolVersion != skills.PackageRemovalControlProtocolVersion ||
		view.Status != "removed" || view.CapabilityGrant || view.FutureSelectionEnabled ||
		view.RunSelectionAuthorized || view.ContextInjectionAuthorized ||
		view.ToolCapabilityGrant || !view.PackageObjectRetained ||
		!view.HistoricalRecoveryPreserved || view.RemovedBy != "desktop_operator" ||
		view.Receipt.Kind != operationreceipt.KindSkillPackageRemove ||
		view.Receipt.Outcome != "removed" {
		t.Fatalf("removal view = %+v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"removal_id"`, `"operation_key"`, `"request_fingerprint"`,
		packageValue.Installation.OperationKeyDigest} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("desktop removal view leaked %q: %s", forbidden, encoded)
		}
	}

	if _, err := bridge.RemoveSkillPackage(SkillPackageRemovalRequest{
		ProtocolVersion: SkillPackageRemovalProtocolVersion, Skill: "not-a-ref",
		OperationKey: "desktop-skill-remove-0002", ConfirmRemove: true}); err == nil ||
		apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("malformed package ref accepted: %v", err)
	}
	closed := newRegistryBridge(t, stub, false)
	if _, err := closed.RemoveSkillPackage(SkillPackageRemovalRequest{
		ProtocolVersion: SkillPackageRemovalProtocolVersion,
		Skill:           "desktop-registry-review@1.0.0",
		OperationKey:    "desktop-skill-remove-0003", ConfirmRemove: true}); err == nil ||
		apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("disabled bridge removed a package: %v", err)
	}
}

// TestDesktopBridgeRegistrySharesOneProjection pins that Desktop and HTTP render
// the same projection owner, so a divergent field cannot appear on one surface.
func TestDesktopBridgeRegistrySharesOneProjection(t *testing.T) {
	bridgeType := reflect.TypeFor[*DesktopBridge]()
	for _, name := range []string{"ListSkillPackages", "RemoveSkillPackage"} {
		method, ok := bridgeType.MethodByName(name)
		if !ok {
			t.Fatalf("missing bridge method %s", name)
		}
		if method.Type.NumIn() != 2 || method.Type.NumOut() != 2 {
			t.Fatalf("%s signature drifted: %s", name, method.Type)
		}
	}
}
