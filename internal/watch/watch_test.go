package watch

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
)

var t0 = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

func TestDecideWatchedAlwaysSpools(t *testing.T) {
	f := File{WatchList: &List{Projects: map[string]ProjectEntry{
		"hash1": {Watched: true},
	}}}
	if got := f.Decide("hash1", "proj", t0); got != DecisionSpool {
		t.Errorf("Decide = %v, want DecisionSpool", got)
	}
	if f.WatchList.Projects["hash1"].DisplayName != "proj" {
		t.Errorf("display name not recorded: %+v", f.WatchList.Projects["hash1"])
	}
}

func TestDecideUnknownProjectUsesWatchNewProjects(t *testing.T) {
	tests := []struct {
		name             string
		watchNewProjects bool
		want             Decision
	}{
		{"watch-new on: unknown project spools", true, DecisionSpool},
		{"watch-new off: unknown project gets a project_seen", false, DecisionEmitProjectSeen},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := File{WatchList: &List{WatchNewProjects: tc.watchNewProjects}}
			if got := f.Decide("hash1", "proj", t0); got != tc.want {
				t.Errorf("Decide = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDecideUnwatchedThrottlesProjectSeenAcrossSixHours(t *testing.T) {
	f := File{WatchList: &List{WatchNewProjects: false}}

	if got := f.Decide("hash1", "proj", t0); got != DecisionEmitProjectSeen {
		t.Fatalf("first sighting = %v, want DecisionEmitProjectSeen", got)
	}
	if got := f.Decide("hash1", "proj", t0.Add(1*time.Minute)); got != DecisionDrop {
		t.Errorf("one minute later = %v, want DecisionDrop", got)
	}
	if got := f.Decide("hash1", "proj", t0.Add(5*time.Hour+59*time.Minute)); got != DecisionDrop {
		t.Errorf("just under six hours later = %v, want DecisionDrop", got)
	}
	if got := f.Decide("hash1", "proj", t0.Add(6*time.Hour+1*time.Minute)); got != DecisionEmitProjectSeen {
		t.Errorf("just over six hours later = %v, want DecisionEmitProjectSeen", got)
	}
	// The clock the throttle now measures from is the second send, not
	// the first: another minute after it must drop again.
	if got := f.Decide("hash1", "proj", t0.Add(6*time.Hour+2*time.Minute)); got != DecisionDrop {
		t.Errorf("one minute after the second send = %v, want DecisionDrop", got)
	}
}

func TestDecideExplicitlyUnwatchedNeverSpools(t *testing.T) {
	f := File{WatchList: &List{WatchNewProjects: true, Projects: map[string]ProjectEntry{
		"hash1": {Watched: false},
	}}}
	if got := f.Decide("hash1", "proj", t0); got != DecisionEmitProjectSeen {
		t.Errorf("Decide = %v, want DecisionEmitProjectSeen (first sighting)", got)
	}
	if got := f.Decide("hash1", "proj", t0.Add(time.Minute)); got != DecisionDrop {
		t.Errorf("Decide = %v, want DecisionDrop", got)
	}
}

func TestApplyRelayListIgnoresStaleVersion(t *testing.T) {
	f := File{WatchList: &List{Version: 5, Projects: map[string]ProjectEntry{
		"hash1": {Watched: true, DisplayName: "keepme", LastProjectSeenAt: "2026-09-14T00:00:00Z"},
	}}}

	applied := f.ApplyRelayList(&relay.WatchList{Version: 5, Projects: map[string]relay.ProjectEntry{
		"hash1": {Watched: false},
	}})
	if applied {
		t.Error("ApplyRelayList applied a version equal to the stored one, want ignored")
	}
	if !f.WatchList.Projects["hash1"].Watched {
		t.Error("stored list was mutated despite the stale version")
	}

	applied = f.ApplyRelayList(&relay.WatchList{Version: 4, Projects: map[string]relay.ProjectEntry{
		"hash1": {Watched: false},
	}})
	if applied {
		t.Error("ApplyRelayList applied a version older than the stored one, want ignored")
	}
}

func TestApplyRelayListReplacesWhollyButKeepsLocalFields(t *testing.T) {
	f := File{WatchList: &List{Version: 5, Projects: map[string]ProjectEntry{
		"hash1": {Watched: true, DisplayName: "keepme", LastProjectSeenAt: "2026-09-14T00:00:00Z"},
		"hash2": {Watched: true, DisplayName: "gone-from-relay"},
	}}}

	applied := f.ApplyRelayList(&relay.WatchList{
		Version:          6,
		WatchNewProjects: false,
		Projects: map[string]relay.ProjectEntry{
			"hash1": {Watched: false},
			"hash3": {Watched: true},
		},
	})
	if !applied {
		t.Fatal("ApplyRelayList did not apply a newer version")
	}
	if f.WatchList.Version != 6 || f.WatchList.WatchNewProjects != false {
		t.Errorf("List = %+v, want version 6, watch_new_projects false", f.WatchList)
	}
	if _, ok := f.WatchList.Projects["hash2"]; ok {
		t.Error("hash2 survived a whole replacement that dropped it (BR-11: never merged)")
	}
	got := f.WatchList.Projects["hash1"]
	if got.Watched {
		t.Error("hash1.Watched was not replaced by the relay's value")
	}
	if got.DisplayName != "keepme" || got.LastProjectSeenAt != "2026-09-14T00:00:00Z" {
		t.Errorf("local-only fields were not preserved across replacement: %+v", got)
	}
	if f.WatchList.Projects["hash3"].Watched != true {
		t.Error("a brand new relay-only project was not added")
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watchlist.json")

	if got := Load(path); got.WatchList != nil {
		t.Errorf("Load of a missing file = %+v, want the zero File", got)
	}

	f := File{WatchList: &List{Version: 3, WatchNewProjects: true, Projects: map[string]ProjectEntry{
		"hash1": {Watched: true, DisplayName: "proj"},
	}}, Settings: &relay.Settings{TaskLabel: true}}
	if err := Save(path, f); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := Load(path)
	if got.Version() != 3 || !got.Settings.TaskLabel || got.WatchList.Projects["hash1"].DisplayName != "proj" {
		t.Errorf("round trip = %+v, want %+v", got, f)
	}
}
