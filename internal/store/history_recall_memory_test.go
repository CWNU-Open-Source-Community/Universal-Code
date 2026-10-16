package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

func historyMemory(t *testing.T, st *SQLiteStore, id, workspaceID, content string) contextmgr.Memory {
	t.Helper()
	scope, scopeID := contextmgr.MemoryScopeProject, workspaceID
	if workspaceID == "" {
		scope, scopeID = contextmgr.MemoryScopeUser, contextmgr.LocalUserMemoryScope
	}
	memory, err := contextmgr.PrepareMemory(contextmgr.CreateMemoryRequest{
		ID: id, Scope: scope, ScopeID: scopeID, Title: "已确认记忆", Content: content,
		SourceRef: "operator-review/" + id, RequestedBy: "test_operator", ExplicitOperator: true,
	}, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateContextMemory(t.Context(), memory); err != nil {
		t.Fatal(err)
	}
	return memory
}

func historyMemoryUpdate(t *testing.T, st *SQLiteStore, memory contextmgr.Memory, request contextmgr.UpdateMemoryRequest) contextmgr.Memory {
	t.Helper()
	request.ExpectedVersion, request.RequestedBy = memory.Version, "test_operator"
	updated, err := contextmgr.UpdateMemory(memory, request, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateContextMemory(t.Context(), updated, memory.Version); err != nil {
		t.Fatal(err)
	}
	return updated
}

func historyNote(t *testing.T, st *SQLiteStore, run domain.Run, title, content string, visibility domain.NoteVisibility, owner string) domain.Note {
	t.Helper()
	note := newNoteTest(run.ID, title, content)
	note.Visibility, note.Owner = visibility, owner
	// These relation values must never become read routing metadata or content.
	note.SourceRefs = []string{"C:/private/.env", "terminal-input", "file:///credentials.txt"}
	note.EvidenceIDs = []string{"credential-link"}
	if err := st.CreateNote(t.Context(), note, newNoteCreatedEvent(t, run.MissionID, note)); err != nil {
		t.Fatal(err)
	}
	return note
}

func historyFindMemory(t *testing.T, st *SQLiteStore, run domain.Run, marker string) domain.HistoryRecord {
	t.Helper()
	result, err := st.SearchThreadHistory(t.Context(), run.ID, domain.HistorySearchRequest{Query: marker})
	if err != nil || len(result.Records) != 1 {
		t.Fatalf("find %q: records=%+v err=%v", marker, result.Records, err)
	}
	return result.Records[0]
}

func TestHistoryRecallConfirmedMemoryCrossesThreadsOnlyWithinWorkspace(t *testing.T) {
	st, first, _ := historyFixture(t)
	project := historyMemory(t, st, "memory-project", "ws-history", "项目记忆回读标记："+strings.Repeat("保留中文🙂。", 70))
	user := historyMemory(t, st, "memory-user", "", "用户记忆回读标记：中文答复")
	historyMemory(t, st, "memory-foreign", "ws-other", "其他项目记忆回读标记")
	disabled := historyMemory(t, st, "memory-disabled", "ws-history", "禁用记忆回读标记")
	status := contextmgr.MemoryStatusDisabled
	historyMemoryUpdate(t, st, disabled, contextmgr.UpdateMemoryRequest{Status: &status})
	expired := historyMemory(t, st, "memory-expired", "ws-history", "过期记忆回读标记")
	retention := time.Now().UTC().Add(-time.Minute)
	expiry := &retention
	historyMemoryUpdate(t, st, expired, contextmgr.UpdateMemoryRequest{RetentionUntil: &expiry})
	historyNote(t, st, first, "原 Thread 笔记", "独立Thread原笔记隔离标记", domain.NoteVisibilityRun, "")

	oldSource := historyFindMemory(t, st, first, "项目记忆回读标记")
	next := historyNewRun(t, st, "ws-history")
	for _, memory := range []contextmgr.Memory{project, user} {
		record := historyFindMemory(t, st, next, memory.Content[:12])
		if record.Kind != "context_memory" || record.MemoryID != memory.ID || record.SourceVersion != memory.Version || record.Status != "active" || record.Scope != string(memory.Scope) || record.ScopeID != memory.ScopeID || record.ProvenanceVersion != contextmgr.MemoryProtocolVersion || record.SourceRef != memory.SourceRef || record.OriginalInstructionAuthorized {
			t.Fatalf("memory provenance=%+v", record)
		}
		if got := historyReadAll(t, st, next.ID, record, "content", 67); got != memory.Content {
			t.Fatal("memory pages changed exact confirmed bytes")
		}
	}
	for _, marker := range []string{"禁用记忆回读标记", "过期记忆回读标记", "其他项目记忆回读标记", "独立Thread原笔记隔离标记"} {
		result, err := st.SearchThreadHistory(t.Context(), next.ID, domain.HistorySearchRequest{Query: marker})
		if err != nil || len(result.Records) != 0 {
			t.Fatalf("invisible source %q leaked: %+v %v", marker, result, err)
		}
	}
	if _, err := st.ReadThreadHistory(t.Context(), next.ID, domain.HistoryReadRequest{SourceID: oldSource.SourceID}); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("source token crossed the current Run binding: %v", err)
	}
	if err := st.SaveWorkspace(t.Context(), WorkspaceRecord{ID: "ws-other", Name: "Other", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, foreign := range []domain.Run{historyNewRun(t, st, "ws-other"), historyNewRun(t, st, "")} {
		result, err := st.SearchThreadHistory(t.Context(), foreign.ID, domain.HistorySearchRequest{Query: "项目记忆回读标记："})
		if err != nil || len(result.Records) != 0 {
			t.Fatalf("project memory crossed Workspace: %+v %v", result, err)
		}
		if record := historyFindMemory(t, st, foreign, "用户记忆回读标记"); record.MemoryID != user.ID {
			t.Fatalf("local user memory unavailable in allowed scope: %+v", record)
		}
	}
	// No application write is needed for either search or exact memory reads.
	st.db.SetMaxOpenConns(1)
	if _, err := st.db.Exec(`PRAGMA query_only=ON`); err != nil {
		t.Fatal(err)
	}
	record := historyFindMemory(t, st, next, "项目记忆回读标记")
	if got := historyReadAll(t, st, next.ID, record, "content", 67); got != project.Content {
		t.Fatal("query-only memory recall changed the source")
	}
}

func TestHistoryRecallNotesFollowLegalRunLineageAndRootVisibility(t *testing.T) {
	st, first, _ := historyFixture(t)
	visible := []domain.Note{
		historyNote(t, st, first, "Run笔记", "笔记血缘回读标记 first", domain.NoteVisibilityRun, ""),
		historyNote(t, st, first, "Root笔记", "笔记血缘回读标记 root", domain.NoteVisibilityRoot, ""),
		historyNote(t, st, first, "Root自有笔记", "笔记血缘回读标记 owned", domain.NoteVisibilityOwner, "root"),
	}
	root, _, err := st.RegisterRootAgent(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	rootOwned := newNoteTest(first.ID, "Root Agent自有笔记", "笔记血缘回读标记 agent-owned")
	rootOwned.Visibility, rootOwned.Owner, rootOwned.OwnerAgentID = domain.NoteVisibilityOwner, root.ID, root.ID
	if err := st.CreateNote(t.Context(), rootOwned, newNoteCreatedEvent(t, first.MissionID, rootOwned)); err != nil {
		t.Fatal(err)
	}
	visible = append(visible, rootOwned)
	historyNote(t, st, first, "私有笔记", "不可读笔记标记 private", domain.NoteVisibilityOwner, "specialist")
	archived := historyNote(t, st, first, "归档笔记", "不可读笔记标记 archived", domain.NoteVisibilityRun, "")
	if err := archived.Transition(domain.NoteArchived, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	archived.Version++
	if err := st.UpdateNote(t.Context(), archived, 1, newNoteChangedEvent(t, first.MissionID, archived, domain.NoteActive)); err != nil {
		t.Fatal(err)
	}
	second := historySuccessor(t, st, first)
	visible = append(visible, historyNote(t, st, second, "继承Run笔记", "笔记血缘回读标记 second", domain.NoteVisibilityRoot, ""))
	third := historySuccessor(t, st, second)
	other := historyNewRun(t, st, "ws-history")
	historyNote(t, st, other, "独立Thread笔记", "不可读笔记标记 other-thread", domain.NoteVisibilityRoot, "")
	for _, note := range visible {
		record := historyFindMemory(t, st, third, note.Content)
		if record.Kind != "note" || record.NoteID != note.ID || record.RunID != note.RunID || record.SourceVersion != note.Version || record.Status != "active" || record.Visibility != string(note.Visibility) || record.SourceKind != "run_note" || record.OriginalInstructionAuthorized {
			t.Fatalf("note provenance=%+v", record)
		}
		if got := historyReadAll(t, st, third.ID, record, "content", 7); got != note.Content {
			t.Fatal("note recall changed original content")
		}
		encoded, _ := json.Marshal(record)
		for _, reference := range append(note.SourceRefs, note.EvidenceIDs...) {
			if strings.Contains(string(encoded), reference) {
				t.Fatalf("note relation became recall metadata: %s", encoded)
			}
		}
	}
	result, err := st.SearchThreadHistory(t.Context(), third.ID, domain.HistorySearchRequest{Query: "不可读笔记标记"})
	if err != nil || len(result.Records) != 0 {
		t.Fatalf("private, archived, or foreign note leaked: %+v %v", result, err)
	}
	// Even a token synthesized with the right caller scope is checked against
	// the current stored visibility and exact validated Thread membership.
	scope, err := st.historyScope(t.Context(), st.db, third.ID)
	if err != nil {
		t.Fatal(err)
	}
	private, err := st.ListNotes(t.Context(), domain.NoteFilter{RunID: first.ID, Owner: "specialist"})
	if err != nil || len(private) != 1 {
		t.Fatalf("private fixture=%+v %v", private, err)
	}
	for _, note := range []domain.Note{private[0], archived} {
		source := mutableHistorySource("note:", scope, note.ID, note.Version, note.Content, note)
		if _, err := st.ReadThreadHistory(t.Context(), third.ID, domain.HistoryReadRequest{SourceID: source}); apperror.CodeOf(err) != apperror.CodeNotFound {
			t.Fatalf("forged inaccessible note token succeeded: %v", err)
		}
	}
}

func TestHistoryRecallMutableSourcesInvalidateEveryPage(t *testing.T) {
	for _, change := range []string{"content", "title", "disabled", "expired", "deleted", "recreated"} {
		t.Run(change, func(t *testing.T) {
			st, run, _ := historyFixture(t)
			memory := historyMemory(t, st, "memory-mutable", "ws-history", "变更失效回读标记 "+strings.Repeat("中文🙂", 60))
			record := historyFindMemory(t, st, run, "变更失效回读标记")
			page, err := st.ReadThreadHistory(t.Context(), run.ID, domain.HistoryReadRequest{SourceID: record.SourceID, Limit: 19})
			if err != nil || !page.HasMore {
				t.Fatalf("first page=%+v %v", page, err)
			}
			want := apperror.CodeConflict
			switch change {
			case "content":
				body := "更新内容"
				historyMemoryUpdate(t, st, memory, contextmgr.UpdateMemoryRequest{Content: &body})
			case "title":
				title := "只改标题也会递增版本"
				historyMemoryUpdate(t, st, memory, contextmgr.UpdateMemoryRequest{Title: &title})
			case "disabled":
				status := contextmgr.MemoryStatusDisabled
				historyMemoryUpdate(t, st, memory, contextmgr.UpdateMemoryRequest{Status: &status})
				want = apperror.CodeNotFound
			case "expired":
				retention := time.Now().UTC().Add(-time.Second)
				expiry := &retention
				historyMemoryUpdate(t, st, memory, contextmgr.UpdateMemoryRequest{RetentionUntil: &expiry})
				want = apperror.CodeNotFound
			case "deleted", "recreated":
				if deleted, err := st.DeleteContextMemory(t.Context(), memory.ID, memory.Version); err != nil || !deleted {
					t.Fatalf("delete=%t %v", deleted, err)
				}
				want = apperror.CodeNotFound
				if change == "recreated" {
					historyMemory(t, st, memory.ID, "ws-history", memory.Content)
					want = apperror.CodeConflict
				}
			}
			for _, offset := range []int{0, page.NextOffset} {
				got, err := st.ReadThreadHistory(t.Context(), run.ID, domain.HistoryReadRequest{SourceID: record.SourceID, Offset: offset, ExpectedSHA256: page.ContentSHA256})
				if apperror.CodeOf(err) != want || got.Content != "" {
					t.Fatalf("stale %s source offset=%d: page=%+v err=%v", change, offset, got, err)
				}
			}
		})
	}
	for _, change := range []string{"content", "visibility", "archived"} {
		t.Run("note-"+change, func(t *testing.T) {
			st, run, _ := historyFixture(t)
			note := historyNote(t, st, run, "笔记变更", "笔记修改回读标记 "+strings.Repeat("原始中文🙂", 40), domain.NoteVisibilityRoot, "")
			record := historyFindMemory(t, st, run, "笔记修改回读标记")
			page, err := st.ReadThreadHistory(t.Context(), run.ID, domain.HistoryReadRequest{SourceID: record.SourceID, Limit: 17})
			if err != nil || !page.HasMore {
				t.Fatalf("first page=%+v %v", page, err)
			}
			note.UpdatedAt, note.Version = time.Now().UTC(), note.Version+1
			want := apperror.CodeNotFound
			switch change {
			case "content":
				note.Content = "更新笔记内容"
				want = apperror.CodeConflict
			case "visibility":
				note.Visibility, note.Owner = domain.NoteVisibilityOwner, "specialist"
			case "archived":
				if err := note.Transition(domain.NoteArchived, note.UpdatedAt); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.UpdateNote(t.Context(), note, 1, newNoteChangedEvent(t, run.MissionID, note, domain.NoteActive)); err != nil {
				t.Fatal(err)
			}
			got, err := st.ReadThreadHistory(t.Context(), run.ID, domain.HistoryReadRequest{SourceID: record.SourceID, Offset: page.NextOffset, ExpectedSHA256: page.ContentSHA256})
			if apperror.CodeOf(err) != want || got.Content != "" {
				t.Fatalf("stale note page=%+v err=%v", got, err)
			}
		})
	}
}

func TestHistoryRecallMemoryCursorBindingAndLegacyCompatibility(t *testing.T) {
	st, run, _ := historyFixture(t)
	for index := range 4 {
		historyMemory(t, st, fmt.Sprintf("memory-page-%d", index), "ws-history", fmt.Sprintf("分页记忆标记 %d", index))
	}
	first, err := st.SearchThreadHistory(t.Context(), run.ID, domain.HistorySearchRequest{Query: "分页记忆标记", Limit: 1})
	if err != nil || len(first.Records) != 1 || !first.HasMore {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	deleted, err := st.DeleteContextMemory(t.Context(), first.Records[0].MemoryID, first.Records[0].SourceVersion)
	if err != nil || !deleted {
		t.Fatalf("delete=%t err=%v", deleted, err)
	}
	// Removing the earlier page must not shift an offset past another record.
	seen := map[string]bool{first.Records[0].MemoryID: true}
	page := first
	for page.HasMore {
		page, err = st.SearchThreadHistory(t.Context(), run.ID, domain.HistorySearchRequest{Query: "分页记忆标记", Cursor: page.NextCursor, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range page.Records {
			if seen[record.MemoryID] {
				t.Fatalf("repeated memory page: %+v", record)
			}
			seen[record.MemoryID] = true
		}
	}
	if len(seen) != 4 {
		t.Fatalf("memory pagination skipped a source: %v", seen)
	}
	other := historyNewRun(t, st, "ws-history")
	if _, err := st.SearchThreadHistory(t.Context(), other.ID, domain.HistorySearchRequest{Query: "分页记忆标记", Cursor: first.NextCursor}); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("cursor crossed Run: %v", err)
	}
	if _, err := st.SearchThreadHistory(t.Context(), run.ID, domain.HistorySearchRequest{Query: "另一个查询", Cursor: first.NextCursor}); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("cursor crossed query: %v", err)
	}
	var cursor historyCursor
	if err := decodeHistoryToken(first.NextCursor, &cursor); err != nil {
		t.Fatal(err)
	}
	cursor.Workspace = "ws-other"
	if _, err := st.SearchThreadHistory(t.Context(), run.ID, domain.HistorySearchRequest{Query: "分页记忆标记", Cursor: encodeHistoryToken(cursor)}); apperror.CodeOf(err) != apperror.CodeInvalidArgument {
		t.Fatalf("cursor accepted foreign Workspace: %v", err)
	}
	message := historyMessage(t, st, run, "旧协议回读标记")
	legacy := historyCursor{Run: run.ID, QuerySHA: session.ContentSHA256(""), MaxMessage: message.ID}
	old, err := st.SearchThreadHistory(t.Context(), run.ID, domain.HistorySearchRequest{Cursor: encodeHistoryToken(legacy)})
	if err != nil || len(old.Records) != 1 || old.Records[0].MessageID != message.ID || old.Records[0].Kind != "message" {
		t.Fatalf("legacy cursor expanded or lost old history: %+v %v", old, err)
	}
	if got := historyReadAll(t, st, run.ID, old.Records[0], "content", 7); got != message.Content {
		t.Fatal("legacy source reader changed")
	}
}

func TestHistoryRecallPendingCandidateBecomesReadableOnlyAfterConfirmation(t *testing.T) {
	st, first, _ := historyFixture(t)
	if _, err := application.NewRunService(st).Start(t.Context(), first.ID); err != nil {
		t.Fatal(err)
	}
	note, err := application.NewNoteService(st).Create(t.Context(), application.CreateNoteRequest{
		RunID: first.ID, Title: "完成后的项目结论", Content: "候选确认边界标记：下一次沿用已验证接口。",
		Category: "decision", Visibility: "run", SourceRefs: []string{"README.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(st).Complete(t.Context(), first.ID); err != nil {
		t.Fatal(err)
	}
	service := application.NewContextMemoryCandidateService(st)
	candidates, err := service.List(t.Context(), contextmgr.MemoryCandidateFilter{WorkspaceID: "ws-history"})
	if err != nil || len(candidates) != 1 || candidates[0].Source.NoteID != note.ID || candidates[0].Status != contextmgr.MemoryCandidatePending {
		t.Fatalf("pending candidate=%+v err=%v", candidates, err)
	}
	next := historyNewRun(t, st, "ws-history")
	result, err := st.SearchThreadHistory(t.Context(), next.ID, domain.HistorySearchRequest{Query: "候选确认边界标记"})
	if err != nil || len(result.Records) != 0 {
		t.Fatalf("unconfirmed candidate or another Thread's raw note leaked: %+v %v", result, err)
	}
	for _, source := range []string{"candidate:" + candidates[0].ID, "note:" + note.ID, "memory:" + candidates[0].ID} {
		if _, err := st.ReadThreadHistory(t.Context(), next.ID, domain.HistoryReadRequest{SourceID: source}); apperror.CodeOf(err) != apperror.CodeNotFound {
			t.Fatalf("unconfirmed source reader accepted %q: %v", source, err)
		}
	}
	candidate := candidates[0]
	confirmed, err := service.Confirm(t.Context(), candidate.ID, contextmgr.ReviewMemoryCandidateRequest{
		WorkspaceID: "ws-history", ExpectedVersion: candidate.Version, ExpectedFingerprint: candidate.Fingerprint,
		ExpectedSourceVersion: candidate.Source.NoteVersion, RequestedBy: "desktop_operator", Confirm: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	record := historyFindMemory(t, st, next, "候选确认边界标记")
	if record.Kind != "context_memory" || record.MemoryID != confirmed.MemoryID || record.SourceRef != candidate.ID || record.SourceVersion != 1 || record.OriginalInstructionAuthorized {
		t.Fatalf("confirmed memory provenance=%+v", record)
	}
	if got := historyReadAll(t, st, next.ID, record, "content", 13); got != note.Content {
		t.Fatal("confirmed memory changed the reviewed candidate content")
	}
}

func TestHistoryRecallMemoryAndNoteBodiesKeepByteLimits(t *testing.T) {
	st, run, _ := historyFixture(t)
	body := "有界记忆正文标记 " + strings.Repeat("中", (contextmgr.MaxMemoryContentBytes-64)/3)
	memory := historyMemory(t, st, strings.Repeat("<", 256), "ws-history", body)
	noteBody := "有界笔记正文标记 " + strings.Repeat("文", (domain.MaxNoteContentBytes-64)/3)
	note := historyNote(t, st, run, "有界笔记标题", noteBody, domain.NoteVisibilityRoot, "")
	titled := historyFindMemory(t, st, run, note.Title)
	if titled.MatchedPart != "title" || titled.Title != note.Title || titled.Excerpt != note.Title {
		t.Fatalf("title match lost complete metadata: %+v", titled)
	}
	for _, original := range []struct{ marker, content string }{{"有界记忆正文标记", memory.Content}, {"有界笔记正文标记", note.Content}} {
		record := historyFindMemory(t, st, run, original.marker)
		if len(record.SourceID) > 2048 {
			t.Fatalf("supported identity exceeded the exact reader's source_id bound: %d", len(record.SourceID))
		}
		if len(record.Excerpt) > 1024 {
			t.Fatalf("search returned an unbounded body: %d", len(record.Excerpt))
		}
		if got := historyReadAll(t, st, run.ID, record, "content", domain.HistoryReadMaxBytes); got != original.content {
			t.Fatal("bounded pages did not reconstruct the largest supported source")
		}
		for _, request := range []domain.HistoryReadRequest{
			{SourceID: record.SourceID, Limit: domain.HistoryReadMaxBytes + 1},
			{SourceID: record.SourceID, Offset: 4},
			{SourceID: record.SourceID, ExpectedSHA256: strings.Repeat("a", 64)},
		} {
			if got, err := st.ReadThreadHistory(t.Context(), run.ID, request); err == nil || got.Content != "" {
				t.Fatalf("invalid mutable source page succeeded: %+v %v", got, err)
			}
		}
	}
	tooLarge := memory
	tooLarge.ID, tooLarge.Content = "memory-over-limit", strings.Repeat("x", contextmgr.MaxMemoryContentBytes+1)
	tooLarge.ContentSHA256 = session.ContentSHA256(tooLarge.Content)
	if err := st.CreateContextMemory(t.Context(), tooLarge); err == nil {
		t.Fatal("oversized memory entered the recall store")
	}
	tooLargeNote := newNoteTest(run.ID, "too large", strings.Repeat("x", domain.MaxNoteContentBytes+1))
	if err := st.CreateNote(t.Context(), tooLargeNote, newNoteCreatedEvent(t, run.MissionID, tooLargeNote)); err == nil {
		t.Fatal("oversized note entered the recall store")
	}
}
