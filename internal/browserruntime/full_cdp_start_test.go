package browserruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
)

func TestPrepareFullCDPProfileRuntimeRootSupportsFreshHomeMaterialization(t *testing.T) {
	home := directTestPath(t, t.TempDir())
	root, err := PrepareFullCDPProfileRuntimeRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot := filepath.Join(home, "runtime", "full-cdp", ProfileRuntimeRootName)
	if root != wantRoot || !profilePathHasNoIndirection(root) {
		t.Fatalf("prepared Full CDP Profile root=%q want direct %q", root, wantRoot)
	}

	session, identity, acceptance, permission := fullCDPAuthorizationFacts(t)
	ownership, err := BuildProfileOwnershipPlan(session, identity, root)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Round(time.Millisecond)
	attempt, err := BuildBrowserLaunchAttempt(session, identity, acceptance, ownership,
		"browser-attempt-full-cdp-fresh-home", base)
	if err != nil {
		t.Fatal(err)
	}
	launchLease, err := BuildBrowserLaunchLease(attempt,
		"browser-lease-full-cdp-fresh-home", "browser-full-cdp-worker", base,
		2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	review, err := BuildBrowserLaunchReview(session, identity, acceptance, ownership,
		attempt, launchLease, "browser-review-full-cdp-fresh-home",
		BrowserLaunchReviewAcceptCandidate, "independent-runtime-operator",
		"browser-review-full-cdp-fresh-home-operation", "", base.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	executionPermission, executionCapabilities, executionFence :=
		fullCDPExecutionFacts(t, session)
	runtimeCapabilities := FullCDPRuntimeCapabilities{
		StartEnabled: true, DisposableProfileEnabled: true, TransportEnabled: true,
	}
	permissionCapabilities := domain.BrowserCDPPermissionRuntimeCapabilities{
		ControlEnabled: true, FullDebugEnabled: true,
	}
	authorizedAt := base.Add(2 * time.Second)
	authorization, err := AuthorizeFullCDPStart(session, identity, acceptance,
		ownership, attempt, launchLease, review, permission, executionPermission,
		runtimeCapabilities, permissionCapabilities, executionCapabilities,
		executionFence, authorizedAt)
	if err != nil {
		t.Fatal(err)
	}
	profileLease, err := MaterializeFullCDPProfile(authorization, session, identity,
		acceptance, ownership, attempt, launchLease, review, permission,
		executionPermission, executionCapabilities, authorizedAt.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	for _, directory := range profileEnvironmentDirectoryNames {
		if info, statErr := os.Lstat(filepath.Join(profileLease.DirectoryPath, directory)); statErr != nil || !info.IsDir() {
			t.Fatalf("fresh-home Profile environment %q missing: %v", directory, statErr)
		}
	}
	released, err := ReleaseFullCDPProfile(authorization, profileLease, ownership,
		true, authorizedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := CleanupReleasedFullCDPProfile(authorization, released, ownership, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(ownership.DirectoryPath); !os.IsNotExist(err) {
		t.Fatalf("fresh-home Full CDP Profile was not cleaned: %v", err)
	}
}

func TestPrepareFullCDPProfileRuntimeRootReconcilesOnlyProvedReleasedCleanup(t *testing.T) {
	home := directTestPath(t, t.TempDir())
	root, err := PrepareFullCDPProfileRuntimeRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	marker, quarantinePath, proofPath := releasedFullCDPCleanupProofFacts(t, root)
	if err := os.Mkdir(quarantinePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(quarantinePath, "partially-deleted-state"),
		[]byte("remaining browser bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeProfileCleanupProofExclusive(proofPath, marker); err != nil {
		t.Fatal(err)
	}
	unprovedPath := filepath.Join(root, ".cleanup-unproved-restart-residue")
	if err := os.Mkdir(unprovedPath, 0o700); err != nil {
		t.Fatal(err)
	}

	prepared, err := PrepareFullCDPProfileRuntimeRoot(home)
	if err != nil || prepared != root {
		t.Fatalf("restart cleanup reconciliation root=%q err=%v", prepared, err)
	}
	for _, path := range []string{quarantinePath, proofPath} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("proved restart cleanup residue remained at %q: %v", path, statErr)
		}
	}
	if info, statErr := os.Lstat(unprovedPath); statErr != nil || !info.IsDir() {
		t.Fatalf("unproved cleanup-like directory was touched: %v", statErr)
	}
}

func TestPrepareFullCDPProfileRuntimeRootRejectsUnsafeCleanupResidue(t *testing.T) {
	t.Run("no proof is left untouched", func(t *testing.T) {
		home := directTestPath(t, t.TempDir())
		root, err := PrepareFullCDPProfileRuntimeRoot(home)
		if err != nil {
			t.Fatal(err)
		}
		_, quarantinePath, _ := releasedFullCDPCleanupProofFacts(t, root)
		if err := os.Mkdir(quarantinePath, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareFullCDPProfileRuntimeRoot(home); err != nil {
			t.Fatalf("unproved quarantine should be ignored, not trusted: %v", err)
		}
		if info, err := os.Lstat(quarantinePath); err != nil || !info.IsDir() {
			t.Fatalf("unproved quarantine was removed: %v", err)
		}
	})

	t.Run("corrupt proof fails closed", func(t *testing.T) {
		home := directTestPath(t, t.TempDir())
		root, err := PrepareFullCDPProfileRuntimeRoot(home)
		if err != nil {
			t.Fatal(err)
		}
		_, quarantinePath, proofPath := releasedFullCDPCleanupProofFacts(t, root)
		if err := os.Mkdir(quarantinePath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(proofPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareFullCDPProfileRuntimeRoot(home); err == nil {
			t.Fatal("corrupt cleanup proof unexpectedly authorized deletion")
		}
		if info, err := os.Lstat(quarantinePath); err != nil || !info.IsDir() {
			t.Fatalf("corrupt proof caused target deletion: %v", err)
		}
	})

	t.Run("active proof fails closed", func(t *testing.T) {
		home := directTestPath(t, t.TempDir())
		root, err := PrepareFullCDPProfileRuntimeRoot(home)
		if err != nil {
			t.Fatal(err)
		}
		marker, quarantinePath, proofPath := releasedFullCDPCleanupProofFacts(t, root)
		marker.State = ProfileMarkerActive
		marker.ReleasedAt = time.Time{}
		marker.Fingerprint = ""
		marker.Fingerprint = browserRuntimeFingerprint(marker)
		raw, err := json.Marshal(marker)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(quarantinePath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(proofPath, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareFullCDPProfileRuntimeRoot(home); err == nil {
			t.Fatal("active owner proof unexpectedly authorized cleanup")
		}
		if info, err := os.Lstat(quarantinePath); err != nil || !info.IsDir() {
			t.Fatalf("active owner target was removed: %v", err)
		}
	})

	t.Run("indirect target fails closed", func(t *testing.T) {
		home := directTestPath(t, t.TempDir())
		root, err := PrepareFullCDPProfileRuntimeRoot(home)
		if err != nil {
			t.Fatal(err)
		}
		marker, quarantinePath, proofPath := releasedFullCDPCleanupProofFacts(t, root)
		outside := filepath.Join(directTestPath(t, t.TempDir()), "outside-profile")
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(outside, "must-remain"), []byte("foreign"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, quarantinePath); err != nil {
			t.Skipf("symbolic-link creation is unavailable for this test process: %v", err)
		}
		if err := writeProfileCleanupProofExclusive(proofPath, marker); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareFullCDPProfileRuntimeRoot(home); err == nil {
			t.Fatal("cleanup proof unexpectedly authorized an indirect target")
		}
		if _, err := os.Lstat(filepath.Join(outside, "must-remain")); err != nil {
			t.Fatalf("indirect external target was touched: %v", err)
		}
	})
}

func TestPrepareFullCDPProfileRuntimeRootBoundsCleanupProofs(t *testing.T) {
	home := directTestPath(t, t.TempDir())
	root, err := PrepareFullCDPProfileRuntimeRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	base, _, _ := releasedFullCDPCleanupProofFacts(t, root)
	proofPaths := make([]string, 0, maxFullCDPProfileReconcileProofs+1)
	for index := 0; index <= maxFullCDPProfileReconcileProofs; index++ {
		marker := base
		marker.OwnerToken = fmt.Sprintf("%016x", index+1) + strings.Repeat("a", 48)
		marker.Fingerprint = ""
		marker.Fingerprint = browserRuntimeFingerprint(marker)
		name := profileCleanupQuarantineName(marker.OwnerToken, marker.Generation) +
			".owner.json"
		path := filepath.Join(root, name)
		if err := writeProfileCleanupProofExclusive(path, marker); err != nil {
			t.Fatal(err)
		}
		proofPaths = append(proofPaths, path)
	}
	if _, err := PrepareFullCDPProfileRuntimeRoot(home); err == nil {
		t.Fatal("excess cleanup proofs unexpectedly bypassed the reconciliation bound")
	}
	for _, path := range proofPaths {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("bounded reconciliation partially consumed proof %q: %v", path, err)
		}
	}
}

func releasedFullCDPCleanupProofFacts(t *testing.T, root string) (
	ProfileOwnerMarker, string, string,
) {
	t.Helper()
	session, identity, _, _ := fullCDPAuthorizationFacts(t)
	ownership, err := BuildProfileOwnershipPlan(session, identity, root)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC().Round(time.Millisecond)
	marker := newProfileOwnerMarker(ownership, createdAt)
	marker.State = ProfileMarkerReleased
	marker.ReleasedAt = createdAt.Add(time.Second)
	marker.Fingerprint = ""
	marker.Fingerprint = browserRuntimeFingerprint(marker)
	quarantineName := profileCleanupQuarantineName(marker.OwnerToken, marker.Generation)
	quarantinePath := filepath.Join(root, quarantineName)
	return marker, quarantinePath, quarantinePath + ".owner.json"
}

func TestCleanupReleasedFullCDPProfileRetriesVerifiedQuarantine(t *testing.T) {
	authorization, released, ownership := materializedReleasedFullCDPProfile(t)
	quarantinePath := filepath.Join(ownership.RootPath,
		".cleanup-"+ownership.OwnerToken[:16]+"-"+
			strconv.FormatUint(ownership.Generation, 10))
	proofPath := quarantinePath + ".owner.json"
	foreignPath := filepath.Join(ownership.RootPath, ".cleanup-foreign-owner")
	if err := os.Mkdir(foreignPath, 0o700); err != nil {
		t.Fatal(err)
	}

	originalTimeout := profileCleanupRetryTimeout
	originalRemove := profileTreeRemoveAll
	profileCleanupRetryTimeout = 50 * time.Millisecond
	unblock := make(chan struct{})
	firstStarted := make(chan struct{})
	var callsMu sync.Mutex
	calls := 0
	profileTreeRemoveAll = func(path string) error {
		callsMu.Lock()
		calls++
		call := calls
		callsMu.Unlock()
		if call == 1 {
			if err := os.Remove(filepath.Join(path, ProfileOwnerMarkerName)); err != nil {
				return err
			}
			close(firstStarted)
			<-unblock
			return &os.PathError{Op: "removeall", Path: path, Err: os.ErrPermission}
		}
		return os.RemoveAll(path)
	}
	defer func() {
		profileCleanupRetryTimeout = originalTimeout
		profileTreeRemoveAll = originalRemove
	}()

	begin := time.Now()
	err := CleanupReleasedFullCDPProfile(authorization, released, ownership, true)
	if err == nil {
		t.Fatal("blocked quarantine removal unexpectedly succeeded")
	}
	if elapsed := time.Since(begin); elapsed > 500*time.Millisecond {
		t.Fatalf("Full CDP Profile cleanup ignored its deadline: %s", elapsed)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("Full CDP Profile janitor never started")
	}
	if _, statErr := os.Lstat(ownership.DirectoryPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("released Profile was not moved out of its active path: %v", statErr)
	}
	if _, markerErr := readProfileOwnerMarker(quarantinePath); markerErr == nil {
		t.Fatal("partial quarantine deletion unexpectedly preserved its in-tree marker")
	}
	proofInfo, proofErr := os.Lstat(proofPath)
	if proofErr != nil || validateProfileCleanupProof(
		proofPath, proofInfo, ownership, released) != nil {
		t.Fatalf("out-of-tree cleanup proof was not preserved: %v", proofErr)
	}
	if _, statErr := os.Lstat(foreignPath); statErr != nil {
		t.Fatalf("foreign cleanup target was touched: %v", statErr)
	}

	close(unblock)
	deadline := time.Now().Add(time.Second)
	for {
		profileCleanupJanitors.Lock()
		_, active := profileCleanupJanitors.jobs[quarantinePath]
		profileCleanupJanitors.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed quarantine janitor did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := CleanupReleasedFullCDPProfile(authorization, released, ownership, true); err != nil {
		t.Fatalf("verified quarantine retry failed: %v", err)
	}
	if _, statErr := os.Lstat(quarantinePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("verified quarantine remained after retry: %v", statErr)
	}
	if _, statErr := os.Lstat(proofPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cleanup proof remained after retry: %v", statErr)
	}
	if _, statErr := os.Lstat(foreignPath); statErr != nil {
		t.Fatalf("foreign cleanup target was removed by retry: %v", statErr)
	}
	if err := CleanupReleasedFullCDPProfile(authorization, released, ownership, true); err != nil {
		t.Fatalf("already-cleaned Full CDP Profile was not idempotent: %v", err)
	}
}

func TestMaterializeFullCDPProfileReturnsRecoverableOwnerWhenCleanupBlocks(t *testing.T) {
	session, identity, acceptance, ownership, attempt, launchLease, review,
		permission := fullCDPLaunchFacts(t)
	executionPermission, executionCapabilities, executionFence :=
		fullCDPExecutionFacts(t, session)
	issuedAt := time.Now().UTC().Add(2 * time.Second)
	authorization, err := AuthorizeFullCDPStart(session, identity, acceptance,
		ownership, attempt, launchLease, review, permission, executionPermission,
		FullCDPRuntimeCapabilities{
			StartEnabled: true, DisposableProfileEnabled: true, TransportEnabled: true,
		}, domain.BrowserCDPPermissionRuntimeCapabilities{
			ControlEnabled: true, FullDebugEnabled: true,
		}, executionCapabilities, executionFence, issuedAt)
	if err != nil {
		t.Fatal(err)
	}

	originalTimeout := profileCleanupRetryTimeout
	originalRemove := profileTreeRemoveAll
	originalEnsurer := profileEnvironmentDirectoryEnsurer
	profileCleanupRetryTimeout = 50 * time.Millisecond
	profileEnvironmentDirectoryEnsurer = func(string) error {
		return errors.New("injected Profile environment materialization failure")
	}
	unblock := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	profileTreeRemoveAll = func(path string) error {
		once.Do(func() { close(started) })
		<-unblock
		return &os.PathError{Op: "removeall", Path: path, Err: os.ErrPermission}
	}
	defer func() {
		profileCleanupRetryTimeout = originalTimeout
		profileTreeRemoveAll = originalRemove
		profileEnvironmentDirectoryEnsurer = originalEnsurer
	}()

	recoveryLease, materializeErr := MaterializeFullCDPProfile(authorization,
		session, identity, acceptance, ownership, attempt, launchLease, review,
		permission, executionPermission, executionCapabilities,
		issuedAt.Add(time.Millisecond))
	if materializeErr == nil || recoveryLease.State != ProfileMarkerReleased ||
		recoveryLease.AuthorizationFingerprint != authorization.Fingerprint ||
		recoveryLease.OwnershipPlanFingerprint != ownership.Fingerprint {
		t.Fatalf("materialization failure lost its cleanup owner: lease=%+v err=%v",
			recoveryLease, materializeErr)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("failed materialization cleanup janitor did not start")
	}
	close(unblock)
	quarantinePath := filepath.Join(ownership.RootPath,
		".cleanup-"+ownership.OwnerToken[:16]+"-"+
			strconv.FormatUint(ownership.Generation, 10))
	deadline := time.Now().Add(time.Second)
	for {
		profileCleanupJanitors.Lock()
		_, active := profileCleanupJanitors.jobs[quarantinePath]
		profileCleanupJanitors.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed materialization cleanup janitor did not stop")
		}
		time.Sleep(5 * time.Millisecond)
	}
	profileTreeRemoveAll = os.RemoveAll
	profileEnvironmentDirectoryEnsurer = originalEnsurer
	if err := CleanupReleasedFullCDPProfile(authorization, recoveryLease,
		ownership, true); err != nil {
		t.Fatalf("recoverable failed materialization owner did not clean: %v", err)
	}
}

func materializedReleasedFullCDPProfile(t *testing.T) (FullCDPStartAuthorization,
	ProfileRuntimeLease, ProfileOwnershipPlan,
) {
	t.Helper()
	session, identity, acceptance, ownership, attempt, launchLease, review,
		permission := fullCDPLaunchFacts(t)
	executionPermission, executionCapabilities, executionFence :=
		fullCDPExecutionFacts(t, session)
	issuedAt := time.Now().UTC().Add(2 * time.Second)
	authorization, err := AuthorizeFullCDPStart(session, identity, acceptance,
		ownership, attempt, launchLease, review, permission, executionPermission,
		FullCDPRuntimeCapabilities{
			StartEnabled: true, DisposableProfileEnabled: true, TransportEnabled: true,
		}, domain.BrowserCDPPermissionRuntimeCapabilities{
			ControlEnabled: true, FullDebugEnabled: true,
		}, executionCapabilities, executionFence, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := MaterializeFullCDPProfile(authorization, session, identity,
		acceptance, ownership, attempt, launchLease, review, permission,
		executionPermission, executionCapabilities, issuedAt.Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	released, err := ReleaseFullCDPProfile(authorization, lease, ownership, true,
		issuedAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return authorization, released, ownership
}

func fullCDPLaunchFacts(t *testing.T) (SessionPlan, BrowserExecutableIdentity,
	BrowserAcceptanceCandidate, ProfileOwnershipPlan, BrowserLaunchAttempt,
	BrowserLaunchLease, BrowserLaunchReview, domain.RunBrowserCDPPermissionSnapshot,
) {
	t.Helper()
	session, identity, acceptance, permission := fullCDPAuthorizationFacts(t)
	// Live launch tests create their review from the wall clock immediately
	// afterward, so the lease timestamp must never round into the future.
	now := time.Now().UTC().Truncate(time.Millisecond)
	ownership, err := BuildProfileOwnershipPlan(session, identity,
		filepath.Join(directTestPath(t, t.TempDir()), ProfileRuntimeRootName))
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := BuildBrowserLaunchAttempt(session, identity, acceptance, ownership,
		"browser-attempt-full-cdp", now)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := BuildBrowserLaunchLease(attempt, "browser-lease-full-cdp",
		"browser-full-cdp-worker", now, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	review, err := BuildBrowserLaunchReview(session, identity, acceptance, ownership,
		attempt, lease, "browser-review-full-cdp", BrowserLaunchReviewAcceptCandidate,
		"independent-runtime-operator", "browser-review-full-cdp-operation", "",
		now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return session, identity, acceptance, ownership, attempt, lease, review, permission
}

func TestRetainRecoverableFullCDPLaunchKeepsResourceSafeReceiptOwner(t *testing.T) {
	session, identity, acceptance, permission := fullCDPAuthorizationFacts(t)
	ownership, err := BuildProfileOwnershipPlan(session, identity,
		filepath.Join(directTestPath(t, t.TempDir()), ProfileRuntimeRootName))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-3 * time.Second)
	attempt, err := BuildBrowserLaunchAttempt(session, identity, acceptance, ownership,
		"browser-attempt-full-cdp-safe-failure", base)
	if err != nil {
		t.Fatal(err)
	}
	launchLease, err := BuildBrowserLaunchLease(attempt,
		"browser-lease-full-cdp-safe-failure", "browser-full-cdp-worker",
		base, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	review, err := BuildBrowserLaunchReview(session, identity, acceptance, ownership,
		attempt, launchLease, "browser-review-full-cdp-safe-failure",
		BrowserLaunchReviewAcceptCandidate, "independent-runtime-operator",
		"browser-review-full-cdp-safe-failure-operation", "", base.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	executionPermission, executionCapabilities, executionFence :=
		fullCDPExecutionFacts(t, session)
	issuedAt := time.Now().UTC()
	authorization, err := AuthorizeFullCDPStart(session, identity, acceptance,
		ownership, attempt, launchLease, review, permission, executionPermission,
		FullCDPRuntimeCapabilities{
			StartEnabled: true, DisposableProfileEnabled: true, TransportEnabled: true,
		}, domain.BrowserCDPPermissionRuntimeCapabilities{
			ControlEnabled: true, FullDebugEnabled: true,
		}, executionCapabilities, executionFence, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	profileLease, err := MaterializeFullCDPProfile(authorization, session, identity,
		acceptance, ownership, attempt, launchLease, review, permission,
		executionPermission, executionCapabilities, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &FullCDPManagedRuntime{
		runtimeID: "full_cdp_runtime-safe-launch-failure", sessionPlan: session,
		attempt: attempt, startAuthorization: authorization, ownership: ownership,
		profileLease: profileLease, startedAt: issuedAt,
		terminalFailureCode: "launch_failed",
	}
	wantErr := errors.New("injected transport open failure")
	retained, gotErr := retainRecoverableFullCDPLaunch(runtime, wantErr)
	if retained != runtime || !errors.Is(gotErr, wantErr) {
		t.Fatalf("resource-safe launch result lost its receipt owner: runtime=%p want=%p err=%v",
			retained, runtime, gotErr)
	}
	receipt, closeErr := retained.Close(context.Background(), "launch_failed")
	if closeErr != nil || receipt.RecoveryRequired || !receipt.ProcessTreeQuiescent ||
		!receipt.ProfileReleased || !receipt.ProfileCleaned || receipt.Succeeded ||
		receipt.FailureCode != "launch_failed" {
		t.Fatalf("resource-safe failed launch receipt=%+v err=%v", receipt, closeErr)
	}
	if _, err := os.Lstat(ownership.DirectoryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resource-safe failed launch retained its Profile: %v", err)
	}
}

func TestAuthorizeFullCDPStartRequiresMaximumAccessDebug(t *testing.T) {
	session, identity, acceptance, ownership, attempt, lease, review,
		permission := fullCDPLaunchFacts(t)
	runtimeCapabilities := FullCDPRuntimeCapabilities{
		StartEnabled: true, DisposableProfileEnabled: true, TransportEnabled: true,
	}
	permissionCapabilities := domain.BrowserCDPPermissionRuntimeCapabilities{
		ControlEnabled: true, FullDebugEnabled: true,
	}
	executionPermission, executionCapabilities, executionFence :=
		fullCDPExecutionFacts(t, session)
	now := time.Now().UTC().Add(2 * time.Second)

	authorization, err := AuthorizeFullCDPStart(session, identity, acceptance,
		ownership, attempt, lease, review, permission, executionPermission,
		runtimeCapabilities, permissionCapabilities, executionCapabilities,
		executionFence, now)
	if err != nil {
		t.Fatal(err)
	}
	if !authorization.ProcessStartAuthorized || !authorization.ProcessTerminationAuthorized ||
		!authorization.ProfileCreateAuthorized || !authorization.ProfileReleaseAuthorized ||
		!authorization.ExactOwnedCleanupAuthorized {
		t.Fatalf("full CDP start authorization has wrong process authority: %#v", authorization)
	}
	if err := ValidateFullCDPStartAuthorization(authorization, session, identity,
		acceptance, ownership, attempt, lease, review, permission,
		executionPermission, executionCapabilities); err != nil {
		t.Fatal(err)
	}
	otherRunPermission := permission
	otherRunPermission.RunID = "run-other-full-cdp"
	if err := ValidateFullCDPStartAuthorization(authorization, session, identity,
		acceptance, ownership, attempt, lease, review, otherRunPermission,
		executionPermission, executionCapabilities); err == nil {
		t.Fatal("Full CDP start authorization accepted a browser permission from another Run")
	}
}

func TestAuthorizeFullCDPStartRejectsRestrictedPermission(t *testing.T) {
	session, identity, acceptance, ownership, attempt, lease, review,
		_ := fullCDPLaunchFacts(t)
	runtimeCapabilities := FullCDPRuntimeCapabilities{
		StartEnabled: true, DisposableProfileEnabled: true, TransportEnabled: true,
	}
	permissionCapabilities := domain.BrowserCDPPermissionRuntimeCapabilities{
		ControlEnabled: true, FullDebugEnabled: true,
	}
	restricted := permissionForFullCDP(t, session)
	executionPermission, executionCapabilities, executionFence :=
		fullCDPExecutionFacts(t, session)
	now := time.Now().UTC().Add(2 * time.Second)
	if _, err := AuthorizeFullCDPStart(session, identity, acceptance, ownership,
		attempt, lease, review, restricted, executionPermission,
		runtimeCapabilities, permissionCapabilities, executionCapabilities,
		executionFence, now); err == nil {
		t.Fatal("restricted permission unexpectedly authorized a full CDP launch")
	}
}

func TestAuthorizeFullCDPStartRejectsFullPermissionFromAnotherRun(t *testing.T) {
	session, identity, acceptance, ownership, attempt, lease, review,
		permission := fullCDPLaunchFacts(t)
	permission.RunID = "run-other-full-cdp"
	executionPermission, executionCapabilities, executionFence :=
		fullCDPExecutionFacts(t, session)
	_, err := AuthorizeFullCDPStart(session, identity, acceptance, ownership,
		attempt, lease, review, permission, executionPermission,
		FullCDPRuntimeCapabilities{
			StartEnabled: true, DisposableProfileEnabled: true, TransportEnabled: true,
		}, domain.BrowserCDPPermissionRuntimeCapabilities{
			ControlEnabled: true, FullDebugEnabled: true,
		}, executionCapabilities, executionFence, time.Now().UTC().Add(2*time.Second))
	if err == nil {
		t.Fatal("Full CDP start accepted a browser permission from another Run")
	}
}
