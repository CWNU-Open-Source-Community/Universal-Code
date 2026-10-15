package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"cyberagent-workbench/internal/approval"
	"cyberagent-workbench/internal/domain"
)

const SupervisorCallAuthorityVersion = 1
const SupervisorOperationAuthorityVersion = 2
const SupervisorCatalogAuthorityVersion = 3
const OperationApprovalTool = "mcp_tool_call"

// SupervisorCallAuthority is the durable, fail-closed execution authority
// attached to an MCP call or catalog search in the Supervisor ledger. It contains only
// scope and revocation facts; server credentials and tool arguments never
// belong in this envelope.
type SupervisorCallAuthority struct {
	Version              int                               `json:"version"`
	RunID                string                            `json:"run_id"`
	MissionID            string                            `json:"mission_id"`
	WorkspaceID          string                            `json:"workspace_id"`
	PermissionSnapshotID string                            `json:"permission_snapshot_id"`
	PermissionRevision   int64                             `json:"permission_revision"`
	PermissionMode       domain.RunExecutionPermissionMode `json:"permission_mode"`
	PermissionGeneration uint64                            `json:"permission_generation"`
	// Optional for decoding historical records. Runtime-backed hosts require
	// their exact process epoch before dispatch; decoding never renews authority.
	PermissionRuntimeEpoch string `json:"permission_runtime_epoch,omitempty"`
	RunAuthorizationFence  uint64 `json:"run_authorization_fence"`
	// The host pins the reviewed registration independently of the remote
	// capability digest. A disable/re-enable or source change invalidates consent.
	ServerID              string `json:"server_id,omitempty"`
	DescriptorFingerprint string `json:"descriptor_fingerprint,omitempty"`
	ServerGeneration      int64  `json:"server_generation,omitempty"`
}

func (a SupervisorCallAuthority) Validate() error {
	if (a.Version != SupervisorCallAuthorityVersion && a.Version != SupervisorOperationAuthorityVersion && a.Version != SupervisorCatalogAuthorityVersion) || !domain.ValidAgentID(a.RunID) ||
		!domain.ValidAgentID(a.MissionID) || !domain.ValidAgentID(a.WorkspaceID) ||
		!domain.ValidAgentID(a.PermissionSnapshotID) || a.PermissionRevision < 1 ||
		!a.PermissionMode.Valid() {
		return errors.New("Supervisor MCP authority is invalid")
	}
	if a.Version == SupervisorCallAuthorityVersion {
		if !a.PermissionMode.IncludesFullAccess() || a.ServerID != "" || a.DescriptorFingerprint != "" || a.ServerGeneration != 0 {
			return errors.New("legacy Supervisor MCP authority is invalid")
		}
	} else if a.Version == SupervisorCatalogAuthorityVersion {
		if a.ServerID != "" || a.DescriptorFingerprint != "" || a.ServerGeneration != 0 {
			return errors.New("MCP catalog authority cannot bind a remote invocation")
		}
	} else if !validClientIdentity(a.ServerID) || !validClientDigest(a.DescriptorFingerprint) || a.ServerGeneration < 1 {
		return errors.New("Supervisor MCP authority requires its exact reviewed registration")
	}
	return nil
}

func (a SupervisorCallAuthority) MatchesServer(record ServerRecord) bool {
	return a.Version == SupervisorOperationAuthorityVersion && a.Validate() == nil &&
		record.Validate() == nil && record.Descriptor.ID == a.ServerID &&
		record.DescriptorFingerprint == a.DescriptorFingerprint && record.Generation == a.ServerGeneration &&
		record.State == TrustEnabled && record.Health == HealthHealthy &&
		record.ApprovedCapabilityFingerprint == record.Capabilities.Fingerprint &&
		scopeMatches(record.Descriptor, a.RunID, a.WorkspaceID)
}

// This durable outer intent is separate from the runtime adapter's ephemeral
// input HMAC. Only the host may bind an approved intent to an actual operation.
func OperationApprovalFingerprint(call domain.SupervisorToolCall) string {
	return approval.Fingerprint(OperationApprovalTool, call.RunID, strconv.Itoa(call.Turn),
		call.AttemptID, call.AgentID, call.AgentAttemptID, string(call.AgentAttribution),
		call.CallID, call.ToolName, call.PayloadJSON, call.AuthorityJSON)
}

func EncodeSupervisorCallAuthority(authority SupervisorCallAuthority) (json.RawMessage, error) {
	if err := authority.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(authority)
}

func DecodeSupervisorCallAuthority(raw json.RawMessage) (SupervisorCallAuthority, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var authority SupervisorCallAuthority
	if err := decoder.Decode(&authority); err != nil {
		return SupervisorCallAuthority{}, errors.New("Supervisor MCP authority is malformed")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return SupervisorCallAuthority{}, errors.New("Supervisor MCP authority contains trailing data")
	}
	return authority, authority.Validate()
}
