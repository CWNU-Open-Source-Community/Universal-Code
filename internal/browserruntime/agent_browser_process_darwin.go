//go:build darwin && cgo

package browserruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
)

// These are sampled termination thresholds, not kernel resource isolation.
const darwinBrowserObservedProcesses = 64
const darwinBrowserObservedRSS = uint64(4 * 1024 * 1024 * 1024)

type darwinAgentBrowserProcess struct {
	pid                       int
	profile, startFingerprint string
	root                      darwinChromeProcessSample
	started, deadline         time.Time
	stopMu, observeMu, mu     sync.Mutex
	done                      chan struct{}
	doneOnce                  sync.Once
	known                     map[int]darwinChromeProcessSample
	reaped                    bool // protected by observeMu; do not recycle PGID before final cleanup
	exit                      BrowserProcessExit
	hasExit                   bool
}

func newDarwinAgentBrowserProcess(pid int, profile, fingerprint string, deadline time.Time) (*darwinAgentBrowserProcess, error) {
	root, err := darwinChromeSample(pid)
	if err != nil || root.UID != os.Getuid() || root.ParentPID != os.Getpid() || root.Group != pid || root.Zombie {
		return nil, fmt.Errorf("native Chrome root identity cannot be held: %w", errors.Join(err, ErrBrowserRuntimeBoundary))
	}
	return &darwinAgentBrowserProcess{pid: pid, profile: profile, startFingerprint: fingerprint, root: root,
		started: time.Now().UTC(), deadline: deadline, done: make(chan struct{}), known: map[int]darwinChromeProcessSample{pid: root}}, nil
}

func (p *darwinAgentBrowserProcess) PID() int              { return p.pid }
func (p *darwinAgentBrowserProcess) Done() <-chan struct{} { return p.done }
func (p *darwinAgentBrowserProcess) Exit() (BrowserProcessExit, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exit, p.hasExit
}
func (p *darwinAgentBrowserProcess) Stop(ctx context.Context) error { return p.stop(ctx, true, false) }

func sameDarwinProcess(a, b darwinChromeProcessSample) bool {
	return a.PID == b.PID && a.StartSeconds == b.StartSeconds && a.StartMicroseconds == b.StartMicroseconds
}

// Observation covers the held process group, discovered descendants, and
// Crashpad's exact private database. Darwin cannot follow every escaped fork.
func (p *darwinAgentBrowserProcess) observe() (rootExited bool, live int, rss uint64, err error) {
	p.observeMu.Lock()
	defer p.observeMu.Unlock()
	if p.reaped {
		return true, 0, 0, nil
	}
	rootExited, err = darwinChromeExited(p.pid)
	if err != nil {
		return
	}
	members, e := darwinChromeGroup(p.pid)
	if e != nil {
		err = e
		return
	}
	handlers, e := darwinChromeCrashpad(p.profile)
	if e != nil {
		err = e
		return
	}
	candidates := append(members, handlers...)
	// Recursively enumerate while parent identity is still present. A previously
	// observed PID is never re-admitted after its birth time changes.
	parents := make([]darwinChromeProcessSample, 0, len(p.known))
	for _, known := range p.known {
		parents = append(parents, known)
	}
	for _, parent := range parents {
		actual, e := darwinChromeSample(parent.PID)
		if errors.Is(e, syscall.ESRCH) || e == nil && (!sameDarwinProcess(parent, actual) || actual.Zombie) {
			continue
		}
		if e != nil {
			err = e
			return
		}
		if actual.UID != parent.UID {
			err = errors.New("observed browser parent changed user identity")
			return
		}
		children, e := darwinChromeChildren(parent.PID)
		if e != nil {
			err = e
			return
		}
		candidates = append(candidates, children...)
	}
	for _, pid := range candidates {
		if _, exists := p.known[pid]; exists {
			continue
		}
		sample, e := darwinChromeSample(pid)
		if errors.Is(e, syscall.ESRCH) {
			continue
		}
		if e != nil {
			err = e
			return
		}
		if sample.UID != p.root.UID || sample.StartSeconds < p.root.StartSeconds || sample.StartSeconds == p.root.StartSeconds && sample.StartMicroseconds < p.root.StartMicroseconds {
			err = errors.New("native browser observation crossed its user or launch identity")
			return
		}
		p.known[pid] = sample
	}
	for pid, born := range p.known {
		sample, e := darwinChromeSample(pid)
		if errors.Is(e, syscall.ESRCH) || e == nil && (!sameDarwinProcess(born, sample) || sample.Zombie) {
			delete(p.known, pid)
			continue
		}
		if e != nil {
			err = e
			return
		}
		if sample.UID != born.UID {
			err = errors.New("observed browser process changed user identity")
			return
		}
		live++
		if ^uint64(0)-rss < sample.RSS {
			err = ErrBrowserRuntimeBoundary
			return
		}
		rss += sample.RSS
	}
	return
}

func (p *darwinAgentBrowserProcess) monitor() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
		}
		exited, count, rss, err := p.observe()
		timedOut := !time.Now().Before(p.deadline)
		if err != nil || exited || timedOut || count > darwinBrowserObservedProcesses || rss > darwinBrowserObservedRSS {
			// Invalidate actions immediately, independently of the bounded cleanup.
			p.doneOnce.Do(func() { close(p.done) })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = p.stop(ctx, !exited, timedOut)
			cancel()
			return
		}
	}
}

func (p *darwinAgentBrowserProcess) stop(ctx context.Context, cancelled, timedOut bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.doneOnce.Do(func() { close(p.done) })
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	if exit, ok := p.Exit(); ok && exit.ProcessScopeVerified {
		return nil
	}
	// Keep the direct child unreaped until group signaling is over, so its numeric
	// PGID cannot become an unrelated newly launched process group.
	if err := syscall.Kill(-p.pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
		return err
	}
	started := time.Now()
	deadline := started.Add(8 * time.Second)
	if bound, ok := ctx.Deadline(); ok && bound.Before(deadline) {
		deadline = bound
	}
	escalated := false
	var cleanupErr error
	code := -1
	verified := false
	for {
		exited, live, _, err := p.observe()
		if err != nil {
			_ = syscall.Kill(-p.pid, syscall.SIGKILL)
			cleanupErr = err
			break
		}
		if exited && live == 0 {
			p.observeMu.Lock()
			code, err = darwinChromeWait(p.pid)
			if err == nil {
				p.reaped = true
				verified = true
			}
			p.observeMu.Unlock()
			cleanupErr = err
			break
		}
		if !escalated && time.Since(started) >= 500*time.Millisecond {
			if err = syscall.Kill(-p.pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
				cleanupErr = err
				break
			}
			escalated = true
		}
		if !time.Now().Before(deadline) {
			cleanupErr = errors.New("native browser group or observed child cleanup remains pending")
			break
		}
		select {
		case <-ctx.Done():
			cleanupErr = ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		if cleanupErr != nil {
			break
		}
	}
	exit := BrowserProcessExit{ProtocolVersion: "darwin_agent_browser_exit.v1", Adapter: "darwin_chrome_supervision.v1",
		StartSpecFingerprint: p.startFingerprint, ExitCode: code, ProcessScope: DarwinAgentBrowserProcessScope,
		ProcessScopeVerified: verified, Cancelled: cancelled, TimedOut: timedOut, StartedAt: p.started, CompletedAt: time.Now().UTC()}
	exit.Fingerprint = browserRuntimeFingerprint(exit)
	p.mu.Lock()
	p.exit = exit
	p.hasExit = true
	p.mu.Unlock()
	return cleanupErr
}
