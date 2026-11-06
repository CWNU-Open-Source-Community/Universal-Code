package repository

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestInspectDiffProjectsRedactedBoundedPatches(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("mode=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("tracked.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Commit("initial", &git.CommitOptions{Author: &object.Signature{
		Name: "test", Email: "test@example.invalid",
	}}); err != nil {
		t.Fatal(err)
	}
	secret := "sk-123456789012345678901234567890"
	if err := os.WriteFile(tracked, []byte("API_KEY="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	projection, err := InspectDiff(context.Background(), root, "workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	if projection.ProtocolVersion != DiffProtocolVersion || projection.Kind != "git" ||
		!projection.Available || projection.ReturnedCount != 2 ||
		projection.TotalPatchBytes <= 0 || projection.RedactionCount == 0 {
		t.Fatalf("unexpected repository diff: %+v", projection)
	}
	joined := projection.Items[0].Patch + projection.Items[1].Patch
	if strings.Contains(joined, secret) || !strings.Contains(joined, "[REDACTED:secret]") ||
		!strings.Contains(joined, "+hello") {
		t.Fatalf("repository diff did not preserve redacted changes: %q", joined)
	}
	if !projection.ReadOnly || projection.InstructionAuthorized ||
		projection.MutationSupported || projection.AuthorityGranted ||
		projection.RootPathExposed || projection.RawContentIncluded ||
		!projection.PatchContentIncluded || projection.RemoteConfigIncluded ||
		projection.ProcessStarted || projection.NetworkUsed || projection.HooksExecuted {
		t.Fatalf("repository diff widened authority: %+v", projection)
	}
}

func TestInspectDiffOmitsOversizeAndLinkedContent(t *testing.T) {
	root := t.TempDir()
	if _, err := git.PlainInit(root, false); err != nil {
		t.Fatal(err)
	}
	large := strings.Repeat("x", MaxDiffFileBytes+1)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(large), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.txt")); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}

	projection, err := InspectDiff(context.Background(), root, "workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, item := range projection.Items {
		states[item.Path] = item.ContentState
		if item.Patch != "" || item.PatchBytes != 0 {
			t.Fatalf("unsupported repository content was projected: %+v", item)
		}
	}
	if states["large.txt"] != DiffContentTooLarge ||
		states["linked.txt"] != DiffContentLinked {
		t.Fatalf("repository content limits were not explicit: %+v", projection.Items)
	}
}

func TestInspectDiffDoesNotDiscoverParentRepository(t *testing.T) {
	root := t.TempDir()
	if _, err := git.PlainInit(root, false); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	projection, err := InspectDiff(context.Background(), child, "workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	if projection.Available || projection.Kind != "none" || len(projection.Items) != 0 ||
		projection.PatchContentIncluded {
		t.Fatalf("parent repository was discovered for diff: %+v", projection)
	}
}

func TestBoundPatchCountsTheTruncationMarkerInsideTheLimit(t *testing.T) {
	patch, truncated := boundPatch(strings.Repeat("+bounded line\n", 30000),
		MaxDiffPatchBytes)
	if !truncated || len([]byte(patch)) > MaxDiffPatchBytes ||
		!strings.HasSuffix(patch, "@@ diff truncated by repository_diff.v1 @@\n") {
		t.Fatalf("bounded patch bytes=%d truncated=%v", len([]byte(patch)), truncated)
	}
}

func TestInspectDiffPromotesItemTruncationToProjection(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "large-diff.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("old line\n", 20000)), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("large-diff.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Commit("large base", &git.CommitOptions{Author: &object.Signature{
		Name: "test", Email: "test@example.invalid",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("new line\n", 20000)), 0o600); err != nil {
		t.Fatal(err)
	}

	projection, err := InspectDiff(context.Background(), root, "workspace-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Items) != 1 || !projection.Items[0].Truncated ||
		!projection.Truncated || projection.Items[0].PatchBytes > MaxDiffPatchBytes {
		t.Fatalf("repository item truncation was not promoted: %+v", projection)
	}
}
