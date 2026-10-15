package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWorkspaceRootsRecheckTaskServerAndPhysicalBinding(t *testing.T) {
	root := WorkspaceRoot{Path: t.TempDir(), Fingerprint: strings.Repeat("b", 64)}
	record := ServerRecord{Descriptor: ServerDescriptor{ID: "roots", Scope: ScopeWorkspace, WorkspaceID: "workspace-1"},
		DescriptorFingerprint: strings.Repeat("a", 64), State: TrustEnabled}
	store := &clientStoreFake{record: record, created: true}
	active := true
	manager, err := NewClientManager(store, nil, ManagerOptions{WorkspaceRoot: func(_ context.Context, run, workspace string) (WorkspaceRoot, error) {
		if run != "run-1" || workspace != "workspace-1" {
			t.Fatalf("server supplied or crossed a root scope: %s %s", run, workspace)
		}
		return root, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	roots, err := manager.prepareWorkspaceRoots(t.Context(), record, "run-1", func(context.Context) error {
		if !active {
			return errors.New("task authority revoked")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{tap: &sdkResponseTap{}}
	client.setWorkspaceRoots(roots)
	options := &sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}}
	middleware := client.configureSDKRoots(options)
	if middleware == nil || options.Capabilities.RootsV2 == nil || options.Capabilities.RootsV2.ListChanged {
		t.Fatal("immutable Workspace roots capability is missing")
	}
	handler := middleware(func(context.Context, string, sdk.Request) (sdk.Result, error) {
		t.Fatal("roots request fell through to the SDK's unchecked static list")
		return nil, nil
	})
	read := func() []*sdk.Root {
		value, err := handler(t.Context(), "roots/list", nil)
		if err != nil {
			t.Fatal(err)
		}
		return value.(*sdk.ListRootsResult).Roots
	}
	if actual := read(); len(actual) != 1 {
		t.Fatal("current task lost its single Workspace root", actual)
	} else if parsed, err := url.Parse(actual[0].URI); err != nil || parsed.Scheme != "file" || !strings.Contains(parsed.Path, filepath.Base(root.Path)) {
		t.Fatal("Workspace file URI is invalid", actual)
	}
	active = false
	if actual := read(); len(actual) != 0 {
		t.Fatal("idle request reused revoked task authority", actual)
	}
	active = true
	root.Fingerprint = strings.Repeat("c", 64)
	if actual := read(); len(actual) != 0 {
		t.Fatal("physical root drift was accepted", actual)
	}
	root.Fingerprint = roots.root.Fingerprint
	store.mu.Lock()
	store.record.State = TrustDisabled
	store.mu.Unlock()
	if actual := read(); len(actual) != 0 {
		t.Fatal("disabled server retained roots", actual)
	}
}

func TestWorkspaceRootsOperatorDiscoveryUsesItsExactLease(t *testing.T) {
	now := time.Now().UTC()
	expires := now.Add(time.Minute)
	record := ServerRecord{Descriptor: ServerDescriptor{ID: "roots", Scope: ScopeWorkspace, WorkspaceID: "workspace-1"},
		DescriptorFingerprint: strings.Repeat("a", 64), State: TrustDiscoveryApproved, Health: HealthConnecting,
		Generation: 2, DiscoveryLeaseID: "discovery-1", DiscoveryLeaseExpiresAt: &expires}
	store := &clientStoreFake{record: record, created: true}
	manager, err := NewClientManager(store, nil, ManagerOptions{Now: func() time.Time { return now },
		WorkspaceRoot: func(_ context.Context, run, workspace string) (WorkspaceRoot, error) {
			if run != "" || workspace != "workspace-1" {
				t.Fatal("operator discovery fabricated a Run")
			}
			return WorkspaceRoot{Path: t.TempDir(), Fingerprint: strings.Repeat("b", 64)}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	roots, err := manager.prepareWorkspaceRoots(t.Context(), record, "", func(context.Context) error { return nil })
	if err != nil {
		t.Fatal("operator-approved discovery requires no active Run", err)
	}
	now = expires
	if err := roots.check(t.Context()); err == nil {
		t.Fatal("expired discovery lease could disclose roots")
	}
}

func TestWorkspaceRootsReplyRequiresExactObservedIDContentAndLiveContext(t *testing.T) {
	active := true
	root := WorkspaceRoot{Path: t.TempDir(), Fingerprint: strings.Repeat("b", 64)}
	uri, err := workspaceRootURI(root)
	if err != nil {
		t.Fatal(err)
	}
	bound := &clientWorkspaceRoots{root: root, uri: uri, fingerprint: root.Fingerprint,
		resolve: func(context.Context) (WorkspaceRoot, error) { return root, nil },
		check: func(context.Context) error {
			if !active {
				return errors.New("revoked")
			}
			return nil
		}}
	client := &Client{tap: &sdkResponseTap{}}
	client.tap.prepareReply = client.prepareLegacyReply // production InputBroker path
	client.setWorkspaceRoots(bound)
	client.configureSDKRoots(&sdk.ClientOptions{Capabilities: &sdk.ClientCapabilities{}})
	message, err := jsonrpc.DecodeMessage([]byte(`{"jsonrpc":"2.0","id":7,"method":"roots/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	request := message.(*jsonrpc.Request)
	prepare := func() (context.Context, *jsonrpc.Response) {
		client.tap.receiveInput(request)
		body, _ := json.Marshal(bound.result(t.Context()))
		response := &jsonrpc.Response{ID: request.ID, Result: body}
		ctx, err := client.tap.prepareReply(t.Context(), response)
		if err != nil {
			t.Fatal("observed roots reply was rejected", err)
		}
		return ctx, response
	}
	ctx, response := prepare()
	if err := authorizeClientReply(ctx, response, false); err != nil {
		t.Fatal("HTTP preflight rejected valid roots", err)
	}
	forged := *response
	forged.Result = json.RawMessage(`{"roots":[{"uri":"file:///unselected"}]}`)
	if err := authorizeClientReply(ctx, &forged, true); err == nil {
		t.Fatal("changed roots escaped the exact host binding")
	}
	if err := authorizeLegacyReply(ctx, response, true); err == nil {
		t.Fatal("roots permit was misused as operator-input authority")
	}
	if err := authorizeClientReply(ctx, response, true); err != nil {
		t.Fatal("valid root reply could not be sent", err)
	}
	if err := authorizeClientReply(ctx, response, true); err == nil {
		t.Fatal("roots reply was replayed")
	}
	if _, err := client.tap.prepareReply(t.Context(), response); err == nil {
		t.Fatal("unobserved request ID obtained a reply permit")
	}
	ctx, response = prepare()
	active = false
	if err := authorizeClientReply(ctx, response, true); err == nil {
		t.Fatal("roots were sent after revocation during reply preparation")
	}
	ctx, response = prepare()
	if err := authorizeClientReply(ctx, response, true); err != nil || string(response.Result) != `{"roots":[]}` {
		t.Fatal("empty roots could not withdraw the old context", err)
	}
}

func TestWorkspaceRootsSDKIdleSessionUsesCurrentTaskWithoutOperationPermit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	server := sdk.NewServer(&sdk.Implementation{Name: "roots-fixture", Version: "1.0.0"}, nil)
	peer, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	client := newSDKClient(clientTransport)
	defer client.Close()
	client.protocolVersion = legacyClientProtocolVersion
	client.inputHandler = func(context.Context, *sdk.ElicitParams) (*sdk.ElicitResult, error) {
		return nil, errors.New("idle session has no operator-input authority")
	}
	root := WorkspaceRoot{Path: t.TempDir(), Fingerprint: strings.Repeat("b", 64)}
	uri, err := workspaceRootURI(root)
	if err != nil {
		t.Fatal(err)
	}
	var revoked atomic.Bool
	client.setWorkspaceRoots(&clientWorkspaceRoots{root: root, uri: uri, fingerprint: root.Fingerprint,
		resolve: func(context.Context) (WorkspaceRoot, error) { return root, nil },
		check: func(context.Context) error {
			if revoked.Load() {
				return errors.New("current task revoked")
			}
			return nil
		}})
	if _, err := client.connect(ctx); err != nil {
		t.Fatal(err)
	}
	// The real SDK now receives server requests on its background session.
	// Clear the previous operation exactly as the persistent Manager does.
	client.resolved = &resolvedClient{}
	clearSessionOperation(client)
	for _, withdraw := range []bool{false, true} {
		revoked.Store(withdraw)
		result, err := peer.ListRoots(ctx, &sdk.ListRootsParams{})
		if err != nil {
			t.Fatal("idle roots/list required a stale operation permit", err)
		}
		if withdraw {
			if len(result.Roots) != 0 {
				t.Fatal("revoked idle session kept disclosing roots", result.Roots)
			}
		} else if len(result.Roots) != 1 || result.Roots[0].URI != uri {
			t.Fatal("idle session lost its current task's exact root", result.Roots)
		}
	}
}

func TestWorkspaceRootsWindowsFileURIs(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows drive and UNC syntax")
	}
	for _, item := range []struct{ path, uri string }{
		{`D:\Project #1`, "file:///D:/Project%20%231"},
		{`\\server\share\Project #1`, "file://server/share/Project%20%231"},
	} {
		uri, err := workspaceRootURI(WorkspaceRoot{Path: item.path, Fingerprint: strings.Repeat("b", 64)})
		if err != nil || uri != item.uri {
			t.Fatalf("Workspace URI for %q = %q, %v; want %q", item.path, uri, err, item.uri)
		}
	}
}
