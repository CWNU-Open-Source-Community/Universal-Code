package application

import (
	"context"
	"encoding/json"
	"errors"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/toolgateway"
)

func isBatchWorkerTool(name string) bool {
	switch name {
	case "workspace_list", "workspace_read", "workspace_glob", "workspace_grep", "workspace_change", "workspace_apply", "git_status", "git_diff", "git_commit", "verify":
		return true
	}
	return false
}

// Keep the Batch schemas and owned-path executor while adding the worker's
// general tools. A root workspace mutation must never shadow a Batch operation.
func (r *SubagentRunner) batchWorkerCatalogue(c workerToolCatalogue, child domain.AgentNode) workerToolCatalogue {
	if r.batchTools == nil {
		return c
	}
	for name := range c.tools {
		if toolgateway.IsAgentCodeTool(toolgateway.ToolName(name)) {
			delete(c.tools, name)
		}
	}
	for _, definition := range toolgateway.SpecialistNativeToolDefinitions(true) {
		if isBatchWorkerTool(string(definition.Name)) && child.Worker.AllowsTool(string(definition.Name)) {
			c.tools[string(definition.Name)] = workerToolEntry{definition: definition}
		}
	}
	return c
}

func workerBatchAuthority(binding domain.SpecialistToolBinding) (string, error) {
	// Only immutable host binding identifiers are recorded; the owner token
	// remains in the Batch workbench and is rechecked before every operation.
	raw, err := json.Marshal(struct {
		Version string                       `json:"version"`
		Binding domain.SpecialistToolBinding `json:"binding"`
	}{Version: "worker-batch-authority.v1", Binding: binding})
	return string(raw), err
}

func (r *SubagentRunner) resolveBatchWorkerTool(ctx context.Context, c domain.SpecialistToolCall, run domain.Run, mission domain.Mission, child domain.AgentNode, attempt domain.AgentAttempt, executor *specialistWorkspaceExecutor, gateway *toolgateway.Gateway) error {
	if !child.Worker.AllowsTool(c.Name) || executor.batch == nil || executor.binding.BatchPlanID == "" {
		return errors.New("isolated worker tool is outside its host binding")
	}
	st, ok := r.store.(workerDispatchStore)
	if !ok {
		return errors.New("isolated worker dispatch store is unavailable")
	}
	if c.StartedAt != nil {
		c.Status, c.ErrorCode = domain.SupervisorToolFailed, "outcome_unknown"
		c.ResultJSON = `{"status":"failed","error_code":"outcome_unknown","message":"The isolated tool was dispatched without a durable result; inspect its checkout and receipt before recovery."}`
		return st.CompleteSpecialistToolCall(ctx, c)
	}
	authority, err := workerBatchAuthority(executor.binding)
	if err != nil {
		return err
	}
	if c.AuthorityJSON == "" {
		c, err = st.BindSpecialistToolAuthority(ctx, c.Ref, c.CallID, authority)
		if err != nil {
			return err
		}
	}
	if c.AuthorityJSON != authority {
		return errors.New("isolated worker recorded authority changed")
	}
	return r.resolveSpecialistReceiptRead(ctx, c, run, mission, child, attempt, executor, gateway)
}

func (e *specialistWorkspaceExecutor) markBatchWorkerToolStarted(ctx context.Context, call toolgateway.ToolCall) error {
	st, ok := e.store.(interface {
		StartSpecialistBatchTool(context.Context, domain.AgentAttemptRef, string, string) error
	})
	if !ok {
		return errors.New("isolated worker start ledger is unavailable")
	}
	authority, err := workerBatchAuthority(e.binding)
	if err != nil {
		return err
	}
	return st.StartSpecialistBatchTool(ctx, e.binding.Ref, call.SpecialistToolCallID, authority)
}
