// Package watch owns $XDG_STATE_HOME/agentpulse/watchlist.json, BR-10's
// local watch list and BR-11's target for the relay's atomic replacement.
// "agentpulse flush" already fetches and persists the relay's watch_list
// and settings (see cmd/agentpulse/flush.go); this package is the single
// place that file's contents are interpreted: whether one classified
// event should be spooled, dropped, or downgraded to a throttled
// `project_seen` (BR-10), and how a fresher relay list replaces the
// local one without losing this package's own local-only bookkeeping
// (display names, last-seen times) that the relay never sends.
package watch

import (
	"encoding/json"
	"os"
	"time"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/atomicfile"
	"github.com/agentpulsesoftware/agentpulse-bridge/internal/relay"
)

// ProjectSeenThrottle is BR-10's "at most one project_seen event every 6
// hours" per unwatched project.
const ProjectSeenThrottle = 6 * time.Hour

// File is the whole persisted watchlist.json document: the relay-owned
// watch list and settings plus
// nothing else — Settings travels alongside WatchList here only because
// they already share one file on disk.
type File struct {
	WatchList *List           `json:"watch_list,omitempty"`
	Settings  *relay.Settings `json:"settings,omitempty"`
}

// List is this bridge's local record of BR-10/BR-11's watch list: the
// relay's wire fields (Version, WatchNewProjects, each project's Watched
// bool) plus two local-only fields per project that never leave the
// machine and that the relay's wholesale replacement (BR-11) must not
// erase: DisplayName (for "agentpulse projects") and LastProjectSeenAt
// (the project_seen throttle and the "last seen" column).
type List struct {
	Version          int                     `json:"version"`
	WatchNewProjects bool                    `json:"watch_new_projects"`
	Projects         map[string]ProjectEntry `json:"projects"`
}

// ProjectEntry is one project's local record.
type ProjectEntry struct {
	Watched bool `json:"watched"`
	// DisplayName is the BR-13 project name last observed locally for
	// this key_hash — never sent anywhere; it is what "agentpulse
	// projects" prints.
	DisplayName string `json:"display_name,omitempty"`
	// LastProjectSeenAt is RFC 3339 UTC: the last time this key_hash was
	// spooled or given a throttled project_seen (never updated on a
	// Drop decision, so the 6-hour window is measured from the last real
	// send, not merely the last hook call).
	LastProjectSeenAt string `json:"last_project_seen_at,omitempty"`
}

// Decision is what Decide says to do with one classified event for a
// project.
type Decision int

const (
	// DecisionSpool: send the event as classified.
	DecisionSpool Decision = iota
	// DecisionDrop: an unwatched project inside its throttle window —
	// nothing is spooled at all (BR-10).
	DecisionDrop
	// DecisionEmitProjectSeen: an unwatched project outside its throttle
	// window (or never seen before) — the caller should spool a
	// `project_seen` event instead of the one it classified.
	DecisionEmitProjectSeen
)

// Load reads watchlist.json at path, tolerantly: a missing or
// unparseable file yields the zero File (no relay sync yet), matching
// internal/config and internal/state's Load contract.
func Load(path string) File {
	data, err := os.ReadFile(path) //nolint:gosec // path is derived from the bridge's own XDG state dir, not attacker input
	if err != nil {
		return File{}
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}
	}
	return f
}

// Save writes watchlist.json at path atomically, mode 0600.
func Save(path string, f File) error {
	return atomicfile.WriteJSON(path, f, 0o600)
}

// Version returns the stored watch list's version, or 0 when none is
// stored yet — the value "agentpulse flush" sends as watch_list_version.
func (f File) Version() int {
	if f.WatchList == nil {
		return 0
	}
	return f.WatchList.Version
}

// ApplyRelayList replaces f's watch list with rl per BR-11: atomic and
// whole (never merged field by field), and only when rl is newer than
// what is already stored. Each project's local-only DisplayName and
// LastProjectSeenAt are carried over from the entry at the same
// key_hash, if any — those fields are never part of what the relay
// sends, so a wholesale replace of the relay's half must not erase them.
// Reports whether it applied.
func (f *File) ApplyRelayList(rl *relay.WatchList) bool {
	if rl == nil {
		return false
	}
	if f.WatchList != nil && rl.Version <= f.WatchList.Version {
		return false
	}
	next := &List{
		Version:          rl.Version,
		WatchNewProjects: rl.WatchNewProjects,
		Projects:         make(map[string]ProjectEntry, len(rl.Projects)),
	}
	var prev map[string]ProjectEntry
	if f.WatchList != nil {
		prev = f.WatchList.Projects
	}
	for hash, entry := range rl.Projects {
		rec := ProjectEntry{Watched: entry.Watched}
		if old, ok := prev[hash]; ok {
			rec.DisplayName = old.DisplayName
			rec.LastProjectSeenAt = old.LastProjectSeenAt
		}
		next.Projects[hash] = rec
	}
	f.WatchList = next
	return true
}

// Decide applies BR-10 to one classified event for the project at
// keyHash: an unknown project defaults to WatchNewProjects; a watched
// project is always spooled; an unwatched project spools at most one
// project_seen every ProjectSeenThrottle.
//
// It mutates f on any decision but Drop (recording displayName and
// bumping LastProjectSeenAt to now), so the caller only needs to save f
// when the decision is not DecisionDrop — the Drop path costs one read
// of the already-loaded file and nothing else (BR-02's budget).
func (f *File) Decide(keyHash, displayName string, now time.Time) Decision {
	if f.WatchList == nil {
		f.WatchList = &List{WatchNewProjects: true}
	}
	wl := f.WatchList
	if wl.Projects == nil {
		wl.Projects = map[string]ProjectEntry{}
	}
	rec, known := wl.Projects[keyHash]
	watched := wl.WatchNewProjects
	if known {
		watched = rec.Watched
	}

	var decision Decision
	switch {
	case watched:
		decision = DecisionSpool
	case known && recentlySeen(rec.LastProjectSeenAt, now):
		decision = DecisionDrop
	default:
		decision = DecisionEmitProjectSeen
	}

	if decision == DecisionDrop {
		return decision
	}
	rec.DisplayName = displayName
	if !known {
		rec.Watched = wl.WatchNewProjects
	}
	rec.LastProjectSeenAt = now.UTC().Format(time.RFC3339)
	wl.Projects[keyHash] = rec
	return decision
}

func recentlySeen(raw string, now time.Time) bool {
	if raw == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false
	}
	return now.Sub(t) < ProjectSeenThrottle
}
