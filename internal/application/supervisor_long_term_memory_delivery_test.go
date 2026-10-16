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
	"cyberagent-workbench/internal/events"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/projectconfig"
	"cyberagent-workbench/internal/store"
)

func TestSupervisorLongTermMemoryDeliverySurvivesBackgroundPressure(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "memory-pressure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, snapshot := newDeliveryTestRun(t, st, t.TempDir(), ".", map[string]string{
		"AGENTS.md": "Keep the project API stable.",
	}, map[string]projectconfig.InstructionRequirement{"AGENTS.md": projectconfig.InstructionMandatory}, false)
	service := application.NewContextMemoryService(st)
	memory := createDeliveryTestMemory(t, service, contextmgr.MemoryScopeProject, "delivery-test-workspace",
		"Confirmed format", "Keep reports short and include focused test outcomes.")
	oversized := createDeliveryTestMemory(t, service, contextmgr.MemoryScopeUser, contextmgr.LocalUserMemoryScope,
		"Long confirmed background", strings.Repeat("l", 12*1024))
	for index := range 40 {
		if _, err := application.NewNoteService(st).Create(t.Context(), application.CreateNoteRequest{
			RunID: run.ID, Title: fmt.Sprintf("Pinned decision %02d", index),
			Content: strings.Repeat("ordinary background detail ", 110), Category: "decision", Pinned: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	provider := &scriptedToolProvider{responses: []*llm.ChatResponse{
		textResponse(rootActionResponse(domain.RootActionContinue, "confirmed context received", "", "")),
	}}
	result, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID)
	if err != nil || result.ModelAttempts != 1 || len(provider.Requests()) != 1 {
		t.Fatalf("memory pressure request failed: result=%+v err=%v", result, err)
	}
	assertRequiredDeliveryRequest(t, provider.Requests()[0], snapshot)
	assertLongTermMemoryDeliveryRequest(t, provider.Requests()[0], []contextmgr.Memory{memory})
	audits := longTermMemoryDeliveryAudits(t, st, run.ID)
	if len(audits) != 1 {
		t.Fatalf("model starts=%d want=1", len(audits))
	}
	omittedNote, omittedMemory := false, false
	for _, source := range audits[0].Omitted {
		omittedNote = omittedNote || source.Kind == "note"
		omittedMemory = omittedMemory || source.Kind == "long_term_memory" && source.SourceID == oversized.ID &&
			source.Tokens > 2048 && source.OmissionReason == "long_term_memory_quota"
	}
	if !omittedNote || !omittedMemory {
		t.Fatalf("actual model audit lost background or oversized memory omissions: %+v", audits[0])
	}
	assertLongTermMemoryDeliveryAudit(t, audits[0], []contextmgr.Memory{memory})
}

func TestSupervisorLongTermMemoryDeliveryUsesCurrentRecordsAcrossTurns(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "memory-current.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, _ := newDeliveryTestRun(t, st, t.TempDir(), ".", map[string]string{
		"AGENTS.md": "Keep the project API stable.",
	}, map[string]projectconfig.InstructionRequirement{"AGENTS.md": projectconfig.InstructionMandatory}, false)
	service := application.NewContextMemoryService(st)
	project := createDeliveryTestMemory(t, service, contextmgr.MemoryScopeProject, "delivery-test-workspace",
		"Report format", "INITIAL_CONFIRMED_REPORT_FORMAT")
	user := createDeliveryTestMemory(t, service, contextmgr.MemoryScopeUser, contextmgr.LocalUserMemoryScope,
		"Language", "Prefer Chinese reports.")
	provider := &scriptedToolProvider{respond: func(_ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
		return textResponse(rootActionResponse(domain.RootActionContinue, fmt.Sprintf("current context received for turn %d", index+1), "", "")), nil
	}}
	runner := newToolLoopSupervisor(st, provider).WithGeneratedContextCompaction(false)
	step := func(expected ...contextmgr.Memory) {
		t.Helper()
		before := len(provider.Requests())
		if _, err := runner.Step(t.Context(), run.ID); err != nil {
			t.Fatal(err)
		}
		requests := provider.Requests()
		if len(requests) != before+1 {
			t.Fatalf("actual model requests=%d want=%d", len(requests), before+1)
		}
		assertLongTermMemoryDeliveryRequest(t, requests[len(requests)-1], expected)
		audits := longTermMemoryDeliveryAudits(t, st, run.ID)
		if len(audits) != len(requests) {
			t.Fatalf("model starts=%d requests=%d", len(audits), len(requests))
		}
		assertLongTermMemoryDeliveryAudit(t, audits[len(audits)-1], expected)
	}
	step(user, project)
	updatedContent := "UPDATED_CONFIRMED_REPORT_FORMAT"
	project, err = service.Update(t.Context(), project.ID, contextmgr.UpdateMemoryRequest{
		Content: &updatedContent, ExpectedVersion: project.Version, RequestedBy: "desktop_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	step(user, project)
	disabled := contextmgr.MemoryStatusDisabled
	project, err = service.Update(t.Context(), project.ID, contextmgr.UpdateMemoryRequest{
		Status: &disabled, ExpectedVersion: project.Version, RequestedBy: "desktop_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	step(user)
	active := contextmgr.MemoryStatusActive
	expires := project.CreatedAt
	retention := &expires
	if _, err := service.Update(t.Context(), project.ID, contextmgr.UpdateMemoryRequest{
		Status: &active, RetentionUntil: &retention, ExpectedVersion: project.Version, RequestedBy: "desktop_operator",
	}); err != nil {
		t.Fatal(err)
	}
	step(user)
	if deleted, err := service.Delete(t.Context(), user.ID, user.Version, "desktop_operator"); err != nil || !deleted {
		t.Fatalf("delete active memory: deleted=%t err=%v", deleted, err)
	}
	step()
	audits := longTermMemoryDeliveryAudits(t, st, run.ID)
	if len(audits) != 5 {
		t.Fatalf("model starts=%d want=5", len(audits))
	}
	for index, audit := range audits {
		if err := audit.Validate(); err != nil {
			t.Fatalf("turn %d context audit: %v", index+1, err)
		}
	}
}

func TestSupervisorLongTermMemoryDeliveryRefreshesAfterToolRound(t *testing.T) {
	for _, mutation := range []string{"update", "disable", "expire", "delete"} {
		t.Run(mutation, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "memory-tool-round.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			run := newStartedRunForProvider(t, st, "tool-loop", domain.Budget{MaxTurns: 3, MaxToolCalls: 3})
			service := application.NewContextMemoryService(st)
			original := createDeliveryTestMemory(t, service, contextmgr.MemoryScopeUser, contextmgr.LocalUserMemoryScope,
				"Report format", "ORIGINAL_CONFIRMED_MEMORY")
			var expected []contextmgr.Memory
			provider := &scriptedToolProvider{respond: func(_ llm.ChatRequest, index int) (*llm.ChatResponse, error) {
				if index > 0 {
					return textResponse(rootActionResponse(domain.RootActionContinue, "current memory received", "", "")), nil
				}
				change := contextmgr.UpdateMemoryRequest{ExpectedVersion: original.Version, RequestedBy: "desktop_operator"}
				switch mutation {
				case "update":
					content := "UPDATED_CONFIRMED_MEMORY"
					change.Content = &content
				case "disable":
					status := contextmgr.MemoryStatusDisabled
					change.Status = &status
				case "expire":
					expires := original.CreatedAt
					retention := &expires
					change.RetentionUntil = &retention
				case "delete":
					if deleted, err := service.Delete(t.Context(), original.ID, original.Version, "desktop_operator"); err != nil || !deleted {
						return nil, fmt.Errorf("delete memory: deleted=%t err=%v", deleted, err)
					}
				}
				if mutation != "delete" {
					updated, err := service.Update(t.Context(), original.ID, change)
					if err != nil {
						return nil, err
					}
					if mutation == "update" {
						expected = []contextmgr.Memory{updated}
					}
				}
				return toolResponse("memory-refresh-round", "work_item_create", `{"title":"Observe current memory"}`), nil
			}}
			result, err := newToolLoopSupervisor(st, provider).Step(t.Context(), run.ID)
			requests := provider.Requests()
			if err != nil || result.ModelAttempts != 2 || len(requests) != 2 {
				t.Fatalf("same-turn refresh failed: result=%+v outbound=%d err=%v", result, len(requests), err)
			}
			assertLongTermMemoryDeliveryRequest(t, requests[0], []contextmgr.Memory{original})
			assertLongTermMemoryDeliveryRequest(t, requests[1], expected)
			audits := longTermMemoryDeliveryAudits(t, st, run.ID)
			if len(audits) != 2 {
				t.Fatalf("model starts=%d want=2", len(audits))
			}
			assertLongTermMemoryDeliveryAudit(t, audits[0], []contextmgr.Memory{original})
			assertLongTermMemoryDeliveryAudit(t, audits[1], expected)
		})
	}
}

func createDeliveryTestMemory(t *testing.T, service *application.ContextMemoryService,
	scope contextmgr.MemoryScope, scopeID, title, content string,
) contextmgr.Memory {
	t.Helper()
	memory, err := service.Create(t.Context(), contextmgr.CreateMemoryRequest{
		Scope: scope, ScopeID: scopeID, Title: title, Content: content, RequestedBy: "desktop_operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	return memory
}

func assertLongTermMemoryDeliveryRequest(t *testing.T, request llm.ChatRequest, expected []contextmgr.Memory) {
	t.Helper()
	remaining := make(map[string]contextmgr.Memory, len(expected))
	for _, memory := range expected {
		remaining[memory.ID] = memory
	}
	for _, message := range request.Messages {
		var envelope struct {
			Version string `json:"version"`
			Source  struct {
				ID            string `json:"id"`
				MemoryVersion int64  `json:"memory_version"`
				ContentSHA256 string `json:"content_sha256"`
			} `json:"source"`
			Content string `json:"content"`
		}
		if json.Unmarshal([]byte(message.Content), &envelope) != nil || envelope.Version != "long_term_memory.v1" {
			continue
		}
		memory, found := remaining[envelope.Source.ID]
		if !found || message.Role != "user" || envelope.Content != memory.Content ||
			envelope.Source.MemoryVersion != memory.Version || envelope.Source.ContentSHA256 != memory.ContentSHA256 {
			t.Fatalf("actual model request contains unexpected or stale memory %q version %d", envelope.Source.ID, envelope.Source.MemoryVersion)
		}
		delete(remaining, memory.ID)
	}
	if len(remaining) != 0 {
		t.Fatalf("actual model request omitted current memories: %v", remaining)
	}
}

func longTermMemoryDeliveryAudits(t *testing.T, st *store.SQLiteStore, runID string) []llm.ModelContextAudit {
	t.Helper()
	eventList, err := st.ListRunEvents(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	var audits []llm.ModelContextAudit
	for _, event := range eventList {
		if event.Type != events.ModelStartedEvent || event.Source != "model_gateway" {
			continue
		}
		var payload struct {
			Context *llm.ModelContextAudit `json:"context"`
		}
		if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil || payload.Context == nil {
			t.Fatalf("actual model-start context audit missing: %v", err)
		}
		audits = append(audits, *payload.Context)
	}
	return audits
}

func assertLongTermMemoryDeliveryAudit(t *testing.T, audit llm.ModelContextAudit, expected []contextmgr.Memory) {
	t.Helper()
	if err := audit.Validate(); err != nil {
		t.Fatalf("invalid actual model context audit: %v", err)
	}
	remaining := make(map[string]contextmgr.Memory, len(expected))
	for _, memory := range expected {
		remaining[memory.ID] = memory
	}
	for _, source := range audit.Included {
		if source.Kind != "long_term_memory" {
			continue
		}
		memory, found := remaining[source.SourceID]
		if !found || source.MemoryVersion != memory.Version || source.ContentSHA256 != memory.ContentSHA256 || source.Tokens <= 0 {
			t.Fatalf("actual model context audit contains unexpected or stale memory: %+v", source)
		}
		delete(remaining, source.SourceID)
	}
	if len(remaining) != 0 {
		t.Fatalf("actual model context audit omitted delivered memories: %v", remaining)
	}
}
