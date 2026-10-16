package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/sandbox"
)

type sbxApplicationStore struct {
	workspace   runworktree.Workspace
	profile     domain.RunExecutionProfileSnapshot
	permission  domain.RunExecutionPermissionSnapshot
	interaction domain.RunExecutionInteractionSnapshot
	lease       domain.RunExecutionLease
}

func (s *sbxApplicationStore) GetRunExecutionProfile(context.Context, string) (domain.RunExecutionProfileSnapshot, error) {
	return s.profile, nil
}
func (s *sbxApplicationStore) GetRunExecutionPermission(context.Context, string) (domain.RunExecutionPermissionSnapshot, error) {
	return s.permission, nil
}
func (s *sbxApplicationStore) GetRunExecutionInteraction(context.Context, string) (domain.RunExecutionInteractionSnapshot, error) {
	return s.interaction, nil
}
func (s *sbxApplicationStore) GetRunExecutionLease(context.Context, string) (domain.RunExecutionLease, bool, error) {
	return s.lease, true, nil
}
func (s *sbxApplicationStore) GetDrydockByRun(context.Context, string) (runworktree.Workspace, bool, error) {
	return s.workspace, true, nil
}

type sbxApplicationTransport struct {
	name        string
	mounts      []string
	verbs       []string
	afterCreate func()
}

func (f *sbxApplicationTransport) Run(_ context.Context, r sandbox.SBXProcessRequest) (sandbox.SBXProcessResult, error) {
	args := r.Arguments
	if len(args) < 3 || args[0] != "--app-name" || args[1] != sandbox.SBXAppName {
		return sandbox.SBXProcessResult{ExitCode: -1}, errors.New("missing fixed namespace")
	}
	args = args[2:]
	f.verbs = append(f.verbs, args[0])
	value := sandbox.SBXProcessResult{ExitCode: 0}
	switch args[0] {
	case "version":
		value.Stdout = []byte("sbx version v0.47.0")
	case "settings":
		value.Stdout = []byte("false")
	case "ls":
		entries := []map[string]any{}
		if f.name != "" {
			entries = append(entries, map[string]any{"id": "app-owned-id", "name": f.name, "workspaces": f.mounts})
		}
		value.Stdout, _ = json.Marshal(map[string]any{"sandboxes": entries})
	case "create":
		f.name = args[slices.Index(args, "--name")+1]
		f.mounts = append([]string{}, args[len(args)-2:]...)
		if f.afterCreate != nil {
			f.afterCreate()
		}
	case "exec":
		value.Stdout = []byte("verified-guest-process")
	case "policy", "stop":
	case "rm":
		f.name = ""
	default:
		return value, errors.New("unexpected CLI invocation")
	}
	return value, nil
}

// The application contract is tested with an explicitly audited fake backend.
// Production SBX remains unavailable until its MCP isolation is provable. The
// sandbox package separately tests the real lifecycle compiler with fake CLI.
type sbxApplicationBackend struct {
	*sandbox.SBXBackend
	transport          *sbxApplicationTransport
	cleanupUnconfirmed bool
}

func (b *sbxApplicationBackend) Readiness(context.Context) (sandbox.SBXReadiness, error) {
	at := time.Now().UTC()
	return sandbox.SBXReadiness{ProtocolVersion: sandbox.SBXReadinessProtocolVersion, Status: "ready", ReasonCode: "ready", Ready: true,
		FeatureEnabled: true, CLIInstalled: true, TemplateConfigured: true, DaemonReachable: true, CredentialIsolationProven: true, MCPIsolationProven: true,
		Generation: b.Generation(), EvidenceFingerprint: strings.Repeat("e", 64), CheckedAt: at, ExpiresAt: at.Add(30 * time.Second)}, nil
}
func (b *sbxApplicationBackend) Run(ctx context.Context, r sandbox.SBXRunRequest, _ io.Reader) (sandbox.SBXExecutionResult, error) {
	result := sandbox.SBXExecutionResult{ExitCode: 125, TreeReaped: true}
	if err := r.AuthorityCheck(ctx); err != nil {
		return result, err
	}
	b.transport.verbs = append(b.transport.verbs, "create")
	if b.transport.afterCreate != nil {
		b.transport.afterCreate()
	}
	err := r.AuthorityCheck(ctx)
	if err == nil && r.BeforeExec != nil {
		err = r.BeforeExec(ctx)
	}
	if err == nil {
		b.transport.verbs = append(b.transport.verbs, "exec")
		result.ExitCode = 0
		result.Stdout = []byte("verified-guest-process")
	}
	b.transport.verbs = append(b.transport.verbs, "stop", "rm")
	if b.cleanupUnconfirmed {
		result.TreeReaped = false
		return result, errors.Join(err, sandbox.ErrSBXCleanup)
	}
	return result, err
}

