package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/codeintel"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/executionauth"
	"cyberagent-workbench/internal/llm"
	"cyberagent-workbench/internal/mcp"
	"cyberagent-workbench/internal/policy"
	"cyberagent-workbench/internal/store"
)

type dependencyDrainStore struct {
	*store.SQLiteStore
	armed                        atomic.Bool
	lookupEntered, lookupRelease chan struct{}
	intentEntered, intentRelease chan struct{}
	lookupOnce                   sync.Once
	rejected                     chan error
}

func (s *dependencyDrainStore) GetMCPClientServer(ctx context.Context, id string) (mcp.ServerRecord, error) {
	if s.armed.Load() {
		s.lookupOnce.Do(func() { close(s.lookupEntered); <-s.lookupRelease })
		// Model a storage read that returns after cancellation. The manager's
		// subsequent native context checks must still report cancellation.
		ctx = context.WithoutCancel(ctx)
	}
	return s.SQLiteStore.GetMCPClientServer(ctx, id)
}

func (s *dependencyDrainStore) ReserveThreadMessageIntent(ctx context.Context, request domain.ThreadMessageIntentRequest) (domain.ThreadMessageIntent, error) {
	intent, err := s.SQLiteStore.ReserveThreadMessageIntent(ctx, request)
	if err == nil && request.OperationKey == "dependency-drain-late-reject" {
		close(s.intentEntered)
		<-s.intentRelease
	}
	return intent, err
}

func (s *dependencyDrainStore) RejectThreadMessageIntent(ctx context.Context, request domain.ThreadMessageIntentRequest) (bool, error) {
	rejected, err := s.SQLiteStore.RejectThreadMessageIntent(ctx, request)
	if err == nil && !rejected {
		err = errors.New("late input intent was not rejected")
	}
	s.rejected <- err
	return rejected, err
}

type dependencyDrainProvider struct {
	cliAgentBrowserProvider
	manager *mcp.Manager
	store   *dependencyDrainStore
	request mcp.InvokeRequest
	result  chan error
}

func (p *dependencyDrainProvider) Chat(ctx context.Context, _ llm.ChatRequest) (*llm.ChatResponse, error) {
	p.store.armed.Store(true)
	_, err := p.manager.Invoke(ctx, p.request)
	p.result <- err
	return nil, err
}

func (p *dependencyDrainProvider) StreamChat(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatChunk, error) {
	_, err := p.Chat(ctx, request)
	return nil, err
}

