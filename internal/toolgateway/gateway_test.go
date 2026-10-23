package toolgateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/artifact"
	"cyberagent-workbench/internal/fileedit"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/scriptprocess"
	"cyberagent-workbench/internal/toolbudget"
	"cyberagent-workbench/internal/toolrun"
	"cyberagent-workbench/internal/tools"
)

type memoryStore struct {
	mu               sync.Mutex
	runs             map[string]toolrun.ToolRun
	edits            map[string]fileedit.Edit
	approvals        map[string]approval.Record
	operations       map[string]string
	processes        map[string]scriptprocess.Process
	artifacts        map[string]artifact.Blob
	artifactBySource map[string]string
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		runs: map[string]toolrun.ToolRun{}, edits: map[string]fileedit.Edit{},
		approvals: map[string]approval.Record{}, operations: map[string]string{},
		processes: map[string]scriptprocess.Process{},
		artifacts: map[string]artifact.Blob{}, artifactBySource: map[string]string{},
	}
}

func (s *memoryStore) SaveToolRun(_ context.Context, run toolrun.ToolRun) (toolrun.ToolRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.ID] = run
	return run, nil
}

func (s *memoryStore) GetToolRun(_ context.Context, id string) (toolrun.ToolRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return toolrun.ToolRun{}, errors.New("tool run not found")
	}
	return run, nil
}

