package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	ClientProtocolVersion       = "mcp-client.v1"
	ServerRecordProtocolVersion = "mcp-client-server.v1"
	CallAuditProtocolVersion    = "mcp-client-call-audit.v1"
	ClientName                  = "prayu"
	ClientVersion               = "1.0.0"

	MaxClientServers          = 64
	MaxClientTools            = 256
	MaxClientResources        = 256
	MaxClientPrompts          = 128
	MaxClientArgumentsBytes   = 256 * 1024
	MaxClientResultBytes      = 128 * 1024
	MaxClientDescriptionBytes = 16 * 1024
	MaxClientTargetBytes      = 4096
	MaxClientTransportArgs    = 32
)

type TransportKind string

const (
	TransportStdio          TransportKind = "stdio"
	TransportStreamableHTTP TransportKind = "streamable_http"
)

func (k TransportKind) Valid() bool {
	return k == TransportStdio || k == TransportStreamableHTTP
}

type ScopeKind string

const (
	ScopeRun       ScopeKind = "run"
	ScopeWorkspace ScopeKind = "workspace"
)

func (k ScopeKind) Valid() bool { return k == ScopeRun || k == ScopeWorkspace }

type TrustState string

const (
	TrustStaged              TrustState = "staged"
	TrustDiscoveryApproved   TrustState = "discovery_approved"
	TrustCapabilitiesPending TrustState = "capabilities_pending"
	TrustEnabled             TrustState = "enabled"
	TrustDisabled            TrustState = "disabled"
	TrustQuarantined         TrustState = "quarantined"
	TrustRevoked             TrustState = "revoked"
)

func (s TrustState) Valid() bool {
	switch s {
	case TrustStaged, TrustDiscoveryApproved, TrustCapabilitiesPending, TrustEnabled,
		TrustDisabled, TrustQuarantined, TrustRevoked:
		return true
	default:
		return false
	}
}

type HealthStatus string

const (
	HealthUnknown     HealthStatus = "unknown"
	HealthConnecting  HealthStatus = "connecting"
	HealthHealthy     HealthStatus = "healthy"
	HealthUnavailable HealthStatus = "unavailable"
	HealthDrifted     HealthStatus = "capability_drift"
)

func (s HealthStatus) Valid() bool {
	switch s {
	case HealthUnknown, HealthConnecting, HealthHealthy, HealthUnavailable, HealthDrifted:
		return true
	default:
		return false
	}
}

type CapabilityKind string

const (
	CapabilityTools     CapabilityKind = "tools"
	CapabilityResources CapabilityKind = "resources"
	CapabilityPrompts   CapabilityKind = "prompts"
)

func (k CapabilityKind) Valid() bool {
	return k == CapabilityTools || k == CapabilityResources || k == CapabilityPrompts
}

type Source struct {
	Kind        string `json:"kind"`
	URI         string `json:"uri"`
	Version     string `json:"version,omitempty"`
	Commit      string `json:"commit,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	PluginID    string `json:"plugin_id,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func (s Source) Validate() error {
	if s.Kind != "manual" && s.Kind != "plugin" {
		return errors.New("MCP source kind must be manual or plugin")
	}
	for label, value := range map[string]string{"uri": s.URI, "version": s.Version,
		"commit": s.Commit, "plugin_id": s.PluginID, "publisher": s.Publisher} {
		if !validClientText(value, MaxClientTargetBytes, label != "uri") {
			return fmt.Errorf("MCP source %s is invalid", label)
		}
	}
	for label, value := range map[string]string{"sha256": s.SHA256, "fingerprint": s.Fingerprint} {
		if value != "" && !validClientDigest(value) {
			return fmt.Errorf("MCP source %s is invalid", label)
		}
	}
	if s.Kind == "plugin" && (s.PluginID == "" || s.Fingerprint == "") {
		return errors.New("plugin MCP source requires plugin identity and fingerprint")
	}
	return nil
}

