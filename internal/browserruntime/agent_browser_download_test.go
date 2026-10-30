package browserruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func downloadTestRuntime(t *testing.T, replies func(request cdpWireMessage) *cdpWireMessage, captured *[]cdpWireMessage) *AgentBrowserRuntime {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(w, req, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request cdpWireMessage
			if conn.ReadJSON(&request) != nil {
				return
			}
			if captured != nil {
				*captured = append(*captured, request)
			}
			if reply := replies(request); reply != nil {
				if err := conn.WriteJSON(*reply); err != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	life, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := &AgentBrowserRuntime{request: AgentBrowserStartRequest{Authority: AgentBrowserAuthority{SessionID: "download-test", Generation: 1},
		CheckAuthority: func(context.Context, AgentBrowserAuthority) error { return nil }},
		life: life, cancel: cancel, done: make(chan struct{}), operation: make(chan struct{}, 1)}
	r.cdp = newAgentBrowserCDP(conn, r.onEvent)
	t.Cleanup(r.cdp.close)
	return r
}

func downloadTestReply(request cdpWireMessage) *cdpWireMessage {
	result := map[string]any{}
	switch request.Method {
	case "Target.createBrowserContext":
		result = map[string]any{"browserContextId": "ctx-1"}
	case "Target.createTarget":
		result = map[string]any{"targetId": "target-1"}
	case "Target.attachToTarget":
		result = map[string]any{"sessionId": "session-1"}
	case "Page.getFrameTree":
		result = map[string]any{"frameTree": map[string]any{"frame": map[string]any{"id": "main", "loaderId": "loader-1", "url": "about:blank"}}}
	case "DOM.getDocument":
		result = map[string]any{"root": map[string]any{"backendNodeId": 7}}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	return &cdpWireMessage{ID: request.ID, SessionID: request.SessionID, Result: raw}
}

func TestAgentBrowserDownloadQuarantineReplacesDeny(t *testing.T) {
	var captured []cdpWireMessage
	r := downloadTestRuntime(t, downloadTestReply, &captured)
	home := t.TempDir()
	r.profile = agentBrowserProfile{path: home}
	if err := r.initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	var behavior map[string]any
	for _, request := range captured {
		if request.Method == "Browser.setDownloadBehavior" {
			if err := json.Unmarshal(request.Params, &behavior); err != nil {
				t.Fatal(err)
			}
		}
	}
	if behavior == nil {
		t.Fatal("setDownloadBehavior was not called")
	}
	if behavior["behavior"] != "allowAndName" || behavior["eventsEnabled"] != true || behavior["browserContextId"] != "ctx-1" {
		t.Fatalf("unexpected download behavior: %+v", behavior)
	}
	quarantine, _ := behavior["downloadPath"].(string)
	if quarantine != filepath.Join(home, "downloads") {
		t.Fatalf("quarantine outside the disposable profile: %q", quarantine)
	}
	if info, err := os.Stat(quarantine); err != nil || !info.IsDir() {
		t.Fatalf("quarantine directory missing: %v", err)
	}
	if r.Status().Downloads != nil {
		t.Fatalf("fresh runtime reported downloads: %+v", r.Status().Downloads)
	}
}

func downloadEventRuntime(t *testing.T) *AgentBrowserRuntime {
	t.Helper()
	r := downloadTestRuntime(t, downloadTestReply, nil)
	r.session = "session-1"
	r.browserContextID = "ctx-1"
	r.downloadPath = t.TempDir()
	r.downloads = map[string]*AgentBrowserDownloadReceipt{}
	return r
}

func waitDownloadState(t *testing.T, r *AgentBrowserRuntime, guid, state string) AgentBrowserDownloadReceipt {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, receipt := range r.Status().Downloads {
			if receipt.GUID == guid && receipt.State == state {
				return receipt
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("download %s never reached %s: %+v", guid, state, r.Status().Downloads)
	return AgentBrowserDownloadReceipt{}
}

func TestAgentBrowserDownloadReceiptLifecycleAndHashing(t *testing.T) {
	r := downloadEventRuntime(t)
	guid := "0f1e2d3c-4b5a-4647-8879-0a1b2c3d4e5f"
	content := []byte("quarantined capture bytes")
	r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
		`{"guid":"` + guid + `","url":"https://example.com/report.pdf","suggestedFilename":"report.pdf"}`)})
	r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(
		`{"guid":"` + guid + `","state":"inProgress","receivedBytes":4,"totalBytes":25}`)})
	if err := os.WriteFile(filepath.Join(r.downloadPath, guid), content, 0o600); err != nil {
		t.Fatal(err)
	}
	r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(
		`{"guid":"` + guid + `","state":"completed","receivedBytes":25}`)})
	receipt := waitDownloadState(t, r, guid, AgentBrowserDownloadCompleted)
	sum := sha256.Sum256(content)
	if receipt.SHA256 != hex.EncodeToString(sum[:]) || receipt.Bytes != 25 ||
		receipt.SuggestedFilename != "report.pdf" || receipt.URL != "https://example.com/report.pdf" {
		t.Fatalf("receipt mismatch: %+v", receipt)
	}

	// A foreign-session download event must not touch this runtime's receipts.
	r.onEvent(cdpWireMessage{SessionID: "other-session", Method: "Browser.downloadWillBegin",
		Params: json.RawMessage(`{"guid":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeffff0001"}`)})
	if len(r.Status().Downloads) != 1 {
		t.Fatalf("foreign session download tracked: %+v", r.Status().Downloads)
	}
	// Malformed payloads are consumed without effects.
	r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(`{"guid":"bad/guid"}`)})
	r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(`{`)})
	if len(r.Status().Downloads) != 1 {
		t.Fatalf("malformed download events mutated state: %+v", r.Status().Downloads)
	}
	// A canceled event after completion is a stale duplicate: the receipt
	// keeps its terminal completed state and digest.
	r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(
		`{"guid":"` + guid + `","state":"canceled"}`)})
	if receipt := waitDownloadState(t, r, guid, AgentBrowserDownloadCompleted); receipt.SHA256 == "" {
		t.Fatalf("completed receipt lost its digest: %+v", receipt)
	}
}

