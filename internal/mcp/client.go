package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cyberagent-workbench/internal/redact"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Client owns one SDK session. Manager-facing methods only project durable,
// redacted records; JSON-RPC framing and request identity belong to the SDK.
type Client struct {
	resolved        *resolvedClient
	remote          *mcpHTTPTransport
	descriptor      ServerDescriptor
	secrets         []string
	secretsMu       sync.RWMutex
	closed          atomic.Bool
	tap             *sdkResponseTap
	protocolVersion string
	transport       sdk.Transport
	mu              sync.Mutex
	session         *sdk.ClientSession
	connectErr      error
	connected       bool
	lifetime        context.Context
	stop            context.CancelFunc
	closeOnce       sync.Once
	closeErr        error
	// Legacy adapter test hooks stay private; resolved clients use ExecutionGuards.
	// The progress handler is the only callback mutable after connection.
	beforeConnect   func(context.Context) error
	beforeCall      func(context.Context) error
	progress        func(context.Context, *sdk.ProgressNotificationParams)
	progressMu      sync.RWMutex
	allowedVersions []string
}

// Keep the existing Manager factory signature without a second transport API.
type clientTransport = *Client

func newClient(transport clientTransport, descriptor ServerDescriptor, secrets ...string) *Client {
	filtered := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		if secret != "" {
			filtered = append(filtered, secret)
		}
	}
	transport.descriptor, transport.secrets = descriptor, filtered
	return transport
}

func (c *Client) Discover(ctx context.Context, at time.Time) (CapabilitySnapshot, error) {
	if c == nil || c.transport == nil || c.closed.Load() {
		return CapabilitySnapshot{}, errors.New("MCP client is closed")
	}
	session, err := c.connect(ctx)
	if err != nil {
		return CapabilitySnapshot{}, c.sdkError(err)
	}
	initialized := session.InitializeResult()
	if initialized == nil || initialized.ServerInfo == nil {
		return CapabilitySnapshot{}, errors.New("MCP initialize response has no server identity")
	}
	serverInfo := *initialized.ServerInfo
	serverInfo.Name = c.sanitizeText(serverInfo.Name)
	serverInfo.Version = c.sanitizeText(serverInfo.Version)
	if !supportedClientProtocol(initialized.ProtocolVersion) ||
		!validClientIdentity(serverInfo.Name) ||
		!validClientText(serverInfo.Version, 128, false) {
		return CapabilitySnapshot{}, errors.New("MCP initialize response has an unsupported protocol or invalid server identity")
	}
	advertised := make([]CapabilityKind, 0, 3)
	if initialized.Capabilities != nil && initialized.Capabilities.Tools != nil {
		advertised = append(advertised, CapabilityTools)
	}
	if initialized.Capabilities != nil && initialized.Capabilities.Resources != nil {
		advertised = append(advertised, CapabilityResources)
	}
	if initialized.Capabilities != nil && initialized.Capabilities.Prompts != nil {
		advertised = append(advertised, CapabilityPrompts)
	}
	for _, capability := range advertised {
		if !slices.Contains(c.descriptor.DeclaredCapabilities, capability) {
			return CapabilitySnapshot{}, fmt.Errorf("MCP server advertised undeclared %s capability", capability)
		}
	}
	var tools []RemoteTool
	var templates []RemoteResourceTemplate
	var resources []RemoteResource
	var prompts []RemotePrompt
	if slices.Contains(advertised, CapabilityTools) {
		tools, err = c.listTools(ctx, session)
	}
	if err == nil && slices.Contains(advertised, CapabilityResources) {
		resources, err = c.listResources(ctx, session)
	}
	if err == nil && slices.Contains(advertised, CapabilityResources) {
		templates, err = c.listResourceTemplates(ctx, session)
	}
	if err == nil && slices.Contains(advertised, CapabilityPrompts) {
		prompts, err = c.listPrompts(ctx, session)
	}
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	snapshot, err := NewCapabilitySnapshot(serverInfo.Name, serverInfo.Version,
		advertised, tools, resources, prompts, at)
	if err == nil {
		snapshot.ResourceTemplates = templates
		snapshot.ProtocolVersion = initialized.ProtocolVersion
		snapshot.Fingerprint = capabilityFingerprint(snapshot)
	}
	if err == nil {
		err = snapshot.Validate()
	}
	return snapshot, err
}

