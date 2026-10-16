package commandruntimeadapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"cyberagent-workbench/internal/domain"
)

const (
	AuthorityProtocolVersion  = "command-runtime-adapter-authority.v1"
	OperationAuthorityVersion = "command-runtime-adapter-authority.v2"
	MaxIdentityRunes          = 256
)

type Kind string

const (
	KindSandboxedWorkspace Kind = "sandboxed_workspace"
	KindHostUnsandboxed    Kind = "host_unsandboxed"
	KindLegacyUnbound      Kind = "legacy_unbound"
)

func (k Kind) Valid() bool {
	switch k {
	case KindSandboxedWorkspace, KindHostUnsandboxed, KindLegacyUnbound:
		return true
	default:
		return false
	}
}

type IsolationGrade string

const (
	IsolationWorkspaceSandbox IsolationGrade = "workspace_sandbox"
	IsolationHostUnsandboxed  IsolationGrade = "host_unsandboxed"
	IsolationLegacyUnknown    IsolationGrade = "legacy_unknown"
)

type NetworkPolicy string

const (
	NetworkDenied        NetworkPolicy = "denied"
	NetworkHostAvailable NetworkPolicy = "host_available"
	NetworkLegacyUnknown NetworkPolicy = "legacy_unknown"
)

type CredentialPolicy string

const (
	CredentialsNone          CredentialPolicy = "none"
	CredentialsHostAvailable CredentialPolicy = "host_available"
	CredentialsLegacyUnknown CredentialPolicy = "legacy_unknown"
)

// Identity is an authority-bearing adapter receipt. Backend is the stable
// backend family exposed to callers, while BackendIdentity binds one installed
// implementation/generation. The model payload never supplies either value.
type Identity struct {
	Kind             Kind             `json:"kind"`
	Backend          string           `json:"backend"`
	BackendIdentity  string           `json:"backend_identity"`
	Generation       string           `json:"generation"`
	IsolationGrade   IsolationGrade   `json:"isolation_grade"`
	NetworkPolicy    NetworkPolicy    `json:"network_policy"`
	CredentialPolicy CredentialPolicy `json:"credential_policy"`
}

func HostUnsandboxed(generation string) Identity {
	return Identity{
		Kind: KindHostUnsandboxed, Backend: "run_owned_command_runtime",
		BackendIdentity: "host-process.v1", Generation: strings.TrimSpace(generation),
		IsolationGrade:   IsolationHostUnsandboxed,
		NetworkPolicy:    NetworkHostAvailable,
		CredentialPolicy: CredentialsHostAvailable,
	}
}

func SandboxedWorkspace(backend, backendIdentity, generation string) Identity {
	return Identity{
		Kind: KindSandboxedWorkspace, Backend: strings.TrimSpace(backend),
		BackendIdentity: strings.TrimSpace(backendIdentity),
		Generation:      strings.TrimSpace(generation),
		IsolationGrade:  IsolationWorkspaceSandbox, NetworkPolicy: NetworkDenied,
		CredentialPolicy: CredentialsNone,
	}
}

func LegacyUnbound() Identity {
	return Identity{
		Kind: KindLegacyUnbound, Backend: "legacy_unbound",
		BackendIdentity: "legacy_unbound", Generation: "legacy_unbound",
		IsolationGrade:   IsolationLegacyUnknown,
		NetworkPolicy:    NetworkLegacyUnknown,
		CredentialPolicy: CredentialsLegacyUnknown,
	}
}

func (i Identity) IsZero() bool {
	return i.Kind == "" && i.Backend == "" && i.BackendIdentity == "" && i.Generation == "" &&
		i.IsolationGrade == "" && i.NetworkPolicy == "" && i.CredentialPolicy == ""
}

