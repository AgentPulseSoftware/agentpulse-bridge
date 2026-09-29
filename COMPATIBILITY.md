# Claude Code compatibility

AgentPulse supports the Claude Code version listed below and later.
`agentpulse doctor` checks the installed version against the table and
warns if it looks older than what has been confirmed to work (NFR-11).

## Status

- Claude Code version: `2.1.283` (from `claude --version`)
- Date: 2026-09-27
- OS: darwin (macOS)

This is the version the subagent hooks were recorded with (see
"Subagent hooks" below); the other eight events' field names and event
names were first written against `2.1.261` (SPEC 7.2). It has **not** yet been confirmed against real
recordings. Confirming a row means running `agentpulse record install`
with a development build and checking the row off below once that hook
event actually shows up in a recording with the fields SPEC 7.2 expects.

## The eleven registered hook events (BR-08)

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
| `StopFailure` | TODO | unreleased | Runs instead of `Stop` when a turn ends on an API error, such as a usage limit (ADR-006 section 6); see "Usage-limit pauses" below. A machine paired before this row existed adds it with `agentpulse pair --hooks-only`. |
| `SubagentStart` | `2.1.283` | unreleased | Added for subagent chains (ADR-005 section 3); see "Subagent hooks" below. A machine paired before this row existed adds it with `agentpulse pair --hooks-only`. |
| `SubagentStop` | `2.1.283` | unreleased | As `SubagentStart`. |

"Released in" is the earliest tagged bridge version (`vX.Y.Z`) whose
release notes confirmed that row against a real recording. It stays
"unreleased" until the first tag; `.github/workflows/release.yml`'s
PR (the one that bumps the tag) is the same PR that should change this
column, so a release and its compatibility claim ship together.

## Subagent hooks

Recorded 2026-09-27 against Claude Code `2.1.283`, from one scripted
session (card P5-14, ADR-005 "Open question, settled by a recording").
The session ran non-interactively (`claude -p` with a pre-approved tool
list, so it could complete without a human present to approve a
permission prompt), so no `PermissionRequest` hook fired in this
recording. That leaves whether `PermissionRequest` or `Notification`
carry `agent_id`/`agent_type` unconfirmed by an actual recording; the
`subagent-permission` fixture below stays entirely synthetic rather than
recorded-shape-confirmed for those two hook names.

Findings (values are never recorded, only field names and yes/no facts):

- (a) Every hook fired for, or inside, a subagent carried the **parent**
  session's `session_id` — with no exception across 19 recorded hook
  calls, two subagents, and both hooks named `SubagentStart`. Subagents
  do not get their own `session_id`.
- (b) `agent_id` and `agent_type` appeared on `SubagentStart`,
  `SubagentStop`, and on `PreToolUse`/`PostToolUse` when that hook fired
  for the subagent's own tool call. They did **not** appear on the main
  session's own `PreToolUse`/`PostToolUse` around the tool call that
  launches a subagent (recorded `tool_name` for that launch: `Agent`).
- (c) The `agent_id` on a subagent's `SubagentStart`, on every
  `PreToolUse`/`PostToolUse` pair fired for that subagent's own tool
  calls, and on that subagent's `SubagentStop`, was identical throughout
  its lifecycle — confirmed for both subagents recorded.
- (d) Key names seen:
  - `SubagentStart`: `agent_id`, `agent_type`, `cwd`, `hook_event_name`,
    `prompt_id`, `scratchpad_dir`, `session_id`, `transcript_path`.
  - `SubagentStop`: `agent_id`, `agent_transcript_path`, `agent_type`,
    `background_tasks`, `cwd`, `effort`, `hook_event_name`,
    `last_assistant_message`, `permission_mode`, `prompt_id`,
    `scratchpad_dir`, `session_crons`, `session_id`, `stop_hook_active`,
    `transcript_path` (the same shape as `Stop`, plus the three
    subagent-identity fields).
- (e) `SessionStart`, `UserPromptSubmit`, and `Stop` never carried
  `agent_id` in this recording (checked every instance: one
  `SessionStart`, three `UserPromptSubmit`, two `Stop`).
- (f) The two subagents' own tool-call hooks did **not** interleave at
  the tool level: one subagent's `PreToolUse`/`PostToolUse` pair for its
  own tool call completed in full before the other's began, even though
  both subagents were launched together and both `SubagentStart` hooks
  arrived consecutively, before either subagent's own tool hook fired.
  Their two `SubagentStop` hooks likewise arrived as a pair, in the
  reverse of start order.

**Consequence for ADR-005:** finding (a) settles the ADR's open question
in favor of its default-assumption branch — the bridge does **not** need
a parent-mapping step for `SubagentStart`/`SubagentStop` (ADR-005
section 3's "Exception" branch does not apply); P5-18 can proceed on
that basis.

Both are registered (BR-08, card P5-21) with `2.1.283`, the version
recorded here, as their minimum in the table above and in
`internal/doctor/compat.json`: the oldest version known to send them,
not a guess at when they were introduced. On an older Claude Code,
`agentpulse doctor` warns that subagents will show without a precise
start and stop.

## Usage-limit pauses

`StopFailure` and three `Notification` types become the `paused` event,
carrying one fixed cause code (ADR-006 sections 1 and 2). No recording
of either has been made yet: a real usage limit cannot be triggered on
demand, so the `stopfailure-*` fixture scenarios are synthetic, written
from Anthropic's hooks reference. The `StopFailure` minimum version
above is the one the subagent hooks were recorded with, the
conservative choice until a recording of `StopFailure` itself exists.

The bridge reads exactly two input fields on this path:

- `error` on `StopFailure`, compared by exact value against a fixed
  table. Any value not in the table, including an empty one, becomes the
  cause `api_error`, so a new Claude Code value still shows as paused.
- `notification_type` on `Notification`, for
  `quota_auto_resume_stale` (the limit reset while the computer slept;
  sent as cause `limit_reset`), `quota_auto_resume_fired` and
  `quota_auto_resume_disabled` (both send nothing). The permission and
  idle notifications are still told apart by their message text.

It never reads, sends or logs these fields, which are message text:
`error_details` and `last_assistant_message` on `StopFailure`, and
`title` and `message` on the usage-limit notifications. A `StopFailure`
or usage-limit notification carrying `agent_id` (inside a subagent)
sends nothing.

Still to confirm with a real usage limit: which `error` value a
claude.ai subscription limit arrives as (`rate_limit` is assumed), and
whether `quota_auto_resume_fired` comes before the continuation's
`UserPromptSubmit`.

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
the same eleven events, in the same order, each with the earliest Claude
Code version known to send it (`min_version`), plus the `baseline_version`
this file's "Status" section names.

Whoever changes the set of registered hook events, or moves the "Status"
version forward once a newer baseline is confirmed, must update this
file, `compat.json`, and `internal/claudehooks.BR08Events` in the same
change. `internal/doctor/compat_test.go` fails the build if any of the
three drifts from the other two — it is the one place all three are
compared, so this note is the only reminder, not the only enforcement.
