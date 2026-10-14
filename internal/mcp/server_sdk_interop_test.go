package mcp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/toolgateway"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPServerStandardSDKStatelessInterop(t *testing.T) {
	server, audit := newTestServer(t)
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- server.Serve(ctx, serverReader, serverWriter)
		_ = serverWriter.Close()
	}()
	t.Cleanup(func() {
		_ = serverReader.Close()
		_ = clientReader.Close()
		_ = clientWriter.Close()
		_ = serverWriter.Close()
	})
	client := sdk.NewClient(&sdk.Implementation{Name: "standard-sdk", Version: "1", Title: "SDK peer"}, nil)
	session, err := client.Connect(ctx, &sdk.IOTransport{Reader: clientReader, Writer: clientWriter}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.InitializeResult().ProtocolVersion != preferredClientProtocolVersion {
		t.Fatalf("SDK did not negotiate current protocol: %#v", session.InitializeResult())
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 2 {
		t.Fatalf("standard tools/list: %v %#v", err, tools)
	}
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "list_workspace"})
	if err != nil || result.IsError || len(result.Content) == 0 {
		t.Fatalf("standard call with omitted optional arguments: %v %#v", err, result)
	}
	resources, err := session.ListResources(ctx, nil)
	if err != nil || len(resources.Resources) != 2 {
		t.Fatalf("standard resources/list: %v %#v", err, resources)
	}
	templates, err := session.ListResourceTemplates(ctx, nil)
	if err != nil || len(templates.ResourceTemplates) != 0 {
		t.Fatalf("standard resource template catalog: %v %#v", err, templates)
	}
	resource, err := session.ReadResource(ctx, &sdk.ReadResourceParams{URI: "cyberagent://run/summary"})
	if err != nil || len(resource.Contents) != 1 || !strings.Contains(resource.Contents[0].Text, "run-1") {
		t.Fatalf("standard resources/read: %v %#v", err, resource)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("server did not close after SDK transport closure")
	}
	audit.mu.Lock()
	defer audit.mu.Unlock()
	if len(audit.audits) != 3 || !strings.Contains(strings.Join(audit.audits, ","), "mcp.initialized") {
		t.Fatalf("SDK operations did not use the persisted audit event vocabulary: %v", audit.audits)
	}
}

func TestMCPServerNegotiatesLegacyRevisions(t *testing.T) {
	for _, version := range serverProtocolVersions[1:] {
		t.Run(version, func(t *testing.T) {
			server, _ := newTestServer(t)
			params, _ := json.Marshal(InitializeParams{ProtocolVersion: version, Capabilities: json.RawMessage(`{}`), ClientInfo: ClientInfo{Name: "legacy", Version: "1", Title: "Legacy peer"}})
			responses := serve(t, server, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":`+string(params)+`}`,
				`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_workspace","_meta":{"progressToken":"progress"}}}`)
			if !strings.Contains(responses[0], `"protocolVersion":"`+version+`"`) || strings.Contains(responses[1], `"error"`) {
				t.Fatalf("legacy metadata/negotiation failed: %v", responses)
			}
		})
	}
}

type failedGatewayTool struct{}

func (failedGatewayTool) Invoke(context.Context, toolgateway.ToolCall) (toolgateway.Outcome, error) {
	return toolgateway.Outcome{Result: &toolgateway.Result{Status: toolgateway.StatusDenied}}, nil
}

func TestMCPServerPreservesDenialAndModernProtocolGate(t *testing.T) {
	server, _ := newTestServer(t)
	server.tools = failedGatewayTool{}
	responses := serve(t, server,
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_workspace","_meta":{"io.modelcontextprotocol/protocolVersion":"2099-01-01"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_workspace","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	if !strings.Contains(responses[0], `"error"`) || !strings.Contains(responses[1], `"error"`) ||
		!strings.Contains(responses[1], `"code":-32022`) || !strings.Contains(responses[1], `"supported"`) ||
		!strings.Contains(responses[2], `"isError":true`) || !strings.Contains(responses[2], `"resultType":"complete"`) {
		t.Fatalf("version gate or gateway denial was lost: %v", responses)
	}
}