type ServerDescriptor struct {
	ProtocolVersion      string            `json:"protocol_version"`
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	Transport            TransportKind     `json:"transport"`
	Target               string            `json:"target"`
	Arguments            []string          `json:"arguments,omitempty"`
	CredentialRef        string            `json:"credential_ref,omitempty"`
	Cwd                  string            `json:"cwd,omitempty"`
	Env                  map[string]string `json:"env,omitempty"`
	EnvCredentialRefs    map[string]string `json:"env_credential_refs,omitempty"`
	Headers              map[string]string `json:"headers,omitempty"`
	DeclaredCapabilities []CapabilityKind  `json:"declared_capabilities"`
	Scope                ScopeKind         `json:"scope"`
	RunID                string            `json:"run_id,omitempty"`
	WorkspaceID          string            `json:"workspace_id"`
	Source               Source            `json:"source"`
	CallTimeoutMillis    int64             `json:"call_timeout_ms"`
	MaxResultBytes       int               `json:"max_result_bytes"`
	NativeSource         *NativeSourceRef  `json:"native_source,omitempty"`
}

func (d ServerDescriptor) Validate() error {
	if (d.ProtocolVersion != ClientProtocolVersion && d.ProtocolVersion != NativeClientProtocolVersion && d.ProtocolVersion != ConfigClientProtocolVersion) || !validClientIdentity(d.ID) ||
		!validClientIdentity(d.Name) || !d.Transport.Valid() || !d.Scope.Valid() ||
		!validClientIdentity(d.WorkspaceID) || d.CallTimeoutMillis < 100 ||
		d.CallTimeoutMillis > int64((5*time.Minute)/time.Millisecond) ||
		d.MaxResultBytes < 1 || d.MaxResultBytes > MaxClientResultBytes ||
		len(d.Arguments) > MaxClientTransportArgs {
		return errors.New("MCP server descriptor identity, scope, or bounds are invalid")
	}
	if d.Scope == ScopeRun && !validClientIdentity(d.RunID) {
		return errors.New("Run-scoped MCP server requires a Run identity")
	}
	if d.Scope == ScopeWorkspace && d.RunID != "" {
		return errors.New("Workspace-scoped MCP server cannot carry a Run identity")
	}
	if err := d.Source.Validate(); err != nil {
		return err
	}
	if d.ProtocolVersion == ConfigClientProtocolVersion {
		if err := d.validateManualConfig(); err != nil {
			return err
		}
	} else if d.Cwd != "" || len(d.Env) != 0 || len(d.EnvCredentialRefs) != 0 || len(d.Headers) != 0 {
		return errors.New("MCP connection configuration requires the manual v3 descriptor protocol")
	}
	if d.ProtocolVersion == NativeClientProtocolVersion {
		if d.NativeSource == nil || d.NativeSource.Validate() != nil || d.Source.Kind != "plugin" ||
			d.Source.PluginID != d.NativeSource.Component.PackageID || d.Source.SHA256 != d.NativeSource.Revision ||
			d.Target != "" || len(d.Arguments) != 0 || d.CredentialRef != "" {
			return errors.New("native MCP registration must retain exact source references without legacy launch fields")
		}
	} else if d.NativeSource != nil {
		return errors.New("legacy MCP registration cannot carry a native source")
	} else if d.Transport == TransportStdio {
		if !filepath.IsAbs(d.Target) || !validClientText(d.Target, MaxClientTargetBytes, false) ||
			d.CredentialRef != "" {
			return errors.New("stdio MCP target must be an absolute path and cannot receive a credential")
		}
	} else {
		parsed, err := url.Parse(d.Target)
		if err != nil || (parsed.Scheme != "https" && !(d.ProtocolVersion == ConfigClientProtocolVersion && agentPluginEndpoint(d.Target))) || parsed.Host == "" || parsed.User != nil ||
			parsed.RawQuery != "" || parsed.Fragment != "" || len(d.Arguments) != 0 {
			return errors.New("remote MCP target must be fixed HTTPS or local HTTP without userinfo, query, fragment, or process arguments")
		}
		if !validClientText(d.Target, MaxClientTargetBytes, false) {
			return errors.New("remote MCP target is invalid")
		}
	}
	for _, argument := range d.Arguments {
		if !validClientText(argument, 1024, true) || sensitiveClientArgument(argument) {
			return errors.New("stdio MCP argument is invalid")
		}
	}
	if d.CredentialRef != "" && !validCredentialReference(d.CredentialRef) {
		return errors.New("MCP credential reference is invalid")
	}
	if len(d.DeclaredCapabilities) == 0 || len(d.DeclaredCapabilities) > 3 {
		return errors.New("MCP descriptor must declare one to three capabilities")
	}
	seen := make(map[CapabilityKind]struct{}, len(d.DeclaredCapabilities))
	for _, capability := range d.DeclaredCapabilities {
		if !capability.Valid() {
			return errors.New("MCP descriptor declares an unsupported capability")
		}
		if _, found := seen[capability]; found {
			return errors.New("MCP descriptor repeats a capability")
		}
		seen[capability] = struct{}{}
	}
	return nil
}

