package main

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	protocol                      = "standard-code-docker-runner.v2"
	workspaceRoot                 = "/workspace"
	cacheRoot                     = "/traverse-board/cache"
	bindingFieldCount             = 24
	workspaceGrowthBytes    int64 = 16 * 1024 * 1024
	workspaceGrowthEntries  int64 = 4_096
	workspaceFileBytes      int64 = 16 * 1024 * 1024
	workspaceFreeBytes      int64 = 2 * 1024 * 1024 * 1024
	workspaceFreeEntries    int64 = 1_000_000
	workspaceInitialEntries       = 250_000
	runnerFailureExitCode         = 125
)

func main() {
	exitCode, err := run(os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standard-code-runner:", err)
	}
	if exitCode < 0 || exitCode > 255 {
		exitCode = runnerFailureExitCode
	}
	os.Exit(exitCode)
}

func run(arguments []string) (int, error) {
	if len(arguments) < bindingFieldCount+1 {
		return runnerFailureExitCode, errors.New("complete Go-owned binding is required")
	}
	fields := arguments[1 : bindingFieldCount+1]
	if fields[0] != protocol {
		return runnerFailureExitCode, errors.New("runner protocol mismatch")
	}
	for _, index := range []int{1, 2, 3, 4, 5, 6, 8, 10, 12} {
		if !validIdentity(fields[index]) {
			return runnerFailureExitCode, errors.New("runner identity is invalid")
		}
	}
	for _, index := range []int{7, 11, 13} {
		if value, err := strconv.ParseInt(fields[index], 10, 64); err != nil || value < 1 {
			return runnerFailureExitCode, errors.New("runner revision is invalid")
		}
	}
	for _, index := range []int{9, 14, 15} {
		if !validDigest(fields[index]) {
			return runnerFailureExitCode, errors.New("runner digest is invalid")
		}
	}
	limits, err := parseExecutionLimits(fields[19:24])
	if err != nil {
		return runnerFailureExitCode, err
	}
	stdinPipe := false
	switch fields[16] {
	case "closed":
	case "pipe":
		stdinPipe = true
	default:
		return runnerFailureExitCode, errors.New("runner stdin policy is invalid")
	}
	workingDirectory, err := resolveWorkingDirectory(fields[18])
	if err != nil {
		return runnerFailureExitCode, err
	}
	executable, environment, err := fixedToolchain(fields[17])
	if err != nil {
		return runnerFailureExitCode, err
	}
	toolArguments := append([]string{executable}, arguments[bindingFieldCount+1:]...)
	for _, argument := range toolArguments {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, 0) {
			return runnerFailureExitCode, errors.New("tool argument is invalid")
		}
	}
	return executeTool(executable, toolArguments, environment, workingDirectory, limits,
		stdinPipe)
}

type executionLimits struct {
	GrowthBytes   int64
	GrowthEntries int64
	FileBytes     int64
	FreeBytes     int64
	FreeEntries   int64
}

func parseExecutionLimits(values []string) (executionLimits, error) {
	if len(values) != 5 {
		return executionLimits{}, errors.New("complete workspace resource limits are required")
	}
	parsed := make([]int64, len(values))
	for index, value := range values {
		current, err := strconv.ParseInt(value, 10, 64)
		if err != nil || current < 1 {
			return executionLimits{}, errors.New("workspace resource limit is invalid")
		}
		parsed[index] = current
	}
	limits := executionLimits{GrowthBytes: parsed[0], GrowthEntries: parsed[1],
		FileBytes: parsed[2], FreeBytes: parsed[3], FreeEntries: parsed[4]}
	if limits != (executionLimits{GrowthBytes: workspaceGrowthBytes,
		GrowthEntries: workspaceGrowthEntries, FileBytes: workspaceFileBytes,
		FreeBytes: workspaceFreeBytes, FreeEntries: workspaceFreeEntries}) {
		return executionLimits{}, errors.New("workspace resource limit does not match the fixed profile")
	}
	return limits, nil
}

type workspaceUsage struct {
	Bytes   int64
	Entries int64
}

func captureWorkspaceUsage(root string, maximumEntries int64) (workspaceUsage, error) {
	usage := workspaceUsage{}
	err := filepath.WalkDir(root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == root {
			return nil
		}
		usage.Entries++
		if usage.Entries > maximumEntries {
			return errors.New("workspace entry count exceeded its accounting bound")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		size := info.Size()
		if size < 0 || size > math.MaxInt64-usage.Bytes {
			return errors.New("workspace byte accounting overflowed")
		}
		if info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			usage.Bytes += size
		}
		return nil
	})
	return usage, err
}

