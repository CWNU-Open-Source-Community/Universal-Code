package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/mcp"
)

func TestMCPServeCLIUsesPersistedWorkspaceForReadTools(t *testing.T) {
	t.Setenv("CYBERAGENT_HOME", t.TempDir())
	command := func(args ...string) string {
		t.Helper()
		stdout, stderr, code := executeTestCommand(t, args...)
		if code != 0 || stderr != "" {
			t.Fatalf("command %v: code=%d stdout=%s stderr=%s", args, code, stdout, stderr)
		}
		return stdout
	}
	command("workspace", "init", "mcp-read")
	shown := command("workspace", "show", "mcp-read")
	workspaceID := fieldLine(shown, "id")
	workspaceRoot := fieldLine(shown, "path")
	if err := os.WriteFile(filepath.Join(workspaceRoot, "marker.txt"), []byte("workspace marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(workspaceRoot), "outside.txt"), []byte("outside marker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created := command("run", "create", "Read the MCP workspace", "--workspace", "mcp-read")
	runID := strings.Fields(created)[1]
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, executable, "-test.run=^TestMCPServeCLIHelper$", "--", runID, workspaceID)
	hideSubprocessWindow(process)
	// The process working directory is deliberately not the Workspace root.
	process.Dir = t.TempDir()
	process.Env = append(os.Environ(), "CYBERAGENT_MCP_CLI_HELPER=1")
	process.Stdin = strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"cli-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"marker.txt"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_workspace","arguments":{"path":"."}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"../outside.txt"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"shell","arguments":{"command":"echo forbidden"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"marker.txt","workspace_root":"outside"}}}`,
	}, "\n") + "\n")
	var stderr bytes.Buffer
	process.Stderr = &stderr
	stdout, err := process.Output()
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("MCP CLI process: %v stderr=%s stdout=%s", err, stderr.String(), stdout)
	}
	responses := make(map[string]mcp.Envelope)
	for _, line := range strings.Split(strings.TrimSpace(string(stdout)), "\n") {
		var envelope mcp.Envelope
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			t.Fatalf("stdout is not MCP JSON: %v: %s", err, line)
		}
		responses[string(envelope.ID)] = envelope
	}
	for _, id := range []string{"2", "3"} {
		var result mcp.CallToolResult
		if err := json.Unmarshal(responses[id].Result, &result); err != nil || len(result.Content) != 1 ||
			!strings.Contains(result.Content[0].Text, `"status":"completed"`) {
			encoded, _ := json.Marshal(responses[id])
			t.Fatalf("workspace read %s did not complete: %s", id, encoded)
		}
	}
	if responses["4"].Error == nil || responses["5"].Error == nil || responses["5"].Error.Code != mcp.CodeMethodNotFound {
		t.Fatalf("MCP read scope widened: traversal=%+v shell=%+v", responses["4"], responses["5"])
	}
	if responses["6"].Error == nil || responses["6"].Error.Code != mcp.CodeInvalidParams {
		t.Fatalf("schema-external workspace root was accepted: %+v", responses["6"])
	}
}

func TestMCPServeCLIHelper(t *testing.T) {
	if os.Getenv("CYBERAGENT_MCP_CLI_HELPER") != "1" {
		t.Skip("MCP CLI subprocess only")
	}
	args := os.Args[len(os.Args)-2:]
	os.Exit(Execute([]string{"mcp", "serve", "--run", args[0], "--workspace", args[1]}, os.Stdout, os.Stderr))
}
