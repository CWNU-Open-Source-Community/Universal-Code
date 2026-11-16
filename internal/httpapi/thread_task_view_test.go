package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"cyberagent-workbench/internal/threadtranscript"
)

func TestThreadTranscriptTaskViewPreservesExactEndingAndOptionalFields(t *testing.T) {
	now := time.Now().UTC()
	for _, state := range []string{"running", "completed", "interrupted", "failed"} {
		t.Run(state, func(t *testing.T) {
			item := threadtranscript.Item{ID: "assistant-event", RunID: "task-run", Task: &threadtranscript.Task{
				ID: "operator-input", InputMessageID: "operator-input", State: state, StartedAt: &now,
			}}
			if state != "running" {
				item.Task.FinishedAt = &now
				item.Task.ChangeSourceEventID = "handoff-ending"
			}
			if state == "completed" {
				item.Task.FinalMessageID, item.Task.ChangeSourceEventID = item.ID, item.ID
			}
			data, err := json.Marshal(threadTranscriptItemView(item))
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				ID   string                   `json:"id"`
				Task ThreadTranscriptTaskView `json:"task"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			if result.Task.ID != item.Task.ID || result.Task.InputMessageID != item.Task.InputMessageID || result.Task.State != state || result.Task.ChangeSourceEventID != item.Task.ChangeSourceEventID || result.Task.FinalMessageID != item.Task.FinalMessageID || result.Task.StartedAt == nil {
				t.Fatalf("task lost across HTTP view: %s", data)
			}
			if state == "completed" && result.Task.FinalMessageID != result.ID {
				t.Fatalf("final ID changed: %s", data)
			}
			if state == "running" {
				var payload map[string]any
				if err := json.Unmarshal(data, &payload); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"finished_at", "final_message_id", "change_source_event_id"} {
					if _, exists := payload["task"].(map[string]any)[key]; exists {
						t.Fatalf("running task includes %s", key)
					}
				}
			}
		})
	}
	data, err := json.Marshal(threadTranscriptItemView(threadtranscript.Item{}))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload["task"]; exists {
		t.Fatalf("unknown task was emitted: %s", data)
	}
}
