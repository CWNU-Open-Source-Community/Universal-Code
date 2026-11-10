package desktop

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/browserruntime"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/httpapi"
	"cyberagent-workbench/internal/store"
	"cyberagent-workbench/internal/toolgateway"
)

func TestControlPlaneWithoutRunExecutionInvalidatesOldBrowserRequests(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "old-browser-queue.db")
	state, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(state).Create(ctx, application.CreateRunRequest{Goal: "restore pending page request", Profile: "review", Surface: "code", Phase: "deliver", Interactive: true, BrowserOrigins: []string{"http://127.0.0.1:4321"}})
	if err != nil {
		state.Close()
		t.Fatal(err)
	}
	scope, err := browserruntime.NewAgentBrowserOriginScope([]string{"http://127.0.0.1:4321"})
	if err != nil {
		state.Close()
		t.Fatal(err)
	}
	authority := toolgateway.AgentBrowserCallAuthority{ProtocolVersion: toolgateway.AgentBrowserAuthorityVersion, RunID: run.ID, MissionID: run.MissionID, SessionID: run.SessionID, RootAgentID: "root", Surface: domain.ExecutionSurfaceCode, Phase: domain.ExecutionPhaseDeliver, Role: domain.AgentRoleRoot, Profile: domain.ProfileCode, PermissionMode: domain.RunExecutionPermissionAsk, ModeRevision: 1, PermissionSnapshotID: "snapshot", PermissionRevision: 1, RunAuthorizationFence: 1, ManagerBootID: "old-boot", BrowserSessionID: "old-browser", SessionGeneration: 1, DeclaredOrigins: []string{"http://127.0.0.1:4321"}, ScopeFingerprint: scope.Fingerprint}
	authority.BrowserNetworkAccess = run.Config.BrowserNetworkAccess
	authority.BrowserNetworkRevision = run.Config.BrowserNetworkRevision
	authority.Generation = authority.Fingerprint()
	raw, _ := json.Marshal(authority)
	source := domain.BrowserNetworkRequest{ID: domain.BrowserNetworkRequestPrefix + "old-process", RunID: run.ID, SessionID: run.SessionID, ManagerBootID: authority.ManagerBootID, RuntimeID: "old-runtime", Fingerprint: strings.Repeat("a", 64), AuthorityJSON: string(raw), Method: "POST", DisplayURL: "http://127.0.0.1:4321/write", BodySHA256: strings.Repeat("b", 64), ExpiresAt: time.Now().Add(2 * time.Minute)}
	if err = state.CreateBrowserNetworkRequest(ctx, source); err != nil {
		state.Close()
		t.Fatal(err)
	}
	if err = state.Close(); err != nil {
		t.Fatal(err)
	}
	plane, err := OpenControlPlane(ControlPlaneConfig{DatabasePath: path, ReadToken: desktopControlPlaneTestToken, RunExecutionEnabled: false, AppVersion: "browser-queue-recovery-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	if plane.agentBrowser != nil {
		t.Fatal("execution-disabled startup created browser service")
	}
	current, err := plane.stateStore.GetBrowserNetworkRequest(ctx, source.ID)
	if err != nil || current.Status != "invalidated" {
		t.Fatalf("old process source=%+v err=%v", current, err)
	}
	record, err := plane.stateStore.GetApprovalByProposal(ctx, source.ID)
	if err != nil || record.Status != approval.StatusDenied {
		t.Fatalf("old process approval=%+v err=%v", record, err)
	}
	response := desktopAPIRequest(plane.Handler(), "/api/v1/runs/"+run.ID+"/approvals")
	if response.Code != http.StatusOK {
		t.Fatalf("queue response %d: %s", response.Code, response.Body.String())
	}
	var envelope desktopAPIEnvelope
	var queue httpapi.ApprovalQueueView
	if err = json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(envelope.Data, &queue); err != nil {
		t.Fatal(err)
	}
	if len(queue.Items) != 0 {
		t.Fatalf("old browser request stayed in queue: %+v", queue.Items)
	}
}
