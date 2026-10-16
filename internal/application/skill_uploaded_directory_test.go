package application

import (
	"bytes"
	"encoding/base64"
	"path/filepath"
	"slices"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/store"
)

func TestSkillPackageUploadedDirectoryStagesStableIdentityAndReplaysRevocation(t *testing.T) {
	database := filepath.Join(t.TempDir(), "directory.db")
	st, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	objects, err := skills.NewLocalPackageObjectStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	builtins, err := skills.BuiltinRegistry()
	if err != nil {
		t.Fatal(err)
	}
	service := NewSkillPackageRegistryService(st, objects, builtins)
	upload := plugins.UploadedDirectory{RootName: "web-skill", Files: []plugins.UploadedDirectoryFile{
		{Path: "SKILL.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte("---\nname: web-skill\ndescription: Native web upload.\n---\nRead assets.\n"))},
		{Path: "assets/data.bin", ContentBase64: base64.StdEncoding.EncodeToString([]byte{0, 255, 128})},
	}}
	request := ImportSkillPackageRequest{Directory: &upload, Surface: domain.ExecutionSurfaceCode,
		OperationKey: "web-directory-install-0001", InstalledBy: "operator", ConfirmUntrusted: true}
	result, err := service.Import(t.Context(), request)
	if err != nil || result.Installation == nil {
		t.Fatalf("directory import: %+v %v", result, err)
	}
	installed := *result.Installation
	if installed.State != plugins.StateStaged || len(installed.EnabledCapabilities) != 0 || installed.Source.Kind != "upload" ||
		installed.Source.URI != "sha256:"+installed.ArchiveSHA256 || installed.PackageID() != "portable-"+portableIdentity("upload-directory\x00web-skill") {
		t.Fatalf("directory upload did not retain stable inert identity: %+v", installed)
	}
	retained, err := st.LoadPluginObject(t.Context(), installed.ID)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(upload.Files)
	replayed, err := service.Import(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Installation.ID != installed.ID {
		t.Fatalf("reordered replay: %+v %v", replayed, err)
	}
	review, _ := plugins.NewService(st)
	for _, action := range []plugins.ReviewAction{plugins.ReviewApprove, plugins.ReviewEnable, plugins.ReviewRevoke} {
		installed, err = review.Review(t.Context(), installed.ID, plugins.ReviewRequest{Action: action,
			ExpectedPackageFingerprint: installed.PackageFingerprint, ExpectedGeneration: installed.Generation,
			Capabilities: []plugins.Capability{plugins.CapabilitySkills}, ConfirmUntrusted: true, ReviewedBy: "operator"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	service = NewSkillPackageRegistryService(st, objects, builtins)
	replayed, err = service.Import(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Installation.State != plugins.StateRevoked || replayed.Installation.Generation != installed.Generation || len(replayed.Installation.EnabledCapabilities) != 0 {
		t.Fatalf("reopened replay revived revoked instructions: %+v %v", replayed, err)
	}
	upload.Files[0].ContentBase64 = base64.StdEncoding.EncodeToString([]byte("new resource bytes"))
	if _, err := service.Import(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed source replay: %v", err)
	}
	request.OperationKey = "web-directory-install-0002"
	changed, err := service.Import(t.Context(), request)
	if err != nil || changed.Installation == nil || changed.Installation.PackageID() != installed.PackageID() ||
		changed.Installation.Revision() == installed.Revision() || changed.Installation.State != plugins.StateStaged || len(changed.Installation.EnabledCapabilities) != 0 {
		t.Fatalf("content change lost package identity or auto-enabled: %+v %v", changed, err)
	}
	after, err := st.LoadPluginObject(t.Context(), installed.ID)
	if err != nil || !bytes.Equal(after, retained) {
		t.Fatal("new upload changed previously retained bytes", err)
	}
	request.Raw = []byte("mixed source")
	if _, err := service.Import(t.Context(), request); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("mixed directory and archive: %v", err)
	}
	request.Raw, request.EnableSkills = nil, true
	if _, err := service.Import(t.Context(), request); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("directory bypassed explicit review: %v", err)
	}
}
