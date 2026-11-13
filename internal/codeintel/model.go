package codeintel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/redact"
)

const (
	ProtocolVersion       = "code-intel-lsp.v1"
	ConfigProtocolVersion = "code-intel-config.v1"

	MaxServers                 = 32
	MaxLanguagesPerServer      = 16
	MaxExtensionsPerLanguage   = 32
	MaxArguments               = 32
	MaxInitializationBytes     = 64 * 1024
	MaxMessageBytes            = 4 * 1024 * 1024
	MaxResultBytes             = 256 * 1024
	MaxLogBytes                = 32 * 1024
	MaxResultItems             = 200
	MaxDiagnostics             = 200
	MaxOpenDocuments           = 64
	MaxHierarchyRoots          = 16
	MaxHierarchyEdges          = 400
	MaxMarkdownBytes           = 32 * 1024
	MaxLinks                   = 32
	DefaultRequestTimeout      = 15 * time.Second
	MinimumRequestTimeout      = 250 * time.Millisecond
	MaximumRequestTimeout      = 60 * time.Second
	MaximumShutdownGracePeriod = 3 * time.Second
)

type HealthStatus string

const (
	HealthConfigured  HealthStatus = "configured"
	HealthStarting    HealthStatus = "starting"
	HealthHealthy     HealthStatus = "healthy"
	HealthUnavailable HealthStatus = "unavailable"
	HealthCrashed     HealthStatus = "crashed"
	HealthTimedOut    HealthStatus = "timed_out"
	HealthProtocolErr HealthStatus = "protocol_error"
	HealthStopped     HealthStatus = "stopped"
)

func (s HealthStatus) Valid() bool {
	switch s {
	case HealthConfigured, HealthStarting, HealthHealthy, HealthUnavailable,
		HealthCrashed, HealthTimedOut, HealthProtocolErr, HealthStopped:
		return true
	default:
		return false
	}
}

type EvidenceState string

const (
	EvidenceCurrent     EvidenceState = "current"
	EvidenceStale       EvidenceState = "stale"
	EvidencePartial     EvidenceState = "partial"
	EvidenceUnavailable EvidenceState = "unavailable"
)

func (s EvidenceState) Valid() bool {
	return s == EvidenceCurrent || s == EvidenceStale || s == EvidencePartial ||
		s == EvidenceUnavailable
}

type Language struct {
	ID         string   `json:"id"`
	Extensions []string `json:"extensions"`
}

func (l Language) Validate() error {
	if !validIdentity(l.ID) || len(l.Extensions) == 0 ||
		len(l.Extensions) > MaxExtensionsPerLanguage || !redactionInvariant(l.ID) {
		return errors.New("LSP language identity or extension list is invalid")
	}
	seen := make(map[string]struct{}, len(l.Extensions))
	for _, extension := range l.Extensions {
		if extension == "" || extension != strings.ToLower(extension) ||
			!strings.HasPrefix(extension, ".") || len(extension) > 32 ||
			strings.ContainsAny(extension, `/\\:`) {
			return errors.New("LSP language extension must be a lower-case file suffix")
		}
		if _, exists := seen[extension]; exists {
			return errors.New("LSP language repeats a file extension")
		}
		seen[extension] = struct{}{}
	}
	return nil
}

type Source struct {
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	SHA256 string `json:"sha256"`
}

func (s Source) Validate() error {
	if s.Kind != "operator_config" || !validDisplayText(s.Label, 256, false) ||
		!redactionInvariant(s.Label) || !validDigest(s.SHA256) {
		return errors.New("LSP source must identify a bounded operator configuration")
	}
	return nil
}

