package codeintel

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReplaceConfigurationPreservesLiveRuntimeOnPublicationFailureAndReapsBeforeActivation(t *testing.T) {
	root := testWorkspace(t)
	manager := testManager(t, root, "normal", 2*time.Second)
	snapshot, err := manager.InitializeServer(context.Background(), helperWorkspaceID, root, "test-lsp")
	if err != nil {
		t.Fatal(err)
	}
	oldClient, err := manager.ensureClient(context.Background(), serverKey(helperWorkspaceID, snapshot.ServerID), root)
	if err != nil {
		t.Fatal(err)
	}
	key := oldClient.key
	descriptors := manager.Descriptors()
	descriptors[0].Name = "Revised fixture"
	publicationFailure := errors.New("fixture publication failure")
	if err := manager.ReplaceConfiguration(context.Background(), descriptors, func() error { return publicationFailure }); !errors.Is(err, publicationFailure) {
		t.Fatalf("publication error=%v", err)
	}
	if manager.clients[key] != oldClient || manager.Inventory()[0].Generation != snapshot.Generation {
		t.Fatal("failed publication changed live runtime")
	}
	if _, err := manager.Execute(context.Background(), semanticRequest(root, snapshot, ToolDocumentSymbols, "main.go", "", "")); err != nil {
		t.Fatalf("old runtime stopped after failed publication: %v", err)
	}
	if err := manager.ReplaceConfiguration(context.Background(), descriptors, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldClient.transport.process.done:
	default:
		t.Fatal("old owned process was not reaped")
	}
	if len(manager.clients) != 0 || manager.Inventory()[0].Health != HealthConfigured || manager.Inventory()[0].Generation != "" {
		t.Fatal("configuration replacement auto-started a process")
	}
	restarted, err := manager.InitializeServer(context.Background(), helperWorkspaceID, root, snapshot.ServerID)
	if err != nil || restarted.Generation == snapshot.Generation {
		t.Fatalf("replacement did not create fresh runtime: %#v %v", restarted, err)
	}
}

func TestEmptyManagerConfigurationDoesNotStartAndConcurrentReplacementRejectsOldQuery(t *testing.T) {
	root := testWorkspace(t)
	manager, err := NewEmptyManager()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestManager(manager) })
	if len(manager.Inventory()) != 0 || len(manager.Capabilities(context.Background(), helperWorkspaceID, root)) != 0 {
		t.Fatal("empty manager invented capabilities")
	}
	descriptor := testServerDescriptor(t, root, "hang", 2*time.Second)
	if err := manager.ReplaceConfiguration(context.Background(), []ServerDescriptor{descriptor}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.InitializeServer(context.Background(), helperWorkspaceID, root, descriptor.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := manager.Execute(context.Background(), semanticRequest(root, snapshot, ToolDefinition, "main.go", "", ""))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	descriptor.Arguments = testServerDescriptor(t, root, "normal", 2*time.Second).Arguments
	if err := manager.ReplaceConfiguration(context.Background(), []ServerDescriptor{descriptor}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("old query was reported successful after replacement")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("query/replacement deadlocked")
	}
}