func TestAppCloseKeepsRuntimeDependenciesUntilTurnDrain(t *testing.T) {
	raw, err := store.Open(filepath.Join(t.TempDir(), "dependency-drain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	st := &dependencyDrainStore{SQLiteStore: raw, lookupEntered: make(chan struct{}), lookupRelease: make(chan struct{}),
		intentEntered: make(chan struct{}), intentRelease: make(chan struct{}), rejected: make(chan error, 1)}
	manager, err := mcp.NewClientManager(st, nil, mcp.ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	intel, err := codeintel.NewEmptyManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), codeintel.MaximumShutdownGracePeriod)
		defer cancel()
		_ = intel.Close(ctx)
	})
	provider := &dependencyDrainProvider{manager: manager, store: st, result: make(chan error, 1)}
	if err := raw.SaveWorkspace(t.Context(), store.WorkspaceRecord{ID: "dependency-workspace", Name: "Dependency workspace", RootPath: t.TempDir(), CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_, run, err := application.NewRunService(raw).Create(t.Context(), application.CreateRunRequest{Goal: "Drain borrowed runtime dependencies", Profile: "review", WorkspaceID: "dependency-workspace", Interactive: true, ModelRoute: provider.Name() + "/model", Budget: domain.Budget{MaxTurns: 4}})
	if err != nil {
		t.Fatal(err)
	}
	mission, err := raw.GetMission(t.Context(), run.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	// The validated catalog record is a fixture saved through SQLite's public API.
	// Cancellation happens before dispatch; no remote MCP server is contacted.
	now := time.Now().UTC()
	catalog, err := mcp.NewCapabilitySnapshot("drain-fixture", "1", []mcp.CapabilityKind{mcp.CapabilityTools}, []mcp.RemoteTool{{Name: "ping", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := mcp.ServerDescriptor{ProtocolVersion: mcp.ClientProtocolVersion, ID: "drain-fixture", Name: "drain-fixture", Transport: mcp.TransportStreamableHTTP, Target: "https://fixture.invalid/mcp", WorkspaceID: mission.WorkspaceID, Scope: mcp.ScopeWorkspace, Source: mcp.Source{Kind: "manual", URI: "test://drain"}, DeclaredCapabilities: []mcp.CapabilityKind{mcp.CapabilityTools}, CallTimeoutMillis: 1000, MaxResultBytes: 8192}
	record, _, err := manager.Stage(t.Context(), descriptor)
	if err != nil {
		t.Fatal(err)
	}
	record.State, record.Health, record.Capabilities = mcp.TrustEnabled, mcp.HealthHealthy, catalog
	record.ApprovedCapabilityFingerprint = catalog.Fingerprint
	record.Generation++
	record.UpdatedAt = time.Now().UTC()
	record.ReviewedBy, record.ReviewedAt = "test_operator", &record.UpdatedAt
	if _, err := raw.UpdateMCPClientServer(t.Context(), record, record.Generation-1); err != nil {
		t.Fatal(err)
	}
	provider.request = mcp.InvokeRequest{RunID: run.ID, WorkspaceID: mission.WorkspaceID, ServerID: descriptor.ID, ToolName: "ping", CapabilityFingerprint: catalog.Fingerprint, Arguments: json.RawMessage(`{}`), OperationID: "draining-host-mcp", Subject: executionauth.SubjectRef{RunID: run.ID, ActorID: "root-fixture"}, Authorizer: executionauth.NewPolicyAuthorizer(nil)}
	router := llm.NewRouter(llm.ModelRef{Provider: provider.Name(), Model: "model"})
	router.RegisterProvider(provider)
	turns := application.NewThreadTurnService(st, application.NewRunLifecycleControlService(raw), application.NewRunExecutionHandoffService(raw, router, policy.NewDefaultChecker())).WithHostLifetime(t.Context())
	a := &App{store: raw, threadTurns: turns, mcpClient: manager, codeIntel: intel}
	input := application.ExecuteThreadTurnRequest{Version: domain.ThreadMessageProtocolVersion, ThreadID: domain.InitialThreadID(run.ID), Content: "Use the fixture runtime", OperationKey: "dependency-drain-first", RequestedBy: "test_operator"}
	turnDone, rejectDone := make(chan struct{}), make(chan struct{})
	var turnErr, rejectErr error
	var lookupRelease, intentRelease sync.Once
	t.Cleanup(func() {
		lookupRelease.Do(func() { close(st.lookupRelease) })
		intentRelease.Do(func() { close(st.intentRelease) })
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = turns.Shutdown(ctx)
	})
	go func() { defer close(turnDone); _, turnErr = turns.Execute(t.Context(), input) }()
	wait := func(signal <-chan struct{}, label string) {
		t.Helper()
		select {
		case <-signal:
		case <-time.After(8 * time.Second):
			t.Fatalf("did not observe %s", label)
		}
	}
	wait(st.lookupEntered, "runtime lookup during host turn")
	late := input
	late.OperationKey = "dependency-drain-late-reject"
	late.Files = []domain.WorkspaceFileReference{{SourceKind: "workspace_file", Path: "missing.txt", ExpectedSHA256: strings.Repeat("a", 64)}}
	go func() { defer close(rejectDone); _, rejectErr = turns.Execute(t.Context(), late) }()
	wait(st.intentEntered, "second input's committed intent")
	closed := make(chan struct{})
	started := time.Now()
	go func() { a.Close(); close(closed) }()
	wait(closed, "bounded App.Close")
	if elapsed := time.Since(started); elapsed > 7*time.Second {
		t.Errorf("App.Close exceeded its bounded turn wait: %s", elapsed)
	}
	if _, err := raw.GetRun(t.Context(), run.ID); err != nil {
		t.Errorf("App.Close closed SQLite before host operations drained: %v", err)
	}
	_, intelErr := intel.InitializeServer(t.Context(), mission.WorkspaceID, t.TempDir(), "not-configured")
	if apperror.CodeOf(intelErr) != apperror.CodeNotFound {
		t.Errorf("code intelligence closed while still borrowed by draining host operations: %v", intelErr)
	}
	lookupRelease.Do(func() { close(st.lookupRelease) })
	intentRelease.Do(func() { close(st.intentRelease) })
	wait(turnDone, "cancelled runtime turn completion")
	wait(rejectDone, "late intent rejection")
	if mcpErr := <-provider.result; !errors.Is(mcpErr, context.Canceled) {
		t.Errorf("shutdown replaced MCP cancellation with dependency failure: %v cause=%v (turn=%v)", mcpErr, errors.Unwrap(mcpErr), turnErr)
	} else {
		t.Logf("MCP retained cancellation through drain: %v", mcpErr)
	}
	if err := <-st.rejected; err != nil || rejectErr == nil {
		t.Errorf("late Reject lost its live SQLite store: reject=%v turn=%v", err, rejectErr)
	}
	// The owned cleanup must eventually close both dependency and store after
	// every turn's deferred rejection/receipt write has returned.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := raw.GetRun(t.Context(), run.ID); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("drained App did not close SQLite")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := intel.InitializeServer(t.Context(), mission.WorkspaceID, "unused", "not-configured"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("drained App did not close code intelligence: %v", err)
	}
}
