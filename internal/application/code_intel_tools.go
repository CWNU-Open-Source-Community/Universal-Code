package application

import (
	"context"
	"encoding/json"
	"fmt"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/toolgateway"
)

type CodeIntelToolExecutor struct {
	manager   *codeintel.Manager
	authority *AgentCodeToolExecutor
}

func (e *CodeIntelToolExecutor) WithRunWorktree(drydocks *RunWorktreeService) *CodeIntelToolExecutor {
	if e != nil && e.authority != nil {
		e.authority.WithRunWorktree(drydocks)
	}
	return e
}

func NewCodeIntelToolExecutor(store AgentCodeToolStore, checker policy.Checker,
	manager *codeintel.Manager,
) *CodeIntelToolExecutor {
	return &CodeIntelToolExecutor{manager: manager,
		authority: NewAgentCodeToolExecutor(store, checker)}
}

func (e *CodeIntelToolExecutor) ExecuteCodeIntel(ctx context.Context,
	scope toolgateway.AgentCodeExecutionScope, name toolgateway.ToolName,
	payload json.RawMessage,
) (toolgateway.CodeIntelExecutionResult, error) {
	if e == nil || e.manager == nil || e.authority == nil {
		return toolgateway.CodeIntelExecutionResult{}, apperror.New(
			apperror.CodeFailedPrecondition, "code-intel tool dependencies are unavailable")
	}
	if err := e.authority.validateScope(ctx, scope, name); err != nil {
		return toolgateway.CodeIntelExecutionResult{}, err
	}
	input, _, err := toolgateway.NormalizeCodeIntelPayload(name, payload)
	if err != nil {
		return toolgateway.CodeIntelExecutionResult{}, err
	}
	run, err := e.authority.store.GetRun(ctx, scope.RunID)
	if err != nil {
		return toolgateway.CodeIntelExecutionResult{}, apperror.Normalize(err)
	}
	pathPolicy, err := projectconfig.PathPolicy(run.Config)
	if err != nil {
		return toolgateway.CodeIntelExecutionResult{}, err
	}
	result, err := e.manager.Execute(ctx, codeintel.Request{
		PathPolicy: pathPolicy,
		Tool:       string(name), WorkspaceID: scope.WorkspaceID,
		WorkspaceRoot: scope.WorkspaceRoot, ServerID: input.ServerID,
		ServerGeneration:      input.ServerGeneration,
		CapabilityFingerprint: input.CapabilityFingerprint,
		Path:                  input.Path, Query: input.Query,
		Position:  codeintel.Position{Line: input.Line, Character: input.Character},
		Direction: input.Direction, IncludeDeclaration: input.IncludeDeclaration,
		Limit: input.Limit, Cursor: input.Cursor,
	})
	if err != nil {
		return toolgateway.CodeIntelExecutionResult{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return toolgateway.CodeIntelExecutionResult{}, err
	}
	metadata := map[string]string{
		"workspace_id":           result.Provenance.WorkspaceID,
		"root_fingerprint":       result.Provenance.RootFingerprint,
		"commit":                 result.Provenance.Commit,
		"dirty":                  fmt.Sprint(result.Provenance.Dirty),
		"dirty_digest":           result.Provenance.DirtyDigest,
		"server_id":              result.Provenance.ServerID,
		"server_generation":      result.Provenance.ServerGeneration,
		"capability_fingerprint": result.Provenance.CapabilityFingerprint,
		"query_fingerprint":      result.Provenance.QueryFingerprint,
		"evidence_state":         string(result.State),
		"evidence_level":         result.EvidenceLevel,
		"result_count":           fmt.Sprint(len(result.Items)),
		"total_count":            fmt.Sprint(result.Page.Total),
		"truncated":              fmt.Sprint(result.Page.Truncated),
	}
	if result.Provenance.DocumentPath != "" {
		metadata["path"] = result.Provenance.DocumentPath
		metadata["content_sha256"] = result.Provenance.DocumentSHA256
		metadata["document_version"] = fmt.Sprint(result.Provenance.DocumentVersion)
	}
	if result.Page.NextCursor != "" {
		metadata["next_cursor"] = result.Page.NextCursor
	}
	return toolgateway.CodeIntelExecutionResult{JSON: string(encoded), Metadata: metadata}, nil
}
