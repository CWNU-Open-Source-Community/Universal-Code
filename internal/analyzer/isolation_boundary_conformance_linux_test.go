//go:build linux

package analyzer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

const analyzerNamespaceUID = 65534

type linuxIsolationIdentityResult struct {
	UID               int    `json:"uid"`
	GID               int    `json:"gid"`
	UserNamespace     string `json:"user_namespace"`
	NoNewPrivileges   bool   `json:"no_new_privileges"`
	EffectiveCapsZero bool   `json:"effective_caps_zero"`
}

type linuxIsolationFilesystemResult struct {
	LandlockABI         int  `json:"landlock_abi"`
	InputRead           bool `json:"input_read"`
	InputWriteDenied    bool `json:"input_write_denied"`
	OutsideWriteDenied  bool `json:"outside_write_denied"`
	StagingWriteAllowed bool `json:"staging_write_allowed"`
}

func observeAnalyzerImmutableHandleHandoff(t *testing.T,
	approval AnalyzerScopeLimitsApproval,
) analyzerImmutableHandleObservation {
	t.Helper()
	original := []byte("caller-owned immutable analyzer image\n")
	replacement := []byte("path replacement must never become executable\n")
	directory := t.TempDir()
	path := filepath.Join(directory, "analyzer-image.bin")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	displaced := path + ".displaced"
	if err := os.Rename(path, displaced); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestAnalyzerIsolationBoundaryHelper$")
	command.Env = []string{
		analyzerIsolationHelperModeEnv + "=linux-handle",
		analyzerIsolationHandleEnv + "=3",
	}
	command.ExtraFiles = []*os.File{file}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("read inherited analyzer handle: %v output=%q", err, output)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	callerBytes, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	pathBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	originalDigest := analyzerIsolationBytesDigest(original)
	childDigest := strings.TrimSpace(string(output))
	return analyzerImmutableHandleObservation{
		Mechanism:            "linux_inherited_read_fd.v1",
		ScopeApprovalSHA256:  analyzerIsolationScopeDigest(t, approval),
		CallerHandleRetained: analyzerIsolationBytesDigest(callerBytes) == originalDigest,
		ChildHandleInherited: childDigest == originalDigest,
		PathReplacedBeforeChildRead: analyzerIsolationBytesDigest(pathBytes) ==
			analyzerIsolationBytesDigest(replacement),
		OriginalBytesObserved:    childDigest == originalDigest,
		ReplacementBytesRejected: childDigest != analyzerIsolationBytesDigest(replacement),
		TestConformanceOnly:      true,
	}
}

func observeAnalyzerLowPrivilegeIdentity(t *testing.T,
	approval AnalyzerScopeLimitsApproval,
) analyzerLowPrivilegeIdentityObservation {
	t.Helper()
	parentNamespace, err := os.Readlink("/proc/self/ns/user")
	if err != nil {
		t.Fatal(err)
	}
	command := newLinuxIsolationCommand("linux-identity")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("start namespaced identity helper: %v output=%q", err, output)
	}
	var result linuxIsolationIdentityResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode namespaced identity observation: %v output=%q", err, output)
	}
	return analyzerLowPrivilegeIdentityObservation{
		Mechanism:           "linux_ephemeral_user_namespace.v1",
		ScopeApprovalSHA256: analyzerIsolationScopeDigest(t, approval),
		SeparateIdentityContext: result.UserNamespace != "" &&
			result.UserNamespace != parentNamespace,
		NonAdministratorObserved: result.UID == analyzerNamespaceUID &&
			result.GID == analyzerNamespaceUID,
		AmbientPrivilegesDenied:  result.EffectiveCapsZero,
		NoNewPrivilegesObserved:  result.NoNewPrivileges,
		DedicatedAccountObserved: false,
		TestConformanceOnly:      true,
	}
}

