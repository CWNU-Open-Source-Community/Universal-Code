package mcp

import (
	"context"
	"crypto/rand"
	"errors"
	"slices"
	"sync/atomic"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/toolcontract"
)

// Operator discovery uses its existing explicit descriptor approval and durable
// discovery lease. It is not a fabricated Run or a model-triggered Full grant.
func (m *Manager) discoverNativeSource(ctx context.Context, record ServerRecord) (CapabilitySnapshot, error) {
	if m.nativeSources == nil || record.Descriptor.NativeSource == nil || record.Health != HealthConnecting {
		return CapabilitySnapshot{}, errors.New("native MCP discovery source is unavailable")
	}
	source := *record.Descriptor.NativeSource
	launch, secrets, checkSource, closeSource, err := m.nativeSources.Resolve(ctx, source, record.Descriptor.RunID)
	if closeSource != nil {
		defer closeSource()
	}
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	if checkSource == nil || closeSource == nil || launch.Component != source.Component || launch.Transport != toolcontract.Transport(record.Descriptor.Transport) {
		return CapabilitySnapshot{}, errors.New("native MCP launch does not bind its source registration")
	}
	recheck := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := m.store.GetMCPClientServer(ctx, record.Descriptor.ID)
		if err != nil {
			return err
		}
		if current.Generation != record.Generation || current.DescriptorFingerprint != record.DescriptorFingerprint || current.State != record.State ||
			current.Health != HealthConnecting || current.DiscoveryLeaseID != record.DiscoveryLeaseID || current.DiscoveryLeaseExpiresAt == nil || !current.DiscoveryLeaseExpiresAt.After(m.now().UTC()) {
			return apperror.New(apperror.CodePolicyDenied, "native MCP discovery review or lease changed")
		}
		return checkSource(ctx)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return CapabilitySnapshot{}, err
	}
	fingerprint, err := toolcontract.FingerprintLaunch(key, launch)
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	connect := runtimeOperation(record.DiscoveryLeaseID, toolcontract.OperationConnect, launch, fingerprint)
	connectFingerprint, err := toolcontract.FingerprintOperation(connect)
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	scope := toolcontract.DiscoveryScope{OperationID: "discovery-" + record.DiscoveryLeaseID, ConnectionFingerprint: fingerprint, Profile: launch.ProtocolVersions[0],
		Methods: []string{"initialize", "notifications/initialized", "tools/list", "resources/list", "resources/templates/list", "prompts/list"}, MaxRequests: 64, MaxBytes: 1024 * 1024,
		ExpiresAt: m.now().Add(time.Duration(record.Descriptor.CallTimeoutMillis) * time.Millisecond)}
	if scope.Profile == preferredClientProtocolVersion {
		scope.Methods = append(scope.Methods, "server/discover", "subscriptions/listen")
	}
	scopeFingerprint, err := toolcontract.FingerprintDiscovery(scope)
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	var connected, discovered atomic.Bool
	guards := toolcontract.ExecutionGuards{
		BeforeConnect: func(ctx context.Context, actual string) error {
			if actual != connectFingerprint || !connected.CompareAndSwap(false, true) {
				return errors.New("native MCP connect authority was changed or consumed")
			}
			return recheck(ctx)
		},
		BeginDiscovery: func(ctx context.Context, actual toolcontract.DiscoveryScope) (toolcontract.DiscoverySendGuard, error) {
			value, err := toolcontract.FingerprintDiscovery(actual)
			if err != nil || value != scopeFingerprint || !discovered.CompareAndSwap(false, true) {
				return nil, errors.New("native MCP discovery authority was changed or consumed")
			}
			if err := recheck(ctx); err != nil {
				return nil, err
			}
			return toolcontract.NewDiscoverySendGuard(scope, recheck)
		},
	}
	client, err := NewResolvedClient(launch, key, connect, guards, nil, m.httpClient)
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	defer client.Close()
	roots, err := m.prepareWorkspaceRoots(ctx, record, record.Descriptor.RunID, recheck)
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	client.setWorkspaceRoots(roots)
	client.secrets = slices.Clone(secrets)
	client.descriptor.DeclaredCapabilities = slices.Clone(record.Descriptor.DeclaredCapabilities)
	capabilities, err := client.DiscoverWithScope(ctx, scope)
	if err != nil {
		return CapabilitySnapshot{}, err
	}
	if err := recheck(ctx); err != nil {
		return CapabilitySnapshot{}, err
	}
	return capabilities, nil
}
