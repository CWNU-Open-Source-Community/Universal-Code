package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/store"
)

type filteredThreadController struct {
	threadTurnControllerStub
	states map[string]string
}

func (s *filteredThreadController) ExecutionStates(_ context.Context, ids []string) (map[string]application.ThreadExecutionState, error) {
	states := make(map[string]application.ThreadExecutionState, len(ids))
	for _, id := range ids {
		state := s.states[id]
		if state == "" {
			state = "idle"
		}
		states[id] = application.ThreadExecutionState{Version: application.ThreadExecutionProtocolVersion, ThreadID: id, State: state}
	}
	return states, nil
}

func TestThreadExecutionFilterFindsOlderSavedConversationsAndScopesCursor(t *testing.T) {
	st := threadListTestStore(t)
	controller := &filteredThreadController{states: map[string]string{}}
	runs := application.NewRunService(st)
	var oldIDs []string
	var waitingID string
	for index := 0; index < 105; index++ {
		_, run, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: fmt.Sprintf("Filter task %03d", index), Profile: "review"})
		if err != nil {
			t.Fatal(err)
		}
		if index < 2 {
			id := domain.InitialThreadID(run.ID)
			controller.states[id] = "running"
			oldIDs = append(oldIDs, id)
		}
		if index == 2 {
			// A durable running Run with no live request must stay out of Running.
			if _, err := runs.Start(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
		}
		if index == 3 {
			running, err := runs.Start(t.Context(), run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = running.Transition(domain.RunWaitingApproval, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			event, err := events.New(run.ID, run.MissionID, events.RunStatusChangedEvent, "test", run.ID, map[string]any{"from": domain.RunRunning, "to": domain.RunWaitingApproval})
			if err != nil {
				t.Fatal(err)
			}
			if err = st.TransitionRun(t.Context(), running, domain.RunRunning, event); err != nil {
				t.Fatal(err)
			}
			waitingID = domain.InitialThreadID(run.ID)
		}
	}
	api := threadListTestAPI(t, st, controller)
	first, page := readThreadList(t, api, "/api/v1/threads?limit=1&execution_state=running")
	if len(first) != 1 || first[0].ID != oldIDs[1] || page.NextCursor == "" {
		t.Fatalf("old running task missed: %+v %+v", first, page)
	}
	second, final := readThreadList(t, api, "/api/v1/threads?limit=1&execution_state=running&cursor="+url.QueryEscape(page.NextCursor))
	if len(second) != 1 || second[0].ID != oldIDs[0] || final.NextCursor != "" {
		t.Fatal(second, final)
	}
	response := performSessionMessageRequest(t, api, http.MethodGet, "/api/v1/threads?execution_state=waiting_approval&cursor="+url.QueryEscape(page.NextCursor), testAccessToken, "", "", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatal("filter accepted another filter's cursor", response.Code)
	}
	waiting, waitingPage := readThreadList(t, api, "/api/v1/threads?execution_state=waiting_approval")
	if len(waiting) != 1 || waiting[0].ID != waitingID || waitingPage.NextCursor != "" {
		t.Fatal(waiting, waitingPage)
	}
	for _, query := range []string{"execution_state=idle", "execution_state=running&execution_state=waiting_approval", "pinned=invalid"} {
		response := performSessionMessageRequest(t, api, http.MethodGet, "/api/v1/threads?"+query, testAccessToken, "", "", nil)
		if response.Code != http.StatusBadRequest {
			t.Fatal(query, response.Code)
		}
	}
}

type sparseThreadFilterStore struct {
	*store.SQLiteStore
	items []domain.Thread
}

func (s *sparseThreadFilterStore) ListThreadsByCreationPage(_ context.Context, filter domain.ThreadFilter, before time.Time, beforeID string) ([]domain.Thread, error) {
	items := make([]domain.Thread, 0, filter.Limit)
	for _, thread := range s.items {
		if !before.IsZero() && (thread.CreatedAt.After(before) || thread.CreatedAt.Equal(before) && thread.ID >= beforeID) {
			continue
		}
		items = append(items, thread)
		if len(items) == filter.Limit {
			break
		}
	}
	return items, nil
}

func (s *sparseThreadFilterStore) GetThreadExecutionFacts(_ context.Context, ids []string) (map[string]domain.ThreadExecutionFacts, error) {
	facts := make(map[string]domain.ThreadExecutionFacts, len(ids))
	for _, id := range ids {
		status := domain.RunCreated
		if id == s.items[len(s.items)-1].ID {
			status = domain.RunWaitingApproval
		}
		facts[id] = domain.ThreadExecutionFacts{RunID: "run-" + id, RunStatus: status}
	}
	return facts, nil
}

func TestThreadExecutionFilterContinuesAfterEmptyBoundedScan(t *testing.T) {
	st := &sparseThreadFilterStore{SQLiteStore: threadListTestStore(t)}
	now := time.Now().UTC()
	for i := 0; i < 1005; i++ {
		id := fmt.Sprintf("sparse-%04d", i)
		st.items = append(st.items, domain.Thread{ID: id, Title: id, ProtocolVersion: domain.ThreadProtocolVersion, MissionID: "mission-" + id,
			ActiveRunID: "run-" + id, LastRunID: "run-" + id, Status: domain.ThreadActive, Version: 1, CreatedAt: now.Add(-time.Duration(i) * time.Second), UpdatedAt: now})
	}
	api := threadListTestAPI(t, st, &filteredThreadController{})
	first, page := readThreadList(t, api, "/api/v1/threads?execution_state=waiting_approval")
	if len(first) != 0 || page.NextCursor == "" || page.Truncated {
		t.Fatal("empty scan claimed completion", first, page)
	}
	second, final := readThreadList(t, api, "/api/v1/threads?execution_state=waiting_approval&cursor="+url.QueryEscape(page.NextCursor))
	if len(second) != 1 || second[0].ID != "sparse-1004" || final.NextCursor != "" {
		t.Fatal(second, final)
	}
}