func TestAgentBrowserDownloadBudgetRejectsOversize(t *testing.T) {
	var mu sync.Mutex
	var canceled []string
	r := downloadTestRuntime(t, func(request cdpWireMessage) *cdpWireMessage {
		if request.Method == "Browser.cancelDownload" {
			var params struct {
				GUID string `json:"guid"`
			}
			_ = json.Unmarshal(request.Params, &params)
			mu.Lock()
			canceled = append(canceled, params.GUID)
			mu.Unlock()
		}
		return downloadTestReply(request)
	}, nil)
	r.session = "session-1"
	r.browserContextID = "ctx-1"
	r.downloadPath = t.TempDir()
	r.downloads = map[string]*AgentBrowserDownloadReceipt{}
	guid := "0f1e2d3c-4b5a-4647-8879-0a1b2c3d4e5f"
	capture := filepath.Join(r.downloadPath, guid)
	if err := os.WriteFile(capture, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
		`{"guid":"` + guid + `","url":"https://example.com/huge.bin"}`)})
	// A known total size above the budget rejects before more bytes land.
	r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(fmt.Sprintf(
		`{"guid":"%s","state":"inProgress","receivedBytes":1024,"totalBytes":%d}`, guid, AgentBrowserMaxDownloadBytes+1))})
	if receipt := waitDownloadState(t, r, guid, AgentBrowserDownloadRejected); receipt.Bytes != 1024 {
		t.Fatalf("rejected receipt mismatch: %+v", receipt)
	}
	if _, err := os.Stat(capture); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("over-budget capture survived: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(canceled)
		mu.Unlock()
		if count > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(canceled) != 1 || canceled[0] != guid {
		t.Fatalf("over-budget download was not cancelled at the browser: %v", canceled)
	}
}

func TestAgentBrowserDownloadReceiptsAreBounded(t *testing.T) {
	r := downloadEventRuntime(t)
	for index := 0; index < AgentBrowserMaxDownloadReceipts; index++ {
		guid := fmt.Sprintf("00000000-0000-4000-8000-%012x", index)
		r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
			`{"guid":"` + guid + `"}`)})
	}
	if len(r.Status().Downloads) != AgentBrowserMaxDownloadReceipts {
		t.Fatalf("unexpected receipt count: %d", len(r.Status().Downloads))
	}
	// One active slot over the bound fails closed for the new download.
	extra := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
		`{"guid":"` + extra + `"}`)})
	status := r.Status()
	if len(status.Downloads) != AgentBrowserMaxDownloadReceipts {
		t.Fatalf("bound exceeded: %d", len(status.Downloads))
	}
	for _, receipt := range status.Downloads {
		if receipt.GUID == extra {
			t.Fatal("untracked download gained a receipt")
		}
	}
	// Completing one capture frees its slot for the next download.
	if err := os.WriteFile(filepath.Join(r.downloadPath, status.Downloads[0].GUID), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(
		`{"guid":"` + status.Downloads[0].GUID + `","state":"completed","receivedBytes":1}`)})
	waitDownloadState(t, r, status.Downloads[0].GUID, AgentBrowserDownloadCompleted)
	r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
		`{"guid":"` + extra + `"}`)})
	if receipt := waitDownloadState(t, r, extra, AgentBrowserDownloadActive); receipt.GUID != extra {
		t.Fatalf("freed slot not reused: %+v", receipt)
	}
}

