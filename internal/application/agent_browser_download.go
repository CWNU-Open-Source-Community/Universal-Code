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
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
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

// AgentBrowserDownloadReleasePrincipalControlPlane names the principal that
// authorized a release. The control-plane authorization carries a bearer
// credential and no human identity, so this label identifies the authorizing
// credential class and never a person. It is decided server-side by the
// control plane and is never derived from the credential itself: no token,
// token hash, or reversible derivative of it is ever persisted.
const AgentBrowserDownloadReleasePrincipalControlPlane = "control_plane"

// AgentBrowserDownloadReleasePrincipalScopeCredentialClass marks an
// authenticated principal that names a credential class instead of a human
// identity. The approval event states this explicitly so a reader cannot
// mistake the authorizing principal for an operator account.
const AgentBrowserDownloadReleasePrincipalScopeCredentialClass = "credential_class"

// AgentBrowserDownloadRelease splits the release identity into the
// authenticated principal and the caller-declared operator name. The declared
// name arrives in the request body and proves nothing about who authorized
// the release; the principal is the server-side decision of the control
// plane, and the two are persisted as separate fields.
type AgentBrowserDownloadRelease struct {
	Principal string
	Operator  string
}

// agentBrowserDownloadReleaseApproval is the release-approval event written
// next to the released capture. It is deliberately separate from the
// deterministic capture sidecar: the sidecar describes which browser download
// the bytes came from, this event describes the approval that released them.
type agentBrowserDownloadReleaseApproval struct {
	Version          string    `json:"version"`
	Kind             string    `json:"kind"`
	GUID             string    `json:"guid"`
	RunID            string    `json:"run_id"`
	WorkspaceID      string    `json:"workspace_id"`
	SHA256           string    `json:"sha256"`
	Bytes            int64     `json:"bytes"`
	ApprovedAt       time.Time `json:"approved_at"`
	Principal        string    `json:"authorized_principal"`
	PrincipalScope   string    `json:"authorized_principal_scope"`
	DeclaredOperator string    `json:"declared_operator"`
}

// AgentBrowserDownloadApproval is the release-approval metadata a release
// actually left on disk. A repeat release reports the first approval — the
// identity and timestamp written into the approval event — instead of the
// caller who replayed the request, so a client that builds an audit trail from
// the response reads back the same values it is told.
type AgentBrowserDownloadApproval struct {
	DeclaredOperator string
	ApprovedAt       time.Time
}

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
// run's workspace artifacts beside a provenance sidecar and a release-approval
// event. The release call is itself the recorded approval: it is
// authenticated against the control plane, bound to the exact run and GUID,
// and the bytes are re-hashed before they are copied. The capture sidecar
// proves which browser download a file came from; the approval event beside
// it proves which principal approved the release and when. The capture dies
// with the session profile, so a release must happen while the session is
// open. Repeating a release with identical bytes is idempotent and keeps the
// first approval event untouched; the returned approval is the event that
// survives on disk, not the identity of the call that replayed the release.
func (s *AgentBrowserService) ReleaseBrowserDownload(ctx context.Context, runID, guid string,
	release AgentBrowserDownloadRelease,
) (string, browserruntime.AgentBrowserDownloadReceipt, AgentBrowserDownloadApproval, error) {
	if s == nil || s.store == nil {
		return "", browserruntime.AgentBrowserDownloadReceipt{}, AgentBrowserDownloadApproval{},
			agentBrowserUnavailable("Agent browser unavailable")
	}
	// Only the control-plane principal kind is accepted here: the authorizing
	// principal is a server-side decision, so a caller cannot declare it.
	principal := strings.TrimSpace(release.Principal)
	if principal != AgentBrowserDownloadReleasePrincipalControlPlane {
		return "", browserruntime.AgentBrowserDownloadReceipt{}, AgentBrowserDownloadApproval{},
			agentBrowserUnavailable("release principal is invalid")
	}
	operator := strings.TrimSpace(release.Operator)
	if operator == "" {
		operator = "httpapi_operator"
	}
	if !utf8.ValidString(operator) || utf8.RuneCountInString(operator) > maxAgentBrowserReleaseOperatorRunes || strings.ContainsFunc(operator, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", browserruntime.AgentBrowserDownloadReceipt{}, AgentBrowserDownloadApproval{},
			agentBrowserUnavailable("release operator identity is invalid")
	}
	release = AgentBrowserDownloadRelease{Principal: principal, Operator: operator}
	if _, e := s.store.GetRun(ctx, runID); e != nil {
		return "", browserruntime.AgentBrowserDownloadReceipt{}, AgentBrowserDownloadApproval{}, e
	}
	s.mu.Lock()
	slot := s.slots[runID]
	if slot == nil || slot.closed || slot.runtime == nil {
		s.mu.Unlock()
		return "", browserruntime.AgentBrowserDownloadReceipt{}, AgentBrowserDownloadApproval{},
			agentBrowserUnavailable("Agent browser session is not live")
	}
	runtime := slot.runtime
	workspaceID := slot.authority.WorkspaceID
	s.mu.Unlock()
	reader, ok := runtime.(agentBrowserDownloadReader)
	if !ok {
		return "", browserruntime.AgentBrowserDownloadReceipt{}, AgentBrowserDownloadApproval{},
			agentBrowserUnavailable("browser download release is unavailable")
	}
	data, receipt, err := reader.ReadDownload(ctx, guid)
	if err != nil {
		if errors.Is(err, browserruntime.ErrAgentBrowserDownloadUnknown) || errors.Is(err, browserruntime.ErrBrowserRuntimeBoundary) {
			return "", browserruntime.AgentBrowserDownloadReceipt{}, AgentBrowserDownloadApproval{},
				agentBrowserUnavailable("browser download is unknown for this session")
		}
		if errors.Is(err, browserruntime.ErrAgentBrowserDownloadGone) {
			return "", browserruntime.AgentBrowserDownloadReceipt{}, AgentBrowserDownloadApproval{},
				agentBrowserUnavailable("browser download capture is gone with the session quarantine")
		}
		return "", browserruntime.AgentBrowserDownloadReceipt{}, AgentBrowserDownloadApproval{}, err
	}
	locator, approval, err := s.persistAgentBrowserDownload(ctx, workspaceID, runID, data, receipt, release)
	if err != nil {
		return "", browserruntime.AgentBrowserDownloadReceipt{}, AgentBrowserDownloadApproval{}, err
	}
	return locator, receipt, approval, nil
}