func (i Identity) Validate() error {
	for _, value := range []string{string(i.Kind), i.Backend, i.BackendIdentity, i.Generation,
		string(i.IsolationGrade), string(i.NetworkPolicy), string(i.CredentialPolicy)} {
		if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) ||
			len([]rune(value)) > MaxIdentityRunes {
			return errors.New("command runtime adapter identity is invalid")
		}
	}
	switch i.Kind {
	case KindSandboxedWorkspace:
		if i.IsolationGrade != IsolationWorkspaceSandbox ||
			i.NetworkPolicy != NetworkDenied || i.CredentialPolicy != CredentialsNone {
			return errors.New("sandboxed command runtime receipt is inconsistent")
		}
	case KindHostUnsandboxed:
		if i.IsolationGrade != IsolationHostUnsandboxed ||
			i.NetworkPolicy != NetworkHostAvailable ||
			i.CredentialPolicy != CredentialsHostAvailable {
			return errors.New("host command runtime receipt is inconsistent")
		}
	case KindLegacyUnbound:
		if i.Backend != "legacy_unbound" || i.BackendIdentity != "legacy_unbound" ||
			i.Generation != "legacy_unbound" ||
			i.IsolationGrade != IsolationLegacyUnknown ||
			i.NetworkPolicy != NetworkLegacyUnknown ||
			i.CredentialPolicy != CredentialsLegacyUnknown {
			return errors.New("legacy command runtime receipt is inconsistent")
		}
	default:
		return errors.New("command runtime adapter kind is invalid")
	}
	return nil
}

func (i Identity) Executable() bool {
	return i.Validate() == nil && i.Kind != KindLegacyUnbound
}

func (i Identity) AllowsPermission(mode domain.RunExecutionPermissionMode) bool {
	if i.Validate() != nil {
		return false
	}
	// The new preference selects operation policy, not an installed backend.
	// Native scope/isolation and the common authorizer must still admit dispatch.
	if mode.IsApprovalMode() {
		return i.Executable()
	}
	switch i.Kind {
	case KindSandboxedWorkspace:
		return mode == domain.RunExecutionPermissionWorkspaceAccess
	case KindHostUnsandboxed:
		return mode.IncludesFullAccess()
	default:
		return false
	}
}

func (i Identity) SameBackend(other Identity) bool {
	return i.Validate() == nil && other.Validate() == nil && i == other
}

// Authority is issued when command_runtime is advertised. It is copied into
// the durable Supervisor tool call and checked again at execution, so a stale
// model response cannot cross an adapter restart or backend replacement.
type Authority struct {
	ProtocolVersion        string                            `json:"protocol_version"`
	RunID                  string                            `json:"run_id"`
	Adapter                Identity                          `json:"adapter"`
	PermissionSnapshotID   string                            `json:"permission_snapshot_id,omitempty"`
	PermissionGeneration   uint64                            `json:"permission_generation,omitempty"`
	PermissionRuntimeEpoch string                            `json:"permission_runtime_epoch,omitempty"`
	PermissionMode         domain.RunExecutionPermissionMode `json:"permission_mode,omitempty"`
	PermissionRevision     int64                             `json:"permission_revision,omitempty"`
	RunAuthorizationFence  uint64                            `json:"run_authorization_fence,omitempty"`
	// Prepared per-call native bindings contain hashes, never resolved secrets.
	ScopeFingerprint    string   `json:"scope_fingerprint,omitempty"`
	CommandFingerprints []string `json:"command_fingerprints,omitempty"`
	JobFingerprint      string   `json:"job_fingerprint,omitempty"`
	// Host-compiled run_tests command. Recompiled and compared before dispatch.
	ProjectTestCommand json.RawMessage `json:"project_test_command,omitempty"`
}

func NewAuthority(runID string, identity Identity) Authority {
	return Authority{ProtocolVersion: AuthorityProtocolVersion,
		RunID: strings.TrimSpace(runID), Adapter: identity}
}

