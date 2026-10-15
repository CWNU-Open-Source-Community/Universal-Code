package mcp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"cyberagent-workbench/internal/toolcontract"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type resolvedClient struct {
	launch      toolcontract.ResolvedLaunch
	key         []byte
	connect     toolcontract.Operation
	guards      toolcontract.ExecutionGuards
	fingerprint string
	mu          sync.Mutex
	capability  string
	negotiated  string
	credential  func(context.Context, toolcontract.CredentialRef) (string, error)
}
type resolvedDiscoveryKey struct{}
type resolvedDiscovery struct {
	scope toolcontract.DiscoveryScope
	guard toolcontract.DiscoverySendGuard
}
type resolvedCatalogKey struct{}
type resolvedCatalog struct {
	results []json.RawMessage
	bytes   int
}
type resolvedCallKey struct{}
type resolvedCall struct{ operation toolcontract.Operation }

// NewResolvedClient freezes the approved contract without connecting. The host
// supplies authorization and revision-bound credentials; no policy or ledger is
// created here. A nil guard at a traversed boundary denies that operation.
func NewResolvedClient(launch toolcontract.ResolvedLaunch, hostKey []byte, connect toolcontract.Operation,
	guards toolcontract.ExecutionGuards, credential func(context.Context, toolcontract.CredentialRef) (string, error),
	base *http.Client,
) (*Client, error) {
	launch = cloneResolvedLaunch(launch)
	key := bytes.Clone(hostKey)
	fingerprint, err := toolcontract.FingerprintLaunch(key, launch)
	if err != nil || connect.Validate() != nil || connect.Kind != toolcontract.OperationConnect ||
		connect.Component != launch.Component || connect.InputFingerprint != fingerprint {
		return nil, errors.New("MCP connect operation does not bind the resolved launch")
	}
	for _, version := range launch.ProtocolVersions {
		if !supportedClientProtocol(version) {
			return nil, errors.New("unsupported MCP client profile")
		}
	}
	r := &resolvedClient{launch: launch, key: key, connect: cloneOperation(connect), guards: guards, fingerprint: fingerprint, credential: credential}
	transport, remote, err := newLaunchClient(launch, base, r.beforeConnect, r.beforeSend)
	if err != nil {
		return nil, err
	}
	if remote != nil && launch.HTTP.Credential != nil {
		remote.beforeConnect = func(ctx context.Context) error {
			if err := r.beforeConnect(ctx); err != nil {
				return err
			}
			if r.credential == nil {
				return errors.New("MCP credential resolver is unavailable")
			}
			value, err := r.credential(ctx, *launch.HTTP.Credential)
			if err != nil || value == "" {
				return errors.New("bound MCP credential is unavailable")
			}
			remote.bearer = value
			return nil
		}
	}
	negotiated := transport.tap.negotiated
	transport.tap.negotiated = func(version string) {
		r.mu.Lock()
		r.negotiated = version
		r.mu.Unlock()
		if negotiated != nil {
			negotiated(version)
		}
	}
	client := newClient(transport, ServerDescriptor{DeclaredCapabilities: []CapabilityKind{CapabilityTools, CapabilityResources, CapabilityPrompts}})
	client.resolved = r
	return client, nil
}

func cloneOperation(operation toolcontract.Operation) toolcontract.Operation {
	operation.Targets = slices.Clone(operation.Targets)
	operation.Effects = slices.Clone(operation.Effects)
	return operation
}
func (r *resolvedClient) currentLaunch() (toolcontract.ResolvedLaunch, error) {
	current := cloneResolvedLaunch(r.launch)
	if current.Stdio != nil {
		var err error
		current.Stdio.Cwd, err = canonicalLaunchDirectory(current.Stdio.Cwd)
		if err != nil {
			return current, err
		}
		current.Stdio.Command, err = resolveLaunchExecutable(current.Stdio.Command, current.Stdio.Cwd, current.Stdio.Env)
		if err != nil {
			return current, err
		}
		current.Stdio.ExecutableSHA256, err = launchExecutableDigest(current.Stdio.Command)
		if err != nil {
			return current, err
		}
	}
	return current, nil
}
func (r *resolvedClient) beforeConnect(ctx context.Context) error {
	if r.guards.BeforeConnect == nil {
		return errors.New("MCP startup requires a host guard")
	}
	current, err := r.currentLaunch()
	if err != nil {
		return err
	}
	fingerprint, err := toolcontract.FingerprintLaunch(r.key, current)
	if err != nil || fingerprint != r.fingerprint {
		return errors.New("MCP launch changed after authorization preparation")
	}
	operation := cloneOperation(r.connect)
	operation.InputFingerprint = fingerprint
	actual, err := toolcontract.FingerprintOperation(operation)
	if err != nil {
		return err
	}
	if err := r.guards.BeforeConnect(ctx, actual); err != nil {
		return err
	}
	// A host check may block while the executable/directory is replaced.
	current, err = r.currentLaunch()
	if err != nil {
		return err
	}
	if after, err := toolcontract.FingerprintLaunch(r.key, current); err != nil || after != fingerprint {
		return errors.New("MCP launch changed during startup authorization")
	}
	return ctx.Err()
}

