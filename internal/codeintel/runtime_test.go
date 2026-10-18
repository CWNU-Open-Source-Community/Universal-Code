package codeintel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
)

const helperWorkspaceID = "workspace-code-intel-test"

func TestCodeIntelLSPHelperProcess(t *testing.T) {
	mode := ""
	for _, argument := range os.Args {
		if strings.HasPrefix(argument, "-test.outputdir=prayu-lsp-") {
			mode = strings.TrimPrefix(argument, "-test.outputdir=prayu-lsp-")
			break
		}
	}
	if mode == "" {
		return
	}
	code := runCodeIntelLSPHelper(mode, os.Stdin, os.Stdout)
	os.Exit(code)
}

func TestManagerExecutesAllReadOnlySemanticToolsAndInvalidatesStaleEvidence(t *testing.T) {
	root := testWorkspace(t)
	manager := testManager(t, root, "normal", 2*time.Second)
	snapshots := manager.Capabilities(context.Background(), helperWorkspaceID, root)
	if len(snapshots) != 1 || snapshots[0].Health != HealthHealthy {
		t.Fatalf("unexpected capability snapshot: %#v", snapshots)
	}
	snapshot := snapshots[0]
	if snapshot.ServerVersion != "clean-environment" || len(snapshot.ModelVisibleTools) != 10 {
		t.Fatalf("unexpected negotiated server: %#v", snapshot)
	}

	requests := []Request{
		semanticRequest(root, snapshot, ToolWorkspaceSymbols, "", "main", ""),
		semanticRequest(root, snapshot, ToolDocumentSymbols, "main.go", "", ""),
		semanticRequest(root, snapshot, ToolDefinition, "main.go", "", ""),
		semanticRequest(root, snapshot, ToolReferences, "main.go", "", ""),
		semanticRequest(root, snapshot, ToolImplementation, "main.go", "", ""),
		semanticRequest(root, snapshot, ToolHover, "main.go", "", ""),
		semanticRequest(root, snapshot, ToolSignatureHelp, "main.go", "", ""),
		semanticRequest(root, snapshot, ToolDiagnostics, "main.go", "", ""),
		semanticRequest(root, snapshot, ToolCallHierarchy, "main.go", "", "both"),
		semanticRequest(root, snapshot, ToolTypeHierarchy, "main.go", "", "both"),
	}
	var definition Result
	for _, request := range requests {
		result, err := manager.Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("%s failed: %v", request.Tool, err)
		}
		if err := result.Validate(); err != nil {
			t.Fatalf("%s returned invalid evidence: %v", request.Tool, err)
		}
		if result.State != EvidenceCurrent && result.State != EvidencePartial {
			t.Fatalf("%s state=%s", request.Tool, result.State)
		}
		if result.Provenance.WorkspaceID != helperWorkspaceID ||
			result.Provenance.ServerGeneration != snapshot.Generation ||
			result.Provenance.CapabilityFingerprint != snapshot.CapabilityFingerprint ||
			result.Provenance.RootFingerprint == "" || result.Provenance.DirtyDigest == "" {
			t.Fatalf("%s omitted provenance: %#v", request.Tool, result.Provenance)
		}
		if request.Path != "" && (result.Provenance.DocumentPath != "main.go" ||
			result.Provenance.DocumentSHA256 == "" || result.Provenance.DocumentVersion < 1) {
			t.Fatalf("%s omitted document binding: %#v", request.Tool, result.Provenance)
		}
		if request.Tool == ToolDefinition {
			definition = result
			if result.State != EvidencePartial || len(result.Items) != 1 ||
				len(result.Warnings) == 0 || result.Items[0].Path != "main.go" {
				t.Fatalf("hostile definition URI was not discarded: %#v", result)
			}
		}
		if request.Tool == ToolHover &&
			(!strings.Contains(result.Content, "[REDACTED:secret]") ||
				strings.Contains(result.Content, "https://") ||
				strings.Contains(result.Content, "super-secret-value")) {
			t.Fatalf("hover output was not safely projected: %q", result.Content)
		}
		if (request.Tool == ToolCallHierarchy || request.Tool == ToolTypeHierarchy) &&
			(len(result.Items) < 2 || len(result.Edges) < 1) {
			t.Fatalf("%s did not return a semantic graph: %#v", request.Tool, result)
		}
	}

	validation := manager.ValidateEvidence(context.Background(), root, definition.Provenance)
	if validation.State != EvidenceCurrent {
		t.Fatalf("fresh evidence was not current: %#v", validation)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"),
		[]byte("package main\nfunc Main() { println(\"changed\") }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	validation = manager.ValidateEvidence(context.Background(), root, definition.Provenance)
	if validation.State != EvidenceStale {
		t.Fatalf("edited document did not stale evidence: %#v", validation)
	}
	request := semanticRequest(root, snapshot, ToolDefinition, "main.go", "", "")
	changed, err := manager.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Provenance.DocumentVersion <= definition.Provenance.DocumentVersion ||
		changed.Provenance.DocumentSHA256 == definition.Provenance.DocumentSHA256 {
		t.Fatalf("didChange did not advance the document binding: old=%#v new=%#v",
			definition.Provenance, changed.Provenance)
	}
}

func TestManagerPaginatesStableEvidenceAndRejectsStaleCursor(t *testing.T) {
	root := testWorkspace(t)
	manager := testManager(t, root, "normal", 2*time.Second)
	snapshot := manager.Capabilities(context.Background(), helperWorkspaceID, root)[0]
	request := semanticRequest(root, snapshot, ToolWorkspaceSymbols, "", "main", "")
	request.Limit = 1
	first, err := manager.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Page.Total != 3 || first.Page.NextCursor == "" ||
		!first.Page.Truncated {
		t.Fatalf("unexpected first semantic page: %#v", first.Page)
	}
	request.Cursor = first.Page.NextCursor
	second, err := manager.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("unexpected second semantic page: %#v", second)
	}
	request.Query = "different"
	if _, err := manager.Execute(context.Background(), request); err == nil ||
		apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatalf("stale cursor error=%v", err)
	}
}

