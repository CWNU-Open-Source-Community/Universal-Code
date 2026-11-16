package application_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

func TestAgentRunnerRetiredWebAuthorityRecovery(t *testing.T) {
	for _, status := range []string{"pending", "unknown", "completed"} {
		t.Run(status, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "historical-web.db")
			state, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			runs := application.NewRunService(state)
			_, run, err := runs.Create(t.Context(), application.CreateRunRequest{
				Goal: "recover stored public search", Profile: "review", Surface: "code", Phase: "deliver",
				ModelRoute: "tool-loop/model", NetworkMode: "disabled",
				Budget: domain.Budget{MaxTurns: 3, MaxToolCalls: 4},
			})
			if err != nil {
				t.Fatal(err)
			}
			setWebTestPermission(t, state, run.ID, domain.RunExecutionPermissionFull)
			if _, err := runs.Start(t.Context(), run.ID); err != nil {
				t.Fatal(err)
			}
			historical := &legacyWebHistoryStore{SQLiteStore: state, t: t, dbPath: dbPath, status: status}
			search := &applicationWebSearchProvider{}
			model := &scriptedToolProvider{responses: []*llm.ChatResponse{
				toolResponse("original-search", string(toolgateway.WebSearchTool), `{"version":"web_search.v1","query":"historical documentation","limit":1}`),
				toolResponse("repeat-search", string(toolgateway.WebSearchTool), `{"version":"web_search.v1","query":"historical documentation","limit":1}`),
				textResponse(rootActionResponse(domain.RootActionContinue, "stored search replayed", "", "")),
			}}
			supervisor := newToolLoopSupervisor(historical, model).
				WithExecutionPermissionCapabilities(webSupervisorTestCapabilities(t, state, run.ID)).
				WithWebEvidence(webevidence.NewService(state, search, &applicationWebFetchBackend{})).
				WithWebFetchAuthorizationScheduler(true)
			result, err := supervisor.Step(t.Context(), run.ID)
			if !historical.rewritten {
				t.Fatal("historical fixture was not installed")
			}
			if status != "completed" {
				if apperror.CodeOf(err) != apperror.CodeFailedPrecondition || search.calls != 0 {
					t.Fatalf("retired %s was reissued: result=%#v calls=%d err=%v", status, result, search.calls, err)
				}
			} else if err != nil || result.ToolCalls != 2 || search.calls != 1 ||
				result.Text != "stored search replayed" {
				t.Fatalf("completed history was not replayed: result=%#v calls=%d err=%v", result, search.calls, err)
			}
			rounds, readErr := state.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 4)
			var original domain.SupervisorToolCall
			for _, round := range rounds {
				for _, call := range round.Calls {
					if call.CallID == historical.callID {
						original = call
					}
				}
			}
			if readErr != nil || original.CallID == "" || original.AuthorityJSON != historical.authority {
				t.Fatalf("historical authority was changed or unreadable: %#v err=%v", rounds, readErr)
			}
			if status == "completed" && (original.Status != domain.SupervisorToolCompleted ||
				original.ResultJSON != historical.result) {
				t.Fatal("completed historical result was rewritten")
			}
		})
	}
}

func webSupervisorTestCapabilities(t *testing.T, state *store.SQLiteStore, runID string) domain.ExecutionPermissionRuntimeCapabilities {
	t.Helper()
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority(),
	}
	t.Cleanup(func() { capabilities.RuntimeAuthority.RevokeRun(runID) })
	permission, err := state.GetRunExecutionPermission(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	// Root model turns require a current Full activation; Web evidence still
	// derives its own network authority rather than inheriting this host grant.
	if permission.Mode == domain.RunExecutionPermissionFull {
		if _, err := capabilities.RuntimeAuthority.ActivateRunFullAccess(permission); err != nil {
			t.Fatal(err)
		}
	}
	return capabilities
}

// Rewrite only this test's SQLite record after a current writer creates it.
// This models an existing database without retaining a legacy production writer.
type legacyWebHistoryStore struct {
	*store.SQLiteStore
	t                                         *testing.T
	dbPath, status, callID, authority, result string
	rewritten                                 bool
}

func (s *legacyWebHistoryStore) RecordSupervisorModelCompletedForAgent(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint, attempt llm.ModelAttempt, response llm.ChatResponse,
	attribution domain.AgentAttribution,
) (domain.SupervisorCheckpoint, error) {
	next, err := s.SQLiteStore.RecordSupervisorModelCompletedForAgent(ctx, checkpoint, attempt, response, attribution)
	if err != nil || s.rewritten || s.status == "completed" {
		return next, err
	}
	rounds, err := s.SQLiteStore.ListSupervisorToolRounds(ctx, next)
	if err != nil {
		return next, err
	}
	call := rounds[0].Calls[0]
	s.rewrite(ctx, call)
	if s.status == "unknown" {
		if started, err := s.SQLiteStore.RecordSupervisorToolExecutionStarted(ctx, next, call.CallID); err != nil || !started {
			s.t.Fatalf("record interrupted dispatch: started=%t err=%v", started, err)
		}
	}
	return next, nil
}