// persistAgentBrowserDownload writes the capture, its provenance sidecar and
// the release-approval event under the workspace browser-artifact tree. The
// GUID is the only file name on disk; the page-controlled suggestion stays
// inside the sidecar metadata. The sidecar stays byte-identical across
// releases; the approval event is written once with an exclusive create and
// is never rewritten. The returned approval is the event left on disk, which
// for a repeat release is the first approval rather than this call's identity.
func (s *AgentBrowserService) persistAgentBrowserDownload(ctx context.Context, workspaceID, runID string, data []byte,
	receipt browserruntime.AgentBrowserDownloadReceipt, release AgentBrowserDownloadRelease,
) (string, AgentBrowserDownloadApproval, error) {
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != receipt.SHA256 || int64(len(data)) != receipt.Bytes {
		return "", AgentBrowserDownloadApproval{}, agentBrowserUnavailable("browser download integrity mismatch")
	}
	st, ok := s.store.(fullCDPWorkspaceInfoStore)
	if !ok {
		return "", AgentBrowserDownloadApproval{}, agentBrowserUnavailable("browser download Workspace unavailable")
	}
	workspace, e := st.GetWorkspaceInfo(ctx, workspaceID)
	if e != nil {
		return "", AgentBrowserDownloadApproval{}, e
	}
	if workspace.ID != workspaceID || !filepath.IsAbs(workspace.RootPath) {
		return "", AgentBrowserDownloadApproval{}, agentBrowserUnavailable("browser download Workspace mismatch")
	}
	root, e := os.OpenRoot(workspace.RootPath)
	if e != nil {
		// The raw *fs.PathError carries the Workspace absolute path; it must stay
		// in the cause and never reach the response message.
		return "", AgentBrowserDownloadApproval{}, apperror.Wrap(apperror.CodeUnavailable,
			"browser download Workspace root could not be opened", e)
	}
	defer root.Close()
	relative := filepath.Join(fullCDPBrowserArtifactDirectory, fullCDPDownloadArtifactDirectory, runID, receipt.GUID)
	if e = root.MkdirAll(filepath.Dir(relative), 0o700); e != nil {
		return "", AgentBrowserDownloadApproval{}, apperror.Wrap(apperror.CodeUnavailable,
			"browser download artifact directory could not be created", e)
	}
	file, e := root.OpenFile(relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(e, os.ErrExist) {
		old, x := readBoundedFullCDPArtifact(root, relative, browserruntime.AgentBrowserMaxDownloadBytes)
		if x != nil || string(old) != string(data) {
			return "", AgentBrowserDownloadApproval{}, agentBrowserUnavailable("browser artifact already contains different bytes")
		}
	} else if e != nil {
		return "", AgentBrowserDownloadApproval{}, apperror.Wrap(apperror.CodeUnavailable,
			"browser download capture could not be created", e)
	} else if e = writeNewAgentBrowserDownloadArtifact(root, relative, file, data); e != nil {
		return "", AgentBrowserDownloadApproval{}, apperror.Wrap(apperror.CodeUnavailable,
			"browser download capture could not be persisted", e)
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
		return "", AgentBrowserDownloadApproval{}, agentBrowserUnavailable("browser download provenance is invalid")
	}
	sidecarFile, e := root.OpenFile(sidecarRelative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(e, os.ErrExist) {
		old, x := readBoundedFullCDPArtifact(root, sidecarRelative, 16*1024)
		if x != nil || string(old) != string(sidecar) {
			return "", AgentBrowserDownloadApproval{}, agentBrowserUnavailable("browser artifact already contains different bytes")
		}
	} else if e != nil {
		return "", AgentBrowserDownloadApproval{}, apperror.Wrap(apperror.CodeUnavailable,
			"browser download provenance could not be created", e)
	} else if e = writeNewAgentBrowserDownloadArtifact(root, sidecarRelative, sidecarFile, sidecar); e != nil {
		return "", AgentBrowserDownloadApproval{}, apperror.Wrap(apperror.CodeUnavailable,
			"browser download provenance could not be persisted", e)
	}
	// The approval event is written after the capture so it always refers to
	// bytes that are already on disk. The exclusive create makes the first
	// approval authoritative: a repeat release with identical bytes keeps the
	// recorded approval timestamp and is a no-op for the audit record.
	approval := agentBrowserDownloadReleaseApproval{
		Version:          AgentBrowserDownloadReleaseVersion,
		Kind:             "release_approval",
		GUID:             receipt.GUID,
		RunID:            runID,
		WorkspaceID:      workspaceID,
		SHA256:           receipt.SHA256,
		Bytes:            receipt.Bytes,
		ApprovedAt:       time.Now().UTC(),
		Principal:        release.Principal,
		PrincipalScope:   AgentBrowserDownloadReleasePrincipalScopeCredentialClass,
		DeclaredOperator: release.Operator,
	}
	encoded, e := json.Marshal(approval)
	if e != nil || !utf8.Valid(encoded) {
		return "", AgentBrowserDownloadApproval{}, agentBrowserUnavailable("browser download approval event is invalid")
	}
	// The caller is told what is on disk. For a repeat release that is the
	// first approval event, not this call's identity or timestamp.
	persisted := AgentBrowserDownloadApproval{DeclaredOperator: approval.DeclaredOperator, ApprovedAt: approval.ApprovedAt}
	approvalRelative := relative + ".approval.json"
	approvalFile, e := root.OpenFile(approvalRelative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(e, os.ErrExist) {
		old, x := readBoundedFullCDPArtifact(root, approvalRelative, 16*1024)
		if x != nil {
			return "", AgentBrowserDownloadApproval{}, agentBrowserUnavailable("browser download approval event is unreadable")
		}
		var recorded agentBrowserDownloadReleaseApproval
		if json.Unmarshal(old, &recorded) != nil {
			return "", AgentBrowserDownloadApproval{}, agentBrowserUnavailable("browser download approval event is invalid")
		}
		if recorded.Kind != approval.Kind || recorded.Version != approval.Version ||
			recorded.GUID != approval.GUID || recorded.RunID != approval.RunID ||
			recorded.WorkspaceID != approval.WorkspaceID || recorded.SHA256 != approval.SHA256 ||
			recorded.Bytes != approval.Bytes || recorded.ApprovedAt.IsZero() ||
			recorded.Principal == "" || recorded.DeclaredOperator == "" ||
			recorded.Principal != approval.Principal || recorded.PrincipalScope != approval.PrincipalScope {
			return "", AgentBrowserDownloadApproval{}, agentBrowserUnavailable("browser download approval event conflicts with the released capture")
		}
		persisted = AgentBrowserDownloadApproval{DeclaredOperator: recorded.DeclaredOperator, ApprovedAt: recorded.ApprovedAt}
	} else if e != nil {
		return "", AgentBrowserDownloadApproval{}, apperror.Wrap(apperror.CodeUnavailable,
			"browser download approval event could not be created", e)
	} else if e = writeNewAgentBrowserDownloadArtifact(root, approvalRelative, approvalFile, encoded); e != nil {
		return "", AgentBrowserDownloadApproval{}, apperror.Wrap(apperror.CodeUnavailable,
			"browser download approval event could not be persisted", e)
	}
	return "workspace:///" + filepath.ToSlash(relative), persisted, nil
}

// writeAgentBrowserDownloadArtifactBytes writes one freshly created artifact.
// It is a package variable so a test can fail the write and watch the rollback
// below without filling a disk.
var writeAgentBrowserDownloadArtifactBytes = writeFullCDPArtifact

// writeNewAgentBrowserDownloadArtifact writes through a file this release just
// created exclusively and removes it when the write fails: a half-written
// capture, sidecar, or approval event would fail the byte comparison on every
// later release and wedge the GUID forever. The rollback only ever touches a
// path this call created, so a completed artifact is still never overwritten.
func writeNewAgentBrowserDownloadArtifact(root *os.Root, relative string, file *os.File, content []byte) error {
	if e := writeAgentBrowserDownloadArtifactBytes(file, content); e != nil {
		_ = root.Remove(relative)
		return e
	}
	return nil
}