func (a Authority) Validate() error {
	if (a.ProtocolVersion != AuthorityProtocolVersion && a.ProtocolVersion != OperationAuthorityVersion) || !domain.ValidAgentID(a.RunID) ||
		!a.Adapter.Executable() {
		return errors.New("command runtime adapter authority is invalid")
	}
	if a.ProtocolVersion == OperationAuthorityVersion {
		if !a.PermissionMode.IsApprovalMode() || !domain.ValidAgentID(a.PermissionSnapshotID) ||
			a.PermissionRevision <= 0 || !ValidRuntimeBinding(a.PermissionMode, a.PermissionRuntimeEpoch, a.PermissionGeneration, a.RunAuthorizationFence) ||
			(a.ScopeFingerprint != "" && !validDigest(a.ScopeFingerprint)) || len(a.CommandFingerprints) > 4 ||
			(a.JobFingerprint != "" && !validDigest(a.JobFingerprint)) ||
			(len(a.ProjectTestCommand) != 0 && (!json.Valid(a.ProjectTestCommand) || len(a.ProjectTestCommand) > 32768)) {
			return errors.New("command runtime operation authority is invalid")
		}
		for _, fingerprint := range a.CommandFingerprints {
			if !validDigest(fingerprint) {
				return errors.New("command runtime input pin is invalid")
			}
		}
		return nil
	}
	if a.PermissionMode != "" || a.PermissionRevision != 0 || a.RunAuthorizationFence != 0 ||
		a.ScopeFingerprint != "" || len(a.CommandFingerprints) != 0 || a.JobFingerprint != "" || len(a.ProjectTestCommand) != 0 {
		return errors.New("legacy command runtime authority contains operation fields")
	}
	if a.PermissionSnapshotID != "" || a.PermissionGeneration != 0 ||
		a.PermissionRuntimeEpoch != "" {
		if !domain.ValidAgentID(a.PermissionSnapshotID) ||
			a.PermissionGeneration == 0 || a.PermissionRuntimeEpoch == "" ||
			a.Adapter.Kind != KindHostUnsandboxed {
			return errors.New("command runtime Full Access grant authority is invalid or partial")
		}
	}
	return nil
}

// ValidRuntimeBinding validates provenance shape only. It cannot reactivate a
// stored epoch/fence or a Full generation; the host must check live authority.
func ValidRuntimeBinding(mode domain.RunExecutionPermissionMode, epoch string, generation, fence uint64) bool {
	if epoch != "" && (!domain.ValidAgentID(epoch) || strings.ContainsRune(epoch, 0)) {
		return false
	}
	if !mode.IsApprovalMode() {
		return fence == 0 && ((epoch == "") == (generation == 0))
	}
	if mode == domain.RunExecutionPermissionFull {
		return epoch != "" && generation > 0 && fence > 0
	}
	return generation == 0 && ((epoch == "" && fence == 0) || (epoch != "" && fence > 0))
}

func validDigest(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == sha256.Size && len(value) == 64 && strings.ToLower(value) == value
}

// OperationApprovalFingerprint identifies the immutable host-recorded call in
// the existing Supervisor/approval ledgers. No model-supplied approval boolean.
func OperationApprovalFingerprint(call domain.SupervisorToolCall) string {
	raw, _ := json.Marshal([]string{OperationAuthorityVersion, call.RunID, call.CallID,
		call.AgentID, call.AgentAttemptID, call.AttemptID, call.ToolName, call.PayloadJSON, call.AuthorityJSON})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func EncodeAuthority(authority Authority) (json.RawMessage, error) {
	if err := authority.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(authority)
}

func DecodeAuthority(raw json.RawMessage) (Authority, error) {
	if len(raw) < 2 || len(raw) > 4*1024 || !utf8.Valid(raw) {
		return Authority{}, errors.New("command runtime adapter authority must be bounded UTF-8 JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var authority Authority
	if err := decoder.Decode(&authority); err != nil {
		return Authority{}, errors.New("command runtime adapter authority does not match its schema")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Authority{}, errors.New("command runtime adapter authority contains trailing data")
	}
	if err := authority.Validate(); err != nil {
		return Authority{}, err
	}
	return authority, nil
}
