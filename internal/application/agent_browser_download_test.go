package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolgateway"
)

type downloadReleaseTestStore struct {
	AgentBrowserStore
	run       domain.Run
	workspace session.WorkspaceInfo
}

func (s *downloadReleaseTestStore) GetRun(context.Context, string) (domain.Run, error) {
	return s.run, nil
}

func (s *downloadReleaseTestStore) GetWorkspaceInfo(context.Context, string) (session.WorkspaceInfo, error) {
	return s.workspace, nil
}

type downloadReleaseTestRuntime struct {
	receipt browserruntime.AgentBrowserDownloadReceipt
	data    []byte
	err     error
}

func (r *downloadReleaseTestRuntime) Status() browserruntime.AgentBrowserStatus {
	return browserruntime.AgentBrowserStatus{Downloads: []browserruntime.AgentBrowserDownloadReceipt{r.receipt}}
}

func (r *downloadReleaseTestRuntime) ReadDownload(context.Context, string) ([]byte, browserruntime.AgentBrowserDownloadReceipt, error) {
	return r.data, r.receipt, r.err
}

// The remaining agentBrowserRuntime methods are unreachable on this fake: the
// release path only touches Status and ReadDownload.
func (r *downloadReleaseTestRuntime) Navigate(context.Context, string) (browserruntime.AgentBrowserNavigation, error) {
	return browserruntime.AgentBrowserNavigation{}, nil
}

func (r *downloadReleaseTestRuntime) Snapshot(context.Context) (browserruntime.AgentBrowserSnapshot, error) {
	return browserruntime.AgentBrowserSnapshot{}, nil
}

func (r *downloadReleaseTestRuntime) Click(context.Context, string, string) (browserruntime.AgentBrowserInteraction, error) {
	return browserruntime.AgentBrowserInteraction{}, nil
}

func (r *downloadReleaseTestRuntime) Type(context.Context, string, string, string, bool) (browserruntime.AgentBrowserInteraction, error) {
	return browserruntime.AgentBrowserInteraction{}, nil
}

func (r *downloadReleaseTestRuntime) Upload(context.Context, string, string, string) (browserruntime.AgentBrowserInteraction, error) {
	return browserruntime.AgentBrowserInteraction{}, nil
}

func (r *downloadReleaseTestRuntime) Screenshot(context.Context) (browserruntime.AgentBrowserScreenshot, error) {
	return browserruntime.AgentBrowserScreenshot{}, nil
}

func (r *downloadReleaseTestRuntime) Scroll(context.Context, float64, float64) (browserruntime.AgentBrowserInteraction, error) {
	return browserruntime.AgentBrowserInteraction{}, nil
}

func (r *downloadReleaseTestRuntime) Key(context.Context, string) (browserruntime.AgentBrowserInteraction, error) {
	return browserruntime.AgentBrowserInteraction{}, nil
}

func (r *downloadReleaseTestRuntime) Cancel() {}

func (r *downloadReleaseTestRuntime) Close(context.Context) (browserruntime.AgentBrowserCleanup, error) {
	return browserruntime.AgentBrowserCleanup{}, nil
}

func (r *downloadReleaseTestRuntime) Done() <-chan struct{} { return nil }

func newDownloadReleaseTestService(t *testing.T, runtime *downloadReleaseTestRuntime) *AgentBrowserService {
	t.Helper()
	root := t.TempDir()
	store := &downloadReleaseTestStore{
		run:       domain.Run{ID: "run-download-release"},
		workspace: session.WorkspaceInfo{ID: "ws-download-release", RootPath: root},
	}
	service := NewAgentBrowserService(store, AgentBrowserOptions{})
	service.slots["run-download-release"] = &agentBrowserSlot{
		runtime:   runtime,
		authority: toolgateway.AgentBrowserCallAuthority{RunID: "run-download-release", WorkspaceID: "ws-download-release", SessionID: "session-download-release"},
	}
	return service
}

func completedDownloadReceipt(t *testing.T, data []byte) browserruntime.AgentBrowserDownloadReceipt {
	t.Helper()
	sum := sha256.Sum256(data)
	return browserruntime.AgentBrowserDownloadReceipt{GUID: "0f1e2d3c-4b5a-4647-8879-0a1b2c3d4e5f",
		SuggestedFilename: "report.pdf", URL: "https://example.com/report.pdf",
		State: browserruntime.AgentBrowserDownloadCompleted, Bytes: int64(len(data)),
		SHA256: hex.EncodeToString(sum[:])}
}