// writeDownloadCapture writes one GUID-named capture into the quarantine.
func writeDownloadCapture(t *testing.T, r *AgentBrowserRuntime, guid string, size int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.downloadPath, guid), make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

// downloadQuarantineBytes sums the bytes the quarantine directory holds.
func downloadQuarantineBytes(t *testing.T, downloadPath string) int64 {
	t.Helper()
	entries, err := os.ReadDir(downloadPath)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total
}

// waitDownloadCanceled waits for the browser-side cancel of one GUID.
func waitDownloadCanceled(t *testing.T, mu *sync.Mutex, canceled *[]string, guid string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		found := false
		for _, entry := range *canceled {
			if entry == guid {
				found = true
				break
			}
		}
		mu.Unlock()
		if found {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("download %s was not cancelled at the browser: %v", guid, *canceled)
}

// fillDownloadSessionBudget completes enough maximum-size captures to fill
// the session download budget exactly.
func fillDownloadSessionBudget(t *testing.T, r *AgentBrowserRuntime) {
	t.Helper()
	for index := 0; index < AgentBrowserMaxDownloadTotalBytes/AgentBrowserMaxDownloadBytes; index++ {
		guid := fmt.Sprintf("00000000-0000-4000-8000-%012x", index)
		writeDownloadCapture(t, r, guid, AgentBrowserMaxDownloadBytes)
		r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
			`{"guid":"` + guid + `"}`)})
		r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(fmt.Sprintf(
			`{"guid":"%s","state":"completed","receivedBytes":%d}`, guid, AgentBrowserMaxDownloadBytes))})
		waitDownloadState(t, r, guid, AgentBrowserDownloadCompleted)
	}
}

// TestAgentBrowserDownloadSessionBudgetBoundsConsecutiveDownloads drives a
// sequence of completed captures that fills the session budget and asserts
// that the next download fails closed instead of growing the quarantine.
func TestAgentBrowserDownloadSessionBudgetBoundsConsecutiveDownloads(t *testing.T) {
	var mu sync.Mutex
	var canceled []string
	r := downloadTestRuntime(t, func(request cdpWireMessage) *cdpWireMessage {
		if request.Method == "Browser.cancelDownload" {
			var params struct {
				GUID string `json:"guid"`
			}
			_ = json.Unmarshal(request.Params, &params)
			mu.Lock()
			canceled = append(canceled, params.GUID)
			mu.Unlock()
		}
		return downloadTestReply(request)
	}, nil)
	r.session = "session-1"
	r.browserContextID = "ctx-1"
	r.downloadPath = t.TempDir()
	r.downloads = map[string]*AgentBrowserDownloadReceipt{}
	fillDownloadSessionBudget(t, r)
	if held := downloadQuarantineBytes(t, r.downloadPath); held != AgentBrowserMaxDownloadTotalBytes {
		t.Fatalf("quarantine holds %d bytes, expected the session budget %d",
			held, AgentBrowserMaxDownloadTotalBytes)
	}

	// One more maximum-size capture would push the session past its budget.
	over := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	writeDownloadCapture(t, r, over, AgentBrowserMaxDownloadBytes)
	r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
		`{"guid":"` + over + `"}`)})
	r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(fmt.Sprintf(
		`{"guid":"%s","state":"inProgress","receivedBytes":%d}`, over, AgentBrowserMaxDownloadBytes))})
	receipt := waitDownloadState(t, r, over, AgentBrowserDownloadRejected)
	if receipt.Bytes != AgentBrowserMaxDownloadBytes {
		t.Fatalf("rejected receipt mismatch: %+v", receipt)
	}
	if _, err := os.Stat(filepath.Join(r.downloadPath, over)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("over-budget capture survived in the quarantine: %v", err)
	}
	waitDownloadCanceled(t, &mu, &canceled, over)
	if held := downloadQuarantineBytes(t, r.downloadPath); held > AgentBrowserMaxDownloadTotalBytes {
		t.Fatalf("quarantine grew past the session budget: %d bytes", held)
	}
}

