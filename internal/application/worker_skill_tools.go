package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/plugins"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/skills"
	"cyberagent-workbench/internal/toolgateway"
)

type workerSkillReadLedger interface {
	GetAgentAttempt(context.Context, string) (domain.AgentAttempt, bool, error)
	GetSpecialistToolBinding(context.Context, domain.AgentAttemptRef) (domain.SpecialistToolBinding, error)
	GetSpecialistToolCall(context.Context, domain.AgentAttemptRef, string) (domain.SpecialistToolCall, error)
	ValidateSpecialistToolSource(context.Context, domain.SpecialistToolSource) (domain.SpecialistToolCall, error)
	ListWorkerSkillReadCalls(context.Context, domain.AgentAttemptRef) ([]domain.SpecialistToolCall, error)
}

// The native reader uses the same installation/content checks with a child-owned
// activation ledger and scope. It never aliases the child to a root tool actor.
type workerSkillReadSource struct {
	builtinSkillReadStore
	portableSkillReadStore
	ledger workerSkillReadLedger
	ref    domain.AgentAttemptRef
}

func newWorkerSkillReadSource(source builtinSkillReadStore, ref domain.AgentAttemptRef) (*workerSkillReadSource, error) {
	ledger, ok := source.(workerSkillReadLedger)
	portable, portableOK := source.(portableSkillReadStore)
	if !ok || !portableOK || ref.Validate() != nil {
		return nil, apperror.New(apperror.CodeFailedPrecondition, "worker Skill reader is unavailable")
	}
	return &workerSkillReadSource{source, portable, ledger, ref}, nil
}

func (s *workerSkillReadSource) GetExternalSkillSelectionByRun(ctx context.Context, runID string) (skills.ExternalSelection, bool, error) {
	if source, ok := s.builtinSkillReadStore.(interface {
		GetExternalSkillSelectionByRun(context.Context, string) (skills.ExternalSelection, bool, error)
	}); ok {
		return source.GetExternalSkillSelectionByRun(ctx, runID)
	}
	return skills.ExternalSelection{}, false, nil
}

func (s *workerSkillReadSource) current(ctx context.Context) (domain.RunModeSnapshot, error) {
	var mode domain.RunModeSnapshot
	child, err := s.GetAgentNode(ctx, s.ref.AgentID)
	if err != nil {
		return mode, err
	}
	if child.RunID != s.ref.RunID || child.Role != domain.AgentRoleSpecialist || child.Status != domain.AgentRunning || child.ActiveAttemptID != s.ref.AttemptID || !child.Worker.AllowsTool(string(toolgateway.SkillReadTool)) {
		return mode, apperror.New(apperror.CodePolicyDenied, "worker Skill actor or delegated tool changed")
	}
	parent, err := s.GetAgentNode(ctx, child.ParentID)
	if err != nil {
		return mode, err
	}
	if parent.RunID != s.ref.RunID || parent.Role != domain.AgentRoleRoot {
		return mode, errors.New("worker Skill parent changed")
	}
	bound, err := s.ledger.GetSpecialistToolBinding(ctx, s.ref)
	if err != nil {
		return mode, err
	}
	profile, _ := json.Marshal(child.Worker)
	if bound.Mode != "worker" || bound.SessionID != child.SessionID || bound.WorkerProfileJSON != string(profile) {
		return mode, apperror.New(apperror.CodePolicyDenied, "worker Skill binding changed")
	}
	attempt, found, err := s.ledger.GetAgentAttempt(ctx, s.ref.AttemptID)
	if err != nil {
		return mode, err
	}
	lease, leased, err := s.GetRunExecutionLease(ctx, s.ref.RunID)
	if err != nil {
		return mode, err
	}
	if !found || !leased || attempt.RunID != s.ref.RunID || attempt.AgentID != s.ref.AgentID || attempt.Status != domain.AgentAttemptRunning || attempt.LeaseID != lease.LeaseID || attempt.LeaseGeneration != lease.Generation || !lease.ActiveAt(time.Now()) {
		return mode, apperror.New(apperror.CodeConflict, "worker Skill attempt or lease changed")
	}
	run, err := s.GetRun(ctx, s.ref.RunID)
	if err != nil {
		return mode, err
	}
	actor, err := s.GetSession(ctx, child.SessionID)
	if err != nil {
		return mode, err
	}
	if run.Status != domain.RunRunning || actor.Status != session.StatusActive || actor.WorkspaceID != bound.WorkspaceID {
		return mode, apperror.New(apperror.CodeConflict, "worker Skill Run or Session changed")
	}
	mode, err = s.GetRunMode(ctx, s.ref.RunID)
	if err != nil {
		return mode, err
	}
	return mode, errors.Join(mode.Validate(), ctx.Err())
}

