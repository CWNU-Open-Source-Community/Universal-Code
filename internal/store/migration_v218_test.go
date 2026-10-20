package store

import (
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

func TestSchemaV218PreservesExistingConversationAndPersistsPin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation-pins.db")
	st := openHistoricalTestDatabase(t, path, 217)
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "Original task", Profile: "review"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := st.GetThreadByRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := st.GetThread(t.Context(), before.ID)
	if err != nil || after.Title != before.Title || after.Pinned || after.Version != before.Version {
		t.Fatalf("migration changed conversation: %+v %v", after, err)
	}
	pinned, err := st.PinThread(t.Context(), before.ID, true)
	if err != nil || !pinned.Pinned {
		t.Fatal(pinned, err)
	}
	replayed, err := st.PinThread(t.Context(), before.ID, true)
	if err != nil || replayed.Version != pinned.Version {
		t.Fatal("repeated PUT changed the version", replayed, err)
	}
	renamed, err := st.RenameThread(t.Context(), before.ID, "  审查结果  ")
	if err != nil || renamed.Title != "审查结果" || !renamed.Pinned {
		t.Fatal(renamed, err)
	}
	mission, err := st.GetMission(t.Context(), run.MissionID)
	if err != nil || mission.Goal != "Original task" {
		t.Fatal("rename changed mission", mission, err)
	}
	sess, err := st.GetSession(t.Context(), run.SessionID)
	if err != nil || sess.Title != "Original task" {
		t.Fatal("rename changed session", sess, err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	state := true
	items, err := st.ListThreadsByCreationPage(t.Context(), domain.ThreadFilter{Pinned: &state, Limit: 10}, time.Time{}, "")
	if err != nil || len(items) != 1 || items[0].ID != before.ID || items[0].Title != "审查结果" || !items[0].Pinned {
		t.Fatal(items, err)
	}
	state = false
	items, err = st.ListThreadsByCreationPage(t.Context(), domain.ThreadFilter{Pinned: &state, Limit: 10}, time.Time{}, "")
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	assertNoForeignKeyViolations(t, st.db)
}
