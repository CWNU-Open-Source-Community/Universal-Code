package application_test

import (
	"fmt"
	"strings"
	"testing"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
)

func TestThreadConsumedContinuationCorrectionReachesNextSegmentWithoutReplay(t *testing.T) {
	responses := []*llm.ChatResponse{
		toolResponse("one", "note_create", `{"title":"One","content":"actual first effect"}`),
		textResponse(`{"version":"root_lifecycle.v1","action":"continue","message":"Next note is still needed."}`),
	}
	for round := 2; round <= domain.MaxSupervisorToolRounds; round++ {
		responses = append(responses, toolResponse(fmt.Sprintf("note-%d", round), "note_create",
			fmt.Sprintf(`{"title":"Note %d","content":"actual effect %d"}`, round, round)))
	}
	wantNotes := domain.MaxSupervisorToolRounds + 1
	wantCalls := domain.MaxSupervisorToolRounds + 4
	responses = append(responses,
		textResponse(`{"version":"root_lifecycle.v1","action":"continue","message":"One more note is needed in the next segment."}`),
		toolResponse("final-segment-note", "note_create", fmt.Sprintf(`{"title":"Note %d","content":"actual final effect"}`, wantNotes)),
		textResponse(fmt.Sprintf(`{"version":"root_lifecycle.v1","action":"finish","message":"Saved %d notes.","summary":"Durable notes saved."}`, wantNotes)),
	)
	provider := &scriptedToolProvider{responses: responses}
	st, turns, request := threadControlFixture(t, provider)
	request.Content = fmt.Sprintf("Save %d notes and verify the count.", wantNotes)
	result, err := turns.Execute(t.Context(), request)
	if err != nil || len(provider.Requests()) != wantCalls {
		t.Fatalf("calls=%d result=%+v err=%v", len(provider.Requests()), result, err)
	}
	notes, err := st.ListNotes(t.Context(), domain.NoteFilter{RunID: result.Submission.Run.ID})
	if err != nil || len(notes) != wantNotes {
		t.Fatalf("notes=%d err=%v", len(notes), err)
	}
	boundary := provider.Requests()[domain.MaxSupervisorToolRounds+1]
	if len(boundary.Tools) != 0 || boundary.Metadata["protocol_repair"] != "1" ||
		!strings.Contains(boundary.Messages[len(boundary.Messages)-1].Content, "Return continue") {
		t.Fatal("consumed correction did not reach the normal no-tool scheduling boundary")
	}
	list, err := st.ListRunEvents(t.Context(), result.Submission.Run.ID)
	if err != nil || countEventType(list, events.ProtocolRepairRequestedEvent) != 1 ||
		countEventType(list, events.SupervisorRunCompletedEvent) != 0 {
		t.Fatal("single repair or resumable Run state changed")
	}
	if _, err := turns.Execute(t.Context(), request); err != nil || len(provider.Requests()) != wantCalls {
		t.Fatal("same-key replay repeated the completed work")
	}
}

