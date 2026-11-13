package toolcontract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testDigest = strings.Repeat("a", 64)

func httpLaunch() ResolvedLaunch {
	return ResolvedLaunch{Component: ComponentRef{"package", "mcp"}, InstanceID: "instance",
		Format: "native-test", FormatVersion: "1", ProtocolVersions: []string{"2025-06-18"},
		Transport: TransportStreamableHTTP,
		HTTP: &HTTPLaunch{Endpoint: "https://example.test/mcp?account=private-value",
			Headers:    map[string]string{"X-API-Key": "short-secret", "Accept": "application/json"},
			Credential: &CredentialRef{ID: "credential-ref", Revision: "revision-1"}}}
}

func TestLaunchBranchesAreExplicitAndResolvedCredentialsAreVersioned(t *testing.T) {
	for name, mutate := range map[string]func(*ResolvedLaunch){
		"missing transport":   func(v *ResolvedLaunch) { v.Transport = "" },
		"wrong branch":        func(v *ResolvedLaunch) { v.Transport = TransportStdio },
		"both branches":       func(v *ResolvedLaunch) { v.Stdio = &StdioLaunch{Command: "node"} },
		"unbound credential":  func(v *ResolvedLaunch) { v.HTTP.Credential.Revision = "" },
		"header injection":    func(v *ResolvedLaunch) { v.HTTP.Headers["X-Key"] = "value\r\nInjected: yes" },
		"duplicate header":    func(v *ResolvedLaunch) { v.HTTP.Headers["accept"] = "text/plain" },
		"invalid header name": func(v *ResolvedLaunch) { v.HTTP.Headers["Bad Name"] = "value" },
		"userinfo":            func(v *ResolvedLaunch) { v.HTTP.Endpoint = "https://user:secret@example.test/mcp" },
		"unresolved endpoint": func(v *ResolvedLaunch) { v.HTTP.Endpoint = "${MCP_URL}" },
		"no versions":         func(v *ResolvedLaunch) { v.ProtocolVersions = nil },
		"duplicate versions":  func(v *ResolvedLaunch) { v.ProtocolVersions = []string{"x", "x"} },
	} {
		t.Run(name, func(t *testing.T) {
			value := httpLaunch()
			mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("accepted invalid resolved launch")
			}
		})
	}
	if err := httpLaunch().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDeclarationPreservesNativeInputsWithoutExpansion(t *testing.T) {
	d := LaunchDeclaration{Component: ComponentRef{"package", "server"}, Format: "native-test",
		FormatVersion: "1", Transport: TransportStdio, Stdio: &StdioLaunch{Command: "node",
			Args: []string{"${PLUGIN_ROOT}/index.js", "", "line1\nline2"},
			Cwd:  "${PLUGIN_ROOT}", Env: map[string]string{"DATA": "${PLUGIN_DATA}", "CERT": "a\nb"}}}
	before := fmt.Sprint(d.Stdio.Args, d.Stdio.Env, d.Stdio.Command, d.Stdio.Cwd)
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if after := fmt.Sprint(d.Stdio.Args, d.Stdio.Env, d.Stdio.Command, d.Stdio.Cwd); before != after {
		t.Fatal("validation changed native configuration")
	}
	d.Transport, d.Stdio = TransportStreamableHTTP, nil
	d.HTTP = &HTTPLaunch{Endpoint: "${MCP_URL}", Headers: map[string]string{"X-Key": "${API_KEY}"},
		Credential: &CredentialRef{ID: "host-credential"}}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if d.HTTP.Endpoint != "${MCP_URL}" || d.HTTP.Headers["X-Key"] != "${API_KEY}" {
		t.Fatal("declaration performed B-owned expansion")
	}
}

