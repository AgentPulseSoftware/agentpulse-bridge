package hooks

import (
	"fmt"
	"strings"
)

// diffContext is how many unchanged lines Diff shows around each change,
// the same default as diff -u.
const diffContext = 3

// Diff produces a line-oriented diff between oldText and newText that
// shows only the changed lines and up to diffContext unchanged lines
// around each, grouped into hunks headed "@@ -l,s +l,s @@" as in a
// unified diff (l is the first line number, s the number of lines). Inside
// a hunk, removed lines are prefixed "- ", added lines "+ ", and unchanged
// lines "  " (two spaces, so everything lines up). It is used to show the
// operator exactly what a settings-file change will do (BR-07: "shows the
// exact hook JSON diff") before they confirm it, without printing the
// rest of a settings file that may hold unrelated values the operator
// would not want on screen. Identical inputs give "".
//
// Settings files are small (a handful of hook entries plus whatever else
// the operator has configured), so a classic O(n*m)
// longest-common-subsequence diff is fast enough and needs no dependency
// beyond the standard library.
func Diff(oldText, newText string) string {
	ops := editScript(splitLines(oldText), splitLines(newText))
	var b strings.Builder
	for start := 0; start < len(ops); {
		first := start
		for first < len(ops) && ops[first].kind == ' ' {
			first++
		}
		if first == len(ops) {
			break
		}
		lo := max(first-diffContext, start)
		hi := first
		for {
			for hi < len(ops) && ops[hi].kind != ' ' {
				hi++
			}
			next := hi
			for next < len(ops) && ops[next].kind == ' ' {
				next++
			}
			// Two changes separated by no more unchanged lines than
			// their combined context share one hunk.
			if next == len(ops) || next-hi > 2*diffContext {
				hi = min(hi+diffContext, len(ops))
				break
			}
			hi = next
		}
		writeHunk(&b, ops[lo:hi])
		start = hi
	}
	return b.String()
}

// diffOp is one line of an edit script: kind is ' ' (unchanged), '-'
// (only in the old text) or '+' (only in the new text). oldLine and
// newLine are the 0-based positions in each text where the op sits.
type diffOp struct {
	kind             byte
	text             string
	oldLine, newLine int
}

// editScript returns the shortest edit script turning oldLines into
// newLines, built from their longest common subsequence.
func editScript(oldLines, newLines []string) []diffOp {
	n, m := len(oldLines), len(newLines)

	// lcs[i][j] = length of the longest common subsequence of
	// oldLines[i:] and newLines[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case oldLines[i] == newLines[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	ops := make([]diffOp, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			ops = append(ops, diffOp{' ', oldLines[i], i, j})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{'-', oldLines[i], i, j})
			i++
		default:
			ops = append(ops, diffOp{'+', newLines[j], i, j})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'-', oldLines[i], i, j})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'+', newLines[j], i, j})
	}
	return ops
}

// writeHunk writes one hunk: its header, then each op with its prefix.
func writeHunk(b *strings.Builder, ops []diffOp) {
	oldCount, newCount := 0, 0
	for _, op := range ops {
		if op.kind != '+' {
			oldCount++
		}
		if op.kind != '-' {
			newCount++
		}
	}
	fmt.Fprintf(b, "@@ -%s +%s @@\n",
		hunkRange(ops[0].oldLine, oldCount), hunkRange(ops[0].newLine, newCount))
	for _, op := range ops {
		b.WriteByte(op.kind)
		b.WriteString(" " + op.text + "\n")
	}
}

// hunkRange formats a hunk header range as unified diffs do: the 1-based
// first line and the line count, except that an empty range names the
// line just before it.
func hunkRange(start, count int) string {
	if count == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	return fmt.Sprintf("%d,%d", start+1, count)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}
