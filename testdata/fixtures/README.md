# Bridge fixture corpus

This folder is the validation basis for the whole classifier (SPEC 9.4),
replayed through the bridge in CI to prove the classifier gets the right
answer.

Every scenario here today is synthetic: hand-authored hook documents,
written to match the exact shape of real Claude Code hook input (field
names confirmed against real hooks), not captured from an actual session.
Each scenario's `expected.json` sets `"synthetic": true` so this is
checkable rather than asserted. A future corpus built from real recorded
sessions — captured with `agentpulse record install` (a development-only
build) and passed through `agentpulse scrub`, which fails rather than
writes if its own self-check finds a leaked path or an over-long line —
will replace or supplement these, and will not set `synthetic`.

## Layout

```
testdata/fixtures/
  <scenario>/
    001-SessionStart.json
    002-UserPromptSubmit.json
    003-PreToolUse.json
    ...
    expected.json
```

- `<scenario>/` is one directory per scenario (a short session's worth
  of hook events with an expected final state; see "Scenarios"
  below), named for what it exercises, e.g. `edit-and-stop`,
  `pytest-fail-then-fix`, `permission-prompt`.
- `NNN-<hook>.json` are the hook documents, hand-authored in the
  scrubbed shape, in event order. `NNN` is a zero-padded counter;
  `<hook>` is the Claude Code hook event name (`SessionStart`,
  `PreToolUse`, `PostToolUse`, and so on — see SPEC 7.2). Today, every
  scenario here is a hand-authored synthetic document matching the real
  hook-input shape; when a scenario is someday captured from a real
  session, it will be exactly what `agentpulse scrub` wrote,
  hand-edited by no one. Each synthetic scenario's `expected.json`
  sets `"synthetic": true` so the two kinds can be told apart and
  everything re-run once real recordings land (SPEC 9.4).
- `expected.json` is the hand-authored answer key for the scenario. See
  the format below.

## The `expected.json` format

```json
{
  "events": [
    {
      "type": "activity",
      "payload": { "category": "edit" }
    },
    {
      "type": "verification_started",
      "payload": { "kind": "test", "runner": "pytest" }
    },
    {
      "type": "verification_finished",
      "payload": { "kind": "test", "runner": "pytest", "outcome": "fail", "passed": 11, "failed": 1 }
    }
  ],
  "final_state": "fixing",
  "recap_ok": true
}
```

- `events` is the ordered list of normalized events (SPEC 10.1's `type` and
  `payload` shapes) the classifier is expected to emit for this scenario,
  in order. Only the payload fields that matter for the scenario need to be
  listed; a reviewer comparing actual output should treat any field left out
  of `expected.json` as "don't care" rather than "must be absent" unless the
  scenario is specifically about that field's absence.
- `final_state` is the SPEC 7.3 state the session should be in after every
  event in the scenario has been applied (e.g. `working`, `needs_you`,
  `done`).
- `recap_ok` is `true` once a human (SPEC 18) has read the Since
  You Left recap SPEC 9.3 would generate from this scenario's events and
  confirmed it reads sensibly. It starts `false` (or absent) when a scenario
  is first added.
- `subagent_events` (optional) is the answer key for the bridge's normal
  mode, where hooks fired inside a subagent are classified as their own
  chain (ADR-005). `make fixtures` replays every scenario twice: once in
  that normal mode, compared to `subagent_events` when present and to
  `events` otherwise, and once with subagent classification paused the
  way the flush fallback pauses it, compared to `events`. Unlike payload
  fields, an event's `subagent` object is checked strictly: an expected
  event without one requires the actual event to have none, and in the
  paused replay no event may carry one. `final_state` is checked in both
  replays; in the normal one it is computed from the events without
  `subagent` only, because the session's own state follows its main
  chain (D71).
- `synthetic` is `true` for a scenario hand-authored in the scrubbed shape
  rather than recorded from a real Claude Code session and scrubbed;
  absent (or `false`) for one recorded from a real session. It exists so
  the classifier can be re-run against real recordings once they exist,
  with the two kinds told apart.

## Scenarios

The full list to record (SPEC 18):

- Simple edit and stop
- Test run with failures then a fix
- Permission prompt
- `AskUserQuestion`
- Plan-mode approval
- Long read-heavy exploration
- Session killed (Ctrl-C twice, or the terminal closed)
- Two concurrent sessions in different projects
- `gh pr create`
- One session per SPEC 7.5 runner: pytest, Jest, Vitest, Mocha, an `npm`/
  `pnpm`/`yarn`/`bun` test script, `go test`, Cargo, Swift, and xcodebuild
- Subagent hooks (D69, ADR-005, cards P5-14 and P5-18): `subagent-single`,
  `subagent-permission`, `subagent-parallel`, `subagent-type-sanitising`
  — see below.

Each scenario gets its own directory. A runner-specific scenario should be
named after the runner, e.g. `verify-pytest`, `verify-go-test`, so the
classifier's table-driven tests can find them by name.

## Subagent scenarios (D69, ADR-005)

`subagent-single`, `subagent-permission`, and `subagent-parallel` are
hand-written in the shape a real Claude Code subagent recording showed
(`COMPATIBILITY.md`'s "Subagent hooks" section, card P5-14), with invented
`session_id`, `agent_id`, and `agent_type` values. `subagent-permission`
in particular is synthetic in the strict sense throughout: the real
recording never exercised a `PermissionRequest` inside a subagent (it was
a non-interactive run with pre-approved tools), so its shape is inferred
from the general `PermissionRequest` shape already recorded elsewhere in
this corpus, not confirmed for the subagent case.

Their `events` are the classifier's output from before subagents were
recognized, which is still its output while subagent classification is
paused: `agent_id`/`agent_type` are ignored and the
`SubagentStart`/`SubagentStop` hook names produce nothing, so a
subagent's activity folds into its parent session exactly as if the tool
calls had happened at the top level. Their `subagent_events` are the
normal output (card P5-18): the same events, with each subagent's marked
by its `subagent` object, plus `subagent_start` and `subagent_stop`.

`subagent-type-sanitising` starts subagents whose `agent_type` is hostile
(a path, a space, 41 characters, a quote, empty, non-ASCII) or merely
padded with spaces, and checks that each is sent cleaned or as the
literal `subagent` (ADR-005 section 1).
