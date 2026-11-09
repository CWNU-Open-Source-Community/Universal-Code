package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type commandRuntimeOutputFaultStarter struct {
	commandRuntimeStarter
	stream CommandRuntimeStream
	wrap   func(io.ReadCloser) io.ReadCloser
}

func (s commandRuntimeOutputFaultStarter) Start(ctx context.Context, scope CommandRuntimeScope, spec CommandRuntimeResolvedSpec) (commandRuntimeProcess, error) {
	process, err := s.commandRuntimeStarter.Start(ctx, scope, spec)
	if err != nil {
		return nil, err
	}
	reader := process.Stdout()
	if s.stream == CommandRuntimeStderr {
		reader = process.Stderr()
	}
	return commandRuntimeOutputFaultProcess{process, s.stream, s.wrap(reader)}, nil
}

type commandRuntimeOutputFaultProcess struct {
	commandRuntimeProcess
	stream CommandRuntimeStream
	reader io.ReadCloser
}

func (p commandRuntimeOutputFaultProcess) Stdout() io.ReadCloser {
	if p.stream == CommandRuntimeStdout {
		return p.reader
	}
	return p.commandRuntimeProcess.Stdout()
}

func (p commandRuntimeOutputFaultProcess) Stderr() io.ReadCloser {
	if p.stream == CommandRuntimeStderr {
		return p.reader
	}
	return p.commandRuntimeProcess.Stderr()
}

