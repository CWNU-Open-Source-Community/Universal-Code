package browserruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cyberagent-workbench/internal/domain"
)

const (
	ProfileMarkerProtocolVersion       = "browser_profile_marker.v1"
	ProfileRuntimeLeaseProtocolVersion = "browser_profile_runtime_lease.v1"
	MaxProfileMarkerBytes              = 16 * 1024
	profileCleanupRetryInterval        = 25 * time.Millisecond
	maxFullCDPProfileReconcileEntries  = 256
	maxFullCDPProfileReconcileProofs   = 32
)

var profileEnvironmentDirectoryNames = [...]string{"Temp", "LocalAppData", "RoamingAppData"}

var (
	profileCleanupRetryTimeout         = 5 * time.Second
	profileTreeRemoveAll               = os.RemoveAll
	profileEnvironmentDirectoryEnsurer = ensureProfileEnvironmentDirectories
)

type profileCleanupJanitorJob struct {
	done chan struct{}
	err  error
}

var profileCleanupJanitors = struct {
	sync.Mutex
	jobs map[string]*profileCleanupJanitorJob
}{
	jobs: make(map[string]*profileCleanupJanitorJob),
}

// PrepareFullCDPProfileRuntimeRoot creates only the fixed Desktop-owned
// hierarchy below an already-existing, direct application home. Every level is
// created independently and revalidated before the next one is touched; this
// deliberately avoids applying MkdirAll to a caller-controlled path.
func PrepareFullCDPProfileRuntimeRoot(homePath string) (string, error) {
	homePath = filepath.FromSlash(strings.TrimSpace(homePath))
	if homePath == "" || !filepath.IsAbs(homePath) || filepath.Clean(homePath) != homePath ||
		!profilePathHasNoIndirection(homePath) {
		return "", errors.New("full CDP application home is unavailable or indirect")
	}
	runtimePath := filepath.Join(homePath, "runtime")
	fullCDPPath := filepath.Join(runtimePath, "full-cdp")
	rootPath := filepath.Join(fullCDPPath, ProfileRuntimeRootName)
	for _, candidate := range []string{runtimePath, fullCDPPath, rootPath} {
		if !pathWithinRoot(homePath, candidate) {
			return "", ErrBrowserRuntimeBoundary
		}
		if err := os.Mkdir(candidate, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("create fixed full CDP Profile runtime hierarchy: %w", err)
		}
		if !profilePathHasNoIndirection(candidate) {
			return "", errors.New("full CDP Profile runtime hierarchy is indirect")
		}
	}
	if !validProfileRuntimeRoot(rootPath) {
		return "", errors.New("full CDP Profile runtime root is invalid")
	}
	if err := reconcileFullCDPProfileCleanup(rootPath, profileCleanupRetryTimeout); err != nil {
		return "", fmt.Errorf("reconcile Full CDP Profile cleanup: %w", err)
	}
	return rootPath, nil
}

type fullCDPProfileCleanupCandidate struct {
	proofPath        string
	proofFingerprint string
	quarantineName   string
	quarantinePath   string
}

// reconcileFullCDPProfileCleanup resumes only cleanups that already crossed
// the release boundary and persisted an exact owner proof outside the tree
// being deleted. Ordinary Profiles and unproved quarantine-like entries are
// intentionally ignored.
func reconcileFullCDPProfileCleanup(rootPath string, timeout time.Duration) error {
	if !validProfileRuntimeRoot(rootPath) || !profilePathHasNoIndirection(rootPath) ||
		timeout <= 0 {
		return errors.New("Full CDP Profile cleanup reconciliation root is invalid")
	}
	root, err := os.Open(rootPath)
	if err != nil {
		return fmt.Errorf("open Full CDP Profile runtime root: %w", err)
	}
	entries, readErr := root.ReadDir(maxFullCDPProfileReconcileEntries + 1)
	closeErr := root.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return fmt.Errorf("read bounded Full CDP Profile runtime root: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close Full CDP Profile runtime root: %w", closeErr)
	}
	if len(entries) > maxFullCDPProfileReconcileEntries {
		return errors.New("Full CDP Profile cleanup reconciliation entry limit exceeded")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	candidates := make([]fullCDPProfileCleanupCandidate, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".cleanup-") || !strings.HasSuffix(name, ".owner.json") {
			continue
		}
		if len(candidates) >= maxFullCDPProfileReconcileProofs {
			return errors.New("Full CDP Profile cleanup reconciliation proof limit exceeded")
		}
		proofPath := filepath.Join(rootPath, name)
		if !pathWithinRoot(rootPath, proofPath) || filepath.Base(proofPath) != name {
			return errors.New("Full CDP Profile cleanup proof escaped its runtime root")
		}
		marker, err := readProfileOwnerMarkerFile(proofPath)
		if err != nil || marker.State != ProfileMarkerReleased || marker.PersonalProfile ||
			marker.ModelOwned {
			return fmt.Errorf("Full CDP Profile cleanup proof %q is invalid", name)
		}
		quarantineName := profileCleanupQuarantineName(marker.OwnerToken, marker.Generation)
		if name != quarantineName+".owner.json" {
			return fmt.Errorf("Full CDP Profile cleanup proof %q does not match its owner", name)
		}
		quarantinePath := filepath.Join(rootPath, quarantineName)
		if !pathWithinRoot(rootPath, quarantinePath) ||
			filepath.Base(quarantinePath) != quarantineName {
			return errors.New("Full CDP Profile cleanup target escaped its runtime root")
		}
		if err := validateReconciledProfileCleanupTarget(quarantinePath); err != nil {
			return fmt.Errorf("validate Full CDP Profile cleanup target %q: %w", quarantineName, err)
		}
		candidates = append(candidates, fullCDPProfileCleanupCandidate{
			proofPath: proofPath, proofFingerprint: marker.Fingerprint,
			quarantineName: quarantineName,
			quarantinePath: quarantinePath,
		})
	}

	deadline := time.Now().Add(timeout)
	for _, candidate := range candidates {
		marker, err := readProfileOwnerMarkerFile(candidate.proofPath)
		if err != nil || marker.State != ProfileMarkerReleased || marker.PersonalProfile ||
			marker.ModelOwned ||
			marker.Fingerprint != candidate.proofFingerprint ||
			profileCleanupQuarantineName(marker.OwnerToken, marker.Generation) != candidate.quarantineName {
			return fmt.Errorf("Full CDP Profile cleanup proof %q changed during reconciliation",
				filepath.Base(candidate.proofPath))
		}
		if err := validateReconciledProfileCleanupTarget(candidate.quarantinePath); err != nil {
			return fmt.Errorf("revalidate Full CDP Profile cleanup target %q: %w",
				candidate.quarantineName, err)
		}
		if _, err := os.Lstat(candidate.quarantinePath); err == nil {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return errors.New("Full CDP Profile cleanup reconciliation deadline exhausted")
			}
			if err := removeProfileTreeBounded(candidate.quarantinePath, remaining,
				profileTreeRemoveAll); err != nil {
				return fmt.Errorf("resume Full CDP Profile cleanup %q: %w",
					candidate.quarantineName, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect Full CDP Profile cleanup target %q: %w",
				candidate.quarantineName, err)
		}
		completedProof, err := readProfileOwnerMarkerFile(candidate.proofPath)
		if err != nil {
			return fmt.Errorf("revalidate completed Full CDP Profile cleanup proof: %w", err)
		}
		if completedProof.Fingerprint != candidate.proofFingerprint {
			return errors.New("completed Full CDP Profile cleanup proof changed")
		}
		if err := os.Remove(candidate.proofPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove completed Full CDP Profile cleanup proof: %w", err)
		}
	}
	return nil
}

func validateReconciledProfileCleanupTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		!profilePathHasNoIndirection(path) {
		return errors.New("proved cleanup target is not a direct directory")
	}
	return nil
}

func profileCleanupQuarantineName(ownerToken string, generation uint64) string {
	if len(ownerToken) < 16 || generation == 0 {
		return ""
	}
	return ".cleanup-" + ownerToken[:16] + "-" + strconv.FormatUint(generation, 10)
}

type ProfileMarkerState string

const (
	ProfileMarkerActive   ProfileMarkerState = "active"
	ProfileMarkerReleased ProfileMarkerState = "released"
)

// ProfileOwnerMarker is the only ownership evidence trusted by the concrete
// filesystem adapter. It contains no host identity, secret, or cleanup grant.
type ProfileOwnerMarker struct {
	ProtocolVersion               string             `json:"protocol_version"`
	OwnershipPlanFingerprint      string             `json:"ownership_plan_fingerprint"`
	PreviousOwnershipFingerprint  string             `json:"previous_ownership_fingerprint,omitempty"`
	SessionPlanFingerprint        string             `json:"session_plan_fingerprint"`
	ExecutableIdentityFingerprint string             `json:"executable_identity_fingerprint"`
	ProfileToken                  string             `json:"profile_token"`
	OwnerToken                    string             `json:"owner_token"`
	MarkerPayloadSHA256           string             `json:"marker_payload_sha256"`
	Generation                    uint64             `json:"generation"`
	State                         ProfileMarkerState `json:"state"`
	CreatedAt                     time.Time          `json:"created_at"`
	ReleasedAt                    time.Time          `json:"released_at,omitempty"`
	PersonalProfile               bool               `json:"personal_profile"`
	ModelOwned                    bool               `json:"model_owned"`
	Fingerprint                   string             `json:"fingerprint"`
}

type ProfileRuntimeLease struct {
	ProtocolVersion           string             `json:"protocol_version"`
	OwnershipPlanFingerprint  string             `json:"ownership_plan_fingerprint"`
	AuthorizationFingerprint  string             `json:"authorization_fingerprint"`
	DirectoryPath             string             `json:"directory_path"`
	OwnerToken                string             `json:"owner_token"`
	MarkerFingerprint         string             `json:"marker_fingerprint"`
	Generation                uint64             `json:"generation"`
	State                     ProfileMarkerState `json:"state"`
	CreatedAt                 time.Time          `json:"created_at"`
	ReleasedAt                time.Time          `json:"released_at,omitempty"`
	PersonalProfile           bool               `json:"personal_profile"`
	ExactOwnedCleanupRequired bool               `json:"exact_owned_cleanup_required"`
	Fingerprint               string             `json:"fingerprint"`
}

func MaterializeDisposableProfile(authorization BrowserStartAuthorization,
	session SessionPlan, identity BrowserExecutableIdentity,
	acceptance BrowserAcceptanceCandidate, ownership ProfileOwnershipPlan,
	attempt BrowserLaunchAttempt, launchLease BrowserLaunchLease,
	review BrowserLaunchReview,
	networkEvidence BrowserNetworkContainmentEvidence,
	networkReview BrowserNetworkContainmentReview,
	networkPlan BrowserNetworkContainmentPlan,
	permission domain.RunBrowserCDPPermissionSnapshot,
	now time.Time,
) (ProfileRuntimeLease, error) {
	if err := ValidateBrowserStartAuthorization(authorization, session, identity,
		acceptance, ownership, attempt, launchLease, review, networkEvidence,
		networkReview, networkPlan, permission); err != nil {
		return ProfileRuntimeLease{}, err
	}
	now = now.UTC()
	if now.IsZero() || now.Before(authorization.IssuedAt) ||
		!now.Before(authorization.StartDeadline) {
		return ProfileRuntimeLease{}, errors.New("browser Profile materialization authorization expired")
	}
	return materializeDisposableProfile(authorization.Fingerprint, ownership, now)
}

