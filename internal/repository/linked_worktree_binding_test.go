package repository

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureLinkedWorktreeBindingPreservesMetadataAndTracksDirtyBytes(t *testing.T) {
	source := newMutationRepo(t)
	fixtureGit(t, "-C", source, "config", "core.autocrlf", "false")
	root := filepath.Join(t.TempDir(), "linked")
	fixtureGit(t, "-C", source, "worktree", "add", "--quiet", "-b", "semantic-child", root, "HEAD")
	parentMetadata := map[string][]byte{}
	for _, name := range []string{"HEAD", "index", "config"} {
		raw, err := os.ReadFile(filepath.Join(source, ".git", name))
		if err != nil {
			t.Fatal(err)
		}
		parentMetadata[name] = raw
	}
	clean, err := CaptureLinkedWorktreeBinding(t.Context(), root)
	if err != nil || clean.Branch != "semantic-child" || clean.CommonDirSHA256 == "" {
		t.Fatalf("linked root binding: %+v %v", clean, err)
	}
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dirty, err := CaptureLinkedWorktreeBinding(t.Context(), root)
	if err != nil || dirty.WorktreeSHA256 == clean.WorktreeSHA256 || dirty.StatusSHA256 == clean.StatusSHA256 || dirty.CommonDirSHA256 != clean.CommonDirSHA256 {
		t.Fatalf("dirty linked root binding: %+v %v", dirty, err)
	}
	fixtureGit(t, "-C", root, "add", "base.txt")
	staged, err := CaptureLinkedWorktreeBinding(t.Context(), root)
	if err != nil || staged.IndexSHA256 == dirty.IndexSHA256 {
		t.Fatalf("linked index was not bound: %+v %v", staged, err)
	}
	for name, before := range parentMetadata {
		after, err := os.ReadFile(filepath.Join(source, ".git", name))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("linked inspection changed parent Git metadata %s: %v", name, err)
		}
	}
	foreign := t.TempDir()
	pointer, err := os.ReadFile(filepath.Join(root, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, ".git"), pointer, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureLinkedWorktreeBinding(t.Context(), foreign); err == nil {
		t.Fatal("foreign root reused another worktree's registration")
	}
	if err := os.WriteFile(filepath.Join(foreign, ".git"), []byte("gitdir: "+filepath.ToSlash(filepath.Join(source, ".git"))+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureLinkedWorktreeBinding(t.Context(), foreign); err == nil {
		t.Fatal("unregistered root redirected to the parent's metadata")
	}
	fixtureGit(t, "-C", source, "config", "filter.unsafe.clean", "not-an-approved-command")
	if _, err := CaptureLinkedWorktreeBinding(t.Context(), root); err == nil || !strings.Contains(err.Error(), "executable Git drivers") {
		t.Fatalf("linked inspection accepted executable Git configuration: %v", err)
	}
}
