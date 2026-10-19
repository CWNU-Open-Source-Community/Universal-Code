package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/threadtranscript"
)

func branchReplyEvent(t *testing.T, st *SQLiteStore, run domain.Run, text string) string {
	t.Helper()
	message, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "assistant", text))
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := st.db.QueryRow(`SELECT event_id FROM run_events WHERE run_id=? AND type=? AND subject_id=?`, run.ID, events.SessionMessageEvent, strconv.FormatInt(message.ID, 10)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestThreadBranchExactPrefixCrossRunReplayRestartAndNestedBranch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "branch.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	runs := application.NewRunService(st)
	_, first, err := runs.Create(t.Context(), application.CreateRunRequest{Goal: "保留原要求", Profile: "review", Interactive: true, Budget: domain.Budget{MaxTurns: 8}})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("早期约束", 1600)
	if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(first.SessionID, "user", long)); err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		branchReplyEvent(t, st, first, fmt.Sprintf("历史回复 %d", i))
		if _, err := st.SaveSessionMessage(t.Context(), session.NewMessage(first.SessionID, "user", fmt.Sprintf("历史输入 %d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runs.Start(t.Context(), first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Fail(t.Context(), first.ID, "fixture end"); err != nil {
		t.Fatal(err)
	}
	continued, err := application.NewThreadService(st).Submit(t.Context(), application.SubmitThreadMessageRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(first.ID), Content: "next", OperationKey: "branch-source-successor", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	second := continued.Run
	for _, message := range []session.Message{session.NewMessage(second.SessionID, "system", "SYSTEM_AUTHORITY_CANARY"), session.NewEvidenceMessage(second.SessionID, session.SourceToolResult, "source-tool", "tool evidence without authority"), session.NewMessage(second.SessionID, "user", "second run instruction")} {
		if _, err := st.SaveSessionMessage(t.Context(), message); err != nil {
			t.Fatal(err)
		}
	}
	selected := branchReplyEvent(t, st, second, "selected answer")
	branchReplyEvent(t, st, second, "FUTURE_REPLY_CANARY")
	if _, err := st.MarkSessionMessagesCompacted(t.Context(), first.SessionID, 999999); err != nil {
		t.Fatal(err)
	}
	request := domain.ThreadBranchRequest{Version: domain.ThreadBranchProtocolVersion, ThreadID: continued.Thread.ID, SourceEventID: selected, OperationKey: "branch-selected-reply", RequestedBy: "test_operator"}
	service := application.NewThreadBranchService(st)
	var outcomes [2]application.ThreadBranchResult
	var failures [2]error
	var wait sync.WaitGroup
	for i := range 2 {
		wait.Add(1)
		go func() { defer wait.Done(); outcomes[i], failures[i] = service.Branch(t.Context(), request) }()
	}
	wait.Wait()
	for _, err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	branch := outcomes[0]
	if branch.Thread.ID == request.ThreadID || branch.Thread.ID != outcomes[1].Thread.ID || branch.Run.ID == second.ID || branch.Run.SessionID == second.SessionID || branch.Run.Status != domain.RunCreated {
		t.Fatal("branch identities or concurrent replay are wrong")
	}
	messages, err := st.ListSessionMessages(t.Context(), branch.Session.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for _, m := range messages {
		joined.WriteString(m.Content)
		if m.Role == "system" || m.Role == "assistant" || (m.Role == "tool" && m.Provenance.InstructionAuthorized) || m.Compacted {
			t.Fatalf("imported context inherited authority or was hidden: %+v", m)
		}
	}
	for _, text := range []string{long, "历史回复 0", "second run instruction", "tool evidence without authority", "selected answer"} {
		if !strings.Contains(joined.String(), text) {
			t.Fatalf("lost original context: %q", text[:min(len(text), 40)])
		}
	}
	for _, text := range []string{"FUTURE_REPLY_CANARY", "SYSTEM_AUTHORITY_CANARY"} {
		if strings.Contains(joined.String(), text) {
			t.Fatalf("copied outside selected context: %s", text)
		}
	}
	sources, err := st.ListThreadTranscriptSourceBefore(t.Context(), branch.Thread.ID, 0, 0, 101)
	if err != nil {
		t.Fatal(err)
	}
	items, err := threadtranscript.Build(branch.Thread.ID, sources)
	if err != nil {
		t.Fatal(err)
	}
	var nestedAnchor string
	answers := 0
	for _, item := range items {
		if item.Detail == "selected answer" && item.Source == "model" {
			answers++
			nestedAnchor = item.ID
		}
	}
	if answers != 1 || nestedAnchor == "" {
		t.Fatal("selected answer is not displayed exactly once")
	}
	request.OperationKey = "nested-branch-selected"
	request.ThreadID = branch.Thread.ID
	request.SourceEventID = nestedAnchor
	nested, err := service.Branch(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	nestedMessages, err := st.ListSessionMessages(t.Context(), nested.Session.ID, false)
	if err != nil || len(nestedMessages) != len(messages) {
		t.Fatalf("nested branch lost prefix or duplicated model evidence: %d vs %d %v", len(nestedMessages), len(messages), err)
	}
	permission, err := st.GetRunExecutionPermission(t.Context(), branch.Run.ID)
	if err != nil || permission.ExecutionAuthorized || permission.ProcessEnabled || permission.CapabilityGrant {
		t.Fatalf("branch inherited execution permission: %+v %v", permission, err)
	}
	eventsBefore, err := st.ListRunEvents(t.Context(), branch.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range eventsBefore {
		if e.Type == events.ModelStartedEvent || e.Type == events.ModelCompletedEvent {
			t.Fatal("creating a branch manufactured model usage")
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	request.ThreadID = continued.Thread.ID
	request.SourceEventID = selected
	request.OperationKey = "branch-selected-reply"
	replayed, err := application.NewThreadBranchService(st).Branch(t.Context(), request)
	if err != nil || !replayed.Replayed || replayed.Thread.ID != branch.Thread.ID {
		t.Fatalf("restart replay=%+v err=%v", replayed, err)
	}
	request.SourceEventID = eventsBefore[0].EventID
	if _, err := application.NewThreadBranchService(st).Branch(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("changed source reused branch key: %v", err)
	}
}

func TestThreadBranchPreservesImageAndFileBindingsWithinWorkspace(t *testing.T) {
	st, _, run, original, _ := threadIntentFixture(t)
	original.Files = nil
	original.OperationKey = "branch-attachment-input"
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewNRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	imageValue, err := st.SaveWorkspaceImage(t.Context(), "ws-intent", "branch-image-upload", "image/png", "source.png", pixels.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	original.Images = []domain.ImageReference{{ID: imageValue.ID, SHA256: imageValue.SHA256}}
	fileValue, err := st.SaveWorkspaceFileAttachment(t.Context(), "ws-intent", "branch-file-upload", "text/plain", "source.txt", []byte("original attachment bytes"))
	if err != nil {
		t.Fatal(err)
	}
	original.Attachments = []domain.FileAttachmentReference{{ID: fileValue.ID, WorkspaceID: fileValue.WorkspaceID, SHA256: fileValue.SHA256, ByteSize: fileValue.ByteSize}}
	if _, err := st.ReserveThreadMessageIntent(t.Context(), original); err != nil {
		t.Fatal(err)
	}
	queued, err := st.CommitThreadMessage(t.Context(), original, run.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.SaveSessionMessage(t.Context(), session.NewMessage(run.SessionID, "user", original.Content))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE operator_steering_messages SET status='committed',session_message_id=?,committed_at=? WHERE id=?`, user.ID, ts(time.Now().UTC()), queued.Message.ID); err != nil {
		t.Fatal(err)
	}
	anchor := branchReplyEvent(t, st, run, "reply with saved attachments")
	result, err := application.NewThreadBranchService(st).Branch(t.Context(), domain.ThreadBranchRequest{Version: domain.ThreadBranchProtocolVersion, ThreadID: original.ThreadID, SourceEventID: anchor, OperationKey: "branch-attachments", RequestedBy: "test_operator"})
	if err != nil {
		t.Fatal(err)
	}
	var copiedID string
	var images, files int
	if err := st.db.QueryRow(`SELECT id,image_count,attachment_count FROM operator_steering_messages WHERE run_id=?`, result.Run.ID).Scan(&copiedID, &images, &files); err != nil || images != 1 || files != 1 {
		t.Fatalf("copied attachments: %d/%d %v", images, files, err)
	}
	var copiedImage, copiedFile string
	if err := st.db.QueryRow(`SELECT image_id FROM thread_message_images WHERE message_id=?`, copiedID).Scan(&copiedImage); err != nil {
		t.Fatal(err)
	}
	if err := st.db.QueryRow(`SELECT attachment_id FROM thread_message_attachments WHERE message_id=?`, copiedID).Scan(&copiedFile); err != nil {
		t.Fatal(err)
	}
	if copiedImage != imageValue.ID || copiedFile != fileValue.ID {
		t.Fatal("attachment identity changed")
	}
	sources, err := st.ListThreadTranscriptSourceBefore(t.Context(), result.Thread.ID, 0, 0, 101)
	if err != nil {
		t.Fatal(err)
	}
	items, err := threadtranscript.Build(result.Thread.ID, sources)
	if err != nil {
		t.Fatal(err)
	}
	visibleInputs := 0
	for _, item := range items {
		if item.Source == "operator" {
			visibleInputs++
			if item.SourceRef != copiedID {
				t.Fatal("visible input cannot resolve its saved attachment references")
			}
		}
	}
	if visibleInputs != 1 {
		t.Fatalf("attachment input displayed %d times", visibleInputs)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "original attachment bytes") {
		t.Fatal("branch response exposed file bodies")
	}
}
