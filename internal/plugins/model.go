// Package plugins implements inert, review-gated plugin installation. Legacy
// plugin.v1 archives reject executable assets. Portable source snapshots retain
// original scripts and binary resources as data; acquisition never runs them.
package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cyberagent-workbench/internal/hooks"
	"cyberagent-workbench/internal/mcp"
)

const (
	ProtocolVersion              = "plugin.v1"
	SignatureProtocolVersion     = "plugin-signature.v1"
	InstallationProtocol         = "plugin-installation.v1"
	PortableInstallationProtocol = "plugin-installation.v2"
	PublisherTrustProtocol       = "plugin-publisher-trust.v1"
	ManifestPath                 = "plugin.json"
	SignaturePath                = "SIGNATURE.json"

	MaxArchiveBytes      = 4 * 1024 * 1024
	MaxUncompressedBytes = 8 * 1024 * 1024
	MaxManifestBytes     = 256 * 1024
	MaxSignatureBytes    = 8 * 1024
	MaxEntries           = 128
)

type Capability string

const (
	CapabilitySkills Capability = "skills"
	CapabilityMCP    Capability = "mcp_servers"
	CapabilityUI     Capability = "ui_metadata"
	CapabilityHooks  Capability = "hooks"
)

func (c Capability) Valid() bool {
	return c == CapabilitySkills || c == CapabilityMCP || c == CapabilityUI || c == CapabilityHooks
}

type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

type SkillContribution struct {
	Name         string `json:"name"`
	ManifestPath string `json:"manifest_path"`
	ContentPath  string `json:"content_path"`
}

type MCPContribution struct {
	ID                   string               `json:"id"`
	Name                 string               `json:"name"`
	Transport            mcp.TransportKind    `json:"transport"`
	Target               string               `json:"target"`
	Arguments            []string             `json:"arguments,omitempty"`
	CredentialRef        string               `json:"credential_ref,omitempty"`
	DeclaredCapabilities []mcp.CapabilityKind `json:"declared_capabilities"`
	CallTimeoutMillis    int64                `json:"call_timeout_ms"`
	MaxResultBytes       int                  `json:"max_result_bytes"`
}

type UIMetadata struct {
	DisplayName string `json:"display_name"`
	Description string `json:"description,omitempty"`
	IconPath    string `json:"icon_path,omitempty"`
}

type Manifest struct {
	ProtocolVersion string              `json:"protocol_version"`
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	Version         string              `json:"version"`
	Publisher       string              `json:"publisher"`
	Description     string              `json:"description"`
	Capabilities    []Capability        `json:"capabilities"`
	Files           []FileEntry         `json:"files"`
	Skills          []SkillContribution `json:"skills,omitempty"`
	MCPServers      []MCPContribution   `json:"mcp_servers,omitempty"`
	UI              *UIMetadata         `json:"ui_metadata,omitempty"`
	Hooks           []hooks.Declaration `json:"hooks,omitempty"`
}

