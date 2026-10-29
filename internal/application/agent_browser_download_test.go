package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestAgentBrowserDownloadReleasePersistsCaptureAndProvenance(t *testing.T) {
	data := []byte("released capture bytes")
	runtime := &downloadReleaseTestRuntime{receipt: completedDownloadReceipt(t, data), data: data}
	service := newDownloadReleaseTestService(t, runtime)

	listed, err := service.BrowserDownloads(context.Background(), "run-download-release")
	if err != nil || len(listed) != 1 || listed[0].State != browserruntime.AgentBrowserDownloadCompleted {
		t.Fatalf("list: %+v %v", listed, err)
	}

	locator, receipt, err := service.ReleaseBrowserDownload(context.Background(),
		"run-download-release", runtime.receipt.GUID, "operator-a")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(locator, "/"+runtime.receipt.GUID) || receipt.SHA256 == "" {
		t.Fatalf("locator/receipt mismatch: %q %+v", locator, receipt)
	}
	root := filepath.Join(service.store.(*downloadReleaseTestStore).workspace.RootPath,
		".cyberagent-workbench", "browser-artifacts", "downloads", "run-download-release")
	captured, err := os.ReadFile(filepath.Join(root, runtime.receipt.GUID))
	if err != nil || string(captured) != string(data) {
		t.Fatalf("capture mismatch: %v", err)
	}
	var sidecar map[string]any
	raw, err := os.ReadFile(filepath.Join(root, runtime.receipt.GUID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &sidecar); err != nil {
		t.Fatal(err)
	}
	if sidecar["sha256"] != receipt.SHA256 || sidecar["suggested_filename"] != "report.pdf" ||
		sidecar["guid"] != runtime.receipt.GUID {
		t.Fatalf("sidecar provenance mismatch: %+v", sidecar)
	}

	// Repeating the release with identical bytes is idempotent.
	again, _, err := service.ReleaseBrowserDownload(context.Background(),
		"run-download-release", runtime.receipt.GUID, "operator-a")
	if err != nil || again != locator {
		t.Fatalf("release replay: %q %+v", again, err)
	}
}

func TestAgentBrowserDownloadReleaseRejectsUnknownAndMismatched(t *testing.T) {
	data := []byte("mismatched bytes")
	receipt := completedDownloadReceipt(t, []byte("other bytes"))
	runtime := &downloadReleaseTestRuntime{receipt: receipt, data: data}
	service := newDownloadReleaseTestService(t, runtime)
	if _, _, err := service.ReleaseBrowserDownload(context.Background(),
		"run-download-release", receipt.GUID, "operator-a"); err == nil ||
		!strings.Contains(err.Error(), "integrity mismatch") {
		t.Fatalf("integrity mismatch not rejected: %v", err)
	}

	unknown := &downloadReleaseTestRuntime{
		receipt: completedDownloadReceipt(t, data), data: data,
		err: browserruntime.ErrAgentBrowserDownloadUnknown,
	}
	unknownService := newDownloadReleaseTestService(t, unknown)
	if _, _, err := unknownService.ReleaseBrowserDownload(context.Background(),
		"run-download-release", receipt.GUID, "operator-a"); err == nil ||
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
	if _, _, err := goneService.ReleaseBrowserDownload(context.Background(),
		"run-download-release", receipt.GUID, "operator-a"); err == nil ||
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
