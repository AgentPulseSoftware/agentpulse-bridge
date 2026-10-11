package hooks

import (
	"fmt"
	"strings"
	"testing"
)

// numbered returns the n lines "line 1" to "line n".
func numbered(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	return lines
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// replaced returns a copy of lines with lines[i] set to s.
func replaced(lines []string, i int, s string) []string {
	out := append([]string(nil), lines...)
	out[i] = s
	return out
}

func TestDiff(t *testing.T) {
	twenty := numbered(20)
	tests := []struct {
		name string
		old  string
		new  string
		want string
	}{
		{
			name: "identical inputs give nothing",
			old:  joinLines(twenty),
			new:  joinLines(twenty),
			want: "",
		},
		{
			name: "new file shows every added line",
			old:  "",
			new:  "{\n}\n",
			want: "@@ -0,0 +1,2 @@\n+ {\n+ }\n",
		},
		{
			name: "one change shows three lines of context each side",
			old:  joinLines(twenty),
			new:  joinLines(replaced(twenty, 9, "changed")),
			want: "@@ -7,7 +7,7 @@\n" +
				"  line 7\n  line 8\n  line 9\n" +
				"- line 10\n+ changed\n" +
				"  line 11\n  line 12\n  line 13\n",
		},
		{
			name: "change at the very top has no leading context",
			old:  joinLines(twenty),
			new:  joinLines(append([]string{"first"}, twenty...)),
			want: "@@ -1,3 +1,4 @@\n+ first\n  line 1\n  line 2\n  line 3\n",
		},
		{
			name: "change at the very end has no trailing context",
			old:  joinLines(twenty),
			new:  joinLines(append(append([]string(nil), twenty...), "last")),
			want: "@@ -18,3 +18,4 @@\n  line 18\n  line 19\n  line 20\n+ last\n",
		},
		{
			name: "changes six lines apart share one hunk",
			old:  joinLines(twenty),
			new:  joinLines(replaced(replaced(twenty, 3, "a"), 10, "b")),
			want: "@@ -1,14 +1,14 @@\n" +
				"  line 1\n  line 2\n  line 3\n- line 4\n+ a\n" +
				"  line 5\n  line 6\n  line 7\n  line 8\n  line 9\n  line 10\n" +
				"- line 11\n+ b\n  line 12\n  line 13\n  line 14\n",
		},
		{
			name: "changes seven lines apart get two hunks",
			old:  joinLines(twenty),
			new:  joinLines(replaced(replaced(twenty, 3, "a"), 11, "b")),
			want: "@@ -1,7 +1,7 @@\n" +
				"  line 1\n  line 2\n  line 3\n- line 4\n+ a\n  line 5\n  line 6\n  line 7\n" +
				"@@ -9,7 +9,7 @@\n" +
				"  line 9\n  line 10\n  line 11\n- line 12\n+ b\n  line 13\n  line 14\n  line 15\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Diff(tt.old, tt.new); got != tt.want {
				t.Errorf("Diff() =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// TestDiffLeavesDistantSettingsOut is the reason Diff prints hunks: the
// pair and unpair previews must not echo a settings value far from the
// hook change, such as an API key in an "env" block, to the terminal.
func TestDiffLeavesDistantSettingsOut(t *testing.T) {
	const fake = "not-a-real-key-0000"
	old := "{\n" +
		"  \"env\": {\n" +
		"    \"SOME_API_KEY\": \"" + fake + "\"\n" +
		"  },\n" +
		"  \"model\": \"x\",\n" +
		"  \"theme\": \"dark\",\n" +
		"  \"verbose\": false,\n" +
		"  \"other\": 1\n" +
		"}\n"
	updated := strings.Replace(old, "  \"other\": 1\n", "  \"other\": 1,\n  \"hooks\": {}\n", 1)
	got := Diff(old, updated)
	if !strings.Contains(got, "+   \"hooks\": {}") {
		t.Fatalf("diff does not show the added hooks line:\n%s", got)
	}
	if strings.Contains(got, fake) {
		t.Errorf("diff shows a settings value five lines from the change:\n%s", got)
	}
}
