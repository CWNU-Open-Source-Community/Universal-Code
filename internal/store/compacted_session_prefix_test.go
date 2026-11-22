package store

import (
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/session"
)

func TestListCompleteCompactedSessionPrefix(t *testing.T) {
	ctx := t.Context()
	st, err := Open(filepath.Join(t.TempDir(), "prefix.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	create := func(title string) string {
		t.Helper()
		sess := session.New("", title, "learn")
		if err := st.SaveSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
		return sess.ID
	}
	save := func(message session.Message) session.Message {
		t.Helper()
		saved, err := st.SaveSessionMessage(ctx, message)
		if err != nil {
			t.Fatal(err)
		}
		return saved
	}
	local, other := create("release"), create("unrelated")
	goal := save(session.NewMessage(local, "user", "按组件分组。"))
	foreign := save(session.NewMessage(other, "user", "Unrelated instructions."))
	report := save(session.NewMessage(local, "assistant", "Old draft."))
	observed := save(session.NewEvidenceMessage(local, session.SourceWorkspaceFile, "README.md", "按风险分组"))
	wrongEnd := save(session.NewMessage(other, "assistant", "Unrelated report."))
	if n, err := st.MarkSessionMessagesCompacted(ctx, local, observed.ID); err != nil || n != 3 {
		t.Fatalf("compacted rows=%d, err=%v", n, err)
	}
	if _, err := st.MarkSessionMessagesCompacted(ctx, other, wrongEnd.ID); err != nil {
		t.Fatal(err)
	}
	after := save(session.NewMessage(local, "user", "This message remains active."))
	end := session.NewMessage(local, "assistant", "Compacted out of order.")
	end.Compacted = true
	end = save(end)
	bytes := len(goal.Content) + len(report.Content) + len(observed.Content)
	got, err := st.ListCompleteCompactedSessionPrefix(ctx, local, observed.ID, 3, bytes)
	if err != nil || len(got) != 3 {
		t.Fatalf("complete prefix: rows=%d err=%v", len(got), err)
	}
	for i, want := range []session.Message{goal, report, observed} {
		row := got[i]
		if row.SourceMessageID != want.ID || row.Role != want.Role || row.Content != want.Content || !row.CreatedAt.Equal(want.CreatedAt) ||
			row.SourceKind != want.Provenance.SourceKind || row.SourceRef != want.Provenance.SourceRef ||
			row.ContentSHA256 != want.Provenance.ContentSHA256 || row.InstructionAuthorized != want.Provenance.InstructionAuthorized {
			t.Fatalf("unexpected archived row %d: %+v", i, row)
		}
	}
	for _, tc := range []struct {
		name, session string
		through       int64
		count, bytes  int
	}{
		{"one byte short", local, observed.ID, 3, bytes - 1},
		{"extra eligible row", local, observed.ID, 2, bytes},
		{"missing expected row", local, observed.ID, 4, bytes},
		{"wrong last ID", local, wrongEnd.ID, 3, bytes},
		{"foreign session", other, observed.ID, 3, bytes},
		{"foreign boundary", local, foreign.ID, 1, bytes},
		{"active row in prefix", local, after.ID, 4, bytes + len(after.Content)},
		{"active hole before compacted end", local, end.ID, 4, bytes + len(end.Content)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := st.ListCompleteCompactedSessionPrefix(ctx, tc.session, tc.through, tc.count, tc.bytes)
			if err != nil || got != nil {
				t.Fatalf("unavailable prefix: got %+v, err=%v", got, err)
			}
		})
	}

	// The eligible path must validate original bodies; a rejected prefix
	// must never scan this deliberately inconsistent test row.
	badSession := create("corrupt row")
	oversized := strings.Repeat("长", 1024)
	inserted, err := st.db.ExecContext(ctx, `INSERT INTO session_messages
		(session_id,role,content,provenance_version,source_kind,source_ref,content_sha256,instruction_authorized,token_estimate,compacted,created_at)
		SELECT ?,role,?,provenance_version,source_kind,source_ref,content_sha256,instruction_authorized,token_estimate,1,created_at
		FROM session_messages WHERE id=?`, badSession, oversized, report.ID)
	if err != nil {
		t.Fatal(err)
	}
	badID, err := inserted.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.ListCompleteCompactedSessionPrefix(ctx, badSession, badID, 1, len(oversized)-1); got != nil || err != nil {
		t.Fatalf("oversized corrupt content was scanned: rows=%d, err=%v", len(got), err)
	}
	if got, err := st.ListCompleteCompactedSessionPrefix(ctx, badSession, badID, 1, len(oversized)); got != nil || err == nil || !strings.Contains(err.Error(), "content digest mismatch") {
		t.Fatalf("eligible corrupt content was accepted: rows=%d, err=%v", len(got), err)
	}

	many := create("many messages")
	const limit = 2 * contextmgr.MaxHandoffMemoryRecords
	var last session.Message
	var atLimit int64
	for i := 0; i <= limit; i++ {
		last = save(session.NewMessage(many, "user", "x"))
		if i+1 == limit {
			atLimit = last.ID
		}
	}
	if _, err := st.MarkSessionMessagesCompacted(ctx, many, last.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ListCompleteCompactedSessionPrefix(ctx, many, atLimit, limit, limit); len(got) != limit || err != nil {
		t.Fatalf("maximum fitting prefix: rows=%d, err=%v", len(got), err)
	}
	if got, err := st.ListCompleteCompactedSessionPrefix(ctx, many, last.ID, limit+1, limit+1); got != nil || err != nil {
		t.Fatalf("overlong prefix: rows=%d, err=%v", len(got), err)
	}
}