func (s *legacyWebHistoryStore) RecordSupervisorToolResult(ctx context.Context,
	checkpoint domain.SupervisorCheckpoint, result domain.SupervisorToolResult,
) (domain.SupervisorToolCall, bool, error) {
	call, fresh, err := s.SQLiteStore.RecordSupervisorToolResult(ctx, checkpoint, result)
	if err == nil && !s.rewritten && s.status == "completed" {
		s.rewrite(ctx, call)
	}
	return call, fresh, err
}

func (s *legacyWebHistoryStore) rewrite(ctx context.Context, call domain.SupervisorToolCall) {
	s.t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(call.AuthorityJSON), &doc); err != nil {
		s.t.Fatal(err)
	}
	permission, err := s.GetRunExecutionPermission(ctx, call.RunID)
	if err != nil {
		s.t.Fatal(err)
	}
	// Exact B38 Full projection and generation format, used only as historical data.
	doc["network_mode"] = "allowlist"
	doc["allowed_targets"] = []string{webevidence.PublicHTTPSTarget}
	doc["permission_snapshot_id"] = permission.ID
	doc["permission_generation"] = 9
	doc["permission_runtime_epoch"] = "old-web-runtime"
	doc["provider_search_independent"] = false
	str := func(key string) string {
		if value, found := doc[key]; found {
			return fmt.Sprint(value)
		}
		return ""
	}
	parts := []string{str("protocol_version"), str("run_id"), str("mission_id"),
		str("session_id"), str("root_agent_id"), str("workspace_id"), str("surface"),
		str("phase"), str("role"), str("profile"), str("permission_mode"),
		str("mode_revision"), str("network_mode"), str("permission_revision"),
		str("provider_available"), str("provider_fingerprint"), "true", "",
		"inline_web_fetch_approval_available=true",
		"permission_snapshot_id=" + permission.ID, "permission_generation=9",
		"permission_runtime_epoch=old-web-runtime", webevidence.PublicHTTPSTarget,
	}
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hash, "%d:%s|", len(part), part)
	}
	doc["generation"] = fmt.Sprintf("%x", hash.Sum(nil))
	raw, err := json.Marshal(doc)
	if err != nil {
		s.t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", s.dbPath)
	if err != nil {
		s.t.Fatal(err)
	}
	defer db.Close()
	updated, err := db.ExecContext(ctx, "UPDATE run_supervisor_tool_calls SET authority_json = ? WHERE run_id = ? AND call_id = ?", string(raw), call.RunID, call.CallID)
	if err != nil {
		s.t.Fatal(err)
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		s.t.Fatalf("updated=%d err=%v", count, err)
	}
	s.callID, s.authority, s.result, s.rewritten = call.CallID, string(raw), call.ResultJSON, true
}