func workspaceGrowthExceeded(baseline, current workspaceUsage,
	limits executionLimits,
) bool {
	return current.Bytes < 0 || current.Entries < 0 ||
		baseline.Bytes > math.MaxInt64-limits.GrowthBytes ||
		baseline.Entries > math.MaxInt64-limits.GrowthEntries ||
		current.Bytes > baseline.Bytes+limits.GrowthBytes ||
		current.Entries > baseline.Entries+limits.GrowthEntries
}

func fixedToolchain(toolchain string) (string, []string, error) {
	return fixedToolchainAt(toolchain, cacheRoot)
}

func fixedToolchainAt(toolchain, scratchRoot string) (string, []string, error) {
	tempRoot := filepath.Join(scratchRoot, "tmp")
	if err := os.MkdirAll(tempRoot, 0o700); err != nil {
		return "", nil, err
	}
	basePath := "/usr/bin:/bin"
	baseEnvironment := []string{"TMPDIR=" + tempRoot}
	switch toolchain {
	case "gofmt":
		return "/opt/toolchains/go/bin/gofmt", append(baseEnvironment, "PATH=/opt/toolchains/go/bin:"+basePath), nil
	case "go":
		cache := filepath.Join(scratchRoot, "go")
		moduleCache := filepath.Join(scratchRoot, "go-mod")
		goPath := filepath.Join(scratchRoot, "go-path")
		goTemp := filepath.Join(tempRoot, "go")
		for _, directory := range []string{cache, moduleCache, goPath, goTemp} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return "", nil, err
			}
		}
		return "/opt/toolchains/go/bin/go", append(baseEnvironment,
			"PATH=/opt/toolchains/go/bin:"+basePath,
			"GOROOT=/opt/toolchains/go", "GOCACHE="+cache,
			"GOMODCACHE="+moduleCache, "GOPATH="+goPath,
			"GOTMPDIR="+goTemp, "GOENV=off", "GOTOOLCHAIN=local",
			"GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=0",
		), nil
	case "node":
		return "/opt/toolchains/node/bin/node", append(baseEnvironment,
			"PATH=/opt/toolchains/node/bin:"+basePath, "npm_config_audit=false", "npm_config_fund=false"), nil
	case "python":
		return "/opt/toolchains/python/usr-local/bin/python3", append(baseEnvironment,
			"PATH=/opt/toolchains/python/usr-local/bin:"+basePath,
			"PYTHONHOME=/opt/toolchains/python/usr-local",
			"PYTHONDONTWRITEBYTECODE=1", "PYTHONNOUSERSITE=1"), nil
	case "rust":
		target := filepath.Join(scratchRoot, "rust-target")
		cargoHome := filepath.Join(scratchRoot, "cargo-home")
		if err := os.MkdirAll(target, 0o700); err != nil {
			return "", nil, err
		}
		if err := os.MkdirAll(cargoHome, 0o700); err != nil {
			return "", nil, err
		}
		return "/usr/local/cargo/bin/cargo", append(baseEnvironment,
			"PATH=/usr/local/cargo/bin:"+basePath,
			"CARGO_HOME="+cargoHome,
			"RUSTUP_HOME=/usr/local/rustup",
			"CARGO_TARGET_DIR="+target, "CARGO_NET_OFFLINE=true",
			"RUSTUP_NO_UPDATE_CHECK=1"), nil
	default:
		return "", nil, errors.New("unsupported fixed toolchain")
	}
}

func resolveWorkingDirectory(relative string) (string, error) {
	if relative == "" || relative != strings.TrimSpace(relative) ||
		strings.Contains(relative, `\`) || filepath.IsAbs(relative) ||
		filepath.Clean(relative) != relative || relative == ".." ||
		strings.HasPrefix(relative, "../") {
		return "", errors.New("working directory is not a normalized Workspace-relative path")
	}
	target := filepath.Join(workspaceRoot, relative)
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	relativeToRoot, err := filepath.Rel(workspaceRoot, resolved)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, "../") {
		return "", errors.New("working directory escaped the Workspace")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("working directory is unavailable")
	}
	return resolved, nil
}

func validIdentity(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) ||
		strings.ContainsRune(value, 0) || value != strings.TrimSpace(value) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return false
		}
	}
	return true
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, current := range value {
		if !strings.ContainsRune("0123456789abcdef", current) {
			return false
		}
	}
	return true
}