func (m Manifest) Validate() error {
	if m.ProtocolVersion != ProtocolVersion || !validIdentity(m.ID) ||
		!validText(m.Name, 256, false) || !validVersion(m.Version) ||
		!validText(m.Publisher, 256, false) || !validText(m.Description, 4096, false) ||
		len(m.Capabilities) == 0 || len(m.Capabilities) > 4 || len(m.Files) > MaxEntries ||
		len(m.Skills) > 32 || len(m.MCPServers) > 32 || len(m.Hooks) > hooks.MaxDeclarations {
		return errors.New("plugin manifest identity, version, or bounds are invalid")
	}
	capabilities := make(map[Capability]struct{}, len(m.Capabilities))
	for _, capability := range m.Capabilities {
		if !capability.Valid() {
			return errors.New("plugin manifest declares an unsupported capability")
		}
		if _, found := capabilities[capability]; found {
			return errors.New("plugin manifest repeats a capability")
		}
		capabilities[capability] = struct{}{}
	}
	expected := map[Capability]bool{CapabilitySkills: len(m.Skills) > 0,
		CapabilityMCP: len(m.MCPServers) > 0, CapabilityUI: m.UI != nil,
		CapabilityHooks: len(m.Hooks) > 0}
	for capability, present := range expected {
		_, declared := capabilities[capability]
		if present != declared {
			return fmt.Errorf("plugin capability %s does not match its declarations", capability)
		}
	}
	files := make(map[string]FileEntry, len(m.Files))
	for _, file := range m.Files {
		if !validPackagePath(file.Path) || !allowedPackagePath(file.Path) ||
			!validDigest(file.SHA256) || file.Bytes < 0 || file.Bytes > MaxUncompressedBytes {
			return errors.New("plugin file manifest contains an invalid entry")
		}
		if _, found := files[file.Path]; found {
			return errors.New("plugin file manifest repeats a path")
		}
		files[file.Path] = file
	}
	seenSkills := make(map[string]struct{}, len(m.Skills))
	for _, skill := range m.Skills {
		if !validIdentity(skill.Name) || !validPackagePath(skill.ManifestPath) ||
			!validPackagePath(skill.ContentPath) ||
			!strings.HasPrefix(skill.ManifestPath, "skills/"+skill.Name+"/") ||
			!strings.HasPrefix(skill.ContentPath, "skills/"+skill.Name+"/") {
			return errors.New("plugin Skill contribution is invalid")
		}
		if _, manifestFound := files[skill.ManifestPath]; !manifestFound {
			return errors.New("plugin Skill manifest is absent from the file list")
		}
		if _, contentFound := files[skill.ContentPath]; !contentFound {
			return errors.New("plugin Skill content is absent from the file list")
		}
		if _, found := seenSkills[skill.Name]; found {
			return errors.New("plugin repeats a Skill contribution")
		}
		seenSkills[skill.Name] = struct{}{}
	}
	seenServers := make(map[string]struct{}, len(m.MCPServers))
	for _, server := range m.MCPServers {
		descriptor := mcp.ServerDescriptor{ProtocolVersion: mcp.ClientProtocolVersion,
			ID: server.ID, Name: server.Name, Transport: server.Transport,
			Target: server.Target, Arguments: server.Arguments, CredentialRef: server.CredentialRef,
			DeclaredCapabilities: server.DeclaredCapabilities, Scope: mcp.ScopeWorkspace,
			WorkspaceID: "validation", Source: mcp.Source{Kind: "manual", URI: "plugin-validation"},
			CallTimeoutMillis: server.CallTimeoutMillis, MaxResultBytes: server.MaxResultBytes}
		if err := descriptor.Validate(); err != nil {
			return fmt.Errorf("plugin MCP contribution is invalid: %w", err)
		}
		if _, found := seenServers[server.ID]; found {
			return errors.New("plugin repeats an MCP server identity")
		}
		seenServers[server.ID] = struct{}{}
	}
	if m.UI != nil {
		if !validText(m.UI.DisplayName, 256, false) ||
			!validText(m.UI.Description, 2048, true) ||
			(m.UI.IconPath != "" && (!validPackagePath(m.UI.IconPath) ||
				(!strings.HasSuffix(m.UI.IconPath, ".png") &&
					!strings.HasSuffix(m.UI.IconPath, ".webp")))) {
			return errors.New("plugin UI metadata is invalid")
		}
		if m.UI.IconPath != "" {
			if _, found := files[m.UI.IconPath]; !found {
				return errors.New("plugin UI icon is absent from the file list")
			}
		}
	}
	seenHooks := make(map[string]struct{}, len(m.Hooks))
	for _, declaration := range m.Hooks {
		if err := hooks.ValidateDeclarative(declaration); err != nil {
			return err
		}
		if _, found := seenHooks[declaration.ID]; found {
			return errors.New("plugin repeats a Hook identity")
		}
		seenHooks[declaration.ID] = struct{}{}
	}
	return nil
}

type Signature struct {
	ProtocolVersion string `json:"protocol_version"`
	Publisher       string `json:"publisher"`
	Algorithm       string `json:"algorithm"`
	PublicKey       string `json:"public_key"`
	Signature       string `json:"signature"`
	SignedAt        string `json:"signed_at,omitempty"`
}

type Package struct {
	Manifest             Manifest
	ArchiveSHA256        string
	PackageFingerprint   string
	ArchiveBytes         int
	UncompressedBytes    int
	SignaturePresent     bool
	SignatureValid       bool
	PublisherFingerprint string
	PublisherPublicKey   string
	raw                  []byte
}

func (p Package) Archive() []byte { return slices.Clone(p.raw) }

type InstallSource struct {
	Kind               string `json:"kind"`
	URI                string `json:"uri"`
	Commit             string `json:"commit,omitempty"`
	SHA256             string `json:"sha256"`
	Surface            string `json:"surface,omitempty"` // host-selected read scope for portable installations
	OperationKeyDigest string `json:"operation_key_digest,omitempty"`
}