func TestLaunchFingerprintBindsHTTPInputsAndUsesPrivateKey(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	base, err := FingerprintLaunch(key, httpLaunch())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ResolvedLaunch){
		"endpoint":            func(v *ResolvedLaunch) { v.HTTP.Endpoint += "&different=1" },
		"header":              func(v *ResolvedLaunch) { v.HTTP.Headers["X-API-Key"] = "different-secret" },
		"credential id":       func(v *ResolvedLaunch) { v.HTTP.Credential.ID = "another-ref" },
		"credential revision": func(v *ResolvedLaunch) { v.HTTP.Credential.Revision = "revision-2" },
		"profile":             func(v *ResolvedLaunch) { v.ProtocolVersions = []string{"another-profile"} },
		"instance":            func(v *ResolvedLaunch) { v.InstanceID = "instance-2" },
	} {
		t.Run(name, func(t *testing.T) {
			value := httpLaunch()
			mutate(&value)
			got, err := FingerprintLaunch(key, value)
			if err != nil || got == base {
				t.Fatalf("changed launch not bound: %v", err)
			}
		})
	}
	value := httpLaunch()
	value.HTTP.Headers = map[string]string{"accept": "application/json", "x-api-key": "short-secret"}
	got, err := FingerprintLaunch(key, value)
	if err != nil || got != base {
		t.Fatalf("equivalent headers differ: %v", err)
	}
	if _, exists := value.HTTP.Headers["Accept"]; exists {
		t.Fatal("fingerprinting mutated headers")
	}
	other, err := FingerprintLaunch(bytes.Repeat([]byte{2}, 32), httpLaunch())
	if err != nil || other == base {
		t.Fatal("launch fingerprint is not key-bound")
	}
	if _, err := FingerprintLaunch(nil, httpLaunch()); err == nil {
		t.Fatal("accepted public secret hash")
	}
}

func TestLaunchFingerprintBindsActualProcessInputs(t *testing.T) {
	root := t.TempDir()
	fixture := func() ResolvedLaunch {
		v := httpLaunch()
		v.Transport, v.HTTP = TransportStdio, nil
		v.Stdio = &StdioLaunch{Command: filepath.Join(root, "node.exe"), ExecutableSHA256: testDigest,
			Args: []string{"a", "b"}, Env: map[string]string{"KEY": "value"}, Cwd: root}
		return v
	}
	key := bytes.Repeat([]byte{1}, 32)
	base, err := FingerprintLaunch(key, fixture())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*StdioLaunch){
		"command":     func(v *StdioLaunch) { v.Command += ".other" },
		"executable":  func(v *StdioLaunch) { v.ExecutableSHA256 = strings.Repeat("b", 64) },
		"args order":  func(v *StdioLaunch) { v.Args = []string{"b", "a"} },
		"environment": func(v *StdioLaunch) { v.Env["KEY"] = "other" },
		"cwd":         func(v *StdioLaunch) { v.Cwd = filepath.Join(root, "other") },
	} {
		t.Run(name, func(t *testing.T) {
			v := fixture()
			mutate(v.Stdio)
			got, err := FingerprintLaunch(key, v)
			if err != nil || got == base {
				t.Fatalf("process input not bound: %v", err)
			}
		})
	}
	v := fixture()
	v.Stdio.Args, v.Stdio.Env = nil, nil
	first, _ := FingerprintLaunch(key, v)
	v.Stdio.Args, v.Stdio.Env = []string{}, map[string]string{}
	second, _ := FingerprintLaunch(key, v)
	if first != second {
		t.Fatal("empty and nil execution inputs differ")
	}
	v.Stdio.Command = "node"
	if v.Validate() == nil {
		t.Fatal("resolved executable remained a bare command")
	}
}

func TestTransientLaunchCannotLeakThroughOrdinarySerializationOrFormatting(t *testing.T) {
	r := httpLaunch()
	declaration := LaunchDeclaration{HTTP: r.HTTP}
	for _, value := range []any{r, &r, *r.HTTP, r.HTTP, declaration,
		LaunchContext{BaseEnv: map[string]string{"KEY": "short-secret"}},
		StdioLaunch{Args: []string{"short-secret"}, Env: map[string]string{"KEY": "short-secret"}}} {
		if raw, err := json.Marshal(value); err == nil || strings.Contains(string(raw), "short-secret") {
			t.Fatal("launch configuration became publicly serializable")
		}
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if text := fmt.Sprintf(format, value); strings.Contains(text, "short-secret") || strings.Contains(text, "private-value") {
				t.Fatal("ordinary formatting leaked launch configuration")
			}
		}
	}
}