func sensitiveClientArgument(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if strings.HasPrefix(normalized, "bearer ") {
		return true
	}
	key := normalized
	if index := strings.IndexByte(key, '='); index >= 0 {
		key = key[:index]
	}
	key = strings.TrimLeft(key, "-/")
	key = strings.ReplaceAll(key, "_", "-")
	switch key {
	case "token", "access-token", "auth-token", "api-token", "api-key",
		"password", "passwd", "secret", "client-secret", "authorization",
		"bearer", "credential", "credentials":
		return true
	default:
		return false
	}
}

func (d ServerDescriptor) Fingerprint() string {
	copyValue := d
	copyValue.Arguments = slices.Clone(d.Arguments)
	copyValue.DeclaredCapabilities = slices.Clone(d.DeclaredCapabilities)
	sort.Slice(copyValue.DeclaredCapabilities, func(i, j int) bool {
		return copyValue.DeclaredCapabilities[i] < copyValue.DeclaredCapabilities[j]
	})
	raw, _ := json.Marshal(copyValue)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

type RemoteTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func (t RemoteTool) Validate() error {
	if !validRemoteName(t.Name) || !validClientText(t.Description, MaxClientDescriptionBytes, true) {
		return errors.New("discovered MCP tool identity is invalid")
	}
	return validateInputSchema(t.InputSchema)
}

type RemoteResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mime_type,omitempty"`
}

func (r RemoteResource) Validate() error {
	if !validClientText(r.URI, 4096, false) || !validClientText(r.Name, 128, false) ||
		!validClientText(r.Description, MaxClientDescriptionBytes, true) ||
		!validClientText(r.MIMEType, 256, true) {
		return errors.New("discovered MCP resource is invalid")
	}
	return nil
}

type RemotePromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type RemoteResourceTemplate struct {
	URITemplate string `json:"uri_template"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mime_type,omitempty"`
}

type RemotePrompt struct {
	Arguments   []RemotePromptArgument `json:"arguments,omitempty"`
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
}

func (p RemotePrompt) Validate() error {
	if len(p.Arguments) > 128 {
		return errors.New("too many MCP prompt arguments")
	}
	seen := map[string]bool{}
	for _, a := range p.Arguments {
		if !validRemoteName(a.Name) || !validClientText(a.Description, MaxClientDescriptionBytes, true) || seen[a.Name] {
			return errors.New("invalid MCP prompt argument")
		}
		seen[a.Name] = true
	}
	if !validRemoteName(p.Name) || !validClientText(p.Description, MaxClientDescriptionBytes, true) {
		return errors.New("discovered MCP prompt is invalid")
	}
	return nil
}

type CapabilitySnapshot struct {
	ResourceTemplates []RemoteResourceTemplate `json:"resource_templates,omitempty"`
	ProtocolVersion   string                   `json:"protocol_version"`
	ServerName        string                   `json:"server_name"`
	ServerVersion     string                   `json:"server_version"`
	Negotiated        []CapabilityKind         `json:"negotiated"`
	Tools             []RemoteTool             `json:"tools"`
	Resources         []RemoteResource         `json:"resources"`
	Prompts           []RemotePrompt           `json:"prompts"`
	Fingerprint       string                   `json:"fingerprint"`
	DiscoveredAt      time.Time                `json:"discovered_at"`
}

