package repository

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

const (
	MaxDiffLines    = 100_000
	maxDiffDistance = 1024
	maxDiffWork     = 2_000_000
)

type diffLine struct {
	kind byte
	text string
}

// lineEdits runs a bounded Myers line diff. Budget exhaustion is explicit: a
// coarse replacement must never masquerade as a complete line-change count.
func lineEdits(ctx context.Context, before, after string) ([]diffLine, bool) {
	if strings.Count(before, "\n") > MaxDiffLines || strings.Count(after, "\n") > MaxDiffLines {
		return nil, false
	}
	a, b := diffLines(before), diffLines(after)
	if len(a) > MaxDiffLines || len(b) > MaxDiffLines {
		return nil, false
	}
	prefix, suffix := 0, 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	x, y := a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]
	var middle []diffLine
	if !hasCommonLine(x, y) {
		for _, line := range x {
			middle = append(middle, diffLine{'-', line})
		}
		for _, line := range y {
			middle = append(middle, diffLine{'+', line})
		}
	} else {
		var ok bool
		middle, ok = myersLines(ctx, x, y)
		if !ok {
			return nil, false
		}
	}
	if ctx.Err() != nil {
		return nil, false
	}
	result := make([]diffLine, 0, prefix+len(middle)+suffix)
	for _, line := range a[:prefix] {
		result = append(result, diffLine{' ', line})
	}
	result = append(result, middle...)
	for _, line := range a[len(a)-suffix:] {
		result = append(result, diffLine{' ', line})
	}
	return result, true
}

func diffLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func hasCommonLine(a, b []string) bool {
	seen := make(map[string]struct{}, len(a))
	for _, line := range a {
		seen[line] = struct{}{}
	}
	for _, line := range b {
		if _, ok := seen[line]; ok {
			return true
		}
	}
	return false
}

func myersLines(ctx context.Context, a, b []string) ([]diffLine, bool) {
	limit := min(len(a)+len(b), maxDiffDistance)
	offset := limit + 1
	v := make([]int, 2*limit+3)
	trace := make([][]int, 0, limit+1)
	work := 0
	for d := 0; d <= limit; d++ {
		if ctx.Err() != nil {
			return nil, false
		}
		for k := -d; k <= d; k += 2 {
			i := offset + k
			x := v[i-1] + 1
			if k == -d || (k != d && v[i-1] < v[i+1]) {
				x = v[i+1]
			}
			y := x - k
			work++
			for x < len(a) && y < len(b) && a[x] == b[y] {
				x++
				y++
				work++
			}
			if work > maxDiffWork {
				return nil, false
			}
			v[i] = x
			if x >= len(a) && y >= len(b) {
				var edits []diffLine
				for depth := d; depth > 0; depth-- {
					previous := trace[depth-1]
					k := x - y
					pk := k - 1
					if k == -depth || (k != depth && previous[offset+k-1] < previous[offset+k+1]) {
						pk = k + 1
					}
					px := previous[offset+pk]
					py := px - pk
					for x > px && y > py {
						x--
						y--
						edits = append(edits, diffLine{' ', a[x]})
					}
					if x == px {
						y--
						edits = append(edits, diffLine{'+', b[y]})
					} else {
						x--
						edits = append(edits, diffLine{'-', a[x]})
					}
				}
				for x > 0 && y > 0 {
					x--
					y--
					edits = append(edits, diffLine{' ', a[x]})
				}
				slices.Reverse(edits)
				return edits, true
			}
		}
		trace = append(trace, slices.Clone(v))
	}
	return nil, false
}

// renderLinePatch counts the whole edit script, but writes only a bounded
// preview. Three context lines surround each independent hunk.
func renderLinePatch(path string, edits []diffLine, limit int) (string, int, int, bool) {
	added, deleted := 0, 0
	oldPositions, newPositions := make([]int, len(edits)+1), make([]int, len(edits)+1)
	oldPositions[0], newPositions[0] = 1, 1
	for i, line := range edits {
		oldPositions[i+1], newPositions[i+1] = oldPositions[i], newPositions[i]
		if line.kind != '+' {
			oldPositions[i+1]++
		}
		if line.kind != '-' {
			newPositions[i+1]++
		}
		if line.kind == '+' {
			added++
		}
		if line.kind == '-' {
			deleted++
		}
	}
	if added+deleted == 0 {
		return "", 0, 0, false
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- a/%s\n+++ b/%s\n", path, path)
	for i := 0; i < len(edits); {
		if edits[i].kind == ' ' {
			i++
			continue
		}
		start, last := max(0, i-3), i
		end := min(len(edits), last+4)
		for j := i + 1; j < len(edits) && j <= last+7; j++ {
			if edits[j].kind != ' ' {
				last = j
				end = min(len(edits), last+4)
			}
		}
		oldCount, newCount := oldPositions[end]-oldPositions[start], newPositions[end]-newPositions[start]
		oldStart, newStart := oldPositions[start], newPositions[start]
		if oldCount == 0 {
			oldStart--
		}
		if newCount == 0 {
			newStart--
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for j := start; j < end; j++ {
			line := edits[j]
			out.WriteByte(line.kind)
			out.WriteString(line.text)
			if !strings.HasSuffix(line.text, "\n") {
				out.WriteString("\n\\ No newline at end of file\n")
			}
			if out.Len() > limit {
				patch, _ := boundPatch(out.String(), limit)
				return patch, added, deleted, true
			}
		}
		i = end
	}
	patch, truncated := boundPatch(out.String(), limit)
	return patch, added, deleted, truncated
}
