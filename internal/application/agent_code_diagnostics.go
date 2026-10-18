package application

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/tools"
	"cyberagent-workbench/internal/workspace"
	"cyberagent-workbench/internal/workspaceidentity"
)

const appliedDiagnosticsTimeout = time.Second
const maxAppliedDiagnosticsBytes = 12 * 1024

// Feedback and its comparison cover one applied document and one server.
type appliedDiagnosticsFeedback struct {
	Status         string                   `json:"status"`
	Path           string                   `json:"path"`
	DocumentSHA256 string                   `json:"document_sha256"`
	Coverage       string                   `json:"coverage"`
	ServerID       string                   `json:"server_id,omitempty"`
	Reason         string                   `json:"reason,omitempty"`
	Result         *codeintel.Result        `json:"result,omitempty"`
	Delta          *appliedDiagnosticsDelta `json:"delta,omitempty"`
	snapshot       codeintel.CapabilitySnapshot
}

type appliedDiagnosticsDelta struct {
	Status     string                   `json:"status"`
	Reason     string                   `json:"reason,omitempty"`
	Baseline   *codeintel.Provenance    `json:"baseline_provenance,omitempty"`
	Added      []codeintel.EvidenceItem `json:"added,omitempty"`
	Resolved   []codeintel.EvidenceItem `json:"resolved,omitempty"`
	Persisting []codeintel.EvidenceItem `json:"persisting,omitempty"`
}

func (e *AgentCodeToolExecutor) WithCodeIntel(manager *codeintel.Manager) *AgentCodeToolExecutor {
	if e != nil {
		e.codeIntel = manager
	}
	return e
}

func (e *AgentCodeToolExecutor) appliedDocumentDiagnostics(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, applied ApplyFileEditResult,
	pathPolicy workspaceidentity.PathPolicy, baseline appliedDiagnosticsFeedback,
) appliedDiagnosticsFeedback {
	path, hash := applied.Edit.Path, applied.Edit.ProposedHash
	if applied.Edit.Operation == fileedit.OperationMove {
		path, hash = applied.Edit.DestinationPath, applied.Edit.DestinationProposedHash
	}
	feedback := e.documentDiagnostics(ctx, scope, path, hash, pathPolicy)
	feedback.Delta = compareAppliedDiagnostics(baseline, feedback, applied)
	if raw, err := json.Marshal(feedback); err != nil || len(raw) > maxAppliedDiagnosticsBytes {
		feedback.Delta = &appliedDiagnosticsDelta{Status: "unavailable", Reason: "inline_summary_limit"}
	}
	return feedback
}

func (e *AgentCodeToolExecutor) documentDiagnostics(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, path, hash string,
	pathPolicy workspaceidentity.PathPolicy,
) appliedDiagnosticsFeedback {
	feedback := appliedDiagnosticsFeedback{Status: "unavailable", Path: path,
		DocumentSHA256: hash, Coverage: "single_document_single_server", Reason: "no_semantic_backend"}
	if e.codeIntel == nil {
		return feedback
	}
	ctx, cancel := context.WithTimeout(ctx, appliedDiagnosticsTimeout)
	defer cancel()
	if err := pathPolicy.Check(path); err != nil {
		feedback.Reason = "path_unavailable"
		return feedback
	}
	if err := e.validateScope(ctx, scope, toolgateway.CodeDiagnosticsTool); err != nil {
		feedback.Reason = "authority_unavailable"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			feedback.Status, feedback.Reason = "timeout", "diagnostics_timeout"
		}
		return feedback
	}
	// Do not query a later edit on replay, or start semantic work for bytes that
	// have changed since the write receipt was recorded.
	_, observed, err := workspace.AgentCodeReadMutationSource(scope.WorkspaceRoot, path)
	if err != nil || observed != hash {
		feedback.Status, feedback.Reason = "pending", "applied_version_no_longer_current"
		return feedback
	}
	// Inventory is observational. Only a server already initialized through the
	// ordinary semantic capability path may contribute automatic feedback.
	var selected codeintel.CapabilitySnapshot
	descriptors := e.codeIntel.Descriptors()
	for _, snapshot := range e.codeIntel.InitializedRootCapabilities(scope.WorkspaceID, scope.RootFingerprint) {
		if snapshot.WorkspaceID != scope.WorkspaceID || snapshot.Health != codeintel.HealthHealthy || !snapshot.Capabilities.Diagnostics {
			continue
		}
		for _, descriptor := range descriptors {
			if descriptor.WorkspaceID == scope.WorkspaceID && descriptor.ID == snapshot.ServerID {
				if _, matches := descriptor.LanguageForPath(path); matches {
					selected = snapshot
					break
				}
			}
		}
		if selected.ServerID != "" {
			break
		}
	}
	if selected.ServerID == "" {
		feedback.Reason = "no_ready_diagnostics_server"
		return feedback
	}
	feedback.ServerID = selected.ServerID
	allowed := func(snapshot codeintel.CapabilitySnapshot) bool {
		payload, _ := json.Marshal(toolgateway.CodeIntelPayload{Version: toolgateway.CodeIntelProtocolVersion,
			ServerID: snapshot.ServerID, ServerGeneration: snapshot.Generation,
			CapabilityFingerprint: snapshot.CapabilityFingerprint, Path: path, Limit: 5})
		if e.checker == nil {
			return false
		}
		decision := e.checker.CheckToolCall(tools.Call{Name: string(toolgateway.CodeDiagnosticsTool),
			Args: map[string]string{"payload": string(payload)}, WorkingDir: scope.WorkspaceRoot})
		return decision.Allowed && !decision.NeedsApproval
	}
	if !allowed(selected) {
		feedback.Reason = "diagnostics_policy_denied"
		return feedback
	}
	// The existing manager refreshes generations after workspace changes. Keep
	// that rule rather than using stale pre-edit capability fingerprints.
	selected, err = e.codeIntel.RefreshInitializedServer(ctx, scope.WorkspaceID, scope.WorkspaceRoot, selected.ServerID, selected.Generation)
	if err == nil && !allowed(selected) {
		feedback.Reason = "diagnostics_policy_denied"
		return feedback
	}
	var result codeintel.Result
	if err == nil {
		result, err = e.codeIntel.Execute(ctx, codeintel.Request{Tool: codeintel.ToolDiagnostics,
			WorkspaceID: scope.WorkspaceID, WorkspaceRoot: scope.WorkspaceRoot,
			ServerID: selected.ServerID, ServerGeneration: selected.Generation,
			CapabilityFingerprint: selected.CapabilityFingerprint, Path: path, Limit: 5,
			PathPolicy: pathPolicy, ExpectedDocumentSHA256: hash})
	}
	if err != nil {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded), apperror.CodeOf(err) == apperror.CodeDeadlineExceeded:
			feedback.Status, feedback.Reason = "timeout", "diagnostics_timeout"
		case errors.Is(err, codeintel.ErrDiagnosticsPending):
			feedback.Status, feedback.Reason = "pending", "document_version_not_reported"
		case apperror.CodeOf(err) == apperror.CodeConflict:
			feedback.Status, feedback.Reason = "pending", "document_or_server_changed"
		default:
			feedback.Reason = "diagnostics_unavailable"
		}
		return feedback
	}
	if err := e.validateScope(ctx, scope, toolgateway.CodeDiagnosticsTool); err != nil {
		feedback.Reason = "authority_unavailable"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			feedback.Status, feedback.Reason = "timeout", "diagnostics_timeout"
		}
		return feedback
	}
	// Keep a successful write receipt small and intact even for a verbose LSP.
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maxAppliedDiagnosticsBytes {
		feedback.Status, feedback.Reason = "pending", "use_code_diagnostics_for_full_result"
		return feedback
	}
	feedback.Status, feedback.Reason, feedback.Result = string(result.State), "", &result
	feedback.snapshot = selected
	return feedback
}

