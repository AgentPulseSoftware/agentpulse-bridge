// Package config reads and writes the bridge's local configuration file
// (BR-05: $XDG_CONFIG_HOME/agentpulse/config.json, mode 0600), holding
// the fields "agentpulse hook" reads on every invocation (BridgeID,
// TaskLabel) plus the pairing-related field "agentpulse flush" needs
// (PairedAt). This is the full record BR-05/BR-06/BR-16/BR-17 describe,
// with atomic writes, unknown-field preservation, and no BridgeSecret
// field at all: the secret lives only in internal/cred's platform
// credential store (SEC-02, SEC-14 — "never keep the secret in the
// config file when a platform store is available"), never in
// config.json.
package config

import (
	"encoding/json"
	"os"

	"github.com/agentpulsesoftware/agentpulse-bridge/internal/atomicfile"
)

// UnpairedBridgeID is the placeholder bridge_id every event carries until
// pairing (BR-07) writes a real one.
const UnpairedBridgeID = "brg_unpaired"

// Config is the bridge's full local configuration record (BR-05).
type Config struct {
	// BridgeID is the paired bridge's id, or UnpairedBridgeID before
	// pairing.
	BridgeID string `json:"bridge_id,omitempty"`
	// PairedAt is when pairing completed, RFC 3339.
	// "agentpulse flush" reads it back for ERR-04's 60-second
	// post-pairing grace period, where a 401 is treated as retryable
	// (ERR-06) rather than counted toward the 3-strike unpair rule,
	// because the relay can briefly lag in recognising a new bridge right
	// after pairing. Empty (unpaired, or a config predating this
	// field) means the grace period never applies.
	PairedAt string `json:"paired_at,omitempty"`
	// DeviceName is the paired phone's display name, from the pairing
	// response, shown by "agentpulse status".
	DeviceName string `json:"device_name,omitempty"`
	// Relay is a persisted "--relay"/AGENTPULSE_RELAY override (SPEC
	// 11.5), empty in a normal install where the compiled-in production
	// host is used.
	Relay string `json:"relay,omitempty"`
	// TaskLabel is BR-17's opt-in: whether the bridge sends the first
	// line of each prompt (truncated, whitespace-collapsed) as a task
	// label. Relay-driven: written back locally whenever a batch
	// response's Settings names it.
	TaskLabel bool `json:"task_label,omitempty"`
	// KeepAwake is BR-16's opt-in: whether "agentpulse hook" spawns
	// caffeinate/systemd-inhibit on session_start. Default false.
	KeepAwake bool `json:"keep_awake,omitempty"`

	// extra holds any field this binary doesn't recognize, so Save never
	// drops one an older binary reading a newer bridge's config.json
	// doesn't know about yet.
	extra map[string]json.RawMessage
}

// knownConfigKeys are Config's own JSON keys: Load strips these out of
// the raw document before keeping the remainder as extra, and Save's
// known-field values always take precedence over anything left in extra.
//
// "watch_new_projects" is a key this package must tolerate and drop:
// BR-10's watch-new-projects setting lives in
// internal/watch.List.WatchNewProjects (watchlist.json), not on Config.
// It stays in this list, not on Config, so an existing config.json
// that still carries it has the key stripped on the next Save instead of
// being preserved forever in extra.
var knownConfigKeys = []string{
	"bridge_id", "paired_at", "device_name", "relay",
	"task_label", "keep_awake", "watch_new_projects",
}

// Default returns the configuration an unpaired bridge has: the
// placeholder bridge_id, no secret store lookup attempted, task labels
// off, keep-awake off.
func Default() Config {
	return Config{BridgeID: UnpairedBridgeID}
}

// Load reads config.json at path. A missing file yields Default(), not an
// error: an unpaired bridge has no config file yet, and that is the
// normal state before "agentpulse pair" runs. A file that exists but
// fails to parse also yields Default() (with the parse error returned for
// optional debug logging) rather than blocking the hook — BR-02 requires
// "agentpulse hook" to never fail because of local configuration trouble.
// A file that parses but omits bridge_id (or sets it to "") still gets
// the placeholder substituted, so every caller of Load can trust
// BridgeID is always non-empty.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path) //nolint:gosec // operator's own config path, not attacker-controlled
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return Default(), err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), err
	}
	if cfg.BridgeID == "" {
		cfg.BridgeID = UnpairedBridgeID
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err == nil {
		for _, k := range knownConfigKeys {
			delete(raw, k)
		}
		cfg.extra = raw
	}
	return cfg, nil
}

// Save writes config.json at path atomically (ERR-02: a failure is
// returned as an *atomicfile.WriteError naming path), mode 0600 with a
// 0700 parent. Any fields Load found in the existing file that this
// binary doesn't recognize (c.extra) are written back unchanged;
// Config's own fields always take precedence over anything with the same
// key in extra.
func Save(path string, c Config) error {
	out := make(map[string]json.RawMessage, len(c.extra)+len(knownConfigKeys))
	for k, v := range c.extra {
		out[k] = v
	}

	known, err := json.Marshal(c)
	if err != nil {
		return err
	}
	var knownMap map[string]json.RawMessage
	if err := json.Unmarshal(known, &knownMap); err != nil {
		return err
	}
	for k, v := range knownMap {
		out[k] = v
	}

	return atomicfile.WriteJSON(path, out, 0o600)
}
