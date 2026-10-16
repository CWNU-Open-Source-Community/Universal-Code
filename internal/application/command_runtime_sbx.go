package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"cyberagent-workbench/internal/commandruntimeadapter"
	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/sandbox"
)

const CommandRuntimeSBXBackend = sandbox.SBXBackendName

// SBX reuses the same durable Run/profile/permission/lease/Drydock readers as
// Local. No caller can restore execution authority from an SBX journal entry.
type SBXCommandRuntimeExecutor struct {
	store    LocalSandboxCommandRuntimeStore
	backend  SBXCommandRuntimeBackend
	identity commandruntimeadapter.Identity
}

type SBXCommandRuntimeBackend interface {
	Available() bool
	Generation() string
	TemplateReference() string
	Readiness(context.Context) (sandbox.SBXReadiness, error)
	Run(context.Context, sandbox.SBXRunRequest, io.Reader) (sandbox.SBXExecutionResult, error)
}

func NewSBXCommandRuntimeExecutor(store LocalSandboxCommandRuntimeStore, backend SBXCommandRuntimeBackend) (*SBXCommandRuntimeExecutor, error) {
	if store == nil || backend == nil || !backend.Available() || backend.Generation() == "" || !sandbox.ValidSBXTemplateReference(backend.TemplateReference()) {
		return nil, sandbox.ErrSBXBoundary
	}
	identity := commandruntimeadapter.SandboxedWorkspace(CommandRuntimeSBXBackend, sandbox.SBXPolicyVersion, backend.Generation())
	if !identity.Executable() {
		return nil, sandbox.ErrSBXBoundary
	}
	return &SBXCommandRuntimeExecutor{store: store, backend: backend, identity: identity}, nil
}
func (e *SBXCommandRuntimeExecutor) Identity() commandruntimeadapter.Identity {
	if e == nil {
		return commandruntimeadapter.Identity{}
	}
	return e.identity
}
func (e *SBXCommandRuntimeExecutor) Available() bool {
	return e != nil && e.store != nil && e.backend != nil && e.backend.Available() && e.identity.Executable() && e.identity.Generation == e.backend.Generation()
}
func (e *SBXCommandRuntimeExecutor) Ready(ctx context.Context, runID string) (bool, error) {
	if !e.Available() || ctx == nil || ctx.Err() != nil || !domain.ValidAgentID(runID) {
		return false, nil
	}
	proof, err := e.backend.Readiness(ctx)
	if err != nil || !proof.ReadyAt(time.Now().UTC()) || proof.Generation != e.identity.Generation {
		return false, err
	}
	interaction, err := e.store.GetRunExecutionInteraction(ctx, runID)
	if err != nil {
		return false, err
	}
	return interaction.Validate() == nil && interaction.RunID == runID && interaction.Mode == domain.RunExecutionInteractionControlled &&
		interaction.ExecutionProfile == domain.RunExecutionProfileSBX && interaction.NetworkScope == domain.ExecutionNetworkDisabled, nil
}

func (e *SBXCommandRuntimeExecutor) NormalizeCommandRuntimeSpec(spec runner.CommandRuntimeSpec, root string) (runner.CommandRuntimeResolvedSpec, error) {
	if !e.Available() {
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeUnavailable
	}
	return NormalizeSBXCommandRuntimeSpec(spec, root, e.backend.TemplateReference())
}