// MaterializeFullCDPProfile creates the same exact-owned disposable Profile as
// Safe Web, but binds the lease to the independent Full CDP start
// authorization. It never accepts a Safe Web authorization as a substitute.
func MaterializeFullCDPProfile(authorization FullCDPStartAuthorization,
	session SessionPlan, identity BrowserExecutableIdentity,
	acceptance BrowserAcceptanceCandidate, ownership ProfileOwnershipPlan,
	attempt BrowserLaunchAttempt, launchLease BrowserLaunchLease,
	review BrowserLaunchReview,
	permission domain.RunBrowserCDPPermissionSnapshot,
	executionPermission domain.RunExecutionPermissionSnapshot,
	executionCapabilities domain.ExecutionPermissionRuntimeCapabilities,
	now time.Time,
) (ProfileRuntimeLease, error) {
	if err := ValidateFullCDPStartAuthorization(authorization, session, identity,
		acceptance, ownership, attempt, launchLease, review, permission,
		executionPermission, executionCapabilities); err != nil {
		return ProfileRuntimeLease{}, err
	}
	now = now.UTC()
	if now.IsZero() || now.Before(authorization.IssuedAt) ||
		!now.Before(authorization.StartDeadline) {
		return ProfileRuntimeLease{}, errors.New(
			"full CDP Profile materialization authorization expired")
	}
	return materializeDisposableProfile(authorization.Fingerprint, ownership, now)
}

func materializeDisposableProfile(authorizationFingerprint string,
	ownership ProfileOwnershipPlan, now time.Time,
) (ProfileRuntimeLease, error) {
	if err := ensureProfileRuntimeRoot(ownership.RootPath); err != nil {
		return ProfileRuntimeLease{}, err
	}
	marker := newProfileOwnerMarker(ownership, now)
	if ownership.Generation == 1 {
		if err := createInitialProfileDirectory(ownership, marker); err != nil {
			return ProfileRuntimeLease{}, err
		}
	} else if err := recoverProfileDirectory(ownership, marker); err != nil {
		return ProfileRuntimeLease{}, err
	}
	lease := ProfileRuntimeLease{
		ProtocolVersion:           ProfileRuntimeLeaseProtocolVersion,
		OwnershipPlanFingerprint:  ownership.Fingerprint,
		AuthorizationFingerprint:  authorizationFingerprint,
		DirectoryPath:             ownership.DirectoryPath,
		OwnerToken:                ownership.OwnerToken,
		MarkerFingerprint:         marker.Fingerprint,
		Generation:                ownership.Generation,
		State:                     ProfileMarkerActive,
		CreatedAt:                 marker.CreatedAt,
		ExactOwnedCleanupRequired: true,
	}
	lease.Fingerprint = browserRuntimeFingerprint(lease)
	if err := profileEnvironmentDirectoryEnsurer(ownership.DirectoryPath); err != nil {
		recoveryLease, cleanupErr := cleanupFailedProfileMaterialization(
			authorizationFingerprint, lease, ownership)
		if cleanupErr != nil {
			return recoveryLease, errors.Join(err, cleanupErr)
		}
		return ProfileRuntimeLease{}, err
	}
	if err := validateProfileRuntimeLeaseBinding(lease, authorizationFingerprint,
		ownership); err != nil {
		recoveryLease, cleanupErr := cleanupFailedProfileMaterialization(
			authorizationFingerprint, lease, ownership)
		if cleanupErr != nil {
			return recoveryLease, errors.Join(err, cleanupErr)
		}
		return ProfileRuntimeLease{}, err
	}
	return lease, nil
}

func cleanupFailedProfileMaterialization(authorizationFingerprint string,
	lease ProfileRuntimeLease, ownership ProfileOwnershipPlan,
) (ProfileRuntimeLease, error) {
	cleanupAt := time.Now().UTC()
	if !cleanupAt.After(lease.CreatedAt) {
		cleanupAt = lease.CreatedAt.Add(time.Nanosecond)
	}
	released, err := releaseDisposableProfile(true, lease, ownership, true,
		cleanupAt)
	if err != nil {
		return lease, err
	}
	if err := cleanupReleasedProfile(authorizationFingerprint, true, released,
		ownership, true); err != nil {
		return released, err
	}
	return ProfileRuntimeLease{}, nil
}

func ValidateProfileRuntimeLease(lease ProfileRuntimeLease,
	authorization BrowserStartAuthorization, ownership ProfileOwnershipPlan,
) error {
	return validateProfileRuntimeLeaseBinding(lease, authorization.Fingerprint,
		ownership)
}

// ValidateFullCDPProfileRuntimeLease validates a Profile lease against the
// independent Full CDP launch authorization.
func ValidateFullCDPProfileRuntimeLease(lease ProfileRuntimeLease,
	authorization FullCDPStartAuthorization, ownership ProfileOwnershipPlan,
) error {
	return validateProfileRuntimeLeaseBinding(lease, authorization.Fingerprint,
		ownership)
}

func validateProfileRuntimeLeaseBinding(lease ProfileRuntimeLease,
	authorizationFingerprint string, ownership ProfileOwnershipPlan,
) error {
	if err := validateProfileOwnershipStructure(ownership); err != nil {
		return err
	}
	if lease.ProtocolVersion != ProfileRuntimeLeaseProtocolVersion ||
		lease.OwnershipPlanFingerprint != ownership.Fingerprint ||
		lease.AuthorizationFingerprint != authorizationFingerprint ||
		lease.DirectoryPath != ownership.DirectoryPath ||
		lease.OwnerToken != ownership.OwnerToken || !validSHA256(lease.MarkerFingerprint) ||
		lease.Generation != ownership.Generation || lease.State != ProfileMarkerActive ||
		lease.CreatedAt.IsZero() || !lease.ReleasedAt.IsZero() || lease.PersonalProfile ||
		!lease.ExactOwnedCleanupRequired ||
		lease.Fingerprint != browserRuntimeFingerprint(lease) {
		return errors.New("browser Profile runtime lease lost an exact ownership boundary")
	}
	return nil
}

