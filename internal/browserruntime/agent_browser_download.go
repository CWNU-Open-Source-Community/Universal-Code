package browserruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// Downloads are bytes a page pushes at the browser while the agent navigates.
// Capture is deliberately narrow: Chromium names every file by its download
// GUID inside the session's disposable profile tree (allowAndName), so a
// Content-Disposition filename can never place or name a file; event-provided
// paths are never trusted; a per-file cap and a session-wide byte budget are
// both enforced against the live progress stream and oversized captures are
// dropped. The quarantine dies with the profile at cleanup, receipts are
// bounded operator metadata, and nothing in this layer releases bytes to the
// workspace or to a tool result.
const (
	// AgentBrowserMaxDownloadBytes mirrors the builtin profile
	// ProfileLimits.MaxDownloadBytes for the agent browser runtime, which does
	// not carry a profile descriptor; a pinning test keeps the values equal.
	AgentBrowserMaxDownloadBytes = 16 << 20
	// AgentBrowserMaxDownloadTotalBytes bounds the bytes one session may keep
	// in its download quarantine. The per-file cap alone does not bound the
	// session: a page can complete many sub-cap downloads, so the quarantine
	// needs its own ceiling.
	//
	// The value is an exact multiple of AgentBrowserMaxDownloadBytes
	// (4 x 16 MiB = 64 MiB). It is derived, not mirrored: the builtin profile
	// exposes only a per-file limit (ProfileLimits.MaxDownloadBytes) and no
	// session-level download limit, so there is nothing to pin against here.
	// The receipt table alone would allow 16 captures x 16 MiB = 256 MiB to
	// sit in the session's temporary storage, which is exactly why the
	// budget is a smaller multiple: it stays observable (a fifth maximum-size
	// capture is refused) while still leaving room for several ordinary
	// captures, and a single ordinary capture is never affected.
	AgentBrowserMaxDownloadTotalBytes = 4 * AgentBrowserMaxDownloadBytes
	// AgentBrowserMaxDownloadReceipts bounds the receipt list. Terminal
	// receipts are evicted first; when every slot holds an active capture the
	// new download is cancelled and left untracked instead of dropping
	// active tracking.
	AgentBrowserMaxDownloadReceipts  = 16
	AgentBrowserMaxDownloadURLBytes  = 2048
	AgentBrowserMaxDownloadNameBytes = 256
)

const (
	AgentBrowserDownloadActive     = "active"
	AgentBrowserDownloadCompleted  = "completed"
	AgentBrowserDownloadCanceled   = "canceled"
	AgentBrowserDownloadRejected   = "rejected_budget"
	AgentBrowserDownloadUnverified = "unverified"
)

