# agentpulse

`agentpulse` is the AgentPulse bridge: a single, statically linked Go binary
that runs on your own machine, hooks into Claude Code, and sends a small
structured event to the AgentPulse relay whenever a session starts, runs a
tool, needs your input, finishes a test run, or stops — so the AgentPulse
iPhone app can show what your agents are doing without you watching a
terminal. It is the only part of AgentPulse that runs on your computer, and
it is open source so that the claim below — what it sends and what it never
sends — is something you can check for yourself rather than take on trust.

AgentPulse is designed and directed by Alex Kemper; the code is implemented
by AI engineering agents working under adversarial review, and this
repository is the audited result of that process.

## Install

**Not published yet.** The three paths below go live with the first tagged
release; until then, build from source (see [Build from source](#build-from-source)).

Homebrew:

```
brew install agentpulsesoftware/tap/agentpulse
```

PyPI (a wheel that carries the prebuilt binary; handy if `pip` is the
package manager you already have):

```
pip install agentpulse
```

Install script — download it, check its SHA-256 against the value in the
release notes, then run it. It is never a `curl | sh` one-liner, and it
verifies the checksum of the archive it downloads before extracting
anything:

```
curl -fsSL https://github.com/agentpulsesoftware/agentpulse-bridge/releases/latest/download/install.sh -o install.sh
sha256sum install.sh   # or: shasum -a 256 install.sh — compare with the release notes
sh install.sh
```

Then run `agentpulse pair` to connect the machine to the AgentPulse app, and
`agentpulse doctor` if anything looks wrong.

## What the bridge sends, and what it does not

Every event the bridge sends is one object from a closed set. "Closed" means
the set of fields is fixed and additive-only: a field can be added in a
future schema version through a reviewed design change, and no field may
ever be added that could carry free text from your machine.

Each event carries: a schema version; a random event id (a ULID, used so a
retry cannot double-count); the id of this bridge; the session id Claude
Code itself generates; the project, as a SHA-256 hash of the project key
plus its short directory name; a timestamp; the event type; and four running
counters (files read, files edited, verification runs, commits).

The payload depends on the type and is just as narrow:

| Event | Payload |
|---|---|
| `session_start` | agent name, bridge version, `darwin` or `linux` |
| `prompt_submitted` | an optional task label of at most 80 characters, only if you turn that on |
| `activity` | a category: `read`, `edit`, or `other` |
| `verification_started` | `test` or `build`, and the runner's identifier (`pytest`, `go test`, …) |
| `verification_finished` | the same, plus `pass`/`fail`/`unknown` and pass/fail/total counts |
| `needs_input` | why: `permission`, `question`, or `plan`, plus a coarse tool category |
| `input_resolved`, `commit`, `stop`, `project_seen` | nothing |
| `pr_created` | the pull request number, if one was printed |
| `session_end` | a reason: `clear`, `logout`, `prompt_input_exit`, `other` |

That is the whole vocabulary. There is no field for a file path, a command
line, a tool's output, your prompt, your source code, your environment
variables, your branch names, or your commit messages — not scrubbed
versions of them, not truncated versions of them, not hashed versions of
them beyond the project key hash above. The directory name of a project is
the one human-readable string that leaves the machine, and only for projects
you are watching.

Three mechanisms keep it that way, and all three are in this repository:

1. **The classifier's fixed field set** — `internal/classify`. The Go types
   in `internal/classify/event.go` are the only things that can be
   serialised and sent; there is no map, no "extra" field, and no passthrough
   of the hook JSON. `internal/classify/br19_test.go` reflects over every
   event and payload type and fails if a field named `cwd`, `prompt`,
   `tool_input`, `tool_response`, or `transcript_path` ever appears.
2. **The relay's own validation** — every event is validated against the
   published `event.v1` JSON Schema, which sets `additionalProperties: false`
   on every variant, so an unknown field is rejected rather than stored.
3. **The scrubber and its self-check** — `internal/scrub`. This is what the
   fixture tooling (`agentpulse scrub <in-dir> <out-dir>`) uses to turn a raw
   recorded Claude Code session into a committable fixture; the corpus
   shipped today is synthetic (see `testdata/fixtures/README.md`). It is
   allow-list based: a field name it does not recognise is scrubbed, not
   kept. After scrubbing it re-reads its own output and fails the whole run
   if it finds `/Users/`, `/home/`, `~/`, a Windows path, a `file://` URL,
   the recorded working directory, or any line longer than 200 characters.
   The check is in `internal/scrub/selfcheck.go`; it either passes or the
   command exits non-zero.

The relay stores events for a bounded window so the app can read them; what
it does with them beyond that is documented with the relay, not here.

## The privacy model

- **Credentials live in your OS credential store.** macOS Keychain, Linux
  Secret Service (`secret-tool`), and, only when neither is available, a file
  under your config directory with mode `0600`. If that file's permissions
  have drifted looser, the bridge tightens them back to `0600` on read and
  proceeds — a fixable local slip is corrected in place rather than turned
  into a hard failure. See `internal/cred`. Nothing is ever written to the
  repository, to a log, or to the event stream.
- **All traffic is outbound.** The bridge opens HTTPS connections to the
  relay; it never listens on a port, and the relay has no way to contact your
  machine. There is nothing to dial into.
- **Logs never contain response bodies.** Relay errors are carried as status
  codes and structured fields, not as text pasted from a response.
- **`agentpulse unpair` undoes everything**: it removes only the hook entries
  this bridge added to `~/.claude/settings.json`, revokes the bridge at the
  relay, and deletes the local credential and state.

## The event schema

The field list above is the `event.v1` AgentPulse event schema. The
machine-readable JSON Schema is published with the AgentPulse relay contract
and is deliberately *not* copied into this repository — a second copy is a
second source of truth, and it would drift. Changes to `event.v1` are
additive-only and require an approved design record before any code changes.

## Build from source

Go's version is pinned in `go.mod`; nothing else is needed.

```
make build   # bin/agentpulse, built the same way a release binary is
make test    # the suite twice: as released, and with -tags dev
make lint    # gofmt, go vet, golangci-lint, each twice (plain and -tags dev)
make fixtures  # replays the synthetic scenario corpus through the built binary
```

A release build additionally sets `CGO_ENABLED=0`, passes `-trimpath` and
`-ldflags "-s -w"`, and stamps in a version string, so `make build`'s binary
is not byte-for-byte identical to one from a tagged release. What stays true
either way: no dev tag, and no `record` command.

The `dev` build tag adds one command, `record`, which writes raw, unscrubbed
Claude Code hook JSON to disk so that new test fixtures can be captured.
Release binaries are built without the tag and therefore do not contain it —
`TestReleaseBuildHasNoRecordCommand` is the test that proves it. Recorded
material is only ever committed after `agentpulse scrub` has rewritten it and
its self-check has passed.

`.goreleaser.yaml` describes how a tagged release is built, optionally signed
and notarised, archived, checksummed, and published.

## Compatibility

`COMPATIBILITY.md` tracks which Claude Code hook events have been confirmed
against which versions (every row starts as TODO until validated against a
real recording); `agentpulse doctor` warns when the installed Claude Code
is outside the confirmed range.

## Contributing and security

See `CONTRIBUTING.md`, and `SECURITY.md` for how to report a vulnerability
privately.

## Licence

Apache License 2.0 — see `LICENSE` and `NOTICE`.