// releaseIdentity is the server-side release identity: the control-plane
// principal that authorized the release plus the declared operator name.
func releaseIdentity(operator string) AgentBrowserDownloadRelease {
	return AgentBrowserDownloadRelease{Principal: AgentBrowserDownloadReleasePrincipalControlPlane, Operator: operator}
}

func TestAgentBrowserDownloadReleasePersistsCaptureAndProvenance(t *testing.T) {
	data := []byte("released capture bytes")
	runtime := &downloadReleaseTestRuntime{receipt: completedDownloadReceipt(t, data), data: data}
	service := newDownloadReleaseTestService(t, runtime)

	listed, err := service.BrowserDownloads(context.Background(), "run-download-release")
	if err != nil || len(listed) != 1 || listed[0].State != browserruntime.AgentBrowserDownloadCompleted {
		t.Fatalf("list: %+v %v", listed, err)
	}

	locator, receipt, reported, err := service.ReleaseBrowserDownload(context.Background(),
		"run-download-release", runtime.receipt.GUID, releaseIdentity("operator-a"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(locator, "/"+runtime.receipt.GUID) || receipt.SHA256 == "" ||
		reported.DeclaredOperator != "operator-a" || reported.ApprovedAt.IsZero() {
		t.Fatalf("locator/receipt/approval mismatch: %q %+v %+v", locator, receipt, reported)
	}
	root := filepath.Join(service.store.(*downloadReleaseTestStore).workspace.RootPath,
		".cyberagent-workbench", "browser-artifacts", "downloads", "run-download-release")
	captured, err := os.ReadFile(filepath.Join(root, runtime.receipt.GUID))
	if err != nil || string(captured) != string(data) {
		t.Fatalf("capture mismatch: %v", err)
	}
	// The deterministic capture sidecar is a release contract: its exact bytes
	// must not change when the approval event is written beside it.
	wantSidecar := fmt.Sprintf(
		`{"bytes":%d,"captured_at":"0001-01-01T00:00:00Z","guid":%q,"run_id":"run-download-release",`+
			`"sha256":%q,"started_at":"0001-01-01T00:00:00Z","suggested_filename":"report.pdf",`+
			`"url":"https://example.com/report.pdf","version":%q,"workspace_id":"ws-download-release"}`,
		receipt.Bytes, receipt.GUID, receipt.SHA256, AgentBrowserDownloadReleaseVersion)
	raw, err := os.ReadFile(filepath.Join(root, runtime.receipt.GUID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != wantSidecar {
		t.Fatalf("capture sidecar changed:\n got %s\nwant %s", raw, wantSidecar)
	}

	// The release-approval event is a separate, non-overwritable record next to
	// the sidecar: it proves which principal approved the release and when.
	approvalPath := filepath.Join(root, runtime.receipt.GUID+".approval.json")
	approvalRaw, err := os.ReadFile(approvalPath)
	if err != nil {
		t.Fatal(err)
	}
	var approval agentBrowserDownloadReleaseApproval
	if err := json.Unmarshal(approvalRaw, &approval); err != nil {
		t.Fatal(err)
	}
	if approval.GUID != receipt.GUID || approval.RunID != "run-download-release" ||
		approval.WorkspaceID != "ws-download-release" || approval.SHA256 != receipt.SHA256 ||
		approval.Bytes != receipt.Bytes || approval.ApprovedAt.IsZero() ||
		approval.ApprovedAt.Location() != time.UTC {
		t.Fatalf("approval event mismatch: %+v", approval)
	}
	if approval.Principal != AgentBrowserDownloadReleasePrincipalControlPlane ||
		approval.PrincipalScope != AgentBrowserDownloadReleasePrincipalScopeCredentialClass {
		t.Fatalf("approval principal mismatch: %+v", approval)
	}
	if approval.DeclaredOperator != "operator-a" || approval.Principal == approval.DeclaredOperator {
		t.Fatalf("declared operator is not a distinct field from the principal: %+v", approval)
	}
	if strings.Contains(strings.ToLower(string(approvalRaw)), "token") {
		t.Fatalf("approval event leaked credential material: %s", approvalRaw)
	}

	// Repeating the release with identical bytes is idempotent: the approval
	// event keeps its first timestamp and is never rewritten.
	before, err := os.Stat(approvalPath)
	if err != nil {
		t.Fatal(err)
	}
	again, _, replay, err := service.ReleaseBrowserDownload(context.Background(),
		"run-download-release", runtime.receipt.GUID, releaseIdentity("operator-a"))
	if err != nil || again != locator || replay.DeclaredOperator != reported.DeclaredOperator ||
		!replay.ApprovedAt.Equal(reported.ApprovedAt) || replay.DeclaredOperator != approval.DeclaredOperator {
		t.Fatalf("release replay: %q %+v %+v %v", again, replay, reported, err)
	}
	repeated, err := os.ReadFile(approvalPath)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(approvalPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(repeated) != string(approvalRaw) || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("repeat release rewrote the approval event: %s", repeated)
	}
	repeatedSidecar, err := os.ReadFile(filepath.Join(root, runtime.receipt.GUID+".json"))
	if err != nil || string(repeatedSidecar) != wantSidecar {
		t.Fatalf("repeat release changed the capture sidecar: %v", err)
	}
}

// TestAgentBrowserDownloadReleaseReportsTheRecordedApproval pins the audit
// contract of a repeat release: the approval event on disk keeps the first
// approver, and what the release returns is that event rather than the identity
// of whoever replayed the request.
func TestAgentBrowserDownloadReleaseReportsTheRecordedApproval(t *testing.T) {
	data := []byte("released capture bytes")
	runtime := &downloadReleaseTestRuntime{receipt: completedDownloadReceipt(t, data), data: data}
	service := newDownloadReleaseTestService(t, runtime)
	_, _, first, err := service.ReleaseBrowserDownload(context.Background(),
		"run-download-release", runtime.receipt.GUID, releaseIdentity("operator-alice"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, replay, err := service.ReleaseBrowserDownload(context.Background(),
		"run-download-release", runtime.receipt.GUID, releaseIdentity("operator-bob"))
	if err != nil {
		t.Fatal(err)
	}
	if replay.DeclaredOperator != "operator-alice" || !replay.ApprovedAt.Equal(first.ApprovedAt) {
		t.Fatalf("replay reported the replaying caller: first=%+v replay=%+v", first, replay)
	}
	directory := filepath.Join(service.store.(*downloadReleaseTestStore).workspace.RootPath,
		".cyberagent-workbench", "browser-artifacts", "downloads", "run-download-release")
	raw, err := os.ReadFile(filepath.Join(directory, runtime.receipt.GUID+".approval.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded agentBrowserDownloadReleaseApproval
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.DeclaredOperator != replay.DeclaredOperator || !recorded.ApprovedAt.Equal(replay.ApprovedAt) {
		t.Fatalf("response is not the persisted approval event: disk=%+v response=%+v", recorded, replay)
	}
}

// TestAgentBrowserDownloadReleaseRollsBackFailedArtifactWrite fails each of the
// three exclusive creates in turn. A half-written capture, sidecar, or approval
// event would fail the byte comparison on every later release and wedge the
// download, so a failed create has to leave nothing behind and the next release
// has to succeed.
func TestAgentBrowserDownloadReleaseRollsBackFailedArtifactWrite(t *testing.T) {
	data := []byte("released capture bytes")
	for index := range 3 {
		failAt := index + 1
		t.Run(fmt.Sprintf("write-%d", failAt), func(t *testing.T) {
			runtime := &downloadReleaseTestRuntime{receipt: completedDownloadReceipt(t, data), data: data}
			service := newDownloadReleaseTestService(t, runtime)
			directory := filepath.Join(service.store.(*downloadReleaseTestStore).workspace.RootPath,
				".cyberagent-workbench", "browser-artifacts", "downloads", "run-download-release")
			guid := runtime.receipt.GUID
			names := []string{guid, guid + ".json", guid + ".approval.json"}

			restore := writeAgentBrowserDownloadArtifactBytes
			// A fatal below must not hand the failing writer to the next case.
			t.Cleanup(func() { writeAgentBrowserDownloadArtifactBytes = restore })
			calls := 0
			writeAgentBrowserDownloadArtifactBytes = func(file *os.File, content []byte) error {
				calls++
				if calls != failAt {
					return restore(file, content)
				}
				// writeFullCDPArtifact always closes the handle, even when the
				// write fails; the rollback needs that on Windows.
				_ = file.Close()
				return errors.New("simulated artifact write failure")
			}
			_, _, _, err := service.ReleaseBrowserDownload(context.Background(),
				"run-download-release", guid, releaseIdentity("operator-a"))
			if err == nil || err.Error() != "simulated artifact write failure" {
				t.Fatalf("release at write %d: %v", failAt, err)
			}
			if _, statErr := os.Stat(filepath.Join(directory, names[failAt-1])); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed create left a partial artifact: %v", statErr)
			}
			for _, name := range names[:failAt-1] {
				if _, statErr := os.Stat(filepath.Join(directory, name)); statErr != nil {
					t.Fatalf("artifact written before the failure vanished: %v", statErr)
				}
			}
			// With the writer restored, the same release completes: the earlier
			// artifacts are replayed byte-for-byte and the missing ones created.
			writeAgentBrowserDownloadArtifactBytes = restore
			locator, _, approval, err := service.ReleaseBrowserDownload(context.Background(),
				"run-download-release", guid, releaseIdentity("operator-a"))
			if err != nil {
				t.Fatalf("retry after the failed write: %v", err)
			}
			if !strings.HasSuffix(locator, "/"+guid) || approval.DeclaredOperator != "operator-a" {
				t.Fatalf("retry result mismatch: %q %+v", locator, approval)
			}
			captured, err := os.ReadFile(filepath.Join(directory, guid))
			if err != nil || string(captured) != string(data) {
				t.Fatalf("retry capture mismatch: %v", err)
			}
			for _, name := range names {
				if _, statErr := os.Stat(filepath.Join(directory, name)); statErr != nil {
					t.Fatalf("artifact %q missing after the retry: %v", name, statErr)
				}
			}
		})
	}
}

func TestAgentBrowserDownloadReleaseRejectsConflictingApprovalEvent(t *testing.T) {
	data := []byte("released capture bytes")
	receipt := completedDownloadReceipt(t, data)
	runtime := &downloadReleaseTestRuntime{receipt: receipt, data: data}
	service := newDownloadReleaseTestService(t, runtime)
	directory := filepath.Join(service.store.(*downloadReleaseTestStore).workspace.RootPath,
		".cyberagent-workbench", "browser-artifacts", "downloads", "run-download-release")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	conflicting := fmt.Sprintf(
		`{"version":%q,"kind":"release_approval","guid":%q,"run_id":"run-download-release",`+
			`"workspace_id":"ws-download-release","sha256":%q,"bytes":%d,`+
			`"approved_at":"2026-01-01T00:00:00Z","authorized_principal":"control_plane",`+
			`"authorized_principal_scope":"credential_class","declared_operator":"someone-else"}`,
		AgentBrowserDownloadReleaseVersion, receipt.GUID, strings.Repeat("a", 64), receipt.Bytes)
	path := filepath.Join(directory, receipt.GUID+".approval.json")
	if err := os.WriteFile(path, []byte(conflicting), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.ReleaseBrowserDownload(context.Background(),
		"run-download-release", receipt.GUID, releaseIdentity("operator-a")); err == nil ||
		!strings.Contains(err.Error(), "approval event conflicts") {
		t.Fatalf("conflicting approval event was not rejected: %v", err)
	}
	recorded, err := os.ReadFile(path)
	if err != nil || string(recorded) != conflicting {
		t.Fatalf("conflicting approval event was overwritten: %v", err)
	}
}

func TestAgentBrowserDownloadReleaseRejectsUnknownAndMismatched(t *testing.T) {
	data := []byte("mismatched bytes")
	receipt := completedDownloadReceipt(t, []byte("other bytes"))
	runtime := &downloadReleaseTestRuntime{receipt: receipt, data: data}
	service := newDownloadReleaseTestService(t, runtime)
	if _, _, _, err := service.ReleaseBrowserDownload(context.Background(),
		"run-download-release", receipt.GUID, releaseIdentity("operator-a")); err == nil ||
		!strings.Contains(err.Error(), "integrity mismatch") {
		t.Fatalf("integrity mismatch not rejected: %v", err)
	}

	unknown := &downloadReleaseTestRuntime{
		receipt: completedDownloadReceipt(t, data), data: data,
		err: browserruntime.ErrAgentBrowserDownloadUnknown,
	}
	unknownService := newDownloadReleaseTestService(t, unknown)
	if _, _, _, err := unknownService.ReleaseBrowserDownload(context.Background(),
		"run-download-release", receipt.GUID, releaseIdentity("operator-a")); err == nil ||
		!strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown download not rejected: %v", err)
	}

	// A capture that vanished with the session quarantine surfaces as a
	// sanitized unavailable error, never a filesystem path.
	gone := &downloadReleaseTestRuntime{
		receipt: completedDownloadReceipt(t, data), data: data,
		err: fmt.Errorf("%w", browserruntime.ErrAgentBrowserDownloadGone),
	}
	goneService := newDownloadReleaseTestService(t, gone)
	if _, _, _, err := goneService.ReleaseBrowserDownload(context.Background(),
		"run-download-release", receipt.GUID, releaseIdentity("operator-a")); err == nil ||
		!strings.Contains(err.Error(), "gone with the session quarantine") ||
		strings.Contains(err.Error(), "downloads") {
		t.Fatalf("gone capture leaked internals: %v", err)
	}

}

func TestAgentBrowserDownloadListWithoutLiveSessionIsEmpty(t *testing.T) {
	service := newDownloadReleaseTestService(t, &downloadReleaseTestRuntime{})
	delete(service.slots, "run-download-release")
	listed, err := service.BrowserDownloads(context.Background(), "run-download-release")
	if err != nil || len(listed) != 0 {
		t.Fatalf("missing session downloads: %+v %v", listed, err)
	}
}

func TestBrowserHeadedFromEnvironment(t *testing.T) {
	t.Setenv("CYBERAGENT_BROWSER_HEADED", "")
	if BrowserHeadedFromEnvironment() {
		t.Fatal("empty environment must stay headless")
	}
	for _, value := range []string{"1", "true", "TRUE", " true "} {
		t.Setenv("CYBERAGENT_BROWSER_HEADED", value)
		if !BrowserHeadedFromEnvironment() {
			t.Fatalf("headed value %q not honored", value)
		}
	}
	for _, value := range []string{"0", "false", "yes", "on"} {
		t.Setenv("CYBERAGENT_BROWSER_HEADED", value)
		if BrowserHeadedFromEnvironment() {
			t.Fatalf("non-opt-in value %q enabled headed mode", value)
		}
	}
}

func TestBrowserSessionLifetimeFromEnvironment(t *testing.T) {
	t.Setenv("CYBERAGENT_BROWSER_SESSION_LIFETIME_MINUTES", "")
	if got := BrowserSessionLifetimeFromEnvironment(); got != 0 {
		t.Fatalf("unset environment must keep the default: %v", got)
	}
	t.Setenv("CYBERAGENT_BROWSER_SESSION_LIFETIME_MINUTES", " 90 ")
	if got := BrowserSessionLifetimeFromEnvironment(); got != 90*time.Minute {
		t.Fatalf("90 minutes not honored: %v", got)
	}
	for _, value := range []string{"abc", "4", "1441", "-10"} {
		t.Setenv("CYBERAGENT_BROWSER_SESSION_LIFETIME_MINUTES", value)
		if got := BrowserSessionLifetimeFromEnvironment(); got != 0 {
			t.Fatalf("out-of-range value %q must fall back to the default: %v", value, got)
		}
	}
}

func TestAgentBrowserUploadResolverContainment(t *testing.T) {
	root := t.TempDir()
	store := &downloadReleaseTestStore{
		run:       domain.Run{ID: "run-upload"},
		workspace: session.WorkspaceInfo{ID: "ws-upload", RootPath: root},
	}
	service := NewAgentBrowserService(store, AgentBrowserOptions{})
	if err := os.WriteFile(filepath.Join(root, "report.txt"), []byte("upload payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := service.resolveAgentBrowserUpload(context.Background(), "ws-upload", "docs/../report.txt")
	if err != nil || !filepath.IsAbs(resolved) {
		t.Fatalf("regular file: %v %q", err, resolved)
	}

	for _, relative := range []string{"missing.txt", "docs", "", "../outside.txt", "report.txt/../../escape.txt", "report.txt/x"} {
		if _, err := service.resolveAgentBrowserUpload(context.Background(), "ws-upload", relative); err == nil {
			t.Fatalf("upload path %q accepted", relative)
		}
	}
	oversize := filepath.Join(root, "big.bin")
	if err := os.WriteFile(oversize, make([]byte, AgentBrowserMaxUploadBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.resolveAgentBrowserUpload(context.Background(), "ws-upload", "big.bin"); err == nil {
		t.Fatal("oversize upload accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "empty.bin"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.resolveAgentBrowserUpload(context.Background(), "ws-upload", "empty.bin"); err == nil {
		t.Fatal("empty upload accepted")
	}
	// The link points OUTSIDE the workspace root: resolving it must escape.
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "escape.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outsideDir, "escape.txt"), filepath.Join(root, "docs", "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := service.resolveAgentBrowserUpload(context.Background(), "ws-upload", "docs/link.txt"); err == nil {
		t.Fatal("symlinked escape accepted")
	}
}
