package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/computeruse"
	"cyberagent-workbench/internal/toolcontract"
)

const (
	ComputerUseSourceURI    = "universal-code:computer-use"
	ComputerUseTarget       = "builtin:windows-computer-use"
	computerUseServerPrefix = "builtin-computer-use-"
	computerUseDisplayName  = "Windows Computer Use"
)

func ComputerUseServerID(workspaceID string) string {
	sum := sha256.Sum256([]byte(workspaceID))
	return computerUseServerPrefix + hex.EncodeToString(sum[:16])
}

// IsComputerUseServer recognizes the host-owned descriptor, never a tool name
// or a remote server's self-reported annotations. Stage rejects this source and
// its reserved IDs. The executable is supplied by the current host at dispatch.
func IsComputerUseServer(d ServerDescriptor) bool {
	return d.ProtocolVersion == ClientProtocolVersion && d.ID == ComputerUseServerID(d.WorkspaceID) &&
		d.Name == computerUseDisplayName && d.Transport == TransportStdio && d.Target == ComputerUseTarget &&
		d.Source == (Source{Kind: "builtin", URI: ComputerUseSourceURI, Version: "1"}) &&
		d.Scope == ScopeWorkspace && d.RunID == "" && validClientIdentity(d.WorkspaceID) &&
		d.NativeSource == nil && len(d.Arguments) == 0 && d.CredentialRef == "" && d.Cwd == "" &&
		len(d.Env) == 0 && len(d.EnvCredentialRefs) == 0 && len(d.Headers) == 0 &&
		len(d.DeclaredCapabilities) == 1 && d.DeclaredCapabilities[0] == CapabilityTools &&
		d.CallTimeoutMillis == 30_000 && d.MaxResultBytes == MaxClientResultBytes
}

func computerUseDescriptor(workspaceID string) ServerDescriptor {
	return ServerDescriptor{ProtocolVersion: ClientProtocolVersion, ID: ComputerUseServerID(workspaceID),
		Name: computerUseDisplayName, Transport: TransportStdio, Target: ComputerUseTarget,
		Scope: ScopeWorkspace, WorkspaceID: workspaceID,
		Source:               Source{Kind: "builtin", URI: ComputerUseSourceURI, Version: "1"},
		DeclaredCapabilities: []CapabilityKind{CapabilityTools}, CallTimeoutMillis: 30_000,
		MaxResultBytes: MaxClientResultBytes}
}

func computerUseCapabilities(at time.Time) (CapabilitySnapshot, error) {
	var tools []RemoteTool
	for _, definition := range computeruse.Catalog() {
		schema, err := json.Marshal(definition.InputSchema)
		if err != nil {
			return CapabilitySnapshot{}, err
		}
		tools = append(tools, RemoteTool{Name: definition.Name, Description: definition.Description, InputSchema: schema})
	}
	snapshot, err := NewCapabilitySnapshot(computeruse.ServerName, computeruse.ServerVersion,
		[]CapabilityKind{CapabilityTools}, tools, nil, nil, at)
	if err == nil {
		snapshot.ProtocolVersion = computeruse.ProtocolVersion
		snapshot.Fingerprint = capabilityFingerprint(snapshot)
		err = snapshot.Validate()
	}
	return snapshot, err
}

// EnsureComputerUse registers the shipped catalog without starting a desktop
// process or inspecting any windows. Existing operator disable/revoke decisions
// are retained. Every actual call still goes through normal MCP host authority.
func (m *Manager) EnsureComputerUse(ctx context.Context, workspaceID string) error {
	if m.computerUseExecutable == "" {
		return nil
	}
	d := computerUseDescriptor(workspaceID)
	if err := d.Validate(); err != nil {
		return err
	}
	lock := m.lock(d.ID)
	lock.Lock()
	defer lock.Unlock()
	record, err := m.store.GetMCPClientServer(ctx, d.ID)
	if apperror.CodeOf(err) == apperror.CodeNotFound {
		now := m.now().UTC()
		record, _, err = m.store.CreateMCPClientServer(ctx, ServerRecord{
			ProtocolVersion: ServerRecordProtocolVersion, Descriptor: d, DescriptorFingerprint: d.Fingerprint(),
			State: TrustStaged, Health: HealthUnknown, Generation: 1, CreatedAt: now, UpdatedAt: now})
	}
	if err != nil {
		return err
	}
	if record.DescriptorFingerprint != d.Fingerprint() || !IsComputerUseServer(record.Descriptor) {
		return apperror.New(apperror.CodeConflict, "Computer Use registration identity is already in use")
	}
	if record.State != TrustStaged && record.State != TrustEnabled {
		return nil
	}
	snapshot, err := computerUseCapabilities(m.now())
	if err != nil {
		return err
	}
	if record.State == TrustEnabled && record.Capabilities.Fingerprint == snapshot.Fingerprint {
		return nil
	}
	record.Capabilities, record.ApprovedCapabilityFingerprint = snapshot, snapshot.Fingerprint
	record.State, record.Health, record.HealthMessage = TrustEnabled, HealthHealthy, ""
	record.Generation++
	record.UpdatedAt = m.now().UTC()
	record.ReviewedAt, record.ReviewedBy = &record.UpdatedAt, "builtin_host"
	_, err = m.store.UpdateMCPClientServer(ctx, record, record.Generation-1)
	return err
}

func (m *Manager) refreshComputerUse(ctx context.Context, record ServerRecord) (ServerRecord, error) {
	if m.computerUseExecutable == "" {
		return ServerRecord{}, apperror.New(apperror.CodeUnavailable, "Computer Use requires the Windows desktop host")
	}
	snapshot, err := computerUseCapabilities(m.now())
	if err != nil {
		return ServerRecord{}, err
	}
	record.Capabilities, record.Health, record.HealthMessage = snapshot, HealthHealthy, ""
	if record.ApprovedCapabilityFingerprint == "" {
		record.State = TrustCapabilitiesPending
	} else {
		record.State, record.ApprovedCapabilityFingerprint = TrustEnabled, snapshot.Fingerprint
	}
	record.Generation++
	record.UpdatedAt = m.now().UTC()
	return m.store.UpdateMCPClientServer(ctx, record, record.Generation-1)
}

func (m *Manager) resolveComputerUse(d ServerDescriptor, version string) (toolcontract.ResolvedLaunch, error) {
	if !IsComputerUseServer(d) || m.computerUseExecutable == "" {
		return toolcontract.ResolvedLaunch{}, errors.New("Computer Use is unavailable in this host")
	}
	// Adapt only the fixed, shipped service to the existing launch guard. The
	// persisted descriptor never supplies a command, arguments or environment.
	resolved := d
	resolved.Source = Source{Kind: "manual", URI: ComputerUseSourceURI}
	resolved.Target, resolved.Arguments = m.computerUseExecutable, []string{computeruse.Arg}
	launch, err := resolveManualLaunch(resolved, []string{version}, nil)
	if err == nil {
		launch.InstanceID = d.Fingerprint()
	}
	return launch, err
}