// ServerDescriptor is accepted only from process-owned configuration. It is
// never discovered from a Workspace, language manifest, editor setting, or
// model response.
type ServerDescriptor struct {
	ProtocolVersion       string          `json:"protocol_version"`
	ID                    string          `json:"id"`
	Name                  string          `json:"name"`
	WorkspaceID           string          `json:"workspace_id"`
	Languages             []Language      `json:"languages"`
	Executable            string          `json:"executable"`
	Arguments             []string        `json:"arguments"`
	ExecutableSHA256      string          `json:"executable_sha256"`
	InitializationOptions json.RawMessage `json:"initialization_options,omitempty"`
	RequestTimeoutMillis  int64           `json:"request_timeout_ms"`
	ReviewedBy            string          `json:"reviewed_by"`
	ReviewedAt            time.Time       `json:"reviewed_at"`
	Source                Source          `json:"source"`
}

func (d ServerDescriptor) Validate() error {
	if d.ProtocolVersion != ProtocolVersion || !validIdentity(d.ID) ||
		!validDisplayText(d.Name, 256, false) || !validIdentity(d.WorkspaceID) ||
		len(d.Languages) == 0 || len(d.Languages) > MaxLanguagesPerServer ||
		len(d.Arguments) > MaxArguments || !filepath.IsAbs(d.Executable) ||
		!validDisplayText(d.Executable, 4096, false) || !validDigest(d.ExecutableSHA256) ||
		!validDisplayText(d.ReviewedBy, 256, false) || d.ReviewedAt.IsZero() {
		return errors.New("LSP server descriptor identity, review, or executable binding is invalid")
	}
	if !redactionInvariant(d.ID) || !redactionInvariant(d.Name) ||
		!redactionInvariant(d.WorkspaceID) || !redactionInvariant(d.ReviewedBy) {
		return errors.New("LSP server descriptor contains secret-shaped public metadata")
	}
	if err := d.Source.Validate(); err != nil {
		return err
	}
	timeout := time.Duration(d.RequestTimeoutMillis) * time.Millisecond
	if timeout < MinimumRequestTimeout || timeout > MaximumRequestTimeout {
		return errors.New("LSP request timeout is outside the supported bounds")
	}
	seenLanguages := make(map[string]struct{}, len(d.Languages))
	seenExtensions := make(map[string]struct{})
	for _, language := range d.Languages {
		if err := language.Validate(); err != nil {
			return err
		}
		if _, exists := seenLanguages[language.ID]; exists {
			return errors.New("LSP descriptor repeats a language identity")
		}
		seenLanguages[language.ID] = struct{}{}
		for _, extension := range language.Extensions {
			if _, exists := seenExtensions[extension]; exists {
				return errors.New("LSP descriptor maps one extension to multiple languages")
			}
			seenExtensions[extension] = struct{}{}
		}
	}
	for _, argument := range d.Arguments {
		if !validDisplayText(argument, 2048, true) || secretShapedArgument(argument) ||
			!redactionInvariant(argument) {
			return errors.New("LSP server argument is invalid or secret-shaped")
		}
	}
	if len(d.InitializationOptions) != 0 {
		if len(d.InitializationOptions) > MaxInitializationBytes ||
			!utf8.Valid(d.InitializationOptions) || !json.Valid(d.InitializationOptions) {
			return errors.New("LSP initialization options must be bounded UTF-8 JSON")
		}
		var value any
		if err := json.Unmarshal(d.InitializationOptions, &value); err != nil || value == nil ||
			initializationValueContainsSecret(value, 0) {
			return errors.New("LSP initialization options are invalid")
		}
	}
	return nil
}

