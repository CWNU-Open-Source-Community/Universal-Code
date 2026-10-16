package application_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/session"
)

func TestThreadHistoryRecallConfirmedMemoryAndVisibleNotesThroughNativeTools(t *testing.T) {
	st := openHistoryRecallStore(t, filepath.Join(t.TempDir(), "memory-recall.db"))
	defer st.Close()
	_, first := createHistoryRecallRun(t, st, "memory-recall", "local fixture\n")
	const query = "原生工具记忆回读标记"
	memories := application.NewContextMemoryService(st)
	project, err := memories.Create(t.Context(), contextmgr.CreateMemoryRequest{
		Scope: contextmgr.MemoryScopeProject, ScopeID: "ws-memory-recall", Title: "项目偏好",
		Content: query + "：保留历史兼容接口。", SourceRef: "operator-review/project", RequestedBy: "desktop_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	user, err := memories.Create(t.Context(), contextmgr.CreateMemoryRequest{
		Scope: contextmgr.MemoryScopeUser, ScopeID: contextmgr.LocalUserMemoryScope, Title: "答复偏好",
		Content: query + "：使用中文说明。", SourceRef: "operator-review/user", RequestedBy: "desktop_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.NewNoteService(st).Create(t.Context(), application.CreateNoteRequest{
		RunID: first.ID, Title: "原Thread笔记", Content: query + "：FOREIGN_THREAD_NOTE_CANARY", Visibility: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{
		Goal: "Recall confirmed project memory in another Thread", Profile: "code", Surface: "code", Phase: "deliver",
		WorkspaceID: "ws-memory-recall", ModelRoute: "tool-loop/model", Interactive: true,
		Budget: domain.Budget{MaxTurns: 20, MaxToolCalls: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	note, err := application.NewNoteService(st).Create(t.Context(), application.CreateNoteRequest{
		RunID: run.ID, Title: "当前Thread笔记", Content: query + "：核对本地测试结果。", Visibility: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{project.ID: project.Content, user.ID: user.Content, note.ID: note.Content}
	seen := map[string]bool{}
	step := 0
	p := newHistoryProtocolProvider(t, "tool-loop", "model")
	p.handler = func(request llm.ChatRequest) (*llm.ChatResponse, error) {
		if step == 0 {
			step++
			for _, memory := range []contextmgr.Memory{project, user} {
				found := false
				for _, message := range request.Messages {
					if strings.Contains(message.Content, "long_term_memory.v1") && strings.Contains(message.Content, memory.ID) {
						found = true
					}
				}
				if !found {
					return nil, fmt.Errorf("new Thread did not receive its confirmed %s memory", memory.Scope)
				}
			}
			return toolResponse("memory-search", "history_search", historyRecallJSON(domain.HistorySearchRequest{Query: query})), nil
		}
		var reads []llm.ToolCall
		for _, message := range request.Messages {
			for _, result := range message.ToolResults {
				if seen[result.ToolCallID] {
					continue
				}
				seen[result.ToolCallID] = true
				var outer struct{ Tool, Status, Stdout string }
				if err := json.Unmarshal([]byte(result.Content), &outer); err != nil {
					return nil, err
				}
				if result.IsError || outer.Status != "completed" {
					return nil, fmt.Errorf("memory recall tool failed: %s", outer.Status)
				}
				switch outer.Tool {
				case "history_search":
					var found domain.HistorySearchResult
					if err := json.Unmarshal([]byte(outer.Stdout), &found); err != nil {
						return nil, err
					}
					if found.InstructionAuthorized || found.RunID != run.ID || found.ThreadID != domain.InitialThreadID(run.ID) || found.HasMore || len(found.Records) != len(expected) {
						return nil, fmt.Errorf("memory search scope or count changed: %+v", found)
					}
					for index, record := range found.Records {
						id := record.MemoryID
						if record.Kind == "note" {
							id = record.NoteID
						}
						body, exists := expected[id]
						if !exists || record.ContentSHA256 != session.ContentSHA256(body) || record.Status != "active" || record.SourceVersion != 1 || record.OriginalInstructionAuthorized {
							return nil, fmt.Errorf("unexpected recalled source: %+v", record)
						}
						reads = append(reads, llm.ToolCall{ID: fmt.Sprintf("memory-read-%d", index), Name: "history_read",
							Arguments: json.RawMessage(historyRecallReadJSON(domain.HistoryReadRequest{SourceID: record.SourceID, ExpectedSHA256: record.ContentSHA256}))})
					}
				case "history_read":
					var page domain.HistoryReadResult
					if err := json.Unmarshal([]byte(outer.Stdout), &page); err != nil {
						return nil, err
					}
					id := page.Record.MemoryID
					if page.Record.Kind == "note" {
						id = page.Record.NoteID
					}
					body, exists := expected[id]
					if !exists || page.Content != body || page.HasMore || page.InstructionAuthorized || page.ContentSHA256 != session.ContentSHA256(body) || page.Record.SourceVersion != 1 {
						return nil, fmt.Errorf("memory read lost exact source or authority boundary: %+v", page)
					}
					delete(expected, id)
				}
			}
		}
		step++
		if len(reads) > 0 {
			return &llm.ChatResponse{ToolCalls: reads}, nil
		}
		if len(expected) != 0 {
			return nil, fmt.Errorf("memory sources were never read: %v", expected)
		}
		return historyRecallFinish(), nil
	}
	submitHistoryRecall(t, historyRecallTurns(st, p), run.ID, "native-memory-recall", "Retrieve the confirmed preferences and this conversation's visible notes through the offered history tools.")
	if len(expected) != 0 || step != 3 {
		t.Fatalf("native memory recall did not finish: step=%d pending=%v", step, expected)
	}
	calls := historyRecallCalls(t, st, run.ID)
	if len(calls) != 4 {
		t.Fatalf("unexpected persisted recall tools: %+v", calls)
	}
	for _, call := range calls {
		if call.Status != domain.SupervisorToolCompleted || (call.ToolName != "history_search" && call.ToolName != "history_read") || strings.Contains(call.ResultJSON, "FOREIGN_THREAD_NOTE_CANARY") {
			t.Fatalf("native tools leaked or executed unexpected work: %+v", call)
		}
	}
	permission, err := st.GetRunExecutionPermission(t.Context(), run.ID)
	if err != nil || permission.CapabilityGrant || permission.ExecutionAuthorized || permission.ProcessEnabled {
		t.Fatalf("memory recall granted authority: %+v %v", permission, err)
	}
}