// ObserveDisposableProfile reads only the bounded owner marker. The caller
// supplies process liveness from the process-tree owner; marker contents never
// decide that a process is alive.
func ObserveDisposableProfile(ownership ProfileOwnershipPlan,
	processActive bool,
) (ProfileDirectoryObservation, error) {
	if err := validateProfileOwnershipStructure(ownership); err != nil {
		return ProfileDirectoryObservation{}, err
	}
	info, err := os.Lstat(ownership.DirectoryPath)
	if errors.Is(err, os.ErrNotExist) {
		return BuildProfileDirectoryObservation(ownership, ProfileDirectoryAbsent, "", 0, "")
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		!profilePathHasNoIndirection(ownership.DirectoryPath) {
		return BuildProfileDirectoryObservation(ownership, ProfileDirectoryCorrupt, "", 0, "")
	}
	marker, err := readProfileOwnerMarker(ownership.DirectoryPath)
	if err != nil {
		return BuildProfileDirectoryObservation(ownership, ProfileDirectoryCorrupt, "", 0, "")
	}
	state := ProfileDirectoryForeign
	if markerMatchesOwnership(marker, ownership) {
		switch marker.State {
		case ProfileMarkerReleased:
			state = ProfileDirectoryOwnedReleased
		case ProfileMarkerActive:
			if processActive {
				state = ProfileDirectoryOwnedActive
			} else {
				state = ProfileDirectoryOwnedStale
			}
		}
	}
	return BuildProfileDirectoryObservation(ownership, state, marker.OwnerToken,
		marker.Generation, marker.MarkerPayloadSHA256)
}

func ReleaseDisposableProfile(authorization BrowserStartAuthorization,
	lease ProfileRuntimeLease, ownership ProfileOwnershipPlan,
	processTreeQuiescent bool, now time.Time,
) (ProfileRuntimeLease, error) {
	if err := ValidateProfileRuntimeLease(lease, authorization, ownership); err != nil {
		return ProfileRuntimeLease{}, err
	}
	return releaseDisposableProfile(authorization.ProfileReleaseAuthorized,
		lease, ownership, processTreeQuiescent, now)
}

// ReleaseFullCDPProfile marks only the exact Full-CDP-owned Profile as released
// after its complete process tree is quiescent.
func ReleaseFullCDPProfile(authorization FullCDPStartAuthorization,
	lease ProfileRuntimeLease, ownership ProfileOwnershipPlan,
	processTreeQuiescent bool, now time.Time,
) (ProfileRuntimeLease, error) {
	if err := ValidateFullCDPProfileRuntimeLease(lease, authorization, ownership); err != nil {
		return ProfileRuntimeLease{}, err
	}
	return releaseDisposableProfile(authorization.ProfileReleaseAuthorized,
		lease, ownership, processTreeQuiescent, now)
}

func releaseDisposableProfile(profileReleaseAuthorized bool,
	lease ProfileRuntimeLease, ownership ProfileOwnershipPlan,
	processTreeQuiescent bool, now time.Time,
) (ProfileRuntimeLease, error) {
	now = now.UTC()
	if !profileReleaseAuthorized || !processTreeQuiescent || now.IsZero() ||
		now.Before(lease.CreatedAt) {
		return ProfileRuntimeLease{}, errors.New("browser Profile release requires a quiescent exact process tree")
	}
	marker, err := readProfileOwnerMarker(ownership.DirectoryPath)
	if err != nil || !markerMatchesOwnership(marker, ownership) ||
		marker.State != ProfileMarkerActive {
		return ProfileRuntimeLease{}, errors.New("browser Profile release marker no longer matches its active owner")
	}
	marker.State = ProfileMarkerReleased
	marker.ReleasedAt = now
	marker.Fingerprint = ""
	marker.Fingerprint = browserRuntimeFingerprint(marker)
	if err := writeProfileOwnerMarkerAtomic(ownership.DirectoryPath, marker); err != nil {
		return ProfileRuntimeLease{}, err
	}
	released := lease
	released.State = ProfileMarkerReleased
	released.ReleasedAt = now
	released.MarkerFingerprint = marker.Fingerprint
	released.Fingerprint = ""
	released.Fingerprint = browserRuntimeFingerprint(released)
	return released, nil
}

func CleanupReleasedProfile(authorization BrowserStartAuthorization,
	lease ProfileRuntimeLease, ownership ProfileOwnershipPlan,
	processTreeQuiescent bool,
) error {
	return cleanupReleasedProfile(authorization.Fingerprint,
		authorization.ExactOwnedCleanupAuthorized, lease, ownership,
		processTreeQuiescent)
}

// CleanupReleasedFullCDPProfile removes only the exact released Profile owned
// by the Full CDP authorization.
func CleanupReleasedFullCDPProfile(authorization FullCDPStartAuthorization,
	lease ProfileRuntimeLease, ownership ProfileOwnershipPlan,
	processTreeQuiescent bool,
) error {
	return cleanupReleasedProfile(authorization.Fingerprint,
		authorization.ExactOwnedCleanupAuthorized, lease, ownership,
		processTreeQuiescent)
}

func cleanupReleasedProfile(authorizationFingerprint string,
	exactOwnedCleanupAuthorized bool, lease ProfileRuntimeLease,
	ownership ProfileOwnershipPlan, processTreeQuiescent bool,
) error {
	if lease.ProtocolVersion != ProfileRuntimeLeaseProtocolVersion ||
		lease.AuthorizationFingerprint != authorizationFingerprint ||
		lease.OwnershipPlanFingerprint != ownership.Fingerprint ||
		lease.DirectoryPath != ownership.DirectoryPath || lease.OwnerToken != ownership.OwnerToken ||
		lease.Generation != ownership.Generation || lease.State != ProfileMarkerReleased ||
		lease.ReleasedAt.IsZero() || lease.PersonalProfile ||
		!lease.ExactOwnedCleanupRequired || lease.Fingerprint != browserRuntimeFingerprint(lease) ||
		!exactOwnedCleanupAuthorized || !processTreeQuiescent {
		return errors.New("browser Profile cleanup requires an exact released owner and quiescent tree")
	}
	if err := ensureProfileRuntimeRoot(ownership.RootPath); err != nil {
		return err
	}
	if !pathWithinRoot(ownership.RootPath, ownership.DirectoryPath) ||
		filepath.Base(ownership.DirectoryPath) != ownership.DirectoryName {
		return errors.New("browser Profile cleanup path is not the exact owned directory")
	}
	quarantineName := profileCleanupQuarantineName(ownership.OwnerToken, ownership.Generation)
	quarantinePath := filepath.Join(ownership.RootPath, quarantineName)
	if !pathWithinRoot(ownership.RootPath, quarantinePath) ||
		filepath.Base(quarantinePath) != quarantineName {
		return errors.New("browser Profile cleanup quarantine escaped its runtime root")
	}
	proofName := quarantineName + ".owner.json"
	proofPath := filepath.Join(ownership.RootPath, proofName)
	if !pathWithinRoot(ownership.RootPath, proofPath) ||
		filepath.Base(proofPath) != proofName {
		return errors.New("browser Profile cleanup proof escaped its runtime root")
	}

	originalInfo, originalErr := os.Lstat(ownership.DirectoryPath)
	quarantineInfo, quarantineErr := os.Lstat(quarantinePath)
	proofInfo, proofErr := os.Lstat(proofPath)
	if originalErr != nil && !errors.Is(originalErr, os.ErrNotExist) {
		return fmt.Errorf("inspect exact browser Profile for cleanup: %w", originalErr)
	}
	if quarantineErr != nil && !errors.Is(quarantineErr, os.ErrNotExist) {
		return fmt.Errorf("inspect browser Profile cleanup quarantine: %w", quarantineErr)
	}
	if proofErr != nil && !errors.Is(proofErr, os.ErrNotExist) {
		return fmt.Errorf("inspect browser Profile cleanup proof: %w", proofErr)
	}
	originalExists := originalErr == nil
	quarantineExists := quarantineErr == nil
	proofExists := proofErr == nil
	if originalExists && quarantineExists {
		return errors.New("browser Profile cleanup found both the owned directory and its quarantine")
	}
	if !originalExists && !quarantineExists {
		if !proofExists {
			return nil
		}
		if err := validateProfileCleanupProof(proofPath, proofInfo, ownership, lease); err != nil {
			return err
		}
		if err := os.Remove(proofPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove completed browser Profile cleanup proof: %w", err)
		}
		return nil
	}

	validateDirectTarget := func(path string, info os.FileInfo) error {
		if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			!profilePathHasNoIndirection(path) {
			return errors.New("browser Profile cleanup target is unavailable or indirect")
		}
		return nil
	}
	readReleasedMarker := func(path string, info os.FileInfo) (ProfileOwnerMarker, error) {
		if err := validateDirectTarget(path, info); err != nil {
			return ProfileOwnerMarker{}, err
		}
		marker, err := readProfileOwnerMarker(path)
		if err != nil || !markerMatchesOwnership(marker, ownership) ||
			marker.State != ProfileMarkerReleased || marker.Fingerprint != lease.MarkerFingerprint {
			return ProfileOwnerMarker{}, errors.New(
				"browser Profile cleanup marker no longer matches its released owner")
		}
		return marker, nil
	}
	ensureProof := func(marker ProfileOwnerMarker) error {
		if proofExists {
			return validateProfileCleanupProof(proofPath, proofInfo, ownership, lease)
		}
		if err := writeProfileCleanupProofExclusive(proofPath, marker); err != nil {
			return err
		}
		proofInfo, proofErr = os.Lstat(proofPath)
		proofExists = proofErr == nil
		if proofErr != nil {
			return fmt.Errorf("inspect created browser Profile cleanup proof: %w", proofErr)
		}
		return validateProfileCleanupProof(proofPath, proofInfo, ownership, lease)
	}

	if originalExists {
		marker, err := readReleasedMarker(ownership.DirectoryPath, originalInfo)
		if err != nil {
			return err
		}
		if err := ensureProof(marker); err != nil {
			return err
		}
		if err := os.Rename(ownership.DirectoryPath, quarantinePath); err != nil {
			return fmt.Errorf("rename exact browser Profile for cleanup: %w", err)
		}
		quarantineInfo, quarantineErr = os.Lstat(quarantinePath)
		if quarantineErr != nil {
			_ = os.Rename(quarantinePath, ownership.DirectoryPath)
			return errors.New("renamed browser Profile failed its final owner recheck")
		}
		if _, markerErr := readReleasedMarker(quarantinePath, quarantineInfo); markerErr != nil {
			_ = os.Rename(quarantinePath, ownership.DirectoryPath)
			return errors.New("renamed browser Profile failed its final owner recheck")
		}
	} else {
		if err := validateDirectTarget(quarantinePath, quarantineInfo); err != nil {
			return fmt.Errorf("browser Profile cleanup quarantine is not direct: %w", err)
		}
		if proofExists {
			if err := validateProfileCleanupProof(proofPath, proofInfo, ownership, lease); err != nil {
				return err
			}
		} else {
			marker, err := readReleasedMarker(quarantinePath, quarantineInfo)
			if err != nil {
				return fmt.Errorf(
					"browser Profile cleanup quarantine is not the exact released owner: %w", err)
			}
			if err := ensureProof(marker); err != nil {
				return err
			}
		}
	}

	if err := removeProfileTreeBounded(quarantinePath, profileCleanupRetryTimeout,
		profileTreeRemoveAll); err != nil {
		return fmt.Errorf("remove exact released browser Profile: %w", err)
	}
	proofInfo, proofErr = os.Lstat(proofPath)
	if proofErr == nil {
		if err := validateProfileCleanupProof(proofPath, proofInfo, ownership, lease); err != nil {
			return err
		}
		if err := os.Remove(proofPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove browser Profile cleanup proof: %w", err)
		}
	} else if !errors.Is(proofErr, os.ErrNotExist) {
		return fmt.Errorf("inspect completed browser Profile cleanup proof: %w", proofErr)
	}
	return nil
}

func writeProfileCleanupProofExclusive(path string, marker ProfileOwnerMarker) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) == path {
		return errors.New("browser Profile cleanup proof path is invalid")
	}
	if err := validateProfileOwnerMarker(marker); err != nil || marker.State != ProfileMarkerReleased {
		return errors.New("browser Profile cleanup proof marker is invalid")
	}
	raw, err := json.Marshal(marker)
	if err != nil || len(raw) == 0 || len(raw) > MaxProfileMarkerBytes {
		return errors.New("browser Profile cleanup proof is oversized")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create browser Profile cleanup proof: %w", err)
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.Join(err, closeErr)
	}
	return nil
}

