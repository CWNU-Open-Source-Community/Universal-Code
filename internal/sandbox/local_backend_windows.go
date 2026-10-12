//go:build windows

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const LocalBackendPolicyVersion = "windows_appcontainer_policy.v3"

type localPreparedRun struct {
	request       LocalRunRequest
	drydock       localPinnedRoot
	toolchains    []localPinnedRoot
	executable    string
	arguments     []string
	workingDir    string
	environment   []uint16
	baselineBytes int64
}

type localReadinessProcessFailure struct {
	cause                   error
	exitCode                int
	timedOut                bool
	cancelled               bool
	outputLimitExceeded     bool
	writeLimitExceeded      bool
	treeReaped              bool
	appContainer            bool
	lessPrivileged          bool
	lowIntegrity            bool
	zeroNetworkCapabilities bool
	matchingProfileSID      bool
	matchingCapabilitySIDs  bool
}

func (e *localReadinessProcessFailure) Error() string {
	if e == nil {
		return "local sandbox readiness child failed"
	}
	return fmt.Sprintf("local sandbox readiness child failed (process_error=%t "+
		"exit_code=%d timed_out=%t cancelled=%t output_limit=%t write_limit=%t "+
		"tree_reaped=%t app_container=%t less_privileged=%t low_integrity=%t "+
		"zero_network=%t matching_profile=%t matching_capabilities=%t)",
		e.cause != nil, e.exitCode, e.timedOut, e.cancelled, e.outputLimitExceeded,
		e.writeLimitExceeded, e.treeReaped, e.appContainer, e.lessPrivileged,
		e.lowIntegrity, e.zeroNetworkCapabilities, e.matchingProfileSID,
		e.matchingCapabilitySIDs)
}

func (e *localReadinessProcessFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (b *windowsLocalBackend) Readiness(ctx context.Context,
	capabilities LocalRuntimeCapabilities,
) (LocalReadiness, error) {
	if b == nil || ctx == nil {
		return LocalReadiness{}, ErrLocalSandboxUnavailable
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.readinessLocked(ctx, capabilities)
}

func (b *windowsLocalBackend) readinessLocked(ctx context.Context,
	capabilities LocalRuntimeCapabilities,
) (LocalReadiness, error) {
	now := time.Now().UTC()
	base := LocalReadiness{ProtocolVersion: LocalReadinessProtocolVersion,
		PolicyVersion: LocalBackendPolicyVersion, Backend: LocalBackendName,
		CheckedAt: now, ExpiresAt: now.Add(LocalReadinessTTL),
		RuntimeGeneration: b.generation, FeatureEnabled: capabilities.Enabled,
		CapabilityGrant: false}
	if !capabilities.Enabled {
		base.Status, base.ReasonCode, base.RemediationCode = LocalReadinessDisabled,
			LocalReasonFeatureDisabled, LocalRemediationEnableFeature
		base.EvidenceFingerprint = localReadinessFingerprint(base)
		return base, base.Validate()
	}
	if b.closed || b.lock == 0 || b.initErr != nil || runtime.GOARCH != "amd64" {
		base.Status, base.ReasonCode, base.RemediationCode = LocalReadinessUnavailable,
			LocalReasonProcessUnavailable, LocalRemediationRepairIsolation
		if runtime.GOARCH != "amd64" {
			base.ReasonCode, base.RemediationCode = LocalReasonArchitectureUnsupported,
				LocalRemediationUseSupportedHost
		}
		base.EvidenceFingerprint = localReadinessFingerprint(base)
		b.readiness = base
		return base, base.Validate()
	}
	if !b.readiness.ExpiresAt.IsZero() && time.Now().UTC().Before(b.readiness.ExpiresAt) &&
		b.readiness.RuntimeGeneration == b.generation && b.readiness.FeatureEnabled {
		return b.readiness, b.readiness.Validate()
	}
	if err := ctx.Err(); err != nil {
		return LocalReadiness{}, err
	}
	if err := b.probeReadinessLocked(ctx); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return LocalReadiness{}, ctxErr
		}
		base.Status, base.ReasonCode, base.RemediationCode = LocalReadinessUnavailable,
			LocalReasonProbeFailed, LocalRemediationRetryProbe
		base.EvidenceFingerprint = localReadinessFingerprint(base)
		b.readiness = base
		return base, base.Validate()
	}
	base.Status, base.ReasonCode, base.RemediationCode = LocalReadinessReady,
		LocalReasonNone, LocalRemediationNone
	base.WindowsHost, base.X64Host, base.PersistentACLs = true, true, true
	base.AppContainerProfile, base.AppContainerToken = true, true
	base.RestrictedToken, base.LowIntegrityToken = false, true
	base.ZeroNetworkCapabilities, base.WFPDefaultDeny = true, true
	base.CreationTimeJobBinding, base.KillOnJobClose = true, true
	base.BoundedHandleInheritance, base.CleanEnvironment = true, true
	base.EphemeralProfile, base.FilesystemProven = true, true
	base.ProcessProven, base.NetworkProven, base.CredentialProven = true, true, true
	base.Ready = true
	base.EvidenceFingerprint = localReadinessFingerprint(base)
	b.readiness = base
	return base, base.Validate()
}

