package store

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/session"
)

func TestSavedPublicCommentaryRetainsFullBodyAndVisibility(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "public-commentary.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "public commentary export", Profile: "review", Interactive: true, Budget: domain.Budget{MaxTurns: 8}})
	if err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(run.ID)
	full := strings.Repeat("完整公开说明。", 1000) + " commentary needle"
	for _, source := range []string{"model", "thread_branch"} {
		message, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "assistant", "placeholder"))
		if err != nil {
			t.Fatal(err)
		}
		var eventID string
		if err := st.db.QueryRow(`SELECT event_id FROM run_events WHERE run_id=? AND type='session.message_created' AND subject_id=?`,
			run.ID, strconv.FormatInt(message.ID, 10)).Scan(&eventID); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]string{"text": full, "source_event_id": "original-reply"})
		if _, err := st.db.Exec(`UPDATE run_events SET type=?,source=?,payload_json=? WHERE event_id=?`,
			events.ModelPublicCommentaryEvent, source, string(payload), eventID); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetThreadSearchMessage(t.Context(), threadID, eventID)
		if err != nil || got.Content != full || got.Role != "assistant" {
			t.Fatalf("full public reply: %+v %v", got, err)
		}
		if _, err := st.GetThreadSearchMessage(t.Context(), threadID+"-other", eventID); err == nil {
			t.Fatal("cross-thread commentary was readable")
		}
		payload, _ = json.Marshal(map[string]string{"text": "api_key=sk-CommentarySecret12345678901234567890"})
		if _, err := st.db.Exec(`UPDATE run_events SET payload_json=? WHERE event_id=?`, string(payload), eventID); err != nil {
			t.Fatal(err)
		}
		got, err = st.GetThreadSearchMessage(t.Context(), threadID, eventID)
		if err != nil || strings.Contains(got.Content, "CommentarySecret") {
			t.Fatalf("commentary redaction: %+v %v", got, err)
		}
		if _, err := st.db.Exec(`UPDATE run_events SET type='model.started' WHERE event_id=?`, eventID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetThreadSearchMessage(t.Context(), threadID, eventID); err == nil {
			t.Fatal("internal event was readable as a public message")
		}
	}
}

func TestThreadSearchRedactsHistoricalCandidatesBeforeMatchingAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "saved-search.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(st)
	_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "saved search history", Profile: "review", Interactive: true, Budget: domain.Budget{MaxTurns: 8}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "assistant", "visible Needle answer")); err != nil {
		t.Fatal(err)
	}
	private, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "assistant", "historical placeholder"))
	if err != nil {
		t.Fatal(err)
	}
	// Old/imported records may predate the current storage redactor. A raw SQL
	// candidate must not become a match or consume a result slot after redaction.
	if _, err := st.db.Exec(`UPDATE run_events SET payload_json=json_set(payload_json,'$.content',?)
	 WHERE run_id=? AND type='session.message_created' AND subject_id=?`,
		"api_key=sk-NeedleSecret123456789012345678901234", run.ID, strconv.FormatInt(private.ID, 10)); err != nil {
		t.Fatal(err)
	}
	var privateEvent string
	if err := st.db.QueryRow(`SELECT event_id FROM run_events WHERE run_id=? AND type='session.message_created' AND subject_id=?`, run.ID, strconv.FormatInt(private.ID, 10)).Scan(&privateEvent); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	items, err := st.SearchThreadMessages(t.Context(), "Needle", false, time.Time{}, "", 2)
	if err != nil || len(items) != 1 || items[0].Content != "visible Needle answer" {
		t.Fatalf("redacted candidate affected matches: %#v %v", items, err)
	}
	more, err := st.SearchThreadMessages(t.Context(), "Needle", false, items[0].CreatedAt, items[0].ID, 2)
	if err != nil || len(more) != 0 {
		t.Fatalf("filtered result affected pagination: %#v %v", more, err)
	}
	message, err := st.GetThreadSearchMessage(t.Context(), domain.InitialThreadID(run.ID), privateEvent)
	if err != nil || strings.Contains(message.Content, "NeedleSecret") {
		t.Fatalf("historical message exposed secret: %#v %v", message, err)
	}
}

func TestThreadSearchUserMockTextAndExactTimestampPagination(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "search-time.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "saved history", Profile: "review", Interactive: true, Budget: domain.Budget{MaxTurns: 8}})
	if err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(run.ID)
	var ids []string
	for _, stamp := range []string{"2026-10-07T00:00:00Z", "2026-10-07T00:00:00.1Z", "2026-10-07T00:00:00.11Z"} {
		message, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", "Mock response for the checkout endpoint"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`UPDATE run_events SET created_at=? WHERE run_id=? AND type='session.message_created' AND subject_id=?`, stamp, run.ID, strconv.FormatInt(message.ID, 10)); err != nil {
			t.Fatal(err)
		}
		var id string
		if err := st.db.QueryRow(`SELECT event_id FROM run_events WHERE run_id=? AND type='session.message_created' AND subject_id=?`, run.ID, strconv.FormatInt(message.ID, 10)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "assistant", "Mock response for the checkout endpoint")); err != nil {
		t.Fatal(err)
	}
	var before time.Time
	beforeID := ""
	for _, expected := range []string{ids[2], ids[1], ids[0]} {
		items, err := st.SearchThreadMessages(t.Context(), "checkout", false, before, beforeID, 1)
		if err != nil || len(items) != 1 || items[0].ID != expected {
			t.Fatalf("timestamp page: %#v %v; want %s", items, err, expected)
		}
		if items[0].MessageID != "" {
			message, err := st.GetThreadSearchMessage(t.Context(), threadID, items[0].MessageID)
			if err != nil || message.Content != "Mock response for the checkout endpoint" {
				t.Fatalf("normal user text could not be located: %#v %v", message, err)
			}
		}
		before, beforeID = items[0].CreatedAt, items[0].ID
	}
	items, err := st.SearchThreadMessages(t.Context(), "checkout", false, before, beforeID, 1)
	if err != nil || len(items) != 0 {
		t.Fatalf("unexpected extra matches: %#v %v", items, err)
	}
}