func TestAgentRunnerWebSearchAcrossModesWithoutShellNetwork(t *testing.T) {
	for _, hosted := range []bool{false, true} {
		route := "configured"
		if hosted {
			route = "hosted"
		}
		for _, mode := range []domain.RunExecutionPermissionMode{
			domain.RunExecutionPermissionAsk, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionFull,
		} {
			t.Run(route+"/"+string(mode), func(t *testing.T) {
				state, err := store.Open(filepath.Join(t.TempDir(), "independent-web.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer state.Close()
				runs := application.NewRunService(state)
				_, run, err := runs.Create(t.Context(), application.CreateRunRequest{
					Goal: "search configured public sources", Profile: "review", Surface: "code", Phase: "deliver",
					ModelRoute: "tool-loop/model", NetworkMode: "disabled",
					Budget: domain.Budget{MaxTurns: 8, MaxToolCalls: 12},
				})
				if err != nil {
					t.Fatal(err)
				}
				setWebTestPermission(t, state, run.ID, mode)
				if _, err := runs.Start(t.Context(), run.ID); err != nil {
					t.Fatal(err)
				}
				search := &applicationWebSearchProvider{}
				connector := &webModeTestConnector{}
				web := webevidence.NewService(state, search, &applicationWebFetchBackend{}).WithSourceConnectors(connector)
				if hosted {
					web.WithSearchProviderResolver(webModeHostedResolver{provider: search})
				}
				model := &scriptedToolProvider{}
				capabilities := webSupervisorTestCapabilities(t, state, run.ID)
				supervisor := newToolLoopSupervisor(state, model).WithExecutionPermissionCapabilities(capabilities).
					WithWebEvidence(web).WithWebFetchAuthorizationScheduler(true)
				modes := []domain.RunExecutionPermissionMode{mode}
				if mode == domain.RunExecutionPermissionFull {
					// Revoke the Supervisor's native grant when downgrading;
					// fresh ordinary Web calls must still work in Auto and Ask.
					modes = append(modes, domain.RunExecutionPermissionAuto, domain.RunExecutionPermissionAsk)
				}
				for index, current := range modes {
					if current != domain.RunExecutionPermissionFull {
						capabilities.RuntimeAuthority.RevokeRun(run.ID)
					}
					setWebTestPermission(t, state, run.ID, current)
					searchPayload, _ := json.Marshal(toolgateway.WebSearchPayload{
						Version: "web_search.v1", Query: "public documentation " + string(current), Limit: 1,
					})
					connectorPayload, _ := json.Marshal(toolgateway.SourceSearchPayload{
						Version: "source_search.v1", Connectors: []string{"github"},
						Query: "public documentation " + string(current), Limit: 1,
					})
					model.responses = append(model.responses,
						toolResponse("search-"+string(current), string(toolgateway.WebSearchTool), string(searchPayload)),
						toolResponse("connector-"+string(current), string(toolgateway.SourceSearchTool), string(connectorPayload)),
						textResponse(rootActionResponse(domain.RootActionContinue, "public sources searched", "", "")),
					)
					result, err := supervisor.Step(t.Context(), run.ID)
					if err != nil || result.ToolCalls != 2 || result.Text != "public sources searched" ||
						search.calls != index+1 || connector.calls != index+1 {
						t.Fatalf("%s search lifecycle=%#v search=%d connectors=%d err=%v", current, result, search.calls, connector.calls, err)
					}
					requests := model.Requests()
					request := requests[index*3]
					for _, name := range []toolgateway.ToolName{toolgateway.WebSearchTool, toolgateway.SourceSearchTool, toolgateway.WebFetchTool} {
						if !hasToolSpec(request, string(name)) {
							t.Fatalf("%s did not advertise %s", current, name)
						}
					}
					persisted, err := state.GetRunMode(t.Context(), run.ID)
					if err != nil || persisted.Scope.NetworkMode != "disabled" || len(persisted.Scope.AllowedTargets) != 0 {
						t.Fatalf("web search changed shell authority: %#v err=%v", persisted, err)
					}
				}
				rounds, err := state.ListRunSupervisorToolRoundsPage(t.Context(), run.ID, 0, 20)
				if err != nil || len(rounds) != len(modes)*2 {
					t.Fatalf("rounds=%#v err=%v", rounds, err)
				}
				for _, round := range rounds {
					for _, call := range round.Calls {
						if call.Status != domain.SupervisorToolCompleted {
							t.Fatalf("call=%#v", call)
						}
						for _, retired := range []string{"permission_snapshot_id", "permission_generation", "permission_runtime_epoch", "provider_search_independent"} {
							if strings.Contains(call.AuthorityJSON, retired) {
								t.Fatalf("retired field %s persisted", retired)
							}
						}
					}
				}
			})
		}
	}
}

type webModeHostedResolver struct{ provider webevidence.SearchProvider }

func (r webModeHostedResolver) ResolveSearch(context.Context, webevidence.SearchRoute, webevidence.NetworkAuthority) (webevidence.SearchSelection, error) {
	return webevidence.SearchSelection{Policy: webevidence.SearchPolicyProviderNative,
		Backend: r.provider.Name(), SelectionReason: "configured_hosted_search", Provider: r.provider,
		ProviderAuthorityIndependent: true,
		ProviderAuthority:            webevidence.NetworkAuthority{Mode: "allowlist", AllowedTargets: []string{"docs.example.com"}},
	}, nil
}

type webModeTestConnector struct{ calls int }

func (*webModeTestConnector) Name() string           { return "github" }
func (*webModeTestConnector) Version() string        { return "web-mode-test.v1" }
func (*webModeTestConnector) SearchEndpoint() string { return "https://docs.example.com/connector" }
func (*webModeTestConnector) MatchURL(string) bool   { return false }
func (c *webModeTestConnector) Search(_ context.Context, _ string, _ int, authority webevidence.NetworkAuthority) ([]webevidence.ConnectorSearchItem, error) {
	if _, err := authority.Authorize(c.SearchEndpoint()); err != nil {
		return nil, err
	}
	c.calls++
	return []webevidence.ConnectorSearchItem{{URL: "https://docs.example.com/source", Title: "Public source"}}, nil
}
func (*webModeTestConnector) Read(context.Context, string, int, webevidence.NetworkAuthority) (webevidence.ConnectorDocument, error) {
	panic("search must not fetch arbitrary result pages")
}