func (b *windowsLocalBackend) probeReadinessLocked(ctx context.Context) (returnErr error) {
	if err := b.recoverOwnersLocked(); err != nil {
		return err
	}
	rootPath, err := os.MkdirTemp(b.ownerRoot, "readiness-")
	if err != nil {
		return err
	}
	rootPath = filepath.Clean(rootPath)
	defer func() { returnErr = errors.Join(returnErr, os.RemoveAll(rootPath)) }()
	root, err := pinLocalRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.close()
	profile, err := prepareLocalProfile(localFingerprint("readiness", b.generation))
	if err != nil {
		return err
	}
	if err := createLocalAppContainerDirectories(rootPath, profile.name); err != nil {
		return err
	}
	// The profile directories are part of the writable boundary and must be
	// present before its inheritable DACL and integrity label are captured.
	root.close()
	root, err = pinLocalRoot(rootPath)
	if err != nil {
		return err
	}
	snapshot, err := captureLocalSecurity(root)
	if err != nil {
		return err
	}
	bindingFingerprint := localFingerprint("local-readiness-binding.v1", b.generation)
	owner := localOwnerRecord{ProtocolVersion: localOwnerProtocolVersion,
		PolicyVersion:      LocalBackendPolicyVersion,
		OwnerID:            localFingerprint("local-readiness-owner.v1", profile.name),
		BindingFingerprint: bindingFingerprint, ProfileName: profile.name,
		ProfileSID: profile.sid.String(), Snapshots: []localSecuritySnapshot{snapshot},
		CreatedAt: time.Now().UTC()}
	owner.seal()
	if err := b.writeOwnerLocked(owner); err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, b.cleanupOwnerLocked(owner)) }()
	if err := materializeLocalProfile(profile); err != nil {
		return err
	}
	if err := grantLocalRoot(snapshot, profile.filesystemCapabilitySID, true, true); err != nil {
		return err
	}
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	cmdPath := filepath.Clean(filepath.Join(systemDirectory, "cmd.exe"))
	environment, err := buildLocalEnvironment(filepath.Join(rootPath, ".traverse-board", "home"), filepath.Join(rootPath, ".traverse-board", "tmp"), []localPinnedRoot{{path: systemDirectory}}, nil)
	if err != nil {
		return err
	}
	process, processErr := runLocalProcess(ctx, localProcessSpec{profile: profile,
		executable: cmdPath, arguments: []string{"/d", "/c",
			"type NUL>null-proof.txt && echo ready>readiness.txt && echo ready>NUL"},
		workingDir: rootPath, environment: environment,
		resources: ResourceLimits{CPUQuotaMillis: 1000, MemoryBytes: 256 * 1024 * 1024,
			PIDs: 4, MaxOutputBytes: 64 * 1024}, timeout: 15 * time.Second,
		captureOut: true, captureErr: true, writeMaximum: 16 * 1024 * 1024})
	if processErr != nil || process.exitCode != 0 || !process.treeReaped ||
		!process.proof.appContainer ||
		!process.proof.lessPrivileged || !process.proof.lowIntegrity ||
		!process.proof.zeroNetworkCapabilities || !process.proof.matchingProfileSID ||
		!process.proof.matchingCapabilitySIDs {
		return &localReadinessProcessFailure{cause: processErr, exitCode: process.exitCode,
			timedOut: process.timedOut, cancelled: process.cancelled,
			outputLimitExceeded: process.outputLimitExceeded,
			writeLimitExceeded:  process.writeLimitExceeded, treeReaped: process.treeReaped,
			appContainer:            process.proof.appContainer,
			lessPrivileged:          process.proof.lessPrivileged,
			lowIntegrity:            process.proof.lowIntegrity,
			zeroNetworkCapabilities: process.proof.zeroNetworkCapabilities,
			matchingProfileSID:      process.proof.matchingProfileSID,
			matchingCapabilitySIDs:  process.proof.matchingCapabilitySIDs}
	}
	payload, err := os.ReadFile(filepath.Join(rootPath, "readiness.txt"))
	if err != nil || strings.TrimSpace(string(payload)) != "ready" {
		return errors.Join(err, errors.New("local sandbox readiness write proof failed"))
	}
	nullProof, err := os.ReadFile(filepath.Join(rootPath, "null-proof.txt"))
	if err != nil || len(nullProof) != 0 {
		return errors.Join(err, errors.New("local sandbox null-device proof failed"))
	}
	return nil
}

