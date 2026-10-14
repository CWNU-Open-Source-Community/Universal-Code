package application_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
	"cyberagent-workbench/internal/webevidence"
)

func TestWebAccessExecutorModesSearchFetchAndSavedRead(t *testing.T) {
	for _, selection := range []string{"disabled", "rules", "full", ""} {
		t.Run(selection, func(t *testing.T) {
			ctx := context.Background()
			state, err := store.Open(filepath.Join(t.TempDir(), "web-access-executor.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			runs := application.NewRunService(state)
			targets := []string(nil)
			if selection == "rules" {
				targets = []string{"docs.example.com"}
			}
			mission, created, err := runs.Create(ctx, application.CreateRunRequest{Goal: "research a public source", Profile: "review", WebAccessMode: selection, WebAllowedTargets: targets, Budget: domain.Budget{MaxTurns: 4, MaxToolCalls: 8}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := runs.Start(ctx, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			mode, _ := state.GetRunMode(ctx, run.ID)
			permission, _ := state.GetRunExecutionPermission(ctx, run.ID)
			root, found, err := state.GetRootAgent(ctx, run.ID)
			if err != nil || !found {
				t.Fatal(err)
			}
			lease, err := state.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{RunID: run.ID, OwnerID: "web-access-executor-test", TTL: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			backend := &applicationWebFetchBackend{}
			provider := &applicationWebSearchProvider{}
			connector := &webModeTestConnector{}
			service := webevidence.NewService(state, provider, backend).WithSourceConnectors(connector)
			executor, err := application.NewWebEvidenceToolExecutor(state, service)
			if err != nil {
				t.Fatal(err)
			}
			searchAuthority := webevidence.NetworkAuthority{Mode: "allowlist", AllowedTargets: []string{webevidence.PublicHTTPSTarget}}
			fingerprint := service.SearchProviderFingerprintForScope(ctx, webevidence.ExecutionScope{RunID: run.ID, MissionID: mission.ID, WorkspaceID: mission.WorkspaceID, ModelRoute: run.Config.ModelRoute, Authority: searchAuthority})
			connectorFingerprint := service.SourceConnectorFingerprintFor(searchAuthority)
			if selection == "disabled" {
				connectorFingerprint = ""
				fingerprint = ""
			}
			networkMode := "disabled"
			authorityTargets := []string(nil)
			if selection == "full" {
				networkMode = "allowlist"
				authorityTargets = []string{webevidence.PublicHTTPSTarget}
			} else if selection == "rules" {
				networkMode = "allowlist"
				authorityTargets = targets
			}
			capability := toolgateway.WebEvidenceCapabilityContext{RunID: run.ID, MissionID: mission.ID, SessionID: run.SessionID, RootAgentID: root.ID, WorkspaceID: mission.WorkspaceID, Surface: mode.Surface, Phase: mode.Phase, Role: root.Role, Profile: mode.Profile, PermissionMode: permission.Mode, PermissionRevision: permission.Revision, ModeRevision: mode.Revision, WebAccessMode: domain.WebAccessMode(selection), NetworkMode: networkMode, AllowedTargets: authorityTargets, ProviderAvailable: fingerprint != "", ProviderFingerprint: fingerprint, SourceConnectorAvailable: connectorFingerprint != "", SourceConnectorFingerprint: connectorFingerprint}
			scope := toolgateway.WebEvidenceExecutionScope{InvocationID: "web-access-invocation-1", OperationKey: "web-access-search-operation-1", RunID: run.ID, SupervisorTurn: 1, SupervisorToolCallID: "web-access-call-1", MissionID: mission.ID, SessionID: run.SessionID, RootAgentID: root.ID, WorkspaceID: mission.WorkspaceID, Surface: mode.Surface, Phase: mode.Phase, Role: root.Role, Profile: mode.Profile, PermissionMode: permission.Mode, PermissionRevision: permission.Revision, ModeRevision: mode.Revision, CapabilityGeneration: toolgateway.WebEvidenceCapabilitySnapshot(capability).Generation, ProviderFingerprint: fingerprint, ConnectorFingerprint: connectorFingerprint, LeaseID: lease.Lease.LeaseID, LeaseGeneration: lease.Lease.Generation, RequestedBy: "run_supervisor", PolicyDecision: toolgateway.Decision{Allowed: true, Approval: toolgateway.ApprovalAutomatic, Risk: "medium", Reason: "web access test"}}
			_, err = executor.ExecuteWebEvidence(ctx, scope, toolgateway.WebSearchTool, json.RawMessage(`{"version":"web_search.v1","query":"public docs","limit":1}`))
			if selection == "disabled" {
				if err == nil || provider.calls != 0 {
					t.Fatalf("disabled search err=%v calls=%d", err, provider.calls)
				}
				if _, err := executor.ExecuteWebEvidence(ctx, scope, toolgateway.SourceSearchTool, json.RawMessage(`{"version":"source_search.v1","connectors":["github"],"query":"docs","limit":1}`)); err == nil || connector.calls != 0 {
					t.Fatal("disabled source search admitted")
				}
			} else if err != nil || provider.calls != 1 {
				t.Fatalf("search err=%v calls=%d", err, provider.calls)
			}
			if selection != "disabled" {
				scope.OperationKey = "web-access-source-operation-1"
				if _, err := executor.ExecuteWebEvidence(ctx, scope, toolgateway.SourceSearchTool, json.RawMessage(`{"version":"source_search.v1","connectors":["github"],"query":"docs","limit":1}`)); err != nil || connector.calls != 1 {
					t.Fatalf("source search err=%v calls=%d", err, connector.calls)
				}
			}
			scope.OperationKey = "web-access-fetch-operation-1"
			fetched, err := executor.ExecuteWebEvidence(ctx, scope, toolgateway.WebFetchTool, json.RawMessage(`{"version":"web_fetch.v1","url":"https://docs.example.com/page"}`))
			if selection == "disabled" || selection == "" {
				if err == nil || backend.calls != 0 {
					t.Fatalf("nonpreauthorized fetch err=%v calls=%d", err, backend.calls)
				}
				return
			}
			if err != nil || backend.calls != 1 {
				t.Fatalf("authorized fetch=%+v calls=%d err=%v", fetched, backend.calls, err)
			}
			if selection == "full" && (len(backend.lastTargets) != 1 || backend.lastTargets[0] != webevidence.PublicHTTPSTarget) {
				t.Fatalf("full authority=%v", backend.lastTargets)
			}
			for _, url := range []string{"http://docs.example.com/page", "https://127.0.0.1/private", "https://metadata.google.internal/secret", "https://localhost/private"} {
				scope.OperationKey = "unsafe-web-fetch-operation"
				_, err := executor.ExecuteWebEvidence(ctx, scope, toolgateway.WebFetchTool, json.RawMessage(`{"version":"web_fetch.v1","url":"`+url+`"}`))
				if err == nil || backend.calls != 1 {
					t.Fatalf("unsafe URL %s err=%v calls=%d", url, err, backend.calls)
				}
			}
			if selection == "rules" {
				scope.OperationKey = "web-access-outside-operation"
				if _, err := executor.ExecuteWebEvidence(ctx, scope, toolgateway.WebFetchTool, json.RawMessage(`{"version":"web_fetch.v1","url":"https://other.example.com/page"}`)); err == nil || backend.calls != 1 {
					t.Fatalf("out-of-scope rules fetch err=%v calls=%d", err, backend.calls)
				}
			}
			var output struct {
				Source struct {
					ID string `json:"id"`
				}
				Snapshot struct {
					SourceID string `json:"source_id"`
					ID       string `json:"snapshot_id"`
				}
			}
			if err := json.Unmarshal([]byte(fetched.Content), &output); err != nil {
				t.Fatal(err)
			}
			if _, _, err := state.ReleaseRunExecutionLease(ctx, lease.Lease); err != nil {
				t.Fatal(err)
			}
			if _, err := runs.Pause(ctx, run.ID); err != nil {
				t.Fatal(err)
			}
			updated, err := application.NewRunWebAccessService(state).Change(ctx, application.ChangeRunWebAccessRequest{Version: application.RunWebAccessControlProtocolVersion, RunID: run.ID, ExpectedModeRevision: mode.Revision, WebAccessMode: "disabled", OperationKey: "web-access-revoke-operation"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := executor.ExecuteWebEvidence(ctx, scope, toolgateway.WebFetchTool, json.RawMessage(`{"version":"web_fetch.v1","url":"https://docs.example.com/after"}`)); apperror.CodeOf(err) != apperror.CodeFailedPrecondition || backend.calls != 1 {
				t.Fatalf("stale scope err=%v calls=%d", err, backend.calls)
			}
			capability.ModeRevision = updated.Mode.Revision
			capability.WebAccessMode = domain.WebAccessDisabled
			capability.NetworkMode = "disabled"
			capability.AllowedTargets = nil
			capability.ProviderAvailable = false
			capability.ProviderFingerprint = ""
			capability.SourceConnectorAvailable = false
			capability.SourceConnectorFingerprint = ""
			scope.ModeRevision = updated.Mode.Revision
			scope.ProviderFingerprint = ""
			scope.ConnectorFingerprint = ""
			scope.CapabilityGeneration = toolgateway.WebEvidenceCapabilitySnapshot(capability).Generation
			scope.OperationKey = "web-access-saved-read-operation"
			pagePayload := json.RawMessage(`{"version":"web_fetch.v1","source_id":"` + output.Snapshot.SourceID + `","snapshot_id":"` + output.Snapshot.ID + `","offset":0,"limit":64}`)
			if page, err := executor.ExecuteWebEvidence(ctx, scope, toolgateway.WebFetchTool, pagePayload); err != nil || page.Content == "" || backend.calls != 1 {
				t.Fatalf("disabled saved read=%+v err=%v calls=%d", page, err, backend.calls)
			}
			if _, err := executor.ExecuteWebEvidence(ctx, scope, toolgateway.WebFetchTool, json.RawMessage(`{"version":"web_fetch.v1","url":"https://docs.example.com/new"}`)); err == nil || backend.calls != 1 {
				t.Fatalf("disabled new fetch err=%v calls=%d", err, backend.calls)
			}
		})
	}
}