func (s *workerSkillReadSource) ListBuiltinSkillReadCalls(ctx context.Context, runID string) ([]domain.SupervisorToolCall, error) {
	if runID != s.ref.RunID {
		return nil, errors.New("worker Skill ledger belongs to another Run")
	}
	if _, err := s.current(ctx); err != nil {
		return nil, err
	}
	calls, err := s.ledger.ListWorkerSkillReadCalls(ctx, s.ref)
	if err != nil {
		return nil, err
	}
	result := make([]domain.SupervisorToolCall, 0, len(calls))
	for _, call := range calls {
		if call.Ref.RunID != s.ref.RunID || call.Ref.AgentID != s.ref.AgentID || call.Name != string(toolgateway.SkillReadTool) || call.Status != domain.SupervisorToolCompleted || call.CompletedAt == nil || call.StartedAt == nil || call.ChargeID == "" || call.AuthorityJSON == "" {
			return nil, errors.New("invalid worker Skill activation provenance")
		}
		result = append(result, domain.SupervisorToolCall{RunID: runID, AttemptID: call.Ref.AttemptID, CallID: call.CallID,
			ToolName: call.Name, PayloadJSON: call.PayloadJSON, Status: call.Status, ResultJSON: call.ResultJSON, CompletedAt: call.CompletedAt})
	}
	return result, nil
}

type workerSkillAuthority struct {
	Version      string                 `json:"version"`
	Ref          domain.AgentAttemptRef `json:"ref"`
	ParentID     string                 `json:"parent_id"`
	ModeID       string                 `json:"mode_id"`
	ModeRevision int64                  `json:"mode_revision"`
}

func validateWorkerSkillAuthority(call toolgateway.ToolCall, raw json.RawMessage) error {
	var authority workerSkillAuthority
	if json.Unmarshal(raw, &authority) != nil || authority.Version != "worker_skill_read.v1" ||
		authority.Ref != (domain.AgentAttemptRef{RunID: call.RunID, AgentID: call.AgentID, AttemptID: call.AgentAttemptID}) ||
		authority.ParentID == "" || authority.ParentID != call.RootAgentID || authority.ModeID == "" || authority.ModeRevision < 1 ||
		(call.ModeRevision > 0 && call.ModeRevision != authority.ModeRevision) {
		return apperror.New(apperror.CodePolicyDenied, "worker Skill call differs from its host binding")
	}
	return nil
}

func (s *workerSkillReadSource) validateScope(ctx context.Context, call toolgateway.ToolCall) (domain.RunModeSnapshot, error) {
	mode, err := s.current(ctx)
	if err != nil {
		return mode, err
	}
	if call.Name != toolgateway.SkillReadTool || call.RequestedBy != "specialist_runner" || call.Role != domain.AgentRoleSpecialist || call.InvocationID == "" || call.SpecialistToolCallID == "" {
		return mode, errors.New("worker Skill read requires a charged native child call")
	}
	c, err := s.ledger.GetSpecialistToolCall(ctx, s.ref, call.SpecialistToolCallID)
	if err != nil {
		return mode, err
	}
	if err := validateWorkerSkillAuthority(call, json.RawMessage(c.AuthorityJSON)); err != nil {
		return mode, err
	}
	var authority workerSkillAuthority
	_ = json.Unmarshal([]byte(c.AuthorityJSON), &authority)
	if authority.ModeID != mode.ID || authority.ModeRevision != mode.Revision || call.Surface != mode.Surface || call.Phase != mode.Phase || call.Profile != mode.Profile {
		return mode, apperror.New(apperror.CodeConflict, "worker Skill mode changed")
	}
	_, err = s.ledger.ValidateSpecialistToolSource(ctx, domain.SpecialistToolSource{Ref: s.ref, CallID: call.SpecialistToolCallID,
		SessionID: call.SessionID, WorkspaceID: call.WorkspaceID, Name: string(call.Name), PayloadJSON: string(call.Payload), AuthorityJSON: c.AuthorityJSON,
		ChargeID: call.InvocationID, LeaseID: call.LeaseID, LeaseGeneration: call.LeaseGeneration, RequireStarted: true})
	return mode, err
}