func TestCommandRuntimeNativeOutputReadFailureStopsUnobservedProcess(t *testing.T) {
	root, manager, state, request := newCommandRuntimeOutputFaultNativeFixture(t,
		`const fs=require('fs');setInterval(()=>fs.appendFileSync('ticks','1'),10);`, newPlatformCommandRuntimeStarter())
	job, _, err := manager.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, err := os.ReadFile(filepath.Join(root, "ticks")); err == nil && len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native Node process did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Close the actual parent pipe while its collector is blocked. Node's
	// execution and the process-tree cleanup remain real native operations.
	if err := manager.entry(job.ID).process.Stdout().Close(); err != nil {
		t.Fatal(err)
	}
	terminal, _ := waitCommandRuntimeTerminal(t, manager, job.ID)
	stored, err := state.GetCommandRuntimeJob(t.Context(), job.ID)
	if err != nil || terminal.State != CommandRuntimeJobInterrupted || !stored.TreeReaped || !strings.Contains(stored.Stderr, "stdout collection failed") {
		t.Fatalf("unobserved native process was not interrupted and reaped: %+v %v", stored, err)
	}
	before, err := os.ReadFile(filepath.Join(root, "ticks"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	after, err := os.ReadFile(filepath.Join(root, "ticks"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("unobserved native process kept executing: %q -> %q %v", before, after, err)
	}
}

type commandRuntimeGatedOutputReader struct {
	io.ReadCloser
	remaining int
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (r *commandRuntimeGatedOutputReader) Read(data []byte) (int, error) {
	if r.remaining > 0 {
		count, err := r.ReadCloser.Read(data[:min(len(data), r.remaining)])
		r.remaining -= count
		return count, err
	}
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return r.ReadCloser.Read(data)
}

func TestCommandRuntimeNativeOutputReadFailurePreservesConfirmedExit(t *testing.T) {
	const prefix = "received-prefix\n"
	gate := &commandRuntimeGatedOutputReader{remaining: len(prefix), entered: make(chan struct{}), release: make(chan struct{})}
	starter := commandRuntimeOutputFaultStarter{newPlatformCommandRuntimeStarter(), CommandRuntimeStdout,
		func(reader io.ReadCloser) io.ReadCloser { gate.ReadCloser = reader; return gate }}
	_, manager, state, request := newCommandRuntimeOutputFaultNativeFixture(t,
		`require('fs').writeSync(1,'received-prefix\nuncaptured-output\n');`, starter)
	release := sync.OnceFunc(func() { close(gate.release) })
	t.Cleanup(release)
	job, _, err := manager.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	entry := manager.entry(job.ID)
	for _, done := range []<-chan struct{}{gate.entered, entry.processDone} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("native process did not exit while output reading was gated")
		}
	}
	// Node exited with zero after writing both lines. Closing its actual parent
	// pipe discards the unread second line and makes Read fail instead of EOF.
	if err := gate.Close(); err != nil {
		t.Fatal(err)
	}
	release()
	terminal, _ := waitCommandRuntimeTerminal(t, manager, job.ID)
	stored, err := state.GetCommandRuntimeJob(t.Context(), job.ID)
	if err != nil || terminal.State != CommandRuntimeJobInterrupted || stored.ExitCode == nil || *stored.ExitCode != 0 || !stored.TreeReaped ||
		stored.Stdout != prefix || stored.StdoutSHA256 != commandRuntimeStringSHA256(prefix) || stored.StdoutObservedBytes != int64(len(prefix)) ||
		!strings.Contains(stored.Stderr, "stdout collection failed") || stored.TruncationReason != "" {
		t.Fatalf("confirmed exit or partial capture was misrepresented: %+v %v", stored, err)
	}
	replay, replayed, err := manager.Start(t.Context(), request)
	if err != nil || !replayed || replay.ID != job.ID || replay.State != CommandRuntimeJobInterrupted || replay.PID != stored.PID {
		t.Fatalf("interrupted capture was not replayed as the same Job: %+v %v", replay, err)
	}
}

func newCommandRuntimeOutputFaultNativeFixture(t *testing.T, script string, starter commandRuntimeStarter) (string, *CommandRuntimeManager, *commandRuntimeMemoryStore, CommandRuntimeStartRequest) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable", err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := NormalizeCommandRuntimeSpec(CommandRuntimeSpec{
		Version: CommandRuntimeProtocolVersion, Profile: CommandRuntimeProcess,
		Executable: node, Arguments: []string{"-e", script},
		WorkingDirectory: ".", Environment: []CommandRuntimeEnvironment{}, StdinPolicy: CommandRuntimeStdinClosed,
		CloseInitialStdin: true, TimeoutMilliseconds: 5000,
		Output:  CommandRuntimeOutputPolicy{InlineBytes: MinCommandRuntimeInlineBytes, ArtifactBytes: MinCommandRuntimeInlineBytes},
		Network: CommandRuntimeNetworkDisabled, Credentials: CommandRuntimeCredentialsNone,
		Purpose: "native output read failure regression",
	}, root)
	if err != nil {
		t.Fatal(err)
	}
	state := newCommandRuntimeMemoryStore()
	manager, err := NewCommandRuntimeManager(state, starter, "native-output-failure-owner")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	request := commandRuntimeTestRequest(manager, 5000)
	request.Spec = resolved
	request.Scope.WorkspaceRootSHA256 = resolved.WorkspaceRootSHA256
	return root, manager, state, request
}

type commandRuntimePartialErrorReader struct {
	release <-chan struct{}
	err     error
}

func (r commandRuntimePartialErrorReader) Read(data []byte) (int, error) {
	<-r.release
	return copy(data, "received-output\n"), r.err
}