func compareAppliedDiagnostics(before, after appliedDiagnosticsFeedback, applied ApplyFileEditResult) *appliedDiagnosticsDelta {
	delta := &appliedDiagnosticsDelta{Status: "unavailable"}
	switch {
	case applied.Replayed:
		delta.Reason = "apply_replayed"
	case applied.Edit.Operation != fileedit.OperationReplace:
		delta.Reason = "operation_has_no_baseline"
	case before.Result == nil:
		delta.Reason = "baseline_" + before.Reason
	case !completeAppliedDiagnostics(before.Result):
		delta.Reason = "baseline_incomplete"
	case after.Result == nil:
		delta.Reason = "current_diagnostics_unavailable"
	case !completeAppliedDiagnostics(after.Result):
		delta.Reason = "current_diagnostics_incomplete"
	default:
		original, current := before.Result.Provenance, after.Result.Provenance
		delta.Baseline = &original
		switch {
		case original.WorkspaceID != current.WorkspaceID || original.RootFingerprint != current.RootFingerprint ||
			original.RepositoryAvailable != current.RepositoryAvailable || original.Commit != current.Commit || original.Branch != current.Branch ||
			original.DocumentPath != current.DocumentPath || original.DocumentURI != current.DocumentURI:
			delta.Reason = "workspace_or_document_changed"
		case original.DocumentSHA256 != applied.Edit.OriginalHash || current.DocumentSHA256 != applied.Edit.ProposedHash ||
			original.DocumentVersion <= 0 || current.DocumentVersion <= 0:
			delta.Reason = "document_version_not_comparable"
		case original.ServerID != current.ServerID || original.CapabilityFingerprint != current.CapabilityFingerprint ||
			before.snapshot.DescriptorFingerprint == "" || before.snapshot.DescriptorFingerprint != after.snapshot.DescriptorFingerprint ||
			before.snapshot.Generation != original.ServerGeneration || after.snapshot.Generation != current.ServerGeneration:
			delta.Reason = "server_changed"
		case original.ServerGeneration == current.ServerGeneration && original.DocumentSHA256 != current.DocumentSHA256 &&
			current.DocumentVersion <= original.DocumentVersion:
			delta.Reason = "document_version_not_comparable"
		default:
			// Both results were obtained in this apply call against its exact
			// original/applied bytes. A Git dirty-state refresh may restart the
			// same reviewed server; it does not make an older cached result a
			// baseline. Descriptor and capability changes remain incomparable.
			delta.Status = "current"
			previous := make(map[string]bool, len(before.Result.Items))
			for _, item := range before.Result.Items {
				previous[diagnosticIdentity(item)] = false
			}
			for _, item := range after.Result.Items {
				key := diagnosticIdentity(item)
				if _, exists := previous[key]; exists {
					delta.Persisting = append(delta.Persisting, item)
					previous[key] = true
				} else {
					delta.Added = append(delta.Added, item)
				}
			}
			for _, item := range before.Result.Items {
				if !previous[diagnosticIdentity(item)] {
					delta.Resolved = append(delta.Resolved, item)
				}
			}
		}
	}
	return delta
}

func completeAppliedDiagnostics(result *codeintel.Result) bool {
	return result.State == codeintel.EvidenceCurrent && !result.Page.Truncated &&
		len(result.Warnings) == 0 && result.Page.Returned == result.Page.Total
}

func diagnosticIdentity(item codeintel.EvidenceItem) string {
	// Evidence IDs bind source, message, code and range; include severity too.
	return item.ID + ":" + strconv.Itoa(item.Severity)
}