func TestThreadToolFreeContinueUsesOneCorrectionWithoutConsumingInput(t *testing.T) {
	for _, test := range []struct {
		name                 string
		after                []*llm.ChatResponse
		wantCalls, wantNotes int
		wantError            bool
		wantStatus           domain.RunStatus
	}{
		{"next tool then explicit finish", []*llm.ChatResponse{
			toolResponse("next", "note_create", `{"title":"Second note","content":"second actual effect"}`),
			textResponse(`{"version":"root_lifecycle.v1","action":"finish","message":"已保存两条笔记。"}`),
		}, 4, 2, false, domain.RunRunning},
		{"explicit finish chooses current answer", []*llm.ChatResponse{textResponse(`{"version":"root_lifecycle.v1","action":"finish","message":"已保存第一条笔记；此处结束当前回复。"}`)}, 3, 1, false, domain.RunRunning},
		{"explicit wait requests external input", []*llm.ChatResponse{textResponse(`{"version":"root_lifecycle.v1","action":"wait","message":"第二条笔记需要你提供内容。","reason":"需要第二条笔记的内容"}`)}, 3, 1, false, domain.RunPaused},
		{"another continue exhausts rather than claims completion", []*llm.ChatResponse{textResponse(`{"version":"root_lifecycle.v1","action":"continue","message":"下一步仍未执行。"}`)}, 3, 1, true, ""},
		{"captured 013 continue with reason remains rejected", []*llm.ChatResponse{textResponse(`{"version":"root_lifecycle.v1","action":"continue","message":"styles.css 与 index.html 的替换提案已获自动授权但尚未写入；下一步先应用 styles.css 提案，再应用 index.html 提案。","reason":"需要先执行已授权的 workspace_apply 才能完成文件变更。"}`)}, 3, 1, true, domain.RunPaused},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &scriptedToolProvider{responses: append([]*llm.ChatResponse{
				toolResponse("first", "note_create", `{"title":"First note","content":"first actual effect"}`),
				textResponse(`{"version":"root_lifecycle.v1","action":"continue","message":"继续处理当前请求。"}`),
			}, test.after...)}
			st, turns, request := threadControlFixture(t, provider)
			request.Content = "保存当前所需笔记；缺少内容时向我提问。"
			result, err := turns.Execute(t.Context(), request)
			if (err != nil) != test.wantError || len(provider.Requests()) != test.wantCalls {
				t.Fatalf("calls=%d err=%v", len(provider.Requests()), err)
			}
			thread, lookupErr := st.GetThread(t.Context(), request.ThreadID)
			if lookupErr != nil || thread.Status != domain.ThreadActive {
				t.Fatalf("thread=%+v err=%v", thread, lookupErr)
			}
			run, lookupErr := st.GetRun(t.Context(), thread.LastRunID)
			if lookupErr != nil || (test.wantStatus != "" && run.Status != test.wantStatus) {
				t.Fatalf("Run state=%s err=%v", run.Status, lookupErr)
			}
			notes, lookupErr := st.ListNotes(t.Context(), domain.NoteFilter{RunID: run.ID})
			if lookupErr != nil || len(notes) != test.wantNotes {
				t.Fatalf("notes=%d err=%v", len(notes), lookupErr)
			}
			requests := provider.Requests()
			firstNoteID := ""
			for _, note := range notes {
				if note.Title == "First note" {
					firstNoteID = note.ID
				}
			}
			if firstNoteID == "" || requests[2].Metadata["protocol_repair"] != "1" || !hasToolSpec(requests[2], "note_create") || !hasToolResult(requests[2], firstNoteID) {
				t.Fatal("correction lost offered tools or completed evidence")
			}
			list, lookupErr := st.ListRunEvents(t.Context(), run.ID)
			if lookupErr != nil || countEventType(list, events.ProtocolRepairRequestedEvent) != 1 || countEventType(list, events.ProtocolRepairStartedEvent) != 1 || countEventType(list, events.SupervisorRunCompletedEvent) != 0 {
				t.Fatal("single repair or resumable Run boundary changed")
			}
			if !test.wantError && result.Submission.Message.Status != domain.OperatorSteeringCommitted {
				t.Fatal("explicit terminal reply did not settle original input")
			}
			_, _ = turns.Execute(t.Context(), request)
			if len(provider.Requests()) != test.wantCalls {
				t.Fatal("original key replay called the model or repeated tools")
			}
		})
	}
}

func TestThreadRecoveredTrailingReplyDoesNotAcquireUnverifiableCorrection(t *testing.T) {
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
		toolResponse("first", "note_create", `{"title":"First note","content":"actual effect"}`),
		textResponse("{\"version\":\"root_lifecycle.v1\",\"action\":\"continue\",\"message\":\"Current report\"}\n\nBounded trailing commentary."),
	}}
	st, turns, request := threadControlFixture(t, provider)
	result, err := turns.Execute(t.Context(), request)
	if err != nil || len(provider.Requests()) != 2 {
		t.Fatalf("calls=%d err=%v", len(provider.Requests()), err)
	}
	cp, found, err := st.GetSupervisorCheckpoint(t.Context(), result.Submission.Run.ID)
	if err != nil || !found || cp.TotalTokens != 8 || cp.RepairPhase != domain.ProtocolRepairNone {
		t.Fatalf("compatibility lost actual usage: %+v %v", cp, err)
	}
}

func TestThreadNoToolChatContinueRetainsSingleReplyCompatibility(t *testing.T) {
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{textResponse(`{"version":"root_lifecycle.v1","action":"continue","message":"你好。"}`)}}
	st, turns, request := threadControlFixture(t, provider)
	request.Content = "你好"
	result, err := turns.Execute(t.Context(), request)
	if err != nil || len(provider.Requests()) != 1 || result.Submission.Message.Status != domain.OperatorSteeringCommitted {
		t.Fatalf("calls=%d err=%v", len(provider.Requests()), err)
	}
	list, err := st.ListRunEvents(t.Context(), result.Submission.Run.ID)
	if err != nil || countEventType(list, events.ProtocolRepairRequestedEvent) != 0 {
		t.Fatal("ordinary chat acquired a continuation loop")
	}
}