func TestCommandRuntimeOutputReadFailurePreservesStopAndCapture(t *testing.T) {
	for _, stream := range []CommandRuntimeStream{CommandRuntimeStdout, CommandRuntimeStderr} {
		for _, discard := range []bool{false, true} {
			for _, reason := range []CommandRuntimeJobState{"", CommandRuntimeJobCancelled, CommandRuntimeJobTimedOut, CommandRuntimeJobFailed, CommandRuntimeJobCompleted} {
				name := string(stream) + "/" + string(reason)
				if discard {
					name += "/digest_only"
				}
				t.Run(name, func(t *testing.T) {
					base := &commandRuntimeFakeStarter{}
					readErr := errors.New("injected read failure token=secret-value-1234567890")
					if reason == CommandRuntimeJobCompleted {
						readErr = io.EOF
					}
					gate := make(chan struct{})
					release := sync.OnceFunc(func() { close(gate) })
					defer release()
					starter := commandRuntimeOutputFaultStarter{base, stream,
						func(io.ReadCloser) io.ReadCloser {
							return io.NopCloser(commandRuntimePartialErrorReader{gate, readErr})
						}}
					state := newCommandRuntimeMemoryStore()
					var jobStore CommandRuntimeStore = state
					handoffFailure := errors.New("injected handoff failure")
					if reason == CommandRuntimeJobFailed {
						jobStore = &commandRuntimeFailedHandoffStore{state, base, make(chan struct{}), handoffFailure, 1}
					}
					manager, err := NewCommandRuntimeManager(jobStore, starter, "output-failure-fixture")
					if err != nil {
						t.Fatal(err)
					}
					request := commandRuntimeTestRequest(manager, 60_000)
					if reason == CommandRuntimeJobTimedOut {
						request = commandRuntimeTestRequest(manager, 25)
					}
					request.DiscardOutput = discard
					job, _, err := manager.Start(t.Context(), request)
					if err != nil && (reason != CommandRuntimeJobFailed || !errors.Is(err, handoffFailure)) {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						release()
						base.last().finish(125)
						ctx, cancel := context.WithTimeout(context.Background(), time.Second)
						defer cancel()
						if err := manager.Shutdown(ctx); err != nil {
							t.Error(err)
						}
					})
					want, exit := reason, 0
					switch reason {
					case "":
						want, exit = CommandRuntimeJobInterrupted, 125
						process := base.last()
						process.mu.Lock()
						process.killFailures = 2
						process.mu.Unlock()
					case CommandRuntimeJobCancelled:
						exit = 125
						if _, err := manager.Stop(t.Context(), job.ID, false, 0); err != nil {
							t.Fatal(err)
						}
					case CommandRuntimeJobTimedOut:
						exit = 125
					case CommandRuntimeJobFailed:
						exit = 125
						base.last().finish(exit)
					default:
						base.last().finish(0)
					}
					if reason != "" {
						select {
						case <-manager.entry(job.ID).processDone:
						case <-time.After(time.Second):
							t.Fatal("fake process did not finish before collector release")
						}
					}
					release()
					terminal, _ := waitCommandRuntimeTerminal(t, manager, job.ID)
					stored, err := state.GetCommandRuntimeJob(t.Context(), job.ID)
					if err != nil || terminal.State != want || stored.ExitCode == nil || *stored.ExitCode != exit || !stored.TreeReaped || stored.TruncationReason != "" {
						t.Fatalf("stop reason or exit was lost: %+v %v", stored, err)
					}
					if discard {
						if stored.Stdout != "" || stored.Stderr != "" || stored.OutputFramesJSON != "[]" {
							t.Fatal("digest-only collection retained output or diagnostics")
						}
					} else if reason != CommandRuntimeJobCompleted && (!strings.Contains(stored.Stderr, string(stream)+" collection failed") || strings.Contains(stored.Stderr, "secret-value")) {
						t.Fatalf("collector diagnostic missing or unredacted: %q", stored.Stderr)
					}
					if !discard {
						captured := stored.Stdout
						if stream == CommandRuntimeStderr {
							captured = stored.Stderr
						}
						if !strings.HasPrefix(captured, "received-output\n") || stored.StdoutSHA256 != commandRuntimeStringSHA256(stored.Stdout) || stored.StderrSHA256 != commandRuntimeStringSHA256(stored.Stderr) {
							t.Fatalf("received output or captured digests changed: %+v", stored)
						}
					}
					if stream == CommandRuntimeStdout || discard {
						digest, observed := stored.StdoutSHA256, stored.StdoutObservedBytes
						if stream == CommandRuntimeStderr {
							digest, observed = stored.StderrSHA256, stored.StderrObservedBytes
						}
						if digest != commandRuntimeStringSHA256("received-output\n") || observed != int64(len("received-output\n")) {
							t.Fatalf("partial bytes or their digest were lost: %+v", stored)
						}
					}
					if replay, replayed, err := manager.Start(t.Context(), request); err != nil || !replayed || replay.State != want || base.starts != 1 {
						t.Fatalf("terminal capture restarted or lost its state: %+v %v", replay, err)
					}
				})
			}
		}
	}
}
