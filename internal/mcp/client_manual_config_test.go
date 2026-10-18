package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cyberagent-workbench/internal/credential"
)

func manualConfigDescriptor(t *testing.T) ServerDescriptor {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	d := clientTransportDescriptor(TransportStdio, executable, nil)
	d.ProtocolVersion, d.Cwd = ConfigClientProtocolVersion, t.TempDir()
	d.Env = map[string]string{"MCP_MODE": "selected"}
	d.EnvCredentialRefs = map[string]string{"MCP_API_KEY": "fixture-secret"}
	return d
}

func TestManualConfigValidationRejectsMixedFieldsAndPlaintext(t *testing.T) {
	for _, kind := range []string{"legacy-fields", "native-fields", "relative-cwd", "literal-secret-name", "literal-token", "literal-bearer", "literal-envelope", "literal-encrypted-envelope", "duplicate-env", "invalid-env-name", "invalid-ref", "stdio-headers", "http-env", "remote-http", "query", "empty-query", "userinfo", "fragment", "auth-header", "cookie-header", "protocol-header", "case-header", "newline-header"} {
		t.Run(kind, func(t *testing.T) {
			d := manualConfigDescriptor(t)
			d.Env, d.EnvCredentialRefs = maps.Clone(d.Env), maps.Clone(d.EnvCredentialRefs)
			switch kind {
			case "legacy-fields":
				d.ProtocolVersion = ClientProtocolVersion
			case "native-fields":
				d.ProtocolVersion = NativeClientProtocolVersion
			case "relative-cwd":
				d.Cwd = "./relative"
			case "literal-secret-name":
				d.Env["GITHUB_TOKEN"] = "synthetic-token"
			case "literal-token":
				d.Env["MCP_MODE"] = "sk-" + strings.Repeat("x", 24)
			case "literal-bearer":
				d.Env["MCP_MODE"] = "Bearer synthetic"
			case "literal-envelope":
				d.Env["MCP_MODE"] = oauthCredentialEnvelopePrefix + "synthetic-envelope"
			case "literal-encrypted-envelope":
				d.Env["MCP_MODE"] = "uc-mcp-oauth-file-v1:synthetic-reference"
			case "duplicate-env":
				d.Env["MCP_API_KEY"] = "override"
			case "invalid-env-name":
				d.Env["invalid=name"] = "value"
			case "invalid-ref":
				d.EnvCredentialRefs["MCP_API_KEY"] = "unsafe reference"
			case "stdio-headers":
				d.Headers = map[string]string{"X-Project": "fixture"}
			default:
				d.Transport, d.Target = TransportStreamableHTTP, "http://127.0.0.1:9000/mcp"
				d.Cwd, d.Env, d.EnvCredentialRefs = "", nil, nil
				switch kind {
				case "http-env":
					d.Env = map[string]string{"MCP_MODE": "value"}
				case "remote-http":
					d.Target = "http://remote.invalid/mcp"
				case "query":
					d.Target += "?value=one"
				case "empty-query":
					d.Target += "?"
				case "userinfo":
					d.Target = "https://name:secret@remote.invalid/mcp"
				case "fragment":
					d.Target += "#fragment"
				case "auth-header":
					d.Headers = map[string]string{"aUtHoRiZaTiOn": "synthetic-token"}
				case "cookie-header":
					d.Headers = map[string]string{"Cookie": "session=fixture"}
				case "protocol-header":
					d.Headers = map[string]string{"MCP-Session-Id": "fixture"}
				case "case-header":
					d.Headers = map[string]string{"X-Project": "one", "x-project": "two"}
				case "newline-header":
					d.Headers = map[string]string{"X-Project": "one\r\nInjected: two"}
				}
			}
			if d.Validate() == nil {
				t.Fatal("unsafe or unsupported manual configuration accepted")
			}
		})
	}
}