func validateProfileCleanupProof(path string, info os.FileInfo,
	ownership ProfileOwnershipPlan, lease ProfileRuntimeLease,
) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		!profilePathHasNoIndirection(path) {
		return errors.New("browser Profile cleanup proof is unavailable or indirect")
	}
	marker, err := readProfileOwnerMarkerFile(path)
	if err != nil || !markerMatchesOwnership(marker, ownership) ||
		marker.State != ProfileMarkerReleased || marker.Fingerprint != lease.MarkerFingerprint {
		return errors.New("browser Profile cleanup proof does not match its released owner")
	}
	return nil
}

func removeProfileTreeBounded(path string, timeout time.Duration,
	remove func(string) error,
) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Dir(path) == path ||
		timeout <= 0 || remove == nil {
		return errors.New("browser Profile cleanup retry target is invalid")
	}
	profileCleanupJanitors.Lock()
	job := profileCleanupJanitors.jobs[path]
	if job == nil {
		job = &profileCleanupJanitorJob{done: make(chan struct{})}
		profileCleanupJanitors.jobs[path] = job
		go runProfileCleanupJanitor(path, timeout, remove, job)
	}
	profileCleanupJanitors.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-job.done:
		return job.err
	case <-timer.C:
		return errors.New("browser Profile cleanup timed out while its exact-path janitor continues")
	}
}

