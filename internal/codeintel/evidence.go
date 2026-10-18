package codeintel

import (
	"context"
	"errors"
)

type EvidenceValidation struct {
	ProtocolVersion string        `json:"protocol_version"`
	State           EvidenceState `json:"state"`
	Reason          string        `json:"reason"`
}

// ValidateEvidence is the stable review/focused-checks freshness boundary. A
// semantic result can inform an edit only while every source binding remains
// current; stale and unavailable are deliberately distinct.
func (m *Manager) ValidateEvidence(ctx context.Context, root string,
	provenance Provenance,
) EvidenceValidation {
	result := EvidenceValidation{ProtocolVersion: ProtocolVersion,
		State: EvidenceUnavailable, Reason: "code-intel runtime is unavailable"}
	if m == nil || provenance.Validate() != nil {
		return result
	}
	workspace, err := captureWorkspaceBinding(ctx, root, provenance.WorkspaceID)
	if err != nil {
		result.Reason, _ = sanitizeText(err.Error(), 512, false)
		return result
	}
	if workspace.RootFingerprint != provenance.RootFingerprint ||
		workspace.RepositoryAvailable != provenance.RepositoryAvailable ||
		workspace.Commit != provenance.Commit || workspace.Branch != provenance.Branch ||
		workspace.Dirty != provenance.Dirty || workspace.DirtyDigest != provenance.DirtyDigest {
		result.State = EvidenceStale
		result.Reason = "Workspace root, branch, commit, or dirty state changed"
		return result
	}
	key := serverKey(provenance.WorkspaceID, provenance.ServerID)
	m.mu.Lock()
	current := m.clients[workspaceClientKey(key, workspace.RootFingerprint)]
	unavailable := m.closing || m.quarantined || current == nil
	m.mu.Unlock()
	if unavailable {
		result.Reason = "reviewed language server is not currently healthy"
		return result
	}
	if current.snapshot.Generation != provenance.ServerGeneration ||
		current.snapshot.CapabilityFingerprint != provenance.CapabilityFingerprint {
		result.State = EvidenceStale
		result.Reason = "language server generation or capability fingerprint changed"
		return result
	}
	if provenance.DocumentPath != "" {
		document, captureErr := captureDocumentBinding(root, provenance.DocumentPath,
			provenance.DocumentVersion)
		if captureErr != nil {
			if errors.Is(captureErr, context.Canceled) {
				result.Reason = "evidence validation was cancelled"
			} else {
				result.State = EvidenceStale
				result.Reason = "semantic document is no longer safely readable"
			}
			return result
		}
		if document.URI != provenance.DocumentURI ||
			document.SHA256 != provenance.DocumentSHA256 {
			result.State = EvidenceStale
			result.Reason = "semantic document content or URI changed"
			return result
		}
		current.mu.Lock()
		opened, openedOK := current.documents[provenance.DocumentPath]
		current.mu.Unlock()
		if !openedOK || opened.Version != provenance.DocumentVersion ||
			opened.SHA256 != provenance.DocumentSHA256 {
			result.State = EvidenceStale
			result.Reason = "language server document version changed"
			return result
		}
	}
	result.State = EvidenceCurrent
	result.Reason = "all Workspace, document, and language server bindings are current"
	return result
}