func (s CapabilitySnapshot) Validate() error {
	if !supportedClientProtocol(s.ProtocolVersion) || !validClientIdentity(s.ServerName) ||
		!validClientText(s.ServerVersion, 128, false) || len(s.Tools) > MaxClientTools ||
		len(s.Resources) > MaxClientResources || len(s.ResourceTemplates) > MaxClientResources || len(s.Prompts) > MaxClientPrompts ||
		!validClientDigest(s.Fingerprint) || s.DiscoveredAt.IsZero() {
		return errors.New("MCP capability snapshot identity or bounds are invalid")
	}
	if expected := capabilityFingerprint(s); expected != s.Fingerprint {
		return errors.New("MCP capability fingerprint is invalid")
	}
	seenKinds := make(map[CapabilityKind]struct{}, len(s.Negotiated))
	for _, value := range s.Negotiated {
		if !value.Valid() {
			return errors.New("MCP capability snapshot contains an unsupported capability")
		}
		if _, found := seenKinds[value]; found {
			return errors.New("MCP capability snapshot repeats a capability")
		}
		seenKinds[value] = struct{}{}
	}
	seenNames := make(map[string]struct{}, len(s.Tools))
	for _, tool := range s.Tools {
		if err := tool.Validate(); err != nil {
			return err
		}
		if _, found := seenNames[tool.Name]; found {
			return errors.New("MCP capability snapshot repeats a tool name")
		}
		seenNames[tool.Name] = struct{}{}
	}
	for _, resource := range s.Resources {
		if err := resource.Validate(); err != nil {
			return err
		}
	}
	for _, template := range s.ResourceTemplates {
		if err := template.Validate(); err != nil {
			return err
		}
	}
	for _, prompt := range s.Prompts {
		if err := prompt.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func NewCapabilitySnapshot(serverName, serverVersion string, negotiated []CapabilityKind,
	tools []RemoteTool, resources []RemoteResource, prompts []RemotePrompt, at time.Time,
) (CapabilitySnapshot, error) {
	value := CapabilitySnapshot{ProtocolVersion: ProtocolVersion, ServerName: strings.TrimSpace(serverName),
		ServerVersion: strings.TrimSpace(serverVersion), Negotiated: slices.Clone(negotiated),
		Tools: slices.Clone(tools), Resources: slices.Clone(resources), Prompts: slices.Clone(prompts),
		DiscoveredAt: at.UTC()}
	sort.Slice(value.Negotiated, func(i, j int) bool { return value.Negotiated[i] < value.Negotiated[j] })
	sort.Slice(value.Tools, func(i, j int) bool { return value.Tools[i].Name < value.Tools[j].Name })
	sort.Slice(value.Resources, func(i, j int) bool { return value.Resources[i].URI < value.Resources[j].URI })
	sort.Slice(value.Prompts, func(i, j int) bool { return value.Prompts[i].Name < value.Prompts[j].Name })
	value.Fingerprint = capabilityFingerprint(value)
	return value, value.Validate()
}

func capabilityFingerprint(value CapabilitySnapshot) string {
	value.Fingerprint = ""
	value.DiscoveredAt = time.Time{}
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

type ServerRecord struct {
	ProtocolVersion               string             `json:"protocol_version"`
	Descriptor                    ServerDescriptor   `json:"descriptor"`
	DescriptorFingerprint         string             `json:"descriptor_fingerprint"`
	State                         TrustState         `json:"state"`
	Capabilities                  CapabilitySnapshot `json:"capabilities,omitempty"`
	ApprovedCapabilityFingerprint string             `json:"approved_capability_fingerprint,omitempty"`
	Health                        HealthStatus       `json:"health"`
	HealthMessage                 string             `json:"health_message,omitempty"`
	Generation                    int64              `json:"generation"`
	ReviewedBy                    string             `json:"reviewed_by,omitempty"`
	ReviewedAt                    *time.Time         `json:"reviewed_at,omitempty"`
	DiscoveryLeaseID              string             `json:"-"`
	DiscoveryLeaseExpiresAt       *time.Time         `json:"-"`
	CreatedAt                     time.Time          `json:"created_at"`
	UpdatedAt                     time.Time          `json:"updated_at"`
}

func (r ServerRecord) Validate() error {
	if r.ProtocolVersion != ServerRecordProtocolVersion || r.Descriptor.Validate() != nil ||
		r.DescriptorFingerprint != r.Descriptor.Fingerprint() || !r.State.Valid() ||
		!r.Health.Valid() || r.Generation < 1 || r.CreatedAt.IsZero() ||
		r.UpdatedAt.Before(r.CreatedAt) || !validClientText(r.HealthMessage, 2048, true) ||
		!validClientText(r.ReviewedBy, 256, true) {
		return errors.New("MCP server record is invalid")
	}
	hasCapabilities := r.Capabilities.Fingerprint != ""
	if hasCapabilities && r.Capabilities.Validate() != nil {
		return errors.New("MCP server capability snapshot is invalid")
	}
	if r.ApprovedCapabilityFingerprint != "" && !validClientDigest(r.ApprovedCapabilityFingerprint) {
		return errors.New("MCP approved capability fingerprint is invalid")
	}
	if r.State == TrustEnabled && (!hasCapabilities ||
		r.ApprovedCapabilityFingerprint != r.Capabilities.Fingerprint) {
		return errors.New("enabled MCP server must pin its current capability fingerprint")
	}
	if r.State == TrustStaged && (r.ReviewedAt != nil || r.ReviewedBy != "") {
		return errors.New("staged MCP server cannot carry a review")
	}
	if r.ReviewedAt != nil && (r.ReviewedBy == "" || r.ReviewedAt.Before(r.CreatedAt)) {
		return errors.New("MCP server review metadata is invalid")
	}
	if r.Health == HealthConnecting {
		if !validClientIdentity(r.DiscoveryLeaseID) || r.DiscoveryLeaseExpiresAt == nil ||
			!r.DiscoveryLeaseExpiresAt.After(r.UpdatedAt) {
			return errors.New("connecting MCP server requires a valid discovery lease")
		}
	} else if r.DiscoveryLeaseID != "" || r.DiscoveryLeaseExpiresAt != nil {
		return errors.New("idle MCP server cannot retain a discovery lease")
	}
	return nil
}

type ReviewAction string

const (
	ReviewApproveDiscovery   ReviewAction = "approve_discovery"
	ReviewEnableCapabilities ReviewAction = "enable_capabilities"
	ReviewDisable            ReviewAction = "disable"
	ReviewRevoke             ReviewAction = "revoke"
)

type ReviewRequest struct {
	Action                        ReviewAction `json:"action"`
	ExpectedDescriptorFingerprint string       `json:"expected_descriptor_fingerprint"`
	ExpectedCapabilityFingerprint string       `json:"expected_capability_fingerprint,omitempty"`
	ReviewedBy                    string       `json:"reviewed_by"`
}

type CallAudit struct {
	ProtocolVersion       string    `json:"protocol_version"`
	ID                    string    `json:"id"`
	RunID                 string    `json:"run_id"`
	WorkspaceID           string    `json:"workspace_id"`
	ServerID              string    `json:"server_id"`
	ToolName              string    `json:"tool_name"`
	CapabilityFingerprint string    `json:"capability_fingerprint"`
	ArgumentsSHA256       string    `json:"arguments_sha256"`
	Status                string    `json:"status"`
	ErrorCode             string    `json:"error_code,omitempty"`
	ResultBytes           int       `json:"result_bytes"`
	Truncated             bool      `json:"truncated"`
	StartedAt             time.Time `json:"started_at"`
	CompletedAt           time.Time `json:"completed_at"`
}

func (a CallAudit) Validate() error {
	if a.ProtocolVersion != CallAuditProtocolVersion || !validClientIdentity(a.ID) ||
		!validClientIdentity(a.RunID) || !validClientIdentity(a.WorkspaceID) ||
		!validClientIdentity(a.ServerID) || !validRemoteName(a.ToolName) ||
		!validClientDigest(a.CapabilityFingerprint) || !validClientDigest(a.ArgumentsSHA256) ||
		(a.Status != "completed" && a.Status != "denied" && a.Status != "failed" &&
			a.Status != "cancelled" && a.Status != "timed_out") ||
		!validClientText(a.ErrorCode, 128, true) || a.ResultBytes < 0 ||
		a.ResultBytes > MaxClientResultBytes || a.StartedAt.IsZero() ||
		a.CompletedAt.Before(a.StartedAt) {
		return errors.New("MCP call audit is invalid")
	}
	return nil
}

type ClientStore interface {
	CreateMCPClientServer(context.Context, ServerRecord) (ServerRecord, bool, error)
	GetMCPClientServer(context.Context, string) (ServerRecord, error)
	ListMCPClientServers(context.Context, string, string, int) ([]ServerRecord, error)
	ListRecoverableMCPClientServers(context.Context, int) ([]ServerRecord, error)
	UpdateMCPClientServer(context.Context, ServerRecord, int64) (ServerRecord, error)
	RecordMCPClientCall(context.Context, CallAudit) error
	ListMCPClientCalls(context.Context, string, int) ([]CallAudit, error)
}

func validateInputSchema(raw json.RawMessage) error {
	if len(raw) < 2 || len(raw) > 64*1024 || !utf8.Valid(raw) {
		return errors.New("MCP tool input schema must be bounded UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return errors.New("MCP tool input schema must be a JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("MCP tool input schema contains trailing JSON")
	}
	if schemaType, ok := value["type"].(string); ok && schemaType != "object" {
		return errors.New("MCP tool input schema root must accept an object")
	}
	if err := validateInputSchemaNode(value, 0); err != nil {
		return err
	}
	return nil
}

func validateInputSchemaNode(value any, depth int) error {
	if depth > 64 {
		return errors.New("MCP tool input schema exceeds its nesting limit")
	}
	switch current := value.(type) {
	case map[string]any:
		for key, item := range current {
			if strings.HasPrefix(key, "$dynamic") {
				return errors.New("MCP tool input schema uses unsupported dynamic references")
			}
			if key == "$ref" || key == "$recursiveRef" {
				reference, ok := item.(string)
				if !ok || !strings.HasPrefix(reference, "#") {
					return errors.New("external MCP schema resources are forbidden")
				}
			}
			if err := validateInputSchemaNode(item, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range current {
			if err := validateInputSchemaNode(item, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func validClientIdentity(value string) bool {
	return validClientText(value, 256, false) && !strings.ContainsAny(value, "/\\")
}

func validRemoteName(value string) bool {
	if !validClientText(value, 256, false) {
		return false
	}
	for _, current := range value {
		if unicode.IsSpace(current) || unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func validClientText(value string, maxBytes int, allowEmpty bool) bool {
	if maxBytes < 1 || !utf8.ValidString(value) || len([]byte(value)) > maxBytes ||
		strings.ContainsRune(value, 0) || value != strings.TrimSpace(value) {
		return false
	}
	if !allowEmpty && value == "" {
		return false
	}
	for _, current := range value {
		if current != '\t' && current != '\n' && current != '\r' && unicode.IsControl(current) {
			return false
		}
	}
	return true
}

func truncateClientUTF8(value string, maxBytes int) string {
	if maxBytes < 1 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

func validClientDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validCredentialReference(value string) bool {
	if value == "" || len(value) > 64 || value != strings.TrimSpace(value) {
		return false
	}
	for _, current := range value {
		if !(unicode.IsLetter(current) || unicode.IsDigit(current) || current == '-' || current == '_') {
			return false
		}
	}
	return true
}
