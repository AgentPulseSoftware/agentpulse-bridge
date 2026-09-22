package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	cases := []struct {
		name    string
		version string
		want    string
	}{
		{name: "dev build", version: "dev", want: "dev"},
		{name: "tagged release", version: "v1.2.3", want: "v1.2.3"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := version
			version = tc.version
			defer func() { version = original }()

			root := newRootCmd()
			out := &bytes.Buffer{}
			root.SetOut(out)
			root.SetArgs([]string{"version"})

			if err := root.Execute(); err != nil {
				t.Fatalf("Execute() returned error: %v", err)
			}

			if got := strings.TrimSpace(out.String()); got != tc.want {
				t.Errorf("version output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRootCommandHasVersionSubcommand(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"version"})
	if err != nil {
		t.Fatalf("Find(version) returned error: %v", err)
	}
	if cmd.Name() != "version" {
		t.Errorf("expected version subcommand, got %q", cmd.Name())
	}
}

func TestRootCommandHasHookAndScrubSubcommands(t *testing.T) {
	root := newRootCmd()
	for _, name := range []string{"hook", "scrub"} {
		if _, _, err := root.Find([]string{name}); err != nil {
			t.Errorf("Find(%s) returned error: %v", name, err)
		}
	}
}
