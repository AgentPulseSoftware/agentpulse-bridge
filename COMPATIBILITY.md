# Claude Code compatibility

AgentPulse supports the Claude Code version listed below and later.
`agentpulse doctor` checks the installed version against the table and
warns if it looks older than what has been confirmed to work (NFR-11).

## Status

- Claude Code version: `2.1.261` (from `claude --version`)
- Date: 2026-09-13
- OS: darwin (macOS)

This is the version the bridge's hook field names and event names were
written against (SPEC 7.2). It has **not** yet been confirmed against real
recordings. Confirming a row means running `agentpulse record install`
with a development build and checking the row off below once that hook
event actually shows up in a recording with the fields SPEC 7.2 expects.

## The eight registered hook events (BR-08)

| Event | Confirmed in recordings | Released in | Notes |
|---|---|---|---|
| `SessionStart` | TODO | unreleased | |
| `UserPromptSubmit` | TODO | unreleased | |
| `PreToolUse` | TODO | unreleased | |
| `PostToolUse` | TODO | unreleased | |
| `PermissionRequest` | TODO | unreleased | Not present in every Claude Code version; if the installed version doesn't recognize it, degrade per SPEC 7.4 (permission prompts detected only via `Notification`). |
| `Notification` | TODO | unreleased | |
| `Stop` | TODO | unreleased | |
| `SessionEnd` | TODO | unreleased | |

"Released in" is the earliest tagged bridge version (`vX.Y.Z`) whose
release notes confirmed that row against a real recording. It stays
"unreleased" until the first tag; `.github/workflows/release.yml`'s
PR (the one that bumps the tag) is the same PR that should change this
column, so a release and its compatibility claim ship together.

## How to update this file

When scenarios are captured from real sessions, update each row's
confirmation column to the Claude Code version you recorded with
(e.g. `2.1.261`) once you've seen at least one recorded file for that
event, or leave a short note if an event never appeared (e.g. "not
emitted by this Claude Code version"). Update "Status"
at the top if you're using a newer Claude Code than what's listed.

## The machine-readable copy

`internal/doctor/compat.json` is the copy `agentpulse doctor`'s
check 3 (BR-15, SPEC 7.4, NFR-11) actually reads at run time — `go:embed`
cannot reach this file from `internal/doctor`'s own package directory, so
the table is duplicated there rather than embedded from here. It lists
the same eight events, in the same order, each with the earliest Claude
Code version known to send it (`min_version`), plus the `baseline_version`
this file's "Status" section names.

Whoever changes the set of registered hook events, or moves the "Status"
version forward once a newer baseline is confirmed, must update this
file, `compat.json`, and `internal/claudehooks.BR08Events` in the same
change. `internal/doctor/compat_test.go` fails the build if any of the
three drifts from the other two — it is the one place all three are
compared, so this note is the only reminder, not the only enforcement.
