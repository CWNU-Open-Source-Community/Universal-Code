package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/desktopbrowser"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/store"
)

func newSharedBrowserStoreTest(t *testing.T) (*SharedBrowserService, string, string) {
	t.Helper()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	workspaceID := "workspace-browser-test"
	if err = st.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: workspaceID, Name: "browser-test", RootPath: home}); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "shared browser", Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: workspaceID, Budget: domain.Budget{MaxTurns: 2}})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := st.GetThreadByRun(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newSharedBrowserService(st, SharedBrowserOptions{HomePath: home, CreateHost: func(context.Context, string, func(desktopbrowser.Event)) (*desktopbrowser.Host, error) {
		return nil, errors.New("test must reuse draft, not create another browser")
	}})
	if err != nil {
		t.Fatal(err)
	}
	return s, workspaceID, thread.ID
}

func TestSharedBrowserAdoptionPersistsOnlyNewOwner(t *testing.T) {
	s, workspaceID, threadID := newSharedBrowserStoreTest(t)
	draftKey, threadKey := "draft:"+workspaceID, "thread:"+threadID
	draft := &sharedBrowserSession{profile: "profile-original", state: SharedBrowserState{SessionID: "session-original", WorkspaceID: workspaceID, State: "ready"}}
	s.sessions[draft.state.SessionID] = draft
	s.owners[draftKey] = draft.state.SessionID
	s.profiles[draftKey] = draft.profile
	req := SharedBrowserOpenRequest{WorkspaceID: workspaceID, ThreadID: threadID, AdoptSessionID: draft.state.SessionID}
	if _, err := s.Open(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	readIndex := func() map[string]string {
		data, err := os.ReadFile(s.profileIndex())
		if err != nil {
			t.Fatal(err)
		}
		var index map[string]string
		if err = json.Unmarshal(data, &index); err != nil {
			t.Fatal(err)
		}
		return index
	}
	index := readIndex()
	if index[threadKey] != draft.profile || index[draftKey] != "" {
		t.Fatalf("profile alias survived migration: %#v", index)
	}
	// A late StrictMode/recovery retry of the old adoption must not consume a
	// different, newly opened draft in the same workspace.
	s.owners[draftKey] = "session-next"
	s.profiles[draftKey] = "profile-next"
	if _, err := s.Open(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	index = readIndex()
	if s.owners[draftKey] != "session-next" || index[draftKey] != "profile-next" || index[threadKey] != draft.profile {
		t.Fatalf("repeat adoption consumed next draft: %#v %#v", s.owners, index)
	}
}

func TestSharedBrowserAdoptionWriteFailureKeepsOwnership(t *testing.T) {
	s, workspaceID, threadID := newSharedBrowserStoreTest(t)
	key := "draft:" + workspaceID
	draft := &sharedBrowserSession{profile: "profile-original", state: SharedBrowserState{SessionID: "session-original", WorkspaceID: workspaceID}}
	s.sessions[draft.state.SessionID] = draft
	s.owners[key] = draft.state.SessionID
	s.profiles[key] = draft.profile
	blocked := filepath.Join(s.options.HomePath, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	s.options.HomePath = blocked
	if _, err := s.Open(t.Context(), SharedBrowserOpenRequest{WorkspaceID: workspaceID, ThreadID: threadID, AdoptSessionID: draft.state.SessionID}); err == nil {
		t.Fatal("expected write failure")
	}
	if draft.state.ThreadID != "" || s.owners[key] != draft.state.SessionID || len(s.profiles) != 1 || s.profiles[key] != draft.profile {
		t.Fatalf("failed adoption changed ownership: %#v %#v", draft.state, s.profiles)
	}
}

func TestSharedBrowserHistoryUsesSuccessfulVisits(t *testing.T) {
	s := &sharedBrowserSession{}
	first := time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC)
	s.recordNavigation(desktopbrowser.State{URL: "https://example.com/first", Title: "First"}, first)
	for _, p := range []desktopbrowser.State{{URL: "about:blank"}, {URL: "https://example.com/failed", Error: "failed"}, {URL: "https://example.com/loading", Loading: true}, {URL: "https://user:secret@example.com/"}, {URL: "file:///private"}} {
		s.recordNavigation(p, first)
	}
	if len(s.state.History) != 1 {
		t.Fatalf("non-visit recorded: %#v", s.state.History)
	}
	s.recordNavigation(desktopbrowser.State{URL: "https://example.com/second", Title: "Second"}, first.Add(time.Minute))
	s.recordNavigation(desktopbrowser.State{URL: "https://example.com/first", Title: "Updated"}, first.Add(2*time.Minute))
	if len(s.state.History) != 2 || s.state.History[0].Title != "Updated" || s.state.History[1].Title != "Second" {
		t.Fatalf("recent visit order incorrect: %#v", s.state.History)
	}
}

func TestSharedBrowserBridgeRejectsUnsupportedProtocolBeforeHost(t *testing.T) {
	b := &DesktopBridge{contextProvider: func() context.Context { return context.Background() }}
	if _, err := b.OpenSharedBrowser(SharedBrowserOpenRequest{ProtocolVersion: SharedBrowserProtocolVersion}); err == nil {
		t.Fatal("missing native host accepted")
	}
	if _, err := b.sharedBrowserContext("unknown"); err == nil {
		t.Fatal("unknown native protocol accepted")
	}
}