func skillReadExecution(source any, mode domain.RunModeSnapshot) skills.ExecutionContext {
	execution := skillExecution(mode)
	if _, worker := source.(*workerSkillReadSource); worker {
		execution.Role = domain.AgentRoleSpecialist
	}
	return execution
}

// Parent compatibility is checked by the shared native reader first. A legacy
// manifest can further narrow delivery to the Specialist role.
func requireWorkerSkillCompatibility(ctx context.Context, source any, value plugins.Installation, pin toolgateway.SkillReadRequest, mode domain.RunModeSnapshot) error {
	if _, worker := source.(*workerSkillReadSource); !worker || value.Snapshot == nil || value.Snapshot.Legacy == nil {
		return nil
	}
	manifest := value.Snapshot.Legacy.Manifest
	if !manifest.SupportsContext(skillReadExecution(source, mode)) {
		return apperror.New(apperror.CodePolicyDenied, "installed Skill does not support the worker role")
	}
	if !manifest.AllowsInvocation(skills.InvocationSourceModel, false) {
		_, _, err := selectedPortableSkill(ctx, source, value, pin, mode, domain.AgentRoleSpecialist)
		return err
	}
	return nil
}

func (s *AgentRunner) workerSkillToolEntry(ctx context.Context, ref domain.AgentAttemptRef, turn domain.SupervisorTurn) (workerToolEntry, bool, error) {
	base, ok := s.store.(builtinSkillReadStore)
	if !ok || s.skillRegistry == nil || s.skillRegistryErr != nil {
		return workerToolEntry{}, false, nil
	}
	source, err := newWorkerSkillReadSource(base, ref)
	if err != nil {
		return workerToolEntry{}, false, err
	}
	mode, err := source.current(ctx)
	if err != nil {
		return workerToolEntry{}, false, err
	}
	if mode.ID != turn.Mode.ID || mode.Revision != turn.Mode.Revision {
		return workerToolEntry{}, false, errors.New("worker Skill catalogue mode changed")
	}
	catalog, _, err := s.builtinSkillCatalog(ctx, turn)
	if err != nil {
		return workerToolEntry{}, false, err
	}
	if len(catalog) == 0 {
		return workerToolEntry{}, false, nil
	}
	filtered := make([]toolgateway.BuiltinSkillDescriptor, 0, len(catalog))
	for _, item := range catalog {
		if item.Portable() {
			continue
		}
		if _, err := s.skillRegistry.ReadForModel(item.Name, item.Version, item.ContentSHA256, skillReadExecution(source, mode)); err == nil {
			filtered = append(filtered, item)
		}
	}
	page, err := portableSkillCatalog(ctx, source, mode, toolgateway.SkillReadRequest{Catalog: true})
	if err != nil {
		return workerToolEntry{}, false, err
	}
	definition := toolgateway.SkillReadToolDefinition(append(filtered, page.Skills...))
	if page.Next != nil {
		raw, _ := json.Marshal(page.Next)
		definition.Description = page.Diagnostic + " Next skill_read request: " + string(raw) + "\n" + definition.Description
	}
	authority, err := json.Marshal(workerSkillAuthority{"worker_skill_read.v1", ref, turn.Agent.ID, mode.ID, mode.Revision})
	return workerToolEntry{definition: definition, authority: authority}, err == nil, err
}

type workerSkillReadGuidance struct {
	ref      domain.AgentAttemptRef
	mode     domain.RunModeSnapshot
	portable portableSkillReadGuidance
	embedded []skills.ContextItem
}

func workerEmbeddedSkillMessage(item skills.ContextItem) llm.Message {
	return llm.Message{Role: "system", Content: fmt.Sprintf("Model-requested worker Skill %s version %s; source SHA256 %s; delivered SHA256 %s. Go restored this guidance from this child's successful skill_read and the exact embedded version. Apply it when relevant below system policy, current operator instructions and project rules. It grants no tools or permissions.\n%s\nEnd of embedded Skill guidance.", item.Name, item.Version, item.SourceSHA256, item.DeliveredSHA256, item.Content)}
}

