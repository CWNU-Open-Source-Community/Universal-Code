package repository

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestDiffLargeFileSmallDistantChanges(t *testing.T) {
	var original strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&original, "line %04d %s\n", i, strings.Repeat("sample ", 60))
	}
	before := original.String()
	after := strings.ReplaceAll(strings.ReplaceAll(before, "line 0010 ", "changed 0010 "), "line 4990 ", "changed 4990 ")
	root := diffTestRepository(t, map[string]string{"engine.txt": before})
	if err := os.WriteFile(filepath.Join(root, "engine.txt"), []byte(after), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := InspectDiff(t.Context(), root, "large-diff")
	if err != nil {
		t.Fatal(err)
	}
	item := got.Items[0]
	if len(before) < 2*1024*1024 || item.ContentState != DiffContentText || !item.StatisticsComplete || item.AddedLines != 2 || item.DeletedLines != 2 || item.Truncated || item.PatchBytes > 10000 {
		t.Fatalf("large sparse diff: state=%s complete=%v +%d/-%d patchBytes=%d", item.ContentState, item.StatisticsComplete, item.AddedLines, item.DeletedLines, item.PatchBytes)
	}
	if strings.Count(item.Patch, "@@ -") != 2 || strings.Contains(item.Patch, "line 2500") {
		t.Fatalf("independent hunks not preserved: %s", item.Patch)
	}
}

func TestDiffPreviewTruncationKeepsFullCounts(t *testing.T) {
	before, after := strings.Repeat("old line\n", 20000), strings.Repeat("new line\n", 20000)
	edits, ok := lineEdits(t.Context(), before, after)
	if !ok {
		t.Fatal("disjoint line sets should be a linear replacement")
	}
	patch, add, del, truncated := renderLinePatch("large.txt", edits, MaxDiffPatchBytes)
	if !truncated || add != 20000 || del != 20000 || len(patch) > MaxDiffPatchBytes {
		t.Fatalf("truncation lost counts: +%d/-%d len=%d", add, del, len(patch))
	}
}

func TestRenderedLinePatchPassesGitApplyCheck(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git not installed")
	}
	for _, pair := range [][2]string{{"", "added\n"}, {"deleted\n", ""}, {"a\nb\nc\n", "a\nchanged\nc\n"}, {"no newline", "no newline\n"}, {"a\n1\n2\n3\n4\n5\n6\n7\nz\n", "b\n1\n2\n3\n4\n5\n6\n7\ny\n"}} {
		root := diffTestRepository(t, map[string]string{"sample.txt": pair[0]})
		edits, ok := lineEdits(t.Context(), pair[0], pair[1])
		if !ok {
			t.Fatal("unexpected budget exhaustion")
		}
		patch, _, _, limited := renderLinePatch("sample.txt", edits, MaxDiffPatchBytes)
		if limited {
			t.Fatal("unexpected truncation")
		}
		cmd := exec.CommandContext(t.Context(), gitPath, "apply", "--check", "--whitespace=nowarn", "-")
		cmd.Dir = root
		cmd.Stdin = strings.NewReader(patch)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("invalid unified patch: %s: %v\n%s", out, err, patch)
		}
	}
}

func TestLargeDiffRedactsBeyondOldExplorerLimit(t *testing.T) {
	before := strings.Repeat("unchanged\n", 10000) + "API_KEY=sk-123456789012345678901234567890\n"
	root := diffTestRepository(t, map[string]string{"secret.txt": before})
	after := strings.Replace(before, "sk-123456789012345678901234567890", "sk-987654321098765432109876543210", 1)
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte(after), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := InspectDiff(t.Context(), root, "secrets")
	if err != nil {
		t.Fatal(err)
	}
	item := got.Items[0]
	if !item.Redacted || item.StatisticsComplete || item.Patch != "" || item.PatchBytes != 0 ||
		item.AddedLines != 0 || item.DeletedLines != 0 || item.ContentState != DiffContentText {
		t.Fatalf("secret projection incorrect: %+v", item)
	}
}

func TestDiffPagesReachFilesBeyondStatePreview(t *testing.T) {
	root := diffTestRepository(t, map[string]string{"base.txt": "base\n"})
	for i := 0; i < 205; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%03d.txt", i)), []byte("new\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	after := ""
	for page := 0; page < 6; page++ {
		got, err := InspectDiff(t.Context(), root, "pages", DiffOptions{After: after})
		if err != nil {
			t.Fatal(err)
		}
		if got.Truncated || len(got.Items) > MaxDiffItems {
			t.Fatalf("pagination incorrectly marked as data loss: %+v", got)
		}
		for _, item := range got.Items {
			if seen[item.Path] {
				t.Fatal("duplicate", item.Path)
			}
			seen[item.Path] = true
		}
		if got.NextAfter == "" {
			break
		}
		if got.NextAfter <= after {
			t.Fatal("nonadvancing page")
		}
		after = got.NextAfter
	}
	if len(seen) != 205 {
		t.Fatalf("only reached %d files", len(seen))
	}
	for _, bad := range []string{"../outside", "C:/file", "a/../b", "a\\b", "."} {
		if _, err := InspectDiff(t.Context(), root, "pages", DiffOptions{After: bad}); err == nil {
			t.Fatal("accepted invalid cursor", bad)
		}
	}
}