func (c *Client) listTools(ctx context.Context, session *sdk.ClientSession) ([]RemoteTool, error) {
	items := make([]RemoteTool, 0)
	cursor := ""
	for page := 0; page < 16; page++ {
		result, err := session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, c.sdkError(err)
		}
		for _, item := range result.Tools {
			if item == nil {
				return nil, errors.New("MCP tool catalog contains null")
			}
			encoded, err := json.Marshal(item.InputSchema)
			if err != nil {
				return nil, err
			}
			schema, err := c.sanitizeJSON(encoded)
			if err != nil {
				return nil, fmt.Errorf("sanitize MCP tool schema: %w", err)
			}
			candidate := RemoteTool{Name: strings.TrimSpace(c.sanitizeText(item.Name)),
				Description: strings.TrimSpace(c.sanitizeText(item.Description)),
				InputSchema: schema}
			if err := candidate.Validate(); err != nil {
				return nil, fmt.Errorf("invalid MCP tool definition: %w", err)
			}
			items = append(items, candidate)
			if len(items) > MaxClientTools {
				return nil, errors.New("MCP tool catalog exceeds its limit")
			}
		}
		cursor = strings.TrimSpace(result.NextCursor)
		if cursor == "" {
			return items, nil
		}
		if !validClientText(cursor, 4096, false) {
			return nil, errors.New("MCP tool pagination cursor is invalid")
		}
	}
	return nil, errors.New("MCP tool pagination exceeded its page limit")
}

func (c *Client) listResources(ctx context.Context, session *sdk.ClientSession) ([]RemoteResource, error) {
	items := make([]RemoteResource, 0)
	cursor := ""
	for page := 0; page < 16; page++ {
		result, err := session.ListResources(ctx, &sdk.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return nil, c.sdkError(err)
		}
		for _, item := range result.Resources {
			if item == nil {
				return nil, errors.New("MCP resource catalog contains null")
			}
			candidate := RemoteResource{URI: strings.TrimSpace(c.sanitizeText(item.URI)),
				Name:        strings.TrimSpace(c.sanitizeText(item.Name)),
				Description: strings.TrimSpace(c.sanitizeText(item.Description)),
				MIMEType:    strings.TrimSpace(c.sanitizeText(item.MIMEType))}
			if err := candidate.Validate(); err != nil {
				return nil, fmt.Errorf("invalid MCP resource definition: %w", err)
			}
			items = append(items, candidate)
			if len(items) > MaxClientResources {
				return nil, errors.New("MCP resource catalog exceeds its limit")
			}
		}
		cursor = strings.TrimSpace(result.NextCursor)
		if cursor == "" {
			return items, nil
		}
		if !validClientText(cursor, 4096, false) {
			return nil, errors.New("MCP resource pagination cursor is invalid")
		}
	}
	return nil, errors.New("MCP resource pagination exceeded its page limit")
}

func (c *Client) listPrompts(ctx context.Context, session *sdk.ClientSession) ([]RemotePrompt, error) {
	items := make([]RemotePrompt, 0)
	cursor := ""
	for page := 0; page < 16; page++ {
		result, err := session.ListPrompts(ctx, &sdk.ListPromptsParams{Cursor: cursor})
		if err != nil {
			return nil, c.sdkError(err)
		}
		for _, item := range result.Prompts {
			if item == nil {
				return nil, errors.New("MCP prompt catalog contains null")
			}
			candidate := RemotePrompt{Name: strings.TrimSpace(c.sanitizeText(item.Name)),
				Description: strings.TrimSpace(c.sanitizeText(item.Description))}
			for _, argument := range item.Arguments {
				if argument == nil {
					return nil, errors.New("null MCP prompt argument")
				}
				candidate.Arguments = append(candidate.Arguments, RemotePromptArgument{Name: c.sanitizeText(argument.Name), Description: c.sanitizeText(argument.Description), Required: argument.Required})
			}
			slices.SortFunc(candidate.Arguments, func(a, b RemotePromptArgument) int { return strings.Compare(a.Name, b.Name) })
			if err := candidate.Validate(); err != nil {
				return nil, fmt.Errorf("invalid MCP prompt definition: %w", err)
			}
			items = append(items, candidate)
			if len(items) > MaxClientPrompts {
				return nil, errors.New("MCP prompt catalog exceeds its limit")
			}
		}
		cursor = strings.TrimSpace(result.NextCursor)
		if cursor == "" {
			return items, nil
		}
		if !validClientText(cursor, 4096, false) {
			return nil, errors.New("MCP prompt pagination cursor is invalid")
		}
	}
	return nil, errors.New("MCP prompt pagination exceeded its page limit")
}