func (d ServerDescriptor) Fingerprint() string {
	copyValue := d
	copyValue.Languages = append([]Language(nil), d.Languages...)
	for index := range copyValue.Languages {
		copyValue.Languages[index].Extensions = append([]string(nil),
			copyValue.Languages[index].Extensions...)
		sort.Strings(copyValue.Languages[index].Extensions)
	}
	sort.Slice(copyValue.Languages, func(i, j int) bool {
		return copyValue.Languages[i].ID < copyValue.Languages[j].ID
	})
	copyValue.Arguments = append([]string(nil), d.Arguments...)
	raw, _ := json.Marshal(copyValue)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (d ServerDescriptor) LanguageForPath(path string) (string, bool) {
	extension := strings.ToLower(filepath.Ext(path))
	trimmed := strings.TrimRightFunc(extension, unicode.IsSpace)
	var paddedLanguage string
	for _, language := range d.Languages {
		for _, candidate := range language.Extensions {
			if extension == candidate {
				return language.ID, true
			}
			// Match an ordinary language suffix on a padded file name without
			// changing the document path or overriding an explicit suffix.
			if paddedLanguage == "" && trimmed == candidate {
				paddedLanguage = language.ID
			}
		}
	}
	return paddedLanguage, paddedLanguage != ""
}

type Capabilities struct {
	WorkspaceSymbols bool `json:"workspace_symbols"`
	DocumentSymbols  bool `json:"document_symbols"`
	Definition       bool `json:"definition"`
	References       bool `json:"references"`
	Implementation   bool `json:"implementation"`
	Hover            bool `json:"hover"`
	SignatureHelp    bool `json:"signature_help"`
	Diagnostics      bool `json:"diagnostics"`
	CallHierarchy    bool `json:"call_hierarchy"`
	TypeHierarchy    bool `json:"type_hierarchy"`
}

func (c Capabilities) Any() bool {
	return c.WorkspaceSymbols || c.DocumentSymbols || c.Definition || c.References ||
		c.Implementation || c.Hover || c.SignatureHelp || c.Diagnostics ||
		c.CallHierarchy || c.TypeHierarchy
}

func (c Capabilities) ToolNames() []string {
	type pair struct {
		name    string
		enabled bool
	}
	pairs := []pair{{"code_workspace_symbols", c.WorkspaceSymbols},
		{"code_document_symbols", c.DocumentSymbols}, {"code_definition", c.Definition},
		{"code_references", c.References}, {"code_implementation", c.Implementation},
		{"code_hover", c.Hover}, {"code_signature_help", c.SignatureHelp},
		{"code_diagnostics", c.Diagnostics}, {"code_call_hierarchy", c.CallHierarchy},
		{"code_type_hierarchy", c.TypeHierarchy}}
	result := make([]string, 0, len(pairs))
	for _, item := range pairs {
		if item.enabled {
			result = append(result, item.name)
		}
	}
	return result
}

type CapabilitySnapshot struct {
	ProtocolVersion       string       `json:"protocol_version"`
	ServerID              string       `json:"server_id"`
	ServerName            string       `json:"server_name"`
	WorkspaceID           string       `json:"workspace_id"`
	Languages             []string     `json:"languages"`
	Source                Source       `json:"source"`
	DescriptorFingerprint string       `json:"descriptor_fingerprint"`
	CapabilityFingerprint string       `json:"capability_fingerprint,omitempty"`
	Generation            string       `json:"generation,omitempty"`
	Health                HealthStatus `json:"health"`
	Capabilities          Capabilities `json:"capabilities"`
	ModelVisibleTools     []string     `json:"model_visible_tools"`
	ServerVersion         string       `json:"server_version,omitempty"`
	LastError             string       `json:"last_error,omitempty"`
	ProcessOwned          bool         `json:"process_owned"`
	ReadOnly              bool         `json:"read_only"`
	NetworkAccessGranted  bool         `json:"network_access_granted"`
	CredentialsGranted    bool         `json:"credentials_granted"`
	ShellProfileLoaded    bool         `json:"shell_profile_loaded"`
	QualifiedAt           *time.Time   `json:"qualified_at,omitempty"`
}

func (s CapabilitySnapshot) Validate() error {
	if s.ProtocolVersion != ProtocolVersion || !validIdentity(s.ServerID) ||
		!validDisplayText(s.ServerName, 256, false) || !validIdentity(s.WorkspaceID) ||
		!validDigest(s.DescriptorFingerprint) || !s.Health.Valid() ||
		!validDisplayText(s.ServerVersion, 256, true) ||
		!validDisplayText(s.LastError, 2048, true) || !s.ProcessOwned || !s.ReadOnly ||
		s.NetworkAccessGranted || s.CredentialsGranted || s.ShellProfileLoaded {
		return errors.New("LSP capability snapshot is invalid")
	}
	if !redactionInvariant(s.ServerID) || !redactionInvariant(s.ServerName) ||
		!redactionInvariant(s.WorkspaceID) || !redactionInvariant(s.ServerVersion) ||
		!redactionInvariant(s.LastError) || len(s.Languages) == 0 ||
		len(s.Languages) > MaxLanguagesPerServer {
		return errors.New("LSP capability snapshot contains unsafe public metadata")
	}
	previousLanguage := ""
	for _, language := range s.Languages {
		if !validIdentity(language) || !redactionInvariant(language) ||
			(previousLanguage != "" && language <= previousLanguage) {
			return errors.New("LSP capability snapshot languages are invalid")
		}
		previousLanguage = language
	}
	if err := s.Source.Validate(); err != nil {
		return err
	}
	if (s.CapabilityFingerprint != "" && !validDigest(s.CapabilityFingerprint)) ||
		(s.Generation != "" && !validDigest(s.Generation)) ||
		(s.QualifiedAt != nil && s.QualifiedAt.IsZero()) {
		return errors.New("LSP capability snapshot carries invalid optional provenance")
	}
	if s.Capabilities.Any() && (!validDigest(s.CapabilityFingerprint) ||
		!validDigest(s.Generation) || s.QualifiedAt == nil) {
		return errors.New("negotiated LSP capabilities lack complete provenance")
	}
	if s.Health == HealthHealthy {
		if !validDigest(s.CapabilityFingerprint) || !validDigest(s.Generation) ||
			!s.Capabilities.Any() || s.QualifiedAt == nil || s.QualifiedAt.IsZero() {
			return errors.New("healthy LSP capability snapshot lacks negotiated provenance")
		}
	}
	expectedTools := s.Capabilities.ToolNames()
	if strings.Join(expectedTools, "\x00") != strings.Join(s.ModelVisibleTools, "\x00") {
		return errors.New("LSP model-visible tools do not match negotiated capabilities")
	}
	return nil
}

func capabilityFingerprint(serverName, serverVersion string, capabilities Capabilities) string {
	raw, _ := json.Marshal(struct {
		Protocol     string       `json:"protocol"`
		Server       string       `json:"server"`
		Version      string       `json:"version"`
		Capabilities Capabilities `json:"capabilities"`
	}{Protocol: ProtocolVersion, Server: serverName, Version: serverVersion,
		Capabilities: capabilities})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

func (p Position) Validate() error {
	if p.Line < 0 || p.Line > 10_000_000 || p.Character < 0 || p.Character > 10_000_000 {
		return errors.New("LSP position is outside supported bounds")
	}
	return nil
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

func (r Range) Validate() error {
	if r.Start.Validate() != nil || r.End.Validate() != nil || r.End.Line < r.Start.Line ||
		(r.End.Line == r.Start.Line && r.End.Character < r.Start.Character) {
		return errors.New("LSP range is invalid")
	}
	return nil
}

type Provenance struct {
	ProtocolVersion       string `json:"protocol_version"`
	WorkspaceID           string `json:"workspace_id"`
	RootFingerprint       string `json:"root_fingerprint"`
	RepositoryAvailable   bool   `json:"repository_available"`
	Commit                string `json:"commit,omitempty"`
	Branch                string `json:"branch,omitempty"`
	Dirty                 bool   `json:"dirty"`
	DirtyDigest           string `json:"dirty_digest"`
	DocumentURI           string `json:"document_uri,omitempty"`
	DocumentPath          string `json:"document_path,omitempty"`
	DocumentSHA256        string `json:"document_sha256,omitempty"`
	DocumentVersion       int    `json:"document_version,omitempty"`
	ServerID              string `json:"server_id"`
	ServerGeneration      string `json:"server_generation"`
	CapabilityFingerprint string `json:"capability_fingerprint"`
	QueryFingerprint      string `json:"query_fingerprint"`
}

func (p Provenance) Validate() error {
	if p.ProtocolVersion != ProtocolVersion || !validIdentity(p.WorkspaceID) ||
		!validDigest(p.RootFingerprint) || !validDigest(p.DirtyDigest) ||
		!validIdentity(p.ServerID) || !validDigest(p.ServerGeneration) ||
		!validDigest(p.CapabilityFingerprint) || !validDigest(p.QueryFingerprint) {
		return errors.New("LSP evidence provenance identity is invalid")
	}
	if !redactionInvariant(p.WorkspaceID) || !redactionInvariant(p.ServerID) ||
		!validDisplayText(p.Branch, 255, true) || !redactionInvariant(p.Branch) {
		return errors.New("LSP evidence provenance contains unsafe public metadata")
	}
	if !p.RepositoryAvailable && (p.Commit != "" || p.Branch != "" || p.Dirty) {
		return errors.New("LSP unavailable repository provenance carries Git state")
	}
	if p.RepositoryAvailable && p.Commit != "" && !validGitObjectID(p.Commit) {
		return errors.New("LSP evidence commit binding is invalid")
	}
	if p.DocumentPath != "" {
		if !validRelativeEvidencePath(p.DocumentPath) || p.DocumentURI == "" ||
			!validDisplayText(p.DocumentURI, 8192, false) || !redactionInvariant(p.DocumentURI) ||
			!validDigest(p.DocumentSHA256) || p.DocumentVersion <= 0 {
			return errors.New("LSP document provenance is incomplete")
		}
	} else if p.DocumentURI != "" || p.DocumentSHA256 != "" || p.DocumentVersion != 0 {
		return errors.New("LSP non-document provenance carries document state")
	}
	return nil
}

type EvidenceItem struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	Name         string            `json:"name,omitempty"`
	Detail       string            `json:"detail,omitempty"`
	Path         string            `json:"path,omitempty"`
	Range        *Range            `json:"range,omitempty"`
	Selection    *Range            `json:"selection_range,omitempty"`
	Severity     int               `json:"severity,omitempty"`
	Code         string            `json:"code,omitempty"`
	Relationship string            `json:"relationship,omitempty"`
	Tags         []string          `json:"tags,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

type GraphEdge struct {
	From         string `json:"from"`
	To           string `json:"to"`
	Relationship string `json:"relationship"`
}

type Page struct {
	Limit      int    `json:"limit"`
	Returned   int    `json:"returned"`
	Total      int    `json:"total"`
	NextCursor string `json:"next_cursor,omitempty"`
	Truncated  bool   `json:"truncated"`
}

type Result struct {
	ProtocolVersion string         `json:"protocol_version"`
	Tool            string         `json:"tool"`
	State           EvidenceState  `json:"state"`
	EvidenceLevel   string         `json:"evidence_level"`
	Provenance      Provenance     `json:"provenance"`
	Items           []EvidenceItem `json:"items"`
	Edges           []GraphEdge    `json:"edges,omitempty"`
	Content         string         `json:"content,omitempty"`
	Page            Page           `json:"page"`
	Warnings        []string       `json:"warnings,omitempty"`
}

func (r Result) Validate() error {
	if r.ProtocolVersion != ProtocolVersion || !validTool(r.Tool) || !r.State.Valid() ||
		r.EvidenceLevel != "semantic_language_server" || r.Provenance.Validate() != nil ||
		len(r.Items) > MaxResultItems || len(r.Edges) > MaxHierarchyEdges ||
		len(r.Content) > MaxMarkdownBytes || !validMarkdownText(r.Content, MaxMarkdownBytes) ||
		r.Page.Limit < 1 ||
		r.Page.Limit > MaxResultItems || r.Page.Returned != len(r.Items) ||
		r.Page.Total < r.Page.Returned || r.Page.Total > MaxResultItems ||
		len(r.Warnings) > 16 || !redactionInvariant(r.Content) ||
		(r.Page.Truncated != (r.Page.NextCursor != "")) ||
		!validDisplayText(r.Page.NextCursor, 8192, true) {
		return errors.New("LSP semantic result is invalid")
	}
	itemIDs := make(map[string]struct{}, len(r.Items))
	for _, item := range r.Items {
		if err := item.validate(); err != nil {
			return err
		}
		if item.ID != evidenceItemID(item) {
			return errors.New("LSP evidence item identity does not match its content")
		}
		if _, exists := itemIDs[item.ID]; exists {
			return errors.New("LSP semantic result repeats an evidence item")
		}
		itemIDs[item.ID] = struct{}{}
	}
	for _, edge := range r.Edges {
		if !validDigest(edge.From) || !validDigest(edge.To) ||
			!validDisplayText(edge.Relationship, 64, false) ||
			!redactionInvariant(edge.Relationship) {
			return errors.New("LSP semantic graph edge is invalid")
		}
		if _, found := itemIDs[edge.From]; !found {
			return errors.New("LSP semantic graph edge source is absent from its page")
		}
		if _, found := itemIDs[edge.To]; !found {
			return errors.New("LSP semantic graph edge target is absent from its page")
		}
	}
	for _, warning := range r.Warnings {
		if !validDisplayText(warning, 512, false) || !redactionInvariant(warning) {
			return errors.New("LSP semantic warning is invalid")
		}
	}
	if r.State == EvidenceCurrent && (len(r.Warnings) != 0 || r.Page.Truncated) {
		return errors.New("current LSP evidence cannot be truncated or carry warnings")
	}
	return nil
}

type Qualification struct {
	ProtocolVersion       string       `json:"protocol_version"`
	ServerID              string       `json:"server_id"`
	WorkspaceID           string       `json:"workspace_id"`
	Eligible              bool         `json:"eligible"`
	Health                HealthStatus `json:"health"`
	DescriptorFingerprint string       `json:"descriptor_fingerprint"`
	ExecutableHashMatched bool         `json:"executable_hash_matched"`
	Reviewed              bool         `json:"reviewed"`
	ProcessOwned          bool         `json:"process_owned"`
	MinimalEnvironment    bool         `json:"minimal_environment"`
	NetworkAccessGranted  bool         `json:"network_access_granted"`
	CredentialsGranted    bool         `json:"credentials_granted"`
	ShellProfileLoaded    bool         `json:"shell_profile_loaded"`
	Reason                string       `json:"reason,omitempty"`
}

func (q Qualification) Validate() error {
	if q.ProtocolVersion != ProtocolVersion || !validIdentity(q.ServerID) ||
		!validIdentity(q.WorkspaceID) || !redactionInvariant(q.ServerID) ||
		!redactionInvariant(q.WorkspaceID) || !q.Health.Valid() ||
		!validDigest(q.DescriptorFingerprint) || !q.ProcessOwned ||
		!q.MinimalEnvironment || q.NetworkAccessGranted || q.CredentialsGranted ||
		q.ShellProfileLoaded || !validDisplayText(q.Reason, 2048, true) ||
		!redactionInvariant(q.Reason) {
		return errors.New("LSP qualification is invalid")
	}
	if q.Eligible && (!q.ExecutableHashMatched || !q.Reviewed ||
		q.Health != HealthConfigured || q.Reason != "") {
		return errors.New("eligible LSP qualification lacks reviewed hash evidence")
	}
	return nil
}

func (item EvidenceItem) validate() error {
	if !validDigest(item.ID) || !validDisplayText(item.Kind, 64, false) ||
		!redactionInvariant(item.Kind) || !validDisplayText(item.Name, 4096, true) ||
		!redactionInvariant(item.Name) || !validDisplayText(item.Detail, MaxMarkdownBytes, true) ||
		!redactionInvariant(item.Detail) || !validRelativeEvidencePath(item.Path) ||
		item.Severity < 0 || item.Severity > 4 ||
		!validDisplayText(item.Code, 256, true) || !redactionInvariant(item.Code) ||
		!validDisplayText(item.Relationship, 64, true) ||
		!redactionInvariant(item.Relationship) || len(item.Tags) > 8 || len(item.Metadata) > 8 {
		return errors.New("LSP evidence item is invalid")
	}
	if item.Range != nil && item.Range.Validate() != nil {
		return errors.New("LSP evidence item range is invalid")
	}
	if item.Selection != nil && item.Selection.Validate() != nil {
		return errors.New("LSP evidence item selection range is invalid")
	}
	for _, tag := range item.Tags {
		if !validDisplayText(tag, 64, false) || !redactionInvariant(tag) {
			return errors.New("LSP evidence item tag is invalid")
		}
	}
	for key, value := range item.Metadata {
		if !validDisplayText(key, 64, false) || !redactionInvariant(key) ||
			!validDisplayText(value, 1024, true) || !redactionInvariant(value) {
			return errors.New("LSP evidence item metadata is invalid")
		}
	}
	return nil
}

func validRelativeEvidencePath(value string) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 512 ||
		strings.ContainsFunc(value, unicode.IsControl) ||
		!redactionInvariant(value) || filepath.IsAbs(value) || strings.ContainsAny(value, `\\:`) {
		return false
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return cleaned == value && cleaned != "." && cleaned != ".." &&
		!strings.HasPrefix(cleaned, "../")
}

func validIdentity(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > 256 || strings.ContainsRune(value, 0) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return false
		}
	}
	return true
}

func validDisplayText(value string, maxRunes int, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || value != strings.TrimSpace(value) ||
		!utf8.ValidString(value) || utf8.RuneCountInString(value) > maxRunes ||
		strings.ContainsRune(value, 0) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) && current != '\t' {
			return false
		}
	}
	return true
}

func validMarkdownText(value string, maxRunes int) bool {
	if value != strings.TrimSpace(value) || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > maxRunes || strings.ContainsRune(value, 0) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) && current != '\t' && current != '\r' && current != '\n' {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func secretShapedArgument(value string) bool {
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
	case "token", "access-token", "auth-token", "api-token", "api-key", "password",
		"passwd", "secret", "client-secret", "authorization", "credential", "credentials":
		return true
	default:
		return false
	}
}

func redactionInvariant(value string) bool {
	return redact.String(value) == value
}

func initializationValueContainsSecret(value any, depth int) bool {
	if depth > 32 {
		return true
	}
	switch current := value.(type) {
	case map[string]any:
		for key, nested := range current {
			if !validDisplayText(key, 256, false) || secretShapedJSONField(key) ||
				!redactionInvariant(key) || initializationValueContainsSecret(nested, depth+1) {
				return true
			}
		}
	case []any:
		for _, nested := range current {
			if initializationValueContainsSecret(nested, depth+1) {
				return true
			}
		}
	case string:
		return !validDisplayText(current, 8192, true) || !redactionInvariant(current)
	case nil, bool, float64:
		return false
	default:
		return true
	}
	return false
}

func secretShapedJSONField(value string) bool {
	normalized := strings.ToLower(value)
	normalized = strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(normalized)
	switch normalized {
	case "token", "accesstoken", "authtoken", "apitoken", "apikey", "password",
		"passwd", "secret", "clientsecret", "authorization", "credential",
		"credentials", "cookie", "setcookie", "privatekey":
		return true
	default:
		return false
	}
}

func digestStrings(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hash, "%d:", len(part))
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{'|'})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