func (r *resolvedClient) beforeSend(ctx context.Context, frame *jsonrpc.Request, size int64) error {
	if err := r.validateFrameProtocol(frame); err != nil {
		if attempt, _ := ctx.Value(sdkCallAttemptKey{}).(*sdkCallAttempt); attempt != nil {
			return attempt.reject(err)
		}
		return err
	}
	if isClientInvocationMethod(frame.Method) {
		attempt, _ := ctx.Value(sdkCallAttemptKey{}).(*sdkCallAttempt)
		call, _ := ctx.Value(resolvedCallKey{}).(*resolvedCall)
		if attempt == nil || call == nil {
			return errors.New("MCP tool call has no host operation")
		}
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal(frame.Params, &params) != nil {
			return attempt.reject(errors.New("MCP tool frame is invalid"))
		}
		var actualInput string
		var err error
		if frame.Method == "tools/call" {
			actualInput, err = fingerprintToolParams(r.key, r.launch, frame.Params)
		} else {
			actualInput, err = FingerprintContentInput(r.key, r.launch, frame.Method, frame.Params)
		}
		if err != nil || actualInput != call.operation.InputFingerprint {
			return attempt.reject(errors.New("MCP tool inputs changed before dispatch"))
		}
		operation := cloneOperation(call.operation)
		operation.InputFingerprint = actualInput
		actual, err := toolcontract.FingerprintOperation(operation)
		if err != nil {
			return attempt.reject(err)
		}
		attempt.mu.Lock()
		attempt.wireID, _ = json.Marshal(frame.ID.Raw())
		attempt.mu.Unlock()
		// beforeSDKDispatch invokes this closure once, after all projection.
		attempt.guard = func(ctx context.Context) error {
			if r.guards.BeforeToolCall == nil {
				return errors.New("MCP tool call requires a host guard")
			}
			if err := r.guards.BeforeToolCall(ctx, actual); err != nil {
				return err
			}
			r.mu.Lock()
			matches := r.capability == call.operation.CapabilityFingerprint
			r.mu.Unlock()
			if !matches {
				return staleCatalogError()
			}
			return nil
		}
		return nil
	}
	if subscription, _ := ctx.Value(catalogSubscriptionKey{}).(*catalogSubscription); subscription != nil {
		if frame.Method == "subscriptions/listen" {
			return subscription.listen(ctx, r.fingerprint, frame, size)
		}
		if frame.Method == "notifications/cancelled" {
			return subscription.cancel(frame)
		}
		return errors.New("MCP subscription cannot send another method")
	}
	if frame.Method == "notifications/cancelled" {
		attempt, _ := ctx.Value(sdkCallAttemptKey{}).(*sdkCallAttempt)
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if attempt == nil || !attempt.dispatched.Load() || json.Unmarshal(frame.Params, &params) != nil {
			return errors.New("unbound MCP cancellation")
		}
		attempt.mu.Lock()
		matches := bytes.Equal(bytes.TrimSpace(params.RequestID), bytes.TrimSpace(attempt.wireID))
		attempt.mu.Unlock()
		if !matches || !attempt.cancelSent.CompareAndSwap(false, true) {
			return errors.New("unbound or repeated MCP cancellation")
		}
		return nil // Cancels only this dispatched request; never authorizes a new call.
	}
	discovery, _ := ctx.Value(resolvedDiscoveryKey{}).(*resolvedDiscovery)
	if discovery == nil || discovery.guard == nil {
		return errors.New("MCP protocol send requires a bounded discovery scope")
	}
	return discovery.guard(ctx, toolcontract.DiscoverySend{ConnectionFingerprint: r.fingerprint, Method: frame.Method, Bytes: size})
}