// SBX selects guest executables from an immutable, explicitly configured OCI
// template. ExecutableSHA256 binds that template and guest path; it is not a
// fabricated hash of a host executable. Native host launch validation must not
// be applied to this guest spec. The executor re-normalizes it at every effect.
// Initial support is the process profile. Shells belong to their own reviewed
// profile and are not accepted as an arbitrary process executable.
func NormalizeSBXCommandRuntimeSpec(spec runner.CommandRuntimeSpec, root, template string) (runner.CommandRuntimeResolvedSpec, error) {
	if !sandbox.ValidSBXTemplateReference(template) || spec.Profile != runner.CommandRuntimeProcess ||
		spec.Network != runner.CommandRuntimeNetworkDisabled || !path.IsAbs(spec.Executable) ||
		path.Clean(spec.Executable) != spec.Executable || strings.HasPrefix(spec.Executable, "//") ||
		strings.Contains(spec.Executable, `\`) {
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
	}
	intent, err := runner.NormalizeCommandRuntimeIntent(spec)
	if err != nil {
		return runner.CommandRuntimeResolvedSpec{}, err
	}
	if !sbxGuestExecutableAllowed(intent.Executable) {
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
	}
	directory := filepath.Join(root, filepath.FromSlash(intent.WorkingDirectory))
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
	}
	relative, err := filepath.Rel(root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
	}
	intent.WorkingDirectory = filepath.ToSlash(relative)
	environment := []string{"PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "HOME=/home/agent", "LANG=C.UTF-8",
		"CI=1", "NO_COLOR=1", "TERM=dumb", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=3", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=",
		"GIT_CONFIG_KEY_1=core.hooksPath", "GIT_CONFIG_VALUE_1=/dev/null", "GIT_CONFIG_KEY_2=core.fsmonitor", "GIT_CONFIG_VALUE_2=false",
		"GIT_ASKPASS=/bin/false", "SSH_ASKPASS=/bin/false", "GIT_LFS_SKIP_SMUDGE=1", "GIT_ALLOW_PROTOCOL=file",
		"GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOENV=off", "CARGO_NET_OFFLINE=true", "NPM_CONFIG_OFFLINE=true", "PIP_NO_INDEX=1",
		"NPM_CONFIG_USERCONFIG=/dev/null", "NPM_CONFIG_GLOBALCONFIG=/tmp/traverse-empty-global.npmrc",
		sandbox.SBXPreparedGoModuleCacheEnvironment + "="}
	moduleCache, err := sandbox.PreparedGoModuleCache(root, intent.WorkingDirectory)
	if err != nil {
		return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
	}
	if moduleCache != "" {
		relative, err := filepath.Rel(directory, filepath.Join(root, filepath.FromSlash(moduleCache)))
		if err != nil {
			return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
		}
		environment[len(environment)-1] = sandbox.SBXPreparedGoModuleCacheEnvironment + "=" + filepath.ToSlash(relative)
	}
	// Preserve the normalized caller bindings, while never allowing them to
	// replace fixed credential/startup/network configuration.
	fixed := map[string]bool{}
	for _, binding := range environment {
		name, _, _ := strings.Cut(binding, "=")
		fixed[strings.ToUpper(name)] = true
	}
	for _, binding := range intent.Environment {
		if fixed[strings.ToUpper(binding.Name)] {
			return runner.CommandRuntimeResolvedSpec{}, runner.ErrCommandRuntimeBoundary
		}
		environment = append(environment, binding.Name+"="+binding.Value)
	}
	sort.Strings(environment)
	rootSHA, err := runner.CommandRuntimeWorkspaceRootSHA256(root)
	if err != nil {
		return runner.CommandRuntimeResolvedSpec{}, err
	}
	data, _ := json.Marshal(environment)
	environmentSHA := sha256.Sum256(data)
	identityData, _ := json.Marshal([]string{sandbox.SBXPolicyVersion, "template-guest-executable", template, intent.Executable})
	executableSHA := sha256.Sum256(identityData)
	return runner.CommandRuntimeResolvedSpec{Spec: intent, ExecutablePath: intent.Executable,
		ExecutableIdentityKind: runner.CommandRuntimeExecutableTemplatePathSHA256,
		ExecutableSHA256:       hex.EncodeToString(executableSHA[:]), ExecutablePinned: true,
		CanonicalArgv: append([]string{}, intent.Arguments...), AbsoluteDirectory: directory, WorkspaceRoot: root,
		Environment: environment, EnvironmentSHA256: hex.EncodeToString(environmentSHA[:]), WorkspaceRootSHA256: rootSHA}, nil
}

func sbxGuestExecutableAllowed(executable string) bool {
	if executable == "/usr/local/go/bin/go" || executable == "/usr/local/go/bin/gofmt" {
		return true
	}
	if !(strings.HasPrefix(executable, "/usr/bin/") || strings.HasPrefix(executable, "/usr/local/bin/") || strings.HasPrefix(executable, "/bin/")) {
		return false
	}
	base := strings.ToLower(path.Base(executable))
	for _, blocked := range []string{"sh", "bash", "dash", "zsh", "fish", "ksh", "csh", "tcsh", "pwsh", "powershell", "env", "busybox", "sudo", "su", "doas", "pkexec", "xargs"} {
		if base == blocked {
			return false
		}
	}
	return path.Dir(executable) == "/usr/bin" || path.Dir(executable) == "/usr/local/bin" || path.Dir(executable) == "/bin"
}

func (e *SBXCommandRuntimeExecutor) ExecuteSandboxCommand(ctx context.Context, scope runner.CommandRuntimeScope, spec runner.CommandRuntimeResolvedSpec, stdin io.ReadCloser) (runner.CommandRuntimeSandboxResult, error) {
	// These exits precede backend dispatch, so no VM/process needs reaping.
	empty := runner.CommandRuntimeSandboxResult{ExitCode: 125, TreeReaped: true}
	if ctx == nil || ctx.Err() != nil || !e.Available() || scope.Validate() != nil || !scope.Adapter.SameBackend(e.identity) ||
		!e.identity.AllowsPermission(scope.PermissionMode) || spec.AttachmentInput != nil || spec.ExecutableIdentityKind != runner.CommandRuntimeExecutableTemplatePathSHA256 ||
		(spec.Spec.StdinPolicy == runner.CommandRuntimeStdinClosed && stdin != nil) ||
		(spec.Spec.StdinPolicy == runner.CommandRuntimeStdinPipe && stdin == nil) {
		return empty, runner.ErrCommandRuntimeBoundary
	}
	normalized, err := e.NormalizeCommandRuntimeSpec(spec.Spec, spec.WorkspaceRoot)
	if err != nil || !sbxResolvedSpecMatches(normalized, spec) {
		return empty, runner.ErrCommandRuntimeBoundary
	}
	check := func(current context.Context) error {
		if err := e.checkAuthority(current, scope, spec); err != nil {
			return err
		}
		return runner.CheckCommandRuntimeDispatch(current, spec)
	}
	if err := check(ctx); err != nil {
		return empty, err
	}
	requestIdentity, _ := json.Marshal(struct {
		Scope           runner.CommandRuntimeScope
		SpecFingerprint string
	}{scope, runner.CommandRuntimeSpecFingerprint(spec)})
	requestFingerprint := sha256.Sum256(requestIdentity)
	result, runErr := e.backend.Run(ctx, sandbox.SBXRunRequest{OperationKey: scope.RunID + "/" + scope.OperationKey,
		RequestFingerprint: hex.EncodeToString(requestFingerprint[:]), RuntimeGeneration: e.identity.Generation,
		DrydockRoot: spec.WorkspaceRoot, SourceRepository: gitBisectRuntimeFromContext(ctx) != nil, Arguments: append([]string{spec.ExecutablePath}, spec.CanonicalArgv...),
		Environment: append([]string{}, spec.Environment...), WorkingDirectory: spec.Spec.WorkingDirectory,
		Timeout: time.Duration(spec.Spec.TimeoutMilliseconds) * time.Millisecond, OutputLimit: spec.Spec.Output.ArtifactBytes, AuthorityCheck: check,
		BeforeExec: func(current context.Context) error {
			if err := e.checkAuthority(current, scope, spec); err != nil {
				return err
			}
			return runner.ConfirmCommandRuntimeDispatch(current, spec)
		}}, stdin)
	value := runner.CommandRuntimeSandboxResult{ExitCode: result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr, TreeReaped: result.TreeReaped}
	if value.ExitCode < 0 {
		value.ExitCode = 125
	}
	if !result.TreeReaped {
		// Preserve the backend's uncertain cleanup proof. A pre-dispatch result
		// must never turn a dispatched but still-owned VM into a terminal Job.
		return value, errors.Join(runErr, sandbox.ErrSBXCleanup)
	}
	if result.ExitCode < 0 {
		return value, errors.Join(runErr, sandbox.ErrSBXCLI)
	}
	return value, runErr
}

func (*SBXCommandRuntimeExecutor) CommandRuntimeAdmissionRequired() bool { return true }

func (e *SBXCommandRuntimeExecutor) checkAuthority(ctx context.Context, scope runner.CommandRuntimeScope, spec runner.CommandRuntimeResolvedSpec) error {
	normalized, err := e.NormalizeCommandRuntimeSpec(spec.Spec, spec.WorkspaceRoot)
	if err != nil || !sbxResolvedSpecMatches(normalized, spec) {
		return runner.ErrCommandRuntimeBoundary
	}
	workspace, found, err := readRunFileDrydock(ctx, e.store, scope.RunID)
	if err != nil || !found {
		return errors.Join(err, runner.ErrCommandRuntimeBoundary)
	}
	workspace, err = batchValidationExecutionWorkspace(ctx, workspace, scope, spec)
	if err != nil {
		return err
	}
	workspace, err = gitBisectExecutionWorkspace(ctx, workspace, scope, spec)
	if err != nil {
		return err
	}
	bound, err := commandRuntimeDrydockBound(ctx, e.store, workspace, scope.RunID, scope.MissionID, scope.SessionID, scope.WorkspaceID)
	if err != nil || !bound || workspace.Path != spec.WorkspaceRoot {
		return errors.Join(err, runner.ErrCommandRuntimeBoundary)
	}
	rootSHA, err := runner.CommandRuntimeWorkspaceRootSHA256(workspace.Path)
	if err != nil || rootSHA != scope.WorkspaceRootSHA256 || rootSHA != spec.WorkspaceRootSHA256 {
		return errors.Join(err, runner.ErrCommandRuntimeBoundary)
	}
	profile, err := e.store.GetRunExecutionProfile(ctx, scope.RunID)
	if err != nil {
		return err
	}
	permission, err := e.store.GetRunExecutionPermission(ctx, scope.RunID)
	if err != nil {
		return err
	}
	interaction, err := e.store.GetRunExecutionInteraction(ctx, scope.RunID)
	if err != nil {
		return err
	}
	lease, found, err := e.store.GetRunExecutionLease(ctx, scope.RunID)
	if err != nil {
		return err
	}
	if !found || profile.RunID != scope.RunID || profile.MissionID != scope.MissionID || profile.ID != scope.ProfileSnapshotID || profile.Revision != scope.ProfileRevision || profile.Profile != domain.RunExecutionProfileSBX ||
		permission.RunID != scope.RunID || permission.MissionID != scope.MissionID || permission.ID != scope.PermissionSnapshotID || permission.Revision != scope.PermissionRevision || permission.Mode != scope.PermissionMode ||
		interaction.Validate() != nil || interaction.RunID != scope.RunID || interaction.MissionID != scope.MissionID || interaction.ExecutionProfileRevision != profile.Revision || interaction.Mode != domain.RunExecutionInteractionControlled || interaction.ExecutionProfile != domain.RunExecutionProfileSBX || interaction.NetworkScope != domain.ExecutionNetworkDisabled ||
		lease.RunID != scope.RunID || lease.Validate() != nil || !lease.ActiveAt(time.Now()) || lease.LeaseID != scope.LeaseID || lease.Generation != scope.LeaseGeneration || lease.OwnerID != scope.LeaseOwnerID || lease.Status != domain.RunExecutionLeaseActive {
		return runner.ErrCommandRuntimeBoundary
	}
	return nil
}

func sbxResolvedSpecMatches(normalized, actual runner.CommandRuntimeResolvedSpec) bool {
	return runner.CommandRuntimeSpecFingerprint(normalized) == runner.CommandRuntimeSpecFingerprint(actual) &&
		slices.Equal(normalized.Environment, actual.Environment) && normalized.AbsoluteDirectory == actual.AbsoluteDirectory &&
		normalized.ExecutablePath == actual.ExecutablePath && actual.ExecutablePinned && !actual.ProfileStartupFiles && !actual.EnvironmentInherited && actual.AttachmentInput == nil
}
