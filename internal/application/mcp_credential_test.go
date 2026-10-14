package application

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/apperror"
	"cyberagent-workbench/internal/credential"
	"cyberagent-workbench/internal/mcp"
)

type mcpCredentialRegistryFake struct{ records []mcp.ServerRecord }

func (r *mcpCredentialRegistryFake) GetMCPClientServer(_ context.Context, id string) (mcp.ServerRecord, error) {
	for _, record := range r.records {
		if record.Descriptor.ID == id {
			return record, nil
		}
	}
	return mcp.ServerRecord{}, apperror.New(apperror.CodeNotFound, "MCP server not found")
}

func TestMCPCredentialConcurrentChangesSerializeMutationAndReadback(t *testing.T) {
	record := mcpCredentialRecord("first")
	service := NewMCPCredentialService(&mcpCredentialRegistryFake{[]mcp.ServerRecord{record}}, credential.NewMemoryStore())
	request := mcpCredentialChange(t, service, mcpCredentialBinding(record))
	var group sync.WaitGroup
	failures := make(chan error, 24)
	for index := range 24 {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			current := request
			if index%2 == 0 {
				current.Action, current.Secret = "delete", ""
			}
			status, err := service.Change(t.Context(), current)
			if err != nil {
				failures <- err
				return
			}
			if status.Configured != (current.Action == "set") {
				failures <- errors.New("concurrent mutation stole final readback")
			}
		}(index)
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

type changingMCPCredentialStore struct {
	*credential.MemoryStore
	onPut func()
}

func (s changingMCPCredentialStore) Put(ctx context.Context, name, secret string) error {
	if err := s.MemoryStore.Put(ctx, name, secret); err != nil {
		return err
	}
	s.onPut()
	return nil
}
func TestMCPCredentialChangedFinalBindingCannotClaimMutationSuccess(t *testing.T) {
	record := mcpCredentialRecord("first")
	for _, kind := range []string{"sharing", "descriptor", "presence"} {
		t.Run(kind, func(t *testing.T) {
			registry := &mcpCredentialRegistryFake{[]mcp.ServerRecord{record}}
			owned := credential.NewMemoryStore()
			service := NewMCPCredentialService(registry, changingMCPCredentialStore{owned, func() {
				switch kind {
				case "sharing":
					registry.records = append(registry.records, mcpCredentialRecord("second"))
				case "descriptor":
					registry.records[0].Descriptor.Target = "https://changed.invalid/mcp"
					registry.records[0].DescriptorFingerprint = registry.records[0].Descriptor.Fingerprint()
				case "presence":
					_ = owned.Delete(t.Context(), record.Descriptor.CredentialRef)
				}
			}})
			request := mcpCredentialChange(t, service, mcpCredentialBinding(record))
			if _, err := service.Change(t.Context(), request); apperror.CodeOf(err) != apperror.CodeUnavailable || strings.Contains(err.Error(), request.Secret) {
				t.Fatalf("unverified mutation reported success or secret: %v", err)
			}
		})
	}
}

func TestMCPCredentialUnsupportedDescriptorsNeverReadSecretPresence(t *testing.T) {
	for _, kind := range []string{"stdio", "no-reference"} {
		t.Run(kind, func(t *testing.T) {
			record := mcpCredentialRecord("first")
			if kind == "stdio" {
				record.Descriptor.Transport = mcp.TransportStdio
				record.Descriptor.Target = filepath.Join(t.TempDir(), "mcp-fixture.exe")
			}
			record.Descriptor.CredentialRef = ""
			record.DescriptorFingerprint = record.Descriptor.Fingerprint()
			service := NewMCPCredentialService(&mcpCredentialRegistryFake{[]mcp.ServerRecord{record}}, failingMCPCredentialStore{MemoryStore: credential.NewMemoryStore(), failPresence: true})
			binding := mcpCredentialBinding(record)
			binding.CredentialRef = "mcp-fixture"
			if _, err := service.Status(t.Context(), binding); apperror.CodeOf(err) != apperror.CodeFailedPrecondition {
				t.Fatalf("unsupported descriptor reached presence lookup: %v", err)
			}
		})
	}
}
func (r *mcpCredentialRegistryFake) ListMCPClientServersByCredentialRef(_ context.Context, name string) ([]mcp.ServerRecord, error) {
	values := []mcp.ServerRecord{}
	for _, record := range r.records {
		if record.Descriptor.ReferencesCredential(name) {
			values = append(values, record)
		}
	}
	return values, nil
}
func mcpCredentialRecord(id string) mcp.ServerRecord {
	d := mcp.ServerDescriptor{ProtocolVersion: mcp.ClientProtocolVersion, ID: id, Name: id,
		Transport: mcp.TransportStreamableHTTP, Target: "https://fixture.invalid/mcp", CredentialRef: "mcp-fixture",
		Scope: mcp.ScopeWorkspace, WorkspaceID: "workspace-one", Source: mcp.Source{Kind: "manual", URI: "http-upload"},
		DeclaredCapabilities: []mcp.CapabilityKind{mcp.CapabilityTools}, CallTimeoutMillis: 3000, MaxResultBytes: 8192}
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	return mcp.ServerRecord{ProtocolVersion: mcp.ServerRecordProtocolVersion, Descriptor: d,
		DescriptorFingerprint: d.Fingerprint(), State: mcp.TrustStaged, Health: mcp.HealthUnknown,
		Generation: 1, CreatedAt: now, UpdatedAt: now}
}
func mcpCredentialBinding(r mcp.ServerRecord) MCPCredentialBinding {
	return MCPCredentialBinding{ServerID: r.Descriptor.ID, WorkspaceID: r.Descriptor.WorkspaceID, RunID: r.Descriptor.RunID,
		ExpectedDescriptorFingerprint: r.DescriptorFingerprint, CredentialRef: r.Descriptor.CredentialRef, Target: r.Descriptor.Target}
}
func mcpCredentialChange(t *testing.T, service *MCPCredentialService, binding MCPCredentialBinding) ChangeMCPCredentialRequest {
	t.Helper()
	status, err := service.Status(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	return ChangeMCPCredentialRequest{Version: MCPCredentialProtocolVersion, Binding: binding, Action: "set",
		Secret: "synthetic-bearer-only", Confirm: true, ExpectedReferenceFingerprint: status.ReferenceFingerprint}
}
func TestMCPCredentialLifecycleIsMetadataOnlyAndDoesNotReview(t *testing.T) {
	record := mcpCredentialRecord("first")
	registry := &mcpCredentialRegistryFake{[]mcp.ServerRecord{record}}
	owned := credential.NewMemoryStore()
	service := NewMCPCredentialService(registry, owned)
	request := mcpCredentialChange(t, service, mcpCredentialBinding(record))
	for _, action := range []string{"set", "set", "delete"} {
		request.Action = action
		if action == "delete" {
			request.Secret = ""
		}
		status, err := service.Change(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if status.Configured != (action == "set") || status.PlaintextReturned || registry.records[0].State != mcp.TrustStaged {
			t.Fatal("presence or review changed incorrectly")
		}
		raw, _ := json.Marshal(status)
		if strings.Contains(string(raw), "synthetic-bearer") || strings.Contains(string(raw), `"secret"`) {
			t.Fatal("secret entered status")
		}
	}
}
func TestMCPCredentialRejectsBindingAndActionDriftBeforeStoreWrite(t *testing.T) {
	for _, kind := range []string{"workspace", "run", "target", "reference", "fingerprint", "confirm", "action", "invalid-secret", "delete-secret"} {
		t.Run(kind, func(t *testing.T) {
			record := mcpCredentialRecord("first")
			owned := credential.NewMemoryStore()
			service := NewMCPCredentialService(&mcpCredentialRegistryFake{[]mcp.ServerRecord{record}}, owned)
			request := mcpCredentialChange(t, service, mcpCredentialBinding(record))
			switch kind {
			case "workspace":
				request.Binding.WorkspaceID = "other"
			case "run":
				request.Binding.RunID = "other"
			case "target":
				request.Binding.Target = "https://other.invalid/mcp"
			case "reference":
				request.Binding.CredentialRef = "other"
			case "fingerprint":
				request.Binding.ExpectedDescriptorFingerprint = strings.Repeat("a", 64)
			case "confirm":
				request.Confirm = false
			case "action":
				request.Action = "authenticate"
			case "invalid-secret":
				request.Secret = " whitespace"
			case "delete-secret":
				request.Action = "delete"
			}
			if _, err := service.Change(t.Context(), request); err == nil {
				t.Fatal("drift accepted")
			}
			if configured, _ := owned.Configured(t.Context(), record.Descriptor.CredentialRef); configured {
				t.Fatal("rejected request wrote secret")
			}
		})
	}
}
func TestMCPCredentialSharedReferencesRequireCurrentAcknowledgmentAndSameEndpoint(t *testing.T) {
	record := mcpCredentialRecord("first")
	registry := &mcpCredentialRegistryFake{[]mcp.ServerRecord{record}}
	owned := credential.NewMemoryStore()
	service := NewMCPCredentialService(registry, owned)
	request := mcpCredentialChange(t, service, mcpCredentialBinding(record))
	other := mcpCredentialRecord("second")
	other.Descriptor.WorkspaceID = "workspace-two"
	other.DescriptorFingerprint = other.Descriptor.Fingerprint()
	registry.records = append(registry.records, other)
	if _, err := service.Change(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatal("stale sharing acknowledgment accepted")
	}
	request = mcpCredentialChange(t, service, request.Binding)
	status, err := service.Change(t.Context(), request)
	if err != nil || status.RegistrationCount != 2 {
		t.Fatalf("same-endpoint sharing failed: %v", err)
	}
	other.Descriptor.Target = "https://different.invalid/mcp"
	other.DescriptorFingerprint = other.Descriptor.Fingerprint()
	registry.records[1] = other
	request = mcpCredentialChange(t, service, request.Binding)
	request.Action, request.Secret = "delete", ""
	if _, err := service.Change(t.Context(), request); apperror.CodeOf(err) != apperror.CodeConflict {
		t.Fatal("different endpoints shared deletion accepted")
	}
	if configured, _ := owned.Configured(t.Context(), record.Descriptor.CredentialRef); !configured {
		t.Fatal("conflicting deletion removed shared token")
	}
}

type failingMCPCredentialStore struct {
	*credential.MemoryStore
	unavailable  bool
	failPresence bool
}

func (s failingMCPCredentialStore) Available() bool { return !s.unavailable }
func (s failingMCPCredentialStore) Put(context.Context, string, string) error {
	return errors.New("synthetic-bearer-only backend error")
}
func (s failingMCPCredentialStore) Configured(ctx context.Context, name string) (bool, error) {
	if s.failPresence {
		return false, errors.New("synthetic-bearer-only presence error")
	}
	return s.MemoryStore.Configured(ctx, name)
}
func TestMCPCredentialUnsupportedStoreAndBackendErrorsNeverExposeSecrets(t *testing.T) {
	record := mcpCredentialRecord("first")
	registry := &mcpCredentialRegistryFake{[]mcp.ServerRecord{record}}
	for _, kind := range []string{"unsupported", "write-error", "presence-error"} {
		t.Run(kind, func(t *testing.T) {
			service := NewMCPCredentialService(registry, failingMCPCredentialStore{MemoryStore: credential.NewMemoryStore(), unavailable: kind == "unsupported", failPresence: kind == "presence-error"})
			binding := mcpCredentialBinding(record)
			status, err := service.Status(t.Context(), binding)
			if kind == "unsupported" && (err != nil || status.StoreAvailable || status.Configured) {
				t.Fatal("unsupported store reported usable presence")
			}
			if kind != "presence-error" {
				request := mcpCredentialChange(t, service, binding)
				_, err = service.Change(t.Context(), request)
			}
			if err == nil || strings.Contains(err.Error(), "synthetic-bearer") {
				t.Fatalf("missing or unsafe backend failure: %v", err)
			}
		})
	}
}
