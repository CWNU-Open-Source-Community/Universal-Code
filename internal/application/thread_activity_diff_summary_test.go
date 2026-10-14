package application

import (
	"strings"
	"testing"
)

func TestThreadActivityDiffSummaryTracksHunkRanges(t *testing.T) {
	for _, test := range []struct {
		name, diff            string
		added, removed, hunks int
	}{
		{"header-like source", strings.Join([]string{
			"--- a/first.ts", "+++ b/first.ts", "@@ -1,2 +1,2 @@",
			"---counter;", "\\ No newline at end of file", "+++counter;", " next();",
			"--- /dev/null", "+++ b/second.ts", "@@ -0,0 +1 @@", "+++created;",
		}, "\r\n"), 2, 1, 2},
		{"deleted file", "--- a/file.ts\n+++ /dev/null\n@@ -1 +0,0 @@\n---counter;", 0, 1, 1},
		{"bounded excerpt", "-before\n+after", 1, 1, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := safeThreadActivityDiffSummary(test.diff, "replace", "file.ts", "")
			if got.AddedLines != test.added || got.RemovedLines != test.removed || got.Hunks != test.hunks {
				t.Fatalf("diff summary = %#v; want +%d -%d, %d hunks", got, test.added, test.removed, test.hunks)
			}
		})
	}
}
