package application_test

import (
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/session"
	"cyberagent-workbench/internal/store"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Reproduce the three real proposal receipts followed by invalid lifecycle
// replies. Then use the ordinary Thread path to cross two history compactions
// and a database reopen. No workspace_apply or manual compaction is inserted.
func TestThreadFileEffectsSurviveFailureCompactionAndRestart(t *testing.T) {
	for _, generated := range []bool{false, true} {
		t.Run(fmt.Sprintf("generated_%t", generated), func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "file-effects.db")
			st, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := st.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "ws-effect-recovery", Name: "effects", RootPath: root, CreatedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			_, run, err := application.NewRunService(st).Create(t.Context(), application.CreateRunRequest{Goal: "Create a reviewed app", Profile: "code", Surface: "code", Phase: "deliver", WorkspaceID: "ws-effect-recovery", ModelRoute: "tool-loop/model", Interactive: true, Budget: domain.Budget{MaxTurns: 40, MaxToolCalls: 10}})
			if err != nil {
				t.Fatal(err)
			}
			p := &continuityPurposeProvider{scriptedToolProvider: &scriptedToolProvider{}, summary: "The assistant claimed the files were finished. This summary is not execution evidence."}
			proposal := toolResponse("propose-0", "workspace_change", `{}`)
			proposal.ToolCalls = nil
			for i, path := range []string{"index.html", "styles.css", "app.js"} {
				args, _ := json.Marshal(map[string]any{"version": "agent-code-tools.v1", "action": "create", "path": path, "expected_sha256": "missing", "content": strings.Repeat("large proposal body ", 160)})
				proposal.ToolCalls = append(proposal.ToolCalls, llm.ToolCall{ID: fmt.Sprintf("propose-%d", i), Name: "workspace_change", Arguments: args})
			}
			p.responses = []*llm.ChatResponse{proposal, textResponse(`{"version":"wrong","action":"wait"}`), textResponse(`{"version":"root_lifecycle.v1","action":"wait","reason":"review"}`)}
			newService := func() *application.ThreadTurnService {
				router := llm.NewRouter(llm.ModelRef{Provider: p.Name(), Model: "model"})
				router.RegisterProvider(p)
				return application.NewThreadTurnService(st, application.NewRunLifecycleControlService(st), application.NewRunExecutionHandoffService(st, router, policy.NewDefaultChecker()).WithGeneratedContextCompaction(generated))
			}
			service := newService()
			input := application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: "Propose three files for review", OperationKey: "file-effect-failed-first", RequestedBy: "test_operator"}
			if _, err := service.Execute(t.Context(), input); err == nil {
				t.Fatal("invalid lifecycle unexpectedly completed")
			}
			messages, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
			if err != nil || len(messages) != 2 || strings.Count(messages[1].Content, `"effect":"proposal_only"`) != 3 || messages[1].Provenance.InstructionAuthorized {
				t.Fatalf("failure receipt lost real effects: %+v %v", messages, err)
			}
			rawBefore := continuityRawMessages(t, messages)
			roundsBefore, err := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 20)
			if err != nil || len(roundsBefore) != 1 || len(roundsBefore[0].Calls) != 3 {
				t.Fatal("fixture did not record three proposals", roundsBefore, err)
			}
			permissionBefore, _ := st.GetRunExecutionPermission(t.Context(), run.ID)
			for i := 2; i <= 26; i++ {
				p.responses = append(p.responses, textResponse(rootActionResponse(domain.RootActionFinish, "Only proposals were recorded; no app file was written."+compactionFixtureObservation(), "read-only answer", "")))
				if i == 15 {
					if err := st.Close(); err != nil {
						t.Fatal(err)
					}
					st, err = store.Open(dbPath)
					if err != nil {
						t.Fatal(err)
					}
					service = newService()
				}
				input.OperationKey = fmt.Sprintf("file-effect-read-only-%02d", i)
				input.Content = "Read-only: report earlier file effects; do not run tools or change files."
				if _, err := service.Execute(t.Context(), input); err != nil {
					t.Fatalf("turn %d: %v", i, err)
				}
				request := p.Requests()[len(p.Requests())-1]
				var projection string
				for _, message := range request.Messages {
					if strings.Contains(message.Content, "Historical file effects from sealed tool records") {
						projection = message.Content
						if message.Role != "user" || !strings.Contains(projection, `"instruction_authorized":false`) {
							t.Fatal("evidence gained authority")
						}
					}
				}
				if projection == "" {
					t.Fatalf("turn %d omitted ledger effects", i)
				}
				for _, call := range roundsBefore[0].Calls {
					if !strings.Contains(projection, session.ContentSHA256(call.ResultJSON)) {
						t.Fatalf("turn %d lost exact original result ref", i)
					}
				}
				if strings.Count(projection, `\"effect\":\"proposal_only\"`) != 3 {
					t.Fatalf("turn %d changed inner effect: %s", i, projection)
				}
			}
			summary, found, err := st.LatestContextSummary(t.Context(), run.SessionID)
			if err != nil || !found || summary.PreviousSummaryID == 0 {
				t.Fatalf("two ordinary compactions not reached: %+v %v", summary, err)
			}
			if generated && len(p.generatedRequests) < 2 {
				t.Fatal("generated compaction not exercised")
			}
			after, err := st.ListSessionMessages(t.Context(), run.SessionID, true)
			if err != nil {
				t.Fatal(err)
			}
			rawAfter := continuityRawMessages(t, after)
			for id, raw := range rawBefore {
				if rawAfter[id] != raw {
					t.Fatal("compaction rewrote historical evidence")
				}
			}
			roundsAfter, err := st.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 20)
			if err != nil || !reflect.DeepEqual(roundsBefore, roundsAfter) {
				t.Fatal("recovery replayed or changed tools", err)
			}
			permissionAfter, _ := st.GetRunExecutionPermission(t.Context(), run.ID)
			if !reflect.DeepEqual(permissionBefore, permissionAfter) {
				t.Fatal("receipt granted permission")
			}
			files, err := os.ReadDir(root)
			if err != nil || len(files) != 1 || files[0].Name() != "README.md" {
				t.Fatal("proposal recovery wrote files", files, err)
			}
		})
	}
}