// Check the final SDK frame, not just the preferred version or method whitelist.
// In particular the SDK's legacy fallback must not emit an unapproved initialize.
func (r *resolvedClient) validateFrameProtocol(frame *jsonrpc.Request) error {
	var params struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Meta            map[string]json.RawMessage `json:"_meta"`
	}
	if len(frame.Params) > 0 && json.Unmarshal(frame.Params, &params) != nil {
		return errors.New("MCP frame has invalid protocol parameters")
	}
	r.mu.Lock()
	negotiated := r.negotiated
	r.mu.Unlock()
	if frame.Method == "initialize" {
		if !slices.Contains(r.launch.ProtocolVersions, params.ProtocolVersion) || params.ProtocolVersion == preferredClientProtocolVersion {
			return errors.New("MCP negotiation exceeds permitted protocol versions")
		}
	}
	meta, present := params.Meta[sdk.MetaKeyProtocolVersion]
	if present {
		var version string
		if json.Unmarshal(meta, &version) != nil || version != preferredClientProtocolVersion || !slices.Contains(r.launch.ProtocolVersions, version) ||
			(negotiated != "" && version != negotiated) || frame.Method == "initialize" {
			return errors.New("MCP frame metadata exceeds its authorized protocol")
		}
	}
	if (frame.Method == "server/discover" || (negotiated == preferredClientProtocolVersion && frame.IsCall())) && !present {
		return errors.New("modern MCP request is missing protocol metadata")
	}
	return nil
}

// DiscoverWithScope authorizes negotiation plus bounded pagination once and
// rechecks each actual encoded JSON-RPC send. Incoming progress uses no send budget.
func (c *Client) DiscoverWithScope(ctx context.Context, scope toolcontract.DiscoveryScope) (CapabilitySnapshot, error) {
	if c == nil || c.resolved == nil {
		return CapabilitySnapshot{}, errors.New("client has no resolved host contract")
	}
	r := c.resolved
	if scope.Validate() != nil || scope.ConnectionFingerprint != r.fingerprint || scope.Profile != r.launch.ProtocolVersions[0] || r.guards.BeginDiscovery == nil {
		return CapabilitySnapshot{}, errors.New("MCP discovery scope does not bind this connection")
	}
	scope.Methods = slices.Clone(scope.Methods)
	ctx, cancel := context.WithDeadline(ctx, scope.ExpiresAt)
	defer cancel()
	guard, err := r.guards.BeginDiscovery(ctx, scope)
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	if guard == nil {
		return CapabilitySnapshot{}, errors.New("MCP discovery was not authorized")
	}
	r.mu.Lock()
	r.capability = ""
	r.mu.Unlock()
	capture := &resolvedCatalog{}
	ctx = context.WithValue(ctx, resolvedDiscoveryKey{}, &resolvedDiscovery{scope: scope, guard: guard})
	ctx = context.WithValue(ctx, resolvedCatalogKey{}, capture)
	revision := c.catalogRevision.Load()
	snapshot, err := c.Discover(ctx, time.Now())
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	// Bind the native pages, including annotations, output schemas and extensions,
	// without forcing them through the legacy storage projection.
	encoded, _ := json.Marshal(struct {
		Protocol, SnapshotFingerprint string
		Pages                         []json.RawMessage
	}{snapshot.ProtocolVersion, snapshot.Fingerprint, capture.results})
	hash := hmac.New(sha256.New, r.key)
	_, _ = hash.Write([]byte("mcp.capabilities.v1\x00"))
	_, _ = hash.Write(encoded)
	r.mu.Lock()
	if c.catalogRevision.Load() != revision {
		r.mu.Unlock()
		return CapabilitySnapshot{}, staleCatalogError()
	}
	r.capability = hex.EncodeToString(hash.Sum(nil))
	r.mu.Unlock()
	return snapshot, nil
}

// NativeCapabilityFingerprint binds the last successful bounded discovery. It
// is ephemeral; C attaches it to the existing operation/authorization ledger.
func (c *Client) NativeCapabilityFingerprint() string {
	if c == nil || c.resolved == nil {
		return ""
	}
	c.resolved.mu.Lock()
	defer c.resolved.mu.Unlock()
	return c.resolved.capability
}

