package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/config"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/watch"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/xdgpaths"
)

// seedWatchListFor writes a watchlist.json with exactly projects.
func seedWatchListFor(t *testing.T, watchNewProjects bool, projects map[string]watch.ProjectEntry) {
	t.Helper()
	f := watch.File{WatchList: &watch.List{Version: 1, WatchNewProjects: watchNewProjects, Projects: projects}}
	if err := watch.Save(xdgpaths.WatchListPath(), f); err != nil {
		t.Fatal(err)
	}
}

// pairedProjectsDeps pairs the machine and returns a projectsDeps pointed
// at a fake relay that answers every PUT /v1/bridges/me/watch with
// handler.
func pairedProjectsDeps(t *testing.T, out *strings.Builder, handler http.HandlerFunc) projectsDeps {
	t.Helper()
	cfg := config.Default()
	cfg.BridgeID = "brg_test"
	if err := config.Save(xdgpaths.ConfigPath(), cfg); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return projectsDeps{out: out, relayFlag: srv.URL, credStore: &fakeCredStore{secret: "s3cr3t", has: true}}
}

func TestProjectsWatchSyncsAndAppliesLocally(t *testing.T) {
	setTestXDGDirs(t)
	seedWatchListFor(t, true, map[string]watch.ProjectEntry{"hash1": {Watched: false, DisplayName: "proja"}})

	var out strings.Builder
	d := pairedProjectsDeps(t, &out, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"watch_list":{"version":2,"watch_new_projects":true,"projects":{"hash1":{"watched":true}}}}`))
	})
	d.watchKey = "proja"

	if err := runProjects(context.Background(), d); err != nil {
		t.Fatalf("runProjects: %v", err)
	}
	if !strings.Contains(out.String(), "Watching proja.") {
		t.Errorf("output = %q", out.String())
	}
	got := watch.Load(xdgpaths.WatchListPath())
	if !got.WatchList.Projects["hash1"].Watched {
		t.Error("hash1 was not marked watched")
	}
	if got.WatchList.Version != 2 {
		t.Errorf("version = %d, want the relay's 2 (synced, not a local guess)", got.WatchList.Version)
	}
}

func TestProjectsUnwatch(t *testing.T) {
	setTestXDGDirs(t)
	seedWatchListFor(t, true, map[string]watch.ProjectEntry{"hash1": {Watched: true, DisplayName: "proja"}})

	var out strings.Builder
	d := pairedProjectsDeps(t, &out, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"watch_list":{"version":2,"watch_new_projects":true,"projects":{"hash1":{"watched":false}}}}`))
	})
	d.unwatchKey = "proja"

	if err := runProjects(context.Background(), d); err != nil {
		t.Fatalf("runProjects: %v", err)
	}
	if !strings.Contains(out.String(), "No longer watching proja.") {
		t.Errorf("output = %q", out.String())
	}
	got := watch.Load(xdgpaths.WatchListPath())
	if got.WatchList.Projects["hash1"].Watched {
		t.Error("hash1 is still marked watched")
	}
}

func TestProjectsWatchNew(t *testing.T) {
	setTestXDGDirs(t)
	seedWatchListFor(t, true, nil)

	var out strings.Builder
	d := pairedProjectsDeps(t, &out, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"watch_list":{"version":2,"watch_new_projects":false,"projects":{}}}`))
	})
	d.watchNew = "off"

	if err := runProjects(context.Background(), d); err != nil {
		t.Fatalf("runProjects: %v", err)
	}
	got := watch.Load(xdgpaths.WatchListPath())
	if got.WatchList.WatchNewProjects {
		t.Error("watch_new_projects is still true")
	}
}

func TestProjectsWatchAmbiguousNameListsCandidates(t *testing.T) {
	setTestXDGDirs(t)
	seedWatchListFor(t, true, map[string]watch.ProjectEntry{
		"hash1": {Watched: false, DisplayName: "app"},
		"hash2": {Watched: false, DisplayName: "app"},
	})

	d := projectsDeps{out: &strings.Builder{}, watchKey: "app"}
	err := runProjects(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "more than one project") {
		t.Fatalf("runProjects error = %v, want an ambiguous-name error", err)
	}
	if !strings.Contains(err.Error(), "hash1") || !strings.Contains(err.Error(), "hash2") {
		t.Errorf("error does not list both candidates: %v", err)
	}
}

func TestProjectsWatchUnknownKey(t *testing.T) {
	setTestXDGDirs(t)
	seedWatchListFor(t, true, map[string]watch.ProjectEntry{"hash1": {Watched: false, DisplayName: "proja"}})

	d := projectsDeps{out: &strings.Builder{}, watchKey: "does-not-exist"}
	err := runProjects(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "no known project matches") {
		t.Fatalf("runProjects error = %v, want an unknown-key error", err)
	}
}

func TestProjectsWatchSavedLocallyWhenRelayRejects(t *testing.T) {
	setTestXDGDirs(t)
	seedWatchListFor(t, true, map[string]watch.ProjectEntry{"hash1": {Watched: false, DisplayName: "proja"}})

	var out strings.Builder
	d := pairedProjectsDeps(t, &out, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	d.watchKey = "proja"

	if err := runProjects(context.Background(), d); err != nil {
		t.Fatalf("runProjects returned %v, want nil (exit 0 even when the relay 404s)", err)
	}
	if !strings.Contains(out.String(), "Saved locally, not yet synced to your phone.") {
		t.Errorf("output = %q", out.String())
	}
	got := watch.Load(xdgpaths.WatchListPath())
	if !got.WatchList.Projects["hash1"].Watched {
		t.Error("the change was not applied locally despite the relay rejecting it")
	}
}

func TestProjectsDefaultListing(t *testing.T) {
	setTestXDGDirs(t)
	seedWatchListFor(t, true, map[string]watch.ProjectEntry{
		"hash1": {Watched: true, DisplayName: "proja", LastProjectSeenAt: "2026-09-14T00:00:00Z"},
		"hash2": {Watched: false, DisplayName: "projb"},
	})

	var out strings.Builder
	if err := runProjects(context.Background(), projectsDeps{out: &out}); err != nil {
		t.Fatalf("runProjects: %v", err)
	}
	got := out.String()
	for _, want := range []string{"proja", "hash1", "on", "projb", "hash2", "off", "Watch new projects: on", "Keep-awake: off"} {
		if !strings.Contains(got, want) {
			t.Errorf("listing does not contain %q:\n%s", want, got)
		}
	}
}

func TestProjectsDefaultListingNoProjectsYet(t *testing.T) {
	setTestXDGDirs(t)
	var out strings.Builder
	if err := runProjects(context.Background(), projectsDeps{out: &out}); err != nil {
		t.Fatalf("runProjects: %v", err)
	}
	if !strings.Contains(out.String(), "No known projects yet.") {
		t.Errorf("output = %q", out.String())
	}
}
