package main

import "testing"

// TestResolveRelayBaseURL covers SPEC 11.5's resolution order (--relay,
// then AGENTPULSE_RELAY, then the compiled-in default) and D93: the
// default is the production Worker's workers.dev address.
func TestResolveRelayBaseURL(t *testing.T) {
	tests := []struct {
		name string
		flag string
		env  string
		want string
	}{
		{
			name: "default is the production worker",
			want: "https://agentpulse-relay.agentpulsesoftware.workers.dev",
		},
		{
			name: "environment overrides the default",
			env:  "http://localhost:8787",
			want: "http://localhost:8787",
		},
		{
			name: "flag overrides the environment",
			flag: "https://relay.example.test",
			env:  "http://localhost:8787",
			want: "https://relay.example.test",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AGENTPULSE_RELAY", tt.env)
			got, err := resolveRelayBaseURL(tt.flag)
			if err != nil {
				t.Fatalf("resolveRelayBaseURL(%q) error: %v", tt.flag, err)
			}
			if got != tt.want {
				t.Errorf("resolveRelayBaseURL(%q) = %q, want %q", tt.flag, got, tt.want)
			}
		})
	}
}