func (s InstallSource) Validate() error {
	if s.Kind != "local_file" && s.Kind != "https" && s.Kind != "git" &&
		s.Kind != "catalog" && s.Kind != "local_directory" && s.Kind != "upload" {
		return errors.New("plugin install source kind is invalid")
	}
	if !validText(s.URI, 4096, false) || !validDigest(s.SHA256) ||
		!validText(s.Commit, 128, true) || (s.Surface != "" && s.Surface != "code" && s.Surface != "cyber") ||
		(s.OperationKeyDigest != "" && !validDigest(s.OperationKeyDigest)) {
		return errors.New("plugin install source is invalid")
	}
	switch s.Kind {
	case "upload":
		if s.URI != "sha256:"+s.SHA256 || s.Commit != "" {
			return errors.New("uploaded plugin source must identify its exact archive digest")
		}
	case "local_file", "local_directory":
		if !filepath.IsAbs(s.URI) || s.Commit != "" {
			return errors.New("local plugin source requires an absolute path and no commit")
		}
	case "https", "git":
		parsed, err := url.Parse(s.URI)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("remote plugin source requires a fixed HTTPS URL without credentials, query, or fragment")
		}
		if s.Kind == "https" && s.Commit != "" {
			return errors.New("HTTPS plugin source cannot carry a Git commit")
		}
		if s.Kind == "git" && !fixedGitCommit(s.Commit) {
			return errors.New("Git plugin source requires a fixed hexadecimal commit")
		}
	case "catalog":
		if !validIdentity(s.URI) || s.Commit != "" {
			return errors.New("catalog plugin source requires a fixed catalog identity")
		}
	}
	return nil
}

func fixedGitCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, current := range value {
		if (current < '0' || current > '9') && (current < 'a' || current > 'f') {
			return false
		}
	}
	return true
}

type State string

const (
	StateStaged      State = "staged"
	StateApproved    State = "approved"
	StateEnabled     State = "enabled"
	StateDisabled    State = "disabled"
	StateRolledBack  State = "rolled_back"
	StateRevoked     State = "revoked"
	StateQuarantined State = "quarantined"
)

func (s State) Valid() bool {
	switch s {
	case StateStaged, StateApproved, StateEnabled, StateDisabled, StateRolledBack,
		StateRevoked, StateQuarantined:
		return true
	default:
		return false
	}
}

type Installation struct {
	ProtocolVersion          string            `json:"protocol_version"`
	ID                       string            `json:"id"`
	Manifest                 Manifest          `json:"manifest"`
	Snapshot                 *PortableSnapshot `json:"snapshot,omitempty"`
	Source                   InstallSource     `json:"source"`
	ArchiveSHA256            string            `json:"archive_sha256"`
	PackageFingerprint       string            `json:"package_fingerprint"`
	ArchiveBytes             int               `json:"archive_bytes"`
	SignaturePresent         bool              `json:"signature_present"`
	SignatureValid           bool              `json:"signature_valid"`
	PublisherFingerprint     string            `json:"publisher_fingerprint,omitempty"`
	PublisherPublicKey       string            `json:"publisher_public_key,omitempty"`
	State                    State             `json:"state"`
	EnabledCapabilities      []Capability      `json:"enabled_capabilities"`
	Generation               int64             `json:"generation"`
	SupersedesInstallationID string            `json:"supersedes_installation_id,omitempty"`
	StagedBy                 string            `json:"staged_by"`
	ReviewedBy               string            `json:"reviewed_by,omitempty"`
	ReviewedAt               *time.Time        `json:"reviewed_at,omitempty"`
	CreatedAt                time.Time         `json:"created_at"`
	UpdatedAt                time.Time         `json:"updated_at"`
}