type ClientCallResult struct {
	Content   string
	IsError   bool
	Bytes     int
	Truncated bool
}

func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage,
	maxBytes int,
) (ClientCallResult, error) {
	if !validRemoteName(name) || len(arguments) == 0 || len(arguments) > MaxClientArgumentsBytes ||
		!json.Valid(arguments) || len(bytes.TrimSpace(arguments)) == 0 || bytes.TrimSpace(arguments)[0] != '{' {
		return ClientCallResult{}, errors.New("MCP tool call name or arguments are invalid")
	}
	if maxBytes < 1 || maxBytes > MaxClientResultBytes {
		return ClientCallResult{}, errors.New("MCP tool result bound is invalid")
	}
	var result struct {
		Content           json.RawMessage `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
		IsError           bool            `json:"isError,omitempty"`
	}
	_, raw, err := c.callTool(ctx, &sdk.CallToolParams{Name: name, Arguments: json.RawMessage(bytes.Clone(arguments))})
	if err != nil {
		return ClientCallResult{}, c.sdkError(err)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return ClientCallResult{}, errors.New("MCP tool result content is invalid")
	}
	value := result.Content
	if len(result.StructuredContent) > 0 && !bytes.Equal(bytes.TrimSpace(result.StructuredContent), []byte("null")) {
		value, _ = json.Marshal(map[string]json.RawMessage{"content": result.Content,
			"structured_content": result.StructuredContent})
	}
	if len(value) == 0 || !json.Valid(value) {
		return ClientCallResult{}, errors.New("MCP tool result content is invalid")
	}
	value, err = c.sanitizeJSON(value)
	if err != nil {
		return ClientCallResult{}, errors.New("MCP tool result content could not be sanitized")
	}
	value, err = redact.SanitizeSensitiveJSON(value)
	if err != nil {
		return ClientCallResult{}, errors.New("MCP tool result sensitive fields could not be sanitized")
	}
	content := string(value)
	truncated := len([]byte(content)) > maxBytes
	if truncated {
		content = truncateClientUTF8(content, maxBytes)
	}
	return ClientCallResult{Content: content, IsError: result.IsError,
		Bytes: len([]byte(content)), Truncated: truncated}, nil
}

func (c *Client) addSecret(secret string) {
	if secret == "" {
		return
	}
	c.secretsMu.Lock()
	defer c.secretsMu.Unlock()
	if !slices.Contains(c.secrets, secret) {
		c.secrets = append(c.secrets, secret)
	}
}

func (c *Client) sanitizeText(value string) string {
	c.secretsMu.RLock()
	defer c.secretsMu.RUnlock()
	value = strings.ToValidUTF8(value, "?")
	for _, secret := range c.secrets {
		value = strings.ReplaceAll(value, secret, "[REDACTED:credential]")
	}
	return redact.String(value)
}

func (c *Client) sanitizeJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > MaxMessageBytes || !json.Valid(raw) {
		return nil, errors.New("remote JSON is missing, invalid, or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	value, err := c.sanitizeJSONValue(value)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > MaxMessageBytes {
		return nil, errors.New("sanitized remote JSON exceeds its bound")
	}
	return encoded, nil
}

func (c *Client) sanitizeJSONValue(value any) (any, error) {
	switch current := value.(type) {
	case string:
		return c.sanitizeText(current), nil
	case []any:
		for index := range current {
			var err error
			current[index], err = c.sanitizeJSONValue(current[index])
			if err != nil {
				return nil, err
			}
		}
		return current, nil
	case map[string]any:
		sanitized := make(map[string]any, len(current))
		for key, item := range current {
			safeKey := c.sanitizeText(key)
			if _, exists := sanitized[safeKey]; exists {
				return nil, errors.New("credential redaction caused a duplicate remote JSON field")
			}
			safeItem, err := c.sanitizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			sanitized[safeKey] = safeItem
		}
		return sanitized, nil
	default:
		return current, nil
	}
}