func operation() Operation {
	return Operation{ID: "op", Kind: OperationToolCall, ToolID: "tool", Component: ComponentRef{"package", "server"},
		AdapterID: "mcp", AdapterRevision: "adapter-1", InputFingerprint: testDigest,
		CapabilityFingerprint: testDigest, Targets: []Target{{"directory", "workspace-ref"}, {"endpoint", "endpoint-ref"}},
		Effects: []Effect{EffectWorkspaceRead, EffectUnknown}}
}

func TestOperationFingerprintBindsFinalEffectAndAuthorityInputs(t *testing.T) {
	base, err := FingerprintOperation(operation())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Operation){
		"input":      func(o *Operation) { o.InputFingerprint = strings.Repeat("b", 64) },
		"capability": func(o *Operation) { o.CapabilityFingerprint = strings.Repeat("b", 64) },
		"adapter":    func(o *Operation) { o.AdapterRevision = "adapter-2" },
		"target":     func(o *Operation) { o.Targets[0].Locator = "other-workspace" },
		"effect":     func(o *Operation) { o.Effects = []Effect{EffectRemoteWrite} },
		"identity":   func(o *Operation) { o.ID = "other-op" },
	} {
		t.Run(name, func(t *testing.T) {
			o := operation()
			mutate(&o)
			got, err := FingerprintOperation(o)
			if err != nil || got == base {
				t.Fatalf("operation change not bound: %v", err)
			}
		})
	}
	o := operation()
	o.Effects[0], o.Effects[1] = o.Effects[1], o.Effects[0]
	o.Targets[0], o.Targets[1] = o.Targets[1], o.Targets[0]
	before := append([]Target(nil), o.Targets...)
	got, err := FingerprintOperation(o)
	if err != nil || got != base || !reflect.DeepEqual(before, o.Targets) {
		t.Fatal("set fingerprinting is unstable or mutating")
	}
	o.Effects = []Effect{"readonly_hint"}
	if o.Validate() == nil {
		t.Fatal("accepted tool hint as an effect")
	}
}

func TestOperationFingerprintPreservesExactFileTargets(t *testing.T) {
	value := operation()
	value.Kind = OperationFileWrite
	value.Targets = []Target{{"file", "workspace: note.txt"}}
	plain, err := FingerprintOperation(value)
	if err != nil {
		t.Fatal(err)
	}
	value.Targets[0].Locator += " "
	exact, err := FingerprintOperation(value)
	if err != nil || exact == plain {
		t.Fatalf("exact file target did not get a distinct fingerprint: %v", err)
	}
	value.Targets[0].Locator += "\n"
	if err := value.Validate(); err == nil {
		t.Fatal("file target containing a control character was accepted")
	}
}

func discovery() DiscoveryScope {
	return DiscoveryScope{OperationID: "discover", ConnectionFingerprint: testDigest,
		Profile: "2025-06-18", Methods: []string{"initialize", "tools/list"},
		MaxRequests: 3, MaxBytes: 100, ExpiresAt: time.Now().Add(time.Minute)}
}

