package mcp

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cyberagent-workbench/internal/toolcontract"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func newStdioClientTransport(descriptor ServerDescriptor) (clientTransport, error) {
	if descriptor.Transport != TransportStdio || descriptor.Validate() != nil {
		return nil, errors.New("valid stdio MCP descriptor is required")
	}
	return newLegacyLaunchClient(descriptor, "", nil)
}

func newRemoteClientTransport(descriptor ServerDescriptor, bearer string, base *http.Client) (clientTransport, error) {
	if descriptor.Transport != TransportStreamableHTTP || descriptor.Validate() != nil {
		return nil, errors.New("valid remote MCP descriptor is required")
	}
	if descriptor.CredentialRef != "" && bearer == "" {
		return nil, errors.New("configured MCP credential is unavailable")
	}
	return newLegacyLaunchClient(descriptor, bearer, base)
}

// Historical descriptors are input adapters, not another client runtime.
// Manager owns their review/authority semantics and persisted representation.
func newLegacyLaunchClient(d ServerDescriptor, bearer string, base *http.Client) (*Client, error) {
	// Keep the historical preferred version and supported negotiation set.
	launch, err := resolveManualLaunch(d, []string{legacyClientProtocolVersion, "2024-11-05", "2025-03-26", "2025-11-25", preferredClientProtocolVersion}, nil)
	if err != nil {
		return nil, err
	}
	if d.Transport == TransportStreamableHTTP && base == nil && d.ProtocolVersion == ClientProtocolVersion {
		// Preserve historical HTTP selection; new configuration uses the
		// existing resolved transport's proxy-free default.
		base = &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	}
	client, remote, err := newLaunchClient(launch, base, nil, nil)
	if err != nil {
		return nil, err
	}
	if remote != nil {
		remote.bearer = bearer
	}
	return client, nil
}

// Both descriptor adapters and host-resolved launches construct this same SDK
// session. Supplied callbacks retain the caller's existing authority boundary.
func newLaunchClient(launch toolcontract.ResolvedLaunch, base *http.Client,
	beforeStart func(context.Context) error,
	beforeSend func(context.Context, *jsonrpc.Request, int64) error,
) (*Client, *mcpHTTPTransport, error) {
	if err := launch.Validate(); err != nil {
		return nil, nil, err
	}
	var client *Client
	var remote *mcpHTTPTransport
	switch launch.Transport {
	case toolcontract.TransportStdio:
		env, err := normalizeLaunchEnvironment(launch.Stdio.Env)
		if err != nil || len(env) != len(launch.Stdio.Env) {
			return nil, nil, errors.New("resolved MCP environment is ambiguous")
		}
		for key, value := range env {
			if original, exists := launch.Stdio.Env[key]; !exists || original != value {
				return nil, nil, errors.New("resolved MCP environment was not normalized")
			}
		}
		if runtime.GOOS == "windows" && env["SYSTEMROOT"] == "" {
			return nil, nil, errors.New("resolved Windows launch is missing SystemRoot")
		}
		cmd := exec.Command(launch.Stdio.Command, launch.Stdio.Args...)
		cmd.Dir, cmd.Env = launch.Stdio.Cwd, launchEnvironmentEntries(env)
		client = newSDKClient(&commandClientTransport{cmd: cmd, beforeStart: beforeStart, beforeSend: beforeSend})
	case toolcontract.TransportStreamableHTTP:
		if base == nil {
			configured := http.DefaultTransport.(*http.Transport).Clone()
			configured.Proxy = nil
			base = &http.Client{Transport: configured}
		}
		client, remote = makeHTTPClientTransport(launch.HTTP.Endpoint, "", base)
		remote.headers = make(http.Header, len(launch.HTTP.Headers))
		for name, value := range launch.HTTP.Headers {
			remote.headers.Set(name, value)
		}
		remote.beforeConnect, remote.beforeSend = beforeStart, beforeSend
	}
	client.protocolVersion = launch.ProtocolVersions[0]
	client.allowedVersions = slices.Clone(launch.ProtocolVersions)
	client.tap.acceptProtocol = func(version string) error {
		if !slices.Contains(launch.ProtocolVersions, version) {
			return errors.New("MCP peer selected an unauthorized profile")
		}
		return nil
	}
	return client, remote, nil
}

func makeHTTPClientTransport(endpoint, bearer string, base *http.Client) (*Client, *mcpHTTPTransport) {
	var roundTripper http.RoundTripper = http.DefaultTransport.(*http.Transport).Clone()
	if base != nil && base.Transport != nil {
		roundTripper = base.Transport
	}
	origin, _ := url.Parse(endpoint) // Constructors validate the endpoint first.
	httpTransport := &mcpHTTPTransport{base: roundTripper, bearer: bearer, origin: origin}
	client := &http.Client{Transport: httpTransport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("MCP redirects are forbidden") }}
	if base != nil {
		client.Timeout = base.Timeout
	}
	transport := newSDKClient(&sdk.StreamableClientTransport{
		Endpoint: endpoint, HTTPClient: client, MaxEventSize: MaxMessageBytes,
		// The host owns reconciliation or explicit retry under fresh authority.
		MaxRetries: -1, DisableStandaloneSSE: true,
		// No OAuthHandler: authentication and interaction belong to the host.
	})
	// The SDK's session-update callback is private and cannot pass through a
	// public Connection observer. Restore only its negotiated header state;
	// the other callback behavior (standalone SSE) is explicitly disabled.
	transport.tap.negotiated = func(version string) { httpTransport.protocolVersion.Store(version) }
	return transport, httpTransport
}

