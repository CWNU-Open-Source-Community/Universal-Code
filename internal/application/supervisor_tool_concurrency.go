package application

import (
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

// SupervisorReadOnlyPoolSize bounds the number of contiguous read-only
// Supervisor tool invocations allowed to execute concurrently within a single
// round sub-batch.
const SupervisorReadOnlyPoolSize = 8

// isReadOnlyConcurrencySafeTool classifies whether a Supervisor tool call is
// verified side-effect-free (or per-call idempotent without shared cross-call
// mutable state) and safe to execute concurrently under a shared lock.
//
// Fail-closed default: any tool not explicitly listed here returns false and
// forms an exclusive serial barrier.
func isReadOnlyConcurrencySafeTool(toolName string) bool {
	name := toolgateway.ToolName(toolName)
	switch name {
	case toolgateway.WorkspaceListTool,
		toolgateway.WorkspaceReadTool,
		toolgateway.WorkspaceGlobTool,
		toolgateway.WorkspaceGrepTool,
		toolgateway.WorkspaceViewImageTool,
		toolgateway.GitHubEvidenceListTool,
		toolgateway.GitHubEvidenceReadTool,
		toolgateway.HistorySearchTool,
		toolgateway.HistoryReadTool,
		toolgateway.ThreadListTool,
		toolgateway.MCPToolSearchTool:
		return true
	default:
		return toolgateway.IsCodeIntelTool(name)
	}
}

type supervisorToolSubBatch struct {
	start    int
	end      int
	readOnly bool
}

// partitionSupervisorToolSubBatches partitions a round's calls into contiguous
// sub-batches in exact slice (Position) order:
//   - Already-terminal (non-pending) calls become 1-element serial steps.
//   - Pending exclusive tools become 1-element serial barriers.
//   - Contiguous runs of pending read-only concurrency-safe tools are grouped
//     into a single shared-lock sub-batch.
func partitionSupervisorToolSubBatches(calls []domain.SupervisorToolCall) []supervisorToolSubBatch {
	if len(calls) == 0 {
		return nil
	}
	batches := make([]supervisorToolSubBatch, 0, len(calls))
	for i := 0; i < len(calls); {
		call := calls[i]
		if call.Status != domain.SupervisorToolPending || !isReadOnlyConcurrencySafeTool(call.ToolName) {
			batches = append(batches, supervisorToolSubBatch{start: i, end: i + 1, readOnly: false})
			i++
			continue
		}
		j := i + 1
		for j < len(calls) && calls[j].Status == domain.SupervisorToolPending && isReadOnlyConcurrencySafeTool(calls[j].ToolName) {
			j++
		}
		batches = append(batches, supervisorToolSubBatch{start: i, end: j, readOnly: true})
		i = j
	}
	return batches
}