func TestDiscoveryPaginationUsesBoundedPermitAndRechecksEverySend(t *testing.T) {
	scope := discovery()
	checks := 0
	guard, err := NewDiscoverySendGuard(scope, func(context.Context) error { checks++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	// Mutating caller-owned slices must not extend an already issued permit.
	scope.Methods[0] = "tools/call"
	for _, method := range []string{"initialize", "tools/list", "tools/list"} {
		if err := guard(context.Background(), DiscoverySend{testDigest, method, 20}); err != nil {
			t.Fatal(err)
		}
	}
	if checks != 3 {
		t.Fatalf("live authority checks = %d", checks)
	}
	if guard(context.Background(), DiscoverySend{testDigest, "tools/list", 1}) == nil {
		t.Fatal("unbounded pagination")
	}
}

func TestDiscoverySendRejectsConnectionMethodByteDriftAndRevocation(t *testing.T) {
	for name, send := range map[string]DiscoverySend{
		"connection":     {strings.Repeat("b", 64), "tools/list", 1},
		"tool call":      {testDigest, "tools/call", 1},
		"unknown method": {testDigest, "extra/approval", 1},
		"oversized":      {testDigest, "tools/list", 101},
		"negative":       {testDigest, "tools/list", -1},
	} {
		t.Run(name, func(t *testing.T) {
			guard, _ := NewDiscoverySendGuard(discovery(), func(context.Context) error { t.Fatal("bad request reached authority"); return nil })
			if guard(context.Background(), send) == nil {
				t.Fatal("expanded discovery scope")
			}
		})
	}
	revoked := errors.New("revoked")
	guard, _ := NewDiscoverySendGuard(discovery(), func(context.Context) error { return revoked })
	if !errors.Is(guard(context.Background(), DiscoverySend{testDigest, "tools/list", 1}), revoked) {
		t.Fatal("lost revocation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	guard, _ = NewDiscoverySendGuard(discovery(), func(context.Context) error { cancel(); return nil })
	if !errors.Is(guard(ctx, DiscoverySend{testDigest, "tools/list", 1}), context.Canceled) {
		t.Fatal("cancelled during check still dispatched")
	}
	guard, _ = NewDiscoverySendGuard(discovery(), func(context.Context) error { return nil })
	if err := guard(context.Background(), DiscoverySend{testDigest, "tools/list", 70}); err != nil {
		t.Fatal(err)
	}
	if guard(context.Background(), DiscoverySend{testDigest, "tools/list", 31}) == nil {
		t.Fatal("byte budget reset per request")
	}
}

func TestDiscoveryConcurrentSendsCannotOverspend(t *testing.T) {
	scope := discovery()
	scope.MaxRequests = 1
	guard, _ := NewDiscoverySendGuard(scope, func(context.Context) error { return nil })
	var passed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if guard(context.Background(), DiscoverySend{testDigest, "tools/list", 1}) == nil {
				passed.Add(1)
			}
		}()
	}
	wg.Wait()
	if passed.Load() != 1 {
		t.Fatalf("permitted %d sends for one request", passed.Load())
	}
}

func TestDiscoveryScopeFingerprintAndValidation(t *testing.T) {
	scope := discovery()
	base, err := FingerprintDiscovery(scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*DiscoveryScope){
		func(s *DiscoveryScope) { s.MaxRequests++ }, func(s *DiscoveryScope) { s.MaxBytes++ },
		func(s *DiscoveryScope) { s.ExpiresAt = s.ExpiresAt.Add(time.Second) },
		func(s *DiscoveryScope) { s.ConnectionFingerprint = strings.Repeat("b", 64) },
		func(s *DiscoveryScope) { s.Profile = "2025-11-25" },
	} {
		changed := scope
		mutate(&changed)
		got, err := FingerprintDiscovery(changed)
		if err != nil || got == base {
			t.Fatal("discovery scope drift not fingerprinted")
		}
	}
	scope.Methods = []string{"tools/call"}
	if scope.Validate() == nil {
		t.Fatal("tools/call admitted as discovery")
	}
	scope = discovery()
	scope.ExpiresAt = time.Now().Add(-time.Second)
	if _, err := NewDiscoverySendGuard(scope, func(context.Context) error { return nil }); err == nil {
		t.Fatal("expired scope accepted")
	}
	if _, err := NewDiscoverySendGuard(discovery(), nil); err == nil {
		t.Fatal("missing live authority accepted")
	}
}

func TestContentReferencesAndReceiptsDoNotClaimAuthorityOrSuccess(t *testing.T) {
	ref := ContentRef{Component: ComponentRef{"package", "skill"}, Path: "references/example.md"}
	if err := ref.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", "/absolute", "C:/outside", "a/../b", "a\\b", "."} {
		ref.Path = path
		if ref.Validate() == nil {
			t.Fatalf("accepted escaped content path %q", path)
		}
	}
	for _, state := range []ReceiptState{ReceiptNotDispatched, ReceiptResultReceived, ReceiptOutcomeUnknown} {
		if err := (Receipt{OperationID: "op", State: state, ErrorCode: "remote_error"}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if (Receipt{OperationID: "op", State: "failed"}).Validate() == nil {
		t.Fatal("legacy failed conflated with effect knowledge")
	}
}