func (i Installation) Validate() error {
	if i.validateDescription() != nil || !validIdentity(i.ID) || i.Source.Validate() != nil ||
		!validDigest(i.ArchiveSHA256) || !validDigest(i.PackageFingerprint) ||
		i.Source.SHA256 != i.ArchiveSHA256 || i.ArchiveBytes < 1 ||
		i.ArchiveBytes > MaxArchiveBytes || !i.State.Valid() || i.Generation < 1 ||
		!validText(i.SupersedesInstallationID, 256, true) ||
		!validText(i.StagedBy, 256, false) ||
		!validText(i.ReviewedBy, 256, true) || i.CreatedAt.IsZero() ||
		i.UpdatedAt.Before(i.CreatedAt) {
		return errors.New("plugin installation is invalid")
	}
	if i.SignatureValid && (!i.SignaturePresent || !validDigest(i.PublisherFingerprint)) {
		return errors.New("valid plugin signature requires a publisher fingerprint")
	}
	if !i.SignaturePresent && (i.SignatureValid || i.PublisherFingerprint != "") {
		return errors.New("unsigned plugin cannot carry signature trust metadata")
	}
	if i.SignatureValid && !validText(i.PublisherPublicKey, 256, false) {
		return errors.New("signed plugin requires its public key for publisher review")
	}
	if !i.SignaturePresent && i.PublisherPublicKey != "" {
		return errors.New("unsigned plugin cannot carry a publisher public key")
	}
	seen := make(map[Capability]struct{}, len(i.EnabledCapabilities))
	for _, capability := range i.EnabledCapabilities {
		if !capability.Valid() || !slices.Contains(i.Capabilities(), capability) {
			return errors.New("plugin installation enables an undeclared capability")
		}
		if _, found := seen[capability]; found {
			return errors.New("plugin installation repeats an enabled capability")
		}
		seen[capability] = struct{}{}
	}
	if i.State == StateStaged && (len(i.EnabledCapabilities) != 0 || i.ReviewedAt != nil || i.ReviewedBy != "") {
		return errors.New("staged plugin cannot carry enabled capabilities or review metadata")
	}
	if i.State == StateEnabled && len(i.EnabledCapabilities) == 0 {
		return errors.New("enabled plugin requires at least one capability")
	}
	if i.ReviewedAt != nil && (i.ReviewedBy == "" || i.ReviewedAt.Before(i.CreatedAt)) {
		return errors.New("plugin installation review metadata is invalid")
	}
	return nil
}

func NewInstallation(id string, pkg Package, source InstallSource, supersedes,
	stagedBy string, at time.Time,
) (Installation, error) {
	value := Installation{ProtocolVersion: InstallationProtocol, ID: id,
		Manifest: pkg.Manifest, Source: source, ArchiveSHA256: pkg.ArchiveSHA256,
		PackageFingerprint: pkg.PackageFingerprint, ArchiveBytes: pkg.ArchiveBytes,
		SignaturePresent: pkg.SignaturePresent, SignatureValid: pkg.SignatureValid,
		PublisherFingerprint: pkg.PublisherFingerprint, PublisherPublicKey: pkg.PublisherPublicKey,
		State:               StateStaged,
		EnabledCapabilities: []Capability{}, Generation: 1,
		SupersedesInstallationID: supersedes, StagedBy: stagedBy,
		CreatedAt: at.UTC(), UpdatedAt: at.UTC()}
	return value, value.Validate()
}

type PublisherState string

const (
	PublisherTrusted PublisherState = "trusted"
	PublisherRevoked PublisherState = "revoked"
)

type PublisherTrust struct {
	ProtocolVersion string         `json:"protocol_version"`
	Fingerprint     string         `json:"fingerprint"`
	Publisher       string         `json:"publisher"`
	PublicKey       string         `json:"public_key"`
	State           PublisherState `json:"state"`
	Generation      int64          `json:"generation"`
	ReviewedBy      string         `json:"reviewed_by"`
	ReviewedAt      time.Time      `json:"reviewed_at"`
}

func (p PublisherTrust) Validate() error {
	if p.ProtocolVersion != PublisherTrustProtocol || !validDigest(p.Fingerprint) ||
		!validText(p.Publisher, 256, false) || !validText(p.PublicKey, 256, false) ||
		(p.State != PublisherTrusted && p.State != PublisherRevoked) || p.Generation < 1 ||
		!validText(p.ReviewedBy, 256, false) || p.ReviewedAt.IsZero() {
		return errors.New("plugin publisher trust record is invalid")
	}
	return nil
}

func InstallationFingerprint(value Installation) string {
	copyValue := value
	copyValue.EnabledCapabilities = slices.Clone(value.EnabledCapabilities)
	sort.Slice(copyValue.EnabledCapabilities, func(i, j int) bool {
		return copyValue.EnabledCapabilities[i] < copyValue.EnabledCapabilities[j]
	})
	copyValue.Generation = 0
	copyValue.ReviewedBy = ""
	copyValue.ReviewedAt = nil
	copyValue.CreatedAt = time.Time{}
	copyValue.UpdatedAt = time.Time{}
	raw, _ := json.Marshal(copyValue)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func validIdentity(value string) bool {
	return validText(value, 256, false) && !strings.ContainsAny(value, "/\\")
}

func validVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return false
		}
		for _, current := range part {
			if current < '0' || current > '9' {
				return false
			}
		}
	}
	return true
}

func validText(value string, maxBytes int, allowEmpty bool) bool {
	if !utf8.ValidString(value) || len([]byte(value)) > maxBytes ||
		strings.ContainsRune(value, 0) || value != strings.TrimSpace(value) {
		return false
	}
	if !allowEmpty && value == "" {
		return false
	}
	for _, current := range value {
		if current != '\n' && current != '\r' && current != '\t' && unicode.IsControl(current) {
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