func TestManualConfigLoopbackAndLegacyFingerprintCompatibility(t *testing.T) {
	d := manualConfigDescriptor(t)
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	before := d.Fingerprint()
	d.Env["MCP_MODE"] = "changed"
	if before == d.Fingerprint() {
		t.Fatal("configuration change did not alter reviewed fingerprint")
	}
	for _, endpoint := range []string{"http://127.0.0.1:9000/mcp", "http://localhost:9000/mcp", "http://[::1]:9000/mcp", "https://remote.invalid/mcp"} {
		d.Transport, d.Target, d.Cwd, d.Env, d.EnvCredentialRefs = TransportStreamableHTTP, endpoint, "", nil, nil
		d.Headers = map[string]string{"X-Project": "fixture"}
		if err := d.Validate(); err != nil {
			t.Fatal(endpoint, err)
		}
	}
	// This literal historical encoding does not know any v3 field. A retained
	// descriptor must still hash the exact old JSON after a newer binary opens it.
	raw := []byte(`{"protocol_version":"mcp-client.v1","id":"old","name":"old","transport":"streamable_http","target":"https://legacy.invalid/mcp","declared_capabilities":["tools"],"scope":"workspace","workspace_id":"workspace","source":{"kind":"manual","uri":"operator"},"call_timeout_ms":1000,"max_result_bytes":4096}`)
	var legacy ServerDescriptor
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if legacy.Validate() != nil || legacy.Fingerprint() != hex.EncodeToString(digest[:]) {
		t.Fatal("historical descriptor/fingerprint drifted")
	}
}

func TestManualConfigRuntimeUsesExplicitEnvironmentAndCredentialRevision(t *testing.T) {
	t.Setenv("MCP_PARENT_UNRELATED", "must-not-inherit")
	d := manualConfigDescriptor(t)
	owned := credential.NewMemoryStore()
	if err := owned.Put(t.Context(), "fixture-secret", "synthetic-secret-value"); err != nil {
		t.Fatal(err)
	}
	manager, err := NewClientManager(&clientStoreFake{}, owned, ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	values, err := manager.readManualEnvironment(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := resolveManualLaunch(d, []string{legacyClientProtocolVersion}, values)
	if err != nil || launch.Stdio.Cwd != filepath.Clean(d.Cwd) || launch.Stdio.Env[manualEnvironmentKey("MCP_MODE")] != "selected" ||
		launch.Stdio.Env[manualEnvironmentKey("MCP_API_KEY")] != "synthetic-secret-value" || launch.Stdio.Env["MCP_PARENT_UNRELATED"] != "" {
		t.Fatal("manual launch lost explicit configuration or inherited ambient environment", err)
	}
	if err := manager.recheckManualEnvironment(t.Context(), d, launch); err != nil {
		t.Fatal(err)
	}
	if err := owned.Put(t.Context(), "fixture-secret", "rotated-secret-value"); err != nil {
		t.Fatal(err)
	}
	if err := manager.recheckManualEnvironment(t.Context(), d, launch); err == nil {
		t.Fatal("rotated credential kept old dispatch authority")
	}
	if err := owned.Put(t.Context(), "fixture-secret", oauthCredentialEnvelopePrefix+"synthetic-envelope"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.readManualEnvironment(t.Context(), d); err == nil {
		t.Fatal("OAuth envelope became a stdio secret")
	}
	if err := owned.Put(t.Context(), "fixture-secret", "uc-mcp-oauth-file-v1:synthetic-reference"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.readManualEnvironment(t.Context(), d); err == nil {
		t.Fatal("OAuth encryption key/reference became a stdio secret")
	}
	if err := owned.Delete(t.Context(), "fixture-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.readManualEnvironment(t.Context(), d); err == nil {
		t.Fatal("missing credential permitted connection")
	}
	if runtime.GOOS == "windows" {
		d.Env = map[string]string{"Path": "one", "PATH": "two"}
		if d.Validate() == nil {
			t.Fatal("Windows environment aliases accepted")
		}
	}
}
