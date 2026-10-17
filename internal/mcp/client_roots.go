package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"

	"cyberagent-workbench/internal/apperror"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// WorkspaceRoot is host-resolved context, not a filesystem execution grant.
// Fingerprint binds the selected physical directory using the existing
// workspace identity rules. A connection never changes to a different root.
type WorkspaceRoot struct{ Path, Fingerprint string }

type WorkspaceRootProvider func(context.Context, string, string) (WorkspaceRoot, error)

// InvocationWorkspace is provided by the host for an execution worker. It is
// never decoded from an MCP argument and is re-resolved before roots are sent.
type InvocationWorkspace struct {
	ScopeID string
	Resolve func(context.Context) (WorkspaceRoot, error)
}

type clientWorkspaceRoots struct {
	fingerprint string
	root        WorkspaceRoot
	uri         string
	resolve     func(context.Context) (WorkspaceRoot, error)
	check       func(context.Context) error
}

type rootsReplyKey struct{}
type rootsReplyPermit struct {
	client   *Client
	roots    *clientWorkspaceRoots
	id       jsonrpc.ID
	expected string
	used     bool // protected by client.rootsMu
}

func (m *Manager) prepareWorkspaceRoots(ctx context.Context, record ServerRecord, runID string,
	taskCheck func(context.Context) error,
	workspaces ...*InvocationWorkspace,
) (*clientWorkspaceRoots, error) {
	var owned *InvocationWorkspace
	if len(workspaces) > 0 {
		owned = workspaces[0]
	}
	if owned != nil && (!validClientIdentity(owned.ScopeID) || owned.Resolve == nil) {
		return nil, errors.New("MCP worker workspace identity is invalid")
	}
	if m.workspaceRoot == nil && owned == nil {
		return nil, nil
	}
	if taskCheck == nil {
		return nil, errors.New("MCP roots require current host context")
	}
	check := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := taskCheck(ctx); err != nil {
			return err
		}
		current, err := m.store.GetMCPClientServer(ctx, record.Descriptor.ID)
		if err != nil {
			return err
		}
		if current.DescriptorFingerprint != record.DescriptorFingerprint ||
			!scopeMatches(current.Descriptor, runID, record.Descriptor.WorkspaceID) {
			return apperror.New(apperror.CodeConflict, "MCP roots no longer match their server and Workspace")
		}
		if record.DiscoveryLeaseID != "" {
			if current.Generation != record.Generation || current.State != record.State || current.Health != HealthConnecting ||
				current.DiscoveryLeaseID != record.DiscoveryLeaseID || current.DiscoveryLeaseExpiresAt == nil || !current.DiscoveryLeaseExpiresAt.After(m.now()) {
				return apperror.New(apperror.CodePolicyDenied, "MCP roots discovery context expired")
			}
		} else if current.State != TrustEnabled {
			return apperror.New(apperror.CodePolicyDenied, "MCP roots server is no longer enabled")
		}
		if source := current.Descriptor.NativeSource; source != nil {
			if m.nativeSources == nil {
				return errors.New("MCP roots native source is unavailable")
			}
			return m.nativeSources.Check(ctx, *source, runID)
		}
		return nil
	}
	if err := check(ctx); err != nil {
		return nil, err
	}
	resolve := func(ctx context.Context) (WorkspaceRoot, error) {
		if owned != nil {
			return owned.Resolve(ctx)
		}
		return m.workspaceRoot(ctx, runID, record.Descriptor.WorkspaceID)
	}
	root, err := resolve(ctx)
	if err != nil {
		return nil, err
	}
	uri, err := workspaceRootURI(root)
	if err != nil {
		return nil, err
	}
	if err := check(ctx); err != nil {
		return nil, err
	}
	return &clientWorkspaceRoots{fingerprint: root.Fingerprint, root: root, uri: uri, resolve: resolve, check: check}, nil
}

func workspaceRootURI(root WorkspaceRoot) (string, error) {
	if !validClientText(root.Path, 4096, false) || !filepath.IsAbs(root.Path) || filepath.Clean(root.Path) != root.Path || !validClientDigest(root.Fingerprint) {
		return "", errors.New("MCP roots require one canonical host-resolved Workspace directory")
	}
	path := filepath.ToSlash(root.Path)
	uri := &url.URL{Scheme: "file", Path: path}
	if runtime.GOOS == "windows" {
		if strings.HasPrefix(path, "//") {
			host, relative, ok := strings.Cut(strings.TrimPrefix(path, "//"), "/")
			if !ok || host == "" {
				return "", errors.New("MCP roots UNC directory has no host or share")
			}
			uri.Host, uri.Path = host, "/"+relative
		} else {
			uri.Path = "/" + path
		}
	}
	return uri.String(), nil
}