func runProfileCleanupJanitor(path string, timeout time.Duration,
	remove func(string) error, job *profileCleanupJanitorJob,
) {
	job.err = removeProfileTreeWithRetry(path, timeout, remove)
	profileCleanupJanitors.Lock()
	if profileCleanupJanitors.jobs[path] == job {
		delete(profileCleanupJanitors.jobs, path)
	}
	profileCleanupJanitors.Unlock()
	close(job.done)
}

func removeProfileTreeWithRetry(path string, timeout time.Duration,
	remove func(string) error,
) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := remove(path); err != nil {
			lastErr = err
		} else if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			lastErr = err
		} else {
			lastErr = errors.New("browser Profile cleanup left the exact directory present")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.Join(errors.New("browser Profile cleanup retry limit exhausted"), lastErr)
		}
		wait := profileCleanupRetryInterval
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		<-timer.C
	}
}

func newProfileOwnerMarker(ownership ProfileOwnershipPlan, now time.Time) ProfileOwnerMarker {
	marker := ProfileOwnerMarker{
		ProtocolVersion:               ProfileMarkerProtocolVersion,
		OwnershipPlanFingerprint:      ownership.Fingerprint,
		PreviousOwnershipFingerprint:  ownership.PreviousOwnershipFingerprint,
		SessionPlanFingerprint:        ownership.SessionPlanFingerprint,
		ExecutableIdentityFingerprint: ownership.ExecutableIdentityFingerprint,
		ProfileToken:                  ownership.ProfileToken,
		OwnerToken:                    ownership.OwnerToken,
		MarkerPayloadSHA256:           ownership.MarkerPayloadSHA256,
		Generation:                    ownership.Generation,
		State:                         ProfileMarkerActive,
		CreatedAt:                     now.UTC(),
	}
	marker.Fingerprint = browserRuntimeFingerprint(marker)
	return marker
}

func validateProfileOwnerMarker(marker ProfileOwnerMarker) error {
	if marker.ProtocolVersion != ProfileMarkerProtocolVersion ||
		!validSHA256(marker.OwnershipPlanFingerprint) ||
		(marker.PreviousOwnershipFingerprint != "" &&
			!validSHA256(marker.PreviousOwnershipFingerprint)) ||
		!validSHA256(marker.SessionPlanFingerprint) ||
		!validSHA256(marker.ExecutableIdentityFingerprint) ||
		!validSHA256(marker.ProfileToken) || !validSHA256(marker.OwnerToken) ||
		!validSHA256(marker.MarkerPayloadSHA256) || marker.Generation == 0 ||
		marker.Generation > MaxProfileOwnershipGeneration || marker.CreatedAt.IsZero() ||
		marker.PersonalProfile || marker.ModelOwned {
		return errors.New("browser Profile owner marker is invalid")
	}
	switch marker.State {
	case ProfileMarkerActive:
		if !marker.ReleasedAt.IsZero() {
			return errors.New("active browser Profile marker contains a release time")
		}
	case ProfileMarkerReleased:
		if marker.ReleasedAt.IsZero() || marker.ReleasedAt.Before(marker.CreatedAt) {
			return errors.New("released browser Profile marker has an invalid release time")
		}
	default:
		return errors.New("browser Profile owner marker state is invalid")
	}
	if marker.Fingerprint != browserRuntimeFingerprint(marker) {
		return errors.New("browser Profile owner marker fingerprint mismatch")
	}
	return nil
}

