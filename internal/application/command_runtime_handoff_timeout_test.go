package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cyberagent-workbench/internal/domain"
	"cyberagent-workbench/internal/runner"
	"cyberagent-workbench/internal/store"
)

// Model a slow database ownership handoff; the process and SQLite writes remain real.
type commandRuntimeSlowHandoffStore struct {
	*store.SQLiteStore
	entered     chan struct{}
	release     chan struct{}
	releaseOnce *sync.Once
	err         error
}

func (s commandRuntimeSlowHandoffStore) unblock() { s.releaseOnce.Do(func() { close(s.release) }) }

func (s commandRuntimeSlowHandoffStore) UpdateCommandRuntimeJob(ctx context.Context, job runner.CommandRuntimeJob, previous int64) (runner.CommandRuntimeJob, error) {
	if previous == 1 && job.State == runner.CommandRuntimeJobRunning {
		close(s.entered)
		select {
		case <-s.release:
			if s.err != nil {
				return runner.CommandRuntimeJob{}, s.err
			}
		case <-ctx.Done():
			return runner.CommandRuntimeJob{}, ctx.Err()
		}
	}
	return s.SQLiteStore.UpdateCommandRuntimeJob(ctx, job, previous)
}

func newCommandRuntimeHandoffFixture(t *testing.T, failure error) (*operatorCommandFixture, commandRuntimeSlowHandoffStore) {
	t.Helper()
	f := newOperatorCommandFixture(t, domain.RunExecutionPermissionFull)
	if err := f.manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	var err error
	gate := commandRuntimeSlowHandoffStore{SQLiteStore: f.st, entered: make(chan struct{}), release: make(chan struct{}), releaseOnce: &sync.Once{}, err: failure}
	t.Cleanup(gate.unblock)
	f.manager, err = runner.NewPlatformCommandRuntimeManager(gate, "slow-handoff-owner")
	if err != nil {
		t.Fatal(err)
	}
	f.service, err = NewCommandRuntimeService(f.st, f.manager, f.caps)
	if err != nil {
		t.Fatal(err)
	}
	return f, gate
}

func TestCommandRuntimeTimeoutIncludesDurableHandoff(t *testing.T) {
	f, gate := newCommandRuntimeHandoffFixture(t, nil)
	request := f.request(t, true)
	request.Command.TimeoutMilliseconds = 500
	request.Command.Arguments = []string{"-e", `const fs=require('fs');fs.writeFileSync('ready','ready');setInterval(()=>{if(fs.existsSync('proceed')){fs.writeFileSync('after-timeout.txt','executed');process.exit(0);}},10);`}
	type outcome struct {
		result OperatorCommandResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.service.RunOperatorCommand(t.Context(), request)
		done <- outcome{result, err}
	}()
	<-gate.entered
	waitCommandRuntimeHandoffFile(t, filepath.Join(f.root, "ready"))
	// The handoff is held, and the real native deadline has elapsed before
	// allowing the child to attempt any observable side effect.
	time.Sleep(time.Duration(request.Command.TimeoutMilliseconds)*time.Millisecond + 200*time.Millisecond)
	if err := os.WriteFile(filepath.Join(f.root, "proceed"), []byte("proceed"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	gate.unblock()
	value := <-done
	result, err := value.result, value.err
	if err != nil {
		t.Fatal(err)
	}
	if result.Job.State != runner.CommandRuntimeJobTimedOut || !result.Job.TreeReaped {
		t.Fatalf("timeout receipt: %+v", result.Job)
	}
	if _, err := os.Stat(filepath.Join(f.root, "after-timeout.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native process kept running beyond its timeout while SQLite handoff was pending: %v", err)
	}
}

func TestCommandRuntimeNaturalExitDuringDurableHandoff(t *testing.T) {
	f, gate := newCommandRuntimeHandoffFixture(t, nil)
	request := f.request(t, true)
	request.Command.TimeoutMilliseconds = 500
	request.Command.Arguments = []string{"-e", `process.stdout.write('fast-handoff-output');require('fs').writeFileSync('ready','ready');`}
	type outcome struct {
		result OperatorCommandResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.service.RunOperatorCommand(t.Context(), request)
		done <- outcome{result, err}
	}()
	<-gate.entered
	waitCommandRuntimeHandoffFile(t, filepath.Join(f.root, "ready"))
	time.Sleep(time.Duration(request.Command.TimeoutMilliseconds)*time.Millisecond + 200*time.Millisecond)
	gate.unblock()
	value := <-done
	job, err := f.st.GetCommandRuntimeJob(t.Context(), value.result.Job.ID)
	if value.err != nil || err != nil || job.State != runner.CommandRuntimeJobCompleted || job.ExitCode == nil || *job.ExitCode != 0 || !job.TreeReaped || job.Stdout != "fast-handoff-output" || job.StdoutObservedBytes != int64(len(job.Stdout)) {
		t.Fatalf("fast native exit lost its success or output during SQL handoff: %+v %v/%v", job, value.err, err)
	}
}

func TestCommandRuntimeFailedDurableHandoffReapsNativeProcess(t *testing.T) {
	failure := errors.New("injected handoff persistence failure")
	f, gate := newCommandRuntimeHandoffFixture(t, failure)
	request := f.request(t, true)
	request.Command.Arguments = []string{"-e", `const fs=require('fs');fs.writeFileSync('ready','ready');setInterval(()=>fs.appendFileSync('ticks','1'),10);`}
	done := make(chan error, 1)
	go func() { _, err := f.service.RunOperatorCommand(t.Context(), request); done <- err }()
	<-gate.entered
	waitCommandRuntimeHandoffFile(t, filepath.Join(f.root, "ticks"))
	gate.unblock()
	if err := <-done; !errors.Is(err, failure) {
		t.Fatalf("handoff failure lost: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(f.root, "ticks"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	after, err := os.ReadFile(filepath.Join(f.root, "ticks"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("failed handoff left native process writing: %q -> %q %v", before, after, err)
	}
	jobs, err := f.st.ListCommandRuntimeJobs(t.Context(), runner.CommandRuntimeListFilter{RunID: f.run.ID})
	if err != nil || len(jobs) != 1 || jobs[0].State != runner.CommandRuntimeJobPrepared {
		t.Fatalf("unknown handoff receipt changed: %+v %v", jobs, err)
	}
	if f.manager.OwnsActiveJob(jobs[0]) {
		t.Fatal("failed handoff registered a live Job")
	}
}

func waitCommandRuntimeHandoffFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) != "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native handoff fixture did not become ready", path)
}
