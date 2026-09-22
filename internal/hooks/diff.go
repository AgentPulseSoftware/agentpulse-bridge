package hooks

import "strings"

// Diff produces a minimal line-oriented diff between oldText and newText,
// prefixing removed lines with "- ", added lines with "+ ", and unchanged
// lines with "  " (two spaces, so everything lines up). It is used to show
// the operator exactly what a settings-file change will do (BR-07: "shows
// the exact hook JSON diff") before they confirm it.
//
// Settings files are small (a handful of hook entries plus whatever else the
// operator has configured), so a classic O(n*m) longest-common-subsequence
// diff is fast enough and needs no dependency beyond the standard library.
func Diff(oldText, newText string) string {
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)
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

	var b strings.Builder
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			b.WriteString("  " + oldLines[i] + "\n")
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			b.WriteString("- " + oldLines[i] + "\n")
			i++
		default:
			b.WriteString("+ " + newLines[j] + "\n")
			j++
		}
	}
	for ; i < n; i++ {
		b.WriteString("- " + oldLines[i] + "\n")
	}
	for ; j < m; j++ {
		b.WriteString("+ " + newLines[j] + "\n")
	}
	return b.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}