func TestManagerBoundsHierarchyNodesWithoutDanglingEdges(t *testing.T) {
	root := testWorkspace(t)
	manager := testManager(t, root, "many-hierarchy", 2*time.Second)
	snapshot := manager.Capabilities(context.Background(), helperWorkspaceID, root)[0]
	request := semanticRequest(root, snapshot, ToolCallHierarchy, "main.go", "", "incoming")
	request.Limit = MaxResultItems
	result, err := manager.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("bounded hierarchy result was invalid: %v", err)
	}
	if result.State != EvidencePartial || len(result.Items) != MaxResultItems ||
		len(result.Edges) != MaxResultItems-1 || len(result.Warnings) == 0 {
		t.Fatalf("hierarchy saturation was not explicit and bounded: items=%d edges=%d state=%s warnings=%v",
			len(result.Items), len(result.Edges), result.State, result.Warnings)
	}
}

func TestManagerBoundsCrashTimeoutCancellationProtocolAndOversizeFailures(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		timeout    time.Duration
		cancel     bool
		wantCode   apperror.Code
		wantHealth HealthStatus
	}{
		{name: "crash", mode: "crash", timeout: time.Second,
			wantCode: apperror.CodeUnavailable, wantHealth: HealthCrashed},
		{name: "timeout", mode: "hang", timeout: 300 * time.Millisecond,
			wantCode: apperror.CodeDeadlineExceeded, wantHealth: HealthTimedOut},
		{name: "cancel", mode: "hang", timeout: 2 * time.Second, cancel: true,
			wantCode: apperror.CodeCancelled, wantHealth: HealthUnavailable},
		{name: "oversize", mode: "oversize", timeout: time.Second,
			wantCode: apperror.CodeUnavailable, wantHealth: HealthUnavailable},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			root := testWorkspace(t)
			manager := testManager(t, root, current.mode, current.timeout)
			snapshot := manager.Capabilities(context.Background(), helperWorkspaceID, root)[0]
			if snapshot.Health != HealthHealthy {
				t.Fatalf("server did not initialize: %#v", snapshot)
			}
			ctx := context.Background()
			var cancel context.CancelFunc
			if current.cancel {
				ctx, cancel = context.WithCancel(ctx)
				time.AfterFunc(50*time.Millisecond, cancel)
			}
			_, err := manager.Execute(ctx,
				semanticRequest(root, snapshot, ToolDefinition, "main.go", "", ""))
			if cancel != nil {
				cancel()
			}
			if err == nil || apperror.CodeOf(apperror.Normalize(err)) != current.wantCode {
				t.Fatalf("error=%v code=%s", err, apperror.CodeOf(apperror.Normalize(err)))
			}
			inventory := manager.Inventory()
			if len(inventory) != 1 || inventory[0].Health != current.wantHealth {
				t.Fatalf("unexpected degraded health: %#v", inventory)
			}
			restarted := manager.Capabilities(context.Background(), helperWorkspaceID, root)[0]
			if restarted.Health != HealthHealthy || restarted.Generation == snapshot.Generation {
				t.Fatalf("server did not restart with a fresh generation: %#v", restarted)
			}
		})
	}

	for _, mode := range []string{"protocol", "message-too-large"} {
		t.Run(mode, func(t *testing.T) {
			root := testWorkspace(t)
			manager := testManager(t, root, mode, 500*time.Millisecond)
			snapshot := manager.Capabilities(context.Background(), helperWorkspaceID, root)[0]
			if snapshot.Health != HealthProtocolErr || snapshot.LastError == "" ||
				len(snapshot.LastError) > 2048 {
				t.Fatalf("protocol failure did not degrade safely: %#v", snapshot)
			}
		})
	}
}

