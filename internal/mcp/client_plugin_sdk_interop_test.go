package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cyberagent-workbench/internal/agentpackages"
	"cyberagent-workbench/internal/computeruse"
	"cyberagent-workbench/internal/toolcontract"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const sdkSharedFixtureEnv = "TRAVERSE_MCP_SDK_SHARED_FIXTURE"

// A's shared manifest launches helper.exe with ordinary plugin arguments, not
// Go test flags. The copied test executable therefore enters the SDK server
// here, without changing the manifest or writing test-runner output to stdout.
func TestMain(m *testing.M) {
	if handled, code := computeruse.Execute(os.Args[1:], os.Stdin, os.Stdout); handled {
		os.Exit(code)
	}
	if os.Getenv(sdkSharedFixtureEnv) == "1" && filepath.Base(os.Args[0]) == "helper.exe" {
		if err := sharedFixtureSDKServer().Run(context.Background(), &sdk.StdioTransport{}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func sharedFixtureSDKServer() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "agent-plugin-sdk-fixture", Version: "1"}, &sdk.ServerOptions{
		Logger:                    slog.New(slog.NewTextHandler(io.Discard, nil)),
		SupportedProtocolVersions: []string{preferredClientProtocolVersion},
	})
	var calls atomic.Int32
	server.AddTool(&sdk.Tool{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(_ context.Context, request *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			return &sdk.CallToolResult{
				Meta:    sdk.Meta{"vendor.fixture": json.RawMessage(`{"large":9007199254740993,"preserved":true}`)},
				Content: []sdk.Content{&sdk.TextContent{Text: "fixture-result"}},
				StructuredContent: map[string]any{
					"large": int64(9007199254740993), "arguments": json.RawMessage(request.Params.Arguments),
					"calls": calls.Add(1), "cwd": cwd, "env": os.Getenv("VALUE"), "argv": os.Args[1:],
				},
			}, nil
		})
	return server
}

// Read A's actual launch-handoff fixture. The HTTP variant changes only its
// reserved test origin before parsing, so all declarations still come through
// OpenDirectory/Launches and literal URL/header fields keep their native bytes.
func sharedFixtureSDKLaunch(t *testing.T, serverName, httpOrigin string) toolcontract.ResolvedLaunch {
	t.Helper()
	base := t.TempDir()
	root, data := filepath.Join(base, "plugin"), filepath.Join(base, "data")
	for _, dir := range []string{filepath.Join(root, "bin"), filepath.Join(root, "work"), filepath.Join(data, "work")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if serverName != "http-literals" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		source, err := os.Open(executable)
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		target, err := os.OpenFile(filepath.Join(root, "bin", "helper.exe"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			t.Fatal(err)
		}
		_, copyErr := io.Copy(target, source)
		closeErr := target.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatalf("copy SDK fixture executable: %v / %v", copyErr, closeErr)
		}
	}
	for _, name := range []string{"plugin.json", "mcp.json"} {
		content, err := os.ReadFile(filepath.Join("..", "agentpackages", "testdata", "launch-handoff", name))
		if err != nil {
			t.Fatal(err)
		}
		if name == "mcp.json" && httpOrigin != "" {
			if strings.Count(string(content), "https://example.invalid") != 1 {
				t.Fatal("shared fixture HTTP origin changed")
			}
			content = []byte(strings.Replace(string(content), "https://example.invalid", httpOrigin, 1))
		}
		if err := os.WriteFile(filepath.Join(root, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pkg, err := agentpackages.OpenDirectory(t.Context(), root, "sdk-handoff", toolcontract.SourceRef{URI: "test:launch-handoff", Revision: "fixture-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer pkg.Close()
	if len(pkg.Launches()) != 6 || len(pkg.Diagnostics()) != 1 {
		t.Fatal("A shared fixture declaration/diagnostic count changed")
	}
	baseEnv := map[string]string{sdkSharedFixtureEnv: "1"}
	if runtime.GOOS == "windows" {
		baseEnv["SystemRoot"] = os.Getenv("SystemRoot")
	}
	host := toolcontract.LaunchContext{InstanceID: "sdk-handoff", InstallRoot: root, DataRoot: data,
		BaseEnv: baseEnv, ProtocolVersions: []string{preferredClientProtocolVersion}}
	for _, declaration := range pkg.Launches() {
		name, _, found := pkg.ServerSource(declaration.Component)
		if found && name == serverName {
			launch, err := ResolveLaunch(declaration, host)
			if err != nil {
				t.Fatal(err)
			}
			return launch
		}
	}
	t.Fatalf("A did not emit server %q", serverName)
	return toolcontract.ResolvedLaunch{}
}

func assertSharedFixtureSDKCall(t *testing.T, launch toolcontract.ResolvedLaunch, httpClient *http.Client) {
	t.Helper()
	probe := &resolvedGuardProbe{}
	client := resolvedFixtureClient(t, launch, probe, httpClient)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	scope := testResolvedScope(t, launch, 2)
	scope.Methods = []string{"server/discover", "tools/list"}
	snapshot, err := client.DiscoverWithScope(ctx, scope)
	if err != nil || snapshot.ProtocolVersion != preferredClientProtocolVersion || client.NativeCapabilityFingerprint() == "" {
		t.Fatalf("A to SDK guarded discovery: %v", err)
	}
	const arguments = `{"large":9007199254740993,"literal":"${UNKNOWN}"}`
	operation := resolvedFixtureCall(t, client, launch, arguments)
	result, native, receipt, err := client.CallToolWithReceipt(ctx, operation, "lookup", json.RawMessage(arguments))
	if err != nil || result == nil || receipt.State != toolcontract.ReceiptResultReceived || result.IsError {
		t.Fatalf("A to SDK typed/raw/receipt: %v %v", receipt, err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("SDK result content count = %d", len(result.Content))
	}
	text, ok := result.Content[0].(*sdk.TextContent)
	if !ok || text.Text != "fixture-result" {
		t.Fatal("SDK typed content was not preserved")
	}
	var raw struct {
		Meta map[string]struct {
			Large     json.Number `json:"large"`
			Preserved bool        `json:"preserved"`
		} `json:"_meta"`
		StructuredContent struct {
			Large     json.Number `json:"large"`
			Arguments struct {
				Large   json.Number `json:"large"`
				Literal string      `json:"literal"`
			} `json:"arguments"`
			Calls int      `json:"calls"`
			Cwd   string   `json:"cwd"`
			Env   string   `json:"env"`
			Argv  []string `json:"argv"`
		} `json:"structuredContent"`
	}
	if err := json.Unmarshal(native, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.StructuredContent.Large != "9007199254740993" || raw.StructuredContent.Arguments.Large != "9007199254740993" ||
		raw.StructuredContent.Arguments.Literal != "${UNKNOWN}" || raw.StructuredContent.Calls != 1 ||
		raw.Meta["vendor.fixture"].Large != "9007199254740993" || !raw.Meta["vendor.fixture"].Preserved {
		t.Fatal("SDK raw result lost native metadata/precision, changed arguments, or repeated the tool")
	}
	if launch.Stdio != nil {
		root, data := launch.Stdio.Env["PLUGIN_ROOT"], launch.Stdio.Env["PLUGIN_DATA"]
		if launch.Stdio.Command != filepath.Join(root, "bin", "helper.exe") || raw.StructuredContent.Cwd != filepath.Join(data, "work") ||
			raw.StructuredContent.Env != root+"|"+data+"|${UNKNOWN}|$HOME|%TEMP%" ||
			!slices.Equal(raw.StructuredContent.Argv, []string{root + "/entry", data + "/cache", "${UNKNOWN}", "$HOME", "%TEMP%"}) {
			t.Fatal("actual SDK process did not receive A's resolved executable/cwd/env/argv")
		}
	}
	expected, err := toolcontract.FingerprintOperation(operation)
	if err != nil {
		t.Fatal(err)
	}
	probe.mu.Lock()
	actual := probe.lastCall
	probe.mu.Unlock()
	if actual != expected || probe.connects.Load() != 1 || probe.discoveries.Load() != 1 || probe.sends.Load() != 2 || probe.calls.Load() != 1 {
		t.Fatalf("startup/discovery/send/tool guards = %d/%d/%d/%d", probe.connects.Load(), probe.discoveries.Load(), probe.sends.Load(), probe.calls.Load())
	}
}

func TestAgentPluginsSharedFixtureSDKStdio(t *testing.T) {
	assertSharedFixtureSDKCall(t, sharedFixtureSDKLaunch(t, "command-versus-data-cwd", ""), nil)
}

func TestAgentPluginsSharedFixtureSDKHTTP(t *testing.T) {
	server := sharedFixtureSDKServer()
	sdkHandler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true})
	var requests atomic.Int32
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/mcp" ||
			r.URL.RawQuery != "root=${PLUGIN_ROOT}&data=${PLUGIN_DATA}&unknown=${UNKNOWN}" ||
			r.Header.Get("X-Literal") != "${PLUGIN_ROOT}|${PLUGIN_DATA}|${UNKNOWN}|$HOME|%TEMP%" ||
			r.Header.Get("Mcp-Protocol-Version") != preferredClientProtocolVersion {
			t.Error("actual SDK HTTP request lost literal fields or protocol binding")
		}
		sdkHandler.ServeHTTP(w, r)
	}))
	defer peer.Close()
	assertSharedFixtureSDKCall(t, sharedFixtureSDKLaunch(t, "http-literals", peer.URL), peer.Client())
	if requests.Load() != 3 {
		t.Fatalf("want discovery/catalog/tool only, got %d HTTP requests", requests.Load())
	}
}