// The SDK owns framing, multiplexing, notifications, and cancellation.
// This adapter owns only process lifetime and bounded stderr retention.
type commandClientTransport struct {
	cmd         *exec.Cmd
	beforeStart func(context.Context) error
	beforeSend  func(context.Context, *jsonrpc.Request, int64) error
}

func (t *commandClientTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.cmd.Stdin != nil || t.cmd.Stdout != nil {
		return nil, errors.New("MCP stdio command already has input or output")
	}
	stdinReader, stdin, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer stdinReader.Close()
	// Own the output pipes so cmd.Wait can reap the root and stop descendants
	// without closing stdout before the SDK drains the final response.
	stdout, stdoutWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr, stderrWriter, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stdoutWriter.Close()
		return nil, err
	}
	started := false
	defer func() {
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()
		if !started {
			_ = stdin.Close()
			_ = stdout.Close()
			_ = stderr.Close()
		}
	}()
	t.cmd.Stdin, t.cmd.Stdout, t.cmd.Stderr = stdinReader, stdoutWriter, stderrWriter
	if t.beforeStart != nil {
		if err := t.beforeStart(ctx); err != nil {
			return nil, err
		}
	}
	tree, err := startClientProcessTree(ctx, t.cmd)
	if err != nil {
		return nil, err
	}
	started = true
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
	process := &clientProcess{cmd: t.cmd, tree: tree, stdin: stdin, stdout: stdout, stderr: stderr,
		stderrLog: newBoundedBuffer(16 * 1024), done: make(chan struct{})}
	// Continue draining after the retention bound, so a chatty child cannot stall.
	go func() {
		defer stderr.Close()
		_, _ = io.Copy(process.stderrLog, stderr)
	}()
	process.wait()
	conn, err := (&sdk.IOTransport{Reader: process, Writer: stdin, MaxLineLength: MaxMessageBytes}).Connect(ctx)
	if err != nil {
		_ = process.Close()
		return nil, err
	}
	return &stdioSDKConnection{Connection: conn, writeGate: make(chan struct{}, 1), beforeSend: t.beforeSend}, nil
}

type clientProcessTree interface {
	Close() error
}

type clientProcess struct {
	cmd       *exec.Cmd
	tree      clientProcessTree
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	stderr    io.ReadCloser
	stderrLog *boundedBuffer
	done      chan struct{}
	waitErr   error
	waitOnce  sync.Once
	stopOnce  sync.Once
	stopErr   error
	closeOnce sync.Once
	closeErr  error
}

func (p *clientProcess) Read(b []byte) (int, error) {
	return p.stdout.Read(b)
}
func (p *clientProcess) wait() {
	p.waitOnce.Do(func() {
		go func() {
			p.waitErr = p.cmd.Wait()
			// A successful root exit still ends ownership of its descendants,
			// including those retaining inherited stdout or stderr handles.
			p.stopTree()
			close(p.done)
		}()
	})
}
func (p *clientProcess) stopTree() {
	p.stopOnce.Do(func() { p.stopErr = p.tree.Close() })
}
func (p *clientProcess) Close() error {
	p.closeOnce.Do(func() {
		_ = p.stdin.Close()
		p.wait()
		select {
		case <-p.done:
			p.closeErr = p.waitErr
		case <-time.After(250 * time.Millisecond):
			p.stopTree()
			select {
			case <-p.done:
			case <-time.After(2 * time.Second):
				p.closeErr = errors.New("MCP stdio process did not exit after termination")
			}
		}
		p.stopTree()
		p.closeErr = errors.Join(p.closeErr, p.stopErr)
		_ = p.stdout.Close()
		_ = p.stderr.Close()
	})
	return p.closeErr
}

// SSE is incremental and bounded per event by the SDK, not per stream.
type mcpHTTPTransport struct {
	protocolVersion atomic.Value
	origin          *url.URL
	base            http.RoundTripper
	bearer          string
	headers         http.Header
	beforeConnect   func(context.Context) error
	beforeSend      func(context.Context, *jsonrpc.Request, int64) error
	connectOnce     sync.Once
	connectErr      error
}