func markerMatchesOwnership(marker ProfileOwnerMarker,
	ownership ProfileOwnershipPlan,
) bool {
	return validateProfileOwnerMarker(marker) == nil &&
		marker.OwnershipPlanFingerprint == ownership.Fingerprint &&
		marker.PreviousOwnershipFingerprint == ownership.PreviousOwnershipFingerprint &&
		marker.SessionPlanFingerprint == ownership.SessionPlanFingerprint &&
		marker.ExecutableIdentityFingerprint == ownership.ExecutableIdentityFingerprint &&
		marker.ProfileToken == ownership.ProfileToken && marker.OwnerToken == ownership.OwnerToken &&
		marker.MarkerPayloadSHA256 == ownership.MarkerPayloadSHA256 &&
		marker.Generation == ownership.Generation
}

func createInitialProfileDirectory(ownership ProfileOwnershipPlan,
	marker ProfileOwnerMarker,
) error {
	if ownership.Generation != 1 || ownership.PreviousOwnershipFingerprint != "" {
		return errors.New("initial browser Profile creation requires generation one")
	}
	if _, err := os.Lstat(ownership.DirectoryPath); !errors.Is(err, os.ErrNotExist) {
		return errors.New("browser Profile directory already exists")
	}
	if err := os.Mkdir(ownership.DirectoryPath, 0o700); err != nil {
		return fmt.Errorf("create disposable browser Profile: %w", err)
	}
	if !profilePathHasNoIndirection(ownership.DirectoryPath) {
		_ = os.Remove(ownership.DirectoryPath)
		return errors.New("created browser Profile directory is indirect")
	}
	if err := writeProfileOwnerMarkerExclusive(ownership.DirectoryPath, marker); err != nil {
		_ = os.Remove(ownership.DirectoryPath)
		return err
	}
	return nil
}

func recoverProfileDirectory(ownership ProfileOwnershipPlan,
	marker ProfileOwnerMarker,
) error {
	if ownership.Generation <= 1 || !validSHA256(ownership.PreviousOwnershipFingerprint) {
		return errors.New("browser Profile recovery requires generation ancestry")
	}
	if !profilePathHasNoIndirection(ownership.DirectoryPath) {
		return errors.New("browser Profile recovery path is unavailable or indirect")
	}
	previous, err := readProfileOwnerMarker(ownership.DirectoryPath)
	if err != nil || previous.OwnershipPlanFingerprint != ownership.PreviousOwnershipFingerprint ||
		previous.Generation+1 != ownership.Generation || previous.State != ProfileMarkerActive ||
		previous.PersonalProfile || previous.ModelOwned {
		return errors.New("browser Profile recovery marker does not match the previous stale generation")
	}
	return writeProfileOwnerMarkerAtomic(ownership.DirectoryPath, marker)
}

func ensureProfileRuntimeRoot(rootPath string) error {
	if !validProfileRuntimeRoot(rootPath) {
		return errors.New("browser Profile runtime root is invalid")
	}
	parent := filepath.Dir(rootPath)
	if !profilePathHasNoIndirection(parent) {
		return errors.New("browser Profile runtime parent is unavailable or indirect")
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create browser Profile runtime root: %w", err)
	}
	info, err := os.Lstat(rootPath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		!profilePathHasNoIndirection(rootPath) {
		return errors.New("browser Profile runtime root is not a direct directory")
	}
	return nil
}

func ensureProfileEnvironmentDirectories(profilePath string) error {
	for _, name := range profileEnvironmentDirectoryNames {
		path := filepath.Join(profilePath, name)
		if !pathWithinRoot(profilePath, path) {
			return ErrBrowserRuntimeBoundary
		}
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if !profilePathHasNoIndirection(path) {
			return errors.New("browser environment directory is indirect")
		}
	}
	return nil
}

func writeProfileOwnerMarkerExclusive(directory string, marker ProfileOwnerMarker) error {
	if err := validateProfileOwnerMarker(marker); err != nil {
		return err
	}
	raw, err := json.Marshal(marker)
	if err != nil || len(raw) == 0 || len(raw) > MaxProfileMarkerBytes {
		return errors.New("browser Profile owner marker is oversized")
	}
	path := filepath.Join(directory, ProfileOwnerMarkerName)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create browser Profile owner marker: %w", err)
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	return closeErr
}

func writeProfileOwnerMarkerAtomic(directory string, marker ProfileOwnerMarker) error {
	if err := validateProfileOwnerMarker(marker); err != nil {
		return err
	}
	raw, err := json.Marshal(marker)
	if err != nil || len(raw) == 0 || len(raw) > MaxProfileMarkerBytes {
		return errors.New("browser Profile owner marker is oversized")
	}
	temporary := filepath.Join(directory, ".prayu-owner-"+marker.Fingerprint[:16]+".tmp")
	final := filepath.Join(directory, ProfileOwnerMarkerName)
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary browser Profile marker: %w", err)
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(temporary)
		return errors.Join(err, closeErr)
	}
	if err := platformReplaceProfileMarker(temporary, final); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace browser Profile owner marker: %w", err)
	}
	return nil
}

func readProfileOwnerMarker(directory string) (ProfileOwnerMarker, error) {
	path := filepath.Join(directory, ProfileOwnerMarkerName)
	return readProfileOwnerMarkerFile(path)
}

