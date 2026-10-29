package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/browserruntime"
)

// agentBrowserDownloadReader is the narrow runtime surface for the operator
// download release. The real runtime implements it; test doubles may not.
type agentBrowserDownloadReader interface {
	ReadDownload(context.Context, string) ([]byte, browserruntime.AgentBrowserDownloadReceipt, error)
}

const AgentBrowserDownloadReleaseVersion = "agent_browser_download_release.v1"
const fullCDPDownloadArtifactDirectory = "downloads"
const maxAgentBrowserReleaseOperatorRunes = 128

// BrowserDownloads returns the bounded quarantine receipts of the run's live
// browser session. Receipts are metadata only: no path ever leaves the
// runtime, and the bytes themselves stay in the disposable profile quarantine.
func (s *AgentBrowserService) BrowserDownloads(ctx context.Context, runID string) ([]browserruntime.AgentBrowserDownloadReceipt, error) {
	if s == nil || s.store == nil {
		return nil, agentBrowserUnavailable("Agent browser unavailable")
	}
	if _, e := s.store.GetRun(ctx, runID); e != nil {
		return nil, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	slot := s.slots[runID]
	if slot == nil || slot.closed || slot.runtime == nil {
		return []browserruntime.AgentBrowserDownloadReceipt{}, nil
	}
	receipts := slot.runtime.Status().Downloads
	if receipts == nil {
		receipts = []browserruntime.AgentBrowserDownloadReceipt{}
	}
	return receipts, nil
}

// ReleaseBrowserDownload copies one completed quarantine capture into the
// run's workspace artifacts beside a provenance sidecar. The operator release
// call is itself the recorded approval: it is authenticated against the
// control plane, bound to the exact run and GUID, and the sidecar carries the
// operator identity beside the recomputed capture digest. The capture dies
// with the session profile, so a release must happen while the session is
// open. Repeating a release with identical bytes is idempotent.
func (s *AgentBrowserService) ReleaseBrowserDownload(ctx context.Context, runID, guid, operator string) (string, browserruntime.AgentBrowserDownloadReceipt, error) {
	if s == nil || s.store == nil {
		return "", browserruntime.AgentBrowserDownloadReceipt{}, agentBrowserUnavailable("Agent browser unavailable")
	}
	operator = strings.TrimSpace(operator)
	if operator == "" {
		operator = "httpapi_operator"
	}
	if !utf8.ValidString(operator) || utf8.RuneCountInString(operator) > maxAgentBrowserReleaseOperatorRunes || strings.ContainsFunc(operator, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", browserruntime.AgentBrowserDownloadReceipt{}, agentBrowserUnavailable("release operator identity is invalid")
	}
	if _, e := s.store.GetRun(ctx, runID); e != nil {
		return "", browserruntime.AgentBrowserDownloadReceipt{}, e
	}
	s.mu.Lock()
	slot := s.slots[runID]
	if slot == nil || slot.closed || slot.runtime == nil {
		s.mu.Unlock()
		return "", browserruntime.AgentBrowserDownloadReceipt{}, agentBrowserUnavailable("Agent browser session is not live")
	}
	runtime := slot.runtime
	workspaceID := slot.authority.WorkspaceID
	s.mu.Unlock()
	reader, ok := runtime.(agentBrowserDownloadReader)
	if !ok {
		return "", browserruntime.AgentBrowserDownloadReceipt{}, agentBrowserUnavailable("browser download release is unavailable")
	}
	data, receipt, err := reader.ReadDownload(ctx, guid)
	if err != nil {
		if errors.Is(err, browserruntime.ErrAgentBrowserDownloadUnknown) || errors.Is(err, browserruntime.ErrBrowserRuntimeBoundary) {
			return "", browserruntime.AgentBrowserDownloadReceipt{}, agentBrowserUnavailable("browser download is unknown for this session")
		}
		if errors.Is(err, browserruntime.ErrAgentBrowserDownloadGone) {
			return "", browserruntime.AgentBrowserDownloadReceipt{}, agentBrowserUnavailable("browser download capture is gone with the session quarantine")
		}
		return "", browserruntime.AgentBrowserDownloadReceipt{}, err
	}
	locator, err := s.persistAgentBrowserDownload(ctx, workspaceID, runID, data, receipt)
	if err != nil {
		return "", browserruntime.AgentBrowserDownloadReceipt{}, err
	}
	return locator, receipt, nil
}

// persistAgentBrowserDownload writes the capture and its provenance sidecar
// under the workspace browser-artifact tree. The GUID is the only file name on
// disk; the page-controlled suggestion stays inside the sidecar metadata.
func (s *AgentBrowserService) persistAgentBrowserDownload(ctx context.Context, workspaceID, runID string, data []byte, receipt browserruntime.AgentBrowserDownloadReceipt) (string, error) {
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != receipt.SHA256 || int64(len(data)) != receipt.Bytes {
		return "", agentBrowserUnavailable("browser download integrity mismatch")
	}
	st, ok := s.store.(fullCDPWorkspaceInfoStore)
	if !ok {
		return "", agentBrowserUnavailable("browser download Workspace unavailable")
	}
	workspace, e := st.GetWorkspaceInfo(ctx, workspaceID)
	if e != nil {
		return "", e
	}
	if workspace.ID != workspaceID || !filepath.IsAbs(workspace.RootPath) {
		return "", agentBrowserUnavailable("browser download Workspace mismatch")
	}
	root, e := os.OpenRoot(workspace.RootPath)
	if e != nil {
		return "", e
	}
	defer root.Close()
	relative := filepath.Join(fullCDPBrowserArtifactDirectory, fullCDPDownloadArtifactDirectory, runID, receipt.GUID)
	if e = root.MkdirAll(filepath.Dir(relative), 0o700); e != nil {
		return "", e
	}
	file, e := root.OpenFile(relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(e, os.ErrExist) {
		old, x := readBoundedFullCDPArtifact(root, relative, browserruntime.AgentBrowserMaxDownloadBytes)
		if x != nil || string(old) != string(data) {
			return "", agentBrowserUnavailable("browser artifact already contains different bytes")
		}
	} else if e != nil {
		return "", e
	} else if e = writeFullCDPArtifact(file, data); e != nil {
		return "", e
	}
	sidecarRelative := relative + ".json"
	sidecar, e := json.Marshal(map[string]any{
		"version":            AgentBrowserDownloadReleaseVersion,
		"guid":               receipt.GUID,
		"url":                receipt.URL,
		"suggested_filename": receipt.SuggestedFilename,
		"sha256":             receipt.SHA256,
		"bytes":              receipt.Bytes,
		"started_at":         receipt.StartedAt.UTC(),
		"captured_at":        receipt.UpdatedAt.UTC(),
		"run_id":             runID,
		"workspace_id":       workspaceID,
	})
	if e != nil || !utf8.Valid(sidecar) {
		return "", agentBrowserUnavailable("browser download provenance is invalid")
	}
	sidecarFile, e := root.OpenFile(sidecarRelative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(e, os.ErrExist) {
		old, x := readBoundedFullCDPArtifact(root, sidecarRelative, 16*1024)
		if x != nil || string(old) != string(sidecar) {
			return "", agentBrowserUnavailable("browser artifact already contains different bytes")
		}
	} else if e != nil {
		return "", e
	} else if e = writeFullCDPArtifact(sidecarFile, sidecar); e != nil {
		return "", e
	}
	return "workspace:///" + filepath.ToSlash(relative), nil
}
