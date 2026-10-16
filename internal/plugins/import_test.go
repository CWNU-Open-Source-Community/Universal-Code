package plugins

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchPinnedHTTPSRejectsRedirectAndDigestDrift(t *testing.T) {
	manifest, files := pluginSkillFixture(t)
	raw, err := BuildUnsignedPackage(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/archive":
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(raw)
		case "/redirect":
			http.Redirect(writer, request, "/archive", http.StatusFound)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	fetched, err := FetchPinnedHTTPS(t.Context(), server.URL+"/archive",
		packageTestDigest(raw), server.Client())
	if err != nil || !bytes.Equal(fetched, raw) {
		t.Fatalf("pinned HTTPS import mismatch: bytes=%d err=%v", len(fetched), err)
	}
	if _, err := FetchPinnedHTTPS(t.Context(), server.URL+"/redirect",
		packageTestDigest(raw), server.Client()); err == nil {
		t.Fatal("plugin HTTPS import followed a redirect")
	}
	if _, err := FetchPinnedHTTPS(t.Context(), server.URL+"/archive",
		strings.Repeat("a", 64), server.Client()); err == nil {
		t.Fatal("plugin HTTPS import accepted digest drift")
	}
}

func TestFetchPinnedGitArchiveReadsOnlyExactCommitBlob(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			testFetchPinnedGitArchiveReadsOnlyExactCommitBlob(t, objectFormat)
		})
	}
}

func testFetchPinnedGitArchiveReadsOnlyExactCommitBlob(t *testing.T, objectFormat string) {
	t.Helper()
	repository := t.TempDir()
	runPluginImportTestGit(t, repository, "init", "--quiet", "--object-format="+objectFormat)
	runPluginImportTestGit(t, repository, "config", "user.name", "Plugin Import Test")
	runPluginImportTestGit(t, repository, "config", "user.email", "plugin-import@example.invalid")

	manifest, files := pluginSkillFixture(t)
	first, err := BuildUnsignedPackage(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(repository, "plugin.zip")
	if err := os.WriteFile(archivePath, first, 0o600); err != nil {
		t.Fatal(err)
	}
	runPluginImportTestGit(t, repository, "add", "--", "plugin.zip")
	runPluginImportTestGit(t, repository, "commit", "--quiet", "-m", "first")
	firstCommit := strings.TrimSpace(runPluginImportTestGit(t, repository, "rev-parse", "HEAD"))
	wantCommitLength := 40
	if objectFormat == "sha256" {
		wantCommitLength = 64
	}
	if len(firstCommit) != wantCommitLength {
		t.Fatalf("Git fixture object format %s produced commit %q", objectFormat, firstCommit)
	}

	manifest.Version = "2.0.0"
	second, err := BuildUnsignedPackage(manifest, files)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, second, 0o600); err != nil {
		t.Fatal(err)
	}
	runPluginImportTestGit(t, repository, "add", "--", "plugin.zip")
	runPluginImportTestGit(t, repository, "commit", "--quiet", "-m", "second")
	secondCommit := strings.TrimSpace(runPluginImportTestGit(t, repository, "rev-parse", "HEAD"))
	if firstCommit == secondCommit {
		t.Fatal("Git fixture did not advance")
	}

	fetched, err := fetchPinnedGitArchive(t.Context(), repository, firstCommit,
		"plugin.zip", t.TempDir(), true)
	if err != nil || !bytes.Equal(fetched, first) {
		t.Fatalf("exact Git commit import mismatch: bytes=%d err=%v", len(fetched), err)
	}
	if _, err := fetchPinnedGitArchive(t.Context(), repository, secondCommit,
		"missing.zip", t.TempDir(), true); err == nil {
		t.Fatal("plugin Git import accepted an absent archive blob")
	}
	if _, err := FetchPinnedGitArchive(t.Context(), repository, firstCommit,
		"plugin.zip", t.TempDir()); err == nil {
		t.Fatal("production plugin Git import accepted a non-HTTPS repository")
	}
}

func runPluginImportTestGit(t *testing.T, repository string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", repository}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
	}
	return string(output)
}