func readProfileOwnerMarkerFile(path string) (ProfileOwnerMarker, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() <= 0 || info.Size() > MaxProfileMarkerBytes ||
		!profilePathHasNoIndirection(path) {
		return ProfileOwnerMarker{}, errors.New("browser Profile owner marker is unavailable or indirect")
	}
	file, err := os.Open(path)
	if err != nil {
		return ProfileOwnerMarker{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, MaxProfileMarkerBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > MaxProfileMarkerBytes {
		return ProfileOwnerMarker{}, errors.New("browser Profile owner marker is unreadable or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var marker ProfileOwnerMarker
	if err := decoder.Decode(&marker); err != nil {
		return ProfileOwnerMarker{}, errors.New("browser Profile owner marker is malformed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ProfileOwnerMarker{}, errors.New("browser Profile owner marker has trailing data")
	}
	if err := validateProfileOwnerMarker(marker); err != nil {
		return ProfileOwnerMarker{}, err
	}
	canonical, err := json.Marshal(marker)
	if err != nil || !bytes.Equal(raw, canonical) {
		return ProfileOwnerMarker{}, errors.New("browser Profile owner marker is not canonical")
	}
	return marker, nil
}

func profilePathHasNoIndirection(path string) bool {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && samePath(path, resolved) && platformProfilePathDirect(path)
}

// Agent browser profiles use a separate ephemeral namespace and ownership proof.
type agentBrowserProfile struct {
	root, path, token string
	marker            []byte
}

func prepareAgentBrowserProfile(home string, authority AgentBrowserAuthority) (agentBrowserProfile, error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || !profilePathHasNoIndirection(home) {
		return agentBrowserProfile{}, ErrBrowserRuntimeBoundary
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		return agentBrowserProfile{}, ErrBrowserRuntimeBoundary
	}
	root := home
	for _, name := range []string{"runtime", "agent-browser", "profiles"} {
		root = filepath.Join(root, name)
		if !pathWithinRoot(home, root) {
			return agentBrowserProfile{}, ErrBrowserRuntimeBoundary
		}
		if err := os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return agentBrowserProfile{}, err
		}
		if !profilePathHasNoIndirection(root) {
			return agentBrowserProfile{}, ErrBrowserRuntimeBoundary
		}
	}
	token := agentBrowserToken()
	p := agentBrowserProfile{root: root, path: filepath.Join(root, token), token: token}
	p.marker, _ = json.Marshal(struct{ Version, Token, Authority string }{"agent_browser_profile.v1", token, browserRuntimeFingerprint(authority)})
	if err := os.Mkdir(p.path, 0700); err != nil {
		return agentBrowserProfile{}, err
	}
	if err := os.WriteFile(filepath.Join(p.path, ".agent-browser-owner.json"), p.marker, 0600); err != nil {
		return p, err
	}
	if err := ensureProfileEnvironmentDirectories(p.path); err != nil {
		return p, err
	}
	return p, nil
}

// The marker is checked before rename. An external proof remains if removal is
// interrupted, so cleanup retry does not depend on a partially deleted marker.
func (p agentBrowserProfile) cleanup() error {
	if p.path == "" {
		return nil
	}
	if !profilePathHasNoIndirection(p.root) || !pathWithinRoot(p.root, p.path) || filepath.Base(p.path) != p.token {
		return ErrBrowserRuntimeBoundary
	}
	quarantine := filepath.Join(p.root, "cleanup-"+p.token)
	proof := filepath.Join(p.root, ".cleanup-"+p.token+".json")
	if _, err := os.Lstat(p.path); err == nil {
		if !profilePathHasNoIndirection(p.path) {
			return ErrBrowserRuntimeBoundary
		}
		if err := p.verifyProof(filepath.Join(p.path, ".agent-browser-owner.json")); err != nil {
			return err
		}
		f, err := os.OpenFile(proof, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, err = f.Write(p.marker)
			syncErr := f.Sync()
			closeErr := f.Close()
			if err == nil {
				err = errors.Join(syncErr, closeErr)
			}
		}
		if err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := p.verifyProof(proof); err != nil {
			return err
		}
		if err := os.Rename(p.path, quarantine); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(quarantine); errors.Is(err, os.ErrNotExist) {
		if err := p.verifyProof(proof); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		return os.Remove(proof)
	} else if err != nil {
		return err
	}
	if !pathWithinRoot(p.root, quarantine) || !profilePathHasNoIndirection(quarantine) {
		return ErrBrowserRuntimeBoundary
	}
	if err := p.verifyProof(proof); err != nil {
		return err
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return err
	}
	return os.Remove(proof)
}

func (p agentBrowserProfile) verifyProof(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(p.marker)) || !profilePathHasNoIndirection(path) {
		return ErrBrowserRuntimeBoundary
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(raw) != string(p.marker) {
		return ErrBrowserRuntimeBoundary
	}
	return nil
}

// prepareAgentBrowserDownloadQuarantine creates the disposable download
// directory inside the session profile tree. It is the only filesystem writer
// for download captures, mirroring the profile lifecycle: the quarantine dies
// with the profile during cleanup.
func prepareAgentBrowserDownloadQuarantine(profilePath string) (string, error) {
	downloadPath := filepath.Join(profilePath, "downloads")
	if err := os.Mkdir(downloadPath, 0o700); err != nil {
		return "", err
	}
	return downloadPath, nil
}

// removeAgentBrowserDownloadCapture deletes one GUID-named capture, used when
// a download is rejected over the byte budget.
func removeAgentBrowserDownloadCapture(downloadPath, guid string) error {
	return os.Remove(filepath.Join(downloadPath, guid))
}

// statAgentBrowserDownloadCapture inspects the GUID-named capture without
// following any event-provided path.
func statAgentBrowserDownloadCapture(downloadPath, guid string) (os.FileInfo, error) {
	return os.Lstat(filepath.Join(downloadPath, guid))
}

// readAgentBrowserDownloadCapture reads the GUID-named capture for digesting.
func readAgentBrowserDownloadCapture(downloadPath, guid string) ([]byte, error) {
	return os.ReadFile(filepath.Join(downloadPath, guid))
}
