package application

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/contextmgr"
	"cyberagent-workbench/internal/domain"
)

func TestSupervisorMemoryContextHonorsTokenBudgetAndPriority(t *testing.T) {
	now := time.Now().UTC()
	workItems := make([]domain.WorkItem, 20)
	for index := range workItems {
		workItems[index] = domain.WorkItem{
			ID: fmt.Sprintf("work-%02d", index), RunID: "run-1", Title: fmt.Sprintf("work item %02d", index),
			Description: strings.Repeat("work detail ", 80), Status: domain.WorkItemPending,
			Priority: domain.WorkItemPriorityHigh, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
	}
	notes := make([]domain.Note, 40)
	for index := range notes {
		notes[index] = domain.Note{
			ID: fmt.Sprintf("note-%02d", index), RunID: "run-1", Title: fmt.Sprintf("note %02d", index),
			Content: strings.Repeat("durable note content ", 200), Category: domain.NoteObservation,
			Visibility: domain.NoteVisibilityRun, Status: domain.NoteActive, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
	}
	notes[0].Category = domain.NoteDecision
	notes[0].Pinned = true
	selection, err := supervisorMemoryContext(contextmgr.Summary{ID: 1, Content: strings.Repeat("summary ", 400)}, true, workItems, notes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if selection.EstimatedTokens > maxSupervisorMemoryTokens || selection.TokenBudget != maxSupervisorMemoryTokens ||
		len(selection.OmittedSources) == 0 {
		t.Fatalf("memory selection did not enforce budget: %#v", selection)
	}
	for _, expected := range []struct{ kind, id string }{
		{"summary", "summary-1"}, {"work_board", "active"}, {"note", "note-00"},
	} {
		if !hasContextSource(selection.IncludedSources, expected.kind, expected.id) {
			t.Fatalf("priority source %s/%s was omitted: %#v", expected.kind, expected.id, selection)
		}
	}
}

func TestSupervisorMemoryContextAlwaysIncludesBoundedRootInboxFirst(t *testing.T) {
	now := time.Now().UTC()
	inbox := make([]domain.AgentMessage, domain.MaxRootInboxContextMessages)
	for index := range inbox {
		payload, err := json.Marshal(domain.AgentAttemptFailurePayload{
			Version: domain.AgentAttemptFailureVersion,
			AgentID: fmt.Sprintf("agent-child-%d", index), AttemptID: fmt.Sprintf("attempt-%d", index),
			FailureCode: "provider_error", Reason: strings.Repeat("failure detail ", 200),
			RetryScheduled: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		inbox[index] = domain.AgentMessage{
			ID: fmt.Sprintf("message-%d", index), RunID: "run-1",
			SenderAgentID: fmt.Sprintf("agent-child-%d", index), RecipientAgentID: "agent-root",
			Sequence: int64(index + 1), Kind: domain.AgentMessageNotification,
			Semantic: domain.AgentMessageSemanticMessage, PayloadJSON: string(payload),
			Status: domain.AgentMessagePending, CreatedAt: now,
		}
	}
	selection, err := supervisorMemoryContext(contextmgr.Summary{
		ID: 9, Content: strings.Repeat("large summary context ", 1200),
	}, true, nil, nil, inbox)
	if err != nil {
		t.Fatal(err)
	}
	if selection.EstimatedTokens > maxSupervisorMemoryTokens {
		t.Fatalf("root inbox context exceeded memory budget: %#v", selection)
	}
	for _, message := range inbox {
		if !hasContextSource(selection.IncludedSources, "agent_inbox", message.ID) {
			t.Fatalf("bound root inbox message was omitted: message=%s selection=%#v",
				message.ID, selection)
		}
	}
}

func TestSupervisorMemoryContextReservesConfirmedMemoryBeforeRunBackground(t *testing.T) {
	memories, err := longTermMemoryContextSections([]contextmgr.Memory{
		supervisorTestMemory(t, "confirmed-style", contextmgr.MemoryScopeUser, "Prefer concise reports."),
	})
	if err != nil {
		t.Fatal(err)
	}
	background := make([]contextmgr.Section, 8)
	for index := range background {
		background[index] = contextmgr.Section{Kind: "note", SourceID: fmt.Sprintf("decision-%d", index),
			Content: strings.Repeat("n", 4096), Priority: 850}
	}
	selection, err := supervisorMemoryContext(contextmgr.Summary{}, false, nil, nil, nil, background, memories)
	if err != nil {
		t.Fatal(err)
	}
	if !hasContextSource(selection.IncludedSources, "long_term_memory", "confirmed-style") ||
		!hasContextSource(selection.OmittedSources, "note", "decision-7") ||
		selection.EstimatedTokens > selection.TokenBudget {
		t.Fatalf("ordinary background displaced the reserved memory: %+v", selection)
	}
}

func TestSupervisorMemoryContextAllocatesInstructionsAndRequiredSourcesBeforeMemory(t *testing.T) {
	memories, err := longTermMemoryContextSections([]contextmgr.Memory{
		supervisorTestMemory(t, "confirmed-style", contextmgr.MemoryScopeUser, "Prefer concise reports."),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, requiredTokens := range []int{768, 900, 1024} {
		t.Run(fmt.Sprint(requiredTokens), func(t *testing.T) {
			protected := []contextmgr.Section{{Kind: "required", SourceID: "existing-rule",
				Content: strings.Repeat("r", requiredTokens*4), Priority: 1, Required: true}}
			if requiredTokens == 768 {
				protected = append(protected, contextmgr.Section{Kind: "project_instruction", SourceID: "AGENTS.md",
					Content: strings.Repeat("p", 640), Priority: 760})
			}
			selection, err := supervisorMemoryContextWithinBudget(1024, false, contextmgr.Summary{}, false,
				nil, nil, nil, protected, memories)
			if err != nil || !hasContextSource(selection.IncludedSources, "required", "existing-rule") ||
				hasContextSource(selection.IncludedSources, "long_term_memory", "confirmed-style") {
				t.Fatalf("memory displaced protected context or caused refusal: %+v err=%v", selection, err)
			}
			if requiredTokens == 768 && !hasContextSource(selection.IncludedSources, "project_instruction", "AGENTS.md") {
				t.Fatal("memory displaced project instructions")
			}
			reason := "required_context_budget"
			if requiredTokens == 768 {
				reason = "project_instruction_budget"
			}
			if len(selection.OmittedSources) != 1 || selection.OmittedSources[0].OmissionReason != reason {
				t.Fatalf("memory omission lost its budget reason: %+v", selection.OmittedSources)
			}
		})
	}
	_, err = supervisorMemoryContextWithinBudget(1024, false, contextmgr.Summary{}, false, nil, nil, nil,
		[]contextmgr.Section{{Kind: "required", SourceID: "too-large", Content: strings.Repeat("r", 4100), Required: true}}, memories)
	if apperror.CodeOf(err) != apperror.CodeResourceExhausted {
		t.Fatalf("impossible Required set no longer refuses delivery: %v", err)
	}
}

func TestSupervisorMemoryContextCapsMemoryAndAuditsOversizedEntries(t *testing.T) {
	memories, err := longTermMemoryContextSections([]contextmgr.Memory{
		supervisorTestMemory(t, "a-large", contextmgr.MemoryScopeUser, strings.Repeat("l", 12*1024)),
		supervisorTestMemory(t, "b-small", contextmgr.MemoryScopeUser, "Prefer concise reports."),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, totalBudget := range []int{1024, maxSupervisorMemoryTokens, 32 * 1024} {
		selection, err := supervisorMemoryContextWithinBudget(totalBudget, false, contextmgr.Summary{}, false,
			nil, nil, nil, memories)
		if err != nil {
			t.Fatal(err)
		}
		if len(selection.Sections) != 1 || selection.Sections[0].SourceID != "b-small" || selection.Sections[0].Required ||
			selection.EstimatedTokens > min(maxSupervisorLongTermMemoryTokens, totalBudget/4) ||
			len(selection.OmittedSources) != 1 || selection.OmittedSources[0].SourceID != "a-large" ||
			selection.OmittedSources[0].OmissionReason != "long_term_memory_quota" {
			t.Fatalf("memory did not fit its explicit sub-budget: total=%d selection=%+v", totalBudget, selection)
		}
	}
}

func TestSupervisorMemoryContextSortsMemoryByScopeThenIdentity(t *testing.T) {
	first := supervisorTestMemory(t, "a-user", contextmgr.MemoryScopeUser, strings.Repeat("u", 2400))
	second := supervisorTestMemory(t, "z-user", contextmgr.MemoryScopeUser, strings.Repeat("v", 2400))
	project := supervisorTestMemory(t, "a-project", contextmgr.MemoryScopeProject, strings.Repeat("p", 2400))
	for _, records := range [][]contextmgr.Memory{{project, second, first}, {second, first, project}} {
		memories, err := longTermMemoryContextSections(records)
		if err != nil {
			t.Fatal(err)
		}
		selection, err := supervisorMemoryContext(contextmgr.Summary{}, false, nil, nil, nil, memories)
		if err != nil {
			t.Fatal(err)
		}
		if len(selection.Sections) != 2 || selection.Sections[0].SourceID != first.ID ||
			selection.Sections[1].SourceID != second.ID || len(selection.OmittedSources) != 1 ||
			selection.OmittedSources[0].SourceID != project.ID {
			t.Fatalf("memory order depended on store/list order: %+v", selection)
		}
	}
}

func supervisorTestMemory(t *testing.T, id string, scope contextmgr.MemoryScope, content string) contextmgr.Memory {
	t.Helper()
	scopeID := contextmgr.LocalUserMemoryScope
	if scope == contextmgr.MemoryScopeProject {
		scopeID = "workspace-memory-budget"
	}
	memory, err := contextmgr.PrepareMemory(contextmgr.CreateMemoryRequest{
		ID: id, Scope: scope, ScopeID: scopeID, Title: "Confirmed preference", Content: content,
		RequestedBy: "desktop_operator", ExplicitOperator: true,
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return memory
}

func hasContextSource(sources []contextmgr.Source, kind string, id string) bool {
	for _, source := range sources {
		if source.Kind == kind && source.SourceID == id {
			return true
		}
	}
	return false
}