func sbxApplicationFixture(t *testing.T) (*SBXCommandRuntimeExecutor, *sbxApplicationStore, *sbxApplicationTransport, runner.CommandRuntimeScope, runner.CommandRuntimeResolvedSpec) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cli, journal, drydock := filepath.Join(root, "sbx-fixture.exe"), filepath.Join(root, "journal"), filepath.Join(root, "drydock")
	if err := os.WriteFile(cli, []byte("fake sbx transport only"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{journal, drydock} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(drydock, ".git"), []byte("gitdir: /private/admin\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fake := &sbxApplicationTransport{}
	backend, err := sandbox.NewSBXBackend(sandbox.SBXBackendConfig{Enabled: true, ExecutablePath: cli, TemplateReference: "example/toolchain@sha256:" + strings.Repeat("a", 64), JournalRoot: journal}, sandbox.WithSBXProcessTransport(fake))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	at := time.Now().UTC()
	state := &sbxApplicationStore{workspace: runworktree.Workspace{ID: "drydock-1", RunID: "run-1", MissionID: "mission-1", SessionID: "session-1", SourceWorkspaceID: "source-1", Path: drydock, State: runworktree.StateReady},
		profile:    domain.RunExecutionProfileSnapshot{ID: "profile-1", RunID: "run-1", MissionID: "mission-1", Revision: 1, Profile: domain.RunExecutionProfileSBX},
		permission: domain.RunExecutionPermissionSnapshot{ID: "permission-1", RunID: "run-1", MissionID: "mission-1", Revision: 1, Mode: domain.RunExecutionPermissionAsk},
		interaction: domain.RunExecutionInteractionSnapshot{ID: "interaction-1", RunID: "run-1", MissionID: "mission-1", Revision: 1, ProtocolVersion: domain.RunExecutionInteractionProtocolVersion,
			Mode: domain.RunExecutionInteractionControlled, Surface: domain.ExecutionSurfaceCode, ExecutionProfile: domain.RunExecutionProfileSBX, ExecutionProfileRevision: 1, WorkspaceTrust: domain.WorkspaceTrustTrusted,
			CommandForm: domain.ExecutionCommandStructuredArgv, NetworkScope: domain.ExecutionNetworkDisabled, RequiredGate: domain.ExecutionInteractionGateSBXMicroVM, PolicyVersion: domain.RunExecutionInteractionPolicyVersion,
			OperatorConfirmed: true, RequestedBy: "operator", Reason: "SBX test", CreatedAt: at},
		lease: domain.RunExecutionLease{RunID: "run-1", LeaseID: "lease-1", OwnerID: "owner-1", Generation: 1, Status: domain.RunExecutionLeaseActive, AcquiredAt: at, RenewedAt: at, ExpiresAt: at.Add(time.Minute)}}
	executor, err := NewSBXCommandRuntimeExecutor(state, &sbxApplicationBackend{SBXBackend: backend, transport: fake})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := executor.NormalizeCommandRuntimeSpec(runner.CommandRuntimeSpec{Version: runner.CommandRuntimeProtocolVersion, Profile: runner.CommandRuntimeProcess,
		Executable: "/usr/bin/printf", Arguments: []string{"literal-argv"}, WorkingDirectory: ".", Environment: []runner.CommandRuntimeEnvironment{},
		StdinPolicy: runner.CommandRuntimeStdinClosed, CloseInitialStdin: true, TimeoutMilliseconds: 1000, Output: runner.CommandRuntimeOutputPolicy{InlineBytes: 4096, ArtifactBytes: 4096},
		Network: runner.CommandRuntimeNetworkDisabled, Credentials: runner.CommandRuntimeCredentialsNone, Purpose: "verify owned SBX process"}, drydock)
	if err != nil {
		t.Fatal(err)
	}
	scope := runner.CommandRuntimeScope{InvocationID: "invocation-1", OperationKey: "operation-1", RunID: "run-1", MissionID: "mission-1", RootAgentID: "root-1", AgentID: "root-1",
		AttributionSource: domain.AgentAttributionOperatorRoot, SessionID: "session-1", WorkspaceID: "source-1", WorkspaceRootSHA256: spec.WorkspaceRootSHA256,
		ModeSnapshotID: "mode-1", ModeRevision: 1, ProfileSnapshotID: "profile-1", ProfileRevision: 1, PermissionSnapshotID: "permission-1", PermissionRevision: 1,
		PermissionMode: domain.RunExecutionPermissionAsk, LeaseID: "lease-1", LeaseGeneration: 1, LeaseOwnerID: "owner-1", Adapter: executor.Identity()}
	if err := scope.Validate(); err != nil {
		t.Fatal(err)
	}
	return executor, state, fake, scope, spec
}

func TestSBXCommandNormalizationBindsGuestTemplateWithoutHostExecutable(t *testing.T) {
	e, _, _, _, spec := sbxApplicationFixture(t)
	if spec.ExecutablePath != "/usr/bin/printf" || spec.ExecutableIdentityKind != runner.CommandRuntimeExecutableTemplatePathSHA256 || !spec.ExecutablePinned || spec.ProfileStartupFiles || spec.EnvironmentInherited {
		t.Fatalf("guest spec=%+v", spec)
	}
	if err := runner.ValidateCommandRuntimeLaunchSpec(spec); err == nil {
		t.Fatal("guest reference was accepted as a native host binary")
	}
	changed, err := NormalizeSBXCommandRuntimeSpec(spec.Spec, spec.WorkspaceRoot, "example/toolchain@sha256:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if changed.ExecutableSHA256 == spec.ExecutableSHA256 || runner.CommandRuntimeSpecFingerprint(changed) == runner.CommandRuntimeSpecFingerprint(spec) {
		t.Fatal("template pin did not change reviewed identity")
	}
	for _, guest := range []string{"/usr/bin/bash", "/bin/sh", "/usr/bin/env", "/workspace/tool", "--cloud", "//usr/bin/node"} {
		input := spec.Spec
		input.Executable = guest
		if _, err := e.NormalizeCommandRuntimeSpec(input, spec.WorkspaceRoot); err == nil {
			t.Fatalf("accepted guest launcher: %s", guest)
		}
	}
}

func TestSBXCommandExecutorBindsAuthorityAndCompleteVMReceipt(t *testing.T) {
	e, _, fake, scope, spec := sbxApplicationFixture(t)
	value, err := e.ExecuteSandboxCommand(context.Background(), scope, spec, nil)
	if err != nil || value.Validate(spec.Spec.Output.ArtifactBytes) != nil || string(value.Stdout) != "verified-guest-process" || !slices.Contains(fake.verbs, "rm") {
		t.Fatalf("value=%+v error=%v", value, err)
	}
}

func TestSBXCommandExecutorRechecksPermissionAtGuestDispatch(t *testing.T) {
	e, state, fake, scope, spec := sbxApplicationFixture(t)
	fake.afterCreate = func() { state.permission.Revision++ }
	_, err := e.ExecuteSandboxCommand(context.Background(), scope, spec, nil)
	if !errors.Is(err, runner.ErrCommandRuntimeBoundary) || slices.Contains(fake.verbs, "exec") || !slices.Contains(fake.verbs, "rm") {
		t.Fatalf("revoked authority reached guest: calls=%v error=%v", fake.verbs, err)
	}
}

func TestSBXCommandExecutorRejectsSourceWorkspaceAndDriftBeforeCreate(t *testing.T) {
	for _, kind := range []string{"foreign-run", "permission-revision", "lease-generation", "expired-lease", "native-identity", "environment-drift"} {
		t.Run(kind, func(t *testing.T) {
			e, state, fake, scope, spec := sbxApplicationFixture(t)
			switch kind {
			case "foreign-run":
				state.workspace.RunID = "foreign-run"
			case "permission-revision":
				state.permission.Revision++
			case "lease-generation":
				state.lease.Generation++
			case "expired-lease":
				state.lease.ExpiresAt = time.Now().Add(-time.Second)
			case "native-identity":
				spec.ExecutableIdentityKind = ""
			case "environment-drift":
				spec.Environment = append(spec.Environment, "HOST_SECRET=unsafe")
			}
			if _, err := e.ExecuteSandboxCommand(context.Background(), scope, spec, nil); err == nil || slices.Contains(fake.verbs, "create") {
				t.Fatalf("boundary drift created VM: calls=%v error=%v", fake.verbs, err)
			}
		})
	}
}

func TestSBXCommandExecutorRejectsAttachmentMountExpansion(t *testing.T) {
	e, _, fake, scope, spec := sbxApplicationFixture(t)
	spec.AttachmentInput = &runner.CommandRuntimeAttachmentInput{}
	if _, err := e.ExecuteSandboxCommand(context.Background(), scope, spec, nil); err == nil || slices.Contains(fake.verbs, "create") {
		t.Fatalf("unexpected attachment mount: %v", err)
	}
}

func TestSBXCommandExecutorPreservesUnconfirmedVMCleanup(t *testing.T) {
	e, _, fake, scope, spec := sbxApplicationFixture(t)
	e.backend.(*sbxApplicationBackend).cleanupUnconfirmed = true
	value, err := e.ExecuteSandboxCommand(context.Background(), scope, spec, nil)
	if !errors.Is(err, sandbox.ErrSBXCleanup) || value.TreeReaped || !slices.Contains(fake.verbs, "exec") || !slices.Contains(fake.verbs, "rm") {
		t.Fatalf("uncertain VM removal became a reaped process: result=%+v calls=%v error=%v", value, fake.verbs, err)
	}
}

func TestSBXCommandExecutorPreDispatchFailureHasNoOwnedProcess(t *testing.T) {
	e, _, fake, scope, spec := sbxApplicationFixture(t)
	spec.ExecutableIdentityKind = ""
	value, err := e.ExecuteSandboxCommand(context.Background(), scope, spec, nil)
	if !errors.Is(err, runner.ErrCommandRuntimeBoundary) || !value.TreeReaped || value.ExitCode != 125 || slices.Contains(fake.verbs, "create") {
		t.Fatalf("pre-dispatch boundary changed cleanup proof: result=%+v calls=%v error=%v", value, fake.verbs, err)
	}
}
