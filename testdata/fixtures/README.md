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

Each scenario gets its own directory. A runner-specific scenario should be
named after the runner, e.g. `verify-pytest`, `verify-go-test`, so the
classifier's table-driven tests can find them by name.
