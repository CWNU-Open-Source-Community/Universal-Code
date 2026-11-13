package rootfs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestExactRootPreservesDistinctFileNames(t *testing.T) {
	dir := t.TempDir()
	root, err := OpenExactRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	plain, exact, staged, linked := "file.txt", "file.txt ", "stage.txt ", "link.txt "
	for _, name := range []string{plain, exact, staged, linked} {
		native, err := ExactAbsolutePath(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(native) })
	}
	write := func(name, content string) {
		t.Helper()
		f, err := root.CreateExclusive(name, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := io.WriteString(f, content)
		if err := errors.Join(writeErr, f.Close()); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name, want string) {
		t.Helper()
		f, err := root.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(f)
		if err := errors.Join(readErr, f.Close()); err != nil || string(data) != want {
			t.Fatalf("read %q = %q, %v, want %q", name, data, err, want)
		}
	}
	write(plain, "plain")
	write(exact, "spaced")
	read(exact, "spaced")
	write(staged, "replacement")
	if err := root.Rename(staged, exact); err != nil {
		t.Fatal(err)
	}
	read(plain, "plain")
	read(exact, "replacement")
	if err := root.Link(exact, linked); err != nil {
		t.Fatal(err)
	}
	if err := root.Link(plain, linked); !os.IsExist(err) {
		t.Fatalf("link must not replace the destination: %v", err)
	}
	read(linked, "replacement")
	reader, err := root.Open(exact)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := root.Remove(exact); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Lstat(exact); !os.IsNotExist(err) {
		t.Fatalf("removed name still resolves while a reader holds it: %v", err)
	}
	if data, err := io.ReadAll(reader); err != nil || string(data) != "replacement" {
		t.Fatalf("existing reader lost access after unlink: %q, %v", data, err)
	}
	read(plain, "plain")
	read(linked, "replacement")
}

func TestExactRootRenamesAndRemovesEmptyDirectories(t *testing.T) {
	dir := t.TempDir()
	root, err := OpenExactRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"folder ", "moved "} {
		native, err := ExactAbsolutePath(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Remove(native) })
	}
	if err := root.Mkdir("folder ", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("folder ", "moved "); err != nil {
		t.Fatal(err)
	}
	if err := root.Remove("moved "); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Lstat("moved "); !os.IsNotExist(err) {
		t.Fatalf("removed directory still resolves: %v", err)
	}
}
