package repository

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	git "github.com/go-git/go-git/v5"
)

func TestDiffDoesNotTreatReadFailureAsBinary(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "directory.txt"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 1}, 0600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"directory.txt": DiffContentMissing, "binary.bin": DiffContentBinary} {
		_, state, _, _ := readWorkspaceText(root, "read-errors", path)
		if state != want {
			t.Fatalf("%s: state=%s, want %s", path, state, want)
		}
	}
	if got := combinedContentState("present", DiffContentMissing); got != DiffContentMissing {
		t.Fatalf("read failure became %s", got)
	}
}

func TestDiffRejectsDetectedChangesAfterStatusScan(t *testing.T) {
	for _, mutation := range []string{"delete", "rename", "replace-with-directory", "reappear"} {
		t.Run(mutation, func(t *testing.T) {
			root := diffTestRepository(t, map[string]string{"sample.txt": "original\n"})
			path := filepath.Join(root, "sample.txt")
			if err := os.WriteFile(path, []byte("modified\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if mutation == "reappear" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			state, err := inspectChanges(t.Context(), root, "races", MaxStatusEntries)
			if err != nil || len(state.Changes) != 1 {
				t.Fatalf("scan: %+v, %v", state, err)
			}
			repo, err := git.PlainOpen(root)
			if err != nil {
				t.Fatal(err)
			}
			tree, err := repositoryHeadTree(repo)
			if err != nil {
				t.Fatal(err)
			}
			// Deterministically change the real filesystem after the same status
			// scan used by InspectDiff, before its per-file read.
			switch mutation {
			case "delete":
				err = os.Remove(path)
			case "rename":
				err = os.Rename(path, filepath.Join(root, "renamed.txt"))
			case "replace-with-directory":
				if err = os.Remove(path); err == nil {
					err = os.Mkdir(path, 0700)
				}
			case "reappear":
				err = os.WriteFile(path, []byte("reappeared\n"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			item, _, err := buildDiffItem(t.Context(), tree, root, "races", state.Changes[0])
			if err != nil {
				t.Fatal(err)
			}
			if item.ContentState != DiffContentMissing || item.StatisticsComplete || item.Patch != "" || item.AddedLines != 0 || item.DeletedLines != 0 {
				t.Fatalf("changed path presented as a complete diff: %+v", item)
			}
			if mutation == "delete" || mutation == "reappear" {
				refreshed, err := InspectDiff(t.Context(), root, "races")
				if err != nil || len(refreshed.Items) != 1 {
					t.Fatalf("refresh: %+v, %v", refreshed, err)
				}
				current := refreshed.Items[0]
				if !current.StatisticsComplete || current.ContentState != DiffContentText || current.DeletedLines != 1 {
					t.Fatalf("refresh did not restore actual text change: %+v", current)
				}
			}
		})
	}
}

func TestDiffLivePaginationRecoversAfterInsertDeleteAndRename(t *testing.T) {
	root := diffTestRepository(t, map[string]string{"base.txt": "base\n"})
	write := func(path string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte("new\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 51; i++ {
		write(fmt.Sprintf("m-%02d.txt", i))
	}
	first, err := InspectDiff(t.Context(), root, "live-pages")
	if err != nil || first.NextAfter != "m-49.txt" {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	write("a-before-cursor.txt")
	write("z-after-cursor.txt")
	if err := os.Remove(filepath.Join(root, "m-50.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "m-48.txt"), filepath.Join(root, "z-renamed.txt")); err != nil {
		t.Fatal(err)
	}
	second, err := InspectDiff(t.Context(), root, "live-pages", DiffOptions{After: first.NextAfter})
	if err != nil {
		t.Fatal(err)
	}
	paths := func(items []DiffItem) []string {
		result := make([]string, 0, len(items))
		for _, item := range items {
			result = append(result, item.Path)
		}
		return result
	}
	if got := paths(second.Items); !slices.Equal(got, []string{"z-after-cursor.txt", "z-renamed.txt"}) {
		t.Fatalf("live continuation: %v", got)
	}
	// Refresh starts without a cursor and discovers the new earlier path.
	var refreshed []string
	after := ""
	for page := 0; page < 3; page++ {
		got, err := InspectDiff(t.Context(), root, "live-pages", DiffOptions{After: after})
		if err != nil {
			t.Fatal(err)
		}
		refreshed = append(refreshed, paths(got.Items)...)
		if got.NextAfter == "" {
			break
		}
		after = got.NextAfter
	}
	if len(refreshed) != 52 || refreshed[0] != "a-before-cursor.txt" || slices.Contains(refreshed, "m-48.txt") || slices.Contains(refreshed, "m-50.txt") {
		t.Fatalf("refresh did not restore current paths: %v", refreshed)
	}
}
