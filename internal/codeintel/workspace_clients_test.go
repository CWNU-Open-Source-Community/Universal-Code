package codeintel

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
)

func TestManagerKeepsIndependentWorkspaceRootGenerations(t *testing.T) {
	roots := []string{testWorkspace(t), testWorkspace(t), testWorkspace(t)}
	manager := testManager(t, roots[0], "normal", 2*time.Second)
	snapshots := make([]CapabilitySnapshot, len(roots))
	for index, root := range roots {
		snapshots[index] = manager.Capabilities(t.Context(), helperWorkspaceID, root)[0]
		if snapshots[index].Health != HealthHealthy {
			t.Fatalf("root %d did not initialize: %+v", index, snapshots[index])
		}
	}
	var queries sync.WaitGroup
	for index, root := range roots {
		queries.Go(func() {
			result, err := manager.Execute(t.Context(), semanticRequest(root, snapshots[index], ToolDocumentSymbols, "main.go", "", ""))
			if err != nil || len(result.Items) == 0 {
				t.Errorf("root %d lost its live generation after another root initialized: %+v %v", index, result, err)
				return
			}
			if validation := manager.ValidateEvidence(t.Context(), root, result.Provenance); validation.State != EvidenceCurrent {
				t.Errorf("root %d evidence used a sibling runtime: %+v", index, validation)
			}
			ready := manager.InitializedRootCapabilities(helperWorkspaceID, result.Provenance.RootFingerprint)
			if len(ready) != 1 || ready[0].Generation != snapshots[index].Generation {
				t.Errorf("root %d automatic diagnostics selected a sibling: %+v", index, ready)
			}
		})
	}
	queries.Wait()
	if t.Failed() {
		return
	}
	for index, root := range roots {
		foreign := snapshots[(index+1)%len(snapshots)]
		if _, err := manager.Execute(t.Context(), semanticRequest(root, foreign, ToolDocumentSymbols, "main.go", "", "")); apperror.CodeOf(err) != apperror.CodeConflict {
			t.Fatalf("root %d accepted a sibling generation: %v", index, err)
		}
		if current := manager.Capabilities(t.Context(), helperWorkspaceID, root)[0]; current.Generation != snapshots[index].Generation {
			t.Fatalf("root %d restarted after a sibling query: %+v", index, current)
		}
	}
}

func TestManagerCloseWorkspaceRootPreservesSiblingAndReapsRetiredProcess(t *testing.T) {
	roots := []string{testWorkspace(t), testWorkspace(t)}
	manager := testManager(t, roots[0], "normal", 2*time.Second)
	clients := make([]*client, len(roots))
	for index, root := range roots {
		var err error
		clients[index], err = manager.ensureClient(t.Context(), serverKey(helperWorkspaceID, "test-lsp"), root)
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := manager.CloseWorkspaceRoot(ctx, helperWorkspaceID, clients[0].workspace.RootFingerprint); err != nil {
		t.Fatal(err)
	}
	select {
	case <-clients[0].transport.process.done:
	default:
		t.Fatal("retired root's owned language server was not reaped")
	}
	if _, err := manager.RefreshInitializedServer(t.Context(), helperWorkspaceID, roots[0], "test-lsp", clients[0].snapshot.Generation); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("closed root accepted its previous generation: %v", err)
	}
	if _, err := manager.Execute(t.Context(), semanticRequest(roots[1], clients[1].snapshot, ToolDocumentSymbols, "main.go", "", "")); err != nil {
		t.Fatalf("root cleanup retired its sibling: %v", err)
	}
	if err := manager.CloseWorkspaceRoot(ctx, helperWorkspaceID, clients[0].workspace.RootFingerprint); err != nil {
		t.Fatalf("repeated root cleanup failed: %v", err)
	}
}

func TestReplaceConfigurationReapsEveryPhysicalRoot(t *testing.T) {
	roots := []string{testWorkspace(t), testWorkspace(t)}
	manager := testManager(t, roots[0], "normal", 2*time.Second)
	clients := make([]*client, len(roots))
	for index, root := range roots {
		var err error
		clients[index], err = manager.ensureClient(t.Context(), serverKey(helperWorkspaceID, "test-lsp"), root)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.ReplaceConfiguration(t.Context(), manager.Descriptors(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for index, current := range clients {
		select {
		case <-current.transport.process.done:
		default:
			t.Fatalf("configuration replacement left root %d running", index)
		}
	}
	if len(manager.clients) != 0 || len(manager.Inventory()) != 1 || manager.Inventory()[0].Health != HealthConfigured {
		t.Fatalf("configuration replacement retained root clients: %+v", manager.Inventory())
	}
}

func TestManagerCloseWorkspaceRootRetriesUnconfirmedExit(t *testing.T) {
	root, siblingRoot := testWorkspace(t), testWorkspace(t)
	manager := testManager(t, root, "normal", 2*time.Second)
	sibling := manager.Capabilities(t.Context(), helperWorkspaceID, siblingRoot)[0]
	workspace, err := captureWorkspaceBinding(t.Context(), root, helperWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	// A failed transport can finish reporting errors before its owned process
	// is reaped. Keep that interval deterministic without leaving a real orphan.
	process := &ownedCommand{done: make(chan struct{})}
	stopped := &transport{closed: true, done: make(chan struct{}), process: process}
	stopped.fail.Do(func() { close(stopped.done) })
	key := workspaceClientKey(serverKey(helperWorkspaceID, "test-lsp"), workspace.RootFingerprint)
	current := &client{key: key, descriptor: manager.Descriptors()[0], workspace: workspace,
		transport: stopped, snapshot: sibling}
	current.snapshot.Generation = digestStrings("retiring-root-generation")
	manager.mu.Lock()
	manager.clients[key] = current
	manager.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-process.done:
		default:
			close(process.done)
		}
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for attempt := range 2 {
		if err := manager.CloseWorkspaceRoot(ctx, helperWorkspaceID, workspace.RootFingerprint); !errors.Is(err, context.Canceled) {
			t.Fatalf("cleanup attempt %d lost the unconfirmed process: %v", attempt, err)
		}
	}
	if _, err := manager.InitializeServer(t.Context(), helperWorkspaceID, root, "test-lsp"); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
		t.Fatalf("root was reused before its process exited: %v", err)
	}
	if ready := manager.InitializedRootCapabilities(helperWorkspaceID, workspace.RootFingerprint); len(ready) != 0 {
		t.Fatalf("retiring root still advertised semantic capabilities: %+v", ready)
	}
	if offered := manager.Capabilities(t.Context(), helperWorkspaceID, root); len(offered) != 1 || offered[0].Health == HealthHealthy {
		t.Fatalf("retiring root advertised its sibling's capabilities: %+v", offered)
	}
	if _, err := manager.Execute(t.Context(), semanticRequest(siblingRoot, sibling, ToolDocumentSymbols, "main.go", "", "")); err != nil {
		t.Fatalf("incomplete root cleanup stopped its sibling: %v", err)
	}
	close(process.done)
	if err := manager.CloseWorkspaceRoot(t.Context(), helperWorkspaceID, workspace.RootFingerprint); err != nil {
		t.Fatalf("cleanup replay did not confirm the exited process: %v", err)
	}
	if _, err := manager.InitializeServer(t.Context(), helperWorkspaceID, root, "test-lsp"); err != nil {
		t.Fatalf("confirmed cleanup did not release the root: %v", err)
	}
}