// FingerprintCallInput binds the frozen launch and semantic tool inputs using
// the host's private key. JSON numbers retain their original precision.
func FingerprintCallInput(key []byte, launch toolcontract.ResolvedLaunch, name string, arguments json.RawMessage) (string, error) {
	if !validRemoteName(name) || len(arguments) == 0 || len(arguments) > MaxClientArgumentsBytes || !json.Valid(arguments) || bytes.TrimSpace(arguments)[0] != '{' {
		return "", errors.New("invalid MCP tool name or arguments")
	}
	launchFingerprint, err := toolcontract.FingerprintLaunch(key, launch)
	if err != nil {
		return "", err
	}
	var args any
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return "", errors.New("invalid MCP arguments")
	}
	raw, err := json.Marshal(struct {
		Launch, Name string
		Arguments    any
	}{launchFingerprint, name, args})
	if err != nil {
		return "", errors.New("MCP tool input could not be fingerprinted")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("mcp.call.input.v1\x00"))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// CallToolWithReceipt returns a receipt even on errors. Native JSON is transient
// sensitive data for the host's redaction boundary, never a logging payload. The
// SDK result provides typed content; raw preserves unknown fields and numbers.
func (c *Client) CallToolWithReceipt(ctx context.Context, operation toolcontract.Operation, name string, arguments json.RawMessage) (*sdk.CallToolResult, json.RawMessage, toolcontract.Receipt, error) {
	return c.callToolParamsWithReceipt(ctx, operation, &sdk.CallToolParams{Name: name, Arguments: json.RawMessage(bytes.Clone(arguments))})
}
func (c *Client) callToolParamsWithReceipt(ctx context.Context, operation toolcontract.Operation, params *sdk.CallToolParams) (*sdk.CallToolResult, json.RawMessage, toolcontract.Receipt, error) {
	receipt := toolcontract.Receipt{OperationID: operation.ID, State: toolcontract.ReceiptNotDispatched}
	if c == nil || c.resolved == nil || c.closed.Load() || operation.Validate() != nil || operation.Kind != toolcontract.OperationToolCall ||
		operation.Component != c.resolved.launch.Component || operation.CapabilityFingerprint != c.NativeCapabilityFingerprint() {
		return nil, nil, receipt, errors.New("MCP tool operation is not bound to current discovery")
	}
	encoded, _ := json.Marshal(params)
	input, err := fingerprintToolParams(c.resolved.key, c.resolved.launch, encoded)
	if err != nil || input != operation.InputFingerprint {
		return nil, nil, receipt, errors.New("MCP tool input fingerprint differs from its operation")
	}
	attempt, capture := &sdkCallAttempt{}, &sdkRawResponse{}
	ctx = context.WithValue(ctx, resolvedCallKey{}, &resolvedCall{operation: cloneOperation(operation)})
	ctx = context.WithValue(ctx, sdkCallAttemptKey{}, attempt)
	ctx = context.WithValue(ctx, sdkRawResponseKey{}, capture)
	defer c.activateInput(ctx, attempt)()
	params.Meta = c.callProgressMeta(operation.ID)
	result, raw, err := c.callTool(ctx, params)
	_, wireError := capture.result()
	if len(raw) > 0 || wireError != nil {
		receipt.State = toolcontract.ReceiptResultReceived
	} else if attempt.dispatched.Load() {
		receipt.State = toolcontract.ReceiptOutcomeUnknown
	}
	if wireError != nil {
		receipt.ErrorCode = "remote_rpc_error"
		raw, _ = json.Marshal(struct {
			Error any `json:"error"`
		}{wireError})
		return nil, raw, receipt, errors.New("MCP peer returned a JSON-RPC error")
	}
	if err != nil {
		receipt.ErrorCode = "mcp_call_failed"
		var required *sdkInputRequiredError
		if len(raw) > 0 && !errors.As(err, &required) {
			receipt.ErrorCode = "invalid_result"
		}
		return nil, raw, receipt, &sanitizedClientError{message: "MCP tool call failed; inspect its execution receipt before recovery", cause: err}
	}
	return result, raw, receipt, nil
}

// SetProgressHandler opts into MCP progress notifications. A call then uses its
// operation ID as the progress token. Handlers must return promptly; callbacks
// contain untrusted peer data and are not authorization or durable state.
func (c *Client) SetProgressHandler(handler func(context.Context, *sdk.ProgressNotificationParams)) error {
	if c == nil || c.closed.Load() {
		return errors.New("MCP client is closed")
	}
	c.progressMu.Lock()
	c.progress = handler
	c.progressMu.Unlock()
	return nil
}
func (c *Client) callProgressMeta(operationID string) sdk.Meta {
	c.progressMu.RLock()
	enabled := c.progress != nil
	c.progressMu.RUnlock()
	if enabled {
		return sdk.Meta{"progressToken": operationID}
	}
	return nil
}
