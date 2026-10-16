package mcp

import (
	"context"
	"path/filepath"
	"testing"

	"cyberagent-workbench/internal/apperror"
)

type computerUseStoreFake struct{ clientStoreFake }

func (s *computerUseStoreFake) GetMCPClientServer(ctx context.Context, id string) (ServerRecord, error) {
	r, err := s.clientStoreFake.GetMCPClientServer(ctx, id)
	if err != nil {
		return r, apperror.New(apperror.CodeNotFound, "server not found")
	}
	return r, nil
}

func TestComputerUseCatalogRequiresHostRegistrationAndPreservesDisable(t *testing.T) {
	st := &computerUseStoreFake{}
	manager, err := NewClientManager(st, nil, ManagerOptions{ComputerUseExecutable: filepath.Join(t.TempDir(), "host.exe")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	caps, err := manager.Capabilities(t.Context(), "run-one", "workspace-one")
	if err != nil || len(caps.Servers) != 1 || len(caps.Servers[0].Tools) < 7 {
		t.Fatalf("shipped catalog unavailable: %+v %v", caps, err)
	}
	record, err := st.GetMCPClientServer(t.Context(), caps.Servers[0].ServerID)
	if err != nil || record.Validate() != nil || !IsComputerUseServer(record.Descriptor) {
		t.Fatalf("invalid builtin record: %+v %v", record, err)
	}
	if _, _, err := manager.Stage(t.Context(), record.Descriptor); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("manual staging impersonated builtin: %v", err)
	}
	manual := record.Descriptor
	manual.Source = Source{Kind: "manual", URI: "operator://manual"}
	manual.Target = filepath.Join(t.TempDir(), "other.exe")
	if _, _, err := manager.Stage(t.Context(), manual); apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("manual staging occupied builtin ID: %v", err)
	}
	_, err = manager.Invoke(t.Context(), InvokeRequest{RunID: "run-one", WorkspaceID: "workspace-one",
		ServerID: record.Descriptor.ID, ToolName: "computer_list_windows", CapabilityFingerprint: record.Capabilities.Fingerprint,
		Arguments: []byte(`{}`)})
	if apperror.CodeOf(err) != apperror.CodePolicyDenied {
		t.Fatalf("builtin invocation without host authority was accepted: %v", err)
	}
	_, err = manager.Review(t.Context(), record.Descriptor.ID, ReviewRequest{Action: ReviewDisable,
		ExpectedDescriptorFingerprint: record.DescriptorFingerprint, ReviewedBy: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	caps, err = manager.Capabilities(t.Context(), "run-one", "workspace-one")
	if err != nil || len(caps.Servers) != 0 {
		t.Fatalf("disabled builtin reappeared: %+v %v", caps, err)
	}
	withoutHost, err := NewClientManager(st, nil, ManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = withoutHost.Close() })
	if err = withoutHost.EnsureComputerUse(t.Context(), "workspace-two"); err != nil {
		t.Fatal(err)
	}
	if _, err = st.GetMCPClientServer(t.Context(), ComputerUseServerID("workspace-two")); apperror.CodeOf(err) != apperror.CodeNotFound {
		t.Fatalf("host without executor registered a desktop service: %v", err)
	}
}

func TestComputerUseDescriptorCannotChooseAnExecutableOrScope(t *testing.T) {
	base := computerUseDescriptor("workspace")
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	changes := []func(*ServerDescriptor){
		func(d *ServerDescriptor) { d.Target = filepath.Join(t.TempDir(), "other.exe") },
		func(d *ServerDescriptor) { d.Arguments = []string{"--other"} },
		func(d *ServerDescriptor) { d.Source.URI = "operator://forged" },
		func(d *ServerDescriptor) { d.Source.Version = "2" },
		func(d *ServerDescriptor) { d.WorkspaceID = "other-workspace" },
		func(d *ServerDescriptor) { d.Scope, d.RunID = ScopeRun, "run-one" },
		func(d *ServerDescriptor) { d.Env = map[string]string{"PATH": "injected"} },
	}
	for i, change := range changes {
		d := base
		change(&d)
		if IsComputerUseServer(d) || d.Validate() == nil {
			t.Fatalf("mutation %d admitted an untrusted builtin", i)
		}
	}
}
