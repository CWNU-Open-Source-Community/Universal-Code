package httpapi

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/runmutation"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/testfixtures/legacyskill"
	"cyberagent-workbench/internal/toolgateway"
)

const (
	inventorySkillName    = "http-inventory-review"
	inventorySkillVersion = "1.0.0"
)

type inventorySkillFixture struct {
	installation skills.PackageInstallation
	archive      []byte
	ref          string
}

func newInventorySkillFixture(t *testing.T) inventorySkillFixture {
	t.Helper()
	content := []byte("# HTTP inventory review\n")
	contentDigest := sha256.Sum256(content)
	manifest := skills.Manifest{
		Protocol: skills.ProtocolVersion, Name: inventorySkillName,
		Version: inventorySkillVersion, Description: "HTTP inventory fixture.",
		Profiles: []domain.Profile{domain.ProfileReview},
		ToolDependencies: []toolgateway.ToolName{
			toolgateway.ListWorkspaceTool, toolgateway.ReadFileTool,
		},
		ContentPath:   skills.PackageContentPath,
		ContentSHA256: hex.EncodeToString(contentDigest[:]),
		ContentBytes:  len(content), ContentTokenUpperBound: skills.ContentTokenUpperBound(content),
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range []struct {
		name string
		data []byte
	}{{skills.PackageManifestPath, manifestRaw}, {skills.PackageContentPath, content}} {
		file, createErr := writer.CreateHeader(&zip.FileHeader{Name: entry.name, Method: zip.Deflate})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := file.Write(entry.data); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive := buffer.Bytes()
	parsed, err := skills.ParsePackage(archive)
	if err != nil {
		t.Fatal(err)
	}
	preview := parsed.Preview()
	createdAt := time.Now().UTC().Add(-time.Minute)
	installation := skills.PackageInstallation{
		ID: idgen.New("skill-install"), ProtocolVersion: skills.PackageInstallationProtocolVersion,
		Name: inventorySkillName, Version: inventorySkillVersion,
		Surface:            domain.ExecutionSurfaceCode,
		Manifest:           preview.Manifest,
		ArchiveSHA256:      preview.ArchiveSHA256,
		PackageFingerprint: preview.PackageFingerprint,
		ArchiveBytes:       preview.ArchiveBytes,
		UncompressedBytes:  preview.UncompressedBytes,
		EntryCount:         preview.EntryCount,
		TrustClass:         preview.TrustClass,
		RiskCodes:          preview.RiskCodes,
		OperatorConfirmed:  true,
		OperationKeyDigest: runmutation.Fingerprint("skill_package_install_operation.v1",
			"http-inventory-operation"),
		InstalledBy: "http_fixture_operator", CreatedAt: createdAt,
	}
	installation.RequestFingerprint = skills.PackageInstallationIntentFingerprint(installation)
	installation.InstallationFingerprint = skills.PackageInstallationFingerprint(installation)
	return inventorySkillFixture{installation: installation, archive: archive,
		ref: inventorySkillName + "@" + inventorySkillVersion}
}

// seedInventorySkill writes one historical installation, optionally with its
// retained object. Production has no legacy first-install writer, so historical
// rows only ever arrive through this compatibility path.
func seedInventorySkill(t *testing.T, st *store.SQLiteStore, databasePath string,
	value inventorySkillFixture, withObject bool, objects skills.PackageObjectStore,
) {
	t.Helper()
	if withObject {
		descriptor := skills.DescriptorForInstallation(value.installation)
		if _, err := objects.Put(t.Context(), value.archive, descriptor); err != nil {
			t.Fatal(err)
		}
	}
	objectKey, err := skills.PackageObjectKey(value.installation.ArchiveSHA256)
	if err != nil {
		t.Fatal(err)
	}
	result, err := skills.NewPackageInstallResult(value.installation, skills.PackageObjectReceipt{
		Descriptor: skills.DescriptorForInstallation(value.installation), ObjectKey: objectKey,
	}, value.installation.CreatedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	legacyskill.Seed(t, databasePath, value.installation)
	if _, _, err := st.CompletePackageInstallation(t.Context(), result); err != nil {
		t.Fatal(err)
	}
}

func newInventorySkillAPI(t *testing.T) (*API, *store.SQLiteStore, string,
	skills.PackageObjectStore, inventorySkillFixture,
) {
	t.Helper()
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "skill-inventory.db")
	st, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	objects, err := skills.NewLocalPackageObjectStore(filepath.Join(directory, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	builtins, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	value := newInventorySkillFixture(t)
	api, err := New(st, Config{AccessToken: testAccessToken, ControlToken: testControlToken,
		SkillInstallationEnabled: true,
		SkillInstallationController: application.NewSkillPackageRegistryService(
			st, objects, builtins)})
	if err != nil {
		t.Fatal(err)
	}
	return api, st, databasePath, objects, value
}

func TestSkillPackageInventoryHTTPReadsVerifiedAndUnverifiedRegistry(t *testing.T) {
	api, st, databasePath, objects, value := newInventorySkillAPI(t)
	seedInventorySkill(t, st, databasePath, value, false, objects)

	first := performRequest(t, api, http.MethodGet, SkillPackageInventoryPath,
		testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	var envelope struct {
		Data SkillPackageInventoryView `json:"data"`
	}
	if first.Code != http.StatusOK ||
		json.Unmarshal(first.Body.Bytes(), &envelope) != nil {
		t.Fatalf("unverified inventory status=%d body=%s", first.Code, first.Body.String())
	}
	view := envelope.Data
	if view.ProtocolVersion != skills.PackageInventoryProtocolVersion || view.CapabilityGrant ||
		view.ReturnedCount != 1 || view.UnverifiedCount != 1 || len(view.Items) != 1 ||
		view.Items[0].Skill != value.ref || view.Items[0].ObjectVerified ||
		view.Items[0].Status != "installed" || view.Items[0].ContentBodyExposed {
		t.Fatalf("unverified inventory projection = %+v", view)
	}
	for _, forbidden := range []string{`"installation_id"`, `"removal_id"`, `"object_key"`,
		`"operation_key"`, `"request_fingerprint"`, `"lease"`, `"fencing"`,
		value.installation.OperationKeyDigest, value.installation.RequestFingerprint,
		value.installation.InstallationFingerprint} {
		if strings.Contains(first.Body.String(), forbidden) {
			t.Fatalf("inventory exposed private identity %q: %s", forbidden, first.Body.String())
		}
	}

	seedInventorySkillObject(t, databasePath, value, objects)
	second := performRequest(t, api, http.MethodGet, SkillPackageInventoryPath,
		testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	if second.Code != http.StatusOK ||
		!strings.Contains(second.Body.String(), `"object_verified":true`) ||
		strings.Contains(second.Body.String(), `"unverified_count":1`) {
		t.Fatalf("verified inventory status=%d body=%s", second.Code, second.Body.String())
	}
	filtered := performRequest(t, api, http.MethodGet,
		SkillPackageInventoryPath+"?surface=cyber", testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	if filtered.Code != http.StatusOK ||
		!strings.Contains(filtered.Body.String(), `"returned_count":0`) {
		t.Fatalf("surface filter status=%d body=%s", filtered.Code, filtered.Body.String())
	}
	unknown := performRequest(t, api, http.MethodGet,
		SkillPackageInventoryPath+"?surface=bogus", testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	assertAPIError(t, unknown, http.StatusBadRequest, "INVALID_ARGUMENT")
	duplicate := performRequest(t, api, http.MethodGet,
		SkillPackageInventoryPath+"?surface=code&surface=code", testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	assertAPIError(t, duplicate, http.StatusBadRequest, "INVALID_ARGUMENT")
}

// seedInventorySkillObject adds the retained package object only, so the same
// installation flips from unverified to verified without rewriting history.
func seedInventorySkillObject(t *testing.T, databasePath string,
	value inventorySkillFixture, objects skills.PackageObjectStore,
) {
	t.Helper()
	if _, err := objects.Put(t.Context(), value.archive,
		skills.DescriptorForInstallation(value.installation)); err != nil {
		t.Fatal(err)
	}
}

func TestSkillPackageRemoveControlWritesImmutableTombstone(t *testing.T) {
	api, st, databasePath, objects, value := newInventorySkillAPI(t)
	seedInventorySkill(t, st, databasePath, value, true, objects)
	body := `{"protocol_version":"` + skills.PackageRemovalRequestProtocolVersion +
		`","skill":"` + value.ref + `","confirm_remove":true}`
	operationKey := "http-skill-remove-0001-aaaaaaaa"

	first := performSessionMessageRequest(t, api, http.MethodPost, SkillPackageRemovePath,
		testControlToken, operationKey, "application/json", strings.NewReader(body))
	var envelope struct {
		Data SkillPackageRemovalView `json:"data"`
	}
	if first.Code != http.StatusAccepted ||
		json.Unmarshal(first.Body.Bytes(), &envelope) != nil {
		t.Fatalf("remove status=%d body=%s", first.Code, first.Body.String())
	}
	removal := envelope.Data
	if removal.ProtocolVersion != skills.PackageRemovalControlProtocolVersion ||
		removal.Skill != value.ref || removal.Status != "removed" || removal.CapabilityGrant ||
		removal.Replayed || !removal.PackageObjectRetained ||
		!removal.HistoricalRecoveryPreserved || removal.FutureSelectionEnabled ||
		removal.RunSelectionAuthorized || removal.ContextInjectionAuthorized ||
		removal.ToolCapabilityGrant || removal.RemovedBy != "http_operator" ||
		removal.Receipt.Kind != "skill_package_remove" ||
		removal.Receipt.Outcome != "removed" {
		t.Fatalf("removal projection = %+v", removal)
	}

	replay := performSessionMessageRequest(t, api, http.MethodPost, SkillPackageRemovePath,
		testControlToken, operationKey, "application/json", strings.NewReader(body))
	if replay.Code != http.StatusAccepted ||
		!strings.Contains(replay.Body.String(), `"replayed":true`) {
		t.Fatalf("removal replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	conflict := performSessionMessageRequest(t, api, http.MethodPost, SkillPackageRemovePath,
		testControlToken, "http-skill-remove-0002-bbbbbbbb", "application/json",
		strings.NewReader(body))
	assertAPIError(t, conflict, http.StatusConflict, "CONFLICT")

	listed := performRequest(t, api, http.MethodGet, SkillPackageInventoryPath,
		testAccessToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	if listed.Code != http.StatusOK ||
		!strings.Contains(listed.Body.String(), `"returned_count":0`) {
		t.Fatalf("default inventory must hide removals: %d %s", listed.Code, listed.Body.String())
	}
	withRemovals := performRequest(t, api, http.MethodGet,
		SkillPackageInventoryPath+"?include_removed=true", testAccessToken,
		"127.0.0.1:8765", "127.0.0.1:45000", nil)
	if withRemovals.Code != http.StatusOK ||
		!strings.Contains(withRemovals.Body.String(), `"status":"removed"`) ||
		!strings.Contains(withRemovals.Body.String(), `"future_selection_enabled":false`) {
		t.Fatalf("tombstone inventory status=%d body=%s",
			withRemovals.Code, withRemovals.Body.String())
	}
	for _, forbidden := range []string{`"removal_id"`, `"operation_key"`, `"object_key"`} {
		if strings.Contains(withRemovals.Body.String(), forbidden) {
			t.Fatalf("tombstone inventory exposed %q", forbidden)
		}
	}

	unconfirmed := performSessionMessageRequest(t, api, http.MethodPost, SkillPackageRemovePath,
		testControlToken, "http-skill-remove-0003-cccccccc", "application/json",
		strings.NewReader(strings.Replace(body, `"confirm_remove":true`,
			`"confirm_remove":false`, 1)))
	assertAPIError(t, unconfirmed, http.StatusBadRequest, "INVALID_ARGUMENT")
	malformed := performSessionMessageRequest(t, api, http.MethodPost, SkillPackageRemovePath,
		testControlToken, "http-skill-remove-0006-ffffffff", "application/json",
		strings.NewReader(`{"protocol_version":"`+skills.PackageRemovalRequestProtocolVersion+
			`","skill":"not-a-ref","confirm_remove":true}`))
	assertAPIError(t, malformed, http.StatusBadRequest, "INVALID_ARGUMENT")
	missing := performSessionMessageRequest(t, api, http.MethodPost, SkillPackageRemovePath,
		testControlToken, "http-skill-remove-0004-dddddddd", "application/json",
		strings.NewReader(`{"protocol_version":"`+skills.PackageRemovalRequestProtocolVersion+
			`","skill":"absent-review@1.0.0","confirm_remove":true}`))
	assertAPIError(t, missing, http.StatusNotFound, "NOT_FOUND")
	readToken := performSessionMessageRequest(t, api, http.MethodPost, SkillPackageRemovePath,
		testAccessToken, "http-skill-remove-0005-eeeeeeee", "application/json",
		strings.NewReader(body))
	assertAPIError(t, readToken, http.StatusUnauthorized, "POLICY_DENIED")
	getMethod := performRequest(t, api, http.MethodGet, SkillPackageRemovePath,
		testControlToken, "127.0.0.1:8765", "127.0.0.1:45000", nil)
	assertAPIError(t, getMethod, http.StatusMethodNotAllowed, "INVALID_ARGUMENT")

	archive := base64.StdEncoding.EncodeToString(value.archive)
	install := performSessionMessageRequest(t, api, http.MethodPost, SkillPackageInstallPath,
		testControlToken, "http-skill-install-after-0001", "application/json",
		strings.NewReader(`{"version":"`+skills.PackageInstallationProtocolVersion+
			`","archive_base64":"`+archive+`","surface":"code","confirm_untrusted":true}`))
	if install.Code != http.StatusAccepted {
		t.Fatalf("reinstall status=%d body=%s", install.Code, install.Body.String())
	}
}

func TestSkillPackageRemoveControlRefusesRunPinnedVersion(t *testing.T) {
	fixture := newAPIFixture(t)
	directory := t.TempDir()
	objects, err := skills.NewLocalPackageObjectStore(filepath.Join(directory, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	builtins, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	fixture.api.skillInstallationEnabled = true
	fixture.api.skillInstallationController = application.NewSkillPackageRegistryService(
		fixture.store, objects, builtins)
	service := application.NewSkillPackageRegistryService(fixture.store, objects, builtins)
	installed, err := service.Inventory(t.Context(),
		application.ListInstalledSkillPackagesRequest{IncludeRemoved: true})
	if err != nil || len(installed.Items) == 0 {
		t.Fatalf("fixture inventory = %+v err = %v", installed, err)
	}
	pinned := installed.Items[0]
	if pinned.Removal != nil {
		t.Fatal("fixture package is already removed")
	}
	body := `{"protocol_version":"` + skills.PackageRemovalRequestProtocolVersion + `","skill":"` +
		skills.FormatInstalledPackageRef(pinned.Installation.Name,
			pinned.Installation.Version) + `","confirm_remove":true}`
	response := performSessionMessageRequest(t, fixture.api, http.MethodPost, SkillPackageRemovePath,
		testControlToken, "http-skill-remove-pinned-0001", "application/json",
		strings.NewReader(body))
	assertAPIError(t, response, http.StatusPreconditionFailed, "FAILED_PRECONDITION")
	after, err := service.Inventory(t.Context(),
		application.ListInstalledSkillPackagesRequest{IncludeRemoved: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range after.Items {
		if item.Installation.ID == pinned.Installation.ID && item.Removal != nil {
			t.Fatal("refused removal still wrote a tombstone")
		}
	}
}
