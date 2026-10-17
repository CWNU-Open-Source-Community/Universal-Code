package commandruntimeadapter

import (
	"reflect"
	"testing"

	"cyberagent-workbench/internal/domain"
)

func TestAdapterPermissionMatrixFailsClosed(t *testing.T) {
	sandboxed := SandboxedWorkspace("local_windows_lpac", "local-windows-lpac.v1", "local-generation-1")
	host := HostUnsandboxed("host-generation-1")
	legacy := LegacyUnbound()
	for name, test := range map[string]struct {
		identity   Identity
		permission domain.RunExecutionPermissionMode
		allowed    bool
		executable bool
	}{
		"sandbox workspace":   {sandboxed, domain.RunExecutionPermissionWorkspaceAccess, true, true},
		"sandbox full access": {sandboxed, domain.RunExecutionPermissionFullAccess, false, true},
		"sandbox debug":       {sandboxed, domain.RunExecutionPermissionDebug, false, true},
		"host full access":    {host, domain.RunExecutionPermissionFullAccess, true, true},
		"host debug":          {host, domain.RunExecutionPermissionDebug, true, true},
		"host workspace":      {host, domain.RunExecutionPermissionWorkspaceAccess, false, true},
		"legacy full access":  {legacy, domain.RunExecutionPermissionFullAccess, false, false},
		"sandbox ask":         {sandboxed, domain.RunExecutionPermissionAsk, true, true},
		"sandbox auto":        {sandboxed, domain.RunExecutionPermissionAuto, true, true},
		"sandbox full":        {sandboxed, domain.RunExecutionPermissionFull, true, true},
		"host ask":            {host, domain.RunExecutionPermissionAsk, true, true},
		"host auto":           {host, domain.RunExecutionPermissionAuto, true, true},
		"host full":           {host, domain.RunExecutionPermissionFull, true, true},
		"legacy ask":          {legacy, domain.RunExecutionPermissionAsk, false, false},
		"legacy auto":         {legacy, domain.RunExecutionPermissionAuto, false, false},
		"legacy full":         {legacy, domain.RunExecutionPermissionFull, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := test.identity.AllowsPermission(test.permission); got != test.allowed {
				t.Fatalf("AllowsPermission() = %v, want %v", got, test.allowed)
			}
			if got := test.identity.Executable(); got != test.executable {
				t.Fatalf("Executable() = %v, want %v", got, test.executable)
			}
		})
	}
}

func TestAdapterReceiptCannotMasqueradeAsSandboxed(t *testing.T) {
	receipt := HostUnsandboxed("host-generation-1")
	receipt.IsolationGrade = IsolationWorkspaceSandbox
	receipt.NetworkPolicy = NetworkDenied
	receipt.CredentialPolicy = CredentialsNone
	if receipt.Validate() == nil {
		t.Fatal("host adapter receipt must not validate as a sandbox receipt")
	}
}

func TestAdapterGenerationIsPartOfAuthorityIdentity(t *testing.T) {
	first := HostUnsandboxed("host-generation-1")
	second := HostUnsandboxed("host-generation-2")
	if first.SameBackend(second) {
		t.Fatal("adapter replacement must change the bound identity")
	}
	if authority := NewAuthority("run-adapter-test", first); authority.Validate() != nil {
		t.Fatalf("authority validation failed: %v", authority.Validate())
	}
}

func TestCommandRuntimeAuthorityBindsOptionalLiveFullAccessGrant(t *testing.T) {
	authority := NewAuthority("run-adapter-test",
		HostUnsandboxed("host-generation-1"))
	authority.PermissionSnapshotID = "permission-full-test"
	authority.PermissionGeneration = 7
	authority.PermissionRuntimeEpoch = "process-epoch-test"
	encoded, err := EncodeAuthority(authority)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeAuthority(encoded)
	if err != nil || !reflect.DeepEqual(decoded, authority) {
		t.Fatalf("live Full Access authority=%+v err=%v", decoded, err)
	}
	partial := authority
	partial.PermissionRuntimeEpoch = ""
	if partial.Validate() == nil {
		t.Fatal("partial live Full Access grant authority was accepted")
	}
}

func TestCommandRuntimeWorkerAuthorityPreservesActor(t *testing.T) {
	authority := Authority{ProtocolVersion: OperationAuthorityVersion, RunID: "run-worker",
		Adapter: HostUnsandboxed("host-generation-1"), PermissionMode: domain.RunExecutionPermissionAuto,
		PermissionSnapshotID: "permission-worker", PermissionRevision: 1,
		Worker: &WorkerAuthority{AgentID: "child-worker", AttemptID: "attempt-worker", SessionID: "session-worker"}}
	encoded, err := EncodeAuthority(authority)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeAuthority(encoded)
	if err != nil || !reflect.DeepEqual(decoded, authority) {
		t.Fatalf("worker authority=%+v err=%v", decoded, err)
	}
	invalid := authority
	invalid.Worker = &WorkerAuthority{AgentID: authority.Worker.AgentID, SessionID: authority.Worker.SessionID}
	if invalid.Validate() == nil {
		t.Fatal("worker authority accepted a missing original attempt")
	}
	legacy := NewAuthority("run-worker", authority.Adapter)
	legacy.Worker = authority.Worker
	if legacy.Validate() == nil {
		t.Fatal("legacy authority accepted child execution")
	}
}