func observeAnalyzerFilesystemIsolation(t *testing.T,
	approval AnalyzerScopeLimitsApproval,
) analyzerFilesystemIsolationObservation {
	t.Helper()
	root := t.TempDir()
	inputPath := filepath.Join(root, "input.bin")
	stagingPath := filepath.Join(root, "staging")
	outsidePath := filepath.Join(root, "outside")
	input := []byte("read-only analyzer input\n")
	resultBytes := []byte("validated analyzer result\n")
	if err := os.WriteFile(inputPath, input, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stagingPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outsidePath, 0o700); err != nil {
		t.Fatal(err)
	}
	command := newLinuxIsolationCommand("linux-filesystem",
		analyzerIsolationInputEnv+"="+inputPath,
		analyzerIsolationStagingEnv+"="+stagingPath,
		analyzerIsolationOutsideEnv+"="+outsidePath)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("start Landlock filesystem helper: %v output=%q", err, output)
	}
	var result linuxIsolationFilesystemResult
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("decode Landlock observation: %v output=%q", err, output)
	}
	stagingFile := filepath.Join(stagingPath, "result.tmp")
	observed, err := os.ReadFile(stagingFile)
	if err != nil || string(observed) != string(resultBytes) {
		t.Fatalf("staged result mismatch: err=%v value=%q", err, observed)
	}
	stagingInfo, err := os.Stat(stagingPath)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "result.final")
	noReplace := analyzerNoReplaceHandoff(t, stagingFile, destination, resultBytes)
	for _, path := range []string{stagingFile, destination, destination + ".conflict"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	residueRemoved := true
	for _, path := range []string{stagingFile, destination, destination + ".conflict"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			residueRemoved = false
		}
	}
	return analyzerFilesystemIsolationObservation{
		Mechanism:                "linux_user_namespace_landlock.v1",
		ScopeApprovalSHA256:      analyzerIsolationScopeDigest(t, approval),
		ReadOnlyInputObserved:    result.InputRead && result.InputWriteDenied,
		OutsideWriteDenied:       result.OutsideWriteDenied,
		PrivateStagingObserved:   stagingInfo.Mode().Perm() == 0o700,
		StagingWriteObserved:     result.StagingWriteAllowed && result.LandlockABI >= 1,
		NoReplaceHandoffObserved: noReplace, ResidueRemoved: residueRemoved,
		CompleteFilesystemSandbox: false, TestConformanceOnly: true,
	}
}

func runAnalyzerIsolationBoundaryHelper(mode string) error {
	switch mode {
	case "linux-handle":
		handleValue, err := strconv.ParseUint(os.Getenv(analyzerIsolationHandleEnv), 10, 64)
		if err != nil || handleValue == 0 {
			return errors.New("invalid inherited analyzer descriptor")
		}
		file := os.NewFile(uintptr(handleValue), "inherited-analyzer-image")
		if file == nil {
			return errors.New("inherited analyzer descriptor is unavailable")
		}
		defer file.Close()
		content, err := io.ReadAll(file)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, analyzerIsolationBytesDigest(content))
		return err
	case "linux-identity":
		result, err := inspectLinuxIsolationIdentity()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(result)
	case "linux-filesystem":
		identity, err := inspectLinuxIsolationIdentity()
		if err != nil {
			return err
		}
		if !identity.NoNewPrivileges || !identity.EffectiveCapsZero {
			return errors.New("filesystem helper did not enter the low-privilege identity")
		}
		inputPath := os.Getenv(analyzerIsolationInputEnv)
		stagingPath := os.Getenv(analyzerIsolationStagingEnv)
		outsidePath := os.Getenv(analyzerIsolationOutsideEnv)
		abi, err := installAnalyzerLandlockFilesystem(inputPath, stagingPath)
		if err != nil {
			return err
		}
		input, readErr := os.ReadFile(inputPath)
		inputWriteErr := os.WriteFile(inputPath, []byte("forbidden"), 0o600)
		outsideErr := os.WriteFile(filepath.Join(outsidePath, "escape"), []byte("forbidden"), 0o600)
		stagingErr := os.WriteFile(filepath.Join(stagingPath, "result.tmp"),
			[]byte("validated analyzer result\n"), 0o600)
		return json.NewEncoder(os.Stdout).Encode(linuxIsolationFilesystemResult{
			LandlockABI:         abi,
			InputRead:           readErr == nil && string(input) == "read-only analyzer input\n",
			InputWriteDenied:    inputWriteErr != nil,
			OutsideWriteDenied:  outsideErr != nil,
			StagingWriteAllowed: stagingErr == nil,
		})
	default:
		return fmt.Errorf("unknown Linux isolation helper mode %q", mode)
	}
}