func TestDiagnosticsDoesNotMaskTransportCrashAsPushFallback(t *testing.T) {
	root := testWorkspace(t)
	manager := testManager(t, root, "diagnostic-crash", time.Second)
	snapshot := manager.Capabilities(context.Background(), helperWorkspaceID, root)[0]
	_, err := manager.Execute(context.Background(),
		semanticRequest(root, snapshot, ToolDiagnostics, "main.go", "", ""))
	if err == nil || apperror.CodeOf(apperror.Normalize(err)) != apperror.CodeUnavailable {
		t.Fatalf("diagnostic transport crash was not surfaced: %v", err)
	}
	inventory := manager.Inventory()
	if len(inventory) != 1 || inventory[0].Health != HealthCrashed {
		t.Fatalf("diagnostic transport crash was masked as fallback: %#v", inventory)
	}
}

func TestTransportPreservesProtocolFailureWrittenBeforeProcessExit(t *testing.T) {
	root := testWorkspace(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	current, err := newTransport(testServerDescriptor(t, root, "protocol-after-notification", time.Second),
		root, t.TempDir(), func(string, json.RawMessage) {
			close(entered)
			<-release
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		unblock()
		ctx, cancel := context.WithTimeout(context.Background(), MaximumShutdownGracePeriod)
		defer cancel()
		_ = current.closeGracefully(ctx)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	response := make(chan error, 1)
	go func() { response <- current.request(ctx, "initialize", nil, nil) }()
	select {
	case <-entered:
	case <-current.done:
		t.Fatalf("transport stopped before reading the notification: %v", current.failure())
	case <-ctx.Done():
		t.Fatal("server did not send its notification")
	}
	select {
	case <-current.process.done:
	case <-ctx.Done():
		t.Fatal("server did not exit after writing its protocol failure")
	}
	// The reader is parked in onNotify while the exit observer is runnable.
	// It must not publish an exit failure before the final frame is parsed.
	select {
	case <-current.done:
		t.Fatalf("process exit overtook buffered protocol output: %v", current.failure())
	case <-time.After(time.Second):
	}
	unblock()
	select {
	case err := <-response:
		if healthForError(err) != HealthProtocolErr || !strings.Contains(err.Error(), "Content-Length") {
			t.Fatalf("final protocol failure was lost: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("protocol failure did not release the pending request")
	}
}

func TestTransportFailureClosesBeforeReleasingPendingRequests(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	// An unbuffered response channel makes the publication order observable:
	// setFailure cannot continue past the send until this waiter inspects done.
	pending := make(chan rpcResponse)
	current := &transport{
		stdin:   writer,
		pending: map[int64]chan rpcResponse{1: pending},
		done:    make(chan struct{}),
	}
	observed := make(chan error, 1)
	go func() {
		response := <-pending
		if response.err == nil {
			observed <- errors.New("pending request was released without the transport failure")
			return
		}
		select {
		case <-current.done:
			observed <- nil
		default:
			observed <- errors.New("pending request was released before transport termination")
		}
	}()

	current.setFailure(errors.New("test transport failure"), false)
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticsUsesOnlyCurrentBoundedPushFallback(t *testing.T) {
	root := testWorkspace(t)
	manager := testManager(t, root, "push-diagnostics", time.Second)
	snapshot := manager.Capabilities(context.Background(), helperWorkspaceID, root)[0]
	result, err := manager.Execute(context.Background(),
		semanticRequest(root, snapshot, ToolDiagnostics, "main.go", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Validate(); err != nil || result.State != EvidencePartial ||
		len(result.Items) != 1 || len(result.Warnings) == 0 ||
		result.Items[0].Detail != "push diagnostic" {
		t.Fatalf("push diagnostics fallback was not explicit and bounded: %#v err=%v", result, err)
	}
}

func TestManagerCloseReapsOwnedProcessAndChangesBootGeneration(t *testing.T) {
	root := testWorkspace(t)
	descriptor := testServerDescriptor(t, root, "normal", time.Second)
	manager, err := NewManager([]ServerDescriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	first := manager.Capabilities(context.Background(), helperWorkspaceID, root)[0]
	current, err := manager.ensureClient(context.Background(), serverKey(helperWorkspaceID, descriptor.ID), root)
	if err != nil {
		t.Fatal(err)
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = manager.Close(closeCtx)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-current.transport.process.done:
	case <-time.After(2 * time.Second):
		t.Fatal("owned language-server process remained after manager close")
	}
	manager2, err := NewManager([]ServerDescriptor{descriptor})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestManager(manager2) })
	second := manager2.Capabilities(context.Background(), helperWorkspaceID, root)[0]
	if first.Generation == second.Generation {
		t.Fatalf("application restart reused a server generation: %s", first.Generation)
	}
}

func TestSanitizersRejectEscapesRemoteLinksSecretsAndControlCharacters(t *testing.T) {
	root := testWorkspace(t)
	validURI, err := fileURI(filepath.Join(root, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	path, canonical, err := workspaceRelativeURI(root, validURI)
	if err != nil || path != "main.go" || canonical != validURI {
		t.Fatalf("valid URI rejected: path=%q uri=%q err=%v", path, canonical, err)
	}
	outsideURI, err := fileURI(filepath.Join(filepath.Dir(root), "outside.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, hostile := range []string{outsideURI, "https://example.com/source.go",
		"file://evil.example/source.go", validURI + "?token=secret", "file:%00bad"} {
		if _, _, err := workspaceRelativeURI(root, hostile); err == nil {
			t.Fatalf("hostile URI accepted: %s", hostile)
		}
	}
	outsideTarget := filepath.Join(filepath.Dir(root), "redirected.go")
	if err := os.WriteFile(outsideTarget, []byte("package redirected\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	redirected := filepath.Join(root, "redirected.go")
	if err := os.Symlink(outsideTarget, redirected); err == nil {
		redirectedURI, uriErr := fileURI(redirected)
		if uriErr != nil {
			t.Fatal(uriErr)
		}
		if _, _, err := workspaceRelativeURI(root, redirectedURI); err == nil {
			t.Fatal("Workspace symlink redirect was accepted as semantic evidence")
		}
	}
	markdown, cleaned := sanitizeMarkdown(root, "token=super-secret-value\u0007 "+
		"[remote](https://example.com/collect) [local]("+validURI+") "+
		"<https://example.com/auto> https://example.com/bare "+
		"<a href=\"//example.com/html\">html</a>\n[id]: javascript:alert(1)")
	if !cleaned || strings.Contains(markdown, "super-secret-value") ||
		strings.Contains(markdown, "https://") || strings.Contains(markdown, "javascript:") ||
		strings.Contains(markdown, "href=") || strings.Contains(markdown, "example.com") ||
		strings.ContainsRune(markdown, '\a') ||
		!strings.Contains(markdown, "main.go") {
		t.Fatalf("unsafe Markdown projection: %q", markdown)
	}
}

func TestReadLSPMessageRejectsMalformedAndUnboundedFrames(t *testing.T) {
	for _, raw := range []string{
		"Content-Type: application/json\r\n\r\n{}",
		"Content-Length: 2\r\nContent-Length: 2\r\n\r\n{}",
		fmt.Sprintf("Content-Length: %d\r\n\r\n", MaxMessageBytes+1),
		"Content-Length: 3\r\n\r\nxxx",
	} {
		if _, err := readLSPMessage(bufio.NewReader(strings.NewReader(raw))); err == nil {
			t.Fatalf("malformed LSP frame accepted: %q", raw)
		}
	}
	message, err := readLSPMessage(bufio.NewReader(strings.NewReader(
		"Content-Length: 2\r\n\r\n{}")))
	if err != nil || string(message) != "{}" {
		t.Fatalf("valid frame failed: %q %v", message, err)
	}
}

func TestDiagnosticNotificationsRequireCurrentOpenedWorkspaceDocument(t *testing.T) {
	root := testWorkspace(t)
	document, err := captureDocumentBinding(root, "main.go", 3)
	if err != nil {
		t.Fatal(err)
	}
	current := &client{workspace: workspaceBinding{Root: root},
		documents:   map[string]documentBinding{"main.go": document},
		diagnostics: make(map[string]json.RawMessage), serverLog: newBoundedBuffer(MaxLogBytes)}
	encode := func(uri string, version int) json.RawMessage {
		raw, err := json.Marshal(map[string]any{"uri": uri, "version": version,
			"diagnostics": []any{}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	outside, err := fileURI(filepath.Join(filepath.Dir(root), "outside.go"))
	if err != nil {
		t.Fatal(err)
	}
	current.handleNotification("textDocument/publishDiagnostics", encode(outside, 3))
	current.handleNotification("textDocument/publishDiagnostics", encode(document.URI, 2))
	if len(current.diagnostics) != 0 {
		t.Fatalf("unsafe or stale diagnostics were retained: %#v", current.diagnostics)
	}
	current.handleNotification("textDocument/publishDiagnostics", encode(document.URI, 3))
	if len(current.diagnostics) != 1 || len(current.diagnostics[document.URI]) == 0 {
		t.Fatalf("current opened-document diagnostics were not retained: %#v",
			current.diagnostics)
	}
}

func semanticRequest(root string, snapshot CapabilitySnapshot,
	tool, path, query, direction string,
) Request {
	request := Request{Tool: tool, WorkspaceID: helperWorkspaceID,
		WorkspaceRoot: root, ServerID: snapshot.ServerID,
		ServerGeneration:      snapshot.Generation,
		CapabilityFingerprint: snapshot.CapabilityFingerprint,
		Path:                  path, Query: query,
		Direction: direction, IncludeDeclaration: tool == ToolReferences, Limit: 100}
	if tool == ToolDefinition || tool == ToolReferences || tool == ToolImplementation ||
		tool == ToolHover || tool == ToolSignatureHelp || tool == ToolCallHierarchy ||
		tool == ToolTypeHierarchy {
		request.Position = Position{Line: 1, Character: 5}
	}
	return request
}

func testWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"),
		[]byte("package main\nfunc Main() { Helper() }\nfunc Helper() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func testManager(t *testing.T, root, mode string, timeout time.Duration) *Manager {
	t.Helper()
	manager, err := NewManager([]ServerDescriptor{testServerDescriptor(t, root, mode, timeout)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestManager(manager) })
	return manager
}

func closeTestManager(manager *Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = manager.Close(ctx)
	cancel()
}

func testServerDescriptor(t *testing.T, _ string, mode string,
	timeout time.Duration,
) ServerDescriptor {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable = filepath.Clean(executable)
	digest, available, err := executableDigest(executable)
	if err != nil || !available {
		t.Fatalf("hash test executable: available=%t err=%v", available, err)
	}
	return ServerDescriptor{ProtocolVersion: ProtocolVersion, ID: "test-lsp",
		Name: "Prayu test LSP", WorkspaceID: helperWorkspaceID,
		Languages:  []Language{{ID: "go", Extensions: []string{".go"}}},
		Executable: executable, Arguments: []string{
			"-test.run=^TestCodeIntelLSPHelperProcess$", "-test.outputdir=prayu-lsp-" + mode},
		ExecutableSHA256: digest, RequestTimeoutMillis: timeout.Milliseconds(),
		ReviewedBy: "code-intel-test", ReviewedAt: time.Now().UTC(),
		Source: Source{Kind: "operator_config", Label: "code-intel-test.json",
			SHA256: strings.Repeat("d", 64)}}
}

func runCodeIntelLSPHelper(mode string, input io.Reader, output io.Writer) int {
	reader := bufio.NewReader(input)
	root := ""
	for {
		raw, err := readLSPMessage(reader)
		if err != nil {
			return 0
		}
		var request rpcEnvelope
		if json.Unmarshal(raw, &request) != nil {
			return 21
		}
		if request.Method == "exit" {
			return 0
		}
		if len(request.ID) == 0 {
			if mode == "push-diagnostics" && request.Method == "textDocument/didOpen" {
				var opened struct {
					TextDocument struct {
						URI     string `json:"uri"`
						Version int    `json:"version"`
					} `json:"textDocument"`
				}
				if json.Unmarshal(request.Params, &opened) == nil {
					valueRange := map[string]any{
						"start": map[string]int{"line": 1, "character": 5},
						"end":   map[string]int{"line": 1, "character": 9}}
					params, _ := json.Marshal(map[string]any{"uri": opened.TextDocument.URI,
						"version": opened.TextDocument.Version,
						"diagnostics": []any{map[string]any{"range": valueRange,
							"severity": 2, "source": "push-lsp", "message": "push diagnostic"}}})
					_ = writeHelperFrame(output, rpcEnvelope{JSONRPC: "2.0",
						Method: "textDocument/publishDiagnostics", Params: params})
				}
			}
			continue
		}
		if request.Method == "initialize" {
			var params struct {
				RootURI string `json:"rootUri"`
			}
			_ = json.Unmarshal(request.Params, &params)
			root = params.RootURI
			if mode == "protocol-after-notification" {
				_ = writeHelperFrame(output, rpcEnvelope{JSONRPC: "2.0", Method: "window/logMessage",
					Params: json.RawMessage(`{"type":3,"message":"before exit"}`)})
				_, _ = fmt.Fprintf(output, "Content-Length: %d\r\n\r\n", MaxMessageBytes+1)
				return 0
			}
			if mode == "protocol" {
				_ = writeHelperFrame(output, map[string]any{"jsonrpc": "1.0", "id": 1,
					"result": map[string]any{}})
				return 0
			}
			if mode == "message-too-large" {
				_, _ = fmt.Fprintf(output, "Content-Length: %d\r\n\r\n", MaxMessageBytes+1)
				return 0
			}
			version := "clean-environment"
			if os.Getenv("PRAYU_TEST_LSP_SECRET") != "" {
				version = "environment-leaked"
			}
			result := map[string]any{
				"capabilities": map[string]any{
					"workspaceSymbolProvider": true, "documentSymbolProvider": true,
					"definitionProvider": true, "referencesProvider": true,
					"implementationProvider": true, "hoverProvider": true,
					"signatureHelpProvider": true, "diagnosticProvider": true,
					"callHierarchyProvider": true, "typeHierarchyProvider": true,
					"textDocumentSync": 1,
				},
				"serverInfo": map[string]string{"name": "Prayu test LSP", "version": version},
			}
			if writeHelperResponse(output, request.ID, result, nil) != nil {
				return 22
			}
			continue
		}
		if request.Method == "shutdown" {
			_ = writeHelperResponse(output, request.ID, nil, nil)
			continue
		}
		cwd, _ := os.Getwd()
		mainURI, _ := fileURI(filepath.Join(cwd, "main.go"))
		outsideURI, _ := fileURI(filepath.Join(filepath.Dir(cwd), "outside.go"))
		valueRange := map[string]any{"start": map[string]int{"line": 1, "character": 5},
			"end": map[string]int{"line": 1, "character": 9}}
		location := func(uri string) map[string]any {
			return map[string]any{"uri": uri, "range": valueRange}
		}
		hierarchyItem := func(name string) map[string]any {
			return map[string]any{"name": name, "kind": 12, "detail": "semantic node",
				"uri": mainURI, "range": valueRange, "selectionRange": valueRange,
				"data": map[string]string{"root": root}}
		}
		var result any
		var rpcErr *rpcError
		switch request.Method {
		case "workspace/symbol":
			result = []any{
				map[string]any{"name": "Alpha", "kind": 12, "location": location(mainURI)},
				map[string]any{"name": "Beta", "kind": 12, "location": location(mainURI)},
				map[string]any{"name": "Gamma", "kind": 12, "location": location(mainURI)},
			}
		case "textDocument/documentSymbol":
			result = []any{map[string]any{"name": "Main", "kind": 12,
				"range": valueRange, "selectionRange": valueRange}}
		case "textDocument/definition":
			switch mode {
			case "crash":
				return 17
			case "hang":
				for {
					time.Sleep(time.Hour)
				}
			case "oversize":
				result = strings.Repeat("x", MaxResultBytes+1)
			default:
				result = []any{location(mainURI), location(outsideURI)}
			}
		case "textDocument/references", "textDocument/implementation":
			result = []any{location(mainURI)}
		case "textDocument/hover":
			result = map[string]any{"contents": map[string]string{"kind": "markdown",
				"value": "token=super-secret-value\u0007 [remote](https://example.com/collect) " +
					"[local](" + mainURI + ")"}, "range": valueRange}
		case "textDocument/signatureHelp":
			result = map[string]any{"signatures": []any{map[string]any{"label": "Main(value string)",
				"documentation": map[string]string{"kind": "markdown", "value": "signature"}}},
				"activeSignature": 0, "activeParameter": 0}
		case "textDocument/diagnostic":
			if mode == "diagnostic-crash" {
				return 18
			}
			if mode == "push-diagnostics" {
				rpcErr = &rpcError{Code: -32601, Message: "pull diagnostics unsupported"}
			} else {
				result = map[string]any{"items": []any{map[string]any{"range": valueRange,
					"severity": 2, "code": "TEST001", "source": "test-lsp",
					"message": "token=diagnostic-super-secret-value", "tags": []int{1}}}}
			}
		case "textDocument/prepareCallHierarchy":
			result = []any{hierarchyItem("Main")}
		case "callHierarchy/incomingCalls":
			if mode == "many-hierarchy" {
				calls := make([]any, 0, MaxResultItems+5)
				for index := 0; index < MaxResultItems+5; index++ {
					calls = append(calls, map[string]any{
						"from":       hierarchyItem(fmt.Sprintf("Caller-%03d", index)),
						"fromRanges": []any{valueRange}})
				}
				result = calls
			} else {
				result = []any{map[string]any{"from": hierarchyItem("Caller"),
					"fromRanges": []any{valueRange}}}
			}
		case "callHierarchy/outgoingCalls":
			result = []any{map[string]any{"to": hierarchyItem("Callee"),
				"fromRanges": []any{valueRange}}}
		case "textDocument/prepareTypeHierarchy":
			result = []any{hierarchyItem("Child")}
		case "typeHierarchy/supertypes":
			result = []any{hierarchyItem("Parent")}
		case "typeHierarchy/subtypes":
			result = []any{hierarchyItem("Grandchild")}
		default:
			rpcErr = &rpcError{Code: -32601, Message: "unsupported test method"}
		}
		if writeHelperResponse(output, request.ID, result, rpcErr) != nil {
			return 23
		}
	}
}

func writeHelperResponse(output io.Writer, id json.RawMessage, result any,
	rpcErr *rpcError,
) error {
	response := rpcEnvelope{JSONRPC: "2.0", ID: append(json.RawMessage(nil), id...),
		Error: rpcErr}
	if rpcErr == nil {
		response.Result, _ = json.Marshal(result)
	}
	return writeHelperFrame(output, response)
}

func writeHelperFrame(output io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Content-Length: %d\r\n\r\n%s", len(raw), raw)
	return err
}

func TestMinimalLSPEnvironmentDropsAmbientSecrets(t *testing.T) {
	t.Setenv("PRAYU_TEST_LSP_SECRET", "token=ambient-super-secret-value")
	values := minimalLSPEnvironment(t.TempDir())
	joined := strings.Join(values, "\n")
	if strings.Contains(joined, "PRAYU_TEST_LSP_SECRET") ||
		strings.Contains(joined, "ambient-super-secret-value") ||
		!strings.Contains(joined, "GOPROXY=off") ||
		!strings.Contains(joined, "NPM_CONFIG_OFFLINE=true") {
		t.Fatalf("minimal environment widened authority: %s", joined)
	}
}

func TestHealthForErrorClassifiesOwnedProcessFailures(t *testing.T) {
	if healthForError(io.EOF) != HealthUnavailable ||
		healthForError(errors.New("read LSP message: EOF")) != HealthCrashed ||
		healthForError(fmt.Errorf("read LSP message: %w", os.ErrClosed)) != HealthCrashed ||
		healthForError(context.DeadlineExceeded) != HealthTimedOut ||
		healthForError(errors.New("invalid JSON-RPC envelope")) != HealthProtocolErr {
		t.Fatal("LSP failure health classification changed")
	}
}
