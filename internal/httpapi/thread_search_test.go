package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

func TestSavedMessageSearchFindsFullCompactedHistoryAndLocatesAcrossRuns(t *testing.T) {
	f := newAPIFixture(t)
	ctx := t.Context()
	full := strings.Repeat("早期背景。", 2400) + " 深处 SavedNeedle 最终要求"
	old, err := f.store.SaveSessionMessage(ctx, session.NewMessage(f.run.SessionID, "user", full))
	if err != nil {
		t.Fatal(err)
	}
	for range 105 {
		if _, err := f.store.SaveSessionMessage(ctx, session.NewMessage(f.run.SessionID, "assistant", "later ordinary answer")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.store.MarkSessionMessagesCompacted(ctx, f.run.SessionID, old.ID); err != nil {
		t.Fatal(err)
	}
	runs := application.NewRunService(f.store)
	if _, err := runs.Fail(ctx, f.run.ID, "search fixture complete"); err != nil {
		t.Fatal(err)
	}
	threadID := domain.InitialThreadID(f.run.ID)
	continued, err := application.NewThreadService(f.store).Submit(ctx, application.SubmitThreadMessageRequest{
		Version: domain.ThreadMessageProtocolVersion, ThreadID: threadID, Content: "a new run",
		OperationKey: "saved-message-search-successor", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SaveSessionMessage(ctx, session.NewMessage(continued.Run.SessionID, "assistant", "new SavedNeedle answer")); err != nil {
		t.Fatal(err)
	}
	var first []ThreadSearchResultView
	envelope := decodeData(t, f.get(t, "/api/v1/threads/search?q=SavedNeedle&limit=1"), &first)
	if len(first) != 1 || first[0].Role != "assistant" || envelope.Page.NextCursor == "" {
		t.Fatalf("first search page: %#v %#v", first, envelope.Page)
	}
	if _, err := f.store.SaveSessionMessage(ctx, session.NewMessage(continued.Run.SessionID, "assistant", "SavedNeedle appended after the search cursor")); err != nil {
		t.Fatal(err)
	}
	var next []ThreadSearchResultView
	decodeData(t, f.get(t, "/api/v1/threads/search?q=savedneedle&limit=1&cursor="+url.QueryEscape(envelope.Page.NextCursor)), &next)
	if len(next) != 1 || next[0].Role != "user" || next[0].ThreadID != threadID || !strings.Contains(next[0].Snippet, "SavedNeedle") {
		t.Fatalf("old full-body match missing: %#v", next)
	}
	var latest []ThreadTranscriptItemView
	decodeData(t, f.get(t, "/api/v1/threads/"+threadID+"/transcript?limit=100"), &latest)
	for _, item := range latest {
		if item.ID == next[0].MessageID {
			t.Fatal("fixture target must precede the loaded window")
		}
	}
	var located ThreadSavedMessageView
	decodeData(t, f.get(t, "/api/v1/threads/"+threadID+"/messages/"+next[0].MessageID), &located)
	if located.Content != full {
		t.Fatalf("located body was truncated: got %d want %d bytes", len(located.Content), len(full))
	}
	var window []ThreadTranscriptItemView
	decodeData(t, f.get(t, "/api/v1/threads/"+threadID+"/transcript?limit=2&cursor="+url.QueryEscape(located.TranscriptCursor)), &window)
	if len(window) == 0 || window[len(window)-1].ID != next[0].MessageID || window[len(window)-1].RunOrdinal != 1 {
		t.Fatalf("cursor did not locate exact cross-Run target: %#v", window)
	}
	assertAPIError(t, f.get(t, "/api/v1/threads/"+threadID+"-other/messages/"+next[0].MessageID), http.StatusNotFound, "NOT_FOUND")
	assertAPIError(t, f.get(t, "/api/v1/threads/search?q=other&cursor="+url.QueryEscape(envelope.Page.NextCursor)), http.StatusBadRequest, "INVALID_ARGUMENT")
	assertAPIError(t, f.get(t, "/api/v1/threads/search?q=savedneedle&include_archived=true&cursor="+url.QueryEscape(envelope.Page.NextCursor)), http.StatusBadRequest, "INVALID_ARGUMENT")
}

func TestSavedMessageSearchPrivacyLifecycleTitlesAndValidation(t *testing.T) {
	f := newAPIFixture(t)
	ctx := t.Context()
	for _, message := range []session.Message{
		session.NewMessage(f.run.SessionID, "assistant", "public body Literal%_ 中文Ä"),
		session.NewMessage(f.run.SessionID, "system", "HiddenSearchCanary"),
		session.NewEvidenceMessage(f.run.SessionID, session.SourceToolResult, "private-tool", "HiddenSearchCanary"),
		session.NewMessage(f.run.SessionID, "user", "Continue mission at turn 12 using only the structured tools offered by Go when needed: HiddenSearchCanary"),
		session.NewMessage(f.run.SessionID, "assistant", "credential api_key=sk-SecretSearchCanary12345678901234567890"),
	} {
		if _, err := f.store.SaveSessionMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"HiddenSearchCanary", "SecretSearchCanary"} {
		var matches []ThreadSearchResultView
		decodeData(t, f.get(t, "/api/v1/threads/search?q="+query), &matches)
		if len(matches) != 0 {
			t.Fatalf("private text produced a match for %s: %#v", query, matches)
		}
	}
	var matches []ThreadSearchResultView
	decodeData(t, f.get(t, "/api/v1/threads/search?q="+url.QueryEscape("literal%_ 中文Ä")), &matches)
	if len(matches) != 1 || matches[0].MessageID == "" {
		t.Fatalf("literal Unicode body match: %#v", matches)
	}
	messageID := matches[0].MessageID
	matches = nil
	decodeData(t, f.get(t, "/api/v1/threads/search?q="+url.QueryEscape("inspect durable agent state")), &matches)
	if len(matches) != 1 || matches[0].Role != "title" || matches[0].MessageID != "" {
		t.Fatalf("title matching was lost: %#v", matches)
	}
	for _, path := range []string{"?q=", "?q=" + strings.Repeat("x", 257), "?q=a&q=b", "?q=a&limit=101", "?q=a&include_archived=invalid", "?q=a&include_archived=true&include_archived=false", "?q=a&unknown=true"} {
		assertAPIError(t, f.get(t, "/api/v1/threads/search"+path), http.StatusBadRequest, "INVALID_ARGUMENT")
	}
	assertAPIError(t, f.request(t, http.MethodGet, "/api/v1/threads/search?q=public", "", "127.0.0.1:8765", "127.0.0.1:45000", nil), http.StatusUnauthorized, "POLICY_DENIED")
	lease, found, err := f.store.GetRunExecutionLease(ctx, f.run.ID)
	if err != nil || !found {
		t.Fatalf("fixture lease: %v %v", found, err)
	}
	if _, _, err := f.store.ReleaseRunExecutionLease(ctx, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(f.store).Fail(ctx, f.run.ID, "archive search fixture"); err != nil {
		t.Fatal(err)
	}
	thread, err := f.store.GetThreadByRun(ctx, f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	archived, err := application.NewThreadService(f.store).Transition(ctx, thread.ID, domain.ThreadArchive, thread.Version, "test_operator", "archive-search-fixture")
	if err != nil {
		t.Fatal(err)
	}
	decodeData(t, f.get(t, "/api/v1/threads/search?q=public"), &matches)
	if len(matches) != 0 {
		t.Fatalf("archived thread included by default: %#v", matches)
	}
	decodeData(t, f.get(t, "/api/v1/threads/search?q=public&include_archived=true"), &matches)
	if len(matches) != 1 || matches[0].ThreadStatus != "archived" {
		t.Fatalf("archived search: %#v", matches)
	}
	var located ThreadSavedMessageView
	decodeData(t, f.get(t, "/api/v1/threads/"+thread.ID+"/messages/"+messageID), &located)
	after, err := f.store.GetThread(ctx, thread.ID)
	if err != nil || after.Version != archived.Version || after.Status != domain.ThreadArchived {
		t.Fatalf("read changed archive lifecycle: %#v %v", after, err)
	}
	if _, err := application.NewThreadService(f.store).Transition(ctx, thread.ID, domain.ThreadDelete, after.Version, "test_operator", "delete-search-fixture"); err != nil {
		t.Fatal(err)
	}
	decodeData(t, f.get(t, "/api/v1/threads/search?q=public&include_archived=true"), &matches)
	if len(matches) != 0 {
		t.Fatalf("deleted history was searchable: %#v", matches)
	}
	assertAPIError(t, f.get(t, "/api/v1/threads/"+thread.ID+"/messages/"+messageID), http.StatusNotFound, "NOT_FOUND")
}
