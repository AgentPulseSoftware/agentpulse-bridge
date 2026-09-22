package cmdnorm

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"bare command", "pytest -q", "pytest -q"},
		{"env assignment", "CI=true pytest -q", "pytest -q"},
		{"multiple env assignments", "FOO=1 BAR=baz pytest -q", "pytest -q"},
		{"cd with &&", "cd /Users/sam/project && pytest -q", "pytest -q"},
		{"cd with ;", "cd /Users/sam/project; pytest -q", "pytest -q"},
		{"env then cd", "CI=true cd /Users/sam/project && pytest -q", "pytest -q"},
		{"pipeline keeps first segment", "go test ./... -v | grep FAIL", "go test ./... -v"},
		{"trims whitespace", "  pytest -q  ", "pytest -q"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.cmd); got != tc.want {
				t.Errorf("Normalize(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}