func newLinuxIsolationCommand(mode string, values ...string) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestAnalyzerIsolationBoundaryHelper$")
	command.Env = append([]string{analyzerIsolationHelperModeEnv + "=" + mode}, values...)
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{
			ContainerID: analyzerNamespaceUID, HostID: os.Getuid(), Size: 1,
		}},
		GidMappings: []syscall.SysProcIDMap{{
			ContainerID: analyzerNamespaceUID, HostID: os.Getgid(), Size: 1,
		}},
		GidMappingsEnableSetgroups: false,
		Credential: &syscall.Credential{Uid: analyzerNamespaceUID, Gid: analyzerNamespaceUID,
			NoSetGroups: true},
		Pdeathsig: syscall.SIGKILL,
	}
	return command
}

func inspectLinuxIsolationIdentity() (linuxIsolationIdentityResult, error) {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return linuxIsolationIdentityResult{}, err
	}
	noNewPrivileges, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil {
		return linuxIsolationIdentityResult{}, err
	}
	namespace, err := os.Readlink("/proc/self/ns/user")
	if err != nil {
		return linuxIsolationIdentityResult{}, err
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return linuxIsolationIdentityResult{}, err
	}
	effectiveCapsZero := false
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "CapEff:") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
			effectiveCapsZero = strings.Trim(value, "0") == ""
			break
		}
	}
	return linuxIsolationIdentityResult{UID: os.Geteuid(), GID: os.Getegid(),
		UserNamespace: namespace, NoNewPrivileges: noNewPrivileges == 1,
		EffectiveCapsZero: effectiveCapsZero}, nil
}

func installAnalyzerLandlockFilesystem(inputPath, stagingPath string) (int, error) {
	abiRaw, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0,
		unix.LANDLOCK_CREATE_RULESET_VERSION, 0, 0, 0)
	if errno != 0 {
		return 0, fmt.Errorf("query Landlock ABI: %w", errno)
	}
	abi := int(abiRaw)
	handled := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		handled |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		handled |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		handled |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	ruleset := unix.LandlockRulesetAttr{Handled_access_fs: handled}
	rulesetRaw, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&ruleset)), unsafe.Sizeof(ruleset), 0, 0, 0, 0)
	if errno != 0 {
		return 0, fmt.Errorf("create Landlock ruleset: %w", errno)
	}
	rulesetFD := int(rulesetRaw)
	defer unix.Close(rulesetFD)
	inputFD, err := unix.Open(inputPath, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer unix.Close(inputFD)
	stagingFD, err := unix.Open(stagingPath, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer unix.Close(stagingFD)
	if err := addAnalyzerLandlockPathRule(rulesetFD, inputFD,
		unix.LANDLOCK_ACCESS_FS_READ_FILE); err != nil {
		return 0, err
	}
	if err := addAnalyzerLandlockPathRule(rulesetFD, stagingFD, handled); err != nil {
		return 0, err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return 0, err
	}
	_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFD), 0, 0)
	if errno != 0 {
		return 0, fmt.Errorf("restrict analyzer with Landlock: %w", errno)
	}
	return abi, nil
}

func addAnalyzerLandlockPathRule(rulesetFD, parentFD int, allowed uint64) error {
	rule := unix.LandlockPathBeneathAttr{Allowed_access: allowed, Parent_fd: int32(parentFD)}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(rulesetFD),
		unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("add Landlock path rule: %w", errno)
	}
	return nil
}