// AgentBrowserDownloadReceipt is bounded, non-authorizing metadata about one
// captured download. SHA256 is set only after the completed GUID-named capture
// has been hashed; a receipt keeps its terminal state once reached, so a
// rejected capture can never regress into a plain cancellation.
type AgentBrowserDownloadReceipt struct {
	GUID              string    `json:"guid"`
	SuggestedFilename string    `json:"suggested_filename,omitempty"`
	URL               string    `json:"url,omitempty"`
	State             string    `json:"state"`
	Bytes             int64     `json:"bytes,omitempty"`
	SHA256            string    `json:"sha256,omitempty"`
	StartedAt         time.Time `json:"started_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (r *AgentBrowserRuntime) startDownloadQuarantine(ctx context.Context) error {
	downloadPath, err := prepareAgentBrowserDownloadQuarantine(r.profile.path)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.downloadPath = downloadPath
	r.mu.Unlock()
	if err := r.cdp.call(ctx, "", "Browser.setDownloadBehavior", map[string]any{
		"behavior": "allowAndName", "downloadPath": downloadPath,
		"eventsEnabled": true, "browserContextId": r.browserContextID,
	}, nil); err != nil {
		return err
	}
	return nil
}

// handleDownloadEventLocked runs on the CDP reader while r.mu is held for a
// browser-level (or, defensively, target-session) Browser.download* event. It
// always consumes the event: a download event must never fall through into the
// target-event switch, no matter how malformed its payload is.
func (r *AgentBrowserRuntime) handleDownloadEventLocked(event cdpWireMessage) {
	now := time.Now().UTC()
	var params struct {
		GUID              string `json:"guid"`
		URL               string `json:"url"`
		SuggestedFilename string `json:"suggestedFilename"`
		State             string `json:"state"`
		ReceivedBytes     int64  `json:"receivedBytes"`
		TotalBytes        int64  `json:"totalBytes"`
	}
	if json.Unmarshal(event.Params, &params) != nil || !validDownloadGUID(params.GUID) {
		return
	}
	switch event.Method {
	case "Browser.downloadWillBegin":
		r.beginDownloadLocked(params.GUID, boundedDownloadMetadata(params.URL, AgentBrowserMaxDownloadURLBytes),
			boundedDownloadFilename(params.SuggestedFilename, AgentBrowserMaxDownloadNameBytes), now)
	case "Browser.downloadProgress":
		receipt := r.downloadReceiptLocked(params.GUID)
		if receipt == nil {
			return
		}
		switch params.State {
		case "inProgress":
			// Late progress after a terminal event must not resurrect a
			// completed, cancelled, or rejected capture.
			if receipt.State != AgentBrowserDownloadActive {
				return
			}
			receipt.UpdatedAt = now
			receipt.Bytes = params.ReceivedBytes
			// The session budget is enforced against the same live progress
			// stream as the per-file cap: a capture that would push the
			// quarantine past its ceiling is refused fail-closed.
			if params.ReceivedBytes > AgentBrowserMaxDownloadBytes ||
				(params.TotalBytes > 0 && params.TotalBytes > AgentBrowserMaxDownloadBytes) ||
				r.downloadBytesLocked() > AgentBrowserMaxDownloadTotalBytes {
				r.rejectDownloadLocked(params.GUID, now)
			}
		case "completed":
			if receipt.State != AgentBrowserDownloadActive {
				return
			}
			receipt.UpdatedAt = now
			receipt.Bytes = params.ReceivedBytes
			if params.ReceivedBytes > AgentBrowserMaxDownloadBytes ||
				r.downloadBytesLocked() > AgentBrowserMaxDownloadTotalBytes {
				r.rejectDownloadLocked(params.GUID, now)
				return
			}
			receipt.State = AgentBrowserDownloadUnverified
			go r.hashCompletedDownload(params.GUID)
		case "canceled":
			// rejected_budget is terminal and security-relevant: Chromium
			// deterministically reports the operator-initiated cancel as a
			// plain cancellation, which must not overwrite it.
			if receipt.State == AgentBrowserDownloadActive || receipt.State == AgentBrowserDownloadUnverified {
				receipt.State = AgentBrowserDownloadCanceled
				receipt.UpdatedAt = now
			}
		}
	}
}

// validDownloadGUID accepts only Chromium's canonical 36-character lowercase
// download UUID (dashes at positions 8, 13, 18, 23). The GUID doubles as the
// capture file name inside the quarantine, so dot segments, separators, and
// Windows-reserved shapes never reach the filesystem helpers.
func validDownloadGUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		switch index {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			isHex := (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')
			if !isHex {
				return false
			}
		}
	}
	return true
}

func (r *AgentBrowserRuntime) beginDownloadLocked(guid, url, suggestedFilename string, now time.Time) {
	if r.downloads == nil {
		r.downloads = map[string]*AgentBrowserDownloadReceipt{}
	}
	if existing := r.downloadReceiptLocked(guid); existing != nil {
		existing.UpdatedAt = now
		return
	}
	if len(r.downloadOrder) >= AgentBrowserMaxDownloadReceipts {
		// Evict terminal receipts first; a full list of active captures fails
		// closed for the new download instead of dropping active tracking.
		// An evicted capture leaves the quarantine with its receipt: its file
		// is removed best-effort and its bytes stop counting against the
		// session budget, so the quarantine cannot grow without bound. An
		// evicted capture can no longer be released through the downstream
		// release flow (no receipt survives to name it), and its bytes die
		// with the session profile.
		kept := r.downloadOrder[:0]
		for _, candidate := range r.downloadOrder {
			if receipt := r.downloads[candidate]; receipt != nil && receipt.State == AgentBrowserDownloadActive {
				kept = append(kept, candidate)
				continue
			}
			r.removeDownloadCaptureLocked(candidate)
			delete(r.downloads, candidate)
		}
		r.downloadOrder = kept
	}
	if len(r.downloadOrder) >= AgentBrowserMaxDownloadReceipts {
		go r.cancelDownload(guid)
		return
	}
	r.downloads[guid] = &AgentBrowserDownloadReceipt{GUID: guid, SuggestedFilename: suggestedFilename,
		URL: url, State: AgentBrowserDownloadActive, StartedAt: now, UpdatedAt: now}
	r.downloadOrder = append(r.downloadOrder, guid)
}

func (r *AgentBrowserRuntime) downloadReceiptLocked(guid string) *AgentBrowserDownloadReceipt {
	return r.downloads[guid]
}

// downloadBytesLocked sums the bytes the session quarantine is accounted
// for. A tracked receipt may still own a file inside the quarantine (an
// active capture is still growing, a cancelled one may have left a partial
// file), so its bytes count until the receipt is evicted; a rejected capture
// is excluded because rejection removes its file and discards the bytes. A
// rejected capture whose removal failed on Windows leaves a residual file
// that is not accounted for; it dies with the session profile at cleanup.
func (r *AgentBrowserRuntime) downloadBytesLocked() int64 {
	var total int64
	for _, receipt := range r.downloads {
		if receipt == nil || receipt.State == AgentBrowserDownloadRejected {
			continue
		}
		total += receipt.Bytes
	}
	return total
}

// removeDownloadCaptureLocked best-effort removes one capture from the
// quarantine. Chromium may still hold the capture handle on Windows, so a
// residual file is acceptable and dies with the profile at cleanup.
func (r *AgentBrowserRuntime) removeDownloadCaptureLocked(guid string) {
	if r.downloadPath == "" {
		return
	}
	_ = removeAgentBrowserDownloadCapture(r.downloadPath, guid)
}

// rejectDownloadLocked marks the capture over budget and best-effort removes
// the bytes that already reached the quarantine; Chromium may still hold the
// capture handle on Windows, so a residual file is acceptable and dies with
// the profile at cleanup. The browser-side download is cancelled from a
// worker; a failing cancel kills the session instead of letting the stream
// continue.
func (r *AgentBrowserRuntime) rejectDownloadLocked(guid string, now time.Time) {
	receipt := r.downloadReceiptLocked(guid)
	if receipt == nil {
		return
	}
	receipt.State = AgentBrowserDownloadRejected
	receipt.UpdatedAt = now
	r.removeDownloadCaptureLocked(guid)
	go r.cancelDownload(guid)
}

func (r *AgentBrowserRuntime) cancelDownload(guid string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.cdp.call(ctx, "", "Browser.cancelDownload", map[string]any{"guid": guid}, nil); err != nil && r.life.Err() == nil {
		go r.cancel()
	}
}

// hashCompletedDownload digests the GUID-named capture inside the quarantine.
// It never follows an event-provided path: allowAndName fixed the name, so any
// missing, non-regular, or oversized capture stays unverified instead. The
// read races benignly with cleanup: losing it keeps the receipt unverified,
// and a cleanup that trips over the open capture is retryable by design.
func (r *AgentBrowserRuntime) hashCompletedDownload(guid string) {
	r.mu.Lock()
	downloadPath := r.downloadPath
	r.mu.Unlock()
	digest := ""
	if info, err := statAgentBrowserDownloadCapture(downloadPath, guid); err == nil && info.Mode().IsRegular() &&
		info.Size() <= AgentBrowserMaxDownloadBytes {
		if data, readErr := readAgentBrowserDownloadCapture(downloadPath, guid); readErr == nil {
			sum := sha256.Sum256(data)
			digest = hex.EncodeToString(sum[:])
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	receipt := r.downloadReceiptLocked(guid)
	if receipt == nil || receipt.State != AgentBrowserDownloadUnverified {
		return
	}
	if digest == "" {
		receipt.State = AgentBrowserDownloadUnverified
		return
	}
	receipt.SHA256 = digest
	receipt.State = AgentBrowserDownloadCompleted
	receipt.UpdatedAt = time.Now().UTC()
}

func (r *AgentBrowserRuntime) downloadReceiptsLocked() []AgentBrowserDownloadReceipt {
	if len(r.downloadOrder) == 0 {
		return nil
	}
	receipts := make([]AgentBrowserDownloadReceipt, 0, len(r.downloadOrder))
	for _, guid := range r.downloadOrder {
		if receipt := r.downloads[guid]; receipt != nil {
			receipts = append(receipts, *receipt)
		}
	}
	return receipts
}

func boundedDownloadMetadata(value string, limit int) string {
	if len(value) > limit {
		value = value[:limit]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
}

// boundedDownloadFilename keeps only the basename of the page-controlled
// suggestion: the receipt is operator metadata, and a suggestion that looks
// like a path must not survive into surfaces that might display or reuse it.
func boundedDownloadFilename(value string, limit int) string {
	if index := strings.LastIndexAny(value, `/\`); index >= 0 {
		value = value[index+1:]
	}
	return boundedDownloadMetadata(value, limit)
}
