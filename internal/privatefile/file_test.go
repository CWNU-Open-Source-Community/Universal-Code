package privatefile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The credential stores depend on these two helpers to keep secret files
// private and to serialize cooperating processes. They carry no durable state
// of their own, so every case below runs against a temporary directory.

func TestWriteReplacesContentWithPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.json")

	if err := Write(path, []byte(`{"token":"a"}`), nil); err != nil {
		t.Fatalf("write private file: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read private file: %v", err)
	}
	if string(content) != `{"token":"a"}` {
		t.Fatalf("unexpected content %q", content)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat private file: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0600 {
			t.Fatalf("private file mode = %v, want 0600", mode)
		}
	}
	assertNoLeftoverTempFiles(t, dir, "secret.json")
}

func TestWriteOverwritesAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(path, []byte("stale"), 0600); err != nil {
		t.Fatalf("seed private file: %v", err)
	}

	if err := Write(path, []byte("fresh"), nil); err != nil {
		t.Fatalf("overwrite private file: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read private file: %v", err)
	}
	if string(content) != "fresh" {
		t.Fatalf("unexpected content %q", content)
	}
	assertNoLeftoverTempFiles(t, dir, "secret.json")
}

func TestWriteReportsAnInterruptedCommitAndKeepsTheOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatalf("seed private file: %v", err)
	}

	interrupted := errors.New("commit interrupted")
	err := Write(path, []byte("replacement"), func(string, string) error { return interrupted })
	if !errors.Is(err, interrupted) {
		t.Fatalf("write error = %v, want the injected commit failure", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read private file: %v", err)
	}
	if string(content) != "original" {
		t.Fatalf("interrupted commit changed the file to %q", content)
	}
	assertNoLeftoverTempFiles(t, dir, "secret.json")
}

func TestWriteFailsWhenTheParentDirectoryIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "secret.json")

	if err := Write(path, []byte("data"), nil); err == nil {
		t.Fatal("write into a missing directory succeeded")
	}
}

func TestWithLockFailsWhenTheParentDirectoryIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "store.lock")

	ran := false
	if err := WithLock(context.Background(), path, func() error { ran = true; return nil }); err == nil {
		t.Fatal("lock in a missing directory succeeded")
	}
	if ran {
		t.Fatal("locked callback ran for a missing lock directory")
	}
}

func TestWithLockRunsTheCallbackAndReleasesTheLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.lock")

	calls := 0
	for range 2 {
		if err := WithLock(context.Background(), path, func() error {
			calls++
			return nil
		}); err != nil {
			t.Fatalf("locked callback: %v", err)
		}
	}
	if calls != 2 {
		t.Fatalf("callback ran %d times, want 2", calls)
	}
}

func TestWithLockPropagatesTheCallbackFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")
	failure := errors.New("callback failed")

	err := WithLock(context.Background(), path, func() error { return failure })
	if !errors.Is(err, failure) {
		t.Fatalf("locked callback error = %v, want the callback failure", err)
	}
}

func TestWithLockRejectsAnAlreadyCancelledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ran := false
	err := WithLock(ctx, path, func() error { ran = true; return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("locked callback error = %v, want context.Canceled", err)
	}
	if ran {
		t.Fatal("locked callback ran for a cancelled context")
	}
}

func TestWithLockCancelsWhileWaitingForAnotherHolder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.lock")

	held := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- WithLock(context.Background(), path, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	ran := false
	err := WithLock(ctx, path, func() error { ran = true; return nil })
	close(release)
	if holderErr := <-holderDone; holderErr != nil {
		t.Fatalf("holder callback: %v", holderErr)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting lock error = %v, want context.DeadlineExceeded", err)
	}
	if ran {
		t.Fatal("waiting callback ran while another holder owned the lock")
	}
}

func TestWithLockSerializesConcurrentCallers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.lock")

	var mu sync.Mutex
	inside := 0
	maxInside := 0
	waited := sync.WaitGroup{}
	for range 4 {
		waited.Add(1)
		go func() {
			defer waited.Done()
			if err := WithLock(context.Background(), path, func() error {
				mu.Lock()
				inside++
				if inside > maxInside {
					maxInside = inside
				}
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
				return nil
			}); err != nil {
				t.Errorf("locked callback: %v", err)
			}
		}()
	}
	waited.Wait()
	if maxInside != 1 {
		t.Fatalf("maximum concurrent holders = %d, want 1", maxInside)
	}
}

func assertNoLeftoverTempFiles(t *testing.T, dir string, want string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read directory: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() == want {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".write-") {
			t.Fatalf("temporary file %q was left behind", entry.Name())
		}
		t.Fatalf("unexpected file %q in the private directory", entry.Name())
	}
}