// TestAgentBrowserDownloadExhaustedBudgetKeepsFailingClosed asserts that a
// session whose budget is exhausted refuses every further download instead
// of quietly accepting more bytes.
func TestAgentBrowserDownloadExhaustedBudgetKeepsFailingClosed(t *testing.T) {
	r := downloadEventRuntime(t)
	fillDownloadSessionBudget(t, r)
	for index := 0; index < 2; index++ {
		guid := fmt.Sprintf("ffffffff-ffff-4fff-8fff-%012x", index)
		writeDownloadCapture(t, r, guid, AgentBrowserMaxDownloadBytes)
		r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
			`{"guid":"` + guid + `"}`)})
		r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(fmt.Sprintf(
			`{"guid":"%s","state":"inProgress","receivedBytes":%d}`, guid, AgentBrowserMaxDownloadBytes))})
		waitDownloadState(t, r, guid, AgentBrowserDownloadRejected)
		if _, err := os.Stat(filepath.Join(r.downloadPath, guid)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejected capture %s survived in the quarantine: %v", guid, err)
		}
	}
	if held := downloadQuarantineBytes(t, r.downloadPath); held > AgentBrowserMaxDownloadTotalBytes {
		t.Fatalf("quarantine grew past the session budget: %d bytes", held)
	}
}

// TestAgentBrowserDownloadEvictionRemovesCaptureFromQuarantine asserts that an
// evicted terminal receipt does not leave its capture behind in the
// quarantine and that the session accepts new downloads again afterwards.
func TestAgentBrowserDownloadEvictionRemovesCaptureFromQuarantine(t *testing.T) {
	r := downloadEventRuntime(t)
	guids := make([]string, 0, AgentBrowserMaxDownloadReceipts)
	for index := 0; index < AgentBrowserMaxDownloadReceipts; index++ {
		guid := fmt.Sprintf("00000000-0000-4000-8000-%012x", index)
		writeDownloadCapture(t, r, guid, 64)
		r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
			`{"guid":"` + guid + `"}`)})
		r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(fmt.Sprintf(
			`{"guid":"%s","state":"completed","receivedBytes":64}`, guid))})
		waitDownloadState(t, r, guid, AgentBrowserDownloadCompleted)
		guids = append(guids, guid)
	}
	if len(r.Status().Downloads) != AgentBrowserMaxDownloadReceipts {
		t.Fatalf("unexpected receipt count: %d", len(r.Status().Downloads))
	}

	// The receipt table is full of terminal receipts, so the next download
	// evicts them to free a slot.
	extra := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
		`{"guid":"` + extra + `"}`)})
	for _, guid := range guids {
		if _, err := os.Stat(filepath.Join(r.downloadPath, guid)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("evicted capture %s survived in the quarantine: %v", guid, err)
		}
	}
	status := r.Status()
	if len(status.Downloads) != 1 || status.Downloads[0].GUID != extra {
		t.Fatalf("eviction did not free the receipt slots: %+v", status.Downloads)
	}
	if receipt := waitDownloadState(t, r, extra, AgentBrowserDownloadActive); receipt.GUID != extra {
		t.Fatalf("session refused a download after the eviction: %+v", receipt)
	}
	// The evicted bytes no longer count against the session budget.
	r.mu.Lock()
	accounted := r.downloadBytesLocked()
	r.mu.Unlock()
	if accounted != 0 {
		t.Fatalf("evicted bytes still counted against the session budget: %d", accounted)
	}
	if held := downloadQuarantineBytes(t, r.downloadPath); held != 0 {
		t.Fatalf("quarantine still holds %d bytes after the eviction", held)
	}
}

func TestAgentBrowserDownloadSuggestedFilenameNeverBecomesAPath(t *testing.T) {
	r := downloadEventRuntime(t)
	guid := "0f1e2d3c-4b5a-4647-8879-0a1b2c3d4e5f"
	r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
		`{"guid":"` + guid + `","suggestedFilename":"..\\..\\evil.exe","url":"https://example.com/x"}`)})
	receipts := r.Status().Downloads
	if len(receipts) != 1 {
		t.Fatalf("receipts: %+v", receipts)
	}
	if strings.Contains(receipts[0].SuggestedFilename, "..") || strings.Contains(receipts[0].SuggestedFilename, "\\") {
		t.Fatalf("suggested filename kept separator characters: %q", receipts[0].SuggestedFilename)
	}
	// The capture is only ever read back at the GUID-named path.
	if err := os.WriteFile(filepath.Join(r.downloadPath, guid), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(
		`{"guid":"` + guid + `","state":"completed","receivedBytes":7,"filePath":"C:\\Windows\\evil"}`)})
	receipt := waitDownloadState(t, r, guid, AgentBrowserDownloadCompleted)
	sum := sha256.Sum256([]byte("payload"))
	if receipt.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest came from a path other than the quarantine: %s", receipt.SHA256)
	}
}

