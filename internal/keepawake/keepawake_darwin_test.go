//go:build darwin

package keepawake

import "testing"

func TestCommandDarwin(t *testing.T) {
	tests := []struct {
		name string
		pid  int
		want []string
	}{
		{"typical pid", 4242, []string{"caffeinate", "-i", "-w", "4242"}},
		{"pid 1", 1, []string{"caffeinate", "-i", "-w", "1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argv, ok := Command(tt.pid)
			if !ok {
				t.Fatalf("Command(%d) ok = false, want true on darwin", tt.pid)
			}
			if !equalArgv(argv, tt.want) {
				t.Errorf("Command(%d) = %v, want %v", tt.pid, argv, tt.want)
			}
		})
	}
}

func TestSupportedDarwin(t *testing.T) {
	if !Supported() {
		t.Error("Supported() = false, want true on darwin (caffeinate ships with macOS)")
	}
}

func equalArgv(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
