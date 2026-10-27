package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/session"
)

func TestSavedMessageSearchFindsAndLocatesInheritedPublicReply(t *testing.T) {
	f := newAPIFixture(t)
	ctx := t.Context()
	_, run, err := application.NewRunService(f.store).Create(ctx, application.CreateRunRequest{
		Goal: "branch search", WorkspaceID: f.workspace.ID, Profile: "review", Interactive: true})
	if err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat("完整的早期回复。", 1000) + " InheritedSearchNeedle"
	for _, message := range []session.Message{
		session.NewMessage(run.SessionID, "user", "preserve the earlier answer"),
		session.NewEvidenceMessage(run.SessionID, session.SourceToolResult, "hidden-tool", "HiddenBranchSearchCanary"),
		session.NewMessage(run.SessionID, "assistant", full),
	} {
		if _, err := f.store.SaveSessionMessage(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	recorded, err := f.store.ListRunEvents(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var anchor string
	for _, event := range recorded {
		if event.Type == events.SessionMessageEvent {
			var payload struct{ Role string }
			_ = json.Unmarshal([]byte(event.PayloadJSON), &payload)
			if payload.Role == "assistant" {
				anchor = event.EventID
			}
		}
	}
	branch, err := application.NewThreadBranchService(f.store).Branch(ctx, domain.ThreadBranchRequest{
		Version: domain.ThreadBranchProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), SourceEventID: anchor,
		OperationKey: "branch-search-inherited-reply", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	var matches []ThreadSearchResultView
	decodeData(t, f.get(t, "/api/v1/threads/search?q=InheritedSearchNeedle&thread_id="+branch.Thread.ID), &matches)
	if len(matches) != 1 || matches[0].ThreadID != branch.Thread.ID || matches[0].Role != "assistant" || matches[0].MessageID == anchor {
		t.Fatalf("inherited reply was not searchable: %#v", matches)
	}
	var located ThreadSavedMessageView
	decodeData(t, f.get(t, "/api/v1/threads/"+branch.Thread.ID+"/messages/"+matches[0].MessageID), &located)
	if located.Content != full {
		t.Fatalf("inherited reply body was truncated: got %d want %d bytes", len(located.Content), len(full))
	}
	var window []ThreadTranscriptItemView
	decodeData(t, f.get(t, "/api/v1/threads/"+branch.Thread.ID+"/transcript?limit=2&cursor="+url.QueryEscape(located.TranscriptCursor)), &window)
	if len(window) == 0 || window[len(window)-1].ID != matches[0].MessageID || window[len(window)-1].SourceRef != "branch-assistant:"+anchor {
		t.Fatalf("inherited reply cursor did not locate its commentary: %#v", window)
	}
	for _, scope := range []string{"", "&thread_id=" + branch.Thread.ID} {
		decodeData(t, f.get(t, "/api/v1/threads/search?q=HiddenBranchSearchCanary"+scope), &matches)
		if len(matches) != 0 {
			t.Fatalf("hidden inherited context was searchable: %#v", matches)
		}
	}
}

func TestSavedMessageSearchFindsFullCompactedHistoryAndLocatesAcrossRuns(t *testing.T) {
	t.Run("all conversations", func(t *testing.T) { testSavedMessageSearchHistory(t, false) })
	t.Run("current conversation", func(t *testing.T) { testSavedMessageSearchHistory(t, true) })
}

func testSavedMessageSearchHistory(t *testing.T, scoped bool) {
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
	filter := ""
	if scoped {
		filter = "&thread_id=" + url.QueryEscape(threadID)
		_, other, err := runs.Create(ctx, application.CreateRunRequest{Goal: "SavedNeedle in another conversation", Profile: "review"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.SaveSessionMessage(ctx, session.NewMessage(other.SessionID, "assistant", "unrelated SavedNeedle answer")); err != nil {
			t.Fatal(err)
		}
	}
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
	envelope := decodeData(t, f.get(t, "/api/v1/threads/search?q=SavedNeedle&limit=1"+filter), &first)
	if len(first) != 1 || first[0].Role != "assistant" || envelope.Page.NextCursor == "" {
		t.Fatalf("first search page: %#v %#v", first, envelope.Page)
	}
	if _, err := f.store.SaveSessionMessage(ctx, session.NewMessage(continued.Run.SessionID, "assistant", "SavedNeedle appended after the search cursor")); err != nil {
		t.Fatal(err)
	}
	var next []ThreadSearchResultView
	decodeData(t, f.get(t, "/api/v1/threads/search?q=savedneedle&limit=1"+filter+"&cursor="+url.QueryEscape(envelope.Page.NextCursor)), &next)
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
	for _, changed := range []string{
		"q=other" + filter,
		"q=savedneedle&include_archived=true" + filter,
		"q=savedneedle&thread_id=" + threadID + "-other",
	} {
		assertAPIError(t, f.get(t, "/api/v1/threads/search?"+changed+"&cursor="+url.QueryEscape(envelope.Page.NextCursor)), http.StatusBadRequest, "INVALID_ARGUMENT")
	}
	// Neither direction may carry a cursor between global and current-chat search.
	changedScope := "&thread_id=" + threadID
	if scoped {
		changedScope = ""
	}
	assertAPIError(t, f.get(t, "/api/v1/threads/search?q=savedneedle"+changedScope+"&cursor="+url.QueryEscape(envelope.Page.NextCursor)), http.StatusBadRequest, "INVALID_ARGUMENT")
}

func TestSavedMessageSearchPrivacyLifecycleTitlesAndValidation(t *testing.T) {
	f := newAPIFixture(t)
	ctx := t.Context()
	filter := "&thread_id=" + domain.InitialThreadID(f.run.ID)
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
		for _, scope := range []string{"", filter} {
			var matches []ThreadSearchResultView
			decodeData(t, f.get(t, "/api/v1/threads/search?q="+query+scope), &matches)
			if len(matches) != 0 {
				t.Fatalf("private text produced a match for %s: %#v", query, matches)
			}
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
	decodeData(t, f.get(t, "/api/v1/threads/search?q="+url.QueryEscape("inspect durable agent state")+filter), &matches)
	if len(matches) != 0 {
		t.Fatalf("current-chat search returned a title instead of a message: %#v", matches)
	}
	decodeData(t, f.get(t, "/api/v1/threads/search?q=public&thread_id=unknown-thread"), &matches)
	if len(matches) != 0 {
		t.Fatalf("unknown Thread matched another conversation: %#v", matches)
	}
	for _, path := range []string{"?q=", "?q=" + strings.Repeat("x", 257), "?q=a&q=b", "?q=a&limit=101", "?q=a&include_archived=invalid", "?q=a&include_archived=true&include_archived=false", "?q=a&unknown=true",
		"?q=a&thread_id=", "?q=a&thread_id=one&thread_id=two", "?q=a&thread_id=%20one", "?q=a&thread_id=one%2Ftwo", "?q=a&thread_id=one%00", "?q=a&thread_id=" + strings.Repeat("x", 257)} {
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
	for _, scope := range []string{"", filter} {
		decodeData(t, f.get(t, "/api/v1/threads/search?q=public"+scope), &matches)
		if len(matches) != 0 {
			t.Fatalf("archived thread included by default: %#v", matches)
		}
		decodeData(t, f.get(t, "/api/v1/threads/search?q=public&include_archived=true"+scope), &matches)
		if len(matches) != 1 || matches[0].ThreadStatus != "archived" {
			t.Fatalf("archived search: %#v", matches)
		}
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
	for _, scope := range []string{"", filter} {
		decodeData(t, f.get(t, "/api/v1/threads/search?q=public&include_archived=true"+scope), &matches)
		if len(matches) != 0 {
			t.Fatalf("deleted history was searchable: %#v", matches)
		}
	}
	assertAPIError(t, f.get(t, "/api/v1/threads/"+thread.ID+"/messages/"+messageID), http.StatusNotFound, "NOT_FOUND")
}
