package githubreview

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/credential"
)

// This opt-in check uses the production HTTPS client and the active gh login.
// It only reads existing PRs. No token or remote comment body enters the report.
func TestLiveFollowupAuthenticatedPagination(t *testing.T) {
	if os.Getenv("TRAVERSE_GITHUB_LIVE") != "1" {
		t.Skip("set TRAVERSE_GITHUB_LIVE=1 for authenticated GitHub reads")
	}
	secret, err := exec.CommandContext(t.Context(), "gh", "auth", "token", "--hostname", "github.com").Output()
	if err != nil {
		t.Fatal("active gh credential is unavailable")
	}
	lifecycle, err := NewAuthLifecycle(credential.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	ref := CredentialReference{Name: "live-acceptance", Kind: AuthOAuthUser}
	if err := lifecycle.ImportToken(t.Context(), ref, stringTrim(secret)); err != nil {
		t.Fatal("import active gh credential failed")
	}
	manager, err := lifecycle.NewManager("")
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(manager, DefaultNetworkScope())
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := ParseRepository("golang/go")
	first, err := client.ReadFollowupSnapshot(t.Context(), FollowupSnapshotRequest{Repository: repo, Number: 51679, Credential: ref})
	if err != nil {
		t.Fatal(err)
	}
	if first.State != EvidenceVerified || first.Validate() != nil {
		t.Fatalf("incomplete live snapshot: state=%s omissions=%v", first.State, first.Omissions)
	}
	if first.Pagination[2].PagesRead < 2 || len(first.IssueComments) <= MaxItemsPerPage {
		t.Fatalf("live continuation not exercised: %+v", first.Pagination[2])
	}
	second, err := client.ReadFollowupSnapshot(t.Context(), FollowupSnapshotRequest{Repository: repo, Number: 51679, Credential: ref})
	if err != nil {
		t.Fatal(err)
	}
	if second.State != EvidenceVerified || first.Fingerprint != second.Fingerprint {
		t.Fatal("remote state changed between live reads; rerun when stable")
	}
	report := struct {
		Protocol      string         `json:"protocol"`
		URL           string         `json:"url"`
		Head          string         `json:"head"`
		Fingerprint   string         `json:"fingerprint"`
		Pages         []PageEvidence `json:"pagination"`
		Authenticated bool           `json:"authenticated"`
		Stable        bool           `json:"unchanged_fingerprint"`
	}{"github-followup-live-read.v1", first.PullRequest.URL, first.PullRequest.HeadSHA, first.Fingerprint, first.Pagination, true, true}
	if path := os.Getenv("TRAVERSE_GITHUB_LIVE_REPORT"); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		data, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("production_https=true authenticated=true issue_comments=%d comment_pages=%d stable_fingerprint=true model_calls=0", len(first.IssueComments), first.Pagination[2].PagesRead)
}

func stringTrim(value []byte) string {
	for len(value) > 0 && (value[len(value)-1] == '\r' || value[len(value)-1] == '\n') {
		value = value[:len(value)-1]
	}
	return string(value)
}
