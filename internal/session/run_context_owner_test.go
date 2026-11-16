package session

import (
	"context"
	"strings"
	"testing"

	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
)

type contextOwnerExecutor struct{ managed bool }

func (e contextOwnerExecutor) ExecuteSessionTurn(context.Context, Session, string) (RunChatResult, bool, error) {
	return RunChatResult{RunID: "run-context-owner", ContextManaged: e.managed, Text: "committed reply"}, true, nil
}

func TestRunContextOwnerPreventsUnleasedPostCommitCompaction(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy_executor", true: "supervisor_owner"}[managed], func(t *testing.T) {
			st := newMemorySessionStore()
			manager := NewManager(st, llm.NewDefaultRouter(), policy.NewDefaultChecker()).
				WithRunChatExecutor(contextOwnerExecutor{managed})
			sess, err := manager.Create(t.Context(), "", "context ownership", "learn")
			if err != nil {
				t.Fatal(err)
			}
			for range 10 {
				if _, err := st.SaveSessionMessage(t.Context(), NewMessage(sess.ID, "user", strings.Repeat("原文。", 350))); err != nil {
					t.Fatal(err)
				}
			}
			st.failMark = true
			result, err := manager.Send(t.Context(), sess.ID, "continue")
			if managed {
				if err != nil || result.Text != "committed reply" || len(st.summaries) != 0 {
					t.Fatalf("committed executor result was changed by outer compaction: %+v %v", result, err)
				}
			} else if err == nil || len(st.summaries) != 1 {
				t.Fatalf("legacy executor lost its existing compaction path: %+v %v", result, err)
			}
		})
	}
}