func (b *windowsLocalBackend) Run(ctx context.Context,
	request LocalRunRequest,
) (result LocalExecutionResult, returnErr error) {
	result = localNoProcessResult()
	if request.StdinPipe {
		return result, ErrLocalSandboxBoundary
	}
	return b.run(ctx, request, nil)
}

func (b *windowsLocalBackend) RunWithStdin(ctx context.Context,
	request LocalRunRequest, stdin io.ReadCloser,
) (result LocalExecutionResult, returnErr error) {
	result = localNoProcessResult()
	if stdin == nil || !request.StdinPipe {
		if stdin != nil {
			_ = stdin.Close()
		}
		return result, ErrLocalSandboxBoundary
	}
	return b.run(ctx, request, stdin)
}

func (b *windowsLocalBackend) run(ctx context.Context,
	request LocalRunRequest, stdin io.ReadCloser,
) (LocalExecutionResult, error) {
	return b.runWithProcess(ctx, request, stdin, runLocalProcess)
}

func (b *windowsLocalBackend) runWithProcess(ctx context.Context,
	request LocalRunRequest, stdin io.ReadCloser,
	runProcess func(context.Context, localProcessSpec) (localProcessResult, error),
) (result LocalExecutionResult, returnErr error) {
	result = localNoProcessResult()
	if stdin != nil {
		defer stdin.Close()
	}
	if b == nil || ctx == nil || runProcess == nil {
		return result, ErrLocalSandboxUnavailable
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.lock == 0 || b.initErr != nil {
		return result, ErrLocalSandboxUnavailable
	}
	normalized, err := NormalizeLocalRunRequest(request)
	if err != nil || normalized.Binding.RuntimeGeneration != b.generation {
		return result, errors.Join(err, ErrLocalSandboxBoundary)
	}
	readiness, err := b.readinessLocked(ctx, LocalRuntimeCapabilities{Enabled: true})
	if err != nil || !readiness.Ready || readiness.Status != LocalReadinessReady {
		return result, errors.Join(err, ErrLocalSandboxUnavailable)
	}
	prepared, err := prepareLocalRun(normalized)
	if err != nil {
		return result, err
	}
	defer prepared.close()
	// Fail before this request creates an owner or changes any ACL. Installed
	// runtimes may be readable by the user but not grantable to an LPAC token.
	for index, toolchain := range prepared.toolchains {
		if localImplicitReadOnlyRoot(toolchain.path) {
			continue
		}
		if err := preflightLocalRootDACLAccess(toolchain); err != nil {
			role := "toolchain"
			if normalized.ToolchainInputs[index].DataOnly {
				role = "attachment input"
			}
			return result, fmt.Errorf("local sandbox preflight: %s root %q requires WRITE_DAC to grant temporary LPAC read access; use a user-owned runtime/input directory or an explicitly configured accessible installation; no command was started: %w", role, toolchain.path, err)
		}
	}
	profile, err := prepareLocalProfileWithInstrumentation(localExecutionBindingFingerprint(normalized), normalized.Instrumentation)
	if err != nil {
		return result, err
	}
	snapshots := make([]localSecuritySnapshot, 0, len(prepared.toolchains)+1)
	drydockSnapshot, err := captureLocalSecurity(prepared.drydock)
	if err != nil {
		return result, err
	}
	snapshots = append(snapshots, drydockSnapshot)
	modifiedToolchains := make(map[string]localSecuritySnapshot)
	for _, toolchain := range prepared.toolchains {
		if localImplicitReadOnlyRoot(toolchain.path) {
			continue
		}
		snapshot, captureErr := captureLocalSecurity(toolchain)
		if captureErr != nil {
			return result, captureErr
		}
		snapshots = append(snapshots, snapshot)
		modifiedToolchains[strings.ToLower(toolchain.path)] = snapshot
	}
	owner := localOwnerRecord{ProtocolVersion: localOwnerProtocolVersion,
		PolicyVersion: LocalBackendPolicyVersion, Instrumentation: normalized.Instrumentation,
		OwnerID: localFingerprint("local-run-owner.v1", profile.name,
			localExecutionBindingFingerprint(normalized)),
		BindingFingerprint: localExecutionBindingFingerprint(normalized),
		ProfileName:        profile.name, ProfileSID: profile.sid.String(),
		Snapshots: snapshots, CreatedAt: time.Now().UTC()}
	scratch, err := b.prepareScratch(owner.OwnerID, prepared)
	if err != nil {
		return result, err
	}
	defer scratch.close()
	owner.Scratch = &localScratchIdentity{PathSHA256: localHostPathDigest(scratch.path), RootIdentity: scratch.identity}
	owner.seal()
	if err := b.writeOwnerLocked(owner); err != nil {
		scratch.close()
		return result, errors.Join(err, os.Remove(scratch.path))
	}
	cleanupRequired := true
	defer func() {
		if cleanupRequired {
			scratch.close()
			returnErr = errors.Join(returnErr, b.cleanupOwnerLocked(owner))
		}
	}()
	if err := prepareLocalScratchDirectories(scratch.path, profile.name); err != nil {
		return result, err
	}
	pathRoots := make([]localPinnedRoot, 0, len(prepared.toolchains))
	attachmentRoot := ""
	for index, input := range normalized.ToolchainInputs {
		if input.DataOnly {
			attachmentRoot = prepared.toolchains[index].path
		} else {
			pathRoots = append(pathRoots, prepared.toolchains[index])
		}
	}
	prepared.environment, err = buildLocalEnvironment(filepath.Join(scratch.path, "home"), filepath.Join(scratch.path, "tmp"), pathRoots, normalized.Manifest.Environment, attachmentRoot)
	if err != nil {
		return result, err
	}
	scratchSnapshot, err := captureLocalSecurity(scratch)
	if err != nil {
		return result, err
	}
	if err := materializeLocalProfile(profile); err != nil {
		return result, err
	}
	if err := grantLocalRoot(scratchSnapshot, profile.filesystemCapabilitySID, true, true); err != nil {
		return result, fmt.Errorf("local sandbox grant: scratch root %q: %w", scratch.path, err)
	}
	if err := grantLocalRoot(drydockSnapshot, profile.filesystemCapabilitySID, true, true); err != nil {
		return result, fmt.Errorf("local sandbox grant: workspace root %q: %w", prepared.drydock.path, err)
	}
	for _, toolchain := range prepared.toolchains {
		snapshot, modified := modifiedToolchains[strings.ToLower(toolchain.path)]
		if modified {
			if err := grantLocalRoot(snapshot, profile.filesystemCapabilitySID, false, false); err != nil {
				return result, fmt.Errorf("local sandbox grant: read-only input root %q: %w", toolchain.path, err)
			}
		}
	}

	// The process transport changes its proof at successful native creation;
	// never infer dispatch or cleanup from an error message at this layer.
	process, processErr := runProcess(ctx, localProcessSpec{profile: profile,
		executable: prepared.executable, arguments: prepared.arguments,
		workingDir: prepared.workingDir, environment: prepared.environment,
		resources:    normalized.Manifest.Resources,
		timeout:      time.Duration(normalized.Manifest.TimeoutSeconds) * time.Second,
		captureOut:   normalized.Manifest.Output.CaptureStdout,
		captureErr:   normalized.Manifest.Output.CaptureStderr,
		stdin:        stdin,
		writeMaximum: normalized.MaxDiskWriteBytes})
	treeErr := errors.Join(validateLocalTree(prepared.drydock.path), validateLocalTree(scratch.path))
	if treeErr != nil {
		processErr = errors.Join(processErr, treeErr)
	}
	var postBytes int64
	var postErr error
	if treeErr == nil {
		postBytes, postErr = localDirectorySize(prepared.drydock.path, math.MaxInt64)
		if postErr == nil {
			var scratchBytes int64
			scratchBytes, postErr = localDirectorySize(scratch.path, normalized.MaxDiskWriteBytes)
			// Deleting project files cannot offset writes into runtime state.
			postBytes = max(postBytes, prepared.baselineBytes)
			if postErr == nil && scratchBytes <= math.MaxInt64-postBytes {
				postBytes += scratchBytes
			} else if postErr == nil {
				postErr = ErrLocalSandboxWriteLimit
			}
		}
		if postErr != nil {
			process.writeLimitExceeded = process.writeLimitExceeded || errors.Is(postErr, ErrLocalSandboxWriteLimit)
			processErr = errors.Join(processErr, postErr)
		} else if postBytes > prepared.baselineBytes &&
			postBytes-prepared.baselineBytes > normalized.MaxDiskWriteBytes {
			process.writeLimitExceeded = true
			processErr = errors.Join(processErr, ErrLocalSandboxWriteLimit)
		}
	} else {
		postErr = treeErr
	}
	scratch.close()
	cleanupErr := b.cleanupOwnerLocked(owner)
	cleanupRequired = false

	manifestFingerprint, _ := normalized.Manifest.Fingerprint()
	result = LocalExecutionResult{ProtocolVersion: LocalExecutionProtocolVersion,
		PolicyVersion: LocalBackendPolicyVersion, Backend: LocalBackendName,
		Instrumentation: normalized.Instrumentation,
		ExitCode:        process.exitCode, Stdout: process.stdout, Stderr: process.stderr,
		StartedAt: process.startedAt, CompletedAt: process.completedAt,
		RuntimeGeneration:   b.generation,
		BindingFingerprint:  localExecutionBindingFingerprint(normalized),
		ManifestFingerprint: manifestFingerprint,
		ProfileFingerprint:  localFingerprint("local-profile-proof.v1", profile.sid.String()),
		TimedOut:            process.timedOut, Cancelled: process.cancelled,
		OutputLimitExceeded: process.outputLimitExceeded,
		WriteLimitExceeded:  process.writeLimitExceeded,
		TreeReaped:          process.treeReaped, DrydockReadWrite: true,
		ToolchainsReadOnly: true, ReparseBoundary: treeErr == nil && (postErr == nil || errors.Is(postErr, ErrLocalSandboxWriteLimit)),
		DiskWritesBound: true, OwnerRecoveryBound: cleanupErr == nil,
		StdinClosed: true, CPUQuotaBound: true, MemoryBound: true,
		ProcessCountBound: true, RuntimeBound: true, OutputBound: true,
		ArtifactBound: true, AppContainerToken: process.proof.appContainer,
		RestrictedToken:         process.proof.restricted,
		LowIntegrityToken:       process.proof.lowIntegrity,
		ZeroNetworkCapabilities: process.proof.zeroNetworkCapabilities,
		WFPDefaultDeny: process.proof.zeroNetworkCapabilities && process.proof.appContainer &&
			process.proof.lessPrivileged,
		CreationTimeJobBinding: true, KillOnJobClose: true,
		BoundedHandleInheritance: true, EnvironmentInherited: false,
		CredentialMaterialized: false, ProfileDeleted: cleanupErr == nil,
		ACLsRestored: cleanupErr == nil, CapabilityGrant: false}
	result.Status = LocalExecutionCompleted
	if result.TimedOut {
		result.Status = LocalExecutionTimedOut
	} else if result.Cancelled {
		result.Status = LocalExecutionCancelled
	} else if result.ExitCode != 0 || result.OutputLimitExceeded ||
		result.WriteLimitExceeded {
		result.Status = LocalExecutionFailed
	}
	result.EvidenceFingerprint = localExecutionFingerprint(result)
	if validationErr := result.Validate(normalized); validationErr != nil {
		return result, errors.Join(processErr, cleanupErr, validationErr)
	}
	return result, errors.Join(processErr, cleanupErr)
}

func prepareLocalRun(request LocalRunRequest) (prepared localPreparedRun, returnErr error) {
	prepared.request = request
	drydock, err := pinLocalRoot(request.Binding.DrydockRoot)
	if err != nil {
		return prepared, err
	}
	prepared.drydock = drydock
	defer func() {
		if returnErr != nil {
			prepared.close()
		}
	}()
	if err := validateLocalTree(drydock.path); err != nil {
		return prepared, err
	}
	if localRootCrossesSensitiveBoundary(drydock.path) {
		return prepared, ErrLocalSandboxBoundary
	}
	for _, input := range request.ToolchainInputs {
		root, err := pinLocalRoot(input.Root)
		if err != nil {
			return prepared, err
		}
		prepared.toolchains = append(prepared.toolchains, root)
		if localRootCrossesSensitiveBoundary(root.path) {
			return prepared, ErrLocalSandboxBoundary
		}
		if err := validateLocalTreeLinks(root.path,
			!localImplicitReadOnlyRoot(root.path)); err != nil {
			return prepared, err
		}
	}
	for index := range prepared.toolchains {
		if localRootsOverlap(prepared.drydock.path, prepared.toolchains[index].path) {
			return prepared, ErrLocalSandboxBoundary
		}
		for other := index + 1; other < len(prepared.toolchains); other++ {
			if localRootsOverlap(prepared.toolchains[index].path,
				prepared.toolchains[other].path) {
				return prepared, ErrLocalSandboxBoundary
			}
		}
	}
	prepared.executable, err = localVirtualToHost(request.Manifest.Command.Executable,
		request, prepared.toolchains)
	if err != nil {
		return prepared, err
	}
	prepared.workingDir, err = localVirtualToHost(
		request.Manifest.Command.WorkingDirectory, request, prepared.toolchains)
	if err != nil || !localHostPathWithin(prepared.workingDir, drydock.path) {
		return prepared, ErrLocalSandboxBoundary
	}
	workingInfo, err := os.Lstat(prepared.workingDir)
	if err != nil || !workingInfo.IsDir() || workingInfo.Mode()&os.ModeSymlink != 0 {
		return prepared, ErrLocalSandboxBoundary
	}
	prepared.arguments = make([]string, len(request.Manifest.Command.Arguments))
	for index, argument := range request.Manifest.Command.Arguments {
		prepared.arguments[index], err = localTranslateArgument(argument, request,
			prepared.toolchains)
		if err != nil {
			return prepared, err
		}
	}
	prepared.baselineBytes, err = localDirectorySize(drydock.path, math.MaxInt64)
	if err != nil {
		return prepared, err
	}
	return prepared, nil
}

func (p *localPreparedRun) close() {
	if p == nil {
		return
	}
	p.drydock.close()
	for index := range p.toolchains {
		p.toolchains[index].close()
	}
}

func localVirtualToHost(value string, request LocalRunRequest,
	roots []localPinnedRoot,
) (string, error) {
	if value == "/workspace" || strings.HasPrefix(value, "/workspace/") {
		relative := strings.TrimPrefix(value, "/workspace")
		return localJoinVirtual(request.Binding.DrydockRoot, relative)
	}
	for index, input := range request.ToolchainInputs {
		if value == input.VirtualRoot || strings.HasPrefix(value, input.VirtualRoot+"/") {
			relative := strings.TrimPrefix(value, input.VirtualRoot)
			return localJoinVirtual(roots[index].path, relative)
		}
	}
	return "", ErrLocalSandboxBoundary
}

func localJoinVirtual(root, relative string) (string, error) {
	relative = strings.TrimPrefix(relative, "/")
	host := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	if !localHostPathWithin(host, root) {
		return "", ErrLocalSandboxBoundary
	}
	return host, nil
}

func localHostPathWithin(value, root string) bool {
	relative, err := filepath.Rel(root, value)
	return err == nil && relative != ".." && !strings.HasPrefix(relative,
		".."+string(os.PathSeparator)) && !filepath.IsAbs(relative)
}

func localTranslateArgument(argument string, request LocalRunRequest,
	roots []localPinnedRoot,
) (string, error) {
	if strings.HasPrefix(argument, "/workspace") {
		if mapped, err := localVirtualToHost(argument, request, roots); err == nil {
			return mapped, nil
		}
	}
	for _, input := range request.ToolchainInputs {
		if strings.HasPrefix(argument, input.VirtualRoot) {
			if mapped, err := localVirtualToHost(argument, request, roots); err == nil {
				return mapped, nil
			}
		}
	}
	if separator := strings.IndexByte(argument, '='); separator > 0 {
		mapped, err := localTranslateArgument(argument[separator+1:], request, roots)
		if err == nil && mapped != argument[separator+1:] {
			return argument[:separator+1] + mapped, nil
		}
	}
	return argument, nil
}

func buildLocalEnvironment(home, temporary string, toolchains []localPinnedRoot,
	bindings []EnvironmentBinding, attachmentRoots ...string,
) ([]uint16, error) {
	if len(attachmentRoots) > 1 || (len(attachmentRoots) == 1 && attachmentRoots[0] != "" && !validLocalHostRoot(attachmentRoots[0])) {
		return nil, ErrLocalSandboxBoundary
	}
	windowsDirectory, err := windows.GetWindowsDirectory()
	if err != nil {
		return nil, err
	}
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	drive := filepath.VolumeName(home)
	tail := strings.TrimPrefix(home, drive)
	pathValues := make([]string, 0, len(toolchains)+1)
	for _, root := range toolchains {
		pathValues = append(pathValues, root.path)
		if info, err := os.Stat(filepath.Join(root.path, "bin", "go.exe")); err == nil && info.Mode().IsRegular() {
			pathValues = append(pathValues, filepath.Join(root.path, "bin"))
		}
	}
	pathValues = append(pathValues, systemDirectory)
	values := map[string]string{
		"APPDATA": home, "LOCALAPPDATA": home, "USERPROFILE": home, "HOME": home,
		"HOMEDRIVE": drive, "HOMEPATH": tail, "TEMP": temporary, "TMP": temporary,
		"SystemRoot": windowsDirectory, "WINDIR": windowsDirectory,
		"SystemDrive": filepath.VolumeName(windowsDirectory),
		"ComSpec":     filepath.Join(systemDirectory, "cmd.exe"),
		"PATH":        strings.Join(pathValues, string(os.PathListSeparator)),
		"PATHEXT":     ".COM;.EXE;.BAT;.CMD", "LANG": "C", "NO_COLOR": "1",
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "NUL",
		"GIT_CONFIG_COUNT": "4", "GIT_CONFIG_KEY_0": "credential.helper",
		"GIT_CONFIG_VALUE_0": "", "GIT_CONFIG_KEY_1": "credential.interactive",
		"GIT_CONFIG_VALUE_1": "never", "GIT_CONFIG_KEY_2": "core.hooksPath",
		"GIT_CONFIG_VALUE_2": "NUL", "GIT_CONFIG_KEY_3": "core.fsmonitor",
		"GIT_CONFIG_VALUE_3": "false", "GIT_TERMINAL_PROMPT": "0",
		"GIT_ASKPASS": "NUL", "SSH_ASKPASS": "NUL", "GCM_INTERACTIVE": "Never",
		"SSH_AUTH_SOCK": "", "AWS_SHARED_CREDENTIALS_FILE": filepath.Join(home, "aws", "credentials"),
		"AWS_CONFIG_FILE":                filepath.Join(home, "aws", "config"),
		"AZURE_CONFIG_DIR":               filepath.Join(home, "azure"),
		"GOOGLE_APPLICATION_CREDENTIALS": filepath.Join(home, "gcp", "application.json"),
		"KUBECONFIG":                     filepath.Join(home, "kube", "config"),
		"DOCKER_CONFIG":                  filepath.Join(home, "docker"), "NUGET_PACKAGES": filepath.Join(home, "nuget"),
		"NPM_CONFIG_USERCONFIG": filepath.Join(home, "npmrc"),
		"GOCACHE":               filepath.Join(home, "go-build"), "GOPATH": filepath.Join(home, "go"),
		"GOMODCACHE": filepath.Join(home, "go", "pkg", "mod"), "GOTOOLCHAIN": "local",
		"GOPROXY": "off", "GOSUMDB": "off",
	}
	for _, binding := range bindings {
		if binding.Source != EnvironmentLiteral || localSensitiveEnvironmentName(binding.Name) {
			return nil, ErrLocalSandboxBoundary
		}
		for existing := range values {
			if strings.EqualFold(existing, binding.Name) {
				return nil, ErrLocalSandboxBoundary
			}
		}
		values[binding.Name] = binding.Value
	}
	if len(attachmentRoots) == 1 && attachmentRoots[0] != "" {
		values[LocalAttachmentEnvironment] = attachmentRoots[0]
	}
	return localEnvironment(values)
}

func createLocalAppContainerDirectories(drydock, profileName string) error {
	if !validLocalProfileName(profileName) || !validLocalHostRoot(drydock) {
		return ErrLocalSandboxBoundary
	}
	packageRoot, err := localAppContainerDirectory(drydock, profileName)
	if err != nil {
		return err
	}
	for _, relative := range []string{"AC\\Temp", "AC\\INetCache", "LocalCache",
		"LocalState", "RoamingState", "Settings", "TempState"} {
		if err := os.MkdirAll(filepath.Join(packageRoot, relative), 0o700); err != nil {
			return err
		}
	}
	return nil
}

func localAppContainerDirectory(drydock, profileName string) (string, error) {
	if !validLocalProfileName(profileName) || !validLocalHostRoot(drydock) {
		return "", ErrLocalSandboxBoundary
	}
	root := filepath.Clean(filepath.Join(drydock, ".traverse-board", "home", "Packages",
		strings.ToLower(profileName)))
	if !localHostPathWithin(root, drydock) {
		return "", ErrLocalSandboxBoundary
	}
	return root, nil
}

func removeLocalAppContainerDirectory(drydock, profileName string) error {
	root, err := localAppContainerDirectory(drydock, profileName)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := validateLocalTree(root); err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func localImplicitReadOnlyRoot(root string) bool {
	candidates := []string{}
	if value, err := windows.GetWindowsDirectory(); err == nil {
		candidates = append(candidates, filepath.Clean(value))
	}
	for _, identifier := range []*windows.KNOWNFOLDERID{windows.FOLDERID_ProgramFiles,
		windows.FOLDERID_ProgramFilesX64, windows.FOLDERID_ProgramFilesX86} {
		if value, err := windows.KnownFolderPath(identifier, windows.KF_FLAG_DEFAULT); err == nil {
			candidates = append(candidates, filepath.Clean(value))
		}
	}
	for _, candidate := range candidates {
		if localHostPathWithin(root, candidate) {
			return true
		}
	}
	return false
}

func localRootContainsUserBoundary(root string) bool {
	for _, identifier := range []*windows.KNOWNFOLDERID{windows.FOLDERID_Profile,
		windows.FOLDERID_LocalAppData, windows.FOLDERID_RoamingAppData} {
		value, err := windows.KnownFolderPath(identifier, windows.KF_FLAG_DEFAULT)
		if err == nil && localHostPathWithin(filepath.Clean(value), root) {
			return true
		}
	}
	return false
}

func localRootCrossesSensitiveBoundary(root string) bool {
	return localRootContainsUserBoundary(root) ||
		localRootOverlapsCredentialDirectory(root)
}

func localRootOverlapsCredentialDirectory(root string) bool {
	profile, err := windows.KnownFolderPath(windows.FOLDERID_Profile,
		windows.KF_FLAG_DEFAULT)
	if err != nil {
		return true
	}
	localAppData, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData,
		windows.KF_FLAG_DEFAULT)
	if err != nil {
		return true
	}
	roamingAppData, err := windows.KnownFolderPath(windows.FOLDERID_RoamingAppData,
		windows.KF_FLAG_DEFAULT)
	if err != nil {
		return true
	}
	sensitive := []string{
		filepath.Join(profile, ".ssh"), filepath.Join(profile, ".aws"),
		filepath.Join(profile, ".azure"), filepath.Join(profile, ".kube"),
		filepath.Join(profile, ".docker"), filepath.Join(profile, ".config", "gcloud"),
		filepath.Join(localAppData, "Google", "Chrome", "User Data"),
		filepath.Join(localAppData, "Microsoft", "Edge", "User Data"),
		filepath.Join(localAppData, "Mozilla", "Firefox", "Profiles"),
		filepath.Join(roamingAppData, "Mozilla", "Firefox", "Profiles"),
		filepath.Join(roamingAppData, "Microsoft", "Credentials"),
		filepath.Join(roamingAppData, "Microsoft", "Protect"),
		filepath.Join(localAppData, "Microsoft", "Credentials"),
		filepath.Join(localAppData, "Microsoft", "Vault"),
	}
	for _, value := range sensitive {
		if localRootsOverlap(root, filepath.Clean(value)) {
			return true
		}
	}
	return false
}

func (b *windowsLocalBackend) cleanupOwnerLocked(record localOwnerRecord) error {
	if record.validate() != nil {
		return ErrLocalSandboxBoundary
	}
	var cleanupErr error
	for index := len(record.Snapshots) - 1; index >= 0; index-- {
		if err := restoreLocalSecurity(record.Snapshots[index]); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("local sandbox restore: root %q: %w", record.Snapshots[index].Path, err))
		}
	}
	if record.Scratch != nil {
		cleanupErr = errors.Join(cleanupErr, b.removeScratch(record))
	} else {
		cleanupErr = errors.Join(cleanupErr, removeLocalAppContainerDirectory(
			record.Snapshots[0].Path, record.ProfileName))
	}
	cleanupErr = errors.Join(cleanupErr, deleteLocalProfile(record.ProfileName))
	if cleanupErr == nil {
		cleanupErr = b.removeOwnerLocked(record.OwnerID)
	}
	return cleanupErr
}