// TestAgentBrowserRealDownloadQuarantine requires the opt-in real installed
// browser (TRAVERSE_TEST_AGENT_BROWSER=1): it captures a real click-initiated
// download end to end and verifies the GUID-named quarantine capture.
func TestAgentBrowserRealDownloadQuarantine(t *testing.T) {
	r, _ := agentBrowserTestRuntime(t)
	const payload = "agent browser quarantine capture"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/file.bin" {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", `attachment; filename="quarantine-proof.bin"`)
			_, _ = w.Write([]byte(payload))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Quarantine</title><a href="/file.bin" aria-label="Download proof">Download proof</a>`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := r.Navigate(ctx, server.URL); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var linkRef string
	for _, element := range snapshot.Elements {
		if element.Name == "Download proof" {
			linkRef = element.Ref
		}
	}
	if linkRef == "" {
		t.Fatalf("download link missing from snapshot: %+v", snapshot.Elements)
	}
	if _, err := r.Click(ctx, snapshot.SnapshotID, linkRef); err != nil {
		t.Fatalf("click: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, receipt := range r.Status().Downloads {
			if receipt.State != AgentBrowserDownloadCompleted {
				continue
			}
			sum := sha256.Sum256([]byte(payload))
			if receipt.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatalf("digest mismatch: %+v", receipt)
			}
			if receipt.SuggestedFilename != "quarantine-proof.bin" || receipt.Bytes != int64(len(payload)) {
				t.Fatalf("receipt mismatch: %+v", receipt)
			}
			capture := filepath.Join(r.profile.path, "downloads", receipt.GUID)
			if info, err := os.Stat(capture); err != nil || !info.Mode().IsRegular() {
				t.Fatalf("capture missing from quarantine: %v", err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("download never completed: %+v", r.Status())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestAgentBrowserDownloadTerminalStatesNeverRegress(t *testing.T) {
	r := downloadEventRuntime(t)
	guid := "0f1e2d3c-4b5a-4647-8879-0a1b2c3d4e5f"
	r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
		`{"guid":"` + guid + `","url":"https://example.com/big.bin"}`)})
	r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(fmt.Sprintf(
		`{"guid":"%s","state":"inProgress","receivedBytes":%d}`, guid, AgentBrowserMaxDownloadBytes+1))})
	if receipt := waitDownloadState(t, r, guid, AgentBrowserDownloadRejected); receipt.SHA256 != "" {
		t.Fatalf("rejected receipt carries a digest: %+v", receipt)
	}
	// The operator-initiated cancel arrives as a plain canceled event and must
	// not overwrite the security-relevant rejected_budget state.
	r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(
		`{"guid":"` + guid + `","state":"canceled"}`)})
	r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(
		`{"guid":"` + guid + `","state":"inProgress","receivedBytes":1}`)})
	if receipt := waitDownloadState(t, r, guid, AgentBrowserDownloadRejected); receipt.SHA256 != "" {
		t.Fatalf("terminal rejected state regressed: %+v", receipt)
	}

	// A completed capture is idempotent: a duplicate completed event keeps the
	// digest instead of regressing to unverified.
	r2 := downloadEventRuntime(t)
	guid2 := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeffff0001"
	if err := os.WriteFile(filepath.Join(r2.downloadPath, guid2), []byte("stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	r2.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
		`{"guid":"` + guid2 + `"}`)})
	r2.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(
		`{"guid":"` + guid2 + `","state":"completed","receivedBytes":6}`)})
	first := waitDownloadState(t, r2, guid2, AgentBrowserDownloadCompleted)
	r2.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(
		`{"guid":"` + guid2 + `","state":"completed","receivedBytes":6}`)})
	if receipt := waitDownloadState(t, r2, guid2, AgentBrowserDownloadCompleted); receipt.SHA256 != first.SHA256 {
		t.Fatalf("duplicate completed event regressed the digest: %+v vs %+v", receipt, first)
	}
}

func TestAgentBrowserDownloadGUIDIsAUUID(t *testing.T) {
	for _, guid := range []string{"0f1e2d3c-4b5a-4647-8879-0a1b2c3d4e5f", "aaaaaaaa-bbbb-4ccc-8ddd-eeeeffff0001"} {
		if !validDownloadGUID(guid) {
			t.Fatalf("valid GUID rejected: %s", guid)
		}
	}
	for _, guid := range []string{
		"../../etc/passwd\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00",
		"gggggggg-gggg-gggg-gggg-gggggggggggg",
		"0F1E2D3C-4B5A-4647-8879-0A1B2C3D4E5F",
		"0f1e2d3c4b5a464788790a1b2c3d4e5f0000",
		"0f1e2d3c-4b5a-4647-8879-0a1b2c3d4e5f ",
		"", "..", "0f1e2d3c-4b5a-4647-8879-0a1b2c3d4e5",
	} {
		if validDownloadGUID(guid) {
			t.Fatalf("invalid GUID accepted: %q", guid)
		}
	}
}

func TestAgentBrowserDownloadBudgetMatchesBuiltinProfileLimits(t *testing.T) {
	safe, ok := BuiltinRegistry().Lookup(ProfileSafeWeb)
	if !ok {
		t.Fatal("builtin safe-web profile missing")
	}
	if safe.Limits.MaxDownloadBytes != AgentBrowserMaxDownloadBytes {
		t.Fatalf("download budget drifted: runtime=%d profile=%d",
			AgentBrowserMaxDownloadBytes, safe.Limits.MaxDownloadBytes)
	}
}

// TestAgentBrowserDownloadTotalBudgetDerivation pins the derivation of the
// session budget: the builtin profile exposes no session-level download
// limit, so the total is derived as an exact multiple of the per-file cap
// instead of being mirrored from the profile.
func TestAgentBrowserDownloadTotalBudgetDerivation(t *testing.T) {
	if AgentBrowserMaxDownloadTotalBytes <= 0 ||
		AgentBrowserMaxDownloadTotalBytes%AgentBrowserMaxDownloadBytes != 0 {
		t.Fatalf("session budget is not an exact multiple of the per-file cap: %d",
			AgentBrowserMaxDownloadTotalBytes)
	}
}

func TestAgentBrowserReadDownloadVerifiesDigestAndLifetime(t *testing.T) {
	r := downloadEventRuntime(t)
	guid := "0f1e2d3c-4b5a-4647-8879-0a1b2c3d4e5f"
	content := []byte("quarantined release probe")
	if _, _, err := r.ReadDownload(context.Background(), guid); err == nil {
		t.Fatal("unknown GUID released")
	}
	r.onEvent(cdpWireMessage{Method: "Browser.downloadWillBegin", Params: json.RawMessage(
		`{"guid":"` + guid + `"}`)})
	if _, _, err := r.ReadDownload(context.Background(), guid); err == nil {
		t.Fatal("active download released")
	}
	if err := os.WriteFile(filepath.Join(r.downloadPath, guid), content, 0o600); err != nil {
		t.Fatal(err)
	}
	r.onEvent(cdpWireMessage{Method: "Browser.downloadProgress", Params: json.RawMessage(
		`{"guid":"` + guid + `","state":"completed","receivedBytes":25}`)})
	if receipt := waitDownloadState(t, r, guid, AgentBrowserDownloadCompleted); receipt.SHA256 == "" {
		t.Fatalf("receipt: %+v", receipt)
	}
	data, receipt, err := r.ReadDownload(context.Background(), guid)
	if err != nil || string(data) != string(content) || receipt.State != AgentBrowserDownloadCompleted {
		t.Fatalf("read: %v %+v", err, receipt)
	}
	// Tampered bytes must never leave the quarantine.
	if err := os.WriteFile(filepath.Join(r.downloadPath, guid), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ReadDownload(context.Background(), guid); err == nil {
		t.Fatal("tampered capture released")
	}
	// A capture that the session cleanup already removed reads as gone, not
	// as a filesystem error carrying the quarantine path.
	if err := os.Remove(filepath.Join(r.downloadPath, guid)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.ReadDownload(context.Background(), guid); !errors.Is(err, ErrAgentBrowserDownloadGone) {
		t.Fatalf("missing capture: %v", err)
	}
	// A GUID that is not a canonical UUID never reaches the filesystem.
	if _, _, err := r.ReadDownload(context.Background(), "0f1e2d3c4b5a464788790a1b2c3d4e5f0000"); err == nil {
		t.Fatal("non-UUID GUID released")
	}
}
