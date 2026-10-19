package application

import (
	"fmt"
	"strings"
	"time"

	gitdiff "github.com/go-git/go-git/v5/utils/diff"
)

// threadChangeDiff retains equal lines between separate edits as context. The
// proposal diff's prefix/suffix-only algorithm would count those lines twice.
// go-git's existing line diff provides the same chunks used for these counts.
func threadChangeDiff(path, before, after string, budget time.Duration) (string, int, int, bool) {
	if budget <= 0 {
		return "", 0, 0, false
	}
	started := time.Now()
	chunks := gitdiff.DoWithTimeout(before, after, min(budget, 100*time.Millisecond))
	if time.Since(started) >= min(budget, 100*time.Millisecond) {
		// The dependency can return a bulk replacement on timeout; do not
		// present that incomplete calculation as a precise line count.
		return "", 0, 0, false
	}
	var body strings.Builder
	start, oldCount, newCount, additions, deletions := 1, 0, 0, 0, 0
	for index, chunk := range chunks {
		lines := strings.SplitAfter(chunk.Text, "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		prefix := byte(' ')
		switch chunk.Type {
		case -1:
			prefix = '-'
			deletions += len(lines)
		case 1:
			prefix = '+'
			additions += len(lines)
		case 0:
			if index == 0 && len(lines) > 3 {
				start += len(lines) - 3
				lines = lines[len(lines)-3:]
			}
			if index == len(chunks)-1 && len(lines) > 3 {
				lines = lines[:3]
			}
		}
		if prefix != '+' {
			oldCount += len(lines)
		}
		if prefix != '-' {
			newCount += len(lines)
		}
		for _, line := range lines {
			body.WriteByte(prefix)
			body.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				body.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
	oldStart, newStart := start, start
	if oldCount == 0 {
		oldStart = 0
	}
	if newCount == 0 {
		newStart = 0
	}
	return fmt.Sprintf("--- a/%s\n+++ b/%s\n@@ -%d,%d +%d,%d @@\n%s", path, path,
		oldStart, oldCount, newStart, newCount, body.String()), additions, deletions, true
}
