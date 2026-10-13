package repository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/credential"
)

func remoteGit(t *testing.T, args ...string) string {
	t.Helper()
	output, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func newRemoteFixture(t *testing.T) (remoteURL, workRoot string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary is not available")
	}
	base := t.TempDir()
	batch := filepath.Join(base, "remote.git")
	remoteGit(t, "init", "--bare", "--quiet", batch)
	workRoot = filepath.Join(base, "work")
	remoteGit(t, "init", "--quiet", workRoot)
	remoteGit(t, "-C", workRoot, "config", "user.email", "test@example.com")
	remoteGit(t, "-C", workRoot, "config", "user.name", "remote-test")
	if err := os.WriteFile(filepath.Join(workRoot, "base.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, "-C", workRoot, "add", "base.txt")
	remoteGit(t, "-C", workRoot, "commit", "--quiet", "-m", "baseline")
	remoteGit(t, "-C", workRoot, "branch", "-M", "main")
	remoteURL = "file://" + filepath.ToSlash(batch)
	remoteGit(t, "-C", workRoot, "push", "--quiet", remoteURL, "main")
	remoteGit(t, "-C", workRoot, "fetch", "--quiet", remoteURL, "main")
	return remoteURL, workRoot
}

func newRemoteExecutor(t *testing.T) *RemoteExecutor {
	t.Helper()
	executor, err := NewRemoteExecutor(credential.NewMemoryStore())
	if err != nil {
		t.Fatal(err)
	}
	executor.allowLocalRemote = true
	return executor
}

func remoteSpec(op RemoteOperation, remoteURL, branch string) RemoteSpec {
	return RemoteSpec{
		ProtocolVersion: RemoteProtocolVersion, Operation: op,
		RemoteURL: remoteURL, Branch: branch,
		NetworkTTLMillis: 30_000,
	}
}

func TestRemoteSpecValidationFailsClosed(t *testing.T) {
	executor := newRemoteExecutor(t)
	executor.allowLocalRemote = false
	cases := []struct {
		name string
		spec RemoteSpec
	}{

		{name: "http scheme", spec: remoteSpec(RemoteFetch, "http://example.com/repo.git", "main")},

		{name: "loopback", spec: remoteSpec(RemoteFetch, "https://127.0.0.1/repo.git", "main")},

		{name: "credential in url", spec: remoteSpec(RemoteFetch, "https://user:pass@example.com/repo.git", "main")},

		{name: "unknown op", spec: RemoteSpec{ProtocolVersion: RemoteProtocolVersion, Operation: "force_push", RemoteURL: "https://example.com/r.git", Branch: "main", NetworkTTLMillis: 1000}},

		{name: "pr without base", spec: RemoteSpec{ProtocolVersion: RemoteProtocolVersion, Operation: RemoteCreatePR, RemoteURL: "https://example.com/r.git", Branch: "feat", PRTitle: "t", NetworkTTLMillis: 1000}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := executor.ValidateSpec(tc.spec); err == nil {
				t.Fatalf("hostile spec accepted: %#v", tc.spec)
			}
		})
	}
}

func TestRemoteFetchAndFastForwardPull(t *testing.T) {
	ctx := context.Background()
	remoteURL, workRoot := newRemoteFixture(t)
	executor := newRemoteExecutor(t)
	// Advance the remote with an unrelated clone.
	other := filepath.Join(t.TempDir(), "other")
	remoteGit(t, "clone", "--quiet", "-b", "main", remoteURL, other)
	remoteGit(t, "-C", other, "config", "user.email", "x@example.com")
	remoteGit(t, "-C", other, "config", "user.name", "x")
	if err := os.WriteFile(filepath.Join(other, "adv.txt"), []byte("adv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, "-C", other, "add", "adv.txt")
	remoteGit(t, "-C", other, "commit", "--quiet", "-m", "advance")
	remoteGit(t, "-C", other, "push", "--quiet", remoteURL, "main")
	advancedHead := remoteGit(t, "-C", other, "rev-parse", "HEAD")

	preHead := remoteGit(t, "-C", workRoot, "rev-parse", "HEAD")
	binding := RemoteBinding{ProtocolVersion: RemoteProtocolVersion, RunID: "run-1",
		WorkspaceID: "ws-1", LocalHead: preHead, RemoteHost: "local", Protocol: "https", Branch: "main"}
	spec := remoteSpec(RemotePullFF, remoteURL, "main")
	receipt, err := executor.ExecuteGit(ctx, workRoot, spec, binding, "op-key-0001")
	if err != nil {
		t.Fatalf("pull_ff: %v", err)
	}
	if receipt.PostHead != advancedHead || receipt.CommitID != advancedHead {
		t.Fatalf("fast-forward pull did not land on %s: %#v", advancedHead, receipt)
	}
}

func TestRemotePushBranchRejectsExistingBranch(t *testing.T) {
	ctx := context.Background()
	remoteURL, workRoot := newRemoteFixture(t)
	executor := newRemoteExecutor(t)
	preHead := remoteGit(t, "-C", workRoot, "rev-parse", "HEAD")
	binding := RemoteBinding{ProtocolVersion: RemoteProtocolVersion, RunID: "run-1",
		WorkspaceID: "ws-1", LocalHead: preHead, RemoteHost: "local", Protocol: "https", Branch: "main"}
	// main already exists on the remote → default-deny.
	spec := remoteSpec(RemotePushBranch, remoteURL, "main")
	if _, err := executor.ExecuteGit(ctx, workRoot, spec, binding, "op-key-0002"); err == nil ||
		apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("existing branch push was not rejected: %v", err)
	}
	// A brand-new branch pushes cleanly.
	remoteGit(t, "-C", workRoot, "checkout", "--quiet", "-b", "feat")
	if err := os.WriteFile(filepath.Join(workRoot, "feat.txt"), []byte("feat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, "-C", workRoot, "add", "feat.txt")
	remoteGit(t, "-C", workRoot, "commit", "--quiet", "-m", "feat")
	featHead := remoteGit(t, "-C", workRoot, "rev-parse", "HEAD")
	binding.LocalHead = featHead
	binding.Branch = "feat"
	spec = remoteSpec(RemotePushBranch, remoteURL, "feat")
	if _, err := executor.ExecuteGit(ctx, workRoot, spec, binding, "op-key-0003"); err != nil {
		t.Fatalf("new branch push: %v", err)
	}
	remoteBranch := remoteGit(t, "ls-remote", remoteURL, "refs/heads/feat")
	if !strings.Contains(remoteBranch, featHead) {
		t.Fatalf("remote branch missing: %q", remoteBranch)
	}
}

func TestRemoteCredentialRedaction(t *testing.T) {
	if strings.Contains(redactRemoteOutput("error: https://token-abc@host", "token-abc"), "token-abc") {
		t.Fatal("credential leaked through redaction")
	}
	if redactRemoteOutput("plain", "") != "plain" {
		t.Fatal("plain output changed")
	}
}

func TestRemoteAskpassHelperResolvesCredentialByName(t *testing.T) {
	ctx := context.Background()
	store := credential.NewMemoryStore()
	if err := store.Put(ctx, "github-pat", "secret-token-value"); err != nil {
		t.Fatal(err)
	}
	executor, err := NewRemoteExecutor(store)
	if err != nil {
		t.Fatal(err)
	}
	path, secret, err := executor.askpassHelper(ctx, "github-pat")
	if err != nil || secret != "secret-token-value" || path == "" {
		t.Fatalf("askpass helper: path=%q secret=%q err=%v", path, secret, err)
	}
	_ = os.Remove(path)
}

func TestRemoteGitCommandIsCredentialedAndHardened(t *testing.T) {
	executor := newRemoteExecutor(t)
	transport, err := executor.pinRemoteTransport("https://example.com/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(transport.path)
	command := executor.remoteGitCommand(t.Context(), t.TempDir(),
		transport, "C:/temporary/askpass.cmd", "secret-token-value",
		"ls-remote", "--heads", "https://example.com/repo.git", "refs/heads/feature")
	args := strings.Join(command.Args, "\x00")
	for _, required := range []string{
		"--no-optional-locks", "http.proxy=", "https.proxy=",
		"core.sshCommand=", "protocol.ext.allow=never", "ls-remote",
	} {
		if !strings.Contains(args, required) {
			t.Fatalf("remote branch probe omitted hardened argument %q: %q", required, command.Args)
		}
	}
	if strings.Contains(args, "secret-token-value") {
		t.Fatalf("credential leaked into git argv: %q", command.Args)
	}
	environment := strings.Join(command.Env, "\x00")
	for _, required := range []string{
		"GIT_ASKPASS=C:/temporary/askpass.cmd", "GIT_PASSWORD=secret-token-value",
		"GIT_CONFIG_GLOBAL=" + transport.path, "GIT_ALLOW_PROTOCOL=https",
	} {
		if !strings.Contains(environment, required) {
			t.Fatalf("remote branch probe omitted credential environment %q", required)
		}
	}
}

func TestRemoteBranchProbeAuthenticatesWithReferencedCredential(t *testing.T) {
	credentials := credential.NewMemoryStore()
	if err := credentials.Put(t.Context(), "private-remote", "secret-token-value"); err != nil {
		t.Fatal(err)
	}
	executor, err := NewRemoteExecutor(credentials)
	if err != nil {
		t.Fatal(err)
	}
	var authenticated atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="private Git"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if username != "secret-token-value" || password != "secret-token-value" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		authenticated.Store(true)
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte("001e# service=git-upload-pack\n00000000"))
	}))
	defer server.Close()
	executor.AllowLocalRemotesForTest()
	transport, err := executor.pinRemoteTransport(server.URL + "/repository.git")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(transport.path)
	askpass, secret, err := executor.askpassHelper(t.Context(), "private-remote")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(askpass)
	exists, err := executor.remoteBranchExists(t.Context(), t.TempDir(), RemoteSpec{
		RemoteURL: server.URL + "/repository.git", Branch: "feature",
	}, transport, askpass, secret)
	if err != nil || exists || !authenticated.Load() {
		t.Fatalf("credentialed branch probe failed: exists=%t authenticated=%t err=%v",
			exists, authenticated.Load(), err)
	}
}

func TestRemoteTransportPinsReadAndPushAcrossRepositoryConfiguration(t *testing.T) {
	for _, source := range []string{"insteadOf", "pushInsteadOf", "include", "worktree", "named-remote"} {
		t.Run(source, func(t *testing.T) {
			approved, root := newRemoteFixture(t)
			unapproved := filepath.Join(t.TempDir(), "unapproved.git")
			remoteGit(t, "init", "--bare", "--quiet", unapproved)
			otherURL := "file://" + filepath.ToSlash(unapproved)
			switch source {
			case "insteadOf", "pushInsteadOf":
				remoteGit(t, "-C", root, "config", "url."+otherURL+"."+source, approved)
			case "include":
				included := filepath.Join(t.TempDir(), "remote.config")
				remoteGit(t, "config", "--file", included, "url."+otherURL+".insteadOf", approved)
				remoteGit(t, "-C", root, "config", "include.path", included)
			case "worktree":
				remoteGit(t, "-C", root, "config", "extensions.worktreeConfig", "true")
				remoteGit(t, "-C", root, "config", "--worktree", "url."+otherURL+".pushInsteadOf", approved)
			case "named-remote":
				remoteGit(t, "-C", root, "config", "remote."+approved+".url", otherURL)
				remoteGit(t, "-C", root, "config", "remote."+approved+".pushurl", otherURL)
			}
			executor := newRemoteExecutor(t)
			head := remoteGit(t, "-C", root, "rev-parse", "HEAD")
			spec := remoteSpec(RemotePushBranch, approved, "main")
			observed, err := executor.ReadRemoteOID(t.Context(), root, spec)
			if err != nil || observed != head {
				t.Fatalf("preview left approved remote: %q %v", observed, err)
			}
			spec.Branch, spec.CommitOID, spec.ExpectedRemoteOID = "reviewed", head, "missing"
			receipt, err := executor.ExecuteGit(t.Context(), root, spec, RemoteBinding{LocalHead: head}, "pinned-remote")
			if err != nil || receipt.CommitID != head {
				t.Fatalf("approved push failed: %+v %v", receipt, err)
			}
			if got := remoteGit(t, "-C", strings.TrimPrefix(approved, "file://"), "rev-parse", "refs/heads/reviewed"); got != head {
				t.Fatalf("approved remote is missing the pushed commit: %s", got)
			}
			if got := remoteGit(t, "-C", unapproved, "for-each-ref", "refs/heads"); got != "" {
				t.Fatalf("unapproved remote was modified: %s", got)
			}
		})
	}
}

func TestRemoteTransportKeepsHTTPSURLWhenConfigChangesAfterPinning(t *testing.T) {
	_, root := newRemoteFixture(t)
	executor := newRemoteExecutor(t)
	const approved = "https://review-target.invalid/project.git"
	transport, err := executor.pinRemoteTransport(approved)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(transport.path)
	remoteGit(t, "-C", root, "config", "URL.file:///unapproved/.inSteAdOf", approved)
	remoteGit(t, "-C", root, "config", "remote."+approved+".url", "https://unapproved.invalid/project.git")
	command := executor.remoteGitCommand(t.Context(), root, transport, "", "", "ls-remote", "--get-url", approved)
	output, err := command.Output()
	if err != nil || strings.TrimSpace(string(output)) != approved {
		t.Fatalf("late configuration replaced the reviewed HTTPS target: %q %v", output, err)
	}
}

func TestRemoteTransportIgnoresURLSpecificProxy(t *testing.T) {
	_, root := newRemoteFixture(t)
	var approvedRequests, proxyRequests atomic.Int32
	approved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		approvedRequests.Add(1)
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = w.Write([]byte("001e# service=git-upload-pack\n00000000"))
	}))
	defer approved.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	rawURL := approved.URL + "/repository.git"
	remoteGit(t, "-C", root, "config", "http."+rawURL+".proxy", proxy.URL)
	executor := newRemoteExecutor(t)
	transport, err := executor.pinRemoteTransport(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(transport.path)
	_, err = executor.remoteBranchExists(t.Context(), root, RemoteSpec{RemoteURL: rawURL, Branch: "main"}, transport, "", "")
	if err != nil || approvedRequests.Load() == 0 || proxyRequests.Load() != 0 {
		t.Fatalf("remote proxy changed destination: approved=%d proxy=%d err=%v", approvedRequests.Load(), proxyRequests.Load(), err)
	}
}

var _ = time.Now
