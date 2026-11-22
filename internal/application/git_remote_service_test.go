package application

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/repository"
	"cyberagent-workbench/internal/store"
)

func remoteFixtureGit(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	hideSubprocessWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func newRemoteServiceFixture(t *testing.T) (*store.SQLiteStore, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary unavailable")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	base := t.TempDir()
	batch := filepath.Join(base, "remote.git")
	remoteFixtureGit(t, "init", "--bare", "--quiet", batch)
	work := filepath.Join(base, "work")
	remoteFixtureGit(t, "init", "--quiet", work)
	remoteFixtureGit(t, "-C", work, "config", "user.email", "x@example.com")
	remoteFixtureGit(t, "-C", work, "config", "user.name", "x")
	if err := os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	remoteFixtureGit(t, "-C", work, "add", "base.txt")
	remoteFixtureGit(t, "-C", work, "commit", "--quiet", "-m", "base")
	remoteFixtureGit(t, "-C", work, "branch", "-M", "main")
	remoteURL := "file://" + filepath.ToSlash(batch)
	remoteFixtureGit(t, "-C", work, "push", "--quiet", remoteURL, "main")
	if err := st.SaveWorkspace(context.Background(), store.WorkspaceRecord{
		ID: "ws-1", Name: "remote", RootPath: work, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	_, run, err := NewRunService(st).Create(context.Background(), CreateRunRequest{
		Goal: "remote", Profile: "code", Surface: "code", Phase: "plan",
		WorkspaceID: "ws-1", Budget: domain.Budget{MaxTurns: 5, MaxToolCalls: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, run.ID, remoteURL
}

func TestGitRemoteServiceCreatePRWithReferencedCredential(t *testing.T) {
	ctx := context.Background()
	st, runID, _ := newRemoteServiceFixture(t)
	storeCred := credential.NewMemoryStore()
	if err := storeCred.Put(ctx, "github-pat", "secret-token"); err != nil {
		t.Fatal(err)
	}
	apiServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret-token" {
			t.Fatalf("missing bearer: %q", request.Header.Get("Authorization"))
		}
		writer.WriteHeader(201)
		_ = json.NewEncoder(writer).Encode(map[string]any{"html_url": "https://github.com/o/r/pull/9",
			"number": 9, "state": "open", "title": "feat"})
	}))
	defer apiServer.Close()
	prClient := repository.NewPRClient()
	prClient.SetAPIBaseForTest(apiServer.URL)
	executor, err := repository.NewRemoteExecutor(storeCred)
	if err != nil {
		t.Fatal(err)
	}
	executor.AllowLocalRemotesForTest()
	service := NewGitRemoteService(st, executor, prClient, storeCred)
	spec := repository.RemoteSpec{
		ProtocolVersion: repository.RemoteProtocolVersion, Operation: repository.RemoteCreatePR,
		RemoteURL: "https://github.com/owner/repo.git", Branch: "feat", BaseBranch: "main",
		PRTitle: "feat", PRBody: "body", CredentialName: "github-pat", NetworkTTLMillis: 30000,
	}
	result, err := service.Execute(ctx, GitRemoteRequest{RunID: runID, OperationKey: "remote-key-0001", Spec: spec})
	if err != nil {
		t.Fatalf("create PR: %v", err)
	}
	if result.Record.PullRequestURL == "" || result.Record.PullRequestNumber != 9 ||
		result.Record.RemoteHost != "github.com" {
		t.Fatalf("receipt invalid: %#v", result.Record)
	}
	// The credential name and secret must not be stored anywhere.
	if strings.Contains(result.Record.SpecJSON, "secret-token") || strings.Contains(result.Record.SpecJSON, "github-pat") == false {
		t.Fatalf("credential hygiene violated: %s", result.Record.SpecJSON)
	}
	timeline, err := st.ListRunEvents(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range timeline {
		if event.Type == "git.remote_completed" {
			found = true
			if strings.Contains(event.PayloadJSON, "secret-token") {
				t.Fatal("credential leaked into run events")
			}
		}
	}
	if !found {
		t.Fatal("git.remote_completed event missing")
	}
}

func TestGitRemoteServicePendingReplayFailsClosed(t *testing.T) {
	ctx := context.Background()
	st, runID, _ := newRemoteServiceFixture(t)
	credentials := credential.NewMemoryStore()
	executor, err := repository.NewRemoteExecutor(credentials)
	if err != nil {
		t.Fatal(err)
	}
	service := NewGitRemoteService(st, executor, nil, credentials)
	request := GitRemoteRequest{RunID: runID, OperationKey: "remote-key-pending-0001",
		Spec: repository.RemoteSpec{
			ProtocolVersion:  repository.RemoteProtocolVersion,
			Operation:        repository.RemoteCreatePR,
			RemoteURL:        "https://github.com/owner/repo.git",
			Branch:           "feature",
			BaseBranch:       "main",
			PRTitle:          "feature",
			NetworkTTLMillis: 30000,
		}}
	if _, err := service.Execute(ctx, request); err == nil ||
		apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("missing PR client did not leave a failed pending attempt: %v", err)
	}
	replayed, err := service.Execute(ctx, request)
	if err == nil || apperror.CodeOf(err) != apperror.CodeFailedPrecondition ||
		!replayed.Replayed || replayed.Record.CompletedAt != nil ||
		!strings.Contains(err.Error(), "reconcile remote state") {
		t.Fatalf("pending remote replay reported success: result=%#v err=%v", replayed, err)
	}
}
