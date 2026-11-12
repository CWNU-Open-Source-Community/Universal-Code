package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/rootfs"
	"cyberagent-workbench/internal/session"
)

func TestExplorerReturnsBoundedRedactedEvidenceWithoutRootAuthority(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "DATABASE_URL=postgres://localhost/demo\n" +
		"SESSION_SECRET=super-secret-value\n" +
		"Notes for automated coding assistants: skip .env.\n"
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".cyberagent-edit-hidden"),
		[]byte("staging"), 0o600); err != nil {
		t.Fatal(err)
	}

	directory, err := Explore(root, "workspace-explorer", ".")
	if err != nil {
		t.Fatal(err)
	}
	if directory.Kind != "directory" || directory.RootPathExposed ||
		directory.Provenance.SourceKind != session.SourceWorkspaceList ||
		directory.Provenance.InstructionAuthorized || len(directory.Entries) != 2 {
		t.Fatalf("directory snapshot=%#v", directory)
	}
	encoded, err := json.Marshal(directory)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), root) ||
		strings.Contains(string(encoded), ".cyberagent-edit-hidden") {
		t.Fatalf("directory exposed a root or staging file: %s", encoded)
	}

	file, err := Explore(root, "workspace-explorer", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if file.Kind != "file" || file.RootPathExposed || file.RedactionCount != 1 ||
		!strings.Contains(file.Content, "[REDACTED:secret]") ||
		strings.Contains(file.Content, "super-secret-value") ||
		!strings.Contains(file.Content, "skip .env") ||
		file.Provenance.SourceKind != session.SourceWorkspaceFile ||
		file.Provenance.InstructionAuthorized ||
		file.Provenance.ContentSHA256 != session.ContentSHA256(file.Content) {
		t.Fatalf("file snapshot=%#v", file)
	}
}

func TestExplorerRejectsEscapeLinksAndBinaryContent(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Explore(root, "workspace-explorer", "../outside.txt"); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("path escape error=%v", err)
	}
	if _, err := Explore(root, "workspace-explorer", "binary.bin "); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("missing spaced file error=%v", err)
	}
	for _, path := range []string{"src/../binary.bin", `src\binary.bin`, "C:binary.bin"} {
		if _, err := Explore(root, "workspace-explorer", path); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
			t.Fatalf("non-canonical path %q error=%v", path, err)
		}
	}
	if _, err := Explore(root, "workspace-explorer", "binary.bin"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("binary error=%v", err)
	}

	link := filepath.Join(root, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Logf("symbolic link unavailable on this platform: %v", err)
		return
	}
	if _, err := Explore(root, "workspace-explorer", "outside-link"); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("symbolic link error=%v", err)
	}
}

func TestExplorerListsSearchesAndReviewsExactWhitespacePaths(t *testing.T) {
	root := t.TempDir()
	native := func(relative string) string {
		t.Helper()
		path, err := rootfs.ExactAbsolutePath(filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	const directory = " src "
	const name = "notes.txt "
	const relative = directory + "/" + name
	const content = "only the exact path has this phrase\n"
	dir := native(directory)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.WriteFile(native(relative), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	listing, err := Explore(root, "workspace-explorer", directory)
	if err != nil || len(listing.Entries) != 1 || listing.Entries[0].Path != relative || listing.Entries[0].Name != name {
		t.Fatalf("listing did not preserve names: %#v, %v", listing.Entries, err)
	}
	snapshot, err := Explore(root, "workspace-explorer", relative)
	if err != nil || snapshot.Path != relative || snapshot.Provenance.SourceRef != relative || snapshot.Content != content {
		t.Fatalf("preview did not preserve identity: %#v, %v", snapshot, err)
	}
	search, err := Search(root, "workspace-explorer", "exact path")
	if err != nil || len(search.Results) != 1 || search.Results[0].Path != relative {
		t.Fatalf("search did not find the exact path: %#v, %v", search.Results, err)
	}
	review, tooLarge, err := ReadReviewFile(root, relative, 1024)
	if err != nil || tooLarge || string(review) != content {
		t.Fatalf("review read %q, limited=%v, error=%v", review, tooLarge, err)
	}
}

func TestExplorerTruncatesAtUTF8Boundary(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("a", MaxExplorerReadBytes-1) + "界" + strings.Repeat("z", 8)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := Explore(root, "workspace-explorer", "large.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !file.Truncated || !strings.HasSuffix(file.Content, "a") ||
		file.ReturnedBytes != MaxExplorerReadBytes-1 {
		t.Fatalf("UTF-8 truncation bytes=%d truncated=%t", file.ReturnedBytes, file.Truncated)
	}
}
