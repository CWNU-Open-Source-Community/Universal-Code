package store

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
)

// seedCreationPageThreads creates Threads through the normal Run service path at
// the given offsets inside one shared second and returns their ids oldest first.
func seedCreationPageThreads(t *testing.T, st *SQLiteStore, offsets []time.Duration) []string {
	t.Helper()
	base := time.Date(2026, time.August, 12, 10, 0, 0, 500000000, time.UTC)
	seed := &threadSearchCreationStore{SQLiteStore: st, createdAt: base}
	runs := application.NewRunService(seed)
	ids := make([]string, 0, len(offsets))
	for index, offset := range offsets {
		seed.createdAt = base.Add(offset)
		_, run, err := runs.Create(t.Context(), application.CreateRunRequest{
			Goal: fmt.Sprintf("thread creation order %03d", index), Profile: "review", ModelRoute: "review"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, domain.InitialThreadID(run.ID))
	}
	return ids
}

func listedThreadOrder(t *testing.T, st *SQLiteStore, ids []string,
	anchor time.Time, anchorID string, limit int,
) []string {
	t.Helper()
	items, err := st.ListThreadsByCreationPage(t.Context(), domain.ThreadFilter{Limit: limit}, anchor, anchorID)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(items))
	for _, item := range items {
		if slices.Contains(ids, item.ID) {
			got = append(got, item.ID)
		}
	}
	return got
}

// GET /api/v1/threads promises created_at DESC, id DESC. ts() trims trailing
// fractional zeros, so raw RFC3339Nano text puts "...00.5Z" (500ms) above
// "...00.52Z" (520ms); only read_pages.go's padded UTC key orders them, which is
// why the runs, sessions, scheduled jobs and saved-message lists already use it.
func TestSQLiteThreadCreationPageOrdersMillisecondCreationTimeNewestFirst(t *testing.T) {
	st := openWorkItemTestStore(t)
	ids := seedCreationPageThreads(t, st, []time.Duration{0, 20 * time.Millisecond, 40 * time.Millisecond})
	if got, want := listedThreadOrder(t, st, ids, time.Time{}, "", 100), []string{ids[2], ids[1], ids[0]}; !slices.Equal(got, want) {
		t.Fatalf("newest-first Thread creation page misordered same-second stamps: got=%v want=%v", got, want)
	}
}

func TestSQLiteThreadCreationPageOrdersSubMillisecondCreationTimeNewestFirst(t *testing.T) {
	st := openWorkItemTestStore(t)
	ids := seedCreationPageThreads(t, st, []time.Duration{0, 100 * time.Nanosecond, 200 * time.Nanosecond})
	if got, want := listedThreadOrder(t, st, ids, time.Time{}, "", 100), []string{ids[2], ids[1], ids[0]}; !slices.Equal(got, want) {
		t.Fatalf("newest-first Thread creation page misordered sub-millisecond stamps: got=%v want=%v", got, want)
	}
}

// The cursor anchor is re-formatted rather than read back, so the same raw text
// key also breaks the continuation: a page size of 2 must not return the oldest
// Thread first or reveal the newest Thread only on a later page.
func TestSQLiteThreadCreationPageCursorWalkKeepsNewestFirstOrder(t *testing.T) {
	st := openWorkItemTestStore(t)
	ctx := t.Context()
	ids := seedCreationPageThreads(t, st, []time.Duration{0, 20 * time.Millisecond, 40 * time.Millisecond})

	var combined []string
	anchor := time.Time{}
	anchorID := ""
	for range 4 {
		items, err := st.ListThreadsByCreationPage(ctx, domain.ThreadFilter{Limit: 2}, anchor, anchorID)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			if slices.Contains(ids, item.ID) {
				combined = append(combined, item.ID)
			}
		}
		anchor, anchorID = items[len(items)-1].CreatedAt, items[len(items)-1].ID
	}
	if want := []string{ids[2], ids[1], ids[0]}; !slices.Equal(combined, want) {
		t.Fatalf("cursor-walked Thread pages differ from the newest-first order: got=%v want=%v", combined, want)
	}
}