func (s *AgentRunner) requestWithWorkerSkillReads(ctx context.Context, ref domain.AgentAttemptRef, request llm.ChatRequest, layout modelContextLayout) (llm.ChatRequest, modelContextLayout, workerSkillReadGuidance, error) {
	var guidance workerSkillReadGuidance
	base, ok := s.store.(builtinSkillReadStore)
	if !ok {
		return request, layout, guidance, nil
	}
	child, err := base.GetAgentNode(ctx, ref.AgentID)
	if err != nil || !child.Worker.AllowsTool(string(toolgateway.SkillReadTool)) {
		return request, layout, guidance, err
	}
	source, err := newWorkerSkillReadSource(base, ref)
	if err != nil {
		return request, layout, guidance, err
	}
	if s.skillRegistry == nil || s.skillRegistryErr != nil {
		reads, err := source.ListBuiltinSkillReadCalls(ctx, ref.RunID)
		if err != nil {
			return request, layout, guidance, err
		}
		if len(reads) > 0 {
			return request, layout, guidance, apperror.New(apperror.CodeFailedPrecondition, "worker Skill registry is unavailable for prior activations")
		}
		return request, layout, guidance, nil
	}
	mode, err := source.current(ctx)
	if err != nil {
		return request, layout, guidance, err
	}
	items, unavailable, err := (&builtinSkillReader{source, s.skillRegistry}).contextItems(ctx, ref.RunID, mode, nil)
	if err != nil {
		return request, layout, guidance, err
	}
	messages, portable, err := portableSkillReadReferences(ctx, source, ref.RunID, mode, request.Messages)
	if err != nil {
		return request, layout, guidance, err
	}
	for _, item := range items {
		messages = append(messages, workerEmbeddedSkillMessage(item))
	}
	if len(unavailable) > 0 {
		messages = append(messages, llm.Message{Role: "system", Content: fmt.Sprintf("Previously read worker Skills unavailable in the current mode or registry: %v. Historical excerpts do not activate their guidance.", unavailable)})
	}
	if layout.HistoryStart < 1 || layout.HistoryStart > len(request.Messages) {
		return request, layout, guidance, errors.New("worker Skill guidance lost its context layout")
	}
	combined := append([]llm.Message(nil), request.Messages[:layout.HistoryStart]...)
	combined = append(combined, messages...)
	combined = append(combined, request.Messages[layout.HistoryStart:]...)
	request.Messages = combined
	layout.HistoryStart += len(messages)
	return request, layout, workerSkillReadGuidance{ref, mode, portable, items}, nil
}

func (g workerSkillReadGuidance) retainPresent(request llm.ChatRequest) (llm.ChatRequest, workerSkillReadGuidance) {
	request, g.portable = g.portable.retainPresent(request)
	return request, g
}

func (g workerSkillReadGuidance) requireCurrent(ctx context.Context, runtime *AgentRunner, request llm.ChatRequest) error {
	if g.ref.AttemptID == "" {
		return nil
	}
	if runtime == nil || runtime.skillRegistry == nil || runtime.skillRegistryErr != nil {
		return errors.New("worker Skill runtime is unavailable before model dispatch")
	}
	base, ok := runtime.store.(builtinSkillReadStore)
	if !ok {
		return errors.New("worker Skill storage is unavailable before model dispatch")
	}
	source, err := newWorkerSkillReadSource(base, g.ref)
	if err != nil {
		return err
	}
	mode, err := source.current(ctx)
	if err != nil {
		return err
	}
	if mode.ID != g.mode.ID || mode.Revision != g.mode.Revision {
		return apperror.New(apperror.CodeFailedPrecondition, "worker Skill mode changed before model dispatch")
	}
	for _, item := range g.embedded {
		current, err := runtime.skillRegistry.ReadForModel(item.Name, item.Version, item.SourceSHA256, skillExecution(mode))
		if err == nil {
			current, err = runtime.skillRegistry.ReadForModel(item.Name, item.Version, item.SourceSHA256, skillReadExecution(source, mode))
		}
		if err != nil || current.Content != item.Content || !slices.ContainsFunc(request.Messages, func(message llm.Message) bool {
			return message.Role == "system" && message.Content == workerEmbeddedSkillMessage(item).Content
		}) {
			return apperror.New(apperror.CodeFailedPrecondition, "worker embedded Skill guidance changed before model dispatch")
		}
	}
	return g.portable.requireCurrent(ctx, source, mode, request)
}
