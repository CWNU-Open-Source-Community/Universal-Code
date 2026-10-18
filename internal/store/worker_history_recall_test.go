package store

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/idgen"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/toolbudget"
)

func admitHistoryWorker(t *testing.T, st *SQLiteStore, run domain.Run, profile *domain.WorkerProfile) (domain.AgentNode, domain.SpecialistAdmission, string) {
	t.Helper()
	root, _, err := st.GetRootAgent(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.SpecialistAdmission{AgentID: idgen.New("agent"), SessionID: idgen.New("sess"), RunID: run.ID,
		ParentAgentID: root.ID, Title: "history worker", Skills: []string{"model.chat"}, TurnLimit: 3, TokenLimit: 256,
		ToolCallLimit: 6, CostLimitMicros: 700, MaxChildren: 2, Worker: profile, CreatedAt: time.Now().UTC()}
	key := idgen.New("history-admission")
	child, _, err := st.AdmitSpecialist(t.Context(), request, key)
	if err != nil {
		t.Fatal(err)
	}
	return child, request, key
}

func startHistoryWorker(t *testing.T, st *SQLiteStore, run domain.Run, child domain.AgentNode, lease domain.RunExecutionLease, batch ...domain.BatchDeliveryWorkspace) domain.AgentAttemptRef {
	t.Helper()
	a, _, err := st.BeginSpecialistAttempt(t.Context(), domain.AgentAttemptStart{AttemptID: idgen.New("attempt"), RunID: run.ID,
		AgentID: child.ID, ParentAgentID: child.ParentID, Lease: lease, StartedAt: time.Now().UTC()}, idgen.New("history-start"))
	if err != nil {
		t.Fatal(err)
	}
	ref := attemptRef(a)
	brief, err := st.PrepareSpecialistContext(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	mission, err := st.GetMission(t.Context(), run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := domain.WorkerProfileJSON(child.Worker)
	if err != nil {
		t.Fatal(err)
	}
	binding := domain.SpecialistToolBinding{Ref: ref, SessionID: child.SessionID,
		WorkspaceID: mission.WorkspaceID, RootFingerprint: strings.Repeat("a", 64), BriefFingerprint: brief.TaskBrief.Fingerprint,
		Mode: "worker", WorkerProfileJSON: profile}
	if len(batch) != 0 {
		binding.BatchPlanID, binding.BatchOrdinal, binding.BatchGeneration = batch[0].PlanID, batch[0].Ordinal, batch[0].Generation
	}
	if err := st.BindSpecialistTools(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	return ref
}

func workerHistoryFind(t *testing.T, st *SQLiteStore, ref domain.AgentAttemptRef, marker, kind string) domain.HistoryRecord {
	t.Helper()
	page, err := st.SearchWorkerHistory(t.Context(), ref, domain.HistorySearchRequest{Query: marker})
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Records {
		if record.Kind == kind {
			return record
		}
	}
	t.Fatalf("worker history missing %q %q: %+v", marker, kind, page)
	return domain.HistoryRecord{}
}

func workerHistoryReadAll(t *testing.T, st *SQLiteStore, ref domain.AgentAttemptRef, source, part string, limit int) string {
	t.Helper()
	request := domain.HistoryReadRequest{SourceID: source, Part: part, Limit: limit}
	var result strings.Builder
	for {
		page, err := st.ReadWorkerHistory(t.Context(), ref, request)
		if err != nil {
			t.Fatal(err)
		}
		if page.InstructionAuthorized || page.Record.OriginalInstructionAuthorized || !utf8.ValidString(page.Content) ||
			len(page.Content) > limit || page.Offset != request.Offset || page.NextOffset != page.Offset+len(page.Content) {
			t.Fatalf("worker history page changed framing: %+v", page)
		}
		result.WriteString(page.Content)
		if !page.HasMore {
			if result.Len() != page.TotalBytes || session.ContentSHA256(result.String()) != page.ContentSHA256 {
				t.Fatal("worker history digest changed")
			}
			return result.String()
		}
		if page.NextOffset <= request.Offset {
			t.Fatal("worker history page made no progress")
		}
		request.Offset, request.ExpectedSHA256 = page.NextOffset, page.ContentSHA256
	}
}

func TestWorkerHistoryOwnConversationReceiptsAndFences(t *testing.T) {
	st := openWorkItemTestStore(t)
	run, child, attempt, _, receipt, lease := workerToolFixture(t, st, &domain.WorkerProfile{})
	ref := attemptRef(attempt)
	secret := "sk-" + strings.Repeat("q", 28)
	message, err := st.SaveSessionMessage(t.Context(), session.NewMessage(child.SessionID, "user", "自身历史标记 "+strings.Repeat("中文🙂原文。", 70)+" token="+secret))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkSessionMessagesCompacted(t.Context(), child.SessionID, message.ID); err != nil {
		t.Fatal(err)
	}
	parentMessage := historyMessage(t, st, run, "父私有标记 parent raw history")
	sibling, _, _ := admitHistoryWorker(t, st, run, &domain.WorkerProfile{})
	siblingMessage, err := st.SaveSessionMessage(t.Context(), session.NewMessage(sibling.SessionID, "user", "兄弟私有标记"))
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status, receipt.ResultJSON = domain.SupervisorToolCompleted, `{"output":"自身回执标记 中文🙂"}`
	if err := st.CompleteSpecialistToolCall(t.Context(), receipt); err != nil {
		t.Fatal(err)
	}
	record := workerHistoryFind(t, st, ref, "自身历史标记", "message")
	if record.MessageID != message.ID || !record.Compacted || record.SessionID != child.SessionID {
		t.Fatalf("wrong child message: %+v", record)
	}
	if got := workerHistoryReadAll(t, st, ref, record.SourceID, "content", 31); got != message.Content || strings.Contains(got, secret) {
		t.Fatal("child history did not preserve redacted bytes")
	}
	tool := workerHistoryFind(t, st, ref, "自身回执标记", "tool_call")
	if got := workerHistoryReadAll(t, st, ref, tool.SourceID, "result", 19); got != receipt.ResultJSON {
		t.Fatal("receipt result changed")
	}
	if got := workerHistoryReadAll(t, st, ref, tool.SourceID, "arguments", 19); got != receipt.PayloadJSON {
		t.Fatal("receipt arguments changed")
	}
	for _, marker := range []string{"父私有标记", "兄弟私有标记"} {
		page, err := st.SearchWorkerHistory(t.Context(), ref, domain.HistorySearchRequest{Query: marker})
		if err != nil || len(page.Records) != 0 {
			t.Fatalf("undelegated history %q leaked: %+v %v", marker, page, err)
		}
	}
	// Tokens are lookup hints, including attacker-crafted digests of known text.
	for _, outside := range []session.Message{parentMessage, siblingMessage} {
		forged := workerHistorySourceID(workerHistoryScope{ref: ref}, "message", strconv.FormatInt(outside.ID, 10), map[string]string{"content": outside.Content})
		if _, err := st.ReadWorkerHistory(t.Context(), ref, domain.HistoryReadRequest{SourceID: forged}); apperror.CodeOf(err) != apperror.CodeNotFound {
			t.Fatalf("foreign session source accepted: %v", err)
		}
	}
	if _, err := st.ReadWorkerHistory(t.Context(), ref, domain.HistoryReadRequest{SourceID: "message:" + strconv.FormatInt(parentMessage.ID, 10)}); err == nil {
		t.Fatal("root source ID accepted")
	}
	if _, err := st.ReadWorkerHistory(t.Context(), ref, domain.HistoryReadRequest{SourceID: record.SourceID, Offset: 1, ExpectedSHA256: record.ContentSHA256}); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("mid-rune page accepted: %v", err)
	}
	if _, err := st.ReadWorkerHistory(t.Context(), ref, domain.HistoryReadRequest{SourceID: record.SourceID, Offset: 3}); err == nil {
		t.Fatal("digestless continuation accepted")
	}
	if _, err := st.ReadWorkerHistory(t.Context(), ref, domain.HistoryReadRequest{SourceID: record.SourceID, ExpectedSHA256: strings.Repeat("b", 64)}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("wrong digest accepted: %v", err)
	}
	if _, _, err := st.ReleaseRunExecutionLease(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReadWorkerHistory(t.Context(), ref, domain.HistoryReadRequest{SourceID: record.SourceID}); err == nil {
		t.Fatal("expired lease read history")
	}
}

func TestWorkerHistoryDeliveredTaskProjection(t *testing.T) {
	st, run, _ := historyFixture(t)
	if _, err := application.NewRunService(st).Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	child, _, _ := admitHistoryWorker(t, st, run, &domain.WorkerProfile{})
	root, _, _ := st.GetRootAgent(t.Context(), run.ID)
	f := specialistAttemptFixture{Run: run, Root: root, Child: child}
	retired := sendBriefOperation(t, st, f, "append", "从未交付的已撤回内容", domain.AgentMessage{})
	sendBriefOperation(t, st, f, "withdraw", "", retired)
	instruction := sendBriefOperation(t, st, f, "append", "明确交付指令标记 中文🙂", domain.AgentMessage{})
	work, err := application.NewWorkItemService(st).Create(t.Context(), application.CreateWorkItemRequest{RunID: run.ID, OwnerAgentID: child.ID, Title: "明确交付工作标记", Description: "完整工作描述"})
	if err != nil {
		t.Fatal(err)
	}
	ref := startHistoryWorker(t, st, run, child, acquireTestRunExecutionLease(t, t.Context(), st, run.ID))
	sendBriefOperation(t, st, f, "append", "未交付父消息标记", domain.AgentMessage{})
	record := workerHistoryFind(t, st, ref, "明确交付指令标记", "parent_instruction")
	if record.SourceRef != instruction.ID || workerHistoryReadAll(t, st, ref, record.SourceID, "content", 19) != "明确交付指令标记 中文🙂" {
		t.Fatal("delivered instruction projection changed")
	}
	brief := workerHistoryFind(t, st, ref, "明确交付工作标记", "task_brief")
	body := workerHistoryReadAll(t, st, ref, brief.SourceID, "content", 91)
	if !strings.Contains(body, work.ID) || strings.Contains(body, "owner_agent_id") || strings.Contains(body, "source_refs") {
		t.Fatalf("brief did not use delivered work projection: %s", body)
	}
	for _, marker := range []string{"从未交付的已撤回内容", "未交付父消息标记"} {
		page, err := st.SearchWorkerHistory(t.Context(), ref, domain.HistorySearchRequest{Query: marker})
		if err != nil || len(page.Records) != 0 {
			t.Fatalf("undelivered source leaked: %+v %v", page, err)
		}
	}
	if _, err := st.RevokeSpecialistReadTools(t.Context(), run.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReadWorkerHistory(t.Context(), ref, domain.HistoryReadRequest{SourceID: record.SourceID}); err == nil {
		t.Fatal("revoked child read history")
	}
}

func TestWorkerHistoryCursorKeepsOwnMessageAndReceiptWatermarks(t *testing.T) {
	st := openWorkItemTestStore(t)
	_, child, attempt, _, pending, _ := workerToolFixture(t, st, &domain.WorkerProfile{})
	ref := attemptRef(attempt)
	for i := 0; i < domain.HistorySearchScanLimit+3; i++ {
		if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(child.SessionID, "user", fmt.Sprintf("分页原始消息 %03d", i))); err != nil {
			t.Fatal(err)
		}
	}
	first, err := st.SearchWorkerHistory(t.Context(), ref, domain.HistorySearchRequest{Query: "尚未存在标记"})
	if err != nil || first.Scanned != domain.HistorySearchScanLimit || !first.HasMore || len(first.Records) != 0 {
		t.Fatalf("bounded scan: %+v %v", first, err)
	}
	if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(child.SessionID, "user", "尚未存在标记 late message")); err != nil {
		t.Fatal(err)
	}
	pending.Status, pending.ResultJSON = domain.SupervisorToolCompleted, `{"output":"尚未存在标记 late receipt"}`
	if err := st.CompleteSpecialistToolCall(t.Context(), pending); err != nil {
		t.Fatal(err)
	}
	second, err := st.SearchWorkerHistory(t.Context(), ref, domain.HistorySearchRequest{Query: "尚未存在标记", Cursor: first.NextCursor})
	if err != nil || second.HasMore || len(second.Records) != 0 {
		t.Fatalf("cursor admitted late source: %+v %v", second, err)
	}
	fresh, err := st.SearchWorkerHistory(t.Context(), ref, domain.HistorySearchRequest{Query: "尚未存在标记", Cursor: second.SnapshotCursor})
	if err != nil || len(fresh.Records) != 0 {
		t.Fatalf("snapshot replay widened: %+v %v", fresh, err)
	}
	if _, err := st.SearchWorkerHistory(t.Context(), ref, domain.HistorySearchRequest{Query: "different", Cursor: first.NextCursor}); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("cursor rebound: %v", err)
	}
}

func TestWorkerHistorySkipsReceiptsWithExpiredPermission(t *testing.T) {
	st := openWorkItemTestStore(t)
	run, child, attempt, _, receipt, _ := workerToolFixture(t, st, &domain.WorkerProfile{})
	ref := attemptRef(attempt)
	permission, err := st.GetRunExecutionPermission(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := json.Marshal(map[string]any{"permission_snapshot_id": permission.ID, "permission_revision": permission.Revision + 1, "permission_mode": permission.Mode})
	receipt, err = st.BindSpecialistToolAuthority(t.Context(), ref, receipt.CallID, string(pin))
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status, receipt.ResultJSON, receipt.ErrorCode = domain.SupervisorToolFailed, `{"output":"旧权限回执标记"}`, "policy_denied"
	if err := st.CompleteSpecialistToolCall(t.Context(), receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(child.SessionID, "user", "当前自身历史标记")); err != nil {
		t.Fatal(err)
	}
	workerHistoryFind(t, st, ref, "当前自身历史标记", "message")
	page, err := st.SearchWorkerHistory(t.Context(), ref, domain.HistorySearchRequest{Query: "旧权限回执标记"})
	if err != nil || len(page.Records) != 0 {
		t.Fatalf("expired receipt broke search or leaked: %+v %v", page, err)
	}
	var rowID int64
	if err := st.db.QueryRow(`SELECT rowid FROM specialist_tool_calls WHERE agent_attempt_id=? AND call_id=?`, ref.AttemptID, receipt.CallID).Scan(&rowID); err != nil {
		t.Fatal(err)
	}
	source := workerHistorySourceID(workerHistoryScope{ref: ref}, "tool", strconv.FormatInt(rowID, 10), map[string]string{"arguments": receipt.PayloadJSON, "result": receipt.ResultJSON})
	if _, err := st.ReadWorkerHistory(t.Context(), ref, domain.HistoryReadRequest{SourceID: source}); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("expired receipt read did not deny: %v", err)
	}
}

func TestWorkerHistorySkillAndBatchReceiptAuthorities(t *testing.T) {
	for _, test := range []struct {
		name      string
		authority string
		tool      string
		change    func(map[string]any)
		valid     bool
		unknown   bool
	}{
		{name: "skill", authority: "skill", tool: "skill_read", valid: true},
		{name: "batch", authority: "batch", tool: "workspace_read", valid: true},
		{name: "skill foreign attempt", authority: "skill", tool: "skill_read", change: func(a map[string]any) {
			ref := a["ref"].(domain.AgentAttemptRef)
			ref.AttemptID = "another-attempt"
			a["ref"] = ref
		}},
		{name: "skill wrong tool", authority: "skill", tool: "workspace_read"},
		{name: "batch foreign generation", authority: "batch", tool: "workspace_read", change: func(a map[string]any) {
			binding := a["binding"].(domain.SpecialistToolBinding)
			binding.BatchGeneration++
			a["binding"] = binding
		}},
		{name: "batch wrong tool", authority: "batch", tool: "skill_read"},
		{name: "unknown version still requires permission", authority: "skill", tool: "skill_read", unknown: true, change: func(a map[string]any) {
			a["version"] = "unknown-authority"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, plan, workspaces := newBatchDeliveryStoreFixture(t, &domain.WorkerProfile{})
			if _, _, _, err := st.CreateBatchDeliveryPlan(t.Context(), plan, workspaces); err != nil {
				t.Fatal(err)
			}
			run, err := st.GetRun(t.Context(), plan.RunID)
			if err != nil {
				t.Fatal(err)
			}
			child, err := st.GetAgentNode(t.Context(), workspaces[0].AgentID)
			if err != nil {
				t.Fatal(err)
			}
			lease := acquireTestRunExecutionLease(t, t.Context(), st, run.ID)
			ref := startHistoryWorker(t, st, run, child, lease, workspaces[0])
			binding, err := st.GetSpecialistToolBinding(t.Context(), ref)
			if err != nil {
				t.Fatal(err)
			}
			mode, err := st.GetRunMode(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			authority := map[string]any{"version": "worker_skill_read.v1", "ref": ref, "parent_id": child.ParentID,
				"mode_id": mode.ID, "mode_revision": mode.Revision}
			if test.authority == "batch" {
				authority = map[string]any{"version": "worker-batch-authority.v1", "binding": binding}
			}
			if test.change != nil {
				test.change(authority)
			}
			payload := json.RawMessage(`{"catalog":true}`)
			if test.tool == "workspace_read" {
				payload = json.RawMessage(`{"version":"agent-code-tools.v1","path":"internal/one/proof.txt","start_line":1,"end_line":2}`)
			}
			model := llm.ModelAttempt{Number: 1, TransportAttempt: 1, MaxAttempts: 1, Provider: "history-authority", Model: "fixture"}
			if _, err := st.RecordSpecialistModelStarted(t.Context(), ref, model); err != nil {
				t.Fatal(err)
			}
			response := llm.ChatResponse{ToolCalls: []llm.ToolCall{{ID: "own-receipt", Name: test.tool, Arguments: payload}}}
			if _, err := st.RecordSpecialistToolModelCompleted(t.Context(), ref, model, response, "read owned evidence", policy.Decision{Allowed: true, Risk: "low", Reason: "fixture"}); err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(authority)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := st.BindSpecialistToolAuthority(t.Context(), ref, "own-receipt", string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.ChargeToolCall(t.Context(), toolbudget.ChargeRequest{RunID: run.ID, AgentID: child.ID,
				AgentAttemptID: ref.AttemptID, ToolCallID: receipt.CallID, SessionID: child.SessionID, WorkspaceID: binding.WorkspaceID,
				ToolName: test.tool, ActionClass: "run_memory", LeaseID: lease.LeaseID, LeaseGeneration: lease.Generation, RequestedBy: "specialist_runner"}); err != nil {
				t.Fatal(err)
			}
			receipt, err = st.MarkSpecialistToolStarted(t.Context(), ref, receipt.CallID, receipt.AuthorityJSON)
			if err != nil {
				t.Fatal(err)
			}
			receipt.Status, receipt.ResultJSON = domain.SupervisorToolCompleted, `{"output":"自身完整回执标记 中文🙂"}`
			if err := st.CompleteSpecialistToolCall(t.Context(), receipt); err != nil {
				t.Fatal(err)
			}
			page, err := st.SearchWorkerHistory(t.Context(), ref, domain.HistorySearchRequest{Query: "自身完整回执标记"})
			if test.valid {
				if err != nil || len(page.Records) != 1 || page.Records[0].CallID != receipt.CallID {
					t.Fatalf("owned receipt missing: %+v %v", page, err)
				}
				for part, want := range map[string]string{"arguments": receipt.PayloadJSON, "result": receipt.ResultJSON} {
					if got := workerHistoryReadAll(t, st, ref, page.Records[0].SourceID, part, 17); got != want {
						t.Fatalf("%s changed: %q", part, got)
					}
				}
				return
			}
			if test.unknown && err == nil || !test.unknown && (err != nil || len(page.Records) != 0) {
				t.Fatalf("invalid authority search: %+v %v", page, err)
			}
			var rowID int64
			if err := st.db.QueryRow(`SELECT rowid FROM specialist_tool_calls WHERE agent_attempt_id=? AND call_id=?`, ref.AttemptID, receipt.CallID).Scan(&rowID); err != nil {
				t.Fatal(err)
			}
			source := workerHistorySourceID(workerHistoryScope{ref: ref}, "tool", strconv.FormatInt(rowID, 10), map[string]string{"arguments": receipt.PayloadJSON, "result": receipt.ResultJSON})
			if _, err := st.ReadWorkerHistory(t.Context(), ref, domain.HistoryReadRequest{SourceID: source}); err == nil || !test.unknown && apperror.CodeOf(err) != apperror.CodePolicyDenied {
				t.Fatalf("invalid authority read was not rejected: %v", err)
			}
		})
	}
}

func TestWorkerHistoryParentSnapshotSealsAdmissionThreadLineage(t *testing.T) {
	st, run, path := historyFixture(t)
	if _, err := application.NewRunService(st).Start(t.Context(), run.ID); err != nil {
		t.Fatal(err)
	}
	original := historyMessage(t, st, run, "委派父历史标记 "+strings.Repeat("中文🙂", 30))
	sourceRef := "archive/token=sk-" + strings.Repeat("q", 28)
	if _, err := st.SaveSessionMessage(t.Context(), session.NewEvidenceMessage(run.SessionID, session.SourceWorkspaceFile, sourceRef, "父来源元数据标记")); err != nil {
		t.Fatal(err)
	}
	note := historyNote(t, st, run, "父可见笔记", "委派父笔记标记 "+strings.Repeat("内容", 30), domain.NoteVisibilityRoot, "")
	historyNote(t, st, run, "子私有笔记", "兄弟私有笔记标记", domain.NoteVisibilityOwner, "specialist")
	historyMemory(t, st, "worker-user-memory", "", "跨会话记忆标记 user")
	historyMemory(t, st, "worker-project-memory", "ws-history", "跨会话记忆标记 project")
	sibling, _, _ := admitHistoryWorker(t, st, run, &domain.WorkerProfile{})
	if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(sibling.SessionID, "user", "兄弟原始会话标记")); err != nil {
		t.Fatal(err)
	}
	child, admission, key := admitHistoryWorker(t, st, run, &domain.WorkerProfile{HistoryScope: domain.WorkerHistoryParentSnapshot})
	var before string
	if err := st.db.QueryRow(`SELECT sources_json FROM worker_parent_history_snapshots WHERE agent_id=?`, child.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	late := historyMessage(t, st, run, "admission之后父消息标记")
	replayed, replay, err := st.AdmitSpecialist(t.Context(), admission, key)
	if err != nil || !replay || replayed.ID != child.ID {
		t.Fatalf("admission replay failed: %+v %v", replayed, err)
	}
	var after string
	if err := st.db.QueryRow(`SELECT sources_json FROM worker_parent_history_snapshots WHERE agent_id=?`, child.ID).Scan(&after); err != nil || before != after {
		t.Fatalf("replay expanded snapshot: %v", err)
	}
	admission.Worker = &domain.WorkerProfile{}
	if _, _, err := st.AdmitSpecialist(t.Context(), admission, key); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("admission history scope was mutable: %v", err)
	}
	ref := startHistoryWorker(t, st, run, child, acquireTestRunExecutionLease(t, t.Context(), st, run.ID))
	record := workerHistoryFind(t, st, ref, "委派父历史标记", "message")
	provenance := workerHistoryFind(t, st, ref, "父来源元数据标记", "message")
	if !provenance.SourceRefRedacted || provenance.SourceRefSHA256 != session.ContentSHA256(sourceRef) || provenance.SourceRef == sourceRef {
		t.Fatalf("parent provenance was re-redacted or lost its original digest: %+v", provenance)
	}
	if got := workerHistoryReadAll(t, st, ref, record.SourceID, "content", 31); got != original.Content {
		t.Fatal("parent snapshot changed stored bytes")
	}
	for _, marker := range []string{"admission之后父消息标记", "兄弟原始会话标记", "兄弟私有笔记标记", "跨会话记忆标记"} {
		page, err := st.SearchWorkerHistory(t.Context(), ref, domain.HistorySearchRequest{Query: marker})
		if err != nil || len(page.Records) != 0 {
			t.Fatalf("parent snapshot leaked %q: %+v %v", marker, page, err)
		}
	}
	forged := workerHistorySourceID(workerHistoryScope{ref: ref}, "message", strconv.FormatInt(late.ID, 10), map[string]string{"content": late.Content})
	if _, err := st.ReadWorkerHistory(t.Context(), ref, domain.HistoryReadRequest{SourceID: forged}); err == nil {
		t.Fatal("late parent message bypassed snapshot")
	}
	noteRecord := workerHistoryFind(t, st, ref, "委派父笔记标记", "note")
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := workerHistoryReadAll(t, reopened, ref, record.SourceID, "content", 97); got != original.Content {
		t.Fatal("restart changed snapshot")
	}
	note.Content = "父笔记现在已变更"
	note.Version++
	note.UpdatedAt = time.Now().UTC()
	if err := reopened.UpdateNote(t.Context(), note, note.Version-1, newNoteChangedEvent(t, run.MissionID, note, domain.NoteActive)); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ReadWorkerHistory(t.Context(), ref, domain.HistoryReadRequest{SourceID: noteRecord.SourceID}); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed parent note remained readable: %v", err)
	}
	if _, err := reopened.db.Exec(`UPDATE worker_parent_history_snapshots SET sources_json='[]' WHERE agent_id=?`, child.ID); err == nil {
		t.Fatal("snapshot could be overwritten")
	}
}

func TestSchemaV209KeepsExistingWorkerHistoryDelegated(t *testing.T) {
	st := openWorkItemTestStore(t)
	_, child, _, _, _, _ := workerToolFixture(t, st, &domain.WorkerProfile{})
	path := filepath.Join(t.TempDir(), "worker-history-v208.db")
	legacy := historicalTestDatabaseFromSeed(t, st, path, 208)
	legacy.Close()
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	stored, err := upgraded.GetAgentNode(t.Context(), child.ID)
	if err != nil || stored.Worker == nil || stored.Worker.HistoryScope != "" || stored.Worker.EffectiveHistoryScope() != domain.WorkerHistoryDelegated {
		t.Fatalf("upgrade widened legacy worker: %+v %v", stored, err)
	}
	var count int
	if err := upgraded.db.QueryRow(`SELECT count(*) FROM worker_parent_history_snapshots`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("upgrade captured parent history retroactively: %d %v", count, err)
	}
	assertNoForeignKeyViolations(t, upgraded.db)
}

func TestWorkerHistoryParentSnapshotSupportsOrdinaryRun(t *testing.T) {
	st, other, _ := historyFixture(t)
	foreign := historyMessage(t, st, other, "其他父会话隔离标记")
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "ordinary parent snapshot", Profile: "code", WorkspaceID: "ws-history"})
	if err != nil {
		t.Fatal(err)
	}
	var threadCount int
	// Ordinary Run creation and schema129 migration both bind the root Session
	// to a Thread. Exercise that public contract without an alternate scope.
	if err := st.db.QueryRow(`SELECT count(*) FROM thread_runs WHERE run_id=?`, run.ID).Scan(&threadCount); err != nil || threadCount != 1 {
		t.Fatalf("ordinary Run lacks its atomic Thread binding: %d %v", threadCount, err)
	}
	original := historyMessage(t, st, run, "单Run父历史标记 中文🙂")
	summary := historyStoredSummary(t, st, run, "单Run摘要标记")
	note := historyNote(t, st, run, "单Run笔记", "单Run可见笔记标记", domain.NoteVisibilityRoot, "")
	historyNote(t, st, run, "私有笔记", "单Run子私有标记", domain.NoteVisibilityOwner, "specialist")
	historyMemory(t, st, "standalone-memory", "ws-history", "单Run跨会话记忆标记")
	checkpoint, callID := prepareHistoryResultCall(t, st, run, "note_create", map[string]string{"title": "receipt fixture", "content": "单Run工具参数标记"})
	receipt, _, err := st.RecordSupervisorToolResult(t.Context(), checkpoint, domain.SupervisorToolResult{
		CallID: callID, Status: domain.SupervisorToolCompleted, ResultJSON: `{"stdout":"单Run父回执标记"}`, CompletedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	_, sequence, err := st.ClaimSupervisorMidTurnSteering(t.Context(), checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	model := llm.ModelAttempt{Number: 2, ToolRound: 1, SteeringSequence: sequence, TransportAttempt: 1, MaxAttempts: 1, Provider: "test", Model: "model"}
	if _, err := st.RecordSupervisorModelStarted(t.Context(), checkpoint, model); err != nil {
		t.Fatal(err)
	}
	model.Outcome = llm.OutcomeSuccess
	response := llm.ChatResponse{Text: "ready to delegate", Provider: "test", Model: "model"}
	checkpoint, err = st.RecordSupervisorModelCompleted(t.Context(), checkpoint, model, response)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.CompleteSupervisorTurn(t.Context(), checkpoint, response,
		domain.RootAction{Version: domain.RootLifecycleVersion, Kind: domain.RootActionContinue, Message: response.Text}, policy.Decision{Allowed: true}, 0); err != nil {
		t.Fatal(err)
	}
	sibling, _, _ := admitHistoryWorker(t, st, run, &domain.WorkerProfile{})
	if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(sibling.SessionID, "user", "单Run兄弟原文标记")); err != nil {
		t.Fatal(err)
	}
	child, _, _ := admitHistoryWorker(t, st, run, &domain.WorkerProfile{HistoryScope: domain.WorkerHistoryParentSnapshot})
	historyMessage(t, st, run, "单Run admission之后标记")
	lease, found, err := st.GetRunExecutionLease(t.Context(), run.ID)
	if err != nil || !found {
		t.Fatalf("missing fixture lease: %v", err)
	}
	ref := startHistoryWorker(t, st, run, child, lease)
	for _, expected := range []struct{ query, kind, part, body string }{
		{"单Run父历史标记", "message", "content", original.Content},
		{"单Run摘要标记", "summary", "content", summary.Content},
		{"单Run可见笔记标记", "note", "content", note.Content},
		{"单Run父回执标记", "tool_call", "result", receipt.ResultJSON},
		{"单Run工具参数标记", "tool_call", "arguments", receipt.PayloadJSON},
	} {
		record := workerHistoryFind(t, st, ref, expected.query, expected.kind)
		if got := workerHistoryReadAll(t, st, ref, record.SourceID, expected.part, 43); got != expected.body {
			t.Fatalf("standalone source changed: %q", expected.kind)
		}
	}
	for _, marker := range []string{"其他父会话隔离标记", "单Run子私有标记", "单Run跨会话记忆标记", "单Run兄弟原文标记", "单Run admission之后标记"} {
		page, err := st.SearchWorkerHistory(t.Context(), ref, domain.HistorySearchRequest{Query: marker})
		if err != nil || len(page.Records) != 0 {
			t.Fatalf("standalone parent snapshot leaked %q: %+v %v", marker, page, err)
		}
	}
	forged := workerHistorySourceID(workerHistoryScope{ref: ref}, "message", strconv.FormatInt(foreign.ID, 10), map[string]string{"content": foreign.Content})
	if _, err := st.ReadWorkerHistory(t.Context(), ref, domain.HistoryReadRequest{SourceID: forged}); err == nil {
		t.Fatal("standalone snapshot opened a foreign Session")
	}
}
