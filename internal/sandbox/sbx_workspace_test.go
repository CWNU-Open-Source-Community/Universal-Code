package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func sbxAssertWorkspaceRejectionBeforeDispatch(t *testing.T, b *SBXBackend, fake *sbxFakeTransport, r SBXRunRequest) {
	t.Helper()
	result, err := b.Run(context.Background(), r, nil)
	if !errors.Is(err, ErrSBXBoundary) || result.ExitCode != 125 || !result.TreeReaped || result.ReceiptFingerprint != "" ||
		fake.count("create") != 0 || fake.count("exec") != 0 || fake.count("stop") != 0 || fake.count("rm") != 0 {
		t.Fatalf("workspace rejection dispatched a VM or lost no-dispatch proof: result=%+v error=%v calls=%+v", result, err, fake.calls)
	}
}

func TestSBXWorkspaceRejectsOutsideHardlinkBeforeVMCreation(t *testing.T) {
	for _, target := range []string{"nested/source.txt", ".git"} {
		t.Run(target, func(t *testing.T) {
			b, fake, r := sbxFixture(t)
			outside := filepath.Join(filepath.Dir(r.DrydockRoot), "outside-sentinel.txt")
			const original = "outside workspace inode must stay unchanged\n"
			if err := os.WriteFile(outside, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(r.DrydockRoot, filepath.FromSlash(target))
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if target == ".git" {
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Link(outside, link); err != nil {
				t.Fatal(err)
			}
			sbxAssertWorkspaceRejectionBeforeDispatch(t, b, fake, r)
			if contents, err := os.ReadFile(outside); err != nil || string(contents) != original {
				t.Fatalf("outside inode changed: %q error=%v", contents, err)
			}
		})
	}
}

func TestSBXWorkspaceRejectsLinkInsertedByFinalAuthorityCheck(t *testing.T) {
	b, fake, r := sbxFixture(t)
	outside := filepath.Join(filepath.Dir(r.DrydockRoot), "late-outside-sentinel.txt")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	inserted := false
	r.AuthorityCheck = func(context.Context) error {
		// Readiness and the collision check have observed the daemon by the
		// final callback. A scan before that callback would miss this link.
		if fake.count("ls") >= 2 && !inserted {
			inserted = true
			return os.Link(outside, filepath.Join(r.DrydockRoot, "late-hardlink.txt"))
		}
		return nil
	}
	sbxAssertWorkspaceRejectionBeforeDispatch(t, b, fake, r)
	if !inserted {
		t.Fatal("test never reached final authority callback")
	}
}

func TestSBXWorkspaceNormalNestedFilesStillDispatch(t *testing.T) {
	b, fake, r := sbxFixture(t)
	file := filepath.Join(r.DrydockRoot, "src", "nested", "normal.txt")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("ordinary owned file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := b.Run(context.Background(), r, nil)
	if err != nil || result.ExitCode != 0 || !result.TreeReaped || fake.count("create") != 1 || fake.count("exec") != 1 {
		t.Fatalf("normal workspace rejected: result=%+v error=%v", result, err)
	}
}

func TestSBXWorkspacePreflightHonorsCancellation(t *testing.T) {
	_, _, r := sbxFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sbxValidateWorkspace(ctx, r.DrydockRoot); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrSBXBoundary) {
		t.Fatalf("cancelled scan continued: %v", err)
	}
}

func TestSBXSourceRepositoryRequiresNativeRequestAndReadonlyMetadata(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "native"}[native], func(t *testing.T) {
			b, fake, r := sbxFixture(t)
			git := filepath.Join(r.DrydockRoot, ".git")
			if err := os.Remove(git); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(git, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(git, "HEAD"), []byte("ref: refs/heads/main\n"), 0600); err != nil {
				t.Fatal(err)
			}
			r.SourceRepository = native
			if !native {
				sbxAssertWorkspaceRejectionBeforeDispatch(t, b, fake, r)
				return
			}
			value, err := b.Run(t.Context(), r, nil)
			if err != nil || !value.TreeReaped || fake.count("exec") != 1 {
				t.Fatalf("native source refused: %+v %v", value, err)
			}
			for _, call := range fake.calls {
				for _, arg := range call.Arguments {
					if arg == "create" && call.Arguments[len(call.Arguments)-1] != git+":ro" {
						t.Fatal("source metadata lost read-only mount")
					}
				}
			}
		})
	}
}

func TestSBXSourceRepositoryStillRejectsLinks(t *testing.T) {
	for _, kind := range []string{"hardlink", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			b, fake, r := sbxFixture(t)
			git := filepath.Join(r.DrydockRoot, ".git")
			if err := os.Remove(git); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(git, 0700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(filepath.Dir(r.DrydockRoot), "outside.txt")
			if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			create := os.Link
			if kind == "symlink" {
				create = os.Symlink
			}
			if err := create(outside, filepath.Join(git, "unsafe")); err != nil {
				if kind == "symlink" && runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314)) {
					t.Skip("Windows account cannot create symbolic links")
				}
				t.Fatal(err)
			}
			r.SourceRepository = true
			sbxAssertWorkspaceRejectionBeforeDispatch(t, b, fake, r)
		})
	}
}
