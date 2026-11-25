package application

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/session"
)

// Recompute advisory feedback from sealed calls, including after recovery. It
// never suppresses tools, changes a receipt, or authorizes the next operation.
// Historical reads outside the bounded lookup are simply unknown here.
func (s *AgentRunner) workspaceReadFeedback(ctx context.Context, checkpoint domain.SupervisorCheckpoint,
	hasTools bool, rounds []domain.SupervisorToolRound,
) (string, error) {
	if !hasTools {
		return "", nil
	}
	var previous []domain.SupervisorToolCall
	if reader, ok := s.store.(supervisorToolBoundaryStore); ok {
		var err error
		previous, err = reader.ToolBoundaryContextCalls(ctx, checkpoint)
		if err != nil {
			return "", err
		}
	}
	count := repeatedWorkspaceReadCount(previous, rounds)
	if count == 0 {
		return "", nil
	}
	return fmt.Sprintf("Execution feedback from the completed tool ledger: %d workspace_read call(s) in this segment returned only lines already observed with identical text, workspace/root, full-file hash, encoding and redaction state. Repeating these reads has added no new file content. Use relevant text already visible in this request to advance the user's task: make the requested bounded edit, run a focused check, or provide a supported read-only answer. If required text is absent, read only that missing range. Current-state verification may still justify a read. This feedback grants no permission and is not an instruction to mutate a read-only task; all normal approval and hash checks remain required.", count), nil
}

func repeatedWorkspaceReadCount(previous []domain.SupervisorToolCall, rounds []domain.SupervisorToolRound) int {
	previous = append([]domain.SupervisorToolCall(nil), previous...)
	sort.SliceStable(previous, func(i, j int) bool {
		if previous[i].Turn != previous[j].Turn {
			return previous[i].Turn < previous[j].Turn
		}
		if previous[i].Round != previous[j].Round {
			return previous[i].Round < previous[j].Round
		}
		return previous[i].Position < previous[j].Position
	})
	seen := map[string]map[int]string{}
	observe := func(call domain.SupervisorToolCall) bool {
		page, _, ok := supervisorWorkspaceReadPage(call)
		if !ok || page["redaction_count"].(float64) != 0 {
			return false
		}
		start, end := int(page["start_line"].(float64)), int(page["end_line"].(float64))
		lines := strings.Split(page["content"].(string), "\n")
		if len(lines) != end-start+1 {
			return false
		}
		identity := map[string]any{}
		for _, key := range []string{"workspace_id", "root_fingerprint", "path", "content_sha256", "encoding", "newline", "redaction_count", "total_lines", "total_bytes"} {
			identity[key] = page[key]
		}
		encoded, _ := json.Marshal(identity)
		key := string(encoded)
		if seen[key] == nil {
			seen[key] = map[int]string{}
		}
		repeated := true
		for i, line := range lines {
			digest := session.ContentSHA256(line)
			if seen[key][start+i] != digest {
				repeated = false
			}
			seen[key][start+i] = digest
		}
		return repeated
	}
	for _, call := range previous {
		observe(call)
	}
	repeated := 0
	for _, round := range rounds {
		for _, call := range round.Calls {
			if observe(call) {
				repeated++
			}
		}
	}
	return repeated
}
