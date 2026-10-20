//go:build darwin && cgo

package browserruntime

import (
	"context"
	"crypto/sha256"
	"debug/macho"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

const darwinChromeBundle = "/Applications/Google Chrome.app"
const darwinChromeExecutable = darwinChromeBundle + "/Contents/MacOS/Google Chrome"

func AgentBrowserPlatformAvailable() bool { return true }

func inspectDarwinChrome() (digest, cdhash string, err error) {
	if !profilePathHasNoIndirection(darwinChromeExecutable) {
		return "", "", ErrBrowserRuntimeBoundary
	}
	pathInfo, err := os.Lstat(darwinChromeExecutable)
	if err != nil || !pathInfo.Mode().IsRegular() || pathInfo.Size() < MinBrowserExecutableBytes || pathInfo.Size() > MaxBrowserExecutableBytes {
		return "", "", errors.New("fixed Chrome executable is unavailable or unsafe")
	}
	file, err := os.Open(darwinChromeExecutable)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, before) {
		return "", "", ErrBrowserRuntimeBoundary
	}
	expected := macho.CpuArm64
	if runtime.GOARCH == "amd64" {
		expected = macho.CpuAmd64
	} else if runtime.GOARCH != "arm64" {
		return "", "", ErrBrowserRuntimeUnavailable
	}
	verified := false
	if fat, e := macho.NewFatFile(file); e == nil {
		for _, arch := range fat.Arches {
			if arch.Cpu == expected && arch.Type == macho.TypeExec {
				verified = true
			}
		}
	} else if errors.Is(e, macho.ErrNotFat) {
		thin, e := macho.NewFile(file)
		if e == nil {
			verified = thin.Cpu == expected && thin.Type == macho.TypeExec
		}
	}
	if !verified {
		return "", "", errors.New("fixed Chrome is not a native executable Mach-O")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", "", err
	}
	digest = hex.EncodeToString(hash.Sum(nil))
	cdhash, err = darwinChromeCDHash(darwinChromeBundle)
	if err != nil {
		return "", "", err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	second := sha256.New()
	if _, err = io.Copy(second, file); err != nil {
		return "", "", err
	}
	after, err := os.Lstat(darwinChromeExecutable)
	if err != nil || !os.SameFile(before, after) || !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() || hex.EncodeToString(second.Sum(nil)) != digest {
		return "", "", errors.New("Chrome executable changed during native identity validation")
	}
	return digest, cdhash, nil
}

func LaunchAgentBrowser(ctx context.Context, request AgentBrowserStartRequest) (_ *AgentBrowserRuntime, launchErr error) {
	cleanup := AgentBrowserNoProcessCleanup(request.Authority.SessionID)
	defer func() {
		if launchErr != nil {
			cleanup.CleanupPending = !cleanup.ProcessesStopped() || !cleanup.ProfileRemoved
			launchErr = &AgentBrowserLaunchError{Err: launchErr, Cleanup: cleanup}
		}
	}()
	if ctx == nil {
		return nil, ErrBrowserRuntimeBoundary
	}
	if err := validateAgentBrowserRequest(request); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !request.Headless || request.Product != "" && request.Product != BrowserProductChrome {
		return nil, ErrBrowserRuntimeUnavailable
	}
	if err := request.CheckAuthority(ctx, request.Authority); err != nil {
		return nil, err
	}
	digest, cdhash, err := inspectDarwinChrome()
	if err != nil {
		return nil, err
	}
	profile, err := prepareAgentBrowserProfile(request.HomePath, request.Authority)
	if err != nil {
		if profile.path != "" {
			cleanupErr := profile.cleanup()
			cleanup.ProfileRemoved = cleanupErr == nil
			err = errors.Join(err, cleanupErr)
		}
		return nil, err
	}
	cleanup.ProfileRemoved = false
	life, cancel := context.WithDeadline(context.Background(), request.RuntimeDeadline)
	r := &AgentBrowserRuntime{request: request, profile: profile, product: BrowserProductChrome, life: life, cancel: cancel,
		done: make(chan struct{}), operation: make(chan struct{}, 1), refs: map[string]agentBrowserRef{}, state: "starting", epoch: 1}
	r.operation <- struct{}{}
	unreapedChild := false
	defer func() {
		if launchErr != nil {
			if unreapedChild {
				r.cancel()
				cleanup = AgentBrowserCleanup{SessionID: request.Authority.SessionID, ProcessScope: DarwinAgentBrowserProcessScope, CleanupPending: true}
				return
			}
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer closeCancel()
			var closeErr error
			cleanup, closeErr = r.Close(closeCtx)
			launchErr = errors.Join(launchErr, closeErr)
		}
	}()
	for _, name := range []string{"Data", "Home", "Crashpad"} {
		if err := os.Mkdir(filepath.Join(profile.path, name), 0700); err != nil {
			return nil, err
		}
	}
	if err := request.CheckAuthority(ctx, request.Authority); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fingerprint := browserRuntimeFingerprint(struct {
		Authority                    AgentBrowserAuthority
		Digest, CDHash, ProfileToken string
		Deadline                     time.Time
	}{request.Authority, digest, cdhash, profile.token, request.RuntimeDeadline})
	pid, err := darwinChromeSpawnSuspended(darwinChromeExecutable, profile.path, cdhash)
	if err != nil {
		unreapedChild = pid != 0
		return nil, err
	}
	owned, err := newDarwinAgentBrowserProcess(pid, profile.path, fingerprint, request.RuntimeDeadline)
	if err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		_, waitErr := darwinChromeWait(pid)
		unreapedChild = waitErr != nil
		return nil, errors.Join(err, waitErr)
	}
	r.process = owned
	// Recheck live authority after dynamic code validation, before any Chrome code
	// executes or CDP endpoint is read. Rejection kills the still-suspended child.
	if err := request.CheckAuthority(ctx, request.Authority); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := syscall.Kill(pid, syscall.SIGCONT); err != nil {
		return nil, err
	}
	go owned.monitor()
	if err := r.connect(ctx, filepath.Join(profile.path, "Data")); err != nil {
		return nil, err
	}
	handlers, err := darwinChromeCrashpad(profile.path)
	if err != nil || len(handlers) == 0 {
		return nil, fmt.Errorf("native Chrome private Crashpad attribution is unavailable: %w", errors.Join(err, ErrBrowserRuntimeBoundary))
	}
	return r, nil
}
