package relay

// WatchList mirrors SPEC 10.1's WatchList response object: the bridge's
// local watched-project map, as the relay wants it to be after this
// batch. BR-11's mechanical half is atomically storing this; acting on
// it (gating events, "agentpulse projects") is separate.
type WatchList struct {
	Version          int                     `json:"version"`
	WatchNewProjects bool                    `json:"watch_new_projects"`
	Projects         map[string]ProjectEntry `json:"projects"`
}

// ProjectEntry is one project's entry in a WatchList.
type ProjectEntry struct {
	Watched bool `json:"watched"`
}

// Settings mirrors SPEC 10.1's BridgeSettings response object.
type Settings struct {
	TaskLabel bool `json:"task_label"`
}

// DroppedEvent is one event PostEvents gave up on delivering permanently
// rather than leaving spooled for a future attempt: either the relay's 400
// named it as invalid, or it alone exceeded MaxChunkBytes and so could
// never be sent (impossible under the closed, length-capped schema —
// handled defensively, see chunk.go). Index is into the events slice
// PostEvents was called with; Type is read from the event's own "type"
// field on a best-effort basis, for logging only.
type DroppedEvent struct {
	Index int
	Type  string
}

// Response is PostEvents' result. Sent is always populated, even when
// PostEvents also returns a non-nil error: events[:Sent] is the leading,
// contiguous run of input events the relay has answered for with a 2xx and
// so is safe to drop from the spool. Dropped lists any further events
// (not necessarily contiguous with Sent) permanently given up on for the
// reasons above; those are also safe to drop from the spool. Everything
// else in the input was never sent, or was rejected by an error the
// caller must retry or leave spooled per SPEC section 14 — see the
// returned error's Kind.
type Response struct {
	Sent       int
	Accepted   int
	Duplicates int
	WatchList  *WatchList
	Settings   *Settings
	Dropped    []DroppedEvent
}

// okBody is the 2xx response body (SPEC 10.1's batch response). Unknown
// fields are ignored by plain json.Unmarshal, never rejected (BR-18): a
// relay ahead of this bridge's schema knowledge must not break delivery.
type okBody struct {
	Accepted   int        `json:"accepted"`
	Duplicates int        `json:"duplicates"`
	WatchList  *WatchList `json:"watch_list"`
	Settings   *Settings  `json:"settings"`
}

// errorBody is a non-2xx response body. detail is intentionally never
// exposed outside this package (StatusError.Error() does not include it,
// and no caller may log a relay response body — CLAUDE.md, SPEC
// section 14's rules for "flush"): it is read only to extract an event
// index (see chunk.go's eventIndexPattern) and, when present, a
// required-bridge-version string, both structured facts the bridge is
// explicitly allowed to record (ERR-03).
type errorBody struct {
	Error                 string `json:"error"`
	Detail                string `json:"detail"`
	RequiredBridgeVersion string `json:"required_bridge_version"`
}
