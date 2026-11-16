package application_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cyberagent-workbench/internal/application"
	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runworktree"
	"cyberagent-workbench/internal/store"
)

func TestRunCapabilityReadinessProjectsStableFailureReasons(t *testing.T) {
	ctx, state, run := newCapabilityReadinessRun(t, "code")
	runtime := application.CapabilityReadinessRuntime{
		RunControlEnabled: true, ExecutionPermissionControlEnabled: true,
	}
	projection, err := application.NewRunCapabilityReadinessService(state, runtime).
		Project(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if projection.ProtocolVersion != application.RunCapabilityReadinessProtocolVersion ||
		projection.RunID != run.ID || projection.CapabilityGrant || projection.Validate() != nil {
		t.Fatalf("invalid readiness envelope: %#v", projection)
	}
	// Approval preferences do not establish an installed sandbox. Check its
	// real proof below in the Local profile, independently of Ask/Auto.
	for _, mode := range []string{"ask", "auto"} {
		option := readinessOption(t, projection.Permissions, mode)
		if !option.Selectable || !option.RuntimeAvailable || option.RestartRequired {
			t.Fatalf("approval preference was coupled to sandbox installation: %#v", option)
		}
	}
	full := readinessOption(t, projection.Permissions, "full")
	assertReadinessBlockers(t, full, application.CapabilityBlockerStartupGateClosed)
	if full.Selectable || full.RuntimeAvailable || !full.RestartRequired {
		t.Fatalf("Full bypassed its runtime gate: %#v", full)
	}
	local := readinessOption(t, projection.Profiles, "local")
	assertReadinessBlockers(t, local,
		application.CapabilityBlockerStartupGateClosed,
		application.CapabilityBlockerWorkspaceUntrusted,
		application.CapabilityBlockerSandboxUnproven)
	if !local.Selectable || local.RuntimeAvailable {
		t.Fatalf("Local intent must remain selectable without claiming runtime: %#v", local)
	}
	docker := readinessOption(t, projection.Profiles, "docker")
	assertReadinessBlockers(t, docker,
		application.CapabilityBlockerStartupGateClosed,
		application.CapabilityBlockerWorkspaceUntrusted,
		application.CapabilityBlockerDockerUnavailable)
	if !docker.Selectable || docker.RuntimeAvailable {
		t.Fatalf("Docker intent must remain selectable without claiming runtime: %#v", docker)
	}
	controlled := readinessOption(t, projection.Interactions, "controlled")
	assertReadinessBlockers(t, controlled,
		application.CapabilityBlockerStartupGateClosed,
		application.CapabilityBlockerProfileMismatch,
		application.CapabilityBlockerWorkspaceUntrusted,
		application.CapabilityBlockerSandboxUnproven)
	if controlled.Selectable || controlled.RuntimeAvailable {
		t.Fatalf("controlled interaction ignored its profile mismatch: %#v", controlled)
	}
	cyber := readinessOption(t, projection.Interactions, "cyber")
	assertReadinessBlockers(t, cyber,
		application.CapabilityBlockerStartupGateClosed,
		application.CapabilityBlockerSurfaceMismatch,
		application.CapabilityBlockerProfileMismatch,
		application.CapabilityBlockerWorkspaceUntrusted,
		application.CapabilityBlockerDockerUnavailable)
	fullCDP := readinessOption(t, projection.BrowserCDPPermissions, "full_debug")
	assertReadinessBlockers(t, fullCDP,
		application.CapabilityBlockerStartupGateClosed,
		application.CapabilityBlockerPermissionMismatch)
	preset := readinessOption(t, projection.Presets, application.StandardCodePresetValue)
	if !hasReadinessBlocker(preset, application.CapabilityBlockerCapabilityUnimplemented) ||
		preset.Selectable || preset.RuntimeAvailable || preset.Selected {
		t.Fatalf("Standard Code preset did not fail closed: %#v", preset)
	}
}

func TestRunCapabilityReadinessLocalHostDoesNotRequireLPACOrInteractionTrust(t *testing.T) {
	ctx, state, run := newCapabilityReadinessRun(t, "code")
	runtime := application.CapabilityReadinessRuntime{RunControlEnabled: true, RunExecutionEnabled: true, ExecutionPermissionControlEnabled: true,
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
			RuntimeAuthority: domain.NewExecutionPermissionRuntimeAuthority()},
		CommandRuntimeAdapters: []commandruntimeadapter.Identity{commandruntimeadapter.HostUnsandboxed("host-default-readiness")}}
	projection, err := application.NewRunCapabilityReadinessService(state, runtime).Project(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	local := readinessOption(t, projection.Profiles, "local")
	if !local.RuntimeAvailable || !local.Selectable || len(local.BlockedBy) != 0 {
		t.Fatalf("installed ordinary host execution was mislabeled as unavailable LPAC: %+v", local)
	}
	// Controlled/Debug interaction remains a separate sandbox/trust contract.
	controlled := readinessOption(t, projection.Interactions, "controlled")
	if controlled.RuntimeAvailable || !hasReadinessBlocker(controlled, application.CapabilityBlockerSandboxUnproven) {
		t.Fatalf("host command readiness granted controlled sandbox interaction: %+v", controlled)
	}
	owned, err := application.NewRunCapabilityReadinessService(readinessUnprovenOwnedStore{state}, runtime).Project(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if readinessOption(t, owned.Profiles, "local").RuntimeAvailable {
		t.Fatal("owned workspace without sandbox proof fell back to an installed host adapter")
	}
}

type readinessUnprovenOwnedStore struct{ *store.SQLiteStore }

func (readinessUnprovenOwnedStore) GetRunFileDrydock(_ context.Context, runID string) (runworktree.Workspace, bool, error) {
	return runworktree.Workspace{ID: "unproven-readiness-owner", RunID: runID, State: runworktree.StateReady}, true, nil
}

func TestRunCapabilityReadinessDistinguishesRunningPausedAndActiveLease(t *testing.T) {
	ctx, state, run := newCapabilityReadinessRun(t, "code")
	runtime := readyCapabilityReadinessRuntime()
	service := application.NewRunCapabilityReadinessService(state, runtime)
	runService := application.NewRunService(state)
	if _, err := runService.Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	running, err := service.Project(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	preview := readinessOption(t, running.Profiles, "preview")
	assertReadinessBlockers(t, preview, application.CapabilityBlockerRunNotQuiescent)
	if preview.Selectable || !preview.RuntimeAvailable || !preview.Selected {
		t.Fatalf("running selected Preview was conflated with runtime failure: %#v", preview)
	}
	restrictedCDP := readinessOption(t, running.BrowserCDPPermissions, "restricted")
	if !restrictedCDP.Selectable {
		t.Fatalf("running Run could not immediately select the restrictive CDP mode: %#v",
			restrictedCDP)
	}
	if _, err := runService.Pause(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	paused, err := service.Project(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	preview = readinessOption(t, paused.Profiles, "preview")
	if !preview.Selectable || !preview.RuntimeAvailable || len(preview.BlockedBy) != 0 {
		t.Fatalf("paused Preview did not become selectable: %#v", preview)
	}
	lease, err := state.AcquireRunExecutionLease(ctx, domain.AcquireRunExecutionLeaseRequest{
		RunID: run.ID, OwnerID: "readiness-test-owner",
		TTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Lease.Status != domain.RunExecutionLeaseActive {
		t.Fatalf("unexpected lease: %#v", lease)
	}
	leased, err := service.Project(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	preview = readinessOption(t, leased.Profiles, "preview")
	assertReadinessBlockers(t, preview, application.CapabilityBlockerExecutionLeaseActive)
	if preview.Selectable || !preview.RuntimeAvailable {
		t.Fatalf("active lease was not separated from runtime readiness: %#v", preview)
	}
	conservative := readinessOption(t, leased.Permissions, "ask")
	if !conservative.Selectable || !conservative.RuntimeAvailable {
		t.Fatalf("permission revision must remain selectable so it can revoke authority: %#v",
			conservative)
	}
	restrictedCDP = readinessOption(t, leased.BrowserCDPPermissions, "restricted")
	if !restrictedCDP.Selectable ||
		hasReadinessBlocker(restrictedCDP,
			application.CapabilityBlockerExecutionLeaseActive) {
		t.Fatalf("active lease blocked an immediate restrictive CDP downgrade: %#v",
			restrictedCDP)
	}
}

func TestRunCapabilityReadinessAllowsFullCDPInsideLiveFullAccessOnly(t *testing.T) {
	ctx, state, run := newCapabilityReadinessRun(t, "code")
	authority := domain.NewExecutionPermissionRuntimeAuthority()
	executionCapabilities := domain.ExecutionPermissionRuntimeCapabilities{
		WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true,
		DangerFullAccessEnabled: true, RuntimeAuthority: authority,
	}
	permissionService := application.NewRunExecutionPermissionService(
		state, executionCapabilities)
	if _, err := permissionService.Change(ctx,
		application.ChangeRunExecutionPermissionRequest{
			RunID: run.ID, Mode: string(domain.RunExecutionPermissionFull),
			OperationKey: "readiness-live-full-access-0001", RequestedBy: "test_operator",
			Reason:      "activate current Run Full Access for Full CDP",
			ConfirmFull: true,
		}); err != nil {
		t.Fatal(err)
	}
	runtime := readyCapabilityReadinessRuntime()
	runtime.ExecutionPermissionCapabilities = executionCapabilities
	service := application.NewRunCapabilityReadinessService(state, runtime)
	live, err := service.Project(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	fullCDP := readinessOption(t, live.BrowserCDPPermissions, "full_debug")
	if !fullCDP.Selectable || !fullCDP.RuntimeAvailable {
		t.Fatalf("live Full Access did not admit its Full CDP sub-capability: %#v", fullCDP)
	}

	authority.RevokeRun(run.ID)
	stale, err := service.Project(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	fullPermission := readinessOption(t, stale.Permissions, "full")
	if !fullPermission.Selectable || fullPermission.RuntimeAvailable ||
		!hasReadinessBlocker(fullPermission,
			application.CapabilityBlockerPermissionMismatch) {
		t.Fatalf("cold historical Full Access was not offered safe re-confirmation: %#v",
			fullPermission)
	}
	fullCDP = readinessOption(t, stale.BrowserCDPPermissions, "full_debug")
	if fullCDP.Selectable || fullCDP.RuntimeAvailable ||
		!hasReadinessBlocker(fullCDP,
			application.CapabilityBlockerPermissionMismatch) {
		t.Fatalf("Full CDP ignored the revoked Full Access authority: %#v", fullCDP)
	}
}

func TestRunCapabilityReadinessSeparatesUnprovenAndUnreadyBackends(t *testing.T) {
	ctx, state, run := newCapabilityReadinessRun(t, "cyber")
	runtime := readyCapabilityReadinessRuntime()
	runtime.LocalBackendReady = false
	runtime.DockerBackendReady = false
	projection, err := application.NewRunCapabilityReadinessService(state, runtime).
		Project(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	local := readinessOption(t, projection.Profiles, "local")
	if !hasReadinessBlocker(local, application.CapabilityBlockerBackendNotReady) ||
		hasReadinessBlocker(local, application.CapabilityBlockerSandboxUnproven) {
		t.Fatalf("proven but unready Local backend was misclassified: %#v", local)
	}
	docker := readinessOption(t, projection.Profiles, "docker")
	if !hasReadinessBlocker(docker, application.CapabilityBlockerBackendNotReady) ||
		hasReadinessBlocker(docker, application.CapabilityBlockerDockerUnavailable) {
		t.Fatalf("installed but unready Docker backend was misclassified: %#v", docker)
	}
}

func TestRunCapabilityReadinessSeparatesInstalledAdapterFromCurrentRunGrant(t *testing.T) {
	ctx, state, run := newCapabilityReadinessRun(t, "code")
	if _, err := application.NewRunExecutionProfileService(state).Change(ctx,
		application.ChangeRunExecutionProfileRequest{RunID: run.ID, Profile: "local",
			OperationKey: "readiness-command-runtime-profile",
			RequestedBy:  "test_operator", Reason: "select host runtime profile"}); err != nil {
		t.Fatal(err)
	}
	capabilities := domain.ExecutionPermissionRuntimeCapabilities{
		OperatorApprovalEnabled: true, DangerFullAccessEnabled: true,
		WorkspaceSandboxEnabled: true,
		RuntimeAuthority:        domain.NewExecutionPermissionRuntimeAuthority()}
	permissions := application.NewRunExecutionPermissionService(state, capabilities)
	if _, err := permissions.Change(ctx,
		application.ChangeRunExecutionPermissionRequest{RunID: run.ID,
			Mode:         string(domain.RunExecutionPermissionFull),
			OperationKey: "readiness-command-runtime-permission",
			RequestedBy:  "test_operator", Reason: "select host runtime permission",
			ConfirmFull: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(state).Start(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	acquired, err := state.AcquireRunExecutionLease(ctx,
		domain.AcquireRunExecutionLeaseRequest{RunID: run.ID,
			OwnerID: "readiness-command-runtime-owner", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	runtime := readyCapabilityReadinessRuntime()
	runtime.RunExecutionEnabled = true
	runtime.ExecutionPermissionCapabilities = capabilities
	identity := commandruntimeadapter.HostUnsandboxed(strings.Repeat("a", 64))
	runtime.CommandRuntimeAdapters = []commandruntimeadapter.Identity{identity}
	advertiser := &readinessCommandRuntimeAdvertiser{identity: identity, ready: true}
	service := application.NewRunCapabilityReadinessService(state, runtime, advertiser)
	granted, err := service.Project(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	status := granted.CommandRuntime
	if !status.ProtocolAvailable || !status.AdapterInstalled || !status.AdapterReady ||
		!status.CurrentRunGranted || status.AdapterKind != "host_unsandboxed" ||
		status.Backend != "run_owned_command_runtime" {
		t.Fatalf("granted Command Runtime status=%#v", status)
	}
	coldRuntime := runtime
	coldRuntime.ExecutionPermissionCapabilities.RuntimeAuthority = domain.NewExecutionPermissionRuntimeAuthority()
	for _, authoritative := range []bool{false, true} {
		coldService := application.NewRunCapabilityReadinessService(state, coldRuntime)
		if authoritative {
			coldService = application.NewRunCapabilityReadinessService(state, coldRuntime, advertiser)
		}
		cold, err := coldService.Project(ctx, run.ID)
		if err != nil || cold.CommandRuntime.CurrentRunGranted || !cold.CommandRuntime.AdapterInstalled || !cold.CommandRuntime.AdapterReady {
			t.Fatalf("cold Full readiness authoritative=%t: %#v err=%v", authoritative, cold, err)
		}
	}
	advertiser.ready = false
	notReady, err := service.Project(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	status = notReady.CommandRuntime
	if !status.ProtocolAvailable || !status.AdapterInstalled || !status.AdapterReady ||
		status.CurrentRunGranted || status.AdapterKind != "host_unsandboxed" {
		t.Fatalf("unavailable current adapter was granted: %#v", status)
	}
	advertiser.ready = true
	if _, _, err := state.ReleaseRunExecutionLease(ctx, acquired.Lease); err != nil {
		t.Fatal(err)
	}
	if _, err := application.NewRunService(state).Pause(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := permissions.Change(ctx,
		application.ChangeRunExecutionPermissionRequest{RunID: run.ID,
			Mode:         string(domain.RunExecutionPermissionAsk),
			OperationKey: "readiness-command-runtime-revoke",
			RequestedBy:  "test_operator", Reason: "revoke current Run grant"}); err != nil {
		t.Fatal(err)
	}
	revoked, err := service.Project(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	status = revoked.CommandRuntime
	if !status.ProtocolAvailable || !status.AdapterInstalled || !status.AdapterReady ||
		status.CurrentRunGranted || status.AdapterKind != "host_unsandboxed" || status.Backend == "" {
		t.Fatalf("revoked Command Runtime status=%#v", status)
	}
}

type readinessCommandRuntimeAdvertiser struct {
	identity commandruntimeadapter.Identity
	ready    bool
}

func (a *readinessCommandRuntimeAdvertiser) AdvertisedCommandRuntimeAdapter(
	_ context.Context, _ string, permission domain.RunExecutionPermissionMode,
) (commandruntimeadapter.Identity, bool, error) {
	if a == nil || !a.ready || !a.identity.AllowsPermission(permission) {
		return commandruntimeadapter.Identity{}, false, nil
	}
	return a.identity, true, nil
}

func TestCapabilityReadinessOptionValidationRejectsContradictoryDisposition(t *testing.T) {
	tests := []struct {
		name   string
		option application.CapabilityReadinessOption
	}{
		{"missing matching remediation", application.CapabilityReadinessOption{
			Value: "local", BlockedBy: []application.CapabilityReadinessBlocker{
				application.CapabilityBlockerSandboxUnproven},
			Remediation: []application.CapabilityReadinessRemediation{
				application.CapabilityRemediationRetryBackendReadiness},
		}},
		{"unrelated extra remediation", application.CapabilityReadinessOption{
			Value: "preview", BlockedBy: []application.CapabilityReadinessBlocker{
				application.CapabilityBlockerRunNotQuiescent},
			Remediation: []application.CapabilityReadinessRemediation{
				application.CapabilityRemediationPauseRun,
				application.CapabilityRemediationTrustWorkspace},
		}},
		{"runtime available with runtime blocker", application.CapabilityReadinessOption{
			Value: "docker", RuntimeAvailable: true,
			BlockedBy: []application.CapabilityReadinessBlocker{
				application.CapabilityBlockerDockerUnavailable},
			Remediation: []application.CapabilityReadinessRemediation{
				application.CapabilityRemediationInstallOrStartDocker},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.option.Validate(); err == nil {
				t.Fatalf("contradictory readiness option passed validation: %#v", test.option)
			}
		})
	}
}

func newCapabilityReadinessRun(t *testing.T, surface string) (
	context.Context, *store.SQLiteStore, domain.Run,
) {
	t.Helper()
	state, err := store.Open(filepath.Join(t.TempDir(), "readiness.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	ctx := context.Background()
	_, run, err := application.NewRunService(state).Create(ctx, application.CreateRunRequest{
		Goal: "project stable capability readiness", Profile: "review", Surface: surface,
		Phase: "deliver", Budget: domain.Budget{MaxTurns: 4},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, state, run
}

func readyCapabilityReadinessRuntime() application.CapabilityReadinessRuntime {
	return application.CapabilityReadinessRuntime{
		RunControlEnabled: true, ExecutionPermissionControlEnabled: true,
		BrowserCDPPermissionControlEnabled: true,
		ExecutionPermissionCapabilities: domain.ExecutionPermissionRuntimeCapabilities{
			WorkspaceSandboxEnabled: true, OperatorApprovalEnabled: true,
			DangerFullAccessEnabled: true},
		BrowserCDPPermissionCapabilities: domain.BrowserCDPPermissionRuntimeCapabilities{
			ControlEnabled: true, FullDebugEnabled: true,
		},
		LocalSandboxInstalled: true, LocalSandboxProven: true, LocalBackendReady: true,
		DockerStartupGateEnabled: true, DockerAvailable: true, DockerBackendReady: true,
		BrowserBackendReady: true,
	}
}

func readinessOption(t *testing.T, options []application.CapabilityReadinessOption,
	value string,
) application.CapabilityReadinessOption {
	t.Helper()
	for _, option := range options {
		if option.Value == value {
			return option
		}
	}
	t.Fatalf("readiness option %q not found in %#v", value, options)
	return application.CapabilityReadinessOption{}
}

func assertReadinessBlockers(t *testing.T, option application.CapabilityReadinessOption,
	want ...application.CapabilityReadinessBlocker,
) {
	t.Helper()
	if len(option.BlockedBy) != len(want) {
		t.Fatalf("option %s blockers=%v want=%v", option.Value, option.BlockedBy, want)
	}
	for index := range want {
		if option.BlockedBy[index] != want[index] {
			t.Fatalf("option %s blockers=%v want=%v", option.Value, option.BlockedBy, want)
		}
	}
}

func hasReadinessBlocker(option application.CapabilityReadinessOption,
	want application.CapabilityReadinessBlocker,
) bool {
	for _, blocker := range option.BlockedBy {
		if blocker == want {
			return true
		}
	}
	return false
}
