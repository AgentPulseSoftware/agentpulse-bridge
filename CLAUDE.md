# agentpulse-bridge

This repository is the AgentPulse bridge: a single statically linked Go
binary named `agentpulse`, built with Cobra, that runs on a developer's
machine, watches Claude Code hooks, and relays session events to the
AgentPulse relay. This file is the standing brief for any coding agent
working in this repository.

## Layout and conventions

Standard Go project layout. `cmd/agentpulse/main.go` wires up the CLI, and
package code that grows beyond `main` belongs under `internal/` — never
under `pkg/`, since nothing here is imported by other modules. Write
idiomatic Go: small interfaces, errors returned and checked rather than
panicked, table-driven tests, `gofmt`-clean, and no new dependency that the
change at hand does not genuinely need. `gofmt`, `go vet`, and
`golangci-lint run` (config in `.golangci.yml`) must all pass before a
change is done, plain and with `-tags dev`; `make lint test` runs the full
check. CI is Linux-only, so run `GOOS=linux go build ./...` and
`GOOS=linux go vet ./...` locally when developing on macOS.

## The hook must stay silent

The `hook` subcommand is called by Claude Code on every tool call. It must
exit `0` on every path and must never print to stdout or stderr unless
`AGENTPULSE_DEBUG=1` is set. A hook that prints, or that exits non-zero,
interrupts the developer's agent — that is the single worst failure this
program can have, and it outranks reporting any error.

## The event schema is closed

The event schema the bridge sends to the relay (`event.v1`) is closed:
never add a field that could carry a file path, command text, tool output,
or prompt text, and never widen an existing field into free text. The
relay validates every event against the published schema with
`additionalProperties: false`, so an unreviewed field is rejected rather
than stored. Any change to the schema needs an approved ADR first; a pull
request that adds a field without one will be declined. `internal/scrub`'s
self-check and `internal/classify/br19_test.go` exist to keep this
provable, not decorative — do not weaken either to make a test pass.

## Keychain rule (macOS)

Never create, unlock, switch, delete, or list a macOS keychain, and never
run a command whose purpose is to discover what `security(1)` does to one.
Never run anything expected to raise a Keychain dialog unattended: an
unattended agent cannot answer a password prompt, and a dialog is to be
diagnosed, never worked around. `security -A` ("any application may read
this without warning") and `-T` (naming a trusted application) are
forbidden everywhere in this repository, in production code and in tests;
the `agentpulse` Keychain item keeps macOS's default access control. The
one sanctioned exception to this whole rule is `release.yml`'s CI signing
keychain: a temporary, ephemeral keychain created and destroyed within a
single release run, on a machine no human is watching, solely to hold the
code-signing certificate for that run.

The only sanctioned real-Keychain runs are `TestCredE2ERealBackend` in
`internal/cred` and the `AGENTPULSE_CRED_E2E=1` mode of
`TestPairAgainstALocalRelay` in `cmd/agentpulse`, both against the default
keychain, both only when someone is present and has asked for them. That
mode overwrites and then deletes the real `agentpulse` Keychain item, so a
machine that is genuinely paired must run `agentpulse pair` again
afterwards. Always run it with `-run TestPairAgainstALocalRelay`, because
`cred.Default()` caches the selected store per process and a wider run
would give later tests the real Keychain under a fabricated `HOME`.

## Never

- Never run `agentpulse pair` or `agentpulse unpair` against a real home
  directory as part of a change; the tests use fabricated `HOME`s.
- Never fetch, read, or copy any third party's proprietary source code or
  documentation. Design from the official documentation of Go, Anthropic,
  and the platforms involved.
- Never commit a credential, a token, or a `.p8`/`.p12` file.