func (t *mcpHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	// Check before resolving credentials or copying configured headers. SDK
	// upgrades, redirects and endpoint events cannot silently change the origin.
	if !sameMCPOrigin(t.origin, request.URL) || (request.Host != "" && !strings.EqualFold(request.Host, request.URL.Host)) {
		return nil, errors.New("MCP request exceeds its bound origin")
	}
	request = request.Clone(request.Context())
	generated := request.Header
	request.Header = make(http.Header, len(generated)+len(t.headers))
	for name, values := range t.headers {
		if !hostControlledMCPHeader(name) {
			request.Header[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
	for name, values := range generated {
		request.Header[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
	}
	if t.beforeConnect != nil {
		t.connectOnce.Do(func() { t.connectErr = t.beforeConnect(request.Context()) })
		if t.connectErr != nil {
			return nil, t.connectErr
		}
	}
	version, _ := t.protocolVersion.Load().(string)
	if request.Header.Get("MCP-Protocol-Version") == "" && version != "" {
		request.Header.Set("MCP-Protocol-Version", version)
	}
	var envelope *jsonrpc.Request
	var wireBytes int64
	if request.Method == http.MethodPost {
		if request.GetBody == nil {
			return nil, errors.New("MCP HTTP dispatch requires a prepared request body")
		}
		body, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(body, MaxMessageBytes+1))
		_ = body.Close()
		if err != nil {
			return nil, err
		}
		if len(raw) > MaxMessageBytes {
			return nil, errors.New("MCP request exceeds the transport limit")
		}
		message, err := jsonrpc.DecodeMessage(raw)
		if err != nil {
			return nil, err
		}
		var ok bool
		envelope, ok = message.(*jsonrpc.Request)
		if !ok {
			return nil, errors.New("MCP client dispatch requires a request")
		}
		// Modern HTTP cancellation is closing the request stream. SDK v1.8.0
		// also emits a legacy cancellation notification; do not send it on
		// this profile after the HTTP request context has already been cancelled.
		if envelope.Method == "notifications/cancelled" && version == preferredClientProtocolVersion {
			return &http.Response{StatusCode: http.StatusAccepted, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		}
		wireBytes = int64(len(raw))
	}
	// Even an Idempotency-Key must not make net/http replay a tool POST.
	// SDK-level reconnect and multi-round-trip retries are disabled separately.
	request.GetBody = nil
	if t.bearer != "" {
		request.Header.Set("Authorization", "Bearer "+t.bearer)
	}
	if request.Method == http.MethodPost {
		if t.beforeSend != nil {
			if err := t.beforeSend(request.Context(), envelope, wireBytes); err != nil {
				return nil, err
			}
		}
		// Final HTTP guard: after body/header/credential projection.
		if err := beforeSDKDispatch(request.Context(), envelope.Method); err != nil {
			return nil, err
		}
	}
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if contentType != "text/event-stream" {
		response.Body = &limitedMCPBody{ReadCloser: response.Body, remaining: MaxMessageBytes}
	}
	return response, nil
}

func sameMCPOrigin(bound, actual *url.URL) bool {
	if bound == nil || actual == nil || bound.Hostname() == "" || actual.Hostname() == "" || actual.User != nil || actual.Opaque != "" ||
		!strings.EqualFold(bound.Scheme, actual.Scheme) || !strings.EqualFold(bound.Hostname(), actual.Hostname()) {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return port(bound) == port(actual)
}

// These values (including absence) belong to HTTP framing or the negotiated MCP
// session. Keep literal configuration in the launch fingerprint, but never let
// it supply a protocol/session header before the SDK has generated one.
func hostControlledMCPHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Host", "Content-Length", "Transfer-Encoding", "Connection", "Trailer", "Te", "Upgrade", "Proxy-Authorization",
		"Accept", "Content-Type", "Mcp-Protocol-Version", "Mcp-Session-Id", "Mcp-Method", "Mcp-Name", "Last-Event-Id":
		return true
	}
	return false
}
func (t *mcpHTTPTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

type limitedMCPBody struct {
	io.ReadCloser
	remaining int
}

func (b *limitedMCPBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.ReadCloser.Read(probe[:])
		if n > 0 {
			return 0, errors.New("remote MCP response exceeds the transport limit")
		}
		return 0, err
	}
	if len(p) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= n
	return n, err
}
func minimalMCPEnvironment() []string {
	allowed := map[string]struct{}{"PATH": {}, "PATHEXT": {}, "SYSTEMROOT": {}, "WINDIR": {},
		"TMP": {}, "TEMP": {}, "TMPDIR": {}, "LANG": {}, "LC_ALL": {}}
	values := make([]string, 0, len(allowed))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, found := allowed[strings.ToUpper(name)]; found {
			values = append(values, entry)
		}
	}
	return values
}

type boundedBuffer struct {
	mu    sync.Mutex
	limit int
	value []byte
}

func newBoundedBuffer(limit int) *boundedBuffer { return &boundedBuffer{limit: limit} }
func (b *boundedBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(value)
	if remaining := b.limit - len(b.value); remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
		}
		b.value = append(b.value, value...)
	}
	return n, nil
}
func (b *boundedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.value) }