func TestDiffByteBudgetContinuesOnNextPage(t *testing.T) {
	root := diffTestRepository(t, map[string]string{"base.txt": "base\n"})
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("large-%02d.txt", i)), []byte(strings.Repeat("new line\n", 40000)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	page, err := InspectDiff(t.Context(), root, "pages")
	if err != nil {
		t.Fatal(err)
	}
	if page.NextAfter == "" || page.TotalPatchBytes > MaxDiffTotalBytes || !page.Truncated {
		t.Fatalf("missing bounded continuation: %+v", page)
	}
	next, err := InspectDiff(t.Context(), root, "pages", DiffOptions{After: page.NextAfter})
	if err != nil || next.ReturnedCount == 0 || page.ReturnedCount+next.ReturnedCount != 10 {
		t.Fatalf("byte paging lost files: %d + %d, %v", page.ReturnedCount, next.ReturnedCount, err)
	}
}

func TestDiffLineBudgetAndCancellationAreExplicit(t *testing.T) {
	if _, ok := lineEdits(t.Context(), "", strings.Repeat("x\n", MaxDiffLines+1)); ok {
		t.Fatal("line limit ignored")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, ok := lineEdits(ctx, "a\nb\n", "b\na\n"); ok {
		t.Fatal("cancellation ignored")
	}
	var a, b strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&a, "%d\n", i)
		fmt.Fprintf(&b, "%d\n", 1999-i)
	}
	if _, ok := lineEdits(t.Context(), a.String(), b.String()); ok {
		t.Fatal("complex diff must stop at the work/distance budget")
	}
}

func TestLineEditsRoundTripAndMinimalCounts(t *testing.T) {
	rng := rand.New(rand.NewPCG(17, 23))
	for trial := 0; trial < 500; trial++ {
		makeText := func() string {
			var b strings.Builder
			for n := rng.IntN(15); n > 0; n-- {
				fmt.Fprintf(&b, "%d\n", rng.IntN(5))
			}
			return b.String()
		}
		a, b := makeText(), makeText()
		if trial%3 == 0 {
			a = strings.TrimSuffix(a, "\n")
		}
		if trial%4 == 0 {
			b = strings.TrimSuffix(b, "\n")
		}
		edits, ok := lineEdits(t.Context(), a, b)
		if !ok {
			t.Fatal("small diff exceeded budget")
		}
		var old, next strings.Builder
		changes := 0
		for _, edit := range edits {
			if edit.kind != '+' {
				old.WriteString(edit.text)
			}
			if edit.kind != '-' {
				next.WriteString(edit.text)
			}
			if edit.kind != ' ' {
				changes++
			}
		}
		if old.String() != a || next.String() != b {
			t.Fatalf("edits do not reconstruct both inputs: %q -> %q", a, b)
		}
		// Independent dynamic-programming oracle on tiny inputs only.
		x, y := diffLines(a), diffLines(b)
		lcs := make([][]int, len(x)+1)
		for i := range lcs {
			lcs[i] = make([]int, len(y)+1)
		}
		for i := len(x) - 1; i >= 0; i-- {
			for j := len(y) - 1; j >= 0; j-- {
				if x[i] == y[j] {
					lcs[i][j] = 1 + lcs[i+1][j+1]
				} else {
					lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
				}
			}
		}
		if changes != len(x)+len(y)-2*lcs[0][0] {
			t.Fatalf("nonminimal line changes: %q -> %q, %d", a, b, changes)
		}
	}
}

func diffTestRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for path, text := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := wt.Commit("diff baseline", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@example.invalid"}}); err != nil {
		t.Fatal(err)
	}
	return root
}

func BenchmarkDiffSparseTwoMiB(b *testing.B) {
	before := strings.Repeat("unchanged line 0123456789\n", 90000)
	after := "new first\n" + before + "new last\n"
	b.ReportAllocs()
	for b.Loop() {
		edits, ok := lineEdits(context.Background(), before, after)
		if !ok {
			b.Fatal("budget")
		}
		renderLinePatch("large.txt", edits, MaxDiffPatchBytes)
	}
}
