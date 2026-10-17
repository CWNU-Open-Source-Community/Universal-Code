package mcp

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWorkerRootsStaySeparateAndNeverFallBackToParent(t *testing.T) {
	record := ServerRecord{Descriptor: ServerDescriptor{ID: "worker-roots", Scope: ScopeWorkspace, WorkspaceID: "workspace-1"}, DescriptorFingerprint: strings.Repeat("a", 64), State: TrustEnabled}
	st := &clientStoreFake{record: record, created: true}
	manager, err := NewClientManager(st, nil, ManagerOptions{WorkspaceRoot: func(context.Context, string, string) (WorkspaceRoot, error) {
		t.Fatal("worker fell back to parent roots")
		return WorkspaceRoot{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	roots := []WorkspaceRoot{{Path: t.TempDir(), Fingerprint: strings.Repeat("b", 64)}, {Path: t.TempDir(), Fingerprint: strings.Repeat("c", 64)}}
	readers := []func() []*sdk.Root{}
	for index := range roots {
		index := index
		owned := &InvocationWorkspace{ScopeID: []string{"worker-attempt-a", "worker-attempt-b"}[index], Resolve: func(context.Context) (WorkspaceRoot, error) { return roots[index], nil }}
		selected, err := manager.prepareWorkspaceRoots(t.Context(), record, "run-1", func(context.Context) error { return nil }, owned)
		if err != nil {
			t.Fatal(err)
		}
		client := &Client{tap: &sdkResponseTap{}}
		client.setWorkspaceRoots(selected)
		middleware := client.configureSDKRoots(&sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}})
		handler := middleware(func(context.Context, string, sdk.Request) (sdk.Result, error) {
			t.Fatal("unchecked SDK roots used")
			return nil, nil
		})
		readers = append(readers, func() []*sdk.Root {
			result, err := handler(t.Context(), "roots/list", nil)
			if err != nil {
				t.Fatal(err)
			}
			return result.(*sdk.ListRootsResult).Roots
		})
	}
	first, second := readers[0](), readers[1]()
	if len(first) != 1 || len(second) != 1 || first[0].URI == second[0].URI {
		t.Fatalf("worker roots leaked across connections: %+v %+v", first, second)
	}
	roots[0] = roots[1]
	if got := readers[0](); len(got) != 0 {
		t.Fatalf("rotated worker inherited sibling root: %+v", got)
	}
	if got := readers[1](); len(got) != 1 || got[0].URI != second[0].URI {
		t.Fatalf("sibling root changed: %+v", got)
	}
}