func (s *memoryStore) ListToolRuns(_ context.Context, filter toolrun.ListFilter) ([]toolrun.ToolRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var runs []toolrun.ToolRun
	for _, run := range s.runs {
		if filter.SessionID != "" && filter.SessionID != run.SessionID {
			continue
		}
		if filter.Status != "" && filter.Status != run.Status {
			continue
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func (s *memoryStore) SaveFileEdit(_ context.Context, edit fileedit.Edit) (fileedit.Edit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.edits[edit.ID] = edit
	return edit, nil
}

func (s *memoryStore) GetFileEdit(_ context.Context, id string) (fileedit.Edit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	edit, ok := s.edits[id]
	if !ok {
		return fileedit.Edit{}, errors.New("file edit not found")
	}
	return edit, nil
}

func (s *memoryStore) ListFileEdits(_ context.Context, filter fileedit.ListFilter) ([]fileedit.Edit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var edits []fileedit.Edit
	for _, edit := range s.edits {
		if filter.SessionID != "" && filter.SessionID != edit.SessionID {
			continue
		}
		if filter.WorkspaceID != "" && filter.WorkspaceID != edit.WorkspaceID {
			continue
		}
		if filter.Status != "" && filter.Status != edit.Status {
			continue
		}
		edits = append(edits, edit)
	}
	return edits, nil
}

func (s *memoryStore) EnsureApproval(_ context.Context, proposal approval.Proposal) (approval.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, ok := s.approvals[proposal.ProposalID]; ok {
		return record, nil
	}
	now := proposal.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	updated := proposal.UpdatedAt
	if updated.IsZero() {
		updated = now
	}
	record := approval.Record{
		ID: "approval-" + proposal.ProposalID, IdempotencyKey: proposal.IdempotencyKey, ProposalID: proposal.ProposalID,
		SessionID: proposal.SessionID, WorkspaceID: proposal.WorkspaceID, ToolName: proposal.ToolName,
		ActionClass: proposal.ActionClass, Mode: proposal.Mode, Status: proposal.Status,
		RequestFingerprint: proposal.RequestFingerprint, DecisionReason: proposal.DecisionReason,
		RequestedBy: proposal.RequestedBy, ReviewedBy: proposal.ReviewedBy, Version: 1,
		CreatedAt: now, UpdatedAt: updated, DecidedAt: proposal.DecidedAt,
	}
	s.approvals[proposal.ProposalID] = record
	return record, nil
}

func (s *memoryStore) DecideApproval(_ context.Context, request approval.DecisionRequest) (approval.DecisionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalized, err := request.Normalize()
	if err != nil {
		return approval.DecisionResult{}, err
	}
	if proposalID, ok := s.operations[normalized.IdempotencyKey]; ok {
		if proposalID != normalized.ProposalID {
			return approval.DecisionResult{}, errors.New("idempotency conflict")
		}
		return approval.DecisionResult{Approval: s.approvals[proposalID], Replayed: true}, nil
	}
	record, ok := s.approvals[normalized.ProposalID]
	if !ok {
		return approval.DecisionResult{}, errors.New("approval not found")
	}
	desired, _ := normalized.Action.Status()
	if record.Status != approval.StatusPending && record.Status != desired {
		return approval.DecisionResult{}, errors.New("approval conflict")
	}
	replayed := record.Status == desired
	if !replayed {
		now := time.Now().UTC()
		record.Status = desired
		record.DecisionReason = normalized.Reason
		record.ReviewedBy = normalized.ReviewedBy
		record.DecidedAt = &now
		record.UpdatedAt = now
		record.Version++
		s.approvals[normalized.ProposalID] = record
	}
	s.operations[normalized.IdempotencyKey] = normalized.ProposalID
	return approval.DecisionResult{Approval: record, Replayed: replayed}, nil
}

func (s *memoryStore) GetApproval(_ context.Context, id string) (approval.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.approvals {
		if record.ID == id {
			return record, nil
		}
	}
	return approval.Record{}, errors.New("approval not found")
}

func (s *memoryStore) GetApprovalByProposal(_ context.Context, proposalID string) (approval.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.approvals[proposalID]
	if !ok {
		return approval.Record{}, errors.New("approval not found")
	}
	return record, nil
}

func (s *memoryStore) ListApprovals(_ context.Context, filter approval.ListFilter) ([]approval.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []approval.Record
	for _, record := range s.approvals {
		if filter.RunID != "" && record.RunID != filter.RunID ||
			filter.SessionID != "" && record.SessionID != filter.SessionID ||
			filter.Status != "" && record.Status != filter.Status ||
			filter.ToolName != "" && record.ToolName != filter.ToolName {
			continue
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *memoryStore) CreateSessionGrant(context.Context, approval.CreateGrantRequest) (approval.GrantResult, error) {
	return approval.GrantResult{}, errors.New("session grants are not supported by this test store")
}

func (s *memoryStore) RevokeSessionGrant(context.Context, approval.RevokeGrantRequest) (approval.GrantResult, error) {
	return approval.GrantResult{}, errors.New("session grants are not supported by this test store")
}

func (s *memoryStore) AuthorizeApprovalWithSessionGrant(context.Context, string, string) (approval.DecisionResult, error) {
	return approval.DecisionResult{}, errors.New("session grants are not supported by this test store")
}

func (s *memoryStore) FindActiveSessionGrant(context.Context, approval.GrantQuery) (approval.SessionGrant, bool, error) {
	return approval.SessionGrant{}, false, nil
}

func (s *memoryStore) GetSessionGrant(context.Context, string) (approval.SessionGrant, error) {
	return approval.SessionGrant{}, errors.New("session grant not found")
}

func (s *memoryStore) ListSessionGrants(context.Context, approval.GrantListFilter) ([]approval.SessionGrant, error) {
	return nil, nil
}

func (s *memoryStore) ChargeToolCall(context.Context, toolbudget.ChargeRequest) (toolbudget.Usage, error) {
	return toolbudget.Usage{Remaining: -1}, nil
}

func (s *memoryStore) GetToolCallUsage(context.Context, string) (toolbudget.Usage, error) {
	return toolbudget.Usage{Remaining: -1}, nil
}

func (s *memoryStore) RecordPolicyDecision(context.Context, policy.DecisionRecord) error {
	return nil
}

func (s *memoryStore) CaptureToolOutput(_ context.Context, request artifact.CaptureRequest) ([]artifact.Descriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalized, err := artifact.NormalizeCaptureRequest(request)
	if err != nil {
		return nil, err
	}
	descriptors := make([]artifact.Descriptor, 0, len(normalized.Outputs))
	for _, output := range normalized.Outputs {
		key := normalized.RunID + "\x00" + normalized.SourceID + "\x00" + string(output.Stream)
		if id := s.artifactBySource[key]; id != "" {
			descriptors = append(descriptors, s.artifacts[id].Descriptor)
			continue
		}
		now := time.Now().UTC()
		descriptor := artifact.Descriptor{
			ID: idgen.New("artifact"), RunID: normalized.RunID, SessionID: normalized.SessionID,
			WorkspaceID: normalized.WorkspaceID, SourceID: normalized.SourceID, ToolName: normalized.ToolName,
			Stream: output.Stream, Kind: artifact.KindToolOutput, MIME: output.MIME, Encoding: artifact.EncodingUTF8,
			SHA256: artifact.Hash(output.Content), SizeBytes: int64(len([]byte(output.Content))),
			Redacted: output.Redacted, CreatedAt: now,
		}
		blob := artifact.Blob{Descriptor: descriptor, Content: output.Content}
		if err := blob.Validate(); err != nil {
			return nil, err
		}
		s.artifacts[descriptor.ID] = blob
		s.artifactBySource[key] = descriptor.ID
		descriptors = append(descriptors, descriptor)
	}
	return descriptors, nil
}

func (s *memoryStore) GetRunArtifact(_ context.Context, id string) (artifact.Blob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, ok := s.artifacts[id]
	if !ok {
		return artifact.Blob{}, errors.New("artifact not found")
	}
	return blob, nil
}

func (s *memoryStore) ListRunArtifacts(_ context.Context, filter artifact.ListFilter) ([]artifact.Descriptor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var descriptors []artifact.Descriptor
	for _, blob := range s.artifacts {
		if filter.RunID != "" && blob.RunID != filter.RunID ||
			filter.SourceID != "" && blob.SourceID != filter.SourceID ||
			filter.Stream != "" && blob.Stream != filter.Stream {
			continue
		}
		descriptors = append(descriptors, blob.Descriptor)
	}
	return descriptors, nil
}

func (s *memoryStore) SaveScriptProcess(_ context.Context, process scriptprocess.Process) (scriptprocess.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.processes[process.ID] = process
	return process, nil
}

func (s *memoryStore) GetScriptProcess(_ context.Context, id string) (scriptprocess.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	process, ok := s.processes[id]
	if !ok {
		return scriptprocess.Process{}, errors.New("script process not found")
	}
	return process, nil
}

func (s *memoryStore) ListScriptProcesses(_ context.Context, filter scriptprocess.ListFilter) ([]scriptprocess.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var processes []scriptprocess.Process
	for _, process := range s.processes {
		if filter.RunID != "" && process.RunID != filter.RunID ||
			filter.SessionID != "" && process.SessionID != filter.SessionID ||
			filter.Status != "" && process.Status != filter.Status {
			continue
		}
		processes = append(processes, process)
	}
	return processes, nil
}

func (s *memoryStore) CreateScriptProcessRun(context.Context, ScriptRunStoreRequest) (ScriptRunStoreResult, error) {
	return ScriptRunStoreResult{}, errors.New("atomic script Run creation is not supported by this test store")
}

func TestGatewayExecutesScopedReadsWithRedactionAndLimits(t *testing.T) {
	root := t.TempDir()
	token := "s" + "k-" + strings.Repeat("a", 28)
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("DEEPSEEK_API_KEY="+token+"\nmore"), 0o600); err != nil {
		t.Fatal(err)
	}
	gateway := New(nil, policy.NewDefaultChecker())
	outcome, err := gateway.Invoke(context.Background(), ToolCall{
		Name: ReadFileTool, WorkspaceID: "ws-1", WorkspaceRoot: root,
		Arguments: map[string]string{"path": "notes.txt", "max_bytes": "64"}, RequestedBy: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Decision.Approval != ApprovalAutomatic || outcome.Execution == nil || outcome.Result == nil ||
		outcome.Result.Status != StatusCompleted || outcome.Result.MIME != "text/plain; charset=utf-8" {
		t.Fatalf("unexpected read outcome: %#v", outcome)
	}
	if outcome.Call.WorkspaceRoot != "" || strings.Contains(outcome.Result.Stdout, token) ||
		!strings.Contains(outcome.Result.Stdout, "[REDACTED:secret]") {
		t.Fatalf("workspace root or secret escaped the gateway: %#v", outcome)
	}
	truncated, err := gateway.Invoke(context.Background(), ToolCall{
		Name: ReadFileTool, WorkspaceID: "ws-1", WorkspaceRoot: root,
		Arguments: map[string]string{"path": "notes.txt", "max_bytes": "3"},
	})
	if err != nil || truncated.Result == nil || !truncated.Result.Truncated {
		t.Fatalf("read truncation was not projected: %#v err=%v", truncated, err)
	}
	if _, err := gateway.Invoke(context.Background(), ToolCall{
		Name: ReadFileTool, WorkspaceID: "ws-1", WorkspaceRoot: root,
		Arguments: map[string]string{"path": "notes.txt", "max_bytes": "999999"},
	}); err == nil {
		t.Fatal("expected oversized read limit rejection")
	}
}

func TestGatewayShellProposalApprovalAndPolicyDenial(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	gateway := New(store, policy.NewDefaultChecker())
	outcome, err := gateway.Invoke(ctx, ToolCall{
		Name: ShellTool, SessionID: "sess-1", WorkspaceID: "ws-1",
		Arguments: map[string]string{"command": "echo hello"}, RequestedBy: "root",
	})
	if err != nil || outcome.Proposal == nil || outcome.Proposal.Status != StatusProposed ||
		outcome.Decision.Approval != ApprovalPerCall || outcome.Execution != nil {
		t.Fatalf("unexpected shell proposal: %#v err=%v", outcome, err)
	}
	operatorProposal, err := gateway.Invoke(ctx, ToolCall{
		Name: ShellTool, SessionID: "sess-1", WorkspaceID: "ws-1",
		Arguments: map[string]string{"command": "echo decline"},
	})
	if err != nil {
		t.Fatal(err)
	}
	operatorDenied, err := gateway.Review(ctx, ReviewRequest{
		Action: ReviewDeny, Tool: ShellTool, ProposalID: operatorProposal.Proposal.ID,
	})
	if err != nil || operatorDenied.Decision.Reason != "denied by operator" ||
		operatorDenied.Result == nil || operatorDenied.Result.Stderr != "denied by operator" {
		t.Fatalf("operator denial reason was inconsistent: %#v err=%v", operatorDenied, err)
	}
	persistedOperatorDenial, err := store.GetToolRun(ctx, operatorProposal.Proposal.ID)
	if err != nil || persistedOperatorDenial.PolicyReason != "denied by operator" {
		t.Fatalf("operator denial reason was not persisted: %#v err=%v", persistedOperatorDenial, err)
	}
	reviewed, err := gateway.Review(ctx, ReviewRequest{
		Action: ReviewApprove, Tool: ShellTool, ProposalID: outcome.Proposal.ID,
	})
	if err != nil || reviewed.Proposal.Status != StatusCompleted || reviewed.Execution == nil ||
		reviewed.Execution.Backend != "dry_run" || reviewed.Result == nil ||
		!strings.Contains(reviewed.Result.Stdout, "dry run: echo hello") {
		t.Fatalf("unexpected shell review: %#v err=%v", reviewed, err)
	}
	denied, err := gateway.Invoke(ctx, ToolCall{
		Name: ShellTool, SessionID: "sess-1", WorkspaceID: "ws-1",
		Arguments: map[string]string{"command": "masscan 0.0.0.0/0"},
	})
	if err != nil || denied.Proposal == nil || denied.Proposal.Status != StatusDenied ||
		denied.Decision.Allowed || denied.Decision.Approval != ApprovalNever || denied.Result == nil {
		t.Fatalf("dangerous shell was not denied durably: %#v err=%v", denied, err)
	}
	persisted, err := store.GetToolRun(ctx, denied.Proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	mapped, mapErr := gateway.outcomeFromToolRun(t.Context(), ToolCall{RequestedBy: "test"}, persisted, errors.New("post-persist failure"))
	if mapErr == nil || mapped.Decision.Allowed || mapped.Decision.Approval != ApprovalNever {
		t.Fatalf("persisted denial changed meaning after an operation error: %#v err=%v", mapped, mapErr)
	}
}

func TestGatewayProtectedDeleteCannotBeApproved(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	gateway := New(store, policy.NewDefaultChecker())
	denied, err := gateway.Invoke(ctx, ToolCall{
		Name: ShellTool, SessionID: "sess-protected-delete", WorkspaceID: "ws-protected-delete",
		Arguments: map[string]string{"command": `rm -rf $HOME`}, RequestedBy: "root",
	})
	if err != nil || denied.Proposal == nil || denied.Proposal.Status != StatusDenied ||
		denied.Decision.Allowed || denied.Decision.Approval != ApprovalNever || denied.Decision.Risk != "critical" {
		t.Fatalf("protected deletion was not denied before approval: %#v err=%v", denied, err)
	}
	if _, err := gateway.Review(ctx, ReviewRequest{
		Action: ReviewApprove, Tool: ShellTool, ProposalID: denied.Proposal.ID,
		ReviewedBy: "operator", IdempotencyKey: "cannot-override-protected-delete",
	}); err == nil {
		t.Fatal("operator approval overrode a permanent protected-delete denial")
	}
	persisted, err := store.GetToolRun(ctx, denied.Proposal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != toolrun.StatusDenied || persisted.Stdout != "" || persisted.ExitCode != 0 {
		t.Fatalf("denied protected deletion acquired an execution result: %#v", persisted)
	}
}

// reviewPolicyFlipChecker delegates to the default checker until the operator
// flips it, modelling a Policy that changed between proposal and review.
type reviewPolicyFlipChecker struct {
	policy.DefaultChecker
	deny bool
}

func (c *reviewPolicyFlipChecker) CheckToolCall(call tools.Call) policy.Decision {
	if c.deny {
		return policy.Decision{Allowed: false, Reason: "current policy denies this command", Risk: "critical"}
	}
	return c.DefaultChecker.CheckToolCall(call)
}

func TestGatewayReviewApprovalRechecksCurrentPolicy(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	checker := &reviewPolicyFlipChecker{}
	gateway := New(store, checker)
	outcome, err := gateway.Invoke(ctx, ToolCall{
		Name: ShellTool, SessionID: "sess-recheck", WorkspaceID: "ws-recheck",
		Arguments: map[string]string{"command": "echo recheck"}, RequestedBy: "root",
	})
	if err != nil || outcome.Proposal == nil || outcome.Proposal.Status != StatusProposed {
		t.Fatalf("shell proposal was not created: %#v err=%v", outcome, err)
	}
	proposalID := outcome.Proposal.ID
	checker.deny = true
	if reviewed, err := gateway.Review(ctx, ReviewRequest{
		Action: ReviewApprove, Tool: ShellTool, ProposalID: proposalID,
		ReviewedBy: "operator", IdempotencyKey: "policy-flipped-before-review",
	}); err == nil {
		t.Fatalf("review approved a proposal the current policy denies: %#v", reviewed)
	}
	persisted, err := store.GetToolRun(ctx, proposalID)
	if err != nil || persisted.Status != toolrun.StatusProposed {
		t.Fatalf("tool run left the proposed state without a policy recheck: %#v err=%v", persisted, err)
	}
	// The review must not reach the ledger at all: no decision record, and never
	// an approved one. An absent record is the fixed behaviour.
	record, err := store.GetApprovalByProposal(ctx, proposalID)
	if err == nil && record.Status != approval.StatusPending {
		t.Fatalf("approval ledger recorded %s for a command current policy denies: %#v", record.Status, record)
	}
}

func TestGatewayFileEditProposalApprovalAndAdapterCompatibility(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	gateway := New(store, policy.NewDefaultChecker()).WithWorkspaceRootResolver(func(context.Context, string) (string, error) {
		return root, nil
	})
	otherRoot := t.TempDir()
	if _, err := gateway.Invoke(ctx, ToolCall{
		Name: ReplaceFileTool, WorkspaceID: "ws-1", WorkspaceRoot: otherRoot,
		Arguments: map[string]string{"path": "README.md", "content": "wrong root\n"},
	}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched workspace root was not rejected: %v", err)
	}
	outcome, err := gateway.Invoke(ctx, ToolCall{
		Name: ReplaceFileTool, WorkspaceID: "ws-1", WorkspaceRoot: root, SessionID: "sess-1",
		Arguments: map[string]string{"path": "README.md", "content": "new\n"}, RequestedBy: "root",
	})
	if err != nil || outcome.Proposal == nil || outcome.Proposal.Status != StatusProposed ||
		outcome.Decision.Approval != ApprovalPerCall || !strings.Contains(outcome.Proposal.Preview, "+new") {
		t.Fatalf("unexpected file proposal: %#v err=%v", outcome, err)
	}
	before, err := os.ReadFile(path)
	if err != nil || string(before) != "old\n" {
		t.Fatalf("proposal changed workspace: %q err=%v", before, err)
	}
	reviewed, err := gateway.Review(ctx, ReviewRequest{
		Action: ReviewApprove, Tool: ReplaceFileTool, ProposalID: outcome.Proposal.ID,
	})
	if err != nil || reviewed.Proposal.Status != StatusCompleted || reviewed.Execution == nil || reviewed.Result == nil ||
		reviewed.Result.Metadata["path"] != "README.md" {
		t.Fatalf("unexpected file approval: %#v err=%v", reviewed, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != "new\n" {
		t.Fatalf("approved edit was not applied: %q err=%v", after, err)
	}

	adapter := gateway.FileEdits()
	second, err := adapter.Propose(ctx, fileedit.Proposal{
		WorkspaceID: "ws-1", WorkspaceRoot: root, Path: "README.md", ProposedText: "third\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err = adapter.Deny(ctx, second.ID, "not now")
	if err != nil || second.Status != fileedit.StatusDenied || second.Reason != "not now" {
		t.Fatalf("file adapter did not use gateway review: %#v err=%v", second, err)
	}
}

func TestGatewayRejectsUnknownArgumentsAndDangerousFileProposal(t *testing.T) {
	root := t.TempDir()
	gateway := New(newMemoryStore(), policy.NewDefaultChecker())
	if _, err := gateway.Invoke(context.Background(), ToolCall{
		Name: ReadFileTool, WorkspaceID: "ws", WorkspaceRoot: root,
		Arguments: map[string]string{"path": "README.md", "extra": "value"},
	}); err == nil {
		t.Fatal("expected unknown argument rejection")
	}
	if _, err := gateway.Invoke(context.Background(), ToolCall{
		Name: ReadFileTool, InvocationID: "caller-controlled", WorkspaceID: "ws", WorkspaceRoot: root,
		Arguments: map[string]string{"path": "README.md"},
	}); err == nil || !strings.Contains(err.Error(), "generated by the gateway") {
		t.Fatalf("caller-controlled invocation id was not rejected: %v", err)
	}
	denied, err := gateway.Invoke(context.Background(), ToolCall{
		Name: ReplaceFileTool, WorkspaceID: "ws", WorkspaceRoot: root,
		Arguments: map[string]string{"path": "notes.txt", "content": "masscan 0.0.0.0/0"},
	})
	if err != nil || denied.Decision.Allowed || denied.Result == nil || denied.Result.Status != StatusDenied || denied.Proposal != nil {
		t.Fatalf("dangerous file proposal was not denied: %#v err=%v", denied, err)
	}
}

func TestBoundResultTextPreservesUTF8AndHardLimit(t *testing.T) {
	value, truncated := boundResultText(strings.Repeat("界", 100), 64)
	if !truncated || !utf8.ValidString(value) || len([]byte(value)) > 64 || !strings.Contains(value, "[truncated") {
		t.Fatalf("invalid bounded result: bytes=%d valid=%t value=%q", len([]byte(value)), utf8.ValidString(value), value)
	}
	tiny, truncated := boundResultText(strings.Repeat("abcdef", 4), 8)
	if !truncated || len([]byte(tiny)) != 8 {
		t.Fatalf("tiny hard limit was not enforced: bytes=%d value=%q", len([]byte(tiny)), tiny)
	}
}

func TestGatewayCapturesFullRedactedOutputBeforeResultTruncation(t *testing.T) {
	store := newMemoryStore()
	gateway := New(store, policy.NewDefaultChecker())
	token := "s" + "k-" + strings.Repeat("z", 28)
	now := time.Now().UTC()
	run := toolrun.ToolRun{
		ID: "tool-large-output", SessionID: "sess-large", WorkspaceID: "ws-large", ToolName: toolrun.ShellTool,
		Command: "echo large", Status: toolrun.StatusCompleted, Risk: "medium", PolicyReason: "approved by test",
		Stdout:    "secret=" + token + "\n" + string([]byte{0xff}) + strings.Repeat("界", MaxResultStdoutBytes),
		CreatedAt: now, UpdatedAt: now,
	}
	outcome, err := gateway.outcomeFromToolRun(t.Context(), ToolCall{
		RunID: "run-large", SessionID: run.SessionID, WorkspaceID: run.WorkspaceID, RequestedBy: "test",
	}, run, nil)
	if err != nil || outcome.Result == nil || !outcome.Result.Truncated || !utf8.ValidString(outcome.Result.Stdout) ||
		strings.Contains(outcome.Result.Stdout, token) || !strings.Contains(outcome.Result.Stdout, "[REDACTED:") {
		t.Fatalf("bounded result was not safely projected: %#v err=%v", outcome, err)
	}
	artifactID := outcome.Result.Metadata["artifact_stdout_id"]
	if artifactID == "" || outcome.Result.Metadata["artifact_stdout_sha256"] == "" {
		t.Fatalf("artifact reference metadata is missing: %#v", outcome.Result.Metadata)
	}
	blob, err := store.GetRunArtifact(t.Context(), artifactID)
	if err != nil || len([]byte(blob.Content)) <= len([]byte(outcome.Result.Stdout)) || !utf8.ValidString(blob.Content) ||
		strings.Contains(blob.Content, token) || !strings.Contains(blob.Content, "[REDACTED:") || !blob.Redacted {
		t.Fatalf("full redacted output was not captured: %#v err=%v", blob, err)
	}
	replayed, err := gateway.outcomeFromToolRun(t.Context(), ToolCall{
		RunID: "run-large", SessionID: run.SessionID, WorkspaceID: run.WorkspaceID, RequestedBy: "test",
	}, run, nil)
	if err != nil || replayed.Result.Metadata["artifact_stdout_id"] != artifactID || len(store.artifacts) != 1 {
		t.Fatalf("artifact capture was not idempotent: %#v err=%v artifacts=%d", replayed, err, len(store.artifacts))
	}
}