func (c *Client) setWorkspaceRoots(roots *clientWorkspaceRoots) {
	c.rootsMu.Lock()
	c.workspaceRoots = roots
	c.rootsMu.Unlock()
}

// The SDK receives server requests on its session context. Roots use their own
// current task/discovery check and never borrow a tools/call dispatch permit.
func (c *Client) configureSDKRoots(options *sdk.ClientOptions) sdk.Middleware {
	c.rootsMu.RLock()
	enabled := c.workspaceRoots != nil
	c.rootsMu.RUnlock()
	if !enabled {
		return nil
	}
	options.Capabilities.RootsV2 = &sdk.RootCapabilities{}
	receive, prepare := c.tap.receiveInput, c.tap.prepareReply
	c.tap.receiveInput = func(request *jsonrpc.Request) {
		if request.Method == "roots/list" && request.IsCall() {
			c.rootsMu.Lock()
			if len(c.pendingRoots) < 32 {
				if c.pendingRoots == nil {
					c.pendingRoots = make(map[jsonrpc.ID]*clientWorkspaceRoots)
				}
				if _, exists := c.pendingRoots[request.ID]; !exists {
					c.pendingRoots[request.ID] = c.workspaceRoots
				}
			}
			c.rootsMu.Unlock()
		}
		if receive != nil {
			receive(request)
		}
	}
	c.tap.prepareReply = func(ctx context.Context, response *jsonrpc.Response) (context.Context, error) {
		c.rootsMu.Lock()
		roots, found := c.pendingRoots[response.ID]
		delete(c.pendingRoots, response.ID)
		c.rootsMu.Unlock()
		if found {
			expected, err := json.Marshal(roots.result(ctx))
			if err != nil || response.Error != nil || canonicalInputJSON(response.Result) != canonicalInputJSON(expected) {
				return ctx, errors.New("MCP roots reply differs from its requested host context")
			}
			return context.WithValue(ctx, rootsReplyKey{}, &rootsReplyPermit{client: c, roots: roots, id: response.ID, expected: canonicalInputJSON(expected)}), nil
		}
		if prepare != nil {
			return prepare(ctx, response)
		}
		return c.prepareLegacyReply(ctx, response)
	}
	return func(next sdk.MethodHandler) sdk.MethodHandler {
		return func(ctx context.Context, method string, request sdk.Request) (sdk.Result, error) {
			if method != "roots/list" {
				return next(ctx, method, request)
			}
			c.rootsMu.RLock()
			roots := c.workspaceRoots
			c.rootsMu.RUnlock()
			if c.closed.Load() {
				roots = nil
			}
			return roots.result(ctx), nil
		}
	}
}

func (r *clientWorkspaceRoots) result(ctx context.Context) *sdk.ListRootsResult {
	result := &sdk.ListRootsResult{Roots: []*sdk.Root{}}
	if r == nil || r.check(ctx) != nil {
		return result
	}
	current, err := r.resolve(ctx)
	if err == nil && current == r.root && r.check(ctx) == nil {
		result.Roots = []*sdk.Root{{URI: r.uri, Name: "Workspace"}}
	}
	return result
}

// Only a response to an observed roots/list ID can use this private permit.
// Other replies retain the exact existing elicitation checks. Tool requests
// never traverse this path and still need their own dispatch authority.
func authorizeClientReply(ctx context.Context, response *jsonrpc.Response, consume bool) error {
	permit, _ := ctx.Value(rootsReplyKey{}).(*rootsReplyPermit)
	if permit == nil {
		return authorizeLegacyReply(ctx, response, consume)
	}
	if ctx.Err() != nil || permit.client.closed.Load() || response.Error != nil || response.ID != permit.id || canonicalInputJSON(response.Result) != permit.expected {
		return errors.New("MCP roots reply identity or content changed")
	}
	// An empty answer can withdraw roots after revocation. Nonempty metadata
	// must still match the current physical root and task at the actual write.
	if permit.expected != `{"roots":[]}` {
		expected, err := json.Marshal(permit.roots.result(ctx))
		if err != nil || canonicalInputJSON(expected) != permit.expected {
			return errors.New("MCP roots reply context expired before send")
		}
	}
	permit.client.rootsMu.Lock()
	defer permit.client.rootsMu.Unlock()
	if permit.used {
		return errors.New("MCP roots reply was already sent")
	}
	if consume {
		permit.used = true
	}
	return nil
}
